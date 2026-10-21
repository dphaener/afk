package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// RFC 8291 Appendix A and Section 5, copied exactly (with the RFC's line
// wrapping removed). These fix every input of one encryption and its
// expected output, so the test below is a byte-for-byte check against the
// standard, not against this package's own idea of it.
const (
	rfcPlaintext  = "When I grow up, I want to be a watermelon"
	rfcASPrivate  = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic   = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate  = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt       = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuthSecret = "BTBZMqHH6r4Tts7J_aSIgg"
	// rfcMessage is the 145-byte body of the Section 5 example request:
	// the 86-byte header followed by the Appendix A ciphertext.
	rfcMessage = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

// b64 decodes an RFC value (base64url, no padding), failing the test on a
// typo in the constants above.
func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return b
}

// rfcUAKey is the Appendix A user agent (receiver) key pair.
func rfcUAKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// decryptForTest is the receiver's side of RFC 8291, written from the RFC
// text (§3 and §4, with RFC 8188 §2) rather than from encrypt.go, so the
// round-trip test is a check between two readings of the standard. It
// parses the aes128gcm header, runs the same ECDH and HKDF steps with the
// receiver's private key, opens the one record, and strips the padding
// delimiter. A message with a second record, zero padding after the
// delimiter, or the wrong last-record delimiter is refused, as a browser
// would refuse it.
func decryptForTest(message []byte, uaPrivate *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(message) < headerLength {
		return nil, errors.New("message shorter than the header")
	}
	salt := message[:saltLength]
	rs := binary.BigEndian.Uint32(message[saltLength : saltLength+4])
	idlen := int(message[saltLength+4])
	if idlen != publicKeyLength || len(message) < headerLength {
		return nil, errors.New("key id is not a P-256 public key")
	}
	asPublic := message[saltLength+5 : headerLength]
	ciphertext := message[headerLength:]
	if uint32(len(ciphertext)) > rs {
		return nil, errors.New("more than one record")
	}
	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	ecdhSecret, err := uaPrivate.ECDH(asKey)
	if err != nil {
		return nil, err
	}
	uaPublic := uaPrivate.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPublic)+string(asPublic), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	// RFC 8188 §2: strip trailing zeros, then the delimiter, which must
	// be 0x02 on the last record.
	end := len(record)
	for end > 0 && record[end-1] == 0 {
		end--
	}
	if end == 0 || record[end-1] != 0x02 {
		return nil, errors.New("record does not end in the last-record delimiter")
	}
	return record[:end-1], nil
}

// TestEncryptRFC8291AppendixA is the load-bearing test of encrypt: with the
// RFC's fixed sender key, receiver key, auth secret and salt, the output
// must equal the RFC's Section 5 message byte for byte. Any slip in the
// key derivation (the order of secret and salt in HKDF-Extract, the
// contents or order of key_info, a missing zero byte in an info string),
// in the header layout, or in the padding delimiter changes the bytes and
// turns this red. It also runs the test receiver on the RFC's own message
// first, so the round-trip test below rests on a receiver the RFC vouches
// for, not on one that merely mirrors encrypt.
func TestEncryptRFC8291AppendixA(t *testing.T) {
	asPrivate, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := asPrivate.PublicKey().Bytes(); !bytes.Equal(got, b64(t, rfcASPublic)) {
		t.Fatalf("as_private does not give the RFC's as_public: %s", base64.RawURLEncoding.EncodeToString(got))
	}
	want := b64(t, rfcMessage)

	t.Run("receiver opens the RFC message", func(t *testing.T) {
		got, err := decryptForTest(want, rfcUAKey(t), b64(t, rfcAuthSecret))
		if err != nil {
			t.Fatalf("decryptForTest(RFC message) error: %v", err)
		}
		if string(got) != rfcPlaintext {
			t.Fatalf("decryptForTest(RFC message) = %q, want %q", got, rfcPlaintext)
		}
	})

	t.Run("encrypt reproduces the RFC message", func(t *testing.T) {
		got, err := encrypt([]byte(rfcPlaintext), b64(t, rfcUAPublic), b64(t, rfcAuthSecret), asPrivate, b64(t, rfcSalt))
		if err != nil {
			t.Fatalf("encrypt() error: %v", err)
		}
		// The RFC's example request says "Content-Length: 145", but its
		// body is 192 base64url characters, 144 bytes: 86 of header, 41
		// of plaintext, the delimiter and the 16-byte tag. The header line
		// is the RFC's slip, not the body's: erratum 5230 corrects it to
		// 144 (https://www.rfc-editor.org/errata/eid5230).
		if len(got) != 144 || len(want) != 144 {
			t.Errorf("encrypt() gave %d bytes and the RFC message decodes to %d, want 144 for both", len(got), len(want))
		}
		if !bytes.Equal(got, want) {
			t.Errorf("encrypt() =\n  %s\nwant\n  %s",
				base64.RawURLEncoding.EncodeToString(got), base64.RawURLEncoding.EncodeToString(want))
		}
	})
}

