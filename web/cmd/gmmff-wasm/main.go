//go:build js && wasm

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
	"github.com/iamdoubz/gmmff/internal/session"
	"github.com/iamdoubz/gmmff/internal/turn"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
)

func main() {
	js.Global().Set("gmmffSend", js.FuncOf(jsSend))
	js.Global().Set("gmmffReceive", js.FuncOf(jsReceive))
	js.Global().Set("gmmffGetDefaultICE", js.FuncOf(jsGetDefaultICE))
	js.Global().Set("gmmffCreateSession", js.FuncOf(jsCreateSession))
	js.Global().Set("gmmffJoinSession", js.FuncOf(jsJoinSession))
	js.Global().Set("gmmffSessionSendFiles", js.FuncOf(jsSessionSendFiles))
	js.Global().Set("gmmffSessionSendMessage", js.FuncOf(jsSessionSendMessage))
	js.Global().Set("gmmffSessionClose", js.FuncOf(jsSessionClose))
	js.Global().Set("gmmffSessionLeave", js.FuncOf(jsSessionLeave))
	js.Global().Set("gmmffChat", js.FuncOf(jsChat))
	js.Global().Set("gmmffChatSend", js.FuncOf(jsChatSend))
	js.Global().Set("gmmffChatJoin", js.FuncOf(jsChatJoin))
	js.Global().Set("gmmffChatQuit", js.FuncOf(jsChatQuit))
	js.Global().Set("gmmffChatLeave", js.FuncOf(jsChatLeave))

	// Block forever — Go Wasm must not exit or the runtime shuts down.
	select {}
}

// ─────────────────────────────────────────────────────────────────────────────
// Send
// ─────────────────────────────────────────────────────────────────────────────

// jsSend is called from JS as: window.gmmffSend(files, serverURL, iceConfig)
func jsSend(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	jsFiles   := args[0]
	serverURL := args[1].String()
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }

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

		if err := sig.CreateSlot("files", 2); err != nil {
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

		cfg := configFromJS(iceCfg)

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

// jsReceive is called from JS as: window.gmmffReceive(code, serverURL, iceConfig)
func jsReceive(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	code      := args[0].String()
	serverURL := args[1].String()
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }

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

		cfg := configFromJS(iceCfg)

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

// activeChatSession holds the live chat session so jsChatSend can reach it.
var activeChatSession *peer.ChatSession

// jsChat — initiator: creates slot, shows code, opens chat session.
func jsChat(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	serverURL := args[0].String()
	maxPeers  := 2
	if len(args) > 1 && !args[1].IsUndefined() && !args[1].IsNull() {
		if n := args[1].Int(); n >= 2 {
			maxPeers = n
		}
	}
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any { cancelCtx(); return nil })
	js.Global().Call("uiRegisterCancel", cancelFn)
	go func() {
		defer cancelCtx()
		defer cancelFn.Release()
		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		if err := sig.CreateSlot("chat", maxPeers); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		var created protocol.SlotCreatedPayload
		if err := json.Unmarshal(createdMsg.Payload, &created); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		js.Global().Call("uiChatShowCode", created.Code)
		if _, err = sig.WaitFor(ctx, protocol.MsgSlotReady); err != nil { js.Global().Call("uiChatError", err.Error()); return }
		session, err := peer.ChatWithCallback(ctx, sig, created.Code, "Sender",
			configFromJS(iceCfg),
			func(from, text string) { js.Global().Call("uiChatMessage", from, text) },
			func(reason string)     { js.Global().Call("uiChatClosed", reason) },
			func(who string)        { js.Global().Call("uiChatParticipantLeft", who) },
		)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		activeChatSession = session
		js.Global().Call("uiChatOpen", "Receiver")
	}()
	return nil
}

// jsChatJoin — responder: joins an existing slot by code.
func jsChatJoin(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	code, serverURL := args[0].String(), args[1].String()
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }
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
		session, err := peer.ChatWithCallback(ctx, sig, code, "Receiver",
			configFromJS(iceCfg),
			func(from, text string) { js.Global().Call("uiChatMessage", from, text) },
			func(reason string)     { js.Global().Call("uiChatClosed", reason) },
			func(who string)        { js.Global().Call("uiChatParticipantLeft", who) },
		)
		if err != nil { js.Global().Call("uiChatError", err.Error()); return }
		activeChatSession = session
		js.Global().Call("uiChatOpen", "Sender")
	}()
	return nil
}

// jsChatQuit — initiator ends session for everyone; responder leaves quietly.
func jsChatQuit(_ js.Value, _ []js.Value) any {
	if activeChatSession == nil {
		return nil
	}
	if activeChatSession.IsInitiator {
		activeChatSession.Close() // TagChatClose — ends for everyone
		js.Global().Call("uiChatClosed", "You ended the session.")
	} else {
		activeChatSession.Leave() // TagParticipantLeave — quiet exit
		js.Global().Call("uiChatClosed", "You left the session.")
	}
	activeChatSession = nil
	return nil
}

