package notify

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// TestNtfyPriority pins the whole kind x blocking table. Load-bearing rows:
// every blocking row notifies (a parked task must never be silent), and the
// non-blocking question and notice rows do not (assumed answers arrive by the
// dozen, so notifying on them would train the owner to ignore the topic). The
// unknown-kind rows keep a future kind from going quiet when it blocks.
func TestNtfyPriority(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		blocking bool
		wantPrio int
		wantSend bool
	}{
		{"failure blocking", "failure", true, 5, true},
		{"failure not blocking", "failure", false, 4, true},
		{"question blocking", "question", true, 4, true},
		{"question not blocking", "question", false, 0, false},
		{"env_var blocking", "env_var", true, 4, true},
		{"env_var not blocking", "env_var", false, 3, true},
		{"external_action blocking", "external_action", true, 4, true},
		{"external_action not blocking", "external_action", false, 3, true},
		{"verification blocking", "verification", true, 4, true},
		{"verification not blocking", "verification", false, 2, true},
		{"notice blocking", "notice", true, 4, true},
		{"notice not blocking", "notice", false, 0, false},
		{"unknown blocking", "mystery", true, 4, true},
		{"unknown not blocking", "mystery", false, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := NtfyPriority(tt.kind, tt.blocking)
			if p != tt.wantPrio || ok != tt.wantSend {
				t.Errorf("NtfyPriority(%q, %v) = (%d, %v), want (%d, %v)", tt.kind, tt.blocking, p, ok, tt.wantPrio, tt.wantSend)
			}
		})
	}
}

type ntfyRecorded struct {
	method, path, contentType, priority, title string
	body                                       string
}

func ntfyServer(t *testing.T, status int, reply string) (*httptest.Server, *[]ntfyRecorded, *int32) {
	t.Helper()
	var got []ntfyRecorded
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		b, _ := io.ReadAll(r.Body)
		got = append(got, ntfyRecorded{
			method: r.Method, path: r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			priority:    r.Header.Get("Priority"),
			title:       r.Header.Get("Title"),
			body:        string(b),
		})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &count
}

// TestNtfySend checks the one request that goes out. Load-bearing rows: the
// exact-one-request assertion (no retry), the RFC 2047 title (a raw UTF-8
// header is not reliably read by ntfy), the newline strip (a CR or LF in a
// header value is header injection), the 3,500 byte cut on a rune boundary
// (ntfy turns a message over 4,096 bytes into an attachment), and the
// priority clamp.
func TestNtfySend(t *testing.T) {
	tests := []struct {
		name      string
		msg       NtfyMessage
		wantPrio  string
		wantTitle string // decoded
		wantBody  func(string) bool
	}{
		{"plain", NtfyMessage{Title: "afk: Set KEY", Body: "hello", Priority: 4}, "4", "afk: Set KEY",
			func(b string) bool { return b == "hello" }},
		{"newline in title", NtfyMessage{Title: "a\r\nb\nc", Body: "x", Priority: 5}, "5", "a  b c",
			func(b string) bool { return b == "x" }},
		{"non-ascii title", NtfyMessage{Title: "afk: déploiement ✓", Body: "x", Priority: 3}, "3", "afk: déploiement ✓",
			func(b string) bool { return b == "x" }},
		{"priority zero becomes 3", NtfyMessage{Title: "t", Body: "x", Priority: 0}, "3", "t",
			func(b string) bool { return true }},
		{"priority six becomes 3", NtfyMessage{Title: "t", Body: "x", Priority: 6}, "3", "t",
			func(b string) bool { return true }},
		{"long multibyte body", NtfyMessage{Title: "t", Body: strings.Repeat("é✓", 1250), Priority: 3}, "3", "t",
			func(b string) bool { return len(b) <= 3500 && len(b) > 3490 && utf8.ValidString(b) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, got, count := ntfyServer(t, 200, "{}")
			if err := (Ntfy{URL: srv.URL + "/topic"}).Send(context.Background(), tt.msg); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if *count != 1 {
				t.Fatalf("requests = %d, want exactly 1", *count)
			}
			r := (*got)[0]
			if r.method != "POST" || r.path != "/topic" {
				t.Errorf("got %s %s", r.method, r.path)
			}
			if r.contentType != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q", r.contentType)
			}
			if r.priority != tt.wantPrio {
				t.Errorf("Priority = %q, want %q", r.priority, tt.wantPrio)
			}
			dec, err := new(mime.WordDecoder).DecodeHeader(r.title)
			if err != nil || dec != tt.wantTitle {
				t.Errorf("Title %q decodes to %q (%v), want %q", r.title, dec, err, tt.wantTitle)
			}
			if tt.name == "non-ascii title" && !strings.HasPrefix(r.title, "=?UTF-8?b?") {
				t.Errorf("non-ASCII title not RFC 2047 B-encoded: %q", r.title)
			}
			if !tt.wantBody(r.body) {
				t.Errorf("unexpected body (len %d)", len(r.body))
			}
		})
	}
}

