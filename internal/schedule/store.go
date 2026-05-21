package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadMeta is stored as a JSON sidecar during a pending upload.
type UploadMeta struct {
	UploadID      string    `json:"upload_id"`
	ChunksTotal   int       `json:"chunks_total"`
	ChunkSize     int       `json:"chunk_size"`    // bytes per plaintext chunk
	ChunksWritten int       `json:"chunks_written"`
	TotalSize     int64     `json:"total_size"`    // plaintext bytes
	ExpiresAt     time.Time `json:"expires_at"`
	MaxDownloads  int       `json:"max_downloads"` // 0 = unlimited
	CreatedAt     time.Time `json:"created_at"`
}

// FileMeta is the permanent sidecar for a completed upload.
type FileMeta struct {
	FileID          string    `json:"file_id"`
	FileNameEnc     string    `json:"filename_enc"`  // hex-encoded encrypted filename
	FileNameNonce   string    `json:"filename_nonce"` // hex-encoded 12-byte nonce
	TotalSize       int64     `json:"total_size"`    // plaintext bytes
	EncryptedSize   int64     `json:"encrypted_size"` // ciphertext bytes on disk
	SHA256Cipher    string    `json:"sha256_cipher"` // hex SHA-256 of full ciphertext
	ChunksTotal     int       `json:"chunks_total"`
	ChunkSize       int       `json:"chunk_size"`
	ExpiresAt       time.Time `json:"expires_at"`
	MaxDownloads    int       `json:"max_downloads"`
	DownloadsLeft   int       `json:"downloads_left"` // -1 = unlimited
	DeleteKey       string    `json:"delete_key"`
	CreatedAt       time.Time `json:"created_at"`
}

const (
	// ChunkSize is the plaintext bytes per chunk. 2 MiB is a safe balance
	// between memory usage and overhead.
	ChunkSize = 2 * 1024 * 1024 // 2 MiB

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
func (s *Store) InitUpload(chunksTotal int, totalSize int64, expires time.Time, maxDownloads int) (*UploadMeta, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	meta := &UploadMeta{
		UploadID:     id,
		ChunksTotal:  chunksTotal,
		ChunkSize:    ChunkSize,
		TotalSize:    totalSize,
		ExpiresAt:    expires,
		MaxDownloads: maxDownloads,
		CreatedAt:    time.Now().UTC(),
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
	// Each chunk = nonce(12) + ciphertext(up to ChunkSize) + tag(16)
	maxChunkBytes := NonceSize + ChunkSize + TagSize
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

	// Move enc file.
	if err := os.Rename(s.pendingEncPath(uploadID), s.completeEncPath(fileID)); err != nil {
		return nil, fmt.Errorf("schedule: move enc: %w", err)
	}
	// Write final meta and remove pending meta.
	if err := s.writeFileMeta(fm); err != nil {
		return nil, err
	}
	_ = os.Remove(s.pendingMetaPath(uploadID))

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
	_ = os.Remove(s.completeEncPath(fileID))
	_ = os.Remove(s.completeMetaPath(fileID))
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

	// Clean complete files.
	entries, _ := os.ReadDir(s.cfg.CompleteDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		fileID := strings.TrimSuffix(e.Name(), ".meta")
		meta, err := s.ReadFileMeta(fileID)
		if err != nil {
			continue
		}
		expired := now.After(meta.ExpiresAt)
		exhausted := meta.DownloadsLeft == 0
		if expired || exhausted {
			_ = os.Remove(s.completeEncPath(fileID))
			_ = os.Remove(s.completeMetaPath(fileID))
			removed++
		}
	}

	// Clean stale pending uploads (older than 24h).
	staleThreshold := now.Add(-24 * time.Hour)
	pendingEntries, _ := os.ReadDir(s.cfg.PendingDir)
	for _, e := range pendingEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		uploadID := strings.TrimSuffix(e.Name(), ".meta")
		meta, err := s.ReadPendingMeta(uploadID)
		if err != nil {
			continue
		}
		if meta.CreatedAt.Before(staleThreshold) {
			_ = os.Remove(s.pendingEncPath(uploadID))
			_ = os.Remove(s.pendingMetaPath(uploadID))
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

func (s *Store) pendingEncPath(id string) string {
	return filepath.Join(s.cfg.PendingDir, id+".enc")
}
func (s *Store) pendingMetaPath(id string) string {
	return filepath.Join(s.cfg.PendingDir, id+".meta")
}
func (s *Store) completeEncPath(id string) string {
	return filepath.Join(s.cfg.CompleteDir, id+".enc")
}
func (s *Store) completeMetaPath(id string) string {
	return filepath.Join(s.cfg.CompleteDir, id+".meta")
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
