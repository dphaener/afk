package webpush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sendTestNow is the injected clock of every Send test; the token's exp must
// follow it, not the machine's clock.
var sendTestNow = time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)

// testSubscription is a subscription for the RFC 8291 Appendix A receiver,
// whose private key the tests hold (rfcUAKey), so a request body can be
// decrypted with decryptForTest.
func testSubscription(endpoint string) Subscription {
	return Subscription{Endpoint: endpoint, P256dh: rfcUAPublic, Auth: rfcAuthSecret}
}

// testPayload is a valid notice.
var testPayload = Payload{Title: "afk", Body: "Task T15.4 is done", URL: "/p/afk/plan", Tag: "t15-4"}

// capturedRequest is what the stand-in push service saw.
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// pushServer is an httptest stand-in for a push service that answers every
// request with status and answer, and keeps what it was sent.
type pushServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []capturedRequest
}

func newPushServer(t *testing.T, status int, answer string) *pushServer {
	t.Helper()
	ps := &pushServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.reqs = append(ps.reqs, capturedRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
		ps.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pushServer) requests() []capturedRequest {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]capturedRequest(nil), ps.reqs...)
}

// testSender is a Sender with every dependency injected.
func testSender(t *testing.T, client *http.Client) Sender {
	t.Helper()
	return Sender{
		Client: client,
		Now:    func() time.Time { return sendTestNow },
		Rand:   rand.Reader,
		Key:    newTestKey(t),
	}
}

// assertNoSecrets fails if err quotes the endpoint, the Authorization header
// or the key: errors from Send end up in logs and on the page.
func assertNoSecrets(t *testing.T, err error, endpoint string, key Key) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	for _, secret := range []string{endpoint, "/push/device-1", "vapid", key.PublicKey(), rfcAuthSecret, rfcUAPublic} {
		if secret != "" && strings.Contains(msg, secret) {
			t.Errorf("error %q quotes %q", msg, secret)
		}
	}
}

// TestSendStatus covers what each push service answer means to the caller.
// The load-bearing rows are 404 and 410 against 429, 500 and 503: 404/410
// mean the subscription is gone for good and the caller removes the device,
// while 429 and 5xx are a busy or broken push service and the device must
// be kept. Mixing them up either keeps sending to dead devices forever or
// silently signs out a working phone during an outage. The closed-server
// row is a network failure, which also keeps the device. Every row checks
// the error never quotes the endpoint, the token or the keys.
func TestSendStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    int // 0: the server is closed before the send
		answer    string
		wantGone  bool
		wantErr   bool
		errSubstr string
	}{
		{name: "201 created", status: http.StatusCreated, wantGone: false},
		{name: "200 ok", status: http.StatusOK, wantGone: false},
		{name: "404 not found is gone", status: http.StatusNotFound, answer: "no such subscription", wantGone: true},
		{name: "410 gone is gone", status: http.StatusGone, answer: "push subscription has unsubscribed or expired", wantGone: true},
		{name: "429 too many requests is not gone", status: http.StatusTooManyRequests, answer: "slow down", wantErr: true, errSubstr: "the push service answered 429: slow down"},
		{name: "500 server error is not gone", status: http.StatusInternalServerError, answer: "{\"reason\":\n\"InternalServerError\"}", wantErr: true, errSubstr: "the push service answered 500: {\"reason\": \"InternalServerError\"}"},
		{name: "503 unavailable is not gone", status: http.StatusServiceUnavailable, wantErr: true, errSubstr: "the push service answered 503"},
		{name: "400 bad request is not gone", status: http.StatusBadRequest, answer: "BadJwtToken", wantErr: true, errSubstr: "the push service answered 400: BadJwtToken"},
		{name: "closed server is a transport error", status: 0, wantErr: true, errSubstr: "could not reach the push service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			if status == 0 {
				status = http.StatusCreated
			}
			ps := newPushServer(t, status, tt.answer)
			client := ps.Client()
			endpoint := ps.URL + "/push/device-1"
			if tt.status == 0 {
				ps.Close()
			}
			s := testSender(t, client)
			gone, err := s.Send(context.Background(), testSubscription(endpoint), testPayload, Options{})
			if gone != tt.wantGone {
				t.Errorf("gone = %v, want %v", gone, tt.wantGone)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("err = %q, want it to contain %q", err, tt.errSubstr)
			}
			assertNoSecrets(t, err, endpoint, s.Key)
			if tt.status != 0 && len(ps.requests()) != 1 {
				t.Errorf("push service saw %d requests, want exactly 1 (no retries)", len(ps.requests()))
			}
		})
	}
}

