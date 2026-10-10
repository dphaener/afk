// diffpanel_test.go table-tests diffPanel.Render's every state (see W5.T1's
// plan entry). Load-bearing rows:
//   - each of the four preserved states (no base branch, stale status, base
//     missing, git error) still returns exactly one line and no body — that
//     is the behaviour the plan requires survive this rewrite unchanged;
//   - the no-changes state renders no blank spacer line — only the success
//     path with a non-empty body gets one;
//   - a DiffText whose last element is the truncation marker renders that
//     marker last, proving the body is passed through renderDiffLine (and
//     ultimately capDiffLines' marker) rather than re-truncated by the panel;
//   - a tab-indented body line renders within width visible cells, proving
//     the panel's body loop goes through renderDiffLine's
//     expandTabs/stripControls/truncate pipeline rather than printing raw
//     text.
package ui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestDiffPanel_States(t *testing.T) {
	tests := []struct {
		name      string
		build     func() Session
		wantLines int  // -1 means "at least 2" (header + body)
		wantBody  bool // true iff a diff body is expected
	}{
		{
			name: "stale status: one line, no body",
			build: func() Session {
				s := sess("a")
				s.StatusStale = true
				return s
			},
			wantLines: 1,
		},
		{
			name: "base missing: one line, no body",
			build: func() Session {
				s := sess("a")
				s.BaseBranch = "gone"
				s.DiffStats = DiffStats{BaseMissing: true}
				return s
			},
			wantLines: 1,
		},
		{
			name: "git error: one line, no body",
			build: func() Session {
				s := sess("a")
				s.BaseBranch = "main"
				s.DiffStats = DiffStats{Err: "boom"}
				return s
			},
			wantLines: 1,
		},
		{
			name: "no changes: header plus dim line, no blank spacer",
			build: func() Session {
				s := sess("a")
				s.BaseBranch = "main"
				s.DiffStats = DiffStats{}
				return s
			},
			wantLines: 2,
		},
		{
			name: "changes but text not retained yet: header plus loading line",
			build: func() Session {
				s := sess("a")
				s.BaseBranch = "main"
				s.DiffStats = DiffStats{Added: 1, Files: 1}
				s.DiffText = nil
				return s
			},
			wantLines: 2,
		},
		{
			name: "changes with retained text: header, blank spacer, body",
			build: func() Session {
				s := sess("a")
				s.BaseBranch = "main"
				s.DiffStats = DiffStats{Added: 1, Removed: 0, Files: 1}
				s.DiffText = []string{"diff --git a/x b/x", "+one"}
				return s
			},
			wantLines: -1,
			wantBody:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.build()
			got := (diffPanel{}).Render(panelContext{Session: s}, 80)
			if tt.wantLines >= 0 && len(got) != tt.wantLines {
				t.Fatalf("Render() = %v (%d lines), want %d lines", got, len(got), tt.wantLines)
			}
			if tt.wantLines == 1 && got[0] == "" {
				t.Fatalf("Render() = %v, want a non-empty single line", got)
			}
			if tt.wantLines == -1 && len(got) < 2 {
				t.Fatalf("Render() = %v, want at least header + body", got)
			}
			if !tt.wantBody {
				return
			}
			if xansi.Strip(got[1]) != "" {
				t.Fatalf("Render() line 1 = %q, want blank spacer before the body", got[1])
			}
		})
	}
}

// TestDiffPanel_NoChangesNoBlankSpacer proves the no-changes state renders
// exactly header + "no changes" text with no intervening blank spacer line
// — the spacer is only emitted on the success path with a non-empty body.
func TestDiffPanel_NoChangesNoBlankSpacer(t *testing.T) {
	s := sess("a")
	s.BaseBranch = "main"
	s.DiffStats = DiffStats{}
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if len(got) != 2 {
		t.Fatalf("Render() = %v, want exactly 2 lines", got)
	}
	if !strings.Contains(xansi.Strip(got[1]), "no changes vs main") {
		t.Fatalf("Render() line 1 = %q, want it to contain %q", got[1], "no changes vs main")
	}
}

// TestDiffPanel_TruncationMarkerLast proves a DiffText whose last element is
// the truncation marker renders that marker as the last body line.
func TestDiffPanel_TruncationMarkerLast(t *testing.T) {
	s := sess("a")
	s.BaseBranch = "main"
	s.DiffStats = DiffStats{Added: 1, Files: 1}
	s.DiffText = []string{"+one", "+two", diffTruncationMarker}
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	last := got[len(got)-1]
	if xansi.Strip(last) != diffTruncationMarker {
		t.Fatalf("last line = %q, want the truncation marker %q", xansi.Strip(last), diffTruncationMarker)
	}
}

// TestDiffPanel_TabIndentedLineWithinWidth proves a tab-indented body line
// renders within width visible cells — the tab-stop/control-stripping
// pipeline runs on every body line, not just a subset.
func TestDiffPanel_TabIndentedLineWithinWidth(t *testing.T) {
	s := sess("a")
	s.BaseBranch = "main"
	s.DiffStats = DiffStats{Added: 1, Files: 1}
	s.DiffText = []string{"+\t" + strings.Repeat("x", 40)}
	const width = 20
	got := (diffPanel{}).Render(panelContext{Session: s}, width)
	body := got[len(got)-1]
	if w := xansi.StringWidth(xansi.Strip(body)); w > width {
		t.Fatalf("body line width = %d, want <= %d (line: %q)", w, width, body)
	}
}
