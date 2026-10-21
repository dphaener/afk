package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// KeyFileName is afk's VAPID key file inside the state folder.
const KeyFileName = "push-key.json"

// The raw sizes of a P-256 key: the private scalar is a 32-byte big-endian
// integer (SEC 1 §2.3.6), and the public key is the 65-byte uncompressed
// point 0x04 || X || Y (SEC 1 §2.3.3). The uncompressed point is the form
// RFC 8292 §3.2 sends in the "k" parameter and the form the browser's
// pushManager.subscribe takes as applicationServerKey, so it is also the
// form the file stores.
const (
	privateKeyLength = 32
	publicKeyLength  = 65
)

// Key is afk's VAPID key: the P-256 key pair every push is signed with.
//
// A browser's subscription is bound to the public key it subscribed with,
// so this key must stay the same for as long as any device is signed up.
// That is why LoadOrCreateKey makes it only when no file exists and never
// replaces a file it cannot read: a new key would silently cut off every
// signed-up device.
//
// The zero Key holds no key; signing with it is an error.
type Key struct {
	priv *ecdsa.PrivateKey
	// pub is the uncompressed public point, kept so PublicKey and every
	// signature do not re-encode it.
	pub []byte
}

// PublicKey is the public half as base64url without padding: the
// applicationServerKey the page passes to pushManager.subscribe, and the "k"
// parameter of every VAPID Authorization header. The zero Key returns "".
func (k Key) PublicKey() string {
	if k.pub == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(k.pub)
}

// keyFile is the on-disk shape of push-key.json. Every key is always
// written; the keys are base64url without padding, created_at is RFC 3339
// in UTC.
type keyFile struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	CreatedAt  string `json:"created_at"`
}

// keyMu serialises LoadOrCreateKey within one process. The daemon and the
// page it serves both ask for the key (T15.5, T15.7), and without the lock
// two first calls racing in one process could each find no file, each make
// a key, and the loser's caller would go on using a key the file no longer
// holds. Two separate processes making the first key at the same moment are
// not covered; afk runs one daemon per machine (the machine lock), which is
// the process that serves the page and sends.
var keyMu sync.Mutex

// LoadOrCreateKey returns the key saved in dir/push-key.json, or makes one
// and saves it when that file does not exist.
//
// rnd is the randomness for a new key (crypto/rand.Reader in real use). It
// is read only when a key is made, and a new key is not reproducible from
// it: Go 1.25's ecdsa.GenerateKey does read a caller's reader, but it calls
// randutil.MaybeReadByte (and drbg.ReadWithReader calls it again for any
// reader other than crypto/rand's), which skips a byte of the stream on a
// coin flip, so even a fixed reader gives one of several keys. Tests
// therefore check "made once, then reused" rather than a fixed key. now
// stamps created_at on a new file.
//
// A file that exists but cannot be read, parsed or checked (bad JSON, keys
// of the wrong length, a public key that is not the private key's) is an
// error naming the file and never quoting it: it holds the private key, and
// an error can end up on the web page or in a log. Such a file is left
// exactly as it was. Replacing it would hand out a new key and silently cut
// off every device signed up with the old one; the owner has to decide.
//
// A new file is written with mode 0600 through a temp file in the same
// folder and a rename, so no reader ever sees half a key, and the folder is
// made 0700 when missing.
func LoadOrCreateKey(dir string, rnd io.Reader, now time.Time) (Key, error) {
	keyMu.Lock()
	defer keyMu.Unlock()

	path := filepath.Join(dir, KeyFileName)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, perr := parseKeyFile(data)
		if perr != nil {
			return Key{}, fmt.Errorf("the push key file %s is damaged (%v); afk will not replace it, because a new key would cut off every signed-up device. Move it aside to make a new key, then sign each device up again", path, perr)
		}
		return key, nil
	case !errors.Is(err, fs.ErrNotExist):
		return Key{}, fmt.Errorf("reading the push key file %s: %w", path, err)
	}

	if rnd == nil {
		return Key{}, errors.New("no source of random bytes to make the push key")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rnd)
	if err != nil {
		return Key{}, fmt.Errorf("making the push key: %w", err)
	}
	key, err := keyFromPrivate(priv)
	if err != nil {
		return Key{}, err
	}
	raw, err := priv.Bytes()
	if err != nil {
		return Key{}, fmt.Errorf("encoding the push key: %w", err)
	}
	out, err := json.MarshalIndent(keyFile{
		PrivateKey: base64.RawURLEncoding.EncodeToString(raw),
		PublicKey:  key.PublicKey(),
		CreatedAt:  now.UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return Key{}, fmt.Errorf("encoding the push key file: %w", err)
	}
	out = append(out, '\n')
	if err := writeFileAtomic(dir, KeyFileName, out); err != nil {
		return Key{}, err
	}
	return key, nil
}

