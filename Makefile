# Cloud OS — development commands. See CLAUDE.md, "Commands".
#
# Tests run against the Compose database (started on demand), each test in its
# own throwaway database. In CI, EXTERNAL_DB=1 uses the job's Postgres service.

DB_PORT ?= $(or $(CLOUDOS_DB_PORT),54320)
TEST_DATABASE_URL ?= postgres://cloudos:cloudos@127.0.0.1:$(DB_PORT)/cloudos?sslmode=disable
EXTERNAL_DB ?=

# CLOUDOS_SMB=1 adds the smb service (its Compose profile) to up and logs, and
# CLOUDOS_TAILSCALE=1 the tailscale service (TS_AUTHKEY in the shell; docs/access.md).
comma := ,
profiles := $(strip $(if $(filter 1,$(CLOUDOS_SMB)),smb) $(if $(filter 1,$(CLOUDOS_TAILSCALE)),tailscale))
ifneq ($(profiles),)
export COMPOSE_PROFILES := $(subst $() ,$(comma),$(profiles))
endif
# The core and smb believe the tailscale service's headers only when it runs.
ifeq ($(CLOUDOS_TAILSCALE),1)
export CLOUDOS_TRUSTED_PROXIES ?= tailscale
endif

.PHONY: up down logs prod prod-down prod-logs deps ffmpeg models test test-db test-core test-worker test-web test-mobile lint fmt

## up: build and start the stack, wait until it is healthy
up:
	docker compose up --build --detach --wait
	@echo "Cloud OS: http://localhost:$${CLOUDOS_CORE_PORT:-8480}  (Vite dev: http://localhost:$${CLOUDOS_WEB_PORT:-5180})"
ifeq ($(CLOUDOS_SMB),1)
	@echo "SMB, read-only: smb://<account>@localhost:$${CLOUDOS_SMB_PORT:-1445}/drive, with the account's SMB password (Settings)"
endif

## down: stop the stack, smb and tailscale included (keeps the database volume)
down:
	docker compose --profile smb --profile tailscale down

## logs: follow the logs of every service
logs:
	docker compose logs --follow

## prod: start the production stack (project cloud-os-box): draw its secrets into
## box.env on the first run, then build and start db, core and worker
## (docs/architecture/box.md)
prod:
	scripts/box init
	scripts/box up

## prod-down: stop the production stack (keeps its database volume)
prod-down:
	scripts/box down

## prod-logs: follow the logs of the production stack
prod-logs:
	scripts/box logs --follow

## deps: install Go, Python, web and mobile dependencies
deps:
	cd core && go mod download
	cd core/third_party/smbsrv && go mod download
	cd worker && uv sync --frozen
	cd web && npm ci
	cd mobile && npm ci

## ffmpeg: build the worker's LGPL ffmpeg and ffprobe into .tools/ffmpeg, for
## the video tests (the same build as worker/Dockerfile; skipped when present)
ffmpeg: .tools/ffmpeg/bin/ffprobe

# No dependency on build.sh: a cache restored in CI is older than the checkout.
# After changing the script, `rm -rf .tools/ffmpeg` to rebuild.
.tools/ffmpeg/bin/ffprobe:
	worker/ffmpeg/build.sh .tools/ffmpeg

## models: fetch the worker's local AI models into .tools/models (about 1 GB),
## checked against their SHA-256, for the model tests (the same files as
## worker/Dockerfile; skipped when present)
models: .tools/models/SHA256SUMS

# fetch.sh writes SHA256SUMS last. After changing the script, run it again
# (`worker/models/fetch.sh .tools/models`): it replaces what changed.
.tools/models/SHA256SUMS:
	worker/models/fetch.sh .tools/models

## test: every test suite (Go, Python, web, mobile)
test: test-core test-worker test-web test-mobile

test-db:
ifeq ($(EXTERNAL_DB),)
	docker compose up --detach --wait db
endif

test-core: test-db
	cd core && CLOUDOS_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -count=1 ./...
	# The SMB library we keep patched (core/third_party/smbsrv/PATCHES.md), a module of its own
	cd core/third_party/smbsrv && go test -race -count=1 ./...

test-worker: test-db
	cd worker && CLOUDOS_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' CLOUDOS_FFMPEG_DIR='$(CURDIR)/.tools/ffmpeg/bin' CLOUDOS_MODELS_DIR='$(CURDIR)/.tools/models' uv run --frozen pytest

test-web:
	cd web && npm test

test-mobile:
	cd mobile && npm test

## lint: formatting and static checks, no warning allowed
lint:
	@unformatted="$$(gofmt -l core)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	cd core && go vet ./...
	cd core/third_party/smbsrv && go vet ./...
	cd worker && uv run --frozen ruff check . && uv run --frozen ruff format --check .
	cd web && npm run typecheck && npm run lint
	cd mobile && npm run typecheck && npm run lint

## fmt: rewrite Go and Python files in place
fmt:
	gofmt -w core
	cd worker && uv run --frozen ruff format . && uv run --frozen ruff check --fix .
