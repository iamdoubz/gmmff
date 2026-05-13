//go:build !js

// Package localmode provides the self-contained local-network mode for gmmff.
// This file embeds the web/static directory at compile time so gmmff local
// can serve the browser UI without any external files.
//
// IMPORTANT: web/static/gmmff.wasm must exist before building.
// Run `make wasm` first, then `make build`.
package localmode

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var embeddedStatic embed.FS

// StaticFS returns the embedded web/static directory as an fs.FS.
// It is used by broker.NewServerWithFS to serve the browser UI.
func StaticFS() (fs.FS, error) {
	return fs.Sub(embeddedStatic, "static")
}
