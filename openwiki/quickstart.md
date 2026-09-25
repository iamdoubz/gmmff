---
type: Guide
title: Quickstart
description: Step-by-step guide to set up, build, and run gmmff for development.
tags: [getting-started, build, setup]
verified:
  - by: openwiki/0.6.0
    at: 2026-09-25T13:12:29.362Z
sources:
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "openwiki/0.6.0", at: "2026-09-25T13:12:29.362Z" }
---

# gmmff Quickstart

This guide gets you from zero to running your own gmmff signaling server and CLI client in minutes.

## Prerequisites

- **Go** 1.23+ (1.26 recommended) – [install Go](https://go.dev/dl/)
- **Git** – [install Git](https://git-scm.com/downloads)
- **Redis** 7+ **or** **Valkey** 7.2+ – either works as a drop-in; for development you can use the in-memory store.
- **Node.js** (optional, only if you wish to serve the WASM client via a Node HTTP server; the built-in Go server works fine) – [install Node.js](https://nodejs.org/)

> **Note for AI-assisted development**: When working on this repository with Claude, always activate the `gmmff` agent at the start of every session using `memanto agent activate gmmff` and interact with MEMANTO for context retention.

## Getting the code

```bash
git clone https://github.com/iamdoubz/gmmff
cd gmmff
```

## Building

The project uses a Makefile for common tasks.

### Build the binary (includes WASM assets)

```bash
make build
```

This first runs `make wasm` to compile the WebAssembly client and embeds it into the binary, then builds the Go binary.

### Build only the WASM client

```bash
make wasm
```

Outputs `web/static/gmmff.wasm` and copies the required `wasm_exec.js`.

## Running the signaling server

### Development (in-memory store)

```bash
make run-server
```

Equivalent to:
```bash
./bin/gmmff serve --memory --log-pretty --log-level debug
```

### With Redis or Valkey

Set the `GMMFF_REDIS_URL` environment variable (e.g., `redis://localhost:6379` or `valkey://localhost:6379`) and run:

```bash
make run-server
```

The Makefile target still works; the binary reads the environment variable.

## Trying it out

Open two terminals.

**Terminal 1 – create a session:**

```bash
make create
```

This will output a code like `bear-cozy-cone` and wait for a peer to join.

**Terminal 2 – join the session:**

```bash
make join CODE=bear-cozy-cone
```

You can then send files or messages.

For a pure chat session:

```bash
make chat
```

And join with:

```bash
make join CODE=<code-from-chat>
```

## Running the WASM web client

After building (`make wasm` or `make build`), the static files are in `web/static/`. You can serve them with any HTTP server.

### Using the built-in Go web server

```bash
make wasm-serve
```

This builds the WASM (if needed) and starts a Go web server on :9000. Open <http://localhost:9000> in your browser.

### Using Node.js (optional)

If you prefer Node.js, install a simple server like `serve` or use `python -m http.server`, then point it to `web/static/`.

## Running tests

```bash
make test
```

For race detection (Linux/macOS only):

```bash
make test-race
```

## Next steps

<!-- openwiki: broken internal link [docs/CLI.md] file "docs/CLI.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- Explore the [CLI documentation](docs/CLI.md) for all available commands.
- Read the [Architecture Overview](/openwiki/architecture/overview.md) to understand the system design.
- Check out the [Key Workflows](/openwiki/workflows/key-workflows.md) for common usage patterns.

Now you're ready to start developing with gmmff!
