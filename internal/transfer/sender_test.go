package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// mockDataChannel — records every frame sent by the Sender
// ─────────────────────────────────────────────────────────────────────────────

type mockDataChannel struct {
	mu      sync.Mutex
	frames  [][]byte
	onSend  func([]byte) // called synchronously after the frame is recorded
	sendErr error        // if set, every Send returns this error
}

func (m *mockDataChannel) Send(data []byte) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.mu.Lock()
	m.frames = append(m.frames, cp)
	fn := m.onSend
	m.mu.Unlock()
	if fn != nil {
		fn(cp)
	}
	return nil
}

func (m *mockDataChannel) SendText(_ string) error { return nil }

func (m *mockDataChannel) sentFrames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([][]byte, len(m.frames))
	copy(cp, m.frames)
	return cp
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// senderPipes creates the channels expected by NewSender.
// resumeFrom is pre-loaded with 0 to skip the 2-second timeout in runFromReader
// (seq=0 means "start from the beginning", identical to a fresh transfer).
func senderPipes() (recvAck chan uint64, resumeFrom chan uint64, remoteCancel chan struct{}) {
	recvAck = make(chan uint64, 512)
	resumeFrom = make(chan uint64, 1)
	remoteCancel = make(chan struct{})
	resumeFrom <- 0
	return
}

// autoACK installs an onSend callback on dc that immediately ACKs every chunk
// frame. Use when the test cares about the complete transfer, not backpressure.
func autoACK(dc *mockDataChannel, recvAck chan uint64) {
	dc.onSend = func(frame []byte) {
		if len(frame) >= 9 && frame[0] == TagChunk {
			seq := binary.BigEndian.Uint64(frame[1:9])
			recvAck <- seq
		}
	}
}

// filterChunks returns only the TagChunk frames from a slice.
func filterChunks(frames [][]byte) [][]byte {
	var out [][]byte
	for _, f := range frames {
		if len(f) > 0 && f[0] == TagChunk {
			out = append(out, f)
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Sender.RunFromBytes tests
// ─────────────────────────────────────────────────────────────────────────────

// TestSender_RunFromStream verifies the two-pass streaming path: the FileHeader
// size/hash describe the produced bytes, and the reassembled chunk payloads equal
// what produce wrote — without any temp file.
func TestSender_RunFromStream(t *testing.T) {
	want := bytes.Repeat([]byte("abcdefgh"), 1000) // 8000 bytes, spans many chunks
	produce := func(w io.Writer) error { _, err := w.Write(want); return err }

	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 4, 64)
	if err := s.RunFromStream("bundle.zip", produce); err != nil {
		t.Fatalf("RunFromStream: %v", err)
	}

	frames := dc.sentFrames()
	var hdr FileHeader
	if err := json.Unmarshal(frames[0][1:], &hdr); err != nil {
		t.Fatalf("unmarshal FileHeader: %v", err)
	}
	if hdr.Size != int64(len(want)) {
		t.Errorf("Size: got %d, want %d", hdr.Size, len(want))
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256(want)); hdr.SHA256 != sum {
		t.Errorf("SHA256: got %s, want %s", hdr.SHA256, sum)
	}

	// Reassemble chunk payloads in order and compare to the source.
	var got []byte
	for _, c := range filterChunks(frames) {
		got = append(got, c[9:]...)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("reassembled %d bytes, want %d, content mismatch", len(got), len(want))
	}
}

func TestSender_RunFromBytes_SingleChunk_FrameSequence(t *testing.T) {
	// 50-byte payload fits in one chunk of 64 bytes.
	data := bytes.Repeat([]byte("x"), 50)
	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 1, 64)
	if err := s.RunFromBytes("hello.txt", data); err != nil {
		t.Fatalf("RunFromBytes: %v", err)
	}

	frames := dc.sentFrames()
	// Minimum expected: [FileHeader, Chunk, TransferDone]
	if len(frames) < 3 {
		t.Fatalf("expected ≥3 frames, got %d", len(frames))
	}
	if frames[0][0] != TagFileHeader {
		t.Errorf("frames[0]: got tag 0x%02x, want TagFileHeader", frames[0][0])
	}

	chunks := filterChunks(frames)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk frame, got %d", len(chunks))
	}
	seq := binary.BigEndian.Uint64(chunks[0][1:9])
	if seq != 0 {
		t.Errorf("chunk seq: got %d, want 0", seq)
	}
	if !bytes.Equal(chunks[0][9:], data) {
		t.Errorf("chunk payload mismatch")
	}

	last := frames[len(frames)-1]
	if last[0] != TagTransferDone {
		t.Errorf("last frame: got tag 0x%02x, want TagTransferDone", last[0])
	}
}

