// Package peer orchestrates the WebRTC connection lifecycle for gmmff.
//
// Flow (initiator side):
//  1. PAKE Start  → send pake.a
//  2. Receive pake.b → PAKE Finish → shared key
//  3. Create PeerConnection + DataChannel
//  4. Create SDP offer → send sdp.offer, trickle ICE
//  5. Receive sdp.answer → SetRemoteDescription
//  6. DataChannel opens → transfer.Sender
//
// Flow (responder side):
//  1. Receive pake.a → PAKE Exchange → send pake.b
//  2. Receive sdp.offer → SetRemoteDescription
//  3. Create SDP answer → send sdp.answer, trickle ICE
//  4. DataChannel opens → transfer.Receiver
package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"filippo.io/cpace"
	"github.com/iamdoubz/gmmff/v2/internal/chat"
	"github.com/iamdoubz/gmmff/v2/internal/pake"
	"github.com/iamdoubz/gmmff/v2/internal/peerconfig"
	"github.com/iamdoubz/gmmff/v2/internal/session"
	"github.com/iamdoubz/gmmff/v2/internal/signaling"
	"github.com/iamdoubz/gmmff/v2/internal/transfer"
	"github.com/iamdoubz/gmmff/v2/internal/turn"
	"github.com/iamdoubz/gmmff/v2/pkg/protocol"
	"github.com/pion/webrtc/v4"
)

// DefaultSTUN and DefaultSTUNServers are re-exported from peerconfig
// for callers that previously imported them from this package.
const DefaultSTUN = peerconfig.DefaultSTUN

var DefaultSTUNServers = peerconfig.DefaultSTUNServers

// Config is the peer connection configuration.
// It lives in peerconfig to avoid import cycles with internal/session.
type Config = peerconfig.Config

// iceServers returns the full ICEServer slice — STUN entries first, then TURN.
// In LocalMode it returns an empty slice so Pion only gathers host candidates
// (direct LAN IPs) — no internet traffic, no STUN/TURN servers required.
func iceServers(c Config) []webrtc.ICEServer {
	if c.LocalMode {
		return []webrtc.ICEServer{} // host candidates only — no internet needed
	}
	urls := c.STUNServers
	if len(urls) == 0 {
		urls = peerconfig.DefaultSTUNServers
	}
	ice := []webrtc.ICEServer{{URLs: urls}}
	ice = append(ice, turn.ICEServers(c.TURNServers)...)
	return ice
}

func windowSize(c Config) int {
	if c.WindowSize > 0 {
		return c.WindowSize
	}
	return transfer.DefaultWindowSize
}

func chunkSize(c Config) int {
	if c.ChunkSize > 0 {
		return c.ChunkSize
	}
	return transfer.DefaultChunkSize
}

// ─────────────────────────────────────────────────────────────────────────────
// dispatcher — fans out a single Recv channel into typed sub-channels
// ─────────────────────────────────────────────────────────────────────────────

type dispatcher struct {
	pakeA      chan signaling.Message
	pakeB      chan signaling.Message
	offer      chan signaling.Message
	answer     chan signaling.Message
	ice        chan signaling.Message
	control    chan signaling.Message
	peerJoined chan signaling.Message // peer.joined notifications
	targeted   chan signaling.Message // targeted peer-to-peer messages
}

func newDispatcher() *dispatcher {
	return &dispatcher{
		pakeA:      make(chan signaling.Message, 4),
		pakeB:      make(chan signaling.Message, 4),
		offer:      make(chan signaling.Message, 4),
		answer:     make(chan signaling.Message, 4),
		ice:        make(chan signaling.Message, 64),
		control:    make(chan signaling.Message, 8),
		peerJoined: make(chan signaling.Message, 16),
		targeted:   make(chan signaling.Message, 16),
	}
}

func (d *dispatcher) run(ctx context.Context, recv <-chan signaling.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-recv:
			if !ok {
				return
			}
			switch msg.Type {
			case protocol.MsgPakeA:
				d.pakeA <- msg
			case protocol.MsgPakeB:
				d.pakeB <- msg
			case protocol.MsgSDPOffer:
				d.offer <- msg
			case protocol.MsgSDPAnswer:
				d.answer <- msg
			case protocol.MsgICECandidate:
				d.ice <- msg
			case protocol.MsgPeerJoined:
				d.peerJoined <- msg
			case protocol.MsgTargeted:
				d.targeted <- msg
			default:
				d.control <- msg
			}
		}
	}
}

func (d *dispatcher) waitFor(ctx context.Context, ch <-chan signaling.Message) (signaling.Message, error) {
	for {
		select {
		case <-ctx.Done():
			return signaling.Message{}, ctx.Err()
		case msg := <-d.control:
			if msg.Type == protocol.MsgError {
				var e protocol.ErrorPayload
				_ = json.Unmarshal(msg.Payload, &e)
				return signaling.Message{}, fmt.Errorf("server error [%s]: %s", e.Code, e.Message)
			}
			// Non-error control message while waiting for something else — discard.
		case msg := <-ch:
			return msg, nil
		}
	}
}

// waitForControl waits for a specific message type on the control channel.
// Unlike waitFor, this reads from d.control directly and returns on type match.
func (d *dispatcher) waitForControl(ctx context.Context, msgType string) (signaling.Message, error) {
	for {
		select {
		case <-ctx.Done():
			return signaling.Message{}, ctx.Err()
		case msg := <-d.control:
			if msg.Type == protocol.MsgError {
				var e protocol.ErrorPayload
				_ = json.Unmarshal(msg.Payload, &e)
				return signaling.Message{}, fmt.Errorf("server error [%s]: %s", e.Code, e.Message)
			}
			if msg.Type == msgType {
				return msg, nil
			}
			// Other control messages (e.g. bye) — discard and keep waiting.
		}
	}
}

func (d *dispatcher) pumpICE(ctx context.Context, pc *webrtc.PeerConnection) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-d.ice:
			ice, err := signaling.DecodeICE(msg)
			if err != nil {
				continue
			}
			fmt.Printf("[ICE] applying remote candidate: %s\n", candidateType(ice.Candidate))
			sdpMid := ice.SDPMid
			sdpIdx := ice.SDPMLineIndex
			_ = pc.AddICECandidate(webrtc.ICECandidateInit{
				Candidate:     ice.Candidate,
				SDPMid:        &sdpMid,
				SDPMLineIndex: &sdpIdx,
			})
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Send — initiator path
// ─────────────────────────────────────────────────────────────────────────────

