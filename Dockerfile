# ── Stage 1: build Wasm ───────────────────────────────────────────────────────
# Wasm must be built before the main binary because internal/localmode/embed.go
# uses //go:embed and requires internal/localmode/static/ to exist at compile time.
FROM golang:1.26-alpine AS wasm-builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the browser Wasm binary.
RUN GOOS=js GOARCH=wasm go build \
    -ldflags="-s -w" \
    -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm

# Copy wasm_exec.js from the Go toolchain (path differs by Go version).
RUN cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/static/wasm_exec.js 2>/dev/null || \
    cp "$(go env GOROOT)/lib/wasm/wasm_exec.js"  web/static/wasm_exec.js

# Populate the embed directory that the main binary needs.
RUN mkdir -p internal/localmode/static && \
    cp -rf web/static/. internal/localmode/static/

# ── Stage 2: build the server binary ──────────────────────────────────────────
FROM golang:1.26-alpine AS builder

# TARGETARCH is set automatically by Docker Buildx for multi-platform builds.
ARG TARGETARCH
ARG TARGETOS=linux

RUN apk add --no-cache ca-certificates git

WORKDIR /src

# Re-download modules in this stage (layer-cache friendly).
COPY go.mod go.sum ./
RUN go mod download

# Copy source and the already-built Wasm + embed assets from stage 1.
COPY . .
COPY --from=wasm-builder /src/web/static/gmmff.wasm       ./web/static/gmmff.wasm
COPY --from=wasm-builder /src/web/static/wasm_exec.js     ./web/static/wasm_exec.js
COPY --from=wasm-builder /src/internal/localmode/static/  ./internal/localmode/static/

# Build a fully-static binary for the target platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -ldflags="-s -w \
      -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
      -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
      -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /bin/gmmff ./cmd/gmmff

# ── Stage 3: minimal runtime ──────────────────────────────────────────────────
# alpine instead of scratch so wget is available for the Docker healthcheck.
FROM alpine:3.23

# ca-certificates for outbound TLS (Redis TLS, TURN ephemeral credentials).
RUN apk add --no-cache ca-certificates wget shadow

# Copy the server binary.
COPY --from=builder /bin/gmmff /gmmff

# Copy the browser UI so gmmff serve --web /web/static serves it.
COPY --from=wasm-builder /src/web/static /web/static

# Entrypoint script: applies PUID/PGID at container start then exec's gmmff.
COPY --chmod=755 <<'EOF' /entrypoint.sh
#!/bin/sh
set -e

PUID=${PUID:-10001}
PGID=${PGID:-10001}

# Create group if it doesn't exist with the requested GID.
if ! getent group gmmff > /dev/null 2>&1; then
  groupadd -g "${PGID}" gmmff
fi

# Create user if it doesn't exist with the requested UID.
if ! getent passwd gmmff > /dev/null 2>&1; then
  useradd -u "${PUID}" -g gmmff -s /sbin/nologin -M gmmff
fi

echo "Running as uid=${PUID} gid=${PGID}"
exec su-exec gmmff "$@"
EOF

RUN apk add --no-cache su-exec

EXPOSE 8080

# OCI labels for image metadata
LABEL description="Fast, secure, private, simple open source file transfer and messaging application" \
      maintainer="iamdoubz <https://github.com/iamdoubz>" \
      org.opencontainers.image.description="Fast, secure, private, simple open source file transfer and messaging application" \
      org.opencontainers.image.authors="iamdoubz" \
      org.opencontainers.image.title="Fast, secure, private, simple open source file transfer and messaging application" \
      org.opencontainers.image.source="https://github.com/iamdoubz/gmmff" \
      org.opencontainers.image.created=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
      org.opencontainers.image.documentation="https://github.com/iamdoubz/gmmff/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT License" \
      org.opencontainers.image.url="https://gmmff.404.mn" \
      org.opencontainers.image.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
      org.opencontainers.image.revision=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/gmmff", "serve", "--web", "/web/static"]
