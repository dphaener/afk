package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

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

// seedAt signs a device up at the given time and returns its id, so a test
// can lay out a list whose order it knows.
func (f *pushFake) seedAt(t *testing.T, endpoint, label string, at time.Time) string {
	t.Helper()
	d, err := webpush.AddDevice(f.dir, webpush.Subscription{Endpoint: endpoint, P256dh: "p256dh-" + label, Auth: "auth-" + label}, label, at, nil)
	must(t, err)
	return d.ID
}

// fixedDeviceID is the id seedFixed gives its device: sixteen zero bytes in
// hex, so a table row can name the device in its address before the rig
// exists.
const fixedDeviceID = "00000000000000000000000000000000"

// seedFixed signs the phone up under fixedDeviceID.
func (f *pushFake) seedFixed(t *testing.T) {
	t.Helper()
	d, err := webpush.AddDevice(f.dir, webpush.Subscription{Endpoint: phoneEndpoint, P256dh: "p256dh-phone", Auth: "auth-phone"}, "Chrome on Android", testNow, bytes.NewReader(make([]byte, 16)))
	must(t, err)
	if d.ID != fixedDeviceID {
		t.Fatalf("seeded id = %q, want %q", d.ID, fixedDeviceID)
	}
}

// idOf is the listed id of the device with this endpoint, or "".
func (f *pushFake) idOf(endpoint string) string {
	for _, d := range webpush.ReadDevices(f.dir) {
		if d.Endpoint == endpoint {
			return d.ID
		}
	}
	return ""
}

// deviceCookieSet is the device cookie the answer sets, or nil when it sets
// none.
func deviceCookieSet(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == deviceCookieName {
			return c
		}
	}
	return nil
}

// checkDeviceCookie fails unless the answer sets the device cookie to id
// with the attributes deviceCookie promises; an empty id means a clearing
// cookie (empty value, expired).
func checkDeviceCookie(t *testing.T, rec *httptest.ResponseRecorder, id string) {
	t.Helper()
	c := deviceCookieSet(rec)
	if c == nil {
		t.Fatalf("no %s cookie set; Set-Cookie = %q", deviceCookieName, rec.Header().Values("Set-Cookie"))
	}
	if c.Value != id {
		t.Errorf("device cookie = %q, want %q", c.Value, id)
	}
	if id == "" && c.MaxAge >= 0 {
		t.Errorf("clearing cookie has Max-Age %d, want it expired", c.MaxAge)
	}
	if id != "" && c.MaxAge <= 0 {
		t.Errorf("device cookie has Max-Age %d, want a lasting cookie", c.MaxAge)
	}
	if c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("device cookie = %+v, want Path=/, HttpOnly, SameSite=Strict", c)
	}
}

// The fake devices' endpoints. They are never dialled: PushSend is the fake.
const (
	phoneEndpoint  = "https://push.example.com/send/phone-0123456789"
	laptopEndpoint = "https://push.example.com/send/laptop-9876543210"
	tabletEndpoint = "https://push.example.com/send/tablet-5555555555"
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
	// device, when set, is the device cookie's value.
	device string
	// form sends the body as a form, the way the confirm page posts it,
	// instead of as JSON.
	form bool
}

