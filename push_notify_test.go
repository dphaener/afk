package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/webpush"
)

// pushRig is a temp device list, a fake send and a log collector.
type pushRig struct {
	dir    string
	calls  []webpush.Subscription
	pays   []webpush.Payload
	opts   []webpush.Options
	hasDL  []bool
	logs   []string
	answer func(sub webpush.Subscription) (bool, error)
}

func (r *pushRig) send(ctx context.Context, sub webpush.Subscription, p webpush.Payload, o webpush.Options) (bool, error) {
	_, ok := ctx.Deadline()
	r.hasDL = append(r.hasDL, ok)
	r.calls = append(r.calls, sub)
	r.pays = append(r.pays, p)
	r.opts = append(r.opts, o)
	if r.answer != nil {
		return r.answer(sub)
	}
	return false, nil
}

func (r *pushRig) logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func newPushRig(t *testing.T, endpoints ...string) *pushRig {
	t.Helper()
	r := &pushRig{dir: t.TempDir()}
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for i, ep := range endpoints {
		sub := webpush.Subscription{Endpoint: ep, P256dh: "key-" + ep, Auth: "auth-" + ep}
		rnd := bytes.NewReader(bytes.Repeat([]byte{byte(i + 1)}, 16))
		if _, err := webpush.AddDevice(r.dir, sub, fmt.Sprintf("Phone %d", i+1), now, rnd); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// TestPushTargetDeliver: the gone row (removed from the list and not
// counted), the failing row (kept, logged, not counted) and the log-secrecy
// check are load-bearing: they are the T15.4 rule that only "gone" removes a
// device, and the promise that no log line carries a capability URL. The
// invalid-payload row pins that nothing is sent for a link that could leave
// the page.
func TestPushTargetDeliver(t *testing.T) {
	good := webpush.Payload{Title: "afk: Need a key", Body: "afk inbox show x", URL: "/p/afk/inbox/x", Tag: "afk-item-x"}
	opt := webpush.Options{Urgency: "high"}
	epA, epB := "https://push.example/secret-a", "https://push.example/secret-b"

	t.Run("zero value sends nothing", func(t *testing.T) {
		if n := (pushTarget{}).deliver(good, opt, nil); n != 0 {
			t.Errorf("sent = %d, want 0", n)
		}
	})
	t.Run("no devices sends and logs nothing", func(t *testing.T) {
		r := newPushRig(t)
		if n := (pushTarget{r.dir, r.send}).deliver(good, opt, r.logf); n != 0 || len(r.calls) != 0 || len(r.logs) != 0 {
			t.Errorf("sent=%d calls=%d logs=%v", n, len(r.calls), r.logs)
		}
	})
	t.Run("send func missing sends nothing", func(t *testing.T) {
		r := newPushRig(t, epA)
		if n := (pushTarget{dir: r.dir}).deliver(good, opt, r.logf); n != 0 {
			t.Errorf("sent = %d", n)
		}
	})
	t.Run("two devices both get the same payload and options", func(t *testing.T) {
		r := newPushRig(t, epA, epB)
		n := (pushTarget{r.dir, r.send}).deliver(good, opt, r.logf)
		if n != 2 || len(r.calls) != 2 {
			t.Fatalf("sent=%d calls=%d", n, len(r.calls))
		}
		for i := range r.calls {
			if r.pays[i] != good || r.opts[i] != opt {
				t.Errorf("call %d: payload %+v options %+v", i, r.pays[i], r.opts[i])
			}
		}
		if len(webpush.ReadDevices(r.dir)) != 2 || len(r.logs) != 0 {
			t.Errorf("devices changed or logged: %v", r.logs)
		}
	})
	t.Run("gone device is removed and not counted", func(t *testing.T) {
		r := newPushRig(t, epA, epB)
		r.answer = func(s webpush.Subscription) (bool, error) { return s.Endpoint == epA, nil }
		n := (pushTarget{r.dir, r.send}).deliver(good, opt, r.logf)
		left := webpush.ReadDevices(r.dir)
		if n != 1 || len(left) != 1 || left[0].Endpoint != epB {
			t.Errorf("sent=%d left=%+v", n, left)
		}
		if len(r.logs) != 1 || !strings.Contains(r.logs[0], "Phone 1") {
			t.Errorf("logs = %v", r.logs)
		}
	})
	t.Run("failing device is kept and logged", func(t *testing.T) {
		r := newPushRig(t, epA, epB)
		r.answer = func(s webpush.Subscription) (bool, error) {
			if s.Endpoint == epA {
				return false, fmt.Errorf("the push service answered 503")
			}
			return false, nil
		}
		n := (pushTarget{r.dir, r.send}).deliver(good, opt, r.logf)
		if n != 1 || len(webpush.ReadDevices(r.dir)) != 2 {
			t.Errorf("sent=%d devices=%d", n, len(webpush.ReadDevices(r.dir)))
		}
		if len(r.logs) != 1 || !strings.Contains(r.logs[0], "Phone 1") || !strings.Contains(r.logs[0], "503") {
			t.Errorf("logs = %v", r.logs)
		}
	})
	t.Run("invalid payload sends nothing", func(t *testing.T) {
		r := newPushRig(t, epA)
		bad := good
		bad.URL = "https://evil.example/"
		n := (pushTarget{r.dir, r.send}).deliver(bad, opt, r.logf)
		if n != 0 || len(r.calls) != 0 || len(r.logs) != 1 {
			t.Errorf("sent=%d calls=%d logs=%v", n, len(r.calls), r.logs)
		}
	})
	t.Run("every send has its own deadline", func(t *testing.T) {
		r := newPushRig(t, epA, epB)
		(pushTarget{r.dir, r.send}).deliver(good, opt, nil)
		if len(r.hasDL) != 2 || !r.hasDL[0] || !r.hasDL[1] {
			t.Errorf("deadlines = %v", r.hasDL)
		}
	})
	t.Run("no log line holds an endpoint or key", func(t *testing.T) {
		r := newPushRig(t, epA, epB)
		r.answer = func(s webpush.Subscription) (bool, error) {
			if s.Endpoint == epA {
				return true, nil
			}
			return false, fmt.Errorf("network down")
		}
		(pushTarget{r.dir, r.send}).deliver(good, opt, r.logf)
		if len(r.logs) != 2 {
			t.Fatalf("logs = %v", r.logs)
		}
		for _, l := range r.logs {
			for _, secret := range []string{"push.example", "secret-", "key-", "auth-"} {
				if strings.Contains(l, secret) {
					t.Errorf("log line %q holds %q", l, secret)
				}
			}
		}
	})
}