func Send(ctx context.Context, sig *signaling.Client, code, filePath string, cfg Config, message string, isZip bool) error {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE
	fmt.Println("Performing cryptographic handshake...")
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgA, state, err := cpace.Start(code, ci)
	if err != nil {
		return fmt.Errorf("peer: PAKE start: %w", err)
	}
	if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil {
		return fmt.Errorf("peer: send pake.a: %w", err)
	}
	msg, err := disp.waitFor(ctx, disp.pakeB)
	if err != nil {
		return fmt.Errorf("peer: wait pake.b: %w", err)
	}
	msgB, err := signaling.DecodeOpaque(msg)
	if err != nil {
		return fmt.Errorf("peer: decode pake.b: %w", err)
	}
	sharedKey, err := state.Finish(msgB)
	if err != nil {
		return fmt.Errorf("peer: PAKE finish: %w — wrong code or tampered connection", err)
	}
	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return fmt.Errorf("peer: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()

	ackCh := make(chan uint64, 32)
	okCh := make(chan struct{}, 1)
	// remoteCancelCh is closed when the receiver cancels — either via a
	// TagCancelled data channel frame or when the data channel closes.
	// Closing it unblocks the sender loop regardless of network state.
	remoteCancelCh := make(chan struct{})
	remoteCancelOnce := make(chan struct{}, 1) // guards close(remoteCancelCh)
	signalRemoteCancel := func() {
		select {
		case remoteCancelOnce <- struct{}{}:
			close(remoteCancelCh)
		default:
		}
	}

	ordered := true
	dc, err := pc.CreateDataChannel("gmmff", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return fmt.Errorf("peer: create data channel: %w", err)
	}

	resumeFromCh := make(chan uint64, 1)

	// transferOKReceived is set when TagTransferOK arrives.
	// OnClose checks this so it does not signal cancellation after a
	// clean transfer — the receiver closes the connection immediately
	// after sending TransferOK, which would otherwise race with OnClose.
	var transferOKReceived bool

	dcReady := make(chan struct{})
	dc.OnOpen(func() { close(dcReady) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		switch m.Data[0] {
		case transfer.TagChunkAck:
			if seq, err := transfer.ParseAckFrame(m.Data); err == nil {
				ackCh <- seq
			}
		case transfer.TagResumeFrom:
			if seq, err := transfer.ParseResumeFrame(m.Data); err == nil {
				select {
				case resumeFromCh <- seq:
				default:
				}
			}
		case transfer.TagTransferOK:
			transferOKReceived = true
			select {
			case okCh <- struct{}{}:
			default:
			}
		case transfer.TagCancelled:
			fmt.Println()
			fmt.Println("Transfer cancelled by receiver.")
			signalRemoteCancel()
		}
	})
	// Watchdog: if the data channel closes for any reason while sending,
	// signal cancellation so the sender loop doesn't hang.
	// Do NOT signal if TransferOK already arrived — the receiver
	// legitimately closes the connection right after sending it.
	dc.OnClose(func() {
		if !transferOKReceived {
			signalRemoteCancel()
		}
	})

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("peer: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("peer: set local description: %w", err)
	}
	sdpJSON, _ := json.Marshal(offer)
	offerMAC := session.SignOffer(sdpJSON)
	if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil {
		return fmt.Errorf("peer: send sdp.offer: %w", err)
	}

	answerMsg, err := disp.waitFor(ctx, disp.answer)
	if err != nil {
		return fmt.Errorf("peer: wait sdp.answer: %w", err)
	}
	answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
	if err != nil {
		return fmt.Errorf("peer: decode sdp.answer: %w", err)
	}
	if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil {
		return fmt.Errorf("peer: %w", err)
	}
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(answerJSON, &answer); err != nil {
		return fmt.Errorf("peer: unmarshal answer: %w", err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("peer: set remote description: %w", err)
	}

	fmt.Println("Establishing direct connection...")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-dcReady:
	}
	fmt.Println("Direct connection established — sending file")

	sender := transfer.NewSender(ctx, remoteCancelCh, dc, filePath, ackCh, resumeFromCh, windowSize(cfg), chunkSize(cfg))
	if message != "" && !isZip {
		sender.SetMessage(message)
	}
	if err := sender.Run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil // message already printed by sender loop
		}
		if errors.Is(err, transfer.ErrCancelled) {
			return nil // message already printed by OnMessage handler
		}
		return fmt.Errorf("peer: transfer: %w", err)
	}

	select {
	case <-ctx.Done():
		_ = dc.Send(transfer.BuildCancelledFrame())
		fmt.Println("Transfer cancelled.")
		return nil
	case <-okCh:
		fmt.Println("Transfer complete — file received and verified by peer")
	}

	sig.Close()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SendBytes — initiator path for in-memory data (browser Wasm)
// ─────────────────────────────────────────────────────────────────────────────

