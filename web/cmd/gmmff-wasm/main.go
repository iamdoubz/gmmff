// Command gmmff-wasm is the WebAssembly build of gmmff for browser use.
//
// It exposes two JavaScript functions:
//
//	window.gmmffSend(file, serverURL)
//	window.gmmffReceive(code, serverURL)
//
// And calls back into JavaScript via the window.ui* functions defined in
// index.html to drive the UI.  All transfer logic lives in the shared
// internal packages — this file is purely the JS↔Go bridge.
//
// Build:
//
//	GOOS=js GOARCH=wasm go build -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm
//
// The wasm_exec.js runtime shim must be copied from your Go installation:
//
//	cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/static/wasm_exec.js
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"
	"time"

	"github.com/iamdoubz/gmmff/internal/archive"
	"github.com/iamdoubz/gmmff/internal/peer"
	filippo.io/cpace
	github.com/iamdoubz/gmmff/internal/pake
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
)

func main() {
	js.Global().Set("gmmffSend", js.FuncOf(jsSend))
	js.Global().Set("gmmffReceive", js.FuncOf(jsReceive))
	js.Global().Set("gmmffChat", js.FuncOf(jsChat))
	js.Global().Set("gmmffChatSend", js.FuncOf(jsChatSend))
	js.Global().Set("gmmffChatJoin", js.FuncOf(jsChatJoin))

	// Block forever — Go Wasm must not exit or the runtime shuts down.
	select {}
}

// ─────────────────────────────────────────────────────────────────────────────
// Send
// ─────────────────────────────────────────────────────────────────────────────

// jsSend is called from JS as: window.gmmffSend(file, serverURL)
// file is a JS File object from an <input type="file">.
func jsSend(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	jsFiles   := args[0] // JS Array of File objects
	serverURL := args[1].String()

	ctx, cancelCtx := context.WithCancel(context.Background())

	// Register cancel function so the UI cancel button can stop the transfer.
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		cancelCtx()
		return nil
	})
	js.Global().Call("uiRegisterCancel", cancelFn)

	go func() {
		defer cancelCtx()
		defer cancelFn.Release()

		uiStatusKey("status_connecting", "send")

		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil {
			uiError(err.Error(), "send")
			return
		}

		if err := sig.CreateSlot(); err != nil {
			uiError(err.Error(), "send")
			return
		}

		createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
		if err != nil {
			uiError(err.Error(), "send")
			return
		}

		var created protocol.SlotCreatedPayload
		if err := json.Unmarshal(createdMsg.Payload, &created); err != nil {
			uiError(err.Error(), "send")
			return
		}

		// Show the code in the UI.
		js.Global().Call("uiShowCode", created.Code)
		uiStatusKey("code_waiting", "send")

		if _, err = sig.WaitFor(ctx, protocol.MsgSlotReady); err != nil {
			uiError(err.Error(), "send")
			return
		}

		// Read all JS File objects into memory and zip if needed.
		namedFiles, err := jsFilesToNamedFiles(jsFiles)
		if err != nil {
			uiError(err.Error(), "send")
			return
		}
		fileData, fileName, err := archive.ZipFilesFromMemory(namedFiles)
		if err != nil {
			uiError(err.Error(), "send")
			return
		}

		cfg := peer.Config{
			WindowSize: transfer.DefaultWindowSize,
			ChunkSize:  transfer.DefaultChunkSize,
		}

		fileSize := int64(len(fileData))
		progress := makeProgressFn("send", fileSize)
		if err := peer.SendBytes(ctx, sig, created.Code, fileName, fileData, cfg, progress, ""); err != nil {
			uiError(err.Error(), "send")
			return
		}
		js.Global().Call("uiDone", "send", "")
	}()

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Receive
// ─────────────────────────────────────────────────────────────────────────────

