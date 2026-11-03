# Integration-harness images. Build from the repository root:
#
#   docker build -f test/Dockerfile --target app        -t ft-test-app .
#   docker build -f test/Dockerfile --target mockgithub -t ft-mockgithub .
#
# CGO is off (modernc sqlite is pure Go), so the binaries are static and run on
# distroless. Containers run as non-root; both binaries have a -healthcheck
# flag because the runtime image has no shell, curl or wget.

FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/ft-test-app ./test/app/cmd/ft-test-app \
 && go build -trimpath -ldflags="-s -w" -o /out/mockgithub ./test/mockgithub/cmd/mockgithub

FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=build /out/ft-test-app /ft-test-app
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=3s --start-period=2s --retries=15 CMD ["/ft-test-app", "-healthcheck"]
ENTRYPOINT ["/ft-test-app"]

FROM gcr.io/distroless/static-debian12:nonroot AS mockgithub
COPY --from=build /out/mockgithub /mockgithub
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=3s --start-period=2s --retries=15 CMD ["/mockgithub", "-healthcheck"]
ENTRYPOINT ["/mockgithub"]
