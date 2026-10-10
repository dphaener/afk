// notification.go gives the dashboard's single status message a severity, and
// makes m.status a field only this file assigns.
//
// The type exists because the status was a bare string with two independent
// consumers — the footer's status row and the corner toast (toast.go) — and a
// bare string cannot say anything about itself. Sixty-odd sites wrote
// "restart failed for x: …" and "opening x…" into the same field; no styling
// could mark one as a failure, and nothing could express "record this, but do
// not lift it into the corner". Every such distinction had to be re-derived
// by reading the words, which is to say it was never derived at all.
//
// Model.notify is the one place a notification is recorded; clearStatus is
// the only other function that assigns the field. The field is unexported, so
// nothing outside package ui can set it at all; inside the package the rule
// is that every assignment to it lives in this file, which is a single grep
// to check rather than sixty-five sites to read.
//
// notify stamps statusChangedAt, the timestamp toast.go compares against its
// clock. It has to happen here rather than after handle returns, because a
// handler issuing two notifications in one Update must see the first one's
// timestamp when it decides what to do about the second.
//
// Notifications have an origin as well as a kind. notify is user-initiated:
// one keypress, one message, written straight into the slot. notifyBackground
// is for events the dashboard observed on its own — the discovery tick's
// session diff, a failing refresh command — and those go through arbitration
// (flushBackground) because there is one slot and no queue: several can
// arrive in one Update, and one can arrive while a message the user just
// triggered is still on screen. The rules are documented on flushBackground.
package ui

import "strings"

// notificationKind is how a notification should be read: whether something
// failed, finished, was refused, or is merely being reported. The toast
// styles the box by kind, so an error is distinguishable from a success
// without reading the words.
//
// The zero value is notifyInfo deliberately: a status acquired by any path
// other than notify — tests that assign the field directly, a Model built
// from a literal — renders as an ordinary informational message rather than
// as an unstyled box or, worse, an error.
type notificationKind int

const (
	// notifyInfo: navigation, mode changes, in-flight "…" progress, and
	// neutral statements of fact.
	notifyInfo notificationKind = iota
	// notifySuccess: an operation with a durable effect finished — a session
	// created, cleaned or restarted, a label saved.
	notifySuccess
	// notifyWarn: a refusal, a blocked action, or a "nothing to do here".
	// The user asked for something and did not get it, but nothing broke.
	notifyWarn
	// notifyError: a failure or a dropped operation.
	notifyError
)

// notify records a notification: its text, its kind, and the moment it was
// (re)issued.
//
// The stamp is skipped in exactly one case: the text is unchanged AND its
// toast is still on screen (toastVisible) — never re-float text the user is
// currently looking at. Skipping on unchanged text alone would be silence
// once the toast has expired, because the toast is the only surface: a
// session that waits on the user, is answered, and waits again produces the
// byte-identical event both times, and `c` pressed twice with a pane open is
// owed its refusal both times.
//
// The kind is recorded either way, so a repeat that changes severity restyles
// a toast still on screen rather than leaving it contradicting itself. An
// empty text never stamps; clearStatus is the way to empty the field.
func (m *Model) notify(kind notificationKind, text string) {
	restamp := text != m.status || !m.toastVisible()
	m.status = text
	m.statusKind = kind
	if restamp && text != "" {
		m.statusChangedAt = m.deps.Now()
	}
}

// clearStatus empties the status, for the sites where the point is that there
// is nothing to say rather than that something happened: a modal taking over,
// a form or the palette opening. notify(notifyInfo, "") would mean the same
// and read as a mistake at every one of them.
//
// statusChangedAt is left alone. toastVisible gates on an empty status before
// it looks at the clock, so there is no stale toast to prevent.
func (m *Model) clearStatus() {
	m.status = ""
	m.statusKind = notifyInfo
}

// backgroundCategory is what a background event is about, for the summary a
// coalesced batch renders as ("2 finished, 1 host died"). This is the
// vocabulary emitters (the session diff on the discovery tick, the refresh
// commands' error paths) speak; a new kind of background event adds a
// category here and a label in backgroundLabels, and nothing else.
//
// The order is the order counts appear in the summary — session outcomes
// first, then the dashboard's own failures — so a batch reads the same
// however its events arrived.
type backgroundCategory int

const (
	// bgFinished: a session crossed into the Done phase.
	bgFinished backgroundCategory = iota
	// bgFailed: a session crossed into the Failed phase.
	bgFailed
	// bgAwaitingInput: a session started waiting on the user.
	bgAwaitingInput
	// bgHostDied: a session's host stopped between ticks (advisory — see
	// discover.HostRunning; never escalated above warn by its emitter).
	bgHostDied
	// bgDiscoveryError: the whole-dashboard discovery pass failed.
	bgDiscoveryError
	// bgStackError: the git-spice stack refresh failed.
	bgStackError
	// bgBranchesError: the plain-git branch listing failed.
	bgBranchesError
	// bgScanError: the project-scan config file failed to parse.
	bgScanError

	backgroundCategoryCount
)