// TestNtfySendStatusError: a non-2xx answer is an error carrying the status and
// the server's own words when the body is JSON with an "error" string; a
// non-JSON body adds nothing, and a 2xx other than 200 is still success.
func TestNtfySendStatusError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		reply   string
		wantErr bool
		want    []string
	}{
		{"429 json error", 429, `{"code":42908,"error":"limit reached: too many requests"}`, true, []string{"429", "limit reached: too many requests"}},
		{"500 plain body", 500, "oops", true, []string{"500"}},
		{"202 accepted", 202, "", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, count := ntfyServer(t, tt.status, tt.reply)
			err := (Ntfy{URL: srv.URL + "/t"}).Send(context.Background(), NtfyMessage{Title: "t", Body: "b", Priority: 3})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if *count != 1 {
				t.Errorf("requests = %d, want 1 (no retry)", *count)
			}
		})
	}
}

// TestNtfySendClosedServer: a server that is gone gives an error, not a panic.
func TestNtfySendClosedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if err := (Ntfy{URL: url + "/t"}).Send(context.Background(), NtfyMessage{Title: "t", Priority: 3}); err == nil {
		t.Fatal("want an error for a closed server")
	}
}

// TestNtfySendBadURL: nothing is requested for an unusable address. The empty
// and scheme rows are load-bearing because an unset ntfy_url must never turn
// into a request to some default host.
func TestNtfySendBadURL(t *testing.T) {
	tests := []struct{ name, url string }{
		{"empty", ""},
		{"ftp scheme", "ftp://example.com/t"},
		{"no scheme", "example.com/t"},
		{"unparseable", "http://[::1"},
		{"no host", "http:///t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := (Ntfy{URL: tt.url}).Send(context.Background(), NtfyMessage{Title: "t", Priority: 3}); err == nil {
				t.Fatalf("want an error for %q", tt.url)
			}
		})
	}
}

// TestNtfySendRedact: the password in a URL must not appear in any error, for
// a refused connection, a bad status and a malformed URL alike. The malformed
// row matters because *url.Error prints the raw input.
func TestNtfySendRedact(t *testing.T) {
	srv, _, _ := ntfyServer(t, 403, `{"error":"forbidden"}`)
	creds := strings.Replace(srv.URL, "http://", "http://user:secret@", 1)

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := strings.Replace(closed.URL, "http://", "http://user:secret@", 1)
	closed.Close()

	tests := []struct{ name, url string }{
		{"bad status", creds + "/t"},
		{"closed server", closedURL + "/t"},
		{"malformed", "http://user:secret@[::1"},
		{"bad scheme", "ftp://user:secret@example.com/t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Ntfy{URL: tt.url}).Send(context.Background(), NtfyMessage{Title: "t", Priority: 3})
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks the password: %v", err)
			}
		})
	}
}

// TestInboxMessage mirrors the table that used to live in package main.
// Load-bearing rows: "no address" (silence, not an error, since most projects
// have no afk.toml), "non-blocking question" (never notifies even with an
// address) and "body and task" (the exact message shape: body, blank line,
// the show command, the task in brackets). Priorities are NtfyPriority's
// table; these rows only prove kind and blocking pass through unchanged.
func TestInboxMessage(t *testing.T) {
	const url = "https://ntfy.example/topic"
	cases := []struct {
		name     string
		id, task string
		kind     string
		title    string
		body     string
		blocking bool
		url      string
		wantSend bool
		want     NtfyMessage
	}{
		{name: "no address", id: "aaaaaa", kind: "failure", blocking: true, title: "CI failed twice"},
		{name: "non-blocking question", id: "aaaaaa", kind: "question", title: "Which queue?", url: url},
		{name: "non-blocking notice", id: "aaaaaa", kind: "notice", title: "Idea", url: url},
		{name: "blocking question", id: "aaaaaa", kind: "question", blocking: true, title: "Which queue?", url: url,
			wantSend: true, want: NtfyMessage{Title: "afk: Which queue?", Body: "afk inbox show aaaaaa", Priority: 4}},
		{name: "blocking failure is the loudest", id: "aaaaaa", kind: "failure", blocking: true, title: "CI failed twice", url: url,
			wantSend: true, want: NtfyMessage{Title: "afk: CI failed twice", Body: "afk inbox show aaaaaa", Priority: 5}},
		{name: "body and task", id: "bbbbbb", task: "T4.3", kind: "env_var", blocking: true, title: "Set STRIPE_KEY", body: "The deploy needs it.", url: url,
			wantSend: true, want: NtfyMessage{Title: "afk: Set STRIPE_KEY", Body: "The deploy needs it.\n\nafk inbox show bbbbbb (T4.3)", Priority: 4}},
		{name: "task without a body", id: "bbbbbb", task: "T4.2a", kind: "external_action", title: "Approve DNS", url: url,
			wantSend: true, want: NtfyMessage{Title: "afk: Approve DNS", Body: "afk inbox show bbbbbb (T4.2a)", Priority: 3}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, send := InboxMessage(tt.id, tt.task, tt.kind, tt.title, tt.body, tt.blocking, tt.url)
			if send != tt.wantSend || got != tt.want {
				t.Errorf("got (%+v, %v), want (%+v, %v)", got, send, tt.want, tt.wantSend)
			}
		})
	}
}

