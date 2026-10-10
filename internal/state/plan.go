// Package state implements the READ-ONLY parsers for afk's three on-disk
// contracts: the plan file (`.afk/plans/{name}.md`), the session status
// file (`.afk/{name}/status.json`), and the review verdict file
// (`.afk/{name}/review-verdict.md`). This package never writes any file;
// it exists purely to feed the dashboard (W5.T2+) a faithful, validated view
// of what the conductor/orchestrator/reviewer already wrote.
//
// plan.go parses `.afk/plans/{name}.md` per the schema in
// plugins/afk/skills/planner/plan-format.md (in this repo). It
// extracts exactly what that schema defines — waves, task IDs, titles,
// checkbox states, Model tiers, and `**Depends on**` task dependencies — and
// invents nothing beyond it.
package state

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// CheckState is a task's checkbox state per plan-format.md's "Checkbox
// states" table.
type CheckState int

const (
	// CheckIncomplete is `- [ ]`.
	CheckIncomplete CheckState = iota
	// CheckComplete is `- [x]`.
	CheckComplete
	// CheckFailed is `- [!]`, always paired with a FAILED: reason.
	CheckFailed
)

// String renders the checkbox state for diagnostics and test output.
func (s CheckState) String() string {
	switch s {
	case CheckIncomplete:
		return "incomplete"
	case CheckComplete:
		return "complete"
	case CheckFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ModelTier is the `**Model**` value on a task, per plan-format.md's "Model
// Tier Definitions" table. There are exactly four valid tiers — the schema
// defines no others, so parsing an unrecognized value is an error rather
// than a silently-accepted new tier.
type ModelTier string

const (
	ModelLight     ModelTier = "light"
	ModelBalanced  ModelTier = "balanced"
	ModelHeavy     ModelTier = "heavy"
	ModelReasoning ModelTier = "reasoning"
)

func validModelTier(s string) (ModelTier, bool) {
	switch ModelTier(s) {
	case ModelLight, ModelBalanced, ModelHeavy, ModelReasoning:
		return ModelTier(s), true
	default:
		return "", false
	}
}

// Task is one `(WN.TM)` task block parsed from the Waves section.
type Task struct {
	// ID is the task ID exactly as it appears, e.g. "W1.T1".
	ID string
	// Wave is N and Num is M, parsed out of the ID for convenient sorting
	// and lookups.
	Wave int
	Num  int
	// Title is the short imperative phrase following the ID.
	Title string
	// State is the checkbox state.
	State CheckState
	// FailureReason is the text after "FAILED:" and is only set when
	// State == CheckFailed. plan-format.md requires every failed task to
	// carry a reason.
	FailureReason string
	// Model is the task's assigned model tier.
	Model ModelTier
	// DependsOn is the task IDs from the `**Depends on**` subsection that
	// plan-format.md defines between `**Files**` and
	// `**Acceptance Criteria**`: comma-separated task IDs from earlier
	// waves, or the literal `none`. nil means the field is absent (a legacy
	// plan, which keeps wave barriers); a non-nil empty slice means the
	// plan said `none`. Callers need that difference, so don't collapse it.
	// The parser records what the plan says and validates nothing: unknown
	// IDs, same-wave deps, mixed presence and cycles are all kept, because
	// the dashboard must still show progress for a plan the engine would
	// refuse to run. Scheduling decides what to do with them.
	DependsOn []string
}

// Wave is a `### Wave N: {name}` heading plus its tasks. plan-format.md
// disallows empty waves, so a successfully-parsed Wave always has >=1 Task.
type Wave struct {
	Number int
	Name   string
	Tasks  []Task
}

// Plan is the fully-parsed contents of a plan file.
type Plan struct {
	// Title is the top-level `# {Plan Title}` heading text.
	Title string
	Waves []Wave
}

var (
	planTitleRE = regexp.MustCompile(`^# (.+)$`)
	sectionRE   = regexp.MustCompile(`^## (.+)$`)
	waveRE      = regexp.MustCompile(`^### Wave (\d+): (.+)$`)
	// taskLooseRE matches any checkbox line, valid or not, so we can
	// surface a precise error for malformed checkbox states/IDs rather than
	// silently ignoring the line.
	taskLooseRE = regexp.MustCompile(`^- \[(.)\] (.*)$`)
	// taskIDRE peels the required `(WN.TM)` prefix off the remainder of a
	// checkbox line, leaving the title/reason text.
	taskIDRE = regexp.MustCompile(`^\((W(\d+)\.T(\d+))\)\s*(.*)$`)
	// failedReasonRE splits a failed task's remainder into title (optional)
	// and the mandatory FAILED: reason.
	failedReasonRE = regexp.MustCompile(`^(.*?)\s*FAILED:\s*(.*)$`)
	modelRE        = regexp.MustCompile(`^\s*\*\*Model\*\*:\s*(.+)$`)
	dependsRE      = regexp.MustCompile(`^\s*\*\*Depends on\*\*:\s*(.*)$`)
	// labelRE matches any `**Label**:` line, the same shape as upstream's
	// generic LABEL_LINE (plan_to_args.rb). A wrapped Depends on value ends
	// at ANY label line, not just the labels this parser knows, otherwise
	// an unrecognized label's text would be glued onto the dependency list.
	labelRE = regexp.MustCompile(`^\s*\*\*([^*]+)\*\*:\s*(.*)$`)
)

// ParsePlanFile reads and parses the plan file at path.
func ParsePlanFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading plan file %s: %w", path, err)
	}
	plan, err := ParsePlan(data)
	if err != nil {
		return nil, fmt.Errorf("parsing plan file %s: %w", path, err)
	}
	return plan, nil
}