// TestSendErrorBodyCut checks a long error answer is cut to about 200 bytes
// and kept on one line, so a proxy's HTML page cannot flood a log.
func TestSendErrorBodyCut(t *testing.T) {
	long := strings.Repeat("x", 150) + "\r\n" + strings.Repeat("é", 200)
	ps := newPushServer(t, http.StatusBadGateway, long)
	s := testSender(t, ps.Client())
	gone, err := s.Send(context.Background(), testSubscription(ps.URL+"/push/device-1"), testPayload, Options{})
	if gone || err == nil {
		t.Fatalf("gone = %v, err = %v; want not gone and an error", gone, err)
	}
	msg := err.Error()
	prefix := "the push service answered 502: "
	if !strings.HasPrefix(msg, prefix) {
		t.Fatalf("err = %q, want prefix %q", msg, prefix)
	}
	rest := strings.TrimPrefix(msg, prefix)
	if len(rest) > errorBodyLimit+len("…") {
		t.Errorf("error carries %d bytes of the answer, want at most %d", len(rest), errorBodyLimit)
	}
	if strings.ContainsAny(msg, "\r\n") {
		t.Errorf("error %q spans more than one line", msg)
	}
	if !strings.HasSuffix(rest, "…") {
		t.Errorf("error %q does not show it was cut", msg)
	}
	if !strings.Contains(rest, "x é") {
		t.Errorf("error %q lost the start of the answer or its line break was not a space", msg)
	}
}

// TestSendHeaders checks the request a push service gets. The load-bearing
// checks are the token's aud, which must be the httptest server's origin
// (scheme, host and port, no path), and exp, which must follow the injected
// clock: a push service compares both and refuses the message otherwise.
// The rows cover the default TTL and urgency and explicit ones.
func TestSendHeaders(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		wantTTL     string
		wantUrgency string
	}{
		{"defaults are 24h and normal", Options{}, "86400", "normal"},
		{"high urgency and a short TTL", Options{TTL: 90 * time.Second, Urgency: "high"}, "90", "high"},
		{"very-low urgency and a fractional TTL drops the fraction", Options{TTL: 1500 * time.Millisecond, Urgency: "very-low"}, "1", "very-low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := newPushServer(t, http.StatusCreated, "")
			s := testSender(t, ps.Client())
			endpoint := ps.URL + "/push/device-1?q=1"
			if _, err := s.Send(context.Background(), testSubscription(endpoint), testPayload, tt.opts); err != nil {
				t.Fatalf("Send: %v", err)
			}
			reqs := ps.requests()
			if len(reqs) != 1 {
				t.Fatalf("push service saw %d requests, want 1", len(reqs))
			}
			r := reqs[0]
			if r.method != http.MethodPost || r.path != "/push/device-1" {
				t.Errorf("request = %s %s, want POST /push/device-1", r.method, r.path)
			}
			for header, want := range map[string]string{
				"Content-Type":     "application/octet-stream",
				"Content-Encoding": "aes128gcm",
				"Ttl":              tt.wantTTL,
				"Urgency":          tt.wantUrgency,
			} {
				if got := r.header.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}

			jwt, k := splitAuthorization(t, r.header.Get("Authorization"))
			if k != s.Key.PublicKey() {
				t.Errorf("k = %q, want Key.PublicKey()", k)
			}
			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Fatalf("JWT has %d parts, want 3", len(parts))
			}
			enc := base64.RawURLEncoding
			rawPub, err := enc.DecodeString(s.Key.PublicKey())
			if err != nil {
				t.Fatal(err)
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), rawPub)
			if err != nil {
				t.Fatal(err)
			}
			sig, err := enc.DecodeString(parts[2])
			if err != nil || len(sig) != 64 {
				t.Fatalf("signature is not 64 raw bytes (err %v)", err)
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
				t.Error("JWT does not verify with Key.PublicKey()")
			}
			body, err := enc.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var claims struct {
				Aud string `json:"aud"`
				Exp int64  `json:"exp"`
				Sub string `json:"sub"`
			}
			if err := json.Unmarshal(body, &claims); err != nil {
				t.Fatal(err)
			}
			if claims.Aud != ps.URL {
				t.Errorf("aud = %q, want the server's origin %q", claims.Aud, ps.URL)
			}
			if want := sendTestNow.Add(12 * time.Hour).Unix(); claims.Exp != want {
				t.Errorf("exp = %d, want %d (12h after the injected clock)", claims.Exp, want)
			}
			if claims.Sub != DefaultSubject {
				t.Errorf("sub = %q, want the default %q", claims.Sub, DefaultSubject)
			}
		})
	}
}

