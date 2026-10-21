package web

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/webpush"
)

// This file is the server side of the Notifications page's This device card
// (docs/design-system.md § Notifications): the page itself, and the three
// JSON addresses its script calls to sign this browser up for web push, take
// it off again and send it one test notification.
//
// The three POSTs are under /api only. The page needs JavaScript to subscribe
// at all (a push subscription is made by the browser's PushManager, which a
// form cannot reach), so they have no form twin and stay out of routes, whose
// entries each get a page address too. The guard covers them exactly as every
// other POST: the cookie, then checkPost's Origin check and size limit, before
// any handler here runs.
//
// Nothing here logs, and no answer echoes an endpoint, a key or a cookie: a
// push endpoint is the device's capability URL, and anyone holding it with
// afk's key could send that device notifications.

// testPayload is the one notification Send test sends. Its tag lets a second
// press replace the first on the device instead of stacking, and its url
// brings a tap back to this page.
var testPayload = webpush.Payload{
	Title: "afk",
	Body:  "Test notification. If you can read this, notifications from afk reach this device.",
	URL:   "/notifications",
	Tag:   "afk-test",
}

// testOptions keep the test short-lived: a test that arrives an hour later,
// after the owner gave up on it, would only confuse.
var testOptions = webpush.Options{TTL: time.Minute, Urgency: "normal"}

// NotificationsView is GET /notifications and /api/notifications: what the
// page's script needs to sign this browser up.
type NotificationsView struct {
	// PublicKey is afk's VAPID public key, the applicationServerKey the
	// browser subscribes with. Empty when KeyError is set.
	PublicKey string `json:"public_key"`
	// Label is how this browser is named in the device list ("Chrome on
	// Android"), worked out from the request's User-Agent.
	Label string `json:"label"`
	// KeyError says, in plain words and naming the file, why the push key
	// could not be read or made. The page shows it in place of the turn-on
	// button: a damaged key is the owner's to fix (webpush.LoadOrCreateKey
	// will not replace it), and a 500 page would hide which file it is.
	KeyError string `json:"key_error"`
}

// pushOn reports whether this server can do web push at all. Both seams are
// needed: without the folder there is no key or device list, and without the
// sender a test could be promised and never sent.
func (s *server) pushOn() bool {
	return s.d.PushDir != "" && s.d.PushSend != nil
}

// notifications is GET /notifications and /api/notifications. It makes the
// push key on first use, which is a write on a GET; that is deliberate, since
// the page cannot offer Turn on without a key, and making it is idempotent
// (LoadOrCreateKey reuses the file once it exists).
func (s *server) notifications(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	v := NotificationsView{Label: webpush.Label(r.UserAgent())}
	key, err := webpush.LoadOrCreateKey(s.d.PushDir, rand.Reader, s.d.Now())
	if err != nil {
		v.KeyError = "Notifications cannot be turned on, because afk's push key (" +
			filepath.Join(s.d.PushDir, webpush.KeyFileName) + ") could not be read or made: " + err.Error() + "."
	} else {
		v.PublicKey = key.PublicKey()
	}
	s.respond(w, r, http.StatusOK, "notifications", v)
}

// subscribeBody is the shape of PushSubscription.toJSON(), which the page
// posts as it is. expirationTime and anything else in it is ignored.
type subscribeBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// endpointBody is the body of unsubscribe and test: which device is meant.
type endpointBody struct {
	Endpoint string `json:"endpoint"`
}

// readPushJSON reads a push POST's JSON body into v, whose endpoint field
// endpoint points at. It answers and returns
// false on a body over the limit (413, as on every POST), a body that is not
// JSON, or one with no endpoint (400). A form body is refused rather than
// read, because the page's script always sends JSON and nothing else posts
// here.
func (s *server) readPushJSON(w http.ResponseWriter, r *http.Request, v any, endpoint *string) bool {
	b, err := readPostBody(r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return false
		}
	}
	if err != nil || b.contentType != "application/json" || json.Unmarshal(b.json, v) != nil || *endpoint == "" {
		s.fail(w, r, http.StatusBadRequest, "The request did not name a push subscription this page can read. Reload the page and try again.")
		return false
	}
	return true
}

// pushSubscribe is POST /api/push/subscribe: keep this browser's
// subscription in push-devices.json. A browser that subscribes again (the
// same endpoint) replaces its old entry, so pressing Turn on twice never
// lists a device twice.
func (s *server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	var body subscribeBody
	if !s.readPushJSON(w, r, &body, &body.Endpoint) {
		return
	}
	sub := webpush.Subscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth}
	dev, err := webpush.AddDevice(s.d.PushDir, sub, webpush.Label(r.UserAgent()), s.d.Now(), rand.Reader)
	if err != nil {
		// AddDevice's errors never quote the endpoint. A refused shape and a
		// failed write both land here as 400: the page shows the sentence
		// either way, and telling them apart would mean matching its text.
		s.fail(w, r, http.StatusBadRequest, "This browser could not be signed up: "+scrub(err.Error(), body.Endpoint)+". Turn notifications off and on again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": dev.ID})
}

// pushUnsubscribe is POST /api/push/unsubscribe: take this browser off the
// list. An endpoint that is not listed is not an error, because the page
// turns notifications off whether or not the server still had it (a gone
// answer to an earlier send may have removed it already).
func (s *server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	var body endpointBody
	if !s.readPushJSON(w, r, &body, &body.Endpoint) {
		return
	}
	removed, err := webpush.RemoveEndpoint(s.d.PushDir, body.Endpoint)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "The device list could not be saved: "+scrub(err.Error(), body.Endpoint)+". Try again in a minute.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

// pushTest is POST /api/push/test: send one test notification to the device
// the request names, and to no other. The endpoint is only ever used to find
// a listed device, never sent to as given: a page that could push to any
// address it was handed would be a way to make afk sign requests for others.
func (s *server) pushTest(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	var body endpointBody
	if !s.readPushJSON(w, r, &body, &body.Endpoint) {
		return
	}
	var dev webpush.Device
	found := false
	for _, d := range webpush.ReadDevices(s.d.PushDir) {
		if d.Endpoint == body.Endpoint {
			dev, found = d, true
			break
		}
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, "This device is not signed up for notifications. Turn them on first.")
		return
	}
	gone, err := s.d.PushSend(r.Context(), dev.Subscription(), testPayload, testOptions)
	switch {
	case gone:
		// The push service says the subscription is over for good, so
		// keeping it would only fail again. A failed removal is not
		// reported: the answer is the same, and the daemon's next gone
		// answer removes it.
		_, _ = webpush.RemoveEndpoint(s.d.PushDir, dev.Endpoint)
		s.fail(w, r, http.StatusGone, "The push service says this device is no longer signed up. Turn notifications on again.")
	case err != nil:
		s.fail(w, r, http.StatusBadGateway, "The test could not be sent: "+scrub(err.Error(), dev.Endpoint)+". Try again in a minute.")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"outcome": "sent"})
	}
}

// scrub takes the endpoint out of an error's text before it is shown.
// webpush's own errors never carry it, but PushSend is a seam and its error
// reaches the page, so the rule "never echo an endpoint" is kept here rather
// than trusted to every sender.
func scrub(text, endpoint string) string {
	if endpoint == "" {
		return text
	}
	return strings.ReplaceAll(text, endpoint, "this device's push address")
}
