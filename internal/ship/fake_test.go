package ship

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/projectconfig"
)

// The fixed names every Advance test ships.
const (
	testTask    = "T9.9"
	testSession = "t9-9"
	testBranch  = "t9-9"
	testDir     = "/work/t9-9/.afk/t9-9"
	testPR      = 7
	testHealth  = "https://app.example.test/up"
)

// tick is how far the fake clock moves after every call of Advance. It is
// longer than PollEvery so the poll limit never holds a scenario back; the
// throttle test sets the clock by hand instead.
const tick = 30 * time.Second

// Check results a scenario scripts per commit.
var (
	ciGreen = Checks{Total: 2, Passed: 2}
	ciRed   = Checks{Total: 2, Passed: 1, Failed: 1, FailedNames: []string{"unit-tests"}}
	ciWait  = Checks{Total: 2, Passed: 1, Pending: 1}
)

// errKilled is what the fake Save panics with to stand for the process being
// killed right after a save reached the disk.
var errKilled = errors.New("killed after the save")

// world is one fake project: a session's branch, its remote, GitHub's view of
// the pull request, the files and markers the daemon would hold, and a clock.
// It implements Git and GH itself and counts every call, so a test asserts on
// what was done to the outside, not on what Advance returned.
type world struct {
	t     *testing.T
	clock time.Time
	cfg   projectconfig.Ship
	base  string

	// git
	local     string
	localTime time.Time
	history   []string // every local commit, oldest first
	remote    string   // "" when the remote has no such branch
	dirty     bool
	nothing   bool // the branch has no commit the base lacks
	baseHas   map[string]bool
	added     []string

	// GitHub
	notReady   error
	prFound    bool
	pr         PR
	conflicts  map[string]bool     // commits that conflict with the base
	ci         map[string][]Checks // per commit: first result, then one per re-run
	ciIndex    map[string]int
	healthyOn  int // Health answers healthy from this call on; 0 means never
	healthSeen []string

	// everything behind Deps
	files        map[string][]byte
	lastPoll     time.Time
	failingSince time.Time
	stopMarker   bool
	stopReason   string
	answered     map[string]bool
	saved        Progress
	savedLog     []Progress
	savedAt      []time.Time
	saveCalls    int
	failSaveAt   int // the Save call that returns an error; 0 means none
	killAfter    int // the Save call that is followed by a kill; 0 means none
	onPrompt     func()
	fixes        []fix // commits the session has yet to make
	promptTexts  []string

	// errs makes the named call fail, by method name.
	errs map[string]error

	// counters
	pushes        map[string]int // per commit pushed
	prsOpened     int
	merges        int
	reruns        int
	prompts       int
	remoteDeletes int
	syncs         int
	syncsChanged  int
	stops         int
	healthCalls   int
	reads         int // every Git and GH read, and every Health call
}

// newWorld is a session that has just finished: one commit "c1" on its
// branch, nothing on the remote, no pull request, CI not scripted (so no
// checks), a deploy that answers healthy at once.
func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{
		t:     t,
		clock: now,
		base:  "main",
		cfg: projectconfig.Ship{
			MergeMethod:   "merge",
			CIReruns:      1,
			CIFixCycles:   2,
			Deploys:       true,
			HealthURL:     testHealth,
			DeployTimeout: 15 * time.Minute,
		},
		local:     "c1",
		localTime: now.Add(-10 * time.Minute),
		history:   []string{"c1"},
		baseHas:   map[string]bool{},
		conflicts: map[string]bool{},
		ci:        map[string][]Checks{},
		ciIndex:   map[string]int{},
		healthyOn: 1,
		files:     map[string][]byte{},
		answered:  map[string]bool{},
		errs:      map[string]error{},
		pushes:    map[string]int{},
	}
}

// at puts the world where a ship that reached state would have left it: the
// branch pushed, pull request #7 open, and for the later states merged.
func (w *world) at(state State) *world {
	w.remote = w.local
	w.prFound = true
	w.pr = PR{Number: testPR, State: PRStateOpen}
	w.saved = Progress{State: state, At: w.clock, PR: testPR}
	switch state {
	case Merged, DeployHealthy, Stopped:
		w.pr.State = PRStateMerged
		w.pr.MergeSHA = "m-" + w.local
		w.saved.MergedSHA = w.pr.MergeSHA
	}
	return w
}

// commit is the session making a new commit now.
func (w *world) commit(sha string) {
	w.local = sha
	w.localTime = w.clock
	w.history = append(w.history, sha)
}

// fixTime is how long the fake session takes to commit a fix after a
// hand-back. It must be longer than a tick: a session that committed in the
// same instant as the prompt would hide a second prompt after a crash, because
// the newer-head rules would run before the hand-back rule ever did.
const fixTime = 3 * time.Minute

