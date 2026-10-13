package daemon

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
)

// This file is the executor's side of the usage governor (PLAN §6.3 to §6.5):
// when usage and the login are asked about, the two standing items, and the
// three actions that stop a session for usage and start it again. The rules
// themselves are in decide.go (held, heldBack, decideUsage, Notices).
//
// Like the rest of the executor it keeps nothing between ticks. A pause is two
// fields of the ledger entry, "already told" is an open inbox item, and the
// last verdict is whatever the state file says.

// consults says whether this tick asks for the usage verdict and the login
// verdict. first is Decide's answer without them.
//
// Asking is not free: behind Deps.Usage is the agent CLI's usage probe, and
// PLAN §6.2 says to probe at decision points, not on every tick. So the two
// are asked only when the answer can change what the tick does:
//
//   - A task is running. Its session may have to be stopped, or started again.
//   - Decide chose to launch a task, to create a session again or to un-park
//     one. Each starts an agent or sets one going.
//   - One of the two standing items (OverageTitle, LoginTitle) is open. An
//     idle project is otherwise never asked about, and the item it raised
//     while it had work would stay open for ever.
//
// A project with nothing to do and no such item is never asked about at all.
// Neither is a tick that hands a task to the owner, or that holds one for an
// open question of the plan or writes the answer to one (ActOwner,
// ActOwnerDone, ActQuestionHold, ActQuestionAnswered): none of them starts an
// agent, so a reading could not change what the tick does. They are left out
// by not being in the list below, and TestTickQuestionHold counts the calls.
//
// One running task is left out: a session paused with a wake time still
// ahead, whose host is gone. Nothing a reading says before that time can
// start it again (Decide waits for the wake time whatever usage says), and a
// pause for a weekly limit would otherwise ask thousands of times to learn
// nothing. Asking starts again once the wake time has passed. A session whose
// host is still up is not left out: Decide has to stop it, and its retries do
// not depend on a reading either, but that state lasts minutes, not days.
//
// Nothing is asked on a tick that cannot read the project or its inbox: such
// a tick starts nothing, and without the items the standing items could be
// neither found nor answered.
func (t *pass) consults(o Observation, first Action) bool {
	if t.d.Usage == nil && t.d.Login == nil {
		return false
	}
	if o.BuildErr != nil || o.InboxErr != nil || o.NoLedger {
		return false
	}
	if _, open := openTitled(o.Items, OverageTitle); open {
		return true
	}
	if _, open := openTitled(o.Items, LoginTitle); open {
		return true
	}
	switch first.Kind {
	case ActLaunch, ActCreate, ActUnpark:
		return true
	case ActStop:
		// A ledger changed by hand into something that cannot be run. The
		// tick stops whatever usage says.
		return false
	}
	e, ok := runningEntry(o.Ledger)
	if !ok {
		return false
	}
	if hasUsagePause(e) && o.Session != nil && !o.Session.HostRunning {
		if wake, known := parseTime(e.UsageWakeAt); known && o.Now.Before(wake) {
			return false
		}
	}
	return true
}

// runningEntry is the entry Decide treats as the task in flight, when that
// task is running: the first entry in ledger order that is running or
// shipping, and only if it is the former. A shipping task has no agent at
// work, so usage has nothing to say about it.
func runningEntry(l ledger.Ledger) (ledger.Entry, bool) {
	for _, e := range l.Tasks {
		if e.Status == ledger.Running || e.Status == ledger.Shipping {
			return e, e.Status == ledger.Running
		}
	}
	return ledger.Entry{}, false
}

// hasUsagePause reports whether an entry is paused for usage. The pause is
// usage_pause_at and nothing else; the wake time is only ever set with it.
func hasUsagePause(e ledger.Entry) bool {
	return e.UsagePauseAt != ""
}

// clearUsagePause removes an entry's pause for usage. It is called wherever
// the task leaves `running` for good, starts a new attempt, or gets its
// session created again. It is not part of clearRecovery, because an un-park
// back to running calls that and must keep the pause (see unpark), and
// neither does parking clear it.
func clearUsagePause(e *ledger.Entry) {
	e.UsagePauseAt, e.UsageWakeAt = "", ""
}

// addUrgent stores an inbox item and tells the owner about it at the highest
// priority, or at the ordinary one when the loop has no urgent sender. It is
// add with the other sender; the governor's items are the ones that mean
// money may be being spent or that nothing can start at all.
func (t *pass) addUrgent(n inbox.NewItem) (inbox.Item, error) {
	it, err := t.store.Add(n)
	if err != nil {
		t.d.logf("%s: the inbox item %q could not be written: %v", t.p.Name, n.Title, err)
		return inbox.Item{}, err
	}
	switch {
	case t.d.NotifyUrgent != nil:
		t.d.NotifyUrgent(t.p, it)
	case t.d.Notify != nil:
		t.d.Notify(t.p, it)
	}
	return it, nil
}

