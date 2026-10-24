// Package afkstatus turns the all-projects rows into the Program status
// records (OSC 7501) that afk reports about itself, and works out which of
// them to send when the rows change.
//
// It exists for T17.5: `afk dash` and a foreground `afk daemon` both tell
// their terminal what needs the owner, so the mapping from rows to records
// and the send-on-change diff live in one place both can call. It reads
// nothing: no file, clock or goroutine. The caller builds the rows, keeps the
// last records it sent and writes the bytes Diff returns.
package afkstatus

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/projectrows"
)

// appName is the app key on every record afk sends.
const appName = "afk"

// Records builds the root record (ID "") followed by one child per row, in
// the order the rows came in. The root says what the whole machine needs
// from the owner, by this precedence: something needs you (blocked, kind
// question), then a stopped, gone or unreadable project (error), then a
// running task (working), then idle. A child follows the same precedence for
// its one row.
//
// Every Msg and Title has control characters replaced by a space and is cut
// to the spec's limit, and every id is one Encode accepts, so Encode never
// refuses a record built here.
func Records(rows []projectrows.Row) []progstatus.Record {
	ids := childIDs(rows)
	root := rootRecord(rows)
	out := make([]progstatus.Record, 0, len(rows)+1)
	out = append(out, root)
	for i, r := range rows {
		out = append(out, childRecord(ids[i], r))
	}
	return out
}

// Diff returns the bytes to write so a terminal that holds prev ends up
// holding next: a clear for each child in prev that next no longer has, then
// every record in next that is new or differs from the one with the same ID
// in prev, root first. Equal sets give nil. A record Encode refuses is
// skipped; that cannot happen for the output of Records, and skipping beats
// writing a sequence the terminal would drop anyway.
func Diff(prev, next []progstatus.Record) []byte {
	old := make(map[string]progstatus.Record, len(prev))
	for _, r := range prev {
		old[r.ID] = r
	}
	have := make(map[string]bool, len(next))
	for _, r := range next {
		have[r.ID] = true
	}
	var out []byte
	for _, r := range prev {
		if r.ID == "" || have[r.ID] {
			continue
		}
		if b, err := progstatus.Encode(progstatus.Record{State: progstatus.StateClear, ID: r.ID}); err == nil {
			out = append(out, b...)
		}
	}
	for _, r := range next {
		if p, ok := old[r.ID]; ok && p == r {
			continue
		}
		if b, err := progstatus.Encode(r); err == nil {
			out = append(out, b...)
		}
	}
	return out
}

// ClearAll is the root clear. The spec says it removes every record beneath
// the root too, so a program that is exiting writes this one sequence.
func ClearAll() []byte {
	b, err := progstatus.Encode(progstatus.Record{State: progstatus.StateClear})
	if err != nil {
		return nil
	}
	return b
}

// rowView is what one row contributes: its state, the pieces that say why,
// and how many things it asks of the owner.
type rowView struct {
	state progstatus.State
	kind  progstatus.Kind
	parts []string
	needs int
}

func view(r projectrows.Row) rowView {
	var v rowView
	if r.Blocking > 0 {
		p := firstLine(r.Wait.Title)
		if p == "" {
			p = "an inbox item"
		}
		if r.Blocking > 1 {
			p += " and " + strconv.Itoa(r.Blocking-1) + " more"
		}
		v.parts = append(v.parts, p)
		v.needs += r.Blocking
	}
	if r.PlanWaiting {
		v.parts = append(v.parts, "a plan to approve")
		v.needs++
	}
	if r.State == projectrows.StateWaiting && r.Blocking == 0 {
		if d := firstLine(r.Detail); d != "" {
			v.parts = append(v.parts, d)
		} else {
			v.parts = append(v.parts, "waiting on you")
		}
		v.needs++
	}
	switch {
	case v.needs > 0:
		v.state, v.kind = progstatus.StateBlocked, progstatus.KindQuestion
	case r.State == projectrows.StateStopped, r.State == projectrows.StateGone, r.State == projectrows.StateUnreadable:
		v.state = progstatus.StateError
		v.parts = []string{firstLine(r.Detail)}
	case r.State == projectrows.StateRunning:
		v.state = progstatus.StateWorking
		v.parts = []string{runningText(r)}
	default:
		v.state = progstatus.StateIdle
		v.parts = []string{r.Label}
	}
	return v
}

