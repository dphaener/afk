package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
)

// rexSess is a running row whose host runs in a Rex block, as discover
// reports it from host.json.
func rexSess(name, windowID string) Session {
	s := runningSess(name)
	s.Runtime = host.RuntimeRex
	s.RexWindowID = windowID
	return s
}

// envOf is a Getenv over a fixed map, so no test reads the real environment
// (a developer running the suite inside Rex would otherwise flip rows).
func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

var insideRexEnv = map[string]string{"REX_SESSION": "dash-session", "REX_BLOCK": "dash-block"}

// rexMeta is a host.json reader answering for every session with the agent's
// Rex session S1 and the given window.
func rexMeta(windowID string) func(string, string) (host.Meta, error) {
	return func(string, string) (host.Meta, error) {
		return host.Meta{Runtime: host.RuntimeRex, Rex: &host.RexBlock{SessionID: "S1", WindowID: windowID, BlockID: "B1"}}, nil
	}
}

const (
	macOnS1    = `{"clients":[{"client_id":"mac","connected_at":"2026-10-28T10:00:00Z","connection_state":"connected","info":{"kind":"app","platform":"macos"},"session_ids":["S1"]}]}`
	macOnOther = `{"clients":[{"client_id":"mac","connected_at":"2026-10-28T10:00:00Z","connection_state":"connected","info":{"kind":"app","platform":"macos"},"session_ids":["S2"]}]}`
	noClients  = `{"clients":[]}`
)

