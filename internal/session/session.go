// Package session implements the bidirectional file + message session
// introduced by gmmff create / gmmff join.
//
// Architecture (Option B — fresh data channel per transfer):
//
//	Session
//	├── control data channel (persistent, carries TagMessage / TagTransferAnnounce / ...)
//	├── outbound queue (transfers and messages serialized here)
//	├── idle timer (10 min, reset by any send or receive activity)
//	└── coordinator goroutine
//	    ├── drains the outbound queue
//	    ├── handles incoming TagTransferAnnounce by receiving on a new DC
//	    └── handles TagMessage by firing the onMessage callback
//
// Transfer channels are named "transfer-<ulid>" so multiple can exist
// simultaneously on the peer connection without colliding.
package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamdoubz/gmmff/internal/peerconfig"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/pion/webrtc/v4"
)

// IdleTimeout is how long a session stays open with no activity.
const IdleTimeout = 10 * time.Minute

// transferRequest is an item on the outbound queue.
type transferRequest struct {
	filePath string   // empty = bytes transfer
	fileName string
	fileData []byte   // non-nil = in-memory (Wasm)
	isZip    bool
	message  string
	onProgress transfer.ProgressFunc
	done     chan error // closed when the transfer completes or fails
}

// Event is an inbound session event delivered to the caller.
type Event struct {
	Type    EventType
	Message string          // EventMessage: text; EventTransferDone: filename; EventError: error text
	Data    []byte          // EventTransferDone: file bytes (Wasm only, nil on CLI)
	Path    string          // EventTransferDone: saved path (CLI only)
	Total   int64           // EventTransferStarted: file size
	Label   string          // EventTransferStarted/Progress: channel label
	Done    int64           // EventTransferProgress: bytes received so far
	Speed   float64         // EventTransferProgress: bytes/sec
}

type EventType int

const (
	EventMessage         EventType = iota // remote sent a text message
	EventTransferStarted                  // remote is sending a file
	EventTransferProgress                 // progress update on an active receive
	EventTransferDone                     // file fully received and verified
	EventTransferQueued                   // local outbound transfer was queued
	EventPeerLeft                         // remote peer left quietly
	EventSessionClosed                    // session ended for everyone
	EventError                            // non-fatal error
)

// Session is a live bidirectional file + message session.
type Session struct {
	pc          *webrtc.PeerConnection
	controlDC   *webrtc.DataChannel
	cfg         peerconfig.Config
	isInitiator bool

	outbound chan *transferRequest
	Events   chan Event

	idleTimer *time.Timer
	idleMu    sync.Mutex

	cancel context.CancelFunc
	ctx    context.Context

	// OutDir is the directory to save received files (CLI). Empty = in-memory only (Wasm).
	OutDir string

	// Sig is the signaling client — kept open until the session ends so the
	// broker does not close the slot prematurely on disconnect.
	Sig interface{ Close() }

	// recvMu serializes inbound transfers (one at a time).
	recvMu sync.Mutex
	// recvWg tracks in-flight receive goroutines so Run() waits for them before
	// closing Events, preventing a send-on-closed-channel panic.
	recvWg sync.WaitGroup
}

// New creates a Session from an established peer connection and control channel.
// Call Run() in a goroutine to start the coordinator.
func New(
	ctx context.Context,
	cancel context.CancelFunc,
	pc *webrtc.PeerConnection,
	controlDC *webrtc.DataChannel,
	cfg peerconfig.Config,
	isInitiator bool,
) *Session {
	s := &Session{
		pc:          pc,
		controlDC:   controlDC,
		cfg:         cfg,
		isInitiator: isInitiator,
		outbound:    make(chan *transferRequest, 16),
		Events:      make(chan Event, 64),
		cancel:      cancel,
		ctx:         ctx,
	}
	s.idleTimer = time.NewTimer(IdleTimeout)
	return s
}

// IsInitiator reports whether this peer started the session.
func (s *Session) IsInitiator() bool { return s.isInitiator }

// ─────────────────────────────────────────────────────────────────────────────
// Public send methods
// ─────────────────────────────────────────────────────────────────────────────

// SendFile enqueues a file path for transfer.
func (s *Session) SendFile(filePath, message string, onProgress transfer.ProgressFunc) <-chan error {
	req := &transferRequest{
		filePath:   filePath,
		message:    message,
		onProgress: onProgress,
		done:       make(chan error, 1),
	}
	s.outbound <- req
	s.resetIdle()
	return req.done
}

// SendBytes enqueues an in-memory file for transfer (Wasm path).
func (s *Session) SendBytes(fileName string, data []byte, message string, onProgress transfer.ProgressFunc) <-chan error {
	req := &transferRequest{
		fileName:   fileName,
		fileData:   data,
		message:    message,
		onProgress: onProgress,
		done:       make(chan error, 1),
	}
	s.outbound <- req
	s.resetIdle()
	return req.done
}