// backgroundLabels is the summary noun per category, singular then plural.
// Indexed by backgroundCategory's own values so there is no second table to
// keep in step with the enum.
var backgroundLabels = [backgroundCategoryCount][2]string{
	bgFinished:       {"finished", "finished"},
	bgFailed:         {"failed", "failed"},
	bgAwaitingInput:  {"awaiting input", "awaiting input"},
	bgHostDied:       {"host died", "hosts died"},
	bgDiscoveryError: {"discovery error", "discovery errors"},
	bgStackError:     {"stack error", "stack errors"},
	bgBranchesError:  {"branch listing error", "branch listing errors"},
	bgScanError:      {"scan error", "scan errors"},
}

// backgroundEvent is one background notification awaiting the slot: the
// kind it would render as on its own, the category it counts under in a
// summary, and its specific text, which is what lands when it is alone.
type backgroundEvent struct {
	kind     notificationKind
	category backgroundCategory
	text     string
}

// notifyBackground records a background-origin event. It does not touch the
// status: events accumulate on pendingBackground until flushBackground runs
// at the end of the Update that collected them, so everything one message
// produces — every transition one discovery snapshot revealed — is one
// batch and one write.
//
// The kind is the emitter's call, not the category's: a category says what
// the event is ABOUT, for the summary's counts, while severity belongs to
// whoever observed it (host death is warn because HostRunning is advisory —
// a fact the emitter knows and this table does not). Deduplication is the
// emitter's job too: the arbiter counts what it is given.
func (m *Model) notifyBackground(kind notificationKind, category backgroundCategory, text string) {
	m.pendingBackground = append(m.pendingBackground, backgroundEvent{kind: kind, category: category, text: text})
}

// flushBackground gives the pending background batch the status slot, or
// makes it wait. Update's wrapper calls it after every message, which is
// what makes it both the coalescing point and the promotion point.
//
// The precedence rule: a background batch never overwrites a LIVE toast —
// one whose status is within toastTTL of statusChangedAt, the same test
// toastVisible applies — whatever that toast's origin. A user-initiated
// notification, by contrast, goes through notify and takes the slot
// immediately, whatever is in it. So:
//
//   - Background yields to a live user toast. The batch is not dropped; it
//     stays pending and lands once the toast has expired.
//   - A background event that arrives while an earlier one is still waiting
//     joins the batch. What eventually lands is one coalesced summary, not
//     the newest event alone — the alternative loses the earlier event.
//   - Background yields to a live BACKGROUND toast as well. projectCmds
//     dispatches discovery, the stack refresh and the branch listing as one
//     batch whose replies land as three separate Updates milliseconds apart;
//     without this, a once-only stack error would be clobbered by the session
//     diff before anyone could read it, and its emitter's repeat suppression
//     would then never show it again. Deferring costs nothing extra, because
//     coalescing bounds what waits to a single summary, never a queue. The
//     milder case — a 2s-old background toast that would have been cut short
//     by the next tick's — is covered by the same rule, deliberately: each
//     background message gets its full toastTTL.
//   - The kind of a coalesced summary is worst-wins: error over warn over
//     success over info, so a batch with one failure in it is never dressed
//     as a success. That is the constants' declared order, which
//     TestWorstKindOrdering pins.
//
// Promotion latency: no timer Cmd drives this (toast.go explains why none is
// armed). A deferred batch is promoted by whichever message arrives first
// after the live toast expires, and the discovery tick guarantees one every
// m.interval — so promotion lands up to one tick (2s by default) after the
// TTL. That is accepted; a timer to shave it would turn every notification
// site into a Cmd-returning one.
func (m *Model) flushBackground() {
	if len(m.pendingBackground) == 0 {
		return
	}
	if m.toastVisible() {
		return
	}
	kind, text := coalesceBackground(m.pendingBackground)
	m.pendingBackground = nil
	m.notify(kind, text)
}

// coalesceBackground reduces a batch to one notification. A lone event keeps
// its own text and kind; two or more collapse to counts by category in
// category order — "2 finished, 1 host died" — under the worst kind present.
// The summary is an outcome with no key hint, so it carries no divider (the
// house style in messages.go: no hint, no "·").
func coalesceBackground(events []backgroundEvent) (notificationKind, string) {
	if len(events) == 1 {
		return events[0].kind, events[0].text
	}
	var counts [backgroundCategoryCount]int
	kind := notifyInfo
	for _, ev := range events {
		counts[ev.category]++
		kind = max(kind, ev.kind)
	}
	parts := make([]string, 0, len(counts))
	for c, n := range counts {
		if n == 0 {
			continue
		}
		label := backgroundLabels[c][0]
		if n > 1 {
			label = backgroundLabels[c][1]
		}
		parts = append(parts, itoa(n)+" "+label)
	}
	return kind, strings.Join(parts, ", ")
}
