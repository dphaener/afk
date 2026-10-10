package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

// tk builds a task from its ID and checkbox char (' ', 'x', '!'). deps nil
// means the Depends on field is absent; use []string{} for `none`.
func tk(id string, box byte, deps []string) Task {
	// IDs in these tests are always "W<w>.T<n>" with single digits.
	w, n := int(id[1]-'0'), int(id[4]-'0')
	st := CheckIncomplete
	switch box {
	case 'x':
		st = CheckComplete
	case '!':
		st = CheckFailed
	}
	return Task{ID: id, Wave: w, Num: n, State: st, DependsOn: deps}
}

func ids(ts []Task) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func mkPlan(waves ...[]Task) *Plan {
	p := &Plan{}
	for _, ts := range waves {
		p.Waves = append(p.Waves, Wave{Number: ts[0].Wave, Tasks: ts})
	}
	return p
}

// TestSchedule rows that carry the contract:
//   - "dag cross-wave ready": a W3 task is Ready while W2 is still pending,
//     the whole reason the Overview stopped using a wave barrier.
//   - "dag failed dep blocks transitive": Blocked follows chains, not just
//     direct deps.
//   - "legacy out-of-order ready": W1 and W3 both Ready; the barrier is the
//     PREVIOUS wave only, matching plan_to_args.rb.
//   - "legacy failed does not block": legacy upstream keeps going past a
//     `[!]`, so no Blocked and the next wave is Ready.
//   - "mixed presence falls back to legacy": upstream rejects such plans;
//     we display them as legacy instead of hiding progress.
//   - "cycle" and "unknown id": must terminate and report neither Ready nor
//     Blocked.
func TestSchedule(t *testing.T) {
	tests := []struct {
		name        string
		plan        *Plan
		wantDAG     bool
		wantReady   []string
		wantBlocked []string
	}{
		{
			name: "dag cross-wave ready",
			plan: mkPlan(
				[]Task{tk("W1.T1", 'x', []string{})},
				[]Task{tk("W2.T1", ' ', []string{"W1.T1"}), tk("W2.T2", ' ', []string{"W1.T1"})},
				[]Task{tk("W3.T1", ' ', []string{"W1.T1"}), tk("W3.T2", ' ', []string{"W2.T1"})},
			),
			wantDAG:     true,
			wantReady:   []string{"W2.T1", "W2.T2", "W3.T1"},
			wantBlocked: []string{},
		},
		{
			name: "dag failed dep blocks transitive",
			plan: mkPlan(
				[]Task{tk("W1.T1", '!', []string{}), tk("W1.T2", 'x', []string{})},
				[]Task{tk("W2.T1", ' ', []string{"W1.T1"}), tk("W2.T2", ' ', []string{"W1.T2"})},
				[]Task{tk("W3.T1", ' ', []string{"W2.T1"}), tk("W3.T2", ' ', []string{"W2.T2"})},
			),
			wantDAG:     true,
			wantReady:   []string{"W2.T2"},
			wantBlocked: []string{"W2.T1", "W3.T1"},
		},
		{
			name: "dag unknown id is neither ready nor blocked",
			plan: mkPlan(
				[]Task{tk("W1.T1", ' ', []string{"W9.T9"}), tk("W1.T2", ' ', []string{})},
			),
			wantDAG:     true,
			wantReady:   []string{"W1.T2"},
			wantBlocked: []string{},
		},
		{
			name: "dag cycle terminates",
			plan: mkPlan(
				[]Task{tk("W1.T1", ' ', []string{"W1.T2"}), tk("W1.T2", ' ', []string{"W1.T1"})},
			),
			wantDAG:     true,
			wantReady:   []string{},
			wantBlocked: []string{},
		},
		{
			name: "dag cycle fed by failed dep is blocked in any visit order",
			plan: mkPlan(
				[]Task{tk("W1.T1", '!', []string{})},
				[]Task{tk("W2.T1", ' ', []string{"W2.T2"}), tk("W2.T2", ' ', []string{"W2.T1", "W1.T1"})},
			),
			wantDAG:     true,
			wantReady:   []string{},
			wantBlocked: []string{"W2.T1", "W2.T2"},
		},
		{
			name: "dag all complete",
			plan: mkPlan(
				[]Task{tk("W1.T1", 'x', []string{})},
				[]Task{tk("W2.T1", 'x', []string{"W1.T1"})},
			),
			wantDAG:     true,
			wantReady:   []string{},
			wantBlocked: []string{},
		},
		{
			name: "legacy out-of-order ready",
			plan: mkPlan(
				[]Task{tk("W1.T1", ' ', nil)},
				[]Task{tk("W2.T1", 'x', nil)},
				[]Task{tk("W3.T1", ' ', nil)},
			),
			wantReady:   []string{"W1.T1", "W3.T1"},
			wantBlocked: []string{},
		},
		{
			name: "legacy previous wave incomplete blocks next",
			plan: mkPlan(
				[]Task{tk("W1.T1", 'x', nil)},
				[]Task{tk("W2.T1", ' ', nil), tk("W2.T2", 'x', nil)},
				[]Task{tk("W3.T1", ' ', nil)},
			),
			wantReady:   []string{"W2.T1"},
			wantBlocked: []string{},
		},
		{
			name: "legacy failed does not block",
			plan: mkPlan(
				[]Task{tk("W1.T1", 'x', nil), tk("W1.T2", '!', nil)},
				[]Task{tk("W2.T1", ' ', nil), tk("W2.T2", ' ', nil)},
				[]Task{tk("W3.T1", ' ', nil)},
			),
			wantReady:   []string{"W2.T1", "W2.T2"},
			wantBlocked: []string{},
		},
		{
			name: "legacy uses highest wave number below, not file order",
			plan: &Plan{Waves: []Wave{
				{Number: 3, Tasks: []Task{tk("W3.T1", ' ', nil)}},
				{Number: 1, Tasks: []Task{tk("W1.T1", ' ', nil)}},
			}},
			wantReady:   []string{"W1.T1"},
			wantBlocked: []string{},
		},
		{
			name: "mixed presence falls back to legacy",
			plan: mkPlan(
				[]Task{tk("W1.T1", ' ', []string{})},
				[]Task{tk("W2.T1", ' ', nil)},
			),
			wantReady:   []string{"W1.T1"},
			wantBlocked: []string{},
		},
		{
			name:        "empty plan",
			plan:        &Plan{},
			wantReady:   []string{},
			wantBlocked: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Schedule(tc.plan)
			if v.DAG != tc.wantDAG {
				t.Errorf("DAG = %v, want %v", v.DAG, tc.wantDAG)
			}
			if got := ids(v.Ready); !reflect.DeepEqual(got, tc.wantReady) {
				t.Errorf("Ready = %v, want %v", got, tc.wantReady)
			}
			if got := ids(v.Blocked); !reflect.DeepEqual(got, tc.wantBlocked) {
				t.Errorf("Blocked = %v, want %v", got, tc.wantBlocked)
			}
		})
	}
}