// commitOnPrompt makes the session answer each hand-back with the next of
// the given commits, fixTime later, as an agent that fixes what it is told
// would.
func (w *world) commitOnPrompt(shas ...string) {
	w.onPrompt = func() {
		if len(shas) > 0 {
			w.fixes = append(w.fixes, fix{sha: shas[0], due: w.clock.Add(fixTime)})
			shas = shas[1:]
		}
	}
}

// fix is a commit the session will make at a later time.
type fix struct {
	sha string
	due time.Time
}

// sessionWorks makes the commits that have come due.
func (w *world) sessionWorks() {
	for len(w.fixes) > 0 && !w.fixes[0].due.After(w.clock) {
		w.commit(w.fixes[0].sha)
		w.fixes = w.fixes[1:]
	}
}

func (w *world) request() Request {
	return Request{
		Task: testTask, Session: testSession, Branch: testBranch, Base: w.base,
		SessionDir: testDir, Progress: w.saved, Config: w.cfg,
	}
}

func (w *world) deps() Deps {
	return Deps{
		Git: w,
		GH:  w,
		Health: func(url string) (bool, error) {
			w.reads++
			w.healthCalls++
			w.healthSeen = append(w.healthSeen, url)
			if err := w.errs["Health"]; err != nil {
				return false, err
			}
			return w.healthyOn > 0 && w.healthCalls >= w.healthyOn, nil
		},
		Prompt: func(text string) error {
			w.prompts++
			if err := w.errs["Prompt"]; err != nil {
				return err
			}
			w.promptTexts = append(w.promptTexts, text)
			if w.onPrompt != nil {
				w.onPrompt()
			}
			return nil
		},
		WriteFile: func(path string, data []byte) error {
			if err := w.errs["WriteFile"]; err != nil {
				return err
			}
			w.files[path] = data
			return nil
		},
		ReadFile: func(path string) ([]byte, error) {
			if err := w.errs["ReadFile"]; err != nil {
				return nil, err
			}
			data, ok := w.files[path]
			if !ok {
				return nil, fmt.Errorf("open %s: %w", path, fs.ErrNotExist)
			}
			return data, nil
		},
		EnvAnswered: func(v string) bool { return w.answered[v] },
		Stop: func(task, reason string) error {
			w.stops++
			if err := w.errs["Stop"]; err != nil {
				return err
			}
			w.stopMarker = true
			w.stopReason = task + ": " + reason
			return nil
		},
		StopMarker: func() bool { return w.stopMarker },
		LastPoll:   func() time.Time { return w.lastPoll },
		MarkPoll: func(at time.Time) error {
			if err := w.errs["MarkPoll"]; err != nil {
				return err
			}
			w.lastPoll = at
			return nil
		},
		ReadFailingSince: func() time.Time { return w.failingSince },
		MarkReadFailure: func(at time.Time) error {
			if w.failingSince.IsZero() {
				w.failingSince = at
			}
			return nil
		},
		ClearReadFailure: func() error {
			w.failingSince = time.Time{}
			return nil
		},
		Now: func() time.Time { return w.clock },
		Save: func(p Progress) error {
			w.saveCalls++
			if w.saveCalls == w.failSaveAt {
				return errors.New("disk full")
			}
			w.saved = p
			w.savedLog = append(w.savedLog, p)
			w.savedAt = append(w.savedAt, w.clock)
			if w.saveCalls == w.killAfter {
				panic(errKilled)
			}
			return nil
		},
	}
}

// step makes one call of Advance from the progress last saved, the way the
// daemon does each tick, and then moves the clock on. killed is true when the
// fake Save "killed the process": nothing Advance did after that save
// happened, and its outcome was never seen.
func (w *world) step() (out Outcome, killed bool) {
	w.t.Helper()
	defer func() {
		w.clock = w.clock.Add(tick)
		if r := recover(); r != nil {
			if r != errKilled {
				panic(r)
			}
			killed = true
		}
	}()
	w.sessionWorks()
	return Advance(w.request(), w.deps()), false
}

// run steps until Advance returns something other than wait, and fails the
// test if that takes more than 500 calls (over four hours of fake time, more
// than any timed rule).
func (w *world) run() Outcome {
	w.t.Helper()
	var last Outcome
	for i := 0; i < 500; i++ {
		out, killed := w.step()
		if killed {
			continue
		}
		if out.Kind != OutcomeWait {
			return out
		}
		last = out
	}
	w.t.Fatalf("the ship did not settle in 500 calls; state %q, last reason: %s", w.saved.State, last.Reason)
	return last
}

func (w *world) fail(name string) error {
	return w.errs[name]
}

// --- Git ---

func (w *world) LocalSHA(string) (string, error) {
	w.reads++
	return w.local, w.fail("LocalSHA")
}

func (w *world) RemoteSHA(string) (string, bool, error) {
	w.reads++
	if err := w.fail("RemoteSHA"); err != nil {
		return "", false, err
	}
	return w.remote, w.remote != "", nil
}

func (w *world) Clean() (bool, error) {
	w.reads++
	return !w.dirty, w.fail("Clean")
}

