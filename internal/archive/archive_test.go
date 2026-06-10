package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// extractZipFile opens a zip on disk and returns a map of entry path → content.
// Directory entries are skipped.
func extractZipFile(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %q: %v", path, err)
	}
	defer r.Close()
	return readZipEntries(t, r.File)
}

// extractZipBytes opens a zip from a byte slice and returns a map of entry path → content.
func extractZipBytes(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parse zip bytes: %v", err)
	}
	return readZipEntries(t, r.File)
}

func readZipEntries(t *testing.T, files []*zip.File) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %q: %v", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read zip entry %q: %v", f.Name, err)
		}
		out[f.Name] = content
	}
	return out
}

// writeTempFile creates a file at dir/name with the given content.
func writeTempFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}

// zipEntryNames returns the keys of a map as a slice (for diagnostic messages).
func zipEntryNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}

// ─────────────────────────────────────────────────────────────────────────────
// Prepare — no paths
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_NoPaths_Error(t *testing.T) {
	if _, err := Prepare(nil); err == nil {
		t.Error("Prepare(nil) should return error")
	}
	if _, err := Prepare([]string{}); err == nil {
		t.Error("Prepare([]) should return error")
	}
}

func TestPrepare_NonexistentPath_Error(t *testing.T) {
	if _, err := Prepare([]string{"/does/not/exist/ever.txt"}); err == nil {
		t.Error("Prepare with nonexistent path should return error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prepare — single regular file passes through unchanged
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_SingleFile_PassThrough(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	path := writeTempFile(t, dir, "report.txt", content)

	result, err := Prepare([]string{path})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	if result.IsTemp {
		t.Error("single regular file should not create a temp archive")
	}
	if result.Path != path {
		t.Errorf("Path: got %q, want %q", result.Path, path)
	}
	if result.Name != "report.txt" {
		t.Errorf("Name: got %q, want %q", result.Name, "report.txt")
	}
}

func TestPrepare_SingleFile_CleanupDoesNotRemoveOriginal(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "keep.txt", []byte("keep me"))

	result, err := Prepare([]string{path})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	result.Cleanup()

	if _, err := os.Stat(path); err != nil {
		t.Error("Cleanup on a pass-through result must not remove the original file")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prepare — single directory is zipped and named after the directory
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_SingleDirectory_NamedAfterDir(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "sendme")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, srcDir, "x.bin", []byte{0x01})

	result, err := Prepare([]string{srcDir})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	if !result.IsTemp {
		t.Error("single directory should produce a temp zip")
	}
	if result.Name != "sendme.zip" {
		t.Errorf("Name: got %q, want %q", result.Name, "sendme.zip")
	}
}

func TestPrepare_SingleDirectory_RoundTrip(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "project")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"project/alpha.txt": []byte("alpha content"),
		"project/beta.bin":  {0xCA, 0xFE, 0xBA, 0xBE},
	}
	for relPath, content := range want {
		dest := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			t.Fatalf("setup %q: %v", dest, err)
		}
	}

	result, err := Prepare([]string{srcDir})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	got := extractZipFile(t, result.Path)
	for zipPath, wantData := range want {
		gotData, ok := got[zipPath]
		if !ok {
			t.Errorf("zip missing %q (entries: %v)", zipPath, zipEntryNames(got))
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("entry %q: content mismatch", zipPath)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prepare — nested directory structure is preserved
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_NestedDirectory_StructurePreserved(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "myapp")
	subDir := filepath.Join(srcDir, "lib")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"myapp/main.go":      []byte("package main"),
		"myapp/lib/util.go":  []byte("package lib"),
		"myapp/lib/math.go":  []byte("package lib // math"),
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), want["myapp/main.go"], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "util.go"), want["myapp/lib/util.go"], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "math.go"), want["myapp/lib/math.go"], 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Prepare([]string{srcDir})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	got := extractZipFile(t, result.Path)
	for zipPath, wantData := range want {
		gotData, ok := got[zipPath]
		if !ok {
			t.Errorf("zip missing nested entry %q (entries: %v)", zipPath, zipEntryNames(got))
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("nested entry %q: content mismatch", zipPath)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prepare — multiple files → timestamped zip name
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_MultipleFiles_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string][]byte{
		"one.txt": []byte("one"),
		"two.bin": {0xDE, 0xAD, 0xBE, 0xEF},
	}
	paths := make([]string, 0, len(want))
	for name, content := range want {
		paths = append(paths, writeTempFile(t, dir, name, content))
	}

	result, err := Prepare(paths)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	if !result.IsTemp {
		t.Error("multiple files should produce a temp zip")
	}

	got := extractZipFile(t, result.Path)
	for name, wantData := range want {
		gotData, ok := got[name]
		if !ok {
			t.Errorf("zip missing %q (entries: %v)", name, zipEntryNames(got))
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("entry %q: content mismatch", name)
		}
	}
}

