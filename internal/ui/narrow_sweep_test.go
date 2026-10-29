package ui

import (
	"fmt"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// TestNarrowSweep renders every control-room view at windows narrower than 80
// columns and short as 8 rows, and fails on a panic, a line wider than the
// window, or a frame taller than the window. Every golden is 80x24 or 120x40,
// so nothing else covers these sizes; a phone-width terminal is below 80, and
// a frame that overflows there wraps in the terminal and shreds the screen.
//
// Load-bearing rows: every row is, because each view draws its own box with its
// own width rules (the home screen's inline 100-column threshold, the
// overlays' modalContentDims floor, the Agent's pane). The widths and heights
// are fixtures that reach the corners, not any device's measured size. No
// golden is written: the check is the three bounds, not a pixel-for-pixel
// frame.
func TestNarrowSweep(t *testing.T) {
	views := []struct {
		name  string
		setup func(t *testing.T, w, h int) Model
	}{
		{"allprojects", func(t *testing.T, w, h int) Model {
			m := openAllProjectsGolden(t, w, h, AllProjects{
				Rows:       allProjectsFixture(),
				ConfigPath: "/home/owner/.config/afk/config.toml",
			})
			m.build, m.buildKnown = goldenUsage(), true
			return m
		}},
		{"project-sessions", func(t *testing.T, w, h int) Model {
			m := sweepProject(t, w, h)
			m, _ = update(t, m, keyRune('1'))
			return m
		}},
		{"project-build", func(t *testing.T, w, h int) Model {
			m := sweepProject(t, w, h)
			m, _ = update(t, m, keyRune('2'))
			return m
		}},
		{"project-inbox", func(t *testing.T, w, h int) Model {
			m := sweepProject(t, w, h)
			m, _ = update(t, m, keyRune('3'))
			return m
		}},
		{"agent", func(t *testing.T, w, h int) Model {
			m := goldenModel(t, w, h)
			s, _ := m.selected()
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("agent output"), name: s.Name})
			m, _ = update(t, m, keyEnter)
			m.clearStatus()
			return m
		}},
		{"details", func(t *testing.T, w, h int) Model {
			m, _ := update(t, goldenModel(t, w, h), keyRune('d'))
			return m
		}},
		{"inbox", func(t *testing.T, w, h int) Model {
			m := goldenModel(t, w, h)
			m, _ = update(t, m, buildLoadedMsg{snap: goldenInbox(), gen: m.projectGen})
			m = pressKey(t, m, keyRune('i'))
			return m
		}},
		{"answer", func(t *testing.T, w, h int) Model {
			m := goldenModel(t, w, h)
			m, _ = update(t, m, buildLoadedMsg{snap: goldenInbox(), gen: m.projectGen})
			m = pressKey(t, m, keyRune('i'))
			m, _ = update(t, m, keyEnter)
			m, _ = update(t, m, keyRune('2'))
			return m
		}},
		{"keys", func(t *testing.T, w, h int) Model {
			m, _ := update(t, goldenModel(t, w, h), keyRune('?'))
			return m
		}},
		{"actions", func(t *testing.T, w, h int) Model {
			m, _ := update(t, goldenModel(t, w, h), keyRune(':'))
			return m
		}},
		{"jump", func(t *testing.T, w, h int) Model {
			m, cmd := update(t, goldenModel(t, w, h), keyRune('/'))
			return pumpForm(t, m, cmd, 0)
		}},
	}
	for _, v := range views {
		t.Run(v.name, func(t *testing.T) {
			for _, w := range []int{20, 30, 40, 50, 60, 79} {
				for _, h := range []int{8, 12, 24} {
					t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
						defer func() {
							if r := recover(); r != nil {
								t.Errorf("%s at %dx%d panicked: %v", v.name, w, h, r)
							}
						}()
						frame := render(v.setup(t, w, h))
						lines := strings.Split(frame, "\n")
						if len(lines) > h {
							t.Errorf("%s at %dx%d: frame is %d lines, window is %d", v.name, w, h, len(lines), h)
						}
						for i, line := range lines {
							if got := xansi.StringWidth(line); got > w {
								t.Errorf("%s at %dx%d: line %d is %d cells wide, window is %d: %q", v.name, w, h, i+1, got, w, line)
								break
							}
						}
					})
				}
			}
		})
	}
}

// sweepProject is the Project view as TestGoldenProjectView opens it: the
// build and inbox read in, feat-auth selected, any toast cleared.
func sweepProject(t *testing.T, w, h int) Model {
	t.Helper()
	m := goldenModel(t, w, h)
	snap := goldenInbox()
	snap.Rows[1].Session = "feat-auth"
	m, _ = update(t, m, buildLoadedMsg{snap: snap, gen: m.projectGen})
	m.clearStatus()
	return m
}