func (w *world) AheadOfBase(base, _ string) (bool, error) {
	w.reads++
	w.wantBase(base)
	return !w.nothing, w.fail("AheadOfBase")
}

func (w *world) Ahead(_, remoteSHA string) (bool, error) {
	w.reads++
	return slices.Contains(w.history, remoteSHA), w.fail("Ahead")
}

func (w *world) HeadTime(string) (time.Time, error) {
	w.reads++
	return w.localTime, w.fail("HeadTime")
}

func (w *world) Title(string) (string, string, error) {
	w.reads++
	return "Add the widget", "It was missing.", w.fail("Title")
}

func (w *world) AddedLines(base, _ string) ([]string, error) {
	w.reads++
	w.wantBase(base)
	return w.added, w.fail("AddedLines")
}

func (w *world) Push(string) error {
	w.pushes[w.local]++
	if err := w.fail("Push"); err != nil {
		return err
	}
	w.remote = w.local
	return nil
}

func (w *world) DeleteRemote(string) error {
	w.remoteDeletes++
	if err := w.fail("DeleteRemote"); err != nil {
		return err
	}
	w.remote = ""
	return nil
}

func (w *world) SyncBase(base string) error {
	w.syncs++
	w.wantBase(base)
	if err := w.fail("SyncBase"); err != nil {
		return err
	}
	if sha := w.pr.MergeSHA; sha != "" && !w.baseHas[sha] {
		w.baseHas[sha] = true
		w.syncsChanged++
	}
	return nil
}

func (w *world) BaseSynced(base, sha string) (bool, error) {
	w.reads++
	w.wantBase(base)
	return w.baseHas[sha], w.fail("BaseSynced")
}

// wantBase fails the test when a call names a base other than the one the
// world was set up with: a guessed base is a bug, not a detail.
func (w *world) wantBase(base string) {
	w.t.Helper()
	want := w.cfg.Base
	if want == "" {
		want = w.base
	}
	if base != want {
		w.t.Errorf("a git call named the base %q, want %q", base, want)
	}
}

// --- GH ---

func (w *world) Ready() error {
	w.reads++
	return w.notReady
}

func (w *world) FindPR(string) (PR, bool, error) {
	w.reads++
	if err := w.fail("FindPR"); err != nil {
		return PR{}, false, err
	}
	if !w.prFound {
		return PR{}, false, nil
	}
	pr := w.pr
	pr.Conflicting = pr.State == PRStateOpen && w.conflicts[w.remote]
	return pr, true, nil
}

func (w *world) OpenPR(_, base, title, body string) (PR, error) {
	w.prsOpened++
	w.wantBase(base)
	if err := w.fail("OpenPR"); err != nil {
		return PR{}, err
	}
	if w.prFound {
		return PR{}, errors.New("a pull request for this branch already exists")
	}
	if title == "" || body == "" {
		w.t.Errorf("OpenPR(title %q, body %q): both must be filled in", title, body)
	}
	w.prFound = true
	w.pr = PR{Number: testPR, State: PRStateOpen}
	return w.pr, nil
}

// Checks answers for the commit on the remote: the scripted result at that
// commit's index, the last one once the script runs out, none when the commit
// has no script.
func (w *world) Checks(int) (Checks, error) {
	w.reads++
	if err := w.fail("Checks"); err != nil {
		return Checks{}, err
	}
	return w.checksNow(), nil
}

func (w *world) checksNow() Checks {
	script := w.ci[w.remote]
	if len(script) == 0 {
		return Checks{}
	}
	return script[min(w.ciIndex[w.remote], len(script)-1)]
}

func (w *world) RerunFailed(int) error {
	w.reruns++
	if err := w.fail("RerunFailed"); err != nil {
		return err
	}
	w.ciIndex[w.remote]++
	return nil
}

func (w *world) FailedLog(int) (string, error) {
	w.reads++
	return "FAIL: TestWidget\nexpected 2, got 3\n", w.fail("FailedLog")
}

// Merge refuses what GitHub would refuse: a pull request that is not open,
// and a head that is not the one named. Every call is counted, refused or
// not, so "exactly one merge" means one attempt.
func (w *world) Merge(_ int, method, headSHA string) error {
	w.merges++
	if err := w.fail("Merge"); err != nil {
		return err
	}
	if w.pr.State != PRStateOpen {
		return errors.New("the pull request is not open")
	}
	if headSHA != w.remote {
		return fmt.Errorf("the head is %s, not %s", w.remote, headSHA)
	}
	if method != w.cfg.MergeMethod {
		w.t.Errorf("Merge used the method %q, want %q", method, w.cfg.MergeMethod)
	}
	if !w.checksNow().Green() {
		w.t.Errorf("Merge was called on %s, whose checks are not green", w.remote)
	}
	w.pr.State = PRStateMerged
	w.pr.MergeSHA = "m-" + headSHA
	return nil
}

// handBackFile is the hand-back file's text, "" when none was written.
func (w *world) handBackFile() string {
	return string(w.files[filepath.Join(testDir, HandBackFile)])
}
