package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// sha256HexOf returns the hex-encoded SHA-256 digest of data.
func sha256HexOf(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// buildChunkFrame constructs a raw TagChunk frame as the sender would send it.
// Layout: [TagChunk][8-byte seq BE][payload]
func buildChunkFrame(seq uint64, payload []byte) []byte {
	frame := make([]byte, 1+8+len(payload))
	frame[0] = TagChunk
	binary.BigEndian.PutUint64(frame[1:], seq)
	copy(frame[9:], payload)
	return frame
}

// buildFileHeaderFrame encodes a FileHeader into a wire frame.
func buildFileHeaderFrame(t *testing.T, hdr FileHeader) []byte {
	t.Helper()
	b, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("marshal FileHeader: %v", err)
	}
	frame := make([]byte, 1+len(b))
	frame[0] = TagFileHeader
	copy(frame[1:], b)
	return frame
}

// ─────────────────────────────────────────────────────────────────────────────
// TagMessage — BuildMessageFrame / ParseMessageFrame
// ─────────────────────────────────────────────────────────────────────────────

func TestMessageFrame_RoundTrip(t *testing.T) {
	cases := []string{
		"hello",
		"Hello, 世界",
		"",
		strings.Repeat("x", 65000),
		"\x01name:Alice",
		"\x01roster:abc=Alice",
	}
	for _, text := range cases {
		frame := BuildMessageFrame(text)
		if frame[0] != TagMessage {
			t.Errorf("BuildMessageFrame(%q): tag = 0x%02x, want 0x%02x",
				text, frame[0], TagMessage)
		}
		got := ParseMessageFrame(frame)
		if got != text {
			t.Errorf("ParseMessageFrame round-trip(%q): got %q", text, got)
		}
	}
}

