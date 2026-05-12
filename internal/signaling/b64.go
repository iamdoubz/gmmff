package signaling

import (
	"encoding/base64"
	"fmt"
)

func encodeB64(data []byte) string {
	return EncodeB64(data)
}

func decodeB64(s string) ([]byte, error) {
	return DecodeB64(s)
}

// EncodeB64 encodes bytes to standard base64.
func EncodeB64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// DecodeB64 decodes standard base64 bytes.
func DecodeB64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return b, nil
}
