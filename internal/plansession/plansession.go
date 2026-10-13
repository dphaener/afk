// Package plansession reads the files of a planning session and decides which
// phase the session is in.
//
// A planning session is the attended agent session that takes an owner's idea
// to a checked first draft of the master plan. Its files live in `.afk/plan/`
// in the project's main checkout and are listed in docs/PLAN.md, M5, "The plan
// session's files": state.json, brief.md, survey.md, questions.jsonl,
// slices.md and critic.md. This package adds no seventh file.
//
// The state is on disk, and the phase is worked out from it, because of M5
// rule 7: a session can be left and picked up days later by a fresh agent with
// no memory of the first, and a later web page must be able to answer the same
// questions the terminal does. Nothing about the session is kept in a
// process, so there is nothing to lose when one ends.
//
// The phase is decided by code and never by the agent (M5 rule 6). An agent
// that tracks its own progress skips a step when it is sure the step is done;
// a gate that looks at the files cannot be talked past. So state.json records
// only the furthest phase the session was allowed to reach, Decide pulls the
// phase back when an earlier phase's file is missing or incomplete, and
// Advance is the one place that says whether the recorded phase may move.
//
// Decide and Advance take a Files value and touch neither the filesystem nor
// the clock. Load does the reading. That split is what lets every branch be a
// row in a table test, and it is also why PlanErrors is filled by the caller:
// counting the plan's errors needs the plan checker, and importing it here
// would tie the phase decision to the plan format.
package plansession

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The phases of a planning session, in the order docs/PLAN.md M5 gives them.
const (
	PhaseBrief    = "brief"
	PhaseSurvey   = "survey"
	PhaseModel    = "model"
	PhaseRounds   = "rounds"
	PhaseSlices   = "slices"
	PhaseDraft    = "draft"
	PhaseCheck    = "check"
	PhaseDesign   = "design"
	PhaseReview   = "review"
	PhaseApproved = "approved"
)

// Phases is every phase in order. The order is the meaning: "earlier" and
// "the following phase" are positions in this slice.
var Phases = []string{
	PhaseBrief, PhaseSurvey, PhaseModel, PhaseRounds, PhaseSlices,
	PhaseDraft, PhaseCheck, PhaseDesign, PhaseReview, PhaseApproved,
}

// The modes of a planning session: writing a new plan, or one of the three
// ways of changing an existing one.
const (
	ModeNew       = "new"
	ModeExtend    = "extend"
	ModeAmend     = "amend"
	ModeReconcile = "reconcile"
)

// Modes is every mode state.json may hold.
var Modes = []string{ModeNew, ModeExtend, ModeAmend, ModeReconcile}

// The two kinds of question (M5 rule 2). A product choice is asked with
// options and a recommendation; a setup fact is asked plainly.
const (
	KindProduct = "product"
	KindSetup   = "setup"
)

// The session's file names inside Dir.
const (
	stateFile     = "state.json"
	briefFile     = "brief.md"
	surveyFile    = "survey.md"
	questionsFile = "questions.jsonl"
	slicesFile    = "slices.md"
	criticFile    = "critic.md"
)

// shownDir is the folder as it is written in messages. Messages name the
// files relative to the project because that is where the owner runs afk.
const shownDir = ".afk/plan/"

// Dir is the planning session's folder in a project's main checkout.
func Dir(project string) string {
	return filepath.Join(project, ".afk", "plan")
}

// StatePath is the session's state file.
func StatePath(project string) string {
	return filepath.Join(Dir(project), stateFile)
}

// State is state.json: how the session was started and the furthest phase it
// was allowed to reach. It holds nothing that can be worked out from the
// other files, so there is nothing in it to drift from them.
type State struct {
	Mode  string `json:"mode"`
	Phase string `json:"phase"`
	// UpdatedAt is an RFC 3339 time in UTC. The caller supplies it (see
	// FormatTime) so that this package never reads the clock.
	UpdatedAt string `json:"updated_at"`
}

