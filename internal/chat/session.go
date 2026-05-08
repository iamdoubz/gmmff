// Package chat implements a symmetric bidirectional text chat session over a
// WebRTC data channel.
//
// Protocol:
//
//	TagMessage          — normal text message (any participant)
//	TagChatClose        — initiator ends the session for everyone
//	TagParticipantLeave — one participant leaves quietly; session continues
//	TagCancelled        — connection-level cancel (treated as TagChatClose)
//
// CLI behaviour:
//
//	Initiator  \q      → sends TagChatClose (ends for everyone)
//	Initiator  Ctrl+C  → sends TagParticipantLeave (leaves quietly)
//	Responder  \q      → sends TagParticipantLeave (leaves quietly)
//	Responder  Ctrl+C  → sends TagParticipantLeave (leaves quietly)
//
// If no message is sent or received for IdleTimeout, the session closes with
// TagChatClose (idle timeout is treated as an initiator-level event).
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

// QuitCommand is the text a user types to quit.
// For the initiator this ends the session for everyone.
// For a responder this leaves quietly.
const QuitCommand = `\q`

// Session manages a live chat session over a data channel.
type Session struct {
	dc          *webrtc.DataChannel
	remoteLabel string            // display label for the remote peer
	isInitiator bool              // true if this peer started the session
	onMsg       func(from, text string)
	onClose     func(reason string) // session ended for everyone
	onLeave     func(who string)    // a participant left but session continues
}

// NewSession creates a Session.
//   - dc is the open WebRTC data channel.
//   - remoteLabel is the display name for the remote peer (e.g. "Participant").
//   - isInitiator distinguishes who can kill the session vs leave quietly.
//   - onMsg, onClose, onLeave may be nil — defaults print to stdout.
func NewSession(
	dc *webrtc.DataChannel,
	remoteLabel string,
	isInitiator bool,
	onMsg func(from, text string),
	onClose func(reason string),
	onLeave func(who string),
) *Session {
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
	if onLeave == nil {
		onLeave = func(who string) {
			fmt.Printf("\r\033[K%s has left the session.\n> ", who)
		}
	}
	return &Session{
		dc:          dc,
		remoteLabel: remoteLabel,
		isInitiator: isInitiator,
		onMsg:       onMsg,
		onClose:     onClose,
		onLeave:     onLeave,
	}
}

// RunCLI runs a blocking REPL on stdin.
// Returns when the local user quits, the session ends, or ctx is cancelled.
func (s *Session) RunCLI(ctx context.Context) error {
	done := make(chan struct{})
	idle := time.NewTimer(IdleTimeout)

	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(IdleTimeout)
	}

	// Register incoming frame handler.
	s.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		switch m.Data[0] {
		case transfer.TagMessage:
			resetIdle()
			s.onMsg(s.remoteLabel, transfer.ParseMessageFrame(m.Data))

		case transfer.TagChatClose, transfer.TagCancelled:
			// Session killed by the initiator — everyone must leave.
			s.onClose("Session ended by " + s.remoteLabel + ".")
			select {
			case <-done:
			default:
				close(done)
			}

		case transfer.TagParticipantLeave:
			// A participant left quietly; session continues.
			s.onLeave(s.remoteLabel)
			// For two-person chat this effectively ends the useful session,
			// but we leave it open so the initiator can wait for a new joiner
			// in a future multi-user implementation.
		}
	})

	fmt.Println("Chat session open. Type a message and press Enter to send.")
	if s.isInitiator {
		fmt.Printf("Type %s to end the session for everyone.  Ctrl+C to leave quietly.\n", QuitCommand)
	} else {
		fmt.Printf("Type %s or press Ctrl+C to leave.  Session stays open for others.\n", QuitCommand)
	}
	fmt.Printf("Session closes after %s of inactivity.\n\n", IdleTimeout)

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
			// Ctrl+C — always a quiet leave regardless of role.
			_ = s.dc.Send(transfer.BuildParticipantLeaveFrame())
			fmt.Println("\nLeft session.")
			return nil

		case <-idle.C:
			// Idle timeout — initiator-level event, close for everyone.
			_ = s.dc.Send(transfer.BuildChatCloseFrame())
			s.onClose("Session closed — no activity for " + IdleTimeout.String() + ".")
			return nil

		case <-done:
			return nil

		case line, ok := <-lineCh:
			if !ok {
				// stdin EOF — quiet leave.
				_ = s.dc.Send(transfer.BuildParticipantLeaveFrame())
				fmt.Println("\nLeft session.")
				return nil
			}
			line = strings.TrimSpace(line)
			if line == QuitCommand {
				if s.isInitiator {
					// Initiator \q — end session for everyone.
					_ = s.dc.Send(transfer.BuildChatCloseFrame())
					s.onClose("Session ended.")
				} else {
					// Responder \q — leave quietly.
					_ = s.dc.Send(transfer.BuildParticipantLeaveFrame())
					fmt.Println("\nLeft session.")
				}
				return nil
			}
			if line == "" {
				fmt.Print("> ")
				continue
			}
			if err := s.dc.Send(transfer.BuildMessageFrame(line)); err != nil {
				return fmt.Errorf("chat: send: %w", err)
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
