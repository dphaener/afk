package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
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
// Load-bearing rows: "no token" (401, and PlanChat never called, so a
// conversation is never read for a stranger) and "page twin" (the address
// without /api is not served until T8.12 adds the page).
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
		{"page twin", "/p/app/plan/chat", true, http.StatusNotFound},
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