func (p pushReq) send(h http.Handler) *httptest.ResponseRecorder {
	method := p.method
	if method == "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, p.path, strings.NewReader(p.body))
	if p.body != "" {
		req.Header.Set("Content-Type", "application/json")
		if p.form {
			req.Header.Set("Content-Type", formType)
		}
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
	if p.device != "" {
		req.AddCookie(&http.Cookie{Name: deviceCookieName, Value: p.device})
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
//   - the device-cookie checks on subscribe, unsubscribe and the two
//     "device lookup" rows: the cookie is the only way the server knows
//     which row is this browser, so a subscribe that did not set it would
//     leave the device unmarked, and a not-listed lookup or an unsubscribe
//     that did not clear it would mark a row the browser no longer is.
//     Every row also checks no header, cookies included, carries an
//     endpoint.
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
				checkDeviceCookie(t, rec, d.ID)
			},
		},
		{
			name:  "subscribe again keeps one device",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "Old label") },
			req:   pushReq{path: "/api/push/subscribe", body: phoneSubscription, ua: androidChrome},
			want:  http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				devs := webpush.ReadDevices(f.dir)
				if len(devs) != 1 || devs[0].Label != "Chrome on Android" {
					t.Fatalf("devices = %+v, want the one phone with its new label", devs)
				}
				// The cookie names the entry kept, whose id is the old one.
				checkDeviceCookie(t, rec, devs[0].ID)
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
				checkDeviceCookie(t, rec, "")
			},
		},
		{
			name: "unsubscribe an unknown endpoint is 200 removed false",
			req:  pushReq{path: "/api/push/unsubscribe", body: `{"endpoint":"` + phoneEndpoint + `"}`, device: "0123abcd"},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := decode(t, rec)["removed"]; got != false {
					t.Errorf("removed = %v, want false", got)
				}
				checkDeviceCookie(t, rec, "")
			},
		},
		{
			name: "device lookup: listed answers its id and sets the cookie",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
				f.seed(t, laptopEndpoint, "laptop")
			},
			req:  pushReq{path: "/api/push/device", body: `{"endpoint":"` + laptopEndpoint + `"}`},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				id := f.idOf(laptopEndpoint)
				m := decode(t, rec)
				if m["listed"] != true || m["id"] != id || len(m) != 2 {
					t.Errorf("answer = %s, want listed true and the laptop's id %q only", dump(m), id)
				}
				checkDeviceCookie(t, rec, id)
				if len(webpush.ReadDevices(f.dir)) != 2 {
					t.Errorf("a lookup changed the device list")
				}
			},
		},
		{
			name:  "device lookup: not listed answers listed false and clears the cookie",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/device", body: `{"endpoint":"` + laptopEndpoint + `"}`, device: "0123abcd"},
			want:  http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if got := strings.TrimSpace(rec.Body.String()); got != `{"listed":false}` {
					t.Errorf("answer = %s, want {\"listed\":false}", got)
				}
				checkDeviceCookie(t, rec, "")
			},
		},
		{
			name: "device lookup: an empty list writes no file",
			req:  pushReq{path: "/api/push/device", body: `{"endpoint":"` + phoneEndpoint + `"}`},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if decode(t, rec)["listed"] != false {
					t.Errorf("answer = %s, want listed false", rec.Body.String())
				}
				if f.devicesWritten() {
					t.Errorf("a lookup wrote push-devices.json")
				}
			},
		},
		{
			name: "device lookup with no endpoint is 400 and sets no cookie",
			req:  pushReq{path: "/api/push/device", body: `{}`},
			want: http.StatusBadRequest,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if c := deviceCookieSet(rec); c != nil {
					t.Errorf("a refused lookup set the device cookie: %+v", c)
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
			name:  "device lookup with no cookie is 401",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/device", body: `{"endpoint":"` + phoneEndpoint + `"}`, noCookie: true},
			want:  http.StatusUnauthorized,
		},
		{
			name:  "device lookup from another site is 403",
			setup: func(t *testing.T, f *pushFake) { f.seed(t, phoneEndpoint, "phone") },
			req:   pushReq{path: "/api/push/device", body: `{"endpoint":"` + phoneEndpoint + `"}`, origin: "https://evil.example"},
			want:  http.StatusForbidden,
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
			for k, vs := range rec.Header() {
				for _, v := range vs {
					if strings.Contains(v, "push.example.com") {
						t.Errorf("header %s echoes an endpoint: %s", k, v)
					}
				}
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
		{path: "/api/push/device", body: `{"endpoint":"` + phoneEndpoint + `"}`},
		{method: http.MethodGet, path: "/notifications/remove?id=0123"},
		{path: "/notifications/remove", body: "confirm=yes&id=0123", form: true},
		{path: "/api/notifications/remove", body: `{"id":"0123","confirm":true}`},
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
				if c := deviceCookieSet(rec); c != nil {
					t.Errorf("push off set the device cookie: %+v", c)
				}
				if strings.Contains(rec.Body.String(), "push.example.com") {
					t.Errorf("the answer echoes an endpoint: %s", rec.Body.String())
				}
			})
		}
	}
}

