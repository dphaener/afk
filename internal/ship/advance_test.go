package ship

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// tally is every counted outside action, for one comparison per row.
type tally struct {
	pushes, prs, merges, reruns, prompts, deletes, syncs int
}

func (w *world) tally() tally {
	pushes := 0
	for _, n := range w.pushes {
		pushes += n
	}
	return tally{pushes, w.prsOpened, w.merges, w.reruns, w.prompts, w.remoteDeletes, w.syncsChanged}
}

// wantText fails the test for each of wants that text does not contain.
func wantText(t *testing.T, label, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not contain %q:\n%s", label, want, text)
		}
	}
}

// wantItem checks that an outcome carries an item of the given kind and
// returns it (an empty one when it is missing, so the caller can go on).
func wantItem(t *testing.T, out Outcome, kind string) Item {
	t.Helper()
	if out.Item == nil {
		t.Errorf("the %s outcome carries no item (reason: %s)", out.Kind, out.Reason)
		return Item{}
	}
	if out.Item.Kind != kind {
		t.Errorf("item kind = %q, want %q", out.Item.Kind, kind)
	}
	if out.Item.Title == "" || out.Item.Body == "" {
		t.Errorf("item has an empty title or body: %+v", *out.Item)
	}
	return *out.Item
}

// wantOrder fails the test unless each of want appears in events and the first
// appearance of each comes after the first appearance of the one before it.
func wantOrder(t *testing.T, events []string, want ...string) {
	t.Helper()
	last := -1
	for _, name := range want {
		at := -1
		for i, e := range events {
			if e == name {
				at = i
				break
			}
		}
		if at <= last {
			t.Errorf("events = %v, want the first %q after everything before it in %v", events, name, want)
			return
		}
		last = at
	}
}

// statesSaved is every state the ship was saved in, in order.
func (w *world) statesSaved() []State {
	var states []State
	for _, p := range w.savedLog {
		states = append(states, p.State)
	}
	return states
}

