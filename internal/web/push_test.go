package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/webpush"
)

// pushCall is one call of the fake PushSend.
type pushCall struct {
	sub webpush.Subscription
	p   webpush.Payload
	o   webpush.Options
}

// pushFake stands in for web push: dir is the rig's PushDir (a temp folder,
// never the real state folder) and send records every call of PushSend and
// answers gone and err. It never reaches a push service.
type pushFake struct {
	dir   string
	calls []pushCall
	gone  bool
	err   error
}

func (f *pushFake) send(_ context.Context, sub webpush.Subscription, p webpush.Payload, o webpush.Options) (bool, error) {
	f.calls = append(f.calls, pushCall{sub: sub, p: p, o: o})
	return f.gone, f.err
}

// withPush turns push on for the rig. newRig builds the server with push off
// (no PushDir, no PushSend), as most tests want; the handlers read s.d on
// every request, so setting the two seams on the built server before the
// first request is the same as building it with them.
func (rg *rig) withPush() *pushFake {
	f := &pushFake{dir: rg.t.TempDir()}
	rg.srv.d.PushDir = f.dir
	rg.srv.d.PushSend = f.send
	return f
}

// devicesWritten reports whether push-devices.json exists in the fake's
// folder, so a refused row can prove nothing was written at all (an empty
// list read back would also pass for a file written with no devices).
func (f *pushFake) devicesWritten() bool {
	_, err := os.Stat(filepath.Join(f.dir, webpush.DevicesFileName))
	return err == nil
}

// seed signs a device up straight through webpush, as an earlier visit of
// the page would have.
func (f *pushFake) seed(t *testing.T, endpoint, label string) {
	t.Helper()
	_, err := webpush.AddDevice(f.dir, webpush.Subscription{Endpoint: endpoint, P256dh: "p256dh-" + label, Auth: "auth-" + label}, label, testNow, nil)
	must(t, err)
}

// The fake devices' endpoints. They are never dialled: PushSend is the fake.
const (
	phoneEndpoint  = "https://push.example.com/send/phone-0123456789"
	laptopEndpoint = "https://push.example.com/send/laptop-9876543210"
	// androidChrome is a User-Agent webpush.Label names "Chrome on Android".
	androidChrome = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36"
)

// phoneSubscription is the phone's PushSubscription.toJSON(), as the page's
// script posts it.
const phoneSubscription = `{"endpoint":"` + phoneEndpoint + `","expirationTime":null,"keys":{"p256dh":"BPhoneKey","auth":"phoneAuth"}}`

// pushReq is one request to a push address. The cookie is the right one
// unless noCookie is set; origin, when set, is the Origin header.
type pushReq struct {
	method, path, body string
	ua                 string
	noCookie           bool
	origin             string
}

