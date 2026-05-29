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
	"strings"
	"sync"
	"time"

	"github.com/iamdoubz/gmmff/v2/internal/peerconfig"
	"github.com/iamdoubz/gmmff/v2/internal/transfer"
	"github.com/pion/webrtc/v4"
)

// IdleTimeout is how long a session stays open with no activity.
const IdleTimeout = 10 * time.Minute

// transferRequest is an item on the outbound queue.
type transferRequest struct {
	filePath   string // empty = bytes transfer
	fileName   string
	fileData   []byte // non-nil = in-memory (Wasm)
	isZip      bool
	message    string
	onProgress transfer.ProgressFunc
	done       chan error // closed when the transfer completes or fails
}

// Event is an inbound session event delivered to the caller.
type Event struct {
	Type      EventType
	Message   string  // EventMessage: text; EventTransferDone: filename; EventError: error text
	From      string  // EventMessage: sender peer ID (empty = initiator sent it)
	Data      []byte  // EventTransferDone: file bytes (Wasm only, nil on CLI)
	Path      string  // EventTransferDone: saved path (CLI only)
	Total     int64   // EventTransferStarted: file size
	Label     string  // EventTransferStarted/Progress: channel label
	Done      int64   // EventTransferProgress: bytes received so far
	Speed     float64 // EventTransferProgress: bytes/sec
	PeerCount int     // EventPeerJoined/Left: new count
	MaxPeers  int     // EventPeerJoined: session max
}

type EventType int

const (
	EventMessage          EventType = iota // remote sent a text message
	EventTransferStarted                   // remote is sending a file
	EventTransferProgress                  // progress update on an active receive
	EventTransferDone                      // file fully received and verified
	EventTransferQueued                    // local outbound transfer was queued
	EventPeerLeft                          // remote peer left quietly
	EventPeerJoined                        // new peer joined the session
	EventSessionClosed                     // session ended for everyone
	EventError                             // non-fatal error
)

// peerConn holds one peer's WebRTC connection and control channel.
type peerConn struct {
	peerID    string
	pc        *webrtc.PeerConnection
	controlDC *webrtc.DataChannel
}

// Session is a live bidirectional file + message session.
type Session struct {
	cfg         peerconfig.Config
	isInitiator bool

	// peers holds all connected peer connections (star topology — initiator only).
	// Non-initiators have exactly one entry.
	peers   []*peerConn
	peersMu sync.RWMutex

	// MaxPeers is the session maximum from the slot.
	MaxPeers int
	// peerCount is the current connected count (including self).
	peerCount int

	outbound chan *transferRequest
	Events   chan Event

	idleTimer *time.Timer
	idleMu    sync.Mutex

	cancel context.CancelFunc
	ctx    context.Context

	// OutDir is the directory to save received files (CLI). Empty = in-memory only (Wasm).
	OutDir string

	// Sig is the signaling client — kept open until the session ends.
	Sig interface{ Close() }

	// roster tracks announced peer names: peerID → name.
	// Maintained by the initiator to relay names to newly joining peers.
	roster   map[string]string
	rosterMu sync.Mutex

	// recvMu serializes inbound transfers (one at a time).
	recvMu sync.Mutex
	// recvWg tracks in-flight receive goroutines.
	recvWg sync.WaitGroup
}

// New creates a Session with the first peer's connection.
// Additional peers are added via AddPeer().
// Call Run() in a goroutine after setup.
func New(
	ctx context.Context,
	cancel context.CancelFunc,
	pc *webrtc.PeerConnection,
	controlDC *webrtc.DataChannel,
	cfg peerconfig.Config,
	isInitiator bool,
) *Session {
	s := &Session{
		cfg:         cfg,
		isInitiator: isInitiator,
		outbound:    make(chan *transferRequest, 16),
		Events:      make(chan Event, 64),
		cancel:      cancel,
		ctx:         ctx,
		peers:       []*peerConn{{peerID: "peer-0", pc: pc, controlDC: controlDC}},
		MaxPeers:    2,
		peerCount:   2,
	}
	s.idleTimer = time.NewTimer(IdleTimeout)
	return s
}

// SetContext replaces the session context (used by multi-peer initiator).
func (s *Session) SetContext(ctx context.Context, cancel context.CancelFunc) {
	s.ctx = ctx
	s.cancel = cancel
}

