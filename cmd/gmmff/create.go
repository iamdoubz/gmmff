package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iamdoubz/gmmff/internal/archive"
	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/session"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/spf13/cobra"
)

var createCfg struct {
	serverURL   string
	stunServers []string
	turnServers []string
	outDir      string
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Start a bidirectional file + message session — prints a code for the other party",
	Long: `Start a secure peer-to-peer session.

Once connected, both sides can transfer files and send messages.
Type 'help' at the prompt to see available commands.

Examples:
  gmmff create
  gmmff create --server wss://your-server/ws`,
	Args: cobra.NoArgs,
	RunE: runCreate,
}

func init() {
	rootCmd.AddCommand(createCmd)

	f := createCmd.Flags()
	f.StringVar(&createCfg.serverURL, "server", envOr("GMMFF_SERVER", "ws://localhost:8080/ws"),
		"Signaling server WebSocket URL (GMMFF_SERVER)")
	f.StringArrayVar(&createCfg.stunServers, "stun", stunServersDefault(),
		"STUN/STUNS server URL, repeatable (GMMFF_STUN)")
	f.StringArrayVar(&createCfg.turnServers, "turn", turnServersDefault(),
		"TURN server, repeatable — format: turn:host:port?[transport=...&][user=u&pass=p|secret=s] (GMMFF_TURN)")
	f.StringVarP(&createCfg.outDir, "out", "o", ".", "Directory to save received files")
}

func runCreate(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Connecting to signaling server %s...\n", createCfg.serverURL)
	sig, err := signaling.Connect(ctx, createCfg.serverURL)
	if err != nil {
		return fmt.Errorf("create: connect: %w", err)
	}

	if err := sig.CreateSlot("files"); err != nil {
		return fmt.Errorf("create: create slot: %w", err)
	}
	createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
	if err != nil {
		return fmt.Errorf("create: wait slot.created: %w", err)
	}
	var created protocol.SlotCreatedPayload
	if err := json.Unmarshal(createdMsg.Payload, &created); err != nil {
		return fmt.Errorf("create: decode slot.created: %w", err)
	}

	fmt.Printf("\n")
	fmt.Printf("  ╔══════════════════════════════════════╗\n")
	fmt.Printf("  ║  Share this code with the other side ║\n")
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
		return fmt.Errorf("create: wait slot.ready: %w", err)
	}

	turnSrvs, err := parseTURNServers(createCfg.turnServers)
	if err != nil {
		return err
	}
	cfg := peer.Config{STUNServers: createCfg.stunServers, TURNServers: turnSrvs}

	sess, err := peer.StartSession(ctx, sig, created.Code, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	sess.OutDir = createCfg.outDir
	go sess.Run()
	return runSessionREPL(ctx, sess, stop)
}

// ─────────────────────────────────────────────────────────────────────────────
// Session REPL (shared by create and join)
// ─────────────────────────────────────────────────────────────────────────────

func runSessionREPL(ctx context.Context, sess *session.Session, stop context.CancelFunc) error {
	// Print incoming events in a goroutine.
	go func() {
		for ev := range sess.Events {
			printSessionEvent(ev)
		}
	}()

	isInitiator := sess.IsInitiator()

	fmt.Println("Session ready. Commands:")
	fmt.Println("  send <file|dir> [file|dir ...]   send file(s) to peer")
	fmt.Println("  message <text>                   send a text message")
	fmt.Println("  chat                             open interactive chat sub-session")
	if isInitiator {
		fmt.Println("  \\q                               end session for everyone")
	} else {
		fmt.Println("  \\q or Ctrl+C                     leave session")
	}
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("> ")
			continue
		}

		// Check context before processing
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := handleSessionCommand(ctx, sess, line, isInitiator, stop); err != nil {
			if errors.Is(err, errSessionEnded) {
				return nil
			}
			fmt.Printf("Error: %v\n", err)
		}
		fmt.Print("> ")
	}
	// EOF on stdin — leave quietly
	sess.Leave()
	return nil
}

var errSessionEnded = errors.New("session ended")

func handleSessionCommand(ctx context.Context, sess *session.Session, line string, isInitiator bool, stop context.CancelFunc) error {
	switch {
	case line == `\q`:
		if isInitiator {
			sess.Close()
			fmt.Println("Session ended.")
		} else {
			sess.Leave()
			fmt.Println("Left session.")
		}
		stop()
		return errSessionEnded

	case line == "chat":
		return runChatSubsession(ctx, sess)

	case strings.HasPrefix(line, "message "):
		text := strings.TrimPrefix(line, "message ")
		text = strings.TrimSpace(text)
		// Strip surrounding quotes if present
		if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
			text = text[1 : len(text)-1]
		}
		if text == "" {
			fmt.Println("Usage: message <text>")
			return nil
		}
		return sess.SendMessage(text)

	case strings.HasPrefix(line, "send "):
		args := strings.Fields(strings.TrimPrefix(line, "send "))
		if len(args) == 0 {
			fmt.Println("Usage: send <file|dir> [file|dir ...]")
			return nil
		}
		return sendFilesInSession(ctx, sess, args)

	case line == "help":
		fmt.Println("  send <file|dir> [file|dir ...]   send file(s) to peer")
		fmt.Println("  message <text>                   send a text message")
		fmt.Println("  chat                             open interactive chat sub-session")
		if isInitiator {
			fmt.Println("  \\q                               end session for everyone")
		} else {
			fmt.Println("  \\q or Ctrl+C                     leave session")
		}
		return nil

	default:
		fmt.Printf("Unknown command %q — type 'help' for available commands\n", line)
		return nil
	}
}

func sendFilesInSession(ctx context.Context, sess *session.Session, args []string) error {
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

	done := sess.SendFile(result.Path, "", makeSessionProgressFn())
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
		fmt.Printf("\r\033[KTransfer complete.\n")
	}
	return nil
}

func runChatSubsession(ctx context.Context, sess *session.Session) error {
	fmt.Println("Chat mode. Type messages, \\q to return to session.")
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("chat> ")
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == `\q` {
			fmt.Println("Returning to session.")
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if line != "" {
			if err := sess.SendMessage(line); err != nil {
				return err
			}
		}
		fmt.Print("chat> ")
	}
	return nil
}

func printSessionEvent(ev session.Event) {
	switch ev.Type {
	case session.EventMessage:
		fmt.Printf("\r\033[KParticipant: %s\n> ", ev.Message)
	case session.EventTransferStarted:
		fmt.Printf("\r\033[KParticipant is sending a file (%.1f MB)...\n> ", float64(ev.Total)/1024/1024)
	case session.EventTransferDone:
		if ev.Message != "" {
			for _, line := range strings.Split(ev.Message, "\n") {
				fmt.Printf("\r\033[K%s\n", line)
			}
		}
		fmt.Print("> ")
	case session.EventPeerLeft:
		fmt.Printf("\r\033[K%s\n> ", ev.Message)
	case session.EventSessionClosed:
		fmt.Printf("\r\033[K%s\n", ev.Message)
	case session.EventError:
		fmt.Printf("\r\033[KError: %s\n> ", ev.Message)
	}
}

func makeSessionProgressFn() func(done, total int64) {
	return func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(float64(done) / float64(total) * 100)
		bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
		fmt.Printf("\r  %s %d%%  %s / %s",
			bar, pct,
			formatBytes(done),
			formatBytes(total))
	}
}

func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
