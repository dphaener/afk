// Package plansession reads the files of a planning session and decides which
// phase the session is in.
//
// A planning session is the attended agent session that takes an owner's idea
// to a checked first draft of the master plan, or that changes a plan that
// exists. Its files live in `.afk/plan/` in the project's main checkout and
// are listed in docs/PLAN.md, M5, "The plan session's files": state.json,
// brief.md, survey.md, questions.jsonl, slices.md and critic.md. A session
// that changes a plan (the extend, amend and reconcile modes) adds two more:
// changes.md, the list of changes it made, and the empty marker `yes`. The
// files of a session that is over are kept under `.afk/plan/archive/`.
//
// The phases a session goes through depend on its mode (see PhasesFor). A
// session that changes a plan has less to settle than one that writes a plan
// from nothing, so it takes a shorter path, and it ends at `review`: its
// changes are already in the plan by then, so there is nothing to approve.
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
// would tie the phase decision to the plan format. The four facts the `design`
// gate needs about the plan (see Files) are filled by the caller for the same
// reason.
//
// One file of a session lives outside `.afk/plan/`: the design brief, at
// docs/design-brief.md (DesignBriefFile). It is written in the `design` phase,
// which only a new plan for a project with an interface has (see Interface),
// and it is kept with the project's documents because the owner takes it to a
// design tool and the plan's design task names it.
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
	"unicode"
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

// Phases is every phase in order, and the path of a `new` session. The order
// is the meaning: "earlier" and "the following phase" are positions in this
// slice. The other modes walk a shorter path through the same names: see
// PhasesFor.
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

// PhasesFor is the path a session in the given mode walks, in order.
//
// The three modes that change a plan leave out what they do not need. None
// has `model` or `design`: the project's terms and its looks were settled
// when the plan was first written. Only `extend` breaks work into tasks, so
// only it has `slices`. `amend` has no `rounds` either: it changes one task,
// and what the owner wants is already in the brief. None reaches `approved`,
// because each change is in the plan as soon as it is made; there is no
// draft waiting to be approved, and the session is over at `review` (see
// Finished).
//
// Any other mode gets the `new` path. A mode afk does not know never comes
// from ReadState, which rejects it; this only decides what a Files value
// built by hand does, and the full path is the one that skips nothing.
//
// Each call returns a slice of its own, so a caller that changes what it got
// cannot change the path every later decision walks.
func PhasesFor(mode string) []string {
	switch mode {
	case ModeExtend:
		return []string{PhaseBrief, PhaseSurvey, PhaseRounds, PhaseSlices, PhaseDraft, PhaseCheck, PhaseReview}
	case ModeAmend:
		return []string{PhaseBrief, PhaseSurvey, PhaseDraft, PhaseCheck, PhaseReview}
	case ModeReconcile:
		return []string{PhaseBrief, PhaseSurvey, PhaseRounds, PhaseDraft, PhaseCheck, PhaseReview}
	}
	return append([]string(nil), Phases...)
}

// changesPlan reports whether the mode is one of the three that change a plan
// that exists. It is the one test every mode-dependent branch uses, so a mode
// afk does not know is treated as `new` everywhere or nowhere.
func changesPlan(mode string) bool {
	return mode == ModeExtend || mode == ModeAmend || mode == ModeReconcile
}

// Finished reports whether a session has nothing left to do: it is approved,
// or it changes a plan and has reached `review`.
//
// The two differ because of what `review` holds. A `new` session at `review`
// has a draft the owner has not approved yet, and it must be picked up again,
// not replaced. A session that changes a plan made each change through the
// guarded patch, so at `review` the changes are in the plan already and the
// next change may start a session of its own.
func Finished(s State) bool {
	if s.Phase == PhaseApproved {
		return true
	}
	return s.Phase == PhaseReview && changesPlan(s.Mode)
}