// TestNotificationsView covers GET /notifications and its JSON twin.
// Load-bearing rows: "carries the public key" (the browser cannot subscribe
// without it, and it must be the key the sender signs with, read back from
// the same file), "damaged key" (a 200 with the file named, never a 500,
// and the file left exactly as it was), "the API lists every device" (the
// order and fields a script and the live list rely on, and no keys), "an
// empty devices array" (null would break a script that reads .length), and
// the page rows: the badge on the cookie's device only, no form in the live
// region (live.js would stop swapping it), and the empty state. Every row
// checks no endpoint reaches the answer.
func TestNotificationsView(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *pushFake)
		path  string
		// device, when set, is the endpoint of the seeded device whose id
		// the request's device cookie holds.
		device string
		check  func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder)
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
			name: "the API lists every device, this device first, then newest first",
			setup: func(t *testing.T, f *pushFake) {
				f.seedAt(t, phoneEndpoint, "Safari on iPhone", testNow.Add(-48*time.Hour))
				f.seedAt(t, laptopEndpoint, "Firefox on Windows", testNow.Add(-24*time.Hour))
				f.seedAt(t, tabletEndpoint, "Chrome on Android", testNow)
			},
			path:   "/api/notifications",
			device: phoneEndpoint,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				var v NotificationsView
				must(t, json.Unmarshal(rec.Body.Bytes(), &v))
				want := []DeviceView{
					{ID: f.idOf(phoneEndpoint), Label: "Safari on iPhone", At: testNow.Add(-48 * time.Hour).UTC().Format(time.RFC3339), SignedUp: testNow.Add(-48 * time.Hour).UTC().Format("Jan 2, 2006"), ThisDevice: true},
					{ID: f.idOf(tabletEndpoint), Label: "Chrome on Android", At: testNow.UTC().Format(time.RFC3339), SignedUp: testNow.UTC().Format("Jan 2, 2006")},
					{ID: f.idOf(laptopEndpoint), Label: "Firefox on Windows", At: testNow.Add(-24 * time.Hour).UTC().Format(time.RFC3339), SignedUp: testNow.Add(-24 * time.Hour).UTC().Format("Jan 2, 2006")},
				}
				if !reflect.DeepEqual(v.Devices, want) {
					t.Errorf("devices = %s\nwant %s", dump(v.Devices), dump(want))
				}
				if strings.Contains(rec.Body.String(), "p256dh-") || strings.Contains(rec.Body.String(), "auth-") {
					t.Errorf("the list carries a device's keys: %s", rec.Body.String())
				}
			},
		},
		{
			name: "the API has an empty devices array with none signed up",
			path: "/api/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				devs, ok := decode(t, rec)["devices"].([]any)
				if !ok || len(devs) != 0 {
					t.Errorf("devices = %v, want an empty array: %s", decode(t, rec)["devices"], rec.Body.String())
				}
			},
		},
		{
			name: "the page draws the rows, the badge only on the cookie's device",
			setup: func(t *testing.T, f *pushFake) {
				f.seedAt(t, phoneEndpoint, "Safari on iPhone", testNow.Add(-48*time.Hour))
				f.seedAt(t, laptopEndpoint, "Firefox <b>on</b> Windows", testNow)
			},
			path:   "/notifications",
			device: phoneEndpoint,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				body := rec.Body.String()
				region := between(t, body, `<div id="push-device-list"`, `</section>`)
				for _, want := range []string{
					`data-live="devices" data-project="" aria-live="polite"`,
					`Signed-up devices</h2><span class="meta">2</span>`,
					`Signed up ` + testNow.UTC().Format("Jan 2, 2006"),
					`Firefox &lt;b&gt;on&lt;/b&gt; Windows`,
				} {
					if !strings.Contains(region, want) {
						t.Errorf("the device list has no %q:\n%s", want, region)
					}
				}
				phone := strings.Index(region, `data-row-id="`+f.idOf(phoneEndpoint)+`"`)
				laptop := strings.Index(region, `data-row-id="`+f.idOf(laptopEndpoint)+`"`)
				if phone < 0 || laptop < 0 || phone > laptop {
					t.Errorf("rows at %d (this phone) and %d (laptop), want this device first", phone, laptop)
				}
				if n := strings.Count(region, "badge--this-device"); n != 1 {
					t.Fatalf("badge--this-device appears %d times, want once", n)
				}
				if b := strings.Index(region, "badge--this-device"); b < phone || b > laptop {
					t.Errorf("the badge is not on the cookie's device's row")
				}
				for _, banned := range []string{"<form", "<input", "<button"} {
					if strings.Contains(region, banned) {
						t.Errorf("the live region holds %s, which live.js will not swap", banned)
					}
				}
				if strings.Contains(region, "empty__title") {
					t.Errorf("the empty state shows beside rows")
				}
				// Each row's Remove is a link to the confirm page, carrying
				// what the script needs to fill the shared dialog; only the
				// cookie's device says it is this device.
				phoneID, laptopID := f.idOf(phoneEndpoint), f.idOf(laptopEndpoint)
				for _, want := range []string{
					`<a class="btn btn--quiet" href="/notifications/remove?id=` + phoneID + `" data-push-remove data-device-id="` + phoneID + `" data-device-label="Safari on iPhone" data-this-device aria-label="Remove Safari on iPhone">Remove</a>`,
					`<a class="btn btn--quiet" href="/notifications/remove?id=` + laptopID + `" data-push-remove data-device-id="` + laptopID + `" data-device-label="Firefox &lt;b&gt;on&lt;/b&gt; Windows" aria-label="Remove Firefox &lt;b&gt;on&lt;/b&gt; Windows">Remove</a>`,
				} {
					if !strings.Contains(region, want) {
						t.Errorf("the device list has no %q:\n%s", want, region)
					}
				}
				if n := strings.Count(region, "data-this-device"); n != 1 {
					t.Errorf("data-this-device appears %d times, want once", n)
				}
				if strings.Contains(region, "data-confirm") {
					t.Errorf("a Remove link carries data-confirm, which would let confirm.js open a dialog the page's own script owns")
				}
				// The one shared dialog sits after the live region, never in
				// it, and posts the confirm and an id to the remove address.
				sheet := between(t, body, `<dialog class="confirm-sheet" id="push-remove-sheet"`, `</dialog>`)
				if strings.Index(body, `id="push-remove-sheet"`) < strings.Index(body, `</section>`) {
					t.Errorf("the shared dialog is not after the device list")
				}
				for _, want := range []string{
					`<form method="post" action="/notifications/remove" id="push-remove-sheet-form"`,
					`<input type="hidden" name="confirm" value="yes">`,
					`<input type="hidden" name="id" value="">`,
					`class="btn btn--danger" data-loading-label="Removing">Remove</button>`,
					`<form method="dialog">`,
					`autofocus>Keep it</button>`,
				} {
					if !strings.Contains(sheet, want) {
						t.Errorf("the shared dialog has no %q:\n%s", want, sheet)
					}
				}
				if n := strings.Count(body, "<dialog"); n != 1 {
					t.Errorf("the page has %d dialogs, want the one shared confirm", n)
				}
			},
		},
		{
			name: "the page marks nothing without a device cookie",
			setup: func(t *testing.T, f *pushFake) {
				f.seed(t, phoneEndpoint, "phone")
			},
			path: "/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if strings.Contains(rec.Body.String(), "badge--this-device") {
					t.Errorf("a device is marked with no device cookie")
				}
			},
		},
		{
			name: "the page shows the empty state with no devices",
			path: "/notifications",
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				region := between(t, rec.Body.String(), `<div id="push-device-list"`, `</section>`)
				for _, want := range []string{
					`<span class="meta">0</span>`,
					`<p class="empty__title">No devices signed up</p>`,
					`Turn notifications on from a phone or computer and it shows up here.`,
				} {
					if !strings.Contains(region, want) {
						t.Errorf("the empty list has no %q:\n%s", want, region)
					}
				}
				if strings.Contains(region, `class="list"`) {
					t.Errorf("the empty state draws a list")
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
			req := pushReq{method: http.MethodGet, path: tc.path, ua: androidChrome}
			if tc.device != "" {
				req.device = f.idOf(tc.device)
			}
			rec := req.send(rg.h)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			if len(f.calls) != 0 {
				t.Errorf("opening the page sent %d pushes", len(f.calls))
			}
			if strings.Contains(rec.Body.String(), "push.example.com") {
				t.Errorf("the answer carries an endpoint:\n%s", rec.Body.String())
			}
			if c := deviceCookieSet(rec); c != nil {
				t.Errorf("a GET set the device cookie: %+v", c)
			}
			tc.check(t, f, rec)
		})
	}
}

