package signaling

import (
	"encoding/base64"
	"fmt"
)

func encodeB64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func decodeB64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return b, nil
}
