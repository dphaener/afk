package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
)

var (
	keyI     = keyRune('i')
	keyCtrlG = chord('g')
)

// inboxStamp is an RFC 3339 time d before the test clock (testDeps pins Now to
// time.Unix(1_000_000, 0)), so every age on screen is fixed.
func inboxStamp(d time.Duration) string {
	return time.Unix(1_000_000, 0).Add(-d).UTC().Format(time.RFC3339)
}

// inboxLongTitle is far wider than any overlay, to prove a title is cut.
var inboxLongTitle = "Decide whether the legacy session rows are migrated or dropped " + strings.Repeat("and then some ", 12) + "END-OF-TITLE"

// inboxLogBody is a 200-line body, the shape a failure item carries.
func inboxLogBody() string {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("log line %03d of the failing run", i+1)
	}
	return strings.Join(lines, "\n")
}

// inboxItems is the fixture inbox, in the store's order: an open blocking
// question with options, an open blocking failure with a long title and a
// long body, an open notice that blocks nothing, and an answered question.
func inboxItems() []inbox.Item {
	return []inbox.Item{
		{
			ID: "qst234", CreatedAt: inboxStamp(3 * time.Hour), Task: "T2.2", Session: "feat-auth",
			Kind: "question", Blocking: true,
			Title:       "Drop the legacy_sessions table?",
			Body:        "The migration removes legacy_sessions. Two reports still read it.",
			Options:     []string{"Drop it now", "Keep it for one release"},
			Recommended: "Keep it for one release",
		},
		{
			ID: "fal567", CreatedAt: inboxStamp(20 * time.Minute), Task: "T2.3", Session: "fix-login",
			Kind: "failure", Blocking: true,
			Title: inboxLongTitle,
			Body:  inboxLogBody(),
		},
		{
			ID: "ntc892", CreatedAt: inboxStamp(49 * time.Hour),
			Kind: "notice", Title: "Used the staging key for the search index",
			Assumed: "staging is fine for now",
		},
		{
			ID: "ans345", CreatedAt: inboxStamp(5 * time.Hour), Task: "T1.2",
			Kind: "question", Blocking: true, Title: "Which mail provider?",
			Options: []string{"Postal", "Mailer"},
			Answer:  "Postal", AnsweredAt: inboxStamp(2 * time.Minute), AnsweredVia: inbox.ViaWeb,
		},
	}
}

func inboxSnap() buildview.Snapshot {
	return buildview.Snapshot{Root: "/main", HasBuild: true, Items: inboxItems()}
}

// answerCall is one call to the AnswerInbox seam.
type answerCall struct{ root, id, text string }

// inboxModel is a Model at w×h holding the fixture inbox, with a recording
// AnswerInbox seam whose reply the test sets.
func inboxModel(t *testing.T, w, h int, reply func(answerCall) (inbox.Item, error)) (Model, *[]answerCall) {
	t.Helper()
	calls := &[]answerCall{}
	d := testDeps()
	d.AnswerInbox = func(root, id, text string) (inbox.Item, error) {
		c := answerCall{root, id, text}
		*calls = append(*calls, c)
		if reply == nil {
			return inbox.Item{}, errors.New("no reply set")
		}
		return reply(c)
	}
	m := New("/repo", time.Second, d)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m.setSessions([]Session{sess("alpha")})
	m = withBuild(t, m, inboxSnap())
	return m, calls
}

// openInboxItem opens the overlay and the item at index i of the list.
func openInboxItem(t *testing.T, m Model, i int) Model {
	t.Helper()
	m = pressKey(t, m, keyI)
	if !m.inboxOpen {
		t.Fatal("ctrl+] i did not open the inbox")
	}
	for range i {
		m, _ = update(t, m, keyRune('j'))
	}
	m, _ = update(t, m, keyEnter)
	if m.inbox.screen != inboxScreenItem {
		t.Fatalf("enter did not open item %d", i)
	}
	return m
}

func typeInbox(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = update(t, m, keyRune(r))
	}
	return m
}

// flat is s with every run of white space folded to one space, so a sentence
// can be found in a frame that wrapped it.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// boxText is the overlay's box with its border cells removed and folded, for
// finding a wrapped sentence inside it.
func boxText(m Model) string {
	var b strings.Builder
	for _, ln := range strings.Split(xansi.Strip(m.renderInboxModal()), "\n") {
		b.WriteString(strings.Trim(ln, "│╭╮╰╯─ "))
		b.WriteByte(' ')
	}
	return flat(b.String())
}

// TestInboxOverlayFitsTheFrame is the layout table for both screens at the
// three sizes the plan names. Each row checks the same things, all
// load-bearing: the box is inside the frame with its margin, the frame is
// still exactly the terminal's size (a line that wrapped would add a row and
// push the footer off), and the overlay's own lines are within the content
// width. 50x20 is the row that catches a column plan that only works wide.
func TestInboxOverlayFitsTheFrame(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 40}, {80, 24}, {50, 20}}
	for _, sz := range sizes {
		for _, screen := range []string{"list", "question", "failure", "answered"} {
			t.Run(fmt.Sprintf("%s at %dx%d", screen, sz.w, sz.h), func(t *testing.T) {
				m, _ := inboxModel(t, sz.w, sz.h, nil)
				switch screen {
				case "list":
					m = pressKey(t, m, keyI)
				case "question":
					m = openInboxItem(t, m, 0)
				case "failure":
					m = openInboxItem(t, m, 1)
				case "answered":
					m = openInboxItem(t, m, 3)
				}

				contentW, contentH := m.inboxSize()
				lines := strings.Split(m.inbox.View(), "\n")
				if len(lines) > contentH {
					t.Errorf("the overlay drew %d lines in %d rows", len(lines), contentH)
				}
				for i, ln := range lines {
					if w := lipgloss.Width(ln); w > contentW {
						t.Errorf("line %d is %d cells wide in a %d-cell overlay: %q", i, w, contentW, xansi.Strip(ln))
					}
				}

				box := m.renderInboxModal()
				if bw, bh := lipgloss.Width(box), lipgloss.Height(box); bw > sz.w-2*modalMargin || bh > sz.h-2*modalMargin {
					t.Errorf("the box is %dx%d, which does not fit %dx%d with its margin", bw, bh, sz.w, sz.h)
				}
				if bh := lipgloss.Height(box); bh != contentH+boxBorderCells {
					t.Errorf("the box is %d rows tall, want the %d content rows plus the border", bh, contentH)
				}

				frame := strings.Split(render(m), "\n")
				if len(frame) != sz.h {
					t.Errorf("the frame is %d rows, want %d", len(frame), sz.h)
				}
				for i, ln := range frame {
					if w := lipgloss.Width(ln); w > sz.w {
						t.Errorf("frame row %d is %d cells wide, want at most %d", i, w, sz.w)
					}
				}
				if !strings.Contains(strings.Join(frame, "\n"), "Inbox") {
					t.Errorf("the overlay is not on screen")
				}
			})
		}
	}
}

