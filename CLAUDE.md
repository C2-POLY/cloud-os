# Cloud OS (nom de travail)

An all-in-one home cloud OS installed on a box: drive, photos, documents and media libraries on one core, with local AI search. The product spec is the PRD: <https://claude.ai/artifact/Ga3px6XWQWViJftYeD8pcR>.

## Project state

- 2026-11-17: repository created; phase 1 skeleton in place (Compose stack, health endpoint, worker heartbeat, CI). The V1 is phases 1 to 3 of the PRD roadmap (Core + Drive, Photos + mobile, AI search + Documents), run in Docker Compose. The OS packaging comes after.
- Decisions are recorded in `docs/decisions/` (one numbered file each, with the why and the options rejected). Stack: `001-stack.md`.
- How it is built, domain by domain with diagrams, is in `docs/architecture/` (index in its `README.md`). Every subject with a design updates its domain's file in the same PR as the code.
- Still open in the PRD: product name, open or closed source, reference hardware (Raspberry Pi 5 or x86 mini PC). Update this file and the `topic` skill when one is decided.

## Stack

Containers first: the whole application runs in Docker Compose, and the OS is only a wrapper around it. Daily development happens on a Mac with Docker Desktop; OS image tests in a QEMU/UTM VM; real-hardware tests on the box.

- Core (accounts, storage, index, API, WebDAV, sharing, task queue): Go.
- AI workers (text and image embeddings, OCR, speech, classification): Python, models run with ONNX Runtime, no PyTorch in the image. They read the task queue and write results to the database; they read user files from a read-only mount and never modify them. The models (Arctic Embed M v2.0, SigLIP 2, PP-OCRv5; `docs/decisions/005-modeles-locaux.md`) are baked into the worker image at build time by `worker/models/fetch.sh`, each checked against its SHA-256, and loaded by `worker/src/worker/models.py`; ONNX Runtime's telemetry is switched off (`ORT_DISABLE_TELEMETRY`). The one call from the core to a worker is `POST /embed` (`worker/src/worker/embedserver.py`, port 8090 on the Compose network only, never published): the vector of a search, which cannot wait behind the queue. Each worker declares the embedding models and versions it serves in `embedding_models`; a new version makes the core queue every content again (`docs/architecture/embeddings.md`).
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
# Install Go, Python (uv) and web (npm) dependencies. The worker's HEIC tests
# also need libheif: `brew install libheif` on a Mac, or on Debian and Ubuntu
# `apt-get install --no-install-recommends libheif1 libheif-plugin-libde265`.
make deps

# Build the LGPL ffmpeg and ffprobe the worker's video tests use, into
# .tools/ffmpeg (a few minutes, once; `rm -rf .tools/ffmpeg` to rebuild).
# Without them `make test` skips the video tests.
make ffmpeg

# Fetch the worker's local AI models (about 740 MB) into .tools/models, each
# checked against its SHA-256 (once; run worker/models/fetch.sh .tools/models
# again after the script changes). Without them `make test` skips the model tests.
make models

# Build and start db, core, worker and web; wait until healthy
make up
# The app (the core serving the built front): http://localhost:8480
# Vite with hot reload, /api forwarded to the core: http://localhost:5180

# Follow the logs, stop the stack (the database volume is kept)
make logs
make down

# Lints, no warning allowed: gofmt, go vet, ruff check and format, tsc, oxlint (web and mobile)
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

# Queue N selftest tasks and wait for the workers to run them (the queue, end to end)
docker compose run --rm core selftest 20

# Queue the thumbnail of every stored image and video that has none (catch-up)
docker compose run --rm core thumbs backfill

# Queue the metadata (date, GPS, camera, a video's length) of every stored image and video that has none
docker compose run --rm core meta backfill

# Queue the text extraction of every stored document (PDF, Office, OpenDocument, text) that has none
docker compose run --rm core text backfill

# Queue the OCR of every stored image the OCR has not looked at, and of every scanned PDF (found without text) it has not read
docker compose run --rm core ocr backfill

# Queue the vector of every stored photo and video that has none of the workers' model version
# (also done once by the core whenever a worker declares a new version)
docker compose run --rm core photovec backfill

# The mobile app (Expo) in the iOS simulator; the box is at localhost:<core port>
cd mobile && npx expo run:ios

# The drive as a network disk: Finder › Se connecter au serveur › http://localhost:8480/dav/