func TestSender_RunFromBytes_FileHeader_ContainsCorrectMetadata(t *testing.T) {
	data := []byte("hello world test data")
	const chunkSize = 8
	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 2, chunkSize)
	if err := s.RunFromBytes("readme.md", data); err != nil {
		t.Fatalf("RunFromBytes: %v", err)
	}

	frames := dc.sentFrames()
	if len(frames) == 0 || frames[0][0] != TagFileHeader {
		t.Fatal("first frame is not TagFileHeader")
	}

	var hdr FileHeader
	if err := json.Unmarshal(frames[0][1:], &hdr); err != nil {
		t.Fatalf("unmarshal FileHeader: %v", err)
	}
	if hdr.Name != "readme.md" {
		t.Errorf("Name: got %q, want readme.md", hdr.Name)
	}
	if hdr.Size != int64(len(data)) {
		t.Errorf("Size: got %d, want %d", hdr.Size, len(data))
	}
	if hdr.ChunkSize != chunkSize {
		t.Errorf("ChunkSize: got %d, want %d", hdr.ChunkSize, chunkSize)
	}
	if hdr.SHA256 == "" {
		t.Error("SHA256 should not be empty")
	}
	wantChunks := int64((len(data) + chunkSize - 1) / chunkSize)
	if hdr.Chunks != wantChunks {
		t.Errorf("Chunks: got %d, want %d", hdr.Chunks, wantChunks)
	}
}

func TestSender_RunFromBytes_MultipleChunks_AllDeliveredInOrder(t *testing.T) {
	// 30 bytes split into 3 chunks of 10.
	data := []byte("AAAAAAAAAA" + "BBBBBBBBBB" + "CCCCCCCCCC")
	const chunkSize = 10
	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 2, chunkSize)
	if err := s.RunFromBytes("data.bin", data); err != nil {
		t.Fatalf("RunFromBytes: %v", err)
	}

	chunks := filterChunks(dc.sentFrames())
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunk frames, got %d", len(chunks))
	}
	for i, cf := range chunks {
		seq := binary.BigEndian.Uint64(cf[1:9])
		if uint64(i) != seq {
			t.Errorf("chunks[%d]: seq=%d, want %d", i, seq, i)
		}
		want := data[i*chunkSize : (i+1)*chunkSize]
		if !bytes.Equal(cf[9:], want) {
			t.Errorf("chunks[%d]: payload mismatch", i)
		}
	}
}

func TestSender_RunFromBytes_WindowSize1_ChunksAreSentSequentially(t *testing.T) {
	// With windowSize=1 the sender must wait for each ACK before sending the next chunk.
	// autoACK fires immediately after dc.Send() returns, so the ACK is always in
	// recvAck before the select is reached — verifying order is sufficient.
	const chunkSize = 4
	data := bytes.Repeat([]byte("x"), 16) // 4 chunks
	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 1, chunkSize)
	if err := s.RunFromBytes("w1.bin", data); err != nil {
		t.Fatalf("RunFromBytes: %v", err)
	}

	chunks := filterChunks(dc.sentFrames())
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}
	for i, cf := range chunks {
		seq := binary.BigEndian.Uint64(cf[1:9])
		if uint64(i) != seq {
			t.Errorf("chunks[%d] seq=%d (not sequential)", i, seq)
		}
	}
}