// TestInboxListRows pins what a list row says and that a long title is cut on
// its own row and never wrapped onto a second one. The 50-column row is the
// one that shows the id and the marker outliving the kind and task columns.
func TestInboxListRows(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{120, 40}, {80, 24}, {50, 20}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m, _ := inboxModel(t, sz.w, sz.h, nil)
			m = pressKey(t, m, keyI)
			lines := plain(strings.Split(m.inbox.View(), "\n"))
			if len(lines) != len(inboxItems()) {
				t.Fatalf("the list drew %d lines for %d items:\n%s", len(lines), len(inboxItems()), strings.Join(lines, "\n"))
			}
			for i, it := range inboxItems() {
				if !strings.Contains(lines[i], it.ID) {
					t.Errorf("row %d does not show the id %s: %q", i, it.ID, lines[i])
				}
				wantMarker := it.Open() && it.Blocking
				if got := strings.HasPrefix(lines[i], "!"); got != wantMarker {
					t.Errorf("row %d marker = %v, want %v: %q", i, got, wantMarker, lines[i])
				}
				if got := strings.Contains(lines[i], "answered"); got != !it.Open() {
					t.Errorf("row %d says answered = %v, want %v: %q", i, got, !it.Open(), lines[i])
				}
			}
			if !strings.Contains(lines[1], "…") || strings.Contains(strings.Join(lines, "\n"), "END-OF-TITLE") {
				t.Errorf("the long title was not cut: %q", lines[1])
			}
			if !strings.Contains(lines[0], "Drop the legacy") {
				t.Errorf("the first row has no readable title: %q", lines[0])
			}
			if sz.w >= 80 {
				if !strings.Contains(lines[0], "question") || !strings.Contains(lines[0], "T2.2") {
					t.Errorf("the kind and the task are missing at %d columns: %q", sz.w, lines[0])
				}
			}
		})
	}
}

// TestInboxColumnsFor is the column plan as a table. The load-bearing rows are
// the two drops: the kind goes before the task, and neither goes while the
// title still has room.
func TestInboxColumnsFor(t *testing.T) {
	items := inboxItems()
	noTasks := []inbox.Item{{ID: "abc234", Kind: "notice", Title: "x"}}
	cases := []struct {
		name  string
		items []inbox.Item
		width int
		want  inboxColumns
	}{
		{"wide shows every column", items, 84, inboxColumns{id: 6, kind: 8, task: 4}},
		{"just enough room keeps every column", items, 56, inboxColumns{id: 6, kind: 8, task: 4}},
		{"one cell under drops the kind first", items, 55, inboxColumns{id: 6, task: 4}},
		{"narrower still drops the task too", items, 42, inboxColumns{id: 6}},
		{"no item has a task, so there is no task column", noTasks, 84, inboxColumns{id: 6, kind: 6}},
		{"empty input", nil, 84, inboxColumns{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboxColumnsFor(tc.items, tc.width); got != tc.want {
				t.Errorf("inboxColumnsFor(width %d) = %+v, want %+v", tc.width, got, tc.want)
			}
		})
	}
}

// TestInboxItemLines is the answer screen's scrolling content as a table over
// the pure function. Load-bearing rows: the facts line carries the id (what
// `afk inbox answer` takes) and the title is there in full, while the options
// are NOT (they are the choices under the body, and an answered item shows
// its answer instead); an answered item names where the answer came from; and
// text from the file cannot carry an escape sequence onto the screen.
func TestInboxItemLines(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	items := inboxItems()
	cases := []struct {
		name   string
		item   inbox.Item
		want   []string
		absent []string
	}{
		{
			name: "an open blocking question",
			item: items[0],
			want: []string{
				"question · qst234 · T2.2 · from feat-auth · raised 3 hours ago · the task waits for your answer",
				"Drop the legacy_sessions table?",
				"The migration removes legacy_sessions.",
			},
			absent: []string{"Answer:", "Assumed answer", "Options", "1. Drop it now", "Keep it for one release"},
		},
		{
			name:   "a notice with an assumed answer",
			item:   items[2],
			want:   []string{"notice · ntc892 · raised 2 days ago · does not hold a task up", "Assumed answer: staging is fine for now"},
			absent: []string{"Options", "Answer:", "from "},
		},
		{
			name:   "answered from the web page",
			item:   items[3],
			want:   []string{"held the task up until it was answered", "Answer: Postal", "Answered 2 minutes ago from the web page."},
			absent: []string{"Mailer", "1. Postal"},
		},
		{
			name: "answered from the dashboard",
			item: inbox.Item{ID: "abc234", Kind: "notice", Title: "t", Answer: "ok", AnsweredAt: inboxStamp(10 * time.Second), AnsweredVia: inbox.ViaTUI},
			want: []string{"Answer: ok", "Answered just now from the dashboard."},
		},
		{
			name: "answered from the command line, with no time recorded",
			item: inbox.Item{ID: "abc234", Kind: "notice", Title: "t", Answer: "ok", AnsweredVia: inbox.ViaCLI},
			want: []string{"Answered from the command line."},
		},
		{
			name:   "a time that cannot be read is left out",
			item:   inbox.Item{ID: "abc234", Kind: "notice", Title: "t", CreatedAt: "yesterday"},
			want:   []string{"notice · abc234 · does not hold a task up"},
			absent: []string{"raised"},
		},
		{
			name:   "escape sequences and control characters are removed",
			item:   inbox.Item{ID: "abc234", Kind: "failure", Title: "t", Body: "red \x1b[31mtext\x1b[0m\ttab\x07bell\r\nnext"},
			want:   []string{"red text    tabbell", "next"},
			absent: []string{"\x1b", "\x07", "\t", "\r"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, width := range []int{80, 40, 12} {
				lines := inboxItemLines(tc.item, now, width)
				for i, ln := range lines {
					if w := lipgloss.Width(ln); w > width {
						t.Errorf("width %d: line %d is %d cells: %q", width, i, w, xansi.Strip(ln))
					}
				}
			}
			text := strings.Join(plain(inboxItemLines(tc.item, now, 100)), "\n")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in:\n%s", want, text)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(text, bad) {
					t.Errorf("unexpected %q in:\n%s", bad, text)
				}
			}
		})
	}
}

// TestInboxAgo pins the age words at their boundaries.
func TestInboxAgo(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name  string
		stamp string
		want  string
	}{
		{"empty", "", ""},
		{"not a time", "soon", ""},
		{"a few seconds", inboxStamp(30 * time.Second), "just now"},
		{"slightly in the future", inboxStamp(-5 * time.Second), "just now"},
		{"one minute", inboxStamp(time.Minute), "1 minute ago"},
		{"59 minutes", inboxStamp(59 * time.Minute), "59 minutes ago"},
		{"one hour", inboxStamp(time.Hour), "1 hour ago"},
		{"47 hours", inboxStamp(47 * time.Hour), "47 hours ago"},
		{"two days", inboxStamp(48 * time.Hour), "2 days ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboxAgo(tc.stamp, now); got != tc.want {
				t.Errorf("inboxAgo(%q) = %q, want %q", tc.stamp, got, tc.want)
			}
		})
	}
}

