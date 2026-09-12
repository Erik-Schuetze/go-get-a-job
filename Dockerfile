# syntax=docker/dockerfile:1

# Both stages are pinned by digest. A tag like :1.27-alpine is mutable - the
# upstream maintainers can repoint it at any time, and `docker build` would
# pick that up with no diff anywhere in this repository. The digest makes the
# image content immutable; the tag stays in front of it so a human reading
# the file can still tell what it is. .github/dependabot.yml proposes the
# next digest as a reviewable pull request.
FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/go-get-a-job /go-get-a-job

USER nonroot:nonroot
ENTRYPOINT ["/go-get-a-job"]
CMD ["--config", "/etc/go-get-a-job/config.yaml"]
