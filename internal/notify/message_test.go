package notify

import (
	"strings"
	"testing"
)

// TestItemNotifies pins the whole kind x blocking table. Load-bearing rows:
// every blocking row notifies (a parked task must never be silent), and the
// non-blocking question and notice rows do not (assumed answers arrive by the
// dozen, so notifying on them would train the owner to ignore the phone). The
// unknown-kind rows keep a future kind from going quiet when it blocks.
func TestItemNotifies(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		blocking bool
		want     bool
	}{
		{"failure blocking", "failure", true, true},
		{"failure not blocking", "failure", false, true},
		{"question blocking", "question", true, true},
		{"question not blocking", "question", false, false},
		{"env_var blocking", "env_var", true, true},
		{"env_var not blocking", "env_var", false, true},
		{"external_action blocking", "external_action", true, true},
		{"external_action not blocking", "external_action", false, true},
		{"verification blocking", "verification", true, true},
		{"verification not blocking", "verification", false, true},
		{"notice blocking", "notice", true, true},
		{"notice not blocking", "notice", false, false},
		{"unknown blocking", "mystery", true, true},
		{"unknown not blocking", "mystery", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ItemNotifies(tt.kind, tt.blocking); got != tt.want {
				t.Errorf("ItemNotifies(%q, %v) = %v, want %v", tt.kind, tt.blocking, got, tt.want)
			}
		})
	}
}

// TestInboxMessage pins the text of an item's notification. Load-bearing
// rows: "non-blocking question" (never notifies), "body and task" (the exact
// message shape: body, blank line, the show command, the task in brackets)
// and "task without a body" (no stray blank line before the command). Which
// kinds notify is ItemNotifies' table; these rows only prove kind and
// blocking pass through unchanged.
func TestInboxMessage(t *testing.T) {
	cases := []struct {
		name     string
		id, task string
		kind     string
		title    string
		body     string
		blocking bool
		wantSend bool
		want     Message
	}{
		{name: "non-blocking question", id: "aaaaaa", kind: "question", title: "Which queue?"},
		{name: "non-blocking notice", id: "aaaaaa", kind: "notice", title: "Idea"},
		{name: "blocking question", id: "aaaaaa", kind: "question", blocking: true, title: "Which queue?",
			wantSend: true, want: Message{Title: "afk: Which queue?", Body: "afk inbox show aaaaaa"}},
		{name: "blocking failure", id: "aaaaaa", kind: "failure", blocking: true, title: "CI failed twice",
			wantSend: true, want: Message{Title: "afk: CI failed twice", Body: "afk inbox show aaaaaa"}},
		{name: "body and task", id: "bbbbbb", task: "T4.3", kind: "env_var", blocking: true, title: "Set STRIPE_KEY", body: "The deploy needs it.",
			wantSend: true, want: Message{Title: "afk: Set STRIPE_KEY", Body: "The deploy needs it.\n\nafk inbox show bbbbbb (T4.3)"}},
		{name: "task without a body", id: "bbbbbb", task: "T4.2a", kind: "external_action", title: "Approve DNS",
			wantSend: true, want: Message{Title: "afk: Approve DNS", Body: "afk inbox show bbbbbb (T4.2a)"}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, send := InboxMessage(tt.id, tt.task, tt.kind, tt.title, tt.body, tt.blocking)
			if send != tt.wantSend || got != tt.want {
				t.Errorf("got (%+v, %v), want (%+v, %v)", got, send, tt.want, tt.wantSend)
			}
		})
	}
}

// TestLinkPaths pins the path-only builders a push notification taps
// through. Load-bearing row: "escaped", because a "/" or a space left raw in
// a path would open a different page (or none).
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
			if got := ProjectPath(tt.project); got != tt.prj {
				t.Errorf("ProjectPath = %q, want %q", got, tt.prj)
			}
		})
	}
}
