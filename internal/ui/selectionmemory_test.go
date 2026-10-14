package ui

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
)

// TestSelectionMemoryNewSeedsRememberedSelectionAndArmsPreview is the startup
// half of the selection-memory feature: New has to seed
// Model.rememberedSelection from Deps.LoadSelections and decide, from that map
// alone, whether the very first sessionsLoadedMsg previews at all. Which row
// that load ends up previewing — the remembered session, or row 0 when it is
// gone — is the restore's business and is pinned in switcher_wiring_test.go;
// this function only proves the arm decision itself.
//
// Row 5 is load-bearing: it is the only row where the launch repo and the
// resolved project path DIFFER (a subdirectory launch), and it is what proves
// selectionKeyFor keys New's decision on Project.Path rather than on repo — a
// version keyed on repo would fail this row while passing every other one.
func TestSelectionMemoryNewSeedsRememberedSelectionAndArmsPreview(t *testing.T) {
	tests := []struct {
		name               string
		repo               string
		projects           []project.Project
		activeIndex        int
		initialSessionName string
		loadSelections     func() map[string]string
		wantArmed          bool
	}{
		{
			name:      "nil LoadSelections leaves the map nil and the flag false",
			repo:      "/repo",
			wantArmed: false,
		},
		{
			name: "a map lacking the initial repo leaves the flag false",
			repo: "/repo",
			loadSelections: func() map[string]string {
				return map[string]string{"/other": "session-a"}
			},
			wantArmed: false,
		},
		{
			name: "an entry for the initial repo with no InitialSessionName sets the flag",
			repo: "/repo",
			loadSelections: func() map[string]string {
				return map[string]string{"/repo": "session-a"}
			},
			wantArmed: true,
		},
		{
			name:               "an entry for the initial repo AND InitialSessionName leaves the flag false",
			repo:               "/repo",
			initialSessionName: "session-b",
			loadSelections: func() map[string]string {
				return map[string]string{"/repo": "session-a"}
			},
			wantArmed: false,
		},
		{
			name:        "a subdirectory launch keys on the resolved project path, not repo",
			repo:        "/repo/sub",
			projects:    []project.Project{{Path: "/repo"}},
			activeIndex: 0,
			loadSelections: func() map[string]string {
				return map[string]string{"/repo": "session-a"}
			},
			wantArmed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := testDeps()
			deps.LoadSelections = tt.loadSelections
			deps.Projects = tt.projects
			deps.ActiveProjectIndex = tt.activeIndex
			deps.InitialSessionName = tt.initialSessionName

			m := New(tt.repo, time.Second, deps)

			if tt.loadSelections == nil {
				if m.rememberedSelection != nil {
					t.Errorf("rememberedSelection = %#v, want nil (LoadSelections was nil)", m.rememberedSelection)
				}
			}
			if m.previewOnNextLoad != tt.wantArmed {
				t.Errorf("previewOnNextLoad = %v, want %v", m.previewOnNextLoad, tt.wantArmed)
			}
		})
	}
}

// TestSelectionMemoryKeyFor is the pure core New's startup arm and every
// later read/write of rememberedSelection depend on: the resolved project
// path when the active index is in range, and the launch repo as a fallback
// otherwise.
func TestSelectionMemoryKeyFor(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		active   int
		repo     string
		want     string
	}{
		{
			name: "empty projects falls back to repo",
			repo: "/repo",
			want: "/repo",
		},
		{
			name:     "an out-of-range index falls back to repo",
			projects: []project.Project{{Path: "/repo"}},
			active:   -1,
			repo:     "/repo",
			want:     "/repo",
		},
		{
			name:     "an out-of-range positive index falls back to repo",
			projects: []project.Project{{Path: "/repo"}},
			active:   1,
			repo:     "/repo",
			want:     "/repo",
		},
		{
			name:     "an in-range index returns that project's resolved path",
			projects: []project.Project{{Path: "/repo/sub"}, {Path: "/other"}},
			active:   0,
			repo:     "/repo/sub",
			want:     "/repo/sub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectionKeyFor(tt.projects, tt.active, tt.repo)
			if got != tt.want {
				t.Errorf("selectionKeyFor(%+v, %d, %q) = %q, want %q", tt.projects, tt.active, tt.repo, got, tt.want)
			}
		})
	}
}

