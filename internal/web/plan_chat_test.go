package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestPlanChat sends one request per state of the planning chat and checks
// what the page's script would read.
//
// Load-bearing rows:
//   - "none" with nil slices: the fake returns Go nil slices and the JSON must
//     still say [] for messages and questions, because the page iterates them
//     without a check.
//   - "waiting for a choice": the options and the recommended flag survive to
//     the JSON under the snake_case names T8.12 and T8.4 will read.
//   - "unknown": the problem sentence reaches the caller.
func TestPlanChat(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		chat        planchat.Chat
		wantState   string
		wantMsgs    int
		wantQs      int
		wantProblem string
	}{
		{name: "none", chat: planchat.Chat{State: planchat.StateNone}, wantState: "none"},
		{name: "working", chat: planchat.Chat{State: planchat.StateWorking, Messages: []planchat.Message{{Who: "owner", Text: "hi", At: at}}}, wantState: "working", wantMsgs: 1},
		{name: "waiting for text", chat: planchat.Chat{State: planchat.StateWaitingForText, Messages: []planchat.Message{{Who: "agent", Text: "What next?", At: at}}}, wantState: "waiting_for_text", wantMsgs: 1},
		{name: "waiting for a choice", chat: planchat.Chat{
			State: planchat.StateWaitingForChoice,
			Questions: []provider.Question{{Question: "Which?", Header: "Pick", Options: []provider.Option{
				{Label: "A web page", Description: "d", Recommended: true}, {Label: "A phone app"}}}},
		}, wantState: "waiting_for_choice", wantQs: 1},
		{name: "unknown", chat: planchat.Chat{State: planchat.StateUnknown, Problem: "afk cannot find its conversation."}, wantState: "unknown", wantProblem: "afk cannot find its conversation."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.chat = tc.chat
			rec := rg.do(http.MethodGet, "/api/p/app/plan/chat", "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
			m := decode(t, rec)
			wantKeys(t, m, "project", "state", "messages", "questions", "problem")
			if m["project"] != "app" || m["state"] != tc.wantState || m["problem"] != tc.wantProblem {
				t.Errorf("project/state/problem = %v/%v/%v, want app/%s/%q", m["project"], m["state"], m["problem"], tc.wantState, tc.wantProblem)
			}
			msgs, ok := m["messages"].([]any)
			if !ok || len(msgs) != tc.wantMsgs {
				t.Errorf("messages = %#v, want a list of %d", m["messages"], tc.wantMsgs)
			}
			qs, ok := m["questions"].([]any)
			if !ok || len(qs) != tc.wantQs {
				t.Errorf("questions = %#v, want a list of %d", m["questions"], tc.wantQs)
			}
			if tc.wantQs > 0 {
				body := rec.Body.String()
				for _, want := range []string{`"multi_select"`, `"recommended":true`, `"label":"A web page"`} {
					if !strings.Contains(body, want) {
						t.Errorf("body lacks %s:\n%s", want, body)
					}
				}
			}
			if len(rg.chatCalls) != 1 || rg.chatCalls[0] != [2]string{"app", rg.root} {
				t.Errorf("PlanChat calls = %v, want one for app at %s", rg.chatCalls, rg.root)
			}
		})
	}
}

