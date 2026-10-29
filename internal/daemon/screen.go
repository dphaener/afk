package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// This file is the daemon's half of keeping the agent's last screen (PLAN
// T17.13). At the moment the loop tells the owner something went wrong with a
// session, or parks its task, it asks Deps.SaveScreen to write the session's
// terminal to .afk/{session}/terminal.html, so the owner can see from a phone
// what the agent's terminal showed. The save itself (reading the block,
// trimming it, writing the page) lives with the caller: this package never
// talks to a terminal, for the reason given at the top of daemon.go.

// screenSaveTimeout bounds one save. The save reads the whole of a terminal's
// output from another process; a wedged reader must not hold up the tick that
// is raising the item, and an item raised without a screen is far better than
// no item at all.
const screenSaveTimeout = 30 * time.Second

// screenSaveDue reports whether the action is one of the moments the screen
// is saved: a failure, a stall, a host gone, or a park. These are the
// transitions where the owner is about to be sent to look at a session, and
// each is taken once per event, so a save here is never repeated on the
// loop's regular tick. Every other action, ActWait and ActIdle included, is
// false, which is what keeps the terminal from being read on every pass.
func screenSaveDue(action ActionKind) bool {
	switch action {
	case ActFail, ActStall, ActHostGone, ActPark:
		return true
	}
	return false
}

// screenNote is the sentence added to an item's body to say whether the save
// worked. It is in the body, and not a field of the item, because the item's
// schema has no place for it (PLAN T17.13, assumption A3) and the web page
// links the file by the session's name anyway.
//
// The session's name goes into the saved path. The failure sentence takes
// the error text as the reason; the saver names the session in its own
// errors where that helps. The trailing full stop of the error text is
// dropped so the sentence does not end in two.
func screenNote(session string, err error) string {
	if err == nil {
		return fmt.Sprintf("The agent's screen was saved to .afk/%s/terminal.html.", session)
	}
	reason := strings.TrimRight(strings.TrimSpace(err.Error()), ".")
	if reason == "" {
		reason = "no reason was given"
	}
	return fmt.Sprintf("The agent's screen could not be saved: %s.", reason)
}

// saveScreen saves the session's screen when the action is a save point and
// the loop has a saver, and returns the note to add to the item's body. ""
// means nothing was tried, and the body is left exactly as it was: a loop
// with no Deps.SaveScreen writes the same items it did before screens were
// saved.
//
// A failed save is only ever a sentence in the body. The item is raised and
// the task parked all the same, because the owner hearing about the session
// matters far more than the picture of it.
func (t *pass) saveScreen(a Action) string {
	if t.d.SaveScreen == nil || !screenSaveDue(a.Kind) || a.Session == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), screenSaveTimeout)
	defer cancel()
	err := t.d.SaveScreen(ctx, a.Session)
	if err != nil {
		t.d.logf("%s: the screen of the session %s could not be saved: %v", t.p.Name, a.Session, err)
	}
	return screenNote(a.Session, err)
}

// withNote appends a screen note to an item's body, as a paragraph of its
// own. An empty note leaves the body unchanged.
func withNote(body, note string) string {
	if note == "" {
		return body
	}
	return body + "\n\n" + note
}