// TestRexGoStep is the half of the going-in decision Update makes without
// asking Rex. Load-bearing rows: "rex outside Rex" (the dashboard in a plain
// terminal must never ask Rex or focus, because focus_window is session-wide
// and would move someone else's screen) and "rex inside Rex with no window
// id" (the reason must be the missing window, not an owner check afk never
// made, which is why rexGoStep assumes the owner fact). "rex inside Rex with
// a window" is the only row that asks Rex; "afk runtime" and "empty runtime"
// keep the Agent level.
func TestRexGoStep(t *testing.T) {
	for _, tc := range []struct {
		name       string
		facts      rex.GoInFacts
		wantKind   string
		wantAsk    bool
		wantReason string
	}{
		{"afk runtime", rex.GoInFacts{Runtime: "afk", InsideRex: true, WindowID: "W1"}, rex.GoInAfkAgent, false, "afk's own host"},
		{"empty runtime", rex.GoInFacts{InsideRex: true, WindowID: "W1"}, rex.GoInAfkAgent, false, "afk's own host"},
		{"rex outside Rex", rex.GoInFacts{Runtime: "rex", WindowID: "W1"}, rex.GoInBandText, false, "not inside Rex"},
		{"rex outside Rex with no window id", rex.GoInFacts{Runtime: "rex"}, rex.GoInBandText, false, "not inside Rex"},
		{"rex inside Rex with no window id", rex.GoInFacts{Runtime: "rex", InsideRex: true}, rex.GoInBandText, false, "which Rex window"},
		{"rex inside Rex with a window", rex.GoInFacts{Runtime: "rex", InsideRex: true, WindowID: "W1"}, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ask := rexGoStep(tc.facts)
			if ask != tc.wantAsk || got.Kind != tc.wantKind {
				t.Fatalf("rexGoStep(%+v) = %+v ask=%v, want kind %q ask=%v", tc.facts, got, ask, tc.wantKind, tc.wantAsk)
			}
			if !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestRexGoInCmd is the half that asks Rex, with a rex.Fake. Load-bearing
// rows: "owner attached and window" is the only one that may call
// session.focus_window, and "owner on another session" must not call it,
// because focus_window moves every client on the agent's session (T17.1).
// "no owner client" covers a Rex with only CLI clients; the two error rows
// prove a failed call becomes an error message, never a silent no-op.
func TestRexGoInCmd(t *testing.T) {
	facts := rex.GoInFacts{Runtime: "rex", InsideRex: true, WindowID: "W1"}
	for _, tc := range []struct {
		name      string
		fake      *rex.Fake
		meta      func(string, string) (host.Meta, error)
		wantKind  string
		wantFocus string // window id focused, "" for no focus call
		wantErr   bool
	}{
		{"owner attached and window", (&rex.Fake{}).On("client.list", macOnS1, nil).On("session.focus_window", `{}`, nil),
			rexMeta(""), rex.GoInFocusWindow, "W1", false},
		{"host.json window wins over the row's", (&rex.Fake{}).On("client.list", macOnS1, nil).On("session.focus_window", `{}`, nil),
			rexMeta("W9"), rex.GoInFocusWindow, "W9", false},
		{"owner on another session", (&rex.Fake{}).On("client.list", macOnOther, nil),
			rexMeta(""), rex.GoInBandText, "", false},
		{"no owner client", (&rex.Fake{}).On("client.list", noClients, nil),
			rexMeta(""), rex.GoInBandText, "", false},
		{"client list fails", (&rex.Fake{}).On("client.list", "", errors.New("rex is not running")),
			rexMeta(""), "", "", true},
		{"focus fails", (&rex.Fake{}).On("client.list", macOnS1, nil).On("session.focus_window", "", errors.New("no such window")),
			rexMeta(""), rex.GoInFocusWindow, "W1", true},
		{"host.json unreadable", &rex.Fake{},
			func(string, string) (host.Meta, error) { return host.Meta{}, errors.New("no host.json") }, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := rexGoInCmd(tc.fake, tc.meta, 3, "feat", "/wt/feat", facts)()
			done, ok := msg.(rexGoInDoneMsg)
			if !ok {
				t.Fatalf("msg = %T, want rexGoInDoneMsg", msg)
			}
			if done.gen != 3 || done.name != "feat" {
				t.Fatalf("done = %+v, want gen 3 and name feat", done)
			}
			if (done.err != nil) != tc.wantErr || done.action.Kind != tc.wantKind {
				t.Fatalf("done = %+v, want kind %q err=%v", done, tc.wantKind, tc.wantErr)
			}
			var focused string
			for _, c := range tc.fake.Calls() {
				if c.Method == "session.focus_window" {
					focused = string(c.Args)
				}
			}
			switch {
			case tc.wantFocus == "" && focused != "":
				t.Fatalf("focus_window called with %s, want no call", focused)
			case tc.wantFocus != "" && !strings.Contains(focused, `"window_id":"`+tc.wantFocus+`"`):
				t.Fatalf("focus_window args = %s, want window %s", focused, tc.wantFocus)
			case tc.wantFocus != "" && !strings.Contains(focused, `"session_id":"S1"`):
				t.Fatalf("focus_window args = %s, want the agent's session S1", focused)
			}
		})
	}
}

// TestRexGoEnter drives enter on a row through the Model and counts dials
// (the OpenHostPane seam). Load-bearing rows: every Rex row must dial 0 times
// and leave the keyboard at Project, and "rex outside Rex" must name the
// session in its band text without asking Rex at all; "afk runtime" pins that
// the Agent level is unchanged for afk's own host (exactly one dial, focus
// right). The two inside-Rex rows prove the Rex calls run in the Cmd, not in
// Update: Update returns before any Rex call is recorded.
func TestRexGoEnter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		row        Session
		env        map[string]string
		fake       *rex.Fake
		wantDials  int
		wantStatus string
		wantFocus  bool // focus_window called
	}{
		{"afk runtime", runningSess("feat"), insideRexEnv, &rex.Fake{}, 1, "", false},
		{"rex outside Rex", rexSess("feat", "W1"), nil, &rex.Fake{}, 0, "This agent runs in Rex: open repo › feat in Rex.", false},
		{"rex inside Rex with owner attached and window", rexSess("feat", "W1"), insideRexEnv,
			(&rex.Fake{}).On("client.list", macOnS1, nil).On("session.focus_window", `{}`, nil), 0, "feat is shown in your Rex app", true},
		{"rex inside Rex without owner", rexSess("feat", "W1"), insideRexEnv,
			(&rex.Fake{}).On("client.list", macOnOther, nil), 0, "open repo › feat in Rex. Your Rex app is not showing", false},
		{"rex with no window id", rexSess("feat", ""), insideRexEnv, &rex.Fake{}, 0, "open repo › feat in Rex. afk does not know which Rex window", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(tc.row)
			dials := 0
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				dials++
				return newFakePane("LIVE"), nil
			}
			m.deps.Getenv = envOf(tc.env)
			m.deps.RexCaller = tc.fake
			m.deps.ReadHostMeta = rexMeta("")

			got, cmd := update(t, m, keyEnter)
			if n := len(tc.fake.Calls()); n != 0 {
				t.Fatalf("Update made %d Rex calls, want none (Rex is only asked from a Cmd)", n)
			}
			if isRexRow(tc.row) && got.focus != focusList {
				t.Fatalf("focus = %v, want the keyboard left at Project for a Rex row", got.focus)
			}
			for _, msg := range collectMsgs(t, cmd) {
				got, _ = update(t, got, msg)
			}
			if dials != tc.wantDials {
				t.Fatalf("OpenHostPane called %d times, want %d", dials, tc.wantDials)
			}
			if tc.wantStatus != "" && !strings.Contains(got.status, tc.wantStatus) {
				t.Fatalf("status = %q, want it to contain %q", got.status, tc.wantStatus)
			}
			called := false
			for _, c := range tc.fake.Calls() {
				called = called || c.Method == "session.focus_window"
			}
			if called != tc.wantFocus {
				t.Fatalf("focus_window called = %v, want %v", called, tc.wantFocus)
			}
			if isRexRow(tc.row) && got.pane != nil {
				t.Fatal("a Rex row must never get a pane")
			}
		})
	}
}

