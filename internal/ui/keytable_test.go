package ui

import (
	"fmt"
	"testing"
)

// TestKeyTable holds the key table's shape. Every check is load-bearing:
//
//   - a row with no name cannot be found by `:`, and one with no help shows
//     a blank line in `?` and `:`;
//   - two rows on one key at one level would make the key mean whichever
//     comes first, silently (keyRowFor stops at the first);
//   - two rows with one name at one level would show `:` a duplicate;
//   - a row at no level can never run;
//   - a by-name row with other keys would answer to a key it does not show.
func TestKeyTable(t *testing.T) {
	levels := []level{levelAllProjects, levelProject, levelAgent}
	for i, r := range keyTable {
		t.Run(fmt.Sprintf("row %d %s", i, r.name), func(t *testing.T) {
			if r.name == "" {
				t.Errorf("row %d (key %q) has no name", i, r.key)
			}
			if r.help == "" {
				t.Errorf("row %q has no help line", r.name)
			}
			if r.levels == 0 || r.levels&^atEveryLevel != 0 {
				t.Errorf("row %q levels = %b, want a non-empty subset of %b", r.name, r.levels, atEveryLevel)
			}
			if r.key == "" && (len(r.also) > 0 || r.label != "") {
				t.Errorf("by-name row %q has other keys %v or a label %q", r.name, r.also, r.label)
			}
		})
	}
	for _, lv := range levels {
		t.Run("keys unique at "+lv.String(), func(t *testing.T) {
			seen := map[string]string{}
			for _, r := range keyTable {
				if !r.levels.has(lv) || r.key == "" {
					continue
				}
				for _, k := range append([]string{r.key}, r.also...) {
					if prev, ok := seen[k]; ok {
						t.Errorf("key %q at %v is both %q and %q", k, lv, prev, r.name)
					}
					seen[k] = r.name
				}
			}
		})
		t.Run("names unique at "+lv.String(), func(t *testing.T) {
			seen := map[string]bool{}
			for _, r := range keyTable {
				if !r.levels.has(lv) {
					continue
				}
				if seen[r.name] {
					t.Errorf("name %q is on two rows at %v", r.name, lv)
				}
				seen[r.name] = true
			}
		})
	}
}

// TestKeyTableByName pins that the plan actions §4.7 lists are in the table
// by name, with no key, and found from inside a project. Load-bearing: they
// have no key, so the `:` search is the only way to run them, and a row
// missing here is an action the dashboard has lost.
func TestKeyTableByName(t *testing.T) {
	tests := []struct {
		name   string
		action modeAction
	}{
		{"extend the plan", actionPlanExtend},
		{"amend the plan", actionPlanAmend},
		{"fix a bug", actionPlanFix},
		{"reconcile the plan with a document", actionPlanReconcile},
		{"check the plan", actionPlanCheck},
		{"approve the plan", actionPlanApprove},
		{"start a new plan", actionPlanNew},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range keyTable {
				if r.name == tc.name && r.key == "" {
					if r.action != tc.action {
						t.Fatalf("row %q runs %v, want %v", tc.name, r.action, tc.action)
					}
					if !r.levels.has(levelProject) || !r.levels.has(levelAgent) {
						t.Fatalf("row %q levels = %b, want Project and the Agent", tc.name, r.levels)
					}
					return
				}
			}
			t.Fatalf("no by-name row %q", tc.name)
		})
	}
}

// TestKeyTableBlockedReasons pins the rows' blocked reasons. The load-bearing
// rows are the two the level changed: a key the all-projects screen's gate
// dims in the leader dialog runs in the key table (the level decides now),
// and add a project runs at All projects without the screen's flag, because
// a dashboard started outside any project is at All projects with the flag
// unset. The rest pin that each row keeps its handler's own guard.
func TestKeyTableBlockedReasons(t *testing.T) {
	ready := leaderConds{
		leader: defaultLeaderKey, hasActiveProject: true, selected: true,
		hasBuild: true, buildKnown: true, canAllProjects: true,
		canAddProject: true, canRemoveProject: true, canRefreshUsage: true,
		canStartPlan: true,
	}
	row := func(lv level, k string) keyRow {
		t.Helper()
		r, ok := keyRowFor(lv, k)
		if !ok {
			t.Fatalf("no row for %q at %v", k, lv)
		}
		return r
	}
	tests := []struct {
		name  string
		row   keyRow
		conds func(c leaderConds) leaderConds
		want  string
	}{
		{"inbox runs with the screen open (load-bearing)", row(levelAllProjects, "i"),
			func(c leaderConds) leaderConds { c.allProjectsOpen = true; return c }, ""},
		{"add a project runs without the screen flag (load-bearing)", row(levelAllProjects, "+"),
			func(c leaderConds) leaderConds { return c }, ""},
		{"add a project without its seam", row(levelAllProjects, "+"),
			func(c leaderConds) leaderConds { c.canAddProject = false; return c }, "adding a project is not available here"},
		{"remove with no project selected", row(levelAllProjects, "-"),
			func(c leaderConds) leaderConds { return c }, "no project is selected"},
		{"pause with no build", row(levelProject, "p"),
			func(c leaderConds) leaderConds { c.hasBuild = false; return c }, "this project has no build"},
		{"pause with a build", row(levelProject, "p"),
			func(c leaderConds) leaderConds { return c }, ""},
		{"details with no session", row(levelProject, "d"),
			func(c leaderConds) leaderConds { c.selected = false; return c }, "no session selected"},
		{"details with a session", row(levelProject, "d"),
			func(c leaderConds) leaderConds { return c }, ""},
		{"restart a live agent", row(levelProject, "r"),
			func(c leaderConds) leaderConds { return c }, "only a stopped or stuck agent can be restarted"},
		{"new session with no project", row(levelProject, "n"),
			func(c leaderConds) leaderConds { c.hasActiveProject = false; return c }, "no project is open"},
		{"keys always runs", row(levelAgent, "?"),
			func(c leaderConds) leaderConds { return leaderConds{} }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.blockedReason(tc.conds(ready)); got != tc.want {
				t.Fatalf("%q blocked = %q, want %q", tc.row.name, got, tc.want)
			}
		})
	}
}