// --- recording the settled row -----------------------------------------------

// selectionSpy is the Deps.RememberSelection fake for every row below.
//
// The mutex is not decoration: a tick flush runs inside a tea.Cmd, and
// collectMsgs runs batch members on their own goroutines, so the write here and
// the test's read of it are genuinely concurrent under -race. And it never calls
// t.Fatal — a Goexit on a Cmd goroutine parks the collector forever instead of
// failing the test (see the fake-seam note in the package's other Cmd tests).
type selectionSpy struct {
	mu    sync.Mutex
	calls [][2]string
}

func (s *selectionSpy) remember(key, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, [2]string{key, name})
	return nil
}

func (s *selectionSpy) snapshot() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// countSelectionFlushes counts the selectionFlushedMsg values in a collected
// batch, which is how "exactly ONE flush Cmd per tick" is asserted: one flush
// Cmd produces exactly one such message, so the message count IS the Cmd count.
// Counting the spy's calls alone cannot tell one Cmd writing two entries from
// two Cmds writing one each — and only the second shape can lose a write.
func countSelectionFlushes(msgs []tea.Msg) int {
	n := 0
	for _, msg := range msgs {
		if _, ok := msg.(selectionFlushedMsg); ok {
			n++
		}
	}
	return n
}

// pendingSelectionSess is the placeholder row openScratch splices in before its
// create runs — no worktree, no host, and a name applyScratchDirs may still
// correct.
func pendingSelectionSess(name string) Session {
	return Session{SessionState: discover.SessionState{Name: name}, Pending: true}
}

// TestSelectionMemoryRecordsTheSettledRow is the recording half of the feature:
// one cursor move records the row it landed on, in memory and in the pending
// set, WITHOUT changing what schedulePreview returns on any path.
//
// The refusal rows are the load-bearing ones. Recording sits in schedulePreview
// ahead of every refusal, so the rows that arm nothing — stopped, worktree
// fault, pending placeholder — must still be remembered (the user chose that
// row) and must still return a nil Cmd. An implementation that recorded after a
// refusal, or that returned a Cmd to carry the write, passes the running row and
// fails these.
func TestSelectionMemoryRecordsTheSettledRow(t *testing.T) {
	tests := []struct {
		name      string
		sessions  []Session
		wantRow   string
		wantArmed bool
	}{
		{
			name:      "a running row is recorded and still arms its preview",
			sessions:  []Session{runningSess("a"), runningSess("b")},
			wantRow:   "b",
			wantArmed: true,
		},
		{
			name:      "a stopped row is recorded and arms nothing",
			sessions:  []Session{runningSess("a"), sess("stopped")},
			wantRow:   "stopped",
			wantArmed: false,
		},
		{
			name:      "a worktree-fault row is recorded and arms nothing",
			sessions:  []Session{runningSess("a"), faultSess("busted", session.KindWorktreeMissing, "worktree is gone")},
			wantRow:   "busted",
			wantArmed: false,
		},
		{
			name:      "a pending placeholder is recorded and arms nothing",
			sessions:  []Session{runningSess("a"), pendingSelectionSess("afk-1")},
			wantRow:   "afk-1",
			wantArmed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.sessions...)
			m.deps.OpenHostPane = refusingOpen(t, "a recorded cursor move must not dial")

			m, cmd := update(t, m, keyDown)

			if s, ok := m.selected(); !ok || s.Name != tt.wantRow {
				t.Fatalf("setup: the move landed on %q (ok=%v), want %q", s.Name, ok, tt.wantRow)
			}
			key := m.selectionKey()
			if got := m.rememberedSelection[key]; got != tt.wantRow {
				t.Errorf("rememberedSelection[%q] = %q, want %q", key, got, tt.wantRow)
			}
			if got := m.dirtySelections[key]; got != tt.wantRow {
				t.Errorf("dirtySelections[%q] = %q, want %q", key, got, tt.wantRow)
			}
			if len(m.dirtySelections) != 1 {
				t.Errorf("dirtySelections = %#v, want exactly one entry", m.dirtySelections)
			}
			if !tt.wantArmed {
				if cmd != nil {
					t.Fatalf("a refused move returned %T, want nil — recording must not change the return value", cmd())
				}
				return
			}
			if cmd == nil {
				t.Fatal("an armed move returned nil, want the preview timer it returned before")
			}
			if !hasMsg[previewTimerMsg](collectMsgsPreview(t, cmd)) {
				t.Error("an armed move's Cmd produced no previewTimerMsg — the return value changed shape")
			}
		})
	}
}

