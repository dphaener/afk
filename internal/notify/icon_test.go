package notify

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestIconPath verifies the embedded icon extracts to a valid PNG in the cache
// dir. Load-bearing: the path exists, contains valid PNG data (8-byte magic),
// and silent degradation (returns "" rather than erroring) when the cache dir
// is unwritable.
func TestIconPath(t *testing.T) {
	got := IconPath()
	if got == "" {
		t.Skip("IconPath() returned empty, cache dir unavailable in test env")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("IconPath() = %q, not absolute", got)
	}

	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("reading icon: %v", err)
	}

	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if !bytes.HasPrefix(data, pngMagic) {
		t.Errorf("icon file does not have PNG magic bytes")
	}

	if len(data) < 1000 {
		t.Errorf("icon file suspiciously small: %d bytes", len(data))
	}
}

// TestIconPathIdempotent confirms repeated calls return the same path and do
// not fail when the file already exists.
func TestIconPathIdempotent(t *testing.T) {
	first := IconPath()
	if first == "" {
		t.Skip("IconPath() returned empty, cache dir unavailable in test env")
	}
	second := IconPath()
	if second != first {
		t.Errorf("IconPath() not idempotent: first=%q, second=%q", first, second)
	}
}

// TestIconPathDegradation verifies silent failure when cache dir is unwritable.
func TestIconPathDegradation(t *testing.T) {
	tmpDir := t.TempDir()
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o555); err != nil {
		t.Fatalf("creating readonly dir: %v", err)
	}

	getCacheDir := func() (string, error) {
		return readOnlyDir, nil
	}

	got := iconPathWith(getCacheDir)
	if got != "" {
		t.Errorf("iconPathWith() with unwritable cache dir = %q, want empty string for silent degradation", got)
	}
}

// TestAppIconMatchesWebCopy pins the embedded icon to the web page's icon byte
// for byte. Load-bearing: the two copies exist only because an embed cannot
// cross packages, so this is the only thing stopping them drifting apart.
func TestAppIconMatchesWebCopy(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"web icon-512", "../web/static/icons/icon-512.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("reading %s: %v", tt.path, err)
			}
			if !bytes.Equal(appIcon, want) {
				t.Errorf("embedded icon differs from %s", tt.path)
			}
		})
	}
}
