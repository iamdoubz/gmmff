// Package archive provides on-the-fly zip archiving for multi-file transfers.
//
// When gmmff send receives multiple paths (or a single directory), the caller
// streams a zip straight onto the wire with WriteZip (see transfer.RunFromStream)
// — no temp file is staged on disk. Name supplies the display name shown to the
// receiver. Single regular files are sent unchanged, with no zip overhead.
//
// ZipFilesFromMemory serves the browser Wasm client, which has no filesystem.
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

// Name returns the display name for an archive of the given paths: "<dir>.zip"
// for a single directory, else a timestamped "gmmff-<ts>.zip".
func Name(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0]) + ".zip"
	}
	return fmt.Sprintf("gmmff-%s.zip", time.Now().Format("20060102-150405"))
}

// WriteZip streams a zip of all given paths into w. Use this to send a
// multi-file archive without staging it on disk first (see transfer.RunFromStream).
func WriteZip(w io.Writer, paths []string) error { return writeZip(w, paths) }

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
