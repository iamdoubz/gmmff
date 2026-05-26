// Command gmmff-web serves the gmmff browser UI for development.
//
// In production, the static files under web/static/ should be served by
// nginx or any static file host (S3, Cloudflare Pages, etc.).
// This server is only intended for local development and testing.
//
// Usage:
//
//	go run ./web --addr :9000
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	addr := flag.String("addr", ":9000", "address to listen on")
	static := flag.String("static", "", "path to static directory (default: web/static relative to module root)")
	flag.Parse()

	dir := *static
	if dir == "" {
		// Resolve relative to the module root — works from any working directory.
		_, file, _, _ := runtime.Caller(0)
		dir = filepath.Join(filepath.Dir(file), "static")
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Fatalf("static directory not found: %s", dir)
	}

	// Serve with correct MIME types, especially application/wasm.
	fs := http.FileServer(http.Dir(dir))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Set required headers for SharedArrayBuffer (needed by some Wasm runtimes).
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		fs.ServeHTTP(w, r)
	})

	fmt.Printf("gmmff web UI → http://localhost%s\n", *addr)
	fmt.Printf("Serving:      %s\n", dir)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}
