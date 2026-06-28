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
// WriteZip — directory structure preserved (nested), via a streamed buffer
// ─────────────────────────────────────────────────────────────────────────────

func TestWriteZip_NestedDirectory_StructurePreserved(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "myapp")
	subDir := filepath.Join(srcDir, "lib")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"myapp/main.go":     []byte("package main"),
		"myapp/lib/util.go": []byte("package lib"),
		"myapp/lib/math.go": []byte("package lib // math"),
	}
	for rel, content := range want {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), content, 0o644); err != nil {
			t.Fatalf("setup %q: %v", rel, err)
		}
	}

	var buf bytes.Buffer
	if err := WriteZip(&buf, []string{srcDir}); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}

	got := extractZipBytes(t, buf.Bytes())
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

func TestWriteZip_MultipleFiles_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string][]byte{
		"one.txt": []byte("one"),
		"two.bin": {0xDE, 0xAD, 0xBE, 0xEF},
	}
	paths := make([]string, 0, len(want))
	for name, content := range want {
		paths = append(paths, writeTempFile(t, dir, name, content))
	}

	var buf bytes.Buffer
	if err := WriteZip(&buf, paths); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}

	got := extractZipBytes(t, buf.Bytes())
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

// ─────────────────────────────────────────────────────────────────────────────
// Name — archive display name
// ─────────────────────────────────────────────────────────────────────────────

func TestName_SingleDirectory_NamedAfterDir(t *testing.T) {
	if got := Name([]string{filepath.Join("some", "sendme")}); got != "sendme.zip" {
		t.Errorf("Name: got %q, want %q", got, "sendme.zip")
	}
}

func TestName_MultipleFiles_Timestamped(t *testing.T) {
	got := Name([]string{"a.txt", "b.txt"})
	if !strings.HasPrefix(got, "gmmff-") || !strings.HasSuffix(got, ".zip") {
		t.Errorf("multi-file archive name should be gmmff-<ts>.zip, got %q", got)
	}
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
