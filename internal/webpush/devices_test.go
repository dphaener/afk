package webpush

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var devNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func devSub(endpoint string) Subscription {
	return Subscription{Endpoint: endpoint, P256dh: "pk", Auth: "au"}
}

func zeroRand() *bytes.Reader { return bytes.NewReader(make([]byte, 64)) }

// TestDevices: the same-endpoint replace row (an id that changes would make
// a re-subscribing browser a second device) and the damaged-file row (the
// list must never stop the loop) are load-bearing; the rest pin the file.
func TestDevices(t *testing.T) {
	t.Run("add then list", func(t *testing.T) {
		dir := t.TempDir()
		d, err := AddDevice(dir, devSub("https://push.example/a"), "Chrome on Android", devNow, zeroRand())
		if err != nil {
			t.Fatal(err)
		}
		if len(d.ID) != 32 || d.At != "2026-10-20T12:00:00Z" {
			t.Errorf("device = %+v", d)
		}
		got := ReadDevices(dir)
		if len(got) != 1 || got[0] != d {
			t.Errorf("list = %+v, want [%+v]", got, d)
		}
		if got[0].Subscription() != devSub("https://push.example/a") {
			t.Errorf("subscription = %+v", got[0].Subscription())
		}
	})
	t.Run("same endpoint twice keeps one entry and the id", func(t *testing.T) {
		dir := t.TempDir()
		first, _ := AddDevice(dir, devSub("https://push.example/a"), "old", devNow, zeroRand())
		sub := Subscription{Endpoint: "https://push.example/a", P256dh: "new", Auth: "new2"}
		later := devNow.Add(time.Hour)
		second, err := AddDevice(dir, sub, "new label", later, bytes.NewReader([]byte{1}))
		if err != nil {
			t.Fatal(err)
		}
		got := ReadDevices(dir)
		if len(got) != 1 || second.ID != first.ID || got[0].P256dh != "new" || got[0].Auth != "new2" ||
			got[0].Label != "new label" || got[0].At != "2026-10-20T13:00:00Z" {
			t.Errorf("list = %+v, first = %+v", got, first)
		}
	})
	t.Run("remove by id", func(t *testing.T) {
		dir := t.TempDir()
		a, _ := AddDevice(dir, devSub("https://push.example/a"), "a", devNow, zeroRand())
		_, _ = AddDevice(dir, devSub("https://push.example/b"), "b", devNow, bytes.NewReader(bytes.Repeat([]byte{7}, 16)))
		ok, err := RemoveDevice(dir, a.ID)
		if !ok || err != nil {
			t.Fatalf("= %v, %v", ok, err)
		}
		if got := ReadDevices(dir); len(got) != 1 || got[0].Endpoint != "https://push.example/b" {
			t.Errorf("list = %+v", got)
		}
	})
	t.Run("remove by endpoint", func(t *testing.T) {
		dir := t.TempDir()
		_, _ = AddDevice(dir, devSub("https://push.example/a"), "a", devNow, zeroRand())
		ok, err := RemoveEndpoint(dir, "https://push.example/a")
		if !ok || err != nil {
			t.Fatalf("= %v, %v", ok, err)
		}
		if got := ReadDevices(dir); len(got) != 0 {
			t.Errorf("list = %+v", got)
		}
	})
	t.Run("remove missing is not an error", func(t *testing.T) {
		dir := t.TempDir()
		for name, f := range map[string]func() (bool, error){
			"id":       func() (bool, error) { return RemoveDevice(dir, "nope") },
			"endpoint": func() (bool, error) { return RemoveEndpoint(dir, "https://x.example/") },
		} {
			if ok, err := f(); ok || err != nil {
				t.Errorf("%s = %v, %v, want false, nil", name, ok, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, DevicesFileName)); err == nil {
			t.Error("removing nothing created the file")
		}
	})
	t.Run("missing file reads empty", func(t *testing.T) {
		got := ReadDevices(t.TempDir())
		if got == nil || len(got) != 0 {
			t.Errorf("= %#v, want empty non-nil", got)
		}
	})
	t.Run("damaged file reads empty and the next add replaces it", func(t *testing.T) {
		for name, content := range map[string]string{"garbage": "{not json", "null list": `{"devices":null}`, "empty": ""} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, DevicesFileName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := ReadDevices(dir); got == nil || len(got) != 0 {
				t.Errorf("%s: read = %#v", name, got)
			}
			if _, err := AddDevice(dir, devSub("https://push.example/a"), "a", devNow, zeroRand()); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := ReadDevices(dir); len(got) != 1 {
				t.Errorf("%s: after add = %+v", name, got)
			}
		}
	})
	t.Run("file mode 0600 and folder 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		if _, err := AddDevice(dir, devSub("https://push.example/a"), "a", devNow, zeroRand()); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(dir, DevicesFileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("file = %v, %v", fi, err)
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", di.Mode().Perm())
		}
	})
	t.Run("empty list is written as an array", func(t *testing.T) {
		dir := t.TempDir()
		a, _ := AddDevice(dir, devSub("https://push.example/a"), "a", devNow, zeroRand())
		if ok, err := RemoveDevice(dir, a.ID); !ok || err != nil {
			t.Fatal(ok, err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, DevicesFileName))
		var compact bytes.Buffer
		compact.Write(bytes.Join(bytes.Fields(data), nil))
		if compact.String() != `{"devices":[]}` {
			t.Errorf("file = %q", data)
		}
	})
}

