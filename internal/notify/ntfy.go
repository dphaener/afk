package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxNtfyBody is the longest message body sent. ntfy turns a message over
// 4,096 bytes into an attachment (docs.ntfy.sh/publish), so the body is cut
// well below that to stay a readable message.
const maxNtfyBody = 3500

// NtfyPriority says whether an inbox item should push a notification and how
// loudly (ntfy priorities run 1 to 5). It is pure and takes plain values so
// this package never imports internal/inbox. A blocking item always notifies:
// a parked task is waiting on the owner. A non-blocking question or notice
// never does, because assumed answers arrive by the dozen.
func NtfyPriority(kind string, blocking bool) (priority int, send bool) {
	switch kind {
	case "failure":
		if blocking {
			return 5, true
		}
		return 4, true
	case "env_var", "external_action":
		if blocking {
			return 4, true
		}
		return 3, true
	case "verification":
		if blocking {
			return 4, true
		}
		return 2, true
	}
	// question, notice and any unknown kind.
	if blocking {
		return 4, true
	}
	return 0, false
}

// NtfyMessage is one push notification. Click, when set, is sent as ntfy's
// Click header, which is the address ntfy opens when the notification is
// tapped. It is empty when the owner has not set a public web address, and
// then no header is sent at all.
type NtfyMessage struct {
	Title    string
	Body     string
	Priority int
	Click    string
}

// Ntfy publishes messages to one ntfy topic URL.
type Ntfy struct {
	URL    string
	Client *http.Client // nil means a client with a 10 second timeout
}

// Send makes one POST to n.URL. It never retries, and no error it returns
// contains URL credentials: the caller prints these errors to a terminal and
// logs, and a topic URL may carry user:password.
func (n Ntfy) Send(ctx context.Context, m NtfyMessage) error {
	u, err := url.Parse(n.URL)
	if err != nil {
		// *url.Error would echo the raw input, credentials included.
		return errors.New("the ntfy address is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("the ntfy address must start with http:// or https://")
	}
	if u.Host == "" {
		return errors.New("the ntfy address has no host name")
	}
	safe := u.Redacted()

	prio := m.Priority
	if prio < 1 || prio > 5 {
		prio = 3
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(cutBody(m.Body)))
	if err != nil {
		return fmt.Errorf("could not build the request to %s", safe)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Priority", strconv.Itoa(prio))
	req.Header.Set("Title", headerTitle(m.Title))
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}

	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the embedded raw URL
		}
		return fmt.Errorf("could not reach %s: %v", safe, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg := fmt.Sprintf("%s answered %d", safe, resp.StatusCode)
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		msg += ": " + body.Error
	}
	return errors.New(msg)
}

// headerTitle makes a title safe for an HTTP header: line breaks become
// spaces (a CR or LF would end the header), and a non-ASCII title is RFC 2047
// encoded since raw UTF-8 in a header is not reliably read.
func headerTitle(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return mime.BEncoding.Encode("UTF-8", s)
		}
	}
	return s
}

// ItemLink is the web address of one inbox item, for a notification's Click
// header. It is empty when there is no public address, or no project or item
// to point at, so a notification without a configured address carries no
// link. The address holds no token and no query string: the web page's cookie
// (or a typed ?token= on first visit) is what authenticates the tap.
func ItemLink(publicURL, project, id string) string {
	base := trimBase(publicURL)
	if base == "" || project == "" || id == "" {
		return ""
	}
	return base + "/p/" + url.PathEscape(project) + "/inbox/" + url.PathEscape(id)
}

// ProjectLink is the web address of one project's page, or "" when there is no
// public address or no project name.
func ProjectLink(publicURL, project string) string {
	base := trimBase(publicURL)
	if base == "" || project == "" {
		return ""
	}
	return base + "/p/" + url.PathEscape(project)
}

// trimBase drops one trailing slash so the link builders never produce a
// double slash.
func trimBase(publicURL string) string {
	return strings.TrimSuffix(publicURL, "/")
}

// cutBody trims s to at most maxNtfyBody bytes without splitting a character.
func cutBody(s string) string {
	if len(s) <= maxNtfyBody {
		return s
	}
	cut := maxNtfyBody
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// InboxMessage decides whether a newly added inbox item sends a push
// notification, and builds it. It is pure so the whole "who gets bothered,
// and with what" table is testable without a server. It takes the item's
// fields rather than an inbox.Item so this package does not import
// internal/inbox.
//
// No address configured is silence, not an error: most projects have no
// afk.toml. Which kinds notify, and how loudly, is NtfyPriority's table. The
// message ends with the command that shows the item, because a notification
// is read on a phone and acted on at a terminal later.
func InboxMessage(id, task, kind, title, body string, blocking bool, ntfyURL string) (NtfyMessage, bool) {
	if ntfyURL == "" {
		return NtfyMessage{}, false
	}
	prio, ok := NtfyPriority(kind, blocking)
	if !ok {
		return NtfyMessage{}, false
	}
	if body != "" {
		body += "\n\n"
	}
	body += "afk inbox show " + id
	if task != "" {
		body += " (" + task + ")"
	}
	return NtfyMessage{Title: "afk: " + title, Body: body, Priority: prio}, true
}