// AddPeerInfo sets the peer count info after creation (first peer, initiator).
func (s *Session) AddPeerInfo(peerID string, peerCount, maxPeers int) {
	s.peers[0].peerID = peerID
	s.peerCount = peerCount
	s.MaxPeers = maxPeers
	s.emit(Event{Type: EventPeerJoined, PeerCount: peerCount, MaxPeers: maxPeers})
}

// AddPeer adds a new peer connection to the session (multi-peer, initiator only).
func (s *Session) AddPeer(peerID string, pc *webrtc.PeerConnection, controlDC *webrtc.DataChannel, peerCount, maxPeers int) {
	pc2 := &peerConn{peerID: peerID, pc: pc, controlDC: controlDC}
	s.peersMu.Lock()
	s.peers = append(s.peers, pc2)
	s.peerCount = peerCount
	s.MaxPeers = maxPeers
	s.peersMu.Unlock()
	// Wire the new peer's data channel into the session.
	s.wirePeer(pc2)
	s.emit(Event{Type: EventPeerJoined, PeerCount: peerCount, MaxPeers: maxPeers})
	// Broadcast the new count to all existing peers so their UIs update.
	s.broadcastPeerCount(peerCount, maxPeers)
}

// PeerCount returns the current connected peer count.
func (s *Session) PeerCount() int { return s.peerCount }

// controlChannels returns all control data channels (one per peer).
func (s *Session) controlChannels() []*webrtc.DataChannel {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	out := make([]*webrtc.DataChannel, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p.controlDC)
	}
	return out
}

// firstPeer returns the first peer connection (used for 2-peer sessions).
func (s *Session) firstPeer() *peerConn {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	if len(s.peers) == 0 {
		return nil
	}
	return s.peers[0]
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

// SendMessage broadcasts a text message to all connected peers.
func (s *Session) SendMessage(text string) error {
	s.resetIdle()
	// If this is a name announcement from the initiator, store it in the roster
	// so it can be included in roster broadcasts to late-joining peers.
	if s.isInitiator && strings.HasPrefix(text, "\x01name:") {
		name := strings.TrimPrefix(text, "\x01name:")
		s.rosterMu.Lock()
		if s.roster == nil {
			s.roster = make(map[string]string)
		}
		s.roster["initiator"] = name
		s.rosterMu.Unlock()
	}
	var lastErr error
	for _, dc := range s.controlChannels() {
		if err := dc.Send(transfer.BuildMessageFrame(text)); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// Close ends the session for everyone (initiator only).
func (s *Session) Close() {
	for _, dc := range s.controlChannels() {
		_ = dc.Send(transfer.BuildSessionCloseFrame())
	}
	s.cancel()
}

// Leave sends a quiet leave frame to all peers and disconnects locally.
func (s *Session) Leave() {
	for _, dc := range s.controlChannels() {
		_ = dc.Send(transfer.BuildParticipantLeaveFrame())
	}
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

	// Wire all existing peers.
	for _, p := range s.peers {
		s.wirePeer(p)
	}

	// Outbound sender loop.
	go s.senderLoop()

	// Wait for context cancellation or idle timeout.
	select {
	case <-s.ctx.Done():
	case <-s.idleTimer.C:
		for _, dc := range s.controlChannels() {
			if s.isInitiator {
				_ = dc.Send(transfer.BuildSessionCloseFrame())
			} else {
				_ = dc.Send(transfer.BuildParticipantLeaveFrame())
			}
		}
		s.emit(Event{Type: EventSessionClosed,
			Message: fmt.Sprintf("Session closed — no activity for %s.", IdleTimeout)})
	}
}

// wirePeer sets up message handlers for one peer connection.
// Safe to call from AddPeer() after Run() has started.
func (s *Session) wirePeer(p *peerConn) {
	p.controlDC.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		s.resetIdle()
		s.handleControlFrame(m.Data, p)
	})
	p.controlDC.OnClose(func() {
		s.peersMu.Lock()
		// Check if this peer was already removed by TagParticipantLeave.
		found := false
		newPeers := s.peers[:0]
		for _, pp := range s.peers {
			if pp.peerID != p.peerID {
				newPeers = append(newPeers, pp)
			} else {
				found = true
			}
		}
		if !found {
			s.peersMu.Unlock()
			return // already handled by TagParticipantLeave
		}
		s.peers = newPeers
		s.peerCount = max(1, s.peerCount-1)
		s.peersMu.Unlock()
		// Look up the leaving peer's name from the roster.
		s.rosterMu.Lock()
		leavingName := s.roster[p.peerID]
		delete(s.roster, p.peerID)
		s.rosterMu.Unlock()
		if leavingName == "" {
			leavingName = "A participant"
		}
		leaveMsg := leavingName + " left the session."
		if len(s.peers) == 0 {
			s.emit(Event{Type: EventSessionClosed, Message: "All peers disconnected."})
			s.cancel()
		} else {
			s.emit(Event{Type: EventPeerLeft, From: p.peerID, Message: leaveMsg, PeerCount: s.peerCount, MaxPeers: s.MaxPeers})
			// Broadcast updated count and leave announcement to remaining peers.
			s.broadcastPeerCount(s.peerCount, s.MaxPeers)
			leaveFrame := transfer.BuildRelayedMessageFrame("__system__", "\x01leave:"+leaveMsg)
			s.peersMu.RLock()
			for _, pp := range s.peers {
				_ = pp.controlDC.Send(leaveFrame)
			}
			s.peersMu.RUnlock()
		}
	})
	p.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "control" {
			return
		}
		s.prepareInboundTransfer(dc)
	})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Control frame handler
