package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// UploadMeta is stored as a JSON sidecar during a pending upload.
type UploadMeta struct {
	UploadID      string    `json:"upload_id"`
	ChunksTotal   int       `json:"chunks_total"`
	ChunkSize     int       `json:"chunk_size"` // bytes per plaintext chunk
	ChunksWritten int       `json:"chunks_written"`
	TotalSize     int64     `json:"total_size"` // plaintext bytes
	ExpiresAt     time.Time `json:"expires_at"`
	MaxDownloads  int       `json:"max_downloads"` // 0 = unlimited
	CreatedAt     time.Time `json:"created_at"`
}

// FileMeta is the permanent sidecar for a completed upload.
type FileMeta struct {
	FileID        string    `json:"file_id"`
	FileNameEnc   string    `json:"filename_enc"`   // hex-encoded encrypted filename
	FileNameNonce string    `json:"filename_nonce"` // hex-encoded 12-byte nonce
	TotalSize     int64     `json:"total_size"`     // plaintext bytes
	EncryptedSize int64     `json:"encrypted_size"` // ciphertext bytes on disk
	SHA256Cipher  string    `json:"sha256_cipher"`  // hex SHA-256 of full ciphertext
	ChunksTotal   int       `json:"chunks_total"`
	ChunkSize     int       `json:"chunk_size"`
	ExpiresAt     time.Time `json:"expires_at"`
	MaxDownloads  int       `json:"max_downloads"`
	DownloadsLeft int       `json:"downloads_left"` // -1 = unlimited
	DeleteKey     string    `json:"delete_key"`
	CreatedAt     time.Time `json:"created_at"`
}

const (
	// ChunkSize is the plaintext bytes per chunk. 256 KiB balances encryption
	// latency on mobile CPUs against HTTP round-trip overhead. The old 2 MiB
	// value caused ~50-150ms stalls per chunk on mid-range phones.
	ChunkSize = 256 * 1024 // 256 KiB

	// NonceSize is the AES-GCM nonce length in bytes.
	NonceSize = 12

	// TagSize is the AES-GCM authentication tag length in bytes.
	TagSize = 16

	// EncryptedChunkSize is the on-disk size of each encrypted chunk.
	// = nonce (12) + ciphertext (ChunkSize) + GCM tag (16)
	EncryptedChunkSize = NonceSize + ChunkSize + TagSize
)

// Store provides file system operations for the schedule feature.
type Store struct {
	cfg *Config
}

// NewStore creates a Store and ensures the directory layout exists.
func NewStore(cfg *Config) (*Store, error) {
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	return &Store{cfg: cfg}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload lifecycle
// ─────────────────────────────────────────────────────────────────────────────

// InitUpload creates a pending slot and returns the upload ID.
func (s *Store) InitUpload(chunksTotal int, totalSize int64, expires time.Time, maxDownloads int, chunkSize int) (*UploadMeta, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	meta := &UploadMeta{
		UploadID:     id,
		ChunksTotal:  chunksTotal,
		ChunkSize:    chunkSize,
		TotalSize:    totalSize,
		ExpiresAt:    expires,
		MaxDownloads: maxDownloads,
		CreatedAt:    time.Now().UTC(),
	}
	// Give this upload its own directory so its files never collide with others.
	if err := os.MkdirAll(s.pendingDir(id), 0o750); err != nil {
		return nil, fmt.Errorf("schedule: create pending dir: %w", err)
	}
	if err := s.writePendingMeta(meta); err != nil {
		return nil, err
	}
	// Create the empty .enc file.
	f, err := os.Create(s.pendingEncPath(id))
	if err != nil {
		return nil, fmt.Errorf("schedule: create pending enc: %w", err)
	}
	f.Close()
	return meta, nil
}

// AppendChunk appends one encrypted chunk to the pending file and
// increments the written counter.  Returns an error if the upload ID is
// unknown, the chunk index is out of order, or the chunk is too large.
func (s *Store) AppendChunk(uploadID string, chunkIndex int, data []byte) error {
	meta, err := s.ReadPendingMeta(uploadID)
	if err != nil {
		return err
	}
	if chunkIndex != meta.ChunksWritten {
		return fmt.Errorf("schedule: expected chunk %d, got %d", meta.ChunksWritten, chunkIndex)
	}
	// Each chunk = nonce(12) + ciphertext(up to meta.ChunkSize) + tag(16)
	maxChunkBytes := NonceSize + meta.ChunkSize + TagSize
	if len(data) > maxChunkBytes {
		return fmt.Errorf("schedule: chunk too large: %d > %d", len(data), maxChunkBytes)
	}
	f, err := os.OpenFile(s.pendingEncPath(uploadID), os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("schedule: open pending enc: %w", err)
	}
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return fmt.Errorf("schedule: write chunk: %w", err)
	}
	meta.ChunksWritten++
	return s.writePendingMeta(meta)
}