// SendBytes is identical to Send but transfers from an in-memory buffer
// instead of a file path.  Used by the browser Wasm client where the
// filesystem is unavailable.
func SendBytes(ctx context.Context, sig *signaling.Client, code, fileName string, data []byte, cfg Config, onProgress transfer.ProgressFunc, message string) error {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE — identical to Send
	fmt.Println("Performing cryptographic handshake...")
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgA, state, err := cpace.Start(code, ci)
	if err != nil {
		return fmt.Errorf("peer: PAKE start: %w", err)
	}
	if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil {
		return fmt.Errorf("peer: send pake.a: %w", err)
	}
	msg, err := disp.waitFor(ctx, disp.pakeB)
	if err != nil {
		return fmt.Errorf("peer: wait pake.b: %w", err)
	}
	msgB, err := signaling.DecodeOpaque(msg)
	if err != nil {
		return fmt.Errorf("peer: decode pake.b: %w", err)
	}
	sharedKey, err := state.Finish(msgB)
	if err != nil {
		return fmt.Errorf("peer: PAKE finish: %w — wrong code or tampered connection", err)
	}
	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return fmt.Errorf("peer: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC — identical to Send
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()

	ackCh := make(chan uint64, 32)
	okCh := make(chan struct{}, 1)
	remoteCancelCh := make(chan struct{})
	remoteCancelOnce := make(chan struct{}, 1)
	signalRemoteCancel := func() {
		select {
		case remoteCancelOnce <- struct{}{}:
			close(remoteCancelCh)
		default:
		}
	}
	var transferOKReceived bool
	resumeFromCh := make(chan uint64, 1)

	ordered := true
	dc, err := pc.CreateDataChannel("gmmff", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return fmt.Errorf("peer: create data channel: %w", err)
	}

	dcReady := make(chan struct{})
	dc.OnOpen(func() { close(dcReady) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		switch m.Data[0] {
		case transfer.TagChunkAck:
			if seq, err := transfer.ParseAckFrame(m.Data); err == nil {
				ackCh <- seq
			}
		case transfer.TagResumeFrom:
			if seq, err := transfer.ParseResumeFrame(m.Data); err == nil {
				select {
				case resumeFromCh <- seq:
				default:
				}
			}
		case transfer.TagTransferOK:
			transferOKReceived = true
			select {
			case okCh <- struct{}{}:
			default:
			}
		case transfer.TagCancelled:
			fmt.Println()
			fmt.Println("Transfer cancelled by receiver.")
			signalRemoteCancel()
		}
	})
	dc.OnClose(func() {
		if !transferOKReceived {
			signalRemoteCancel()
		}
	})

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("peer: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("peer: set local description: %w", err)
	}
	sdpJSON, _ := json.Marshal(offer)
	offerMAC := session.SignOffer(sdpJSON)
	if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil {
		return fmt.Errorf("peer: send sdp.offer: %w", err)
	}

	answerMsg, err := disp.waitFor(ctx, disp.answer)
	if err != nil {
		return fmt.Errorf("peer: wait sdp.answer: %w", err)
	}
	answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
	if err != nil {
		return fmt.Errorf("peer: decode sdp.answer: %w", err)
	}
	if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil {
		return fmt.Errorf("peer: %w", err)
	}
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(answerJSON, &answer); err != nil {
		return fmt.Errorf("peer: unmarshal answer: %w", err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("peer: set remote description: %w", err)
	}

	fmt.Println("Establishing direct connection...")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-dcReady:
	}
	fmt.Println("Direct connection established — sending file")

	// Use RunFromBytes instead of Run — no filesystem needed.
	sender := transfer.NewSender(ctx, remoteCancelCh, dc, "", ackCh, resumeFromCh, windowSize(cfg), chunkSize(cfg))
	if onProgress != nil {
		sender.SetProgress(onProgress)
	}
	if message != "" {
		sender.SetMessage(message)
	}
	if err := sender.RunFromBytes(fileName, data); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if errors.Is(err, transfer.ErrCancelled) {
			return nil
		}
		return fmt.Errorf("peer: transfer: %w", err)
	}

	select {
	case <-ctx.Done():
		_ = dc.Send(transfer.BuildCancelledFrame())
		fmt.Println("Transfer cancelled.")
		return nil
	case <-okCh:
		fmt.Println("Transfer complete — file received and verified by peer")
	}

	sig.Close()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Receive — responder path
// ─────────────────────────────────────────────────────────────────────────────

func Receive(ctx context.Context, sig *signaling.Client, code, outDir string, cfg Config) error {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE
	fmt.Println("Performing cryptographic handshake...")
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)

	msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
	if err != nil {
		return fmt.Errorf("peer: wait pake.a: %w", err)
	}
	msgA, err := signaling.DecodeOpaque(msgAEnv)
	if err != nil {
		return fmt.Errorf("peer: decode pake.a: %w", err)
	}
	msgB, sharedKey, err := cpace.Exchange(code, ci, msgA)
	if err != nil {
		return fmt.Errorf("peer: PAKE exchange: %w", err)
	}
	if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
		return fmt.Errorf("peer: send pake.b: %w", err)
	}
	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return fmt.Errorf("peer: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()

	transferDone := make(chan error, 1)
	cancelDC := make(chan *webrtc.DataChannel, 1)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		// Make dc available to the cancellation path.
		select {
		case cancelDC <- dc:
		default:
		}
		rs := transfer.NewReceiveState(outDir,
			func(seq uint64) error {
				return dc.Send(transfer.BuildAckFrame(seq))
			},
			func(seq uint64) error {
				return dc.Send(transfer.BuildResumeFrame(seq))
			},
		)
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			done, err := rs.Feed(m.Data)
			if err != nil {
				if errors.Is(err, transfer.ErrCancelled) {
					fmt.Println("Transfer cancelled by sender.")
					select {
					case transferDone <- nil:
					default:
					}
					return
				}
				_ = dc.Send(transfer.BuildErrorFrame("ERR_RECEIVE", err.Error()))
				select {
				case transferDone <- err:
				default:
				}
				return
			}
			if done {
				_ = dc.Send([]byte{transfer.TagTransferOK})
				if rs.Header != nil && rs.Header.Message != "" {
					fmt.Printf("Sender message: %s\n", rs.Header.Message)
				}
				fmt.Printf("Saved to: %s\n", rs.OutputPath())
				select {
				case transferDone <- nil:
				default:
				}
			}
		})
	})

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	fmt.Println("Waiting for sender...")
	offerMsg, err := disp.waitFor(ctx, disp.offer)
	if err != nil {
		return fmt.Errorf("peer: wait sdp.offer: %w", err)
	}
	offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
	if err != nil {
		return fmt.Errorf("peer: decode sdp.offer: %w", err)
	}
	if err := session.VerifyOffer(offerJSON, offerMAC); err != nil {
		return fmt.Errorf("peer: %w", err)
	}
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(offerJSON, &offer); err != nil {
		return fmt.Errorf("peer: unmarshal offer: %w", err)
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		return fmt.Errorf("peer: set remote description: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("peer: create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("peer: set local description: %w", err)
	}
	answerJSON, _ := json.Marshal(answer)
	answerMAC := session.SignAnswer(answerJSON)
	if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
		return fmt.Errorf("peer: send sdp.answer: %w", err)
	}

	fmt.Println("Direct connection established — receiving file")

	select {
	case <-ctx.Done():
		// Send TagCancelled over the data channel so the sender gets a clean
		// message instead of "ack channel closed unexpectedly".
		select {
		case dc := <-cancelDC:
			_ = dc.Send(transfer.BuildCancelledFrame())
		default:
		}
		fmt.Println("Transfer cancelled.")
		sig.Close()
		return nil
	case err := <-transferDone:
		sig.Close()
		return err
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ReceiveToBytes — responder path for browser Wasm (no filesystem)
// ─────────────────────────────────────────────────────────────────────────────

// ReceiveToBytes performs the full responder flow and returns the received
// file as (fileName, data).  Used by the browser Wasm client where the
// filesystem is unavailable.  Resume is not supported.
func ReceiveToBytes(ctx context.Context, sig *signaling.Client, code string, cfg Config, onProgress transfer.ProgressFunc) (fileName string, data []byte, err error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE
	fmt.Println("Performing cryptographic handshake...")
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
	if err != nil {
		return "", nil, fmt.Errorf("peer: wait pake.a: %w", err)
	}
	msgA, err := signaling.DecodeOpaque(msgAEnv)
	if err != nil {
		return "", nil, fmt.Errorf("peer: decode pake.a: %w", err)
	}
	msgB, sharedKey, err := cpace.Exchange(code, ci, msgA)
	if err != nil {
		return "", nil, fmt.Errorf("peer: PAKE exchange: %w", err)
	}
	if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
		return "", nil, fmt.Errorf("peer: send pake.b: %w", err)
	}
	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return "", nil, fmt.Errorf("peer: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return "", nil, err
	}
	defer pc.Close()

	type result struct {
		name string
		data []byte
		err  error
	}
	transferDone := make(chan result, 1)
	cancelDC := make(chan *webrtc.DataChannel, 1)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		select {
		case cancelDC <- dc:
		default:
		}
		rs := transfer.NewReceiveStateMem(func(seq uint64) error {
			return dc.Send(transfer.BuildAckFrame(seq))
		})
		if onProgress != nil {
			rs.SetProgress(onProgress)
		}
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			done, err := rs.Feed(m.Data)
			if err != nil {
				if errors.Is(err, transfer.ErrCancelled) {
					fmt.Println("Transfer cancelled by sender.")
					select {
					case transferDone <- result{err: nil}:
					default:
					}
					return
				}
				_ = dc.Send(transfer.BuildErrorFrame("ERR_RECEIVE", err.Error()))
				select {
				case transferDone <- result{err: err}:
				default:
				}
				return
			}
			if done {
				_ = dc.Send([]byte{transfer.TagTransferOK})
				select {
				case transferDone <- result{name: rs.FileName(), data: rs.Result()}:
				default:
				}
			}
		})
	})

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	fmt.Println("Waiting for sender...")
	offerMsg, err := disp.waitFor(ctx, disp.offer)
	if err != nil {
		return "", nil, fmt.Errorf("peer: wait sdp.offer: %w", err)
	}
	offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
	if err != nil {
		return "", nil, fmt.Errorf("peer: decode sdp.offer: %w", err)
	}
	if err := session.VerifyOffer(offerJSON, offerMAC); err != nil {
		return "", nil, fmt.Errorf("peer: %w", err)
	}
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(offerJSON, &offer); err != nil {
		return "", nil, fmt.Errorf("peer: unmarshal offer: %w", err)
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		return "", nil, fmt.Errorf("peer: set remote description: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", nil, fmt.Errorf("peer: create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", nil, fmt.Errorf("peer: set local description: %w", err)
	}
	answerJSON, _ := json.Marshal(answer)
	answerMAC := session.SignAnswer(answerJSON)
	if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
		return "", nil, fmt.Errorf("peer: send sdp.answer: %w", err)
	}

	fmt.Println("Direct connection established — receiving file")

	select {
	case <-ctx.Done():
		select {
		case dc := <-cancelDC:
			_ = dc.Send(transfer.BuildCancelledFrame())
		default:
		}
		fmt.Println("Transfer cancelled.")
		sig.Close()
		return "", nil, nil
	case res := <-transferDone:
		sig.Close()
		if res.err != nil {
			return "", nil, res.err
		}
		return res.name, res.data, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Chat — symmetric bidirectional text session
// ─────────────────────────────────────────────────────────────────────────────

// Chat performs the PAKE+WebRTC handshake and then runs a symmetric chat
// session.  Either peer can send messages; the session closes on \q, idle
// timeout, or ctx cancellation.
// role is "Sender" or "Receiver" — used as the remote peer's display name.
func Chat(ctx context.Context, sig *signaling.Client, code, role string, cfg Config) error {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE — same as Send/Receive
	fmt.Println("Performing cryptographic handshake...")
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)

	var sharedKey []byte
	if role == "Receiver" {
		msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
		if err != nil {
			return fmt.Errorf("peer: chat wait pake.a: %w", err)
		}
		msgA, err := signaling.DecodeOpaque(msgAEnv)
		if err != nil {
			return fmt.Errorf("peer: chat decode pake.a: %w", err)
		}
		msgB, sk, err := cpace.Exchange(code, ci, msgA)
		if err != nil {
			return fmt.Errorf("peer: chat PAKE exchange: %w", err)
		}
		sharedKey = sk
		if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
			return fmt.Errorf("peer: chat send pake.b: %w", err)
		}
	} else {
		msgA, state, err := cpace.Start(code, ci)
		if err != nil {
			return fmt.Errorf("peer: chat PAKE start: %w", err)
		}
		if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil {
			return fmt.Errorf("peer: chat send pake.a: %w", err)
		}
		msg, err := disp.waitFor(ctx, disp.pakeB)
		if err != nil {
			return fmt.Errorf("peer: chat wait pake.b: %w", err)
		}
		msgB, err := signaling.DecodeOpaque(msg)
		if err != nil {
			return fmt.Errorf("peer: chat decode pake.b: %w", err)
		}
		sk, err := state.Finish(msgB)
		if err != nil {
			return fmt.Errorf("peer: chat PAKE finish: %w", err)
		}
		sharedKey = sk
	}

	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return fmt.Errorf("peer: chat derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	pc, err := newPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()

	dcReady := make(chan *webrtc.DataChannel, 1)

	if role == "Sender" {
		// Initiator creates data channel
		ordered := true
		dc, err := pc.CreateDataChannel("gmmff-chat", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil {
			return fmt.Errorf("peer: chat create data channel: %w", err)
		}
		dc.OnOpen(func() { dcReady <- dc })
	} else {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) { dcReady <- dc })
	}

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	if role == "Sender" {
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return fmt.Errorf("peer: chat create offer: %w", err)
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			return fmt.Errorf("peer: chat set local description: %w", err)
		}
		sdpJSON, _ := json.Marshal(offer)
		offerMAC := session.SignOffer(sdpJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil {
			return fmt.Errorf("peer: chat send sdp.offer: %w", err)
		}
		answerMsg, err := disp.waitFor(ctx, disp.answer)
		if err != nil {
			return fmt.Errorf("peer: chat wait sdp.answer: %w", err)
		}
		answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
		if err != nil {
			return fmt.Errorf("peer: chat decode sdp.answer: %w", err)
		}
		if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil {
			return fmt.Errorf("peer: chat %w", err)
		}
		var answer webrtc.SessionDescription
		if err := json.Unmarshal(answerJSON, &answer); err != nil {
			return fmt.Errorf("peer: chat unmarshal answer: %w", err)
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			return fmt.Errorf("peer: chat set remote description: %w", err)
		}
	} else {
		fmt.Println("Waiting for sender...")
		offerMsg, err := disp.waitFor(ctx, disp.offer)
		if err != nil {
			return fmt.Errorf("peer: chat wait sdp.offer: %w", err)
		}
		offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
		if err != nil {
			return fmt.Errorf("peer: chat decode sdp.offer: %w", err)
		}
		if err := session.VerifyOffer(offerJSON, offerMAC); err != nil {
			return fmt.Errorf("peer: chat %w", err)
		}
		var offer webrtc.SessionDescription
		if err := json.Unmarshal(offerJSON, &offer); err != nil {
			return fmt.Errorf("peer: chat unmarshal offer: %w", err)
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			return fmt.Errorf("peer: chat set remote description: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("peer: chat create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("peer: chat set local description: %w", err)
		}
		answerJSON, _ := json.Marshal(answer)
		answerMAC := session.SignAnswer(answerJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
			return fmt.Errorf("peer: chat send sdp.answer: %w", err)
		}
	}

	fmt.Println("Direct connection established.")
	select {
	case <-ctx.Done():
		return nil
	case dc := <-dcReady:
		isInitiator := (role == "Sender")
		s := chat.NewSession(dc, "Participant", isInitiator, nil, nil, nil)
		sig.Close()
		return s.RunCLI(ctx)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ChatWithCallback — chat session for Wasm (no stdin REPL)
// ─────────────────────────────────────────────────────────────────────────────

// StartChatSession creates a chat slot, waits for peers, and returns a live
// multi-peer session using the same PAKE+WebRTC infrastructure as file sessions.
// The caller must call sess.Run() in a goroutine.
func StartChatSession(ctx context.Context, sig *signaling.Client, code string, cfg Config, maxPeers int) (*session.Session, error) {
	return StartSession(ctx, sig, code, cfg, maxPeers)
}

// JoinChatSession joins a chat slot by code and returns a live session.
// The caller must call sess.Run() in a goroutine.
func JoinChatSession(ctx context.Context, sig *signaling.Client, code string, cfg Config) (*session.Session, error) {
	return JoinSession(ctx, sig, code, cfg, nil)
}

// The caller sends messages via Send(), leaves quietly via Leave(),
// or ends the session for everyone via Close() (initiator only).
type ChatSession struct {
	dc          *webrtc.DataChannel
	cancel      context.CancelFunc
	IsInitiator bool
}

// Send delivers a text message to the remote peer.
func (s *ChatSession) Send(text string) error {
	return s.dc.Send(transfer.BuildMessageFrame(text))
}

// Close ends the session for everyone (initiator only).
// Sends TagChatClose so all participants are notified to shut down.
func (s *ChatSession) Close() {
	_ = s.dc.Send(transfer.BuildChatCloseFrame())
	s.cancel()
}

// Leave sends TagParticipantLeave (quiet departure) and tears down locally.
// The session continues for other participants.
func (s *ChatSession) Leave() {
	_ = s.dc.Send(transfer.BuildParticipantLeaveFrame())
	s.cancel()
}

// ChatWithCallback performs the PAKE+WebRTC handshake and returns a
// ChatSession.  Incoming messages are delivered via onMessage(from, text).
// onClose is called when the remote peer closes or the connection drops.
// role must be "Sender" (initiator) or "Receiver" (responder).
func ChatWithCallback(
	ctx context.Context,
	sig *signaling.Client,
	code, role string,
	cfg Config,
	onMessage func(from, text string),
	onClose func(reason string),
	onLeave func(who string),
) (*ChatSession, error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	var sharedKey []byte

	if role == "Receiver" {
		msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
		if err != nil {
			return nil, err
		}
		msgA, err := signaling.DecodeOpaque(msgAEnv)
		if err != nil {
			return nil, err
		}
		msgB, sk, err := cpace.Exchange(code, ci, msgA)
		if err != nil {
			return nil, err
		}
		sharedKey = sk
		if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
			return nil, err
		}
	} else {
		msgA, state, err := cpace.Start(code, ci)
		if err != nil {
			return nil, err
		}
		if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil {
			return nil, err
		}
		msg, err := disp.waitFor(ctx, disp.pakeB)
		if err != nil {
			return nil, err
		}
		msgB, err := signaling.DecodeOpaque(msg)
		if err != nil {
			return nil, err
		}
		sk, err := state.Finish(msgB)
		if err != nil {
			return nil, err
		}
		sharedKey = sk
	}

	session, err := pake.NewSession(sharedKey)
	if err != nil {
		return nil, err
	}

	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, err
	}

	dcReady := make(chan *webrtc.DataChannel, 1)
	if role == "Sender" {
		ordered := true
		dc, err := pc.CreateDataChannel("gmmff-chat", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil {
			return nil, err
		}
		dc.OnOpen(func() { dcReady <- dc })
	} else {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) { dcReady <- dc })
	}

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	if role == "Sender" {
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return nil, err
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			return nil, err
		}
		sdpJSON, _ := json.Marshal(offer)
		offerMAC := session.SignOffer(sdpJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil {
			return nil, err
		}
		answerMsg, err := disp.waitFor(ctx, disp.answer)
		if err != nil {
			return nil, err
		}
		answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
		if err != nil {
			return nil, err
		}
		if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil {
			return nil, err
		}
		var answer webrtc.SessionDescription
		if err := json.Unmarshal(answerJSON, &answer); err != nil {
			return nil, err
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			return nil, err
		}
	} else {
		offerMsg, err := disp.waitFor(ctx, disp.offer)
		if err != nil {
			return nil, err
		}
		offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
		if err != nil {
			return nil, err
		}
		if err := session.VerifyOffer(offerJSON, offerMAC); err != nil {
			return nil, err
		}
		var offer webrtc.SessionDescription
		if err := json.Unmarshal(offerJSON, &offer); err != nil {
			return nil, err
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			return nil, err
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return nil, err
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return nil, err
		}
		answerJSON, _ := json.Marshal(answer)
		answerMAC := session.SignAnswer(answerJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
			return nil, err
		}
	}

	ctxChild, cancel := context.WithCancel(ctx)
	select {
	case <-ctxChild.Done():
		cancel()
		return nil, ctxChild.Err()
	case dc := <-dcReady:
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if len(m.Data) == 0 {
				return
			}
			switch m.Data[0] {
			case transfer.TagMessage:
				if onMessage != nil {
					// Label the sender by their role — the simplified chat path
					// does not use the full name-announcement roster protocol.
					from := "Sender"
					if role == "Sender" {
						from = "Receiver"
					}
					onMessage(from, transfer.ParseMessageFrame(m.Data))
				}
			case transfer.TagChatClose, transfer.TagCancelled:
				// Initiator ended the session for everyone.
				if onClose != nil {
					onClose("Session ended.")
				}
				cancel()
			case transfer.TagParticipantLeave:
				// Peer left — chat uses a 1-to-1 data channel so a leave
				// always ends this session regardless of max peers.
				if onClose != nil {
					onClose("The other participant left.")
				}
				cancel()
			}
		})
		dc.OnClose(func() {
			if onClose != nil {
				onClose("Connection closed.")
			}
			cancel()
		})
		return &ChatSession{dc: dc, cancel: cancel, IsInitiator: role == "Sender"}, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StartSession / JoinSession — bidirectional file + message session