// TestAdvanceScenarios drives whole ships over the fake project, one call of
// Advance per tick from the progress last saved, and asserts on what was done
// to the outside (pushes, pull requests, merges, re-runs, prompts).
//
// Load-bearing rows, and why:
//
//   - "no checks parks after five minutes and never merges": a pull request
//     no CI has passed must never merge. The row keeps calling after the park
//     and the fake Merge itself fails the test if it is ever called on checks
//     that are not green, so every other row guards this too.
//   - "park at the hand-back cap": without the cap a red CI prompts the
//     session for ever. The tally pins two prompts and three re-runs.
//   - "required env parks before the merge": the merge must wait for the
//     owner, and must happen once the owner has answered.
//   - "deploy timeout stops the line": the marker must exist before the
//     outcome is returned, and the task finishes only once it is gone.
//   - "after-merge command runs once, after the merge and before the deploy
//     check": the order of base update, branch delete, command and first
//     health read is the feature. The fake command also fails any row that
//     runs it before the merge is wholly finished or without a start mark.
//   - Every row that sets no command expects zero runs (the runs column), so
//     "empty means no step" is checked on all the rows that existed before.
//   - "after-merge failure stops the line ...": one run, an item with the end
//     of the output, no health read, and still one run after the owner clears
//     the stop. Running the command again there is the mistake A2 forbids.
//   - "a record for another task ..." and "... another merge commit ...": the
//     record file is one per project and outlives its ship. Taking the last
//     ship's "exit 0" for this one would skip the command without a word.
func TestAdvanceScenarios(t *testing.T) {
	afterMergeGreen := func(w *world) {
		w.ci["c1"] = []Checks{ciGreen}
		w.cfg.AfterMerge = testCommand
		w.cfg.AfterMergeTimeout = 10 * time.Minute
	}
	greenTally := tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1}
	tests := []struct {
		name  string
		setup func(w *world)
		want  OutcomeKind
		tally tally
		// runs is how many times the after-merge command must have been
		// started when the first outcome that is not a wait comes back.
		runs  int
		check func(t *testing.T, w *world, out Outcome)
	}{
		{
			name:  "green first time",
			setup: func(w *world) { w.ci["c1"] = []Checks{ciGreen} },
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, out Outcome) {
				if w.saved.State != DeployHealthy || w.saved.MergedSHA != "m-c1" || w.saved.PR != testPR {
					t.Errorf("saved = %+v, want deploy_healthy with merge m-c1 and pull request #%d", w.saved, testPR)
				}
				if out.Progress != w.saved {
					t.Errorf("outcome progress = %+v, want the saved %+v", out.Progress, w.saved)
				}
				if w.remote != "" {
					t.Errorf("the remote branch is still there (%s)", w.remote)
				}
			},
		},
		{
			name: "[ship] base wins over the session's base",
			setup: func(w *world) {
				// wantBase in the fake fails the test on any other base.
				w.cfg.Base = "release"
				w.ci["c1"] = []Checks{ciGreen}
			},
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
		},
		{
			name:  "red then green on re-run",
			setup: func(w *world) { w.ci["c1"] = []Checks{ciRed, ciGreen} },
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, reruns: 1, deletes: 1, syncs: 1},
		},
		{
			name: "red, re-run red, hand-back, new commit, green",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciRed, ciRed}
				w.ci["c2"] = []Checks{ciGreen}
				w.commitOnPrompt("c2")
			},
			want:  OutcomeDone,
			tally: tally{pushes: 2, prs: 1, merges: 1, reruns: 1, prompts: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				wantText(t, "the hand-back file", w.handBackFile(),
					"#7", "Head commit: c1", "unit-tests", "expected 2, got 3", "Do not push")
				wantText(t, "the prompt", w.promptTexts[0],
					testDir+"/"+HandBackFile, "commit on this branch", "Do not push", "do not open a pull request")
				if strings.Contains(w.promptTexts[0], "\n") {
					t.Errorf("the prompt is more than one line: %q", w.promptTexts[0])
				}
				if w.saved.FixCycles != 1 || w.saved.MergedSHA != "m-c2" {
					t.Errorf("saved = %+v, want one fix cycle and merge m-c2", w.saved)
				}
			},
		},
		{
			name: "park at the hand-back cap",
			setup: func(w *world) {
				for _, c := range []string{"c1", "c2", "c3"} {
					w.ci[c] = []Checks{ciRed, ciRed}
				}
				w.commitOnPrompt("c2", "c3")
			},
			want:  OutcomePark,
			tally: tally{pushes: 3, prs: 1, reruns: 3, prompts: 2},
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item", item.Title+"\n"+item.Body,
					"CI is still failing", "2 hand-backs", "unit-tests", "#7", "afk ledger set T9.9 failed")
			},
		},
		{
			name: "conflict hands back",
			setup: func(w *world) {
				w.conflicts["c1"] = true
				w.ci["c1"] = []Checks{ciGreen}
				w.ci["c2"] = []Checks{ciGreen}
				w.commitOnPrompt("c2")
			},
			want:  OutcomeDone,
			tally: tally{pushes: 2, prs: 1, merges: 1, prompts: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				wantText(t, "the hand-back file", w.handBackFile(),
					"#7", "Head commit: c1", "conflicts with `main`", "must have `main` merged in")
				if strings.Contains(w.handBackFile(), "expected 2, got 3") {
					t.Errorf("a conflict hand-back carries a CI log:\n%s", w.handBackFile())
				}
			},
		},
		{
			name: "no checks parks after five minutes and never merges",
			want: OutcomePark,
			// No CI is scripted, so GitHub reports no check at all.
			tally: tally{pushes: 1, prs: 1},
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item", item.Body,
					"CI workflow that runs on pull requests", "afk ledger set T9.9 done", "another five minutes")
				if waited := w.clock.Sub(w.saved.At); w.saved.State != PROpen || waited < NoChecksGrace {
					t.Errorf("parked in %q after %s, want pr_open after at least %s", w.saved.State, waited, NoChecksGrace)
				}
				for i := 0; i < 20; i++ {
					if again, _ := w.step(); again.Kind != OutcomePark {
						t.Fatalf("call %d after the park returned %q (%s), want park", i+1, again.Kind, again.Reason)
					}
				}
				if w.merges != 0 {
					t.Errorf("a pull request with no checks was merged %d time(s)", w.merges)
				}
			},
		},
		{
			name: "required env parks before the merge, and merges once answered",
			setup: func(w *world) {
				w.cfg.RequiredEnvPattern = `ENV\["([A-Z_]+)"\]`
				w.added = []string{`key := ENV["STRIPE_KEY"]`, `hook := ENV["WEBHOOK_SECRET"]`}
				w.ci["c1"] = []Checks{ciGreen}
			},
			want:  OutcomePark,
			tally: tally{pushes: 1, prs: 1},
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemEnvVar)
				if item.Title != "Set STRIPE_KEY before T9.9 merges" {
					t.Errorf("title = %q", item.Title)
				}
				wantText(t, "the item", item.Body, "WEBHOOK_SECRET")

				// One item per variable: answering the first raises the second.
				w.answered["STRIPE_KEY"] = true
				second := wantItem(t, w.run(), ItemEnvVar)
				if second.Title != "Set WEBHOOK_SECRET before T9.9 merges" {
					t.Errorf("second title = %q", second.Title)
				}
				if w.merges != 0 {
					t.Fatalf("merged %d time(s) with a variable unanswered", w.merges)
				}

				w.answered["WEBHOOK_SECRET"] = true
				if done := w.run(); done.Kind != OutcomeDone {
					t.Fatalf("after both answers: %q (%s), want done", done.Kind, done.Reason)
				}
				if w.merges != 1 {
					t.Errorf("merges = %d, want 1", w.merges)
				}
			},
		},
		{
			name: "deploy healthy after a few polls",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.healthyOn = 4
			},
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.healthCalls != 4 || w.stops != 0 {
					t.Errorf("health calls = %d, stops = %d; want 4 and 0", w.healthCalls, w.stops)
				}
				for _, url := range w.healthSeen {
					if url != testHealth {
						t.Errorf("Health was asked %q, want %q", url, testHealth)
					}
				}
			},
		},
		{
			name: "deploys off",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.cfg.Deploys = false
				w.healthyOn = 0
			},
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.healthCalls != 0 {
					t.Errorf("Health was asked %d time(s) with deploys off", w.healthCalls)
				}
			},
		},
		{
			name: "no health url",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.cfg.HealthURL = ""
				w.healthyOn = 0
			},
			want:  OutcomeDone,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.healthCalls != 0 {
					t.Errorf("Health was asked %d time(s) with no URL", w.healthCalls)
				}
			},
		},
		{
			name: "a health check that errors counts as not healthy yet",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.errs["Health"] = errors.New("connection refused")
			},
			want:  OutcomeStop,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, _ Outcome) {
				if !w.failingSince.IsZero() {
					t.Errorf("a refused health check was recorded as a failed GitHub read")
				}
			},
		},
		{
			name: "deploy timeout stops the line, and done once it is cleared",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.cfg.HealthURL = "https://deploy:hunter2@app.example.test/up?token=abc"
				w.healthyOn = 0
			},
			want:  OutcomeStop,
			tally: tally{pushes: 1, prs: 1, merges: 1, deletes: 1, syncs: 1},
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item", item.Title+"\n"+item.Body,
					testTask, "https://app.example.test/up", "15m0s", "afk resume")
				for _, secret := range []string{"hunter2", "token=abc"} {
					if strings.Contains(item.Body, secret) {
						t.Errorf("the item shows the health URL's secret %q:\n%s", secret, item.Body)
					}
				}
				if !w.stopMarker || w.stops != 1 || w.saved.State != Stopped {
					t.Fatalf("marker = %v, stops = %d, state = %q; want the marker, one stop, stopped",
						w.stopMarker, w.stops, w.saved.State)
				}
				wantText(t, "the stop reason", w.stopReason, testTask, "did not become healthy")
				if out.Progress != w.saved {
					t.Errorf("outcome progress = %+v, want the saved %+v", out.Progress, w.saved)
				}
				for i := 0; i < 5; i++ {
					if again, _ := w.step(); again.Kind != OutcomeWait {
						t.Fatalf("call %d while stopped returned %q, want wait", i+1, again.Kind)
					}
				}
				w.stopMarker = false
				if done := w.run(); done.Kind != OutcomeDone {
					t.Fatalf("after the stop was cleared: %q (%s), want done", done.Kind, done.Reason)
				}
				if w.stops != 1 || w.merges != 1 {
					t.Errorf("stops = %d, merges = %d; want 1 and 1", w.stops, w.merges)
				}
			},
		},
		{
			name:  "after-merge command runs once, after the merge and before the deploy check",
			setup: afterMergeGreen,
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, _ Outcome) {
				wantOrder(t, w.events, "sync", "delete", "command", "health")
				if len(w.runCommands) != 1 || w.runCommands[0] != testCommand || w.runTimeouts[0] != 10*time.Minute {
					t.Errorf("the command ran as %q with %v; want %q with a ten-minute limit", w.runCommands, w.runTimeouts, testCommand)
				}
				want := []State{Committed, Pushed, PROpen, CIGreen, Merged, Merged, AfterMerge, DeployHealthy}
				if got := w.statesSaved(); !reflect.DeepEqual(got, want) {
					t.Errorf("saved states = %v, want %v (the command itself saves nothing)", got, want)
				}
				rec := w.record
				if rec == nil || rec.task != testTask || rec.sha != "m-c1" || !rec.run.Finished || rec.run.ExitCode != 0 || rec.run.TimedOut {
					t.Errorf("the run record = %+v, want a finished run with exit 0 for %s at m-c1", rec, testTask)
				}
			},
		},
		{
			name:  "empty after-merge command means no step",
			setup: func(w *world) { w.ci["c1"] = []Checks{ciGreen}; w.cfg.AfterMerge = "" },
			want:  OutcomeDone,
			tally: greenTally,
			check: func(t *testing.T, w *world, _ Outcome) {
				if slices.Contains(w.statesSaved(), AfterMerge) {
					t.Errorf("saved states = %v, want no after_merge", w.statesSaved())
				}
				if w.record != nil {
					t.Errorf("a run record was written with no command: %+v", w.record)
				}
			},
		},
		{
			name: "after-merge command with no deploy to check",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.cfg.Deploys = false
				w.healthyOn = 0
			},
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.healthCalls != 0 {
					t.Errorf("Health was asked %d time(s) with deploys off", w.healthCalls)
				}
			},
		},
		{
			name: "after-merge failure stops the line, and clearing the marker finishes the task",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.runExit = 3
				w.runTail = "afk: running the after-merge command\ncompiling\nboom: no such target\n"
			},
			want:  OutcomeStop,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				t.Logf("the item the owner reads:\n%s\n\n%s", item.Title, item.Body)
				if item.Title != "The after-merge command failed after T9.9" {
					t.Errorf("title = %q", item.Title)
				}
				wantText(t, "the item body", item.Body,
					"T9.9 was merged", "exit code 3", "afk.toml", "after_merge", "project folder",
					"    compiling\n    boom: no such target\n", AfterMergeLog,
					"run it by hand", "afk resume", "marked done once it is cleared", "does not run the command again")
				for _, text := range []string{item.Title, item.Body, out.Reason, w.stopReason} {
					if strings.Contains(text, "hunter2") {
						t.Errorf("the command's text was shown to the owner: %q", text)
					}
				}
				wantText(t, "the stop reason", w.stopReason, testTask, "exit code 3")
				if !w.stopMarker || w.stops != 1 || w.saved.State != Stopped || out.Progress != w.saved {
					t.Fatalf("marker = %v, stops = %d, saved = %+v, outcome progress = %+v; want the marker, one stop, stopped",
						w.stopMarker, w.stops, w.saved, out.Progress)
				}
				if w.healthCalls != 0 {
					t.Errorf("the deploy was checked %d time(s) after a failed command", w.healthCalls)
				}
				for i := 0; i < 5; i++ {
					if again, _ := w.step(); again.Kind != OutcomeWait {
						t.Fatalf("call %d while stopped returned %q, want wait", i+1, again.Kind)
					}
				}
				w.stopMarker = false
				if done := w.run(); done.Kind != OutcomeDone {
					t.Fatalf("after the stop was cleared: %q (%s), want done", done.Kind, done.Reason)
				}
				if w.runs != 1 || w.stops != 1 || w.healthCalls != 0 {
					t.Errorf("after the clear: runs = %d, stops = %d, health calls = %d; want 1, 1 and 0",
						w.runs, w.stops, w.healthCalls)
				}
			},
		},
		{
			name: "after-merge timeout stops the line",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.runExit, w.runTimedOut = -1, true
				w.runTakes = 10 * time.Minute
			},
			want:  OutcomeStop,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item", item.Title+"\n"+item.Body,
					"The after-merge command failed after T9.9", "did not finish within 10m0s",
					"afk has no output from the command", AfterMergeLog, "afk resume")
				if strings.Contains(item.Body, "exit code") {
					t.Errorf("a timeout is shown as an exit code:\n%s", item.Body)
				}
				if !w.stopMarker || w.saved.State != Stopped || w.healthCalls != 0 {
					t.Errorf("marker = %v, state = %q, health calls = %d", w.stopMarker, w.saved.State, w.healthCalls)
				}
			},
		},
		{
			name: "an after-merge command that cannot be started stops the line",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.runErr = errors.New("fork/exec /bin/sh: no such file or directory")
				w.runTail = "afk: the command could not be started: fork/exec /bin/sh: no such file or directory"
			},
			want:  OutcomeStop,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item body", item.Body, "could not be started", "fork/exec /bin/sh")
				if strings.Contains(item.Body, "exit code") {
					t.Errorf("a command that never ran is shown with an exit code:\n%s", item.Body)
				}
				if rec := w.record; rec == nil || !rec.run.Finished || rec.run.ExitCode != -1 {
					t.Errorf("the run record = %+v, want a finished run with exit code -1", rec)
				}
			},
		},
		{
			name: "an after-merge run that was started and never finished stops the line without running",
			setup: func(w *world) {
				w.at(AfterMerge)
				w.cfg.AfterMerge = testCommand
				w.ran(AfterMergeRun{})
				w.runTail = "half a line of outp"
			},
			want: OutcomeStop,
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				t.Logf("the item the owner reads:\n%s\n\n%s", item.Title, item.Body)
				if item.Title != "The after-merge command was interrupted after T9.9" {
					t.Errorf("title = %q", item.Title)
				}
				wantText(t, "the item body", item.Body,
					"afk itself was stopped while the command ran", "does not know whether the command finished",
					"    half a line of outp\n", AfterMergeLog, "run it by hand", "afk resume", "does not run the command again")
				if !w.stopMarker || w.saved.State != Stopped || w.healthCalls != 0 {
					t.Errorf("marker = %v, state = %q, health calls = %d", w.stopMarker, w.saved.State, w.healthCalls)
				}
				w.stopMarker = false
				if done := w.run(); done.Kind != OutcomeDone || w.runs != 0 {
					t.Errorf("after the clear: %q, runs = %d; want done and no run", done.Kind, w.runs)
				}
			},
		},
		{
			name: "a record for another task is ignored and the command runs",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.record = &afterMergeRecord{task: "T1.1", sha: "m-c1",
					run: AfterMergeRun{Finished: true, FinishedAt: now.Add(-time.Hour)}, startedAt: now.Add(-time.Hour)}
			},
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
		},
		{
			name: "a record for another merge commit of the same task is ignored and the command runs",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.record = &afterMergeRecord{task: testTask, sha: "m-older",
					run: AfterMergeRun{Finished: true, ExitCode: 2, FinishedAt: now.Add(-time.Hour)}, startedAt: now.Add(-time.Hour)}
			},
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
		},
		{
			name: "an unfinished record for another task does not stop this ship",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.record = &afterMergeRecord{task: "T1.1", sha: "m-c1", startedAt: now.Add(-time.Hour)}
			},
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
		},
		{
			name: "a slow after-merge command does not use up the deploy's time",
			setup: func(w *world) {
				afterMergeGreen(w)
				// Longer than the whole deploy timeout; the deploy then
				// takes three more polls to answer.
				w.runTakes = 20 * time.Minute
				w.healthyOn = 3
			},
			want:  OutcomeDone,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.stops != 0 || w.healthCalls != 3 {
					t.Errorf("stops = %d, health calls = %d; want 0 and 3", w.stops, w.healthCalls)
				}
				if rec := w.record; rec == nil || rec.run.FinishedAt.Sub(rec.startedAt) != 20*time.Minute {
					t.Errorf("the run record = %+v, want an end twenty minutes after the start", rec)
				}
			},
		},
		{
			name: "after-merge command, then a deploy that never turns healthy stops for the deploy",
			setup: func(w *world) {
				afterMergeGreen(w)
				w.healthyOn = 0
			},
			want:  OutcomeStop,
			tally: greenTally,
			runs:  1,
			check: func(t *testing.T, w *world, out Outcome) {
				item := wantItem(t, out, ItemFailure)
				wantText(t, "the item", item.Title+"\n"+item.Body, "The deploy after T9.9 did not become healthy", "15m0s")
				finished := w.record.run.FinishedAt
				if waited := w.savedAt[len(w.savedAt)-1].Sub(finished); waited < 15*time.Minute || waited > 16*time.Minute {
					t.Errorf("stopped %s after the command ended, want the deploy timeout", waited)
				}
			},
		},
		{
			name: "the command is removed from afk.toml before it ran: straight to the deploy check",
			setup: func(w *world) {
				w.at(AfterMerge)
				w.cfg.AfterMerge = ""
				w.healthyOn = 2
			},
			want: OutcomeDone,
			check: func(t *testing.T, w *world, _ Outcome) {
				if w.healthCalls != 2 || w.saved.State != DeployHealthy || w.record != nil {
					t.Errorf("health calls = %d, state = %q, record = %+v; want 2, deploy_healthy and none",
						w.healthCalls, w.saved.State, w.record)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			if tt.setup != nil {
				tt.setup(w)
			}
			out := w.run()
			if out.Kind != tt.want {
				t.Fatalf("outcome = %q (%s), want %q", out.Kind, out.Reason, tt.want)
			}
			if got := w.tally(); got != tt.tally {
				t.Errorf("calls = %+v, want %+v", got, tt.tally)
			}
			if w.runs != tt.runs {
				t.Errorf("the after-merge command ran %d time(s), want %d", w.runs, tt.runs)
			}
			for sha, n := range w.pushes {
				if n != 1 {
					t.Errorf("commit %s was pushed %d times", sha, n)
				}
			}
			if tt.check != nil {
				tt.check(t, w, out)
			}
		})
	}
}