// FormatTime renders a time the way state.json and questions.jsonl hold one:
// RFC 3339, in UTC, to the second. One function so every writer agrees.
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// ReadState reads state.json. A missing file is not an error: it reports
// false, which is how "no planning session here" is told.
//
// Every other fault is an error that names the file. A damaged state file
// must never be read as "no session", because the next step after "no
// session" is to seed a new one at the first phase, which would silently
// throw away how far the owner had got.
func ReadState(project string) (State, bool, error) {
	path := StatePath(project)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("%s could not be read: %v", path, err)
	}

	// The keys are checked by hand, before the fields are decoded, for two
	// reasons. encoding/json matches a struct's keys without regard to case,
	// so "Phase" would be accepted as "phase"; and its own message for an
	// unknown key is not written for the person who has to fix the file.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return State{}, false, fmt.Errorf("%s is damaged: it is not a JSON object (%v). Fix the file by hand; afk will not guess the phase", path, err)
	}
	var unknown []string
	for key := range raw {
		switch key {
		case "mode", "phase", "updated_at":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return State{}, false, fmt.Errorf("%s has a key afk does not know: %q. The only keys are mode, phase and updated_at", path, unknown[0])
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, false, fmt.Errorf("%s is damaged: mode, phase and updated_at must each be text (%v)", path, err)
	}
	if err := checkState(s); err != nil {
		return State{}, false, fmt.Errorf("%s %v", path, err)
	}
	return s, true, nil
}

// checkState says what is wrong with a state's mode or phase, as the end of a
// sentence that starts with the file's path.
func checkState(s State) error {
	if s.Mode == "" {
		return fmt.Errorf("has no mode. It must be one of: %s", strings.Join(Modes, ", "))
	}
	if !contains(Modes, s.Mode) {
		return fmt.Errorf("has the mode %q, which afk does not know. It must be one of: %s", s.Mode, strings.Join(Modes, ", "))
	}
	if s.Phase == "" {
		return fmt.Errorf("has no phase. It must be one of: %s", strings.Join(Phases, ", "))
	}
	if phaseIndex(s.Phase) < 0 {
		return fmt.Errorf("has the phase %q, which afk does not know. It must be one of: %s", s.Phase, strings.Join(Phases, ", "))
	}
	return nil
}

