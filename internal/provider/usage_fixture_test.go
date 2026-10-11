package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// fixturePath is the redacted capture of a real `claude -p /usage
// --output-format stream-json --verbose` run (see the .afk/t1-3 spike notes).
const fixturePath = "testdata/usage/captured.jsonl"

// fixtureNow is the fixed clock every fixture case uses; it is the snapshot's
// At and nothing else depends on it.
var fixtureNow = time.Date(2026, 10, 11, 20, 2, 21, 0, time.UTC)

// fixtureWant is what the captured stream holds, copied from the fixture's
// rate_limits.limits in order. It is the one test tied to the real CLI's
// output: if the fixture is re-captured and this fails, the shape changed.
func fixtureWant(t *testing.T) UsageSnapshot {
	t.Helper()
	return UsageSnapshot{
		At: fixtureNow,
		Windows: []Window{
			{Kind: WindowSession, UsedPct: 30, ResetsAt: mustTime(t, "2026-10-11T21:50:00.248413Z")},
			{Kind: WindowWeekly, UsedPct: 71, ResetsAt: mustTime(t, "2026-10-12T14:00:00.248442Z")},
			{Kind: WindowWeeklyModel, UsedPct: 13, ResetsAt: mustTime(t, "2026-10-12T14:00:00.248750Z"), Scope: "Fable"},
		},
		OverageEnabled: false,
	}
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// assertSnapshot compares every field. Times are compared with Equal so the
// location and monotonic-clock details of time.Time cannot cause a false diff.
func assertSnapshot(t *testing.T, got, want UsageSnapshot) {
	t.Helper()
	if !got.At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
	if got.OverageEnabled != want.OverageEnabled {
		t.Errorf("OverageEnabled = %v, want %v", got.OverageEnabled, want.OverageEnabled)
	}
	if len(got.Windows) != len(want.Windows) {
		t.Fatalf("got %d windows, want %d: %+v", len(got.Windows), len(want.Windows), got.Windows)
	}
	for i := range want.Windows {
		g, w := got.Windows[i], want.Windows[i]
		if g.Kind != w.Kind || g.UsedPct != w.UsedPct || g.Scope != w.Scope || !g.ResetsAt.Equal(w.ResetsAt) {
			t.Errorf("window %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestFixture runs the captured stream through the parser and through the
// default provider's Usage, and checks the whole snapshot. Load-bearing rows:
// the parser and Usage rows assert the same values (the fixture must read the
// same whichever way it arrives), and drop_usage_report_line is the only row
// that proves the parser reads the report from the assistant line rather than
// from some other line, by removing that line and expecting ErrNoUsageReport.
// Failing here after a re-capture means the CLI's output shape changed.
func TestFixture(t *testing.T) {
	data := readFixture(t)
	want := fixtureWant(t)

	tests := []struct {
		name    string
		run     func(t *testing.T) (UsageSnapshot, error)
		want    UsageSnapshot
		wantErr error
	}{
		{
			name: "parser reads captured stream",
			run: func(t *testing.T) (UsageSnapshot, error) {
				return parseUsageStream(data, fixtureNow)
			},
			want: want,
		},
		{
			name: "default provider reads captured stream",
			run: func(t *testing.T) (UsageSnapshot, error) {
				d := UsageDeps{
					Run: func(context.Context, string, ...string) ([]byte, error) { return data, nil },
					Now: func() time.Time { return fixtureNow },
				}
				return claude{}.Usage(context.Background(), d)
			},
			want: want,
		},
		{
			name: "drop_usage_report_line",
			run: func(t *testing.T) (UsageSnapshot, error) {
				return parseUsageStream(withoutUsageReport(t, data), fixtureNow)
			},
			wantErr: ErrNoUsageReport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.run(t)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSnapshot(t, got, tt.want)
		})
	}
}

// withoutUsageReport returns the fixture with every line that carries a
// usage_report key removed, in memory only. The fixture file is not touched.
func withoutUsageReport(t *testing.T, data []byte) []byte {
	t.Helper()
	var kept [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatalf("fixture line is not JSON: %v", err)
		}
		if _, has := obj["usage_report"]; has {
			continue
		}
		kept = append(kept, line)
	}
	return bytes.Join(kept, []byte("\n"))
}
