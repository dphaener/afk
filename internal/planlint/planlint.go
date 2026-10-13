// Package planlint checks a master plan (a project's docs/PLAN.md) and lists
// everything wrong with it. It is the checker behind afk plan lint, and it
// implements the rule table in
// plugins/afk/skills/master-plan/plan-format.md.
//
// Lint is a pure function from plan text to findings. It reads no file, no
// clock and no environment, so the same text always gives the same report
// and every rule can be table-tested.
//
// The package is built on masterplan.Scan and not on regexes of its own for
// one reason: it must read a plan exactly as the daemon does. The daemon
// decides what to run from masterplan's reading of the task headings and the
// Depends on lines. A checker with its own idea of what a task heading or a
// fence is could call a plan fine that the daemon then refuses, or report a
// task the daemon never sees. So the tasks, their dependencies, the problems,
// the headings and the fenced lines all come from the one scan, and this
// package only adds what the daemon does not look at (sections, task fields,
// the open-questions table, headings that look like tasks but are not).
//
// Errors are what the daemon would refuse or misread. Warnings are what a
// reader would miss.
package planlint

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dphaener/afk/internal/masterplan"
)

// The two levels a finding can have. There is no third: a finding either
// stops the daemon reading the plan correctly or it does not.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// Rule ids. They are a contract with the format document, which lists each
// one in its rule table; a test holds the two together.
const (
	RuleNoTasks                 = "no-tasks"
	RuleBadTaskHeading          = "bad-task-heading"
	RuleDuplicateTask           = "duplicate-task"
	RuleNoDependsLine           = "no-depends-line"
	RuleUnknownDependency       = "unknown-dependency"
	RuleDependencyCycle         = "dependency-cycle"
	RuleOpenQuestionUnknownTask = "open-question-unknown-task"
	RuleDependsNoTaskID         = "depends-no-task-id"
	RuleMissingTaskField        = "missing-task-field"
	RuleMissingSection          = "missing-section"
	RuleOpenQuestionNoTask      = "open-question-no-task"
)

// Rules lists every rule id, errors first, in the order of the format
// document's table.
var Rules = []string{
	RuleNoTasks,
	RuleBadTaskHeading,
	RuleDuplicateTask,
	RuleNoDependsLine,
	RuleUnknownDependency,
	RuleDependencyCycle,
	RuleOpenQuestionUnknownTask,
	RuleDependsNoTaskID,
	RuleMissingTaskField,
	RuleMissingSection,
	RuleOpenQuestionNoTask,
}

// Finding is one thing the checker found.
type Finding struct {
	// Level is LevelError or LevelWarning.
	Level string `json:"level"`
	// Line is the 1-based line the finding points at, or 0 for a finding
	// about the whole file (a missing section, a plan with no task).
	Line int `json:"line"`
	// Task is the task id the finding is about, or empty.
	Task string `json:"task"`
	// Rule is one of the ids in Rules.
	Rule string `json:"rule"`
	// Message says what is wrong and what to do about it, in plain words.
	Message string `json:"message"`
}

// Summary holds the counts afk plan lint --summary prints.
type Summary struct {
	Milestones int `json:"milestones"`
	Tasks      int `json:"tasks"`
	OwnerTasks int `json:"owner_tasks"`
	// OpenQuestions counts the rows of the open-questions table that are not
	// yet answered.
	OpenQuestions int `json:"open_questions"`
	Errors        int `json:"errors"`
	Warnings      int `json:"warnings"`
}

// Report is what Lint returns.
type Report struct {
	// Findings is never nil, so it is written as [] and not null in JSON.
	Findings []Finding `json:"findings"`
	Summary  Summary   `json:"summary"`
}

