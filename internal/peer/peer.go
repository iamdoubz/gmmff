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

	"filippo.io/cpace"
	"github.com/iamdoubz/gmmff/internal/chat"
	"github.com/iamdoubz/gmmff/internal/pake"
	"github.com/iamdoubz/gmmff/internal/peerconfig"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/session"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/internal/turn"
	"github.com/iamdoubz/gmmff/pkg/protocol"
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
func iceServers(c Config) []webrtc.ICEServer {
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
	pakeA   chan signaling.Message
	pakeB   chan signaling.Message
	offer   chan signaling.Message
	answer  chan signaling.Message
	ice     chan signaling.Message
	control chan signaling.Message
}

func newDispatcher() *dispatcher {
	return &dispatcher{
		pakeA:   make(chan signaling.Message, 4),
		pakeB:   make(chan signaling.Message, 4),
		offer:   make(chan signaling.Message, 4),
		answer:  make(chan signaling.Message, 4),
		ice:     make(chan signaling.Message, 64),
		control: make(chan signaling.Message, 8),
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
		case msg := <-ch:
			return msg, nil
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
	cancelDC     := make(chan *webrtc.DataChannel, 1)

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

// ChatSession is the handle returned by ChatWithCallback.
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
		if err != nil { return nil, err }
		msgA, err := signaling.DecodeOpaque(msgAEnv)
		if err != nil { return nil, err }
		msgB, sk, err := cpace.Exchange(code, ci, msgA)
		if err != nil { return nil, err }
		sharedKey = sk
		if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil { return nil, err }
	} else {
		msgA, state, err := cpace.Start(code, ci)
		if err != nil { return nil, err }
		if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil { return nil, err }
		msg, err := disp.waitFor(ctx, disp.pakeB)
		if err != nil { return nil, err }
		msgB, err := signaling.DecodeOpaque(msg)
		if err != nil { return nil, err }
		sk, err := state.Finish(msgB)
		if err != nil { return nil, err }
		sharedKey = sk
	}

	session, err := pake.NewSession(sharedKey)
	if err != nil { return nil, err }

	pc, err := newPeerConnection(cfg)
	if err != nil { return nil, err }

	dcReady := make(chan *webrtc.DataChannel, 1)
	if role == "Sender" {
		ordered := true
		dc, err := pc.CreateDataChannel("gmmff-chat", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil { return nil, err }
		dc.OnOpen(func() { dcReady <- dc })
	} else {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) { dcReady <- dc })
	}

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	if role == "Sender" {
		offer, err := pc.CreateOffer(nil)
		if err != nil { return nil, err }
		if err := pc.SetLocalDescription(offer); err != nil { return nil, err }
		sdpJSON, _ := json.Marshal(offer)
		offerMAC := session.SignOffer(sdpJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil { return nil, err }
		answerMsg, err := disp.waitFor(ctx, disp.answer)
		if err != nil { return nil, err }
		answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
		if err != nil { return nil, err }
		if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil { return nil, err }
		var answer webrtc.SessionDescription
		if err := json.Unmarshal(answerJSON, &answer); err != nil { return nil, err }
		if err := pc.SetRemoteDescription(answer); err != nil { return nil, err }
	} else {
		offerMsg, err := disp.waitFor(ctx, disp.offer)
		if err != nil { return nil, err }
		offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
		if err != nil { return nil, err }
		if err := session.VerifyOffer(offerJSON, offerMAC); err != nil { return nil, err }
		var offer webrtc.SessionDescription
		if err := json.Unmarshal(offerJSON, &offer); err != nil { return nil, err }
		if err := pc.SetRemoteDescription(offer); err != nil { return nil, err }
		answer, err := pc.CreateAnswer(nil)
		if err != nil { return nil, err }
		if err := pc.SetLocalDescription(answer); err != nil { return nil, err }
		answerJSON, _ := json.Marshal(answer)
		answerMAC := session.SignAnswer(answerJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil { return nil, err }
	}

	ctxChild, cancel := context.WithCancel(ctx)
	select {
	case <-ctxChild.Done():
		cancel()
		return nil, ctxChild.Err()
	case dc := <-dcReady:
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if len(m.Data) == 0 { return }
			switch m.Data[0] {
			case transfer.TagMessage:
				if onMessage != nil {
					onMessage("Participant", transfer.ParseMessageFrame(m.Data))
				}
			case transfer.TagChatClose, transfer.TagCancelled:
				// Initiator ended the session for everyone.
				if onClose != nil {
					onClose("Session ended by Participant.")
				}
				cancel()
			case transfer.TagParticipantLeave:
				// Participant left quietly — notify but do NOT cancel the session.
				if onLeave != nil {
					onLeave("Participant")
				}
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
// The caller must call session.Run() in a goroutine.
func StartSession(ctx context.Context, sig *signaling.Client, code string, cfg Config) (*session.Session, error) {
	return doSessionHandshake(ctx, sig, code, cfg, true)
}

// JoinSession performs PAKE+WebRTC as the responder and returns a live Session.
// The caller must call session.Run() in a goroutine.
func JoinSession(ctx context.Context, sig *signaling.Client, code string, cfg Config) (*session.Session, error) {
	return doSessionHandshake(ctx, sig, code, cfg, false)
}

// doSessionHandshake does PAKE + WebRTC + control DC for both roles.
func doSessionHandshake(ctx context.Context, sig *signaling.Client, code string, cfg Config, isInitiator bool) (*session.Session, error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// ── PAKE ──────────────────────────────────────────────────────────────────
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	var sharedKey []byte

	if isInitiator {
		msgA, state, err := cpace.Start(code, ci)
		if err != nil {
			return nil, fmt.Errorf("session: PAKE start: %w", err)
		}
		if err := sig.SendOpaque(protocol.MsgPakeA, msgA); err != nil {
			return nil, fmt.Errorf("session: send pake.a: %w", err)
		}
		msg, err := disp.waitFor(ctx, disp.pakeB)
		if err != nil {
			return nil, fmt.Errorf("session: wait pake.b: %w", err)
		}
		msgB, err := signaling.DecodeOpaque(msg)
		if err != nil {
			return nil, fmt.Errorf("session: decode pake.b: %w", err)
		}
		sk, err := state.Finish(msgB)
		if err != nil {
			return nil, fmt.Errorf("session: PAKE finish: %w — wrong code or tampered connection", err)
		}
		sharedKey = sk
	} else {
		msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
		if err != nil {
			return nil, fmt.Errorf("session: wait pake.a: %w", err)
		}
		msgA, err := signaling.DecodeOpaque(msgAEnv)
		if err != nil {
			return nil, fmt.Errorf("session: decode pake.a: %w", err)
		}
		msgB, sk, err := cpace.Exchange(code, ci, msgA)
		if err != nil {
			return nil, fmt.Errorf("session: PAKE exchange: %w", err)
		}
		sharedKey = sk
		if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
			return nil, fmt.Errorf("session: send pake.b: %w", err)
		}
	}

	sess, err := pake.NewSession(sharedKey)
	if err != nil {
		return nil, fmt.Errorf("session: derive session keys: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// ── WebRTC ────────────────────────────────────────────────────────────────
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, err
	}

	var controlDC *webrtc.DataChannel
	dcReady := make(chan *webrtc.DataChannel, 1)

	if isInitiator {
		ordered := true
		dc, err := pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil {
			return nil, fmt.Errorf("session: create control channel: %w", err)
		}
		dc.OnOpen(func() { dcReady <- dc })
	} else {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) {
			if dc.Label() == "control" {
				dcReady <- dc
			}
		})
	}

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	if isInitiator {
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return nil, fmt.Errorf("session: create offer: %w", err)
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			return nil, fmt.Errorf("session: set local description: %w", err)
		}
		sdpJSON, _ := json.Marshal(offer)
		offerMAC := sess.SignOffer(sdpJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC); err != nil {
			return nil, fmt.Errorf("session: send sdp.offer: %w", err)
		}
		answerMsg, err := disp.waitFor(ctx, disp.answer)
		if err != nil {
			return nil, fmt.Errorf("session: wait sdp.answer: %w", err)
		}
		answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
		if err != nil {
			return nil, fmt.Errorf("session: decode sdp.answer: %w", err)
		}
		if err := sess.VerifyAnswer(answerJSON, answerMAC); err != nil {
			return nil, fmt.Errorf("session: %w", err)
		}
		var answer webrtc.SessionDescription
		if err := json.Unmarshal(answerJSON, &answer); err != nil {
			return nil, fmt.Errorf("session: unmarshal answer: %w", err)
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			return nil, fmt.Errorf("session: set remote description: %w", err)
		}
	} else {
		fmt.Println("Waiting for initiator...")
		offerMsg, err := disp.waitFor(ctx, disp.offer)
		if err != nil {
			return nil, fmt.Errorf("session: wait sdp.offer: %w", err)
		}
		offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
		if err != nil {
			return nil, fmt.Errorf("session: decode sdp.offer: %w", err)
		}
		if err := sess.VerifyOffer(offerJSON, offerMAC); err != nil {
			return nil, fmt.Errorf("session: %w", err)
		}
		var offer webrtc.SessionDescription
		if err := json.Unmarshal(offerJSON, &offer); err != nil {
			return nil, fmt.Errorf("session: unmarshal offer: %w", err)
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			return nil, fmt.Errorf("session: set remote description: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return nil, fmt.Errorf("session: create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return nil, fmt.Errorf("session: set local description: %w", err)
		}
		answerJSON, _ := json.Marshal(answer)
		answerMAC := sess.SignAnswer(answerJSON)
		if err := sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC); err != nil {
			return nil, fmt.Errorf("session: send sdp.answer: %w", err)
		}
	}

	fmt.Println("Direct connection established.")
	select {
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		pc.Close()
		return nil, fmt.Errorf("session: control channel open timeout")
	case controlDC = <-dcReady:
	}

	sig.Close()

	sessCtx, sessCancel := context.WithCancel(ctx)
	return session.New(sessCtx, sessCancel, pc, controlDC, cfg, isInitiator), nil
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
	return pc, nil
}

func trickleICE(sig *signaling.Client, pc *webrtc.PeerConnection) {
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
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
		_ = sig.SendICE(init.Candidate, mid, idx)
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