func TestSender_RunFromBytes_ContextCancel_SendsCancelTagAndReturnsError(t *testing.T) {
	// Cancel the context immediately after the first chunk is sent (while the
	// sender is blocked waiting for an ACK). The context.Canceled case fires and
	// the sender emits TagCancelled before returning.
	data := bytes.Repeat([]byte("z"), 200)
	const chunkSize = 10

	recvAck := make(chan uint64, 512)
	resumeFrom := make(chan uint64, 1)
	resumeFrom <- 0
	remoteCancel := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	dc := &mockDataChannel{}
	var once sync.Once
	dc.onSend = func(frame []byte) {
		if len(frame) > 0 && frame[0] == TagChunk {
			// No ACK is pushed — cancel instead so the sender blocks on the select.
			once.Do(cancel)
		}
	}

	s := NewSender(ctx, remoteCancel, dc, "", recvAck, resumeFrom, 1, chunkSize)
	err := s.RunFromBytes("large.bin", data)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	found := false
	for _, f := range dc.sentFrames() {
		if len(f) == 1 && f[0] == TagCancelled {
			found = true
			break
		}
	}
	if !found {
		t.Error("TagCancelled frame was not sent after context cancellation")
	}
}

func TestSender_RunFromBytes_RemoteCancel_ReturnsErrCancelled(t *testing.T) {
	// Closing remoteCancel after the first chunk is sent causes the sender to
	// return ErrCancelled from the ACK-wait select.
	data := bytes.Repeat([]byte("y"), 200)
	const chunkSize = 10

	recvAck := make(chan uint64, 512)
	resumeFrom := make(chan uint64, 1)
	resumeFrom <- 0
	remoteCancel := make(chan struct{})

	dc := &mockDataChannel{}
	var once sync.Once
	dc.onSend = func(frame []byte) {
		if len(frame) > 0 && frame[0] == TagChunk {
			once.Do(func() { close(remoteCancel) })
		}
	}

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 1, chunkSize)
	err := s.RunFromBytes("file.bin", data)

	if !errors.Is(err, ErrCancelled) {
		t.Errorf("expected ErrCancelled, got %v", err)
	}
}

func TestSender_RunFromBytes_ResumeFromSeq_SkipsEarlierChunks(t *testing.T) {
	// 30 bytes / chunkSize=10 → chunks 0,1,2.
	// resumeFrom=2 means the receiver already has chunks 0 and 1.
	// The sender should seek to offset 20 and emit only chunk 2.
	data := []byte("AAAAAAAAAA" + "BBBBBBBBBB" + "CCCCCCCCCC")
	const chunkSize = 10

	recvAck := make(chan uint64, 512)
	resumeFrom := make(chan uint64, 1)
	resumeFrom <- 2 // skip chunks 0 and 1
	remoteCancel := make(chan struct{})

	dc := &mockDataChannel{}
	autoACK(dc, recvAck)

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 2, chunkSize)
	if err := s.RunFromBytes("data.bin", data); err != nil {
		t.Fatalf("RunFromBytes: %v", err)
	}

	chunks := filterChunks(dc.sentFrames())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (seq 2 only), got %d", len(chunks))
	}
	seq := binary.BigEndian.Uint64(chunks[0][1:9])
	if seq != 2 {
		t.Errorf("resumed chunk seq: got %d, want 2", seq)
	}
	want := data[20:] // "CCCCCCCCCC"
	if !bytes.Equal(chunks[0][9:], want) {
		t.Errorf("resumed chunk payload: got %q, want %q", chunks[0][9:], want)
	}
}

func TestSender_RunFromBytes_DCError_PropagatedImmediately(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 50)
	recvAck, resumeFrom, remoteCancel := senderPipes()
	dc := &mockDataChannel{sendErr: errors.New("channel closed")}

	s := NewSender(context.Background(), remoteCancel, dc, "", recvAck, resumeFrom, 1, 64)
	err := s.RunFromBytes("f.txt", data)
	if err == nil {
		t.Error("expected error from dc.Send, got nil")
	}
}
