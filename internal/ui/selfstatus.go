package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/afkstatus"
)

// reportSelfStatus builds afk's own Program status records (T17.5) from the
// newest project rows and returns a command that writes only what changed
// since the last report, or nil when nothing did.
//
// It uses tea.Raw because tea.View has no field for OSC 7501: the view
// declares the title and tab progress, and a status record is neither. Raw
// bytes go straight to the terminal, outside the frame.
//
// The clear on exit is NOT sent from here: a Cmd returned beside tea.Quit may
// never run, so dash.go writes it after the program returns.
//
// loadFailed keeps the last report: a failed config read says nothing about
// the projects, and telling the terminal "nothing needs you" would be a lie.
func (m Model) reportSelfStatus(loadFailed bool) (Model, tea.Cmd) {
	if loadFailed {
		return m, nil
	}
	next := afkstatus.Records(m.everyProject.Rows)
	out := afkstatus.Diff(m.lastReported, next)
	m.lastReported = next
	if out == nil {
		return m, nil
	}
	return m, tea.Raw(string(out))
}
