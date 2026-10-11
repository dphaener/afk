package provider

import (
	"errors"
	"sort"
	"testing"
)

// fixtureSessionID is the finished t0-4 session whose trimmed transcripts sit
// in testdata/claude-home (see testdata/README.md).
const fixtureSessionID = "66065044-0f91-4d8d-9972-5f1cd1878494"

// fixtureRow is one expected line of the README's phase, role and model table.
type fixtureRow struct {
	phase, role, model                   string
	calls                                int
	input, output, cacheWrite, cacheRead int64
}

// fixtureSums is the local summing helper: it groups records by phase, role
// and model without using internal/tokens, so this test cannot agree with a
// bug in the aggregation by sharing it.
func fixtureSums(records []TokenRecord) map[[3]string]*fixtureRow {
	got := map[[3]string]*fixtureRow{}
	for _, r := range records {
		k := [3]string{r.Phase, r.Role, r.Model}
		row := got[k]
		if row == nil {
			row = &fixtureRow{phase: r.Phase, role: r.Role, model: r.Model}
			got[k] = row
		}
		row.calls++
		row.input += r.Input
		row.output += r.Output
		row.cacheWrite += r.CacheWrite
		row.cacheRead += r.CacheRead
	}
	return got
}

// TestSessionTokensFixture reads the committed trimmed copy of a real
// autonomous session and compares it with numbers computed by the `jq`
// scripts in testdata/README.md (## Expected numbers), typed in here as
// literals. They were not produced by running this reader.
//
// Load-bearing rows:
//   - "execute task opus" and "execute task sonnet": one role on two models in
//     one phase must stay two rows.
//   - "execute conductor", "review conductor" and "ship conductor": one role
//     that runs in three phases must split by phase, not collapse.
//   - "ship conductor": the ship rule (5 calls in the reviewing tail after the
//     last sub-agent call plus 16 after the done write) is the only place the
//     reader invents a phase the transcript never wrote.
//   - the total of 143 records: 284 assistant lines carry 143 message ids, so
//     a reader that does not dedupe by id would be far over.
func TestSessionTokensFixture(t *testing.T) {
	p, err := Lookup(ClaudeName)
	if err != nil {
		t.Fatal(err)
	}
	records, err := p.SessionTokens(TokenQuery{Home: "testdata/claude-home", SessionID: fixtureSessionID})
	if err != nil {
		t.Fatalf("SessionTokens: %v", err)
	}
	if len(records) != 143 {
		t.Fatalf("records = %d, want 143 (unique message ids)", len(records))
	}

	want := []fixtureRow{
		{"execute", "conductor", "claude-opus-5-5", 6, 14, 4496, 16195, 1196648},
		{"execute", "task", "claude-opus-5-5", 44, 88, 62039, 274846, 2796297},
		{"execute", "task", "claude-sonnet-5-5", 17, 34, 9323, 86739, 717783},
		{"execute", "verifier", "claude-sonnet-5-5", 18, 38, 3782, 104781, 679825},
		{"plan", "planner", "claude-opus-5-5", 12, 26, 24006, 164921, 1788598},
		{"plan", "research", "claude-sonnet-5-5", 7, 14, 8114, 78599, 330015},
		{"review", "conductor", "claude-opus-5-5", 2, 4, 723, 8023, 428189},
		{"review", "review:compliance", "claude-sonnet-5-5", 4, 8, 1554, 57300, 139017},
		{"review", "review:qa", "claude-sonnet-5-5", 9, 18, 2417, 41538, 438831},
		{"review", "review:quality", "claude-sonnet-5-5", 3, 6, 890, 24209, 93606},
		{"ship", "conductor", "claude-opus-5-5", 21, 48, 10387, 48352, 2395261},
	}

	got := fixtureSums(records)
	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k[0]+"/"+k[1]+"/"+k[2])
		}
		sort.Strings(keys)
		t.Errorf("got %d phase/role/model rows, want %d: %v", len(got), len(want), keys)
	}
	// One First record per agent: the fixture has 13 transcript files (the
	// main session and 12 sub-agents), so 13 agents and 13 first calls.
	firsts := map[string]int{}
	for _, r := range records {
		if r.First {
			firsts[r.AgentID]++
		}
	}
	if len(firsts) != 13 {
		t.Errorf("agents with a first call = %d, want 13", len(firsts))
	}
	for agent, n := range firsts {
		if n != 1 {
			t.Errorf("agent %q has %d first calls, want 1", agent, n)
		}
	}

	var calls int
	var in, out, cw, cr int64
	for _, w := range want {
		name := w.phase + " " + w.role + " " + w.model
		t.Run(name, func(t *testing.T) {
			g := got[[3]string{w.phase, w.role, w.model}]
			if g == nil {
				t.Fatal("row missing")
			}
			if *g != w {
				t.Errorf("got %+v, want %+v", *g, w)
			}
		})
		calls += w.calls
		in += w.input
		out += w.output
		cw += w.cacheWrite
		cr += w.cacheRead
	}
	// The README's TOTAL row, checked against the literals above as well as
	// the reader, so a typo in a row cannot hide.
	if calls != 143 || in != 298 || out != 127731 || cw != 905503 || cr != 11004070 {
		t.Errorf("expected rows sum to %d/%d/%d/%d/%d, README TOTAL is 143/298/127731/905503/11004070", calls, in, out, cw, cr)
	}
	var gi, goo, gcw, gcr int64
	for _, r := range records {
		gi += r.Input
		goo += r.Output
		gcw += r.CacheWrite
		gcr += r.CacheRead
	}
	if gi != in || goo != out || gcw != cw || gcr != cr {
		t.Errorf("record sums = %d/%d/%d/%d, want %d/%d/%d/%d", gi, goo, gcw, gcr, in, out, cw, cr)
	}
}

// TestSessionTokensFixtureNoSession pins the failure case against the same
// fixture: a session id the fixture does not hold is ErrNoTranscript with no
// records, not a panic or an empty success.
func TestSessionTokensFixtureNoSession(t *testing.T) {
	p, err := Lookup(ClaudeName)
	if err != nil {
		t.Fatal(err)
	}
	records, err := p.SessionTokens(TokenQuery{Home: "testdata/claude-home", SessionID: "00000000-0000-0000-0000-000000000000"})
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("error = %v, want ErrNoTranscript", err)
	}
	if len(records) != 0 {
		t.Errorf("records = %d, want none", len(records))
	}
}
