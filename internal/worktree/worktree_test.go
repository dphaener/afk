package worktree

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/state"
)

// validStatus renders a schema-valid status.json body for a session named
// name. Every case below starts from this and mutates exactly one thing, so
// a failure names the mutation rather than the whole file.
func validStatus(name string) string {
	return `{"session_id":"sess-1","plan_name":"` + name + `","phase":"executing","branch":"` + name +
		`","base_branch":"main","loop_count":2,"awaiting_input":false,"tmux_window":"","updated_at":"2026-01-01T00:00:00Z"}`
}

// writeAfkDir seeds `<root>/.afk/<name>/` with the given file bodies. A
// body of "" means "do not create that file", which is how the scratch shape
// (host.json, no status.json) and the status-only shape are both expressed.
func writeAfkDir(t *testing.T, root, name, status, hostJSON string) {
	t.Helper()
	dir := filepath.Join(root, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(status), 0o644); err != nil {
			t.Fatalf("writing status.json: %v", err)
		}
	}
	if hostJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(hostJSON), 0o644); err != nil {
			t.Fatalf("writing host.json: %v", err)
		}
	}
}

// dirByName finds the observed `.afk/{name}` entry, failing the test when
// it is absent — every caller below is asserting about a directory it just
// wrote, so "not observed" is a failure, not a skip.
func dirByName(t *testing.T, obs Observation, name string) AfkDir {
	t.Helper()
	for _, d := range obs.Dirs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no observed .afk dir named %q (observed %d)", name, len(obs.Dirs))
	return AfkDir{}
}

