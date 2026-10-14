package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticFiles holds the page's own script (and room for a stylesheet) inside
// the binary, so the page needs no files next to it, no build step and no
// outside address. Only files in the static folder are reachable: the
// handler looks names up in a map built from this folder, never in a path
// the request supplies.
//
//go:embed static
var staticFiles embed.FS

// asset is one embedded file, ready to serve.
type asset struct {
	body []byte
	// hash is the first 12 hex characters of the body's SHA-256. It is the
	// middle of the address, so a changed file gets a new address and a
	// browser may keep the old one for a year.
	hash string
	typ  string
}

// assetTypes are the content types served, by extension. A file with another
// extension is not served, so an embedded file of a type nobody planned for
// cannot be sniffed into something else.
var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

// loadAssets reads and hashes every embedded static file once, at New.
func loadAssets() (map[string]asset, error) {
	out := map[string]asset{}
	err := fs.WalkDir(staticFiles, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		typ, ok := assetTypes[path.Ext(p)]
		if !ok {
			return nil
		}
		b, err := staticFiles.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(p, "static/")] = asset{body: b, hash: hex.EncodeToString(sum[:])[:12], typ: typ}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the web page's static files are damaged: %w", err)
	}
	return out, nil
}

// assetURL is the address of a static file, hash included. An unknown name
// gives an address that answers 404, so a typo in a template shows up in
// the page test instead of breaking quietly.
func (s *server) assetURL(name string) string {
	return "/static/" + s.assets[name].hash + "/" + name
}

// static serves one embedded file. A name the binary does not hold, or a hash
// that is not the file's current one, is a 404 with no-store: an immutable
// answer is only right for the exact address that names the content, and a
// stale address cached as a miss for a year would hide the new file.
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assets[r.PathValue("file")]
	if !ok || a.hash != r.PathValue("hash") {
		s.notFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.typ)
	// guard set no-store for every answer; this is the one answer that
	// overrides it.
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(a.body)
}
