// Package webpass owns the web page's password: a slow, salted hash of it,
// the cookie value a right password earns, and the file both live in.
//
// The page's one secret lives in afk's per-machine state folder
// (usagestate.Dir, normally ~/.local/state/afk), not in config.toml. That
// file is a settings file the owner reads, edits and may copy between
// machines; a secret there is one paste away from leaking. The state folder
// is machine-written, so the hash sits beside the other files afk owns, in a
// file of its own with mode 0600.
//
// Nothing here caches the record. Callers read the file again on every
// check, so a new password saved by `afk web password` takes effect on the
// very next request, without restarting the daemon, and every old cookie
// stops matching at the same moment (see Record.CookieValue).
//
// The hash is PBKDF2-HMAC-SHA256 from the standard library's crypto/pbkdf2
// (Go 1.24+), so no new module is needed. Its iteration count is stored in
// the record, so a later raise of DefaultIterations still verifies passwords
// saved under the old count.
package webpass

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/usagestate"
)

// Algorithm is the only hash this package writes or accepts. It is stored
// in the record so a reader can refuse a file it does not understand rather
// than comparing against a key computed some other way.
const Algorithm = "pbkdf2-sha256"

// DefaultIterations is the PBKDF2 iteration count for new passwords, the
// OWASP 2023 figure for PBKDF2-HMAC-SHA256. A sign-in pays it once, which is
// a fraction of a second; a guesser pays it for every guess.
const DefaultIterations = 600000

// MinLength is the shortest password accepted, counted in runes so a
// password in a script with multibyte letters is measured as the owner sees
// it, not by its UTF-8 bytes.
const MinLength = 8

// FileName is the record's name inside the state folder.
const FileName = "web-password.json"

// saltLength and keyLength are fixed: 16 bytes of salt is the usual floor
// (and the floor crypto/pbkdf2 enforces in FIPS 140-only mode), and 32 bytes
// of key is SHA-256's own output size, so PBKDF2 computes a single block.
const (
	saltLength = 16
	keyLength  = 32
)

// cookieLabel is the fixed message CookieValue signs. It only has to differ
// from anything else ever signed with the key, and nothing else is.
const cookieLabel = "afk-web-cookie"

// ErrTooShort is New's refusal of a password under MinLength runes.
var ErrTooShort = fmt.Errorf("the password needs at least %d characters", MinLength)

// ErrNoPassword is Read's answer when no password has been saved yet. It is
// its own error, not a generic one, because "no password" is the ordinary
// state of a fresh machine (the page stays off) while a broken file is
// something the owner has to fix.
var ErrNoPassword = errors.New("no web password is set")

// Record is the saved form of a password: never the password itself, only
// the salt, the iteration count and the derived key. The []byte fields are
// written as base64 by encoding/json.
type Record struct {
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Key        []byte `json:"key"`
}

// New hashes password with a fresh salt read from rnd (crypto/rand.Reader in
// real use; a fixed reader in tests, so the result is reproducible). The
// error never includes the password.
func New(password string, rnd io.Reader, iterations int) (Record, error) {
	if utf8.RuneCountInString(password) < MinLength {
		return Record{}, ErrTooShort
	}
	if iterations <= 0 {
		return Record{}, fmt.Errorf("the iteration count must be positive, got %d", iterations)
	}
	if rnd == nil {
		return Record{}, errors.New("no source of random bytes for the salt")
	}
	salt := make([]byte, saltLength)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return Record{}, fmt.Errorf("reading a random salt: %w", err)
	}
	key, err := derive(password, salt, iterations)
	if err != nil {
		return Record{}, err
	}
	return Record{Algorithm: Algorithm, Iterations: iterations, Salt: salt, Key: key}, nil
}

// derive is the one place the key is computed, so New and Verify cannot
// drift apart. pbkdf2.Key returns an error only when FIPS 140-only mode
// refuses its parameters (a key under 112 bits, a salt under 128 bits or a
// non-SHA-2/3 hash); ours meet all three, so the error is passed on rather
// than expected.
func derive(password string, salt []byte, iterations int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLength)
	if err != nil {
		return nil, fmt.Errorf("hashing the password: %w", err)
	}
	return key, nil
}

// validate says whether a record is one this package could have written. It
// is shared by Read, Write and Verify so a hand-edited or truncated file is
// refused the same way everywhere. The key length is pinned to keyLength
// because Verify recomputes a key of that size; a record with another length
// could never match and would only hide the real problem.
func (r Record) validate() error {
	switch {
	case r.Algorithm != Algorithm:
		// The stored name is not quoted back: Read wraps this error, and
		// its text must never carry the file's contents.
		return errors.New("the password record uses an unknown hash")
	case r.Iterations <= 0:
		return errors.New("the password record has no iteration count")
	case len(r.Salt) == 0:
		return errors.New("the password record has no salt")
	case len(r.Key) != keyLength:
		return errors.New("the password record has no valid key")
	}
	return nil
}

