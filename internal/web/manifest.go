package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"net/http"
	"path"
)

// The app's two fixed addresses. A browser finds the manifest through the
// page's <link rel="manifest"> and the worker through sw-register.js, and
// both look again later at the same address, so neither can carry a hash
// the way the other static files do.
const (
	manifestPath      = "/manifest.webmanifest"
	serviceWorkerPath = "/sw.js"
)

// appColor is the light page ground (--bg in docs/design-system.md). The
// manifest has one colour for the splash screen and the title bar, and light
// is the page's default look; the head's theme-color tags add the dark one.
const appColor = "#F6F5F1"

// iconsDir is the embedded folder of the owner's app icons. The manifest
// lists every file in it and the guard lets every one of them through, so
// an icon added there is installed and reachable with no other change.
const iconsDir = "static/icons"

// maskableIcon is the one icon drawn with room around it for a phone's mask.
// Marking any other icon maskable would have its edges cropped.
const maskableIcon = "icon-maskable-512.png"

// webManifest is the web app manifest's fields that afk sets. They are the
// ones Chrome needs to call a page installable (a name, a start address, a
// standalone display and 192 and 512 pixel icons) plus the scope and the
// colours.
type webManifest struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	ThemeColor      string         `json:"theme_color"`
	BackgroundColor string         `json:"background_color"`
	Icons           []manifestIcon `json:"icons"`
}

// manifestIcon is one entry of the manifest's icon list.
type manifestIcon struct {
	Src     string `json:"src"`
	Type    string `json:"type"`
	Sizes   string `json:"sizes"`
	Purpose string `json:"purpose"`
}

// buildManifest makes the manifest from the embedded icon folder. Each icon's
// size is read from its own PNG header rather than written down, so a
// replaced icon can never be listed at a size it is not; an SVG scales, so
// it is "any".
func buildManifest(assetURL func(string) string) ([]byte, error) {
	entries, err := fs.ReadDir(staticFiles, iconsDir)
	if err != nil {
		return nil, fmt.Errorf("the web page's icons are missing: %w", err)
	}
	m := webManifest{
		Name: "afk", ShortName: "afk", StartURL: "/", Scope: "/", Display: "standalone",
		ThemeColor: appColor, BackgroundColor: appColor,
		Icons: []manifestIcon{},
	}
	for _, e := range entries {
		name := e.Name()
		ext := path.Ext(name)
		if e.IsDir() || (ext != ".png" && ext != ".svg") {
			continue
		}
		ic := manifestIcon{Src: assetURL("icons/" + name), Type: assetTypes[ext], Sizes: "any", Purpose: "any"}
		if ext == ".png" {
			b, err := staticFiles.ReadFile(iconsDir + "/" + name)
			if err != nil {
				return nil, fmt.Errorf("the web page's icon %s cannot be read: %w", name, err)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(b))
			if err != nil {
				return nil, fmt.Errorf("the web page's icon %s is not a PNG: %w", name, err)
			}
			ic.Sizes = fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
		}
		if name == maskableIcon {
			ic.Purpose = "maskable"
		}
		m.Icons = append(m.Icons, ic)
	}
	return json.Marshal(m)
}

// manifest serves the web app manifest. It is built on each request, like
// every other answer here, but from the binary's own files only, so it holds
// nothing about any project. A damaged icon is caught by the tests; should
// one slip through, the browser gets a plain error instead of a manifest
// that lies about an icon.
func (s *server) manifest(w http.ResponseWriter, r *http.Request) {
	b, err := buildManifest(s.assetURL)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "The app description could not be made: "+err.Error()+". Install a fresh copy of afk.")
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	_, _ = w.Write(b)
}

// serviceWorker serves static/sw.js at /sw.js. A worker controls only the
// addresses under its own, so it has to be served from the top of the site
// to cover every page; its hashed address under /static/ would cover
// nothing. It keeps the guard's no-store: the address never changes, and a
// stale copy would hold back the push handler T15.5 adds.
func (s *server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assets["sw.js"]
	if !ok {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.typ)
	_, _ = w.Write(a.body)
}