// TestSendSubject checks a set Subject is the token's sub, not the default.
func TestSendSubject(t *testing.T) {
	ps := newPushServer(t, http.StatusCreated, "")
	s := testSender(t, ps.Client())
	s.Subject = "mailto:owner@example.com"
	if _, err := s.Send(context.Background(), testSubscription(ps.URL+"/p"), testPayload, Options{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	jwt, _ := splitAuthorization(t, ps.requests()[0].header.Get("Authorization"))
	body, err := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"sub":"mailto:owner@example.com"`) {
		t.Errorf("claims = %s, want the set subject", body)
	}
}

// TestSendDecrypts checks the body is the notice, encrypted for the device:
// the RFC 8291 receiver in encrypt_test.go, holding the subscription's
// private key, recovers exactly the JSON payload the service worker reads.
func TestSendDecrypts(t *testing.T) {
	ps := newPushServer(t, http.StatusCreated, "")
	s := testSender(t, ps.Client())
	if _, err := s.Send(context.Background(), testSubscription(ps.URL+"/push/device-1"), testPayload, Options{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	plain, err := decryptForTest(ps.requests()[0].body, rfcUAKey(t), b64(t, rfcAuthSecret))
	if err != nil {
		t.Fatalf("decrypting the request body: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("payload %q is not JSON: %v", plain, err)
	}
	want := map[string]string{"title": "afk", "body": "Task T15.4 is done", "url": "/p/afk/plan", "tag": "t15-4"}
	if len(got) != len(want) {
		t.Errorf("payload = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// TestSendRefusals covers what Send refuses before any request: each row
// must leave the push service untouched, since a refused notice that went
// out anyway (or half-built) is worse than one that did not. The urgency row
// is load-bearing: an unknown Urgency header would be passed on by some
// push services and refused by others.
func TestSendRefusals(t *testing.T) {
	tests := []struct {
		name      string
		sub       func(url string) Subscription
		payload   Payload
		opts      Options
		key       bool
		errSubstr string
	}{
		{name: "unknown urgency", opts: Options{Urgency: "urgent"}, key: true, errSubstr: "urgency must be very-low, low, normal or high"},
		{name: "negative TTL", opts: Options{TTL: -time.Second}, key: true, errSubstr: "cannot be negative"},
		{name: "payload url is a full address", payload: Payload{Title: "x", URL: "https://evil.example/"}, key: true, errSubstr: "path on the page"},
		{name: "bad p256dh", sub: func(u string) Subscription { return Subscription{Endpoint: u, P256dh: "AAAA", Auth: rfcAuthSecret} }, key: true, errSubstr: "p256dh"},
		{name: "endpoint not http", sub: func(string) Subscription { return testSubscription("ftp://push.example/device-1") }, key: true, errSubstr: "not an absolute https or http address"},
		{name: "no key", key: false, errSubstr: "no push key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()
			endpoint := srv.URL + "/push/device-1"
			sub := testSubscription(endpoint)
			if tt.sub != nil {
				sub = tt.sub(endpoint)
			}
			p := testPayload
			if tt.payload != (Payload{}) {
				p = tt.payload
			}
			s := testSender(t, srv.Client())
			if !tt.key {
				s.Key = Key{}
			}
			gone, err := s.Send(context.Background(), sub, p, tt.opts)
			if gone || err == nil {
				t.Fatalf("gone = %v, err = %v; want a refusal", gone, err)
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("err = %q, want it to contain %q", err, tt.errSubstr)
			}
			assertNoSecrets(t, err, endpoint, s.Key)
			if n := hits.Load(); n != 0 {
				t.Errorf("push service saw %d requests, want none", n)
			}
		})
	}
}

// TestSendCancelled checks a cancelled context is a not-gone error, so a
// daemon shutting down mid-send never removes a device.
func TestSendCancelled(t *testing.T) {
	ps := newPushServer(t, http.StatusCreated, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := testSender(t, ps.Client())
	gone, err := s.Send(ctx, testSubscription(ps.URL+"/push/device-1"), testPayload, Options{})
	if gone || err == nil {
		t.Fatalf("gone = %v, err = %v; want not gone and an error", gone, err)
	}
	assertNoSecrets(t, err, ps.URL+"/push/device-1", s.Key)
}
