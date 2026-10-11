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

// Parse reads a master plan. Code fences are handled first and exactly as
// internal/state does for work plans (a fence line toggles, fenced lines are
// skipped, an unclosed fence hides the rest of the file), so a heading shown
// as an example inside a fence is never a task.
func Parse(data []byte) ([]Task, error) {
	var tasks []Task
	seen := map[string]int{}
	cur := -1 // index of the task whose Depends on line is still wanted
	haveDeps := false
	inFence := false

	for i, raw := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		line := strings.TrimRight(raw, " \t\r")

		if lead := strings.TrimLeft(line, " \t"); strings.HasPrefix(lead, "```") || strings.HasPrefix(lead, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if anyHeadingRE.MatchString(line) {
			if cur >= 0 && !haveDeps {
				return nil, noDeps(tasks[cur])
			}
			cur, haveDeps = -1, false
			if m := taskHeadingRE.FindStringSubmatch(line); m != nil {
				if first, dup := seen[m[1]]; dup {
					return nil, fmt.Errorf("line %d: task %s already defined on line %d: %w", lineNo, m[1], first, ErrDuplicateTask)
				}
				seen[m[1]] = lineNo
				tasks = append(tasks, Task{ID: m[1], Title: strings.TrimSpace(m[2]), Line: lineNo})
				cur = len(tasks) - 1
			} else if badHeadingRE.MatchString(line) {
				return nil, fmt.Errorf("line %d: %q is not \"#### T<m>.<n> — <title>\": %w", lineNo, line, ErrBadHeading)
			}
			continue
		}

		if cur >= 0 && !haveDeps {
			if m := dependsRE.FindStringSubmatch(line); m != nil {
				haveDeps = true
				tasks[cur].DependsOn, tasks[cur].Note = readDepends(m[1], tasks[:cur])
			}
		}
	}
	if cur >= 0 && !haveDeps {
		return nil, noDeps(tasks[cur])
	}
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	return tasks, nil
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
