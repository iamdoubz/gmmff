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

	"github.com/iamdoubz/gmmff/internal/peer"
	"github.com/iamdoubz/gmmff/internal/signaling"
	"github.com/iamdoubz/gmmff/internal/transfer"
	"github.com/iamdoubz/gmmff/pkg/protocol"
)

func main() {
	js.Global().Set("gmmffSend", js.FuncOf(jsSend))
	js.Global().Set("gmmffReceive", js.FuncOf(jsReceive))

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
	jsFile    := args[0]
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

		// Read the JS File into memory — no filesystem access needed.
		fileName, fileData, err := jsFileToBytes(jsFile)
		if err != nil {
			uiError(err.Error(), "send")
			return
		}

		cfg := peer.Config{
			WindowSize: transfer.DefaultWindowSize,
			ChunkSize:  transfer.DefaultChunkSize,
		}

		if err := peer.SendBytes(ctx, sig, created.Code, fileName, fileData, cfg); err != nil {
			uiError(err.Error(), "send")
			return
		}
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
		fileName, fileData, err := peer.ReceiveToBytes(ctx, sig, code, cfg)
		if err != nil {
			uiError(err.Error(), "receive")
			return
		}
		if len(fileData) > 0 {
			browserDownload(fileName, fileData)
		}
	}()

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// JS File → Wasm in-memory temp file
// ─────────────────────────────────────────────────────────────────────────────

// jsFileToBytes reads a JS File object entirely into memory.
// Returns the file name and raw bytes — no filesystem access required.
func jsFileToBytes(jsFile js.Value) (name string, data []byte, err error) {
	name = jsFile.Get("name").String()
	size := jsFile.Get("size").Int()

	done := make(chan error, 1)
	buf  := make([]byte, size)

	reader := js.Global().Get("FileReader").New()
	onLoad := js.FuncOf(func(_ js.Value, args []js.Value) any {
		result := args[0].Get("target").Get("result")
		arr    := js.Global().Get("Uint8Array").New(result)
		js.CopyBytesToGo(buf, arr)
		done <- nil
		return nil
	})
	onErr := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		done <- fmt.Errorf("FileReader failed")
		return nil
	})
	defer onLoad.Release()
	defer onErr.Release()

	reader.Set("onload", onLoad)
	reader.Set("onerror", onErr)
	reader.Call("readAsArrayBuffer", jsFile)

	if err := <-done; err != nil {
		return "", nil, err
	}
	return name, buf, nil
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
// UI bridge helpers
// ─────────────────────────────────────────────────────────────────────────────

func uiStatusKey(key, panel string) {
	js.Global().Call("uiStatus", key, js.Global().Get("Object").New(), panel)
}

func uiError(message, panel string) {
	js.Global().Call("uiError", message, panel)
}
