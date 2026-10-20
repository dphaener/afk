// Package masterplan reads a project's master plan (the document the build
// loop works through) into an ordered list of tasks and their dependencies.
// It implements docs/PLAN.md §5.1.
//
// The rules are strict on purpose. A runner that guesses at a heading it half
// recognises, or at a dependency line it cannot read, could start a task
// before the work it needs has landed, so every ambiguity is either an error
// (a malformed heading, a task without a Depends on line, an unknown id, a
// cycle) or an explicit, visible fallback (prose with no task ids becomes a
// Note and no dependencies, which the caller can show to the owner).
//
// The package is standard library only and imports no other afk package, so
// the parser can be used and tested on its own.
package masterplan

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Sentinel errors. Parse and Validate wrap them with the line and the ids
// involved, so callers match with errors.Is and print the message as is.
var (
	ErrNoTasks           = errors.New("master plan has no tasks")
	ErrBadHeading        = errors.New("malformed task heading")
	ErrDuplicateTask     = errors.New("duplicate task id")
	ErrNoDependsLine     = errors.New("task has no Depends on line")
	ErrUnknownDependency = errors.New("unknown dependency")
	ErrCycle             = errors.New("dependency cycle")
)

// Task is one task heading of a master plan.
type Task struct {
	ID    string
	Title string
	// Line is the 1-based line of the heading.
	Line      int
	DependsOn []string
	// Note holds the raw Depends on text when it was prose that named no
	// task and was not a none-marker (for example "M3–M8 as available").
	// DependsOn is empty then, and the note lets a person see why.
	Note string
}

// ownerMarkerRE matches a bracketed marker that is exactly "(owner)" or
// starts "(owner;", ignoring case and allowing spaces after the opening
// bracket and around the semicolon.
var ownerMarkerRE = regexp.MustCompile(`(?i)\(\s*owner\s*(\)|;)`)

// Owner reports whether the task is the owner's to do, not an agent's: its
// title carries "(owner)" or "(owner; ...)", as in "T0.0 — Stand up the VM
// (owner; not an agent task)".
//
// The match is deliberately narrow. The daemon once launched an agent for
// the owner's VM checklist (PLAN T2.2), so the marker must be unmistakable.
// "(owner + agent)" is an ordinary task that an agent does with the owner's
// help, and "(owners)" or "(owner approval)" is just prose; treating any of
// them as owner tasks would leave agent work undone.
func (t Task) Owner() bool { return ownerMarkerRE.MatchString(t.Title) }

// fixMarkerRE matches a bracketed marker that is exactly "(fix)" or starts
// "(fix;", ignoring case and allowing spaces the way ownerMarkerRE does.
var fixMarkerRE = regexp.MustCompile(`(?i)\(\s*fix\s*(\)|;)`)

// Fix reports whether the task is a bug fix: its title carries "(fix)" or
// "(fix; ...)", as in "T13.5 — Stop the duplicate reminder (fix; regression
// from T4.2)". A fix task is what a fix planning session adds, and the daemon
// picks a ready one ahead of every other ready task (docs/PLAN.md §2, "Bug
// fixes"), so the match must be unmistakable. "(fixes)", "(fix up)" and
// "(prefix)" are prose, and a task that read as a fix would jump the queue.
func (t Task) Fix() bool { return fixMarkerRE.MatchString(t.Title) }

// DesignSystemFile is where the owner saves the design system the design tool
// hands back. It is written into the plan's design task and the daemon's item
// for that task, so both name the same file.
const DesignSystemFile = "docs/design-system.md"

// bracketRE matches one bracket group with no nested brackets, so the owner
// bracket can be cut out of a title with the words of its note.
var bracketRE = regexp.MustCompile(`\([^()]*\)`)

// DesignSystem reports whether the task is the owner's design task: an owner
// task whose title, with its owner bracket removed, is "Design system". The
// title is the only identity a task has in the plan, so the plan-session gate
// (internal/plansession via the root package) and the daemon's item both find
// the task by this one test; defining it once here keeps them from drifting.
//
// The match is deliberately narrow. "Design system (owner + agent)" is an
// agent task that builds the tokens, and "Design system for the admin pages
// (owner)" is a different task; taking either for the design hand-off would
// send the owner a reconcile step for work that is not the design.
func (t Task) DesignSystem() bool {
	if !t.Owner() {
		return false
	}
	stripped := bracketRE.ReplaceAllStringFunc(t.Title, func(group string) string {
		if ownerMarkerRE.MatchString(group) {
			return ""
		}
		return group
	})
	return strings.EqualFold(strings.Join(strings.Fields(stripped), " "), "design system")
}

