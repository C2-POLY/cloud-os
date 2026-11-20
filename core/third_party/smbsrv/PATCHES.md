# smbsrv in Cloud OS: what differs from upstream

This is a copy of `github.com/tobychui/smbsrv` (MIT, `LICENSE`), linked into the core by `replace github.com/tobychui/smbsrv => ./third_party/smbsrv` in `core/go.mod`. The changes below are ours: they are reviewed in our pull requests and published nowhere else. Why this library, and why these changes: `docs/decisions/006-smb.md`. How the core uses it: `docs/architecture/smb.md`.

- **Upstream commit:** `7a70fc2e0cbb` (pseudo-version `v0.0.0-20261109114502-7a70fc2e0cbb`, committed 2026-11-09).
- **Left out:** `cmd/` (the standalone server) and `wsd/` (WS-Discovery and LLMNR, which announce the server on the network). Without them, `go.mod` no longer needs `golang.org/x/net` nor `golang.org/x/term`.

## Moving to a new upstream version

1. Copy the new version over this folder, without `cmd/` and `wsd/`, and keep this file.
2. Apply each change below again. Its test says whether it is still needed: run the `patch_*_test.go` files and `internal/rpc/patch_test.go` against the new code before patching it.
3. Run the whole suite with `go test -race ./...` here, then `make test`.

Each test below fails on the upstream code. The failure seen at `7a70fc2e0cbb` is given for each one.

## 1. The private range cannot spell `.`, `..` or a separator

- **Files:** `names.go` (`parsePathStream`, `hasDotSegment`).
- **Why:** macOS sends the characters Windows forbids in names as private-use code points: U+F029 is a period and U+F026 a backslash. Upstream refused `.` and `..` first, and only then mapped those code points back. `\x` reached the Backend as `../x`, and so did a rename target and a search pattern. A stream named `` became the stream `..`.
- **Change:**
  - Each name is mapped from the private range first, then refused if it is `.` or `..`.
  - A mapped name that holds a backslash is refused if `.` or `..` is one of its backslash-separated segments, since a Windows disk or a path cleaner would split on it.
  - A stream name is refused if, once mapped, it is `.` or `..` or holds `/` or `\`.
  - The characters Windows forbids are still refused as sent: only their private spellings are names a share may hold.
- **Test:** `TestPatchPrivateRangeCannotTraverse` (`patch_names_test.go`). Upstream accepted `` as `..` and `\x` as `../x`.

## 2. An RPC bind cannot make a reply loop forever

- **Files:** `internal/rpc/pipe.go` (`handleBind`, `respond`).
- **Why:** a DCE/RPC bind on IPC$ with a `max_recv_frag` between 24 and 31 left no room for the stub in a fragment. `respond` then wrote empty fragments forever, and the pipe's output grew until the process ran out of memory. Any signed-in user can reach IPC$, and it cannot be turned off.
- **Change:**
  - A `max_recv_frag` below 1432 bytes, the smallest every DCE/RPC peer must take (C706, `MUST_RECV_FRAG_SIZE`), is raised to 1432.
  - `respond` never makes a fragment with less than 8 bytes of stub.
- **Test:** `TestPatchTinyRecvFragDoesNotLoop` (`internal/rpc/patch_test.go`). Upstream was still splitting the reply when the test gave up, at `max_recv_frag` 24.

## 3. Search patterns match in linear time

- **Files:** `names.go` (`matchRunes`, `matcher`).
- **Why:** the `*` and `<` wildcards tried every split of the name recursively. A QUERY_DIRECTORY pattern such as `************#` pinned a core for seconds, and a few more stars for longer than anyone would wait. The search held the open's lock throughout, and did not stop when the client disconnected.
- **Change:**
  - The pattern runs as a set of positions over the name, one character at a time. That costs O(pattern × name) whatever the pattern. The rules are FsRtlIsNameInExpression's, as upstream implemented them, including the DOS forms `<`, `>` and `"`.
  - A pattern longer than a name (255 characters) is refused with STATUS_OBJECT_NAME_INVALID. Without that cap, a pattern of 32767 stars still cost 70 ms per entry.
  - A listing stops when its connection is gone.
- **Tests:**
  - `TestPatchWildcardIsLinear` (`patch_names_test.go`). Upstream was still matching 40 stars after 2 s.
  - `TestPatchWildcardSameAnswers` checks that 200,000 random patterns and names get the same answer as upstream's matcher, which the test keeps as its reference.
  - `TestPatchLongPatternIsRefused`: upstream listed a folder with a pattern of 300 stars.

## 4. What a client can make the server hold is bounded

- **Files:** `limits.go` (new), `conn.go`, `fileio.go`, `server.go`.
- **Why:**
  - The limit of 32 requests running per connection was given back as soon as a request went async. A slow request answers STATUS_PENDING after a second, and goes async then. 400 slow READs all ran in the backend at once.
  - Each READ allocated its full length (up to 8 MiB) before calling the backend, even on a 10-byte file.
  - Requests waiting for a change or a lock had no limit at all.
