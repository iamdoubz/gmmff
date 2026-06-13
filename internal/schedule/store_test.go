package schedule

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestStore creates a Store backed by a temporary directory that is
// automatically removed when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{
		Dir:          dir,
		PendingDir:   filepath.Join(dir, "pending"),
		CompleteDir:  filepath.Join(dir, "complete"),
		MaxSize:      1 << 30,
		MaxDownloads: 1,
		TTLOptions:   DefaultTTLOptions(),
	}
	st, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

// fakeChunk produces a deterministic byte slice that looks like an encrypted
// chunk — nonce (12 bytes) + payload + tag (16 bytes).
// Using deterministic data makes test failures easy to reproduce.
func fakeChunk(chunkIndex int, payloadSize int) []byte {
	chunk := make([]byte, NonceSize+payloadSize+TagSize)
	// Fill nonce with chunk index repeated.
	for i := range chunk[:NonceSize] {
		chunk[i] = byte(chunkIndex)
	}
	// Fill payload with recognisable pattern.
	for i := NonceSize; i < NonceSize+payloadSize; i++ {
		chunk[i] = byte(i)
	}
	// Leave tag as zeros — we're not doing real AES-GCM here.
	return chunk
}

// doFullUpload is a convenience helper that runs the complete upload lifecycle:
// InitUpload → N×AppendChunk → FinalizeUpload.
// Returns the completed FileMeta.
func doFullUpload(t *testing.T, st *Store, chunks int, chunkPayload int) *FileMeta {
	t.Helper()
	totalSize := int64(chunks * chunkPayload)
	expires := time.Now().Add(time.Hour)

	meta, err := st.InitUpload(chunks, totalSize, expires, 1, chunkPayload)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	for i := 0; i < chunks; i++ {
		data := fakeChunk(i, chunkPayload)
		if err := st.AppendChunk(meta.UploadID, i, data); err != nil {
			t.Fatalf("AppendChunk(%d): %v", i, err)
		}
	}

	fm, err := st.FinalizeUpload(meta.UploadID, "encname", "encnonce", "sha256hex")
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}
	return fm
}

// ─────────────────────────────────────────────────────────────────────────────
// NewStore
// ─────────────────────────────────────────────────────────────────────────────

func TestNewStore_CreatesDirs(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Dir:         dir,
		PendingDir:  filepath.Join(dir, "pending"),
		CompleteDir: filepath.Join(dir, "complete"),
	}
	_, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, sub := range []string{cfg.PendingDir, cfg.CompleteDir} {
		if _, err := os.Stat(sub); os.IsNotExist(err) {
			t.Errorf("directory not created: %s", sub)
		}
	}
}