// TestPlanChatWithoutTheDependency pins the message for a page built with no
// reader: the owner is told, in words, not given an empty chat.
func TestPlanChatWithoutTheDependency(t *testing.T) {
	h, err := New(Config{Token: testToken}, Deps{
		Projects:  func() []Project { return []Project{{Name: "app", Root: t.TempDir()}} },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/p/app/plan/chat", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	m := decode(t, rec)
	if m["state"] != "unknown" || m["problem"] != "This page cannot read planning sessions." {
		t.Errorf("state/problem = %v/%v", m["state"], m["problem"])
	}
}

// TestPlanChatRefusals covers the requests that must not reach the reader.
//
// Load-bearing rows: "no token" and "page without the token" (401, and
// PlanChat never called, so a conversation is never read for a stranger) and
// "page for a plan waiting for approval" (the page, like the API, answers
// only for configured projects).
func TestPlanChatRefusals(t *testing.T) {
	tests := []struct {
		name   string
		target string
		auth   bool
		want   int
	}{
		{"no token", "/api/p/app/plan/chat", false, http.StatusUnauthorized},
		{"unknown project", "/api/p/nothing-here/plan/chat", true, http.StatusNotFound},
		{"a plan waiting for approval", "/api/p/draft/plan/chat", true, http.StatusNotFound},
		{"page without the token", "/p/app/plan/chat", false, http.StatusUnauthorized},
		{"page for an unknown project", "/p/nothing-here/plan/chat", true, http.StatusNotFound},
		{"page for a plan waiting for approval", "/p/draft/plan/chat", true, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.auth {
				req.Header.Set("Authorization", "Bearer "+testToken)
			}
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(rg.chatCalls) != 0 {
				t.Errorf("PlanChat was called for a refused request: %v", rg.chatCalls)
			}
			noSecrets(t, rec)
		})
	}
}

// chatFixture is a conversation with every kind of message: agent text with
// markdown, an earlier answered question, the owner's answer and reply, and a
// pending question whose recommended option is not first.
func chatFixture(state planchat.State) planchat.Chat {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	earlier := []provider.Question{{Question: "Which language?", Header: "Language", Options: []provider.Option{
		{Label: "Go", Recommended: true}, {Label: "Ruby"}}}}
	pending := []provider.Question{{Question: "Which framework?", Header: "Framework", Options: []provider.Option{
		{Label: "Django", Description: "Batteries included"},
		{Label: "Python", Description: "Plain, no framework", Recommended: true},
		{Label: "Flask"}}}}
	c := planchat.Chat{
		Project: "app",
		State:   state,
		Messages: []planchat.Message{
			{Who: provider.WhoAgent, Text: "# Brief\n\nHere is the **plan**.", At: at},
			{Who: provider.WhoAgent, Text: "Which language?", At: at, Questions: earlier},
			{Who: provider.WhoOwner, Text: "Language: Go", At: at},
			{Who: provider.WhoOwner, Text: "Keep it small.", At: at},
		},
		Questions: []provider.Question{},
	}
	if state == planchat.StateWaitingForChoice {
		c.Messages = append(c.Messages, planchat.Message{Who: provider.WhoAgent, Text: "Which framework?", At: at, Questions: pending})
		c.Questions = pending
	}
	return c
}

// TestPlanChatJSONUnchanged pins the API's answer to the bytes planchat.Chat
// encodes to on its own, the encoding before the page existed. ChatView
// embeds the chat and adds a page-only field; if that field ever reached the
// JSON, or the embedding changed a name, this fails. Load-bearing for every
// state with a pending question, since that is the richest shape.
func TestPlanChatJSONUnchanged(t *testing.T) {
	for _, state := range []planchat.State{planchat.StateWaitingForChoice, planchat.StateWaitingForText, planchat.StateNone} {
		t.Run(string(state), func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.chat = chatFixture(state)
			rec := rg.do(http.MethodGet, "/api/p/app/plan/chat", "", "")
			var want bytes.Buffer
			if err := json.NewEncoder(&want).Encode(rg.chat); err != nil {
				t.Fatal(err)
			}
			if rec.Body.String() != want.String() {
				t.Errorf("JSON changed:\n got %s\nwant %s", rec.Body.String(), want.String())
			}
		})
	}
}

