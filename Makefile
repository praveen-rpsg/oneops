SHELL   := /bin/bash
VERSION ?= 0.1.0-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG     := github.com/rpsg/oneops
LDFLAGS := -s -w -X $(PKG)/pkg/version.Version=$(VERSION) -X $(PKG)/pkg/version.Commit=$(COMMIT)

.DEFAULT_GOAL := help

.PHONY: help up down build run test test-integration cover lint fmt vet tidy docker gen migrate-hash migrate-validate clean

MIGRATIONS_DIR := internal/store/migrate/sql
ATLAS := docker run --rm -v $(PWD):/src -w /src arigaio/atlas:latest

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

lint: ## Run golangci-lint
	golangci-lint run ./...

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

clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html cover.out

web: ## Build the console into the Go embed directory
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

web-test: ## Typecheck and unit-test the console
	pnpm --dir web exec tsc -b --noEmit
	pnpm --dir web exec vitest run
