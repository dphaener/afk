package state

import "sort"

// ScheduleView is the scheduler's answer for one plan: which incomplete
// tasks can start now, which are stuck behind a failure, and the dependency
// sets those answers were computed from.
type ScheduleView struct {
	// DAG reports the mode. It is true only when EVERY task carries a
	// `**Depends on**` field. A plan with none is legacy (wave barriers). A
	// plan with some is mixed: upstream (plan_to_args.rb) rejects it, but
	// afk deliberately displays it as legacy rather than hiding
	// progress for a plan the dashboard can still read.
	DAG bool

	// Ready is every incomplete task whose effective dependencies are all
	// complete, in plan order. The plan file cannot tell a running `[ ]`
	// task from a not-started one, so Ready also covers tasks already in
	// flight.
	Ready []Task

	// Blocked is every incomplete task with a transitive dependency that
	// failed, in plan order. In DAG mode the upstream engine rewrites such
	// dependents to `[!] ... FAILED: blocked by X` itself, so a non-empty
	// Blocked mostly signals the window before that write, a crashed engine
	// run, or the Agent-loop fallback. Always empty in legacy mode.
	Blocked []Task

	// EffectiveDeps maps every task ID (complete ones too) to the deps the
	// scheduler used: DependsOn verbatim in DAG mode, the synthesized
	// previous-wave IDs in legacy mode. The Waves tab reads this so its
	// suffix can never disagree with the Overview.
	EffectiveDeps map[string][]string
}

// Schedule computes ready and blocked tasks. It is pure: no I/O, and it
// never mutates p.
//
// Unknown dependency IDs count as unmet, so such a task is neither Ready
// nor (on that account) Blocked. Cycles are tolerated and terminate: a
// cycle's members never become Ready because each waits on another, and they
// are Blocked only if a failure feeds into the cycle.
func Schedule(p *Plan) ScheduleView {
	view := ScheduleView{
		Ready:         []Task{},
		Blocked:       []Task{},
		EffectiveDeps: map[string][]string{},
	}

	var all []Task
	present, absent := 0, 0
	for _, w := range p.Waves {
		for _, t := range w.Tasks {
			all = append(all, t)
			if t.DependsOn == nil {
				absent++
			} else {
				present++
			}
		}
	}
	view.DAG = present > 0 && absent == 0

	state := map[string]CheckState{}
	for _, t := range all {
		state[t.ID] = t.State
	}

	if view.DAG {
		for _, t := range all {
			view.EffectiveDeps[t.ID] = t.DependsOn
		}
	} else {
		legacyDeps(p, view.EffectiveDeps)
	}

	// Legacy upstream execution does NOT stop the next wave on a failed
	// task, so there a failed dependency counts as satisfied. In DAG mode
	// gate() blocks on it (execute.js), so only a complete dep is met.
	met := func(id string) bool {
		s, ok := state[id]
		if !ok {
			return false
		}
		return s == CheckComplete || (!view.DAG && s == CheckFailed)
	}

	var tainted map[string]bool
	if view.DAG {
		tainted = failureReach(all, view.EffectiveDeps)
	}

	for _, t := range all {
		if t.State != CheckIncomplete {
			continue
		}
		ready := true
		for _, d := range view.EffectiveDeps[t.ID] {
			if !met(d) {
				ready = false
				break
			}
		}
		switch {
		case ready:
			view.Ready = append(view.Ready, t)
		case tainted[t.ID]:
			view.Blocked = append(view.Blocked, t)
		}
	}
	return view
}

// legacyDeps fills deps with each task's synthesized dependencies for a plan
// without (complete) Depends on data: every task of the wave with the highest
// Number below the task's. Selection is by wave number, not file order,
// matching plan_to_args.rb's legacy rule, so an out-of-order file still
// resolves the same way the engine would.
func legacyDeps(p *Plan, deps map[string][]string) {
	byNum := map[int][]string{}
	var nums []int
	for _, w := range p.Waves {
		if _, ok := byNum[w.Number]; !ok {
			nums = append(nums, w.Number)
		}
		for _, t := range w.Tasks {
			byNum[w.Number] = append(byNum[w.Number], t.ID)
		}
	}
	sort.Ints(nums)

	for _, w := range p.Waves {
		prev := []string{}
		for _, n := range nums {
			if n < w.Number {
				prev = byNum[n]
			}
		}
		for _, t := range w.Tasks {
			deps[t.ID] = append([]string{}, prev...)
		}
	}
}

// failureReach returns the IDs of every task downstream of a failed task by
// walking dependents outward from the failures. A forward DFS memoized with
// an in-progress mark would cache a wrong answer for a cycle member when the
// failure is reachable only through a later edge, so the result would depend
// on visit order; a breadth-first walk over reversed edges has no such
// order and terminates because each ID is enqueued once. A failed task is
// itself included only if another failure reaches it.
func failureReach(all []Task, deps map[string][]string) map[string]bool {
	dependents := map[string][]string{}
	for _, t := range all {
		for _, d := range deps[t.ID] {
			dependents[d] = append(dependents[d], t.ID)
		}
	}
	reached := map[string]bool{}
	var queue []string
	for _, t := range all {
		if t.State == CheckFailed {
			queue = append(queue, t.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range dependents[id] {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	return reached
}