// TestAddDeviceRefusals: the endpoint rows are load-bearing, since a stored
// endpoint is later POSTed to by the daemon. Errors must not quote it.
func TestAddDeviceRefusals(t *testing.T) {
	cases := []struct {
		name string
		sub  Subscription
		ok   bool
	}{
		{"https", Subscription{"https://push.example/a", "k", "a"}, true},
		{"http loopback ip", Subscription{"http://127.0.0.1:8080/a", "k", "a"}, true},
		{"http localhost", Subscription{"http://localhost:1/a", "k", "a"}, true},
		{"http ipv6 loopback", Subscription{"http://[::1]:1/a", "k", "a"}, true},
		{"http remote", Subscription{"http://push.example/a", "k", "a"}, false},
		{"relative", Subscription{"/a", "k", "a"}, false},
		{"empty", Subscription{"", "k", "a"}, false},
		{"other scheme", Subscription{"file:///etc/passwd", "k", "a"}, false},
		{"user info", Subscription{"https://u:p@push.example/a", "k", "a"}, false},
		{"no host", Subscription{"https:///a", "k", "a"}, false},
		{"no p256dh", Subscription{"https://push.example/a", "", "a"}, false},
		{"no auth", Subscription{"https://push.example/a", "k", ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := AddDevice(dir, c.sub, "x", devNow, zeroRand())
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if err != nil && c.sub.Endpoint != "" && strings.Contains(err.Error(), c.sub.Endpoint) {
				t.Errorf("error quotes the endpoint: %v", err)
			}
			if !c.ok && len(ReadDevices(dir)) != 0 {
				t.Error("a refused device was kept")
			}
		})
	}
	t.Run("short random source", func(t *testing.T) {
		_, err := AddDevice(t.TempDir(), devSub("https://push.example/a"), "x", devNow, bytes.NewReader([]byte{1}))
		if err == nil {
			t.Error("want an error")
		}
	})
}

// TestLabel: the order rows are load-bearing: Edge and Chrome UAs contain
// "Safari", and iPhone and iPad UAs contain "Mac OS X". The iPadOS row pins
// the documented limit (it claims to be a Mac).
func TestLabel(t *testing.T) {
	cases := []struct{ name, ua, want string }{
		{"chrome android", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", "Chrome on Android"},
		{"safari iphone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "Safari on iPhone"},
		{"safari ipad", "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "Safari on iPad"},
		{"firefox windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0", "Firefox on Windows"},
		{"edge windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", "Edge on Windows"},
		{"chrome mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "Chrome on Mac"},
		{"safari mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", "Safari on Mac"},
		{"ipados safari claims mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", "Safari on Mac"},
		{"firefox linux", "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0", "Firefox on Linux"},
		{"chrome linux", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "Chrome on Linux"},
		{"empty", "", "Unknown browser"},
		{"curl", "curl/8.5.0", "Unknown browser"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Label(c.ua); got != c.want {
				t.Errorf("Label = %q, want %q", got, c.want)
			}
		})
	}
}