// Verify says whether password is the one r was made from. It recomputes the
// key with r's own salt and iteration count and compares in constant time,
// so the time taken does not reveal how many leading bytes matched. A record
// that fails validate (unknown algorithm, no iterations, empty key) never
// verifies: an empty key compared with ConstantTimeCompare against nothing
// would otherwise be a match for any password.
func (r Record) Verify(password string) bool {
	if r.validate() != nil {
		return false
	}
	key, err := derive(password, r.Salt, r.Iterations)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, r.Key) == 1
}

// CookieValue is the value of the sign-in cookie for this password: the hex
// of HMAC-SHA256 keyed by the derived key over a fixed label.
//
// It is tied to the key, not stored on its own, so changing the password
// signs every browser out: a new password has a new salt and so a new key,
// and every cookie made from the old key stops matching. Nothing has to
// remember which cookies were handed out.
//
// It cannot be turned back into the password: it is an HMAC of the key, and
// the key is itself a slow one-way hash of the password.
//
// Two honest limits follow from having no server-side session list. Every
// browser signed in with one password carries the same value, so signing out
// clears the cookie in that browser only; the others stay signed in until
// the password changes. And anyone who can read the 0600 record file can
// compute this value and sign in without the password, the same exposure a
// reader of config.toml had when the page used a token.
//
// A record that fails validate returns "", which callers must treat as
// matching nothing (they compare against a non-empty cookie).
func (r Record) CookieValue() string {
	if r.validate() != nil {
		return ""
	}
	mac := hmac.New(sha256.New, r.Key)
	mac.Write([]byte(cookieLabel))
	return hex.EncodeToString(mac.Sum(nil))
}

// withDefaults fills every nil field of d from usagestate.DefaultDeps.
// usagestate does the same internally but does not export it, and the
// callers here pass partly filled Deps (the daemon's test rig sets only
// Getenv and HomeDir; some tests pass a zero Deps), so Read, Write and
// Exists must never call a nil func.
func withDefaults(d usagestate.Deps) usagestate.Deps {
	def := usagestate.DefaultDeps()
	if d.Getenv == nil {
		d.Getenv = def.Getenv
	}
	if d.HomeDir == nil {
		d.HomeDir = def.HomeDir
	}
	if d.ReadFile == nil {
		d.ReadFile = def.ReadFile
	}
	if d.WriteFile == nil {
		d.WriteFile = def.WriteFile
	}
	if d.Rename == nil {
		d.Rename = def.Rename
	}
	if d.MkdirAll == nil {
		d.MkdirAll = def.MkdirAll
	}
	if d.Remove == nil {
		d.Remove = def.Remove
	}
	return d
}

// Path is the record file's location: the state folder plus FileName.
func Path(d usagestate.Deps) (string, error) {
	dir, err := usagestate.Dir(withDefaults(d))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// tmpCounter makes each temp file name unique within one process, so two
// saves racing in one process never write the same temp file.
var tmpCounter atomic.Uint64

// Write saves r atomically with mode 0600: a same-directory temp file, then
// a rename over the record. The temp file is in the same directory because
// rename is only atomic within one filesystem, and a reader must never see
// half a record (it would sign everybody out until the next save). The
// directory is made 0700 when missing, since it now holds a secret. The mode
// is set when the temp file is created, so the record is never readable by
// others, not even for a moment.
func Write(d usagestate.Deps, r Record) error {
	d = withDefaults(d)
	if err := r.validate(); err != nil {
		return fmt.Errorf("not saving the web password: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the web password record: %w", err)
	}
	data = append(data, '\n')

	path, err := Path(d)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := d.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state directory %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d.%d", FileName, os.Getpid(), tmpCounter.Add(1)))
	if err := d.WriteFile(tmp, data, 0o600); err != nil {
		_ = d.Remove(tmp)
		return fmt.Errorf("writing temp password file %s: %w", tmp, err)
	}
	if err := d.Rename(tmp, path); err != nil {
		_ = d.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}

// Read loads the saved record. A missing file is ErrNoPassword. A file that
// cannot be read, parsed or validated is a plain error naming the path and
// the problem, never the file's contents: the file holds the key and salt,
// and an error can end up on a web page or in a log.
func Read(d usagestate.Deps) (Record, error) {
	d = withDefaults(d)
	path, err := Path(d)
	if err != nil {
		return Record{}, err
	}
	data, err := d.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, ErrNoPassword
		}
		return Record{}, fmt.Errorf("reading the web password file %s: %w", path, err)
	}
	var r Record
	// The json error is dropped on purpose: its text can quote the bad
	// input (for example "invalid character 'x' looking for ...").
	if json.Unmarshal(data, &r) != nil {
		return Record{}, fmt.Errorf("the web password file %s is not a valid password record", path)
	}
	if err := r.validate(); err != nil {
		return Record{}, fmt.Errorf("the web password file %s is not a valid password record: %w", path, err)
	}
	return r, nil
}

// Exists says whether a usable password is saved: Read succeeds. A broken
// file counts as no password, so the daemon keeps the page off rather than
// serving a page nobody can sign in to.
func Exists(d usagestate.Deps) bool {
	_, err := Read(d)
	return err == nil
}