// TestSelectionMemoryIgnoresFilterDrivenMoves pins the one selection change
// that must NOT be remembered: `/` jumps the cursor to the first match and every
// key typed into the prompt moves it again, so recording there would overwrite
// the user's real choice with whatever their query happened to match.
func TestSelectionMemoryIgnoresFilterDrivenMoves(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "filtering must not dial")

	m, _ = pressAction(t, m, keyRune('/'))
	if !m.list.SettingFilter() {
		t.Fatal("setup: `/` did not open the filter prompt")
	}
	m, _ = update(t, m, keyRune('b'))

	if len(m.rememberedSelection) != 0 {
		t.Errorf("rememberedSelection = %#v, want nothing recorded while filtering", m.rememberedSelection)
	}
	if len(m.dirtySelections) != 0 {
		t.Errorf("dirtySelections = %#v, want nothing pending while filtering", m.dirtySelections)
	}
}

// --- flushing on the discovery tick -------------------------------------------

// TestSelectionMemoryFlushesOnTick is the debounce: the 2s discovery tick is the
// only thing that writes, it writes through exactly one Cmd, and it empties the
// pending set so the next tick writes nothing.
//
// The "a second tick writes nothing" row is what the clear buys — without it
// every tick for the rest of the session rewrites the same entry, which is a
// file write every 2s forever from one cursor move.
func TestSelectionMemoryFlushesOnTick(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "a tick flush must not dial")
	spy := &selectionSpy{}
	m.deps.RememberSelection = spy.remember

	m, _ = update(t, m, keyDown)
	key := m.selectionKey()
	if len(m.dirtySelections) != 1 {
		t.Fatalf("setup: dirtySelections = %#v, want the moved-to row pending", m.dirtySelections)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtySelections) != 0 {
		t.Errorf("dirtySelections = %#v, want the set emptied by the flush", m.dirtySelections)
	}
	msgs := collectMsgs(t, tick)
	if n := countSelectionFlushes(msgs); n != 1 {
		t.Errorf("tick produced %d flush Cmds, want exactly 1", n)
	}
	if got, want := spy.snapshot(), [][2]string{{key, "b"}}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("RememberSelection calls = %v, want %v", got, want)
	}

	m, second := update(t, m, tickMsg(time.Unix(2, 0)))
	if n := countSelectionFlushes(collectMsgs(t, second)); n != 0 {
		t.Errorf("a second tick with nothing changed produced %d flush Cmds, want 0", n)
	}
	if got := spy.snapshot(); len(got) != 1 {
		t.Errorf("RememberSelection calls = %v, want the first tick's one call and no more", got)
	}
}

// TestSelectionMemoryTickWithNilSeamClearsTheSet covers the un-wired dashboard
// (every test in this package that does not opt in, and any caller that builds
// Deps without the seam): nothing is written, and the pending set is cleared
// anyway rather than growing an entry per cursor move that nothing will ever
// flush.
func TestSelectionMemoryTickWithNilSeamClearsTheSet(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "a tick must not dial")
	if m.deps.RememberSelection != nil {
		t.Fatal("setup: testDeps must leave RememberSelection nil")
	}

	m, _ = update(t, m, keyDown)
	if len(m.dirtySelections) != 1 {
		t.Fatalf("setup: dirtySelections = %#v, want one pending entry", m.dirtySelections)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtySelections) != 0 {
		t.Errorf("dirtySelections = %#v, want the set cleared even with no seam", m.dirtySelections)
	}
	if n := countSelectionFlushes(collectMsgs(t, tick)); n != 0 {
		t.Errorf("a nil seam produced %d flush Cmds, want 0", n)
	}
}