// ─────────────────────────────────────────────────────────────────────────────

// StartSession performs PAKE+WebRTC as the initiator and returns a live Session.
// For multi-peer sessions, it continues to accept new peers as they join.
// The caller must call session.Run() in a goroutine.
func StartSession(ctx context.Context, sig *signaling.Client, code string, cfg Config, maxPeers int) (*session.Session, error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	fmt.Println("Waiting for first peer to connect...")

	// Wait for slot.ready — indicates first peer joined and session is live.
	readyMsg, err := disp.waitForControl(ctx, protocol.MsgSlotReady)
	if err != nil {
		return nil, fmt.Errorf("session: wait slot.ready: %w", err)
	}
	var ready protocol.SlotReadyPayload
	if err := json.Unmarshal(readyMsg.Payload, &ready); err != nil {
		return nil, fmt.Errorf("session: decode slot.ready: %w", err)
	}

	// Also consume the peer.joined that arrives at the same time.
	// The first peer.joined arrives alongside slot.ready for the initiator.
	var firstJoinMsg signaling.Message
	select {
	case firstJoinMsg = <-disp.peerJoined:
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("session: timeout waiting for first peer.joined")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var firstJoin protocol.PeerJoinedPayload
	if err := json.Unmarshal(firstJoinMsg.Payload, &firstJoin); err != nil {
		return nil, fmt.Errorf("session: decode peer.joined: %w", err)
	}

	// Run PAKE+WebRTC handshake with the first peer using targeted signaling.
	fmt.Printf("Peer connected (%d/%d) — authenticating...\n", firstJoin.PeerCount, firstJoin.MaxPeers)
	pc, sess, err := initiatorHandshakeWithPeer(ctx, sig, disp, code, cfg, firstJoin.PeerID, ready, maxPeers)
	if err != nil {
		return nil, err
	}

	_ = pc // pc is stored in sess already

	// Spawn a goroutine to handle subsequent peer joins.
	if maxPeers > 2 {
		go initiatorAcceptMorePeers(ctx, sig, disp, code, cfg, sess)
	}

	sessCtx, sessCancel := context.WithCancel(ctx)
	sess.SetContext(sessCtx, sessCancel)
	sess.Sig = sig
	return sess, nil
}

// JoinSession performs PAKE+WebRTC as the responder and returns a live Session.
// The caller must call session.Run() in a goroutine.
// ready is the pre-decoded SlotReadyPayload from the caller — pass nil if
// the caller has not yet consumed slot.ready (JoinSession will read it).
func JoinSession(ctx context.Context, sig *signaling.Client, code string, cfg Config, ready *protocol.SlotReadyPayload) (*session.Session, error) {
	return doSessionHandshake(ctx, sig, code, cfg, false, "", ready)
}

// initiatorHandshakeWithPeer runs PAKE+WebRTC between the initiator and one
// specific peer identified by peerID, using targeted signaling messages.
// Returns the new PeerConnection (already added to sess) and the session.
func initiatorHandshakeWithPeer(
	ctx context.Context,
	sig *signaling.Client,
	disp *dispatcher,
	code string,
	cfg Config,
	peerID string,
	ready protocol.SlotReadyPayload,
	maxPeers int,
) (*webrtc.PeerConnection, *session.Session, error) {
	// Create a targeted sub-dispatcher that filters messages from this peerID.
	td := &targetedDispatcher{peerID: peerID, parent: disp}

	// PAKE
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgA, state, err := cpace.Start(code, ci)
	if err != nil {
		return nil, nil, fmt.Errorf("session: PAKE start: %w", err)
	}
	// Send pake.a targeted to this specific peer.
	if err := sig.SendTargeted(peerID, "", protocol.MustEnvelope(protocol.MsgPakeA,
		protocol.OpaquePayload{Data: signaling.EncodeB64(msgA)})); err != nil {
		return nil, nil, fmt.Errorf("session: send targeted pake.a: %w", err)
	}

	msgBMsg, err := td.waitFor(ctx, protocol.MsgPakeB)
	if err != nil {
		return nil, nil, fmt.Errorf("session: wait pake.b from %s: %w", peerID, err)
	}
	msgBBytes, err := signaling.DecodeOpaque(msgBMsg)
	if err != nil {
		return nil, nil, fmt.Errorf("session: decode pake.b: %w", err)
	}
	sharedKey, err := state.Finish(msgBBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("session: PAKE finish: %w", err)
	}
	pakeSession, err := pake.NewSession(sharedKey)
	if err != nil {
		return nil, nil, fmt.Errorf("session: derive session keys: %w", err)
	}
	fmt.Printf("Authenticated with Participant %s\n", peerID[:8])

	// WebRTC
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, nil, err
	}

	ordered := true
	controlDC, err := pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return nil, nil, fmt.Errorf("session: create control channel: %w", err)
	}
	dcReady := make(chan struct{}, 1)
	controlDC.OnOpen(func() { dcReady <- struct{}{} })

	// Buffer ICE candidates that arrive before SetRemoteDescription.
	// pumpICETargeted and td.waitFor would race on disp.targeted otherwise.
	iceBuf := make(chan protocol.ICECandidatePayload, 64)

	// Collect targeted ICE candidates into the buffer in a goroutine.
	// This runs alongside td.waitFor so neither blocks the other.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-disp.targeted:
				var tp protocol.TargetedPayload
				if err := json.Unmarshal(msg.Payload, &tp); err != nil {
					continue
				}
				var inner signaling.Message
				if err := json.Unmarshal(tp.Inner, &inner); err != nil {
					continue
				}
				if inner.Type == protocol.MsgICECandidate {
					var cp protocol.ICECandidatePayload
					if err := json.Unmarshal(inner.Payload, &cp); err == nil && cp.Candidate != "" {
						iceBuf <- cp
					}
				} else {
					// Not ICE — put back for td.waitFor to pick up.
					go func(m signaling.Message) { disp.targeted <- m }(msg)
				}
			}
		}
	}()

	// Also buffer plain ICE from disp.ice (responder sends un-targeted).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-disp.ice:
				ice, err := signaling.DecodeICE(msg)
				if err == nil && ice.Candidate != "" {
					iceBuf <- ice
				}
			}
		}
	}()

	trickleICETargeted(sig, pc, peerID)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("session: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, nil, fmt.Errorf("session: set local desc: %w", err)
	}
	sdpJSON, _ := json.Marshal(offer)
	offerMAC := pakeSession.SignOffer(sdpJSON)
	inner := protocol.MustEnvelope(protocol.MsgSDPOffer,
		struct {
			SDP string `json:"sdp"`
			MAC string `json:"mac"`
		}{SDP: signaling.EncodeB64(sdpJSON), MAC: offerMAC})
	if err := sig.SendTargeted(peerID, "", inner); err != nil {
		return nil, nil, fmt.Errorf("session: send targeted sdp.offer: %w", err)
	}

	answerMsg, err := td.waitFor(ctx, protocol.MsgSDPAnswer)
	if err != nil {
		return nil, nil, fmt.Errorf("session: wait sdp.answer: %w", err)
	}
	answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
	if err != nil {
		return nil, nil, fmt.Errorf("session: decode sdp.answer: %w", err)
	}
	if err := pakeSession.VerifyAnswer(answerJSON, answerMAC); err != nil {
		return nil, nil, fmt.Errorf("session: %w", err)
	}
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(answerJSON, &answer); err != nil {
		return nil, nil, fmt.Errorf("session: unmarshal answer: %w", err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return nil, nil, fmt.Errorf("session: set remote desc: %w", err)
	}

	// Now that SetRemoteDescription is done, drain the ICE buffer into the PC.
	go func() {
		for cp := range iceBuf {
			fmt.Printf("[ICE] applying buffered remote candidate (initiator): %s\n", candidateType(cp.Candidate))
			mlineIdx := cp.SDPMLineIndex
			mid := cp.SDPMid
			_ = pc.AddICECandidate(webrtc.ICECandidateInit{
				Candidate:     cp.Candidate,
				SDPMid:        &mid,
				SDPMLineIndex: &mlineIdx,
			})
		}
	}()

	select {
	case <-ctx.Done():
		pc.Close()
		return nil, nil, ctx.Err()
	case <-time.After(20 * time.Second):
		pc.Close()
		return nil, nil, fmt.Errorf("session: control channel open timeout for peer %s", peerID[:8])
	case <-dcReady:
	}

	// First peer — create the session object.
	// Subsequent peers will be added via sess.AddPeer().
	sessCtx, sessCancel := context.WithCancel(context.Background())
	sess := session.New(sessCtx, sessCancel, pc, controlDC, cfg, true)
	sess.MaxPeers = maxPeers
	sess.AddPeerInfo(peerID, ready.PeerCount, ready.MaxPeers)
	return pc, sess, nil
}

