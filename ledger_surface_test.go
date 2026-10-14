package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
)

// TestLedgerSetForSurface pins the entry point the dashboard shares with
// `afk ledger set`.
//
// Load-bearing rows: "old status comes from the file" because the dashboard
// shows old to new and a caller's guess may be stale; "already at status"
// because nothing may be reported changed when the file already agrees;
// "superseded without replacements" and "replacements with another status"
// because they are the Code 2 refusals the picker's replacing-ids step relies
// on; "unknown id" and "unknown replacing id" because a typo must not reach the
// ledger; "ledger cannot be written" because Code 1 with the file named is the
// only way the person learns the write did not happen.
func TestLedgerSetForSurface(t *testing.T) {
	cases := []struct {
		name         string
		existing     string
		dirLedger    bool
		readOnlyDir  bool
		ids          []string
		st           ledger.Status
		repl         []string
		replGiven    bool
		wantOutcome  ledgerSetOutcome
		wantCode     int
		wantStderr   bool
		wantText     []string
		wantOld      []ledger.Status
		wantChanged  []bool
		wantNoFile   bool
		wantReplaced []string
		wantInFile   string
	}{
		{name: "pending to running", ids: []string{"T1.1"}, st: ledger.Running, wantOutcome: ledgerChanged,
			wantText: []string{"T1.1: pending → running"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}},
		{name: "pending to shipping", ids: []string{"T1.1"}, st: ledger.Shipping, wantOutcome: ledgerChanged,
			wantText: []string{"T1.1: pending → shipping"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}},
		{name: "pending to parked", ids: []string{"T1.1"}, st: ledger.Parked, wantOutcome: ledgerChanged,
			wantText: []string{"T1.1: pending → parked"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}},
		{name: "pending to done", ids: []string{"T1.1"}, st: ledger.Done, wantOutcome: ledgerChanged,
			wantText: []string{"T1.1: pending → done"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}},
		{name: "pending to failed", ids: []string{"T1.1"}, st: ledger.Failed, wantOutcome: ledgerChanged,
			wantText: []string{"T1.1: pending → failed"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}},
		{name: "failed to pending", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, ids: []string{"T1.1"}, st: ledger.Pending,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: failed → pending"}, wantOld: []ledger.Status{ledger.Failed}, wantChanged: []bool{true}},
		{name: "failed to running", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, ids: []string{"T1.1"}, st: ledger.Running,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: failed → running"}, wantOld: []ledger.Status{ledger.Failed}, wantChanged: []bool{true}},
		{name: "failed to shipping", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, ids: []string{"T1.1"}, st: ledger.Shipping,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: failed → shipping"}, wantOld: []ledger.Status{ledger.Failed}, wantChanged: []bool{true}},
		{name: "failed to parked", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, ids: []string{"T1.1"}, st: ledger.Parked,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: failed → parked"}, wantOld: []ledger.Status{ledger.Failed}, wantChanged: []bool{true}},
		{name: "failed to done", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, ids: []string{"T1.1"}, st: ledger.Done,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: failed → done"}, wantOld: []ledger.Status{ledger.Failed}, wantChanged: []bool{true}},
		{name: "superseded with replacements", ids: []string{"T1.2"}, st: ledger.Superseded, repl: []string{"T1.2a", "T1.2b"}, replGiven: true,
			wantOutcome: ledgerChanged, wantText: []string{"T1.2: pending → superseded (replaced by T1.2a, T1.2b)"},
			wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{true}, wantReplaced: []string{"T1.2a", "T1.2b"}, wantInFile: `"T1.2b"`},
		{name: "old status comes from the file", existing: `{"tasks":[{"id":"T1.1","status":"running","session":"s1"}]}`, ids: []string{"T1.1"}, st: ledger.Parked,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: running → parked"}, wantOld: []ledger.Status{ledger.Running}, wantChanged: []bool{true}, wantInFile: `"session": "s1"`},
		{name: "already at status", existing: `{"tasks":[{"id":"T1.1","status":"pending"}]}`, ids: []string{"T1.1"}, st: ledger.Pending,
			wantOutcome: ledgerAlready, wantText: []string{"T1.1 is already pending"}, wantOld: []ledger.Status{ledger.Pending}, wantChanged: []bool{false},
			wantInFile: `"status": "pending"`},
		{name: "one changed and one already", existing: `{"tasks":[{"id":"T1.2","status":"done"}]}`, ids: []string{"T1.1", "T1.2"}, st: ledger.Done,
			wantOutcome: ledgerChanged, wantText: []string{"T1.1: pending → done", "T1.2 is already done"},
			wantOld: []ledger.Status{ledger.Pending, ledger.Done}, wantChanged: []bool{true, false}},
		{name: "superseded without replacements", ids: []string{"T1.2"}, st: ledger.Superseded,
			wantOutcome: ledgerRefused, wantCode: 2, wantStderr: true, wantText: []string{"needs the tasks that replace it"}, wantNoFile: true},
		{name: "replacements with another status", ids: []string{"T1.2"}, st: ledger.Done, repl: []string{"T1.2a"}, replGiven: true,
			wantOutcome: ledgerRefused, wantCode: 2, wantStderr: true, wantText: []string{"--replaced-by only goes with the status superseded, not done"}, wantNoFile: true},
		{name: "replacements for several ids", ids: []string{"T1.1", "T1.2"}, st: ledger.Superseded, repl: []string{"T1.2a"}, replGiven: true,
			wantOutcome: ledgerRefused, wantCode: 2, wantStderr: true, wantText: []string{"cannot be used with 2 task ids"}, wantNoFile: true},
		{name: "unknown id", ids: []string{"T9.9"}, st: ledger.Done,
			wantOutcome: ledgerRefused, wantCode: 1, wantStderr: true, wantText: []string{"T9.9 is not a task in", "PLAN.md", `"afk tasks"`}, wantNoFile: true},
		{name: "unknown replacing id", ids: []string{"T1.2"}, st: ledger.Superseded, repl: []string{"T1.2a", "T7"}, replGiven: true,
			wantOutcome: ledgerRefused, wantCode: 1, wantStderr: true, wantText: []string{"T7 is not a task in"}, wantNoFile: true},
		{name: "self replacement", ids: []string{"T1.2"}, st: ledger.Superseded, repl: []string{"T1.2"}, replGiven: true,
			wantOutcome: ledgerRefused, wantCode: 2, wantStderr: true, wantText: []string{"T1.2 cannot be replaced by itself"}, wantNoFile: true},
		{name: "ledger path is a folder", dirLedger: true, ids: []string{"T1.1"}, st: ledger.Done,
			wantOutcome: ledgerFailed, wantCode: 1, wantStderr: true, wantText: []string{"cannot read the task ledger", "ledger.json"}},
		{name: "ledger cannot be written", readOnlyDir: true, ids: []string{"T1.1"}, st: ledger.Done,
			wantOutcome: ledgerFailed, wantCode: 1, wantStderr: true, wantText: []string{"could not update", "ledger.json", "Nothing was changed"}, wantNoFile: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			switch {
			case tt.dirLedger:
				if err := os.MkdirAll(ledger.Path(project), 0o755); err != nil {
					t.Fatal(err)
				}
			case tt.readOnlyDir:
				if os.Geteuid() == 0 {
					t.Skip("root ignores folder permissions")
				}
				dir := filepath.Dir(ledger.Path(project))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			case tt.existing != "":
				writeLedgerFile(t, project, tt.existing)
			}
			res := setLedgerStatusForSurface(project, tt.ids, tt.st, tt.repl, tt.replGiven)
			if res.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %d, want %d (text: %s)", res.Outcome, tt.wantOutcome, res.Text)
			}
			if res.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", res.Code, tt.wantCode)
			}
			if res.ToStderr != tt.wantStderr {
				t.Errorf("ToStderr = %v, want %v", res.ToStderr, tt.wantStderr)
			}
			for _, w := range tt.wantText {
				if !strings.Contains(res.Text, w) {
					t.Errorf("text missing %q:\n%s", w, res.Text)
				}
			}
			if tt.wantOld != nil {
				if len(res.Changes) != len(tt.wantOld) {
					t.Fatalf("changes = %+v, want %d", res.Changes, len(tt.wantOld))
				}
				for i, c := range res.Changes {
					if c.Old != tt.wantOld[i] || c.New != tt.st || c.Changed != tt.wantChanged[i] {
						t.Errorf("change %d = %+v, want old %s new %s changed %v", i, c, tt.wantOld[i], tt.st, tt.wantChanged[i])
					}
				}
			} else if len(res.Changes) != 0 {
				t.Errorf("changes = %+v, want none", res.Changes)
			}
			if tt.wantReplaced != nil && !slices.Equal(res.Changes[0].ReplacedBy, tt.wantReplaced) {
				t.Errorf("replaced by = %v, want %v", res.Changes[0].ReplacedBy, tt.wantReplaced)
			}
			data, err := os.ReadFile(ledger.Path(project))
			switch {
			case tt.wantNoFile && err == nil:
				t.Errorf("ledger file was created:\n%s", data)
			case tt.wantInFile != "" && !strings.Contains(string(data), tt.wantInFile):
				t.Errorf("ledger missing %q:\n%s", tt.wantInFile, data)
			}
		})
	}
}