func TestPrepare_MultipleFiles_TimestampedName(t *testing.T) {
	dir := t.TempDir()
	p1 := writeTempFile(t, dir, "a.txt", []byte("a"))
	p2 := writeTempFile(t, dir, "b.txt", []byte("b"))

	result, err := Prepare([]string{p1, p2})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer result.Cleanup()

	if !strings.HasPrefix(result.Name, "gmmff-") {
		t.Errorf("multi-file archive name should start with gmmff-, got %q", result.Name)
	}
	if !strings.HasSuffix(result.Name, ".zip") {
		t.Errorf("multi-file archive name should end with .zip, got %q", result.Name)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Result.Cleanup
// ─────────────────────────────────────────────────────────────────────────────

func TestResult_Cleanup_RemovesTempZip(t *testing.T) {
	dir := t.TempDir()
	p1 := writeTempFile(t, dir, "x.txt", []byte("x"))
	p2 := writeTempFile(t, dir, "y.txt", []byte("y"))

	result, err := Prepare([]string{p1, p2})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	tmpPath := result.Path
	result.Cleanup()

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("Cleanup should remove the temp zip")
	}
}

func TestResult_Cleanup_SafeToCallTwice(t *testing.T) {
	dir := t.TempDir()
	p1 := writeTempFile(t, dir, "a.txt", []byte("a"))
	p2 := writeTempFile(t, dir, "b.txt", []byte("b"))

	result, err := Prepare([]string{p1, p2})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	result.Cleanup()
	result.Cleanup() // second call must not panic
}

// ─────────────────────────────────────────────────────────────────────────────
// ZipFilesFromMemory
// ─────────────────────────────────────────────────────────────────────────────

func TestZipFilesFromMemory_Empty_Error(t *testing.T) {
	if _, _, err := ZipFilesFromMemory(nil); err == nil {
		t.Error("ZipFilesFromMemory(nil) should return error")
	}
	if _, _, err := ZipFilesFromMemory([]NamedFile{}); err == nil {
		t.Error("ZipFilesFromMemory([]) should return error")
	}
}

func TestZipFilesFromMemory_SingleFlatFile_PassThrough(t *testing.T) {
	content := []byte("hello from memory")
	files := []NamedFile{{ZipPath: "readme.txt", Data: content}}

	data, name, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}
	if name != "readme.txt" {
		t.Errorf("name: got %q, want readme.txt", name)
	}
	if !bytes.Equal(data, content) {
		t.Error("single flat file should be returned as-is without zip wrapping")
	}
}

func TestZipFilesFromMemory_SingleNestedFile_Zipped(t *testing.T) {
	// A "/" in the path means the file is inside a directory — must be zipped.
	content := []byte("nested content")
	files := []NamedFile{{ZipPath: "subdir/nested.txt", Data: content}}

	data, _, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}

	got := extractZipBytes(t, data)
	gotData, ok := got["subdir/nested.txt"]
	if !ok {
		t.Fatalf("zip missing entry subdir/nested.txt (entries: %v)", zipEntryNames(got))
	}
	if !bytes.Equal(gotData, content) {
		t.Error("nested entry content mismatch")
	}
}

func TestZipFilesFromMemory_MultipleFiles_CommonPrefix_Name(t *testing.T) {
	files := []NamedFile{
		{ZipPath: "docs/a.txt", Data: []byte("alpha")},
		{ZipPath: "docs/b.txt", Data: []byte("beta")},
		{ZipPath: "docs/sub/c.txt", Data: []byte("gamma")},
	}

	_, name, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}
	if name != "docs.zip" {
		t.Errorf("name: got %q, want docs.zip", name)
	}
}

func TestZipFilesFromMemory_MultipleFiles_NoCommonPrefix_TimestampedName(t *testing.T) {
	files := []NamedFile{
		{ZipPath: "alpha/a.txt", Data: []byte("a")},
		{ZipPath: "beta/b.txt", Data: []byte("b")},
	}

	_, name, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}
	if !strings.HasPrefix(name, "gmmff-") || !strings.HasSuffix(name, ".zip") {
		t.Errorf("mixed-prefix archive name should be timestamped gmmff-*.zip, got %q", name)
	}
}

