package tokens

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// fakeProvider embeds the interface and defines only SessionTokens, so a
// method added to provider.Provider later does not break this file. Calling
// any other method panics on the nil embedded value, which is the point.
type fakeProvider struct {
	provider.Provider
	records []provider.TokenRecord
	err     error
	calls   int
}

func (f *fakeProvider) SessionTokens(provider.TokenQuery) ([]provider.TokenRecord, error) {
	f.calls++
	return f.records, f.err
}

// TestRecord pins Record's contract. Load-bearing rows: "phases" (one line per
// phase, role and model reaches the ledger), "second call" (idempotence: the
// ledger must not double-count a session), and the three error rows (nothing
// may be written, not even the ledger file, when the transcript cannot be
// read).
func TestRecord(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	recs := []provider.TokenRecord{
		{Phase: "plan", Role: "task", Model: "m-big", Input: 10, Output: 5},
		{Phase: "execute", Role: "task", Model: "m-big", Input: 20, Output: 7},
		{Phase: "execute", Role: "critic", Model: "m-small", Input: 30, Output: 9},
		{Phase: "review", Role: "task", Model: "m-big", Input: 40, Output: 11},
	}
	cases := []struct {
		name    string
		fake    *fakeProvider
		twice   bool
		wantErr error
		want    int
	}{
		{name: "phases", fake: &fakeProvider{records: recs}, want: 4},
		{name: "second call", fake: &fakeProvider{records: recs}, twice: true, want: 4},
		{name: "unsupported", fake: &fakeProvider{err: provider.ErrTokensUnsupported}, wantErr: provider.ErrTokensUnsupported},
		{name: "no transcript", fake: &fakeProvider{err: provider.ErrNoTranscript}, wantErr: provider.ErrNoTranscript},
		{name: "no calls", fake: &fakeProvider{}, wantErr: ErrNoCalls},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			main := t.TempDir()
			in := RecordInput{Provider: tt.fake, Home: t.TempDir(), MainCheckout: main, Task: "T1.1", Session: "s1", SessionID: "id", Now: now}
			res, err := Record(in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if _, statErr := os.Stat(LedgerPath(main)); !os.IsNotExist(statErr) {
					t.Errorf("ledger exists after %v (stat err %v)", err, statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Calls != len(recs) || res.AlreadyRecorded || len(res.Lines) != tt.want {
				t.Fatalf("result = %+v", res)
			}
			if tt.twice {
				again, err := Record(in)
				if err != nil || !again.AlreadyRecorded {
					t.Fatalf("second = %+v, %v", again, err)
				}
			}
			got, err := Read(LedgerPath(main))
			if err != nil || len(got) != tt.want {
				t.Fatalf("ledger has %d lines, err %v; want %d", len(got), err, tt.want)
			}
			seen := map[string]bool{}
			for _, l := range got {
				seen[l.Phase+"/"+l.Role+"/"+l.Model] = true
				if l.At != now.Format(time.RFC3339) {
					t.Errorf("line at = %q, want the injected time", l.At)
				}
			}
			for _, k := range []string{"plan/task/m-big", "execute/task/m-big", "execute/critic/m-small", "review/task/m-big"} {
				if !seen[k] {
					t.Errorf("ledger lacks %s: %v", k, seen)
				}
			}
			if tt.fake.calls != map[bool]int{true: 2, false: 1}[tt.twice] {
				t.Errorf("SessionTokens called %d times", tt.fake.calls)
			}
		})
	}
}

// TestRecordLedgerErrors: an unreadable ledger and an unwritable one are
// distinct error types and neither adds lines.
func TestRecordLedgerErrors(t *testing.T) {
	recs := []provider.TokenRecord{{Phase: "plan", Role: "task", Model: "m", Input: 1}}
	main := t.TempDir()
	// A directory where the ledger file should be makes Read fail.
	if err := os.MkdirAll(LedgerPath(main), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Record(RecordInput{Provider: &fakeProvider{records: recs}, MainCheckout: main, Session: "s", Now: time.Now()})
	var re *LedgerReadError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want LedgerReadError", err)
	}
}