var (
	// headingRE matches a heading of level 1 to 6. masterplan lists only
	// levels 1 to 4 (the daemon does not treat deeper ones as headings), so
	// the look-alike check reads the lines itself to reach levels 5 and 6.
	headingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(\S.*)$`)
	// looksLikeTaskRE is assumption A2 of the T5.1 plan: a heading whose
	// text starts with T or t and a digit was meant as a task.
	looksLikeTaskRE = regexp.MustCompile(`^[Tt]\d`)
	milestoneRE     = regexp.MustCompile(`^M\d+\s+[—–-]\s+\S`)
	sectionNumberRE = regexp.MustCompile(`^\d+\.\s*`)
)

// taskFields are the three bullets every task should carry, with the pattern
// that finds each. The label must be exact, as the format document says.
var taskFields = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Goal", regexp.MustCompile(`^\s*-\s+\*\*Goal:\*\*`)},
	{"Scope", regexp.MustCompile(`^\s*-\s+\*\*Scope:\*\*`)},
	{"Acceptance", regexp.MustCompile(`^\s*-\s+\*\*Acceptance:\*\*`)},
}

// requiredSections are the level 2 sections the checker looks for.
var requiredSections = []string{
	"Context", "Decisions", "Milestones", "Deferred", "Open questions", "Definition of done",
}

// Lint checks a master plan and returns every finding, sorted by line, then
// errors before warnings, then rule id, so the output is the same on every
// run.
func Lint(data []byte) Report {
	o := masterplan.Scan(data)
	// The same split and trim as masterplan.Scan, so a line number here is
	// the line number there and o.Fenced lines up index for index.
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	fenced := func(i int) bool { return i < len(o.Fenced) && o.Fenced[i] }

	index := make(map[string]int, len(o.Tasks))
	for i, t := range o.Tasks {
		index[t.ID] = i
	}

	findings := []Finding{}
	add := func(level string, line int, task, rule, format string, args ...any) {
		findings = append(findings, Finding{Level: level, Line: line, Task: task, Rule: rule, Message: fmt.Sprintf(format, args...)})
	}

	// What the daemon's parser refuses.
	reported := map[int]bool{}
	for _, t := range o.Tasks {
		reported[t.Line] = true
	}
	for _, p := range o.Problems {
		reported[p.Line] = true
		switch {
		case errors.Is(p.Err, masterplan.ErrBadHeading):
			add(LevelError, p.Line, "", RuleBadTaskHeading,
				"%q is not a task heading, so afk refuses the whole plan. Write it as \"#### T<milestone>.<n> — Title\": four hashes, a capital T, a dash with a space each side, then a title.",
				lineAt(lines, p.Line))
		case errors.Is(p.Err, masterplan.ErrDuplicateTask):
			add(LevelError, p.Line, p.Task, RuleDuplicateTask,
				"%s is already a task on line %d. A task id is used once: give this task its own id.",
				p.Task, o.Tasks[index[p.Task]].Line)
		case errors.Is(p.Err, masterplan.ErrNoDependsLine):
			add(LevelError, p.Line, p.Task, RuleNoDependsLine,
				"%s has no Depends on line. Add \"- **Depends on:** —\" (or the ids of the tasks it waits for) under its heading, before the next heading.",
				p.Task)
		}
	}
	if len(o.Tasks) == 0 && len(o.Problems) == 0 {
		add(LevelError, 0, "", RuleNoTasks,
			"This plan has no task. Add a heading like \"#### T1.1 — Title\" with a \"- **Depends on:**\" line under it.")
	}

	// Headings that look like tasks but that the daemon reads as ordinary
	// headings and so never runs. The parser only refuses "#### T<digit>".
	for i, line := range lines {
		if fenced(i) || reported[i+1] {
			continue
		}
		m := headingRE.FindStringSubmatch(line)
		if m == nil || !looksLikeTaskRE.MatchString(m[2]) {
			continue
		}
		switch {
		case !validTaskHeadingText(m[2]):
			add(LevelError, i+1, "", RuleBadTaskHeading,
				"%q looks like a task but is not a task heading, so afk would never run it. Write it as \"#### T<milestone>.<n> — Title\": four hashes, a capital T, a dash with a space each side, then a title.",
				line)
		case len(m[1]) != 4:
			add(LevelError, i+1, "", RuleBadTaskHeading,
				"%q has %d hashes, so afk would never run it. A task heading needs exactly four: \"#### %s\".",
				line, len(m[1]), m[2])
		default:
			add(LevelError, i+1, "", RuleBadTaskHeading,
				"%q has extra space after the hashes, so afk would never run it. Put exactly one space there: \"#### %s\".",
				line, m[2])
		}
	}

	// Dependencies. These run even when the parser found a problem above:
	// the daemon would refuse such a plan outright, so here they mean "this
	// is also wrong", not "this is what the daemon would run".
	for _, t := range o.Tasks {
		for _, d := range t.DependsOn {
			if _, ok := index[d]; !ok {
				add(LevelError, t.Line, t.ID, RuleUnknownDependency,
					"%s depends on %s, which is not a task in this plan. Fix the id or add the task.", t.ID, d)
			}
		}
	}
	if cyc := firstCycle(o.Tasks, index); cyc != nil {
		add(LevelError, o.Tasks[index[cyc[0]]].Line, cyc[0], RuleDependencyCycle,
			"These tasks wait for each other in a loop, so none of them can start: %s. Remove one of these dependencies.",
			strings.Join(cyc, " -> "))
	}

	for _, t := range o.Tasks {
		if t.Note != "" {
			add(LevelWarning, t.Line, t.ID, RuleDependsNoTaskID,
				"%s's Depends on line says %q, which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none.",
				t.ID, t.Note)
		}
	}

	// Goal, Scope and Acceptance. A task's body ends at the next heading the
	// scan lists, the same place the daemon stops looking for Depends on.
	for _, t := range o.Tasks {
		end := len(lines)
		for _, h := range o.Headings {
			if h.Line > t.Line {
				end = h.Line - 1
				break
			}
		}
		var missing []string
		for _, f := range taskFields {
			found := false
			for i := t.Line; i < end && !found; i++ {
				found = !fenced(i) && f.re.MatchString(lines[i])
			}
			if !found {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			add(LevelWarning, t.Line, t.ID, RuleMissingTaskField,
				"%s has no %s line. Add %s under its heading, so the agent that runs the task knows what to build and when it is done.",
				t.ID, joinOr(missing), bulletList(missing))
		}
	}

	// Sections.
	firstLevel2 := len(lines) + 1
	for _, h := range o.Headings {
		if h.Level == 2 {
			firstLevel2 = h.Line
			break
		}
	}
	howTo := false
	for i := 0; i < firstLevel2-1 && i < len(lines) && !howTo; i++ {
		howTo = !fenced(i) && strings.Contains(strings.ToLower(lines[i]), "**how to use")
	}
	if !howTo {
		add(LevelWarning, 0, "", RuleMissingSection,
			"The plan has no \"**How to use this document.**\" paragraph before its first \"##\" section. Add one that tells a reader how to read the plan.")
	}
	for _, name := range requiredSections {
		if sectionHeading(o.Headings, name) < 0 {
			add(LevelWarning, 0, "", RuleMissingSection,
				"The plan has no \"## %s\" section. Add it, even if it is short.", name)
		}
	}

	// Open questions.
	open := 0
	for _, r := range masterplan.Questions(data) {
		if !r.Open {
			continue
		}
		open++
		ids := r.NeededBy
		if len(ids) == 0 {
			add(LevelWarning, r.Line, "", RuleOpenQuestionNoTask,
				"This open question's \"Needed by\" names no task, so no task waits for the answer. Name the tasks that need it, or mark the question answered.")
			continue
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if _, ok := index[id]; ok || seen[id] {
				continue
			}
			seen[id] = true
			add(LevelError, r.Line, id, RuleOpenQuestionUnknownTask,
				"This open question is needed by %s, which is not a task in this plan. Fix the id, or mark the question answered.", id)
		}
	}

	// A stable sort: findings that tie on all three keys (two unknown
	// dependencies of one task) keep the order they were found in, which is
	// itself fixed by the plan text.
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Level != b.Level {
			return a.Level == LevelError
		}
		return a.Rule < b.Rule
	})

	s := Summary{Tasks: len(o.Tasks), OpenQuestions: open}
	for _, h := range o.Headings {
		if h.Level == 3 && milestoneRE.MatchString(h.Text) {
			s.Milestones++
		}
	}
	for _, t := range o.Tasks {
		if t.Owner() {
			s.OwnerTasks++
		}
	}
	for _, f := range findings {
		if f.Level == LevelError {
			s.Errors++
		} else {
			s.Warnings++
		}
	}
	return Report{Findings: findings, Summary: s}
}

func lineAt(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// validTaskHeadingText reports whether text would be a task heading if it
// were written with exactly four hashes and one space. It asks the daemon's
// own scan and keeps no copy of its heading pattern, so the "wrong number of
// hashes" message can never disagree with what the daemon accepts.
func validTaskHeadingText(text string) bool {
	return len(masterplan.Scan([]byte("#### "+text)).Tasks) == 1
}

// firstCycle returns the first dependency cycle in plan order as a path that
// ends where it began (T1.1, T1.2, T1.1), or nil. Dependencies on ids that
// are not in the plan are skipped; they have their own finding.
//
// Only one cycle is reported, as masterplan.Validate does. Cycles overlap
// and share edges, so listing "all" of them repeats the same mistake many
// times over, and removing one edge often clears several. The next run shows
// the next cycle, if one is left.
//
// The walk is written here and not read out of Validate's error text: the
// wording of an error is not an interface, and Validate stops at an unknown
// dependency before it ever looks for a cycle.
func firstCycle(tasks []masterplan.Task, index map[string]int) []string {
	const (
		unseen = iota
		onPath
		finished
	)
	state := make([]int, len(tasks))
	var path []string
	var visit func(i int) []string
	visit = func(i int) []string {
		state[i] = onPath
		path = append(path, tasks[i].ID)
		for _, d := range tasks[i].DependsOn {
			j, ok := index[d]
			if !ok {
				continue
			}
			switch state[j] {
			case onPath:
				start := 0
				for k, id := range path {
					if id == d {
						start = k
						break
					}
				}
				return append(append([]string{}, path[start:]...), d)
			case unseen:
				if cyc := visit(j); cyc != nil {
					return cyc
				}
			}
		}
		path = path[:len(path)-1]
		state[i] = finished
		return nil
	}
	for i := range tasks {
		if state[i] == unseen {
			if cyc := visit(i); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// sectionHeading returns the index in headings of the first level 2 heading
// whose title starts with name, or -1. A leading number and dot are dropped
// and case is ignored, so "## 9. Deferred / later" is the Deferred section.
func sectionHeading(headings []masterplan.Heading, name string) int {
	for i, h := range headings {
		if h.Level != 2 {
			continue
		}
		text := strings.ToLower(sectionNumberRE.ReplaceAllString(h.Text, ""))
		if strings.HasPrefix(text, strings.ToLower(name)) {
			return i
		}
	}
	return -1
}

// joinOr writes "Goal", "Goal or Scope", "Goal, Scope or Acceptance".
func joinOr(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// bulletList writes the bullets to add, as they must be typed.
func bulletList(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("\"- **%s:**\"", n)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
