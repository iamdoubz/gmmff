.PHONY: build install local run-server create join chat dev test test-race test-cover cover lint fmt fmt-check tidy docker up down clean wasm wasm-serve cleanup help

BINARY    := gmmff
CMD       := ./cmd/gmmff
PREFIX    ?= /usr/local
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
             -X main.version=$(VERSION) \
             -X main.commit=$(COMMIT) \
             -X main.date=$(DATE)

# Detect Go version and set the correct wasm_exec.js path.
# Go 1.24+ moved the file from misc/wasm/ to lib/wasm/.
GO_VERSION_MINOR := $(shell go version | sed 's/.*go1\.\([0-9]*\).*/\1/')
WASM_EXEC_SRC    := $(shell \
	if [ "$(GO_VERSION_MINOR)" -ge 24 ] 2>/dev/null; then \
		echo "$$(go env GOROOT)/lib/wasm/wasm_exec.js"; \
	else \
		echo "$$(go env GOROOT)/misc/wasm/wasm_exec.js"; \
	fi)

## build: compile the binary (builds Wasm first, then copies assets for embed)
build: wasm
	mkdir -p internal/localmode/static
	cp -rf web/static/. internal/localmode/static/
	go build -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

## install: install gmmff to PREFIX/bin (default /usr/local/bin, requires sudo)
install: build
	install -m 755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)

## local: alias for running gmmff local after building
local: build
	./bin/$(BINARY) local $(ARGS)

## run-server: run the signaling server with in-memory store (dev)
run-server: build
	./bin/$(BINARY) serve --memory --log-pretty --log-level debug

## create: quick test — create a file+message session
create: build
	./bin/$(BINARY) create $(ARGS)

## join: quick test — join a session (usage: make join CODE=word-word-word)
join: build
	./bin/$(BINARY) join $(CODE) $(ARGS)

## chat: quick test — start a pure chat session
chat: build
	./bin/$(BINARY) chat $(ARGS)

## dev: run signaling server with live-reload (go install github.com/air-verse/air@latest)
dev:
	air

## test: run all tests (Wasm package excluded via build constraints)
test:
	go test -count=1 ./...

## test-race: run tests with race detector (requires clang/gcc with CGO; not supported on Windows MSVC)
test-race:
	CGO_ENABLED=1 CC=clang go test -race -count=1 ./...

## test-cover: run tests with coverage report
test-cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

## cover: open visual coverage report in browser (run after test-cover)
cover: test-cover
	go tool cover -html=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## fmt: format all Go source files with gofmt -s (simplify + format)
fmt:
	gofmt -s -w $(shell find . -name "*.go" -not -path "./vendor/*")

## fmt-check: verify all Go source files are gofmt -s compliant (non-destructive, use in CI)
fmt-check:
	@unformatted=$$(gofmt -s -l $$(find . -name "*.go" -not -path "./vendor/*")); \
	if [ -n "$$unformatted" ]; then \
		echo "The following files need gofmt -s:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "All files are gofmt -s compliant."

## lint: run golangci-lint (go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
lint:
	golangci-lint run ./...

## tidy: tidy Go modules
tidy:
	go mod tidy

## docker: build Docker image
docker:
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  -t iamdoubz/gmmff:$(VERSION) \
	  -t iamdoubz/gmmff:latest \
	  .

## up: start full stack with Docker Compose
up:
	docker compose up --build

## down: stop Docker Compose stack
down:
	docker compose down

## wasm: build the browser Wasm binary and copy wasm_exec.js
wasm:
	GOOS=js GOARCH=wasm go build -ldflags="$(LDFLAGS)" -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm
	cp "$(WASM_EXEC_SRC)" web/static/wasm_exec.js
	@echo "Built: web/static/gmmff.wasm"
	@echo "  Size: $$(du -sh web/static/gmmff.wasm | cut -f1)"
	@echo "  wasm_exec.js from: $(WASM_EXEC_SRC)"

## wasm-serve: build Wasm and start the dev web server on :9000
wasm-serve: wasm
	go run ./web --addr :9000

## clean: remove build artifacts
clean:
	rm -rf bin/ coverage.out coverage.html web/static/gmmff.wasm web/static/wasm_exec.js

## cleanup: remove expired schedule uploads (suitable for cron)
cleanup: build
	./bin/$(BINARY) cleanup

## help: show this help
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