func runningText(r projectrows.Row) string {
	s := strings.TrimSpace(strings.TrimSpace(firstLine(r.Task) + " " + firstLine(r.Title)))
	if p := firstLine(r.Phase); p != "" {
		if s != "" {
			s += " · "
		}
		s += p
	}
	return s
}

func rootRecord(rows []projectrows.Row) progstatus.Record {
	rec := progstatus.Record{App: appName}
	views := make([]rowView, len(rows))
	needs := 0
	for i, r := range rows {
		views[i] = view(r)
		needs += views[i].needs
	}
	named := func(i int, parts []string) string {
		p := strings.Join(parts, "; ")
		if p == "" {
			return rows[i].Name
		}
		return rows[i].Name + ": " + p
	}
	var segs []string
	switch {
	case needs > 0:
		rec.State, rec.Kind = progstatus.StateBlocked, progstatus.KindQuestion
		for i, v := range views {
			if v.needs > 0 {
				for _, p := range v.parts {
					segs = append(segs, rows[i].Name+": "+p)
				}
			}
		}
		head := "1 thing needs you: "
		if needs > 1 {
			head = strconv.Itoa(needs) + " things need you: "
		}
		rec.Msg = head + strings.Join(segs, "; ")
	case anyState(views, progstatus.StateError):
		rec.State = progstatus.StateError
		for i, v := range views {
			if v.state == progstatus.StateError {
				segs = append(segs, named(i, v.parts))
			}
		}
		rec.Msg = strings.Join(segs, "; ")
	case anyState(views, progstatus.StateWorking):
		rec.State = progstatus.StateWorking
		for i, v := range views {
			if v.state == progstatus.StateWorking {
				segs = append(segs, named(i, v.parts))
			}
		}
		rec.Msg = strings.Join(segs, "; ")
	default:
		rec.State = progstatus.StateIdle
		for _, r := range rows {
			switch r.State {
			case projectrows.StatePaused:
				segs = append(segs, r.Name+" paused")
			case projectrows.StateHeld:
				segs = append(segs, r.Name+" held for usage")
			}
		}
		rec.Msg = "Nothing running"
		if len(segs) > 0 {
			rec.Msg = strings.Join(segs, "; ")
		}
	}
	rec.Msg = clean(rec.Msg, progstatus.MaxMsgDecoded)
	return rec
}

func anyState(vs []rowView, s progstatus.State) bool {
	for _, v := range vs {
		if v.state == s {
			return true
		}
	}
	return false
}

func childRecord(id string, r projectrows.Row) progstatus.Record {
	v := view(r)
	return progstatus.Record{
		State: v.state,
		ID:    id,
		Kind:  v.kind,
		App:   appName,
		Title: clean(r.Name, progstatus.MaxTitleDecoded),
		Msg:   clean(strings.Join(v.parts, "; "), progstatus.MaxMsgDecoded),
	}
}

// childIDs gives each row an id. Encode refuses any id outside
// [A-Za-z0-9_.+-] segments of at most 32 bytes, and a project name can be any
// folder name, so the id is a lowercased, sanitized, cut copy and the record's
// Title carries the real name. Names that sanitize alike get -2, -3 and so on.
func childIDs(rows []projectrows.Row) []string {
	used := make(map[string]bool, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		base := idBase(r.Name)
		id := base
		for n := 2; used[id]; n++ {
			suffix := "-" + strconv.Itoa(n)
			b := base
			if len(b)+len(suffix) > progstatus.MaxSegment {
				b = b[:progstatus.MaxSegment-len(suffix)]
			}
			id = b + suffix
		}
		used[id] = true
		ids[i] = id
	}
	return ids
}

func idBase(name string) string {
	b := []byte(strings.ToLower(name))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '+' || c == '-') {
			b[i] = '-'
		}
	}
	if len(b) > progstatus.MaxSegment {
		b = b[:progstatus.MaxSegment]
	}
	if len(b) == 0 {
		return "project"
	}
	return string(b)
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(first)
}

// clean replaces control characters with a space and cuts s to max bytes on
// a rune boundary. Invalid UTF-8 becomes U+FFFD, which Encode accepts.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
