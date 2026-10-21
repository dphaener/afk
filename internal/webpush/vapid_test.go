package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// newTestKey makes a key the way the daemon will, in a temp folder.
func newTestKey(t *testing.T) Key {
	t.Helper()
	key, err := LoadOrCreateKey(t.TempDir(), rand.Reader, keyTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// splitAuthorization takes "vapid t=<jwt>, k=<key>" apart.
func splitAuthorization(t *testing.T, header string) (jwt, k string) {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		t.Fatalf("header %q does not start with \"vapid t=\"", header)
	}
	jwt, k, ok = strings.Cut(rest, ", k=")
	if !ok {
		t.Fatalf("header %q has no \", k=\"", header)
	}
	return jwt, k
}

// TestVAPIDAuthorization checks the token a push service will verify. The
// load-bearing rows are the aud rows: the push service compares aud with its
// own origin, so a path or query left in (the subscription's capability) or
// a dropped port gets every push refused. Every row also verifies the
// signature with ecdsa.Verify against the public key taken from the header's
// own k parameter, parsed independently of Key, and checks the raw 64-byte
// r || s layout, because a DER signature is the classic ES256 mistake and
// looks fine until a push service rejects it.
func TestVAPIDAuthorization(t *testing.T) {
	key := newTestKey(t)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		endpoint string
		subject  string
		wantAud  string
	}{
		{"aud drops path and query", "https://fcm.googleapis.com/fcm/send/abc123?x=1#frag", "mailto:owner@example.com", "https://fcm.googleapis.com"},
		{"aud keeps the port", "http://127.0.0.1:41234/push/device-1", "https://github.com/dphaener/afk", "http://127.0.0.1:41234"},
		{"aud for a bare origin", "https://web.push.apple.com", "https://example.com/afk", "https://web.push.apple.com"},
		{"aud for an IPv6 host", "https://[::1]:8443/p", "mailto:a@b.c", "https://[::1]:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header, err := vapidAuthorization(key, tt.endpoint, tt.subject, now)
			if err != nil {
				t.Fatalf("vapidAuthorization: %v", err)
			}
			jwt, k := splitAuthorization(t, header)
			if k != key.PublicKey() {
				t.Errorf("k = %q, want PublicKey() %q", k, key.PublicKey())
			}

			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Fatalf("JWT has %d parts, want 3", len(parts))
			}
			enc := base64.RawURLEncoding
			head, err := enc.DecodeString(parts[0])
			if err != nil {
				t.Fatalf("JWT header is not unpadded base64url: %v", err)
			}
			if string(head) != `{"typ":"JWT","alg":"ES256"}` {
				t.Errorf("JWT header = %s", head)
			}
			body, err := enc.DecodeString(parts[1])
			if err != nil {
				t.Fatalf("JWT claims are not unpadded base64url: %v", err)
			}
			var claims map[string]any
			if err := json.Unmarshal(body, &claims); err != nil {
				t.Fatalf("JWT claims are not JSON: %v", err)
			}
			if claims["aud"] != tt.wantAud {
				t.Errorf("aud = %v, want %q", claims["aud"], tt.wantAud)
			}
			if exp, ok := claims["exp"].(float64); !ok || int64(exp) != now.Add(12*time.Hour).Unix() {
				t.Errorf("exp = %v, want %d (12h after the injected time)", claims["exp"], now.Add(12*time.Hour).Unix())
			}
			if claims["sub"] != tt.subject {
				t.Errorf("sub = %v, want %q passed through", claims["sub"], tt.subject)
			}
			if len(claims) != 3 {
				t.Errorf("claims = %v, want only aud, exp, sub", claims)
			}

			sig, err := enc.DecodeString(parts[2])
			if err != nil {
				t.Fatalf("JWT signature is not unpadded base64url: %v", err)
			}
			if len(sig) != 64 {
				t.Fatalf("signature is %d bytes, want the raw 64-byte r||s", len(sig))
			}
			rawPub, err := enc.DecodeString(k)
			if err != nil {
				t.Fatalf("k is not unpadded base64url: %v", err)
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), rawPub)
			if err != nil {
				t.Fatalf("k is not a P-256 point: %v", err)
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			r := new(big.Int).SetBytes(sig[:32])
			s := new(big.Int).SetBytes(sig[32:])
			if !ecdsa.Verify(pub, digest[:], r, s) {
				t.Error("JWT signature does not verify with the public key")
			}
			if strings.Contains(jwt, "=") {
				t.Errorf("JWT %q carries base64 padding", jwt)
			}
		})
	}
}

// TestVAPIDRefusals covers what vapidAuthorization will not sign. The
// endpoint rows are load-bearing because aud is derived from the endpoint:
// anything without a scheme and host has no origin to put there. Every row
// also checks the endpoint is not quoted back, since it is the device's
// capability URL.
func TestVAPIDRefusals(t *testing.T) {
	key := newTestKey(t)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		key      Key
		endpoint string
		subject  string
	}{
		{"bad endpoint: empty", key, "", "mailto:a@b.c"},
		{"bad endpoint: no scheme", key, "fcm.googleapis.com/fcm/send/SECRET", "mailto:a@b.c"},
		{"bad endpoint: path only", key, "/push/SECRET", "mailto:a@b.c"},
		{"bad endpoint: other scheme", key, "ftp://push.example.com/SECRET", "mailto:a@b.c"},
		{"bad endpoint: opaque", key, "https:push.example.com/SECRET", "mailto:a@b.c"},
		{"bad endpoint: mailto", key, "mailto:SECRET@example.com", "mailto:a@b.c"},
		{"bad endpoint: user info", key, "https://user:SECRET@push.example.com/p", "mailto:a@b.c"},
		{"bad endpoint: unparseable", key, "https://push.example.com/%zzSECRET", "mailto:a@b.c"},
		{"empty subject", key, "https://push.example.com/p", ""},
		{"zero key", Key{}, "https://push.example.com/p", "mailto:a@b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header, err := vapidAuthorization(tt.key, tt.endpoint, tt.subject, now)
			if err == nil {
				t.Fatalf("signed %q, want a refusal", header)
			}
			if header != "" {
				t.Errorf("header = %q alongside an error, want empty", header)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error %q quotes the endpoint", err)
			}
		})
	}
}

// TestVAPIDShortSignatureHalves pins the zero padding of r and s. About one
// signature in 256 has an r below 2^248 (and as many an s), whose big-endian
// bytes are shorter than 32; written without padding, that token alone would
// fail at the push service, which no single-signature row catches. A short r
// written unpadded cannot be spotted in the output (it just looks like a
// different r), so this verifies every one of enough signatures that missing
// a short r and a short s is about one run in 10^5.
func TestVAPIDShortSignatureHalves(t *testing.T) {
	key := newTestKey(t)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), key.pub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3000; i++ {
		header, err := vapidAuthorization(key, "https://push.example.com/p", "mailto:a@b.c", now)
		if err != nil {
			t.Fatal(err)
		}
		jwt, _ := splitAuthorization(t, header)
		parts := strings.Split(jwt, ".")
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(sig) != 64 {
			t.Fatalf("signature is not 64 bytes of base64url (err %v, len %d)", err, len(sig))
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			t.Fatalf("signature %d does not verify", i)
		}
	}
}
