go := env_var_or_default("GO", "go")

# List available recipes
default:
    @just --list

# Build the Go binary into bin/
build:
    {{go}} build -o bin/frozen-throne ./cmd/frozen-throne

# Run Go tests
test:
    {{go}} test ./...

# Run go vet
vet:
    {{go}} vet ./...

# Fail if any Go file is not gofmt'd
fmt:
    @test -z "$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)

# Go vet, gofmt, OpenAPI lint, TS typecheck
lint: vet fmt lint-api web-typecheck

# Install web dependencies
web-install:
    cd web && npm ci

# Typecheck the web app
web-typecheck: web-install
    cd web && npm run typecheck

# Run web tests
web-test: web-install
    cd web && npm test

# Production web build into web/dist
web-build: web-install
    cd web && npm run build

# Regenerate web/src/api/schema.d.ts from api/openapi.yaml
gen-api: web-install
    cd web && npm run gen:api

# Validate api/openapi.yaml
lint-api: web-install
    cd web && npm run lint:api

# Everything CI runs
check: lint test web-test build web-build