// TestItemLink pins the inbox item address a notification taps through to.
// Load-bearing rows: the empty public address (no link must mean no Click
// header at all, so a notification never points at a guessed host), the
// trailing-slash rows (a double slash would miss the route), and the escaping
// row (a project name with a space must stay one path segment).
func TestItemLink(t *testing.T) {
	tests := []struct {
		name, publicURL, project, id, want string
	}{
		{"plain", "https://afk.example", "myapp", "abc234", "https://afk.example/p/myapp/inbox/abc234"},
		{"trailing slash trimmed", "https://afk.example/", "myapp", "abc234", "https://afk.example/p/myapp/inbox/abc234"},
		{"project with a space is escaped", "https://afk.example/", "my app", "abc234", "https://afk.example/p/my%20app/inbox/abc234"},
		{"empty public address gives no link", "", "myapp", "abc234", ""},
		{"lone slash gives no link", "/", "myapp", "abc234", ""},
		{"empty project gives no link", "https://afk.example", "", "abc234", ""},
		{"empty id gives no link", "https://afk.example", "myapp", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ItemLink(tt.publicURL, tt.project, tt.id); got != tt.want {
				t.Errorf("ItemLink(%q, %q, %q) = %q, want %q", tt.publicURL, tt.project, tt.id, got, tt.want)
			}
		})
	}
}

// TestProjectLink pins the project page address. Load-bearing rows: the empty
// public address and the empty project, which must both give no link for the
// same reason as TestItemLink.
func TestProjectLink(t *testing.T) {
	tests := []struct {
		name, publicURL, project, want string
	}{
		{"plain", "https://afk.example", "myapp", "https://afk.example/p/myapp"},
		{"trailing slash trimmed", "https://afk.example/", "myapp", "https://afk.example/p/myapp"},
		{"project with a space is escaped", "https://afk.example", "my app", "https://afk.example/p/my%20app"},
		{"empty public address gives no link", "", "myapp", ""},
		{"empty project gives no link", "https://afk.example", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectLink(tt.publicURL, tt.project); got != tt.want {
				t.Errorf("ProjectLink(%q, %q) = %q, want %q", tt.publicURL, tt.project, got, tt.want)
			}
		})
	}
}

// TestNtfySendClick checks the Click header. Load-bearing rows: the unset row
// (no Click field must mean no Click header, not an empty one, because ntfy
// would then have nothing sensible to open), and the set row (the address goes
// out unchanged). This uses its own server so ntfyServer stays as it was.
func TestNtfySendClick(t *testing.T) {
	tests := []struct {
		name      string
		click     string
		wantSet   bool
		wantValue string
	}{
		{"click unset sends no header", "", false, ""},
		{"click set is sent as is", "https://afk.example/p/myapp/inbox/abc234", true, "https://afk.example/p/myapp/inbox/abc234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotSet bool
			var gotValues []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotValues, gotSet = r.Header["Click"]
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
			}))
			t.Cleanup(srv.Close)
			err := (Ntfy{URL: srv.URL + "/t"}).Send(context.Background(), NtfyMessage{Title: "t", Body: "b", Priority: 3, Click: tt.click})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if gotSet != tt.wantSet {
				t.Fatalf("Click header present = %v, want %v", gotSet, tt.wantSet)
			}
			if tt.wantSet && (len(gotValues) != 1 || gotValues[0] != tt.wantValue) {
				t.Errorf("Click header = %q, want %q", gotValues, tt.wantValue)
			}
		})
	}
}

