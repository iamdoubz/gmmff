package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/spf13/cobra"
)

var chatCfg struct {
	serverURL  string
	stunServers []string
}

// ── chat (initiator) ──────────────────────────────────────────────────────────

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Start a symmetric chat session — prints a code for the other party",
	Long: `Open a secure peer-to-peer chat session.

The session stays open until either party types \q, the connection is lost,
or no message is sent or received for 10 minutes.

Examples:
  # Machine A — starts the session
  gmmff chat

  # Machine B — joins with the code
  gmmff join <code>`,
	Args: cobra.NoArgs,
	RunE: runChat,
}

func init() {
	rootCmd.AddCommand(chatCmd)

	f := chatCmd.Flags()
	f.StringVar(&chatCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringArrayVar(&chatCfg.stunServers, "stun", stunServersDefault(),
		"STUN/STUNS server URL (repeatable); env GMMFF_STUN accepts comma-separated list")
}

func runChat(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Connecting to signaling server %s...\n", chatCfg.serverURL)
	sig, err := signaling.Connect(ctx, chatCfg.serverURL)
	if err != nil {
		return fmt.Errorf("chat: connect: %w", err)
	}

	if err := sig.CreateSlot(); err != nil {
		return fmt.Errorf("chat: create slot: %w", err)
	}
	createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
	if err != nil {
		return fmt.Errorf("chat: wait slot.created: %w", err)
	}
	var created protocol.SlotCreatedPayload
	if err := json.Unmarshal(createdMsg.Payload, &created); err != nil {
		return fmt.Errorf("chat: decode slot.created: %w", err)
	}

	fmt.Printf("\n")
	fmt.Printf("  ╔══════════════════════════════════════╗\n")
	fmt.Printf("  ║   Share this code to start chatting: ║\n")
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║    %-34s║\n", created.Code)
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║  Expires in %d minutes               ║\n", created.TTLSeconds/60)
	fmt.Printf("  ╚══════════════════════════════════════╝\n")
	fmt.Printf("\n  Run on the other machine:\n")
	fmt.Printf("    gmmff join %s\n\n", created.Code)

	fmt.Println("Waiting for the other party to connect...")
	_, err = sig.WaitFor(ctx, protocol.MsgSlotReady)
	if err != nil {
		return fmt.Errorf("chat: wait slot.ready: %w", err)
	}

	cfg := peer.Config{STUNServers: chatCfg.stunServers}
	if err := peer.Chat(ctx, sig, created.Code, "Receiver", cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// ── join (responder) ──────────────────────────────────────────────────────────

var joinCmd = &cobra.Command{
	Use:   "join <code>",
	Short: "Join a chat session using the code printed by the other party",
	Args:  cobra.ExactArgs(1),
	RunE:  runJoin,
}

func init() {
	rootCmd.AddCommand(joinCmd)

	f := joinCmd.Flags()
	f.StringVar(&chatCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringArrayVar(&chatCfg.stunServers, "stun", stunServersDefault(),
		"STUN/STUNS server URL (repeatable); env GMMFF_STUN accepts comma-separated list")
}

func runJoin(_ *cobra.Command, args []string) error {
	code := args[0]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Connecting to signaling server %s...\n", chatCfg.serverURL)
	sig, err := signaling.Connect(ctx, chatCfg.serverURL)
	if err != nil {
		return fmt.Errorf("join: connect: %w", err)
	}

	if err := sig.JoinSlot(code); err != nil {
		return fmt.Errorf("join: join slot: %w", err)
	}
	_, err = sig.WaitFor(ctx, protocol.MsgSlotReady)
	if err != nil {
		return fmt.Errorf("join: wait slot.ready: %w", err)
	}

	cfg := peer.Config{STUNServers: chatCfg.stunServers}
	if err := peer.Chat(ctx, sig, code, "Sender", cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}