// TestList covers `git worktree list --porcelain` parsing through the
// injected Run seam.
//
// Load-bearing rows:
//   - "missing dir keeps branch": the exact shape git emits for a worktree
//     whose directory was deleted (verified against git 2.51.0: the record
//     keeps its `branch refs/heads/...` line and gains `prunable <reason>`).
//     W2.T2 routes on this record's Path and Branch, and a parser that
//     dropped either on seeing `prunable` would hand it a zero WorktreePath —
//     the footgun the plan calls out, where `git status` and `git add -A`
//     would run in the caller's cwd.
//   - "detached missing dir": same shape with `detached` and no branch line.
//     Detached worktrees are never flagged by either scanner, so losing the
//     flag turns one into a spurious branch-named orphan.
//   - "primary is index 0 even when bare": `git worktree list` always emits
//     the main worktree first, and both scanners derive "primary" from that
//     position alone — including when it is bare, which they skip separately.
func TestList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Worktree
	}{
		{
			name: "main plus linked",
			out: "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n" +
				"worktree /wt/feat\nHEAD abc\nbranch refs/heads/feat\n",
			want: []Worktree{
				{Path: "/repo", Branch: "main", Primary: true},
				{Path: "/wt/feat", Branch: "feat"},
			},
		},
		{
			name: "missing dir keeps branch",
			out: "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n" +
				"worktree /wt/gone\nHEAD abc\nbranch refs/heads/gone\nprunable gitdir file points to non-existent location\n",
			want: []Worktree{
				{Path: "/repo", Branch: "main", Primary: true},
				{Path: "/wt/gone", Branch: "gone"},
			},
		},
		{
			name: "detached missing dir",
			out: "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n" +
				"worktree /wt/det\nHEAD abc\ndetached\nprunable gitdir file points to non-existent location\n",
			want: []Worktree{
				{Path: "/repo", Branch: "main", Primary: true},
				{Path: "/wt/det", Detached: true},
			},
		},
		{
			name: "primary is index 0 even when bare",
			out: "worktree /repo.git\nbare\n\n" +
				"worktree /wt/feat\nHEAD abc\nbranch refs/heads/feat\n",
			want: []Worktree{
				{Path: "/repo.git", Bare: true, Primary: true},
				{Path: "/wt/feat", Branch: "feat"},
			},
		},
		{
			name: "empty output",
			out:  "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotDir string
			var gotArgs []string
			run := func(dir, name string, args ...string) (string, error) {
				gotDir = dir
				gotArgs = append([]string{name}, args...)
				return tt.out, nil
			}
			got, err := List(run, "/repo")
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if gotDir != "/repo" {
				t.Errorf("List ran in %q, want %q", gotDir, "/repo")
			}
			wantArgs := []string{"git", "worktree", "list", "--porcelain"}
			if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
				t.Errorf("List argv = %v, want %v", gotArgs, wantArgs)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("List() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("List()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestListRunError checks that a git failure surfaces rather than being
// silently read as "no worktrees" — an empty list would make every session
// look orphaned at once.
func TestListRunError(t *testing.T) {
	run := func(dir, name string, args ...string) (string, error) {
		return "", errors.New("boom")
	}
	if _, err := List(run, "/repo"); err == nil {
		t.Fatal("List() error = nil, want the git failure")
	}
}

// TestObserveWorktreeFacts pins the worktree-level facts Observe carries
// through from the porcelain record plus one stat.
//
// The load-bearing row is "missing directory": DirOnDisk is the single fact
// W2.T2 disambiguates on before any git command runs, and it must be false
// for a registered worktree whose directory is gone while Path and Branch
// stay populated. The "file not a directory" row exists because a stat that
// merely succeeded would report a stray file at the worktree path as a live
// worktree.
func TestObserveWorktreeFacts(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		wt   Worktree
		want Observation
	}{
		{
			name: "live directory",
			wt:   Worktree{Path: live, Branch: "feat", Primary: true},
			want: Observation{Path: live, Branch: "feat", Primary: true, DirOnDisk: true},
		},
		{
			name: "missing directory",
			wt:   Worktree{Path: filepath.Join(root, "gone"), Branch: "gone"},
			want: Observation{Path: filepath.Join(root, "gone"), Branch: "gone", DirOnDisk: false},
		},
		{
			name: "detached missing directory",
			wt:   Worktree{Path: filepath.Join(root, "det"), Detached: true},
			want: Observation{Path: filepath.Join(root, "det"), Detached: true, DirOnDisk: false},
		},
		{
			name: "file not a directory",
			wt:   Worktree{Path: file, Branch: "feat"},
			want: Observation{Path: file, Branch: "feat", DirOnDisk: false},
		},
		{
			name: "bare",
			wt:   Worktree{Path: live, Bare: true, Primary: true},
			want: Observation{Path: live, Bare: true, Primary: true, DirOnDisk: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Observe(tt.wt, Deps{})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if got.Path != tt.want.Path || got.Branch != tt.want.Branch ||
				got.Bare != tt.want.Bare || got.Primary != tt.want.Primary ||
				got.Detached != tt.want.Detached || got.DirOnDisk != tt.want.DirOnDisk {
				t.Errorf("Observe() worktree facts = %+v, want %+v", got, tt.want)
			}
			if got.MatchedOwnBranch {
				t.Errorf("Observe() MatchedOwnBranch = true, want false (no .afk dirs seeded)")
			}
		})
	}
}

// TestObserveMatchedOwnBranch is the full matrix for the one derived fact
// this package computes.
//
// Every row is load-bearing, because the definition is narrower than it
// looks and both narrowings are load-bearing in opposite directions:
//
//   - "scratch dir of the same name" MUST be false. A `.afk/{branch}/`
//     holding only host.json is a `afk new --scratch` one-off that says
//     nothing about whether this worktree has a session of its own
//     (internal/session/clean.go:227-230). Counting it would silently
//     suppress a real worktree-without-status orphan.
//   - "corrupt status of the same name" MUST be true. The directory is
//     status-backed regardless of whether the JSON parses; treating a
//     corrupt file as "no session here" would emit a spurious orphan for a
//     session that plainly exists.
//   - "different name" and "empty branch" are the clauses that make a
//     healthy non-primary session emit no orphan at all.
func TestObserveMatchedOwnBranch(t *testing.T) {
	tests := []struct {
		name     string
		branch   string
		dirName  string
		status   string
		hostJSON string
		want     bool
	}{
		{"status-backed dir matching branch", "alpha", "alpha", validStatus("alpha"), "", true},
		{"status and host dir matching branch", "alpha", "alpha", validStatus("alpha"), `{"pid":1}`, true},
		{"corrupt status of the same name", "alpha", "alpha", "{", "", true},
		{"scratch dir of the same name", "alpha", "alpha", "", `{"pid":1}`, false},
		{"status-backed dir with a different name", "alpha", "beta", validStatus("beta"), "", false},
		{"empty branch with a matching-looking dir", "", "alpha", validStatus("alpha"), "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAfkDir(t, root, tt.dirName, tt.status, tt.hostJSON)

			got, err := Observe(Worktree{Path: root, Branch: tt.branch}, Deps{})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if got.MatchedOwnBranch != tt.want {
				t.Errorf("MatchedOwnBranch = %v, want %v", got.MatchedOwnBranch, tt.want)
			}
		})
	}
}

// TestObserveAfkDirShape pins the per-directory facts and the paths read,
// across the three on-disk shapes a `.afk/{name}/` can take.
//
// The "both files" row is load-bearing: a normal session's directory holds a
// host.json for as long as its host runs, so HasHostFile alone can never mean
// "scratch" — that is HasHostFile && !HasStatusFile, and this package reports
// the two facts separately precisely so neither consumer has to guess.
func TestObserveAfkDirShape(t *testing.T) {
	root := t.TempDir()
	writeAfkDir(t, root, "both", validStatus("both"), `{"pid":11,"socket":"/s","started_at":"t"}`)
	writeAfkDir(t, root, "statusonly", validStatus("statusonly"), "")
	writeAfkDir(t, root, "hostonly", "", `{"pid":22,"socket":"/s2","started_at":"t"}`)

	obs, err := Observe(Worktree{Path: root, Branch: "unrelated"}, Deps{})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if len(obs.Dirs) != 3 {
		t.Fatalf("observed %d dirs, want 3: %+v", len(obs.Dirs), obs.Dirs)
	}

	tests := []struct {
		name        string
		wantStatus  bool
		wantHost    bool
		wantHostPID int
	}{
		{"both", true, true, 11},
		{"statusonly", true, false, 0},
		{"hostonly", false, true, 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := dirByName(t, obs, tt.name)
			if d.HasStatusFile != tt.wantStatus || d.HasHostFile != tt.wantHost {
				t.Errorf("HasStatusFile/HasHostFile = %v/%v, want %v/%v",
					d.HasStatusFile, d.HasHostFile, tt.wantStatus, tt.wantHost)
			}
			wantDir := filepath.Join(root, ".afk", tt.name)
			if d.Path != wantDir {
				t.Errorf("Path = %q, want %q", d.Path, wantDir)
			}
			wantStatusPath := ""
			if tt.wantStatus {
				wantStatusPath = filepath.Join(wantDir, "status.json")
			}
			if d.StatusPath != wantStatusPath {
				t.Errorf("StatusPath = %q, want %q", d.StatusPath, wantStatusPath)
			}
			wantHostPath := ""
			if tt.wantHost {
				wantHostPath = filepath.Join(wantDir, "host.json")
			}
			if d.HostPath != wantHostPath {
				t.Errorf("HostPath = %q, want %q", d.HostPath, wantHostPath)
			}
			if d.HostMeta.PID != tt.wantHostPID {
				t.Errorf("HostMeta.PID = %d, want %d", d.HostMeta.PID, tt.wantHostPID)
			}
			if tt.wantHost && d.HostMetaErr != nil {
				t.Errorf("HostMetaErr = %v, want nil", d.HostMetaErr)
			}
			if !tt.wantHost && !errors.Is(d.HostMetaErr, ErrHostAbsent) {
				t.Errorf("HostMetaErr = %v, want ErrHostAbsent", d.HostMetaErr)
			}
			if !tt.wantStatus {
				if !errors.Is(d.StatusStrictErr, ErrStatusAbsent) {
					t.Errorf("StatusStrictErr = %v, want ErrStatusAbsent", d.StatusStrictErr)
				}
				// A zero-value ReadResult reads as "valid, empty status".
				// Synthesizing the missing result keeps a caller that
				// neglects to check HasStatusFile from seeing a phantom
				// schema-valid session.
				if !d.StatusSchema.Stale || d.StatusSchema.Reason != state.StaleMissing {
					t.Errorf("StatusSchema = %+v, want stale/missing", d.StatusSchema)
				}
			}
		})
	}
}

