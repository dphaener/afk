package tokens

import (
	"sort"
	"strconv"
)

// Summary is one task's token record as numbers rather than text: the total,
// each phase's part of it and each role's counts. The web page draws a split
// bar from Phases and a role table from Roles, and both must agree with
// `afk tokens <task>`, so Summarize is built on the same tableRows the
// command's renderers print instead of a second tally.
type Summary struct {
	// Total is the weighted cost of every line.
	Total  float64
	Phases []PhaseTotal
	Roles  []RoleTotal
}

// PhaseTotal is one phase's weighted cost and its share of the total.
type PhaseTotal struct {
	Phase    string
	Weighted float64
	// Share is a percentage (0 to 100) rounded to one decimal, the same
	// rounding the command's share column uses, so the bar and the table show
	// the same figure.
	Share float64
}

// RoleTotal is one role's counts summed over phases and models.
type RoleTotal struct {
	Role                                        string
	Calls, Input, Output, CacheWrite, CacheRead int64
	Weighted                                    float64
}

// Summarize totals lines by phase and by role. Phases follow the command's
// order (plan, execute, review, fix, ship, then any other alphabetically);
// roles come heaviest first, ties by name. No lines give the zero Summary.
func Summarize(lines []Line) Summary {
	rows, total := tableRows(lines)
	var s Summary
	s.Total = total
	roles := map[string]*RoleTotal{}
	for _, r := range rows {
		switch r.role {
		case "(subtotal)":
			s.Phases = append(s.Phases, PhaseTotal{Phase: r.phase, Weighted: r.weighted, Share: share(r.weighted, total)})
		case "":
			// The total row.
		default:
			t := roles[r.role]
			if t == nil {
				t = &RoleTotal{Role: r.role}
				roles[r.role] = t
			}
			t.Calls += r.calls
			t.Input += r.in
			t.Output += r.out
			t.CacheWrite += r.cw
			t.CacheRead += r.cr
			t.Weighted += r.weighted
		}
	}
	for _, t := range roles {
		s.Roles = append(s.Roles, *t)
	}
	sort.Slice(s.Roles, func(i, j int) bool {
		if s.Roles[i].Weighted != s.Roles[j].Weighted {
			return s.Roles[i].Weighted > s.Roles[j].Weighted
		}
		return s.Roles[i].Role < s.Roles[j].Role
	})
	return s
}

// share is pct's number: the part's percentage of the total at one decimal.
// It goes through the same formatting so the two can never round differently.
func share(part, total float64) float64 {
	v, _ := strconv.ParseFloat(pct(part, total)[:len(pct(part, total))-1], 64)
	return v
}
