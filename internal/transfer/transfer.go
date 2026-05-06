// Package transfer defines the application-level file transfer protocol
// that runs over a WebRTC data channel.
//
// Wire format
//
// All messages are length-prefixed binary frames sent over the SCTP data
// channel.  The first byte is the message type tag.
//
//	Tag 0x01 — FileHeader    (initiator → responder, once)
//	Tag 0x02 — Chunk         (initiator → responder, repeated)
//	Tag 0x03 — ChunkAck      (responder → initiator, per chunk)
//	Tag 0x04 — TransferDone  (initiator → responder, after last chunk)
//	Tag 0x05 — TransferOK    (responder → initiator, after hash verified)
//	Tag 0x06 — TransferError (either direction)
//	Tag 0x07 — ResumeFrom    (responder → initiator, after FileHeader if resuming)
//
// Resume protocol
//
//	Initiator sends FileHeader.
//	Responder checks for a .gmmff_partial file whose .gmmff_meta matches
//	  the incoming SHA256 and ChunkSize.
//	If found: responder sends ResumeFrom{Seq: N} and initiator seeks to chunk N.
//	If not found: fresh transfer begins.
//	On success: partial and meta files are deleted, final file is renamed into place.
//	On cancel: partial and meta are left on disk for the next attempt.
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/schollz/progressbar/v3"
)

// DefaultChunkSize is the default chunk size in bytes.
// Set to the SCTP maximum (65535 − 9 bytes frame header) for best throughput.
const DefaultChunkSize = 65526

// MaxChunkSize is the largest permitted chunk size.
//
// SCTP (the transport under WebRTC data channels) has a hard message size
// limit of 65535 bytes.  Our frame header consumes 9 bytes (1 tag + 8 seq),
// leaving 65526 bytes for payload.
const MaxChunkSize = 65526 // 65535 − 9 bytes of frame header

// DefaultWindowSize is the number of chunks that may be in flight
// (sent but not yet acknowledged) at once.
const DefaultWindowSize = 2

// Temp file suffixes used during an in-progress receive.
const (
	PartialSuffix = ".gmmff_partial"
	MetaSuffix    = ".gmmff_meta"
)

// Message type tags.
const (
	TagFileHeader    byte = 0x01
	TagChunk         byte = 0x02
	TagChunkAck      byte = 0x03
	TagTransferDone  byte = 0x04
	TagTransferOK    byte = 0x05
	TagTransferError byte = 0x06
	TagResumeFrom    byte = 0x07
	TagCancelled     byte = 0x08 // sender or receiver intentionally stopped
)

// ErrCancelled is returned when the remote peer intentionally cancelled the
// transfer.  Callers should check for this with errors.Is to print a clean
// cancellation message instead of treating it as a failure.
var ErrCancelled = fmt.Errorf("transfer cancelled by remote peer")

// ─────────────────────────────────────────────────────────────────────────────
// Wire types
// ─────────────────────────────────────────────────────────────────────────────

// FileHeader is the first message sent by the initiator.
type FileHeader struct {
	Name      string `json:"name"`       // base name only — no path components
	Size      int64  `json:"size"`       // total bytes
	ChunkSize int    `json:"chunk_size"` // bytes per chunk
	SHA256    string `json:"sha256"`     // hex-encoded full-file hash
	Chunks    int64  `json:"chunks"`     // total number of chunks
}

// ResumeFromPayload is sent by the responder when a valid partial exists.
// Frame layout: [TagResumeFrom][8-byte seq BE]
type ResumeFromPayload struct {
	Seq uint64 // first chunk the sender should transmit
}

// PartialMeta is stored alongside a .gmmff_partial file so a future session
// can validate that the partial belongs to the same transfer.
type PartialMeta struct {
	SHA256    string `json:"sha256"`
	ChunkSize int    `json:"chunk_size"`
	BytesDone int64  `json:"bytes_done"`
	ChunksDone int64 `json:"chunks_done"`
}

// ErrorMsg carries a human-readable error code safe to display.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Sender
// ─────────────────────────────────────────────────────────────────────────────

// DataChannelWriter is the subset of the Pion DataChannel we need.
type DataChannelWriter interface {
	SendText(s string) error
	Send(data []byte) error
}

