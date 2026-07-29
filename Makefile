SHELL   := /bin/bash
VERSION ?= 0.1.0-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG     := github.com/rpsg/oneops
LDFLAGS := -s -w -X $(PKG)/pkg/version.Version=$(VERSION) -X $(PKG)/pkg/version.Commit=$(COMMIT)

.DEFAULT_GOAL := help

.PHONY: help up down build run test test-integration cover lint fmt vet tidy docker gen migrate-hash migrate-validate clean db-backup db-restore dr-drill db-reset web web-test contract-breaking

MIGRATIONS_DIR := internal/store/migrate/sql
ATLAS := docker run --rm -v "$(PWD)":/src -w /src arigaio/atlas:latest

OPENAPI_SPEC := internal/httpapi/openapi.yaml
# Pinned, and kept identical to the version in .github/workflows/ci.yml. An
# unpinned diff tool silently changes what counts as a breaking change, so a
# release that adds a rule turns an unrelated PR red and `make contract-breaking`
# would disagree with CI about what passes.
OASDIFF_VERSION := v1.11.7

# Backup tooling runs in a container pinned to the server's major version.
# pg_dump refuses to dump a server newer than itself, so a developer's
# Homebrew client (14.x on this machine) cannot back up the 16.x server. In
# production the same constraint applies, which is why the backup CronJob must
# use the postgres image matching the database rather than a generic runner.
# Attaching to the compose network lets the container reach postgres by name,
# so this works identically regardless of host port remapping.
PG_IMAGE   := postgres:16
PG_NETWORK := oneops_default
PG_URL     := postgres://oneops:dev@oneops-postgres:5432/oneops?sslmode=disable
PGTOOLS    := docker run --rm --network $(PG_NETWORK) -v "$(PWD)":/src -w /src -e "ONEOPS_DB_URL=$(PG_URL)" $(PG_IMAGE)

# Load .env (gitignored) so local runs hit this machine's port mapping rather
# than the compose defaults, which other projects here already occupy.
DOTENV := set -a; if [ -f .env ]; then . ./.env; fi; set +a;

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

up: ## Start local dependencies (postgres, nats, redis, opensearch, minio)
	docker compose up -d

down: ## Stop local dependencies and remove volumes
	docker compose down -v

build: ## Build the control-plane binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/controlplane ./cmd/controlplane

run: ## Run the control plane locally
	@$(DOTENV) go run ./cmd/controlplane

db-backup: ## Take a verified logical backup into ./backups
	@$(PGTOOLS) scripts/db-backup.sh /src/backups

dr-drill: ## Back up, restore into a throwaway database, and verify (see docs/disaster-recovery.md)
	@$(PGTOOLS) scripts/dr-drill.sh

db-reset: ## Drop and recreate the local dev schema (destructive; dev only)
	@echo "Resetting the local development database. This destroys all local data."
	@echo "audit_event is append-only, so DROP SCHEMA is the only way to clear"
	@echo "audit history — which is why this target exists and why it is dev-only."
	@docker exec oneops-postgres psql -U oneops -d oneops -q \
		-c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;' \
		-c 'GRANT ALL ON SCHEMA public TO oneops; GRANT ALL ON SCHEMA public TO public;'
	@echo "Done. Migrations re-run on next control-plane start."

test: ## Run all tests with the race detector and coverage
	go test ./... -race -cover

cover: ## Produce an HTML coverage report
	go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out -o coverage.html

GOLANGCI_VERSION ?= v2.12.2
GOBIN_DIR := $(shell go env GOPATH)/bin

lint: ## Run golangci-lint (installs it on first use)
	@command -v golangci-lint >/dev/null 2>&1 || \
		[ -x "$(GOBIN_DIR)/golangci-lint" ] || { \
			echo "installing golangci-lint $(GOLANGCI_VERSION)..."; \
			go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION); \
		}
	@PATH="$(GOBIN_DIR):$$PATH" golangci-lint run ./...

fmt: ## Format Go sources
	gofmt -w .

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go modules
	go mod tidy

test-integration: ## Run integration tests (TEST_DATABASE_URL, from .env if present)
	@$(DOTENV) go test ./... -tags=integration -race -cover

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t oneops/controlplane:$(VERSION) .

gen: ## Generate the Go SDK from the OpenAPI contract
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 \
		-generate types,client -package oneops \
		-o sdk/go/client.gen.go internal/httpapi/openapi.yaml

migrate-hash: ## Recompute the Atlas migration checksum (atlas.sum)
	$(ATLAS) migrate hash --dir "file://$(MIGRATIONS_DIR)"

migrate-validate: ## Validate the Atlas migration directory
	$(ATLAS) migrate validate --dir "file://$(MIGRATIONS_DIR)"

# BASE_REF is the git ref to compare against; override to check against
# something other than the trunk, e.g. `make contract-breaking BASE_REF=HEAD~1`.
BASE_REF ?= origin/master

contract-breaking: ## Fail if the OpenAPI contract breaks clients built against BASE_REF
	@git show "$(BASE_REF):$(OPENAPI_SPEC)" > /tmp/oneops-openapi-base.yaml 2>/dev/null \
		|| { echo "no $(OPENAPI_SPEC) at $(BASE_REF) — nothing to compare"; exit 0; }
	@go run github.com/oasdiff/oasdiff@$(OASDIFF_VERSION) breaking \
		--fail-on ERR /tmp/oneops-openapi-base.yaml $(OPENAPI_SPEC)

clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html cover.out

web: ## Build the console into the Go embed directory
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

web-test: ## Typecheck and unit-test the console
	pnpm --dir web exec tsc -b --noEmit
	pnpm --dir web exec vitest run
