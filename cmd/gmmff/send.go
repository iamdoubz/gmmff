package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/spf13/cobra"
)

var sendCfg struct {
	serverURL  string
	stunServer string
	window     int
	chunkSize  int
}

var sendCmd = &cobra.Command{
	Use:   "send <file>",
	Short: "Send a file — prints a one-time code for the receiver",
	Args:  cobra.ExactArgs(1),
	RunE:  runSend,
}

func init() {
	rootCmd.AddCommand(sendCmd)

	f := sendCmd.Flags()
	f.StringVar(&sendCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringVar(&sendCfg.stunServer, "stun", envOr("GMMFF_STUN", peer.DefaultSTUN),
		"STUN server URL (GMMFF_STUN)")
	f.IntVar(&sendCfg.window, "window", transfer.DefaultWindowSize,
		"Sliding window size — chunks in flight simultaneously (min 1)")
	f.IntVar(&sendCfg.chunkSize, "chunk-size", transfer.DefaultChunkSize,
		"Chunk size in bytes (default 65526 — SCTP max; min 1)")
}

func runSend(_ *cobra.Command, args []string) error {
	filePath := args[0]
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("cannot access file %q: %w", filePath, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Connect to signaling server ──────────────────────────────────────────
	fmt.Printf("Connecting to signaling server %s...\n", sendCfg.serverURL)
	sig, err := signaling.Connect(ctx, sendCfg.serverURL)
	if err != nil {
		return fmt.Errorf("send: connect: %w", err)
	}

	// ── Create slot, get code ────────────────────────────────────────────────
	if err := sig.CreateSlot(); err != nil {
		return fmt.Errorf("send: create slot: %w", err)
	}
	createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
	if err != nil {
		return fmt.Errorf("send: wait slot.created: %w", err)
	}
	var created protocol.SlotCreatedPayload
	if err := json.Unmarshal(createdMsg.Payload, &created); err != nil {
		return fmt.Errorf("send: decode slot.created: %w", err)
	}

	// ── Print the code — the only thing the user needs to share ─────────────
	fmt.Printf("\n")
	fmt.Printf("  ╔══════════════════════════════════════╗\n")
	fmt.Printf("  ║  Share this code with the receiver:  ║\n")
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║    %-36s║\n", created.Code)
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║  Expires in %d minutes               ║\n", created.TTLSeconds/60)
	fmt.Printf("  ╚══════════════════════════════════════╝\n")
	fmt.Printf("\n  Run on the other machine:\n")
	fmt.Printf("    gmmff receive %s\n\n", created.Code)

	// ── Wait for receiver to join ────────────────────────────────────────────
	fmt.Println("Waiting for receiver to connect...")
	_, err = sig.WaitFor(ctx, protocol.MsgSlotReady)
	if err != nil {
		return fmt.Errorf("send: wait slot.ready: %w", err)
	}

	// ── Run the full send flow ───────────────────────────────────────────────
	cfg := peer.Config{STUNServer: sendCfg.stunServer, WindowSize: sendCfg.window, ChunkSize: sendCfg.chunkSize}
	return peer.Send(ctx, sig, created.Code, filePath, cfg)
}