// TestPlanChatPage draws the chat screen for each state and checks the markup
// that tells the states apart.
//
// Load-bearing rows:
//   - "waiting for a choice, one question": each option is a submit button
//     named choice-0 (one press sends it without the script), the
//     recommended one first and marked, the own-answer box is there, and the
//     composer is drawn disabled with "Answer the question above.";
//   - "several questions" and "multi-select": radios or checkboxes with one
//     Send, and no own-answer box, which planchat would refuse there;
//   - "working": the working line, data-working="true" for the live bar,
//     and the composer disabled with its sentence;
//   - "waiting for text": the only state whose composer takes a reply;
//   - "none": the empty block and no form at all;
//   - "unknown": the problem in a notice, never a reply box that works.
func TestPlanChatPage(t *testing.T) {
	multi := chatFixture(planchat.StateWaitingForChoice)
	multi.Questions = []provider.Question{
		{Question: "Database?", Options: []provider.Option{{Label: "SQLite"}, {Label: "Postgres", Recommended: true}}},
		{Question: "Hosting?", Options: []provider.Option{{Label: "Fly"}, {Label: "A VM"}}},
	}
	multiSelect := chatFixture(planchat.StateWaitingForChoice)
	multiSelect.Questions = []provider.Question{{Question: "Which parts?", MultiSelect: true, Options: []provider.Option{{Label: "API"}, {Label: "Web"}}}}
	unknown := planchat.Chat{State: planchat.StateUnknown, Problem: "afk cannot find its conversation."}

	composerOff := `<textarea class="textarea" id="chat-text" name="text" rows="1" disabled aria-describedby="chat-composer-hint"></textarea>`
	composerOn := `<textarea class="textarea" id="chat-text" name="text" rows="1"></textarea>`
	tests := []struct {
		name    string
		chat    planchat.Chat
		want    []string
		notWant []string
	}{
		{name: "none", chat: planchat.Chat{State: planchat.StateNone},
			want: []string{`<div class="empty">`, "No planning session", "Start one to change the plan:",
				`<a class="btn btn--secondary" href="/p/app/plan/change?mode=extend">Extend</a>`,
				`<a class="btn btn--secondary" href="/p/app/plan/change?mode=amend">Amend</a>`,
				`<a class="btn btn--secondary" href="/p/app/plan/change?mode=reconcile">Reconcile</a>`},
			notWant: []string{"later", "afk dash", "<form", "data-working", `class="working"`, "The planning session&rsquo;s overview"}},
		{name: "working", chat: chatFixture(planchat.StateWorking),
			want: []string{`data-project="app" data-working="true">`, `<p class="working"><span class="working__dots" aria-hidden="true">`, "The agent is working</p>",
				composerOff, `<p class="field__hint" id="chat-composer-hint">The agent is working. You can reply when it asks.</p>`,
				`<button type="submit" class="btn btn--primary" disabled>Send</button>`},
			notWant: []string{"data-chat-option", `class="empty"`}},
		{name: "waiting for text", chat: chatFixture(planchat.StateWaitingForText),
			want:    []string{composerOn, `<form method="post" action="/p/app/plan/chat" class="composer" data-chat-form data-project="app">`, `<label class="sr-only" for="chat-text">Your reply</label>`},
			notWant: []string{"disabled", "data-working", "chat-composer-hint", "data-chat-option"}},
		{name: "waiting for a choice, one question", chat: chatFixture(planchat.StateWaitingForChoice),
			want: []string{
				`<form method="post" action="/p/app/plan/chat" class="question" data-chat-form data-project="app">`,
				`<p class="question__text" id="chat-q-0">Which framework?</p>`,
				`<button type="submit" class="option option--recommended" name="choice-0" value="Python" aria-pressed="false" data-chat-option><span class="option__top">Python<span class="option__mark">Recommended</span></span><span class="option__desc">Plain, no framework</span></button>`,
				`<button type="submit" class="option" name="choice-0" value="Django" aria-pressed="false" data-chat-option>`,
				`<label class="field__label" for="chat-other">Or answer in your own words</label>`, `name="other"`,
				composerOff, `id="chat-composer-hint">Answer the question above.</p>`,
			},
			notWant: []string{`type="radio"`, `type="checkbox"`, "data-working"}},
		{name: "several questions", chat: multi,
			want: []string{`<legend class="question__text">Database?</legend>`, `<input type="radio" name="choice-0" value="Postgres">`, `<input type="radio" name="choice-1" value="Fly">`,
				`<div class="btn-row"><button type="submit" class="btn btn--primary">Send</button></div>`},
			notWant: []string{"data-chat-option", `name="other"`}},
		{name: "multi-select", chat: multiSelect,
			want:    []string{`<input type="checkbox" name="choice-0" value="API">`, `<input type="checkbox" name="choice-0" value="Web">`},
			notWant: []string{"data-chat-option", `name="other"`, `type="radio"`}},
		{name: "unknown", chat: unknown,
			want:    []string{`<div class="notice notice--error"><div class="notice__body">afk cannot find its conversation.</div></div>`, composerOff},
			notWant: []string{"data-chat-option", "data-working", `class="empty"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.chat = tc.chat
			rec := rg.pageDo(http.MethodGet, "/p/app/plan/chat", "")
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d\n%s", rec.Code, body)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
			if !strings.Contains(body, "<h1>Planning app</h1>") {
				t.Errorf("no heading")
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("must not contain %q", w)
				}
			}
			// The recommended option is drawn before every other one.
			if tc.name == "waiting for a choice, one question" {
				py, dj := strings.Index(body, `value="Python"`), strings.Index(body, `value="Django"`)
				if py < 0 || dj < 0 || py > dj {
					t.Errorf("the recommended option is not first (Python at %d, Django at %d)", py, dj)
				}
			}
			// The thread never holds a form or an input: live.js swaps it whole.
			thread := body[strings.Index(body, `id="chat-thread"`):strings.Index(body, `id="chat-reply"`)]
			for _, w := range []string{"<form", "<input", "<textarea", "<button"} {
				if strings.Contains(thread, w) {
					t.Errorf("the thread holds %s", w)
				}
			}
		})
	}
}

// TestPlanChatPageThread checks how the thread draws messages. Load-bearing:
// agent markdown goes through the chat renderer (h1 drawn as h3, no id), the
// earlier question shows its options read-only, the owner's message is plain
// text in a bubble, and the pending question is not drawn twice.
func TestPlanChatPageThread(t *testing.T) {
	rg := newRig(t, Config{})
	rg.chat = chatFixture(planchat.StateWaitingForChoice)
	body := rg.pageDo(http.MethodGet, "/p/app/plan/chat", "").Body.String()
	for _, w := range []string{
		`<div class="msg msg--agent"><span class="msg__who">Agent</span>`,
		`<div class="prose"><h3>Brief</h3>`, "<p>Here is the <strong>plan</strong>.</p>",
		`<p class="question__text">Which language?</p>`, `<li>Go <span class="meta">(Recommended)</span></li><li>Ruby</li>`,
		`<div class="msg msg--owner"><span class="msg__who">You</span><div class="msg__bubble item-body">Keep it small.</div></div>`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q\n%s", w, body)
		}
	}
	if n := strings.Count(body, "Which framework?"); n != 1 {
		t.Errorf("the pending question is drawn %d times, want once (as the card)", n)
	}
}

// TestPlanChatPageEscapes is load-bearing: agent text comes from a model that
// may quote anything, and owner text from a form, and neither may reach the
// page as markup. Both kinds, and an earlier question's option, are checked.
func TestPlanChatPageEscapes(t *testing.T) {
	rg := newRig(t, Config{})
	hostile := "<script>alert(1)</script> and <b>x</b>"
	rg.chat = planchat.Chat{State: planchat.StateWaitingForText, Messages: []planchat.Message{
		{Who: provider.WhoAgent, Text: hostile},
		{Who: provider.WhoOwner, Text: hostile},
		{Who: provider.WhoAgent, Text: "q", Questions: []provider.Question{{Question: hostile, Options: []provider.Option{{Label: hostile}}}}},
	}}
	body := rg.pageDo(http.MethodGet, "/p/app/plan/chat", "").Body.String()
	if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") || strings.Contains(body, "<b>") {
		t.Errorf("markup was not escaped:\n%s", body)
	}
	for _, w := range []string{
		`<div class="prose"><p>&lt;script&gt;alert(1)&lt;/script&gt; and &lt;b&gt;x&lt;/b&gt;</p>`,
		`<div class="msg__bubble item-body">&lt;script&gt;alert(1)&lt;/script&gt; and &lt;b&gt;x&lt;/b&gt;</div>`,
		`<p class="question__text">&lt;script&gt;alert(1)&lt;/script&gt; and &lt;b&gt;x&lt;/b&gt;</p>`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing the escaped %q", w)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Errorf("a raw <script>alert is on the page")
	}
}

// TestChatPageFor is the screen's pure decision.
//
// Load-bearing rows: "steps place the phase" (done before, current at, and
// nothing after), "approved after review" (a mode whose list stops at review
// shows every step done), "damaged state" (no line and no steps, rather than
// a guess), "unknown mode shown as written", "a state this page does not
// know" (drawn as unknown, with a disabled composer), "recommended first"
// (the agent's order otherwise), and "pending question left out of the
// thread".
func TestChatPageFor(t *testing.T) {
	status := func(mode, phase string) PlanStatus {
		return PlanStatus{Known: true, Session: true, Mode: mode, Phase: phase}
	}
	tests := []struct {
		name  string
		in    chatPageInput
		check func(t *testing.T, pg chatPage)
	}{
		{name: "line under the title", in: chatPageInput{Project: "app", Chat: chatFixture(planchat.StateWorking), Status: status(plansession.ModeNew, plansession.PhaseRounds)},
			check: func(t *testing.T, pg chatPage) {
				if pg.Sub != "New plan · Rounds" {
					t.Errorf("Sub = %q", pg.Sub)
				}
			}},
		{name: "steps place the phase", in: chatPageInput{Chat: chatFixture(planchat.StateWorking), Status: status(plansession.ModeAmend, plansession.PhaseDraft)},
			check: func(t *testing.T, pg chatPage) {
				want := []chatStep{{Name: "Brief", Done: true}, {Name: "Survey", Done: true}, {Name: "Draft", Current: true}, {Name: "Check"}, {Name: "Review"}}
				if fmt.Sprint(pg.Steps) != fmt.Sprint(want) {
					t.Errorf("Steps = %v, want %v", pg.Steps, want)
				}
				if pg.Sub != "Amending the plan · Draft" {
					t.Errorf("Sub = %q", pg.Sub)
				}
			}},
		{name: "approved after review", in: chatPageInput{Chat: chatFixture(planchat.StateWorking), Status: status(plansession.ModeExtend, plansession.PhaseApproved)},
			check: func(t *testing.T, pg chatPage) {
				for _, st := range pg.Steps {
					if !st.Done || st.Current {
						t.Errorf("step %+v, want every step done", st)
					}
				}
			}},
		{name: "damaged state", in: chatPageInput{Chat: chatFixture(planchat.StateWorking), Status: PlanStatus{Known: true, Session: true, Damaged: true, Mode: "new", Phase: "rounds"}},
			check: func(t *testing.T, pg chatPage) {
				if pg.Sub != "" || pg.Steps != nil {
					t.Errorf("Sub %q Steps %v, want none", pg.Sub, pg.Steps)
				}
			}},
		{name: "no session state", in: chatPageInput{Chat: chatFixture(planchat.StateWorking), Status: PlanStatus{Known: true}},
			check: func(t *testing.T, pg chatPage) {
				if pg.Sub != "" || pg.Steps != nil {
					t.Errorf("Sub %q Steps %v, want none", pg.Sub, pg.Steps)
				}
			}},
		{name: "unknown mode shown as written", in: chatPageInput{Chat: chatFixture(planchat.StateWorking), Status: status("rewrite", "")},
			check: func(t *testing.T, pg chatPage) {
				if pg.Sub != "rewrite" {
					t.Errorf("Sub = %q", pg.Sub)
				}
			}},
		{name: "folder, repository and overview", in: chatPageInput{Project: "my app", Chat: chatFixture(planchat.StateWorking), Folder: "/code/app"},
			check: func(t *testing.T, pg chatPage) {
				if pg.Folder != "/code/app" || pg.Repository != "None set" || pg.Overview != "/p/my%20app/sessions/master-plan" || pg.Action != "/p/my%20app/plan/chat" {
					t.Errorf("page = %+v", pg)
				}
			}},
		{name: "no overview without a session", in: chatPageInput{Project: "app", Chat: planchat.Chat{State: planchat.StateNone}, Repository: "github.com/a/b"},
			check: func(t *testing.T, pg chatPage) {
				if pg.Overview != "" || pg.Composer || pg.Repository != "github.com/a/b" {
					t.Errorf("page = %+v", pg)
				}
			}},
		{name: "a state this page does not know", in: chatPageInput{Chat: planchat.Chat{State: "sleeping"}},
			check: func(t *testing.T, pg chatPage) {
				if pg.State != "unknown" || !pg.Composer || pg.ComposerOn || pg.Problem == "" {
					t.Errorf("page = %+v", pg)
				}
			}},
		{name: "waiting for text takes a reply", in: chatPageInput{Chat: chatFixture(planchat.StateWaitingForText)},
			check: func(t *testing.T, pg chatPage) {
				if !pg.Composer || !pg.ComposerOn || pg.ComposerHint != "" || pg.Working {
					t.Errorf("page = %+v", pg)
				}
			}},
		{name: "recommended first", in: chatPageInput{Chat: chatFixture(planchat.StateWaitingForChoice)},
			check: func(t *testing.T, pg chatPage) {
				if len(pg.Questions) != 1 {
					t.Fatalf("Questions = %+v", pg.Questions)
				}
				var labels []string
				for _, o := range pg.Questions[0].Options {
					labels = append(labels, o.Label)
				}
				if strings.Join(labels, ",") != "Python,Django,Flask" {
					t.Errorf("options = %v, want Python,Django,Flask", labels)
				}
				if !pg.OneTap || !pg.OwnAnswer || pg.ComposerOn || pg.ComposerHint != "Answer the question above." {
					t.Errorf("page = %+v", pg)
				}
			}},
		{name: "pending question left out of the thread", in: chatPageInput{Chat: chatFixture(planchat.StateWaitingForChoice)},
			check: func(t *testing.T, pg chatPage) {
				if len(pg.Thread) != 4 {
					t.Fatalf("thread has %d messages, want 4", len(pg.Thread))
				}
				if len(pg.Thread[1].Questions) != 1 || pg.Thread[1].Questions[0].Question != "Which language?" || pg.Thread[2].Agent {
					t.Errorf("thread = %+v", pg.Thread)
				}
			}},
		{name: "refusal and drafts carried", in: chatPageInput{Chat: chatFixture(planchat.StateWaitingForText), Error: "No.", Text: "my words", Other: "mine"},
			check: func(t *testing.T, pg chatPage) {
				if pg.Error != "No." || pg.Text != "my words" || pg.Other != "mine" {
					t.Errorf("page = %+v", pg)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, chatPageFor(tc.in))
		})
	}
}

// TestPlanChatEmptyStateWithoutAPlan is load-bearing: with no plan the three
// buttons would lead to a page that can only say there is nothing to change,
// so the empty state says that itself and offers none.
func TestPlanChatEmptyStateWithoutAPlan(t *testing.T) {
	rg := newRig(t, Config{})
	delete(rg.plans, rg.root)
	rg.chat = planchat.Chat{State: planchat.StateNone}
	body := rg.pageDo(http.MethodGet, "/p/app/plan/chat", "").Body.String()
	if !strings.Contains(body, "There is no plan to change yet.") {
		t.Errorf("missing the no-plan sentence")
	}
	for _, w := range []string{"plan/change?mode=", "Start one to change the plan:", "later"} {
		if strings.Contains(body, w) {
			t.Errorf("must not contain %q", w)
		}
	}
}