// between is the text of s from the first start up to the next end, failing
// when either is missing.
func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", start, s)
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		t.Fatalf("no %q after %q", end, start)
	}
	return s[i : i+j]
}

// TestPushDeviceViews covers the pure order and formatting of the device list.
//
// Load-bearing rows: "this device first" (the owner looks for the device in
// their hand), "the rest newest first", "an unknown id marks nothing" (a
// stale cookie from a removed device must not mark another row), "ties and
// unreadable times keep the file's order, unreadable last" (a list that
// reshuffled between two reads of one file would flash rows on every live
// re-fetch) and "no devices" (never nil, so the JSON is [] and not null).
func TestPushDeviceViews(t *testing.T) {
	dev := func(id, at string) webpush.Device {
		return webpush.Device{ID: id, Endpoint: "https://push.example.com/" + id, Label: "L" + id, At: at}
	}
	const (
		oct2  = "2026-10-02T09:30:00Z"
		oct5  = "2026-10-05T23:59:00Z"
		oct9  = "2026-10-09T00:00:00-07:00" // Oct 9 in UTC too, at 07:00
		late  = "2026-10-20T23:30:00-05:00" // Oct 21 in UTC
		wrong = "yesterday"
	)
	tests := []struct {
		name   string
		devs   []webpush.Device
		thisID string
		want   []string // ids in order
		mark   string   // the one id marked as this device, or ""
	}{
		{name: "no devices", devs: nil, want: []string{}},
		{name: "the rest newest first", devs: []webpush.Device{dev("a", oct2), dev("b", oct9), dev("c", oct5)}, want: []string{"b", "c", "a"}},
		{name: "this device first", devs: []webpush.Device{dev("a", oct2), dev("b", oct9), dev("c", oct5)}, thisID: "a", want: []string{"a", "b", "c"}, mark: "a"},
		{name: "this device first even with an unreadable time", devs: []webpush.Device{dev("a", oct2), dev("b", wrong)}, thisID: "b", want: []string{"b", "a"}, mark: "b"},
		{name: "an unknown id marks nothing", devs: []webpush.Device{dev("a", oct2), dev("b", oct9)}, thisID: "zzz", want: []string{"b", "a"}},
		{name: "ties and unreadable times keep the file's order, unreadable last", devs: []webpush.Device{dev("x", wrong), dev("a", oct5), dev("y", ""), dev("b", oct5), dev("c", oct9)}, want: []string{"c", "a", "b", "x", "y"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deviceViews(tc.devs, tc.thisID)
			if got == nil {
				t.Fatal("deviceViews = nil, want a non-nil slice")
			}
			ids := []string{}
			for _, v := range got {
				ids = append(ids, v.ID)
				if v.ThisDevice != (v.ID == tc.mark) {
					t.Errorf("%s: ThisDevice = %v, want %v", v.ID, v.ThisDevice, v.ID == tc.mark)
				}
				if v.Label != "L"+v.ID {
					t.Errorf("%s: Label = %q", v.ID, v.Label)
				}
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("order = %v, want %v", ids, tc.want)
			}
		})
	}

	// The date the page shows: "Jan 2, 2006" in UTC, and "" for a time that
	// cannot be read.
	dates := []struct{ at, want string }{
		{oct2, "Oct 2, 2026"},
		{oct9, "Oct 9, 2026"},
		{late, "Oct 21, 2026"},
		{wrong, ""},
		{"", ""},
	}
	for _, d := range dates {
		t.Run("signed up "+d.at, func(t *testing.T) {
			got := deviceViews([]webpush.Device{dev("a", d.at)}, "")
			if got[0].SignedUp != d.want || got[0].At != d.at {
				t.Errorf("SignedUp, At = %q, %q; want %q, %q", got[0].SignedUp, got[0].At, d.want, d.at)
			}
		})
	}
}