// The two kinds of question (M5 rule 2). A product choice is asked with
// options and a recommendation; a setup fact is asked plainly.
const (
	KindProduct = "product"
	KindSetup   = "setup"
)

// InterfaceQuestionID is the id of the round-one question that asks whether
// the project has an interface.
//
// It is fixed because the answer decides a phase, and the phase is decided by
// code (M5 rule 6). Code cannot tell which of a session's questions is the
// one about the interface by reading its wording, and it cannot tell from a
// free answer what was meant. So the question has one id that every session
// uses, and its answer starts with `yes` or `no` (see Interface). The
// master-plan skill's text names the same id; a change here must be made
// there too.
const InterfaceQuestionID = "r1-interface"

// DesignBriefFile is the design brief, as a slash path from the project
// folder: the prompt the owner pastes into a design tool.
//
// It is fixed, and exported, because three things must agree on it without
// reading each other: the `design` gate here, the skill's text that tells the
// agent where to write it, and the scope of the plan's design task, which
// tells the owner where to find it. It is in docs/ and not in `.afk/plan/`
// because it outlives the session: Archive moves the session's files away
// when the next one starts, and the owner may not have used the brief yet.
const DesignBriefFile = "docs/design-brief.md"

// The session's file names inside Dir.
const (
	stateFile     = "state.json"
	briefFile     = "brief.md"
	surveyFile    = "survey.md"
	questionsFile = "questions.jsonl"
	slicesFile    = "slices.md"
	criticFile    = "critic.md"
)

// The two files only a session that changes a plan has. They are exported
// because the command that starts such a session names them: it writes the
// marker, and it tells the owner where the list of changes is.
const (
	// ChangesFile lists every change the session made to the plan. It is the
	// `draft` gate of the three change modes: the plan exists before such a
	// session starts, so "the plan exists" proves nothing there.
	ChangesFile = "changes.md"
	// YesFile is an empty marker that says the owner already agreed to the
	// change, so the agent does not ask again. Nothing in this package reads
	// it; it is named here so that every file of the folder is named in one
	// place.
	YesFile = "yes"
)

// SummaryFile is the review summary every session mode writes before it
// prints it. It is exported because `afk plan status` names it in its output,
// and because every mode's `review` phase writes it.
const SummaryFile = "summary.md"