// notices raises and answers the two standing items, on a tick that asked
// about usage and the login.
//
// Each item is about the machine's account, not about a task: it names no
// task and no session, so nothing parks on it. What holds the work back is
// Decide's own rule, read from the same verdict on every tick. The item is
// how the owner hears about it and is told what to do. The inbox is one per
// project, so a machine with several projects gets one item in each.
//
// An item is found by its open title before one is added, so a daemon killed
// right after adding it adds no second. And the loop answers it by itself
// once its cause is gone, as it does for StopTitle: the owner fixed the thing
// away from afk and should not have to come back to say so.
//
// "Gone" is read narrowly for extra usage. Only a reading that the governor
// could judge (it said run, no_launch or pause) shows extra usage is off. A
// hold means there is no usable reading, and that says nothing about the
// account either way, so the item stays. For the login it is a verdict that
// says logged in; an unknown login leaves the item as well.
func (t *pass) notices(o Observation) {
	overage, loggedOut := Notices(o)

	overageGone := false
	if o.Usage.Consulted {
		switch o.Usage.Action {
		case governor.ActionRun, governor.ActionNoLaunch, governor.ActionPause:
			overageGone = true
		}
	}
	t.standing(o, standingItem{
		on: overage, gone: overageGone,
		title: OverageTitle, body: overageBody(),
		kind: "overage", goneKind: "overage_off", rule: ruleGovernor,
		question: "Is extra usage switched on for this account?",
		yes:      "yes; nothing is started and a running session is stopped until it is switched off",
	})
	t.standing(o, standingItem{
		on: loggedOut, gone: o.Login.Known && o.Login.LoggedIn,
		title: LoginTitle, body: loginBody(),
		kind: "logged_out", goneKind: "logged_in", rule: ruleLogin,
		question: "Is the agent logged in on this machine?",
		yes:      "no; nothing new is started until it is logged in",
	})
}

// standingItem is one of the two items notices keeps: whether its cause is
// there now, whether it is known to be gone (the two are not opposites; see
// notices), and the words for the item and for the decision log.
type standingItem struct {
	on, gone       bool
	title, body    string
	kind, goneKind string
	rule, question string
	// yes is the decision-log answer when the item is raised.
	yes string
}

// standing raises the item when its cause is there and no open one says so,
// and answers every open one when the cause is known to be gone.
func (t *pass) standing(o Observation, s standingItem) {
	switch {
	case s.on:
		if _, have := openTitled(o.Items, s.title); have {
			return
		}
		it, err := t.addUrgent(inbox.NewItem{Kind: "failure", Blocking: true, Title: s.title, Body: s.body})
		if err != nil {
			return
		}
		t.record(s.kind, "", "", s.question, fmt.Sprintf("%s (inbox %s)", s.yes, it.ID), s.rule)
	case s.gone:
		var closed []string
		for _, it := range o.Items {
			if !it.Open() || it.Title != s.title {
				continue
			}
			if _, err := t.store.Answer(it.ID, "fixed", "cli"); err != nil {
				// Answered by the owner in the meantime is the same outcome;
				// anything else is tried again on the next tick that asks.
				if !errors.Is(err, inbox.ErrAlreadyAnswered) {
					t.d.logf("%s: inbox item %s could not be answered: %v", t.p.Name, it.ID, err)
				}
				continue
			}
			closed = append(closed, it.ID)
		}
		if len(closed) > 0 {
			t.record(s.goneKind, "", "", s.question,
				fmt.Sprintf("that is put right; answered inbox %s as fixed", strings.Join(closed, ", ")), s.rule)
		}
	}
}

// overageBody tells the owner why nothing runs and the one thing to do. It
// says "the agent's account" and names no product: this package does not know
// which agent it runs.
func overageBody() string {
	return "Extra usage is switched on for the agent's account. With it on, work past the plan's limit is charged " +
		"as extra money, so afk starts nothing in this project and stops a session that is running.\n\n" +
		"What to do next:\n" +
		"1. Switch extra usage off in the agent's account settings.\n" +
		"2. That is all. afk reads the account's usage again by itself, closes this item once extra usage is off, " +
		"and carries on from where it stopped. `afk usage` shows what afk last read.\n\n" +
		"You do not need to answer this item."
}

// loginBody is the same for a logged-out agent.
func loginBody() string {
	return "The agent's command-line tool is logged out on this machine, so afk cannot start an agent. " +
		"Nothing new starts in this project until it is logged in again. A session that is already running is left alone.\n\n" +
		"What to do next:\n" +
		"1. Log the agent in again on this machine, the way you did when you set it up.\n" +
		"2. That is all. afk checks the login again by itself and closes this item once it sees the agent logged in. " +
		"`afk daemon status` shows when it is seen.\n\n" +
		"You do not need to answer this item."
}