// TestInboxLongBodyScrolls drives the 200-line failure item at each size: the
// first page shows the start and not the end, pgdn moves the page without
// typing anything, and enough of them reach the last line. pgup goes back.
// The input staying empty is load-bearing: the scroll keys must not be text.
func TestInboxLongBodyScrolls(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{120, 40}, {80, 24}, {50, 20}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m, _ := inboxModel(t, sz.w, sz.h, nil)
			m = openInboxItem(t, m, 1)
			first := xansi.Strip(m.inbox.View())
			if !strings.Contains(first, "log line 001") || strings.Contains(first, "log line 200") {
				t.Fatalf("the first page should hold the start of the body only:\n%s", first)
			}
			if !strings.Contains(first, "> ") {
				t.Errorf("the answer field is not on screen under a long body:\n%s", first)
			}
			if m.inbox.hint() != "0%" {
				t.Errorf("hint = %q, want 0%% at the top of a body that scrolls", m.inbox.hint())
			}
			m, _ = update(t, m, keyPgDown)
			if second := xansi.Strip(m.inbox.View()); second == first {
				t.Fatal("pgdn did not move the body")
			}
			for range 200 {
				m, _ = update(t, m, keyPgDown)
			}
			if last := xansi.Strip(m.inbox.View()); !strings.Contains(last, "log line 200") {
				t.Fatalf("the end of the body was never reached:\n%s", last)
			}
			if m.inbox.hint() != "100%" {
				t.Errorf("hint = %q at the bottom, want 100%%", m.inbox.hint())
			}
			m, _ = update(t, m, keyPgUp)
			if strings.Contains(xansi.Strip(m.inbox.View()), "log line 200") {
				t.Error("pgup did not move the body back")
			}
			if got := m.inbox.input.Value(); got != "" {
				t.Errorf("the scroll keys typed %q into the answer", got)
			}
		})
	}
}

// TestInboxKeys is the key routing on the list and on the answer screen in
// both its states, through Update. The load-bearing rows are the ones about
// not quitting: `q` and ctrl+c are the dashboard's quit keys everywhere else,
// and here `q` does nothing while choosing and is a letter in the field,
// because a quit would lose what was typed. The esc rows pin where back goes:
// out of the field keeping its text, then to the list when the item was
// opened from it, and out of the overlay when it was opened straight on.
func TestInboxKeys(t *testing.T) {
	t.Run("the list: move, read, close", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m, cmd := pressAction(t, m, keyI)
		if cmd != nil || !m.inboxOpen || m.inbox.screen != inboxScreenList {
			t.Fatalf("i: open=%v screen=%v cmd=%v, want the list and no command", m.inboxOpen, m.inbox.screen, cmd != nil)
		}
		if got := activeInputMode(m.modeSnapshot()); got != inputInbox {
			t.Fatalf("mode = %v, want inbox", got)
		}
		steps := []struct {
			key  tea.Msg
			want int
		}{
			{keyRune('k'), 0}, {keyUp, 0}, {keyRune('j'), 1}, {keyDown, 2}, {keyRune('j'), 3},
			{keyDown, 3}, {keyRune('k'), 2}, {keyUp, 1},
		}
		for i, st := range steps {
			m, _ = update(t, m, st.key)
			if m.inbox.cursor != st.want {
				t.Fatalf("step %d: cursor = %d, want %d", i, m.inbox.cursor, st.want)
			}
		}
		for _, k := range []tea.Msg{keyRune('q'), chord('c'), keyRune('b'), keyRune('i'), keyRune('n'), keyRune(':'), keyRune('?')} {
			m, _ = update(t, m, k)
			if m.quitting || !m.inboxOpen || m.formOpen || m.leaderOpen || m.keysOpen || m.inbox.screen != inboxScreenList {
				t.Fatalf("%v leaked out of the list screen", k)
			}
		}
		m, _ = update(t, m, keyEnter)
		if m.inbox.screen != inboxScreenItem || m.inbox.item.ID != "fal567" {
			t.Fatalf("enter opened %q on screen %v, want fal567", m.inbox.item.ID, m.inbox.screen)
		}
	})

	for _, tc := range []struct {
		name string
		key  tea.Msg
	}{{"esc closes the list", keyEsc}, {"ctrl+g closes the list", keyCtrlG}} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := inboxModel(t, 120, 40, nil)
			m = pressKey(t, m, keyI)
			capture := m.mouseCapture
			m, _ = update(t, m, tc.key)
			if m.inboxOpen || m.quitting {
				t.Fatalf("open=%v quitting=%v, want the overlay closed and the dashboard running", m.inboxOpen, m.quitting)
			}
			if got := activeInputMode(m.modeSnapshot()); got != inputNormal {
				t.Errorf("mode = %v after closing, want normal", got)
			}
			// ctrl+g is the mouse chord everywhere else; here it only closes.
			if m.mouseCapture != capture {
				t.Errorf("closing the overlay toggled the mouse capture")
			}
		})
	}

	t.Run("choosing: q and every other letter do nothing, and nothing quits", func(t *testing.T) {
		m, calls := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "qbi:x ")
		m, _ = update(t, m, chord('c'))
		if m.quitting {
			t.Fatal("a key quit the dashboard from the answer screen")
		}
		if !m.inboxOpen || m.inbox.screen != inboxScreenItem || m.inbox.item.ID != "qst234" {
			t.Fatalf("a letter left the answer screen (open=%v screen=%v item=%q)", m.inboxOpen, m.inbox.screen, m.inbox.item.ID)
		}
		if m.inbox.typing || m.inbox.input.Value() != "" || m.inbox.choice != 1 {
			t.Fatalf("typing=%v text=%q choice=%d, want still choosing on the recommended option", m.inbox.typing, m.inbox.input.Value(), m.inbox.choice)
		}
		if m.focusedSection() != sectionSessions || m.leaderOpen || m.formOpen {
			t.Error("a letter acted on the dashboard underneath")
		}
		if len(*calls) != 0 {
			t.Errorf("letters sent %d answers", len(*calls))
		}
	})

	t.Run("typing: q and every other letter is text, and nothing quits", func(t *testing.T) {
		m, calls := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m, _ = update(t, m, keyRune('3'))
		if !m.inbox.typing || m.inbox.choice != 2 {
			t.Fatalf("3 on a two-option item: typing=%v choice=%d, want the field on the last choice", m.inbox.typing, m.inbox.choice)
		}
		m = typeInbox(t, m, "quit? b i j k 1 :")
		m, _ = update(t, m, chord('c'))
		if m.quitting {
			t.Fatal("a key quit the dashboard from the answer field")
		}
		if got := m.inbox.input.Value(); got != "quit? b i j k 1 :" {
			t.Fatalf("the answer field holds %q, want what was typed", got)
		}
		if m.inbox.item.ID != "qst234" || len(*calls) != 0 {
			t.Errorf("typing moved the item (%q) or sent %d answers", m.inbox.item.ID, len(*calls))
		}
	})

	t.Run("a digit past the last choice does nothing", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m, _ = update(t, m, keyRune('9'))
		if m.inbox.choice != 1 || m.inbox.typing {
			t.Fatalf("9 on a three-choice item: choice=%d typing=%v", m.inbox.choice, m.inbox.typing)
		}
	})

	t.Run("esc in the field keeps the text and goes back to the choices, then to the list, then closes", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m, _ = update(t, m, keyRune('3'))
		m = typeInbox(t, m, "draft")
		m, _ = update(t, m, keyEsc)
		if m.inbox.screen != inboxScreenItem || m.inbox.typing || m.inbox.input.Value() != "draft" {
			t.Fatalf("first esc: screen=%v typing=%v text=%q, want the choices with the text kept", m.inbox.screen, m.inbox.typing, m.inbox.input.Value())
		}
		if view := xansi.Strip(m.inbox.View()); !strings.Contains(view, "> draft") {
			t.Errorf("the kept text is not shown under the choices:\n%s", view)
		}
		m, _ = update(t, m, keyEsc)
		if !m.inboxOpen || m.inbox.screen != inboxScreenList {
			t.Fatalf("second esc: open=%v screen=%v, want the list", m.inboxOpen, m.inbox.screen)
		}
		if m.inbox.cursor != 0 {
			t.Errorf("back on the list the cursor is on %d, want the item just read (0)", m.inbox.cursor)
		}
		m, _ = update(t, m, keyEsc)
		if m.inboxOpen {
			t.Fatal("third esc did not close the overlay")
		}
	})

	t.Run("an item with no options opens in the field and esc goes back to the list", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 2)
		if !m.inbox.typing {
			t.Fatal("an item with no options did not open in the field")
		}
		m = typeInbox(t, m, "q")
		m, _ = update(t, m, keyEsc)
		if !m.inboxOpen || m.inbox.screen != inboxScreenList || m.inbox.cursor != 2 {
			t.Fatalf("esc: open=%v screen=%v cursor=%d, want the list on item 2", m.inboxOpen, m.inbox.screen, m.inbox.cursor)
		}
	})

	t.Run("opened straight on an item, esc closes the overlay", func(t *testing.T) {
		for _, id := range []string{"qst234", "ntc892", "ans345"} {
			m, _ := inboxModel(t, 120, 40, nil)
			m = m.openInboxOn(id)
			if !m.inboxOpen || m.inbox.screen != inboxScreenItem || m.inbox.fromList {
				t.Fatalf("%s: open=%v screen=%v fromList=%v", id, m.inboxOpen, m.inbox.screen, m.inbox.fromList)
			}
			m, _ = update(t, m, keyEsc)
			if m.inboxOpen {
				t.Errorf("%s: esc went to a list the owner never saw instead of closing", id)
			}
		}
	})

	t.Run("ctrl+g closes the overlay outright, choosing or typing", func(t *testing.T) {
		for _, typing := range []bool{false, true} {
			m, _ := inboxModel(t, 120, 40, nil)
			m = openInboxItem(t, m, 0)
			if typing {
				m, _ = update(t, m, keyRune('3'))
				m = typeInbox(t, m, "draft")
			}
			m, _ = update(t, m, keyCtrlG)
			if m.inboxOpen || m.quitting {
				t.Fatalf("typing=%v: open=%v quitting=%v, want closed and running", typing, m.inboxOpen, m.quitting)
			}
		}
	})

	t.Run("text typed for one item is not carried to the next", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m, _ = update(t, m, keyRune('3'))
		m = typeInbox(t, m, "for the first")
		m, _ = update(t, m, keyEsc)
		m, _ = update(t, m, keyDown)
		if m.inbox.item.ID != "fal567" || m.inbox.input.Value() != "" {
			t.Fatalf("down moved to %q with %q in the field, want fal567 and an empty field", m.inbox.item.ID, m.inbox.input.Value())
		}
		m, _ = update(t, m, keyCtrlG)
		m = openInboxItem(t, m, 2)
		if got := m.inbox.input.Value(); got != "" {
			t.Fatalf("the next item's field starts with %q", got)
		}
	})

	t.Run("an answered item has no choices and no field and enter does nothing", func(t *testing.T) {
		m, calls := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 3)
		view := xansi.Strip(m.inbox.View())
		for _, bad := range []string{"> ", inboxChooseHeading, ownChoiceLabel, "of 3"} {
			if strings.Contains(view, bad) {
				t.Errorf("an answered item shows %q:\n%s", bad, view)
			}
		}
		for _, want := range []string{"Answer: Postal", "from the web page"} {
			if !strings.Contains(view, want) {
				t.Errorf("missing %q in:\n%s", want, view)
			}
		}
		m = typeInbox(t, m, "1again")
		m, cmd := update(t, m, keyEnter)
		if cmd != nil || len(*calls) != 0 {
			t.Fatalf("enter on an answered item issued a command or sent an answer (calls=%d)", len(*calls))
		}
		if m.inbox.screen != inboxScreenItem || m.inbox.input.Value() != "" {
			t.Errorf("screen=%v text=%q, want the item unchanged", m.inbox.screen, m.inbox.input.Value())
		}
		m, _ = update(t, m, keyEsc)
		if m.inbox.screen != inboxScreenList {
			t.Error("esc on an answered item did not go back to the list")
		}
	})

	t.Run("a paste goes into the field only while it has the keys", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m, _ = update(t, m, tea.PasteMsg{Content: "sk-live-123"})
		if got := m.inbox.input.Value(); got != "" {
			t.Fatalf("a paste while choosing went into the unseen field: %q", got)
		}
		m, _ = update(t, m, keyRune('3'))
		m, _ = update(t, m, tea.PasteMsg{Content: "sk-live-123"})
		if got := m.inbox.input.Value(); got != "sk-live-123" {
			t.Fatalf("after a paste the field holds %q", got)
		}
	})
}