// TestSchedule_EffectiveDeps pins that the Waves suffix and the Overview
// read the same deps: legacy synthesizes the previous wave by number, DAG
// passes DependsOn through unchanged. Every task gets an entry, complete
// ones included.
func TestSchedule_EffectiveDeps(t *testing.T) {
	legacy := mkPlan(
		[]Task{tk("W1.T1", 'x', nil), tk("W1.T2", ' ', nil)},
		[]Task{tk("W2.T1", ' ', nil)},
		[]Task{tk("W3.T1", ' ', nil)},
	)
	want := map[string][]string{
		"W1.T1": {},
		"W1.T2": {},
		"W2.T1": {"W1.T1", "W1.T2"},
		"W3.T1": {"W2.T1"},
	}
	if got := Schedule(legacy).EffectiveDeps; !reflect.DeepEqual(got, want) {
		t.Errorf("legacy EffectiveDeps = %v, want %v", got, want)
	}

	dag := mkPlan(
		[]Task{tk("W1.T1", 'x', []string{})},
		[]Task{tk("W2.T1", ' ', []string{"W1.T1", "W9.T9"})},
	)
	wantDAG := map[string][]string{
		"W1.T1": {},
		"W2.T1": {"W1.T1", "W9.T9"},
	}
	if got := Schedule(dag).EffectiveDeps; !reflect.DeepEqual(got, wantDAG) {
		t.Errorf("dag EffectiveDeps = %v, want %v", got, wantDAG)
	}
}

// TestSchedule_Fixtures runs the real fixtures through the parser so the
// scheduler is checked against what ParsePlan actually produces. valid.md is
// legacy with W1 = [x],[!]: the failed task must not hold W2 back, and W3
// waits on W2's open tasks.
func TestSchedule_Fixtures(t *testing.T) {
	valid, err := ParsePlanFile(filepath.Join("testdata", "plan", "valid.md"))
	if err != nil {
		t.Fatal(err)
	}
	v := Schedule(valid)
	if v.DAG {
		t.Errorf("valid.md DAG = true, want false")
	}
	if got, want := ids(v.Ready), []string{"W2.T1", "W2.T2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("valid.md Ready = %v, want %v", got, want)
	}
	if len(v.Blocked) != 0 {
		t.Errorf("valid.md Blocked = %v, want none", ids(v.Blocked))
	}
}