// TestRexGoNeverDials covers the other dial entrances for a Rex row: the
// preview timer, the restart chain and a direct openPaneFor (the startup
// pane and the post-create open go through it). Load-bearing: "restart
// landing" keeps `r` working (the success is reported) while dialing 0 times,
// and "preview" must stay silent because nobody asked.
func TestRexGoNeverDials(t *testing.T) {
	for _, tc := range []struct {
		name       string
		run        func(t *testing.T, m Model) Model
		wantStatus string
	}{
		{"preview is not armed", func(t *testing.T, m Model) Model {
			if cmd := m.schedulePreview(); cmd != nil {
				t.Fatal("schedulePreview armed a timer for a Rex row")
			}
			return m
		}, ""},
		{"preview open is silent", func(t *testing.T, m Model) Model {
			if cmd := m.openPaneFor(paneOpenRequest{name: "feat", worktree: "/wt/feat", preview: true}); cmd != nil {
				t.Fatal("openPaneFor dispatched a dial for a Rex row")
			}
			if m.status != "" {
				t.Fatalf("status = %q, want a preview refusal to say nothing", m.status)
			}
			return m
		}, ""},
		{"explicit open says where it runs", func(t *testing.T, m Model) Model {
			if cmd := m.openPaneFor(paneOpenRequest{name: "feat", worktree: "/wt/feat"}); cmd != nil {
				t.Fatal("openPaneFor dispatched a dial for a Rex row")
			}
			if m.attachInFlight.name != "" {
				t.Fatalf("attachInFlight = %+v, want nothing recorded for a refused open", m.attachInFlight)
			}
			return m
		}, "This agent runs in Rex: open repo › feat in Rex."},
		{"restart landing", func(t *testing.T, m Model) Model {
			got, cmd := update(t, m, hostRestartedMsg{name: "feat", worktree: "/wt/feat", focusOnArrival: true})
			for _, msg := range collectMsgs(t, cmd) {
				if _, ok := msg.(paneAttachedMsg); ok {
					t.Fatal("the restart chain dialed a Rex row")
				}
			}
			if got.focus != focusList {
				t.Fatalf("focus = %v, want it handed back to the list", got.focus)
			}
			return got
		}, "restarted feat · This agent runs in Rex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(rexSess("feat", "W1"))
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				t.Fatal("a Rex row was dialed")
				return nil, nil
			}
			m.deps.RexCaller = &rex.Fake{}
			got := tc.run(t, m)
			if tc.wantStatus != "" && !strings.Contains(got.status, tc.wantStatus) {
				t.Fatalf("status = %q, want it to contain %q", got.status, tc.wantStatus)
			}
		})
	}
}

// TestRexGoInDoneStaleProject pins the generation check: an answer that
// crossed a project switch says nothing about the project now shown.
func TestRexGoInDoneStaleProject(t *testing.T) {
	m := testModel(rexSess("feat", "W1"))
	m.projectGen = 2
	got, _ := update(t, m, rexGoInDoneMsg{gen: 1, name: "feat", action: rex.GoInAction{Kind: rex.GoInBandText}})
	if got.status != "" {
		t.Fatalf("status = %q, want a stale answer dropped", got.status)
	}
}

// TestRexGoBandText pins the band line's wording, with and without a project.
func TestRexGoBandText(t *testing.T) {
	for _, tc := range []struct {
		name, project, session, reason, want string
	}{
		{"with project and reason", "afk", "feat", "Because.", "This agent runs in Rex: open afk › feat in Rex. Because."},
		{"no project", "", "feat", "", "This agent runs in Rex: open feat in Rex."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rexBandText(tc.project, tc.session, tc.reason); got != tc.want {
				t.Fatalf("rexBandText = %q, want %q", got, tc.want)
			}
		})
	}
}
