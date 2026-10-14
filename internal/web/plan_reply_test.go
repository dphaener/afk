package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestPlanReply sends one POST per outcome of the dependency and checks the
// status, the sentence and that the fake saw the call.
//
// Load-bearing rows: each refusal reason (the status is what a page script
// branches on, and the message must be the refusal's own, not web's), and
// "unreachable" (502, a different cause from a refusal, so the page can say
// "try again").
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
	h, err := New(Config{Token: testToken}, Deps{
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
	req.Header.Set("Authorization", "Bearer "+testToken)
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
// (413) and "page twin" (not served until T8.12).
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
		{"page twin", "/p/app/plan/chat", true, "application/json", "", `{"text":"x"}`, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body))
			if tc.auth {
				req.Header.Set("Authorization", "Bearer "+testToken)
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