// TestInboxAnswerScreen pins what the answer screen draws for one item: the
// strip naming the inbox and the project with the item's place among the
// open ones, CHOOSE ONE with the options numbered, the recommended one
// labelled and selected, the last choice to write one's own, and the line on
// what happens next. The load-bearing rows are the selection (the fill is on
// exactly the selected choice, drawn by §3.5's row) and the count, which
// skips answered items because the arrows do.
func TestInboxAnswerScreen(t *testing.T) {
	m, _ := inboxModel(t, 120, 40, nil)
	m = openInboxItem(t, m, 0)
	view := plain(strings.Split(m.inbox.View(), "\n"))
	text := strings.Join(view, "\n")
	if !strings.HasPrefix(strings.TrimSpace(view[0]), "Inbox · main") || !strings.HasSuffix(strings.TrimSpace(view[0]), "1 of 3 · ↑↓ other items") {
		t.Errorf("strip = %q, want the inbox, the project and 1 of 3", view[0])
	}
	for _, want := range []string{
		inboxChooseHeading,
		"1. Drop it now",
		"2. Keep it for one release  recommended",
		"3. " + ownChoiceLabel,
		"T2.2 starts again within a few seconds of your answer",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	raw := strings.Split(m.inbox.View(), "\n")
	c := answerChoices(m.inbox.item)[1]
	if inboxChoiceLines(1, c, m.inbox.width, true)[0] == inboxChoiceLines(1, c, m.inbox.width, false)[0] {
		t.Fatal("fixture broken: a selected choice draws the same as an unselected one, so the rows below prove nothing")
	}
	if got, want := raw[inboxLineIndex(view, "Keep it")], inboxChoiceLines(1, answerChoices(m.inbox.item)[1], m.inbox.width, true)[0]; got != want {
		t.Errorf("the selected choice is not drawn with the row selection:\n got %q\nwant %q", got, want)
	}
	if got, want := raw[inboxLineIndex(view, "Drop it now")], inboxChoiceLines(0, answerChoices(m.inbox.item)[0], m.inbox.width, false)[0]; got != want {
		t.Errorf("an unselected choice is drawn selected:\n got %q\nwant %q", got, want)
	}

	m, _ = update(t, m, keyRune('1'))
	moved := strings.Split(m.inbox.View(), "\n")
	if got, want := moved[inboxLineIndex(view, "Drop it now")], inboxChoiceLines(0, answerChoices(m.inbox.item)[0], m.inbox.width, true)[0]; got != want {
		t.Errorf("1 did not move the selection fill to the first choice:\n got %q\nwant %q", got, want)
	}

	t.Run("a notice that blocks nothing says so", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 2)
		text := strings.Join(plain(strings.Split(m.inbox.View(), "\n")), "\n")
		if !strings.Contains(text, "Nothing waits on this; afk keeps your answer for the record") || !strings.Contains(text, "3 of 3") {
			t.Errorf("the notice's screen:\n%s", text)
		}
		if strings.Contains(text, inboxOtherItemsHint) {
			t.Errorf("the strip offers the arrows while they type into the field:\n%s", text)
		}
	})
}

