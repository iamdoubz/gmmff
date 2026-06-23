package transfer

import (
	"encoding/json"
	"testing"
)

func TestReceiveStateMem_DoneBeforeHeader(t *testing.T) {
	r := NewReceiveStateMem(func(uint64) error { return nil })
	_, err := r.Feed([]byte{TagTransferDone})
	if err == nil {
		t.Fatal("expected error for done before header")
	}
}

func TestReceiveStateMem_UnknownTag(t *testing.T) {
	r := NewReceiveStateMem(func(uint64) error { return nil })
	done, err := r.Feed([]byte{0xFF, 0x01, 0x02})
	if err != nil || done {
		t.Errorf("unknown tag: err=%v, done=%v", err, done)
	}
}

func TestReceiveStateMem_AckError(t *testing.T) {
	data := []byte("ack-err")
	hash := sha256HexOf(data)
	hdr := buildFileHeaderFrame(t, FileHeader{
		Name:      "a.txt",
		Size:      int64(len(data)),
		ChunkSize: len(data),
		SHA256:    hash,
		Chunks:    1,
	})

	failAck := func(uint64) error { return json.Unmarshal([]byte("bad"), nil) }
	r := NewReceiveStateMem(failAck)
	r.Feed(hdr) //nolint:errcheck

	_, err := r.Feed(buildChunkFrame(0, data))
	if err != ErrCancelled {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}

func TestReceiveStateMem_ChunkTooShort(t *testing.T) {
	r := NewReceiveStateMem(func(uint64) error { return nil })
	payload := []byte("x")
	hdr := buildFileHeaderFrame(t, FileHeader{
		Name:      "f.txt",
		Size:      1,
		ChunkSize: 1,
		SHA256:    sha256HexOf(payload),
		Chunks:    1,
	})
	r.Feed(hdr) //nolint:errcheck

	// Chunk frame with only 3 bytes after tag (needs 8 for seq).
	shortFrame := []byte{TagChunk, 0x00, 0x01, 0x02}
	_, err := r.Feed(shortFrame)
	if err == nil {
		t.Fatal("expected error for short chunk frame")
	}
}