// TestEncryptRoundTrip is the other load-bearing test: what Encrypt makes
// with fresh randomness, the RFC receiver gets back. It uses the RFC's
// receiver key pair so the receiver can be given its private key; the
// sender key and salt come from crypto/rand as in real use. The spelling
// rows pin the promise that a subscription key is accepted as a browser
// or a JSON round trip may spell it (padded, standard alphabet), and the
// size rows pin the body size: the longest allowed plaintext fills a
// 4096-byte body exactly, the cap push services enforce.
func TestEncryptRoundTrip(t *testing.T) {
	uaPublic := b64(t, rfcUAPublic)
	auth := b64(t, rfcAuthSecret)
	std := base64.StdEncoding.EncodeToString(uaPublic)
	if !strings.ContainsAny(std, "+/") {
		t.Fatalf("the RFC key's standard base64 %q has no + or /, so the spelling rows prove nothing", std)
	}
	stdAuth := base64.StdEncoding.EncodeToString(auth)
	if !strings.HasSuffix(stdAuth, "==") {
		t.Fatalf("the RFC auth secret's standard base64 %q is unpadded, so the spelling rows prove nothing", stdAuth)
	}
	long := bytes.Repeat([]byte("w"), MaxPlaintextLength)

	tests := []struct {
		name      string
		p256dh    string
		auth      string
		plaintext []byte
	}{
		{"base64url, no padding (as the browser gives it)", rfcUAPublic, rfcAuthSecret, []byte(rfcPlaintext)},
		{"base64url, padded", rfcUAPublic + "=", rfcAuthSecret + "==", []byte(rfcPlaintext)},
		{"standard base64, padded", std, stdAuth, []byte(rfcPlaintext)},
		{"standard base64, no padding", strings.TrimRight(std, "="), strings.TrimRight(stdAuth, "="), []byte(rfcPlaintext)},
		{"a JSON payload", rfcUAPublic, rfcAuthSecret, []byte(`{"title":"afk","body":"T1 needs you","url":"/p/afk/plan","tag":"afk-inbox"}`)},
		{"empty plaintext", rfcUAPublic, rfcAuthSecret, []byte{}},
		{"longest allowed plaintext fills a 4096-byte body", rfcUAPublic, rfcAuthSecret, long},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := Subscription{Endpoint: "https://push.example.net/push/abc", P256dh: tt.p256dh, Auth: tt.auth}
			msg, err := Encrypt(sub, tt.plaintext, rand.Reader)
			if err != nil {
				t.Fatalf("Encrypt() error: %v", err)
			}
			if wantLen := headerLength + len(tt.plaintext) + 1 + gcmTagLength; len(msg) != wantLen {
				t.Errorf("Encrypt() body is %d bytes, want %d", len(msg), wantLen)
			}
			if rs := binary.BigEndian.Uint32(msg[saltLength : saltLength+4]); rs != 4096 {
				t.Errorf("header rs = %d, want 4096", rs)
			}
			if msg[saltLength+4] != publicKeyLength {
				t.Errorf("header idlen = %d, want %d", msg[saltLength+4], publicKeyLength)
			}
			got, err := decryptForTest(msg, rfcUAKey(t), auth)
			if err != nil {
				t.Fatalf("decryptForTest() error: %v", err)
			}
			if !bytes.Equal(got, tt.plaintext) {
				t.Errorf("round trip gave %q, want %q", got, tt.plaintext)
			}
			// Fresh key and salt every time: the same notice twice must
			// not give the same bytes (a repeated nonce under one key is
			// AES-GCM's one fatal misuse).
			again, err := Encrypt(sub, tt.plaintext, rand.Reader)
			if err != nil {
				t.Fatalf("Encrypt() again error: %v", err)
			}
			if bytes.Equal(msg, again) {
				t.Error("two encryptions of the same notice gave the same bytes")
			}
			if bytes.Equal(msg[:saltLength], again[:saltLength]) {
				t.Error("two encryptions used the same salt")
			}
		})
	}
	if len(long) != 3993 {
		t.Errorf("MaxPlaintextLength = %d, want 3993", len(long))
	}
}

