package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/ui"
)

const dashStopJSON = `{"at":"2026-10-11T00:00:00Z","task":"T0.1","reason":"deploy did not come up"}`

func blockAfkFolder(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".afk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDashPause runs the dashboard's pause wrapper on a real temp project.
//
// Load-bearing rows: "fresh" must leave the flag and exactly one pause line,
// the same as `afk pause`; "already paused" must report already and still log
// (the command does); "folder cannot be written" must return an error naming
// the reason and the folder, and leave no flag.
func TestDashPause(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		prepause    bool
		blockAfk    bool
		wantAlready bool
		wantErr     bool
		wantKinds   []string
	}{
		{name: "fresh", wantKinds: []string{"pause"}},
		{name: "already paused", prepause: true, wantAlready: true, wantKinds: []string{"pause"}},
		{name: "folder cannot be written", blockAfk: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.prepause {
				if err := daemonstate.Pause(root, now.Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if tt.blockAfk {
				blockAfkFolder(t, root)
			}
			already, err := dashPauseAt(root, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if already != tt.wantAlready {
				t.Errorf("already = %v, want %v", already, tt.wantAlready)
			}
			if tt.wantErr {
				if daemonstate.Paused(root) {
					t.Error("flag written despite error")
				}
				if !strings.Contains(err.Error(), "Check that afk can write to") || !strings.Contains(err.Error(), filepath.Join(root, ".afk", "build")) {
					t.Errorf("error %q does not name the folder to check", err)
				}
				return
			}
			if !daemonstate.Paused(root) {
				t.Error("no pause flag")
			}
			if got := logKinds(t, root); strings.Join(got, ",") != strings.Join(tt.wantKinds, ",") {
				t.Errorf("log kinds = %v, want %v", got, tt.wantKinds)
			}
		})
	}
}

// TestDashResume runs the dashboard's resume wrapper on a real temp project.
//
// Load-bearing rows: "stop marker only" carries the task name out (the
// dashboard's notice names it) and logs stop_cleared; "nothing to resume"
// gives the zero result and no log line; "folder cannot be written" returns
// an error.
func TestDashResume(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		paused    bool
		stop      string
		blockAfk  bool
		want      ui.ResumeResult
		wantErr   bool
		wantKinds []string
	}{
		{name: "paused", paused: true, want: ui.ResumeResult{WasPaused: true}, wantKinds: []string{"resume"}},
		{name: "stop marker only", stop: dashStopJSON,
			want: ui.ResumeResult{WasStopped: true, StopTask: "T0.1"}, wantKinds: []string{"stop_cleared"}},
		{name: "paused and stopped", paused: true, stop: dashStopJSON,
			want: ui.ResumeResult{WasPaused: true, WasStopped: true, StopTask: "T0.1"}, wantKinds: []string{"resume", "stop_cleared"}},
		{name: "nothing to resume"},
		{name: "folder cannot be written", blockAfk: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.paused {
				if err := daemonstate.Pause(root, now); err != nil {
					t.Fatal(err)
				}
			}
			if tt.stop != "" {
				if err := os.MkdirAll(filepath.Dir(daemonstate.StopPath(root)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(daemonstate.StopPath(root), []byte(tt.stop), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.blockAfk {
				blockAfkFolder(t, root)
			}
			got, err := dashResumeAt(root, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), "Check that afk can write to") {
					t.Errorf("error %q does not name the folder to check", err)
				}
				return
			}
			if daemonstate.Paused(root) || daemonstate.Stopped(root) {
				t.Error("a flag file is left behind")
			}
			if got := logKinds(t, root); strings.Join(got, ",") != strings.Join(tt.wantKinds, ",") {
				t.Errorf("log kinds = %v, want %v", got, tt.wantKinds)
			}
		})
	}
}

// TestDashPauseResumeNeverWriteTheLedger holds the Must NOT: pausing and
// resuming change the flag, the marker and the log, never the ledger.
func TestDashPauseResumeNeverWriteTheLedger(t *testing.T) {
	root := t.TempDir()
	ledgerPath := filepath.Join(root, ".afk", "build", "ledger.json")
	if _, err := dashPauseBuild(root); err != nil {
		t.Fatal(err)
	}
	if _, err := dashResumeBuild(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Errorf("ledger.json exists or cannot be checked: %v", err)
	}
}

// TestDashPauseErrorWording pins the dashboard's error text for each stage.
//
// Load-bearing: the three log rows say the change WAS made, so the owner does
// not press the key again; the two file rows say which folder to check.
func TestDashPauseErrorWording(t *testing.T) {
	cause := errors.New("open x: permission denied")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "flag not written", err: &pauseError{stage: stagePauseFlag, err: cause},
			want: []string{"permission denied", "Check that afk can write to", "/p/.afk/build"}},
		{name: "stop marker not removed", err: &pauseError{stage: stageStopMarker, err: cause},
			want: []string{"permission denied", "Check that afk can write to", "/p/.afk/build"}},
		{name: "pause log failed", err: &pauseError{stage: stageLogPause, err: cause},
			want: []string{"the project is paused, but the decision log could not be written", "permission denied"}},
		{name: "resume log failed", err: &pauseError{stage: stageLogResume, err: cause},
			want: []string{"the project is resumed, but the decision log could not be written", "permission denied"}},
		{name: "stop-cleared log failed", err: &pauseError{stage: stageLogStopCleared, err: cause},
			want: []string{"the stopped line is cleared, but the decision log could not be written", "permission denied"}},
		{name: "other error", err: cause, want: []string{"permission denied"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashPauseError("/p", tt.err)
			if got == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(got.Error(), w) {
					t.Errorf("%q lacks %q", got.Error(), w)
				}
			}
		})
	}
}

// TestDashPauseErrorReasonFirst pins that a failed flag write leads with the
// reason and not the temp file's long path, which the dashboard's narrow
// notice box cuts off. Load-bearing: the 2026-10-13 walkthrough showed
// "permission denied" never appearing.
func TestDashPauseErrorReasonFirst(t *testing.T) {
	pathErr := &fs.PathError{Op: "open", Path: "/p/.afk/build/.paused.tmp-1", Err: syscall.EACCES}
	got := dashPauseError("/p", &pauseError{stage: stagePauseFlag, err: pathErr})
	if got == nil {
		t.Fatal("no error")
	}
	want := "permission denied. Check that afk can write to /p/.afk/build"
	if got.Error() != want {
		t.Errorf("got %q, want %q", got.Error(), want)
	}
}