func TestMessageFrame_ParseInvalidInputs(t *testing.T) {
	// None of these should panic.
	ParseMessageFrame(nil)
	ParseMessageFrame([]byte{})
	ParseMessageFrame([]byte{TagMessage}) // tag only — text is ""

	// Wrong tag must return empty string.
	got := ParseMessageFrame([]byte{TagChunk, 'h', 'i'})
	if got != "" {
		t.Errorf("wrong tag: expected empty string, got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TagRelayedMessage — BuildRelayedMessageFrame / ParseRelayedMessageFrame
// This is the regression test for the bug where sender identity was lost
// when the initiator relayed messages to other peers.
// ─────────────────────────────────────────────────────────────────────────────

func TestRelayedMessageFrame_RoundTrip(t *testing.T) {
	cases := []struct {
		senderID string
		text     string
	}{
		{"peer-uuid-1234", "hello from peer 1"},
		{"initiator", "message from initiator"},
		{"__system__", "\x01leave:Alice left the session."},
		{"", "message with empty sender"},
		{"a", ""},
		{"a", "b"},
		{strings.Repeat("x", 255), "sender at max length"},
		{strings.Repeat("x", 256), "sender trimmed to 255"},
	}
	for _, tc := range cases {
		frame := BuildRelayedMessageFrame(tc.senderID, tc.text)

		if frame[0] != TagRelayedMessage {
			t.Errorf("BuildRelayedMessageFrame(%q, %q): tag = 0x%02x, want 0x%02x",
				tc.senderID, tc.text, frame[0], TagRelayedMessage)
		}

		gotSender, gotText := ParseRelayedMessageFrame(frame)

		wantSender := tc.senderID
		if len(wantSender) > 255 {
			wantSender = wantSender[:255]
		}
		if gotSender != wantSender {
			t.Errorf("senderID(%q): got %q, want %q", tc.senderID, gotSender, wantSender)
		}
		if gotText != tc.text {
			t.Errorf("text(%q): got %q, want %q", tc.text, gotText, tc.text)
		}
	}
}

func TestRelayedMessageFrame_SenderIDAndTextAreIndependent(t *testing.T) {
	frame1 := BuildRelayedMessageFrame("alice", "hello world")
	frame2 := BuildRelayedMessageFrame("bob", "hello world")

	s1, t1 := ParseRelayedMessageFrame(frame1)
	s2, t2 := ParseRelayedMessageFrame(frame2)

	if s1 == s2 {
		t.Error("different sender IDs should differ in decoded output")
	}
	if t1 != t2 {
		t.Errorf("same text should parse identically: %q != %q", t1, t2)
	}
}

func TestRelayedMessageFrame_ParseInvalidInputs(t *testing.T) {
	// None of these should panic.
	ParseRelayedMessageFrame(nil)
	ParseRelayedMessageFrame([]byte{})
	ParseRelayedMessageFrame([]byte{TagRelayedMessage})
	ParseRelayedMessageFrame([]byte{TagRelayedMessage, 0xFF}) // claims 255 bytes but frame too short

	// Wrong tag.
	s, txt := ParseRelayedMessageFrame([]byte{TagMessage, 0x03, 'a', 'b', 'c'})
	if s != "" || txt != "" {
		t.Errorf("wrong tag: expected (\"\",\"\"), got (%q, %q)", s, txt)
	}
}

func TestRelayedMessageFrame_Layout(t *testing.T) {
	// Verify: [0x11][len(senderID)][senderID bytes][text bytes]
	senderID := "peer-42"
	text := "hi"
	frame := BuildRelayedMessageFrame(senderID, text)

	if frame[1] != byte(len(senderID)) {
		t.Errorf("frame[1] (senderID length): got %d, want %d", frame[1], len(senderID))
	}
	if string(frame[2:2+len(senderID)]) != senderID {
		t.Errorf("senderID bytes: got %q, want %q", frame[2:2+len(senderID)], senderID)
	}
	if string(frame[2+len(senderID):]) != text {
		t.Errorf("text bytes: got %q, want %q", frame[2+len(senderID):], text)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TagPeerCount — BuildPeerCountFrame / ParsePeerCountFrame
// ─────────────────────────────────────────────────────────────────────────────

func TestPeerCountFrame_RoundTrip(t *testing.T) {
	cases := []struct{ count, maxPeers int }{
		{1, 2}, {2, 2}, {3, 10}, {10, 10}, {0, 0}, {255, 255},
	}
	for _, tc := range cases {
		frame := BuildPeerCountFrame(tc.count, tc.maxPeers)

		if frame[0] != TagPeerCount {
			t.Errorf("BuildPeerCountFrame(%d,%d): tag = 0x%02x, want 0x%02x",
				tc.count, tc.maxPeers, frame[0], TagPeerCount)
		}
		if len(frame) != 3 {
			t.Errorf("BuildPeerCountFrame(%d,%d): length = %d, want 3",
				tc.count, tc.maxPeers, len(frame))
		}

		gotCount, gotMax := ParsePeerCountFrame(frame)
		if gotCount != tc.count || gotMax != tc.maxPeers {
			t.Errorf("round-trip(%d,%d): got (%d,%d)", tc.count, tc.maxPeers, gotCount, gotMax)
		}
	}
}

func TestPeerCountFrame_ParseInvalidInputs(t *testing.T) {
	invalids := [][]byte{
		nil,
		{},
		{TagPeerCount},
		{TagPeerCount, 0x02},
		{TagMessage, 0x02, 0x0A},
	}
	for _, frame := range invalids {
		count, max := ParsePeerCountFrame(frame)
		if count != 0 || max != 0 {
			t.Errorf("invalid frame %v: expected (0,0), got (%d,%d)", frame, count, max)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TagResumeFrom — BuildResumeFrame / ParseResumeFrame
// ─────────────────────────────────────────────────────────────────────────────

func TestResumeFrame_RoundTrip(t *testing.T) {
	seqs := []uint64{0, 1, 100, 1<<32 - 1, 1<<63 - 1}
	for _, seq := range seqs {
		frame := BuildResumeFrame(seq)

		if frame[0] != TagResumeFrom {
			t.Errorf("BuildResumeFrame(%d): tag = 0x%02x", seq, frame[0])
		}
		if len(frame) != 9 {
			t.Errorf("BuildResumeFrame(%d): length = %d, want 9", seq, len(frame))
		}

		got, err := ParseResumeFrame(frame)
		if err != nil {
			t.Errorf("ParseResumeFrame(%d): %v", seq, err)
		}
		if got != seq {
			t.Errorf("seq round-trip(%d): got %d", seq, got)
		}
	}
}

func TestResumeFrame_BigEndian(t *testing.T) {
	frame := BuildResumeFrame(1)
	want := []byte{TagResumeFrom, 0, 0, 0, 0, 0, 0, 0, 1}
	if !bytes.Equal(frame, want) {
		t.Errorf("BuildResumeFrame(1) = %v, want %v", frame, want)
	}
}

func TestResumeFrame_ParseInvalidInputs(t *testing.T) {
	invalids := [][]byte{
		nil,
		{},
		{TagResumeFrom, 0, 0, 0},
		append([]byte{TagMessage}, make([]byte, 8)...),
	}
	for _, frame := range invalids {
		_, err := ParseResumeFrame(frame)
		if err == nil {
			t.Errorf("ParseResumeFrame(%v): expected error, got nil", frame)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Single-byte control frames
// ─────────────────────────────────────────────────────────────────────────────

func TestControlFrames_CorrectTags(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  byte
	}{
		{"ChatClose", BuildChatCloseFrame(), TagChatClose},
		{"ParticipantLeave", BuildParticipantLeaveFrame(), TagParticipantLeave},
		{"SessionReady", BuildSessionReadyFrame(), TagSessionReady},
		{"SessionClose", BuildSessionCloseFrame(), TagSessionClose},
		{"Cancelled", BuildCancelledFrame(), TagCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.frame) != 1 {
				t.Errorf("length = %d, want 1", len(tc.frame))
			}
			if tc.frame[0] != tc.want {
				t.Errorf("tag = 0x%02x, want 0x%02x", tc.frame[0], tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TransferAnnounce / TransferAccepted label encoding
// ─────────────────────────────────────────────────────────────────────────────

func TestTransferAnnounceFrame_RoundTrip(t *testing.T) {
	labels := []string{"channel-1", "transfer-abc-def", "", "x"}
	for _, label := range labels {
		frame := BuildTransferAnnounceFrame(label)
		if frame[0] != TagTransferAnnounce {
			t.Errorf("BuildTransferAnnounceFrame(%q): wrong tag 0x%02x", label, frame[0])
		}
		got := ParseTransferAnnounceFrame(frame)
		if got != label {
			t.Errorf("round-trip(%q): got %q", label, got)
		}
	}
}

func TestTransferAcceptedFrame_Layout(t *testing.T) {
	label := "channel-1"
	frame := BuildTransferAcceptedFrame(label)
	if frame[0] != TagTransferAccepted {
		t.Errorf("tag = 0x%02x, want 0x%02x", frame[0], TagTransferAccepted)
	}
	if string(frame[1:]) != label {
		t.Errorf("body = %q, want %q", frame[1:], label)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrorFrame
// ─────────────────────────────────────────────────────────────────────────────

func TestErrorFrame_TagAndJSON(t *testing.T) {
	frame := BuildErrorFrame("ERR_HASH", "SHA-256 mismatch")
	if frame[0] != TagTransferError {
		t.Errorf("tag = 0x%02x, want 0x%02x", frame[0], TagTransferError)
	}
	body := string(frame[1:])
	if !strings.Contains(body, "ERR_HASH") {
		t.Errorf("body missing code: %s", body)
	}
	if !strings.Contains(body, "SHA-256 mismatch") {
		t.Errorf("body missing message: %s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sanitiseName
// ─────────────────────────────────────────────────────────────────────────────

func TestSanitiseName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"document.pdf", "document.pdf"},
		{"my file.txt", "my file.txt"},
		{"/etc/passwd", "etcpasswd"},
		{"../../../etc/shadow", "etcshadow"},    // separators + traversal stripped
		{"C:\\Users\\file.txt", "C:Usersfile.txt"},
		{"", "gmmff_received_file"},
		{"\x00null\x00bytes\x00", "nullbytes"},
		{"a/b/c", "abc"},
		{"....dotdotdot", "dotdotdot"},          // even literal ".." in names are stripped
		{"file..name.txt", "filename.txt"},
	}
	for _, tc := range cases {
		got := sanitiseName(tc.input)
		if got != tc.want {
			t.Errorf("sanitiseName(%q): got %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ReceiveStateMem
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveStateMem_CompleteTransfer(t *testing.T) {
	var acks []uint64
	recv := NewReceiveStateMem(func(seq uint64) error {
		acks = append(acks, seq)
		return nil
	})

	payload1 := []byte("hello ")
	payload2 := []byte("world")
	content := append([]byte(nil), payload1...)
	content  = append(content, payload2...)

	hdr := FileHeader{
		Name:      "test.txt",
		Size:      int64(len(content)),
		ChunkSize: len(payload1),
		SHA256:    sha256HexOf(content),
		Chunks:    2,
	}

	if done, err := recv.Feed(buildFileHeaderFrame(t, hdr)); err != nil || done {
		t.Fatalf("Feed(FileHeader): done=%v err=%v", done, err)
	}
	if recv.Header == nil {
		t.Fatal("Header should be set after FileHeader")
	}
	if recv.FileName() != "test.txt" {
		t.Errorf("FileName: got %q, want %q", recv.FileName(), "test.txt")
	}

	if done, err := recv.Feed(buildChunkFrame(0, payload1)); err != nil || done {
		t.Fatalf("Feed(chunk 0): done=%v err=%v", done, err)
	}
	if done, err := recv.Feed(buildChunkFrame(1, payload2)); err != nil || done {
		t.Fatalf("Feed(chunk 1): done=%v err=%v", done, err)
	}

	done, err := recv.Feed([]byte{TagTransferDone})
	if err != nil {
		t.Fatalf("Feed(TransferDone): %v", err)
	}
	if !done {
		t.Error("Feed(TransferDone): expected done=true")
	}

	if !bytes.Equal(recv.Result(), content) {
		t.Errorf("Result: got %q, want %q", recv.Result(), content)
	}
	if len(acks) != 2 {
		t.Errorf("acks: got %d, want 2", len(acks))
	}
}

func TestReceiveStateMem_IntegrityFailure(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	payload := []byte("some data")

	hdr := FileHeader{
		Name:      "file.txt",
		Size:      int64(len(payload)),
		ChunkSize: len(payload),
		SHA256:    strings.Repeat("0", 64), // deliberately wrong hash
		Chunks:    1,
	}
	recv.Feed(buildFileHeaderFrame(t, hdr)) //nolint:errcheck
	recv.Feed(buildChunkFrame(0, payload))  //nolint:errcheck

	_, err := recv.Feed([]byte{TagTransferDone})
	if err == nil {
		t.Error("TransferDone with wrong hash: expected error, got nil")
	}
}

func TestReceiveStateMem_CancelledFrame(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	done, err := recv.Feed([]byte{TagCancelled})
	if done {
		t.Error("Cancelled should not set done=true")
	}
	if err != ErrCancelled {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}

func TestReceiveStateMem_ErrorFrame(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	done, err := recv.Feed(BuildErrorFrame("ERR_DISK", "disk full"))
	if done {
		t.Error("ErrorFrame should not set done=true")
	}
	if err == nil {
		t.Error("ErrorFrame: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_DISK") {
		t.Errorf("error should mention code, got %v", err)
	}
}

func TestReceiveStateMem_ChunkBeforeHeader(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	_, err := recv.Feed(buildChunkFrame(0, []byte("data")))
	if err == nil {
		t.Error("chunk before header: expected error, got nil")
	}
}

func TestReceiveStateMem_EmptyAndNilFrames(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	if done, err := recv.Feed([]byte{}); err != nil || done {
		t.Errorf("empty frame: got (%v, %v), want (false, nil)", done, err)
	}
	if done, err := recv.Feed(nil); err != nil || done {
		t.Errorf("nil frame: got (%v, %v), want (false, nil)", done, err)
	}
}

func TestReceiveStateMem_FilenameSanitised(t *testing.T) {
	recv := NewReceiveStateMem(func(uint64) error { return nil })
	payload := []byte("hello")
	hdr := FileHeader{
		Name:      "../../malicious.sh",
		Size:      int64(len(payload)),
		ChunkSize: len(payload),
		SHA256:    sha256HexOf(payload),
		Chunks:    1,
	}
	recv.Feed(buildFileHeaderFrame(t, hdr)) //nolint:errcheck

	name := recv.FileName()
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		t.Errorf("filename contains path separator: %q", name)
	}
	if strings.Contains(name, "..") {
		t.Errorf("filename contains traversal: %q", name)
	}
}

func TestReceiveStateMem_ProgressCallback(t *testing.T) {
	var calls int
	var lastDone, lastTotal int64

	recv := NewReceiveStateMem(func(uint64) error { return nil })
	recv.SetProgress(func(done, total int64) {
		calls++
		lastDone = done
		lastTotal = total
	})

	payload := []byte("hello world")
	hdr := FileHeader{
		Name:      "f.txt",
		Size:      int64(len(payload)),
		ChunkSize: len(payload),
		SHA256:    sha256HexOf(payload),
		Chunks:    1,
	}
	recv.Feed(buildFileHeaderFrame(t, hdr)) //nolint:errcheck
	recv.Feed(buildChunkFrame(0, payload))  //nolint:errcheck

	if calls != 1 {
		t.Errorf("progress calls: got %d, want 1", calls)
	}
	if lastDone != int64(len(payload)) {
		t.Errorf("progress done: got %d, want %d", lastDone, len(payload))
	}
	if lastTotal != int64(len(payload)) {
		t.Errorf("progress total: got %d, want %d", lastTotal, len(payload))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tag constant values — wire protocol stability
// These values are fixed forever. Changing them breaks existing clients.
// ─────────────────────────────────────────────────────────────────────────────

func TestTagConstants_WireValues(t *testing.T) {
	cases := []struct {
		name string
		got  byte
		want byte
	}{
		{"TagFileHeader", TagFileHeader, 0x01},
		{"TagChunk", TagChunk, 0x02},
		{"TagChunkAck", TagChunkAck, 0x03},
		{"TagTransferDone", TagTransferDone, 0x04},
		{"TagTransferOK", TagTransferOK, 0x05},
		{"TagTransferError", TagTransferError, 0x06},
		{"TagResumeFrom", TagResumeFrom, 0x07},
		{"TagCancelled", TagCancelled, 0x08},
		{"TagMessage", TagMessage, 0x09},
		{"TagChatClose", TagChatClose, 0x0A},
		{"TagParticipantLeave", TagParticipantLeave, 0x0B},
		{"TagSessionReady", TagSessionReady, 0x0C},
		{"TagTransferAnnounce", TagTransferAnnounce, 0x0D},
		{"TagTransferAccepted", TagTransferAccepted, 0x0E},
		{"TagSessionClose", TagSessionClose, 0x0F},
		{"TagPeerCount", TagPeerCount, 0x10},
		{"TagRelayedMessage", TagRelayedMessage, 0x11},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = 0x%02x, want 0x%02x — wire protocol broken!", tc.name, tc.got, tc.want)
		}
	}
}
