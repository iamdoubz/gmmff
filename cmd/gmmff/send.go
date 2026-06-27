package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
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
	recursive   bool
}

var sendCmd = &cobra.Command{
	Use:   "send <file|dir> [file|dir ...]",
	Short: "Send file(s) to a peer and exit — prints a code for the receiver",
	Long: `One-off file transfer. Creates a session, waits for a peer to join,
sends the specified file(s), and exits once the transfer is verified.

The receiver can use 'gmmff join <code>' or the web UI to receive.

Glob patterns are expanded by gmmff itself, so they work even on shells that
don't expand them (e.g. Windows PowerShell). Use -r to match recursively.

Examples:
  gmmff send report.pdf
  gmmff send photos/
  gmmff send '*.txt'              # all .txt files in the current dir
  gmmff send -r '*.go'           # all .go files in this dir and below
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
	f.BoolVarP(&sendCfg.recursive, "recursive", "r", false,
		"Match glob patterns recursively (e.g. '*.txt' in all subdirectories)")
}

// expandArgs turns glob patterns in args into concrete paths. Literal paths
// that already exist are kept untouched, so non-glob args (and dirs) behave as
// before. With recursive, patterns match against filenames at any depth.
func expandArgs(args []string, recursive bool) ([]string, error) {
	var out []string
	for _, a := range args {
		if _, err := os.Stat(a); err == nil {
			out = append(out, a) // literal existing path
			continue
		}
		var matches []string
		var err error
		if recursive {
			matches, err = globRecursive(a)
		} else {
			matches, err = filepath.Glob(a)
		}
		if err != nil {
			return nil, fmt.Errorf("send: bad pattern %q: %w", a, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("send: no files match %q", a)
		}
		out = append(out, matches...)
	}
	return out, nil
}

// globRecursive walks the pattern's directory and matches the filename part of
// the pattern against every file at any depth below it.
func globRecursive(pattern string) ([]string, error) {
	dir, base := filepath.Dir(pattern), filepath.Base(pattern)
	var matches []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ok, err := filepath.Match(base, d.Name())
		if err != nil {
			return err // ErrBadPattern
		}
		if ok {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func runSend(_ *cobra.Command, args []string) error {
	// Expand glob patterns ourselves so they work on shells that don't (e.g.
	// PowerShell). Already-expanded args from POSIX shells pass through.
	args, err := expandArgs(args, sendCfg.recursive)
	if err != nil {
		return err
	}

	// Validate all paths exist before connecting.
	for _, p := range args {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("send: cannot access %q: %w", p, err)
		}
	}

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

	progress := func(sent, total int64) {
		if total > 0 {
			pct := min(int(float64(sent)/float64(total)*100), 100)
			bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
			fmt.Printf("\r  %s %d%%  %s / %s",
				bar, pct,
				display.FormatBytes(sent),
				display.FormatBytes(total))
		}
	}

	// A single regular file streams straight from disk. Multiple files or a
	// directory are zipped on the fly into the wire — no temp archive on disk,
	// so a 7GB send never has to fit in /tmp.
	var done <-chan error
	if single := len(args) == 1 && !isDir(args[0]); single {
		done = sess.SendFile(args[0], sendCfg.message, progress)
	} else {
		paths := args
		done = sess.SendFileStream(archive.Name(paths), func(w io.Writer) error {
			return archive.WriteZip(w, paths)
		}, sendCfg.message, progress)
	}

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
