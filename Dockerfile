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
FROM scratch

# Import TLS root CAs from the builder.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary.
COPY --from=builder /bin/gmmff /gmmff

# Non-root UID/GID (scratch has no /etc/passwd — declare numerically).
USER 65534:65534

EXPOSE 8080

ENTRYPOINT ["/gmmff", "serve"]