// TestObserveStatusParsersDisagree is the reason statusStrict and
// statusSchema are two facts and not one.
//
// internal/session decodes with DisallowUnknownFields and nothing else;
// internal/state's ReadStatus adds a trailing-data check and three schema
// invariants. Every "strict ok, schema stale" row below is a real file that
// one scanner treats as a live session and the other treats as stale — the
// disagreement dashboardPhase (internal/ui/messages.go:1081) is built on.
// Collapsing the two results would silently change the clean-refusal gate,
// so each row asserts BOTH results rather than whichever one the caller
// happens to want.
//
// The two agreement rows are here to prove the disagreement is specific:
// unknown keys and malformed JSON fail both parsers, so a test that only
// showed disagreement could be satisfied by a parser that always failed.
func TestObserveStatusParsersDisagree(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStrictErr bool
		wantStale     bool
		wantReason    state.StaleReason
		wantPhase     state.Phase
	}{
		{
			name:      "valid: both accept",
			body:      validStatus("alpha"),
			wantPhase: state.PhaseExecuting,
		},
		{
			name:          "trailing data: strict accepts, schema rejects",
			body:          validStatus("alpha") + "\n{}\n",
			wantStrictErr: false,
			wantStale:     true,
			wantReason:    state.StaleTrailingData,
			wantPhase:     state.PhaseExecuting,
		},
		{
			name:          "plan_name mismatch: strict accepts, schema rejects",
			body:          `{"session_id":"s","plan_name":"other","phase":"executing","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"t"}`,
			wantStrictErr: false,
			wantStale:     true,
			wantReason:    state.StaleNameMismatch,
			wantPhase:     state.PhaseExecuting,
		},
		{
			name:          "invalid phase: strict accepts, schema rejects",
			body:          `{"session_id":"s","plan_name":"alpha","phase":"bogus","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"t"}`,
			wantStrictErr: false,
			wantStale:     true,
			wantReason:    state.StaleInvalidPhase,
			wantPhase:     "bogus",
		},
		{
			name:          "empty session_id outside planning: strict accepts, schema rejects",
			body:          `{"session_id":"","plan_name":"alpha","phase":"executing","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"t"}`,
			wantStrictErr: false,
			wantStale:     true,
			wantReason:    state.StaleInvariant,
			wantPhase:     state.PhaseExecuting,
		},
		{
			name:          "unknown key: both reject",
			body:          `{"plan_name":"alpha","phase":"planning","nope":1}`,
			wantStrictErr: true,
			wantStale:     true,
			wantReason:    state.StaleUnknownField,
		},
		{
			name:          "malformed: both reject",
			body:          "{",
			wantStrictErr: true,
			wantStale:     true,
			wantReason:    state.StaleUnparseable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAfkDir(t, root, "alpha", tt.body, "")

			obs, err := Observe(Worktree{Path: root, Branch: "alpha"}, Deps{})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			d := dirByName(t, obs, "alpha")

			if (d.StatusStrictErr != nil) != tt.wantStrictErr {
				t.Errorf("StatusStrictErr = %v, want error: %v", d.StatusStrictErr, tt.wantStrictErr)
			}
			if !tt.wantStrictErr && d.StatusStrict.Phase != tt.wantPhase {
				t.Errorf("StatusStrict.Phase = %q, want %q", d.StatusStrict.Phase, tt.wantPhase)
			}
			if tt.wantStrictErr && errors.Is(d.StatusStrictErr, ErrStatusAbsent) {
				t.Errorf("StatusStrictErr = %v, want a decode error, not ErrStatusAbsent", d.StatusStrictErr)
			}
			if tt.wantStrictErr && errors.Is(d.StatusStrictErr, ErrStatusUnreadable) {
				t.Errorf("StatusStrictErr = %v, want a decode error, not ErrStatusUnreadable", d.StatusStrictErr)
			}
			if d.StatusSchema.Stale != tt.wantStale {
				t.Errorf("StatusSchema.Stale = %v (%s), want %v", d.StatusSchema.Stale, d.StatusSchema.Reason, tt.wantStale)
			}
			if d.StatusSchema.Reason != tt.wantReason {
				t.Errorf("StatusSchema.Reason = %q, want %q", d.StatusSchema.Reason, tt.wantReason)
			}
			if !tt.wantStale && d.StatusSchema.Status.Phase != tt.wantPhase {
				t.Errorf("StatusSchema.Status.Phase = %q, want %q", d.StatusSchema.Status.Phase, tt.wantPhase)
			}
			// The directory is status-backed either way — a corrupt file is
			// still a session that exists.
			if !d.HasStatusFile {
				t.Error("HasStatusFile = false, want true")
			}
		})
	}
}

