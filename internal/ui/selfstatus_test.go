package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// rawSeqs returns the text of every tea.RawMsg a command produced.
func rawSeqs(t *testing.T, cmd tea.Cmd) []string {
	t.Helper()
	var out []string
	for _, msg := range collectMsgs(t, cmd) {
		if r, ok := msg.(tea.RawMsg); ok {
			s, _ := r.Msg.(string)
			out = append(out, s)
		}
	}
	return out
}

// TestSelfStatusProgramStatus drives allProjectsLoadedMsg through Update. The
// load-bearing rows: "flag off" (the dashboard under test sends nothing, so
// the pinned commands stay as they were), "same rows again" (a 2s tick must
// not rewrite the terminal's records) and "error result" (a failed read must
// not report that nothing needs the owner).
func TestSelfStatusProgramStatus(t *testing.T) {
	rows := allProjectsFixture()
	load := func(m Model, res AllProjects) (Model, []string) {
		m, cmd := update(t, m, allProjectsLoadedMsg{gen: m.allProjectsGen, seq: m.everyProjectSeq + 1, at: time.Now(), res: res})
		return m, rawSeqs(t, cmd)
	}
	newModel := func(on bool) Model {
		d := testDeps()
		d.ProgramStatus = on
		return New("/repo", time.Second, d)
	}

	t.Run("flag off sends nothing", func(t *testing.T) {
		_, raws := load(newModel(false), AllProjects{Rows: rows})
		if len(raws) != 0 {
			t.Fatalf("raw output with the flag off: %q", raws)
		}
	})
	t.Run("first load sends blocked", func(t *testing.T) {
		_, raws := load(newModel(true), AllProjects{Rows: rows})
		if len(raws) != 1 || !strings.Contains(raws[0], "state=blocked") {
			t.Fatalf("want one raw with state=blocked, got %q", raws)
		}
	})
	t.Run("same rows again sends nothing", func(t *testing.T) {
		m, _ := load(newModel(true), AllProjects{Rows: rows})
		_, raws := load(m, AllProjects{Rows: rows})
		if len(raws) != 0 {
			t.Fatalf("resent unchanged records: %q", raws)
		}
	})
	t.Run("changed row sends only the changes", func(t *testing.T) {
		m, first := load(newModel(true), AllProjects{Rows: rows})
		changed := append(rows[:0:0], rows...)
		changed[5].PlanWaiting = true
		_, raws := load(m, AllProjects{Rows: changed})
		if len(raws) != 1 || raws[0] == "" || len(raws[0]) >= len(first[0]) {
			t.Fatalf("want a smaller raw than the first send (%d bytes), got %q", len(first[0]), raws)
		}
	})
	t.Run("error result sends nothing", func(t *testing.T) {
		m, _ := load(newModel(true), AllProjects{Rows: rows})
		_, raws := load(m, AllProjects{Err: errors.New("bad toml")})
		if len(raws) != 0 {
			t.Fatalf("reported after a failed read: %q", raws)
		}
	})
}