// ParsePlan parses raw plan-file markdown per plan-format.md. It never
// panics: malformed input (bad checkbox states, missing task IDs, invalid
// Model tiers, empty waves, duplicate task IDs) is surfaced as an error with
// a line number, not a partial/best-effort result.
func ParsePlan(data []byte) (*Plan, error) {
	plan := &Plan{}

	inWaves := false
	var curWave *int // index into plan.Waves, or nil if no wave open yet
	var curTask *int // index into the current wave's Tasks, or nil
	// dependsRaw accumulates the wrapped `**Depends on**` value of the
	// current task, and dependsOpen says whether the next non-blank line
	// still continues it. dependsSeen makes the first line win when a task
	// repeats the label, as plan_to_args.rb does.
	var dependsRaw string
	dependsOpen := false
	dependsSeen := false

	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimRight(raw, " \t\r")

		if plan.Title == "" {
			if m := planTitleRE.FindStringSubmatch(line); m != nil {
				plan.Title = strings.TrimSpace(m[1])
				continue
			}
		}

		if m := sectionRE.FindStringSubmatch(line); m != nil {
			inWaves = strings.TrimSpace(m[1]) == "Waves"
			curWave = nil
			curTask = nil
			dependsOpen = false
			continue
		}

		if !inWaves {
			continue
		}

		if m := waveRE.FindStringSubmatch(line); m != nil {
			num, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("line %d: unparseable wave number %q", lineNo, m[1])
			}
			plan.Waves = append(plan.Waves, Wave{Number: num, Name: strings.TrimSpace(m[2])})
			idx := len(plan.Waves) - 1
			curWave = &idx
			curTask = nil
			dependsOpen = false
			continue
		}

		if m := taskLooseRE.FindStringSubmatch(line); m != nil {
			if curWave == nil {
				return nil, fmt.Errorf("line %d: task checkbox appears before any wave heading", lineNo)
			}
			task, err := parseTaskLine(lineNo, m[1], m[2])
			if err != nil {
				return nil, err
			}
			w := &plan.Waves[*curWave]
			w.Tasks = append(w.Tasks, *task)
			idx := len(w.Tasks) - 1
			curTask = &idx
			dependsOpen = false
			dependsSeen = false
			continue
		}

		// Any other heading, blank line, or label line ends a wrapped
		// Depends on value.
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") || labelRE.MatchString(line) {
			dependsOpen = false
		}

		if m := dependsRE.FindStringSubmatch(line); m != nil {
			// Unlike **Model**, a stray line with no task is ignored: the
			// field is optional and must never make a plan unparseable.
			if curWave == nil || curTask == nil || dependsSeen {
				continue
			}
			dependsSeen = true
			dependsOpen = true
			dependsRaw = strings.TrimSpace(m[1])
			plan.Waves[*curWave].Tasks[*curTask].DependsOn = parseDepends(dependsRaw)
			continue
		}

		if dependsOpen && curWave != nil && curTask != nil {
			dependsRaw = strings.TrimSpace(dependsRaw + " " + strings.TrimSpace(line))
			plan.Waves[*curWave].Tasks[*curTask].DependsOn = parseDepends(dependsRaw)
			continue
		}

		if m := modelRE.FindStringSubmatch(line); m != nil {
			if curWave == nil || curTask == nil {
				return nil, fmt.Errorf("line %d: **Model** line without a preceding task", lineNo)
			}
			tier, ok := validModelTier(strings.TrimSpace(m[1]))
			if !ok {
				return nil, fmt.Errorf("line %d: invalid Model tier %q (must be light, balanced, heavy, or reasoning)", lineNo, strings.TrimSpace(m[1]))
			}
			plan.Waves[*curWave].Tasks[*curTask].Model = tier
			continue
		}
	}

	if err := validatePlan(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// parseDepends splits a joined `**Depends on**` value into task IDs. Each
// item is trimmed of spaces and backticks, and items equal to `none` or `n/a`
// (any case) are dropped individually, so `W1.T1, none` yields [W1.T1]. The
// result is never nil: a present value with nothing left means "no deps".
func parseDepends(raw string) []string {
	deps := []string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(strings.ReplaceAll(item, "`", ""))
		switch strings.ToLower(item) {
		case "", "none", "n/a":
			continue
		}
		deps = append(deps, item)
	}
	return deps
}