// TestObserveStatusUnreadable checks that an unreadable (as opposed to
// unparseable) status.json is distinguishable through StatusStrictErr.
//
// internal/session's discovery aborts the whole scan on a read failure and
// keeps going on a decode failure; W2.T1 can only preserve that split if the
// two are still told apart here, and both arrive as a non-nil
// StatusStrictErr.
func TestObserveStatusUnreadable(t *testing.T) {
	root := t.TempDir()
	writeAfkDir(t, root, "alpha", validStatus("alpha"), "")
	statusPath := filepath.Join(root, ".afk", "alpha", "status.json")

	deps := Deps{ReadFile: func(path string) ([]byte, error) {
		if path == statusPath {
			return nil, os.ErrPermission
		}
		return os.ReadFile(path)
	}}
	obs, err := Observe(Worktree{Path: root, Branch: "alpha"}, deps)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	d := dirByName(t, obs, "alpha")
	if !errors.Is(d.StatusStrictErr, ErrStatusUnreadable) {
		t.Errorf("StatusStrictErr = %v, want ErrStatusUnreadable", d.StatusStrictErr)
	}
	if !errors.Is(d.StatusStrictErr, os.ErrPermission) {
		t.Errorf("StatusStrictErr = %v, want the underlying cause preserved", d.StatusStrictErr)
	}
	// The directory still exists and is still status-backed; a read failure
	// is not a reason to lose track of it.
	if !d.HasStatusFile {
		t.Error("HasStatusFile = false, want true")
	}
}