// archiveDir is the folder inside Dir that holds the sessions that are over.
const archiveDir = "archive"

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
	// The phase is checked against every phase, not against the mode's path.
	// A phase the mode never reaches is reported by Decide instead, with the
	// mode still readable, so `afk plan status` can say which session it is.
	if !contains(Phases, s.Phase) {
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

// Interface says whether the project has an interface, read from the answer
// to the round-one question with the id InterfaceQuestionID. It is pure.
//
// known is false when nothing can be said: no question has the id, or its
// answer does not start with the word `yes` or the word `no`. has is only
// meaningful when known is true, and is false otherwise, so a caller that
// reads has alone never enters the design phase on an answer nobody can read.
//
// Only the first word counts, in any case: the letters up to the first thing
// that is not a letter. The rest of the answer is the owner's own words for
// the kind of interface (`yes: a web page`, `no: a command line`) and is for
// people. The word must be whole so that `none`, `nope` and `yesterday` are
// not read as answers: a prefix test would take `none` for `no`, which
// happens to be right, and `not sure` for `no`, which is a guess.
//
// When two questions carry the id the first one is read. ParseQuestions
// never returns such a pair (a repeated id is a fault and the second line is
// left out), so this only settles what a Files value built by hand does.
func Interface(questions []Question) (has, known bool) {
	for _, q := range questions {
		if q.ID != InterfaceQuestionID {
			continue
		}
		answer := strings.ToLower(strings.TrimSpace(q.Answer))
		word := answer
		for i, r := range answer {
			if !unicode.IsLetter(r) {
				word = answer[:i]
				break
			}
		}
		switch word {
		case "yes":
			return true, true
		case "no":
			return false, true
		}
		return false, false
	}
	return false, false
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

	// Brief, Survey, Slices, Critic and Changes say that the file exists and
	// is not blank. An empty file is a file somebody started, not a finished
	// one.
	Brief  bool
	Survey bool
	Slices bool
	Critic bool
	// Changes is changes.md, which only a session that changes a plan writes.
	Changes bool

	// Summary is the text of summary.md exactly as written, and "" when the
	// file is missing, blank (only white space) or cannot be read. A missing
	// summary is never an error and never a Problem, and it gates nothing: it
	// is there for the owner to read after the session has ended.
	Summary string

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

	// DesignBrief says that the design brief (DesignBriefFile, in the project
	// and not in Dir) exists and is not blank. Load sets it.
	DesignBrief bool

	// The four fields below are what the `design` gate needs to know about the
	// plan. Load never sets them: like PlanErrors they are read from the plan's
	// text, so the caller fills them and this package stays free of the plan
	// format. They are read only for a new plan whose project has an interface
	// (see Interface); left at zero anywhere else they hold nothing back.

	// DesignRule says that the plan's header states which document wins: the
	// plan on behaviour, the design system on looks.
	DesignRule bool
	// DesignTask is the id of the plan's design task, the owner task titled
	// "Design system (owner)", or "" when the plan has none.
	DesignTask string
	// DesignTaskFirst says that the design task is in the plan's first
	// milestone. It means nothing while DesignTask is empty.
	DesignTaskFirst bool
	// DesignDependents is how many tasks wait for the design task.
	DesignDependents int
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
	f.Changes = notBlank(filepath.Join(dir, ChangesFile))
	if data, err := os.ReadFile(filepath.Join(dir, SummaryFile)); err == nil && len(bytes.TrimSpace(data)) > 0 {
		f.Summary = string(data)
	}
	f.DesignBrief = notBlank(filepath.Join(project, filepath.FromSlash(DesignBriefFile)))

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
// state.json says. The recorded phase is still needed: `model` and `review`
// have no file of their own, so only the record can say they are done. The
// same holds for `design` when the project has no interface: the phase is
// then passed over and has no gate. When the project has one, `design` has a
// gate like any other, so a session recorded at `review` whose design brief
// was deleted is back at `design`.
//
// "Earlier" is a position on the mode's own path (PhasesFor), so a phase the
// mode leaves out never holds a session back: an `amend` session is not
// waiting for a task breakdown it will never write.
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
	path := PhasesFor(f.State.Mode)
	recorded := phaseIndex(path, f.State.Phase)
	if recorded < 0 && contains(Phases, f.State.Phase) {
		// A real phase that this mode never reaches: state.json was edited by
		// hand, since Advance only ever hands out the next phase of the path.
		// It is reported, and the phase is left empty, because any guess at
		// where the work is would send the agent to do a step the mode has
		// not got.
		st.Waiting = fmt.Sprintf("The planning session's recorded phase is not one a session in the %s mode goes through, so the state file must be fixed.", f.State.Mode)
		st.Problems = append([]string{fmt.Sprintf("%s has the phase %q, which a session in the %s mode never reaches. Its phases are: %s.", shownDir+stateFile, f.State.Phase, f.State.Mode, strings.Join(path, ", "))}, problems...)
		return st
	}
	if recorded < 0 {
		// Load cannot produce this (ReadState rejects the phase), but Files is
		// a plain value and a caller can build one by hand.
		st.Waiting = "The planning session's recorded phase is not one afk knows, so the state file must be fixed."
		st.Problems = append([]string{fmt.Sprintf("%s has the phase %q, which afk does not know.", shownDir+stateFile, f.State.Phase)}, problems...)
		return st
	}

	st.Phase = f.State.Phase
	for _, phase := range path[:recorded] {
		if missing(phase, f) != "" {
			st.Phase = phase
			break
		}
	}
	st.Waiting = missing(st.Phase, f)
	if st.Waiting == "" {
		st.Waiting = waitingWhenComplete(st.Phase, f)
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

	if changesPlan(f.State.Mode) {
		path := PhasesFor(f.State.Mode)
		at := phaseIndex(path, st.Recorded)
		if at == len(path)-1 {
			// `review` is the end of the path, and unlike a new session's
			// review there is no approval to wait for: every change went into
			// the plan through the guarded patch as it was made.
			return "", errors.New("This planning session is finished and the changes are in the plan, so there is no later phase to move to. To change the plan again, run `afk plan`.")
		}
		return path[at+1], nil
	}

	switch st.Recorded {
	case PhaseCheck:
		// The design phase is for a project with an interface. A project
		// with none (a command line, an API) gives a design tool nothing to
		// draw, so its session goes straight from check to review: no session
		// is ever recorded at a phase with nothing behind it, and no agent is
		// sent to a phase prompt that has no work for it.
		//
		// The answer is always known here. A new session cannot leave
		// `rounds` without it (see missingRounds), and Decide has just put
		// this session at `check`, so `rounds` is complete. Were it unknown
		// all the same, review is the side to fall on: it is where the owner
		// reads the plan and can see what is missing.
		if has, _ := Interface(f.Questions); has {
			return PhaseDesign, nil
		}
		return PhaseReview, nil
	case PhaseReview:
		// Leaving review is approval, and approval is an owner's act with its
		// own command. Advance is run by the agent, so it must not grant it.
		return "", errors.New("The plan is waiting for your approval. Read it, then run `afk plan approve` to start building it.")
	case PhaseApproved:
		return "", errors.New("The plan is approved, so there is no later phase to move to.")
	}
	// Every other phase goes on to the one after it. That includes `design`,
	// which goes on to `review` whether it had a gate or not: a session
	// recorded there with no interface (the answer was changed, or the state
	// file was edited) has nothing to do and must not be stuck.
	return Phases[phaseIndex(Phases, st.Recorded)+1], nil
}

// ApproveVerdict is what `afk plan approve` may do for a set of loaded files.
type ApproveVerdict string

const (
	// ApproveGo means the plan is a reviewed draft and may be approved.
	ApproveGo ApproveVerdict = "go"
	// ApproveAlready means the session is already approved: nothing to do.
	ApproveAlready ApproveVerdict = "already"
)

// Approve says whether the plan may be approved. It is pure; the command
// does the work and writes `approved` with WriteState, last.
//
// `already` comes first among the verdicts so that running the command twice
// is harmless. Checker errors are tested before the phase Decide reports,
// because Decide pulls a session recorded at `review` back to `check` while
// the plan has errors, and the useful advice then is to run `afk plan lint`,
// not to finish the check phase. Change sessions are told apart with
// changesPlan (the question Finished asks): their changes went into the plan
// as they were made, so there is never a draft to approve.
func Approve(f Files) (ApproveVerdict, error) {
	st := Decide(f)
	if f.StateProblem != "" {
		return "", errors.New(f.StateProblem)
	}
	if !st.Session {
		return "", errors.New("There is no planning session here, so there is nothing to approve. Run `afk plan` to start one.")
	}
	if f.State.Phase == PhaseApproved {
		return ApproveAlready, nil
	}
	if changesPlan(f.State.Mode) {
		return "", fmt.Errorf("This planning session changes a plan that already exists (the %s mode), and its changes are in the plan already, so there is nothing to approve.", f.State.Mode)
	}
	if st.Phase == "" {
		return "", errors.New(st.Problems[0])
	}
	if st.Recorded == PhaseReview && f.PlanErrors > 0 {
		return "", fmt.Errorf("The plan cannot be approved yet. %s Run `afk plan lint` to see them, fix them, then run `afk plan approve` again.", planErrorsSentence(f.PlanErrors))
	}
	if st.Phase != PhaseReview {
		return "", fmt.Errorf("The plan is not ready to approve: the session is at the %s phase, not review. %s", st.Phase, st.Waiting)
	}
	return ApproveGo, nil
}

// missing says what a phase still lacks before it can be left, as one plain
// sentence, or "" when its exit gate is met. It is the single definition of
// every gate: Decide and Advance both read it, so they cannot disagree.
//
// Four gates depend on the session's mode, read from f.State.Mode: `rounds`
// (see missingRounds), `draft`, `check` and `design` (see missingDesign).
func missing(phase string, f Files) string {
	change := changesPlan(f.State.Mode)
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
		if change {
			// The plan was there before the session started, so its existence
			// says nothing about this session's work. The list of changes is
			// the only thing on disk that shows the plan was changed at all.
			if !f.Changes {
				return "The changes made to the plan have not been listed yet: " + shownDir + ChangesFile + " is missing or empty."
			}
			break
		}
		if !f.PlanExists {
			return "The master plan has not been written yet."
		}
	case PhaseCheck:
		// Errors come first: the critic reviews a draft that passes the
		// checker, so its findings are not what to look at while errors stand.
		if f.PlanErrors > 0 {
			return planErrorsSentence(f.PlanErrors)
		}
		// Of the change modes only `extend` has the critic: it adds a whole
		// milestone, which is enough new text to be worth a second reader.
		// One task, or a plan brought in line with a document, is not.
		if change && f.State.Mode != ModeExtend {
			break
		}
		if !f.Critic {
			return "The critic has not reviewed the draft yet: " + shownDir + criticFile + " is missing or empty."
		}
	case PhaseDesign:
		return missingDesign(f)
	}
	// model, review and approved have no file. They are held by the recorded
	// phase alone, and so is design for a project with no interface.
	return ""
}

// planErrorsSentence is the one sentence for a plan the checker rejects. The
// `check` and `design` gates both say it, word for word, so the owner reads
// the same thing whichever phase found the errors.
func planErrorsSentence(n int) string {
	return fmt.Sprintf("The plan has %s that `afk plan lint` reports, and each must be fixed.", count(n, "error", "errors"))
}

// missingDesign is the gate of the design phase. Only a new plan for a
// project with an interface has one: a session that changes a plan never
// reaches the phase, and with no interface there is nothing to hand to a
// design tool, so in both cases nothing is asked.
//
// The order is the order the work is done in, so the sentence always names
// the next thing to do. Checker errors come first for the same reason as in
// `check`, and because the three facts about the design task are read from
// the plan: a plan that does not parse reports no design task at all, and
// "the plan has no design task" would then send the agent to add one to a
// plan that has it. The errors are asked about here although `check` asks
// too, and comes earlier, so that this gate is true or false on its own and
// does not lean on the order of the phases: the phase changes the plan (it
// adds the task and the dependencies), and it must not be left with a plan
// it broke.
//
// Which tasks build part of the interface is the session's judgement and is
// not checked. What is checked is that the judgement was made at all: at
// least one task waits for the design task. With none, the design task would
// hold nothing back and every screen would be built before the design.
func missingDesign(f Files) string {
	if changesPlan(f.State.Mode) {
		return ""
	}
	if has, _ := Interface(f.Questions); !has {
		return ""
	}
	switch {
	case f.PlanErrors > 0:
		return planErrorsSentence(f.PlanErrors)
	case !f.DesignBrief:
		return "The design brief has not been written yet: " + DesignBriefFile + " is missing or empty."
	case !f.DesignRule:
		return "The plan's header does not say yet which document wins on what: it must say that the plan wins on behaviour and docs/design-system.md wins on looks."
	case f.DesignTask == "":
		return `The plan has no task titled "Design system (owner)" yet.`
	case !f.DesignTaskFirst:
		return fmt.Sprintf(`The task %s, "Design system (owner)", is not in the plan's first milestone, and it must be so that it can be done before anything with an interface is built.`, f.DesignTask)
	case f.DesignDependents == 0:
		return fmt.Sprintf(`No task waits for the design task %s yet: every task that builds part of the interface must name it in its "Depends on" line.`, f.DesignTask)
	}
	return ""
}

// missingRounds is the gate of the rounds phase: the questions file exists,
// holds at least one question, has no faults and every question is answered.
// Faults are reported before open questions because a faulty line is left out
// of the questions, so the count of open ones is not yet the true count.
//
// A `reconcile` session does not need a question. It asks the owner only
// about real choices between the plan and the document, and two texts that
// agree leave none, so there a missing or empty file passes. A question that
// was asked must still be answered, and a faulty line still holds the gate.
//
// A new session must also have settled whether the project has an interface,
// in the form code can read (see Interface), because that answer decides
// later whether the session has a design phase. It is asked for here, and not
// when the answer is needed at `check`, because here the owner is still being
// asked questions: at `check` the plan is written, and one written for the
// wrong kind of project would have to be written again. It comes last
// because the sentences before it are about questions as such, and an
// interface question with no answer yet is simply an open question. The
// sessions that change a plan are not asked: they have no round one, and the
// plan they change settled this when it was written.
func missingRounds(f Files) string {
	path := shownDir + questionsFile
	needsOne := f.State.Mode != ModeReconcile
	if !f.QuestionsPresent && needsOne {
		return "No questions have been asked yet: " + path + " is missing."
	}
	if n := len(f.QuestionProblems); n > 0 {
		return fmt.Sprintf("%s has %s that must be fixed first.", path, count(n, "problem", "problems"))
	}
	if len(f.Questions) == 0 && needsOne {
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
	if !changesPlan(f.State.Mode) {
		return missingInterface(f.Questions, path)
	}
	return ""
}

// missingInterface says what the interface answer still lacks, or "" when
// Interface can read it. The two faults get a sentence each because the fix
// differs: one is a question to ask the owner, the other an answer to write
// again in the fixed form without asking anything.
func missingInterface(questions []Question, path string) string {
	if _, known := Interface(questions); known {
		return ""
	}
	for _, q := range questions {
		if q.ID == InterfaceQuestionID {
			return fmt.Sprintf("The answer to the question with the id %s in %s must start with yes or no, and it is %q. Rewrite the answer as `yes: <kind>` or `no: <kind>`, for example `yes: a web page` or `no: a command line`.", InterfaceQuestionID, path, strings.TrimSpace(q.Answer))
		}
	}
	return fmt.Sprintf("%s needs a question with the id %s saying whether the project has an interface.", path, InterfaceQuestionID)
}

// waitingWhenComplete is the sentence for a phase whose gate is met (or that
// has none): what happens there, and what moves the session on.
//
// A session that changes a plan has its own sentences, because the ones for a
// new session name the phase that follows and say the plan is being written.
// On a shorter path both would be wrong.
func waitingWhenComplete(phase string, f Files) string {
	if changesPlan(f.State.Mode) {
		return waitingInChangeMode(phase, f)
	}
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
		// The sentence names the phase Advance will hand out, so it asks the
		// same question Advance does.
		if has, _ := Interface(f.Questions); has {
			return "The plan passes the checker and the critic has reviewed it; `afk plan advance` moves the session on to the design hand-off."
		}
		return "The plan passes the checker and the critic has reviewed it; `afk plan advance` moves the session on to your review."
	case PhaseDesign:
		// With no interface the phase has no gate, so "the brief is written"
		// would be said of a brief nobody wrote. A session only gets here
		// that way when the answer was changed after the phase was entered,
		// or the state file was edited.
		if has, _ := Interface(f.Questions); !has {
			return "The project has no interface, so there is no design to hand off; `afk plan advance` moves the session on to your review."
		}
		return "The design brief is written and the plan has your design task; `afk plan advance` moves the session on to your review."
	case PhaseReview:
		return "The draft is ready for you to read. When you are happy with it, run `afk plan approve` to start building it."
	case PhaseApproved:
		return "The plan is approved, and the planning session has nothing left to do."
	}
	return ""
}

// waitingInChangeMode is waitingWhenComplete for the three modes that change
// a plan. The phase that follows is looked up on the mode's path, so the
// sentence cannot name a phase the mode leaves out.
func waitingInChangeMode(phase string, f Files) string {
	mode := f.State.Mode
	path := PhasesFor(mode)
	at := phaseIndex(path, phase)
	if at < 0 {
		return ""
	}
	if at == len(path)-1 {
		return "The session is finished and the changes are in the plan."
	}

	var done string
	switch phase {
	case PhaseBrief:
		done = "Your request is saved"
	case PhaseSurvey:
		done = "The survey is written"
	case PhaseRounds:
		done = "Every question has an answer"
		if len(f.Questions) == 0 {
			// Only `reconcile` gets here with no question (see missingRounds).
			done = "No question is waiting for an answer"
		}
	case PhaseSlices:
		done = "The task breakdown is saved"
	case PhaseDraft:
		done = "The changes made to the plan are listed"
	case PhaseCheck:
		done = "The plan passes the checker"
		if mode == ModeExtend {
			done = "The plan passes the checker and the critic has reviewed the new milestone"
		}
	}

	var next string
	switch path[at+1] {
	case PhaseSurvey:
		next = "the survey"
	case PhaseRounds:
		next = "the questions"
	case PhaseSlices:
		next = "the task breakdown"
	case PhaseDraft:
		next = "changing the plan"
	case PhaseCheck:
		next = "checking the plan"
	case PhaseReview:
		next = "the summary of what changed"
	}
	return done + "; `afk plan advance` moves the session on to " + next + "."
}

// Archive moves the files of a planning session that is over out of the way
// of the next one. Every entry of `.afk/plan/` except the folder `archive`
// goes into `.afk/plan/archive/<time>/`, where the time is now in UTC, and
// that folder is returned.
//
// The files are kept and not overwritten because the brief is the owner's
// own words and is never edited after it is saved; a new session writing its
// brief over the old one would lose the request the plan's tasks came from.
// They are moved, and not left where they are, because the gates read these
// files: a new session that found the old survey.md would pass a gate on
// work it never did.
//
// It does nothing and returns "" when there is no `.afk/plan/` or nothing in
// it to move, so the first session on a project leaves no empty folder.
//
// Two sessions archived within one second get different folders (`-2`, `-3`
// after the time). The folder is made with Mkdir, which fails when the name
// is taken, so the check and the creation are one step and an older archive
// can never be merged into.
//
// A move that fails part of the way leaves some files in the archive and
// some in place; the folder is returned with the error so they can be found.
// It is not undone, because the undo could fail the same way.
func Archive(project string, now time.Time) (string, error) {
	dir := Dir(project)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s could not be read: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == archiveDir && entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return "", nil
	}

	root := filepath.Join(dir, archiveDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("the folder for old planning sessions, %s, could not be made: %v", root, err)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	target := filepath.Join(root, stamp)
	for n := 2; ; n++ {
		err := os.Mkdir(target, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("the folder for the old planning session, %s, could not be made: %v", target, err)
		}
		target = filepath.Join(root, fmt.Sprintf("%s-%d", stamp, n))
	}

	for _, name := range names {
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(target, name)); err != nil {
			return target, fmt.Errorf("the old planning session's file %s could not be moved to %s: %v. Move what is left in %s there by hand, then try again", name, target, err, dir)
		}
	}
	return target, nil
}

// phaseIndex is a phase's position on a path, or -1 when it is not on it.
func phaseIndex(path []string, phase string) int {
	for i, p := range path {
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
