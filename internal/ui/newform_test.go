// newform_test.go tests newNewForm's base-option assembly directly (no
// Model/Update indirection), covering the git-spice arm's union with plain
// local branches (W2.T2) and pinning that the other two arms are untouched,
// plus the Provider selector's pure state transitions.
package ui

import (
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/spice"
)

// TestNewNewFormBaseOptions is the load-bearing regression guard for the
// worktrunk-invisibility bug: in git-spice mode, a session branch created by
// worktrunk is never git-spice-tracked, so the tracked stack alone hides it
// from the base picker (see the plan's Context). Row (a) is load-bearing —
// it fails if the git-spice arm reverts to session.BranchOptions, which
// drops every plain-git-only branch from the list.
func TestNewNewFormBaseOptions(t *testing.T) {
	tests := []struct {
		name          string
		stack         stackState
		plainBranches []string
		plainTrunk    string
		want          []string
		wantSelected  string
	}{
		{
			// (a) load-bearing: detected stack unions with plain branches,
			// deduplicated, trunk first, then stack order, then the plain
			// names not already present.
			name: "detected stack unions with plain branches",
			stack: stackState{Detected: true, Branches: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a"},
			}},
			plainBranches: []string{"bug-x", "feat-a", "main", "zeta"},
			want:          []string{"main", "feat-a", "bug-x", "zeta"},
			wantSelected:  "main",
		},
		{
			// (b) detected but empty stack falls through to the plain-trunk
			// arm unchanged (OrderTrunkFirst), per newNewForm's existing
			// fall-through comment — this task does not touch that arm.
			name:          "detected empty stack falls through to plain trunk ordering",
			stack:         stackState{Detected: true, Branches: nil},
			plainBranches: []string{"feature", "master"},
			plainTrunk:    "master",
			want:          []string{"master", "feature"},
			wantSelected:  "master",
		},
		{
			// (c) not detected: plain-trunk arm (or default) unchanged.
			name:          "not detected uses plain trunk ordering",
			stack:         stackState{Detected: false},
			plainBranches: []string{"feature-x", "main"},
			plainTrunk:    "main",
			want:          []string{"main", "feature-x"},
			wantSelected:  "main",
		},
		{
			// (d) detected stack with nil plain branches: union degrades to
			// the stack alone, matching session.BranchOptions today.
			name: "detected stack with nil plain branches is stack only",
			stack: stackState{Detected: true, Branches: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a"},
			}},
			plainBranches: nil,
			want:          []string{"main", "feat-a"},
			wantSelected:  "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNewForm(tt.stack, tt.plainBranches, tt.plainTrunk)

			items := f.baseList.Items()
			got := make([]string, len(items))
			for i, it := range items {
				got[i] = string(it.(baseItem))
			}
			if len(got) != len(tt.want) {
				t.Fatalf("base options = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("base options = %v, want %v", got, tt.want)
				}
			}

			sel := f.baseList.SelectedItem()
			if tt.wantSelected == "" {
				if sel != nil {
					t.Fatalf("selected = %v, want none preselected", sel)
				}
				return
			}
			if sel == nil || string(sel.(baseItem)) != tt.wantSelected {
				gotSel := ""
				if sel != nil {
					gotSel = string(sel.(baseItem))
				}
				t.Fatalf("selected = %q, want %q", gotSel, tt.wantSelected)
			}
		})
	}
}

// TestNewFormProviderCycle pins the selector's pure transitions on a bare
// newForm. Load-bearing rows: the default is provider.Default (an untouched
// form must create exactly what it did before the selector existed); a cycle
// that lands on codex ticks Autonomous; a cycle away from codex leaves the box
// alone; and a full lap in either direction returns to the start (the wrap).
func TestNewFormProviderCycle(t *testing.T) {
	names := provider.Names()
	tests := []struct {
		name           string
		startAuto      bool
		deltas         []int
		wantProvider   string
		wantAutonomous bool
	}{
		{"untouched form is the default provider", false, nil, provider.Default, false},
		{"forward to codex ticks autonomous", false, []int{1}, provider.CodexName, true},
		{"backward wraps to opencode, leaving autonomous alone", false, []int{-1}, provider.OpenCodeName, false},
		{"away from codex keeps autonomous ticked", false, []int{1, 1}, provider.OpenCodeName, true},
		{"full lap forward returns to the default", false, repeat(1, len(names)), provider.Default, true},
		{"full lap backward returns to the default", false, repeat(-1, len(names)), provider.Default, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNewForm(stackState{}, []string{"main"}, "main")
			f.autonomous = tt.startAuto
			for _, d := range tt.deltas {
				f.cycleProvider(d)
			}
			if got := f.providerName(); got != tt.wantProvider {
				t.Fatalf("provider = %q, want %q", got, tt.wantProvider)
			}
			if f.autonomous != tt.wantAutonomous {
				t.Fatalf("autonomous = %v, want %v", f.autonomous, tt.wantAutonomous)
			}
		})
	}
}

// TestNewFormValidateProvider pins validate's provider/mode gate: it defers to
// provider.CheckMode with scratch=false, so codex without Autonomous fails
// with the CLI's exact text recorded as the inline error, and every allowed
// pair returns the selected provider name. The name check still runs first, so
// a bad name reports the name error, not the mode one.
func TestNewFormValidateProvider(t *testing.T) {
	tests := []struct {
		name         string
		sessionName  string
		cycles       int
		autonomous   bool
		wantErr      string
		wantProvider string
	}{
		{"claude interactive is allowed", "ok-name", 0, false, "", provider.ClaudeName},
		{"claude autonomous is allowed", "ok-name", 0, true, "", provider.ClaudeName},
		{"codex autonomous is allowed", "ok-name", 1, true, "", provider.CodexName},
		{"codex interactive is refused with the CLI text", "ok-name", 1, false, provider.CodexInteractiveConflictMsg, ""},
		{"a bad name reports the name error first", "Bad_Name", 1, false, "invalid session name", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNewForm(stackState{}, []string{"main"}, "main")
			f.nameInput.SetValue(tt.sessionName)
			for i := 0; i < tt.cycles; i++ {
				f.cycleProvider(1)
			}
			f.autonomous = tt.autonomous
			_, _, _, _, got, err := f.validate()
			if tt.wantErr == "" {
				if err != nil || f.err != "" {
					t.Fatalf("validate err = %v (form err %q), want none", err, f.err)
				}
				if got != tt.wantProvider {
					t.Fatalf("provider = %q, want %q", got, tt.wantProvider)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate succeeded, want an error containing %q", tt.wantErr)
			}
			if !strings.HasPrefix(f.err, tt.wantErr) {
				t.Fatalf("form err = %q, want it to start with %q", f.err, tt.wantErr)
			}
		})
	}
}

// repeat returns n copies of d, for walking the selector one key at a time.
func repeat(d, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = d
	}
	return out
}
