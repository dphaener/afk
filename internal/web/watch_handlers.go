package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"

	"github.com/dphaener/afk/internal/screen"
)

// The Watch view's three addresses (T17.14). They are HTML-only, so they sit
// in New beside the other pages and not in routes, which would give them
// /api twins. None of them takes a key, an input body or a size: the view is
// read-only, and the ping's request body, if any, is never read.

// watchFrameCSP is the content policy of the live frame: no load of any kind
// and no script, only inline styles, because Rex's colours are style
// attributes. The agent's screen text is untrusted, so it gets the same
// policy as the saved screen (sessionScreen), and the page holds it in a
// sandboxed iframe on top of this.
const watchFrameCSP = "default-src 'none'; style-src 'unsafe-inline'"

// watchFrameMeta is the policy again as the first line of the frame's
// document. The frame is loaded from srcdoc, which has no response headers
// of its own, so the policy has to travel inside the document; it must stay
// the same text as watch.js's WATCH_CSP.
const watchFrameMeta = `<meta http-equiv="Content-Security-Policy" content="` + watchFrameCSP + `">`

// watchFragment is a reading's HTML with the palette in front, as a <style>.
// Rex writes its colours as var(--vt-palette-N) and the frame has no other
// definition of them, so without this every coloured cell shows as plain
// text. It is added here, once, for both the page's first frame and the live
// fragment; the saved screen carries its own copy through screen.Document.
func watchFragment(html []byte) string {
	return "<style>" + screen.Palette() + "</style>" + string(html)
}

// WatchView is the Watch page.
type WatchView struct {
	Project string
	Name    string
	// HasFrame says the page draws the frame and loads the live script. It
	// is false when there is no screen to follow.
	HasFrame bool
	// Frame is the frame's first document (srcdoc), or "".
	Frame string
	// Note is the plain line shown when there is no frame, or a reading
	// failed; "" for none.
	Note string
	// Back is the session's own screen.
	Back string
}

// watcherID names a watching browser without a cookie or a URL part: the
// peer address and browser name, hashed. Two tabs of one browser count as
// one watcher, which is all the lease needs (it only asks whether anyone
// watches). The hash keeps the address out of the registry.
func watcherID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	sum := sha256.Sum256([]byte(host + "\x00" + r.UserAgent()))
	return hex.EncodeToString(sum[:8])
}

// watchSession finds the project and the session the address names and
// answers 404 itself when either is missing, so each route starts alike.
func (s *server) watchSession(w http.ResponseWriter, r *http.Request) (Project, string, bool) {
	p, ok := s.findProject(w, r)
	if !ok {
		return p, "", false
	}
	name := r.PathValue("name")
	if screenNameRe.MatchString(name) {
		for _, sv := range s.d.Sessions(p.Root) {
			if sv.Name == name {
				return p, name, true
			}
		}
	}
	s.noSuchSession(w, r)
	return p, "", false
}

// watchSentence is the plain line for a failed screen read.
func watchSentence(err error) string {
	switch {
	case errors.Is(err, ErrWatchNotRex):
		return "This session does not run in Rex, so there is no screen to watch."
	case errors.Is(err, ErrWatchGone):
		return "The agent's screen is gone: its Rex block has closed, most likely because the session stopped. Reload to see the latest."
	}
	return "The agent's screen could not be read just now. Reload to see the latest."
}

// watchPage is GET /p/{project}/sessions/{name}/watch. A page load is a
// watcher, so it renews the lease at once and reads the screen once for the
// first frame; the live reader takes over from the next two seconds on.
func (s *server) watchPage(w http.ResponseWriter, r *http.Request) {
	p, name, ok := s.watchSession(w, r)
	if !ok {
		return
	}
	v := WatchView{Project: p.Name, Name: name, Back: sessionBase(p.Name, name)}
	if s.d.WatchScreen == nil {
		v.Note = "This page cannot show the agent's screen live. Reload to see the latest."
		s.respond(w, r, http.StatusOK, "watch", v)
		return
	}
	s.watch.Touch(watchKey(p.Name, name), watcherID(r), s.d.Now())
	ctx, cancel := context.WithTimeout(r.Context(), watchReadLimit)
	defer cancel()
	reading, err := s.d.WatchScreen(ctx, p.Root, name, watchBound)
	switch {
	case err == nil:
		v.HasFrame, v.Frame = true, watchFrameMeta+watchFragment(reading.HTML)
	case errors.Is(err, ErrWatchNotRex), errors.Is(err, ErrWatchGone):
		v.Note = watchSentence(err)
	default:
		// A read that failed may work next time, so the frame stays and the
		// live script can fill it in.
		v.HasFrame, v.Note = true, watchSentence(err)
	}
	s.respond(w, r, http.StatusOK, "watch", v)
}

// watchScreen is GET .../watch/screen: the fragment the live script puts in
// the frame. It serves the live reader's last good reading when there is one,
// so a change event costs no second Rex call, and reads once itself
// otherwise. The CSP header is the frame's own policy; the fragment is
// agent text.
func (s *server) watchScreen(w http.ResponseWriter, r *http.Request) {
	p, name, ok := s.watchSession(w, r)
	if !ok {
		return
	}
	if s.d.WatchScreen == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "This page cannot show the agent's screen. Reload the page to see the latest.")
		return
	}
	reading, have := s.hub.watchFrame(p.Name, name)
	if !have {
		ctx, cancel := context.WithTimeout(r.Context(), watchReadLimit)
		defer cancel()
		var err error
		reading, err = s.d.WatchScreen(ctx, p.Root, name, watchBound)
		if err != nil {
			status := http.StatusBadGateway
			switch {
			case errors.Is(err, ErrWatchNotRex):
				status = http.StatusNotFound
			case errors.Is(err, ErrWatchGone):
				status = http.StatusGone
			}
			s.fail(w, r, status, watchSentence(err))
			return
		}
	}
	h := w.Header()
	h.Set("Content-Security-Policy", watchFrameCSP)
	h.Set("Content-Type", "text/html; charset=utf-8")
	if reading.Slow {
		h.Set("X-Watch-Slow", "1")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(watchFragment(reading.HTML)))
}

// watchPing is POST .../watch/ping: it renews this browser's lease and
// answers 204. The body is not read.
func (s *server) watchPing(w http.ResponseWriter, r *http.Request) {
	p, name, ok := s.watchSession(w, r)
	if !ok {
		return
	}
	s.watch.Touch(watchKey(p.Name, name), watcherID(r), s.d.Now())
	w.WriteHeader(http.StatusNoContent)
}

// watchPingWrongMethod answers any other method on the ping address with 405
// and the one method it takes, rather than the catch-all's 404.
func (s *server) watchPingWrongMethod(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	s.fail(w, r, http.StatusMethodNotAllowed, "The watch ping only takes a POST, which the Watch page sends by itself. Open the Watch page instead.")
}

// watchAddress is the page address of a session's Watch view.
func watchAddress(project, name string) string {
	return sessionBase(project, name) + "/watch"
}