// governorKind reports whether a state kind is one of the three waits the
// usage governor causes. They are the states that nothing else logs: each is
// Decide saying "nothing is done", so no action records a line for it.
func governorKind(kind string) bool {
	switch kind {
	case string(ActUsageWait), string(ActLoginWait), string(ActUsagePaused):
		return true
	}
	return false
}

// governorChanged is called by save after it has stored a state that differs
// from the one before. It writes the decision-log line for a change into or
// out of one of the governor's waits, and sends the one notification a hold
// gets.
//
// The line is here, in one place, because those waits change nothing else: no
// ledger write and no item mark the tick where usage began to hold the work
// back, or stopped. It compares kinds and not whole states. A state also
// differs when only its reason does, and for a paused session that happens
// with nothing new decided: "waiting for it to stop", then "paused until",
// then "starts again when a reading allows" are one pause. It follows the
// write, like save's own notification: a state that cannot be stored would
// otherwise log on every tick.
//
// One change of reason does get a line: from one ActUsageWait to another.
// Decide gives that wait one fixed sentence for each governor action (see
// held), so there a different reason is a different verdict, no_launch
// becoming a hold, say, and each change of the verdict belongs in the log.
// Two readings under one unchanged verdict give the same sentence, so the
// state is not written at all and this is never reached.
//
// A hold (the governor has no usable reading) is the one wait the owner is
// told about here: nothing starts, nothing says when that ends, and it may be
// theirs to fix. It is sent when the state becomes that hold, from another
// kind or from another reason of the same kind, and the fixed sentence Decide
// gives a hold is what makes "the same hold as before" readable from the
// state file. A wait for no_launch or for a pause line sends nothing: it ends
// by itself at the reset. Extra usage and a logged-out agent have their items.
func (t *pass) governorChanged(prev daemonstate.State, had bool, st daemonstate.State) {
	old := ""
	if had {
		old = prev.Kind
	}
	kindChanged := old != st.Kind && (governorKind(old) || governorKind(st.Kind))
	verdictChanged := old == st.Kind && st.Kind == string(ActUsageWait) && prev.Reason != st.Reason
	if kindChanged || verdictChanged {
		t.record("governor", st.Task, st.Session, st.Reason, st.Kind, ruleGovernor)
	}

	hold := st.Kind == string(ActUsageWait) && t.obs.Usage.Consulted && t.obs.Usage.Action == governor.ActionHold
	sameHold := had && prev.Kind == st.Kind && prev.Reason == st.Reason
	if hold && !sameHold && t.d.NotifyIdle != nil {
		t.d.NotifyIdle(t.p, fmt.Sprintf("%s is holding new work", t.p.Name), st.Reason)
	}
}

// usagePause stops a running session's agent because of usage.
//
// The first time (Force false) the ledger is written before the host is
// asked to end, and that order is the safety of the whole pause. The pause is
// those two fields and nothing else. A daemon killed after the host ended and
// before the fields were saved would come back to a running task with no
// host, take it for a crash, and start it again inside the limit it was
// stopped for. In this order the worst case is a pause that is saved and a
// host that was never asked, which Decide's retry then stops.
//
// The pending continue prompt is dropped with it: there is about to be no
// agent to type into, and the resume sets its own. The restart count is left
// alone, because a pause is not a recovery and must not use one up.
//
// The host is asked only when it is running. Decide pauses a session whose
// host is already gone too (the limit is the same), and asking a host that is
// not there would only log an error. A request that fails is logged and left:
// the pause stands, and Decide repeats it with Force once PauseRetry has
// passed.
//
// With Force true the pause is already saved and the host did not go. The
// ledger is not touched, so the time the pause began stands: the retry and
// the report to the owner are both timed from it, and a rewrite would push
// them back for ever. Nothing is recorded either. The decision was to pause,
// and that line is already in the log; save logs the change of state.
func (t *pass) usagePause(a Action) daemonstate.State {
	if a.Force {
		if err := t.d.StopSession(t.row.WorktreePath, a.Session, true); err != nil {
			t.d.logf("%s: the session %s for %s is paused for usage and could not be made to stop: %v",
				t.p.Name, a.Session, a.Task, err)
		}
		return stateOf(a)
	}

	wake := ""
	if !a.WakeAt.IsZero() {
		wake = stamp(a.WakeAt)
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.UsagePauseAt = stamp(t.now)
		e.UsageWakeAt = wake
		e.ContinuePending = false
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}

	if t.obs.Session.HostRunning {
		if err := t.d.StopSession(t.row.WorktreePath, a.Session, false); err != nil {
			t.d.logf("%s: the session %s for %s is paused for usage, but its host could not be asked to stop: %v; trying again",
				t.p.Name, a.Session, a.Task, err)
		}
	}

	until := "it starts again, in the same conversation, when a new usage reading allows it"
	if wake != "" {
		until = fmt.Sprintf("it starts again, in the same conversation, after %s if usage allows it then", wakeText(a.WakeAt))
	}
	t.record("usage_pause", a.Task, a.Session, fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		"stop its agent; "+until, ruleGovernor)
	if t.d.NotifyIdle != nil {
		t.d.NotifyIdle(t.p, fmt.Sprintf("%s paused %s for usage", t.p.Name, a.Task),
			fmt.Sprintf("%s. Nothing is lost: %s.", upperFirst(a.Reason), until))
	}
	return stateOf(a)
}

