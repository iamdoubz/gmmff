.PHONY: build run-server send receive dev test test-cover lint tidy docker up down clean wasm wasm-serve help

BINARY    := gmmff
CMD       := ./cmd/gmmff
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
             -X main.version=$(VERSION) \
             -X main.commit=$(COMMIT) \
             -X main.date=$(DATE)

## build: compile a local binary
build:
	go build -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

## run-server: run the signaling server with in-memory store (dev)
run-server: build
	./bin/$(BINARY) serve --memory --log-pretty --log-level debug

## send: quick test — send a file (usage: make send FILE=path/to/file)
send: build
	./bin/$(BINARY) send $(FILE)

## receive: quick test — receive a file (usage: make receive CODE=word-word-word)
receive: build
	./bin/$(BINARY) receive $(CODE)

## dev: run with live-reload via air (go install github.com/air-verse/air@latest)
dev:
	air

## test: run all tests
test:
	go test -race -count=1 ./...

## test-cover: run tests with coverage report
test-cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: run golangci-lint (go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
lint:
	golangci-lint run ./...

## tidy: tidy and vendor modules
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
	cp "$$(go env GOROOT)/misc/wasm/wasm_exec.js" web/static/wasm_exec.js
	@echo "Built: web/static/gmmff.wasm"
	@echo "  Size: $$(du -sh web/static/gmmff.wasm | cut -f1)"

## wasm-serve: build Wasm and start the dev web server
wasm-serve: wasm
	go run ./web --addr :9000

## clean: remove build artifacts
clean:
	rm -rf bin/ coverage.out coverage.html web/static/gmmff.wasm web/static/wasm_exec.js

## help: show this help
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