// SendMessage sends a text message to the remote peer.
func (s *Session) SendMessage(text string) error {
	s.resetIdle()
	return s.controlDC.Send(transfer.BuildMessageFrame(text))
}

// Close ends the session for everyone (initiator only).
func (s *Session) Close() {
	_ = s.controlDC.Send(transfer.BuildSessionCloseFrame())
	s.cancel()
}

// Leave sends a quiet leave frame and disconnects locally.
func (s *Session) Leave() {
	_ = s.controlDC.Send(transfer.BuildParticipantLeaveFrame())
	s.cancel()
}

// ─────────────────────────────────────────────────────────────────────────────
// Coordinator
// ─────────────────────────────────────────────────────────────────────────────

// Run starts the coordinator loop. Blocks until the session ends.
// Call in a goroutine.
func (s *Session) Run() {
	defer s.cancel()
	// Wait for all in-flight receive goroutines to finish before closing
	// Events — otherwise we get a send-on-closed-channel panic.
	defer func() {
		s.recvWg.Wait()
		close(s.Events)
	}()
	defer func() {
		if s.Sig != nil {
			s.Sig.Close()
		}
	}()

	// Wire control channel message handler.
	s.controlDC.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		s.resetIdle()
		s.handleControlFrame(m.Data)
	})
	s.controlDC.OnClose(func() {
		s.emit(Event{Type: EventSessionClosed, Message: "Connection closed."})
		s.cancel()
	})

	// Wire inbound transfer channels — remote opens a new DC for each transfer.
	// OnDataChannel fires synchronously when Pion delivers a new data channel.
	// We register dc.OnMessage here — before returning — so no frames can
	// arrive before the handler is in place. The blocking completion wait
	// happens in a goroutine launched from inside prepareInboundTransfer.
	s.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "control" {
			return
		}
		s.prepareInboundTransfer(dc)
	})

	// Outbound sender loop.
	go s.senderLoop()

	// Wait for context cancellation or idle timeout.
	select {
	case <-s.ctx.Done():
	case <-s.idleTimer.C:
		if s.isInitiator {
			_ = s.controlDC.Send(transfer.BuildSessionCloseFrame())
		} else {
			_ = s.controlDC.Send(transfer.BuildParticipantLeaveFrame())
		}
		s.emit(Event{Type: EventSessionClosed,
			Message: fmt.Sprintf("Session closed — no activity for %s.", IdleTimeout)})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Control frame handler
// ─────────────────────────────────────────────────────────────────────────────

func (s *Session) handleControlFrame(data []byte) {
	switch data[0] {
	case transfer.TagMessage:
		s.emit(Event{
			Type:    EventMessage,
			Message: transfer.ParseMessageFrame(data),
		})

	case transfer.TagSessionClose, transfer.TagCancelled:
		s.emit(Event{Type: EventSessionClosed, Message: "Session ended by Participant."})
		s.cancel()

	case transfer.TagParticipantLeave:
		s.emit(Event{Type: EventPeerLeft, Message: "Participant left the session."})
		// Session continues — don't cancel.

	case transfer.TagSessionReady:
		// Remote is ready — nothing to do here, used for handshake confirmation.
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Outbound sender loop
// ─────────────────────────────────────────────────────────────────────────────

func (s *Session) senderLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case req, ok := <-s.outbound:
			if !ok {
				return
			}
			if err := s.execTransfer(req); err != nil {
				req.done <- err
			}
			close(req.done)
		}
	}
}