// jsReceive is called from JS as: window.gmmffReceive(code, serverURL)
func jsReceive(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	code      := args[0].String()
	serverURL := args[1].String()

	ctx, cancelCtx := context.WithCancel(context.Background())

	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		cancelCtx()
		return nil
	})
	js.Global().Call("uiRegisterCancel", cancelFn)

	go func() {
		defer cancelCtx()
		defer cancelFn.Release()

		uiStatusKey("status_connecting", "receive")

		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil {
			uiError(err.Error(), "receive")
			return
		}

		if err := sig.JoinSlot(code); err != nil {
			uiError(err.Error(), "receive")
			return
		}

		if _, err = sig.WaitFor(ctx, protocol.MsgSlotReady); err != nil {
			uiError(err.Error(), "receive")
			return
		}

		cfg := peer.Config{
			WindowSize: transfer.DefaultWindowSize,
			ChunkSize:  transfer.DefaultChunkSize,
		}

		// ReceiveToBytes keeps everything in memory — no filesystem access.
		// Progress total is unknown until the FileHeader arrives, so we pass
		// -1 initially and the JS side handles that gracefully.
		progress := makeProgressFn("receive", -1)
		fileName, fileData, err := peer.ReceiveToBytes(ctx, sig, code, cfg, progress)
		if err != nil {
			uiError(err.Error(), "receive")
			return
		}
		if len(fileData) > 0 {
			js.Global().Call("uiDone", "receive", "")
			browserDownload(fileName, fileData)
		}
	}()

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// JS File → Wasm in-memory temp file
// ─────────────────────────────────────────────────────────────────────────────

// jsFilesToNamedFiles reads a JS Array of File objects into archive.NamedFile slices.
// webkitRelativePath is used when available to preserve directory structure.
func jsFilesToNamedFiles(jsFiles js.Value) ([]archive.NamedFile, error) {
	count := jsFiles.Length()
	if count == 0 {
		return nil, fmt.Errorf("no files selected")
	}
	result := make([]archive.NamedFile, 0, count)
	for i := 0; i < count; i++ {
		f := jsFiles.Index(i)
		data, err := readJSFile(f)
		if err != nil {
			return nil, err
		}
		zipPath := f.Get("webkitRelativePath").String()
		if zipPath == "" {
			zipPath = f.Get("name").String()
		}
		result = append(result, archive.NamedFile{ZipPath: zipPath, Data: data})
	}
	return result, nil
}

// readJSFile reads a single JS File object into a []byte via FileReader.
func readJSFile(jsFile js.Value) ([]byte, error) {
	size := jsFile.Get("size").Int()
	done := make(chan error, 1)
	buf  := make([]byte, size)
	reader := js.Global().Get("FileReader").New()
	onLoad := js.FuncOf(func(_ js.Value, args []js.Value) any {
		arr := js.Global().Get("Uint8Array").New(args[0].Get("target").Get("result"))
		js.CopyBytesToGo(buf, arr)
		done <- nil
		return nil
	})
	onErr := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		done <- fmt.Errorf("FileReader error")
		return nil
	})
	defer onLoad.Release()
	defer onErr.Release()
	reader.Set("onload", onLoad)
	reader.Set("onerror", onErr)
	reader.Call("readAsArrayBuffer", jsFile)
	if err := <-done; err != nil {
		return nil, err
	}
	return buf, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Chat
// ─────────────────────────────────────────────────────────────────────────────

// activeChatDC holds the open data channel for the current chat session.
var activeChatDC *webrtc.DataChannel

// jsChat is called from JS as: window.gmmffChat(serverURL)
// Initiator path — creates a slot, shows code, opens chat.
func jsChat(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	serverURL := args[0].String()
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any { cancelCtx(); return nil })
	js.Global().Call("uiRegisterCancel", cancelFn)
	go func() {
		defer cancelCtx()
		defer cancelFn.Release()
		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		if err := sig.CreateSlot(); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		var created protocol.SlotCreatedPayload
		if err := json.Unmarshal(createdMsg.Payload, &created); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		js.Global().Call("uiChatShowCode", created.Code)
		if _, err = sig.WaitFor(ctx, protocol.MsgSlotReady); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		dc, err := runChatSession(ctx, sig, created.Code, "Receiver", peer.Config{})
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		activeChatDC = dc
	}()
	return nil
}