var (
	taskHeadingRE = regexp.MustCompile(`^#### (T\d+\.\d+[a-z]?)\s+[—–-]\s+(\S.*)$`)
	badHeadingRE  = regexp.MustCompile(`^#### T\d`)
	anyHeadingRE  = regexp.MustCompile(`^#{1,4}\s`)
	dependsRE     = regexp.MustCompile(`^\s*-\s+\*\*Depends on:\*\*\s*(.*)$`)
	leadingIDRE   = regexp.MustCompile(`^(T\d+\.\d+[a-z]?)(\s|$)`)
	validIDRE     = regexp.MustCompile(`^T\d+\.\d+[a-z]?$`)
)

// ValidID reports whether s is a task id such as T4.2 or T4.2b.
func ValidID(s string) bool { return validIDRE.MatchString(s) }

// ParseFile reads path and parses it.
func ParseFile(path string) ([]Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Heading is one markdown heading of level 1 to 4 that sits outside a code
// fence.
type Heading struct {
	// Level is the number of leading hashes, 1 to 4.
	Level int
	// Text is the heading text after the hashes, trimmed.
	Text string
	// Line is the 1-based line of the heading.
	Line int
}

// Problem is one thing Scan found that makes Parse refuse the plan.
type Problem struct {
	// Line is the 1-based line the problem is reported at: the heading
	// itself for a malformed or duplicate heading, and the task's heading
	// for a task with no Depends on line.
	Line int
	// Task is the task id when one is known (a duplicate, a task with no
	// Depends on line), and empty for a malformed heading, whose id is
	// exactly the part that could not be trusted.
	Task string
	// Err is the wrapped error Parse returns for this problem, so
	// errors.Is against the sentinels and the printed message are the same
	// whichever of the two a caller used.
	Err error
}

// Outline is everything Scan reads from a master plan in one pass.
type Outline struct {
	// Tasks holds every task heading that parsed, in plan order. Only the
	// first use of an id is kept. A task with no Depends on line is kept
	// too, with no dependencies, and has a Problem.
	Tasks []Task
	// Problems holds what Parse would refuse, in the order Parse would meet
	// them, which is also line order. It never holds ErrNoTasks: an empty
	// plan is a fact about the whole file with no line to point at, so
	// callers read it from len(Tasks) == 0.
	Problems []Problem
	// Headings holds every level 1 to 4 heading outside a fence, task
	// headings and malformed ones included. Level 5 and 6 headings are
	// left out because the scan does not treat them as headings at all: a
	// "##### Sub" line does not end a task's section.
	Headings []Heading
	// Fenced has one entry per line (index is line number minus one), true
	// for a line that opens or closes a code fence and for every line
	// inside one. Lines are the text split on "\n", so a file that ends
	// with a newline has a last, empty entry.
	Fenced []bool
}

// Scan reads a master plan from top to bottom and reports everything it
// finds, without stopping at a problem.
//
// It exists because the checker in internal/planlint has two needs Parse
// cannot meet. It must report every problem in a plan, where Parse stops at
// the first. And it must read a plan exactly as the daemon does: a second
// scanner inside the checker could drift and call a plan fine that the
// daemon then refuses, or the other way round. So there is one scan, and
// Parse is built on it.
//
// Code fences are handled first and exactly as internal/state does for work
// plans (a fence line toggles, fenced lines are skipped, an unclosed fence
// hides the rest of the file), so a heading shown as an example inside a
// fence is never a task.
//
// After a problem the scan carries on in the way that invents the least:
//   - a malformed "#### T<digit>" heading opens no task, so the Depends on
//     line under it is not read;
//   - a duplicate id is not added to Tasks and opens no task, so the first
//     copy keeps its own dependencies and the second copy's are not read;
//   - a task whose section ends with no Depends on line stays in Tasks with
//     no dependencies, so later problems that mention it can still name it.
//
// Up to the first problem the scan does exactly what Parse always did, which
// is why Parse can return Problems[0] and stay unchanged for its callers.
func Scan(data []byte) Outline {
	var o Outline
	seen := map[string]int{}
	cur := -1 // index of the task whose Depends on line is still wanted
	haveDeps := false
	inFence := false

	// endSection closes the open task, if any. It runs at every heading and
	// at the end of the file, the two places a task's section can end.
	endSection := func() {
		if cur >= 0 && !haveDeps {
			t := o.Tasks[cur]
			o.Problems = append(o.Problems, Problem{Line: t.Line, Task: t.ID, Err: noDeps(t)})
		}
		cur, haveDeps = -1, false
	}

	lines := strings.Split(string(data), "\n")
	o.Fenced = make([]bool, len(lines))
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimRight(raw, " \t\r")

		if lead := strings.TrimLeft(line, " \t"); strings.HasPrefix(lead, "```") || strings.HasPrefix(lead, "~~~") {
			inFence = !inFence
			o.Fenced[i] = true
			continue
		}
		if inFence {
			o.Fenced[i] = true
			continue
		}

		if anyHeadingRE.MatchString(line) {
			endSection()
			text := strings.TrimLeft(line, "#")
			o.Headings = append(o.Headings, Heading{Level: len(line) - len(text), Text: strings.TrimSpace(text), Line: lineNo})
			if m := taskHeadingRE.FindStringSubmatch(line); m != nil {
				if first, dup := seen[m[1]]; dup {
					o.Problems = append(o.Problems, Problem{
						Line: lineNo,
						Task: m[1],
						Err:  fmt.Errorf("line %d: task %s already defined on line %d: %w", lineNo, m[1], first, ErrDuplicateTask),
					})
					continue
				}
				seen[m[1]] = lineNo
				o.Tasks = append(o.Tasks, Task{ID: m[1], Title: strings.TrimSpace(m[2]), Line: lineNo})
				cur = len(o.Tasks) - 1
			} else if badHeadingRE.MatchString(line) {
				o.Problems = append(o.Problems, Problem{
					Line: lineNo,
					Err:  fmt.Errorf("line %d: %q is not \"#### T<m>.<n> — <title>\": %w", lineNo, line, ErrBadHeading),
				})
			}
			continue
		}

		if cur >= 0 && !haveDeps {
			if m := dependsRE.FindStringSubmatch(line); m != nil {
				haveDeps = true
				o.Tasks[cur].DependsOn, o.Tasks[cur].Note = readDepends(m[1], o.Tasks[:cur])
			}
		}
	}
	endSection()
	return o
}

