package ui

import (
	"testing"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/projectrows"
)

// TestAnswerChoices pins the choice list. Load-bearing rows: "no options"
// (only the Own choice, so the screen opens straight in the field) and
// "recommended" (exactly the matching option is flagged, never Own).
func TestAnswerChoices(t *testing.T) {
	tests := []struct {
		name string
		it   inbox.Item
		want []answerChoice
	}{
		{"no options", inbox.Item{}, []answerChoice{{Label: "Write my own answer", Own: true}}},
		{"recommended", inbox.Item{Options: []string{"a", "b"}, Recommended: "b"}, []answerChoice{
			{Label: "a"}, {Label: "b", Recommended: true}, {Label: "Write my own answer", Own: true}}},
		{"none recommended", inbox.Item{Options: []string{"a"}}, []answerChoice{
			{Label: "a"}, {Label: "Write my own answer", Own: true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := answerChoices(tc.it)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("choice %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestAnswerStartChoice: the recommended start is load-bearing; an item with
// no options and an unmatched recommendation both fall back to 0.
func TestAnswerStartChoice(t *testing.T) {
	tests := []struct {
		name string
		it   inbox.Item
		want int
	}{
		{"recommended is second", inbox.Item{Options: []string{"a", "b"}, Recommended: "b"}, 1},
		{"no recommendation", inbox.Item{Options: []string{"a", "b"}}, 0},
		{"recommendation not an option", inbox.Item{Options: []string{"a"}, Recommended: "z"}, 0},
		{"no options", inbox.Item{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := answerStartChoice(tc.it); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAnswerTextFor: the "option whose text is a number" row proves the
// digit string is sent rather than the text; the empty own answer sends
// nothing.
func TestAnswerTextFor(t *testing.T) {
	numeric := answerChoices(inbox.Item{Options: []string{"3", "1"}})
	plain := answerChoices(inbox.Item{Options: []string{"yes", "no"}})
	tests := []struct {
		name    string
		choices []answerChoice
		idx     int
		own     string
		want    string
		ok      bool
	}{
		{"option by digit", plain, 1, "", "2", true},
		{"option whose text is a number", numeric, 0, "", "1", true},
		{"second numeric option", numeric, 1, "", "2", true},
		{"own answer as typed", plain, 2, " use blue ", " use blue ", true},
		{"empty own answer", plain, 2, "", "", false},
		{"blank own answer", plain, 2, "  \n", "", false},
		{"out of range", plain, 9, "x", "", false},
		{"negative", plain, -1, "x", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := answerTextFor(tc.choices, tc.idx, tc.own)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestOldestBlockingItem: ties by id and the answered blocking item being
// skipped are load-bearing; the slice order is deliberately scrambled.
func TestOldestBlockingItem(t *testing.T) {
	mk := func(id, at string, blocking bool, answer string) inbox.Item {
		return inbox.Item{ID: id, CreatedAt: at, Blocking: blocking, Answer: answer}
	}
	tests := []struct {
		name  string
		items []inbox.Item
		want  string
		ok    bool
	}{
		{"empty", nil, "", false},
		{"no blocking", []inbox.Item{mk("a", "2026-01-01T00:00:00Z", false, "")}, "", false},
		{"oldest wins regardless of order", []inbox.Item{
			mk("c", "2026-01-03T00:00:00Z", true, ""),
			mk("a", "2026-01-01T00:00:00Z", true, ""),
			mk("b", "2026-01-02T00:00:00Z", true, "")}, "a", true},
		{"tie goes to smaller id", []inbox.Item{
			mk("z", "2026-01-01T00:00:00Z", true, ""),
			mk("m", "2026-01-01T00:00:00Z", true, "")}, "m", true},
		{"answered blocking skipped", []inbox.Item{
			mk("a", "2026-01-01T00:00:00Z", true, "done"),
			mk("b", "2026-01-02T00:00:00Z", true, "")}, "b", true},
		{"non-blocking older is skipped", []inbox.Item{
			mk("a", "2026-01-01T00:00:00Z", false, ""),
			mk("b", "2026-01-02T00:00:00Z", true, "")}, "b", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := oldestBlockingItem(tc.items)
			if ok != tc.ok || got.ID != tc.want {
				t.Errorf("got (%q, %v), want (%q, %v)", got.ID, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestAnswerNextLine: one row per branch of the line under the choices.
func TestAnswerNextLine(t *testing.T) {
	tests := []struct {
		name string
		it   inbox.Item
		want string
	}{
		{"answered", inbox.Item{Blocking: true, Task: "T1", Answer: "x"}, ""},
		{"blocking with task", inbox.Item{Blocking: true, Task: "T1.2"}, "T1.2 starts again within a few seconds of your answer"},
		{"blocking without task", inbox.Item{Blocking: true}, "afk carries on within a few seconds of your answer"},
		{"not blocking", inbox.Item{Task: "T1"}, "Nothing waits on this; afk keeps your answer for the record"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := answerNextLine(tc.it); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAnswerItemPosition: answered items do not count, and an absent or
// answered id gives 0, 0.
func TestAnswerItemPosition(t *testing.T) {
	items := []inbox.Item{
		{ID: "a"}, {ID: "done", Answer: "x"}, {ID: "b"}, {ID: "c"},
	}
	tests := []struct {
		name       string
		id         string
		pos, total int
	}{
		{"first", "a", 1, 3},
		{"skips answered", "b", 2, 3},
		{"last", "c", 3, 3},
		{"answered id", "done", 0, 0},
		{"absent", "nope", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos, total := answerItemPosition(items, tc.id)
			if pos != tc.pos || total != tc.total {
				t.Errorf("got (%d, %d), want (%d, %d)", pos, total, tc.pos, tc.total)
			}
		})
	}
}

// TestAnswerKeyPicksByLevel pins which item `a` opens the answer screen on at
// each level. Load-bearing rows: "project picks the project's oldest, not the
// other project's" (Project must read the active project's items, never the
// All projects rows), "agent behaves like project and moves the keyboard to
// the list" (the screen opens over the Project) and "nothing blocking says
// so with no overlay" (the Project notice, distinct from the all-projects
// one).
func TestAnswerKeyPicksByLevel(t *testing.T) {
	mine := buildview.Snapshot{Root: "/repo", HasBuild: true, Items: []inbox.Item{
		{ID: "mine02", CreatedAt: "2026-10-19T09:00:00Z", Kind: "question", Blocking: true, Title: "newer"},
		{ID: "mine01", CreatedAt: "2026-10-18T09:00:00Z", Kind: "question", Blocking: true, Title: "older"},
		{ID: "fyi003", CreatedAt: "2026-10-17T09:00:00Z", Kind: "notice", Title: "fyi"},
	}}
	none := buildview.Snapshot{Root: "/repo", HasBuild: true, Items: []inbox.Item{
		{ID: "fyi003", CreatedAt: "2026-10-17T09:00:00Z", Kind: "notice", Title: "fyi"},
	}}
	// elsewhere holds the oldest blocking item of all, in another project.
	elsewhere := projectrows.Wait{ItemID: "other1", CreatedAt: "2026-10-01T09:00:00Z"}

	tests := []struct {
		name       string
		lv         level
		snap       buildview.Snapshot
		wantItem   string
		wantNotice string
	}{
		{"project picks the project's oldest, not the other project's", levelProject, mine, "mine01", ""},
		{"agent behaves like project and moves the keyboard to the list", levelAgent, mine, "mine01", ""},
		{"nothing blocking says so with no overlay", levelProject, none, "", nothingBlockingHereNotice},
		{"nothing blocking from the agent says so too", levelAgent, none, "", nothingBlockingHereNotice},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := allProjectsModel(t)
			m.allProjectsOpen = false
			m = withBuild(t, m, tc.snap)
			if len(m.allProjectsRows) > 0 {
				m.allProjectsRows[0].Wait = elsewhere
			}
			nm, _ := m.runLevelAction(tc.lv, inputNormal, actionAnswerOldest, false)
			got := nm.(Model)
			if tc.wantItem == "" {
				if got.inboxOpen || got.status != tc.wantNotice {
					t.Fatalf("inbox open = %v, status = %q; want no overlay and %q", got.inboxOpen, got.status, tc.wantNotice)
				}
				return
			}
			if !got.inboxOpen || got.inbox.screen != inboxScreenItem || got.inbox.item.ID != tc.wantItem {
				t.Fatalf("inbox open = %v, screen = %v, item = %q; want it open on %q", got.inboxOpen, got.inbox.screen, got.inbox.item.ID, tc.wantItem)
			}
			if got.focus != focusList {
				t.Errorf("focus = %v, want the list", got.focus)
			}
		})
	}

	t.Run("all projects picks across projects", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		for i := range m.allProjectsRows {
			m.allProjectsRows[i].Wait = projectrows.Wait{}
		}
		m.allProjectsRows[0].Wait = elsewhere
		r, ok := oldestBlockingRow(m.allProjectsRows)
		if !ok || r.Wait.ItemID != "other1" {
			t.Fatalf("oldestBlockingRow = %q, %v; want other1", r.Wait.ItemID, ok)
		}
	})
}
