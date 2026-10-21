package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

// iconAddrs is every file in static/icons/ by name, with the address its own
// bytes earn. It is computed from the embedded files here, never read back
// from the server, so a wrong hash in the server cannot pass.
func iconAddrs(t *testing.T) map[string]string {
	t.Helper()
	entries, err := fs.ReadDir(staticFiles, "static/icons")
	if err != nil || len(entries) == 0 {
		t.Fatalf("the binary holds no icons: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := staticFiles.ReadFile("static/icons/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		out[e.Name()] = "/static/" + hex.EncodeToString(sum[:])[:12] + "/icons/" + e.Name()
	}
	return out
}

// TestPublicAppFiles covers the app files served without a cookie: the
// manifest, every icon at its current address and the service worker. A
// browser fetches each of them without the cookie, so behind the sign-in
// the page could never be installed.
//
// Load-bearing rows: one row per icon and the manifest and /sw.js rows (each
// must answer 200 with its own type, the security policy and no cookie), and
// every refused row: a POST to each of the three kinds, an icon under a
// made-up hash, an icon's name under another file's hash, a ".." address
// that names live.js, live.js itself, a page and /api/ must all still get the
// refusal, so the exemption is exact addresses and never a folder or a
// method other than GET and HEAD.
func TestPublicAppFiles(t *testing.T) {
	rg := newRig(t, Config{})
	icons := iconAddrs(t)
	liveJS := regexp.MustCompile(`/static/[0-9a-f]{12}/live\.js`).FindString(rg.pageDo("GET", "/usage", "").Body.String())
	if liveJS == "" {
		t.Fatalf("the page names no live.js")
	}
	liveHash := strings.Split(liveJS, "/")[2]
	icon512 := icons["icon-512.png"]
	iconHash := strings.Split(icon512, "/")[2]

	type row struct {
		name   string
		method string
		path   string
		status int
		typ    string // the Content-Type of a 200
		json   bool   // a refusal answered as JSON rather than the form
	}
	tests := []row{
		{name: "manifest", method: "GET", path: "/manifest.webmanifest", status: 200, typ: "application/manifest+json"},
		{name: "manifest HEAD", method: "HEAD", path: "/manifest.webmanifest", status: 200, typ: "application/manifest+json"},
		{name: "service worker", method: "GET", path: "/sw.js", status: 200, typ: "text/javascript; charset=utf-8"},
		{name: "service worker HEAD", method: "HEAD", path: "/sw.js", status: 200, typ: "text/javascript; charset=utf-8"},
		{name: "icon HEAD", method: "HEAD", path: icon512, status: 200, typ: "image/png"},
		{name: "POST to the manifest", method: "POST", path: "/manifest.webmanifest", status: 401},
		{name: "POST to the service worker", method: "POST", path: "/sw.js", status: 401},
		{name: "POST to an icon", method: "POST", path: icon512, status: 401},
		{name: "icon under a made-up hash", method: "GET", path: "/static/000000000000/icons/icon-512.png", status: 401},
		{name: "icon under live.js's hash", method: "GET", path: "/static/" + liveHash + "/icons/icon-512.png", status: 401},
		{name: "unknown icon under an icon's hash", method: "GET", path: "/static/" + iconHash + "/icons/nope.png", status: 401},
		{name: "dot-dot to live.js", method: "GET", path: "/static/" + iconHash + "/icons/../live.js", status: 401},
		{name: "live.js is not exempt", method: "GET", path: liveJS, status: 401},
		{name: "the service worker's hashed address is not exempt", method: "GET", path: "/static/" + strings.Split(liveJS, "/")[2] + "/sw.js", status: 401},
		{name: "a page", method: "GET", path: "/usage", status: 401},
		{name: "the API", method: "GET", path: "/api/", status: 401, json: true},
		{name: "no API twin of the manifest", method: "GET", path: "/api/manifest.webmanifest", status: 401, json: true},
	}
	for name, addr := range icons {
		typ := "image/png"
		if path.Ext(name) == ".svg" {
			typ = "image/svg+xml"
		}
		tests = append(tests, row{name: "icon " + name, method: "GET", path: addr, status: 200, typ: typ})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tt.status, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
			}
			if setCookie(rec) != nil {
				t.Errorf("a request without a cookie was handed a cookie")
			}
			body := rec.Body.String()
			if tt.status != 200 {
				switch {
				case tt.json:
					if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
						t.Errorf("Content-Type = %q, want JSON", rec.Header().Get("Content-Type"))
					}
				default:
					if !strings.Contains(body, "This page is private") {
						t.Errorf("the refusal is not the sign-in form:\n%s", body)
					}
				}
				return
			}
			if got := rec.Header().Get("Content-Type"); got != tt.typ {
				t.Errorf("Content-Type = %q, want %q", got, tt.typ)
			}
			if tt.method == "GET" && len(body) == 0 {
				t.Errorf("the answer has no body")
			}
		})
	}
}

