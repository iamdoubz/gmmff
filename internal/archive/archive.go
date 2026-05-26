// Package archive provides on-the-fly zip archiving for multi-file transfers.
//
// When gmmff send receives multiple paths (or a single directory), it calls
// ZipToTemp to produce a single temporary .zip file.  The caller sends that
// file normally through the transfer pipeline, then calls the returned cleanup
// function to remove the temp file.
//
// Single regular files are passed through unchanged — no zip overhead.
package archive

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Result describes the file that should be sent.
type Result struct {
	// Path is the file to send (either the original or a temp zip).
	Path string

	// Name is the display name — what the receiver will see.
	Name string

	// IsTemp is true when Path is a temporary file that must be removed
	// after the transfer by calling Cleanup.
	IsTemp bool
}

// Cleanup removes the temp file if one was created.  Always safe to call.
func (r Result) Cleanup() {
	if r.IsTemp {
		_ = os.Remove(r.Path)
	}
}

// Prepare decides whether to zip or pass through based on the given paths.
//
//   - Single regular file → returned as-is, no temp file created.
//   - Single directory    → zipped into a temp file named after the directory.
//   - Multiple paths      → zipped into a temp file named gmmff-<timestamp>.zip.
//
// Callers must call Result.Cleanup() when the transfer is done.
func Prepare(paths []string) (Result, error) {
	if len(paths) == 0 {
		return Result{}, fmt.Errorf("archive: no paths provided")
	}

	// Validate all paths exist up front.
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return Result{}, fmt.Errorf("archive: cannot access %q: %w", p, err)
		}
	}

	// Single regular file — pass through unchanged.
	if len(paths) == 1 {
		info, _ := os.Stat(paths[0])
		if !info.IsDir() {
			return Result{
				Path:   paths[0],
				Name:   filepath.Base(paths[0]),
				IsTemp: false,
			}, nil
		}
	}

	// One directory or multiple paths — zip them.
	return zipPaths(paths)
}

// zipPaths creates a temp zip containing all given paths and returns its location.
func zipPaths(paths []string) (Result, error) {
	// Choose a display name for the archive.
	var archiveName string
	if len(paths) == 1 {
		// Single directory — name after the directory.
		archiveName = filepath.Base(paths[0]) + ".zip"
	} else {
		archiveName = fmt.Sprintf("gmmff-%s.zip", time.Now().Format("20060102-150405"))
	}

	// Create temp file in the OS temp directory.
	tmp, err := os.CreateTemp("", "gmmff-*.zip")
	if err != nil {
		return Result{}, fmt.Errorf("archive: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if err := writeZip(tmp, paths); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return Result{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return Result{}, fmt.Errorf("archive: close temp file: %w", err)
	}

	return Result{
		Path:   tmpPath,
		Name:   archiveName,
		IsTemp: true,
	}, nil
}

// writeZip writes a zip archive containing all paths into w.
func writeZip(w io.Writer, paths []string) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("archive: stat %q: %w", root, err)
		}

		if info.IsDir() {
			if err := addDir(zw, root, filepath.Base(root)); err != nil {
				return err
			}
		} else {
			if err := addFile(zw, root, filepath.Base(root)); err != nil {
				return err
			}
		}
	}
	return nil
}

// addDir recursively adds a directory to the zip, preserving structure.
// prefix is the path inside the archive for this directory's contents.
func addDir(zw *zip.Writer, dir, prefix string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Compute the path inside the zip.
		rel, err := filepath.Rel(filepath.Dir(dir), path)
		if err != nil {
			return err
		}
		// Use forward slashes inside the zip regardless of OS.
		zipPath := strings.ReplaceAll(rel, string(filepath.Separator), "/")

		if info.IsDir() {
			// Directories need a trailing slash entry so empty dirs are preserved.
			if zipPath != "." {
				_, err = zw.Create(zipPath + "/")
			}
			return err
		}
		return addFile(zw, path, zipPath)
	})
}

// addFile adds a single file to the zip at the given path inside the archive.
func addFile(zw *zip.Writer, src, zipPath string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("archive: open %q: %w", src, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("archive: stat %q: %w", src, err)
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("archive: header %q: %w", src, err)
	}
	header.Name = zipPath
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("archive: create zip entry %q: %w", zipPath, err)
	}

	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("archive: write %q: %w", zipPath, err)
	}
	return nil
}

// Summary returns a human-readable description of what will be sent.
// Used to print a confirmation line before the transfer starts.
func Summary(paths []string) string {
	if len(paths) == 1 {
		info, err := os.Stat(paths[0])
		if err != nil {
			return paths[0]
		}
		if info.IsDir() {
			return fmt.Sprintf("directory %q", filepath.Base(paths[0]))
		}
		return fmt.Sprintf("%q (%.1f MB)", filepath.Base(paths[0]), float64(info.Size())/1024/1024)
	}
	return fmt.Sprintf("%d files", len(paths))
}

// ─────────────────────────────────────────────────────────────────────────────
// In-memory zip (browser Wasm — no filesystem)
// ─────────────────────────────────────────────────────────────────────────────

// NamedFile is an in-memory file with its path inside the archive.
type NamedFile struct {
	// ZipPath is the path the file will have inside the zip archive.
	// Use forward slashes; include subdirectory components to preserve structure.
	ZipPath string
	// Data is the raw file content.
	Data []byte
}

// ZipFilesFromMemory zips a slice of in-memory files into a []byte.
// If only one file is provided and it is not in a subdirectory, it is
// returned as-is (no zip wrapper) along with its bare filename.
// Returns (data, name, zipped) where zipped reports whether a zip was made.
func ZipFilesFromMemory(files []NamedFile) (data []byte, name string, err error) {
	if len(files) == 0 {
		return nil, "", fmt.Errorf("archive: no files provided")
	}

	// Single flat file — return as-is, no zip overhead.
	if len(files) == 1 && !strings.Contains(files[0].ZipPath, "/") {
		return files[0].Data, files[0].ZipPath, nil
	}

	// Multiple files or directory structure — zip them.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.ZipPath)
		if err != nil {
			return nil, "", fmt.Errorf("archive: create entry %q: %w", f.ZipPath, err)
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, "", fmt.Errorf("archive: write entry %q: %w", f.ZipPath, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("archive: close zip: %w", err)
	}

	// Name the archive after the top-level directory if all files share one,
	// otherwise use a timestamp.
	archiveName := zipArchiveName(files)
	return buf.Bytes(), archiveName, nil
}

// zipArchiveName picks a display name for the archive.
// If all entries share a common top-level prefix, use "<prefix>.zip".
// Otherwise use "gmmff-<timestamp>.zip".
func zipArchiveName(files []NamedFile) string {
	if len(files) == 0 {
		return "gmmff.zip"
	}
	// Find common top-level directory.
	first := strings.SplitN(files[0].ZipPath, "/", 2)[0]
	allSame := true
	for _, f := range files[1:] {
		top := strings.SplitN(f.ZipPath, "/", 2)[0]
		if top != first {
			allSame = false
			break
		}
	}
	if allSame && first != "" {
		return first + ".zip"
	}
	return fmt.Sprintf("gmmff-%s.zip", time.Now().Format("20060102-150405"))
}

// InjectMessage prepends a message.txt entry into a slice of NamedFiles.
// Used when the sender provides both files and a message.
func InjectMessage(files []NamedFile, message string) []NamedFile {
	msg := NamedFile{ZipPath: "message.txt", Data: []byte(message)}
	return append([]NamedFile{msg}, files...)
}
