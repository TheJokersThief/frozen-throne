# syntax=docker/dockerfile:1

# 1. Dashboard
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Server (static, CGO off, dashboard embedded)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/ internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/frozen-throne ./cmd/frozen-throne \
 && mkdir /out/data

# 3. Runtime: no shell, no package manager, non-root
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/frozen-throne /usr/local/bin/frozen-throne
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV FT_DB_PATH=/data/frozen-throne.db \
    FT_LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/frozen-throne", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/frozen-throne"]