func (p pushReq) send(h http.Handler) *httptest.ResponseRecorder {
	method := p.method
	if method == "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, p.path, strings.NewReader(p.body))
	if p.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.ua != "" {
		req.Header.Set("User-Agent", p.ua)
	}
	if !p.noCookie {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
	}
	if p.origin != "" {
		req.Header.Set("Origin", p.origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPushEndpoints covers the Notifications page's server side: the page's
// view and the three JSON POSTs, each against a temp PushDir and a fake
// PushSend.
//
// Load-bearing rows:
//   - "test sends to that device only": with two devices signed up, one test
//     press makes exactly one send, to the device it names, with the test
//     payload. Sending to every device would wake the owner's other devices,
//     and sending to an address the request made up would let the page sign
//     pushes for strangers.
//   - the refused rows ("... with no cookie", "... from another site"): a
//     stranger who can reach the port must neither sign a device up (and so
//     receive the owner's notices) nor make afk send anything; nothing is
//     written and nothing is sent.
//   - "send error ..." keeps the device and does not echo its endpoint, the
//     device's capability URL, even when the sender's error holds it.
func TestPushEndpoints(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *pushFake)
		req   pushReq
		want  int
		check func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder)
	}{
		{
			name: "subscribe adds a device",
			req:  pushReq{path: "/api/push/subscribe", body: phoneSubscription, ua: androidChrome},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				devs := webpush.ReadDevices(f.dir)
				if len(devs) != 1 {
					t.Fatalf("devices = %+v, want one", devs)
				}
				d := devs[0]
				if d.Endpoint != phoneEndpoint || d.P256dh != "BPhoneKey" || d.Auth != "phoneAuth" || d.Label != "Chrome on Android" {
					t.Errorf("device = %+v, want the phone's subscription labelled Chrome on Android", d)
				}
				if got := decode(t, rec)["id"]; got != d.ID {
					t.Errorf("id = %v, want the stored %q", got, d.ID)
				}
			},
		},
		{
			name:  "subscribe again keeps one device",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "Old label") },
			req:   pushReq{path: "/api/push/subscribe", body: phoneSubscription, ua: androidChrome},
			want:  http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if devs := webpush.ReadDevices(f.dir); len(devs) != 1 || devs[0].Label != "Chrome on Android" {
					t.Errorf("devices = %+v, want the one phone with its new label", devs)
				}
			},
		},
		{
			name: "subscribe with no keys is 400 and writes nothing",
			req:  pushReq{path: "/api/push/subscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusBadRequest,
		},
		{
			name: "subscribe with no endpoint is 400 and writes nothing",
			req:  pushReq{path: "/api/push/subscribe", body: `{"keys":{"p256dh":"k","auth":"a"}}`},
			want: http.StatusBadRequest,
		},
		{
			name: "subscribe with a body that is not JSON is 400 and writes nothing",
			req:  pushReq{path: "/api/push/subscribe", body: `endpoint=` + phoneEndpoint},
			want: http.StatusBadRequest,
		},
		{
			name: "subscribe with a plain http endpoint is refused by the device list",
			req:  pushReq{path: "/api/push/subscribe", body: `{"endpoint":"http://push.example.com/x","keys":{"p256dh":"k","auth":"a"}}`},
			want: http.StatusBadRequest,
		},
		{
			name: "unsubscribe removes the device",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
				f.seed(t, laptopEndpoint, "laptop")
			},
			req:  pushReq{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["removed"]; got != true {
					t.Errorf("removed = %v, want true", got)
				}
				if devs := webpush.ReadDevices(f.dir); len(devs) != 1 || devs[0].Endpoint != laptopEndpoint {
					t.Errorf("devices = %+v, want only the laptop", devs)
				}
			},
		},
		{
			name: "unsubscribe an unknown endpoint is 200 removed false",
			req:  pushReq{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["removed"]; got != false {
					t.Errorf("removed = %v, want false", got)
				}
			},
		},
		{
			name: "unsubscribe with no endpoint is 400",
			req:  pushReq{path: "/api/push/unsubscribe", body: `{}`},
			want: http.StatusBadRequest,
		},
		{
			name: "test sends to that device only",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
				f.seed(t, laptopEndpoint, "laptop")
			},
			req:  pushReq{path: "/api/push/test", body: `{"endpoint":"` + laptopEndpoint + `"}`},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["outcome"]; got != "sent" {
					t.Errorf("outcome = %v, want sent", got)
				}
				if len(f.calls) != 1 {
					t.Fatalf("PushSend ran %d times, want exactly once", len(f.calls))
				}
				c := f.calls[0]
				if c.sub != (webpush.Subscription{Endpoint: laptopEndpoint, P256dh: "p256dh-laptop", Auth: "auth-laptop"}) {
					t.Errorf("sent to %+v, want the laptop's stored subscription", c.sub)
				}
				wantP := webpush.Payload{Title: "afk", Body: "Test notification. If you can read this, notifications from afk reach this device.", URL: "/notifications", Tag: "afk-test"}
				if c.p != wantP {
					t.Errorf("payload = %+v, want %+v", c.p, wantP)
				}
				if c.o.TTL.Seconds() != 60 || c.o.Urgency != "normal" {
					t.Errorf("options = %+v, want a one-minute TTL and normal urgency", c.o)
				}
				if len(webpush.ReadDevices(f.dir)) != 2 {
					t.Errorf("a sent test changed the device list")
				}
			},
		},
		{
			name:  "test for an unknown endpoint is 404 and sends nothing",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/test", body: `{"endpoint":"` + laptopEndpoint + `"}`},
			want:  http.StatusNotFound,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["error"]; got != "This device is not signed up for notifications. Turn them on first." {
					t.Errorf("error = %v", got)
				}
			},
		},
		{
			name: "test with a gone answer removes the device and is 410",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
				f.seed(t, laptopEndpoint, "laptop")
				f.gone = true
			},
			req:  pushReq{path: "/api/push/test", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusGone,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["error"]; got != "The push service says this device is no longer signed up. Turn notifications on again." {
					t.Errorf("error = %v", got)
				}
				if devs := webpush.ReadDevices(f.dir); len(devs) != 1 || devs[0].Endpoint != laptopEndpoint {
					t.Errorf("devices = %+v, want only the laptop left", devs)
				}
			},
		},
		{
			name: "send error is 502, keeps the device and does not echo the endpoint",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
				f.err = errors.New("push service answered 503 for " + phoneEndpoint)
			},
			req:  pushReq{path: "/api/push/test", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusBadGateway,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				msg, _ := decode(t, rec)["error"].(string)
				if !strings.HasPrefix(msg, "The test could not be sent: push service answered 503") || !strings.HasSuffix(msg, "Try again in a minute.") {
					t.Errorf("error = %q", msg)
				}
				if len(webpush.ReadDevices(f.dir)) != 1 {
					t.Errorf("a failed send removed the device")
				}
			},
		},
		{
			name: "subscribe with no cookie is 401",
			req:  pushReq{path: "/api/push/subscribe", body: phoneSubscription, noCookie: true},
			want: http.StatusUnauthorized,
		},
		{
			name:  "test with no cookie is 401",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/test", body: `{"endpoint":"` + phoneEndpoint + `"}`, noCookie: true},
			want:  http.StatusUnauthorized,
		},
		{
			name:  "unsubscribe with no cookie is 401",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`, noCookie: true},
			want:  http.StatusUnauthorized,
		},
		{
			name: "subscribe from another site is 403",
			req:  pushReq{path: "/api/push/subscribe", body: phoneSubscription, origin: "https://evil.example"},
			want: http.StatusForbidden,
		},
		{
			name:  "test from another site is 403",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/test", body: `{"endpoint":"` + phoneEndpoint + `"}`, origin: "https://evil.example"},
			want:  http.StatusForbidden,
		},
		{
			name:  "unsubscribe from another site is 403",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`, origin: "https://evil.example"},
			want:  http.StatusForbidden,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			f := rg.withPush()
			if tc.setup != nil {
				tc.setup(t, f)
			}
			before := webpush.ReadDevices(f.dir)
			seeded := f.devicesWritten()
			rec := tc.req.send(rg.h)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "push.example.com") {
				t.Errorf("the answer echoes an endpoint: %s", rec.Body.String())
			}
			if tc.want != http.StatusOK && tc.want != http.StatusGone {
				// Every refusal and failure leaves the list as it was: not
				// written at all when nothing was seeded, unchanged when it
				// was.
				if !seeded && f.devicesWritten() {
					t.Errorf("a refused request wrote push-devices.json")
				}
				if after := webpush.ReadDevices(f.dir); len(after) != len(before) {
					t.Errorf("devices went from %d to %d on a refused request", len(before), len(after))
				}
				if _, ok := decode(t, rec)["error"]; !ok {
					t.Errorf("body = %s, want {\"error\": ...}", rec.Body.String())
				}
			}
			// Only a test that reached a listed device may send, and then
			// once; every other row sends nothing.
			wantCalls := 0
			if strings.HasSuffix(tc.req.path, "/test") && (tc.want == http.StatusOK || tc.want == http.StatusGone || tc.want == http.StatusBadGateway) {
				wantCalls = 1
			}
			if len(f.calls) != wantCalls {
				t.Errorf("PushSend ran %d times, want %d", len(f.calls), wantCalls)
			}
			if tc.check != nil {
				tc.check(t, f, rec)
			}
		})
	}
}

