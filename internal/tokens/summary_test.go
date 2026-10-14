package tokens

import (
	"math"
	"reflect"
	"testing"
)

// TestSummarize pins the numbers the web page draws.
//
// Load-bearing rows: "full record" (phases in the command's order, roles
// heaviest first, shares summing to 100 within rounding, roles merged across
// phases), "unknown phase" (sorts after the known ones, as the command's
// subtotals do) and "empty" (no lines must give the zero Summary, not a
// division by zero).
func TestSummarize(t *testing.T) {
	full := []Line{
		{Phase: "review", Role: "verifier", Calls: 1, Input: 50, Output: 10, Weighted: 100},
		{Phase: "execute", Role: "task", Calls: 3, Input: 1000, Output: 200, CacheWrite: 400, CacheRead: 5000, Weighted: 3000},
		{Phase: "plan", Role: "planner", Calls: 2, Input: 10, Weighted: 300},
		{Phase: "fix", Role: "task", Calls: 1, Input: 5, Weighted: 600},
	}
	cases := []struct {
		name       string
		lines      []Line
		total      float64
		phases     []string
		roles      []string
		wantShares []float64
	}{
		{name: "full record", lines: full, total: 4000,
			phases: []string{"plan", "execute", "review", "fix"}, roles: []string{"task", "planner", "verifier"},
			wantShares: []float64{7.5, 75, 2.5, 15}},
		{name: "one phase only", lines: full[1:2], total: 3000,
			phases: []string{"execute"}, roles: []string{"task"}, wantShares: []float64{100}},
		{name: "empty", lines: nil},
		{name: "unknown phase", lines: []Line{
			{Phase: "zeta", Role: "a", Weighted: 1}, {Phase: "alpha", Role: "b", Weighted: 1}, {Phase: "ship", Role: "c", Weighted: 2}},
			total: 4, phases: []string{"ship", "alpha", "zeta"}, roles: []string{"c", "a", "b"}, wantShares: []float64{50, 25, 25}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Summarize(c.lines)
			if s.Total != c.total {
				t.Fatalf("Total = %v, want %v", s.Total, c.total)
			}
			var phases, roles []string
			var shares []float64
			var sum float64
			for _, p := range s.Phases {
				phases = append(phases, p.Phase)
				shares = append(shares, p.Share)
				sum += p.Share
			}
			for _, r := range s.Roles {
				roles = append(roles, r.Role)
			}
			if !reflect.DeepEqual(phases, c.phases) || !reflect.DeepEqual(roles, c.roles) || !reflect.DeepEqual(shares, c.wantShares) {
				t.Fatalf("phases %v roles %v shares %v", phases, roles, shares)
			}
			if len(c.lines) > 0 && math.Abs(sum-100) > 0.5 {
				t.Fatalf("shares sum to %v", sum)
			}
		})
	}
}

// TestSummarizeRoleCounts: a role that ran in two phases is one row with the
// counts added, and the share matches the command's pct for an awkward ratio.
func TestSummarizeRoleCounts(t *testing.T) {
	s := Summarize([]Line{
		{Phase: "execute", Role: "task", Calls: 3, Input: 1, Output: 2, CacheWrite: 3, CacheRead: 4, Weighted: 2},
		{Phase: "fix", Role: "task", Calls: 1, Input: 10, Output: 20, CacheWrite: 30, CacheRead: 40, Weighted: 1},
	})
	want := RoleTotal{Role: "task", Calls: 4, Input: 11, Output: 22, CacheWrite: 33, CacheRead: 44, Weighted: 3}
	if len(s.Roles) != 1 || s.Roles[0] != want {
		t.Fatalf("roles = %+v", s.Roles)
	}
	if s.Phases[0].Share != 66.7 || s.Phases[1].Share != 33.3 {
		t.Fatalf("shares = %+v", s.Phases)
	}
}