// TestPublicAppFilesCache pins the cache header of each kind of app file.
// Load-bearing: the manifest and /sw.js keep the guard's no-store (their
// address never changes, so a cached copy would hide a new one; browsers
// check a worker for updates on their own), while an icon is immutable like
// every other hashed file.
func TestPublicAppFilesCache(t *testing.T) {
	rg := newRig(t, Config{})
	tests := []struct {
		name  string
		path  string
		cache string
	}{
		{name: "manifest", path: "/manifest.webmanifest", cache: "no-store"},
		{name: "service worker", path: "/sw.js", cache: "no-store"},
		{name: "icon", path: iconAddrs(t)["icon-192.png"], cache: "public, max-age=31536000, immutable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
		})
	}
}

// TestManifest reads the manifest as a browser does, signed out.
//
// Load-bearing rows: "icons" (every file in static/icons/ is listed, read
// with fs.ReadDir so an icon added later is checked without a new row, each
// at an address that answers 200 signed out, with the size its own header
// gives), "purpose" (only the maskable icon is maskable: a plain icon marked
// maskable is cropped by the phone's mask) and the fixed fields, which are
// what makes Chrome call the page installable.
func TestManifest(t *testing.T) {
	rg := newRig(t, Config{})
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var m struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		ThemeColor      string `json:"theme_color"`
		BackgroundColor string `json:"background_color"`
		Icons           []struct {
			Src     string `json:"src"`
			Type    string `json:"type"`
			Sizes   string `json:"sizes"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("the manifest is not JSON: %v\n%s", err, rec.Body.String())
	}

	fields := []struct{ name, got, want string }{
		{"name", m.Name, "afk"},
		{"short_name", m.ShortName, "afk"},
		{"start_url", m.StartURL, "/"},
		{"scope", m.Scope, "/"},
		{"display", m.Display, "standalone"},
		{"theme_color", m.ThemeColor, "#1C1B18"},
		{"background_color", m.BackgroundColor, "#1C1B18"},
	}
	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			if f.got != f.want {
				t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
			}
		})
	}

	wantSizes := map[string]string{
		"icon-512.png":          "512x512",
		"icon-maskable-512.png": "512x512",
		"icon-192.png":          "192x192",
		"apple-touch-icon.png":  "180x180",
		"favicon-32.png":        "32x32",
		"favicon.svg":           "any",
	}
	icons := iconAddrs(t)
	t.Run("icons", func(t *testing.T) {
		if len(m.Icons) != len(icons) {
			t.Errorf("the manifest lists %d icons, the folder holds %d", len(m.Icons), len(icons))
		}
		seen := map[string]bool{}
		for _, ic := range m.Icons {
			name := path.Base(ic.Src)
			seen[name] = true
			if ic.Src != icons[name] {
				t.Errorf("%s: src = %q, want %q", name, ic.Src, icons[name])
			}
			if want, ok := wantSizes[name]; ok && ic.Sizes != want {
				t.Errorf("%s: sizes = %q, want %q", name, ic.Sizes, want)
			}
			wantType := "image/png"
			if path.Ext(name) == ".svg" {
				wantType = "image/svg+xml"
			}
			if ic.Type != wantType {
				t.Errorf("%s: type = %q, want %q", name, ic.Type, wantType)
			}
			got := httptest.NewRecorder()
			rg.h.ServeHTTP(got, httptest.NewRequest("GET", ic.Src, nil))
			if got.Code != 200 {
				t.Errorf("%s: %s answers %d signed out", name, ic.Src, got.Code)
			}
		}
		for name := range icons {
			if !seen[name] {
				t.Errorf("the manifest does not list %s", name)
			}
		}
	})
	t.Run("purpose", func(t *testing.T) {
		for _, ic := range m.Icons {
			want := "any"
			if path.Base(ic.Src) == "icon-maskable-512.png" {
				want = "maskable"
			}
			if ic.Purpose != want {
				t.Errorf("%s: purpose = %q, want %q", ic.Src, ic.Purpose, want)
			}
		}
	})
}

// TestPageTemplatesAppHead checks the head tags that make the page an app.
//
// Load-bearing rows: "sign-in form" (a browser without the cookie must still
// find the manifest and icons, or it never offers to install), "signed-in
// page" (the same tags, plus the worker registration) and "worker has no
// fetch listener" (a fetch handler would serve stale copies of live pages;
// the worker exists only so T15.5 can add push). The sign-in form must not
// load sw-register.js: it is the bare page and loads no script at all.
func TestPageTemplatesAppHead(t *testing.T) {
	rg := newRig(t, Config{})
	icons := iconAddrs(t)
	head := []string{
		`<link rel="icon" href="` + icons["favicon.svg"] + `" type="image/svg+xml">`,
		`<link rel="icon" href="` + icons["favicon-32.png"] + `" sizes="32x32" type="image/png">`,
		`<link rel="manifest" href="/manifest.webmanifest">`,
		`<link rel="apple-touch-icon" href="` + icons["apple-touch-icon.png"] + `">`,
		`<meta name="theme-color" content="#F6F5F1" media="(prefers-color-scheme: light)">`,
		`<meta name="theme-color" content="#141412" media="(prefers-color-scheme: dark)">`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-title" content="afk">`,
		`<meta name="apple-mobile-web-app-status-bar-style" content="default">`,
	}
	register := regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/sw-register\.js" defer></script>`)
	signedOut := httptest.NewRecorder()
	rg.h.ServeHTTP(signedOut, httptest.NewRequest("GET", "/", nil))

	tests := []struct {
		name     string
		body     string
		register bool
	}{
		{name: "signed-in page", body: rg.pageDo("GET", "/", "").Body.String(), register: true},
		// The error page is bare like the form: it loads no script, so it
		// registers no worker, but it still carries the icons.
		{name: "signed-in error page", body: rg.pageDo("GET", "/nope", "").Body.String(), register: false},
		{name: "sign-in form", body: signedOut.Body.String(), register: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, want := range head {
				if !strings.Contains(tt.body, want) {
					t.Errorf("the head lacks %s", want)
				}
			}
			if got := register.MatchString(tt.body); got != tt.register {
				t.Errorf("loads sw-register.js = %v, want %v", got, tt.register)
			}
		})
	}
	t.Run("worker has no fetch listener", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))
		body := rec.Body.String()
		if !strings.Contains(body, "'install'") || !strings.Contains(body, "'activate'") {
			t.Errorf("/sw.js is not the worker:\n%s", body)
		}
		if strings.Contains(body, "'fetch'") || strings.Contains(body, `"fetch"`) || strings.Contains(body, "caches") {
			t.Errorf("/sw.js handles fetches or caches:\n%s", body)
		}
	})
	t.Run("the register script registers /sw.js at the root", func(t *testing.T) {
		js, err := staticFiles.ReadFile("static/sw-register.js")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`'serviceWorker' in navigator`, `register('/sw.js', { scope: '/' })`, `.catch(function () {})`} {
			if !strings.Contains(string(js), want) {
				t.Errorf("sw-register.js lacks %s", want)
			}
		}
	})
}

