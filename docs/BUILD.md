# Building

The `gmmff` binary is an all-in-one file. From this file, you can start the signalling server or start the CLI client. In order to build it, there is one mandatory and one optional requirement.

## Prerequisites

### Mandatory

- golang >= 1.23 (1.26 recommended) [Download here](https://go.dev/)
- git (For Windows users I use [this](https://git-scm.com/install/windows))
- redis-server (For production environments)

### Optional

- make

## Clone the repo

```
git clone https://github.com/iamdoubz/gmmff
cd gmmff
```

## Signalling server

If you plan on using the WASM webclient, run `make wasm` and copy the generated contents found in `./web/static` to your root website directory.

Then, build the binary with `make build`.

## CLI client

Although the recommended route is to use the latest builds, if your OS/platform isn't automatically built by default, you will have to compile it manually.

Simply type `make build`. If you do not have make for your system, either install it or build it with go only:

### Windows

```
go build -ldflags="-s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/gmmff.exe ./cmd/gmmff
```

### Non-windows

```
go build -ldflags="-s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/gmmff ./cmd/gmmff
```

## Next steps

- Read the documentation for how to use the [CLI client here](CLI.md)
- Read how to start `gmmff` signalling server at boot using [systemd](SYSTEMD.md)
- Read how to setup a [reverse proxy](NGINX.md) for your signalling server