// TestObserveHostMeta pins the host.json half of the observation, including
// the corrupt case W2.T2's route 3 turns on: no PID is recoverable, but the
// directory and its derivable socket path still are.
func TestObserveHostMeta(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantPID  int
		wantErr  bool
		wantLast string
	}{
		{"parsed", `{"pid":4242,"socket":"/tmp/s.sock","started_at":"t","last_output_at":"2026-01-01T00:00:00Z"}`, 4242, false, "2026-01-01T00:00:00Z"},
		{"corrupt", "{", 0, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAfkDir(t, root, "alpha", "", tt.body)

			obs, err := Observe(Worktree{Path: root, Branch: "alpha"}, Deps{})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			d := dirByName(t, obs, "alpha")
			if !d.HasHostFile {
				t.Fatal("HasHostFile = false, want true")
			}
			if (d.HostMetaErr != nil) != tt.wantErr {
				t.Errorf("HostMetaErr = %v, want error: %v", d.HostMetaErr, tt.wantErr)
			}
			if d.HostMeta.PID != tt.wantPID {
				t.Errorf("HostMeta.PID = %d, want %d", d.HostMeta.PID, tt.wantPID)
			}
			if d.HostMeta.LastOutputAt != tt.wantLast {
				t.Errorf("HostMeta.LastOutputAt = %q, want %q", d.HostMeta.LastOutputAt, tt.wantLast)
			}
			if tt.wantErr && errors.Is(d.HostMetaErr, ErrHostAbsent) {
				t.Errorf("HostMetaErr = %v, want a parse error, not ErrHostAbsent", d.HostMetaErr)
			}
		})
	}
}