func (s *Session) execTransfer(req *transferRequest) error {
	// Generate a unique label for this transfer's data channel.
	label := fmt.Sprintf("transfer-%d", time.Now().UnixNano())

	// Announce the transfer on the control channel.
	if err := s.controlDC.Send(transfer.BuildTransferAnnounceFrame(label)); err != nil {
		return fmt.Errorf("session: announce transfer: %w", err)
	}

	// Create the transfer data channel.
	ordered := true
	dc, err := s.pc.CreateDataChannel(label, &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return fmt.Errorf("session: create transfer channel: %w", err)
	}

	// Wait for it to open.
	dcOpen := make(chan struct{}, 1)
	dc.OnOpen(func() { dcOpen <- struct{}{} })

	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-time.After(15 * time.Second):
		return fmt.Errorf("session: transfer channel open timeout")
	case <-dcOpen:
	}

	// Wire ack channel.
	ackCh := make(chan uint64, 32)
	okCh := make(chan struct{}, 1)
	cancelCh := make(chan struct{})
	cancelOnce := sync.Once{}
	signalCancel := func() { cancelOnce.Do(func() { close(cancelCh) }) }
	var okReceived bool

	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		switch m.Data[0] {
		case transfer.TagChunkAck:
			if seq, err := transfer.ParseAckFrame(m.Data); err == nil {
				ackCh <- seq
			}
		case transfer.TagTransferOK:
			okReceived = true
			select {
			case okCh <- struct{}{}:
			default:
			}
		case transfer.TagCancelled:
			signalCancel()
		}
	})
	dc.OnClose(func() {
		if !okReceived {
			signalCancel()
		}
	})

	s.resetIdle()

	// Run the transfer.
	sender := transfer.NewSender(s.ctx, cancelCh, dc, req.filePath,
		ackCh, make(chan uint64, 1), s.cfg.WindowSize, s.cfg.ChunkSize)
	if req.onProgress != nil {
		sender.SetProgress(req.onProgress)
	}
	if req.message != "" {
		sender.SetMessage(req.message)
	}

	var runErr error
	if req.fileData != nil {
		runErr = sender.RunFromBytes(req.fileName, req.fileData)
	} else {
		runErr = sender.Run()
	}
	if runErr != nil {
		return runErr
	}

	// Wait for TransferOK from receiver.
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-cancelCh:
		return fmt.Errorf("session: transfer cancelled by receiver")
	case <-okCh:
		s.resetIdle()
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Inbound transfer receiver
// ─────────────────────────────────────────────────────────────────────────────

// prepareInboundTransfer is called synchronously inside pc.OnDataChannel.
// It registers dc.OnMessage immediately so no messages can be lost to a
// scheduling race. The blocking wait and completion logic run in a goroutine.
func (s *Session) prepareInboundTransfer(dc *webrtc.DataChannel) {
	rs := transfer.NewReceiveStateMem(func(seq uint64) error {
		return dc.Send(transfer.BuildAckFrame(seq))
	})

	var headerEmitted bool
	rs.SetProgress(func(bytesRecv, total int64) {
		s.emit(Event{
			Type:  EventTransferProgress,
			Label: dc.Label(),
			Done:  bytesRecv,
			Total: total,
		})
	})

	done      := make(chan struct{})
	errCh     := make(chan error, 1)
	var closeOnce sync.Once

	// Register OnMessage synchronously — guaranteed race-free with Pion.
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		finished, err := rs.Feed(m.Data)
		if err != nil {
			_ = dc.Send(transfer.BuildErrorFrame("ERR_RECEIVE", err.Error()))
			closeOnce.Do(func() { errCh <- err; close(done) })
			return
		}
		if rs.Header != nil && !headerEmitted {
			headerEmitted = true
			s.emit(Event{
				Type:  EventTransferStarted,
				Label: dc.Label(),
				Total: rs.Header.Size,
			})
		}
		if finished {
			_ = dc.Send([]byte{transfer.TagTransferOK})
			s.resetIdle()
			closeOnce.Do(func() { close(done) })
		}
	})

	// Wait for completion in a goroutine.
	// recvMu serializes inbound transfers so they complete one at a time.
	s.recvWg.Add(1)
	go func() {
		defer s.recvWg.Done()
		s.recvMu.Lock()
		defer s.recvMu.Unlock()

		select {
		case <-s.ctx.Done():
			return
		case <-done:
		}

		var recvErr error
		select {
		case recvErr = <-errCh:
		default:
		}
		if recvErr != nil {
			s.emit(Event{Type: EventError, Message: recvErr.Error()})
			return
		}

		msg := ""
		if rs.Header != nil {
			msg = rs.Header.Message
		}

		var savedPath string
		if s.OutDir != "" && len(rs.Result()) > 0 {
			path, err := saveReceivedFile(s.OutDir, rs.FileName(), rs.Result())
			if err != nil {
				s.emit(Event{Type: EventError, Message: fmt.Sprintf("save file: %v", err)})
				return
			}
			savedPath = path
		}

		displayMsg := msg
		if savedPath != "" && msg == "" {
			displayMsg = "Saved to: " + savedPath
		} else if savedPath != "" && msg != "" {
			displayMsg = msg + "
Saved to: " + savedPath
		}

		s.emit(Event{
			Type:    EventTransferDone,
			Message: displayMsg,
			Label:   dc.Label(),
			Data:    rs.Result(),
			Path:    rs.FileName(), // bare filename — used by browserDownload on Wasm
		})
	}()
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (s *Session) emit(e Event) {
	select {
	case s.Events <- e:
	default:
	}
}

func (s *Session) resetIdle() {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if !s.idleTimer.Stop() {
		select {
		case <-s.idleTimer.C:
		default:
		}
	}
	s.idleTimer.Reset(IdleTimeout)
}

// saveReceivedFile writes data to outDir/name, avoiding collisions.
func saveReceivedFile(outDir, name string, data []byte) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	dest := filepath.Join(outDir, filepath.Base(name))
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return dest, nil
}