// initiatorAcceptMorePeers runs in a goroutine and handles subsequent joins.
func initiatorAcceptMorePeers(
	ctx context.Context,
	sig *signaling.Client,
	disp *dispatcher,
	code string,
	cfg Config,
	sess *session.Session,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-disp.peerJoined:
			if !ok {
				return
			}
			var pj protocol.PeerJoinedPayload
			if err := json.Unmarshal(msg.Payload, &pj); err != nil {
				continue
			}
			go func(peerID string, peerCount, maxPeers int) {
				fmt.Printf("New peer connecting (%d/%d)...\n", peerCount, maxPeers)
				td := &targetedDispatcher{peerID: peerID, parent: disp}
				ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
				msgA, state, err := cpace.Start(code, ci)
				if err != nil {
					fmt.Printf("PAKE start failed for peer %s: %v\n", peerID[:8], err)
					return
				}
				if err := sig.SendTargeted(peerID, "", protocol.MustEnvelope(protocol.MsgPakeA,
					protocol.OpaquePayload{Data: signaling.EncodeB64(msgA)})); err != nil {
					return
				}
				msgBMsg, err := td.waitFor(ctx, protocol.MsgPakeB)
				if err != nil {
					return
				}
				msgBBytes, err := signaling.DecodeOpaque(msgBMsg)
				if err != nil {
					return
				}
				sharedKey, err := state.Finish(msgBBytes)
				if err != nil {
					return
				}
				pakeSession, err := pake.NewSession(sharedKey)
				if err != nil {
					return
				}

				pc, err := newPeerConnection(cfg)
				if err != nil {
					return
				}

				ordered := true
				dc, err := pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
				if err != nil {
					pc.Close()
					return
				}
				dcReady := make(chan struct{}, 1)
				dc.OnOpen(func() { dcReady <- struct{}{} })

				trickleICETargeted(sig, pc, peerID)
				go pumpICETargeted(ctx, pc, disp, peerID)

				offer, err := pc.CreateOffer(nil)
				if err != nil {
					pc.Close()
					return
				}
				if err := pc.SetLocalDescription(offer); err != nil {
					pc.Close()
					return
				}
				sdpJSON, _ := json.Marshal(offer)
				offerMAC := pakeSession.SignOffer(sdpJSON)
				inner := protocol.MustEnvelope(protocol.MsgSDPOffer,
					struct {
						SDP string `json:"sdp"`
						MAC string `json:"mac"`
					}{
						SDP: signaling.EncodeB64(sdpJSON), MAC: offerMAC,
					})
				if err := sig.SendTargeted(peerID, "", inner); err != nil {
					pc.Close()
					return
				}

				answerMsg, err := td.waitFor(ctx, protocol.MsgSDPAnswer)
				if err != nil {
					pc.Close()
					return
				}
				answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
				if err != nil {
					pc.Close()
					return
				}
				if err := pakeSession.VerifyAnswer(answerJSON, answerMAC); err != nil {
					pc.Close()
					return
				}
				var answer webrtc.SessionDescription
				if err := json.Unmarshal(answerJSON, &answer); err != nil {
					pc.Close()
					return
				}
				if err := pc.SetRemoteDescription(answer); err != nil {
					pc.Close()
					return
				}

				select {
				case <-ctx.Done():
					pc.Close()
					return
				case <-time.After(20 * time.Second):
					pc.Close()
					return
				case <-dcReady:
				}

				sess.AddPeer(peerID, pc, dc, peerCount, maxPeers)
				fmt.Printf("Participant connected (%d/%d)\n", peerCount, maxPeers)
			}(pj.PeerID, pj.PeerCount, pj.MaxPeers)
		}
	}
}