// jsChatLeave — any participant leaves quietly ("End session" button).
func jsChatLeave(_ js.Value, _ []js.Value) any {
	if activeChatSession == nil {
		return nil
	}
	activeChatSession.Leave()
	activeChatSession = nil
	return nil
}

// jsChatSend sends a message on the active chat session.
func jsChatSend(_ js.Value, args []js.Value) any {
	if len(args) < 1 || activeChatSession == nil {
		return nil
	}
	_ = activeChatSession.Send(args[0].String())
	return nil
}


// ─────────────────────────────────────────────────────────────────────────────
// Files session (bidirectional)
// ─────────────────────────────────────────────────────────────────────────────

var activeSession *session.Session

// jsCreateSession — initiator: creates a files session slot.
func jsCreateSession(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	serverURL := args[0].String()
	maxPeers  := getJSInt(args, 1, 2)
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any { cancelCtx(); return nil })
	js.Global().Call("uiRegisterCancel", cancelFn)
	go func() {
		defer cancelCtx()
		defer cancelFn.Release()
		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		if err := sig.CreateSlot("files", maxPeers); err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		createdMsg, err := sig.WaitFor(ctx, protocol.MsgSlotCreated)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		var created protocol.SlotCreatedPayload
		if err := json.Unmarshal(createdMsg.Payload, &created); err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		js.Global().Call("uiFilesShowCode", created.Code)
		// StartSession waits for slot.ready internally.
		sessCtx := context.Background()
		sess, err := peer.StartSession(sessCtx, sig, created.Code, configFromJS(iceCfg), maxPeers)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		activeSession = sess
		go runWasmSession(sessCtx, sess)
		js.Global().Call("uiFilesSessionReady", true, sess.PeerCount(), sess.MaxPeers)
	}()
	return nil
}

// jsJoinSession — responder: joins a files session.
func jsJoinSession(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	code, serverURL := args[0].String(), args[1].String()
	var iceCfg js.Value
	if len(args) > 2 { iceCfg = args[2] }
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelFn := js.FuncOf(func(_ js.Value, _ []js.Value) any { cancelCtx(); return nil })
	js.Global().Call("uiRegisterCancel", cancelFn)
	go func() {
		defer cancelCtx()
		defer cancelFn.Release()
		sig, err := signaling.Connect(ctx, serverURL)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		if err := sig.JoinSlot(code); err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		// JoinSession waits for slot.ready internally.
		sessCtx := context.Background()
		sess, err := peer.JoinSession(sessCtx, sig, code, configFromJS(iceCfg), nil)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		activeSession = sess
		go runWasmSession(sessCtx, sess)
		js.Global().Call("uiFilesSessionReady", false, sess.PeerCount(), sess.MaxPeers)
	}()
	return nil
}

// jsSessionSendFiles sends files over the active session.
func jsSessionSendFiles(_ js.Value, args []js.Value) any {
	if len(args) < 1 || activeSession == nil {
		return nil
	}
	jsFiles := args[0]
	go func() {
		namedFiles, err := jsFilesToNamedFiles(jsFiles)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }
		fileData, fileName, err := archive.ZipFilesFromMemory(namedFiles)
		if err != nil { js.Global().Call("uiFilesError", err.Error()); return }

		label := fmt.Sprintf("tx-%d", time.Now().UnixNano())
		total := int64(len(fileData))
		progress := makeSessionProgressFn(label, total)
		done := activeSession.SendBytes(fileName, fileData, "", progress)
		if err := <-done; err != nil {
			js.Global().Call("uiFilesTransferError", label, err.Error())
		} else {
			js.Global().Call("uiFilesTransferDone", label, fileName)
		}
	}()
	return nil
}

func makeSessionProgressFn(label string, total int64) func(done, total int64) {
	return func(done, tot int64) {
		if tot <= 0 { return }
		pct := int(float64(done) / float64(tot) * 100)
		js.Global().Call("uiFilesProgress", label, pct, done, tot)
	}
}

// jsSessionSendMessage sends a text message over the active session.
func jsSessionSendMessage(_ js.Value, args []js.Value) any {
	if len(args) < 1 || activeSession == nil {
		return nil
	}
	_ = activeSession.SendMessage(args[0].String())
	return nil
}

// jsSessionClose ends the session for everyone (initiator only).
func jsSessionClose(_ js.Value, _ []js.Value) any {
	if activeSession != nil {
		activeSession.Close()
		activeSession = nil
	}
	return nil
}

// jsSessionLeave leaves the session quietly.
func jsSessionLeave(_ js.Value, _ []js.Value) any {
	if activeSession != nil {
		activeSession.Leave()
		activeSession = nil
	}
	return nil
}

