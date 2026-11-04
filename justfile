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

# Copy the production web build into the Go embed directory
embed-web: web-build
    rm -rf internal/webui/dist
    mkdir -p internal/webui/dist
    cp -R web/dist/. internal/webui/dist/
    touch internal/webui/dist/.gitkeep

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version

# Static (CGO off) binaries with the dashboard embedded, for linux/darwin amd64+arm64, into dist/
release-build: embed-web
    #!/usr/bin/env bash
    set -euo pipefail
    rm -rf dist && mkdir -p dist
    for os in linux darwin; do
      for arch in amd64 arm64; do
        echo "building $os/$arch"
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch {{go}} build -trimpath -ldflags '{{ldflags}}' \
          -o dist/frozen-throne-$os-$arch ./cmd/frozen-throne
      done
    done
    (cd dist && shasum -a 256 frozen-throne-* > checksums.txt)

# Build the Docker image (tag defaults to frozen-throne:dev)
image tag="frozen-throne:dev":
    docker build --build-arg VERSION={{version}} -t {{tag}} .

# Integration tests against the mock GitHub (INTEGRATION_MODE=auto|compose|inprocess|external; see test/README.md)
integration:
    {{go}} test -tags integration -race -count=1 -timeout 20m ./test/integration/...

# Integration tests against the docker compose stack
integration-compose:
    INTEGRATION_MODE=compose {{go}} test -tags integration -race -count=1 -timeout 20m ./test/integration/...

# Everything CI runs
check: lint test web-test build web-build
