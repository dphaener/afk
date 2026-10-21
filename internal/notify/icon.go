package notify

import (
	_ "embed"
	"os"
	"path/filepath"
)

// appIcon is the owner's app icon, the same picture the web page installs with.
// It is a byte-for-byte copy of internal/web/static/icons/icon-512.png: go:embed
// cannot reach a sibling package's folder, and this package cannot import
// internal/web (which depends on it through internal/probe). A test compares the
// two files so the copies cannot drift.
//
//go:embed icon-512.png
var appIcon []byte

// IconPath extracts the embedded app icon to the user cache dir and returns
// its path, or "" on any failure. The icon is embedded (rather than repo-relative)
// so it survives `make install` moving the binary to ~/bin away from the repo.
// The cached file is named icon-512.png, not the name of the earlier icon, so the
// os.Stat skip below can never reuse a cached old icon.
// Returning "" rather than an error on failure is load-bearing: --app-icon is
// private-API dependent per alerter's own help, so a missing icon must silently
// omit the flag rather than failing the toast.
func IconPath() string {
	return iconPathWith(os.UserCacheDir)
}

func iconPathWith(getCacheDir func() (string, error)) string {
	cacheDir, err := getCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(cacheDir, "afk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, "icon-512.png")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.WriteFile(path, appIcon, 0o644); err != nil {
		return ""
	}
	return path
}
