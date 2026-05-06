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
//
// Chunk size is 16 KiB — chosen to stay well within SCTP's MTU and give
// smooth progress updates.  The final SHA-256 hash covers the entire file
// and is verified before TransferOK is sent.
package transfer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/schollz/progressbar/v3"
)

// DefaultChunkSize is the default chunk size in bytes.
const DefaultChunkSize = 16 * 1024 // 16 KiB

// MaxChunkSize is the largest permitted chunk size.
// Values beyond 1 MiB give no measurable throughput benefit and make
// the progress bar choppy on slow connections.
const MaxChunkSize = 1024 * 1024 // 1 MiB

// DefaultWindowSize is the number of chunks that may be in flight
// (sent but not yet acknowledged) at once.  Increasing this improves
// throughput on high-latency links at the cost of more memory.
const DefaultWindowSize = 2

// Message type tags.
const (
	TagFileHeader    byte = 0x01
	TagChunk         byte = 0x02
	TagChunkAck      byte = 0x03
	TagTransferDone  byte = 0x04
	TagTransferOK    byte = 0x05
	TagTransferError byte = 0x06
)

// ─────────────────────────────────────────────────────────────────────────────
// Wire types
// ─────────────────────────────────────────────────────────────────────────────

// FileHeader is the first message sent by the initiator.
type FileHeader struct {
	Name      string `json:"name"`       // base name only — no path components
	Size      int64  `json:"size"`       // total bytes
	ChunkSize int    `json:"chunk_size"` // bytes per chunk (informational)
	SHA256    string `json:"sha256"`     // hex-encoded full-file hash
	Chunks    int64  `json:"chunks"`     // total number of chunks
}

// ChunkHeader precedes each chunk's raw bytes in a single frame.
// Frame layout: [TagChunk][8-byte seq big-endian][raw chunk bytes]
type ChunkHeader struct {
	Seq uint64 // zero-based chunk sequence number
}

// ErrorMsg carries a human-readable error code safe to display.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Sender — breaks a file into chunks and sends them over a data channel
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
	recvAck    <-chan uint64 // acks from the receiver (chunk seq numbers)
	windowSize int          // max chunks in flight simultaneously
	chunkSize  int          // bytes per chunk
}

// NewSender creates a Sender.
//   - recvAck receives chunk sequence numbers as the receiver acknowledges them.
//   - windowSize is the maximum number of unacknowledged chunks in flight.
//     Pass DefaultWindowSize if unsure. Values < 1 are clamped to 1.
//   - chunkSize is the number of bytes per chunk.
//     Pass DefaultChunkSize if unsure. Values are clamped to [1, MaxChunkSize].
func NewSender(dc DataChannelWriter, path string, recvAck <-chan uint64, windowSize, chunkSize int) *Sender {
	if windowSize < 1 {
		windowSize = 1
	}
	if chunkSize < 1 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize > MaxChunkSize {
		chunkSize = MaxChunkSize
	}
	return &Sender{dc: dc, path: path, recvAck: recvAck, windowSize: windowSize, chunkSize: chunkSize}
}

// Run executes the full send flow: header → chunks (sliding window) → done.
// Blocks until the transfer completes or fails.
func (s *Sender) Run() error {
	// ── Open and stat the file ───────────────────────────────────────────────
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("transfer: open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("transfer: stat file: %w", err)
	}

	// ── Compute SHA-256 ──────────────────────────────────────────────────────
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

	// ── Sliding window send loop ─────────────────────────────────────────────
	//
	// Invariant: inFlight = nextSeq - base
	//   base    = lowest sent-but-unacked sequence number
	//   nextSeq = next sequence number to send
	//
	// Rules:
	//   - Send a chunk when inFlight < windowSize AND file not exhausted.
	//   - Block for one ack when inFlight == windowSize OR all chunks sent
	//     but acks still outstanding.
	//   - Because SCTP is ordered and the receiver acks every chunk in order,
	//     acks arrive strictly in sequence; we assert this and fail fast.

	bar := progressbar.DefaultBytes(info.Size(), "sending")
	buf := make([]byte, s.chunkSize)

	var (
		base    uint64 // oldest unacked seq
		nextSeq uint64 // next seq to send
		fileEOF bool   // true once the last byte has been read
	)

	for !fileEOF || nextSeq > base {
		inFlight := int(nextSeq - base)

		switch {
		case inFlight >= s.windowSize || (fileEOF && nextSeq > base):
			// Window full, or all sent and draining remaining acks.
			ackSeq, ok := <-s.recvAck
			if !ok {
				return fmt.Errorf("transfer: ack channel closed unexpectedly")
			}
			if ackSeq != base {
				return fmt.Errorf("transfer: out-of-order ack: got %d want %d", ackSeq, base)
			}
			base++

		case !fileEOF && inFlight < s.windowSize:
			// Window has space — read and send the next chunk.
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

	// ── Send done ────────────────────────────────────────────────────────────
	if err := s.sendTag(TagTransferDone); err != nil {
		return err
	}

	fmt.Println() // newline after progress bar
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
	// Frame: [TagChunk][8-byte seq BE][data]
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
// Receiver — reassembles chunks from a data channel into a file
// ─────────────────────────────────────────────────────────────────────────────

// ReceiveState holds mutable state built up as frames arrive.
// Call Feed() for each incoming data channel message.
type ReceiveState struct {
	Header   *FileHeader
	outPath  string
	f        *os.File
	h        hash.Hash
	bar      *progressbar.ProgressBar
	received int64
	done     bool
	sendAck  func(seq uint64) error
}

// NewReceiveState initialises receiver state.
// outDir is where the completed file will be saved.
func NewReceiveState(outDir string, sendAck func(seq uint64) error) *ReceiveState {
	return &ReceiveState{sendAck: sendAck, outPath: outDir}
}

// Feed processes one raw data channel frame.
// Returns (true, nil) when the transfer is complete and verified.
// Returns (false, err) on a fatal error.
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

	default:
		return false, nil // ignore unknown tags gracefully
	}
}

func (rs *ReceiveState) handleHeader(data []byte) (bool, error) {
	var hdr FileHeader
	if err := json.Unmarshal(data, &hdr); err != nil {
		return false, fmt.Errorf("transfer: decode header: %w", err)
	}
	rs.Header = &hdr

	safeName := sanitiseName(hdr.Name)
	outPath := rs.outPath + string(os.PathSeparator) + safeName

	f, err := os.Create(outPath)
	if err != nil {
		return false, fmt.Errorf("transfer: create output file: %w", err)
	}
	rs.f = f
	rs.outPath = outPath
	rs.h = sha256.New()
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
	fmt.Println() // newline after progress bar

	got := fmt.Sprintf("%x", rs.h.Sum(nil))
	if got != rs.Header.SHA256 {
		_ = os.Remove(rs.outPath)
		return false, fmt.Errorf("transfer: integrity check failed\n  want %s\n  got  %s",
			rs.Header.SHA256, got)
	}

	rs.done = true
	return true, nil
}

// OutputPath returns the path of the completed file (valid after done==true).
func (rs *ReceiveState) OutputPath() string { return rs.outPath }

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
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
