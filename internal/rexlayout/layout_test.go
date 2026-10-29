package rexlayout

import (
	"errors"
	"strings"
	"testing"
)

// TestWindowLabel: the planning row is load-bearing (no prefix, whatever the
// phase) and so is the unknown row (the phase text passes through).
func TestWindowLabel(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		phase    string
		planning bool
		want     string
	}{
		{"planning session", "master-plan", "executing", true, "plan"},
		{"planning phase", "t4-3", "planning", false, "t4-3 · plan"},
		{"executing", "t4-3", "executing", false, "t4-3 · build"},
		{"reviewing", "t4-3", "reviewing", false, "t4-3 · review"},
		{"fixing", "t4-3", "fixing", false, "t4-3 · fix"},
		{"done", "t4-3", "done", false, "t4-3 · done"},
		{"failed", "t4-3", "failed", false, "t4-3 · failed"},
		{"unknown phase", "t4-3", "waiting", false, "t4-3 · waiting"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WindowLabel(tc.session, tc.phase, tc.planning); got != tc.want {
				t.Errorf("WindowLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLayout: the label clash row is load-bearing: two listed sessions share
// a name, and the recorded id alone decides. The not-listed row proves a
// session closed by hand is created again.
func TestLayout(t *testing.T) {
	tests := []struct {
		name string
		in   Facts
		want Plan
	}{
		{"first session of a project", Facts{ProjectName: "afk"},
			Plan{CreateSession: true, SessionName: "afk"}},
		{"second session", Facts{ProjectName: "afk", SessionID: "s1", SessionListed: true, WindowHasBlock: true},
			Plan{SessionID: "s1"}},
		{"planning session reuses the project session", Facts{ProjectName: "afk", SessionID: "s1", SessionListed: true, WindowHasBlock: true},
			Plan{SessionID: "s1"}},
		{"recorded session closed by hand", Facts{ProjectName: "afk", SessionID: "s1", SessionListed: false},
			Plan{CreateSession: true, SessionName: "afk"}},
		{"label clash, recorded id decides", Facts{ProjectName: "afk", SessionID: "s2", SessionListed: true, WindowHasBlock: true},
			Plan{SessionID: "s2"}},
		{"window closed by hand", Facts{ProjectName: "afk", SessionID: "s1", SessionListed: true, WindowHasBlock: false},
			Plan{SessionID: "s1", RecordedWindowGone: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Layout(tc.in); got != tc.want {
				t.Errorf("Layout = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRefusal: backend afk must be nil for every Rex fact (no Rex need for
// the default), and the two rex refusals must each say how to fix it.
func TestRefusal(t *testing.T) {
	bad := errors.New("config.toml: [runtime] backend must be \"afk\" or \"rex\"")
	tests := []struct {
		name      string
		backend   string
		cfgErr    error
		installed bool
		answers   bool
		wantErr   string // substring; empty means nil
	}{
		{"afk, nothing installed", "afk", nil, false, false, ""},
		{"afk, installed, silent", "afk", nil, true, false, ""},
		{"afk, installed, answering", "afk", nil, true, true, ""},
		{"afk, answering without install", "afk", nil, false, true, ""},
		{"bad word", "", bad, true, true, "[runtime] backend"},
		{"rex, not installed", "rex", nil, false, false, "Rex is not installed"},
		{"rex, no answer", "rex", nil, true, false, "does not answer"},
		{"rex, answering", "rex", nil, true, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Refusal(tc.backend, tc.cfgErr, tc.installed, tc.answers)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Refusal = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Refusal = %v, want it to contain %q", err, tc.wantErr)
			}
			if tc.backend == "rex" && !strings.Contains(err.Error(), `backend = "afk"`) {
				t.Errorf("refusal %q does not say how to run without Rex", err)
			}
		})
	}
}

func TestTerminalSize(t *testing.T) {
	if TerminalSize.Columns != 160 || TerminalSize.Rows != 48 {
		t.Errorf("size = %dx%d, want 160x48", TerminalSize.Columns, TerminalSize.Rows)
	}
}
