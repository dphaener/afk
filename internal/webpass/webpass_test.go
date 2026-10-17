package webpass

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/usagestate"
)

// testIterations keeps the tests fast; the count is stored in the record, so
// nothing here depends on DefaultIterations.
const testIterations = 1000

// fixedSalt returns a reader that yields the same salt bytes every time, so a
// test can make two records from the same salt or from different ones.
func fixedSalt(b byte) *bytes.Reader {
	return bytes.NewReader(bytes.Repeat([]byte{b}, saltLength))
}

// stateDeps points the state folder at a fresh temp directory through an
// injected Getenv, leaving every other field nil so withDefaults is
// exercised on every call.
func stateDeps(t *testing.T) (usagestate.Deps, string) {
	t.Helper()
	root := t.TempDir()
	return usagestate.Deps{
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return root
			}
			return ""
		},
		HomeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
	}, filepath.Join(root, "afk", FileName)
}

func mustNew(t *testing.T, password string, salt byte) Record {
	t.Helper()
	r, err := New(password, fixedSalt(salt), testIterations)
	if err != nil {
		t.Fatalf("New(%q) error = %v", password, err)
	}
	return r
}

// TestNewAndVerify covers what a password must be and what it matches. The
// load-bearing rows are the length boundary (7 runes refused, 8 accepted)
// and the multibyte rows: the limit is counted in runes, so 7 two-byte
// letters (14 bytes) must still be refused and 8 of them accepted. The wrong
// password row is the whole point of Verify.
func TestNewAndVerify(t *testing.T) {
	tests := []struct {
		name       string
		password   string
		iterations int
		wantErr    error // nil means New succeeds
		anyErr     bool  // New fails with some error other than ErrTooShort
		try        string
		wantMatch  bool
	}{
		{name: "right password", password: "correct-horse", iterations: testIterations, try: "correct-horse", wantMatch: true},
		{name: "wrong password", password: "correct-horse", iterations: testIterations, try: "correct-horsf"},
		{name: "empty try", password: "correct-horse", iterations: testIterations, try: ""},
		{name: "7 runes refused", password: "1234567", iterations: testIterations, wantErr: ErrTooShort},
		{name: "8 runes accepted", password: "12345678", iterations: testIterations, try: "12345678", wantMatch: true},
		{name: "empty refused", password: "", iterations: testIterations, wantErr: ErrTooShort},
		{name: "7 multibyte runes refused", password: "ééééééé", iterations: testIterations, wantErr: ErrTooShort},
		{name: "8 multibyte runes accepted", password: "éééééééé", iterations: testIterations, try: "éééééééé", wantMatch: true},
		{name: "zero iterations refused", password: "correct-horse", iterations: 0, anyErr: true},
		{name: "negative iterations refused", password: "correct-horse", iterations: -5, anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New(tt.password, fixedSalt(7), tt.iterations)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("New() error = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.anyErr:
				if err == nil {
					t.Fatal("New() error = nil, want an error")
				}
				if tt.password != "" && strings.Contains(err.Error(), tt.password) {
					t.Errorf("New() error %q contains the password", err)
				}
				return
			case err != nil:
				t.Fatalf("New() error = %v", err)
			}
			if r.Algorithm != Algorithm || r.Iterations != tt.iterations || len(r.Salt) != saltLength || len(r.Key) != keyLength {
				t.Fatalf("New() = %+v, want a %s record with %d iterations", r, Algorithm, tt.iterations)
			}
			if got := r.Verify(tt.try); got != tt.wantMatch {
				t.Errorf("Verify(%q) = %v, want %v", tt.try, got, tt.wantMatch)
			}
		})
	}
}

// TestNewShortRandom pins that a salt source that runs dry is an error, not
// a record with a short or zero salt.
func TestNewShortRandom(t *testing.T) {
	if _, err := New("correct-horse", bytes.NewReader([]byte{1, 2, 3}), testIterations); err == nil {
		t.Fatal("New() with 3 random bytes: error = nil, want an error")
	}
	if _, err := New("correct-horse", nil, testIterations); err == nil {
		t.Fatal("New() with a nil reader: error = nil, want an error")
	}
}

