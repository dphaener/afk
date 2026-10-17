package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestPlanReply sends one POST per outcome of the dependency and checks the
// status, the sentence and that the fake saw the call.
//
// Load-bearing rows: each refusal reason (the status is what a page script
// branches on, and the message must be the refusal's own, not web's), and
// "unreachable" (502, a different cause from a refusal, so the page can say
// "try again"). A refusal's body carries its reason beside the sentence, for
// a page script to branch on; an unreachable host's body has no reason.
func TestPlanReply(t *testing.T) {
	refusal := func(reason planchat.RefusalReason) error {
		return &planchat.Refusal{Reason: reason, Message: "sentence for " + string(reason)}
	}
	tests := []struct {
		name    string
		body    string
		err     error
		want    int
		message string
		sent    string
	}{
		{"text sent", `{"text":"hello"}`, nil, 200, "", "text"},
		{"choices sent", `{"choices":[["a"]]}`, nil, 200, "", "choice"},
		{"no_session", `{"text":"x"}`, refusal(planchat.ReasonNoSession), 409, "sentence for no_session", ""},
		{"not_waiting", `{"text":"x"}`, refusal(planchat.ReasonNotWaiting), 409, "sentence for not_waiting", ""},
		{"wrong_kind", `{"text":"x"}`, refusal(planchat.ReasonWrongKind), 409, "sentence for wrong_kind", ""},
		{"unsupported", `{"choices":[["a"]]}`, refusal(planchat.ReasonUnsupported), 409, "sentence for unsupported", ""},
		{"not_offered", `{"choices":[["z"]]}`, refusal(planchat.ReasonNotOffered), 400, "sentence for not_offered", ""},
		{"bad_reply", `{"text":"x"}`, refusal(planchat.ReasonBadReply), 400, "sentence for bad_reply", ""},
		{"both empty goes to the dependency", `{}`, refusal(planchat.ReasonBadReply), 400, "sentence for bad_reply", ""},
		{"wrapped refusal", `{"text":"x"}`, errors.Join(errors.New("ctx"), refusal(planchat.ReasonNotWaiting)), 409, "", ""},
		{"unreachable", `{"text":"x"}`, errors.New("dial: no such file"), 502, "could not be reached", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.replyErr = tc.err
			rec := rg.do(http.MethodPost, "/api/p/app/plan/chat", "application/json", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(rg.replies) != 1 {
				t.Fatalf("PlanReply calls = %d, want 1", len(rg.replies))
			}
			if rg.chatCalls[0] != [2]string{"app", rg.root} {
				t.Errorf("called with %v", rg.chatCalls[0])
			}
			m := decode(t, rec)
			if tc.want == 200 {
				if m["sent"] != tc.sent || m["project"] != "app" {
					t.Errorf("body = %v", m)
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
				}
				return
			}
			if msg, _ := m["error"].(string); !strings.Contains(msg, tc.message) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.message)
			}
			var refusal *planchat.Refusal
			if errors.As(tc.err, &refusal) {
				if m["reason"] != string(refusal.Reason) || len(m) != 2 {
					t.Errorf("body = %v, want error and reason %q only", m, refusal.Reason)
				}
			} else if _, has := m["reason"]; has {
				t.Errorf("an unreachable host's body has a reason: %v", m)
			}
		})
	}
}

// TestPlanReplyBody checks the choices arrive intact and the text too.
func TestPlanReplyBody(t *testing.T) {
	rg := newRig(t, Config{})
	rg.do(http.MethodPost, "/api/p/app/plan/chat", "application/json; charset=utf-8", `{"choices":[["a","b"],["c"]]}`)
	if len(rg.replies) != 1 || len(rg.replies[0].Choices) != 2 || rg.replies[0].Choices[0][1] != "b" || rg.replies[0].Text != "" {
		t.Fatalf("reply = %+v", rg.replies)
	}
}

