package webpush

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DefaultSubject is the VAPID contact sent when Sender.Subject is empty.
// Apple's push service refuses a token with no sub claim, so there is always
// one; T15.7 can pass the page's public address instead (docs/decisions.md
// T15.4, A3).
const DefaultSubject = "https://github.com/dphaener/afk"

// defaultTTL is how long a push service keeps a notice for a device that is
// offline when Options.TTL is zero. A day covers a phone left off overnight
// while not delivering yesterday's news a week late.
const defaultTTL = 24 * time.Hour

// sendTimeout is the whole-request timeout of the client used when
// Sender.Client is nil, the same as the ntfy sender in internal/notify: long
// enough for a slow push service, short enough that a hung one cannot hold
// up the daemon's pass.
const sendTimeout = 10 * time.Second

// errorBodyLimit is how much of a push service's error answer goes into
// Send's error. Push services explain a refusal in a line or two (FCM and
// Mozilla send short JSON, Apple a reason), and the error ends up in logs
// and on the page, so a page of HTML from a proxy is cut short.
const errorBodyLimit = 200

// drainLimit bounds how much of an answer's body Send reads before closing
// it. Reading the body to the end lets the client reuse the connection; the
// bound keeps a push service that streams forever from holding Send.
const drainLimit = 64 << 10

// The urgencies RFC 8030 §5.3 defines, in its order.
var urgencies = map[string]bool{"very-low": true, "low": true, "normal": true, "high": true}

// Sender sends notices to push services, signed with afk's VAPID key.
//
// Every dependency is a field so tests can stand in for it: Client for the
// network (an httptest server's client), Now for the token's expiry, Rand
// for the message keys. The zero value of each is the real thing.
type Sender struct {
	// Client makes the request. nil means a client with a 10 second
	// timeout, as the ntfy sender uses.
	Client *http.Client
	// Now is the clock the VAPID token's expiry counts from. nil means
	// time.Now.
	Now func() time.Time
	// Rand supplies each message's ephemeral key and salt. nil means
	// crypto/rand.Reader.
	Rand io.Reader
	// Key is afk's VAPID key, from LoadOrCreateKey. It must be the key the
	// device subscribed with, or the push service refuses the message.
	Key Key
	// Subject is the VAPID sub claim, a contact for the sender ("mailto:"
	// or "https:"). Empty means DefaultSubject.
	Subject string
}

// Options are the per-message RFC 8030 headers.
type Options struct {
	// TTL is how long the push service keeps the message for a device that
	// is offline, sent in whole seconds (a fraction of a second is dropped).
	// 0 means 24 hours; a negative TTL is an error.
	TTL time.Duration
	// Urgency is one of "very-low", "low", "normal" or "high" (RFC 8030
	// §5.3); a phone on low battery may hold back anything below high.
	// "" means normal.
	Urgency string
}

// Send encrypts p for the one device behind sub and POSTs it to the
// device's push service, once. It never retries: the caller knows whether a
// notice is worth sending again, and a retry loop here would hold the
// daemon's pass for a push service that is down.
//
// The result is one of three:
//
//   - (false, nil): the push service took the message (any 2xx).
//   - (true, nil): the push service answered 404 or 410, which RFC 8030
//     §7.3 and every big push service use to say the subscription is gone
//     for good (the browser unsubscribed, or the app was removed). The
//     caller removes the device. Gone is not an error, so callers branch on
//     gone alone and an error always means "keep the device, it may work
//     next time".
//   - (false, err): anything else, including a refusal before any request
//     (a bad urgency, payload or subscription), a 429 or 5xx, and a
//     network failure. The device stays signed up.
//
// No error carries the endpoint, the Authorization header or the key: the
// endpoint is the device's capability URL, and the errors end up in logs
// and on the page.
func (s Sender) Send(ctx context.Context, sub Subscription, p Payload, o Options) (gone bool, err error) {
	urgency := o.Urgency
	if urgency == "" {
		urgency = "normal"
	}
	if !urgencies[urgency] {
		return false, errors.New("the push urgency must be very-low, low, normal or high")
	}
	ttl := o.TTL
	if ttl < 0 {
		return false, errors.New("the push time to live cannot be negative")
	}
	if ttl == 0 {
		ttl = defaultTTL
	}
	if err := p.Validate(); err != nil {
		return false, err
	}
	plaintext, err := json.Marshal(p)
	if err != nil {
		return false, fmt.Errorf("encoding the notice: %w", err)
	}

	rnd := s.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	body, err := Encrypt(sub, plaintext, rnd)
	if err != nil {
		return false, err
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	subject := s.Subject
	if subject == "" {
		subject = DefaultSubject
	}
	// vapidAuthorization also checks the endpoint is an absolute http(s)
	// URL, so nothing below sends to anything else.
	auth, err := vapidAuthorization(s.Key, sub.Endpoint, subject, now())
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		// NewRequest's error quotes the URL, so it is dropped.
		return false, errors.New("could not build the request to the push service")
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	req.Header.Set("Urgency", urgency)
	req.Header.Set("Authorization", auth)

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: sendTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error quotes the endpoint; keep only what went wrong.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return false, fmt.Errorf("could not reach the push service: %v", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit+1))
	// Read the rest (bounded) so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return true, nil
	}
	msg := fmt.Sprintf("the push service answered %d", resp.StatusCode)
	if text := errorText(answer); text != "" {
		msg += ": " + text
	}
	return false, errors.New(msg)
}

// errorText makes the start of a push service's answer fit for one line of
// an error: at most errorBodyLimit bytes, cut on a character boundary, with
// line breaks and other control characters turned into spaces so a log line
// stays one line, and the ends trimmed.
func errorText(b []byte) string {
	cut := len(b) > errorBodyLimit
	if cut {
		b = b[:errorBodyLimit]
	}
	// A multi-byte character split by the cut, or a body that is not
	// text at all, leaves invalid UTF-8, which is dropped rather than
	// carried into a log line.
	s := strings.ToValidUTF8(string(b), "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if s != "" && cut {
		s += "…"
	}
	return s
}