// TestAdvanceActionFailuresPark makes each outside action fail in turn. Every
// row must park with a failure item that carries the command's own error, and
// must finish the ship once the error is gone and the daemon calls again,
// which is what "answering the item retries" means for a machine with no
// memory.
//
// Load-bearing rows: "merge" (a refused merge must never be read as merged)
// and "prompt" (the retry must not type the prompt a second time; the file
// already names the head, so the owner is told to point the session at it).
func TestAdvanceActionFailuresPark(t *testing.T) {
	green := func(w *world) { w.ci["c1"] = []Checks{ciGreen} }
	handBack := func(w *world) {
		w.ci["c1"] = []Checks{ciRed, ciRed}
		w.ci["c2"] = []Checks{ciGreen}
	}
	tests := []struct {
		name     string
		call     string
		setup    func(w *world)
		wantBody []string
		// errText replaces the default made-up error for a row that needs
		// git's real wording.
		errText string
		// owner is what the owner does before answering, beyond the error
		// going away.
		owner       func(w *world)
		wantPrompts int
	}{
		{name: "push", call: "Push", setup: green,
			wantBody: []string{"has commits the session does not have", "Pull them into the session's branch"}},
		{name: "push refused for workflow scope", call: "Push", setup: green,
			errText:  "! [remote rejected] t9-9 -> t9-9 (refusing to allow an OAuth App to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)",
			wantBody: []string{"gh auth refresh -s workflow", "git remote set-url origin"}},
		{name: "open pull request", call: "OpenPR", setup: green,
			wantBody: []string{"could not open a pull request", "main"}},
		{name: "re-run", call: "RerunFailed",
			setup:    func(w *world) { w.ci["c1"] = []Checks{ciRed, ciGreen} },
			wantBody: []string{"#7", "could not re-run"}},
		{name: "merge", call: "Merge", setup: green,
			wantBody: []string{"#7", "the merge was refused"}},
		{name: "base update", call: "SyncBase", setup: green,
			wantBody: []string{"checked out in another worktree", "local commits on main", "git pull --ff-only"}},
		{name: "delete remote branch", call: "DeleteRemote", setup: green,
			wantBody: []string{"could not delete the branch t9-9"}},
		{name: "hand-back file", call: "WriteFile",
			setup: func(w *world) {
				handBack(w)
				w.commitOnPrompt("c2")
			},
			wantBody: []string{testDir + "/" + HandBackFile}, wantPrompts: 1},
		{name: "prompt", call: "Prompt", setup: handBack,
			wantBody: []string{"could not be reached", "afk attach t9-9", HandBackFile, "does not type the prompt again"},
			// The owner points the session at the file, and it commits.
			owner: func(w *world) { w.commit("c2") }, wantPrompts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			tt.setup(w)
			boom := fmt.Errorf("boom in %s\nsecond line", tt.call)
			wantErr := []string{"boom in " + tt.call, "second line"}
			if tt.errText != "" {
				boom = errors.New(tt.errText)
				wantErr = []string{tt.errText}
			}
			w.errs[tt.call] = boom

			out := w.run()
			if out.Kind != OutcomePark {
				t.Fatalf("outcome = %q (%s), want park", out.Kind, out.Reason)
			}
			item := wantItem(t, out, ItemFailure)
			wantText(t, "the item body", item.Body, append(tt.wantBody, wantErr...)...)
			wantText(t, "the item title", item.Title, testTask)
			wantText(t, "the reason", out.Reason, wantErr[0])
			if tt.call == "SyncBase" && strings.Index(item.Body, "another worktree") > strings.Index(item.Body, "boom in") {
				t.Errorf("the likely cause comes after git's own message:\n%s", item.Body)
			}
			if out.Progress != w.saved {
				t.Errorf("a failed action changed the progress: %+v, saved %+v", out.Progress, w.saved)
			}
			if tt.call == "Merge" && (w.saved.State != CIGreen || w.pr.State != PRStateOpen) {
				t.Errorf("after a refused merge: state %q, pull request %s", w.saved.State, w.pr.State)
			}

			delete(w.errs, tt.call)
			if tt.owner != nil {
				tt.owner(w)
			}
			if done := w.run(); done.Kind != OutcomeDone {
				t.Fatalf("after the error went away: %q (%s), want done", done.Kind, done.Reason)
			}
			if w.prompts != tt.wantPrompts {
				t.Errorf("prompts = %d, want %d", w.prompts, tt.wantPrompts)
			}
			if w.prsOpened > 2 || w.syncsChanged != 1 {
				t.Errorf("pull requests opened = %d, base updates = %d", w.prsOpened, w.syncsChanged)
			}
		})
	}
}