// FinalizeUpload moves the pending files to the complete directory and
// writes the permanent FileMeta sidecar.  Returns the permanent FileID.
func (s *Store) FinalizeUpload(uploadID string, fileNameEnc, fileNameNonce, sha256Cipher string) (*FileMeta, error) {
	meta, err := s.ReadPendingMeta(uploadID)
	if err != nil {
		return nil, err
	}
	if meta.ChunksWritten != meta.ChunksTotal {
		return nil, fmt.Errorf("schedule: incomplete upload: %d/%d chunks", meta.ChunksWritten, meta.ChunksTotal)
	}

	fileID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	deleteKey, err := randomHex(8)
	if err != nil {
		return nil, err
	}

	// Measure encrypted size on disk.
	info, err := os.Stat(s.pendingEncPath(uploadID))
	if err != nil {
		return nil, fmt.Errorf("schedule: stat pending enc: %w", err)
	}

	downloadsLeft := meta.MaxDownloads
	if meta.MaxDownloads == 0 {
		downloadsLeft = -1 // unlimited
	}

	fm := &FileMeta{
		FileID:        fileID,
		FileNameEnc:   fileNameEnc,
		FileNameNonce: fileNameNonce,
		TotalSize:     meta.TotalSize,
		EncryptedSize: info.Size(),
		SHA256Cipher:  sha256Cipher,
		ChunksTotal:   meta.ChunksTotal,
		ChunkSize:     meta.ChunkSize,
		ExpiresAt:     meta.ExpiresAt,
		MaxDownloads:  meta.MaxDownloads,
		DownloadsLeft: downloadsLeft,
		DeleteKey:     deleteKey,
		CreatedAt:     meta.CreatedAt,
	}

	// Give the completed file its own directory, then move the enc file into it.
	if err := os.MkdirAll(s.completeDir(fileID), 0o750); err != nil {
		return nil, fmt.Errorf("schedule: create complete dir: %w", err)
	}
	if err := moveFile(s.pendingEncPath(uploadID), s.completeEncPath(fileID)); err != nil {
		return nil, fmt.Errorf("schedule: move enc: %w", err)
	}
	// Write final meta and remove the entire pending directory for this upload.
	if err := s.writeFileMeta(fm); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(s.pendingDir(uploadID))

	return fm, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download
// ─────────────────────────────────────────────────────────────────────────────

// OpenDownload opens the ciphertext file for reading and decrements the
// download counter.  Returns the FileMeta and an open *os.File.
// Caller must close the file.
func (s *Store) OpenDownload(fileID string) (*FileMeta, *os.File, error) {
	meta, err := s.ReadFileMeta(fileID)
	if err != nil {
		return nil, nil, err
	}
	if time.Now().After(meta.ExpiresAt) {
		return nil, nil, fmt.Errorf("schedule: file expired")
	}
	if meta.DownloadsLeft == 0 {
		return nil, nil, fmt.Errorf("schedule: download limit reached")
	}

	f, err := os.Open(s.completeEncPath(fileID))
	if err != nil {
		return nil, nil, fmt.Errorf("schedule: open enc: %w", err)
	}

	// Decrement download counter.
	if meta.DownloadsLeft > 0 {
		meta.DownloadsLeft--
		if err := s.writeFileMeta(meta); err != nil {
			f.Close()
			return nil, nil, err
		}
	}

	return meta, f, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

// Delete removes the files for the given fileID if the deleteKey matches.
func (s *Store) Delete(fileID, deleteKey string) error {
	meta, err := s.ReadFileMeta(fileID)
	if err != nil {
		return err
	}
	if meta.DeleteKey != deleteKey {
		return fmt.Errorf("schedule: invalid delete key")
	}
	_ = os.RemoveAll(s.completeDir(fileID))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Cleanup
// ─────────────────────────────────────────────────────────────────────────────

// CleanExpired removes all expired complete files and stale pending uploads.
// A pending upload is considered stale if it was created more than 24 hours ago.
// Returns the number of files removed.
func (s *Store) CleanExpired() (int, error) {
	removed := 0
	now := time.Now()

	// Clean complete uploads. Each lives in its own directory named by file ID;
	// an expired or download-exhausted upload has its whole directory removed.
	entries, _ := os.ReadDir(s.cfg.CompleteDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fileID := e.Name()
		meta, err := s.ReadFileMeta(fileID)
		if err != nil {
			continue
		}
		expired := now.After(meta.ExpiresAt)
		exhausted := meta.DownloadsLeft == 0
		if expired || exhausted {
			_ = os.RemoveAll(s.completeDir(fileID))
			removed++
		}
	}

	// Clean stale pending uploads (older than 24h). Each is its own directory.
	staleThreshold := now.Add(-24 * time.Hour)
	pendingEntries, _ := os.ReadDir(s.cfg.PendingDir)
	for _, e := range pendingEntries {
		if !e.IsDir() {
			continue
		}
		uploadID := e.Name()
		meta, err := s.ReadPendingMeta(uploadID)
		if err != nil {
			continue
		}
		if meta.CreatedAt.Before(staleThreshold) {
			_ = os.RemoveAll(s.pendingDir(uploadID))
			removed++
		}
	}

	return removed, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Meta I/O
// ─────────────────────────────────────────────────────────────────────────────

func (s *Store) ReadPendingMeta(uploadID string) (*UploadMeta, error) {
	data, err := os.ReadFile(s.pendingMetaPath(uploadID))
	if err != nil {
		return nil, fmt.Errorf("schedule: read pending meta: %w", err)
	}
	var m UploadMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("schedule: decode pending meta: %w", err)
	}
	return &m, nil
}

func (s *Store) ReadFileMeta(fileID string) (*FileMeta, error) {
	data, err := os.ReadFile(s.completeMetaPath(fileID))
	if err != nil {
		return nil, fmt.Errorf("schedule: read file meta: %w", err)
	}
	var m FileMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("schedule: decode file meta: %w", err)
	}
	return &m, nil
}

func (s *Store) writePendingMeta(m *UploadMeta) error {
	data, _ := json.Marshal(m)
	return os.WriteFile(s.pendingMetaPath(m.UploadID), data, 0o640)
}

func (s *Store) writeFileMeta(m *FileMeta) error {
	data, _ := json.Marshal(m)
	return os.WriteFile(s.completeMetaPath(m.FileID), data, 0o640)
}

// ─────────────────────────────────────────────────────────────────────────────
// Path helpers
// ─────────────────────────────────────────────────────────────────────────────

// Each upload lives in its own directory named by its ID, so an upload's
// ciphertext and meta sidecar are isolated from every other upload's files.
// pending/<uploadID>/ during transfer; complete/<fileID>/ once finalized.

func (s *Store) pendingDir(id string) string {
	return filepath.Join(s.cfg.PendingDir, id)
}
func (s *Store) pendingEncPath(id string) string {
	return filepath.Join(s.pendingDir(id), "data.enc")
}
func (s *Store) pendingMetaPath(id string) string {
	return filepath.Join(s.pendingDir(id), "meta.json")
}
func (s *Store) completeDir(id string) string {
	return filepath.Join(s.cfg.CompleteDir, id)
}
func (s *Store) completeEncPath(id string) string {
	return filepath.Join(s.completeDir(id), "data.enc")
}
func (s *Store) completeMetaPath(id string) string {
	return filepath.Join(s.completeDir(id), "meta.json")
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("schedule: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// OpenComplete opens the ciphertext for reading without modifying download counts.
// Used internally by the download handler after auth is confirmed.
func (s *Store) OpenComplete(fileID string) (*os.File, error) {
	return os.Open(s.completeEncPath(fileID))
}

// moveFile moves src to dst. It first tries an atomic os.Rename, which only
// works within a single filesystem. When pending/ and complete/ live on
// different devices (e.g. separate Docker volumes, a tmpfs, or distinct
// mounts), rename fails with EXDEV ("invalid cross-device link"); in that case
// we fall back to copying the bytes and removing the original.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyFileContents(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyFileContents copies src to dst, flushing to disk before returning.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
