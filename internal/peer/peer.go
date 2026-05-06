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
	"fmt"
	"os"

	"filippo.io/cpace"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/pion/webrtc/v4"
)

// DefaultSTUN is the STUN server used when none is configured.
const DefaultSTUN = "stun:stun.l.google.com:19302"

// Config holds peer connection settings.
type Config struct {
	STUNServer string
}

func (c Config) stunURL() string {
	if c.STUNServer != "" {
		return c.STUNServer
	}
	return DefaultSTUN
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

func Send(ctx context.Context, sig *signaling.Client, code, filePath string, cfg Config) error {
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
	if _, err := state.Finish(msgB); err != nil {
		return fmt.Errorf("peer: PAKE finish: %w — wrong code or tampered connection", err)
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
		case transfer.TagTransferOK:
			select {
			case okCh <- struct{}{}:
			default:
			}
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
	if err := sig.SendSDP(protocol.MsgSDPOffer, sdpJSON); err != nil {
		return fmt.Errorf("peer: send sdp.offer: %w", err)
	}

	answerMsg, err := disp.waitFor(ctx, disp.answer)
	if err != nil {
		return fmt.Errorf("peer: wait sdp.answer: %w", err)
	}
	answerJSON, err := signaling.DecodeOpaque(answerMsg)
	if err != nil {
		return fmt.Errorf("peer: decode sdp.answer: %w", err)
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

	sender := transfer.NewSender(dc, filePath, ackCh)
	if err := sender.Run(); err != nil {
		return fmt.Errorf("peer: transfer: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
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
	msgB, _, err := cpace.Exchange(code, ci, msgA)
	if err != nil {
		return fmt.Errorf("peer: PAKE exchange: %w", err)
	}
	if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil {
		return fmt.Errorf("peer: send pake.b: %w", err)
	}
	fmt.Println("Handshake complete — connection authenticated")

	// WebRTC
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()

	transferDone := make(chan error, 1)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		rs := transfer.NewReceiveState(outDir, func(seq uint64) error {
			return dc.Send(transfer.BuildAckFrame(seq))
		})
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			done, err := rs.Feed(m.Data)
			if err != nil {
				_ = dc.Send(transfer.BuildErrorFrame("ERR_RECEIVE", err.Error()))
				select {
				case transferDone <- err:
				default:
				}
				return
			}
			if done {
				_ = dc.Send([]byte{transfer.TagTransferOK})
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
	offerJSON, err := signaling.DecodeOpaque(offerMsg)
	if err != nil {
		return fmt.Errorf("peer: decode sdp.offer: %w", err)
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
	if err := sig.SendSDP(protocol.MsgSDPAnswer, answerJSON); err != nil {
		return fmt.Errorf("peer: send sdp.answer: %w", err)
	}

	fmt.Println("Direct connection established — receiving file")

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-transferDone:
		sig.Close()
		return err
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func newPeerConnection(cfg Config) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{cfg.stunURL()}}},
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