// TestAdvanceParkItems checks the parks that come from what was read, not
// from a failed action: each must carry a failure item that tells the owner
// what to do, and must save, push, open and merge nothing.
//
// Load-bearing rows: "pull request missing" (never open a second pull
// request for the same work) and "no base" (never guess main, and make no
// call at all).
func TestAdvanceParkItems(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(w *world)
		want      []string
		wantReads func(reads int) bool
	}{
		{
			name:  "gh not ready",
			setup: func(w *world) { w.notReady = errors.New("gh: command not found") },
			want:  []string{"gh: command not found", "Install `gh` or run `gh auth login`"},
			// Ready is the only call: a missing tool cannot answer the rest.
			wantReads: func(n int) bool { return n == 1 },
		},
		{
			name:  "dirty worktree",
			setup: func(w *world) { w.dirty = true },
			want:  []string{"never committed", "afk attach t9-9"},
		},
		{
			name:  "nothing committed",
			setup: func(w *world) { w.nothing = true },
			want:  []string{"nothing to ship", "afk ledger set T9.9 done"},
		},
		{
			name: "pull request closed",
			setup: func(w *world) {
				w.at(PROpen)
				w.pr.State = PRStateClosed
			},
			want: []string{"#7 was closed without being merged", "Reopen it", "afk ledger set T9.9 failed"},
		},
		{
			name: "pull request missing",
			setup: func(w *world) {
				w.at(PROpen)
				w.prFound = false
			},
			want: []string{"#7 can no longer be found", "Reopen it", "afk ledger set T9.9 failed"},
		},
		{
			name: "pushed branch has commits the session does not",
			setup: func(w *world) {
				w.at(PROpen)
				w.ci["zz"] = []Checks{ciGreen}
				w.remote = "zz"
			},
			want: []string{"has commits the session does not have", "Pull them into the session's branch", "afk attach t9-9"},
		},
		{
			name: "branch gone from GitHub",
			setup: func(w *world) {
				w.at(PROpen)
				w.remote = ""
			},
			want: []string{"t9-9 was deleted on GitHub", "afk ledger set T9.9 failed"},
		},
		{
			name: "hand-back timed out",
			setup: func(w *world) {
				w.at(HandedBack)
				w.files[testDir+"/"+HandBackFile] = []byte("Pull request: #7\nHead commit: c1\n")
			},
			want: []string{"no new commit", "afk attach t9-9", "another hour", HandBackFile},
		},
		{
			name: "unknown saved state",
			setup: func(w *world) {
				w.at(PROpen)
				w.saved.State = "teleported"
			},
			want:      []string{`"teleported"`, "newer version of afk", "afk ledger set T9.9 failed"},
			wantReads: func(n int) bool { return n == 0 },
		},
		{
			name: "no base",
			setup: func(w *world) {
				w.base = ""
			},
			want:      []string{"will not guess", "[ship]", "base = ", "afk.toml"},
			wantReads: func(n int) bool { return n == 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			tt.setup(w)
			start := w.saved

			out := w.run()
			if out.Kind != OutcomePark {
				t.Fatalf("outcome = %q (%s), want park", out.Kind, out.Reason)
			}
			item := wantItem(t, out, ItemFailure)
			wantText(t, "the item", item.Title+"\n"+item.Body, append(tt.want, testTask)...)
			if w.saveCalls != 0 || w.saved != start {
				t.Errorf("a park saved: %d call(s), %+v -> %+v", w.saveCalls, start, w.saved)
			}
			if got := w.tally(); got != (tally{}) {
				t.Errorf("a park acted on the outside: %+v", got)
			}
			if tt.wantReads != nil && !tt.wantReads(w.reads) {
				t.Errorf("reads = %d", w.reads)
			}
			if tt.name == "hand-back timed out" {
				if waited := w.clock.Sub(start.At); waited <= HandBackTimeout {
					t.Errorf("parked after %s, want more than %s", waited, HandBackTimeout)
				}
			}
		})
	}
}

