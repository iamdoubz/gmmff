// Package peerconfig defines the peer connection configuration type shared
// between the peer and session packages.  It is a separate package solely to
// avoid the import cycle that would result from session importing peer.
package peerconfig

import "github.com/iamdoubz/gmmff/internal/turn"

// DefaultSTUN is the default STUN server URL.
const DefaultSTUN = "stun:stun.l.google.com:19302"

// DefaultSTUNServers is the slice form of the default,
// used when STUNServers is empty.
var DefaultSTUNServers = []string{DefaultSTUN}

// Config holds optional WebRTC and signaling tuning parameters.
type Config struct {
	// STUNServers is the list of STUN/STUNS URLs to use for ICE negotiation.
	// Each entry must begin with "stun:" or "stuns:".
	// Defaults to DefaultSTUNServers when empty.
	STUNServers []string

	// TURNServers is the list of pre-parsed TURN server entries.
	// Use turn.ParseAll to convert raw flag strings into this slice.
	TURNServers []turn.Server

	// WindowSize is the number of chunks that may be in flight simultaneously.
	// Defaults to transfer.DefaultWindowSize (2) when zero.
	WindowSize int

	// ChunkSize is the number of bytes per chunk.
	// Defaults to transfer.DefaultChunkSize (65526) when zero.
	ChunkSize int
}
