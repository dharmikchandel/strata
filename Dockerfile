# Builds and runs the Strata server. Deliberately simple: one stage, no
# build-cache tricks. (The image is large because it keeps the Go toolchain;
# a multi-stage build would shrink it but isn't needed to learn or run this.)
# The Go version must be at least the `go` line in go.mod.
FROM golang:1.26.7-bookworm

WORKDIR /src

# Download dependencies first, so editing code doesn't re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: the SQLite driver is pure Go, so these are static binaries.
# strata-gen (the traffic generator) is built into the same image so that compose
# can run it next to the server; it is not started unless asked.
# The server runs as an unprivileged user and keeps its state in /data.
RUN CGO_ENABLED=0 go build -o /usr/local/bin/strata ./cmd/strata \
    && CGO_ENABLED=0 go build -o /usr/local/bin/strata-gen ./cmd/strata-gen \
    && useradd --system --no-create-home strata \
    && mkdir /data && chown strata /data

USER strata

# Inside a container the server must listen on all interfaces, or the published
# port can't reach it. Who can reach it from outside is decided by how the port
# is published (docker-compose.yml binds it to the host's loopback).
ENV STRATA_LISTEN=0.0.0.0:7070 \
    STRATA_MANIFEST=/data/manifest.db

# The manifest (SQLite) lives here and MUST survive restarts: without it the
# server can no longer tell which segments in the bucket are real.
VOLUME /data
EXPOSE 7070

HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
    CMD ["strata", "-healthcheck"]

# Exec form, so the server is PID 1 and receives SIGTERM from `docker stop`
# directly (a shell wrapper would swallow it).
ENTRYPOINT ["strata"]
