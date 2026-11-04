# Integration-harness images. Build from the repository root:
#
#   docker build -f test/Dockerfile --target app        -t frozen-throne-it .
#   docker build -f test/Dockerfile --target mockgithub -t ft-mockgithub .
#
# CGO is off (modernc sqlite is pure Go), so the binaries are static and run on
# distroless. Containers run as non-root; both binaries have a -healthcheck
# flag because the runtime image has no shell, curl or wget.

FROM golang:1.26.1 AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/frozen-throne ./cmd/frozen-throne \
 && go build -trimpath -ldflags="-s -w" -o /out/mockgithub ./test/mockgithub/cmd/mockgithub

FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=build /out/frozen-throne /frozen-throne
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=3s --start-period=2s --retries=15 CMD ["/frozen-throne", "-healthcheck"]
ENTRYPOINT ["/frozen-throne"]

FROM gcr.io/distroless/static-debian12:nonroot AS mockgithub
COPY --from=build /out/mockgithub /mockgithub
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=3s --start-period=2s --retries=15 CMD ["/mockgithub", "-healthcheck"]
ENTRYPOINT ["/mockgithub"]