// TestAdvanceReadFailure walks one outage: a failed read waits with the error
// and saves nothing, a good read clears the record, and thirty minutes of
// nothing but failures park with the last error. The last step is the
// load-bearing one: after the park the record is cleared, so the owner's
// answer buys a fresh thirty minutes instead of an immediate second park.
func TestAdvanceReadFailure(t *testing.T) {
	w := newWorld(t).at(PROpen)
	w.ci["c1"] = []Checks{ciWait}
	w.errs["Checks"] = errors.New("HTTP 502 from api.github.com")
	began := w.clock

	for i := 0; i < 2; i++ {
		out, _ := w.step()
		if out.Kind != OutcomeWait || !strings.Contains(out.Reason, "HTTP 502") {
			t.Fatalf("failed read %d: %q (%s), want wait carrying the error", i+1, out.Kind, out.Reason)
		}
		if !w.failingSince.Equal(began) {
			t.Fatalf("failing since %s, want the first failure at %s", w.failingSince, began)
		}
	}

	delete(w.errs, "Checks")
	if out, _ := w.step(); out.Kind != OutcomeWait || !w.failingSince.IsZero() {
		t.Fatalf("after a good read: %q, failing since %s; want wait and no record", out.Kind, w.failingSince)
	}

	w.errs["Checks"] = errors.New("HTTP 502 from api.github.com")
	began = w.clock
	out := w.run()
	if out.Kind != OutcomePark {
		t.Fatalf("outcome = %q (%s), want park", out.Kind, out.Reason)
	}
	if lasted := w.clock.Sub(began); lasted < ReadFailureLimit {
		t.Errorf("parked after %s of failures, want at least %s", lasted, ReadFailureLimit)
	}
	item := wantItem(t, out, ItemFailure)
	wantText(t, "the item", item.Title+"\n"+item.Body,
		"GitHub could not be reached", testTask, "HTTP 502 from api.github.com", "gh auth status")
	if !w.failingSince.IsZero() {
		t.Errorf("the failure record survived the park (%s); an answered item would park again at once", w.failingSince)
	}
	if w.saveCalls != 0 || w.tally() != (tally{}) {
		t.Errorf("failed reads saved %d time(s) and acted: %+v", w.saveCalls, w.tally())
	}
	if next, _ := w.step(); next.Kind != OutcomeWait {
		t.Errorf("the call after the park: %q, want wait (a fresh thirty minutes)", next.Kind)
	}
}