// Parse reads a master plan into its tasks, or returns the first problem.
//
// It returns only the first problem, although Scan finds them all, because
// that is what its callers are built around: the daemon refuses a plan at
// its first problem, and afk tasks and afk next print that one message. A
// plan with any problem yields no tasks, so nothing can be started from a
// plan that was only half understood. Use Scan to list every problem.
func Parse(data []byte) ([]Task, error) {
	o := Scan(data)
	if len(o.Problems) > 0 {
		return nil, o.Problems[0].Err
	}
	if len(o.Tasks) == 0 {
		return nil, ErrNoTasks
	}
	return o.Tasks, nil
}

func noDeps(t Task) error {
	return fmt.Errorf("line %d: task %s: %w", t.Line, t.ID, ErrNoDependsLine)
}

// readDepends turns the text after "Depends on:" into task ids. earlier is
// every task before this one, for "all prior".
func readDepends(raw string, earlier []Task) (deps []string, note string) {
	raw = strings.TrimSpace(raw)
	val := strings.TrimSpace(stripParens(raw))
	switch {
	case val == "", val == "—", val == "–", val == "-", strings.EqualFold(val, "none"):
		return nil, ""
	case strings.HasPrefix(strings.ToLower(val), "all prior"):
		for _, t := range earlier {
			deps = append(deps, t.ID)
		}
		return deps, ""
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(val, ",") {
		m := leadingIDRE.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			break
		}
		if !seen[m[1]] {
			seen[m[1]] = true
			deps = append(deps, m[1])
		}
	}
	if len(deps) == 0 {
		return nil, raw
	}
	return deps, ""
}

// stripParens removes every parenthesised span, nested ones included, and
// from an unclosed "(" to the end of the line. Wrapped Depends on lines
// leave exactly that unclosed form on their first line.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Validate checks that every dependency names a task in the plan and that
// there is no cycle. It walks in plan order so the error is deterministic.
func Validate(tasks []Task) error {
	index := make(map[string]int, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
	}
	for _, t := range tasks {
		for _, d := range t.DependsOn {
			if _, ok := index[d]; !ok {
				return fmt.Errorf("line %d: task %s depends on %s: %w", t.Line, t.ID, d, ErrUnknownDependency)
			}
		}
	}

	const (
		unseen = iota
		onPath
		finished
	)
	state := make([]int, len(tasks))
	var path []string
	var visit func(i int) error
	visit = func(i int) error {
		state[i] = onPath
		path = append(path, tasks[i].ID)
		for _, d := range tasks[i].DependsOn {
			j := index[d]
			switch state[j] {
			case onPath:
				start := 0
				for k, id := range path {
					if id == d {
						start = k
						break
					}
				}
				cyc := append(append([]string{}, path[start:]...), d)
				return fmt.Errorf("line %d: %s: %w", tasks[j].Line, strings.Join(cyc, " -> "), ErrCycle)
			case unseen:
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		state[i] = finished
		return nil
	}
	for i := range tasks {
		if state[i] == unseen {
			if err := visit(i); err != nil {
				return err
			}
		}
	}
	return nil
}
