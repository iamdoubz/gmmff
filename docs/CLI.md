# CLI Documentation

## One-off file send

To send a file without entering an interactive session, use `gmmff send`:

```bash
gmmff send report.pdf --server wss://your-server/ws
```

```
  ╔══════════════════════════════════════╗
  ║  Share this code with the receiver   ║
  ║                                      ║
  ║    acid-lake-drum                    ║
  ║                                      ║
  ║  Expires in 10 minutes               ║
  ╚══════════════════════════════════════╝

  Queued: "report.pdf" (1.5 MB)
  Waiting for receiver to join...
```

Once the receiver joins (`gmmff join` or web UI), the file transfers automatically and the session closes when done. No REPL, no interaction needed.

Multiple files or directories are zipped on the fly:

```bash
gmmff send file1.txt file2.txt --message "both files"
gmmff send ./project-folder
```

## Creating a file session

Peer A creates the session and receives a code:

```bash
gmmff create --server wss://your-server/ws
```

```
     ╔══════════════════════════════════════╗
     ║  Share this code with the other side ║
     ║                                      ║
     ║    acid-lake-drum                    ║
     ║                                      ║
     ║  Expires in 10 minutes               ║
     ╚══════════════════════════════════════╝

     Run on the other machine:
       gmmff join acid-lake-drum
```

## Joining a session

Peer B joins the session:

```bash
gmmff join acid-lake-drum --server wss://your-server/ws
```

## In-session controls

Once connected, both sides drop into the session REPL:

```
Session ready. Commands:
     send <file|dir> [file|dir ...]   send file(s) to peer
     message <text>                   send a text message
     chat                             open interactive chat sub-session
     \q                               end session for everyone (initiator only)
```

### Sending file, files, and/or directory

A single file is sent as-is. Multiple files or a directory are zipped on the fly — the receiver gets one `.zip` archive.

```bash
> send report.pdf
> send notes.txt data.csv
> send ./project-folder
```

### Sending a message in a session

Messages appear instantly on the other terminal. Optionally, with a single

file transfer the message is printed before the file saves; with multiple

files it is injected as `message.txt` inside the zip.

```bash
> message "Here is the Q3 report, let me know if you have questions"
```

### Opening a chat sub-session

Type `\q` inside `chat` to return to the session REPL without ending the session.

```bash
> chat
chat> Hello! Ready to transfer?
chat> \q
Returning to session.
>
```

### Session control

| Who | Action | Effect |
|-----|--------|--------|
| Initiator | `\q` | Ends the session for everyone |
| Initiator | `Ctrl+C` | Leaves quietly; session stays open |
| Responder | `\q` or `Ctrl+C` | Leaves quietly; session stays open |

### Multi-peer sessions

By default, sessions allow 2 participants. Use `--max-peers` to allow up to 10:

```bash
# Allow up to 5 participants
gmmff create --max-peers 5 --server wss://your-server/ws
```

Share the same code with up to 4 other people — they all `gmmff join` the same code.

**Transfer rules by participant count:**

| Session size | File transfers | Chat messages |
|-------------|---------------|---------------|
| 2 peers | Either side can send (bidirectional) | Either side |
| 3–10 peers | Initiator only (broadcast to all) | Any participant |

The initiator is the hub — all file transfers flow through them. If a peer leaves mid-transfer, their transfer ends but all other peers continue receiving. A session slot never reopens once it has been fully filled.

The session closes automatically after 10 minutes of inactivity. Any file transfer or message resets the timer.

## Creating a chat session (no files)

For a text-only session without file transfer, use `gmmff chat`:

```bash
# Machine A
gmmff chat --server wss://your-server/ws

# Machine B — gmmff join detects the session type and routes to the chat REPL
gmmff join river-stone-fog --server wss://your-server/ws
```