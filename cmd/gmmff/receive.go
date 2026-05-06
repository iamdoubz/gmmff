package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/spf13/cobra"
)

var receiveCfg struct {
	serverURL  string
	stunServer string
	outDir     string
}

var receiveCmd = &cobra.Command{
	Use:   "receive <code>",
	Short: "Receive a file using the code printed by the sender",
	Args:  cobra.ExactArgs(1),
	RunE:  runReceive,
}

func init() {
	rootCmd.AddCommand(receiveCmd)

	f := receiveCmd.Flags()
	f.StringVar(&receiveCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringVar(&receiveCfg.stunServer, "stun", envOr("GMMFF_STUN", peer.DefaultSTUN),
		"STUN server URL (GMMFF_STUN)")
	f.StringVarP(&receiveCfg.outDir, "out", "o", ".",
		"Directory to save the received file")
}

func runReceive(_ *cobra.Command, args []string) error {
	code := args[0]

	if err := os.MkdirAll(receiveCfg.outDir, 0o755); err != nil {
		return fmt.Errorf("receive: create output directory: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Connect to signaling server ──────────────────────────────────────────
	fmt.Printf("Connecting to signaling server %s...\n", receiveCfg.serverURL)
	sig, err := signaling.Connect(ctx, receiveCfg.serverURL)
	if err != nil {
		return fmt.Errorf("receive: connect: %w", err)
	}

	// ── Join the slot ────────────────────────────────────────────────────────
	if err := sig.JoinSlot(code); err != nil {
		return fmt.Errorf("receive: join slot: %w", err)
	}
	_, err = sig.WaitFor(ctx, protocol.MsgSlotReady)
	if err != nil {
		return fmt.Errorf("receive: wait slot.ready: %w", err)
	}

	// ── Run the full receive flow ─────────────────────────────────────────────
	cfg := peer.Config{STUNServer: receiveCfg.stunServer}
	outDir := peer.DefaultOutDir(receiveCfg.outDir)
	return peer.Receive(ctx, sig, code, outDir, cfg)
}