// TestObserveReadsEachFileOnce is the whole point of the package: one
// observation, not one per question asked.
//
// The glob assertion is exact rather than a ceiling. classifyAfkDirs'
// doc (internal/session/clean.go:379-392) declares the single-observation
// property load-bearing: a second look at the same directory can disagree
// with the first while a writer is mid-rename, and the same `.afk/{name}/`
// then gets emitted under two kinds — after which "first match by name" picks
// the teardown route by sort order. A stat-per-directory would reintroduce
// exactly that, which is why HasStatusFile/HasHostFile come from the glob
// results alone.
//
// The host-read assertion covers the other half: a status-backed directory
// with no host.json must not trigger a speculative ReadMeta whose ENOENT
// would be a second observation of a question the glob already answered.
func TestObserveReadsEachFileOnce(t *testing.T) {
	root := t.TempDir()
	writeAfkDir(t, root, "alpha", validStatus("alpha"), `{"pid":1}`)
	writeAfkDir(t, root, "beta", validStatus("beta"), "")
	writeAfkDir(t, root, "gamma", "", `{"pid":2}`)

	var globs, stats, metaReads []string
	deps := Deps{
		Glob: func(pattern string) ([]string, error) {
			globs = append(globs, pattern)
			return filepath.Glob(pattern)
		},
		Stat: func(path string) (os.FileInfo, error) {
			stats = append(stats, path)
			return os.Stat(path)
		},
		ReadMeta: func(worktreePath, name string) (host.Meta, error) {
			metaReads = append(metaReads, name)
			return host.ReadMeta(worktreePath, name)
		},
	}

	if _, err := Observe(Worktree{Path: root, Branch: "alpha"}, deps); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	wantGlobs := []string{
		filepath.Join(root, ".afk", "*", "status.json"),
		filepath.Join(root, ".afk", "*", "host.json"),
	}
	if strings.Join(globs, "\n") != strings.Join(wantGlobs, "\n") {
		t.Errorf("globs = %v, want exactly %v", globs, wantGlobs)
	}
	// One stat, for the worktree directory itself — nothing per .afk dir.
	if len(stats) != 1 || stats[0] != root {
		t.Errorf("stats = %v, want exactly [%s]", stats, root)
	}
	if strings.Join(metaReads, ",") != "alpha,gamma" {
		t.Errorf("host meta reads = %v, want exactly [alpha gamma]", metaReads)
	}
}

// TestObserveGlobError checks that a malformed glob pattern (a worktree path
// holding an unterminated character class) surfaces as an error instead of
// an empty, wrong observation that would read as "this worktree has no
// sessions".
func TestObserveGlobError(t *testing.T) {
	_, err := Observe(Worktree{Path: "/repo/[", Branch: "x"}, Deps{})
	if err == nil {
		t.Fatal("Observe() error = nil, want the bad-pattern error")
	}
}