- **Change:**
  - **In flight.** A request that goes async keeps its place among the `maxInFlight` requests until its handler returns, along with the frame it came in.
  - **READ buffer.** A READ allocates what the open knows the file holds past its offset, not what was asked. At or past that end it allocates 64 KiB, to see whether the file grew. If the file grew since the open, the read goes on in steps while the budget has room.
  - **Offsets.** A READ offset that would be negative as an `int64` is refused.
  - **Memory budget.** Bytes are counted per connection, in two budgets: 32 MiB of frames being handled, and 32 MiB of READ data not yet written (`maxConnInbound`, `maxConnOutbound`).
    - A frame or a read waits for room. A charge larger than the budget runs alone.
    - A READ that follows another READ in the same compound does not wait, since the room it needs would only come back with that compound's responses. It gets STATUS_INSUFFICIENT_RESOURCES when the budget is full.
  - **One frame per compound.** The READs of one compound return at most `maxTransact` bytes together; the next ones get STATUS_INSUFFICIENT_RESOURCES. Upstream sent a compound of three 8 MiB READs as one frame of 24 MiB, whose 24-bit length wrapped: the client read garbage. A response that would not fit a frame now closes the connection instead.
    - What a connection still holds is given back when it ends.
    - There is no budget for the whole server: a client that stops reading its responses would hold it, and stall every other client, until the write deadline. Many connections together are bounded by `MaxConns`, `MaxPerIP` and the container's memory limit.
  - **Pending requests.** At most 1024 requests per connection wait for a change or a lock (`maxPendingAsync`). The next ones get STATUS_INSUFFICIENT_RESOURCES.
- **Tests** (`patch_limits_test.go`):
  - `TestPatchPendingRequestsCountInFlight`: upstream ran 129 reads in the backend at once, against a limit of 32.
  - `TestPatchReadAllocatesWhatTheFileHas`: upstream gave a 10-byte file a buffer of 8 MiB, and answered a read at offset 2^63 with STATUS_UNSUCCESSFUL. The test also checks that bytes added after the open are read.
  - `TestPatchReadDataIsCappedPerConnection`: upstream filled 96 MiB of reads at once on one connection.
  - `TestPatchCompoundOfReadsDoesNotWaitForItself`: a compound of six 8 MiB READs gets one valid frame, the reads beyond 8 MiB refused, and the connection still works. Upstream sent a frame whose length had wrapped.
  - `TestPatchPendingRequestsAreCapped`: upstream left 1064 requests pending.
  - `TestMemLimit` checks the budget itself.

## 5. A panic ends one connection, not the process

- **Files:** `guard.go` (new), `conn.go`, `server.go`, `fileio.go`, `lease.go`, `notify.go`, `durable.go`.
- **Why:** there was no `recover` anywhere. A panic in a handler, or in the Backend code it calls, ended the whole process.
- **Change:**
  - **Connections.** Every goroutine of a connection defers a guard: the reader, the writer, each request, each pending request, the lock waits and cancels. So does the code that runs a handler apart (`guardedRoute`). A panic is logged with its stack and closes that connection only. A handler that panicked still answers, with STATUS_INTERNAL_ERROR, so nothing waits on it forever.
  - **Server.** The server's own goroutines and timers (lease and notification polling, lease break timeouts, durable handle timers) log the panic and end. `Backend.Allow` panicking refuses the connection.
- **Test:** `TestPatchPanicClosesOneConnection` (`patch_limits_test.go`). A share's `Stat` panics on one name, which a request reaches through the async path. A backend's `AuthLog` panics during a login, on the connection's reading goroutine. Another connection still reads, and a new one signs in. Upstream, the test process died.

## 6. `LookupHash` gets the client's address, and only a 16-byte hash signs in

- **Files:** `backend.go`, `session.go`, `internal/auth/ntlm.go`, `localfs/backend.go`.
- **Why:**
  - Cloud OS counts login attempts per account and per address (`auth.SMBHash`), and upstream's `LookupHash(username)` did not say who was asking.
  - smbsrv keys the response's HMAC with whatever hash the Backend returns. An empty or short hash is a key a client can compute, which `localfs` refused but the protocol core did not.
- **Change:**
  - `Backend.LookupHash(username, remote string)`, where `remote` is the client's `ip:port`, the same address `AuthLog` gets.
  - The NTLM check refuses any hash that is not 16 bytes.
- **Tests** (`patch_backend_test.go`):
  - `TestPatchLookupHashGetsTheAddress`.
  - `TestPatchShortHashLogsNobodyIn`: upstream let a client in with a 3-byte or a 17-byte hash.

## 7. The server can serve a listener it is given

- **Files:** `server.go` (`Config.Listener`, `Start`).
- **Why:** behind the box's tailscale service, every SMB connection comes from the proxy. The proxy names the real client in a PROXY protocol header, which a wrapping listener reads (`core/internal/edge`, `ProxyListener`). Upstream always listened on `Address` itself, so its limits per address and `LookupHash` only ever saw the proxy.
- **Change:** `Config.Listener`, when set, is served instead of listening on `Address`. The `RemoteAddr` of its connections is the client's address for `Allow`, the per-address limit, `LookupHash` and `AuthLog`. `Close` closes it.
- **Test:** `TestPatchServesTheGivenListener` (`patch_listener_test.go`). Upstream has no `Listener`, so the test does not build.