// TestPushOff covers a server built without push: every push address is not
// found, so a page or a script cannot tell a half-wired push from none, and
// nothing is written or sent. Load-bearing: the PushSend-nil rows, since a
// server with a folder but no sender would otherwise promise a test it can
// never send.
func TestPushOff(t *testing.T) {
	addrs := []pushReq{
		{method: http.MethodGet, path: "/notifications"},
		{method: http.MethodGet, path: "/api/notifications"},
		{path: "/api/push/subscribe", body: phoneSubscription},
		{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`},
		{path: "/api/push/test", body: `{"endpoint":"` + phoneEndpoint + `"}`},
	}
	ways := []struct {
		name string
		set  func(rg *rig) string
	}{
		{"no PushDir", func(rg *rig) string {
			f := rg.withPush()
			rg.srv.d.PushDir = ""
			return f.dir
		}},
		{"no PushSend", func(rg *rig) string {
			f := rg.withPush()
			rg.srv.d.PushSend = nil
			return f.dir
		}},
	}
	for _, way := range ways {
		for _, a := range addrs {
			t.Run(way.name+" "+a.path, func(t *testing.T) {
				rg := newRig(t, Config{})
				dir := way.set(rg)
				rec := a.send(rg.h)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404\n%s", rec.Code, rec.Body.String())
				}
				entries, err := os.ReadDir(dir)
				must(t, err)
				if len(entries) != 0 {
					t.Errorf("push off wrote %d files", len(entries))
				}
			})
		}
	}
}

// TestNotificationsView covers GET /notifications and its JSON twin.
// Load-bearing rows: "carries the public key" (the browser cannot subscribe
// without it, and it must be the key the sender signs with, read back from
// the same file) and "damaged key" (a 200 with the file named, never a 500,
// and the file left exactly as it was).
func TestNotificationsView(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *pushFake)
		path  string
		check func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder)
	}{
		{
			name: "carries the public key and this browser's label",
			path: "/api/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				m := decode(t, rec)
				key, err := webpush.LoadOrCreateKey(f.dir, nil, testNow)
				must(t, err)
				if m["public_key"] != key.PublicKey() || key.PublicKey() == "" {
					t.Errorf("public_key = %v, want the stored %q", m["public_key"], key.PublicKey())
				}
				if m["label"] != "Chrome on Android" || m["key_error"] != "" {
					t.Errorf("view = %s, want label Chrome on Android and no key error", dump(m))
				}
			},
		},
		{
			name: "damaged key is a plain error naming the file",
			setup: func(t *testing.T, f *pushFake) {
				must(t, os.WriteFile(filepath.Join(f.dir, webpush.KeyFileName), []byte("not a key"), 0o600))
			},
			path: "/api/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				m := decode(t, rec)
				msg, _ := m["key_error"].(string)
				if !strings.HasPrefix(msg, "Notifications cannot be turned on") || !strings.Contains(msg, filepath.Join(f.dir, webpush.KeyFileName)) {
					t.Errorf("key_error = %q, want a sentence naming the key file", msg)
				}
				if m["public_key"] != "" {
					t.Errorf("public_key = %v, want none", m["public_key"])
				}
				b, err := os.ReadFile(filepath.Join(f.dir, webpush.KeyFileName))
				must(t, err)
				if string(b) != "not a key" {
					t.Errorf("the damaged key file was replaced")
				}
			},
		},
		{
			name: "the page answers",
			path: "/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("Content-Type = %q, want HTML", ct)
				}
				if !strings.Contains(rec.Body.String(), "<h1>Notifications</h1>") {
					t.Errorf("the page has no Notifications heading:\n%s", rec.Body.String())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			f := rg.withPush()
			if tc.setup != nil {
				tc.setup(t, f)
			}
			rec := pushReq{method: http.MethodGet, path: tc.path, ua: androidChrome}.send(rg.h)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			if len(f.calls) != 0 {
				t.Errorf("opening the page sent %d pushes", len(f.calls))
			}
			tc.check(t, f, rec)
		})
	}
}
