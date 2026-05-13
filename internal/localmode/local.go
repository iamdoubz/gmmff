//go:build !js

// Package localmode implements gmmff's self-contained local-network mode.
// All components (broker, web server, mDNS, TLS) run in the same process.
package localmode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iamdoubz/gmmff/internal/broker"
	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/peerconfig"
	"github.com/iamdoubz/gmmff/internal/protocol"
	"github.com/iamdoubz/gmmff/internal/session"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/store"
	"github.com/mdp/qrterminal/v3"
)

// Config holds all settings for local mode.
type Config struct {
	Port     int  // 0 = pick a random available port
	NoTLS    bool // skip TLS (plain HTTP)
	MaxPeers int  // 2-10, default 2
	PeerCfg  peerconfig.Config
}

// Run starts local mode and blocks until the user quits or context is cancelled.
// This is the entry point called by cmd/gmmff/local.go.
func Run(cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── 1. Discover other gmmff peers before we start ────────────────────────
	fmt.Println("Scanning for other gmmff instances on the local network...")
	peers, err := DiscoverPeers(ctx, 2*time.Second)
	if err == nil && len(peers) > 0 {
		fmt.Printf("  Found %d gmmff instance(s):\n", len(peers))
		for _, p := range peers {
			fmt.Printf("    %s://%s\n", p.Scheme, p.Addr)
		}
	} else {
		fmt.Println("  No other gmmff instances found.")
	}
	fmt.Println()

	// ── 2. TLS certificate ────────────────────────────────────────────────────
	var certPaths CertPaths
	var certCleanup func()
	scheme := "http"
	wsScheme := "ws"

	if !cfg.NoTLS {
		fmt.Println("Generating self-signed TLS certificate...")
		certPaths, certCleanup, err = GenerateSelfSignedCert()
		if err != nil {
			return fmt.Errorf("local: TLS: %w", err)
		}
		defer certCleanup()
		scheme = "https"
		wsScheme = "wss"
		fmt.Printf("  Certificate: %s\n", certPaths.CertFile)
		fmt.Println("  Note: browsers will show a security warning for self-signed certs.")
		fmt.Println("  Mobile users: tap Advanced → Proceed to connect.")
		fmt.Println()
	}

	// ── 3. Pick a port ───────────────────────────────────────────────────────
	port := cfg.Port
	if port == 0 {
		port, err = findFreePort()
		if err != nil {
			return fmt.Errorf("local: find port: %w", err)
		}
	}

	// ── 4. Start the embedded broker ─────────────────────────────────────────
	st := store.NewMemStore()
	b := broker.New(st)

	staticFS, err := StaticFS()
	if err != nil {
		return fmt.Errorf("local: embedded assets: %w", err)
	}

	srv := broker.NewServerWithFS(b, st, staticFS, false)

	addr := fmt.Sprintf(":%d", port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	serverErr := make(chan error, 1)
	go func() {
		if !cfg.NoTLS {
			serverErr <- httpServer.ListenAndServeTLS(certPaths.CertFile, certPaths.KeyFile)
		} else {
			serverErr <- httpServer.ListenAndServe()
		}
	}()

	// Give the server a moment to start.
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-serverErr:
		return fmt.Errorf("local: server failed to start: %w", err)
	default:
	}

	// ── 5. Register on mDNS ──────────────────────────────────────────────────
	mdnsSrv, err := RegisterMDNS(port, scheme)
	if err != nil {
		fmt.Printf("  Warning: mDNS registration failed: %v\n", err)
		fmt.Println("  Other gmmff instances won't discover this one automatically.")
	} else {
		defer mdnsSrv.Shutdown()
	}

	// ── 6. Connect to our own broker as a client ─────────────────────────────
	wsURL := fmt.Sprintf("%s://127.0.0.1:%d/ws", wsScheme, port)

	var sig *signaling.Client
	if !cfg.NoTLS {
		sig, err = signaling.ConnectInsecure(ctx, wsURL)
	} else {
		sig, err = signaling.Connect(ctx, wsURL)
	}
	if err != nil {
		return fmt.Errorf("local: connect to own broker: %w", err)
	}

	// Create a session slot.
	maxPeers := cfg.MaxPeers
	if maxPeers < 2 {
		maxPeers = 2
	}
	if err := sig.CreateSlot("files", maxPeers); err != nil {
		return fmt.Errorf("local: create slot: %w", err)
	}
	createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
	if err != nil {
		return fmt.Errorf("local: wait slot.created: %w", err)
	}
	var created protocol.SlotCreatedPayload
	if err := json.Unmarshal(createdMsg.Payload, &created); err != nil {
		return fmt.Errorf("local: decode slot.created: %w", err)
	}

	// ── 7. Build the QR code URL ──────────────────────────────────────────────
	// Determine the best local IP to put in the URL.
	localIP := getPreferredLocalIP()
	joinURL := fmt.Sprintf("%s://%s:%d/?code=%s&type=files&autoconnect=1&local=1",
		scheme, localIP, port, created.Code)
	serverURL := fmt.Sprintf("%s://%s:%d", scheme, localIP, port)

	// ── 8. Print the banner ───────────────────────────────────────────────────
	fmt.Printf("╔══════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  gmmff local mode                                    ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Server:   %-41s║\n", serverURL)
	fmt.Printf("║  Code:     %-41s║\n", created.Code)
	fmt.Printf("║  Join URL: %-41s║\n", truncate(joinURL, 41))
	fmt.Printf("╚══════════════════════════════════════════════════════╝\n")
	fmt.Println()

	// Print QR code or fall back to plain URL.
	fmt.Println("Scan this QR code to join:")
	if err := printQR(joinURL); err != nil {
		fmt.Printf("  (QR code unavailable — share this URL instead):\n  %s\n", joinURL)
	}
	fmt.Println()

	if !cfg.NoTLS {
		fmt.Println("  ⚠  Self-signed certificate: tap 'Advanced → Proceed' in your browser.")
		fmt.Println()
	}

	fmt.Println("Session REPL ready. Commands:")
	fmt.Println("  send <file|dir>   send file(s) to all peers")
	fmt.Println("  message <text>    send a text message")
	fmt.Println("  \\q                end session and shut down")
	fmt.Println()

	// ── 9. Start the session ─────────────────────────────────────────────────
	sess, err := peer.StartSession(ctx, sig, created.Code, cfg.PeerCfg, maxPeers)
	if err != nil {
		return fmt.Errorf("local: start session: %w", err)
	}
	sess.OutDir = "."

	// ── 10. Run the session REPL ─────────────────────────────────────────────
	go runLocalEvents(sess)
	go sess.Run()

	replErr := runLocalREPL(ctx, sess, stop)

	// ── 11. Shutdown ─────────────────────────────────────────────────────────
	fmt.Println("\nShutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)

	if !cfg.NoTLS {
		certCleanup()
		certCleanup = func() {} // prevent double-call from defer
	}

	return replErr
}

// ─────────────────────────────────────────────────────────────────────────────
// Local REPL — simplified version of the session REPL for local mode
// ─────────────────────────────────────────────────────────────────────────────

func runLocalREPL(ctx context.Context, sess *session.Session, stop context.CancelFunc) error {
	lineCh := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	fmt.Print("> ")
	for {
		select {
		case <-ctx.Done():
			sess.Close()
			return nil
		case line, ok := <-lineCh:
			if !ok {
				sess.Close()
				return nil
			}
			line = strings.TrimSpace(line)
			if line == `\q` {
				sess.Close()
				fmt.Println("Session ended. Shutting down.")
				stop()
				return nil
			}
			if strings.HasPrefix(line, "send ") {
				args := strings.Fields(strings.TrimPrefix(line, "send "))
				if len(args) == 0 {
					fmt.Println("Usage: send <file|dir> [file|dir ...]")
				} else {
					go sendLocalFiles(ctx, sess, args)
				}
			} else if strings.HasPrefix(line, "message ") {
				text := strings.TrimSpace(strings.TrimPrefix(line, "message "))
				if text == "" {
					fmt.Println("Usage: message <text>")
				} else if err := sess.SendMessage(text); err != nil {
					fmt.Printf("Error sending message: %v\n", err)
				}
			} else if line == "help" {
				fmt.Println("  send <file|dir>   send file(s) to all peers")
				fmt.Println("  message <text>    send a text message")
				fmt.Println("  \\q                end session and shut down")
			} else if line != "" {
				fmt.Printf("Unknown command %q — type 'help'\n", line)
			}
			fmt.Print("> ")
		}
	}
}

func runLocalEvents(sess *session.Session) {
	for ev := range sess.Events {
		switch ev.Type {
		case session.EventMessage:
			fmt.Printf("\r\033[KParticipant: %s\n> ", ev.Message)
		case session.EventTransferStarted:
			fmt.Printf("\r\033[KParticipant is sending a file (%.1f MB)...\n", float64(ev.Total)/1024/1024)
		case session.EventTransferProgress:
			if ev.Total > 0 {
				pct := int(float64(ev.Done) / float64(ev.Total) * 100)
				bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
				fmt.Printf("\r  %s %d%%  %s / %s",
					bar, pct,
					formatBytes(ev.Done), formatBytes(ev.Total))
			}
		case session.EventTransferDone:
			fmt.Print("\r\033[K")
			if ev.Message != "" {
				for _, line := range strings.Split(ev.Message, "\n") {
					fmt.Printf("%s\n", line)
				}
			}
		case session.EventPeerJoined:
			fmt.Printf("\r\033[KParticipant joined (%d/%d)\n", ev.PeerCount, ev.MaxPeers)
		case session.EventPeerLeft:
			fmt.Printf("\r\033[K%s (%d/%d)\n", ev.Message, ev.PeerCount, ev.MaxPeers)
		case session.EventSessionClosed:
			fmt.Printf("\r\033[K%s\n", ev.Message)
		case session.EventError:
			fmt.Printf("\r\033[KError: %s\n", ev.Message)
		}
	}
}

func sendLocalFiles(ctx context.Context, sess *session.Session, args []string) {
	// Import archive package inline to avoid circular deps.
	// We call the same Prepare + SendFile pattern as create.go.
	fmt.Printf("Preparing %s...\n", strings.Join(args, ", "))
	// Use the session's SendFile directly with the first arg for simplicity.
	// Multi-file: the archive package is used in create.go — replicate here.
	if len(args) == 1 {
		done := sess.SendFile(args[0], "", func(sent, total int64) {
			if total > 0 {
				pct := int(float64(sent) / float64(total) * 100)
				bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
				fmt.Printf("\r  %s %d%%  %s / %s",
					bar, pct, formatBytes(sent), formatBytes(total))
			}
		})
		select {
		case <-ctx.Done():
		case err := <-done:
			if err != nil {
				fmt.Printf("\r\033[KTransfer error: %v\n> ", err)
			} else {
				fmt.Printf("\r\033[KTransfer complete.\n> ")
			}
		}
	} else {
		fmt.Println("Multi-file: use the send command with a directory, or send files one at a time.")
		fmt.Print("> ")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func findFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func getPreferredLocalIP() string {
	// Try to find a non-loopback IPv4 address.
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}

func printQR(url string) error {
	defer func() {
		if r := recover(); r != nil {
			// qrterminal can panic on some terminals; treat as fallback.
		}
	}()
	config := qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     os.Stdout,
		HalfBlocks: true,
		BlackChar:  qrterminal.BLACK,
		WhiteChar:  qrterminal.WHITE,
		QuietZone:  1,
	}
	qrterminal.GenerateWithConfig(url, config)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
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