// runWasmSession drains the session event channel concurrently with Run().
// Events must be dispatched to JS as they arrive — not after Run() returns —
// otherwise progress bars, downloads, and messages are all delayed until the
// session ends (because Run() blocks until the session is over).
func runWasmSession(_ context.Context, sess *session.Session) {
	// Drain Events in a separate goroutine so it runs alongside Run().
	go func() {
		for ev := range sess.Events {
			dispatchSessionEvent(ev)
		}
	}()
	// Run blocks until the session ends, then closes Events,
	// which causes the range above to exit.
	sess.Run()
}

func dispatchSessionEvent(ev session.Event) {
	switch ev.Type {
	case session.EventMessage:
		js.Global().Call("uiFilesMessage", ev.From, ev.Message)
	case session.EventTransferStarted:
		js.Global().Call("uiFilesInboundStarted", ev.Label, ev.Total)
	case session.EventTransferProgress:
		if ev.Total > 0 {
			pct := int(float64(ev.Done) / float64(ev.Total) * 100)
			js.Global().Call("uiFilesProgress", ev.Label, pct, ev.Done, ev.Total)
		}
	case session.EventTransferDone:
		js.Global().Call("uiFilesTransferDone", ev.Label, ev.Path)
		if ev.Message != "" {
			js.Global().Call("uiFilesMessage", "Participant", ev.Message)
		}
		if len(ev.Data) > 0 {
			browserDownload(ev.Path, ev.Data)
		}
	case session.EventPeerJoined:
		js.Global().Call("uiFilesPeerCount", ev.PeerCount, ev.MaxPeers)
	case session.EventPeerLeft:
		js.Global().Call("uiFilesParticipantLeft", ev.Message, ev.From)
		js.Global().Call("uiFilesPeerCount", ev.PeerCount, ev.MaxPeers)
	case session.EventSessionClosed:
		js.Global().Call("uiFilesSessionClosed", ev.Message)
		activeSession = nil
	case session.EventError:
		js.Global().Call("uiFilesError", ev.Message)
	}
}

// getJSInt safely reads an integer from a JS args slice.
func getJSInt(args []js.Value, idx, defaultVal int) int {
	if idx >= len(args) || args[idx].IsUndefined() || args[idx].IsNull() {
		return defaultVal
	}
	return args[idx].Int()
}

// ─────────────────────────────────────────────────────────────────────────────
// ICE configuration helpers
// ─────────────────────────────────────────────────────────────────────────────

// configFromJS builds a peer.Config from a JS object {stun: [...], turn: [...]}.
// If iceCfg is zero/undefined the default config is returned.
func configFromJS(iceCfg js.Value) peer.Config {
	cfg := peer.Config{
		WindowSize: transfer.DefaultWindowSize,
		ChunkSize:  transfer.DefaultChunkSize,
	}
	if iceCfg.IsUndefined() || iceCfg.IsNull() {
		return cfg
	}
	// STUN: use pushed list or append user list to defaults.
	stunArr := iceCfg.Get("stun")
	if !stunArr.IsUndefined() && !stunArr.IsNull() {
		stuns := peer.DefaultSTUNServers
		for i := 0; i < stunArr.Length(); i++ {
			v := stunArr.Index(i).String()
			if v != "" {
				stuns = append(stuns, v)
			}
		}
		cfg.STUNServers = stuns
	}
	// pushed_turn: pre-resolved {url, username, password} objects from /api/ice.
	// These bypass turn.ParseOne since credentials are already resolved server-side.
	pushedTurn := iceCfg.Get("pushed_turn")
	if !pushedTurn.IsUndefined() && !pushedTurn.IsNull() && pushedTurn.Length() > 0 {
		for i := 0; i < pushedTurn.Length(); i++ {
			entry := pushedTurn.Index(i)
			url  := entry.Get("url").String()
			user := entry.Get("username").String()
			pass := entry.Get("password").String()
			if url != "" {
				cfg.TURNServers = append(cfg.TURNServers, turn.Server{
					URL:      url,
					Username: user,
					Password: pass,
				})
			}
		}
		return cfg // pushed TURN replaces user-defined TURN
	}
	// TURN: parse user-defined entries via turn.ParseOne (includes ephemeral cred derivation).
	turnArr := iceCfg.Get("turn")
	if !turnArr.IsUndefined() && !turnArr.IsNull() {
		for i := 0; i < turnArr.Length(); i++ {
			raw := turnArr.Index(i).String()
			if raw == "" {
				continue
			}
			srv, err := turn.ParseOne(raw)
			if err == nil {
				cfg.TURNServers = append(cfg.TURNServers, srv)
			}
		}
	}
	return cfg
}

// jsGetDefaultICE returns the default ICE config as a JS object.
// Called by the UI to pre-populate the ICE settings panel.
func jsGetDefaultICE(_ js.Value, _ []js.Value) any {
	obj := js.Global().Get("Object").New()
	stunArr := js.Global().Get("Array").New()
	for i, s := range peer.DefaultSTUNServers {
		stunArr.SetIndex(i, s)
	}
	obj.Set("stun", stunArr)
	obj.Set("turn", js.Global().Get("Array").New())
	return obj
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
