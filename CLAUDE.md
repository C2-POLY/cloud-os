# Cloud OS (nom de travail)

An all-in-one home cloud OS installed on a box: drive, photos, documents and media libraries on one core, with local AI search. The product spec is the PRD: <https://claude.ai/artifact/Ga3px6XWQWViJftYeD8pcR>.

## Project state

- 2026-11-17: repository created; phase 1 skeleton in place (Compose stack, health endpoint, worker heartbeat, CI). The V1 is phases 1 to 3 of the PRD roadmap (Core + Drive, Photos + mobile, AI search + Documents), run in Docker Compose. The OS packaging comes after.
- Decisions are recorded in `docs/decisions/` (one numbered file each, with the why and the options rejected). Stack: `001-stack.md`.
- Still open in the PRD: product name, open or closed source, reference hardware (Raspberry Pi 5 or x86 mini PC). Update this file and the `topic` skill when one is decided.

## Stack

Containers first: the whole application runs in Docker Compose, and the OS is only a wrapper around it. Daily development happens on a Mac with Docker Desktop; OS image tests in a QEMU/UTM VM; real-hardware tests on the box.

- Core (accounts, storage, index, API, WebDAV, sharing, task queue): Go.
- AI workers (text and image embeddings, OCR, speech, classification): Python, models run with ONNX Runtime, no PyTorch in the image. They read the task queue and write results to the database; they never modify user files.
- Task queue: a PostgreSQL table (`SELECT … FOR UPDATE SKIP LOCKED`), no Redis.
- Database: PostgreSQL with pgvector (HNSW index, one table per embedding model, model name and version stored with each vector).
- LLM: Ollama or llama.cpp, behind the AI provider interface.
- AI is local by default; an external API key is opt-in per module.
- Web front: React with Vite, in TypeScript, served as static files by the core.
- Mobile: React Native, in TypeScript, sharing API types with the web front.
- Images are built for ARM64 (Apple Silicon, Raspberry Pi) and AMD64 (x86 mini PCs).

## Commands

Run from the repository root (or a worktree). Steps 5 to 7 of the `topic` skill use the same commands.

```bash
# Install Go, Python (uv) and web (npm) dependencies
make deps

# Build and start db, core, worker and web; wait until healthy
make up
# The app (the core serving the built front): http://localhost:8480
# Vite with hot reload, /api forwarded to the core: http://localhost:5180

# Follow the logs, stop the stack (the database volume is kept)
make logs
make down

# Lints, no warning allowed: gofmt, go vet, ruff check and format, tsc, oxlint
make lint

# Every test suite. Starts the Compose database if needed; each database test
# creates and drops its own throwaway database.
make test

# Rewrite Go and Python files in place
make fmt

# Import a folder into an owner's space (the folder is mounted read-only), then
# check every stored file against the index (exit 1 on any difference)
docker compose run --rm -v "<folder>:/import:ro" core import <owner> /import
docker compose run --rm core verify

# Set a new password for an account that lost it (read from stdin) and end its sessions
printf '%s\n' '<new password>' | docker compose run --rm -T core reset-password <account>
```

Layout: `core/` (Go module, `cmd/core`, `internal/`, SQL migrations in `core/migrations/`, applied by the core at startup with goose), `worker/` (Python, uv), `web/` (React, Vite, npm), `compose.yaml`, `Makefile`, `.github/workflows/ci.yml` (`make lint` and `make test` on every PR and on `main`).

**One stack per worktree.** Give each worktree its own Compose project and, when another stack runs, its own host ports, so stacks never share a database:

```bash
COMPOSE_PROJECT_NAME=cloud-os-<name> CLOUDOS_DB_PORT=54321 CLOUDOS_CORE_PORT=8481 CLOUDOS_WEB_PORT=5181 make up
```

Defaults: database `54320`, core `8480`, Vite `5180`, all bound to `127.0.0.1`. User files are stored in `CLOUDOS_DATA_DIR` (default `./.data/files`, git-ignored), mounted at `/data` in the core. The Compose credentials (`cloudos`/`cloudos`) are for the local throwaway database only.