// TestEncryptRefusals is the table of inputs Encrypt and Payload.Validate
// refuse before any encryption, each with a plain error. The rows that
// matter most: a plaintext one byte over the cap (a 4097-byte body would
// be refused by the push service with a far less helpful answer), a
// p256dh that decodes but is not a point on the curve (crypto/ecdh
// refuses it; without that check ECDH against an invalid point is the
// classic key-recovery attack), and a payload URL that would leave the
// page (a full address and the scheme-relative "//host").
func TestEncryptRefusals(t *testing.T) {
	okSub := Subscription{Endpoint: "https://push.example.net/push/abc", P256dh: rfcUAPublic, Auth: rfcAuthSecret}
	notOnCurve := base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	compressed := base64.RawURLEncoding.EncodeToString(append([]byte{2}, b64(t, rfcUAPublic)[1:33]...))
	shortKey := base64.RawURLEncoding.EncodeToString(b64(t, rfcUAPublic)[:64])
	// mixedAlphabet has one character from each base64 alphabet, which
	// neither decoder takes: the decoder is lenient about padding and
	// alphabet, not about garbage.
	mixedAlphabet := rfcUAPublic[:len(rfcUAPublic)-2] + "+_"
	// countingReader fails the test if a refused call reaches for
	// randomness: a refusal happens before the ephemeral key is made.
	countingReader := &countReader{}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"plaintext one byte over the cap", func() error {
			_, err := Encrypt(okSub, make([]byte, MaxPlaintextLength+1), countingReader)
			return err
		}, "too long"},
		{"p256dh not base64", func() error {
			_, err := Encrypt(Subscription{P256dh: "not base64!", Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"p256dh mixes the two base64 alphabets", func() error {
			_, err := Encrypt(Subscription{P256dh: mixedAlphabet, Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"p256dh 64 bytes", func() error {
			_, err := Encrypt(Subscription{P256dh: shortKey, Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"p256dh compressed point", func() error {
			_, err := Encrypt(Subscription{P256dh: compressed, Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"p256dh 65 bytes not on the curve", func() error {
			_, err := Encrypt(Subscription{P256dh: notOnCurve, Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"p256dh empty", func() error {
			_, err := Encrypt(Subscription{P256dh: "", Auth: rfcAuthSecret}, []byte("x"), countingReader)
			return err
		}, "p256dh"},
		{"auth 15 bytes", func() error {
			_, err := Encrypt(Subscription{P256dh: rfcUAPublic, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 15))}, []byte("x"), countingReader)
			return err
		}, "auth secret is not 16 bytes"},
		{"auth 17 bytes", func() error {
			_, err := Encrypt(Subscription{P256dh: rfcUAPublic, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 17))}, []byte("x"), countingReader)
			return err
		}, "auth secret is not 16 bytes"},
		{"auth not base64", func() error {
			_, err := Encrypt(Subscription{P256dh: rfcUAPublic, Auth: "***"}, []byte("x"), countingReader)
			return err
		}, "auth secret is not 16 bytes"},
		{"auth empty", func() error {
			_, err := Encrypt(Subscription{P256dh: rfcUAPublic, Auth: ""}, []byte("x"), countingReader)
			return err
		}, "auth secret is not 16 bytes"},
		{"no randomness", func() error {
			_, err := Encrypt(okSub, []byte("x"), nil)
			return err
		}, "random"},
		{"payload url is a full address", func() error {
			return Payload{Title: "afk", Body: "b", URL: "https://evil.example/x"}.Validate()
		}, "path on the page"},
		{"payload url is //host", func() error {
			return Payload{Title: "afk", Body: "b", URL: "//evil.example/x"}.Validate()
		}, "path on the page"},
		{"payload url is a scheme without a host", func() error {
			return Payload{Title: "afk", Body: "b", URL: "mailto:owner@example.com"}.Validate()
		}, "path on the page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("got no error, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			// Logged so a -v run shows the words a caller would see.
			t.Logf("refused: %v", err)
		})
	}
	if countingReader.n != 0 {
		t.Errorf("refused calls read %d random bytes, want 0: a refusal must come before the message key is made", countingReader.n)
	}

	t.Run("inner encrypt refuses a bad salt", func(t *testing.T) {
		asPrivate, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
		if err != nil {
			t.Fatal(err)
		}
		_, err = encrypt([]byte("x"), b64(t, rfcUAPublic), b64(t, rfcAuthSecret), asPrivate, make([]byte, 15))
		if err == nil || !strings.Contains(err.Error(), "salt") {
			t.Errorf("encrypt() with a 15-byte salt: error = %v, want one about the salt", err)
		}
		_, err = encrypt([]byte("x"), b64(t, rfcUAPublic), b64(t, rfcAuthSecret), nil, b64(t, rfcSalt))
		if err == nil || !strings.Contains(err.Error(), "message key") {
			t.Errorf("encrypt() with no sender key: error = %v, want one about the message key", err)
		}
	})
}

// countReader counts the bytes read from it; it hands out zeros, so a test
// that expects no reads is not silently given a working key.
type countReader struct{ n int }

func (r *countReader) Read(p []byte) (int, error) {
	r.n += len(p)
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TestPayloadValidate is the full matrix of the URL rule: a path on the
// page, with its query and fragment, is the only thing accepted. The
// refused rows that matter are the ones a browser would resolve to another
// site: "//host", "/\host" (the WHATWG parser reads the backslash as a
// slash, which url.Parse does not, so the rule checks it by hand), and a
// full address.
func TestPayloadValidate(t *testing.T) {
	tests := []struct {
		name string
		url  string
		ok   bool
	}{
		{"page root", "/", true},
		{"plan page", "/p/afk/plan", true},
		{"path with query and fragment", "/p/afk/plan?task=T1#top", true},
		{"path with a colon in a later segment", "/p/afk/plan/a:b", true},
		{"empty", "", false},
		{"relative path", "p/afk/plan", false},
		{"full https address", "https://afk.example/p/afk/plan", false},
		{"full http address", "http://afk.example/", false},
		{"scheme-relative //host", "//afk.example/p", false},
		{"backslash /\\host", "/\\afk.example/p", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"mailto", "mailto:owner@example.com", false},
		{"control character", "/p/\x00", false},
		{"userinfo", "//owner@afk.example/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Payload{Title: "afk", Body: "b", URL: tt.url}.Validate()
			if tt.ok && err != nil {
				t.Errorf("Validate() = %v, want ok", err)
			}
			if !tt.ok && err == nil {
				t.Error("Validate() = nil, want a refusal")
			}
			if err != nil && strings.Contains(err.Error(), tt.url) && tt.url != "" {
				t.Errorf("Validate() error quotes the input: %q", err)
			}
		})
	}
}