# Set a new password for an account that lost it (read from stdin) and end its sessions
printf '%s\n' '<new password>' | docker compose run --rm -T core reset-password <account>
```

Layout: `core/` (Go module, `cmd/core`, `internal/`, SQL migrations in `core/migrations/`, applied by the core at startup with goose), `worker/` (Python, uv; the models' fetch script in `worker/models/`, their benchmark in `worker/bench/`), `web/` (React, Vite, npm), `mobile/` (Expo, React Native; `docs/architecture/mobile.md`), `shared/api-types.ts` (API types used by web and mobile), `compose.yaml`, `Makefile`, `.github/workflows/ci.yml` (`make lint` and `make test` on every PR and on `main`).

**One stack per worktree.** Give each worktree its own Compose project and, when another stack runs, its own host ports, so stacks never share a database:

```bash
COMPOSE_PROJECT_NAME=cloud-os-<name> CLOUDOS_DB_PORT=54321 CLOUDOS_CORE_PORT=8481 CLOUDOS_WEB_PORT=5181 make up
```

Defaults: database `54320`, core `8480`, Vite `5180`, all bound to `127.0.0.1`. User files are stored in `CLOUDOS_DATA_DIR` (default `./.data/files`, git-ignored), mounted at `/data` in the core. `CLOUDOS_MAX_UPLOAD` (bytes) caps a resumable upload; unset, only the free space does. `CLOUDOS_MAPS_DIR` (default `./.data/maps`) holds the `.pmtiles` file of the Photos map, mounted read-only at `/maps` in the core; without one the map says how to install it (`docs/architecture/map.md`). `CLOUDOS_MODELS_DIR` names the worker's model folder (`/opt/models` in its image, `.tools/models` for `make test`). The Compose credentials (`cloudos`/`cloudos`) are for the local throwaway database only.

## Non-negotiable rules

- **No data loss, ever.** Indexing never modifies original files; integrity is checked by hash; deletes go to the trash; database migrations are versioned and reversible. A feature that can lose a file does not ship.
- **Nothing leaves the box by default.** Any call to an external API is explicit and enabled per module.
- Files are stored in clear, in a readable tree: the data stays usable without the product. Only `core/internal/storage` writes there: it writes to a temporary file, checks it, then links it into place without ever replacing a file; it never modifies a stored file in place: replacing one (explicit `Replace` only) first moves its content to `.cloudos/versions`, and deleting means moving to the trash (`.cloudos/trash`); only the trash purge and the version retention erase bytes: on expiry (`CLOUDOS_TRASH_DAYS`, 30 by default), on a confirmed request, or beyond the last `CLOUDOS_VERSIONS_KEEP` (10) versions older than `CLOUDOS_VERSIONS_DAYS` (30). `core/internal/guard` fails the build if any function outside its closed `allowedErasers` list can erase a file. A move, a trash, a restore or a replace is journaled in `moves` before the disk is touched, holds the owner's lock throughout, never renames onto an existing path, and is finished or dropped by `RecoverMoves` at startup. Every ancestor of an index row has its own `dir` row. Every query on live files filters `trash_id IS NULL`.
- **Every API route needs a session** except the ones listed as `public` in `core/internal/httpapi` (health, setup, login, logout, and the read-only guest routes of a share link under `/api/s/{token}`); `TestEveryRouteIsProtected` fails when that list changes. State-changing requests must be JSON (cross-site request forgery). WebDAV at `/dav/` is the one exception: it uses HTTP Basic with the account's password (checked logins cached 10 minutes, keyed by a hash), and its methods cannot be sent cross-site without a CORS preflight. Passwords are argon2id; only the SHA-256 of a session token is stored.
- **Dependencies and models have a permissive licence** (MIT, BSD including 0BSD, Apache-2.0, MPL-2.0, ISC, BlueOak-1.0.0). One exception: LGPL for an unmodified library linked dynamically or an unmodified separate binary, built without any GPL part (libheif, libde265, an LGPL ffmpeg without x264 or x265; `docs/decisions/002-lgpl.md`). The worker image installs libheif's Debian packages with `--no-install-recommends`, which keeps the x265 encoder plugin out. Fonts may be under the SIL Open Font License, served unmodified with their licence (the map's Noto Sans; `docs/decisions/004-fonts-ofl.md`). No AGPL or GPL, no non-commercial licence, no source-available licence (Commons Clause, GSAP's Standard License), so the project can still be released as open source: GSAP and React Bits were rejected for this (`docs/decisions/003-front-motion.md`).

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