// Sender manages sending a single file over a data channel.
type Sender struct {
	dc         DataChannelWriter
	path       string
	ctx        context.Context
	recvAck    <-chan uint64
	resumeFrom <-chan uint64 // receives the resume seq from the receiver (buffered, len 1)
	windowSize int
	chunkSize  int
}

// NewSender creates a Sender.
//   - ctx: when cancelled, the sender sends TagCancelled to the receiver and
//     returns ErrCancelled (via context.Canceled wrapping).
//   - recvAck receives chunk sequence numbers as the receiver acknowledges them.
//   - resumeFrom receives the resume sequence number if the receiver has a
//     partial file.  The channel should be buffered (capacity 1).
//   - windowSize: max in-flight chunks.  Values < 1 clamped to 1.
//   - chunkSize: bytes per chunk.  Clamped to [1, MaxChunkSize].
func NewSender(ctx context.Context, dc DataChannelWriter, path string, recvAck <-chan uint64, resumeFrom <-chan uint64, windowSize, chunkSize int) *Sender {
	if windowSize < 1 {
		windowSize = 1
	}
	if chunkSize < 1 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize > MaxChunkSize {
		chunkSize = MaxChunkSize
	}
	return &Sender{
		dc:         dc,
		ctx:        ctx,
		path:       path,
		recvAck:    recvAck,
		resumeFrom: resumeFrom,
		windowSize: windowSize,
		chunkSize:  chunkSize,
	}
}

// Run executes the full send flow: header → (optional resume) → chunks → done.
func (s *Sender) Run() error {
	// ── Open and stat ────────────────────────────────────────────────────────
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("transfer: open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("transfer: stat file: %w", err)
	}

	// ── Hash ─────────────────────────────────────────────────────────────────
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("transfer: hash file: %w", err)
	}
	hexHash := fmt.Sprintf("%x", h.Sum(nil))
	_, _ = f.Seek(0, io.SeekStart)

	totalChunks := (info.Size() + int64(s.chunkSize) - 1) / int64(s.chunkSize)
	if totalChunks == 0 {
		totalChunks = 1
	}

	// ── Send FileHeader ──────────────────────────────────────────────────────
	hdr := FileHeader{
		Name:      info.Name(),
		Size:      info.Size(),
		ChunkSize: s.chunkSize,
		SHA256:    hexHash,
		Chunks:    totalChunks,
	}
	if err := s.sendHeader(hdr); err != nil {
		return err
	}

	// ── Check for resume ─────────────────────────────────────────────────────
	// The receiver sends a ResumeFrom frame immediately after the FileHeader
	// if it has a valid partial.  We wait up to 2 seconds for it to arrive —
	// enough for any realistic round-trip — before assuming a fresh transfer.
	var startSeq uint64
	select {
	case seq := <-s.resumeFrom:
		startSeq = seq
	case <-time.After(2 * time.Second):
		// No resume frame arrived — fresh transfer.
	case <-s.ctx.Done():
		return s.ctx.Err()
	}

	if startSeq > 0 {
		offset := int64(startSeq) * int64(s.chunkSize)
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("transfer: seek to resume offset: %w", err)
		}
		fmt.Printf("Resuming from chunk %d (%.1f MB already received)\n",
			startSeq, float64(offset)/1024/1024)
	}

	// ── Sliding window send loop ─────────────────────────────────────────────
	// Create the bar AFTER resume negotiation so it starts at the right offset.
	var startBytes int64
	if startSeq > 0 {
		startBytes = int64(startSeq) * int64(s.chunkSize)
	}
	bar := progressbar.DefaultBytes(info.Size(), "sending")
	if startBytes > 0 {
		_ = bar.Add64(startBytes)
	}

	buf := make([]byte, s.chunkSize)

	var (
		base    = startSeq
		nextSeq = startSeq
		fileEOF bool
	)

	for !fileEOF || nextSeq > base {
		inFlight := int(nextSeq - base)

		switch {
		case inFlight >= s.windowSize || (fileEOF && nextSeq > base):
			// Need an ack before we can proceed — also watch for cancellation.
			select {
			case <-s.ctx.Done():
				_ = s.sendTag(TagCancelled)
				fmt.Println()
				fmt.Println("Transfer cancelled.")
				return s.ctx.Err()
			case ackSeq, ok := <-s.recvAck:
				if !ok {
					// ackCh was closed by the peer handler — receiver cancelled.
					return ErrCancelled
				}
				if ackSeq != base {
					return fmt.Errorf("transfer: out-of-order ack: got %d want %d", ackSeq, base)
				}
				base++
			}

		case !fileEOF && inFlight < s.windowSize:
			// Check for cancellation before each read/send.
			select {
			case <-s.ctx.Done():
				_ = s.sendTag(TagCancelled)
				fmt.Println()
				fmt.Println("Transfer cancelled.")
				return s.ctx.Err()
			default:
			}
			n, readErr := f.Read(buf)
			if n > 0 {
				if err := s.sendChunk(nextSeq, buf[:n]); err != nil {
					return err
				}
				_ = bar.Add(n)
				nextSeq++
			}
			if readErr == io.EOF {
				fileEOF = true
			} else if readErr != nil {
				return fmt.Errorf("transfer: read file: %w", readErr)
			}
		}
	}

	if err := s.sendTag(TagTransferDone); err != nil {
		return err
	}
	fmt.Println()
	return nil
}

