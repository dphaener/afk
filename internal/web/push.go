package web

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/webpush"
)

// This file is the server side of the Notifications page
// (docs/design-system.md § Notifications): the page itself with its This
// device card and signed-up devices list, and the four JSON addresses its
// script calls to sign this browser up for web push, take it off again, send
// it one test notification and ask whether it is still listed.
//
// The four POSTs are under /api only. The page needs JavaScript to subscribe
// at all (a push subscription is made by the browser's PushManager, which a
// form cannot reach), so they have no form twin and stay out of routes, whose
// entries each get a page address too. The guard covers them exactly as every
// other POST: the cookie, then checkPost's Origin check and size limit, before
// any handler here runs.
//
// Removing a device from the list is the exception: it needs no
// PushManager, so it is a form POST in routes (POST /notifications/remove,
// with its /api twin) behind a confirm, and a no-JavaScript confirm page
// leads to it like every other confirmed action.
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
	// Devices is every signed-up device, this device first, then the
	// newest sign-up first (deviceViews). Never null, so a script reads an
	// empty list as an empty array rather than checking for null.
	Devices []DeviceView `json:"devices"`
	// RemoveSheet is the one confirm dialog every row's Remove opens. It is
	// drawn once, outside the live list, because live.js never swaps a
	// region holding a form, and the script fills its question and id for
	// the row pressed. The page alone needs it, so the JSON leaves it out.
	RemoveSheet sessionSheet `json:"-"`
}

// DeviceView is one row of the signed-up devices list. It carries no
// endpoint and no keys: the endpoint is the device's capability URL, and the
// list is drawn into HTML and sent to every signed-in page, so only what the
// owner reads (and the id that names the row) leaves the server.
type DeviceView struct {
	// ID is the device's id in push-devices.json, the handle a removal
	// names.
	ID string `json:"id"`
	// Label is how the device was named when it signed up ("Chrome on
	// Android").
	Label string `json:"label"`
	// At is when it signed up, RFC 3339 as stored.
	At string `json:"at"`
	// SignedUp is At as the page shows it ("Oct 2, 2026", in UTC), or ""
	// when At cannot be read.
	SignedUp string `json:"signed_up"`
	// ThisDevice marks the device the request came from, known by the
	// device cookie (deviceCookieName).
	ThisDevice bool `json:"this_device"`
}

// deviceCookieName is the cookie that tells the server which listed device a
// browser is. It holds the device's id and nothing else, never the endpoint:
// the id is useless without the signed-in cookie, while the endpoint would let
// anyone holding it and afk's key push to the device.
//
// The server has to know this itself, rather than leave it to the page's
// script, because the list is redrawn by the server on every live re-fetch
// (live.js swaps the region from a fresh GET): a mark only the script drew
// would vanish at the first swap.
const deviceCookieName = "afk_push_device"

// deviceCookie builds the device cookie holding id; an empty id builds the
// one that clears it. Secure follows the sign-in cookie (server.cookie) for
// the same reason: a browser drops a Secure cookie that arrived over plain
// HTTP. SameSite is Strict, unlike the sign-in cookie, because nothing needs
// it on a cross-site arrival: a tap on a notification that lands here
// without it only shows the list unmarked until the page's own requests,
// which are same-site, carry it again.
//
// Its life is the sign-in cookie's: a session cookie would be gone after the
// browser restarts, and the device would stop being marked although it is
// still signed up.
func (s *server) deviceCookie(id string) *http.Cookie {
	c := &http.Cookie{
		Name:     deviceCookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	}
	if id == "" {
		c.MaxAge = -1
	}
	return c
}

