# Cloud OS (nom de travail)

An all-in-one home cloud OS installed on a box: drive, photos, documents and media libraries on one core, with local AI search. The product spec is the PRD: <https://claude.ai/artifact/Ga3px6XWQWViJftYeD8pcR>.

## Project state

- 2026-11-17: repository created, no code yet. The V1 is phases 1 to 3 of the PRD roadmap (Core + Drive, Photos + mobile, AI search + Documents), run in Docker Compose. The OS packaging comes after.
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

None yet. Add the build, run, test and lint commands here with the first code, and use the same ones in steps 5 to 7 of the `topic` skill.

## Non-negotiable rules

- **No data loss, ever.** Indexing never modifies original files; integrity is checked by hash; deletes go to the trash; database migrations are versioned and reversible. A feature that can lose a file does not ship.
- **Nothing leaves the box by default.** Any call to an external API is explicit and enabled per module.
- Files are stored in clear, in a readable tree: the data stays usable without the product.

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

- `MAJOR.MINOR.PATCH`, 0.x until the first box runs at home. The file that holds the version is chosen with the stack; record it here.
- One working day is one minor version. Bump it in the first commit of the day that changes something visible to a user. A day that only fixes bugs bumps the patch instead. Commits that only touch docs or tooling do not bump it.
- Never put the version number in a commit message.

## Commits and language

- Commit messages, PR titles and code are in English and follow Conventional Commits (`feat(drive): …`, `fix(photos): …`).
- Issues and conversation are in French.
