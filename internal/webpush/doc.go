// Package webpush sends afk's notices to the browsers and phones signed up
// on the web page, through each one's own push service.
//
// Web push is three RFCs working together, and this package implements the
// sender's side of each:
//
//   - RFC 8030, the push protocol: a POST of the message to the endpoint a
//     browser handed out when it subscribed, with TTL and Urgency headers.
//   - RFC 8291 (with RFC 8188's aes128gcm framing), message encryption: the
//     push service only relays the bytes and must not be able to read them,
//     so each message is encrypted for that one browser's key.
//   - RFC 8292, VAPID: a short-lived ES256 token signed with afk's own key,
//     so the push service knows every message for a subscription comes from
//     the server the browser subscribed with, and nobody else can use it.
//
// Everything is built on Go's standard library (crypto/ecdh, crypto/ecdsa,
// crypto/hkdf, AES-GCM) with no new module: Go 1.25 has every primitive web
// push needs, and a library would bring its own JWT code for what is a few
// hundred lines we can test against the RFCs' own examples (docs/decisions.md
// T15.4, A1).
//
// The state lives in afk's per-machine state folder, like the web page's
// password in internal/webpass: push-key.json holds afk's VAPID key and
// push-devices.json the list of signed-up devices. Both are machine-written
// secrets, so they are mode 0600, written through a same-directory temp file
// and rename, and never put into config.toml, which the owner edits and may
// copy between machines. Every function takes the folder as a plain
// directory, so tests run against t.TempDir() and never the real one.
package webpush
