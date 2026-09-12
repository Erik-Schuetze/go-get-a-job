# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO disabled: modernc.org/sqlite is a pure-Go driver, so no C toolchain
# is needed and the resulting binary is fully static - this keeps the
# final image minimal (no libc dependency) and avoids cross-compile pain.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/go-get-a-job ./cmd/go-get-a-job

# distroless/static has no shell, no package manager, and runs as a
# non-root user by default - go-get-a-job never needs any of that at runtime.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/go-get-a-job /go-get-a-job

USER nonroot:nonroot
ENTRYPOINT ["/go-get-a-job"]
CMD ["--config", "/etc/go-get-a-job/config.yaml"]
