package planpatch

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/dphaener/afk/internal/masterplan"
)

// idRE is a task id in its three parts: milestone, number and an optional
// letter. masterplan.ValidID accepts the same shape; this one is here only
// because the id choice needs the parts and ValidID gives a yes or no.
var idRE = regexp.MustCompile(`^T(\d+)\.(\d+)([a-z]?)$`)

// NextInMilestone returns the id for a task added at the end of a
// milestone: T<milestone>.<n>, where n is one more than the highest task
// number the milestone has among ids, or 1 when it has no task.
//
// It is the highest number plus one and never a number that is free lower
// down. A gap in the numbers is a task that was taken out of use, and the
// ledger, the decision log and old pull requests still name it, so giving
// its number to a new task would make two different pieces of work share
// one name. A letter does not count as a number of its own: with T2.2 and
// T2.2a the next is T2.3.
//
// ids must be every task id of the plan, taken from the plan itself. The
// caller passes them because this function reads no file.
func NextInMilestone(ids []string, milestone int) (string, error) {
	if milestone < 0 {
		return "", fmt.Errorf("%d is not a milestone number. Give the number from the milestone's heading, such as 2 for \"### M2\"", milestone)
	}
	highest := 0
	for _, id := range ids {
		m := idRE.FindStringSubmatch(id)
		if m == nil {
			continue
		}
		// A number too long to read is passed over: no id this function can
		// return would be equal to it, so it cannot be reused by accident.
		ms, err := strconv.Atoi(m[1])
		if err != nil || ms != milestone {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("T%d.%d", milestone, highest+1), nil
}

// Between returns the id for a task that must run right after the task
// after and before the task that follows it: after's number with the first
// free letter that comes later than after's own letter. After T2.2 that is
// T2.2a, or T2.2b when T2.2a is taken; after T2.2a it is T2.2b.
//
// A letter is used because the numbers on both sides are taken and rule 8
// forbids renumbering: every later task would change its name while the
// ledger still held the old one. Only letters after after's own are tried,
// so the new id sorts where the task runs.
//
// ids must be every task id of the plan.
func Between(ids []string, after string) (string, error) {
	if !masterplan.ValidID(after) {
		return "", fmt.Errorf("%q is not a task id. A task id looks like T2.3 or T2.3a", after)
	}
	have := make(map[string]bool, len(ids))
	for _, id := range ids {
		have[id] = true
	}
	if !have[after] {
		return "", fmt.Errorf("there is no task %s in the plan, so no task can be put after it. Run afk tasks to see the ids", after)
	}
	m := idRE.FindStringSubmatch(after)
	base := "T" + m[1] + "." + m[2]
	first := byte('a')
	if m[3] != "" {
		first = m[3][0] + 1
	}
	for c := first; c <= 'z'; c++ {
		if id := base + string(c); !have[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("every letter after %s is taken, up to %sz. Use the next number in the milestone instead: afk plan next-id --milestone %s", after, base, m[1])
}

// followupRE is a design follow-up prompt's base name: the digits are the
// whole number, and nothing may come before or after, so a backup copy
// (design-followup-2.md.bak) or a name in another case is not a prompt.
var followupRE = regexp.MustCompile(`^design-followup-(\d+)\.md$`)

// NextFollowup returns the path for the next design follow-up prompt:
// docs/design-followup-<n>.md, where n is one more than the highest number
// among names, or 1 when none is a follow-up prompt.
//
// It is the highest number plus one and never a free number lower down,
// for the reason NextInMilestone gives: a prompt that was deleted was still
// handed to a design tool and may be named in a decision or a commit, so its
// number is not given to a different prompt.
//
// names are the base names of what is in docs/. The caller lists the folder
// because this function reads no file.
func NextFollowup(names []string) string {
	highest := 0
	for _, name := range names {
		m := followupRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		// A number too long to read is passed over, as in NextInMilestone.
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("docs/design-followup-%d.md", highest+1)
}
