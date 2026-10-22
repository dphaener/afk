package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/activity"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/tokens"
)

func activityTestParts() dashActivityParts {
	return dashActivityParts{tail: activity.Tail, loc: time.UTC}
}

func writeActivityLog(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decisionLine(at time.Time) string {
	return fmt.Sprintf(`{"at":%q}`+"\n", at.Format(time.RFC3339))
}

func tokenLine(at time.Time, w float64) string {
	return fmt.Sprintf(`{"at":%q,"weighted":%v}`+"\n", at.Format(time.RFC3339), w)
}

// TestLoadDashActivity pins the loader over the real logs.
//
// Load-bearing rows: "two projects" (counts stay per root while the week
// sums are added), "no logs" (a missing log is zero and the week is still
// known, not an error), "big file cut" (only the end of a log over 1 MB is
// read, so the old line at the front must not count) and "unreadable" (one
// bad path must neither fail the others nor draw a made-up quiet day).
func TestLoadDashActivity(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Minute)
	yesterday := now.Add(-24 * time.Hour)

	t.Run("two projects", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		writeActivityLog(t, decisionlog.Path(a), decisionLine(recent)+decisionLine(recent))
		writeActivityLog(t, decisionlog.Path(b), decisionLine(recent))
		writeActivityLog(t, tokens.LedgerPath(a), tokenLine(recent, 100)+tokenLine(yesterday, 10))
		writeActivityLog(t, tokens.LedgerPath(b), tokenLine(recent, 5))
		got := loadDashActivity(activityTestParts(), []string{a, b}, now)
		if got.Lines[a][activity.Buckets-1] != 2 || got.Lines[b][activity.Buckets-1] != 1 {
			t.Fatalf("Lines = %v, want 2 and 1 in the last cell", got.Lines)
		}
		if !got.WeekKnown || got.Week.Sums[6] != 105 || got.Week.Sums[5] != 10 {
			t.Fatalf("Week = %+v known=%v, want today 105 and yesterday 10", got.Week.Sums, got.WeekKnown)
		}
		if got.Week.Days[6].IsZero() {
			t.Fatal("Week.Days not set")
		}
	})

	t.Run("no logs", func(t *testing.T) {
		a := t.TempDir()
		got := loadDashActivity(activityTestParts(), []string{a}, now)
		if c, ok := got.Lines[a]; !ok || c != [activity.Buckets]int{} {
			t.Fatalf("Lines[a] = %v ok=%v, want an entry of zeros", c, ok)
		}
		if !got.WeekKnown || got.Week.Sums != [7]float64{} {
			t.Fatalf("Week = %+v known=%v, want known zeros", got.Week.Sums, got.WeekKnown)
		}
	})

	t.Run("big file cut", func(t *testing.T) {
		a := t.TempDir()
		pad := strings.Repeat(" ", 1000)
		var b strings.Builder
		b.WriteString(decisionLine(recent)) // the oldest line, cut off
		for i := 0; i < 1100; i++ {
			b.WriteString(`{"at":"nope","pad":"` + pad + `"}` + "\n")
		}
		b.WriteString(decisionLine(recent)) // the newest, kept
		writeActivityLog(t, decisionlog.Path(a), b.String())
		got := loadDashActivity(activityTestParts(), []string{a}, now)
		if n := got.Lines[a][activity.Buckets-1]; n != 1 {
			t.Fatalf("last cell = %d, want 1 (the front line is past the 1 MB read)", n)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		bad, good := t.TempDir(), t.TempDir()
		// A folder where the log should be: opening works, reading fails.
		if err := os.MkdirAll(decisionlog.Path(bad), 0o755); err != nil {
			t.Fatal(err)
		}
		writeActivityLog(t, tokens.LedgerPath(bad), tokenLine(recent, 7))
		writeActivityLog(t, decisionlog.Path(good), decisionLine(recent))
		writeActivityLog(t, tokens.LedgerPath(good), tokenLine(recent, 3))
		got := loadDashActivity(activityTestParts(), []string{bad, good}, now)
		if _, ok := got.Lines[bad]; ok {
			t.Fatal("unreadable root has an entry, want none")
		}
		if got.Lines[good][activity.Buckets-1] != 1 {
			t.Fatalf("good root = %v, want one line", got.Lines[good])
		}
		if got.Week.Sums[6] != 10 {
			t.Fatalf("week today = %v, want 10", got.Week.Sums[6])
		}
	})

	t.Run("ledger unreadable everywhere", func(t *testing.T) {
		a := t.TempDir()
		p := dashActivityParts{
			tail: func(string, int64) ([]string, error) { return nil, errors.New("denied") },
			loc:  time.UTC,
		}
		got := loadDashActivity(p, []string{a}, now)
		if got.WeekKnown || len(got.Lines) != 0 {
			t.Fatalf("got %+v, want nothing known", got)
		}
	})
}