func TestNewStore_IdempotentDirCreation(t *testing.T) {
	// Calling NewStore twice on the same path should not error.
	dir := t.TempDir()
	cfg := &Config{
		Dir:         dir,
		PendingDir:  filepath.Join(dir, "pending"),
		CompleteDir: filepath.Join(dir, "complete"),
	}
	if _, err := NewStore(cfg); err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	if _, err := NewStore(cfg); err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// InitUpload
// ─────────────────────────────────────────────────────────────────────────────

func TestInitUpload_CreatesFiles(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(3, 1024, time.Now().Add(time.Hour), 1, 256)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	// Upload ID must be non-empty hex.
	if meta.UploadID == "" {
		t.Error("UploadID should not be empty")
	}

	// Pending .enc file must exist and be empty.
	encPath := st.pendingEncPath(meta.UploadID)
	info, err := os.Stat(encPath)
	if err != nil {
		t.Fatalf("pending enc file not created: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("pending enc file should be empty, got size %d", info.Size())
	}

	// Pending .meta file must exist and be readable.
	loaded, err := st.ReadPendingMeta(meta.UploadID)
	if err != nil {
		t.Fatalf("ReadPendingMeta: %v", err)
	}
	if loaded.ChunksTotal != 3 {
		t.Errorf("ChunksTotal: got %d, want 3", loaded.ChunksTotal)
	}
	if loaded.ChunksWritten != 0 {
		t.Errorf("ChunksWritten should start at 0, got %d", loaded.ChunksWritten)
	}
}

func TestInitUpload_UniqueIDs(t *testing.T) {
	st := newTestStore(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		meta, err := st.InitUpload(1, 64, time.Now().Add(time.Hour), 1, 64)
		if err != nil {
			t.Fatalf("InitUpload %d: %v", i, err)
		}
		if seen[meta.UploadID] {
			t.Fatalf("duplicate upload ID after %d iterations: %s", i, meta.UploadID)
		}
		seen[meta.UploadID] = true
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AppendChunk
// ─────────────────────────────────────────────────────────────────────────────

func TestAppendChunk_Sequential(t *testing.T) {
	st := newTestStore(t)
	const chunks = 4
	const payload = 64

	meta, err := st.InitUpload(chunks, chunks*payload, time.Now().Add(time.Hour), 1, payload)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	for i := 0; i < chunks; i++ {
		if err := st.AppendChunk(meta.UploadID, i, fakeChunk(i, payload)); err != nil {
			t.Fatalf("AppendChunk(%d): %v", i, err)
		}
		// ChunksWritten should increment after each append.
		updated, err := st.ReadPendingMeta(meta.UploadID)
		if err != nil {
			t.Fatalf("ReadPendingMeta after chunk %d: %v", i, err)
		}
		if updated.ChunksWritten != i+1 {
			t.Errorf("after chunk %d: ChunksWritten = %d, want %d",
				i, updated.ChunksWritten, i+1)
		}
	}

	// .enc file must have grown to exactly chunks × encryptedChunkSize.
	encPath := st.pendingEncPath(meta.UploadID)
	info, err := os.Stat(encPath)
	if err != nil {
		t.Fatalf("stat enc file: %v", err)
	}
	want := int64(chunks * (NonceSize + payload + TagSize))
	if info.Size() != want {
		t.Errorf("enc file size: got %d, want %d", info.Size(), want)
	}
}

func TestAppendChunk_OutOfOrder_Rejected(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(3, 192, time.Now().Add(time.Hour), 1, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	// Sending chunk 1 before chunk 0 must fail.
	err = st.AppendChunk(meta.UploadID, 1, fakeChunk(1, 64))
	if err == nil {
		t.Error("AppendChunk with out-of-order index should return error, got nil")
	}
}

func TestAppendChunk_TooLarge_Rejected(t *testing.T) {
	st := newTestStore(t)
	const chunkPayload = 64
	meta, err := st.InitUpload(1, chunkPayload, time.Now().Add(time.Hour), 1, chunkPayload)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	// Chunk exceeds nonce + chunkPayload + tag.
	oversized := make([]byte, NonceSize+chunkPayload+TagSize+1)
	err = st.AppendChunk(meta.UploadID, 0, oversized)
	if err == nil {
		t.Error("AppendChunk with oversized chunk should return error, got nil")
	}
}

func TestAppendChunk_UnknownUploadID(t *testing.T) {
	st := newTestStore(t)
	err := st.AppendChunk("doesnotexist", 0, fakeChunk(0, 64))
	if err == nil {
		t.Error("AppendChunk with unknown upload ID should return error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// FinalizeUpload
// ─────────────────────────────────────────────────────────────────────────────

func TestFinalizeUpload_CompletesLifecycle(t *testing.T) {
	st := newTestStore(t)
	const chunks = 2
	const payload = 128

	fm := doFullUpload(t, st, chunks, payload)

	// FileID must be non-empty.
	if fm.FileID == "" {
		t.Error("FileID should not be empty")
	}

	// DeleteKey must be non-empty.
	if fm.DeleteKey == "" {
		t.Error("DeleteKey should not be empty")
	}

	// Complete .enc file must exist.
	if _, err := os.Stat(st.completeEncPath(fm.FileID)); err != nil {
		t.Errorf("complete enc file not found: %v", err)
	}

	// Pending files must be gone.
	pendingFiles, _ := os.ReadDir(st.cfg.PendingDir)
	if len(pendingFiles) != 0 {
		t.Errorf("pending dir should be empty after finalize, found %d files", len(pendingFiles))
	}

	// Complete .meta must be readable and fields correct.
	loaded, err := st.ReadFileMeta(fm.FileID)
	if err != nil {
		t.Fatalf("ReadFileMeta: %v", err)
	}
	if loaded.FileNameEnc != "encname" {
		t.Errorf("FileNameEnc: got %q, want %q", loaded.FileNameEnc, "encname")
	}
	if loaded.SHA256Cipher != "sha256hex" {
		t.Errorf("SHA256Cipher: got %q, want %q", loaded.SHA256Cipher, "sha256hex")
	}
	if loaded.ChunksTotal != chunks {
		t.Errorf("ChunksTotal: got %d, want %d", loaded.ChunksTotal, chunks)
	}
}

func TestFinalizeUpload_IncompleteRejected(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(3, 192, time.Now().Add(time.Hour), 1, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	// Only write 1 of 3 chunks.
	if err := st.AppendChunk(meta.UploadID, 0, fakeChunk(0, 64)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	_, err = st.FinalizeUpload(meta.UploadID, "enc", "nonce", "sha256")
	if err == nil {
		t.Error("FinalizeUpload with incomplete chunks should return error, got nil")
	}
}

func TestFinalizeUpload_MaxDownloads_Unlimited(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(1, 64, time.Now().Add(time.Hour), 0 /*unlimited*/, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	if err := st.AppendChunk(meta.UploadID, 0, fakeChunk(0, 64)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	fm, err := st.FinalizeUpload(meta.UploadID, "enc", "nonce", "sha256")
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}

	// MaxDownloads=0 means unlimited — DownloadsLeft should be -1.
	if fm.DownloadsLeft != -1 {
		t.Errorf("unlimited downloads: DownloadsLeft = %d, want -1", fm.DownloadsLeft)
	}
}

func TestFinalizeUpload_EncryptedSizeRecorded(t *testing.T) {
	st := newTestStore(t)
	const chunks = 3
	const payload = 64
	fm := doFullUpload(t, st, chunks, payload)

	wantSize := int64(chunks * (NonceSize + payload + TagSize))
	if fm.EncryptedSize != wantSize {
		t.Errorf("EncryptedSize: got %d, want %d", fm.EncryptedSize, wantSize)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenDownload
// ─────────────────────────────────────────────────────────────────────────────

func TestOpenDownload_ReturnsFileAndDecrements(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 2, 64)

	// MaxDownloads was 1, so DownloadsLeft should start at 1.
	if fm.DownloadsLeft != 1 {
		t.Fatalf("expected DownloadsLeft=1, got %d", fm.DownloadsLeft)
	}

	meta, f, err := st.OpenDownload(fm.FileID)
	if err != nil {
		t.Fatalf("OpenDownload: %v", err)
	}
	defer f.Close()

	if meta.DownloadsLeft != 0 {
		t.Errorf("DownloadsLeft after download: got %d, want 0", meta.DownloadsLeft)
	}

	// File must be readable and non-empty.
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(data) == 0 {
		t.Error("downloaded file is empty")
	}
}

func TestOpenDownload_LimitEnforced(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	// First download succeeds.
	_, f, err := st.OpenDownload(fm.FileID)
	if err != nil {
		t.Fatalf("first OpenDownload: %v", err)
	}
	f.Close()

	// Second download must fail — limit reached.
	_, _, err = st.OpenDownload(fm.FileID)
	if err == nil {
		t.Error("second OpenDownload should fail when limit reached, got nil")
	}
}

func TestOpenDownload_UnlimitedNeverExhausts(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(1, 64, time.Now().Add(time.Hour), 0 /*unlimited*/, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	if err := st.AppendChunk(meta.UploadID, 0, fakeChunk(0, 64)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	fm, err := st.FinalizeUpload(meta.UploadID, "enc", "nonce", "sha256")
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}

	// 5 downloads on an unlimited file must all succeed.
	for i := 0; i < 5; i++ {
		_, f, err := st.OpenDownload(fm.FileID)
		if err != nil {
			t.Fatalf("download %d of unlimited file: %v", i+1, err)
		}
		f.Close()
	}
}

func TestOpenDownload_ExpiredFileRejected(t *testing.T) {
	st := newTestStore(t)
	meta, err := st.InitUpload(1, 64, time.Now().Add(-time.Second) /*already expired*/, 1, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	if err := st.AppendChunk(meta.UploadID, 0, fakeChunk(0, 64)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	fm, err := st.FinalizeUpload(meta.UploadID, "enc", "nonce", "sha256")
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}

	_, _, err = st.OpenDownload(fm.FileID)
	if err == nil {
		t.Error("OpenDownload on expired file should fail, got nil")
	}
}

func TestOpenDownload_UnknownFileID(t *testing.T) {
	st := newTestStore(t)
	_, _, err := st.OpenDownload("doesnotexist")
	if err == nil {
		t.Error("OpenDownload with unknown fileID should return error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

func TestDelete_RemovesFiles(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	if err := st.Delete(fm.FileID, fm.DeleteKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Both .enc and .meta must be gone.
	if _, err := os.Stat(st.completeEncPath(fm.FileID)); !os.IsNotExist(err) {
		t.Error("enc file should be deleted")
	}
	if _, err := os.Stat(st.completeMetaPath(fm.FileID)); !os.IsNotExist(err) {
		t.Error("meta file should be deleted")
	}
}

func TestDelete_WrongKey_Rejected(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	err := st.Delete(fm.FileID, "wrongkey")
	if err == nil {
		t.Error("Delete with wrong key should return error, got nil")
	}

	// Files must still exist.
	if _, err := os.Stat(st.completeEncPath(fm.FileID)); err != nil {
		t.Error("enc file should still exist after failed delete")
	}
}

func TestDelete_UnknownFileID(t *testing.T) {
	st := newTestStore(t)
	err := st.Delete("doesnotexist", "anykey")
	if err == nil {
		t.Error("Delete with unknown fileID should return error, got nil")
	}
}

func TestDelete_IdempotentAfterDownloadExhaustion(t *testing.T) {
	// A file that has been downloaded to exhaustion should still be deletable
	// by the uploader (they have the delete key).
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	// Download once to exhaust.
	_, f, err := st.OpenDownload(fm.FileID)
	if err != nil {
		t.Fatalf("OpenDownload: %v", err)
	}
	f.Close()

	// Delete should still work.
	if err := st.Delete(fm.FileID, fm.DeleteKey); err != nil {
		t.Fatalf("Delete after exhaustion: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CleanExpired
// ─────────────────────────────────────────────────────────────────────────────

func TestCleanExpired_RemovesExpiredFiles(t *testing.T) {
	st := newTestStore(t)

	// Upload a file that expired 1 second ago.
	meta, err := st.InitUpload(1, 64, time.Now().Add(-time.Second), 1, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	if err := st.AppendChunk(meta.UploadID, 0, fakeChunk(0, 64)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	fm, err := st.FinalizeUpload(meta.UploadID, "enc", "nonce", "sha256")
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}

	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("CleanExpired: removed %d, want 1", removed)
	}

	// File must be gone.
	if _, err := os.Stat(st.completeEncPath(fm.FileID)); !os.IsNotExist(err) {
		t.Error("expired enc file should have been removed")
	}
}

func TestCleanExpired_PreservesActiveFiles(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired: %v", err)
	}
	if removed != 0 {
		t.Errorf("CleanExpired: removed %d active files, want 0", removed)
	}

	// File must still exist.
	if _, err := os.Stat(st.completeEncPath(fm.FileID)); err != nil {
		t.Errorf("active file should not have been removed: %v", err)
	}
}

func TestCleanExpired_RemovesExhaustedFiles(t *testing.T) {
	st := newTestStore(t)
	fm := doFullUpload(t, st, 1, 64)

	// Download once — this sets DownloadsLeft to 0.
	_, f, err := st.OpenDownload(fm.FileID)
	if err != nil {
		t.Fatalf("OpenDownload: %v", err)
	}
	f.Close()

	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("CleanExpired: removed %d, want 1 (exhausted file)", removed)
	}
}

func TestCleanExpired_RemovesStaleInProgress(t *testing.T) {
	st := newTestStore(t)

	// Create a pending upload with a CreatedAt 25 hours ago.
	meta, err := st.InitUpload(3, 192, time.Now().Add(time.Hour), 1, 64)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}

	// Backdate the CreatedAt in the meta file to simulate a stale upload.
	m, err := st.ReadPendingMeta(meta.UploadID)
	if err != nil {
		t.Fatalf("ReadPendingMeta: %v", err)
	}
	m.CreatedAt = time.Now().Add(-25 * time.Hour)
	if err := st.writePendingMeta(m); err != nil {
		t.Fatalf("writePendingMeta: %v", err)
	}

	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("CleanExpired: removed %d, want 1 (stale pending)", removed)
	}

	// Pending files must be gone.
	if _, err := os.Stat(st.pendingEncPath(meta.UploadID)); !os.IsNotExist(err) {
		t.Error("stale pending enc file should have been removed")
	}
	if _, err := os.Stat(st.pendingMetaPath(meta.UploadID)); !os.IsNotExist(err) {
		t.Error("stale pending meta file should have been removed")
	}
}

func TestCleanExpired_EmptyStore_NoPanic(t *testing.T) {
	st := newTestStore(t)
	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired on empty store: %v", err)
	}
	if removed != 0 {
		t.Errorf("empty store: removed %d, want 0", removed)
	}
}

func TestCleanExpired_MixedFiles(t *testing.T) {
	st := newTestStore(t)

	// Upload 3 files: 1 expired, 1 exhausted, 1 active.
	// Expired.
	expMeta, _ := st.InitUpload(1, 64, time.Now().Add(-time.Second), 1, 64)
	st.AppendChunk(expMeta.UploadID, 0, fakeChunk(0, 64)) //nolint:errcheck
	st.FinalizeUpload(expMeta.UploadID, "e", "n", "s")    //nolint:errcheck

	// Exhausted (will download once to exhaust).
	exhFM := doFullUpload(t, st, 1, 64)
	_, f, _ := st.OpenDownload(exhFM.FileID)
	if f != nil {
		f.Close()
	}

	// Active.
	activeFM := doFullUpload(t, st, 1, 64)

	removed, err := st.CleanExpired()
	if err != nil {
		t.Fatalf("CleanExpired: %v", err)
	}
	if removed != 2 {
		t.Errorf("CleanExpired: removed %d, want 2", removed)
	}

	// Active file must survive.
	if _, err := os.Stat(st.completeEncPath(activeFM.FileID)); err != nil {
		t.Error("active file should not have been removed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// moveFile / copyFileContents — cross-device-safe move used by FinalizeUpload
// ─────────────────────────────────────────────────────────────────────────────

func TestMoveFile_MovesAndRemovesOriginal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "sub", "dst.bin")
	want := []byte("encrypted-payload-bytes")

	if err := os.WriteFile(src, want, 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("dst contents: got %q, want %q", got, want)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source should be gone after moveFile")
	}
}

func TestCopyFileContents_ByteIdentical(t *testing.T) {
	// copyFileContents is the cross-device fallback path in moveFile; verify it
	// reproduces the source bytes exactly.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	want := make([]byte, 4096)
	for i := range want {
		want[i] = byte(i % 251)
	}
	if err := os.WriteFile(src, want, 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyFileContents(src, dst); err != nil {
		t.Fatalf("copyFileContents: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("dst size: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d differs: got %d, want %d", i, got[i], want[i])
		}
	}
}