// removeRig is a rig with push on and two devices signed up: the phone, the
// older, and the laptop, whose label holds markup.
func removeRig(t *testing.T) (*rig, *pushFake) {
	t.Helper()
	rg := newRig(t, Config{})
	f := rg.withPush()
	f.seedAt(t, phoneEndpoint, "Safari on iPhone", testNow.Add(-48*time.Hour))
	f.seedAt(t, laptopEndpoint, "Firefox <b>on</b> Windows", testNow)
	return rg, f
}

// listed is the device with this endpoint as stored, and whether it is.
func (f *pushFake) listed(endpoint string) (webpush.Device, bool) {
	for _, d := range webpush.ReadDevices(f.dir) {
		if d.Endpoint == endpoint {
			return d, true
		}
	}
	return webpush.Device{}, false
}

// TestPushRemoveDevice covers POST /notifications/remove and its /api twin
// against a temp PushDir holding the phone and the laptop.
//
// Load-bearing rows:
//   - "page remove takes off that device only": the other device's entry is
//     byte for byte what it was, so removing one device never costs the
//     owner another's keys.
//   - the "gone id" rows: an id already removed (a second tab, a gone
//     answer to a send) is success with removed false, never an error page.
//   - the "no confirm" rows, the query-confirm row included: a followed link
//     or a crafted address must never remove a device, and the file must be
//     exactly as it was.
//   - "removing this device clears the cookie": a cookie left naming a gone
//     id would mark nothing now, but the server would go on believing the
//     browser is a listed device.
//
// Every row checks no endpoint reaches the body or a header.
func TestPushRemoveDevice(t *testing.T) {
	tests := []struct {
		name string
		req  func(f *pushFake) pushReq
		want int
		// location is the redirect a page POST answers with, or "".
		location string
		// gone lists the endpoints that must be off the list afterwards;
		// every other seeded device must be exactly as it was.
		gone  []string
		check func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder)
	}{
		{
			name: "page remove takes off that device only",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/notifications/remove", body: "confirm=yes&id=" + f.idOf(laptopEndpoint), form: true}
			},
			want: http.StatusSeeOther, location: "/notifications?done=device-removed", gone: []string{laptopEndpoint},
		},
		{
			name: "API remove answers removed true",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/api/notifications/remove", body: `{"id":"` + f.idOf(phoneEndpoint) + `","confirm":true}`}
			},
			want: http.StatusOK, gone: []string{phoneEndpoint},
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if m := decode(t, rec); m["removed"] != true || len(m) != 1 {
					t.Errorf("body = %s, want {\"removed\":true}", rec.Body.String())
				}
			},
		},
		{
			name: "page gone id is success, not an error",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/notifications/remove", body: "confirm=yes&id=4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a", form: true}
			},
			want: http.StatusSeeOther, location: "/notifications?done=device-gone",
		},
		{
			name: "API gone id answers removed false",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/api/notifications/remove", body: `{"id":"4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a","confirm":true}`}
			},
			want: http.StatusOK,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if m := decode(t, rec); m["removed"] != false {
					t.Errorf("body = %s, want {\"removed\":false}", rec.Body.String())
				}
			},
		},
		{
			name: "page no confirm is refused",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/notifications/remove", body: "id=" + f.idOf(laptopEndpoint), form: true}
			},
			want: http.StatusBadRequest,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if !strings.Contains(rec.Body.String(), "removing a device needs the confirm") {
					t.Errorf("the refusal does not say why:\n%s", rec.Body.String())
				}
			},
		},
		{
			name: "API no confirm is refused",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/api/notifications/remove", body: `{"id":"` + f.idOf(laptopEndpoint) + `"}`}
			},
			want: http.StatusBadRequest,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if m := decode(t, rec); m["reason"] != "confirm_required" {
					t.Errorf("body = %s, want reason confirm_required", rec.Body.String())
				}
			},
		},
		{
			name: "a confirm in the address does not count",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/notifications/remove?confirm=yes", body: "id=" + f.idOf(laptopEndpoint), form: true}
			},
			want: http.StatusBadRequest,
		},
		{
			name: "no id is refused",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/notifications/remove", body: "confirm=yes&id=+", form: true}
			},
			want: http.StatusBadRequest,
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if !strings.Contains(rec.Body.String(), "no device was named") {
					t.Errorf("the refusal does not say why:\n%s", rec.Body.String())
				}
			},
		},
		{
			name: "an id that is not text is refused",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/api/notifications/remove", body: `{"id":7,"confirm":true}`}
			},
			want: http.StatusBadRequest,
		},
		{
			name: "removing this device clears the cookie",
			req: func(f *pushFake) pushReq {
				id := f.idOf(phoneEndpoint)
				return pushReq{path: "/notifications/remove", body: "confirm=yes&id=" + id, form: true, device: id}
			},
			want: http.StatusSeeOther, location: "/notifications?done=device-removed", gone: []string{phoneEndpoint},
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				checkDeviceCookie(t, rec, "")
			},
		},
		{
			name: "removing another device keeps this device's cookie",
			req: func(f *pushFake) pushReq {
				return pushReq{path: "/api/notifications/remove", body: `{"id":"` + f.idOf(laptopEndpoint) + `","confirm":true}`, device: f.idOf(phoneEndpoint)}
			},
			want: http.StatusOK, gone: []string{laptopEndpoint},
			check: func(t *testing.T, f *pushFake, rec *httptest.ResponseRecorder) {
				if c := deviceCookieSet(rec); c != nil {
					t.Errorf("removing another device touched this device's cookie: %+v", c)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg, f := removeRig(t)
			before := map[string]webpush.Device{}
			for _, d := range webpush.ReadDevices(f.dir) {
				before[d.Endpoint] = d
			}
			raw, err := os.ReadFile(filepath.Join(f.dir, webpush.DevicesFileName))
			must(t, err)
			rec := tc.req(f).send(rg.h)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tc.location {
				t.Errorf("Location = %q, want %q", got, tc.location)
			}
			gone := map[string]bool{}
			for _, e := range tc.gone {
				gone[e] = true
			}
			for e, d := range before {
				after, ok := f.listed(e)
				switch {
				case gone[e] && ok:
					t.Errorf("%s is still listed", d.Label)
				case !gone[e] && !reflect.DeepEqual(after, d):
					t.Errorf("%s changed: %+v, want %+v", d.Label, after, d)
				}
			}
			if len(tc.gone) == 0 {
				after, err := os.ReadFile(filepath.Join(f.dir, webpush.DevicesFileName))
				must(t, err)
				if string(after) != string(raw) {
					t.Errorf("push-devices.json changed although nothing was removed")
				}
			}
			if strings.Contains(rec.Body.String(), "push.example.com") {
				t.Errorf("the answer echoes an endpoint: %s", rec.Body.String())
			}
			for k, vs := range rec.Header() {
				for _, v := range vs {
					if strings.Contains(v, "push.example.com") {
						t.Errorf("header %s echoes an endpoint: %s", k, v)
					}
				}
			}
			if len(f.calls) != 0 {
				t.Errorf("removing a device sent %d pushes", len(f.calls))
			}
			if tc.check != nil {
				tc.check(t, f, rec)
			}
		})
	}
}