// TestPlanLink pins the plan view's address, which the "plan ready for
// review" notification taps through to. Load-bearing rows: the empty public
// address and the empty project (no link, so no Click header pointing at a
// guessed host), the escaped name (a review entry's name is a folder name and
// may hold a space or a slash-like character; it must stay one path segment
// or the page answers 404), and the token row: the address is built from the
// public address and the name alone, so nothing secret can be in it.
func TestPlanLink(t *testing.T) {
	tests := []struct {
		name, publicURL, project, want string
	}{
		{"plain", "https://afk.example", "myapp", "https://afk.example/p/myapp/plan"},
		{"trailing slash trimmed", "https://afk.example/", "myapp", "https://afk.example/p/myapp/plan"},
		{"project with a space is escaped", "https://afk.example", "my app", "https://afk.example/p/my%20app/plan"},
		{"project with a slash stays one segment", "https://afk.example", "a/b", "https://afk.example/p/a%2Fb/plan"},
		{"a review name with a number suffix", "https://afk.example", "app-2", "https://afk.example/p/app-2/plan"},
		{"empty public address gives no link", "", "myapp", ""},
		{"lone slash gives no link", "/", "myapp", ""},
		{"empty project gives no link", "https://afk.example", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanLink(tt.publicURL, tt.project)
			if got != tt.want {
				t.Errorf("PlanLink(%q, %q) = %q, want %q", tt.publicURL, tt.project, got, tt.want)
			}
			if strings.Contains(got, "token") || strings.Contains(got, "?") {
				t.Errorf("PlanLink = %q; a link must carry no token and no query", got)
			}
		})
	}
}

// TestChatLink pins the planning chat's address for the "waiting for you"
// notification. The rows that matter most are the escaped project name (the
// chat URL must reach the right project even with a space in its name) and the
// empty rows (no public address or project means no Click header at all). The
// query check guards against a token ever being appended to the link.
func TestChatLink(t *testing.T) {
	tests := []struct {
		name, publicURL, project, want string
	}{
		{"plain", "https://afk.example", "myapp", "https://afk.example/p/myapp/plan/chat"},
		{"trailing slash trimmed", "https://afk.example/", "myapp", "https://afk.example/p/myapp/plan/chat"},
		{"project with a space is escaped", "https://afk.example", "my project", "https://afk.example/p/my%20project/plan/chat"},
		{"project with a slash stays one segment", "https://afk.example", "a/b", "https://afk.example/p/a%2Fb/plan/chat"},
		{"empty public address gives no link", "", "myapp", ""},
		{"lone slash gives no link", "/", "myapp", ""},
		{"empty project gives no link", "https://afk.example", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChatLink(tt.publicURL, tt.project)
			if got != tt.want {
				t.Errorf("ChatLink(%q, %q) = %q, want %q", tt.publicURL, tt.project, got, tt.want)
			}
			if strings.Contains(got, "?") || strings.Contains(got, "#") || strings.Contains(got, "token") {
				t.Errorf("ChatLink = %q; a link must carry no token, query or fragment", got)
			}
		})
	}
}

// TestLinkPaths pins the path-only builders a push notification taps
// through. Load-bearing rows: the escaped name and id, because a "/" or a
// space left raw in a path would open a different page (or none), and the
// agreement with the full links, which are these paths behind the public
// address.
func TestLinkPaths(t *testing.T) {
	tests := []struct {
		name                  string
		project, id           string
		item, plan, chat, prj string
	}{
		{"plain", "afk", "abc123", "/p/afk/inbox/abc123", "/p/afk/plan", "/p/afk/plan/chat", "/p/afk"},
		{"escaped", "my app/x", "id 1/2?", "/p/my%20app%2Fx/inbox/id%201%2F2%3F", "/p/my%20app%2Fx/plan", "/p/my%20app%2Fx/plan/chat", "/p/my%20app%2Fx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ItemPath(tt.project, tt.id); got != tt.item {
				t.Errorf("ItemPath = %q, want %q", got, tt.item)
			}
			if got := PlanPath(tt.project); got != tt.plan {
				t.Errorf("PlanPath = %q, want %q", got, tt.plan)
			}
			if got := ChatPath(tt.project); got != tt.chat {
				t.Errorf("ChatPath = %q, want %q", got, tt.chat)
			}
			const base = "https://afk.example"
			if got := ItemLink(base+"/", tt.project, tt.id); got != base+tt.item {
				t.Errorf("ItemLink = %q", got)
			}
			if got := PlanLink(base, tt.project); got != base+tt.plan {
				t.Errorf("PlanLink = %q", got)
			}
			if got := ChatLink(base, tt.project); got != base+tt.chat {
				t.Errorf("ChatLink = %q", got)
			}
			if got := ProjectLink(base, tt.project); got != base+tt.prj {
				t.Errorf("ProjectLink = %q", got)
			}
		})
	}
}
