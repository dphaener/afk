package notify

import (
	_ "embed"
	"os"
	"path/filepath"
)

// pickaxeGray is the embedded icon asset: ⛏ emoji drawn at 400pt into a 512x512
// transparent PNG via Swift/AppKit with NSColor(calibratedWhite: 0.55, alpha: 1.0).
// The gray value is load-bearing: the default near-black emoji render is invisible
// against the notification background; medium gray (0.55) reads on both light and
// dark. Generator parameters recorded here so the asset can be regenerated: see
// /tmp/icon2.swift from W0.T1 for the full Swift source.
//
//go:embed pickaxe.png
var pickaxeGray []byte

// IconPath extracts the embedded pickaxe icon to the user cache dir and returns
// its path, or "" on any failure. The icon is embedded (rather than repo-relative)
// so it survives `make install` moving the binary to ~/bin away from the repo.
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
	path := filepath.Join(dir, "pickaxe.png")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.WriteFile(path, pickaxeGray, 0o644); err != nil {
		return ""
	}
	return path
}
