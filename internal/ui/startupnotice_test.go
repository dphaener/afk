package ui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// startupNoticeText is a neutral stand-in. This package does not know what the
// caller's notice is about, and the test must not either.
const startupNoticeText = "Something changed since the last version. The details were printed in the terminal."

// startupNoticeModel builds a Model the way `afk dash` does, with the scan and
// listed seams wired (so the scan tick has real commands to issue) and a clock
// the test moves.
func startupNoticeModel(notice string, now *time.Time) Model {
	d := noSearchFolders(testDeps())
	d, _ = listedSeam(d)
	d.Now = func() time.Time { return *now }
	d.ActiveProjectIndex = -1
	d.StartupNotice = notice
	m := New("/elsewhere", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	return m
}

// TestStartupNoticeIsOneToastAtStart pins the once-per-run rule from the
// Model's side.
//
// Load-bearing rows:
//   - "a notice": the toast is up with the caller's exact text as soon as the
//     Model exists and is still the same single toast after Init, and no later
//     message brings it back. The status is cleared and the clock moved past
//     the toast's life before every later message, so what is being tested is
//     that nothing RAISES the notice again; without the clear, notify's own
//     same-text skip would hide a second raise.
//   - "no notice": an empty StartupNotice raises nothing at all, at start or
//     later. This is the row a machine without the old settings file lives on.
func TestStartupNoticeIsOneToastAtStart(t *testing.T) {
	tests := []struct {
		name   string
		notice string
	}{
		{name: "a notice", notice: startupNoticeText},
		{name: "no notice", notice: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(1_000_000, 0)
			m := startupNoticeModel(tt.notice, &now)

			if m.Init() == nil {
				t.Fatal("Init returned no Cmd")
			}
			if m.status != tt.notice {
				t.Fatalf("status after Init = %q, want %q", m.status, tt.notice)
			}
			if len(m.pendingBackground) != 0 {
				t.Fatalf("pendingBackground = %v, want nothing left waiting", m.pendingBackground)
			}
			if tt.notice == "" {
				if m.toastVisible() {
					t.Fatal("a toast is visible with no notice set")
				}
			} else {
				if !m.toastVisible() {
					t.Fatal("the notice is not visible as a toast at start")
				}
				if m.statusKind != notifyWarn {
					t.Errorf("kind = %v, want a warning: nothing failed, there is something to do", m.statusKind)
				}
			}
			raisedAt := m.statusChangedAt

			// Everything a running dashboard receives on its timers, several
			// rounds of it.
			later := []tea.Msg{
				scanTickMsg(now),
				projectScanMsg{configPath: listedConfigPath, interval: time.Minute},
				listedProjectsMsg{path: listedConfigPath},
				tea.WindowSizeMsg{Width: 120, Height: 40},
			}
			for round := 0; round < 3; round++ {
				for _, msg := range later {
					now = now.Add(toastTTL + time.Second)
					m.clearStatus()
					m, _ = update(t, m, msg)
					if m.status != "" {
						t.Fatalf("round %d, %T: status = %q, want nothing raised after the start", round, msg, m.status)
					}
					if len(m.pendingBackground) != 0 {
						t.Fatalf("round %d, %T: pendingBackground = %v, want empty", round, msg, m.pendingBackground)
					}
				}
			}
			if !m.statusChangedAt.Equal(raisedAt) {
				t.Errorf("the status was stamped again at %v, after the start at %v", m.statusChangedAt, raisedAt)
			}
		})
	}
}

// TestStartupNoticeDoesNotSwallowAnotherNotice is why raiseStartupNotice
// flushes at once. Two background events flushed together collapse into counts
// and lose both texts, and the listed-read error is raised only once per
// distinct text, so it would never be shown again. Here the notice is already
// up when the error arrives: the error waits, then lands in its own words.
func TestStartupNoticeDoesNotSwallowAnotherNotice(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := startupNoticeModel(startupNoticeText, &now)

	fail := listedProjectsMsg{path: listedConfigPath, err: errors.New(listedConfigPath + ": line 2: bad key")}
	m, _ = update(t, m, fail)
	if m.status != startupNoticeText {
		t.Fatalf("status = %q, want the start-up notice still up while it is live", m.status)
	}

	now = now.Add(toastTTL + time.Second)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	want := "Could not read the project list: " + listedConfigPath + ": line 2: bad key. The switcher keeps the projects it had."
	if m.status != want || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want the waiting error in its own words: %q", m.status, m.statusKind, want)
	}
}