// TestSaltChangesKeyAndCookie is what makes a new password sign every
// browser out: the same password saved again draws a new salt, so the key
// and the cookie value both change. The same salt must give the same value,
// or no cookie would ever match on the next request.
func TestSaltChangesKeyAndCookie(t *testing.T) {
	a := mustNew(t, "correct-horse", 1)
	b := mustNew(t, "correct-horse", 2)
	again := mustNew(t, "correct-horse", 1)

	if bytes.Equal(a.Key, b.Key) {
		t.Error("different salts gave the same key")
	}
	if a.CookieValue() == b.CookieValue() {
		t.Error("different salts gave the same cookie value")
	}
	if a.CookieValue() != again.CookieValue() {
		t.Error("the same salt and password gave different cookie values")
	}
	if got := a.CookieValue(); len(got) != 64 || strings.Contains(got, "correct-horse") {
		t.Errorf("CookieValue() = %q, want 64 hex characters without the password", got)
	}
}

// TestInvalidRecordNeverMatches covers records Read would refuse but a
// caller might still hold. The empty-key row is load-bearing: a constant-time
// compare of two empty keys is a match, so without the check any password
// would verify. Each invalid record must also give an empty cookie value.
func TestInvalidRecordNeverMatches(t *testing.T) {
	good := mustNew(t, "correct-horse", 3)
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{"tampered algorithm", func(r *Record) { r.Algorithm = "pbkdf2-sha1" }},
		{"empty algorithm", func(r *Record) { r.Algorithm = "" }},
		{"zero iterations", func(r *Record) { r.Iterations = 0 }},
		{"negative iterations", func(r *Record) { r.Iterations = -1 }},
		{"empty key", func(r *Record) { r.Key = nil }},
		{"short key", func(r *Record) { r.Key = r.Key[:16] }},
		{"empty salt", func(r *Record) { r.Salt = nil }},
		{"zero record", func(r *Record) { *r = Record{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			r.Salt = append([]byte(nil), good.Salt...)
			r.Key = append([]byte(nil), good.Key...)
			tt.mutate(&r)
			if r.Verify("correct-horse") {
				t.Error("Verify() = true for an invalid record")
			}
			if r.Verify("") {
				t.Error("Verify(\"\") = true for an invalid record")
			}
			if got := r.CookieValue(); got != "" {
				t.Errorf("CookieValue() = %q, want empty for an invalid record", got)
			}
		})
	}
}