// targetedDispatcher filters targeted messages from a specific peer.
type targetedDispatcher struct {
	peerID string
	parent *dispatcher
}

func (td *targetedDispatcher) waitFor(ctx context.Context, msgType string) (signaling.Message, error) {
	// plainCh is the direct (un-targeted) channel for this message type.
	// The responder sends pake.b and sdp.answer as plain messages which the
	// broker routes to the initiator; those land in disp.pakeB / disp.answer,
	// not disp.targeted. We accept from both paths.
	var plainCh <-chan signaling.Message
	switch msgType {
	case protocol.MsgPakeB:
		plainCh = td.parent.pakeB
	case protocol.MsgPakeA:
		plainCh = td.parent.pakeA
	case protocol.MsgSDPAnswer:
		plainCh = td.parent.answer
	case protocol.MsgSDPOffer:
		plainCh = td.parent.offer
	}

	for {
		select {
		case <-ctx.Done():
			return signaling.Message{}, ctx.Err()

		case msg, ok := <-plainCh:
			if !ok {
				return signaling.Message{}, fmt.Errorf("channel closed")
			}
			return msg, nil

		case msg := <-td.parent.targeted:
			var tp protocol.TargetedPayload
			if err := json.Unmarshal(msg.Payload, &tp); err != nil {
				continue
			}
			// Unwrap and check the inner message type.
			var inner signaling.Message
			if err := json.Unmarshal(tp.Inner, &inner); err != nil {
				continue
			}
			if inner.Type == msgType {
				return inner, nil
			}
			// Different type — put it back for another goroutine.
			go func(m signaling.Message) {
				td.parent.targeted <- m
			}(msg)
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// trickleICETargeted sends ICE candidates targeted to a specific peer.
func trickleICETargeted(sig *signaling.Client, pc *webrtc.PeerConnection, peerID string) {
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		ci := c.ToJSON()
		s := iceString(c, ci.Candidate)
		if s == "" {
			return // skip empty end-of-candidates marker
		}
		inner := protocol.MustEnvelope(protocol.MsgICECandidate, protocol.ICECandidatePayload{
			Candidate:     s,
			SDPMid:        *ci.SDPMid,
			SDPMLineIndex: *ci.SDPMLineIndex,
		})
		_ = sig.SendTargeted(peerID, "", inner)
	})
}

// pumpICETargeted processes incoming ICE candidates for a specific peer.
// Accepts both targeted (MsgTargeted wrapping MsgICECandidate) and plain
// MsgICECandidate messages — the responder sends plain ICE candidates which
// the broker routes to the initiator and land in disp.ice.
// If peerID is empty, targeted ICE from any peer is accepted.
func pumpICETargeted(ctx context.Context, pc *webrtc.PeerConnection, disp *dispatcher, peerID string) {
	processCandidate := func(cp protocol.ICECandidatePayload) {
		mlineIdx := cp.SDPMLineIndex
		mid := cp.SDPMid
		_ = pc.AddICECandidate(webrtc.ICECandidateInit{
			Candidate:     cp.Candidate,
			SDPMid:        &mid,
			SDPMLineIndex: &mlineIdx,
		})
	}

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-disp.ice:
			// Plain ICE candidate (from responder via broker relay).
			var cp protocol.ICECandidatePayload
			if err := json.Unmarshal(msg.Payload, &cp); err != nil {
				continue
			}
			processCandidate(cp)

		case msg := <-disp.targeted:
			var tp protocol.TargetedPayload
			if err := json.Unmarshal(msg.Payload, &tp); err != nil {
				continue
			}
			// Accept from any peer if peerID is empty, otherwise filter.
			if peerID != "" && tp.FromPeerID != peerID {
				go func(m signaling.Message) { disp.targeted <- m }(msg)
				continue
			}
			var inner signaling.Message
			if err := json.Unmarshal(tp.Inner, &inner); err != nil {
				continue
			}
			if inner.Type != protocol.MsgICECandidate {
				go func(m signaling.Message) { disp.targeted <- m }(msg)
				continue
			}
			var cp protocol.ICECandidatePayload
			if err := json.Unmarshal(inner.Payload, &cp); err != nil {
				continue
			}
			processCandidate(cp)
		}
	}
}

