# Cloud OS — development commands. See CLAUDE.md, "Commands".
#
# Tests run against the Compose database (started on demand), each test in its
# own throwaway database. In CI, EXTERNAL_DB=1 uses the job's Postgres service.

DB_PORT ?= $(or $(CLOUDOS_DB_PORT),54320)
TEST_DATABASE_URL ?= postgres://cloudos:cloudos@127.0.0.1:$(DB_PORT)/cloudos?sslmode=disable
EXTERNAL_DB ?=

.PHONY: up down logs deps test test-db test-core test-worker test-web test-mobile lint fmt

## up: build and start the stack, wait until it is healthy
up:
	docker compose up --build --detach --wait
	@echo "Cloud OS: http://localhost:$${CLOUDOS_CORE_PORT:-8480}  (Vite dev: http://localhost:$${CLOUDOS_WEB_PORT:-5180})"

## down: stop the stack (keeps the database volume)
down:
	docker compose down

## logs: follow the logs of every service
logs:
	docker compose logs --follow

## deps: install Go, Python, web and mobile dependencies
deps:
	cd core && go mod download
	cd worker && uv sync --frozen
	cd web && npm ci
	cd mobile && npm ci

## test: every test suite (Go, Python, web, mobile)
test: test-core test-worker test-web test-mobile

test-db:
ifeq ($(EXTERNAL_DB),)
	docker compose up --detach --wait db
endif

test-core: test-db
	cd core && CLOUDOS_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -count=1 ./...

test-worker: test-db
	cd worker && CLOUDOS_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' uv run --frozen pytest

test-web:
	cd web && npm test

test-mobile:
	cd mobile && npm test

## lint: formatting and static checks, no warning allowed
lint:
	@unformatted="$$(gofmt -l core)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	cd core && go vet ./...
	cd worker && uv run --frozen ruff check . && uv run --frozen ruff format --check .
	cd web && npm run typecheck && npm run lint
	cd mobile && npm run typecheck && npm run lint

## fmt: rewrite Go and Python files in place
fmt:
	gofmt -w core
	cd worker && uv run --frozen ruff format . && uv run --frozen ruff check --fix .
