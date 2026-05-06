# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

# ca-certificates needed for outbound TLS (Redis TLS, TURN credentials)
RUN apk add --no-cache ca-certificates git

WORKDIR /src

# Cache module downloads before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a fully-static binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-s -w \
      -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
      -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
      -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /bin/gmmff ./cmd/gmmff

# ── Stage 2: minimal runtime ──────────────────────────────────────────────────
FROM scratch

# Import TLS root CAs from the builder.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary.
COPY --from=builder /bin/gmmff /gmmff

# Non-root UID/GID (scratch has no /etc/passwd — declare numerically).
USER 65534:65534

EXPOSE 8080

ENTRYPOINT ["/gmmff", "serve"]