func (s *Sender) sendHeader(hdr FileHeader) error {
	b, err := json.Marshal(hdr)
	if err != nil {
		return fmt.Errorf("transfer: marshal header: %w", err)
	}
	frame := make([]byte, 1+len(b))
	frame[0] = TagFileHeader
	copy(frame[1:], b)
	return s.dc.Send(frame)
}

func (s *Sender) sendChunk(seq uint64, data []byte) error {
	frame := make([]byte, 1+8+len(data))
	frame[0] = TagChunk
	binary.BigEndian.PutUint64(frame[1:9], seq)
	copy(frame[9:], data)
	return s.dc.Send(frame)
}

func (s *Sender) sendTag(tag byte) error {
	return s.dc.Send([]byte{tag})
}

// ─────────────────────────────────────────────────────────────────────────────
// Receiver
// ─────────────────────────────────────────────────────────────────────────────

// ReceiveState holds mutable state built up as frames arrive.
type ReceiveState struct {
	Header      *FileHeader
	outDir      string   // directory to write into
	partialPath string   // <outDir>/<name>.gmmff_partial
	metaPath    string   // <outDir>/<name>.gmmff_meta
	finalPath   string   // <outDir>/<name>
	f           *os.File // handle to the partial file
	h           hash.Hash
	bar         *progressbar.ProgressBar
	received    int64
	resumeSeq   uint64 // first chunk we need (0 = fresh)
	sendAck     func(seq uint64) error
	sendResume  func(seq uint64) error // sends TagResumeFrom to the sender
}

// NewReceiveState initialises receiver state.
//   - outDir is the directory where the completed file will be saved.
//   - sendAck sends a ChunkAck frame back to the sender.
//   - sendResume sends a ResumeFrom frame back to the sender.
func NewReceiveState(outDir string, sendAck func(seq uint64) error, sendResume func(seq uint64) error) *ReceiveState {
	return &ReceiveState{
		outDir:     outDir,
		sendAck:    sendAck,
		sendResume: sendResume,
	}
}

// Feed processes one raw data channel frame.
// Returns (true, nil) when the transfer is complete and verified.
func (rs *ReceiveState) Feed(frame []byte) (done bool, err error) {
	if len(frame) == 0 {
		return false, nil
	}
	switch frame[0] {
	case TagFileHeader:
		return rs.handleHeader(frame[1:])
	case TagChunk:
		return rs.handleChunk(frame[1:])
	case TagTransferDone:
		return rs.handleDone()
	case TagTransferError:
		var e ErrorMsg
		_ = json.Unmarshal(frame[1:], &e)
		return false, fmt.Errorf("sender error [%s]: %s", e.Code, e.Message)
	case TagCancelled:
		return false, ErrCancelled
	default:
		return false, nil
	}
}

