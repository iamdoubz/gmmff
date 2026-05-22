# WASM webclient

WASM is WebAssembly. It let's you run high-preformance code directly in the web browser at near-native speeds. It is included in `gmmff` to enable a simple, yet elegant way to start/join sessions without the need to compile anything (you are welcome non-techys).

## Create wasm file

This file is not in the repo. You will have to generate it manually. There are two methods:

```
make wasm
```
...OR...
```
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm
# GO < 1.24
cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/static/wasm_exec.js
# GO >= 1.24
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/static/wasm_exec.js
```

## Host static files

Once you have created `gmmff.wasm` and copied `wasm_exec.js` into the `./web/static` directory, you can choose to have these files read by a webserver `sudo cp -r ./web/static/. /var/www/html` or pass in the `--web` argument when you start the signalling server.

## How to use the webclient

### Files tab

Open the **Files** tab, click **Start session** to get a code, or click
**Join with a code** to enter one. Once connected:

- Set your **name** (optional) — shown to other participants as your message label
- Set **Max participants** (2–10) before starting — 2 is bidirectional, 3–10 makes the initiator the broadcaster
- Drag and drop files anywhere on the page, or use **Choose files** / **Choose folder**
- Click **Send** to transfer — the other side auto-downloads once verified
- Type in the message box to send a text message
- **End session** leaves quietly; typing `\q` ends for everyone (initiator) or leaves quietly (responder)

A progress bar appears per transfer. Queued transfers each get their own bar.

### Chat tab

Open the **Chat** tab, click **Start session** to get a code, or click
**Join with a code** to enter one. Type `\q` in the message box to end the
session (initiator) or leave quietly (responder). The **End session** button
always leaves quietly.

### Schedule tab

The **Schedule** tab is hidden by default and must be enabled by the server
operator (`GMMFF_SHOW_SCHEDULE=true`). It allows asynchronous encrypted file
delivery — no simultaneous connection between sender and recipient required.

**Uploading (Create)**

1. Click **Schedule** → **Create**
2. If the server requires an upload password, enter it when prompted
3. Choose a file or folder — multiple files are zipped automatically in the browser before encryption
4. Select how long the link should remain valid from the **Valid for** dropdown
5. Optionally set a **Max downloads** limit (default: 1, set by server; 0 = unlimited)
6. Click **Upload** — the file is encrypted with AES-256-GCM in your browser before a single byte is sent to the server
7. Once complete you receive:
   - **Share URL** — includes the decryption key in the `#key=` fragment; share this with the recipient
   - **QR code** — scannable version of the share URL
   - **Delete link** — only you have this; use it to remove the file before it expires
   - **Expiry date** — when the file will be automatically deleted

The server never sees plaintext. The decryption key lives only in the URL
fragment which browsers never transmit to the server.

**Downloading (Join)**

1. Click **Schedule** → **Join**
2. Paste the full share URL (including the `#key=` part)
3. Click **Download & Decrypt** — the file is fetched and decrypted entirely in the browser
4. The original file downloads automatically once decryption is complete

If the share URL contains `&dl=1`, the download starts automatically when the
page loads — useful for links shared via email or messaging apps.

**Deleting an upload**

Open the delete URL shown on the success screen. The file is removed immediately.
You can also use the CLI: `gmmff schedule delete "<delete-url>"`

## Screenshots

<p align="center">
  <img src="../imgs/wasm.png" alt="Wasm web interface with black and gray default theme">
</p>

## Next steps

- Read how to start `gmmff` signalling server at boot using [systemd](SYSTEMD.md)
- Read how to setup a [reverse proxy](NGINX.md) for your signalling server