## Non-negotiable rules

- **No data loss, ever.** Indexing never modifies original files; integrity is checked by hash; deletes go to the trash; database migrations are versioned and reversible. A feature that can lose a file does not ship.
- **Nothing leaves the box by default.** Any call to an external API is explicit and enabled per module.
- Files are stored in clear, in a readable tree: the data stays usable without the product. Only `core/internal/storage` writes there: it writes to a temporary file, checks it, then links it into place without ever replacing a file; it never deletes or modifies a stored file. A move is journaled in `moves` before the disk is touched, holds the owner's lock throughout, never renames onto an existing path, and is finished or dropped by `RecoverMoves` at startup. Every ancestor of an index row has its own `dir` row.
- **Every API route needs a session** except the ones listed as `public` in `core/internal/httpapi` (health, setup, login, logout, and later share links); `TestEveryRouteIsProtected` fails when that list changes. State-changing requests must be JSON (cross-site request forgery). Passwords are argon2id; only the SHA-256 of a session token is stored.
- **Dependencies and models have a permissive licence** (MIT, BSD, Apache-2.0, MPL-2.0, ISC). No AGPL or GPL, no non-commercial licence, so the project can still be released as open source.

## New subjects

Every new subject (feature, improvement, bug) follows the `topic` skill (`.claude/skills/topic/SKILL.md`): analysis, GitHub issue in the Cloud OS project, worktree, development, tests, visual check, dev test, then commit, PR and project status.

## Work items (GitHub)

Work items are GitHub issues of `C2-POLY/cloud-os`, tracked in the organisation's project **Cloud OS** (<https://github.com/orgs/C2-POLY/projects/10>, number 10). `gh` must be signed in with the `project` scope (`gh auth refresh -s project`).

Issues are written in French and kept short:

- Title: `<Zone> : <ce qu'il apporte>`, for example `Drive : partager un dossier par lien avec expiration`.
- Body: `**En tant que** …, **je veux** …, **afin de** …`, then `**Critères d'acceptation**` with 3 to 5 checkboxes, then `**Livré** : \`vX.Y.Z\`` once delivered.
- Type: the organisation's issue types, `Feature` (a capability, a parent issue when it is split), `Task` (an improvement, tooling, docs) or `Bug`. Split work is sub-issues; "B cannot start before A" is a `blocked by` relation, not a sentence.
- Labels: one `zone:` label (`zone:cœur`, `zone:drive`, `zone:photos`, `zone:documents`, `zone:bibliothèques`, `zone:recherche`, `zone:ia`, `zone:web`, `zone:mobile`, `zone:os`, `zone:outillage`), and once delivered one version label (`v0.1.0`). Create a missing label rather than skipping it.
- Assignee: the person who did the work.
- Status on the project: `Backlog` (defined), `Ready` (ready to take), `In progress` (its worktree exists), `In review` (PR open), `To test` (merged, a criterion still untested by hand), `Done` (tested by the dev and merged).
- Every delivered change has an issue, including small ones only visible in `git log`.
- The PR closes its issue (`Closes #12` in its body).

Project ids for `gh project item-edit`: project `PVT_kwDODd-WqM4Br97_`, Status field `PVTSSF_lADODd-WqM4Br97_zhqOKlM`, options Backlog `de2aea2b`, Ready `97943967`, In progress `b1f57525`, In review `07884e7d`, To test `5c37f743`, Done `b8c5fbda`.

## Versioning

- `MAJOR.MINOR.PATCH`, 0.x until the first box runs at home. The version lives only in `VERSION` at the repository root; the core gets it at build time (`-X main.version`) and `/api/health` returns it. `web/package.json` and `worker/pyproject.toml` keep their own package versions, not the product's.
- One working day is one minor version. Bump it in the first commit of the day that changes something visible to a user. A day that only fixes bugs bumps the patch instead. Commits that only touch docs or tooling do not bump it.
- Never put the version number in a commit message.

## Commits and language

- Commit messages, PR titles and code are in English and follow Conventional Commits (`feat(drive): …`, `fix(photos): …`).
- Issues and conversation are in French.