// ─────────────────────────────────────────────────────────────────────────────

func (s *Session) handleControlFrame(data []byte, src *peerConn) {
	switch data[0] {
	case transfer.TagMessage:
		msg := transfer.ParseMessageFrame(data)
		s.emit(Event{
			Type:    EventMessage,
			Message: msg,
			From:    src.peerID,
		})
		// Star topology: relay to all other peers with the sender's ID embedded.
		if s.isInitiator {
			s.peersMu.RLock()
			others := make([]*peerConn, 0, len(s.peers))
			for _, p := range s.peers {
				if p != src {
					others = append(others, p)
				}
			}
			s.peersMu.RUnlock()
			for _, p := range others {
				_ = p.controlDC.Send(transfer.BuildRelayedMessageFrame(src.peerID, msg))
			}

			// When a name announcement arrives, update the roster and send it to the new peer.
			if strings.HasPrefix(msg, "\x01name:") {
				newName := strings.TrimPrefix(msg, "\x01name:")
				s.rosterMu.Lock()
				if s.roster == nil {
					s.roster = make(map[string]string)
				}
				s.roster[src.peerID] = newName
				var parts []string
				for pid, name := range s.roster {
					if pid != src.peerID {
						parts = append(parts, pid+"="+name)
					}
				}
				s.rosterMu.Unlock()
				if len(parts) > 0 {
					rosterMsg := "\x01roster:" + strings.Join(parts, ",")
					_ = src.controlDC.Send(transfer.BuildMessageFrame(rosterMsg))
				}
			}
		}

	case transfer.TagRelayedMessage:
		// Message relayed by the initiator — original sender ID is embedded.
		senderID, msg := transfer.ParseRelayedMessageFrame(data)
		if senderID == "__system__" && strings.HasPrefix(msg, "\x01leave:") {
			// Peer-left notification broadcast by the initiator.
			leaveMsg := strings.TrimPrefix(msg, "\x01leave:")
			s.peerCount = max(1, s.peerCount-1)
			s.emit(Event{Type: EventPeerLeft, Message: leaveMsg, PeerCount: s.peerCount, MaxPeers: s.MaxPeers})
			return
		}
		s.emit(Event{
			Type:    EventMessage,
			Message: msg,
			From:    senderID,
		})

	case transfer.TagPeerCount:
		// Initiator broadcasts the current count whenever peers join or leave.
		// Update locally and emit so the UI counter refreshes.
		peerCount, maxPeers := transfer.ParsePeerCountFrame(data)
		if peerCount > 0 {
			s.peerCount = peerCount
			s.MaxPeers = maxPeers
			// Emit PeerJoined purely to trigger a UI count refresh.
			// The leave/join system messages handle the text notification separately.
			s.emit(Event{Type: EventPeerJoined, PeerCount: peerCount, MaxPeers: maxPeers})
		}

	case transfer.TagSessionClose, transfer.TagCancelled:
		s.emit(Event{Type: EventSessionClosed, Message: "Session ended by Participant."})
		s.cancel()

	case transfer.TagParticipantLeave:
		// Look up the leaving peer's name from the roster.
		s.rosterMu.Lock()
		leavingName := s.roster[src.peerID]
		delete(s.roster, src.peerID)
		s.rosterMu.Unlock()
		if leavingName == "" {
			leavingName = "A participant"
		}
		leaveMsg := leavingName + " left the session."
		// Remove the peer from our list and update count.
		s.peersMu.Lock()
		newPeers := s.peers[:0]
		for _, pp := range s.peers {
			if pp.peerID != src.peerID {
				newPeers = append(newPeers, pp)
			}
		}
		s.peers = newPeers
		s.peerCount = max(1, s.peerCount-1)
		s.peersMu.Unlock()
		s.emit(Event{Type: EventPeerLeft, From: src.peerID, Message: leaveMsg, PeerCount: s.peerCount, MaxPeers: s.MaxPeers})
		// Notify remaining peers of the leave and updated count.
		s.broadcastPeerCount(s.peerCount, s.MaxPeers)
		leaveFrame := transfer.BuildRelayedMessageFrame("__system__", "\x01leave:"+leaveMsg)
		s.peersMu.RLock()
		for _, pp := range s.peers {
			_ = pp.controlDC.Send(leaveFrame)
		}
		s.peersMu.RUnlock()

	case transfer.TagSessionReady:
		// Remote is ready — nothing to do here, used for handshake confirmation.
	}
}

