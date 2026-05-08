// Package chat implements a symmetric bidirectional text chat session over a
// WebRTC data channel.
//
// Either peer can send messages at any time.  If no message is sent or
// received for IdleTimeout, the session closes automatically with a clean
// notification to the other side.  Either peer can also type \q to quit
// immediately.
package chat

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/pion/webrtc/v4"
)

// IdleTimeout is how long the session stays open with no activity.
const IdleTimeout = 10 * time.Minute

// QuitCommand is the text a user types to end the session.
const QuitCommand = `\q`

// Session manages a live chat session over a data channel.
type Session struct {
	dc       *webrtc.DataChannel
	role     string // "Sender" or "Receiver" — used in display prefix
	onMsg    func(from, text string) // called when a message arrives
	onClose  func(reason string)     // called when the session ends
}

// NewSession creates a Session.
//   - dc is the open WebRTC data channel.
//   - role is the display label for the remote peer ("Sender" or "Receiver").
//   - onMsg is called on every incoming message (may be nil — defaults to printing).
//   - onClose is called when the session ends for any reason.
func NewSession(dc *webrtc.DataChannel, role string, onMsg func(from, text string), onClose func(reason string)) *Session {
	if onMsg == nil {
		onMsg = func(from, text string) {
			fmt.Printf("\r\033[K%s: %s\n> ", from, text)
		}
	}
	if onClose == nil {
		onClose = func(reason string) {
			fmt.Println("\n" + reason)
		}
	}
	return &Session{dc: dc, role: role, onMsg: onMsg, onClose: onClose}
}

// RunCLI runs a blocking read-eval-print loop on stdin.
// Incoming messages arrive via the data channel OnMessage handler set here.
// Returns when the session ends (idle timeout, \q, remote close, or ctx cancel).
func (s *Session) RunCLI(ctx context.Context) error {
	done    := make(chan struct{})
	idle    := time.NewTimer(IdleTimeout)
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(IdleTimeout)
	}

	// Register incoming message handler.
	s.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		switch m.Data[0] {
		case transfer.TagMessage:
			text := transfer.ParseMessageFrame(m.Data)
			resetIdle()
			s.onMsg(s.role, text)
		case transfer.TagChatClose:
			s.onClose("Session closed by " + s.role + ".")
			close(done)
		case transfer.TagCancelled:
			s.onClose("Session closed by " + s.role + ".")
			close(done)
		}
	})

	fmt.Println("Chat session open. Type a message and press Enter to send.")
	fmt.Printf("Type %s to quit.  Session closes after %s of inactivity.\n\n", QuitCommand, IdleTimeout)

	// Read stdin in a goroutine.
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
			_ = s.dc.Send(transfer.BuildChatCloseFrame())
			s.onClose("Session cancelled.")
			return nil

		case <-idle.C:
			_ = s.dc.Send(transfer.BuildChatCloseFrame())
			s.onClose("Session closed — no activity for " + IdleTimeout.String() + ".")
			return nil

		case <-done:
			return nil

		case line, ok := <-lineCh:
			if !ok {
				// EOF on stdin.
				_ = s.dc.Send(transfer.BuildChatCloseFrame())
				s.onClose("Session closed.")
				return nil
			}
			line = strings.TrimSpace(line)
			if line == QuitCommand {
				_ = s.dc.Send(transfer.BuildChatCloseFrame())
				s.onClose("Session closed.")
				return nil
			}
			if line == "" {
				fmt.Print("> ")
				continue
			}
			if err := s.dc.Send(transfer.BuildMessageFrame(line)); err != nil {
				return fmt.Errorf("chat: send message: %w", err)
			}
			resetIdle()
			fmt.Print("> ")
		}
	}
}

// SendMessage sends a single message frame and returns.
// Used for the --message flag (one-shot, no REPL).
func SendMessage(dc *webrtc.DataChannel, text string) error {
	return dc.Send(transfer.BuildMessageFrame(text))
}