func (rs *ReceiveState) handleHeader(data []byte) (bool, error) {
	var hdr FileHeader
	if err := json.Unmarshal(data, &hdr); err != nil {
		return false, fmt.Errorf("transfer: decode header: %w", err)
	}
	rs.Header = &hdr

	safeName := sanitiseName(hdr.Name)
	rs.partialPath = filepath.Join(rs.outDir, safeName+PartialSuffix)
	rs.metaPath    = filepath.Join(rs.outDir, safeName+MetaSuffix)
	rs.finalPath   = filepath.Join(rs.outDir, safeName)

	// ── Check for a usable partial ───────────────────────────────────────────
	if resumeSeq, bytesAlready := rs.checkPartial(hdr); resumeSeq > 0 {
		// Reopen the partial for appending and replay the hash up to the
		// already-written bytes.
		f, err := os.OpenFile(rs.partialPath, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			// Partial unreadable — fall through to fresh start.
			goto freshStart
		}
		h, err := replayHash(rs.partialPath, bytesAlready)
		if err != nil {
			_ = f.Close()
			goto freshStart
		}

		rs.f        = f
		rs.h        = h
		rs.received = bytesAlready
		rs.resumeSeq = resumeSeq
		rs.bar = progressbar.DefaultBytes(hdr.Size, "receiving")
		_ = rs.bar.Add64(bytesAlready)

		fmt.Printf("Resuming — %d chunks already received (%.1f MB)\n",
			resumeSeq, float64(bytesAlready)/1024/1024)

		// Tell the sender which chunk to start from.
		if err := rs.sendResume(resumeSeq); err != nil {
			_ = f.Close()
			return false, fmt.Errorf("transfer: send resume: %w", err)
		}
		return false, nil
	}

freshStart:
	// ── Fresh transfer — create partial + meta ───────────────────────────────
	f, err := os.Create(rs.partialPath)
	if err != nil {
		return false, fmt.Errorf("transfer: create partial file: %w", err)
	}
	if err := rs.writeMeta(hdr, 0, 0); err != nil {
		_ = f.Close()
		return false, err
	}

	rs.f        = f
	rs.h        = sha256.New()
	rs.received = 0
	rs.resumeSeq = 0
	rs.bar = progressbar.DefaultBytes(hdr.Size, "receiving")
	return false, nil
}

func (rs *ReceiveState) handleChunk(data []byte) (bool, error) {
	if rs.Header == nil || rs.f == nil {
		return false, fmt.Errorf("transfer: chunk received before header")
	}
	if len(data) < 8 {
		return false, fmt.Errorf("transfer: chunk frame too short")
	}

	seq := binary.BigEndian.Uint64(data[:8])
	payload := data[8:]

	if _, err := rs.f.Write(payload); err != nil {
		return false, fmt.Errorf("transfer: write chunk %d: %w", seq, err)
	}
	_, _ = rs.h.Write(payload)
	rs.received += int64(len(payload))
	_ = rs.bar.Add(len(payload))

	// Update meta periodically (every chunk is fine — files are large).
	_ = rs.writeMeta(*rs.Header, rs.received, int64(seq)+1)

	if err := rs.sendAck(seq); err != nil {
		return false, fmt.Errorf("transfer: send ack %d: %w", seq, err)
	}
	return false, nil
}

func (rs *ReceiveState) handleDone() (bool, error) {
	if rs.Header == nil || rs.f == nil {
		return false, fmt.Errorf("transfer: done received before header")
	}
	_ = rs.f.Close()
	fmt.Println()

	// Verify hash.
	got := fmt.Sprintf("%x", rs.h.Sum(nil))
	if got != rs.Header.SHA256 {
		// Leave the partial in place — hash mismatch on a resumed transfer
		// means corruption; the user should delete manually.
		return false, fmt.Errorf("transfer: integrity check failed\n  want %s\n  got  %s",
			rs.Header.SHA256, got)
	}

	// Rename partial → final.
	if err := os.Rename(rs.partialPath, rs.finalPath); err != nil {
		return false, fmt.Errorf("transfer: rename partial to final: %w", err)
	}

	// Clean up meta file.
	_ = os.Remove(rs.metaPath)

	return true, nil
}

