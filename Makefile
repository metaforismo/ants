# Ants monorepo task runner.
# Every CI gate is runnable locally: `make ci` is the full suite.

SHELL := /bin/sh
GO := go
STATICCHECK_VERSION := 2026.2.1
PNPM ?= pnpm

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## Format all Go code
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is unformatted
	@out=$$($(GO) fmt ./...); if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet on all packages
	$(GO) vet ./...

.PHONY: lint
lint: ## Run staticcheck (pinned version)
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

.PHONY: test
test: ## Run unit and contract tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector
	$(GO) test -race ./...

.PHONY: build
build: ## Build the CLI and API server binaries into bin/
	$(GO) build -o bin/ants ./cmd/ants
	$(GO) build -o bin/ants-api ./cmd/api

.PHONY: demo
demo: build ## Run the deterministic vertical slice end to end (real git, real commands)
	./bin/ants demo run

.PHONY: contracts-generate
contracts-generate: ## Regenerate TypeScript types from the OpenAPI spec
	$(PNPM) --filter @ants/contracts generate

.PHONY: contracts-test
contracts-test: ## Test the generated contracts package
	$(PNPM) --filter @ants/contracts test

.PHONY: contracts-drift
contracts-drift: ## Fail if generated types are stale relative to the OpenAPI spec
	$(PNPM) --filter @ants/contracts generate
	@git diff --exit-code -- packages/contracts/src/schema.d.ts \
		|| (echo "schema.d.ts is stale; run 'make contracts-generate' and commit" && exit 1)

.PHONY: integration-postgres
integration-postgres: ## Run migration integration tests against a disposable Postgres container
	./scripts/test-postgres.sh

.PHONY: manifest-check
manifest-check: ## Verify every direct Go dependency is recorded in third_party/manifest.yaml
	$(GO) run ./scripts/manifestcheck

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum are not tidy
	$(GO) mod tidy
	@git diff --exit-code -- go.mod go.sum \
		|| (echo "go.mod/go.sum changed; commit the tidied versions" && exit 1)

.PHONY: ci
ci: fmt-check vet lint tidy-check manifest-check test test-race build contracts-test contracts-drift ## Full local CI suite

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/