// doSessionHandshake does PAKE + WebRTC + control DC for the responder (joiner).
// ready must be the already-decoded SlotReadyPayload from the caller's WaitFor.
// The initiator uses StartSession + initiatorHandshakeWithPeer instead.
func doSessionHandshake(ctx context.Context, sig *signaling.Client, code string, cfg Config, isInitiator bool, _ string, ready *protocol.SlotReadyPayload) (*session.Session, error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// If the caller hasn't pre-read slot.ready, consume it now.
	if ready == nil {
		readyMsg, err := disp.waitForControl(ctx, protocol.MsgSlotReady)
		if err != nil {
			return nil, fmt.Errorf("session: wait slot.ready: %w", err)
		}
		var r protocol.SlotReadyPayload
		_ = json.Unmarshal(readyMsg.Payload, &r)
		ready = &r
	}

	// Drain any peer.joined notifications — responder doesn't need them for
	// the handshake but they may arrive before pake.a.
	go func() {
		for range disp.peerJoined {
			// discard
		}
	}()

	// Responder receives targeted pake.a from initiator.
	// The initiator sends via MsgTargeted so we read from disp.targeted.
	// For 2-peer sessions the broker also relays un-targeted pake.a.
	// We accept from either channel.
	var pakeAMsg signaling.Message
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case pakeAMsg = <-disp.pakeA:
		// Un-targeted (2-peer legacy path)
	case raw := <-disp.targeted:
		var tp protocol.TargetedPayload
		if err := json.Unmarshal(raw.Payload, &tp); err != nil {
			return nil, fmt.Errorf("session: decode targeted pake.a: %w", err)
		}
		var inner signaling.Message
		if err := json.Unmarshal(tp.Inner, &inner); err != nil {
			return nil, fmt.Errorf("session: decode inner pake.a: %w", err)
		}
		pakeAMsg = inner
	}

	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgABytes, err := signaling.DecodeOpaque(pakeAMsg)
	if err != nil {
		return nil, fmt.Errorf("session: decode pake.a: %w", err)
	}
	msgB, sharedKey, err := cpace.Exchange(code, ci, msgABytes)
	if err != nil {
		return nil, fmt.Errorf("session: PAKE exchange: %w", err)
	}
	// Send pake.b targeted back to initiator.
	if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
		return nil, fmt.Errorf("session: send pake.b: %w", err)
	}

	pakeSession, err := pake.NewSession(sharedKey)
	if err != nil {
		return nil, fmt.Errorf("session: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC — responder waits for the SDP offer from initiator.
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, err
	}

	dcReady := make(chan *webrtc.DataChannel, 1)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "control" {
			dcReady <- dc
		}
	})

	// Buffer incoming ICE candidates — both plain (disp.ice) and targeted
	// (disp.targeted wrapping MsgICECandidate). We must not call
	// AddICECandidate before SetRemoteDescription or Pion silently drops them.
	iceBuf := make(chan protocol.ICECandidatePayload, 64)

	// Drain targeted channel: ICE goes to buffer, everything else goes back.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-disp.targeted:
				var tp protocol.TargetedPayload
				if err := json.Unmarshal(msg.Payload, &tp); err != nil {
					continue
				}
				var inner signaling.Message
				if err := json.Unmarshal(tp.Inner, &inner); err != nil {
					continue
				}
				if inner.Type == protocol.MsgICECandidate {
					var cp protocol.ICECandidatePayload
					if err := json.Unmarshal(inner.Payload, &cp); err == nil && cp.Candidate != "" {
						iceBuf <- cp
					}
				} else {
					go func(m signaling.Message) { disp.targeted <- m }(msg)
				}
			}
		}
	}()

	// Drain plain ICE (un-targeted relay from broker).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-disp.ice:
				ice, err := signaling.DecodeICE(msg)
				if err == nil && ice.Candidate != "" {
					iceBuf <- ice
				}
			}
		}
	}()

	trickleICE(sig, pc)

	// Receive the SDP offer — may arrive as targeted or direct.
	var offerMsg signaling.Message
	select {
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	case offerMsg = <-disp.offer:
		// Direct (2-peer plain relay)
	case raw := <-disp.targeted:
		var tp protocol.TargetedPayload
		if err := json.Unmarshal(raw.Payload, &tp); err != nil {
			pc.Close()
			return nil, fmt.Errorf("session: decode targeted offer: %w", err)
		}
		var inner signaling.Message
		if err := json.Unmarshal(tp.Inner, &inner); err != nil {
			pc.Close()
			return nil, fmt.Errorf("session: decode inner offer: %w", err)
		}
		offerMsg = inner
	}

	offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: decode sdp.offer: %w", err)
	}
	if err := pakeSession.VerifyOffer(offerJSON, offerMAC); err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: verify offer: %w", err)
	}
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(offerJSON, &offer); err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: unmarshal offer: %w", err)
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: set remote description: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: set local description: %w", err)
	}
	answerJSON, _ := json.Marshal(answer)
	answerMAC := pakeSession.SignAnswer(answerJSON)
	if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
		pc.Close()
		return nil, fmt.Errorf("session: send sdp.answer: %w", err)
	}

	// SetRemoteDescription done — now drain buffered ICE candidates into the PC.
	go func() {
		for cp := range iceBuf {
			fmt.Printf("[ICE] applying buffered remote candidate (responder): %s\n", candidateType(cp.Candidate))
			mlineIdx := cp.SDPMLineIndex
			mid := cp.SDPMid
			_ = pc.AddICECandidate(webrtc.ICECandidateInit{
				Candidate:     cp.Candidate,
				SDPMid:        &mid,
				SDPMLineIndex: &mlineIdx,
			})
		}
	}()

	fmt.Println("Direct connection established.")
	var controlDC *webrtc.DataChannel
	select {
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		pc.Close()
		return nil, fmt.Errorf("session: control channel open timeout")
	case controlDC = <-dcReady:
	}

	sessCtx, sessCancel := context.WithCancel(ctx)
	s := session.New(sessCtx, sessCancel, pc, controlDC, cfg, false)
	s.Sig = sig
	// Apply peer count info from slot.ready if provided.
	if ready != nil {
		s.MaxPeers = ready.MaxPeers
		if ready.PeerCount > 0 {
			s.AddPeerInfo("initiator", ready.PeerCount, ready.MaxPeers)
		}
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func newPeerConnection(cfg Config) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: iceServers(cfg),
	})
	if err != nil {
		return nil, fmt.Errorf("peer: new PeerConnection: %w", err)
	}

	// Verbose ICE diagnostics — log every state transition and candidate.
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		fmt.Printf("[ICE] connection state: %s\n", s)
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("[PC]  connection state: %s\n", s)
	})
	pc.OnSignalingStateChange(func(s webrtc.SignalingState) {
		fmt.Printf("[PC]  signaling state: %s\n", s)
	})

	return pc, nil
}