// OutputPath returns the final file path (valid after done == true).
func (rs *ReceiveState) OutputPath() string { return rs.finalPath }

// ─────────────────────────────────────────────────────────────────────────────
// Resume helpers
// ─────────────────────────────────────────────────────────────────────────────

// checkPartial returns (resumeSeq, bytesAlready) if a valid partial exists
// for this header, or (0, 0) if no resume is possible.
func (rs *ReceiveState) checkPartial(hdr FileHeader) (uint64, int64) {
	// Read meta file.
	raw, err := os.ReadFile(rs.metaPath)
	if err != nil {
		return 0, 0
	}
	var meta PartialMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return 0, 0
	}

	// Validate that this partial belongs to the same file.
	if meta.SHA256 != hdr.SHA256 || meta.ChunkSize != hdr.ChunkSize {
		return 0, 0
	}

	// Verify the partial file itself exists and is the expected size.
	info, err := os.Stat(rs.partialPath)
	if err != nil {
		return 0, 0
	}
	if info.Size() != meta.BytesDone {
		return 0, 0 // truncated or grown — don't trust it
	}
	if meta.ChunksDone == 0 {
		return 0, 0
	}

	return uint64(meta.ChunksDone), meta.BytesDone
}

// writeMeta serialises a PartialMeta to the meta sidecar file.
func (rs *ReceiveState) writeMeta(hdr FileHeader, bytesDone int64, chunksDone int64) error {
	meta := PartialMeta{
		SHA256:     hdr.SHA256,
		ChunkSize:  hdr.ChunkSize,
		BytesDone:  bytesDone,
		ChunksDone: chunksDone,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("transfer: marshal meta: %w", err)
	}
	if err := os.WriteFile(rs.metaPath, b, 0o644); err != nil {
		return fmt.Errorf("transfer: write meta: %w", err)
	}
	return nil
}

// replayHash reads the first n bytes of path through a fresh SHA-256 hasher
// and returns it.  Used to reconstruct the running hash when resuming.
func replayHash(path string, n int64) (hash.Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil && err != io.EOF {
		return nil, err
	}
	return h, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Frame helpers
// ─────────────────────────────────────────────────────────────────────────────

// BuildAckFrame builds a ChunkAck frame for the given sequence number.
func BuildAckFrame(seq uint64) []byte {
	frame := make([]byte, 1+8)
	frame[0] = TagChunkAck
	binary.BigEndian.PutUint64(frame[1:], seq)
	return frame
}

// ParseAckFrame extracts the sequence number from a ChunkAck frame.
func ParseAckFrame(frame []byte) (uint64, error) {
	if len(frame) < 9 || frame[0] != TagChunkAck {
		return 0, fmt.Errorf("transfer: invalid ack frame")
	}
	return binary.BigEndian.Uint64(frame[1:]), nil
}

// BuildResumeFrame builds a ResumeFrom frame.
func BuildResumeFrame(seq uint64) []byte {
	frame := make([]byte, 1+8)
	frame[0] = TagResumeFrom
	binary.BigEndian.PutUint64(frame[1:], seq)
	return frame
}

// ParseResumeFrame extracts the resume sequence number.
func ParseResumeFrame(frame []byte) (uint64, error) {
	if len(frame) < 9 || frame[0] != TagResumeFrom {
		return 0, fmt.Errorf("transfer: invalid resume frame")
	}
	return binary.BigEndian.Uint64(frame[1:]), nil
}

// BuildCancelledFrame builds a TagCancelled frame.
func BuildCancelledFrame() []byte {
	return []byte{TagCancelled}
}

// BuildErrorFrame serialises a TransferError frame.
func BuildErrorFrame(code, message string) []byte {
	b, _ := json.Marshal(ErrorMsg{Code: code, Message: message})
	frame := make([]byte, 1+len(b))
	frame[0] = TagTransferError
	copy(frame[1:], b)
	return frame
}

// sanitiseName strips all path separators from a filename.
func sanitiseName(name string) string {
	safe := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '/' || c == '\\' || c == 0 {
			continue
		}
		safe = append(safe, c)
	}
	if len(safe) == 0 {
		return "gmmff_received_file"
	}
	return string(safe)
}