// inboxLineIndex is the first line of lines holding sub, or -1.
func inboxLineIndex(lines []string, sub string) int {
	for i, ln := range lines {
		if strings.Contains(ln, sub) {
			return i
		}
	}
	return -1
}

// TestInboxMovesBetweenItems drives ↑ and ↓ on the answer screen over a
// fixture with an answered item between two open ones. Load-bearing rows: the
// answered item is skipped (there is nothing to answer on it, and the count
// does not include it), the ends stay put rather than wrapping, and each item
// starts fresh on its recommended option.
func TestInboxMovesBetweenItems(t *testing.T) {
	items := []inbox.Item{
		{ID: "aaa234", Kind: "question", Blocking: true, Title: "First", Options: []string{"a", "b"}, Recommended: "b"},
		{ID: "bbb234", Kind: "question", Title: "Answered", Options: []string{"x"}, Answer: "x", AnsweredVia: inbox.ViaCLI},
		{ID: "ccc234", Kind: "question", Blocking: true, Title: "Second", Options: []string{"c", "d", "e"}},
	}
	m, _ := inboxModel(t, 120, 40, nil)
	m = withBuild(t, m, buildview.Snapshot{Root: "/main", Items: items})
	m = m.openInboxOn("aaa234")
	cases := []struct {
		name       string
		key        tea.Msg
		wantID     string
		wantChoice int
		wantCount  string
	}{
		{"up at the first item stays", keyUp, "aaa234", 1, "1 of 2"},
		{"down skips the answered item", keyDown, "ccc234", 0, "2 of 2"},
		{"down at the last item stays", keyRune('j'), "ccc234", 0, "2 of 2"},
		{"k goes back, on the recommended option again", keyRune('k'), "aaa234", 1, "1 of 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ = update(t, m, tc.key)
			if m.inbox.item.ID != tc.wantID || m.inbox.choice != tc.wantChoice {
				t.Fatalf("item=%q choice=%d, want %q on %d", m.inbox.item.ID, m.inbox.choice, tc.wantID, tc.wantChoice)
			}
			if got := m.inbox.stripContext(); got != tc.wantCount+" · "+inboxOtherItemsHint {
				t.Errorf("strip context = %q, want %q", got, tc.wantCount)
			}
		})
	}
}

// TestInboxEmptyAnswer: enter in the field with nothing typed, or only
// spaces, sends nothing and says so under the field. "Sends nothing" is the
// load-bearing half: an empty answer written to the file would be an item
// that reads as open forever while the store refuses every later answer to
// it. The no-options row matters because that item opens in the field with
// no choice to fall back on.
func TestInboxEmptyAnswer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
		keys  string
	}{
		{"own answer, nothing typed", 0, "3"},
		{"own answer, only spaces", 0, "3   "},
		{"no options, nothing typed", 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, calls := inboxModel(t, 120, 40, nil)
			m = openInboxItem(t, m, tc.index)
			m = typeInbox(t, m, tc.keys)
			m, cmd := update(t, m, keyEnter)
			if cmd != nil {
				t.Fatalf("enter on an empty answer issued a command (%T)", cmd())
			}
			if len(*calls) != 0 {
				t.Fatalf("an empty answer was sent: %+v", *calls)
			}
			if m.inbox.screen != inboxScreenItem || !m.inbox.typing {
				t.Fatal("the empty answer left the field")
			}
			if got := boxText(m); !strings.Contains(got, inboxEmptyAnswerNote) {
				t.Errorf("the box does not say %q:\n%s", inboxEmptyAnswerNote, got)
			}
			// Typing is the fix the note asks for, so it clears the note.
			m, _ = update(t, m, keyRune('y'))
			if got := boxText(m); strings.Contains(got, inboxEmptyAnswerNote) {
				t.Errorf("the note outlived the next keystroke")
			}
		})
	}
}