// TestAdvancePollThrottle pins the rate at which GitHub is asked: two calls
// inside PollEvery make one set of reads. Without it a one-second daemon tick
// would spend the hourly API allowance in minutes.
func TestAdvancePollThrottle(t *testing.T) {
	call := func(w *world, at time.Time) Outcome {
		w.clock = at
		return Advance(w.request(), w.deps())
	}

	t.Run("a second call inside twenty seconds reads nothing", func(t *testing.T) {
		w := newWorld(t).at(PROpen)
		w.ci["c1"] = []Checks{ciWait}

		call(w, now)
		first := w.reads
		if first == 0 || !w.lastPoll.Equal(now) {
			t.Fatalf("first call: %d reads, last poll %s; want reads and a poll at %s", first, w.lastPoll, now)
		}
		out := call(w, now.Add(PollEvery-time.Second))
		if out.Kind != OutcomeWait || w.reads != first || !w.lastPoll.Equal(now) {
			t.Errorf("call inside the limit: %q, %d reads (want %d), last poll %s", out.Kind, w.reads, first, w.lastPoll)
		}
		call(w, now.Add(PollEvery))
		if w.reads != 2*first || !w.lastPoll.Equal(now.Add(PollEvery)) {
			t.Errorf("call at the limit: %d reads (want %d), last poll %s", w.reads, 2*first, w.lastPoll)
		}
	})

	// after_merge asks GitHub nothing, but it polls the health URL, so it is
	// held to the same rate.
	for _, state := range []State{Pushed, PROpen, HandedBack, CIGreen, Merged, AfterMerge} {
		t.Run("held: "+string(state), func(t *testing.T) {
			w := newWorld(t).at(state)
			w.cfg.AfterMerge = testCommand
			w.lastPoll = now.Add(-time.Second)
			if out := call(w, now); out.Kind != OutcomeWait || w.reads != 0 || w.saveCalls != 0 || w.runs != 0 {
				t.Errorf("%q, %d reads, %d saves, %d runs; want wait and no call", out.Kind, w.reads, w.saveCalls, w.runs)
			}
		})
	}

	t.Run("not held: committed pushes at once", func(t *testing.T) {
		w := newWorld(t)
		w.saved = Progress{State: Committed, At: now}
		w.lastPoll = now.Add(-time.Second)
		call(w, now)
		if w.pushes["c1"] != 1 || w.saved.State != Pushed {
			t.Errorf("pushes = %d, state = %q; want one push and pushed", w.pushes["c1"], w.saved.State)
		}
	})

	t.Run("a last poll in the future does not hold the ship", func(t *testing.T) {
		w := newWorld(t).at(PROpen)
		w.lastPoll = now.Add(time.Hour)
		call(w, now)
		if w.reads == 0 {
			t.Errorf("no read was made while the last poll is in the future")
		}
	})

	t.Run("a poll that cannot be recorded reads nothing", func(t *testing.T) {
		w := newWorld(t).at(PROpen)
		w.errs["MarkPoll"] = errors.New("read-only file system")
		out := call(w, now)
		if out.Kind != OutcomeWait || w.reads != 0 || !strings.Contains(out.Reason, "read-only file system") {
			t.Errorf("%q (%s), %d reads; want wait with the error and no read", out.Kind, out.Reason, w.reads)
		}
	})
}

// TestAdvanceWithoutGitOrGH runs the two states that follow the merge with
// nil Git and GH and no base: by then the session's worktree may be cleaned
// up, so a call to either would panic here and fail for real in the daemon.
func TestAdvanceWithoutGitOrGH(t *testing.T) {
	tests := []struct {
		name   string
		state  State
		marker bool
		want   OutcomeKind
	}{
		{"deploy_healthy is done", DeployHealthy, false, OutcomeDone},
		{"deploy_healthy is done even while another task's stop is up", DeployHealthy, true, OutcomeDone},
		{"stopped waits while the marker is there", Stopped, true, OutcomeWait},
		{"stopped is done once the marker is gone", Stopped, false, OutcomeDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t).at(tt.state)
			w.stopMarker = tt.marker
			w.base = ""
			w.lastPoll = w.clock // these states are not held to the poll limit
			d := w.deps()
			d.Git, d.GH = nil, nil

			out := Advance(w.request(), d)
			if out.Kind != tt.want {
				t.Errorf("outcome = %q (%s), want %q", out.Kind, out.Reason, tt.want)
			}
			if out.Item != nil {
				t.Errorf("unexpected item: %+v", *out.Item)
			}
			if w.reads != 0 || w.saveCalls != 0 || w.stops != 0 {
				t.Errorf("reads = %d, saves = %d, stops = %d; want none", w.reads, w.saveCalls, w.stops)
			}
		})
	}
}

// TestAdvanceAfterMergeWithoutGitOrGH runs a whole after_merge with nil Git
// and GH and no base, as TestAdvanceWithoutGitOrGH does for the two states
// after it: the session's worktree may be cleaned up before the daemon comes
// back to a ship in this state, and the command runs in the main checkout,
// which needs neither.
func TestAdvanceAfterMergeWithoutGitOrGH(t *testing.T) {
	w := newWorld(t).at(AfterMerge)
	w.cfg.AfterMerge = testCommand
	w.base = ""

	var out Outcome
	for i := 0; i < 10 && out.Kind != OutcomeDone; i++ {
		d := w.deps()
		d.Git, d.GH = nil, nil
		out = Advance(w.request(), d)
		w.clock = w.clock.Add(tick)
		if out.Kind == OutcomePark || out.Kind == OutcomeStop {
			t.Fatalf("call %d: %q (%s), want wait or done", i+1, out.Kind, out.Reason)
		}
	}
	if out.Kind != OutcomeDone || w.runs != 1 || w.healthCalls != 1 || w.saved.State != DeployHealthy {
		t.Errorf("outcome = %q, runs = %d, health calls = %d, state = %q; want done, 1, 1 and deploy_healthy",
			out.Kind, w.runs, w.healthCalls, w.saved.State)
	}
	if !w.failingSince.IsZero() || w.reads != w.healthCalls {
		t.Errorf("failing since %s, %d reads; want no read but the health call", w.failingSince, w.reads)
	}
}

