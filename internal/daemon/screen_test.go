package daemon

import (
	"errors"
	"testing"
)

// TestScreenSaveDue: every action kind, each a row. The four true rows are
// the save points of PLAN T17.13 (assumption A1). The false rows are as
// load-bearing: ActWait and ActIdle are what the loop answers on almost every
// tick, and a true there would read the terminal every few seconds. The
// recovery and give-up kinds are false because the plan names its save points
// and these are not among them.
func TestScreenSaveDue(t *testing.T) {
	cases := []struct {
		kind ActionKind
		want bool
	}{
		{ActFail, true},
		{ActStall, true},
		{ActHostGone, true},
		{ActPark, true},

		{ActStop, false},
		{ActWait, false},
		{ActCreate, false},
		{ActShip, false},
		{ActUnpark, false},
		{ActLaunch, false},
		{ActIdle, false},
		{ActNoLedger, false},
		{ActOwner, false},
		{ActOwnerDone, false},
		{ActShipStep, false},
		{ActRestart, false},
		{ActRelaunch, false},
		{ActContinue, false},
		{ActGiveUp, false},
		{ActUsageWait, false},
		{ActLoginWait, false},
		{ActUsagePause, false},
		{ActUsagePaused, false},
		{ActUsageStuck, false},
		{ActUsageResume, false},
		{ActQuestionHold, false},
		{ActQuestionAnswered, false},
		{ActWake, false},
		{ActWakeGiveUp, false},
		{ActionKind(KindStopped), false},
		{ActionKind(KindBlocked), false},
		{ActionKind(KindLineStopped), false},
		{ActionKind(""), false},
	}
	seen := map[string]bool{}
	for _, tt := range cases {
		seen[string(tt.kind)] = true
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := screenSaveDue(tt.kind); got != tt.want {
				t.Errorf("screenSaveDue(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
	// A kind added to the loop must be given a row here, so whether it saves
	// the screen is decided on purpose and not by the default.
	for _, k := range Kinds() {
		if !seen[k] {
			t.Errorf("the kind %q has no row in TestScreenSaveDue", k)
		}
	}
}

// TestScreenNote: both branches. The error row's trailing full stop is
// load-bearing: an error text ending in one must not make the sentence end
// in two. The blank-error row keeps the sentence readable when a saver
// returns an error with no text.
func TestScreenNote(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "saved", want: "The agent's screen was saved to .afk/t1-1/terminal.html."},
		{name: "error", err: errors.New("rex is not running"),
			want: "The agent's screen could not be saved: rex is not running."},
		{name: "error ending in a full stop", err: errors.New("the block for t1-1 is gone."),
			want: "The agent's screen could not be saved: the block for t1-1 is gone."},
		{name: "blank error", err: errors.New(" "),
			want: "The agent's screen could not be saved: no reason was given."},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := screenNote("t1-1", tt.err); got != tt.want {
				t.Errorf("screenNote = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestScreenWithNote: an empty note leaves a body byte for byte as it was,
// which is what keeps every item of a loop with no saver unchanged.
func TestScreenWithNote(t *testing.T) {
	cases := []struct {
		name, body, note, want string
	}{
		{name: "no note", body: "Body.", want: "Body."},
		{name: "note", body: "Body.", note: "Note.", want: "Body.\n\nNote."},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := withNote(tt.body, tt.note); got != tt.want {
				t.Errorf("withNote = %q, want %q", got, tt.want)
			}
		})
	}
}
