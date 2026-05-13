package main

import (
	"github.com/iamdoubz/gmmff/internal/localmode"
	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/spf13/cobra"
)

var localCfg struct {
	port     int
	noTLS    bool
	maxPeers int
}

var localCmd = &cobra.Command{
	Use:   "local",
	Short: "Self-contained local-network mode — no internet required",
	Long: `Start a fully self-contained gmmff session on your local network.

gmmff local starts an embedded signaling server, serves the browser UI,
registers on mDNS so other gmmff instances can find it, and immediately
opens a session. Scan the QR code with any mobile device to join.

No internet connection is required. WebRTC uses direct LAN IP addresses
(host candidates) — no STUN or TURN servers are contacted.

By default a self-signed TLS certificate is generated automatically so
WebRTC works in all browsers including Safari. Pass --no-tls for plain
HTTP (Chrome and Firefox only — Safari requires HTTPS for WebRTC).

The session ends and the server shuts down when you type \q.`,
	Args: cobra.NoArgs,
	RunE: runLocal,
}

func init() {
	rootCmd.AddCommand(localCmd)

	f := localCmd.Flags()
	f.IntVar(&localCfg.port, "port", 0,
		"Port to listen on (default: random available port)")
	f.BoolVar(&localCfg.noTLS, "no-tls", false,
		"Disable TLS — use plain HTTP (Chrome/Firefox only; Safari requires HTTPS for WebRTC)")
	f.IntVar(&localCfg.maxPeers, "max-peers", 2,
		"Maximum number of participants (2-10, including yourself)")
}

func runLocal(_ *cobra.Command, _ []string) error {
	maxPeers := localCfg.maxPeers
	if maxPeers < 2 {
		maxPeers = 2
	}
	if maxPeers > 10 {
		maxPeers = 10
	}

	return localmode.Run(localmode.Config{
		Port:     localCfg.port,
		NoTLS:    localCfg.noTLS,
		MaxPeers: maxPeers,
		PeerCfg: peer.Config{
			LocalMode: true, // host candidates only — no internet needed
		},
	})
}