// jsChatJoin is called from JS as: window.gmmffChatJoin(code, serverURL)
// Responder path — joins with the given code.
func jsChatJoin(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	code, serverURL := args[0].String(), args[1].String()
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any { cancelCtx(); return nil })
	js.Global().Call("uiRegisterCancel", cancelFn)
	go func() {
		defer cancelCtx()
		defer cancelFn.Release()
		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		if err := sig.JoinSlot(code); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		if _, err = sig.WaitFor(ctx, protocol.MsgSlotReady); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		dc, err := runChatSession(ctx, sig, code, "Sender", peer.Config{})
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		activeChatDC = dc
	}()
	return nil
}

// jsChatSend sends a message on the active chat data channel.
func jsChatSend(_ js.Value, args []js.Value) any {
	if len(args) < 1 || activeChatDC == nil {
		return nil
	}
	_ = activeChatDC.Send(transfer.BuildMessageFrame(args[0].String()))
	return nil
}

// jsChat (legacy — kept for back-compat, maps to jsChat initiator)
func jsChatNoop(_ js.Value, _ []js.Value) any { return nil }

// runChatSession sets up the WebRTC data channel for chat and wires JS callbacks.
// Returns the open data channel so jsChatSend can use it.
func runChatSession(ctx context.Context, sig *signaling.Client, code, remoteRole string, cfg peer.Config) (*webrtc.DataChannel, error) {
	disp := newDispatcher()
	go disp.run(ctx, sig.Recv())

	// PAKE
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	var sharedKey []byte
	if remoteRole == "Sender" {
		// We are Receiver (joined)
		msgAEnv, err := disp.waitFor(ctx, disp.pakeA)
		if err != nil { return nil, err }
		msgA, err := signaling.DecodeOpaque(msgAEnv)
		if err != nil { return nil, err }
		msgB, sk, err := cpace.Exchange(code, ci, msgA)
		if err != nil { return nil, err }
		sharedKey = sk
		if err := sig.SendOpaque(protocol.MsgPakeB, msgB); err != nil { return nil, err }
	} else {
		// We are Sender (initiated)
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

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{cfg.STUNServer}}},
	})
	if err != nil { return nil, err }

	dcReady := make(chan *webrtc.DataChannel, 1)
	if remoteRole == "Receiver" {
		ordered := true
		dc, err := pc.CreateDataChannel("gmmff-chat", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil { return nil, err }
		dc.OnOpen(func() { dcReady <- dc })
	} else {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) { dcReady <- dc })
	}

	trickleICE(sig, pc)
	go disp.pumpICE(ctx, pc)

	if remoteRole == "Receiver" {
		offer, _ := pc.CreateOffer(nil)
		_ = pc.SetLocalDescription(offer)
		sdpJSON, _ := json.Marshal(offer)
		offerMAC := session.SignOffer(sdpJSON)
		_ = sig.SendSignedSDP(protocol.MsgSDPOffer, sdpJSON, offerMAC)
		answerMsg, err := disp.waitFor(ctx, disp.answer)
		if err != nil { return nil, err }
		answerJSON, answerMAC, err := signaling.DecodeSignedSDP(answerMsg)
		if err != nil { return nil, err }
		if err := session.VerifyAnswer(answerJSON, answerMAC); err != nil { return nil, err }
		var answer webrtc.SessionDescription
		_ = json.Unmarshal(answerJSON, &answer)
		_ = pc.SetRemoteDescription(answer)
	} else {
		offerMsg, err := disp.waitFor(ctx, disp.offer)
		if err != nil { return nil, err }
		offerJSON, offerMAC, err := signaling.DecodeSignedSDP(offerMsg)
		if err != nil { return nil, err }
		if err := session.VerifyOffer(offerJSON, offerMAC); err != nil { return nil, err }
		var offer webrtc.SessionDescription
		_ = json.Unmarshal(offerJSON, &offer)
		_ = pc.SetRemoteDescription(offer)
		answer, _ := pc.CreateAnswer(nil)
		_ = pc.SetLocalDescription(answer)
		answerJSON, _ := json.Marshal(answer)
		answerMAC := session.SignAnswer(answerJSON)
		_ = sig.SendSignedSDP(protocol.MsgSDPAnswer, answerJSON, answerMAC)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case dc := <-dcReady:
		// Wire incoming messages to JS
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if len(m.Data) == 0 { return }
			switch m.Data[0] {
			case transfer.TagMessage:
				text := transfer.ParseMessageFrame(m.Data)
				js.Global().Call("uiChatMessage", remoteRole, text)
			case transfer.TagChatClose, transfer.TagCancelled:
				js.Global().Call("uiChatClosed", remoteRole+" ended the session.")
				activeChatDC = nil
			}
		})
		js.Global().Call("uiChatOpen", remoteRole)
		return dc, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Browser download