func TestZipFilesFromMemory_RoundTrip_ByteIdentical(t *testing.T) {
	want := map[string][]byte{
		"pkg/a.txt":     []byte("alpha"),
		"pkg/b.bin":     {0xDE, 0xAD, 0xBE, 0xEF},
		"pkg/sub/c.txt": []byte("deep"),
	}
	files := []NamedFile{
		{ZipPath: "pkg/a.txt", Data: want["pkg/a.txt"]},
		{ZipPath: "pkg/b.bin", Data: want["pkg/b.bin"]},
		{ZipPath: "pkg/sub/c.txt", Data: want["pkg/sub/c.txt"]},
	}

	data, _, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}

	got := extractZipBytes(t, data)
	for zipPath, wantData := range want {
		gotData, ok := got[zipPath]
		if !ok {
			t.Errorf("zip missing %q (entries: %v)", zipPath, zipEntryNames(got))
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("entry %q: content mismatch", zipPath)
		}
	}
}

func TestZipFilesFromMemory_LargePayload_Intact(t *testing.T) {
	// Verify the deflate pipeline doesn't corrupt a non-trivial payload.
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	files := []NamedFile{
		{ZipPath: "dir/large.bin", Data: payload},
	}

	data, _, err := ZipFilesFromMemory(files)
	if err != nil {
		t.Fatalf("ZipFilesFromMemory: %v", err)
	}

	got := extractZipBytes(t, data)
	gotData, ok := got["dir/large.bin"]
	if !ok {
		t.Fatal("zip missing dir/large.bin")
	}
	if !bytes.Equal(gotData, payload) {
		t.Error("large payload round-trip: content mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// InjectMessage
// ─────────────────────────────────────────────────────────────────────────────

func TestInjectMessage_PrependedAsFirstEntry(t *testing.T) {
	files := []NamedFile{
		{ZipPath: "file.txt", Data: []byte("content")},
	}
	result := InjectMessage(files, "hello there")

	if len(result) != 2 {
		t.Fatalf("expected 2 files after inject, got %d", len(result))
	}
	if result[0].ZipPath != "message.txt" {
		t.Errorf("first entry should be message.txt, got %q", result[0].ZipPath)
	}
	if string(result[0].Data) != "hello there" {
		t.Errorf("message content: got %q, want %q", string(result[0].Data), "hello there")
	}
	if result[1].ZipPath != "file.txt" {
		t.Errorf("original file should be second, got %q", result[1].ZipPath)
	}
}

func TestInjectMessage_DoesNotMutateOriginalSlice(t *testing.T) {
	original := []NamedFile{
		{ZipPath: "a.txt", Data: []byte("a")},
	}
	_ = InjectMessage(original, "msg")

	if len(original) != 1 {
		t.Error("InjectMessage must not mutate the original slice")
	}
}

func TestInjectMessage_EmptyMessage(t *testing.T) {
	files := []NamedFile{{ZipPath: "f.txt", Data: []byte("f")}}
	result := InjectMessage(files, "")
	if string(result[0].Data) != "" {
		t.Errorf("empty message should produce empty message.txt data")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Summary
// ─────────────────────────────────────────────────────────────────────────────

func TestSummary_SingleFile_ContainsName(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "report.pdf", make([]byte, 512*1024))

	s := Summary([]string{path})
	if !strings.Contains(s, "report.pdf") {
		t.Errorf("Summary for single file should contain filename, got %q", s)
	}
}

func TestSummary_SingleDirectory_ContainsDirName(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "myproject")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s := Summary([]string{srcDir})
	if !strings.Contains(s, "myproject") {
		t.Errorf("Summary for directory should contain dir name, got %q", s)
	}
	if !strings.Contains(s, "directory") {
		t.Errorf("Summary for directory should say 'directory', got %q", s)
	}
}

func TestSummary_MultipleFiles_ContainsCount(t *testing.T) {
	dir := t.TempDir()
	p1 := writeTempFile(t, dir, "a.txt", []byte("a"))
	p2 := writeTempFile(t, dir, "b.txt", []byte("b"))
	p3 := writeTempFile(t, dir, "c.txt", []byte("c"))

	s := Summary([]string{p1, p2, p3})
	if !strings.Contains(s, "3") {
		t.Errorf("Summary for 3 files should mention count, got %q", s)
	}
}

func TestSummary_NonexistentPath_NoPanic(t *testing.T) {
	s := Summary([]string{"/no/such/file.xyz"})
	if s == "" {
		t.Error("Summary for nonexistent path should return non-empty string, not panic")
	}
}
