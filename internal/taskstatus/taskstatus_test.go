package taskstatus

import (
	"slices"
	"testing"
)

// TestChoices: the order is the ledger's lifecycle order and every word
// `afk ledger set` accepts is present. Load-bearing: the dashboard's picker
// index and the web sheet's list both come from it.
func TestChoices(t *testing.T) {
	want := []string{"pending", "running", "shipping", "done", "failed", "parked", "superseded"}
	got := Choices()
	if len(got) != len(want) {
		t.Fatalf("Choices() = %q, want %d words", got, len(want))
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("Choices() = %q, missing %q", got, w)
		}
	}
}

// TestConsequence: one row per status. The unknown row is load-bearing: an
// unknown status gets no sentence, never a guess.
func TestConsequence(t *testing.T) {
	tests := []struct {
		name, status, want string
	}{
		{"pending", "pending", "The build loop may start it on its next pass."},
		{"done", "done", "Tasks that depend on it may start."},
		{"failed", "failed", "The build loop will not start it again until you set it back to pending."},
		{"parked", "parked", "The build loop will wait until its inbox items are answered."},
		{"superseded", "superseded", "Tasks that depend on it will wait for the tasks that replace it."},
		{"running", "running", "The build loop will treat it as having a session; use this only to repair the ledger."},
		{"shipping", "shipping", "The build loop will treat it as having a session; use this only to repair the ledger."},
		{"unknown", "bogus", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Consequence(tt.status); got != tt.want {
				t.Fatalf("Consequence(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

// TestWorking: running and shipping are the two statuses with an agent.
func TestWorking(t *testing.T) {
	for _, tt := range []struct {
		old  string
		want bool
	}{
		{"running", true}, {"shipping", true}, {"pending", false}, {"failed", false}, {"", false},
	} {
		t.Run(tt.old, func(t *testing.T) {
			if got := Working(tt.old); got != tt.want {
				t.Fatalf("Working(%q) = %v, want %v", tt.old, got, tt.want)
			}
		})
	}
}

// TestConfirmText: the load-bearing rows are running and shipping (the
// agent-working line comes first), superseded with replacements (the
// parenthesis) and the unknown status (no consequence line).
func TestConfirmText(t *testing.T) {
	const working = "An agent is working on T2.3 now; changing its status does not stop it."
	tests := []struct {
		name, old, new string
		ids            []string
		want           string
	}{
		{"failed to pending", "failed", "pending", nil,
			"T2.3: failed → pending.\nThe build loop may start it on its next pass."},
		{"running to pending", "running", "pending", nil,
			working + "\nT2.3: running → pending.\nThe build loop may start it on its next pass."},
		{"shipping to done", "shipping", "done", nil,
			working + "\nT2.3: shipping → done.\nTasks that depend on it may start."},
		{"to superseded with ids", "failed", "superseded", []string{"T2.3a", "T2.3b"},
			"T2.3: failed → superseded (replaced by T2.3a, T2.3b).\nTasks that depend on it will wait for the tasks that replace it."},
		{"unknown new status", "failed", "bogus", nil, "T2.3: failed → bogus."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfirmText("T2.3", tt.old, tt.new, tt.ids); got != tt.want {
				t.Fatalf("ConfirmText = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSplitIDs: commas and white space both separate, empties are dropped.
// The all-blank row is load-bearing: it is what the empty prompt answers.
func TestSplitIDs(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
	}{
		{"commas", "T1a,T1b", []string{"T1a", "T1b"}},
		{"comma and space", "T1a, T1b", []string{"T1a", "T1b"}},
		{"spaces and newline", " T1a  T1b\nT1c ", []string{"T1a", "T1b", "T1c"}},
		{"stray commas", ",,T1a,,", []string{"T1a"}},
		{"blank", "  \t ", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SplitIDs(tt.text); !slices.Equal(got, tt.want) {
				t.Fatalf("SplitIDs(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestSentences: the already, empty-ids and action-label words, each quoted
// whole because both surfaces show them as they are.
func TestSentences(t *testing.T) {
	if got, want := Already("T3.2", "done"), "T3.2 is already done."; got != want {
		t.Errorf("Already = %q, want %q", got, want)
	}
	if got, want := EmptyIDsPrompt("T3.2"), "Type the ids of the tasks that replace T3.2, for example T3.2a, T3.2b."; got != want {
		t.Errorf("EmptyIDsPrompt = %q, want %q", got, want)
	}
	for status, want := range map[string]string{"done": "Mark done", "pending": "Mark pending", "superseded": "Mark superseded"} {
		if got := ActionLabel(status); got != want {
			t.Errorf("ActionLabel(%q) = %q, want %q", status, got, want)
		}
	}
}
