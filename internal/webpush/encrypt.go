package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Subscription is what a browser hands out when it subscribes to push
// (the PushSubscription's endpoint and its two keys), as the page receives
// it in JSON.
//
// P256dh is the browser's P-256 public key, a 65-byte uncompressed point,
// and Auth its 16-byte authentication secret (RFC 8291 §2). Both are
// base64url as the browser gives them, but Encrypt also takes them padded
// and in standard base64: PushSubscription.toJSON() gives base64url without
// padding, while hand-copied values and some JSON round trips add padding
// or swap the alphabet, and a key refused for its spelling alone would be
// a subscription silently never delivered to.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Payload is the notice a device shows: the JSON the service worker reads
// from the push event. Its field names are the contract with the worker
// script, so they are fixed here as tags.
//
// URL is where tapping the notice opens, as a path on afk's own page
// ("/", "/p/<project>/plan"); see Validate for why nothing else is allowed.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Validate refuses a payload whose URL is not a path on the page.
//
// The service worker opens the URL with clients.openWindow against the
// page's own origin, so a path is all a notice ever needs, and anything
// that could leave the page is refused rather than resolved: a full
// address (a scheme or a host, which is what url.Parse reports for
// "https://…" and "mailto:…"), a scheme-relative "//host", and "/\host",
// which the WHATWG URL parser browsers use treats as "//host" for http(s)
// because it reads a backslash as a slash there. url.Parse keeps "/\host"
// as a plain path, so that case is checked by hand.
//
// An empty URL is refused too: a notice with nowhere to go is a bug in the
// sender, and the page root "/" is one character away.
func (p Payload) Validate() error {
	// url.Parse's error quotes the input, so it is dropped: the message
	// below says what a good link looks like, which is all a caller needs.
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil ||
		!strings.HasPrefix(p.URL, "/") || strings.HasPrefix(p.URL, "//") || strings.HasPrefix(p.URL, "/\\") {
		return errors.New("the notice link must be a path on the page, starting with / (not a full address or //host)")
	}
	return nil
}

// The fixed sizes of RFC 8291 §3 and RFC 8188 §2.
const (
	// saltLength is the salt in the aes128gcm header, 16 bytes (RFC 8188
	// §2.1), drawn fresh for every message.
	saltLength = 16
	// authSecretLength is the browser's authentication secret (RFC 8291
	// §2): 16 bytes, no more and no less.
	authSecretLength = 16
	// contentKeyLength and nonceLength are AES-128-GCM's key and nonce
	// (RFC 8188 §2.2, §2.3).
	contentKeyLength = 16
	nonceLength      = 12
	// ikmLength is the input keying material RFC 8291 §3.4 derives from
	// the ECDH secret before the content key.
	ikmLength = 32
	// recordSize is the rs field of the header. A push message is one
	// record, so rs only has to be at least the ciphertext; 4096 is what
	// RFC 8291 §4 requires of push services and what its example uses.
	recordSize = 4096
	// paddingDelimiter ends the last (and only) record (RFC 8188 §2): the
	// receiver strips it to find where the plaintext ends.
	paddingDelimiter = 0x02
	// headerLength is salt(16) || rs(4) || idlen(1) || as_public(65), the
	// 86-octet header of RFC 8291 §4.
	headerLength = saltLength + 4 + 1 + publicKeyLength
	// gcmTagLength is the authentication tag AES-GCM adds to each record.
	gcmTagLength = 16
	// MaxPlaintextLength is the most plaintext one push message can carry.
	// Push services cap the whole body at 4096 bytes (RFC 8030 §7.2 asks
	// for at least that, and the big ones allow exactly that), and the
	// body is the 86-byte header, the plaintext, the one-byte delimiter
	// and the 16-byte tag: 4096 - 86 - 1 - 16 = 3993.
	MaxPlaintextLength = recordSize - headerLength - 1 - gcmTagLength
)

// The HKDF info strings of RFC 8291 §3.3–3.4 and RFC 8188 §2.2–2.3, each
// ending in a zero byte that is part of the string.
const (
	keyInfoPrefix = "WebPush: info\x00"
	cekInfo       = "Content-Encoding: aes128gcm\x00"
	nonceInfo     = "Content-Encoding: nonce\x00"
)