// TestInboxAnswerSent is the successful answer, one row per way to give it.
// Load-bearing rows:
//
//   - an option goes out as its NUMBER, a digit or enter on the recommended
//     one alike: the store resolves an in-range number to that option exactly
//     as `afk inbox answer <id> 2` does, so the item file comes out the same
//     as from the command line;
//   - an own answer goes out exactly as typed, a bare "2" included: the store
//     turns a number into its option, and resolving it here too would pick
//     the wrong option when the options are themselves numbers;
//   - the seam is called with the SNAPSHOT's root (/main), not the folder the
//     dashboard was started in (/repo), because the inbox lives in the main
//     checkout and the dashboard may be running inside a session's worktree;
//   - nothing is written on the Update goroutine: the call count is zero
//     until the returned command runs;
//   - after the answer the screen goes back to where it was opened from: the
//     list, or out of the overlay when opened straight on the item;
//   - the two notices differ on whether a task was waiting.
func TestInboxAnswerSent(t *testing.T) {
	cases := []struct {
		name       string
		openOn     string // "" opens item index from the list
		index      int
		keys       []tea.Msg
		wantID     string
		wantText   string
		wantNotice string
	}{
		{"an option by its digit", "", 0, []tea.Msg{keyRune('1'), keyEnter}, "qst234", "1",
			"Answered qst234. The task carries on within a few seconds."},
		{"the recommended option by enter alone", "", 0, []tea.Msg{keyEnter}, "qst234", "2",
			"Answered qst234. The task carries on within a few seconds."},
		{"an own answer", "", 0, append(append([]tea.Msg{keyRune('3')}, keyRunes("keep it, and tell me who reads it")...), keyEnter), "qst234",
			"keep it, and tell me who reads it", "Answered qst234. The task carries on within a few seconds."},
		{"an own answer that is a number goes out as typed", "", 0, []tea.Msg{keyRune('3'), keyRune('2'), keyEnter}, "qst234", "2",
			"Answered qst234. The task carries on within a few seconds."},
		{"an item with no options", "", 2, append(keyRunes("use production"), keyEnter), "ntc892", "use production", "Answered ntc892."},
		{"down to the next item, then answered", "", 0, append(append([]tea.Msg{keyDown}, keyRunes("retry it")...), keyEnter), "fal567",
			"retry it", "Answered fal567. The task carries on within a few seconds."},
		{"opened straight on the item", "qst234", 0, []tea.Msg{keyEnter}, "qst234", "2",
			"Answered qst234. The task carries on within a few seconds."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, calls := inboxModel(t, 120, 40, func(c answerCall) (inbox.Item, error) {
				for _, it := range inboxItems() {
					if it.ID == c.id {
						it.Answer, it.AnsweredAt, it.AnsweredVia = "stored:"+c.text, inboxStamp(0), inbox.ViaTUI
						return it, nil
					}
				}
				return inbox.Item{}, inbox.ErrNotFound
			})
			loads := 0
			m.deps.LoadBuild = func(string, []BuildSession) buildview.Snapshot {
				loads++
				return inboxSnap()
			}
			if tc.openOn != "" {
				m = m.openInboxOn(tc.openOn)
			} else {
				m = openInboxItem(t, m, tc.index)
			}
			var cmd tea.Cmd
			for _, k := range tc.keys {
				m, cmd = update(t, m, k)
			}
			if len(*calls) != 0 {
				t.Fatal("the answer was written from Update, not from a command")
			}
			if got := boxText(m); !strings.Contains(got, inboxSendingNote) {
				t.Errorf("the box does not say the answer is being saved:\n%s", got)
			}
			// A second enter while the first is on its way sends nothing more.
			m, again := update(t, m, keyEnter)
			if again != nil {
				t.Fatal("a second enter issued a second send")
			}

			msgs := collectMsgs(t, cmd)
			if len(msgs) != 1 {
				t.Fatalf("the send produced %d messages, want one", len(msgs))
			}
			if want := (answerCall{"/main", tc.wantID, tc.wantText}); len(*calls) != 1 || (*calls)[0] != want {
				t.Fatalf("seam calls = %+v, want exactly %+v", *calls, want)
			}

			m, refresh := update(t, m, msgs[0])
			if m.status != tc.wantNotice {
				t.Errorf("notice = %q, want %q", m.status, tc.wantNotice)
			}
			if !hasMsg[buildLoadedMsg](collectMsgs(t, refresh)) || loads != 1 {
				t.Errorf("the answer did not ask for a fresh read of the inbox (loads=%d)", loads)
			}
			if tc.openOn != "" {
				if m.inboxOpen {
					t.Fatal("an answer to an item opened straight on stayed in the overlay")
				}
				return
			}
			if !m.inboxOpen || m.inbox.screen != inboxScreenList {
				t.Fatalf("after the answer: open=%v screen=%v, want the list", m.inboxOpen, m.inbox.screen)
			}
			if row := xansi.Strip(strings.Split(m.inbox.View(), "\n")[m.inbox.cursor]); !strings.Contains(row, tc.wantID) || !strings.Contains(row, "answered") {
				t.Errorf("the answered item's row does not say so yet: %q", row)
			}
		})
	}
}

// keyRunes is one key press per rune of s.
func keyRunes(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, keyRune(r))
	}
	return out
}

// TestInboxAnswerRefused is the table of sends that did not save. In every row
// the overlay stays on the item, and the load-bearing difference is what
// happens to the screen:
//
//   - "already answered" swaps in the stored item, so the answer that stands
//     is what is shown and the field is gone; the typed answer was not kept
//     on disk and must not look as if it was;
//   - "the item file has gone" and "the disk refused" keep the field and the
//     typed text, and say what happened with the id in the sentence;
//   - no row shows the success notice.
func TestInboxAnswerRefused(t *testing.T) {
	stored := inboxItems()[0]
	stored.Answer, stored.AnsweredAt, stored.AnsweredVia = "Drop it now", inboxStamp(time.Minute), inbox.ViaWeb

	cases := []struct {
		name      string
		reply     func(answerCall) (inbox.Item, error)
		wantNote  string
		wantField bool
		wantBody  []string
	}{
		{
			name:     "already answered",
			reply:    func(answerCall) (inbox.Item, error) { return stored, inbox.ErrAlreadyAnswered },
			wantNote: inboxAlreadyAnsweredNote,
			wantBody: []string{"Answer: Drop it now", "from the web page"},
		},
		{
			name:      "the item file has gone",
			reply:     func(answerCall) (inbox.Item, error) { return inbox.Item{}, inbox.ErrNotFound },
			wantNote:  "Item qst234 is no longer in the inbox, so the answer was not saved.",
			wantField: true,
		},
		{
			name: "a wrapped not-found error is still recognised",
			reply: func(answerCall) (inbox.Item, error) {
				return inbox.Item{}, fmt.Errorf("answering: %w", inbox.ErrNotFound)
			},
			wantNote:  "Item qst234 is no longer in the inbox, so the answer was not saved.",
			wantField: true,
		},
		{
			name:      "the disk refused",
			reply:     func(answerCall) (inbox.Item, error) { return inbox.Item{}, errors.New("disk full") },
			wantNote:  "The answer to qst234 could not be saved: disk full. Press enter to try again.",
			wantField: true,
		},
		{
			name:      "the store calls the answer empty",
			reply:     func(answerCall) (inbox.Item, error) { return inbox.Item{}, inbox.ErrEmptyAnswer },
			wantNote:  inboxEmptyAnswerNote,
			wantField: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, calls := inboxModel(t, 120, 40, tc.reply)
			m = openInboxItem(t, m, 0)
			// The own-answer choice, then "1" typed: the field's text is what
			// a refusal must keep.
			m = typeInbox(t, m, "31")
			m, cmd := update(t, m, keyEnter)
			msgs := collectMsgs(t, cmd)
			if len(msgs) != 1 || len(*calls) != 1 {
				t.Fatalf("the send produced %d messages and %d seam calls, want one of each", len(msgs), len(*calls))
			}
			m, _ = update(t, m, msgs[0])

			if !m.inboxOpen || m.inbox.screen != inboxScreenItem || m.inbox.item.ID != "qst234" {
				t.Fatalf("open=%v screen=%v item=%q, want to stay on qst234", m.inboxOpen, m.inbox.screen, m.inbox.item.ID)
			}
			box := boxText(m)
			if !strings.Contains(box, tc.wantNote) {
				t.Errorf("the box does not say %q:\n%s", tc.wantNote, box)
			}
			if strings.Contains(m.status, "Answered qst234") {
				t.Errorf("a refused answer raised the success notice %q", m.status)
			}
			hasField := strings.Contains(xansi.Strip(m.inbox.View()), "> ")
			if hasField != tc.wantField {
				t.Errorf("answer field shown = %v, want %v:\n%s", hasField, tc.wantField, xansi.Strip(m.inbox.View()))
			}
			if tc.wantField && m.inbox.input.Value() != "1" {
				t.Errorf("the typed answer was lost: field holds %q", m.inbox.input.Value())
			}
			for _, want := range tc.wantBody {
				if !strings.Contains(box, want) {
					t.Errorf("missing %q in:\n%s", want, box)
				}
			}
			if tc.wantField {
				// The refusal is not a dead end: the same enter sends again.
				_, retry := update(t, m, keyEnter)
				if retry == nil {
					t.Error("enter after a refusal did not send again")
				}
			}
		})
	}

	t.Run("a refusal that lands after the overlay closed is said as a notice", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, func(answerCall) (inbox.Item, error) { return inbox.Item{}, inbox.ErrNotFound })
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "1")
		m, cmd := update(t, m, keyEnter)
		m, _ = update(t, m, keyCtrlG)
		m, _ = update(t, m, collectMsgs(t, cmd)[0])
		if m.inboxOpen {
			t.Fatal("the outcome reopened the overlay")
		}
		if want := "Item qst234 is no longer in the inbox, so the answer was not saved."; m.status != want {
			t.Errorf("notice = %q, want %q", m.status, want)
		}
	})

	t.Run("no seam wired says so on the item and keeps the text", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m.deps.AnswerInbox = nil
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "31")
		m, cmd := update(t, m, keyEnter)
		if cmd != nil {
			t.Fatalf("a send with no seam issued a command (%T)", cmd())
		}
		if box := boxText(m); !strings.Contains(box, "afk inbox answer qst234") {
			t.Errorf("the box does not say how to answer instead:\n%s", box)
		}
		if m.inbox.input.Value() != "1" {
			t.Errorf("the typed answer was lost")
		}
	})
}

