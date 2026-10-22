package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/state"
)

// TestStatusChangeStampsTheToast pins Model.notify (notification.go, which took
// the stamping over from Update's wrapper): a message that changes
// the status stamps statusChangedAt with the injected clock; one that leaves
// it alone, or clears it, does not. The clear row matters — a toast for an
// emptied status would be an empty box in the corner.
func TestStatusChangeStampsTheToast(t *testing.T) {
	m := testModel(sess("a"))
	if !m.statusChangedAt.IsZero() {
		t.Fatalf("a fresh model must not be stamped")
	}
	m = pressKey(t, m, keyRune('d')) // "scrolling the detail view"
	if m.statusChangedAt != m.deps.Now() {
		t.Fatalf("statusChangedAt = %v, want the clock's %v after a status change", m.statusChangedAt, m.deps.Now())
	}
	if !m.toastVisible() {
		t.Fatalf("a just-changed status must toast")
	}
	stamped := m.statusChangedAt
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // moves the cursor, no status write
	if m.statusChangedAt != stamped {
		t.Fatalf("an unchanged status must not be re-stamped")
	}
	m.status = ""
	if m.toastVisible() {
		t.Fatalf("an empty status must never toast")
	}
}

// TestToastExpiresByClock pins the no-Cmd design: visibility is a comparison
// against the injected clock, so advancing it past toastTTL hides the toast
// with no message delivered.
func TestToastExpiresByClock(t *testing.T) {
	m := testModel(sess("a"))
	m.status = "restarted a"
	m.statusChangedAt = m.deps.Now().Add(-toastTTL + time.Second)
	if !m.toastVisible() {
		t.Fatalf("a status %v old should still toast", toastTTL-time.Second)
	}
	m.statusChangedAt = m.deps.Now().Add(-toastTTL)
	if m.toastVisible() {
		t.Fatalf("a status exactly toastTTL old should no longer toast")
	}
}

// TestToastFloatsInTheCorner renders a fresh status and checks the frame: the
// box sits one row under the header at the right edge and carries the text.
//
// It used to assert the same text on the footer's status line as well. That
// row is gone — the toast is the only surface a notification has — so the
// footer half of the assertion went with it. The toast half is the whole
// contract now and stays.
func TestToastFloatsInTheCorner(t *testing.T) {
	m := testModel(sess("a"))
	m.status = "restarted a — connecting…"
	m.statusChangedAt = m.deps.Now()
	lines := strings.Split(xansi.Strip(render(m)), "\n")
	if !strings.Contains(lines[1], "╭") || !strings.HasSuffix(strings.TrimRight(lines[1], " "), "╮") {
		t.Fatalf("row 1 should carry the toast's top edge at the right margin: %q", lines[1])
	}
	if !strings.Contains(lines[2], "restarted a") {
		t.Fatalf("row 2 should carry the toast text: %q", lines[2])
	}
	if last := lines[len(lines)-1]; strings.Contains(last, "restarted a") {
		t.Fatalf("the footer must not repeat the toast's text: %q", last)
	}
}

// TestRenderToastWrapsAndClips: a long status wraps to the toast width and is
// cut at three lines, and a short one makes a box no wider than its text.
//
// Both calls pass notifyInfo, the kind zero value: it carries no marker
// (styleSet.toastKinds), so the widths this test pins predate kinds entirely
// and only the signature gained a parameter here.
func TestRenderToastWrapsAndClips(t *testing.T) {
	short := xansi.Strip(renderToast(notifyInfo, "saved", 120))
	if w := lipgloss.Width(short); w != len("saved")+4 {
		t.Fatalf("short toast is %d wide, want text plus padding and border (%d)", w, len("saved")+4)
	}
	long := xansi.Strip(renderToast(notifyInfo, strings.Repeat("word ", 60), 120))
	if h := lipgloss.Height(long); h != 5 {
		t.Fatalf("long toast is %d rows, want 3 text rows plus the border", h)
	}
	if w := lipgloss.Width(long); w > toastMaxWidth+4 {
		t.Fatalf("long toast is %d wide, want at most %d", w, toastMaxWidth+4)
	}
}

// TestRenderToastStylesEachKindDistinctly renders the same text at every
// notificationKind and asserts each produces different output (border colour
// and, for the three non-info kinds, a leading marker) — a kind that fell
// back to the same look as its neighbour would defeat the point of tagging
// notifications with a severity at all.
func TestRenderToastStylesEachKindDistinctly(t *testing.T) {
	kinds := []notificationKind{notifyInfo, notifySuccess, notifyWarn, notifyError}
	seen := make(map[string]notificationKind, len(kinds))
	for _, k := range kinds {
		out := renderToast(k, "outcome", 120)
		if prior, ok := seen[out]; ok {
			t.Fatalf("kind %d rendered identically to kind %d: %q", k, prior, out)
		}
		seen[out] = k
	}
}

// TestWindowTitleNamesProjectAndAwaiting covers the three shapes: bare, with a
// project (several known), and with the awaiting count.
func TestWindowTitleNamesProjectAndAwaiting(t *testing.T) {
	m := testModel(sess("a"))
	if got := m.windowTitle(); got != "afk" {
		t.Fatalf("title = %q, want \"afk\" with one project and nothing waiting", got)
	}
	m.projects = []project.Project{{Path: "/a", Name: "api"}, {Path: "/b", Name: "web"}}
	m.activeProject = 0
	if got := m.windowTitle(); got != "afk · api" {
		t.Fatalf("title = %q, want the project named", got)
	}
	waiting := sess("w")
	waiting.AwaitingInput = true
	m.setSessions([]Session{sess("a"), waiting})
	if got := m.windowTitle(); got != "afk · api · 1 awaiting input" {
		t.Fatalf("title = %q, want the awaiting count", got)
	}
}

// TestTerminalProgressAggregatesActiveSessions: the bar is the fleet's task
// completion while any planned session is still working, and absent once
// nothing is (no bar left nagging on the tab) or when nothing has a plan.
func TestTerminalProgressAggregatesActiveSessions(t *testing.T) {
	a := sess("a")
	a.Progress = discover.Progress{Total: 4, Complete: 1}
	b := sess("b")
	b.Progress = discover.Progress{Total: 6, Complete: 4}
	m := testModel(a, b, scratchSess("scratch-1"))
	pb := m.terminalProgress()
	if pb == nil || pb.State != tea.ProgressBarDefault || pb.Value != 50 {
		t.Fatalf("progress = %+v, want default state at 50%% (5 of 10 tasks)", pb)
	}

	a.Phase, b.Phase = state.PhaseDone, state.PhaseDone
	a.Progress.Complete, b.Progress.Complete = 4, 6
	m.setSessions([]Session{a, b})
	if pb := m.terminalProgress(); pb != nil {
		t.Fatalf("progress = %+v, want none once every session is finished", pb)
	}

	m.setSessions([]Session{scratchSess("scratch-1")})
	if pb := m.terminalProgress(); pb != nil {
		t.Fatalf("progress = %+v, want none with nothing planned", pb)
	}
}