// thisDeviceID is the device id the request's device cookie names, or "".
// It is only ever compared with the listed ids, so a made-up value marks
// nothing.
func thisDeviceID(r *http.Request) string {
	c, err := r.Cookie(deviceCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// deviceViews is the device list as the page shows it. Pure.
//
// The device whose id is thisID comes first, since it is the one the owner is
// holding; the rest are newest sign-up first. A time that cannot be parsed
// sorts after every readable one, and ties keep the file's order, so the list
// never reshuffles between two reads of the same file (a live re-fetch would
// otherwise flash rows that did not change). An empty thisID, or one no device
// has, marks nothing. The result is never nil.
func deviceViews(devs []webpush.Device, thisID string) []DeviceView {
	type keyed struct {
		v  DeviceView
		at time.Time
		ok bool
	}
	rows := make([]keyed, 0, len(devs))
	for _, d := range devs {
		k := keyed{v: DeviceView{ID: d.ID, Label: d.Label, At: d.At}}
		if t, err := time.Parse(time.RFC3339, d.At); err == nil {
			k.at, k.ok = t, true
			k.v.SignedUp = t.UTC().Format("Jan 2, 2006")
		}
		k.v.ThisDevice = thisID != "" && d.ID == thisID
		rows = append(rows, k)
	}
	slices.SortStableFunc(rows, func(a, b keyed) int {
		switch {
		case a.v.ThisDevice != b.v.ThisDevice:
			if a.v.ThisDevice {
				return -1
			}
			return 1
		case a.ok != b.ok:
			if a.ok {
				return -1
			}
			return 1
		case !a.ok:
			return 0
		}
		return cmp.Compare(b.at.UnixNano(), a.at.UnixNano())
	})
	out := make([]DeviceView, len(rows))
	for i, k := range rows {
		out[i] = k.v
	}
	return out
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
	v := NotificationsView{
		Label:       webpush.Label(r.UserAgent()),
		Devices:     deviceViews(webpush.ReadDevices(s.d.PushDir), thisDeviceID(r)),
		RemoveSheet: deviceRemoveSheet("", "", false),
	}
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
	// The cookie names the entry just kept, so this browser's row is marked
	// from now on. AddDevice keeps an existing entry's id when the endpoint
	// was listed already, so dev.ID is the listed id either way.
	http.SetCookie(w, s.deviceCookie(dev.ID))
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
	// The browser is off the list whether or not this call removed it, so it
	// is no longer any listed device.
	http.SetCookie(w, s.deviceCookie(""))
	writeJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

// pushDevice is POST /api/push/device: is the subscription this browser holds
// still on the list, and which device is it? The page asks on load, because
// a device can be removed while it is away (from another device's list, or
// by a gone answer to a send), and a browser still holding its subscription
// would otherwise show On for a device afk no longer sends to.
//
// Listed, it answers the id and sets the device cookie, which is also how a
// browser that signed up before the cookie existed, or lost it, gets marked.
// Not listed, it clears the cookie. Neither is an error: both are the
// expected answers to the question. It writes nothing to the device list.
func (s *server) pushDevice(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	var body endpointBody
	if !s.readPushJSON(w, r, &body, &body.Endpoint) {
		return
	}
	for _, d := range webpush.ReadDevices(s.d.PushDir) {
		if d.Endpoint == body.Endpoint {
			http.SetCookie(w, s.deviceCookie(d.ID))
			writeJSON(w, http.StatusOK, map[string]any{"listed": true, "id": d.ID})
			return
		}
	}
	http.SetCookie(w, s.deviceCookie(""))
	writeJSON(w, http.StatusOK, map[string]bool{"listed": false})
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

// The sentences removing a device says. They name the device only by the
// label read from the list, never by anything the request carried.
const (
	deviceRemoveNoConfirm = "Nothing changed: removing a device needs the confirm. Open the Notifications page and press Remove beside the device."
	deviceRemoveUnread    = `Nothing changed: the request could not be read. Send it as JSON like {"id": "3f9a...", "confirm": true}.`
	deviceRemoveNoID      = "Nothing changed: no device was named, so there is nothing to remove. Choose one from the list on the Notifications page."
	// deviceRemoveOther and deviceRemoveThis are what removing does, for a
	// device the owner is not holding and for the one they are. Another
	// device is told how to come back, since removing it does not stop the
	// browser there from asking again; this device needs no such line,
	// because its own Turn on is on the same page.
	deviceRemoveOther = "It stops getting notifications. To get them again, open afk on it and turn them on."
	deviceRemoveThis  = "This device stops getting notifications."
)

// deviceRemoveSheet is the remove question for one device: the shared dialog
// on the Notifications page and the no-JavaScript confirm page are drawn from
// it, so the two say the same thing. With no id it is the dialog's starting
// words, which the page's script replaces for the row pressed (W3.T1); its
// hidden id is then empty, and a POST of it is refused for naming no device.
func deviceRemoveSheet(id, label string, thisDevice bool) sessionSheet {
	sh := sessionSheet{
		ID: "push-remove-sheet", Action: "remove",
		Title:  "Remove this device?",
		Body:   []sheetLine{{Text: deviceRemoveOther}},
		Button: "Remove", Loading: "Removing", Danger: true, Safe: "Keep it",
		Path: "/notifications/remove", Page: "/notifications/remove",
		Fields: []sheetField{{Name: "id", Value: id}},
	}
	if id == "" {
		return sh
	}
	sh.Title = "Remove " + label + "?"
	if thisDevice {
		sh.Body = []sheetLine{{Text: deviceRemoveThis}}
	}
	return sh
}

// NotificationsRemoveConfirmView is GET /notifications/remove?id=, the page
// behind a row's Remove for a browser without JavaScript: the question the
// shared dialog asks, drawn from the same sheet.
type NotificationsRemoveConfirmView struct {
	ID, Label  string
	ThisDevice bool
	Sheet      sessionSheet
}

// removeDevicePage is GET /notifications/remove?id=. The id must be on the
// device list now, and the label shown is the list's, never the query's, so
// the address cannot make the page name a device afk does not have. It is
// HTML only, so it is not in routes and gets no /api twin.
func (s *server) removeDevicePage(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		s.notFound(w, r)
		return
	}
	for _, d := range webpush.ReadDevices(s.d.PushDir) {
		if d.ID == id {
			this := id == thisDeviceID(r)
			s.renderPage(w, r, http.StatusOK, "notifications_remove_confirm",
				NotificationsRemoveConfirmView{ID: d.ID, Label: d.Label, ThisDevice: this, Sheet: deviceRemoveSheet(d.ID, d.Label, this)})
			return
		}
	}
	s.notFound(w, r)
}

// removeDevice is POST /notifications/remove and /api/notifications/remove:
// take one device off the list by its id. It is in routes, unlike the push
// POSTs, because it has a form twin: the confirm page posts it without
// JavaScript.
//
// The confirm is read exactly as removing a project reads it
// (readProjectEdit), so following a link can never remove a device. An id no
// device has is not an error: the owner's press, a second tab and a gone
// answer to a send all race to remove the same entry, and the device is off
// the list whichever won. The id is only ever compared with the listed ids,
// so a made-up one removes nothing.
func (s *server) removeDevice(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		s.notFound(w, r)
		return
	}
	b, ok := s.readProjectEdit(w, r, deviceRemoveNoConfirm)
	if !ok {
		return
	}
	id, okID := bodyField(b, "id")
	if !okID {
		s.fail(w, r, http.StatusBadRequest, deviceRemoveUnread)
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		s.fail(w, r, http.StatusBadRequest, deviceRemoveNoID)
		return
	}
	removed, err := webpush.RemoveDevice(s.d.PushDir, id)
	if err != nil {
		// RemoveDevice's errors name the state folder, never an endpoint.
		s.fail(w, r, http.StatusInternalServerError, "The device list could not be saved, so nothing was removed: "+err.Error()+". Try again in a minute.")
		return
	}
	// Removing the device this browser is leaves its cookie naming an id
	// that is gone; clear it, as unsubscribing does, so no later row is
	// marked as this device by a stale id.
	if id == thisDeviceID(r) {
		http.SetCookie(w, s.deviceCookie(""))
	}
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusOK, map[string]bool{"removed": removed})
		return
	}
	// The code is fixed text chosen from the result, never from the request,
	// so the toast it leads to echoes nothing.
	code := "device-removed"
	if !removed {
		code = "device-gone"
	}
	w.Header().Set("Location", "/notifications?done="+code)
	w.WriteHeader(http.StatusSeeOther)
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