// TestObserveDirOrder pins the emission order: status-glob matches first, in
// glob order, then host-only matches. Both consumers sort their own output
// afterwards with a total comparator, so nothing depends on this order for
// correctness — it is pinned so that W2.T1's repointing is provably
// order-neutral rather than order-neutral by inspection.
func TestObserveDirOrder(t *testing.T) {
	root := t.TempDir()
	writeAfkDir(t, root, "zed", validStatus("zed"), "")
	writeAfkDir(t, root, "abe", "", `{"pid":1}`)
	writeAfkDir(t, root, "mid", validStatus("mid"), "")

	obs, err := Observe(Worktree{Path: root}, Deps{})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	var names []string
	for _, d := range obs.Dirs {
		names = append(names, d.Name)
	}
	want := "mid,zed,abe"
	if strings.Join(names, ",") != want {
		t.Errorf("dir order = %v, want %s", names, want)
	}
}

// TestObserveNeverBlocks mechanically enforces the ticker contract this
// package inherits: internal/discover calls Discover on a 2s refresh, and
// W2.T1 puts Observe on that path, so nothing here may dial, connect, or
// perform IPC of any kind.
//
// It mirrors TestDiscoverNeverBlocks (internal/discover/discover_test.go) and
// is an allowlist for the same reason: a denylist of known blocking APIs is
// only as current as the last person who thought to extend it. Note what is
// NOT on the list — `os/exec` is absent because this package never runs a
// subprocess itself; `git worktree list --porcelain` is executed by the
// caller's injected Run, which is what keeps the one unavoidable subprocess
// on the caller's side of the seam.
//
// The internal/host selector allowlist is the same assertion discover.go
// carries, and for the same reason: internal/host is also where Dial lives.
func TestObserveNeverBlocks(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "worktree.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing worktree.go: %v", err)
	}

	allowedImports := map[string]bool{
		"bytes":                                  true,
		"encoding/json":                          true,
		"errors":                                 true,
		"fmt":                                    true,
		"os":                                     true,
		"path/filepath":                          true,
		"strings":                                true,
		"github.com/dphaener/afk/internal/host":  true,
		"github.com/dphaener/afk/internal/state": true,
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import %s: %v", imp.Path.Value, err)
		}
		if !allowedImports[path] {
			t.Errorf("worktree.go imports %q, which is not on the never-block allowlist; "+
				"confirm it cannot dial, connect, or otherwise stall the dashboard's 2s ticker, then add it", path)
		}
	}

	usedHost := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "host" {
			usedHost[sel.Sel.Name] = true
		}
		return true
	})
	allowedHost := map[string]bool{"Meta": true, "ReadMeta": true}
	for name := range usedHost {
		if !allowedHost[name] {
			t.Errorf("worktree.go references host.%s; only Meta and ReadMeta (a plain file read) belong "+
				"here — host.Dial and friends can stall the ticker", name)
		}
	}
}

// TestObserveReturnsNoVerdict guards the package boundary the plan draws:
// Observe reports facts, and the two consumers legitimately disagree about
// what identical facts mean, so no classification may cross this boundary.
//
// It is a lexical check on the exported API surface, which makes it a
// tripwire rather than a proof — but the failure mode it guards against is a
// well-meaning future edit adding a `Kind`/`Broken`/`Orphan` field "since we
// already know", and that edit is exactly the one this catches.
func TestObserveReturnsNoVerdict(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "worktree.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing worktree.go: %v", err)
	}

	banned := []string{"orphan", "broken", "healthy", "kind", "scratch", "stale"}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || !ts.Name.IsExported() {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			for _, name := range fld.Names {
				if !name.IsExported() {
					continue
				}
				lower := strings.ToLower(name.Name)
				for _, b := range banned {
					if strings.Contains(lower, b) {
						t.Errorf("%s.%s: %q reads as a verdict, not a fact; policy belongs in the "+
							"consumer packages, which disagree about what identical facts mean",
							ts.Name.Name, name.Name, b)
					}
				}
			}
		}
		return true
	})
}