// Encrypt encrypts plaintext for the one browser behind sub, as RFC 8291
// says: the result is the body of the push request, aes128gcm-encoded,
// which the push service relays without being able to read.
//
// rnd supplies the message's ephemeral P-256 key and its 16-byte salt
// (crypto/rand.Reader in real use). Both are fresh for every message, so
// the same notice sent twice gives two different bodies and the content
// key is never reused with the same nonce.
//
// It refuses, with a plain error and before reading rnd, a plaintext over
// MaxPlaintextLength, a p256dh that is not a P-256 public key, and an auth
// that is not 16 bytes.
func Encrypt(sub Subscription, plaintext []byte, rnd io.Reader) ([]byte, error) {
	uaPublic, err := decodeSubscriptionKey(sub.P256dh)
	if err != nil || len(uaPublic) != publicKeyLength {
		return nil, errors.New("the subscription's p256dh key is not a P-256 public key")
	}
	// Checked here as well as in encrypt, so a 65-byte value that is not
	// a point on the curve is refused before any randomness is drawn,
	// like every other refusal.
	if _, err := ecdh.P256().NewPublicKey(uaPublic); err != nil {
		return nil, errors.New("the subscription's p256dh key is not a P-256 public key")
	}
	authSecret, err := decodeSubscriptionKey(sub.Auth)
	if err != nil || len(authSecret) != authSecretLength {
		return nil, fmt.Errorf("the subscription's auth secret is not %d bytes", authSecretLength)
	}
	if len(plaintext) > MaxPlaintextLength {
		return nil, fmt.Errorf("the notice is too long to push: %d bytes, and a push message holds at most %d", len(plaintext), MaxPlaintextLength)
	}
	if rnd == nil {
		return nil, errors.New("no source of random bytes to encrypt the notice")
	}
	asPrivate, err := ecdh.P256().GenerateKey(rnd)
	if err != nil {
		return nil, fmt.Errorf("making the message key: %w", err)
	}
	salt := make([]byte, saltLength)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return nil, fmt.Errorf("making the message salt: %w", err)
	}
	return encrypt(plaintext, uaPublic, authSecret, asPrivate, salt)
}

// encrypt is RFC 8291 §3.3–3.4 with the inputs fixed, so a test can feed
// it the RFC's own Appendix A values and compare the output byte for byte.
// Every step is named as the RFC names it.
func encrypt(plaintext, uaPublic, authSecret []byte, asPrivate *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintextLength {
		return nil, fmt.Errorf("the notice is too long to push: %d bytes, and a push message holds at most %d", len(plaintext), MaxPlaintextLength)
	}
	// NewPublicKey decodes the uncompressed point and refuses a compressed
	// one, the point at infinity, and a point that is not on the curve, so
	// the ECDH below never runs against an invalid key.
	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, errors.New("the subscription's p256dh key is not a P-256 public key")
	}
	if len(authSecret) != authSecretLength {
		return nil, fmt.Errorf("the subscription's auth secret is not %d bytes", authSecretLength)
	}
	if len(salt) != saltLength {
		return nil, fmt.Errorf("the message salt is not %d bytes", saltLength)
	}
	if asPrivate == nil || asPrivate.Curve() != ecdh.P256() {
		return nil, errors.New("the message key is not a P-256 key")
	}
	asPublic := asPrivate.PublicKey().Bytes()

	// RFC 8291 §3.1: ecdh_secret = ECDH(as_private, ua_public), the
	// x-coordinate of the shared point.
	ecdhSecret, err := asPrivate.ECDH(uaKey)
	if err != nil {
		return nil, errors.New("the subscription's p256dh key is not a P-256 public key")
	}
	// RFC 8291 §3.3: the auth secret is the HKDF salt and the ECDH secret
	// the keying material (hkdf.Extract takes secret then salt), and the
	// info binds both public keys, receiver's first.
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, fmt.Errorf("deriving the message key: %w", err)
	}
	keyInfo := keyInfoPrefix + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, ikmLength)
	if err != nil {
		return nil, fmt.Errorf("deriving the message key: %w", err)
	}
	// RFC 8291 §3.4 / RFC 8188 §2.2–2.3: the content key and nonce come
	// from the salt and the IKM.
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("deriving the message key: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, cekInfo, contentKeyLength)
	if err != nil {
		return nil, fmt.Errorf("deriving the message key: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, nonceInfo, nonceLength)
	if err != nil {
		return nil, fmt.Errorf("deriving the message key: %w", err)
	}

	// RFC 8188 §2: one record of plaintext || delimiter, sealed with the
	// key and the nonce. The nonce of record 0 is NONCE XOR 0, so NONCE
	// itself; there is never a second record.
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("encrypting the notice: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypting the notice: %w", err)
	}
	record := make([]byte, 0, len(plaintext)+1)
	record = append(record, plaintext...)
	record = append(record, paddingDelimiter)

	// RFC 8188 §2.1 header: salt, rs big-endian, the key id's length and
	// the key id, which for web push is the sender's public key
	// (RFC 8291 §4). The ciphertext is sealed straight after it.
	out := make([]byte, 0, headerLength+len(record)+gcmTagLength)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	return gcm.Seal(out, nonce, record, nil), nil
}

// decodeSubscriptionKey decodes a subscription key the way a browser or a
// JSON round trip may have spelled it: base64url with or without padding,
// or standard base64 with or without padding. Padding is stripped first
// so one unpadded decoder serves each alphabet; a mix of the two alphabets
// in one value is still refused.
func decodeSubscriptionKey(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