// ─────────────────────────────────────────────────────────────────────────────

// browserDownload creates a temporary Blob URL and programmatically clicks
// a hidden anchor to trigger a browser Save dialog.
func browserDownload(filename string, data []byte) {
	uint8arr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(uint8arr, data)

	jsArray := js.Global().Get("Array").New(1)
	jsArray.SetIndex(0, uint8arr)

	blob := js.Global().Get("Blob").New(jsArray)
	url  := js.Global().Get("URL").Call("createObjectURL", blob)

	doc    := js.Global().Get("document")
	anchor := doc.Call("createElement", "a")
	anchor.Set("href", url.String())
	anchor.Set("download", filename)
	doc.Get("body").Call("appendChild", anchor)
	anchor.Call("click")
	doc.Get("body").Call("removeChild", anchor)
	js.Global().Get("URL").Call("revokeObjectURL", url)
}

// ─────────────────────────────────────────────────────────────────────────────
// Progress
// ─────────────────────────────────────────────────────────────────────────────

// makeProgressFn returns a transfer.ProgressFunc that calls window.uiSendProgress
// or window.uiReceiveProgress with percentage, bytes, total, speed, and ETA.
// totalBytes may be -1 if unknown (receive path before FileHeader arrives).
func makeProgressFn(panel string, totalBytes int64) func(done, total int64) {
	var (
		startTime  = time.Now()
		lastCall   time.Time
		jsFn       = "uiSendProgress"
	)
	if panel == "receive" {
		jsFn = "uiReceiveProgress"
	}
	return func(done, total int64) {
		// Use the live total if our initial estimate was unknown.
		if totalBytes < 0 && total > 0 {
			totalBytes = total
		}
		if totalBytes <= 0 {
			return
		}
		// Throttle UI updates to ~10 per second to avoid saturating the JS event loop.
		now := time.Now()
		if !lastCall.IsZero() && now.Sub(lastCall) < 100*time.Millisecond {
			return
		}
		lastCall = now

		// Clamp done to total so the bar never exceeds 100%.
		if done > totalBytes {
			done = totalBytes
		}
		elapsed := now.Sub(startTime).Seconds()
		var speed, eta float64
		if elapsed > 0 {
			speed = float64(done) / elapsed           // bytes/sec
			remaining := float64(totalBytes - done)
			if speed > 0 {
				eta = remaining / speed // seconds
			}
		}
		pct := int(float64(done) / float64(totalBytes) * 100)
		js.Global().Call(jsFn, pct, done, totalBytes, speed, eta)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UI bridge helpers
// ─────────────────────────────────────────────────────────────────────────────

func uiStatusKey(key, panel string) {
	js.Global().Call("uiStatus", key, js.Global().Get("Object").New(), panel)
}

func uiError(message, panel string) {
	js.Global().Call("uiError", message, panel)
}
