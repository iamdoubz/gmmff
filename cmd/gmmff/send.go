package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamdoubz/gmmff/internal/archive"
	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/spf13/cobra"
)

var sendCfg struct {
	serverURL  string
	stunServers []string
	window     int
	chunkSize  int
	message    string
}

var sendCmd = &cobra.Command{
	Use:   "send <file|dir> [file|dir ...]",
	Short: "Send one or more files — prints a one-time code for the receiver",
	Long: `Send one or more files or directories to another gmmff peer.

A single regular file is sent as-is.
A single directory or multiple paths are zipped on the fly into a single
archive — the receiver gets one .zip file with everything inside.

Use --message / -m to attach a text message alongside the transfer.
With a single file the message is delivered as a printed note.
With multiple files the message is injected as message.txt in the zip.

Examples:
  gmmff send photo.jpg
  gmmff send report.pdf data.csv
  gmmff send ./project-folder
  gmmff send notes.txt -m "Here is the context"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSend,
}

func init() {
	rootCmd.AddCommand(sendCmd)

	f := sendCmd.Flags()
	f.StringVar(&sendCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringArrayVar(&sendCfg.stunServers, "stun", stunServersDefault(),
		"STUN/STUNS server URL (repeatable, e.g. --stun stun:h1:3478 --stun stuns:h2:5349); env GMMFF_STUN accepts comma-separated list")
	f.IntVar(&sendCfg.window, "window", transfer.DefaultWindowSize,
		"Sliding window size — chunks in flight simultaneously (min 1)")
	f.IntVar(&sendCfg.chunkSize, "chunk-size", transfer.DefaultChunkSize,
		"Chunk size in bytes (default 65526 — SCTP max; min 1)")
	f.StringVarP(&sendCfg.message, "message", "m", "",
		"Attach a text message to the transfer")
}

func runSend(_ *cobra.Command, args []string) error {
	// ── Prepare the file(s) to send ──────────────────────────────────────────
	result, err := archive.Prepare(args)
	if err != nil {
		return err
	}
	defer result.Cleanup()

	if result.IsTemp {
		fmt.Printf("Archiving %s → %s\n", archive.Summary(args), result.Name)
	} else {
		fmt.Printf("Sending %s\n", archive.Summary(args))
	}
	if sendCfg.message != "" {
		fmt.Printf("Message: %s\n", sendCfg.message)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Connecting to signaling server %s...\n", sendCfg.serverURL)
	sig, err := signaling.Connect(ctx, sendCfg.serverURL)
	if err != nil {
		return fmt.Errorf("send: connect: %w", err)
	}

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

	fmt.Printf("\n")
	fmt.Printf("  ╔══════════════════════════════════════╗\n")
	fmt.Printf("  ║  Share this code with the receiver:  ║\n")
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║    %-34s║\n", created.Code)
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║  Expires in %d minutes               ║\n", created.TTLSeconds/60)
	fmt.Printf("  ╚══════════════════════════════════════╝\n")
	fmt.Printf("\n  Run on the other machine:\n")
	fmt.Printf("    gmmff receive %s\n\n", created.Code)

	fmt.Println("Waiting for receiver to connect...")
	_, err = sig.WaitFor(ctx, protocol.MsgSlotReady)
	if err != nil {
		return fmt.Errorf("send: wait slot.ready: %w", err)
	}

	cfg := peer.Config{
		STUNServers: sendCfg.stunServers,
		WindowSize: sendCfg.window,
		ChunkSize:  sendCfg.chunkSize,
	}
	if err := peer.Send(ctx, sig, created.Code, result.Path, cfg, sendCfg.message, result.IsTemp); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}