// WriteState writes state.json, creating the folder when it is not there.
//
// It refuses a mode or phase that ReadState would reject. Without that check
// a caller's typo would be written successfully and only surface on the next
// read, as a "damaged" file the owner then has to repair by hand.
func WriteState(project string, s State) error {
	path := StatePath(project)
	if err := checkState(s); err != nil {
		return fmt.Errorf("refusing to write %s: the new state %v", path, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// writeAtomic writes data to path through a temp file in the same folder and
// a rename, so a reader never sees half a file. The rename must stay within
// one folder: across filesystems it is not atomic.
//
// It is a copy of the helper in internal/daemonstate rather than an import,
// so that this package depends on nothing else in afk.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Question is one line of questions.jsonl. The line is written when the
// question is asked and rewritten with the answer when it is answered, so an
// empty Answer means the question is still open.
type Question struct {
	ID          string   `json:"id"`
	Round       int      `json:"round"`
	Kind        string   `json:"kind"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended"`
	Answer      string   `json:"answer"`
	AnsweredAt  string   `json:"answered_at"`
}

// ParseQuestions reads questions.jsonl: one JSON object on each line that is
// not blank. The second value lists what is wrong, one plain sentence for
// each fault, each starting with its line number.
//
// A line with a fault is left out of the questions. Keeping it would let a
// half-written question count as asked, or as answered, and the rounds phase
// would then pass its gate on a record nobody can trust. Any fault holds the
// gate instead (see Decide), so the file is repaired before the session moves.
//
// Keys other than the eight in the M5 table are ignored rather than reported:
// the list of faults is the one the plan gives, and a misspelt "answer" still
// cannot slip through, because the question then reads as unanswered.
func ParseQuestions(data []byte) ([]Question, []string) {
	var questions []Question
	var problems []string
	firstLine := map[string]int{}

	// The lines are split by hand rather than with bufio.Scanner, which stops
	// with an error at a line longer than its buffer; a question with a long
	// answer is ordinary here.
	for i, line := range bytes.Split(data, []byte("\n")) {
		n := i + 1
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var q Question
		if err := json.Unmarshal(line, &q); err != nil {
			problems = append(problems, fmt.Sprintf("line %d: this line is not a question afk can read; it must be one JSON object (%v).", n, err))
			continue
		}

		var faults []string
		q.ID = strings.TrimSpace(q.ID)
		switch first, seen := firstLine[q.ID]; {
		case q.ID == "":
			faults = append(faults, "the question has no id")
		case seen:
			faults = append(faults, fmt.Sprintf("the id %q is already used on line %d", q.ID, first))
		default:
			firstLine[q.ID] = n
		}
		if q.Round < 1 {
			faults = append(faults, fmt.Sprintf("the round is %d, and rounds start at 1", q.Round))
		}
		if strings.TrimSpace(q.Question) == "" {
			faults = append(faults, "the question text is empty")
		}
		switch q.Kind {
		case KindProduct:
			switch {
			case len(q.Options) == 0:
				faults = append(faults, "a product question needs options to choose from, and this one has none")
			case q.Recommended == "":
				faults = append(faults, "a product question needs one recommended option, and this one has none")
			case !contains(q.Options, q.Recommended):
				faults = append(faults, fmt.Sprintf("the recommended answer %q is not one of the question's options", q.Recommended))
			}
		case KindSetup:
			if len(q.Options) > 0 {
				faults = append(faults, "a setup question is asked plainly, and this one has options")
			}
		default:
			faults = append(faults, fmt.Sprintf("the kind is %q, and it must be product or setup", q.Kind))
		}

		if len(faults) > 0 {
			for _, fault := range faults {
				problems = append(problems, fmt.Sprintf("line %d: %s.", n, fault))
			}
			continue
		}
		questions = append(questions, q)
	}
	return questions, problems
}

// Files is everything the phase decision reads, as plain values. Load fills
// it from disk; a test fills it by hand.
type Files struct {
	// HasState is true when state.json was read and is sound. A damaged
	// state.json leaves it false and sets StateProblem, and Decide still
	// reports a session: see Decide.
	HasState     bool
	State        State
	StateProblem string

	// Brief, Survey, Slices and Critic say that the file exists and is not
	// blank. An empty file is a file somebody started, not a finished one.
	Brief  bool
	Survey bool
	Slices bool
	Critic bool

	// QuestionsPresent is true when questions.jsonl exists, even when empty.
	QuestionsPresent bool
	Questions        []Question
	QuestionProblems []string

	// PlanExists says that the master plan file exists and is not blank.
	PlanExists bool
	// PlanErrors is the number of errors the plan checker reports. Load never
	// sets it: the caller runs the checker, so that this package does not
	// depend on the plan format.
	PlanErrors int
}

// Load reads a project's planning session into a Files value. planPath is the
// master plan; a relative path is taken from the project folder, and an empty
// one means there is no plan.
//
// It never returns an error. Its callers print a status, and a status that
// cannot be printed because one file is damaged tells the owner nothing; the
// fault is carried in the value (StateProblem, QuestionProblems) and shown.
func Load(project, planPath string) Files {
	dir := Dir(project)
	var f Files

	state, ok, err := ReadState(project)
	switch {
	case err != nil:
		f.StateProblem = err.Error()
	case ok:
		f.HasState = true
		f.State = state
	}

	f.Brief = notBlank(filepath.Join(dir, briefFile))
	f.Survey = notBlank(filepath.Join(dir, surveyFile))
	f.Slices = notBlank(filepath.Join(dir, slicesFile))
	f.Critic = notBlank(filepath.Join(dir, criticFile))

	questionsPath := filepath.Join(dir, questionsFile)
	data, err := os.ReadFile(questionsPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		// Something is there but cannot be read (a folder, or no permission).
		// It is reported as a problem so the gate holds, rather than being
		// read as "no questions yet".
		f.QuestionsPresent = true
		f.QuestionProblems = []string{fmt.Sprintf("%s could not be read: %v.", shownDir+questionsFile, err)}
	default:
		f.QuestionsPresent = true
		f.Questions, f.QuestionProblems = ParseQuestions(data)
	}

	if planPath != "" {
		if !filepath.IsAbs(planPath) {
			planPath = filepath.Join(project, planPath)
		}
		f.PlanExists = notBlank(planPath)
	}
	return f
}

// notBlank reports whether path is a file holding something other than white
// space. Any failure to read it counts as blank: for a gate, a file that
// cannot be read is a file whose work cannot be shown to be done.
func notBlank(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(data)) > 0
}

// Status is what a planning session is doing now.
type Status struct {
	// Session is false only when there is no state.json at all.
	Session bool
	Mode    string
	// Recorded is the phase in state.json: the furthest the session was
	// allowed to reach.
	Recorded string
	// Phase is where the work actually is: Recorded, or an earlier phase
	// whose file has gone missing or is incomplete. It is empty when
	// state.json is damaged, because then nobody knows.
	Phase string
	// Waiting is one plain sentence saying what the phase is waiting for.
	Waiting string
	// Problems are faults in the session's files, each a plain sentence.
	Problems []string
}

// Decide says which phase a planning session is in and what it waits for.
// It is pure: the same Files always gives the same Status.
//
// The phase is the earliest phase before the recorded one whose exit gate is
// not met, and otherwise the recorded one. So a phase is reached only when
// every earlier phase's file exists and is complete, and a session that is
// picked up again lands on the first phase with work left, whatever
// state.json says. The recorded phase is still needed: `model`, `design` and
// `review` have no file of their own, so only the record can say they are
// done.
//
// A damaged state.json is a session, not the absence of one. Reporting "no
// session" would invite `afk plan` to seed a fresh state over it.
func Decide(f Files) Status {
	problems := append([]string(nil), f.QuestionProblems...)

	if f.StateProblem != "" {
		return Status{
			Session:  true,
			Waiting:  "The planning session's state file is damaged and must be fixed before the session can go on.",
			Problems: append([]string{f.StateProblem}, problems...),
		}
	}
	if !f.HasState {
		return Status{
			Waiting:  "There is no planning session here. Run `afk plan` to start one.",
			Problems: problems,
		}
	}

	st := Status{Session: true, Mode: f.State.Mode, Recorded: f.State.Phase, Problems: problems}
	recorded := phaseIndex(f.State.Phase)
	if recorded < 0 {
		// Load cannot produce this (ReadState rejects the phase), but Files is
		// a plain value and a caller can build one by hand.
		st.Waiting = "The planning session's recorded phase is not one afk knows, so the state file must be fixed."
		st.Problems = append([]string{fmt.Sprintf("%s has the phase %q, which afk does not know.", shownDir+stateFile, f.State.Phase)}, problems...)
		return st
	}

	st.Phase = f.State.Phase
	for _, phase := range Phases[:recorded] {
		if missing(phase, f) != "" {
			st.Phase = phase
			break
		}
	}
	st.Waiting = missing(st.Phase, f)
	if st.Waiting == "" {
		st.Waiting = waitingWhenComplete(st.Phase)
	}
	return st
}

// Advance says which phase the session may move to next, or why it may not.
// It is pure; the caller writes the new phase with WriteState.
//
// It refuses when an earlier phase's file is missing as well as when the
// recorded phase's own gate is not met. Moving the record forward while an
// earlier file is gone would put it further from where the work really is.
func Advance(f Files) (string, error) {
	st := Decide(f)
	if f.StateProblem != "" {
		return "", errors.New(f.StateProblem)
	}
	if !st.Session {
		return "", errors.New("There is no planning session here. Run `afk plan` to start one.")
	}
	if st.Phase == "" {
		return "", errors.New(st.Problems[0])
	}
	if st.Phase != st.Recorded {
		return "", fmt.Errorf("The session reached the %s phase, but the earlier %s phase is not finished. %s", st.Recorded, st.Phase, st.Waiting)
	}
	if reason := missing(st.Recorded, f); reason != "" {
		return "", fmt.Errorf("The %s phase is not finished. %s", st.Recorded, reason)
	}

	switch st.Recorded {
	case PhaseCheck:
		// The design phase belongs to a later task (T5.5) and is not built.
		// Until it is, a session goes straight from check to review, so that
		// no session is ever recorded at a phase with nothing behind it.
		return PhaseReview, nil
	case PhaseReview, PhaseApproved:
		// Leaving review is approval, and approval is an owner's act with its
		// own command. Advance is run by the agent, so it must not grant it.
		return "", errors.New("The plan is waiting for your approval, which comes with `afk plan approve`. That command is not built yet, so the session stops here.")
	}
	return Phases[phaseIndex(st.Recorded)+1], nil
}

// missing says what a phase still lacks before it can be left, as one plain
// sentence, or "" when its exit gate is met. It is the single definition of
// every gate: Decide and Advance both read it, so they cannot disagree.
func missing(phase string, f Files) string {
	switch phase {
	case PhaseBrief:
		if !f.Brief {
			return "Your request has not been saved yet: " + shownDir + briefFile + " is missing or empty."
		}
	case PhaseSurvey:
		if !f.Survey {
			return "The survey of the folder and your profile has not been written yet: " + shownDir + surveyFile + " is missing or empty."
		}
	case PhaseRounds:
		return missingRounds(f)
	case PhaseSlices:
		if !f.Slices {
			return "The task breakdown has not been confirmed and saved yet: " + shownDir + slicesFile + " is missing or empty."
		}
	case PhaseDraft:
		if !f.PlanExists {
			return "The master plan has not been written yet."
		}
	case PhaseCheck:
		// Errors come first: the critic reviews a draft that passes the
		// checker, so its findings are not what to look at while errors stand.
		if f.PlanErrors > 0 {
			return fmt.Sprintf("The plan has %s that `afk plan lint` reports, and each must be fixed.", count(f.PlanErrors, "error", "errors"))
		}
		if !f.Critic {
			return "The critic has not reviewed the draft yet: " + shownDir + criticFile + " is missing or empty."
		}
	}
	// model, design, review and approved have no file. They are held by the
	// recorded phase alone.
	return ""
}

// missingRounds is the gate of the rounds phase: the questions file exists,
// holds at least one question, has no faults and every question is answered.
// Faults are reported before open questions because a faulty line is left out
// of the questions, so the count of open ones is not yet the true count.
func missingRounds(f Files) string {
	path := shownDir + questionsFile
	if !f.QuestionsPresent {
		return "No questions have been asked yet: " + path + " is missing."
	}
	if n := len(f.QuestionProblems); n > 0 {
		return fmt.Sprintf("%s has %s that must be fixed first.", path, count(n, "problem", "problems"))
	}
	if len(f.Questions) == 0 {
		return "No questions have been asked yet: " + path + " is empty."
	}
	open := 0
	for _, q := range f.Questions {
		if strings.TrimSpace(q.Answer) == "" {
			open++
		}
	}
	if open > 0 {
		if open == 1 {
			return fmt.Sprintf("1 question of %d has no answer yet.", len(f.Questions))
		}
		return fmt.Sprintf("%d questions of %d have no answer yet.", open, len(f.Questions))
	}
	return ""
}

// waitingWhenComplete is the sentence for a phase whose gate is met (or that
// has none): what happens there, and what moves the session on.
func waitingWhenComplete(phase string) string {
	switch phase {
	case PhaseBrief:
		return "Your request is saved; `afk plan advance` moves the session on to the survey."
	case PhaseSurvey:
		return "The survey is written; `afk plan advance` moves the session on to the domain model."
	case PhaseModel:
		return "The session is settling what the project's terms mean with you; `afk plan advance` moves it on once they are settled."
	case PhaseRounds:
		return "Every question has an answer; `afk plan advance` moves the session on to the task breakdown, or another round of questions can be asked first."
	case PhaseSlices:
		return "The task breakdown is saved; `afk plan advance` moves the session on to writing the plan."
	case PhaseDraft:
		return "The master plan is written; `afk plan advance` moves the session on to checking it."
	case PhaseCheck:
		return "The plan passes the checker and the critic has reviewed it; `afk plan advance` moves the session on to your review."
	case PhaseDesign:
		return "The design phase is not built yet; `afk plan advance` moves the session on to your review."
	case PhaseReview:
		return "The draft is ready for you to read; approving it with `afk plan approve` is not built yet."
	case PhaseApproved:
		return "The plan is approved, and the planning session has nothing left to do."
	}
	return ""
}

// phaseIndex is a phase's position in Phases, or -1 for an unknown phase.
func phaseIndex(phase string) int {
	for i, p := range Phases {
		if p == phase {
			return i
		}
	}
	return -1
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// count renders a number with its noun, so no message says "1 errors".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