// TestInboxFollowsTheFiles is the live refresh: a new snapshot lands while the
// overlay is open. Load-bearing rows:
//
//   - the list cursor stays on the same ITEM when a new one arrives above it;
//     staying on the same row number would move the selection under the
//     owner's hand and the next enter would open the wrong item;
//   - the item screen keeps the typed text across a refresh, since a refresh
//     arrives every two seconds and an answer takes longer than that to type;
//   - an item answered elsewhere turns into its answered view;
//   - an older snapshot cannot turn an answered item back into an open one.
func TestInboxFollowsTheFiles(t *testing.T) {
	fresh := inbox.Item{ID: "new234", CreatedAt: inboxStamp(0), Kind: "question", Blocking: true, Title: "A new question"}

	t.Run("the list keeps the cursor on the same item", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = pressKey(t, m, keyI)
		m, _ = update(t, m, keyRune('j'))
		snap := inboxSnap()
		snap.Items = append([]inbox.Item{fresh}, snap.Items...)
		m = withBuild(t, m, snap)
		if got := m.inbox.items[m.inbox.cursor].ID; got != "fal567" {
			t.Fatalf("the cursor is on %q after a new item arrived, want fal567", got)
		}
		if view := xansi.Strip(m.inbox.View()); !strings.Contains(view, "A new question") {
			t.Errorf("the new item is not in the list:\n%s", view)
		}
		if _, h := m.inboxSize(); h != 5 {
			t.Errorf("the box is %d rows for 5 items; it did not grow with the list", h)
		}
	})

	t.Run("the selected item leaving the inbox clamps the cursor", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = pressKey(t, m, keyI)
		for range 3 {
			m, _ = update(t, m, keyRune('j'))
		}
		snap := inboxSnap()
		snap.Items = snap.Items[:2]
		m = withBuild(t, m, snap)
		if m.inbox.cursor != 1 {
			t.Fatalf("cursor = %d over a 2-item list, want 1", m.inbox.cursor)
		}
	})

	t.Run("the item keeps the typed text and takes the newest copy", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "3half an ans")
		snap := inboxSnap()
		snap.Items[0].Body = "The body was rewritten."
		snap.Items = append([]inbox.Item{fresh}, snap.Items...)
		m = withBuild(t, m, snap)
		if m.inbox.screen != inboxScreenItem || m.inbox.item.ID != "qst234" {
			t.Fatalf("the refresh moved off the item (screen=%v item=%q)", m.inbox.screen, m.inbox.item.ID)
		}
		if got := m.inbox.input.Value(); got != "half an ans" {
			t.Fatalf("the typed text became %q", got)
		}
		if box := boxText(m); !strings.Contains(box, "The body was rewritten.") {
			t.Errorf("the item did not take its newest copy:\n%s", box)
		}
	})

	t.Run("an item answered from the phone turns into its answered view", func(t *testing.T) {
		m, calls := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "3half an ans")
		snap := inboxSnap()
		snap.Items[0].Answer, snap.Items[0].AnsweredAt, snap.Items[0].AnsweredVia = "Drop it now", inboxStamp(0), inbox.ViaWeb
		m = withBuild(t, m, snap)
		view := xansi.Strip(m.inbox.View())
		if strings.Contains(view, "> ") || strings.Contains(view, inboxChooseHeading) {
			t.Errorf("the field or the choices are still shown on an answered item:\n%s", view)
		}
		if box := boxText(m); !strings.Contains(box, "Answer: Drop it now") || !strings.Contains(box, "from the web page") {
			t.Errorf("the stored answer is not shown:\n%s", box)
		}
		m, cmd := update(t, m, keyEnter)
		if cmd != nil || len(*calls) != 0 {
			t.Errorf("enter on the now-answered item sent the half-typed text")
		}
	})

	t.Run("an older snapshot does not reopen an answered item", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, func(c answerCall) (inbox.Item, error) {
			it := inboxItems()[0]
			it.Answer, it.AnsweredAt, it.AnsweredVia = "Keep it for one release", inboxStamp(0), inbox.ViaTUI
			return it, nil
		})
		m = openInboxItem(t, m, 0)
		m = typeInbox(t, m, "2")
		m, cmd := update(t, m, keyEnter)
		m, _ = update(t, m, collectMsgs(t, cmd)[0])
		// A read that began before the answer was written lands after it.
		m = withBuild(t, m, inboxSnap())
		if m.inbox.items[0].Open() {
			t.Fatal("a stale snapshot turned the answered item back into an open one")
		}
	})

	t.Run("the count of files that could not be read follows too", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = pressKey(t, m, keyI)
		snap := inboxSnap()
		snap.Unreadable = 2
		m = withBuild(t, m, snap)
		if view := xansi.Strip(m.inbox.View()); !strings.Contains(view, "2 inbox files could not be read") {
			t.Errorf("the unreadable files are not mentioned:\n%s", view)
		}
	})
}