// TestPushRemoveWriteError covers a device list that cannot be written: a
// 500 in plain words, nothing removed. Load-bearing: the owner must be told
// the device is still signed up, not shown "Device removed".
func TestPushRemoveWriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	rg, f := removeRig(t)
	id := f.idOf(laptopEndpoint)
	must(t, os.Chmod(f.dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })
	rec := pushReq{path: "/notifications/remove", body: "confirm=yes&id=" + id, form: true}.send(rg.h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The device list could not be saved, so nothing was removed") {
		t.Errorf("the error does not say nothing was removed:\n%s", rec.Body.String())
	}
	if _, ok := f.listed(laptopEndpoint); !ok {
		t.Errorf("the laptop is gone although the write failed")
	}
	if strings.Contains(rec.Body.String(), "push.example.com") {
		t.Errorf("the answer echoes an endpoint: %s", rec.Body.String())
	}
}

// hiddenField matches a hidden input, for posting a page's form as a browser
// without JavaScript would.
var hiddenField = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

// TestPushRemoveConfirmPage covers GET /notifications/remove?id=, the page a
// browser without JavaScript reaches from a row's Remove.
//
// Load-bearing rows: "another device" and "this device" (the sentence must
// say what happens to the device meant: this device is told it stops,
// another is told how to come back), "the label is escaped" (the label comes
// from a User-Agent, which anyone signing up chooses), the 404 rows (the
// address must not make the page name a device afk does not have) and
// "posting its form removes the device" (the whole no-JavaScript path: page,
// form, redirect, toast, list).
func TestPushRemoveConfirmPage(t *testing.T) {
	tests := []struct {
		name   string
		path   func(f *pushFake) string
		device func(f *pushFake) string
		want   int
		has    []string
		hasNot []string
	}{
		{
			name: "another device",
			path: func(f *pushFake) string { return "/notifications/remove?id=" + f.idOf(phoneEndpoint) },
			want: http.StatusOK,
			has: []string{
				"<h1>Remove Safari on iPhone?</h1>", "<p>" + deviceRemoveOther + "</p>",
				`<form method="post" action="/notifications/remove" class="stack">`,
				`<input type="hidden" name="confirm" value="yes">`,
				`<button type="submit" class="btn btn--danger">Remove</button>`,
				`<a class="btn btn--secondary" href="/notifications" autofocus>Keep it</a>`,
			},
			hasNot: []string{deviceRemoveThis},
		},
		{
			name:   "this device",
			path:   func(f *pushFake) string { return "/notifications/remove?id=" + f.idOf(phoneEndpoint) },
			device: func(f *pushFake) string { return f.idOf(phoneEndpoint) },
			want:   http.StatusOK,
			has:    []string{"<h1>Remove Safari on iPhone?</h1>", "<p>" + deviceRemoveThis + "</p>"},
			hasNot: []string{deviceRemoveOther},
		},
		{
			name:   "the label is escaped",
			path:   func(f *pushFake) string { return "/notifications/remove?id=" + f.idOf(laptopEndpoint) },
			want:   http.StatusOK,
			has:    []string{"<h1>Remove Firefox &lt;b&gt;on&lt;/b&gt; Windows?</h1>"},
			hasNot: []string{"<b>on</b>"},
		},
		{
			name: "unknown id is not found",
			path: func(f *pushFake) string { return "/notifications/remove?id=4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a" },
			want: http.StatusNotFound,
		},
		{
			name: "no id is not found",
			path: func(f *pushFake) string { return "/notifications/remove" },
			want: http.StatusNotFound,
		},
		{
			name: "a label is not an id",
			path: func(f *pushFake) string { return "/notifications/remove?id=Safari+on+iPhone" },
			want: http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg, f := removeRig(t)
			req := pushReq{method: http.MethodGet, path: tc.path(f)}
			if tc.device != nil {
				req.device = tc.device(f)
			}
			rec := req.send(rg.h)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.has {
				if !strings.Contains(body, w) {
					t.Errorf("the page has no %q:\n%s", w, body)
				}
			}
			for _, w := range tc.hasNot {
				if strings.Contains(body, w) {
					t.Errorf("the page has %q", w)
				}
			}
			if strings.Contains(body, "push.example.com") {
				t.Errorf("the page carries an endpoint")
			}
			if len(webpush.ReadDevices(f.dir)) != 2 {
				t.Errorf("opening the confirm page changed the list")
			}
			if c := deviceCookieSet(rec); c != nil {
				t.Errorf("a GET set the device cookie: %+v", c)
			}
		})
	}

	t.Run("posting its form removes the device", func(t *testing.T) {
		rg, f := removeRig(t)
		id := f.idOf(laptopEndpoint)
		page := pushReq{method: http.MethodGet, path: "/notifications/remove?id=" + id}.send(rg.h)
		if page.Code != http.StatusOK {
			t.Fatalf("confirm page status = %d", page.Code)
		}
		form := between(t, page.Body.String(), `<form method="post" action="/notifications/remove"`, `</form>`)
		vals := url.Values{}
		for _, m := range hiddenField.FindAllStringSubmatch(form, -1) {
			vals.Set(m[1], html.UnescapeString(m[2]))
		}
		if vals.Get("id") != id || vals.Get("confirm") != "yes" {
			t.Fatalf("the form posts %v, want the confirm and the laptop's id", vals)
		}
		rec := pushReq{path: "/notifications/remove", body: vals.Encode(), form: true}.send(rg.h)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notifications?done=device-removed" {
			t.Fatalf("POST = %d to %q, want 303 to /notifications?done=device-removed", rec.Code, rec.Header().Get("Location"))
		}
		if _, ok := f.listed(laptopEndpoint); ok {
			t.Errorf("the laptop is still listed")
		}
		if _, ok := f.listed(phoneEndpoint); !ok {
			t.Errorf("the phone went too")
		}
		landed := pushReq{method: http.MethodGet, path: "/notifications?done=device-removed"}.send(rg.h)
		body := landed.Body.String()
		if !strings.Contains(body, `<div class="toast" role="status" data-toast>Device removed.</div>`) {
			t.Errorf("the Notifications page shows no Device removed toast")
		}
		if strings.Contains(body, `data-row-id="`+id+`"`) || !strings.Contains(body, `data-row-id="`+f.idOf(phoneEndpoint)+`"`) {
			t.Errorf("the list does not show the phone alone")
		}
	})
}

// TestPushRemoveToasts covers where the two remove toasts show. Load-bearing:
// the rows on other pages, where the codes must show nothing, since a crafted
// address must not make the overview or a project say a device was removed.
func TestPushRemoveToasts(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/notifications?done=device-removed", "Device removed."},
		{"/notifications?done=device-gone", "That device was already removed."},
		{"/notifications?done=project_removed", ""},
		{"/notifications?done=nonsense", ""},
		{"/p/app?done=device-removed", ""},
		{"/?done=device-gone", ""},
		{"/usage?done=device-removed", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.withPush()
			rec := pushReq{method: http.MethodGet, path: tc.path}.send(rg.h)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			has := strings.Contains(body, "data-toast>")
			switch {
			case tc.want == "" && has:
				t.Errorf("a toast shows: %s", between(t, body, "data-toast>", "</div>"))
			case tc.want != "" && !strings.Contains(body, "data-toast>"+tc.want+"</div>"):
				t.Errorf("no toast %q", tc.want)
			}
		})
	}
}