// TestPath pins where the file lives. The relative row is load-bearing: the
// XDG spec says a relative XDG_STATE_HOME is ignored, so the file must fall
// back to the home folder rather than land in the current directory.
func TestPath(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		want string
	}{
		{"absolute XDG_STATE_HOME", "/srv/state", filepath.Join("/srv/state", "afk", FileName)},
		{"relative XDG_STATE_HOME ignored", "state", filepath.Join("/home/owner", ".local", "state", "afk", FileName)},
		{"unset XDG_STATE_HOME", "", filepath.Join("/home/owner", ".local", "state", "afk", FileName)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := usagestate.Deps{
				Getenv: func(k string) string {
					if k == "XDG_STATE_HOME" {
						return tt.xdg
					}
					return ""
				},
				HomeDir: func() (string, error) { return "/home/owner", nil },
			}
			got, err := Path(d)
			if err != nil {
				t.Fatalf("Path() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWrite checks what lands on disk. The load-bearing assertions are the
// 0600 mode and that the bytes hold no copy of the password: the record is
// the page's only secret, and the plan promises the owner both.
func TestWrite(t *testing.T) {
	const password = "correct-horse-battery"
	d, path := stateDeps(t)
	r := mustNew(t, password, 9)

	if err := Write(d, r); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("record mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if bytes.Contains(data, []byte(password)) {
		t.Errorf("record file holds the password: %s", data)
	}
	got, err := Read(d)
	if err != nil {
		t.Fatalf("Read() after Write error = %v", err)
	}
	if !got.Verify(password) || got.CookieValue() != r.CookieValue() {
		t.Error("the record read back does not match the one written")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("state dir holds %d entries, want only %s (a temp file was left behind?)", len(entries), FileName)
	}

	// Saving again replaces the record, so the old cookie stops matching.
	next := mustNew(t, password, 10)
	if err := Write(d, next); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	again, err := Read(d)
	if err != nil {
		t.Fatalf("Read() after second Write error = %v", err)
	}
	if again.CookieValue() == r.CookieValue() {
		t.Error("saving a new record kept the old cookie value")
	}
}

// TestWriteFailures pins that a failed save leaves no temp file behind and
// that an invalid record is never written (it would lock the owner out).
func TestWriteFailures(t *testing.T) {
	t.Run("rename fails, temp file removed", func(t *testing.T) {
		d, path := stateDeps(t)
		d.Rename = func(string, string) error { return errors.New("disk on fire") }
		if err := Write(d, mustNew(t, "correct-horse", 4)); err == nil {
			t.Fatal("Write() error = nil, want the rename failure")
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 0 {
			t.Errorf("state dir holds %d entries after a failed save, want 0", len(entries))
		}
	})
	t.Run("invalid record refused", func(t *testing.T) {
		d, path := stateDeps(t)
		if err := Write(d, Record{}); err == nil {
			t.Fatal("Write(Record{}) error = nil, want a refusal")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an invalid record was written (stat err = %v)", err)
		}
	})
}

// TestRead covers the ways a saved record can be absent or broken. The
// load-bearing rows are the missing file (ErrNoPassword is how the daemon
// knows to keep the page off) and the garbage rows: the error must never
// carry the file's bytes, because it can reach a web page or a log.
func TestRead(t *testing.T) {
	const secret = "s3cr3t-garbage-bytes"
	tests := []struct {
		name     string
		content  *string // nil means no file
		readErr  error   // injected ReadFile failure
		wantNoPw bool
	}{
		{name: "missing file", wantNoPw: true},
		{name: "garbage file", content: ptr("not json " + secret)},
		{name: "empty file", content: ptr("")},
		{name: "unknown algorithm", content: ptr(`{"algorithm":"` + secret + `","iterations":1000,"salt":"AAAAAAAAAAAAAAAAAAAAAA==","key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)},
		{name: "zero iterations", content: ptr(`{"algorithm":"pbkdf2-sha256","iterations":0,"salt":"AAAAAAAAAAAAAAAAAAAAAA==","key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)},
		{name: "unreadable file", readErr: os.ErrPermission},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, path := stateDeps(t)
			if tt.content != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.readErr != nil {
				d.ReadFile = func(string) ([]byte, error) { return nil, tt.readErr }
			}
			_, err := Read(d)
			if err == nil {
				t.Fatal("Read() error = nil, want an error")
			}
			t.Logf("Read() error: %v", err)
			if got := errors.Is(err, ErrNoPassword); got != tt.wantNoPw {
				t.Errorf("errors.Is(err, ErrNoPassword) = %v, want %v", got, tt.wantNoPw)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Read() error %q quotes the file's contents", err)
			}
			if Exists(d) {
				t.Error("Exists() = true for a missing or broken record")
			}
		})
	}
}

// TestZeroAndPartialDeps pins that the file functions fill nil fields
// themselves. Both rows are load-bearing: the daemon's test rig passes only
// Getenv and HomeDir, and daemon_web_test.go passes a zero usagestate.Deps;
// a nil func there would panic. The zero row reaches the real environment,
// so XDG_STATE_HOME is pointed at a temp folder first to keep it hermetic.
func TestZeroAndPartialDeps(t *testing.T) {
	t.Run("zero deps", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("XDG_STATE_HOME", root)
		var d usagestate.Deps
		if Exists(d) {
			t.Fatal("Exists() = true before any save")
		}
		if _, err := Read(d); !errors.Is(err, ErrNoPassword) {
			t.Fatalf("Read() error = %v, want ErrNoPassword", err)
		}
		if err := Write(d, mustNew(t, "correct-horse", 5)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if !Exists(d) {
			t.Error("Exists() = false after a save")
		}
		if _, err := os.Stat(filepath.Join(root, "afk", FileName)); err != nil {
			t.Errorf("record not under XDG_STATE_HOME: %v", err)
		}
	})
	t.Run("partial deps", func(t *testing.T) {
		d, path := stateDeps(t) // only Getenv and HomeDir set
		if Exists(d) {
			t.Fatal("Exists() = true before any save")
		}
		if err := Write(d, mustNew(t, "correct-horse", 6)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		r, err := Read(d)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if !r.Verify("correct-horse") || !Exists(d) {
			t.Error("the saved record does not read back")
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("record not at %s: %v", path, err)
		}
	})
}

func ptr(s string) *string { return &s }
