# Security

## CPace PAKE
Both peers authenticate using CPace over the ristretto255 group
(`filippo.io/cpace`).  The signaling server forwards PAKE messages opaquely
and never learns the shared secret.

### Split codes (v2.4.0+)
The code users share is `<nameplate>-<secret>` (e.g.
`bear-cozy-cone-maple-river`). The server generates the 3-word nameplate and
uses it only to find the slot; the initiator's client appends a 2-word secret
that never reaches the server. CPace runs on the full code, so the server, or
anyone who can read Redis or proxy logs, cannot complete the PAKE and MITM.
Share links carry the code in the URL fragment (`#code=`), which browsers do
not send to the server.

> **Remaining limitation — browser clients.** The browser loads its JS/Wasm
> from the signaling server, so an actively malicious server could serve a
> client that leaks the secret. The CLI is not affected. See ADR-014.

## SDP MAC binding (zero-trust signaling)
After the PAKE handshake, two subkeys are derived from the shared secret using
HKDF-SHA256:

```
offerKey  = HKDF(sharedKey, salt="gmmff-v1", info="sdp-offer-mac")
answerKey = HKDF(sharedKey, salt="gmmff-v1", info="sdp-answer-mac")
```

The initiator HMAC-signs the SDP offer with `offerKey` before sending it to
the relay.  The responder verifies the MAC before calling `SetRemoteDescription`
— and vice versa for the answer.  A compromised signaling server cannot
substitute its own SDP fingerprints because it does not know the shared key.

## DTLS 1.3
All data channel traffic is encrypted end-to-end by Pion's DTLS 1.3
implementation.  The signaling server is out of the loop once ICE completes.

## Resumable transfers
Partial files are written as `<name>.gmmff_partial` with a `<name>.gmmff_meta`
sidecar (SHA256 + chunk size + bytes written).  On resume, the receiver
replays the partial file through SHA-256 to reconstruct the running hash and
sends a `ResumeFrom` frame to the sender.  Both progress bars advance to the
correct offset before transfer continues.  On completion, both temp files are
deleted and the final file is renamed into place.