// broadcastPeerCount sends the current peer count to all connected peers.
// Called by the initiator when peers join or leave, so non-initiator UIs update.
func (s *Session) broadcastPeerCount(peerCount, maxPeers int) {
	frame := transfer.BuildPeerCountFrame(peerCount, maxPeers)
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	for _, p := range s.peers {
		_ = p.controlDC.Send(frame)
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

	// Announce the transfer on all peer control channels.
	for _, dc := range s.controlChannels() {
		if err := dc.Send(transfer.BuildTransferAnnounceFrame(label)); err != nil {
			return fmt.Errorf("session: announce transfer: %w", err)
		}
	}

	// Create the transfer data channel on the first peer.
	// For multi-peer: each peer gets their own DC in parallel.
	p := s.firstPeer()
	if p == nil {
		return fmt.Errorf("session: no connected peers")
	}
	ordered := true
	dc, err := p.pc.CreateDataChannel(label, &webrtc.DataChannelInit{Ordered: &ordered})
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

	// For multi-peer: queue the same transfer to remaining peers.
	s.peersMu.RLock()
	extraPeers := s.peers[1:]
	s.peersMu.RUnlock()
	if len(extraPeers) > 0 && req.fileData != nil {
		s.broadcastToExtraPeers(req, label, extraPeers)
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

	done := make(chan struct{})
	errCh := make(chan error, 1)
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
			displayMsg = msg + "\nSaved to: " + savedPath
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
	// Guard against sending on a closed channel — can happen when a Pion
	// callback fires (e.g. OnClose) after Run() has already exited and
	// called close(s.Events).
	select {
	case <-s.ctx.Done():
		return
	default:
	}
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

// broadcastToExtraPeers sends the same in-memory file to peers beyond the first.
// Each gets its own labeled data channel. Runs in the background.
func (s *Session) broadcastToExtraPeers(req *transferRequest, baseLabel string, peers []*peerConn) {
	for i, p := range peers {
		pLabel := fmt.Sprintf("%s-p%d", baseLabel, i+1)
		go func(pc *peerConn, label string) {
			ordered := true
			dc, err := pc.pc.CreateDataChannel(label, &webrtc.DataChannelInit{Ordered: &ordered})
			if err != nil {
				return
			}
			dcOpen := make(chan struct{}, 1)
			dc.OnOpen(func() { dcOpen <- struct{}{} })
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(15 * time.Second):
				return
			case <-dcOpen:
			}
			ackCh := make(chan uint64, 32)
			okCh := make(chan struct{}, 1)
			cancelCh := make(chan struct{})
			var cancelOnce sync.Once
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
					select {
					case okCh <- struct{}{}:
					default:
					}
				case transfer.TagCancelled:
					cancelOnce.Do(func() { close(cancelCh) })
				}
			})
			sender := transfer.NewSender(s.ctx, cancelCh, dc, "",
				ackCh, make(chan uint64, 1), s.cfg.WindowSize, s.cfg.ChunkSize)
			if req.onProgress != nil {
				sender.SetProgress(req.onProgress)
			}
			if req.message != "" {
				sender.SetMessage(req.message)
			}
			if err := sender.RunFromBytes(req.fileName, req.fileData); err != nil {
				return
			}
			select {
			case <-s.ctx.Done():
			case <-cancelCh:
			case <-okCh:
				s.resetIdle()
			}
		}(p, pLabel)
	}
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