// usageStuck tells the owner about a session that was to be stopped for usage
// and is still running after every try.
//
// It only adds the item. The item is blocking and names the task and the
// session, so Decide's rule e parks the task on it on the next tick, with its
// pause fields kept, and that park is also what keeps this from running
// twice: a parked task is not looked at again until the item is answered.
// Nothing is looked up first for the same reason. If the owner answers
// without ending the session, the un-parked task comes back here and a new
// item is raised, which is right: it is still spending.
func (t *pass) usageStuck(a Action) daemonstate.State {
	it, err := t.addUrgent(inbox.NewItem{
		Task: a.Task, Session: a.Session, Kind: "failure", Blocking: true,
		Title: fitTitle(PauseFailedTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)),
		Body:  pauseFailedBody(a.Task, a.Session),
	})
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
	}
	t.record("usage_stuck", a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("tell the owner to stop it by hand (inbox %s); %s parks on that item", it.ID, a.Task), ruleGovernor)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// pauseFailedBody tells the owner how to stop, by hand, a session afk could
// not stop. Attaching comes first because it keeps the session: the pause is
// still saved, so afk starts the same conversation again once usage allows.
func pauseFailedBody(task, sess string) string {
	return fmt.Sprintf("afk tried to stop the session for %s because of usage, and the session is still running. "+
		"It may still be spending usage.\n\n"+
		"What to do next:\n"+
		"1. Stop it by hand. Either run `afk attach %s` and quit the agent there, "+
		"or run `afk clean %s` to remove the session altogether.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`).\n\n"+
		"Once the session has stopped and usage allows work again, afk starts it again by itself, in the same conversation. "+
		"If you removed the session, afk creates it again and the agent picks %s up from the start. "+
		"If you answer while it is still running, afk tells you again.",
		task, sess, sess, task)
}

// usageResume starts a session that was stopped for usage again. Decide gives
// it only when the host is not running.
//
// It is relaunch without the restart count: the same request, the same
// continue prompt owed only when a conversation is resumed, and the same
// order, ledger first. Here the order guards against something worse than a
// lost count. One write clears the pause and saves the restart time, and only
// then is the host started. A daemon killed after the host started and
// before a later write would otherwise come back to a paused entry with a
// running host, and stop the session it had just resumed.
//
// A relaunch that fails leaves the pause cleared, on purpose, and drops the
// pending prompt. The entry is then an ordinary running task whose host is
// gone, with a restart time of now: recovery starts it again after the host's
// grace, and counts it, so a session that cannot be started is bounded by the
// restart limit and handed to the owner like any other. Putting the pause
// back would try the same relaunch on every tick with nothing to bound it.
func (t *pass) usageResume(a Action) daemonstate.State {
	req := RelaunchRequest{Worktree: t.row.WorktreePath, Session: a.Session}
	// As in relaunch: the session id of a stale view was not read.
	if !t.obs.Session.Stale {
		req.ResumeID = t.row.SessionID
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		clearUsagePause(e)
		e.RestartAt = stamp(t.now)
		e.ContinuePending = req.ResumeID != ""
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}

	how := "as a new conversation that picks the work up from the session's files"
	if req.ResumeID != "" {
		how = "resuming the same conversation"
	}
	st := stateOf(a)
	var chosen string
	if err := t.d.Relaunch(req); err != nil {
		t.noContinue(a)
		t.d.logf("%s: the session %s for %s could not be started again after its pause for usage: %v",
			t.p.Name, a.Session, a.Task, err)
		chosen = fmt.Sprintf("tried to start its host again and could not: %v", err)
		st.Reason = fmt.Sprintf("usage allows work again, but the session for %s (%s) could not be started again: %v",
			a.Task, a.Session, err)
	} else {
		t.restartedAt(a)
		chosen = "started its host again, " + how
		st.Reason = fmt.Sprintf("usage allows work again; started the session for %s (%s) again, %s", a.Task, a.Session, how)
	}
	t.record("usage_resume", a.Task, a.Session, fmt.Sprintf("%s. What now?", upperFirst(a.Reason)), chosen, ruleGovernor)
	return st
}
