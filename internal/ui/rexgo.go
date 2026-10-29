// rexgo.go is how the dashboard goes into a session whose host runs inside a
// Rex block (T17.10). Such a host refuses the dashboard's dial, so the Agent
// level cannot draw it through termpane; instead the owner's own Rex window
// for it is brought forward, or the dashboard says where it runs.
//
// The choice itself is rex.GoIn, shared with `afk attach`. This file only
// gathers the facts: the row's runtime and window id (from host.json, through
// discover), whether the dashboard runs inside Rex (the environment), and
// whether the owner's Rex app is on the agent's Rex session (a Rex call). The
// first three are free, so the band text is decided in Update without asking
// Rex; only a row that could be focused pays for the Rex calls, and those run
// in rexGoInCmd, never in Update.
package ui

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
)

// rexGoInBudget bounds the Rex calls one going-in makes together (a client
// list and a focus); each call also has the caller's own deadline. It only
// bounds a Cmd's goroutine, so a slow Rex delays the toast, never a frame.
const rexGoInBudget = 25 * time.Second

// isRexRow is whether a row's host runs inside a Rex block. Runtime comes
// straight from host.json (discover.SessionState.Runtime); empty means afk's
// own host, which is also what a missing host.json reads as.
func isRexRow(s Session) bool {
	return s.Runtime == host.RuntimeRex
}

// insideRex is whether the dashboard itself runs in a Rex terminal: Rex sets
// REX_SESSION and REX_BLOCK in every block it starts, and both are needed, as
// in rex.GoInFacts. getenv is the caller's (Deps.Getenv) so tests never read
// the real environment.
func insideRex(getenv func(string) string) bool {
	return getenv("REX_SESSION") != "" && getenv("REX_BLOCK") != ""
}

// rexGoStep is the part of the going-in decision Update can make without
// asking Rex. It returns the action, or ask=true when the answer turns on the
// owner's client, which only a Rex call can tell.
//
// Every action comes from rex.GoIn so the dashboard and `afk attach` cannot
// disagree. The owner flag is passed as true here on purpose: rex.GoIn checks
// InsideRex first and the window id last, so with the owner fact assumed the
// answer for "outside Rex" and "no window id" carries the reason that is
// actually true, rather than "your app is not on this session", which afk has
// not checked. A result of focus_window means only "worth asking".
func rexGoStep(f rex.GoInFacts) (action rex.GoInAction, ask bool) {
	f.OwnerClientOnAgentSession = true
	a := rex.GoIn(f)
	if a.Kind == rex.GoInFocusWindow {
		return rex.GoInAction{}, true
	}
	return a, false
}

// ownerOnSession is whether the owner's own Rex app client (rex.OwnerClient:
// the Mac before iOS, then the newest connection) is attached to the agent's
// Rex session. Rex's focus_window moves every client on that session, so a
// focus is only right when the owner's client is one of them.
func ownerOnSession(clients []rex.Client, sessionID string) bool {
	owner, ok := rex.OwnerClient(clients)
	return ok && sessionID != "" && slices.Contains(owner.SessionIDs, sessionID)
}

// rexBandText is the line that tells the owner where a Rex-backed agent runs,
// with the cause after it when there is one. With no project name it names
// the session alone rather than printing an empty "›".
func rexBandText(project, session, reason string) string {
	where := session
	if project != "" {
		where = project + " › " + session
	}
	line := "This agent runs in Rex: open " + where + " in Rex."
	if reason != "" {
		line += " " + reason
	}
	return line
}

// rexProjectName is the project the band text names: the active project's
// name, else the folder the dashboard was opened on.
func (m Model) rexProjectName() string {
	if name := m.inboxProjectName(); name != "" {
		return name
	}
	if m.repo != "" {
		return filepath.Base(m.repo)
	}
	return ""
}

// rexGoInDoneMsg is rexGoInCmd's answer: the action taken, or err when Rex
// could not be asked or the focus failed. gen is the project generation the
// request was made in, so an answer that crossed a project switch is dropped.
type rexGoInDoneMsg struct {
	gen    int
	name   string
	action rex.GoInAction
	err    error
}

// goInRex is going into a Rex-backed row: the band text at once when the
// facts already decide it, otherwise a Cmd that asks Rex. It never touches
// the pane, the focus or the view, so the keyboard stays where it was.
func (m *Model) goInRex(s Session) tea.Cmd {
	facts := rex.GoInFacts{Runtime: s.Runtime, InsideRex: insideRex(m.deps.Getenv), WindowID: s.RexWindowID}
	action, ask := rexGoStep(facts)
	if !ask {
		m.notify(notifyInfo, rexBandText(m.rexProjectName(), s.Name, action.Reason))
		return nil
	}
	m.notify(notifyInfo, "asking Rex to show "+s.Name+"…")
	return rexGoInCmd(m.deps.RexCaller, m.deps.ReadHostMeta, m.projectGen, s.Name, s.WorktreePath, facts)
}

// rexGoInCmd reads the agent's Rex session id from its host.json, asks Rex
// for the connected app clients, decides with rex.GoIn and, on focus_window,
// brings the window forward. All of it runs in the Cmd's goroutine: a Rex
// that is slow or gone costs a late toast, never a stalled dashboard.
//
// The window id from host.json wins over the row's when both are set, since
// the file was read just now and the row up to one tick ago.
func rexGoInCmd(c rex.Caller, readMeta func(worktree, name string) (host.Meta, error), gen int, name, worktree string, facts rex.GoInFacts) tea.Cmd {
	return func() tea.Msg {
		done := rexGoInDoneMsg{gen: gen, name: name}
		meta, err := readMeta(worktree, name)
		if err != nil {
			done.err = err
			return done
		}
		var sessionID string
		if meta.Rex != nil {
			sessionID = meta.Rex.SessionID
			if meta.Rex.WindowID != "" {
				facts.WindowID = meta.Rex.WindowID
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), rexGoInBudget)
		defer cancel()
		clients, err := rex.ListClients(ctx, c, "app")
		if err != nil {
			done.err = err
			return done
		}
		facts.OwnerClientOnAgentSession = ownerOnSession(clients, sessionID)
		done.action = rex.GoIn(facts)
		if done.action.Kind == rex.GoInFocusWindow {
			done.err = rex.FocusWindow(ctx, c, sessionID, facts.WindowID)
		}
		return done
	}
}

// applyRexGoInDone reports what going in did. A failure says what went wrong
// and still says where the agent runs, so the owner always has a way in.
func (m Model) applyRexGoInDone(msg rexGoInDoneMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	project := m.rexProjectName()
	switch {
	case msg.err != nil:
		m.notify(notifyWarn, "afk could not bring "+msg.name+" forward in Rex ("+msg.err.Error()+"). "+rexBandText(project, msg.name, ""))
	case msg.action.Kind == rex.GoInFocusWindow:
		m.notify(notifySuccess, msg.name+" is shown in your Rex app")
	default:
		m.notify(notifyInfo, rexBandText(project, msg.name, msg.action.Reason))
	}
	return m, nil
}