// candidateType extracts just the candidate type (host/srflx/relay/prflx)
// from an SDP candidate string without exposing IP addresses.
func candidateType(candidate string) string {
	parts := strings.Fields(candidate)
	for i, p := range parts {
		if p == "typ" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if strings.Contains(candidate, "local") {
		return "host (.local mDNS)"
	}
	return "unknown"
}

// iceString returns a fully RFC 8445 compliant SDP ICE candidate string.
//
// Pion's ICECandidate.ToJSON().Candidate is the correct SDP attribute value
// (e.g. "candidate:xxx 1 udp 1677729535 1.2.3.4 1234 typ srflx") but omits
// the mandatory raddr/rport fields for non-host candidates in some versions.
// ICECandidate.String() is a human-readable DEBUG format, NOT valid SDP —
// never use it as a candidate string.
//
// Strategy: use ToJSON().Candidate as the base and append raddr/rport for
// srflx/relay/prflx candidates when those fields are missing.
func iceString(c *webrtc.ICECandidate, base string) string {
	if base == "" {
		return ""
	}
	switch c.Typ {
	case webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypeRelay, webrtc.ICECandidateTypePrflx:
		if !strings.Contains(base, " raddr ") {
			raddr := c.RelatedAddress
			if raddr == "" {
				raddr = "0.0.0.0"
			}
			// Strip brackets from IPv6 addresses — they're invalid in SDP.
			raddr = strings.TrimPrefix(raddr, "[")
			raddr = strings.TrimSuffix(raddr, "]")
			return fmt.Sprintf("%s raddr %s rport %d", base, raddr, c.RelatedPort)
		}
	}
	return base
}

// fixCandidate is kept for compatibility but no longer used.
func fixCandidate(c *webrtc.ICECandidate, s string) string {
	return iceString(c, s)
}

func trickleICE(sig *signaling.Client, pc *webrtc.PeerConnection) {
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			fmt.Println("[ICE] local gathering complete (nil candidate)")
			return
		}
		init := c.ToJSON()
		mid := ""
		if init.SDPMid != nil {
			mid = *init.SDPMid
		}
		idx := uint16(0)
		if init.SDPMLineIndex != nil {
			idx = *init.SDPMLineIndex
		}
		s := iceString(c, init.Candidate)
		if s == "" {
			return
		}
		fmt.Printf("[ICE] sending local candidate: %s\n", candidateType(s))
		_ = sig.SendICE(s, mid, idx)
	})
}

// DefaultOutDir ensures outDir exists and returns it (defaults to ".").
func DefaultOutDir(outDir string) string {
	if outDir == "" {
		outDir = "."
	}
	_ = os.MkdirAll(outDir, 0o755)
	return outDir
}
