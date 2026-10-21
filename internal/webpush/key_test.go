package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingReader counts the bytes read through it, so a test can tell
// whether LoadOrCreateKey made a key (it reads) or reused one (it must not).
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

var keyTestNow = time.Date(2026, 10, 20, 9, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

// TestLoadOrCreateKey covers the healthy life of push-key.json. The
// load-bearing row is "made once then reused": every browser subscribes
// with the public key, so a second call that made a new key, or rewrote the
// file, would silently cut off every signed-up device. "file mode 0600" and
// "missing dir is created" matter because the file holds a private key and
// the state folder does not exist on a fresh machine.
func TestLoadOrCreateKey(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, dir string)
	}{
		{"made once then reused", func(t *testing.T, dir string) {
			first, err := LoadOrCreateKey(dir, rand.Reader, keyTestNow)
			if err != nil {
				t.Fatalf("first call: %v", err)
			}
			path := filepath.Join(dir, KeyFileName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading the key file: %v", err)
			}
			rnd := &countingReader{r: rand.Reader}
			second, err := LoadOrCreateKey(dir, rnd, keyTestNow.Add(time.Hour))
			if err != nil {
				t.Fatalf("second call: %v", err)
			}
			if second.PublicKey() != first.PublicKey() {
				t.Errorf("second call public key = %q, want the first one %q", second.PublicKey(), first.PublicKey())
			}
			if rnd.n != 0 {
				t.Errorf("second call read %d random bytes; a saved key must be reused, not remade", rnd.n)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading the key file again: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("the key file changed on the second call:\nbefore %s\nafter  %s", before, after)
			}
			if !first.priv.Equal(second.priv) {
				t.Error("the second call returned a different private key")
			}
		}},
		{"file mode 0600", func(t *testing.T, dir string) {
			if _, err := LoadOrCreateKey(dir, rand.Reader, keyTestNow); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, KeyFileName))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("key file mode = %o, want 600", got)
			}
		}},
		{"missing dir is created", func(t *testing.T, dir string) {
			nested := filepath.Join(dir, "state", "afk")
			if _, err := LoadOrCreateKey(nested, rand.Reader, keyTestNow); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(nested)
			if err != nil {
				t.Fatalf("the state folder was not made: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("state folder mode = %o, want 700", got)
			}
			if _, err := os.Stat(filepath.Join(nested, KeyFileName)); err != nil {
				t.Errorf("no key file in the new folder: %v", err)
			}
		}},
		{"file shape and no temp file left", func(t *testing.T, dir string) {
			key, err := LoadOrCreateKey(dir, rand.Reader, keyTestNow)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, KeyFileName))
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]string
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("key file is not a JSON object of strings: %v", err)
			}
			if len(raw) != 3 {
				t.Errorf("key file has keys %v, want exactly private_key, public_key, created_at", raw)
			}
			if raw["created_at"] != "2026-10-20T07:30:00Z" {
				t.Errorf("created_at = %q, want the injected time in UTC", raw["created_at"])
			}
			if raw["public_key"] != key.PublicKey() {
				t.Errorf("public_key = %q, want PublicKey() %q", raw["public_key"], key.PublicKey())
			}
			priv, err := base64.RawURLEncoding.DecodeString(raw["private_key"])
			if err != nil || len(priv) != 32 {
				t.Errorf("private_key is not 32 bytes of unpadded base64url (err %v, len %d)", err, len(priv))
			}
			pub, err := base64.RawURLEncoding.DecodeString(key.PublicKey())
			if err != nil || len(pub) != 65 || pub[0] != 4 {
				t.Errorf("PublicKey() is not a 65-byte uncompressed point in unpadded base64url (err %v, len %d)", err, len(pub))
			}
			if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub); err != nil {
				t.Errorf("PublicKey() is not a P-256 point: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				names := []string{}
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("state folder holds %v, want only %s", names, KeyFileName)
			}
		}},
		{"no random source refused", func(t *testing.T, dir string) {
			if _, err := LoadOrCreateKey(dir, nil, keyTestNow); err == nil {
				t.Fatal("a nil random source made a key")
			}
			if _, err := os.Stat(filepath.Join(dir, KeyFileName)); !os.IsNotExist(err) {
				t.Errorf("a refused call left a key file (stat err %v)", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, t.TempDir())
		})
	}
}