// keyFromPrivate fills a Key from a private key, encoding its public point.
func keyFromPrivate(priv *ecdsa.PrivateKey) (Key, error) {
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return Key{}, fmt.Errorf("encoding the push public key: %w", err)
	}
	return Key{priv: priv, pub: pub}, nil
}

// parseKeyFile checks and decodes the contents of push-key.json. Its errors
// say what is wrong in plain words and never carry the input: the decoding
// errors of encoding/json and encoding/base64 are dropped rather than
// wrapped, since json's can quote the bad bytes.
//
// The public key is checked against the private one rather than trusted.
// The private key is what signs, and the public key is what browsers were
// given; if the two disagree every push would be refused by the push
// service, which is far harder to trace than an error here.
func parseKeyFile(data []byte) (Key, error) {
	var f keyFile
	if json.Unmarshal(data, &f) != nil {
		return Key{}, errors.New("it is not valid JSON")
	}
	rawPriv, err := base64.RawURLEncoding.DecodeString(f.PrivateKey)
	if err != nil || len(rawPriv) != privateKeyLength {
		return Key{}, fmt.Errorf("its private key is not %d bytes of base64url", privateKeyLength)
	}
	rawPub, err := base64.RawURLEncoding.DecodeString(f.PublicKey)
	if err != nil || len(rawPub) != publicKeyLength {
		return Key{}, fmt.Errorf("its public key is not %d bytes of base64url", publicKeyLength)
	}
	if _, err := time.Parse(time.RFC3339, f.CreatedAt); err != nil {
		return Key{}, errors.New("its created_at is not an RFC 3339 time")
	}
	// ParseRawPrivateKey refuses zero and a scalar not below the curve
	// order, so a key that could never sign is caught here.
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), rawPriv)
	if err != nil {
		return Key{}, errors.New("its private key is not a valid P-256 key")
	}
	key, err := keyFromPrivate(priv)
	if err != nil {
		return Key{}, err
	}
	if !bytes.Equal(key.pub, rawPub) {
		return Key{}, errors.New("its public key does not belong to its private key")
	}
	return key, nil
}

// writeFileAtomic replaces dir/name with data, mode 0600, through a temp
// file in the same folder and a rename. The temp file is in the same folder
// because rename is only atomic within one filesystem, and a reader must
// never see half a file. os.CreateTemp creates the file 0600, so the secret
// is never readable by others, not even for a moment. The folder is made
// 0700 when missing, since it holds secrets.
//
// It is shared by every file this package writes (push-key.json now,
// push-devices.json in T15.4 W4.T1), so they cannot drift apart.
func writeFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating the state folder %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp.*")
	if err != nil {
		return fmt.Errorf("creating a temp file for %s in %s: %w", name, dir, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("setting the mode of %s: %w", tmpName, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(fmt.Errorf("writing %s: %w", tmpName, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming %s to %s: %w", tmpName, path, err)
	}
	return nil
}