// TestInboxOpening covers the `i` key itself. The load-bearing row is "no
// build data yet": before the first read an empty inbox and an unread one
// look the same, and opening on "Nothing in the inbox." would be a wrong
// answer the owner has no reason to doubt.
func TestInboxOpening(t *testing.T) {
	t.Run("no build data yet shows the notice and opens nothing", func(t *testing.T) {
		m := testModel(sess("alpha"))
		// The dialog dims the inbox row until the first read, so the handler's
		// own refusal is reached by calling it (see runAction).
		m, cmd := runAction(m, actionInbox)
		if cmd != nil || m.inboxOpen {
			t.Fatalf("open=%v cmd=%v, want neither", m.inboxOpen, cmd != nil)
		}
		if m.status != inboxNotReadNotice {
			t.Errorf("notice = %q, want %q", m.status, inboxNotReadNotice)
		}
	})

	t.Run("an empty inbox opens and says so", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha")), buildview.Snapshot{Root: "/repo"})
		m = pressKey(t, m, keyI)
		if !m.inboxOpen {
			t.Fatal("i did not open the overlay on an empty inbox")
		}
		if frame := render(m); !strings.Contains(frame, inboxEmptyText) {
			t.Errorf("the frame does not say %q:\n%s", inboxEmptyText, frame)
		}
		m, cmd := update(t, m, keyEnter)
		if cmd != nil || m.inbox.screen != inboxScreenList {
			t.Errorf("enter on an empty list did something")
		}
		m, _ = update(t, m, keyRune('j'))
		if m.inbox.cursor != 0 {
			t.Errorf("cursor = %d on an empty list", m.inbox.cursor)
		}
	})

	t.Run("not from the details overlay, which keeps its keys", func(t *testing.T) {
		m := focusOverview(t, withBuild(t, testModel(sess("alpha")), inboxSnap()))
		m = pressKey(t, m, keyI)
		if m.inboxOpen || activeInputMode(m.modeSnapshot()) != inputDetail {
			t.Fatalf("i in the details: inbox=%v mode=%v, want the details still up and no inbox",
				m.inboxOpen, activeInputMode(m.modeSnapshot()))
		}
	})

	t.Run("a live agent pane is left alone", func(t *testing.T) {
		m := withBuild(t, testModel(runningSess("alpha")), inboxSnap())
		pane := newFakePane("agent screen")
		m.pane, m.paneName, m.view = pane, "alpha", viewAgent
		m = pressKey(t, m, keyI)
		m = typeInbox(t, m, "jq")
		m, _ = update(t, m, keyEsc)
		if pane.closed != 0 || len(pane.keys)+len(pane.texts) != 0 || m.view != viewAgent || m.pane == nil {
			t.Errorf("the overlay touched the pane (closed=%d forwarded=%d view=%v)", pane.closed, len(pane.keys)+len(pane.texts), m.view)
		}
	})

	t.Run("the overlay is drawn in the empty state too", func(t *testing.T) {
		m := New("", time.Second, testDeps())
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		if m.hasActiveProject() {
			t.Fatal("fixture broken: the model has an active project")
		}
		m.inboxOpen = true
		m.inbox = newInboxOverlay(inboxItems(), 0, m.deps.Now(), 1, 1)
		m.sizeInbox()
		if frame := render(m); !strings.Contains(frame, "Inbox") || !strings.Contains(frame, "qst234") {
			t.Errorf("an open overlay is not on screen in the empty state:\n%s", frame)
		}
	})

	t.Run("a resize re-fits an open overlay", func(t *testing.T) {
		m, _ := inboxModel(t, 120, 40, nil)
		m = openInboxItem(t, m, 1)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 50, Height: 20})
		w, h := m.inboxSize()
		if m.inbox.width != w || m.inbox.height != h {
			t.Fatalf("the overlay is laid out for %dx%d in a %dx%d box", m.inbox.width, m.inbox.height, w, h)
		}
		if box := m.renderInboxModal(); lipgloss.Width(box) > 50-2*modalMargin {
			t.Errorf("the box is %d cells wide in a 50-column terminal", lipgloss.Width(box))
		}
	})
}

// TestInboxClosesOnAProjectSwitch: the overlay shows one project's items, so
// it goes with the project. The second half is the late answer: a send that
// was on its way when the project changed must not raise "Answered …" under
// the new project's name.
func TestInboxClosesOnAProjectSwitch(t *testing.T) {
	m := buildModel(t, nil)
	answered := inboxItems()[0]
	answered.Answer = "Drop it now"
	m.deps.AnswerInbox = func(string, string, string) (inbox.Item, error) { return answered, nil }
	snap := inboxSnap()
	snap.Root = "/alpha"
	m = withBuild(t, m, snap)
	m = openInboxItem(t, m, 0)
	m = typeInbox(t, m, "1")
	m, send := update(t, m, keyEnter)

	m, _ = m.switchProject("/beta")
	if m.repo != "/beta" {
		t.Fatalf("fixture broken: repo = %q after the switch", m.repo)
	}
	if m.inboxOpen {
		t.Fatal("the overlay stayed open across a project switch")
	}
	if got := activeInputMode(m.modeSnapshot()); got == inputInbox {
		t.Fatal("the closed overlay still owns the keyboard")
	}

	m.clearStatus()
	m, cmd := update(t, m, collectMsgs(t, send)[0])
	if cmd != nil || m.status != "" || m.inboxOpen {
		t.Errorf("an answer from the project that was left was reported: status=%q cmd=%v", m.status, cmd != nil)
	}
}

// TestInboxKeyIsInTheKeyTable is the proof that the `:` search lists the
// inbox under i and that the key runs it alone at Project: the rows come from
// the key table, so this fails if the row or its help line is removed. The
// short footer line must not grow (it is at its measured-width cap), and
// while the overlay is open the footer shows the keys of the screen that is
// up.
func TestInboxKeyIsInTheKeyTable(t *testing.T) {
	m := testModel(sess("alpha"))
	short := xansi.Strip(m.renderHelp())
	if strings.Contains(short, "inbox") {
		t.Errorf("the short footer line grew: %q", short)
	}
	m.buildKnown = true
	rows := actionRows(levelProject, defaultLeaderKey, m.leaderConditions())
	row, ok := leaderRowFor(rows, actionInbox)
	if !ok {
		t.Fatal("the `:` search does not list the inbox")
	}
	if row.keyLabel != "i" || row.name != "inbox" || row.blocked != "" {
		t.Errorf("row = %+v, want a runnable \"inbox\" under i", row)
	}
	if got := decideKey(levelProject, overlayNone, false, defaultLeaderKey, "i"); got.kind != outcomeRun || got.action != actionInbox {
		t.Errorf("i at Project = %v, want the inbox action", got)
	}

	om, _ := inboxModel(t, 120, 40, nil)
	om = pressKey(t, om, keyI)
	if line := xansi.Strip(om.renderHelp()); !strings.Contains(line, "read") || !strings.Contains(line, "close") || strings.Contains(line, "send answer") {
		t.Errorf("the list screen's footer = %q", line)
	}
	om, _ = update(t, om, keyEnter)
	if line := xansi.Strip(om.renderHelp()); !strings.Contains(line, "1-3 choose") || !strings.Contains(line, "enter send") || !strings.Contains(line, "other items") || !strings.Contains(line, "esc back") {
		t.Errorf("the answer screen's footer while choosing = %q", line)
	}
	om, _ = update(t, om, keyRune('3'))
	if line := xansi.Strip(om.renderHelp()); !strings.Contains(line, "send answer") || !strings.Contains(line, "back to the choices") || !strings.Contains(line, "close inbox") {
		t.Errorf("the answer screen's footer while typing = %q", line)
	}
	om, _ = update(t, om, keyEsc)
	if line := xansi.Strip(om.renderHelp()); !strings.Contains(line, "write my own answer") {
		t.Errorf("enter on the own-answer choice is not said to open the field: %q", line)
	}
	om, _ = update(t, om, keyEsc)
	for range 3 {
		om, _ = update(t, om, keyRune('j'))
	}
	om, _ = update(t, om, keyEnter)
	if line := xansi.Strip(om.renderHelp()); strings.Contains(line, "send answer") || !strings.Contains(line, "back") {
		t.Errorf("an answered item's footer = %q", line)
	}
}
