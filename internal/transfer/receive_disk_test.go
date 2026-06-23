package transfer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func noopAckDisk(_ uint64) error { return nil }
func noopResume(_ uint64) error  { return nil }
func failResume(_ uint64) error  { return fmt.Errorf("resume send failed") }
func failAckDisk(_ uint64) error { return fmt.Errorf("ack failed") }

func tmpDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	return d
}

func makeHeader(name string, data []byte, chunkSize int) FileHeader {
	h := sha256.Sum256(data)
	chunks := int64(len(data)) / int64(chunkSize)
	if int64(len(data))%int64(chunkSize) != 0 {
		chunks++
	}
	return FileHeader{
		Name:      name,
		Size:      int64(len(data)),
		ChunkSize: chunkSize,
		SHA256:    fmt.Sprintf("%x", h),
		Chunks:    chunks,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fresh transfer: header → chunks → done
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveState_FreshTransfer_SingleChunk(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("hello disk receiver")
	hdr := makeHeader("test.txt", data, len(data))

	var acks []uint64
	ack := func(seq uint64) error { acks = append(acks, seq); return nil }
	rs := NewReceiveState(dir, ack, noopResume)

	if done, err := rs.Feed(buildFileHeaderFrame(t, hdr)); err != nil || done {
		t.Fatalf("header: done=%v err=%v", done, err)
	}
	if done, err := rs.Feed(buildChunkFrame(0, data)); err != nil || done {
		t.Fatalf("chunk: done=%v err=%v", done, err)
	}
	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if !done {
		t.Fatal("expected done=true")
	}

	// Final file should exist with correct content.
	got, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content mismatch: got %q", got)
	}

	// Partial and meta should be cleaned up.
	if _, err := os.Stat(filepath.Join(dir, "test.txt"+PartialSuffix)); !os.IsNotExist(err) {
		t.Error("partial file should be removed after successful transfer")
	}
	if _, err := os.Stat(filepath.Join(dir, "test.txt"+MetaSuffix)); !os.IsNotExist(err) {
		t.Error("meta file should be removed after successful transfer")
	}

	if len(acks) != 1 || acks[0] != 0 {
		t.Errorf("acks = %v, want [0]", acks)
	}
	if rs.OutputPath() != filepath.Join(dir, "test.txt") {
		t.Errorf("OutputPath = %q", rs.OutputPath())
	}
}

func TestReceiveState_FreshTransfer_MultiChunk(t *testing.T) {
	dir := tmpDir(t)
	part1 := []byte("first-chunk-")
	part2 := []byte("second-chunk")
	full := append(part1, part2...)
	hdr := makeHeader("multi.bin", full, len(part1))

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, part1))
	rs.Feed(buildChunkFrame(1, part2))

	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil || !done {
		t.Fatalf("done: err=%v done=%v", err, done)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "multi.bin"))
	if string(got) != string(full) {
		t.Errorf("content mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Hash mismatch
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveState_HashMismatch_LeavesPartial(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("real data")
	hdr := makeHeader("bad.txt", data, len(data))
	hdr.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, data))

	_, err := rs.Feed([]byte{TagTransferDone})
	if err == nil {
		t.Fatal("expected integrity error")
	}

	// Partial should still exist (not renamed to final).
	if _, err := os.Stat(filepath.Join(dir, "bad.txt"+PartialSuffix)); os.IsNotExist(err) {
		t.Error("partial file should be preserved on hash mismatch")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.txt")); !os.IsNotExist(err) {
		t.Error("final file should NOT exist on hash mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Resume from partial
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveState_Resume(t *testing.T) {
	dir := tmpDir(t)
	part1 := []byte("already-here-")
	part2 := []byte("new-data")
	full := append(part1, part2...)
	hdr := makeHeader("resume.bin", full, len(part1))

	// Pre-create the partial file with part1 content.
	partialPath := filepath.Join(dir, "resume.bin"+PartialSuffix)
	if err := os.WriteFile(partialPath, part1, 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the meta file matching part1.
	meta := PartialMeta{
		SHA256:     hdr.SHA256,
		ChunkSize:  hdr.ChunkSize,
		BytesDone:  int64(len(part1)),
		ChunksDone: 1,
	}
	metaBytes, _ := json.Marshal(meta)
	metaPath := filepath.Join(dir, "resume.bin"+MetaSuffix)
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var resumedSeq uint64
	resume := func(seq uint64) error { resumedSeq = seq; return nil }
	rs := NewReceiveState(dir, noopAckDisk, resume)

	// Feed header — should detect the partial and resume.
	if done, err := rs.Feed(buildFileHeaderFrame(t, hdr)); err != nil || done {
		t.Fatalf("header: done=%v err=%v", done, err)
	}
	if resumedSeq != 1 {
		t.Errorf("resumedSeq = %d, want 1", resumedSeq)
	}

	// Feed only the second chunk.
	if done, err := rs.Feed(buildChunkFrame(1, part2)); err != nil || done {
		t.Fatalf("chunk 1: done=%v err=%v", done, err)
	}

	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if !done {
		t.Fatal("expected done=true")
	}

	got, _ := os.ReadFile(filepath.Join(dir, "resume.bin"))
	if string(got) != string(full) {
		t.Errorf("content mismatch: got %q, want %q", got, full)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// checkPartial edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveState_NoMeta_FreshStart(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("fresh")
	hdr := makeHeader("fresh.txt", data, len(data))

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, data))

	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil || !done {
		t.Fatalf("err=%v done=%v", err, done)
	}
}

func TestReceiveState_MetaMismatch_FreshStart(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("new-data-here")
	hdr := makeHeader("mismatch.txt", data, len(data))

	// Create a meta file with a different SHA256.
	meta := PartialMeta{SHA256: "different-hash", ChunkSize: len(data), BytesDone: 5, ChunksDone: 1}
	metaBytes, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "mismatch.txt"+MetaSuffix), metaBytes, 0o644)
	os.WriteFile(filepath.Join(dir, "mismatch.txt"+PartialSuffix), []byte("old"), 0o644)

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, data))

	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil || !done {
		t.Fatalf("should succeed with fresh start: err=%v done=%v", err, done)
	}
}