// TestAdvanceAfterMergeMarks covers the two writes of the run record failing.
// Both rows are load-bearing, and they pull in opposite directions:
//
//   - A start mark that cannot be written must run nothing. A command that ran
//     with no start mark on disk would run a second time after a crash.
//   - A finish mark that cannot be written must not be tried again by running
//     the command again. The record still says "started", so the next call
//     stops the line: a result that was lost is read as an interrupted run.
//
// Neither saves the ship's progress, and neither moves Progress.At.
func TestAdvanceAfterMergeMarks(t *testing.T) {
	t.Run("the start mark cannot be written: nothing runs until it can", func(t *testing.T) {
		w := newWorld(t).at(AfterMerge)
		w.cfg.AfterMerge = testCommand
		w.failStart = true
		start := w.saved

		for i := 0; i < 3; i++ {
			out, _ := w.step()
			if out.Kind != OutcomeWait || !strings.Contains(out.Reason, "start mark: disk full") {
				t.Fatalf("call %d: %q (%s), want wait with the error", i+1, out.Kind, out.Reason)
			}
		}
		if w.runs != 0 || w.record != nil || w.saveCalls != 0 || w.saved != start {
			t.Fatalf("runs = %d, record = %+v, saves = %d; want nothing done", w.runs, w.record, w.saveCalls)
		}
		w.failStart = false
		if out := w.run(); out.Kind != OutcomeDone || w.runs != 1 {
			t.Errorf("after the error went away: %q, runs = %d; want done and one run", out.Kind, w.runs)
		}
	})

	t.Run("the finish mark cannot be written: the line stops, the command is not run again", func(t *testing.T) {
		w := newWorld(t).at(AfterMerge)
		w.cfg.AfterMerge = testCommand
		w.failFinish = true
		start := w.saved

		out, _ := w.step()
		if out.Kind != OutcomeWait || !strings.Contains(out.Reason, "finish mark: disk full") {
			t.Fatalf("%q (%s), want wait with the error", out.Kind, out.Reason)
		}
		if w.runs != 1 || w.record == nil || w.record.run.Finished || w.saveCalls != 0 || w.saved != start {
			t.Fatalf("runs = %d, record = %+v, saves = %d; want one run, a start mark and no save", w.runs, w.record, w.saveCalls)
		}
		w.failFinish = false
		out = w.run()
		if out.Kind != OutcomeStop || w.runs != 1 || w.healthCalls != 0 {
			t.Fatalf("next: %q (%s), runs = %d, health calls = %d; want stop, one run, no deploy check",
				out.Kind, out.Reason, w.runs, w.healthCalls)
		}
		wantText(t, "the item title", wantItem(t, out, ItemFailure).Title, "was interrupted")
	})

	t.Run("the command does not move the time the state was entered", func(t *testing.T) {
		w := newWorld(t).at(AfterMerge)
		w.cfg.AfterMerge = testCommand
		start := w.saved
		w.runTakes = 5 * time.Minute

		out, _ := w.step()
		if out.Kind != OutcomeWait || out.Progress != start || w.saved != start || w.saveCalls != 0 {
			t.Errorf("%q, progress %+v, saved %+v, %d saves; want wait and the progress untouched (%+v)",
				out.Kind, out.Progress, w.saved, w.saveCalls, start)
		}
		if strings.Contains(out.Reason, "hunter2") {
			t.Errorf("the reason quotes the command: %q", out.Reason)
		}
	})
}

// TestAdvanceStopFailures covers a stop whose marker cannot be written. It
// must not save "stopped" and must not return the stop outcome: Decide reads
// a saved "stopped" with no marker as the owner having cleared it, so saving
// here would mark a failed deploy done. (The other write of a stop failing,
// the save, is the "stopped" row of the crash table.)
func TestAdvanceStopFailures(t *testing.T) {
	t.Run("the marker cannot be written", func(t *testing.T) {
		w := newWorld(t).at(Merged)
		w.saved.At = w.clock.Add(-time.Hour)
		w.baseHas[w.saved.MergedSHA] = true
		w.remote = ""
		w.healthyOn = 0
		w.errs["Stop"] = errors.New("permission denied")

		out, _ := w.step()
		if out.Kind != OutcomeWait || !strings.Contains(out.Reason, "permission denied") {
			t.Fatalf("%q (%s), want wait with the error", out.Kind, out.Reason)
		}
		if w.saveCalls != 0 || w.stopMarker {
			t.Errorf("saved %d time(s), marker %v; want neither", w.saveCalls, w.stopMarker)
		}
		delete(w.errs, "Stop")
		if out := w.run(); out.Kind != OutcomeStop || !w.stopMarker || w.saved.State != Stopped {
			t.Errorf("after the error went away: %q, marker %v, state %q", out.Kind, w.stopMarker, w.saved.State)
		}
	})
}