// TestSelectionMemoryFlushSurvivesAProjectSwitch is the row the per-key pending
// set exists for, and the one a single dirty flag cannot pass.
//
// A cursor move in alpha and a switch to beta both happen inside one 2s tick.
// At flush time selectionKey() answers beta, so an implementation that remembers
// only "something changed" and looks up the CURRENT key writes beta's (empty)
// entry and loses alpha's move for the rest of the run — the in-memory map
// already matches on return, so nothing re-dirties it. The Cmd count is asserted
// alongside the call: two Cmds writing one entry each would both re-read the
// state file and rename over each other.
func TestSelectionMemoryFlushSurvivesAProjectSwitch(t *testing.T) {
	m, _, _ := switcherWiringModel(t)
	spy := &selectionSpy{}
	m.deps.RememberSelection = spy.remember

	m, _ = update(t, m, keyDown)
	alphaKey := m.selectionKey()
	if alphaKey != "/roots/alpha" {
		t.Fatalf("setup: selectionKey() = %q, want alpha's resolved path", alphaKey)
	}
	wantName := m.dirtySelections[alphaKey]
	if wantName == "" {
		t.Fatalf("setup: dirtySelections = %#v, want alpha's moved-to row pending", m.dirtySelections)
	}

	m = openSwitcherOverlay(t, m)
	// By identity: the overlay's row order is not scan order, so no fixed
	// keyDown count means beta. See switcherSelectByPath.
	m = switcherSelectByPath(t, m, "/roots/beta")
	m, _ = update(t, m, keyEnter)
	if m.repo != "/roots/beta" {
		t.Fatalf("setup: the switch must land on beta, got %q", m.repo)
	}
	if m.selectionKey() == alphaKey {
		t.Fatalf("setup: selectionKey() still answers %q after the switch", alphaKey)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if n := countSelectionFlushes(collectMsgsPreview(t, tick)); n != 1 {
		t.Errorf("tick after a switch produced %d flush Cmds, want exactly 1", n)
	}
	if got, want := spy.snapshot(), [2]string{alphaKey, wantName}; len(got) != 1 || got[0] != want {
		t.Errorf("RememberSelection calls = %v, want exactly one for %v", got, want)
	}
	if len(m.dirtySelections) != 0 {
		t.Errorf("dirtySelections = %#v, want the set emptied by the flush", m.dirtySelections)
	}
}

// --- flushing on quit ----------------------------------------------------------

// TestSelectionMemoryQuitFlushesInline pins that the quit path writes INLINE,
// not through a Cmd. tea.Quit is returned beside it, so a Cmd may never run —
// the same reason requestQuit already closes watchers and removes dashmeta
// entries synchronously. The assertion is deliberately made BEFORE the returned
// Cmd is touched: running it first would let a Cmd-based implementation pass.
func TestSelectionMemoryQuitFlushesInline(t *testing.T) {
	tests := []struct {
		name  string
		dirty map[string]string
		want  int
	}{
		{
			name:  "two pending entries are both written before the quit",
			dirty: map[string]string{"/repo": "a", "/other": "b"},
			want:  2,
		},
		{
			name:  "an empty set writes nothing",
			dirty: nil,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("a"))
			spy := &selectionSpy{}
			m.deps.RememberSelection = spy.remember
			m.dirtySelections = tt.dirty

			nm, cmd := m.requestQuit()

			if got := spy.snapshot(); len(got) != tt.want {
				t.Errorf("RememberSelection calls = %v, want %d synchronous calls", got, tt.want)
			}
			for _, call := range spy.snapshot() {
				if tt.dirty[call[0]] != call[1] {
					t.Errorf("RememberSelection(%q, %q) does not match the pending set %#v", call[0], call[1], tt.dirty)
				}
			}
			if got, ok := nm.(Model); !ok || !got.quitting {
				t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
			}
			if cmd == nil {
				t.Error("requestQuit returned no Cmd, want tea.Quit")
			}
		})
	}
}

// TestSelectionMemoryQuitWithNilSeamIsInert is the un-wired dashboard on the
// quit path: no seam, no write, no panic.
func TestSelectionMemoryQuitWithNilSeamIsInert(t *testing.T) {
	m := testModel(runningSess("a"))
	m.dirtySelections = map[string]string{"/repo": "a"}

	nm, cmd := m.requestQuit()

	if got, ok := nm.(Model); !ok || !got.quitting {
		t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
	}
	if cmd == nil {
		t.Error("requestQuit returned no Cmd, want tea.Quit")
	}
}