func TestReceiveState_PartialSizeMismatch_FreshStart(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("complete-data")
	hdr := makeHeader("sizemis.txt", data, len(data))

	// Meta says 10 bytes done but partial file is only 3 bytes.
	meta := PartialMeta{SHA256: hdr.SHA256, ChunkSize: hdr.ChunkSize, BytesDone: 10, ChunksDone: 1}
	metaBytes, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "sizemis.txt"+MetaSuffix), metaBytes, 0o644)
	os.WriteFile(filepath.Join(dir, "sizemis.txt"+PartialSuffix), []byte("abc"), 0o644)

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, data))

	done, err := rs.Feed([]byte{TagTransferDone})
	if err != nil || !done {
		t.Fatalf("should fall through to fresh start: err=%v done=%v", err, done)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestReceiveState_EmptyFrame(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	done, err := rs.Feed([]byte{})
	if err != nil || done {
		t.Errorf("empty: err=%v done=%v", err, done)
	}
}

func TestReceiveState_ChunkBeforeHeader(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	_, err := rs.Feed(buildChunkFrame(0, []byte("data")))
	if err == nil {
		t.Fatal("expected error for chunk before header")
	}
}

func TestReceiveState_DoneBeforeHeader(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	_, err := rs.Feed([]byte{TagTransferDone})
	if err == nil {
		t.Fatal("expected error for done before header")
	}
}

func TestReceiveState_Cancelled(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	_, err := rs.Feed([]byte{TagCancelled})
	if err != ErrCancelled {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}

func TestReceiveState_TransferError(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	_, err := rs.Feed(BuildErrorFrame("ERR_DISK", "disk full"))
	if err == nil {
		t.Fatal("expected error from TransferError frame")
	}
}

func TestReceiveState_UnknownTag(t *testing.T) {
	rs := NewReceiveState(tmpDir(t), noopAckDisk, noopResume)
	done, err := rs.Feed([]byte{0xFF, 0x01})
	if err != nil || done {
		t.Errorf("unknown tag: err=%v done=%v", err, done)
	}
}

func TestReceiveState_AckError_ReturnsCancelled(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("ack-fail")
	hdr := makeHeader("ackfail.txt", data, len(data))

	rs := NewReceiveState(dir, failAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))

	_, err := rs.Feed(buildChunkFrame(0, data))
	if err != ErrCancelled {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	// Close the partial file handle so Windows can clean up the temp dir.
	if rs.f != nil {
		rs.f.Close()
	}
}

func TestReceiveState_ResumeError(t *testing.T) {
	dir := tmpDir(t)
	part1 := []byte("partial-data-")
	part2 := []byte("more")
	full := append(part1, part2...)
	hdr := makeHeader("reserr.bin", full, len(part1))

	// Seed partial + meta.
	os.WriteFile(filepath.Join(dir, "reserr.bin"+PartialSuffix), part1, 0o644)
	meta := PartialMeta{SHA256: hdr.SHA256, ChunkSize: hdr.ChunkSize, BytesDone: int64(len(part1)), ChunksDone: 1}
	metaBytes, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "reserr.bin"+MetaSuffix), metaBytes, 0o644)

	rs := NewReceiveState(dir, noopAckDisk, failResume)
	_, err := rs.Feed(buildFileHeaderFrame(t, hdr))
	if err == nil {
		t.Fatal("expected error when sendResume fails")
	}
}

func TestReceiveState_SanitisesFilename(t *testing.T) {
	dir := tmpDir(t)
	data := []byte("x")
	hdr := makeHeader("../../etc/passwd", data, len(data))

	rs := NewReceiveState(dir, noopAckDisk, noopResume)
	rs.Feed(buildFileHeaderFrame(t, hdr))
	rs.Feed(buildChunkFrame(0, data))
	rs.Feed([]byte{TagTransferDone})

	// Should not write outside the output dir.
	if _, err := os.Stat(filepath.Join(dir, "etcpasswd")); os.IsNotExist(err) {
		t.Error("sanitised file should exist in outDir")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Ack/Resume frame builders (not yet tested)
// ─────────────────────────────────────────────────────────────────────────────

func TestAckFrame_RoundTrip(t *testing.T) {
	for _, seq := range []uint64{0, 1, 42, 1<<32 - 1, 1<<63 - 1} {
		frame := BuildAckFrame(seq)
		got, err := ParseAckFrame(frame)
		if err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
		if got != seq {
			t.Errorf("seq %d: got %d", seq, got)
		}
	}
}

func TestParseAckFrame_Invalid(t *testing.T) {
	if _, err := ParseAckFrame([]byte{TagChunkAck}); err == nil {
		t.Error("expected error for short frame")
	}
	if _, err := ParseAckFrame([]byte{0xFF, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("expected error for wrong tag")
	}
}

// Silence the unused import lint — binary is used by buildChunkFrame in transfer_test.go
// but we also reference it indirectly through the shared test helpers.
var _ = binary.BigEndian
