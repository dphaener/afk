package plansession

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// complete is a session in which every phase's file exists and is complete,
// recorded at the given phase. Each row then breaks exactly one thing, so a
// row's name is the whole difference between it and a passing session.
func complete(recorded string) Files {
	return Files{
		HasState: true,
		State:    State{Mode: ModeNew, Phase: recorded, UpdatedAt: "2026-10-13T00:00:00Z"},
		Brief:    true,
		Survey:   true,
		Slices:   true,
		Critic:   true,

		QuestionsPresent: true,
		Questions: []Question{
			{ID: "q1", Round: 1, Kind: KindSetup, Question: "Who is it for?", Answer: "Me", AnsweredAt: "2026-10-13T00:00:00Z"},
			{ID: "q2", Round: 1, Kind: KindProduct, Question: "Web or CLI?", Options: []string{"web", "cli"}, Recommended: "web", Answer: "web"},
		},
		PlanExists: true,
	}
}

// with returns a complete session changed by one edit.
func with(recorded string, edit func(*Files)) Files {
	f := complete(recorded)
	edit(&f)
	return f
}

// TestDecide is the phase decision, one row per branch.
//
// The load-bearing rows are the "held back" ones and "resumed". They are the
// reason the phase is worked out from the files instead of read from
// state.json: an agent that picks a session up again, or one whose earlier
// file was deleted, must land on the first phase with work left. If Decide
// trusted the record, every one of those rows would report the recorded
// phase and a plan could be drafted from a breakdown that no longer exists.
//
// "damaged state is still a session" matters as much: reading a damaged
// state.json as "no session" would let `afk plan` seed a new state over it
// and lose how far the owner had got.
//
// `model`, `design` and `review` have no "held back" row because they have no
// file: nothing on disk can pull the phase back to them.
func TestDecide(t *testing.T) {
	tests := []struct {
		name        string
		files       Files
		wantSession bool
		wantPhase   string
		wantWaiting string // a fragment Waiting must contain
		wantProblem string // a fragment some problem must contain; "" for none
	}{
		// One row per phase reached: every earlier file is complete.
		{"reached brief", with(PhaseBrief, func(f *Files) { *f = Files{HasState: true, State: f.State} }), true, PhaseBrief, "brief.md is missing or empty", ""},
		{"reached survey", complete(PhaseSurvey), true, PhaseSurvey, "on to the domain model", ""},
		{"reached model", complete(PhaseModel), true, PhaseModel, "settling what the project's terms mean", ""},
		{"reached rounds", complete(PhaseRounds), true, PhaseRounds, "Every question has an answer", ""},
		{"reached slices", complete(PhaseSlices), true, PhaseSlices, "task breakdown is saved", ""},
		{"reached draft", complete(PhaseDraft), true, PhaseDraft, "master plan is written", ""},
		{"reached check", complete(PhaseCheck), true, PhaseCheck, "passes the checker", ""},
		{"reached design", complete(PhaseDesign), true, PhaseDesign, "design phase is not built yet", ""},
		{"reached review", complete(PhaseReview), true, PhaseReview, "afk plan approve", ""},
		{"reached approved", complete(PhaseApproved), true, PhaseApproved, "plan is approved", ""},

		// One row per way an earlier file holds the phase back.
		{"held back at brief: brief.md missing", with(PhaseApproved, func(f *Files) { f.Brief = false }), true, PhaseBrief, "brief.md is missing or empty", ""},
		{"held back at survey: survey.md missing", with(PhaseReview, func(f *Files) { f.Survey = false }), true, PhaseSurvey, "survey.md is missing or empty", ""},
		{"held back at rounds: questions.jsonl missing", with(PhaseReview, func(f *Files) { f.QuestionsPresent = false; f.Questions = nil }), true, PhaseRounds, "questions.jsonl is missing", ""},
		{"held back at rounds: no questions", with(PhaseReview, func(f *Files) { f.Questions = nil }), true, PhaseRounds, "questions.jsonl is empty", ""},
		{"held back at rounds: one question open", with(PhaseReview, func(f *Files) { f.Questions[1].Answer = "" }), true, PhaseRounds, "1 question of 2 has no answer yet", ""},
		{"held back at rounds: two questions open", with(PhaseReview, func(f *Files) { f.Questions[0].Answer = " "; f.Questions[1].Answer = "" }), true, PhaseRounds, "2 questions of 2 have no answer yet", ""},
		{"held back at rounds: a question problem", with(PhaseReview, func(f *Files) { f.QuestionProblems = []string{"line 3: the question has no id."} }), true, PhaseRounds, "has 1 problem that must be fixed", "line 3"},
		{"held back at slices: slices.md missing", with(PhaseReview, func(f *Files) { f.Slices = false }), true, PhaseSlices, "slices.md is missing or empty", ""},
		{"held back at draft: plan missing", with(PhaseReview, func(f *Files) { f.PlanExists = false }), true, PhaseDraft, "master plan has not been written", ""},
		{"held back at check: plan has errors", with(PhaseReview, func(f *Files) { f.PlanErrors = 3 }), true, PhaseCheck, "3 errors", ""},
		{"held back at check: one plan error", with(PhaseReview, func(f *Files) { f.PlanErrors = 1 }), true, PhaseCheck, "1 error that", ""},
		{"held back at check: critic.md missing", with(PhaseReview, func(f *Files) { f.Critic = false }), true, PhaseCheck, "critic.md is missing or empty", ""},

		// The earliest unmet gate wins, not the latest.
		{"two files missing lands on the earlier", with(PhaseReview, func(f *Files) { f.Slices = false; f.Survey = false }), true, PhaseSurvey, "survey.md", ""},
		{"resumed: recorded draft, slices.md deleted", with(PhaseDraft, func(f *Files) { f.Slices = false; f.PlanExists = false }), true, PhaseSlices, "slices.md is missing or empty", ""},

		// A later phase's file never pulls the phase forward or back.
		{"later file missing does not hold an earlier phase", with(PhaseModel, func(f *Files) { f.Slices = false; f.Critic = false; f.PlanExists = false }), true, PhaseModel, "settling", ""},
		{"own gate unmet stays on the recorded phase", with(PhaseSlices, func(f *Files) { f.Slices = false }), true, PhaseSlices, "slices.md is missing or empty", ""},
		{"a question problem is reported before rounds", with(PhaseSurvey, func(f *Files) { f.QuestionProblems = []string{"line 1: the question text is empty."} }), true, PhaseSurvey, "domain model", "line 1"},

		{"no state", Files{Brief: true}, false, "", "Run `afk plan` to start one", ""},
		{"damaged state is still a session", Files{StateProblem: "/p/.afk/plan/state.json is damaged: it is not a JSON object"}, true, "", "state file is damaged", "state.json is damaged"},
		{"unknown recorded phase", with("polish", func(*Files) {}), true, "", "not one afk knows", `the phase "polish"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.files)
			if got.Session != tt.wantSession {
				t.Errorf("Session = %v, want %v", got.Session, tt.wantSession)
			}
			if got.Phase != tt.wantPhase {
				t.Errorf("Phase = %q, want %q", got.Phase, tt.wantPhase)
			}
			if !strings.Contains(got.Waiting, tt.wantWaiting) {
				t.Errorf("Waiting = %q, want it to contain %q", got.Waiting, tt.wantWaiting)
			}
			if tt.wantSession && tt.files.HasState {
				if got.Recorded != tt.files.State.Phase || got.Mode != tt.files.State.Mode {
					t.Errorf("Recorded, Mode = %q, %q, want %q, %q", got.Recorded, got.Mode, tt.files.State.Phase, tt.files.State.Mode)
				}
			}
			joined := strings.Join(got.Problems, "\n")
			if tt.wantProblem == "" && len(got.Problems) != 0 {
				t.Errorf("Problems = %q, want none", got.Problems)
			}
			if !strings.Contains(joined, tt.wantProblem) {
				t.Errorf("Problems = %q, want one to contain %q", got.Problems, tt.wantProblem)
			}
		})
	}
}

// TestDecideNeverPastRecorded holds the other half of the rule: the files can
// pull the phase back but never push it on. A session recorded at `model`
// whose later files all happen to exist (left over from an earlier attempt)
// is still at `model`, because `model` has no file and only the record can
// say it is done.
func TestDecideNeverPastRecorded(t *testing.T) {
	for _, phase := range Phases {
		t.Run(phase, func(t *testing.T) {
			if got := Decide(complete(phase)); got.Phase != phase {
				t.Errorf("Decide with every file complete, recorded %q: Phase = %q", phase, got.Phase)
			}
		})
	}
}

// TestAdvance covers every move and every refusal.
//
// Three rows are load-bearing. "check skips design" is the stand-in for a
// phase that is not built: without it a session would be recorded at `design`
// with nothing to do there. "review refuses" and "approved refuses" keep
// approval out of the agent's hands, since the agent is what runs advance.
// And "an earlier file missing refuses" is the one place the two checks
// differ: the recorded phase's own gate is met there, so a rule that looked
// only at that gate would move the record further from the real work.
//
// `model` and `design` have no "gate not met" row because they have no gate.
func TestAdvance(t *testing.T) {
	tests := []struct {
		name    string
		files   Files
		want    string
		wantErr string // a fragment the error must contain; "" for no error
	}{
		{"brief, gate met", complete(PhaseBrief), PhaseSurvey, ""},
		{"brief, gate not met", with(PhaseBrief, func(f *Files) { f.Brief = false }), "", "brief.md is missing or empty"},
		{"survey, gate met", complete(PhaseSurvey), PhaseModel, ""},
		{"survey, gate not met", with(PhaseSurvey, func(f *Files) { f.Survey = false }), "", "survey.md is missing or empty"},
		{"model, no gate", complete(PhaseModel), PhaseRounds, ""},
		{"model, later files absent", with(PhaseModel, func(f *Files) {
			f.QuestionsPresent, f.Questions, f.Slices, f.PlanExists, f.Critic = false, nil, false, false, false
		}), PhaseRounds, ""},
		{"rounds, gate met", complete(PhaseRounds), PhaseSlices, ""},
		{"rounds, no questions file", with(PhaseRounds, func(f *Files) { f.QuestionsPresent = false; f.Questions = nil }), "", "questions.jsonl is missing"},
		{"rounds, no questions", with(PhaseRounds, func(f *Files) { f.Questions = nil }), "", "questions.jsonl is empty"},
		{"rounds, a question open", with(PhaseRounds, func(f *Files) { f.Questions[0].Answer = "" }), "", "1 question of 2 has no answer yet"},
		{"rounds, a question problem", with(PhaseRounds, func(f *Files) { f.QuestionProblems = []string{"line 2: the question has no id."} }), "", "1 problem"},
		{"slices, gate met", complete(PhaseSlices), PhaseDraft, ""},
		{"slices, gate not met", with(PhaseSlices, func(f *Files) { f.Slices = false }), "", "slices.md is missing or empty"},
		{"draft, gate met", complete(PhaseDraft), PhaseCheck, ""},
		{"draft, gate not met", with(PhaseDraft, func(f *Files) { f.PlanExists = false }), "", "master plan has not been written"},
		{"check skips design", complete(PhaseCheck), PhaseReview, ""},
		{"check, plan has errors", with(PhaseCheck, func(f *Files) { f.PlanErrors = 2 }), "", "2 errors"},
		{"check, no critic", with(PhaseCheck, func(f *Files) { f.Critic = false }), "", "critic.md is missing or empty"},
		{"design, no gate", complete(PhaseDesign), PhaseReview, ""},
		{"review refuses", complete(PhaseReview), "", "afk plan approve"},
		{"approved refuses", complete(PhaseApproved), "", "afk plan approve"},

		{"an earlier file missing refuses", with(PhaseDraft, func(f *Files) { f.Slices = false }), "", "earlier slices phase is not finished"},
		{"review with an earlier file missing names the file", with(PhaseReview, func(f *Files) { f.Brief = false }), "", "brief.md is missing or empty"},
		{"no session", Files{}, "", "Run `afk plan` to start one"},
		{"damaged state", Files{StateProblem: "/p/.afk/plan/state.json is damaged"}, "", "state.json is damaged"},
		{"unknown recorded phase", complete("polish"), "", `the phase "polish"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Advance(tt.files)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Advance() error = %v, want %q", err, tt.want)
				}
				if got != tt.want {
					t.Errorf("Advance() = %q, want %q", got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Advance() = %q, want an error containing %q", got, tt.wantErr)
			}
			if got != "" {
				t.Errorf("Advance() = %q with an error, want no phase", got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Advance() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestParseQuestions has one row per fault the parser reports.
//
// The load-bearing part is the pair of assertions on every fault row: the
// sentence carries the line number (the owner or the agent has to find the
// line in a file that is one long line per question), and the faulty line is
// left out of the questions, so it can never count as asked or as answered
// when the rounds gate is checked. "blank lines keep the numbering" pins the
// number to the line in the file, not to the count of questions.
func TestParseQuestions(t *testing.T) {
	const good = `{"id":"q1","round":1,"kind":"setup","question":"Who is it for?","answer":"Me","answered_at":"2026-10-13T00:00:00Z"}`
	tests := []struct {
		name     string
		data     string
		wantIDs  []string
		problems []string // one fragment per expected problem, in order
	}{
		{"empty file", "", nil, nil},
		{"only blank lines", "\n  \n\t\n", nil, nil},
		{"one setup question", good + "\n", []string{"q1"}, nil},
		{"no final newline and CRLF", good + "\r\n" + `{"id":"q2","round":2,"kind":"setup","question":"Where?"}`, []string{"q1", "q2"}, nil},
		{"a product question", `{"id":"q1","round":1,"kind":"product","question":"Web or CLI?","options":["web","cli"],"recommended":"web"}`, []string{"q1"}, nil},
		{"an unknown key is ignored", `{"id":"q1","round":1,"kind":"setup","question":"Where?","note":"x"}`, []string{"q1"}, nil},

		{"not JSON", "not json\n" + good, []string{"q1"}, []string{"line 1: this line is not a question afk can read"}},
		{"a JSON array", "[1,2]", nil, []string{"line 1: this line is not a question afk can read"}},
		{"round is text", `{"id":"q1","round":"1","kind":"setup","question":"Where?"}`, nil, []string{"line 1: this line is not a question afk can read"}},
		{"empty id", `{"id":" ","round":1,"kind":"setup","question":"Where?"}`, nil, []string{"line 1: the question has no id"}},
		{"duplicate id", good + "\n" + good, []string{"q1"}, []string{`line 2: the id "q1" is already used on line 1`}},
		{"round below 1", `{"id":"q1","kind":"setup","question":"Where?"}`, nil, []string{"line 1: the round is 0, and rounds start at 1"}},
		{"unknown kind", `{"id":"q1","round":1,"kind":"design","question":"Where?"}`, nil, []string{`line 1: the kind is "design"`}},
		{"missing kind", `{"id":"q1","round":1,"question":"Where?"}`, nil, []string{`line 1: the kind is ""`}},
		{"empty question", `{"id":"q1","round":1,"kind":"setup","question":"  "}`, nil, []string{"line 1: the question text is empty"}},
		{"product with no options", `{"id":"q1","round":1,"kind":"product","question":"Web or CLI?","recommended":"web"}`, nil, []string{"line 1: a product question needs options"}},
		{"product with no recommendation", `{"id":"q1","round":1,"kind":"product","question":"Web or CLI?","options":["web","cli"]}`, nil, []string{"line 1: a product question needs one recommended option"}},
		{"product recommending something else", `{"id":"q1","round":1,"kind":"product","question":"Web or CLI?","options":["web","cli"],"recommended":"both"}`, nil, []string{`line 1: the recommended answer "both" is not one of the question's options`}},
		{"setup with options", `{"id":"q1","round":1,"kind":"setup","question":"Where?","options":["here"]}`, nil, []string{"line 1: a setup question is asked plainly"}},

		{"two faults on one line", `{"id":"","round":0,"kind":"setup","question":"Where?"}`, nil, []string{"line 1: the question has no id", "line 1: the round is 0"}},
		{"blank lines keep the numbering", "\n" + good + "\n\n" + `{"id":"q2","round":1,"kind":"nope","question":"Where?"}`, []string{"q1"}, []string{"line 4: the kind is"}},
		{"a faulty line still holds its id", `{"id":"q1","round":0,"kind":"setup","question":"Where?"}` + "\n" + good, nil, []string{"line 1: the round is 0", `line 2: the id "q1" is already used on line 1`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			questions, problems := ParseQuestions([]byte(tt.data))
			var ids []string
			for _, q := range questions {
				ids = append(ids, q.ID)
			}
			if !reflect.DeepEqual(ids, tt.wantIDs) {
				t.Errorf("question ids = %q, want %q", ids, tt.wantIDs)
			}
			if len(problems) != len(tt.problems) {
				t.Fatalf("problems = %q, want %d of them", problems, len(tt.problems))
			}
			for i, want := range tt.problems {
				if !strings.HasPrefix(problems[i], want) {
					t.Errorf("problem %d = %q, want it to start with %q", i, problems[i], want)
				}
			}
		})
	}
}

// TestParseQuestionsFields checks that every key of the M5 table lands in its
// field. A renamed JSON tag would otherwise pass the fault rows above, since
// a key nobody reads is simply ignored.
func TestParseQuestionsFields(t *testing.T) {
	line := `{"id":"q7","round":2,"kind":"product","question":"Web or CLI?","options":["web","cli"],"recommended":"web","answer":"cli","answered_at":"2026-10-13T09:30:00Z"}`
	got, problems := ParseQuestions([]byte(line))
	if len(problems) != 0 {
		t.Fatalf("problems = %q, want none", problems)
	}
	want := []Question{{
		ID: "q7", Round: 2, Kind: KindProduct, Question: "Web or CLI?",
		Options: []string{"web", "cli"}, Recommended: "web",
		Answer: "cli", AnsweredAt: "2026-10-13T09:30:00Z",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseQuestions() = %+v, want %+v", got, want)
	}
}

// writeFile writes one file of a test's project, creating its folder.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadState covers a sound file, a missing one and each kind of damage.
//
// The load-bearing split is between the "missing file" row and every damage
// row. Missing is the only case that reports "no session" with no error.
// Each damaged file must come back as an error naming state.json, and never
// as (zero, false, nil): that reading would let `afk plan` seed a new state
// over the file and send the owner back to the first phase.
func TestReadState(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" with missing set means no file is written
		missing bool
		want    State
		wantOK  bool
		wantErr string // a fragment the error must contain; "" for no error
	}{
		{name: "missing file", missing: true},
		{name: "a sound file", content: `{"mode":"new","phase":"brief","updated_at":"2026-10-13T00:00:00Z"}`, want: State{ModeNew, PhaseBrief, "2026-10-13T00:00:00Z"}, wantOK: true},
		{name: "every mode and the last phase", content: `{"mode":"reconcile","phase":"approved","updated_at":""}`, want: State{ModeReconcile, PhaseApproved, ""}, wantOK: true},

		{name: "damaged file: not JSON", content: `{"mode":"new",`, wantErr: "is damaged: it is not a JSON object"},
		{name: "damaged file: empty", content: "", wantErr: "is damaged: it is not a JSON object"},
		{name: "damaged file: a JSON array", content: `["new","brief"]`, wantErr: "is damaged: it is not a JSON object"},
		{name: "damaged file: phase is a number", content: `{"mode":"new","phase":3}`, wantErr: "must each be text"},
		{name: "unknown key", content: `{"mode":"new","phase":"brief","step":2}`, wantErr: `has a key afk does not know: "step"`},
		{name: "unknown key: wrong case", content: `{"mode":"new","Phase":"brief"}`, wantErr: `has a key afk does not know: "Phase"`},
		{name: "unknown mode", content: `{"mode":"rewrite","phase":"brief"}`, wantErr: `has the mode "rewrite", which afk does not know`},
		{name: "missing mode", content: `{"phase":"brief"}`, wantErr: "has no mode"},
		{name: "unknown phase", content: `{"mode":"new","phase":"polish"}`, wantErr: `has the phase "polish", which afk does not know`},
		{name: "missing phase", content: `{"mode":"new"}`, wantErr: "has no phase"},
		{name: "null", content: `null`, wantErr: "has no mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			if !tt.missing {
				writeFile(t, StatePath(project), tt.content)
			}
			got, ok, err := ReadState(project)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ReadState() = %+v, %v, nil; want an error containing %q", got, ok, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ReadState() error = %q, want it to contain %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), StatePath(project)) {
					t.Errorf("ReadState() error = %q, want it to name %s", err, StatePath(project))
				}
				if !strings.Contains(err.Error(), "state.json") {
					t.Errorf("ReadState() error = %q, want it to name state.json", err)
				}
				if ok || got != (State{}) {
					t.Errorf("ReadState() = %+v, %v with an error; want the zero state and false", got, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadState() error = %v", err)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ReadState() = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestWriteState is the round trip and the refusals.
//
// "creates the folder" is load-bearing: `afk plan` seeds the state in a
// project that has never had a `.afk/plan/`. The refusal rows hold the
// promise that WriteState never writes a file ReadState would call damaged,
// and they check that a refused write leaves the old file as it was.
func TestWriteState(t *testing.T) {
	stamp := FormatTime(time.Date(2026, 10, 13, 14, 5, 0, 0, time.FixedZone("EST", -5*3600)))
	if stamp != "2026-10-13T19:05:00Z" {
		t.Fatalf("FormatTime() = %q, want the time in UTC", stamp)
	}

	tests := []struct {
		name    string
		state   State
		wantErr string
	}{
		{"creates the folder and round trips", State{ModeNew, PhaseBrief, stamp}, ""},
		{"another mode and phase", State{ModeAmend, PhaseReview, stamp}, ""},
		{"unknown mode refused", State{"rewrite", PhaseBrief, stamp}, `the mode "rewrite"`},
		{"empty mode refused", State{"", PhaseBrief, stamp}, "has no mode"},
		{"unknown phase refused", State{ModeNew, "polish", stamp}, `the phase "polish"`},
		{"empty phase refused", State{ModeNew, "", stamp}, "has no phase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			err := WriteState(project, tt.state)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("WriteState() error = %v, want one containing %q", err, tt.wantErr)
				}
				if _, statErr := os.Stat(Dir(project)); !os.IsNotExist(statErr) {
					t.Errorf("a refused write created %s", Dir(project))
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteState() error = %v", err)
			}
			got, ok, err := ReadState(project)
			if err != nil || !ok || got != tt.state {
				t.Errorf("ReadState() after WriteState() = %+v, %v, %v; want %+v, true, nil", got, ok, err, tt.state)
			}
			data, err := os.ReadFile(StatePath(project))
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{`"mode"`, `"phase"`, `"updated_at"`} {
				if !strings.Contains(string(data), key) {
					t.Errorf("state.json = %s, want the key %s", data, key)
				}
			}
			entries, err := os.ReadDir(Dir(project))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "state.json" {
				t.Errorf("%s holds %v, want only state.json (no temp file left behind)", Dir(project), entries)
			}
		})
	}

	t.Run("a refused write keeps the old file", func(t *testing.T) {
		project := t.TempDir()
		old := State{ModeNew, PhaseSurvey, stamp}
		if err := WriteState(project, old); err != nil {
			t.Fatal(err)
		}
		if err := WriteState(project, State{ModeNew, "polish", stamp}); err == nil {
			t.Fatal("WriteState() with an unknown phase succeeded")
		}
		if got, _, err := ReadState(project); err != nil || got != old {
			t.Errorf("after a refused write ReadState() = %+v, %v; want %+v", got, err, old)
		}
	})
}

// TestLoad reads real files from a temp folder.
//
// The load-bearing rows are the ones where a file is present but does not
// count: a blank brief, a damaged state, an unreadable questions file. Each
// could be mistaken for "done" or for "nothing here", and each would then
// move the phase the wrong way. "damaged state" also holds Load's promise
// never to fail: the fault arrives as StateProblem, so `afk plan status` can
// still print it.
func TestLoad(t *testing.T) {
	const state = `{"mode":"new","phase":"check","updated_at":"2026-10-13T00:00:00Z"}`
	const questions = `{"id":"q1","round":1,"kind":"setup","question":"Who is it for?","answer":"Me"}` + "\n" +
		`{"id":"q1","round":1,"kind":"setup","question":"Again?"}` + "\n"

	tests := []struct {
		name     string
		files    map[string]string // path under the project -> content
		dirs     []string
		planPath string
		check    func(t *testing.T, project string, f Files)
	}{
		{
			name: "an empty project",
			check: func(t *testing.T, _ string, f Files) {
				if !reflect.DeepEqual(f, Files{}) {
					t.Errorf("Load() = %+v, want the zero value", f)
				}
				if Decide(f).Session {
					t.Error("an empty project decided as a session")
				}
			},
		},
		{
			name: "every file present",
			files: map[string]string{
				".afk/plan/state.json":      state,
				".afk/plan/brief.md":        "Build me a thing.\n",
				".afk/plan/survey.md":       "Read the folder.\n",
				".afk/plan/questions.jsonl": `{"id":"q1","round":1,"kind":"setup","question":"Who is it for?","answer":"Me"}` + "\n",
				".afk/plan/slices.md":       "1. First\n",
				".afk/plan/critic.md":       "No findings.\n",
				"docs/PLAN.md":              "# Plan\n",
			},
			planPath: "docs/PLAN.md",
			check: func(t *testing.T, _ string, f Files) {
				want := Files{
					HasState: true, State: State{ModeNew, PhaseCheck, "2026-10-13T00:00:00Z"},
					Brief: true, Survey: true, Slices: true, Critic: true,
					QuestionsPresent: true,
					Questions:        []Question{{ID: "q1", Round: 1, Kind: KindSetup, Question: "Who is it for?", Answer: "Me"}},
					PlanExists:       true,
				}
				if !reflect.DeepEqual(f, want) {
					t.Errorf("Load() = %+v\nwant     %+v", f, want)
				}
				if got := Decide(f); got.Phase != PhaseCheck {
					t.Errorf("Decide(Load()).Phase = %q, want check", got.Phase)
				}
				if next, err := Advance(f); err != nil || next != PhaseReview {
					t.Errorf("Advance(Load()) = %q, %v; want review", next, err)
				}
			},
		},
		{
			name: "blank files do not count",
			files: map[string]string{
				".afk/plan/state.json":      state,
				".afk/plan/brief.md":        " \n\t\n",
				".afk/plan/survey.md":       "",
				".afk/plan/slices.md":       "\n",
				".afk/plan/critic.md":       "  ",
				".afk/plan/questions.jsonl": "",
				"docs/PLAN.md":              "\n\n",
			},
			planPath: "docs/PLAN.md",
			check: func(t *testing.T, _ string, f Files) {
				if f.Brief || f.Survey || f.Slices || f.Critic || f.PlanExists {
					t.Errorf("Load() = %+v, want every blank file to read as not there", f)
				}
				if !f.QuestionsPresent || len(f.Questions) != 0 || len(f.QuestionProblems) != 0 {
					t.Errorf("an empty questions.jsonl: present=%v questions=%v problems=%v; want present with nothing in it", f.QuestionsPresent, f.Questions, f.QuestionProblems)
				}
				if got := Decide(f); got.Phase != PhaseBrief {
					t.Errorf("Decide(Load()).Phase = %q, want brief", got.Phase)
				}
			},
		},
		{
			name:  "damaged state",
			files: map[string]string{".afk/plan/state.json": "{", ".afk/plan/brief.md": "x"},
			check: func(t *testing.T, project string, f Files) {
				if f.HasState {
					t.Error("HasState is true for a damaged state.json")
				}
				if !strings.Contains(f.StateProblem, StatePath(project)) {
					t.Errorf("StateProblem = %q, want it to name %s", f.StateProblem, StatePath(project))
				}
				if !f.Brief {
					t.Error("a damaged state.json stopped the other files being read")
				}
				if got := Decide(f); !got.Session || len(got.Problems) != 1 {
					t.Errorf("Decide(Load()) = %+v, want a session with one problem", got)
				}
			},
		},
		{
			name:  "question problems are carried",
			files: map[string]string{".afk/plan/state.json": state, ".afk/plan/questions.jsonl": questions},
			check: func(t *testing.T, _ string, f Files) {
				if len(f.Questions) != 1 || len(f.QuestionProblems) != 1 {
					t.Fatalf("questions=%v problems=%v; want one of each", f.Questions, f.QuestionProblems)
				}
				if !strings.HasPrefix(f.QuestionProblems[0], "line 2:") {
					t.Errorf("problem = %q, want it to start with the line number", f.QuestionProblems[0])
				}
			},
		},
		{
			name:  "questions.jsonl that cannot be read is a problem",
			files: map[string]string{".afk/plan/state.json": state},
			dirs:  []string{".afk/plan/questions.jsonl"},
			check: func(t *testing.T, _ string, f Files) {
				if !f.QuestionsPresent || len(f.QuestionProblems) != 1 {
					t.Fatalf("present=%v problems=%v; want present with one problem", f.QuestionsPresent, f.QuestionProblems)
				}
				if !strings.Contains(f.QuestionProblems[0], "questions.jsonl could not be read") {
					t.Errorf("problem = %q", f.QuestionProblems[0])
				}
			},
		},
		{
			name:     "no plan path means no plan",
			files:    map[string]string{"docs/PLAN.md": "# Plan\n"},
			planPath: "",
			check: func(t *testing.T, _ string, f Files) {
				if f.PlanExists {
					t.Error("PlanExists is true with no plan path")
				}
			},
		},
		{
			name:     "a plan path that is a folder is no plan",
			dirs:     []string{"docs/PLAN.md"},
			planPath: "docs/PLAN.md",
			check: func(t *testing.T, _ string, f Files) {
				if f.PlanExists {
					t.Error("PlanExists is true for a folder")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			for rel, content := range tt.files {
				writeFile(t, filepath.Join(project, rel), content)
			}
			for _, rel := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(project, rel), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f := Load(project, tt.planPath)
			if f.PlanErrors != 0 {
				t.Errorf("Load() set PlanErrors = %d; the caller fills it", f.PlanErrors)
			}
			tt.check(t, project, f)
		})
	}

	t.Run("an absolute plan path is used as given", func(t *testing.T) {
		project := t.TempDir()
		plan := filepath.Join(t.TempDir(), "PLAN.md")
		writeFile(t, plan, "# Plan\n")
		if !Load(project, plan).PlanExists {
			t.Error("PlanExists is false for an absolute plan path that exists")
		}
	})
}

// TestDir pins the folder, which docs/PLAN.md M5 and the master-plan skill
// both name in text: a change here would silently split the code from them.
func TestDir(t *testing.T) {
	if got, want := Dir("/p"), filepath.Join("/p", ".afk", "plan"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if got, want := StatePath("/p"), filepath.Join("/p", ".afk", "plan", "state.json"); got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
}