// parseTaskLine interprets the contents of one `- [X] ...` line already
// split into its checkbox char and remainder by the caller.
func parseTaskLine(lineNo int, stateChar, rest string) (*Task, error) {
	var state CheckState
	switch stateChar {
	case " ":
		state = CheckIncomplete
	case "x":
		state = CheckComplete
	case "!":
		state = CheckFailed
	default:
		return nil, fmt.Errorf("line %d: invalid checkbox state %q (must be one of ' ', 'x', '!')", lineNo, stateChar)
	}

	m := taskIDRE.FindStringSubmatch(rest)
	if m == nil {
		return nil, fmt.Errorf("line %d: task checkbox line missing (WN.TM) task ID: %q", lineNo, rest)
	}
	id := m[1]
	waveNum, err := strconv.Atoi(m[2])
	if err != nil {
		return nil, fmt.Errorf("line %d: unparseable wave number in task ID %q", lineNo, id)
	}
	taskNum, err := strconv.Atoi(m[3])
	if err != nil {
		return nil, fmt.Errorf("line %d: unparseable task number in task ID %q", lineNo, id)
	}
	remainder := strings.TrimSpace(m[4])

	task := &Task{ID: id, Wave: waveNum, Num: taskNum, State: state}

	if state == CheckFailed {
		fm := failedReasonRE.FindStringSubmatch(remainder)
		if fm == nil {
			return nil, fmt.Errorf("line %d: failed task %s missing required FAILED: reason", lineNo, id)
		}
		task.Title = strings.TrimSpace(fm[1])
		task.FailureReason = strings.TrimSpace(fm[2])
		if task.FailureReason == "" {
			return nil, fmt.Errorf("line %d: failed task %s has an empty FAILED: reason", lineNo, id)
		}
	} else {
		task.Title = remainder
		if task.Title == "" {
			return nil, fmt.Errorf("line %d: task %s has no title", lineNo, id)
		}
	}

	return task, nil
}

// validatePlan enforces the plan-format.md rules that only make sense once
// the whole file has been seen: every wave has >=1 task (empty waves are
// disallowed), every task has a valid Model tier, task IDs are unique, and a
// plan title was found.
func validatePlan(plan *Plan) error {
	if plan.Title == "" {
		return fmt.Errorf("plan is missing its top-level title (# heading)")
	}
	if len(plan.Waves) == 0 {
		return fmt.Errorf("plan has no ## Waves section, or it contains no wave headings")
	}

	seen := map[string]bool{}
	for _, w := range plan.Waves {
		if len(w.Tasks) == 0 {
			return fmt.Errorf("wave %d (%s) has no tasks — empty waves are not allowed", w.Number, w.Name)
		}
		for _, t := range w.Tasks {
			if t.Model == "" {
				return fmt.Errorf("task %s is missing a **Model** tier", t.ID)
			}
			if seen[t.ID] {
				return fmt.Errorf("duplicate task ID %s — task IDs must be unique within a plan", t.ID)
			}
			seen[t.ID] = true
		}
	}
	return nil
}