// TestServiceWorkerIcon covers the icon address written into /sw.js as it is
// served. The worker cannot know the icon's hashed address, so the server
// fills AFK_ICON in.
//
// Load-bearing rows: "icon present" (the address is the one the icon's own
// bytes earn, computed here and not read back from the server, and it
// answers without a cookie, as a notification's icon is fetched by the
// worker) and "icon missing" (the placeholder never reaches a browser as an
// address).
func TestServiceWorkerIcon(t *testing.T) {
	tests := []struct {
		name     string
		dropIcon bool
		want     string
	}{
		{name: "icon present", want: "var AFK_ICON = '" + iconAddrs(t)["icon-192.png"] + "';"},
		{name: "icon missing", dropIcon: true, want: "var AFK_ICON = '';"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tt.dropIcon {
				delete(rg.srv.assets, workerIcon)
			}
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("/sw.js lacks %q:\n%s", tt.want, body)
			}
			if strings.Contains(body, workerIconToken) {
				t.Errorf("/sw.js still holds the placeholder %s", workerIconToken)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if tt.dropIcon {
				return
			}
			icon := httptest.NewRecorder()
			rg.h.ServeHTTP(icon, httptest.NewRequest("GET", iconAddrs(t)["icon-192.png"], nil))
			if icon.Code != 200 || icon.Header().Get("Content-Type") != "image/png" {
				t.Errorf("the worker's icon answers %d %q without a cookie", icon.Code, icon.Header().Get("Content-Type"))
			}
		})
	}
}
