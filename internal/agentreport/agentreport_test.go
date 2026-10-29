package agentreport

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestAgentReportBlocked runs every state Claude Code sends (docs/PLAN.md §8
// T17.1) and every blocked kind. The load-bearing rows are the three blocked
// kinds, because the stall rule counts a permission and an auth wait as
// owner waits too, and "working with a question msg", because T17.1 saw that
// record arrive just before the real question and it must not count as a
// wait. "blocked with no kind" pins that the kind is never consulted.
func TestAgentReportBlocked(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want bool
	}{
		{"blocked permission", Report{State: "blocked", Kind: "permission", Msg: "approve Bash: touch hello.txt"}, true},
		{"blocked question", Report{State: "blocked", Kind: "question", Msg: "answer: Which colour?"}, true},
		{"blocked auth", Report{State: "blocked", Kind: "auth"}, true},
		{"blocked with no kind", Report{State: "blocked"}, true},
		{"working", Report{State: "working"}, false},
		{"working with a question msg", Report{State: "working", Msg: "answer: Which colour?"}, false},
		{"done", Report{State: "done"}, false},
		{"idle", Report{State: "idle"}, false},
		{"error", Report{State: "error", Msg: "crashed"}, false},
		{"clear", Report{State: "clear"}, false},
		{"empty state", Report{}, false},
		{"kind without blocked state", Report{State: "working", Kind: "question"}, false},
		{"state in another case", Report{State: "Blocked", Kind: "question"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Blocked(tt.r); got != tt.want {
				t.Errorf("Blocked(%+v) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

// TestAgentReportFresh checks a report's time against the host's start and
// the last exit. The load-bearing rows are "equal to last exit" and "equal
// to host start", which pin that a same-second report is stale (RFC 3339 has
// one-second resolution, so it cannot be told from one before the exit), and
// "older than last exit", the record a crashed agent leaves behind under a
// living host. "zero report time" pins that an absent report is never fresh
// even with no bounds at all.
func TestAgentReportFresh(t *testing.T) {
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	exit := start.Add(10 * time.Minute)
	var zero time.Time

	tests := []struct {
		name     string
		at       time.Time
		started  time.Time
		lastExit time.Time
		want     bool
	}{
		{"newer than both bounds", exit.Add(time.Second), start, exit, true},
		{"older than host start", start.Add(-time.Second), start, zero, false},
		{"older than host start with a last exit", start.Add(-time.Second), start, exit, false},
		{"older than last exit", exit.Add(-time.Second), start, exit, false},
		{"equal to last exit", exit, start, exit, false},
		{"equal to host start", start, start, zero, false},
		{"newer than host start, no last exit", start.Add(time.Second), start, zero, true},
		{"newer than last exit, no host start", exit.Add(time.Second), zero, exit, true},
		{"zero bounds", start, zero, zero, true},
		{"zero report time", zero, zero, zero, false},
		{"zero report time with bounds", zero, start, exit, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Report{State: "blocked", Kind: "question", At: tt.at}
			if got := Fresh(r, tt.started, tt.lastExit); got != tt.want {
				t.Errorf("Fresh(at=%v, started=%v, lastExit=%v) = %v, want %v (stale when false)",
					tt.at, tt.started, tt.lastExit, got, tt.want)
			}
		})
	}
}

// TestAgentReportClean checks the text a stall item quotes. The load-bearing
// rows are "control bytes", because the msg is the agent's raw text and an
// escape sequence must never reach a push message, "newline between words",
// which pins that white space becomes a space before control characters go
// (or the words would join), and "long message", which pins the 200-rune cut
// with its ellipsis. "empty message" and "only white space" pin that nothing
// to quote stays "", so the caller can keep its old text.
func TestAgentReportClean(t *testing.T) {
	long := strings.Repeat("a", 250)
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"plain message", "answer: Which colour?", "answer: Which colour?"},
		{"control bytes", "answer:\x1b[31m red\x07\x00 now\x7f", "answer:[31m red now"},
		{"newline between words", "approve\nBash:\ttouch\r\nhello.txt", "approve Bash: touch hello.txt"},
		{"runs of white space", "  answer:   Which    colour?  ", "answer: Which colour?"},
		{"invalid utf-8", "answer:\xff ok", "answer: ok"},
		{"unicode kept", "answer: Q (Red · Green · Blue)", "answer: Q (Red · Green · Blue)"},
		{"long message", long, strings.Repeat("a", MaxQuote-1) + "…"},
		{"exactly the limit", strings.Repeat("b", MaxQuote), strings.Repeat("b", MaxQuote)},
		{"cut at a space", strings.Repeat("c", MaxQuote-2) + " dddd", strings.Repeat("c", MaxQuote-2) + "…"},
		{"empty message", "", ""},
		{"only white space", " \t\r\n  ", ""},
		{"only control bytes", "\x1b\x07\x00", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clean(tt.msg)
			if got != tt.want {
				t.Errorf("Clean(%q) = %q, want %q", tt.msg, got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > MaxQuote {
				t.Errorf("Clean(%q) is %d runes, over %d", tt.msg, n, MaxQuote)
			}
		})
	}
}