// keyFileJSON builds push-key.json contents from raw parts, so damaged rows
// can break exactly one thing.
func keyFileJSON(t *testing.T, priv, pub []byte, createdAt string) []byte {
	t.Helper()
	data, err := json.Marshal(keyFile{
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv),
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
		CreatedAt:  createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// rawKeyPair makes a P-256 key pair for building fixture files.
func rawKeyPair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv, err = k.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	pub, err = k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

// TestLoadOrCreateKeyDamaged covers a push-key.json afk cannot use. Every
// row is load-bearing in the same two ways: the error must name the file
// (so the owner can find it) and must not quote it (it holds the private
// key, and an error can reach the web page or a log); and the file must be
// left byte for byte as it was, with no new key made, because replacing it
// would cut off every signed-up device. "mismatched pair" is the subtle row:
// both halves are well-formed, but signing with one and handing browsers the
// other would make every push fail at the push service.
func TestLoadOrCreateKeyDamaged(t *testing.T) {
	privA, pubA := rawKeyPair(t)
	_, pubB := rawKeyPair(t)
	good := "2026-10-20T07:30:00Z"
	zero := make([]byte, 32)
	order := elliptic.P256().Params().N.FillBytes(make([]byte, 32))

	tests := []struct {
		name string
		data []byte
		// secret is a string from the file that must not appear in the
		// error; empty means the row checks only the private key.
		secret string
	}{
		{"bad JSON", []byte(`{"private_key": "SECRET-MARKER-not-json`), "SECRET-MARKER"},
		{"empty file", []byte{}, ""},
		{"JSON array", []byte(`["SECRET-MARKER"]`), "SECRET-MARKER"},
		{"short private key", keyFileJSON(t, privA[:31], pubA, good), ""},
		{"short public key", keyFileJSON(t, privA, pubA[:64], good), ""},
		{"private key not base64url", []byte(`{"private_key":"SECRET+MARKER/xx==","public_key":"` + base64.RawURLEncoding.EncodeToString(pubA) + `","created_at":"` + good + `"}`), "SECRET+MARKER"},
		{"mismatched pair", keyFileJSON(t, privA, pubB, good), ""},
		{"zero private key", keyFileJSON(t, zero, pubA, good), ""},
		{"private key not below the curve order", keyFileJSON(t, order, pubA, good), ""},
		{"created_at missing", keyFileJSON(t, privA, pubA, ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, KeyFileName)
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			rnd := &countingReader{r: rand.Reader}
			key, err := LoadOrCreateKey(dir, rnd, keyTestNow)
			if err == nil {
				t.Fatalf("a damaged key file loaded as key %q", key.PublicKey())
			}
			msg := err.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("error %q does not name the file %s", msg, path)
			}
			for _, s := range []string{tt.secret, base64.RawURLEncoding.EncodeToString(privA)} {
				if s != "" && strings.Contains(msg, s) {
					t.Errorf("error %q quotes the file's contents (%q)", msg, s)
				}
			}
			if len(tt.data) > 0 && strings.Contains(msg, string(tt.data)) {
				t.Errorf("error %q quotes the whole file", msg)
			}
			if rnd.n != 0 {
				t.Errorf("read %d random bytes; a damaged file must not lead to a new key", rnd.n)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the damaged file is gone: %v", err)
			}
			if !bytes.Equal(after, tt.data) {
				t.Errorf("the damaged file was overwritten:\nbefore %q\nafter  %q", tt.data, after)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("state folder holds %d entries, want only the damaged file", len(entries))
			}
		})
	}

	// A path that cannot be read at all (here a folder where the file
	// should be) is not "missing" either, so it must not lead to a new key.
	t.Run("damaged: key file is a folder", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, KeyFileName)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := LoadOrCreateKey(dir, rand.Reader, keyTestNow)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("err = %v, want an error naming %s", err, path)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("the folder in the key file's place was replaced (stat err %v)", err)
		}
	})
}