// TestPlanReplyWithoutTheDependency pins the 503 sentence.
func TestPlanReplyWithoutTheDependency(t *testing.T) {
	rg := newRig(t, Config{})
	// A second handler over the same projects but no PlanReply.
	h, err := New(Config{}, Deps{
		Password:  testPasswordSeam,
		Projects:  func() []Project { return rg.projects },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/p/app/plan/chat", strings.NewReader(`{"text":"x"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 || decode(t, rec)["error"] != "This page cannot send replies." {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// TestPlanReplyRefusedBeforeTheDependency covers requests that must never
// reach PlanReply.
//
// Load-bearing rows: "no token" and "cross-site Origin" (nothing is typed
// into the planning session for a stranger or another site), "too large"
// (413), and the page route's "page cross-site Origin" (the form post is
// guarded like the API) and "page JSON" (the page takes its forms' fields
// only, and says so rather than sending an empty reply).
func TestPlanReplyRefusedBeforeTheDependency(t *testing.T) {
	big := `{"text":"` + strings.Repeat("a", 70*1024) + `"}`
	tests := []struct {
		name   string
		target string
		auth   bool
		ctype  string
		origin string
		body   string
		want   int
	}{
		{"not JSON", "/api/p/app/plan/chat", true, "text/plain", "", `{"text":"x"}`, 415},
		{"no content type", "/api/p/app/plan/chat", true, "", "", `{"text":"x"}`, 415},
		{"unknown field", "/api/p/app/plan/chat", true, "application/json", "", `{"text":"x","extra":1}`, 400},
		{"broken JSON", "/api/p/app/plan/chat", true, "application/json", "", `{"text":`, 400},
		{"unknown project", "/api/p/nothing-here/plan/chat", true, "application/json", "", `{"text":"x"}`, 404},
		{"too large", "/api/p/app/plan/chat", true, "application/json", "", big, 413},
		{"no token", "/api/p/app/plan/chat", false, "application/json", "", `{"text":"x"}`, 401},
		{"cross-site Origin", "/api/p/app/plan/chat", true, "application/json", "https://evil.example", `{"text":"x"}`, 403},
		{"page cross-site Origin", "/p/app/plan/chat", true, formType, "https://evil.example", "text=x", 403},
		{"page JSON", "/p/app/plan/chat", true, "application/json", "", `{"text":"x"}`, 415},
		{"page unknown project", "/p/nothing-here/plan/chat", true, formType, "", "text=x", 404},
		{"page too large", "/p/app/plan/chat", true, formType, "", "text=" + strings.Repeat("a", 70*1024), 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body))
			if tc.auth {
				req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
			}
			if tc.ctype != "" {
				req.Header.Set("Content-Type", tc.ctype)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(rg.replies) != 0 {
				t.Errorf("PlanReply was called for a refused request: %v", rg.replies)
			}
			noSecrets(t, rec)
		})
	}
}

// TestPlanChatFormReply is the chat screen's form post without the script.
//
// Load-bearing rows:
//   - "single choice, one press": the option button's choice-0 alone is a
//     choices reply [["Python"]], answered 303 to ?done=sent;
//   - "multi-question choices ordered": the fields arrive in any order and
//     the reply lists them by question, choice-0 first;
//   - "own answer beats a choice": the typed words win, as in the inbox;
//   - "refusal re-rendered": the screen again at the refusal's status, its
//     sentence in an alert, the typed text back in its box, no redirect;
//   - "unreachable": the error page at 502, not the screen;
//   - "no token" and "cross-site Origin": PlanReply never runs.
func TestPlanChatFormReply(t *testing.T) {
	twoQuestions := planchat.Chat{State: planchat.StateWaitingForChoice, Questions: []provider.Question{
		{Question: "Database?", Options: []provider.Option{{Label: "SQLite"}, {Label: "Postgres"}}},
		{Question: "Parts?", MultiSelect: true, Options: []provider.Option{{Label: "API"}, {Label: "Web"}}},
	}}
	oneQuestion := planchat.Chat{State: planchat.StateWaitingForChoice, Questions: []provider.Question{
		{Question: "Which framework?", Options: []provider.Option{{Label: "Django"}, {Label: "Python", Recommended: true}}},
	}}
	wrongKind := &planchat.Refusal{Reason: planchat.ReasonWrongKind, Message: "The planning session is waiting for you to pick from the options it offered, not for typed text. Choose an option and send again."}
	tests := []struct {
		name     string
		chat     planchat.Chat
		body     string
		err      error
		noCookie bool
		origin   string
		want     int
		wantLoc  string
		wantSent *planchat.Reply
		wantBody []string
	}{
		{name: "text sent", chat: planchat.Chat{State: planchat.StateWaitingForText}, body: "text=Keep+it+small",
			want: 303, wantLoc: "/p/app/plan/chat?done=sent", wantSent: &planchat.Reply{Text: "Keep it small"}},
		{name: "single choice, one press", chat: oneQuestion, body: "choice-0=Python&other=",
			want: 303, wantLoc: "/p/app/plan/chat?done=sent", wantSent: &planchat.Reply{Choices: [][]string{{"Python"}}}},
		{name: "multi-question choices ordered", chat: twoQuestions, body: "choice-1=Web&choice-0=Postgres&choice-1=API",
			want: 303, wantLoc: "/p/app/plan/chat?done=sent", wantSent: &planchat.Reply{Choices: [][]string{{"Postgres"}, {"Web", "API"}}}},
		{name: "own answer", chat: oneQuestion, body: "other=Use+Flask",
			want: 303, wantLoc: "/p/app/plan/chat?done=sent", wantSent: &planchat.Reply{Other: "Use Flask"}},
		{name: "own answer beats a choice", chat: oneQuestion, body: "choice-0=Python&other=Use+Flask",
			want: 303, wantLoc: "/p/app/plan/chat?done=sent", wantSent: &planchat.Reply{Other: "Use Flask"}},
		{name: "refusal re-rendered", chat: oneQuestion, body: "text=hello+there", err: wrongKind, want: 409,
			wantSent: &planchat.Reply{Text: "hello there"},
			wantBody: []string{`<div class="notice notice--error" role="alert"><div class="notice__body">` + wrongKind.Message + `</div></div>`, ">hello there</textarea>", "<h1>Planning app</h1>"}},
		{name: "bad reply re-rendered at 400", chat: oneQuestion, body: "choice-0=Nope", err: &planchat.Refusal{Reason: planchat.ReasonNotOffered, Message: "Not offered."}, want: 400,
			wantSent: &planchat.Reply{Choices: [][]string{{"Nope"}}}, wantBody: []string{`role="alert"><div class="notice__body">Not offered.</div>`}},
		{name: "unreachable", chat: planchat.Chat{State: planchat.StateWaitingForText}, body: "text=x", err: errors.New("dial: no such file"), want: 502,
			wantSent: &planchat.Reply{Text: "x"}, wantBody: []string{"The planning session could not be reached"}},
		{name: "cross-site Origin", body: "text=x", origin: "https://evil.example", want: 403},
		{name: "no cookie", body: "text=x", noCookie: true, want: 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.chat = tc.chat
			rg.replyErr = tc.err
			req := httptest.NewRequest(http.MethodPost, "/p/app/plan/chat", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", formType)
			if !tc.noCookie {
				req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != tc.wantLoc {
				t.Errorf("Location = %q, want %q", loc, tc.wantLoc)
			}
			if tc.wantSent == nil {
				if len(rg.replies) != 0 {
					t.Errorf("PlanReply ran: %+v", rg.replies)
				}
				return
			}
			if len(rg.replies) != 1 || !reflect.DeepEqual(rg.replies[0], *tc.wantSent) {
				t.Errorf("PlanReply got %+v, want one %+v", rg.replies, *tc.wantSent)
			}
			body := rec.Body.String()
			for _, w := range tc.wantBody {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q\n%s", w, body)
				}
			}
			if tc.want != 303 && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Errorf("Content-Type = %q, want a page", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestPlanChatFormReplyWithoutTheDependency: a daemon with no PlanReply has
// no reply on this page, so the form's address is not found, as the other
// page actions are when their seam is missing.
func TestPlanChatFormReplyWithoutTheDependency(t *testing.T) {
	rg := newRig(t, Config{})
	h, err := New(Config{}, Deps{
		Password:  testPasswordSeam,
		Projects:  func() []Project { return rg.projects },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/p/app/plan/chat", strings.NewReader("text=x"))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
	req.Header.Set("Content-Type", formType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// TestReplyFromForm is the pure shaping of a form into one reply. Nothing is
// refused here; planchat.Check does that behind Deps.PlanReply.
//
// Load-bearing rows: "missing question" (an empty list in its place, so
// Check names the question), "field for a question no longer pending" (one
// answer too many, so Check refuses the count rather than sending a stale
// choice to the wrong question), "leading zero" (choice-00 is not choice-0),
// "own answer wins", and "spaces only" (passed on so Check says it is empty).
func TestReplyFromForm(t *testing.T) {
	tests := []struct {
		name    string
		form    string
		pending int
		want    planchat.Reply
	}{
		{name: "text", form: "text=hi", want: planchat.Reply{Text: "hi"}},
		{name: "nothing", form: "", want: planchat.Reply{}},
		{name: "one choice", form: "choice-0=a", pending: 1, want: planchat.Reply{Choices: [][]string{{"a"}}}},
		{name: "ordered by question", form: "choice-1=b&choice-0=a", pending: 2, want: planchat.Reply{Choices: [][]string{{"a"}, {"b"}}}},
		{name: "multi-select keeps every value", form: "choice-0=a&choice-0=b", pending: 1, want: planchat.Reply{Choices: [][]string{{"a", "b"}}}},
		{name: "missing question", form: "choice-0=a", pending: 2, want: planchat.Reply{Choices: [][]string{{"a"}, {}}}},
		{name: "field for a question no longer pending", form: "choice-1=b", pending: 1, want: planchat.Reply{Choices: [][]string{{}, {}}}},
		{name: "nothing pending", form: "choice-0=a", pending: 0, want: planchat.Reply{Choices: [][]string{{}}}},
		{name: "leading zero", form: "choice-00=a", pending: 1, want: planchat.Reply{Choices: [][]string{{}, {}}}},
		{name: "blank value dropped", form: "choice-0=", pending: 1, want: planchat.Reply{Choices: [][]string{{}}}},
		{name: "own answer", form: "other=mine", want: planchat.Reply{Other: "mine"}},
		{name: "own answer wins", form: "choice-0=a&other=mine", pending: 1, want: planchat.Reply{Other: "mine"}},
		{name: "blank own answer leaves the choice", form: "choice-0=a&other=", pending: 1, want: planchat.Reply{Choices: [][]string{{"a"}}}},
		{name: "spaces only", form: "other=+++", want: planchat.Reply{Other: "   "}},
		{name: "text and a choice both passed on", form: "text=hi&choice-0=a", pending: 1, want: planchat.Reply{Text: "hi", Choices: [][]string{{"a"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form, err := url.ParseQuery(tc.form)
			if err != nil {
				t.Fatal(err)
			}
			if got := replyFromForm(form, tc.pending); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("replyFromForm = %#v, want %#v", got, tc.want)
			}
		})
	}
}
