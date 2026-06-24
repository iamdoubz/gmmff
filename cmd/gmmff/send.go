package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iamdoubz/gmmff/v2/internal/archive"
	"github.com/iamdoubz/gmmff/v2/internal/display"
	"github.com/iamdoubz/gmmff/v2/internal/peer"
	"github.com/iamdoubz/gmmff/v2/internal/session"
	"github.com/iamdoubz/gmmff/v2/internal/signaling"
	"github.com/iamdoubz/gmmff/v2/pkg/protocol"
	"github.com/spf13/cobra"
)

var sendCfg struct {
	serverURL   string
	stunServers []string
	turnServers []string
	message     string
}

var sendCmd = &cobra.Command{
	Use:   "send <file|dir> [file|dir ...]",
	Short: "Send file(s) to a peer and exit — prints a code for the receiver",
	Long: `One-off file transfer. Creates a session, waits for a peer to join,
sends the specified file(s), and exits once the transfer is verified.

The receiver can use 'gmmff join <code>' or the web UI to receive.

Examples:
  gmmff send report.pdf
  gmmff send photos/
  gmmff send file1.txt file2.txt --message "both files"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSend,
}

func init() {
	rootCmd.AddCommand(sendCmd)

	f := sendCmd.Flags()
	f.StringVar(&sendCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringArrayVar(&sendCfg.stunServers, "stun", stunServersDefault(),
		"STUN/STUNS server URL, repeatable (GMMFF_STUN)")
	f.StringArrayVar(&sendCfg.turnServers, "turn", turnServersDefault(),
		"TURN server, repeatable (GMMFF_TURN)")
	f.StringVarP(&sendCfg.message, "message", "m", "", "Message to attach to the transfer")
}

func runSend(_ *cobra.Command, args []string) error {
	// Validate and prepare files before connecting.
	result, err := archive.Prepare(args)
	if err != nil {
		return err
	}
	defer result.Cleanup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Connecting to signaling server %s...\n", sendCfg.serverURL)
	sig, err := signaling.Connect(ctx, sendCfg.serverURL)
	if err != nil {
		return fmt.Errorf("send: connect: %w", err)
	}

	if err := sig.CreateSlot("files", 2); err != nil {
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
	fmt.Printf("  ║  Share this code with the receiver   ║\n")
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║    %-34s║\n", created.Code)
	fmt.Printf("  ║                                      ║\n")
	fmt.Printf("  ║  Expires in %d minutes               ║\n", created.TTLSeconds/60)
	fmt.Printf("  ╚══════════════════════════════════════╝\n")
	fmt.Printf("\n  Queued: %s\n", archive.Summary(args))
	fmt.Printf("  Waiting for receiver to join...\n\n")

	turnSrvs, err := parseTURNServers(sendCfg.turnServers)
	if err != nil {
		return err
	}
	cfg := peer.Config{STUNServers: sendCfg.stunServers, TURNServers: turnSrvs}

	sess, err := peer.StartSession(ctx, sig, created.Code, cfg, 2)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	go sess.Run()

	fmt.Printf("Receiver connected. Sending %s...\n", archive.Summary(args))

	done := sess.SendFile(result.Path, sendCfg.message, func(sent, total int64) {
		if total > 0 {
			pct := min(int(float64(sent)/float64(total)*100), 100)
			bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
			fmt.Printf("\r  %s %d%%  %s / %s",
				bar, pct,
				display.FormatBytes(sent),
				display.FormatBytes(total))
		}
	})

	// Wait for transfer result or session events.
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if err != nil {
				fmt.Printf("\r\033[KTransfer failed: %v\n", err)
				return err
			}
			fmt.Printf("\r\033[KTransfer complete and verified. Session closed.\n")
			sess.Close()
			return nil
		case ev, ok := <-sess.Events:
			if !ok {
				return nil
			}
			switch ev.Type {
			case session.EventSessionClosed:
				fmt.Printf("\r\033[K%s\n", ev.Message)
				return nil
			case session.EventError:
				fmt.Printf("\r\033[KError: %s\n", ev.Message)
			}
		}
	}
}