// TestAdvanceCrashTable is the reason the ship is a saved state machine. For
// each script it first runs clean to learn the saves a full ship makes, then
// replays the script once per save in two ways:
//
//   - "save fails": the k-th Save returns an error. The action before it
//     happened and was not recorded, which is also what a kill between an
//     action and its save looks like to the next call.
//   - "killed after save": the k-th Save reaches the disk and the process
//     dies before anything else runs; the next call starts from that save.
//
// Every replay must still reach done, with each commit pushed once, one pull
// request, one merge, one base update, one prompt per hand-back, and the
// merge's time unmoved by the steps after it. The whole table is
// load-bearing: remove any "did it already happen" read from Decide or
// gather (remote head equals local, a pull request exists, the pull request
// is MERGED, the hand-back file names the head) and the replay at that save
// pushes, opens, merges or prompts twice.
//
// The two "stopped" rows of the deploy-timeout script are the stop-the-line
// crashes: a failed save after the marker stops again (two Stop calls, one
// outcome), and a kill after the save finds the marker and waits. In the
// second the stop outcome is never returned, which the table allows and
// issues.md records.
//
// The scripts with an after-merge command add one more thing every replay
// must hold: the command is started exactly once (the runs column; zero for
// the scripts with no command). The save replays already cover "killed before
// the command" (the kill after the save of after_merge: the next call finds
// no run record and runs it, for the first time). The command itself saves
// nothing, so its other two kill points cannot be reached by breaking a save
// and are replayed by name after the save loop:
//
//   - "killed while the command runs": the start mark is on disk and the end
//     is not. The line must stop, with one run. This row is the one that goes
//     red if Decide's "started but not finished" rule is removed: the next
//     call then reads the half-finished run as one that ended well and
//     carries on to the deploy check.
//   - "killed after the finish mark": the result is on disk and Advance never
//     returned. The next call must carry on to the deploy check, with one run.
//
// All three are load-bearing; they are the three crashes docs/PLAN.md T2.2a
// names.
func TestAdvanceCrashTable(t *testing.T) {
	withCommand := func(w *world) {
		w.ci["c1"] = []Checks{ciGreen}
		w.cfg.AfterMerge = testCommand
		w.cfg.AfterMergeTimeout = 10 * time.Minute
	}
	scripts := []struct {
		name    string
		setup   func(w *world)
		commits []string
		reruns  int
		prompts int
		// runs is how many times the after-merge command is started.
		runs  int
		final State
	}{
		{
			name:    "green",
			setup:   func(w *world) { w.ci["c1"] = []Checks{ciGreen} },
			commits: []string{"c1"},
			final:   DeployHealthy,
		},
		{
			name: "red, hand-back, green",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciRed, ciRed}
				w.ci["c2"] = []Checks{ciGreen}
				w.commitOnPrompt("c2")
			},
			commits: []string{"c1", "c2"},
			reruns:  1,
			prompts: 1,
			final:   DeployHealthy,
		},
		{
			name: "deploy timeout",
			setup: func(w *world) {
				w.ci["c1"] = []Checks{ciGreen}
				w.healthyOn = 0
			},
			commits: []string{"c1"},
			final:   Stopped,
		},
		{
			name:    "after-merge command",
			setup:   withCommand,
			commits: []string{"c1"},
			runs:    1,
			final:   DeployHealthy,
		},
		{
			name: "after-merge command fails",
			setup: func(w *world) {
				withCommand(w)
				w.runExit = 1
			},
			commits: []string{"c1"},
			runs:    1,
			final:   Stopped,
		},
		{
			name: "after-merge command, then deploy timeout",
			setup: func(w *world) {
				withCommand(w)
				w.healthyOn = 0
			},
			commits: []string{"c1"},
			runs:    1,
			final:   Stopped,
		},
	}
	const (
		clean = "clean"
		fails = "save fails"
		kill  = "killed after save"
	)

	for _, sc := range scripts {
		// drive runs to done. Any park fails the test, which is what keeps
		// a crash from ending in the sixty-minute hand-back park. The owner
		// clears a stopped line a few ticks after it stops.
		drive := func(t *testing.T, w *world) (stopOutcomes int) {
			t.Helper()
			stoppedFor := 0
			for i := 0; i < 500; i++ {
				out, killed := w.step()
				if w.stopMarker {
					if stoppedFor++; stoppedFor > 3 {
						w.stopMarker = false
					}
				}
				if killed {
					continue
				}
				switch out.Kind {
				case OutcomeDone:
					return stopOutcomes
				case OutcomePark:
					t.Fatalf("parked in %q: %s", w.saved.State, out.Reason)
				case OutcomeStop:
					stopOutcomes++
				}
			}
			t.Fatalf("did not reach done in 500 calls; state %q", w.saved.State)
			return 0
		}

		// target is the state whose save the replay breaks, "" for a clean
		// run and for a kill that is not at a save. final is the state the
		// ship must end in, which a kill inside the command changes.
		check := func(t *testing.T, w *world, mode string, target, final State, stopOutcomes int) {
			t.Helper()
			if w.runs != sc.runs {
				t.Errorf("the after-merge command was started %d time(s), want %d", w.runs, sc.runs)
			}
			if sc.runs == 0 && (w.record != nil || slices.Contains(w.statesSaved(), AfterMerge)) {
				t.Errorf("a ship with no command wrote a run record (%+v) or saved after_merge (%v)", w.record, w.statesSaved())
			}
			for _, sha := range sc.commits {
				if w.pushes[sha] != 1 {
					t.Errorf("commit %s was pushed %d times, want 1", sha, w.pushes[sha])
				}
			}
			if len(w.pushes) != len(sc.commits) {
				t.Errorf("pushed commits = %v, want only %v", w.pushes, sc.commits)
			}
			if w.prsOpened != 1 {
				t.Errorf("pull requests opened = %d, want 1", w.prsOpened)
			}
			if w.merges != 1 {
				t.Errorf("merges = %d, want 1", w.merges)
			}
			if w.syncsChanged != 1 || w.syncs != 1 {
				t.Errorf("base updates = %d (%d changed something), want 1", w.syncs, w.syncsChanged)
			}
			if w.remoteDeletes != 1 {
				t.Errorf("remote branch deletes = %d, want 1", w.remoteDeletes)
			}
			if w.prompts != sc.prompts {
				t.Errorf("prompts = %d, want %d (one per hand-back)", w.prompts, sc.prompts)
			}
			// A re-run whose save was lost is made again; it has no other
			// trace to read. It can happen once more, never twice more.
			maxReruns := sc.reruns
			if mode == fails && sc.reruns > 0 {
				maxReruns++
			}
			if w.reruns < sc.reruns || w.reruns > maxReruns {
				t.Errorf("re-runs = %d, want %d to %d", w.reruns, sc.reruns, maxReruns)
			}
			if w.saved.State != final || w.saved.MergedSHA == "" || w.saved.PR != testPR {
				t.Errorf("final progress = %+v, want %q with a merge commit and the pull request", w.saved, final)
			}

			// The merge's time is the time of the save that recorded the
			// merge, and no later save inside merged moves it.
			var mergeTime time.Time
			for i, p := range w.savedLog {
				if p.State != Merged {
					continue
				}
				if mergeTime.IsZero() {
					mergeTime = p.At
					if !p.At.Equal(w.savedAt[i]) {
						t.Errorf("merged was saved at %s with At %s", w.savedAt[i], p.At)
					}
				} else if !p.At.Equal(mergeTime) {
					t.Errorf("a later save in merged moved At from %s to %s", mergeTime, p.At)
				}
			}
			if mergeTime.IsZero() {
				t.Errorf("merged was never saved")
			}

			if final == Stopped {
				wantStops, wantOutcomes := 1, 1
				if target == Stopped && mode == fails {
					wantStops = 2
				}
				if target == Stopped && mode == kill {
					wantOutcomes = 0
				}
				if w.stops != wantStops || stopOutcomes != wantOutcomes {
					t.Errorf("stops = %d, stop outcomes = %d; want %d and %d", w.stops, stopOutcomes, wantStops, wantOutcomes)
				}
			} else if w.stops != 0 || stopOutcomes != 0 {
				t.Errorf("stops = %d, stop outcomes = %d; want none", w.stops, stopOutcomes)
			}
		}

		t.Run(sc.name, func(t *testing.T) {
			base := newWorld(t)
			sc.setup(base)
			check(t, base, clean, "", sc.final, drive(t, base))
			saves := append([]Progress(nil), base.savedLog...)
			if len(saves) < 7 {
				t.Fatalf("a clean run made %d saves; the table needs every state of a ship", len(saves))
			}

			for k := 1; k <= len(saves); k++ {
				for _, mode := range []string{fails, kill} {
					name := fmt.Sprintf("save %02d %s/%s", k, saves[k-1].State, mode)
					t.Run(name, func(t *testing.T) {
						w := newWorld(t)
						sc.setup(w)
						if mode == fails {
							w.failSaveAt = k
						} else {
							w.killAfter = k
						}
						check(t, w, mode, saves[k-1].State, sc.final, drive(t, w))
					})
				}
			}

			if sc.runs == 0 {
				return
			}
			entered := slices.IndexFunc(saves, func(p Progress) bool { return p.State == AfterMerge })
			if entered < 0 {
				t.Fatalf("a clean run with a command never saved after_merge: %v", base.statesSaved())
			}
			points := []struct {
				name  string
				arm   func(w *world)
				final State
				// deployChecked is whether the health URL may have been
				// asked by the end.
				deployChecked bool
			}{
				{"killed before the command", func(w *world) { w.killAfter = entered + 1 }, sc.final, true},
				{"killed while the command runs", func(w *world) { w.killInRun = true }, Stopped, false},
				{"killed after the finish mark", func(w *world) { w.killAtFinish = true }, sc.final, true},
			}
			for _, pt := range points {
				t.Run(pt.name, func(t *testing.T) {
					w := newWorld(t)
					sc.setup(w)
					pt.arm(w)
					check(t, w, pt.name, "", pt.final, drive(t, w))
					if w.killInRun || w.killAtFinish {
						t.Errorf("the kill never happened")
					}
					if !pt.deployChecked && w.healthCalls != 0 {
						t.Errorf("the deploy was checked %d time(s) after a run nobody saw the end of", w.healthCalls)
					}
					if pt.name == "killed while the command runs" {
						if rec := w.record; rec == nil || rec.run.Finished {
							t.Errorf("the run record = %+v, want the start mark left as it was", rec)
						}
						wantText(t, "the stop reason", w.stopReason, "not known whether the command finished")
					}
				})
			}
		})
	}
}
