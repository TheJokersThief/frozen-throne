.PHONY: all build test lint vet fmt web-install web-test web-build web-typecheck gen-api lint-api check

GO ?= go

all: build

build: ## Build the Go binary into bin/
	$(GO) build -o bin/frozen-throne ./cmd/frozen-throne

test: ## Run Go tests
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt: ## Fail if any Go file is not gofmt'd
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)

lint: vet fmt lint-api web-typecheck ## Go vet, gofmt, OpenAPI lint, TS typecheck

web-install:
	cd web && npm ci

web-typecheck: web-install
	cd web && npm run typecheck

web-test: web-install
	cd web && npm test

web-build: web-install
	cd web && npm run build

gen-api: web-install ## Regenerate web/src/api/schema.d.ts from api/openapi.yaml
	cd web && npm run gen:api

lint-api: web-install ## Validate api/openapi.yaml
	cd web && npm run lint:api

check: lint test web-test build web-build ## Everything CI runs
