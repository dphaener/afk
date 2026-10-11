package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fullReport = `{"type":"assistant","usage_report":{"session":{},"rate_limits":{"limits":[` +
	`{"kind":"session","group":"session","percent":24,"resets_at":"2026-10-11T21:49:59.645156+00:00","scope":null,"severity":"normal","is_active":false},` +
	`{"kind":"weekly_all","group":"weekly","percent":71,"resets_at":"2026-10-12T13:59:59.645184+00:00","scope":null,"severity":"normal","is_active":true},` +
	`{"kind":"weekly_scoped","group":"weekly","percent":13,"resets_at":"2026-10-12T13:59:59.645389+00:00","scope":{"model":{"display_name":"Fable"},"surface":null},"severity":"normal","is_active":false}` +
	`],"extra_usage":{"is_enabled":false,"monthly_limit":null,"used_credits":null,"utilization":null,"currency":null}}}}`

// report builds a one-line stream whose limits array is the given rows and
// whose extra_usage is the given JSON ("" omits the key).
func report(rows, extra string) string {
	e := ""
	if extra != "" {
		e = `,"extra_usage":` + extra
	}
	return `{"type":"assistant","usage_report":{"rate_limits":{"limits":[` + rows + `]` + e + `}}}`
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v.UTC()
}

// TestParseUsageStream pins the stream parser. Load-bearing rows: full_report
// (the real shape, offsets and fractional seconds), the null/missing/empty
// rows (each must be ErrNoUsageReport, not a zero snapshot that would read as
// "0% used" and let the governor run past a limit), percent_string and
// percent_missing (a skipped row, never 0%), clamping (never a 0-1 fraction),
// and big_line_before_report (a default bufio.Scanner would drop the report).
func TestParseUsageStream(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	sess := mustTime(t, "2026-10-11T21:49:59.645156Z")
	week := mustTime(t, "2026-10-12T13:59:59.645184Z")
	model := mustTime(t, "2026-10-12T13:59:59.645389Z")

	tests := []struct {
		name    string
		in      string
		want    []Window
		overage bool
		wantErr bool
	}{
		{"full report", `{"type":"system"}` + "\n" + fullReport + "\n" + `{"type":"result"}`,
			[]Window{
				{Kind: WindowSession, UsedPct: 24, ResetsAt: sess},
				{Kind: WindowWeekly, UsedPct: 71, ResetsAt: week},
				{Kind: WindowWeeklyModel, UsedPct: 13, ResetsAt: model, Scope: "Fable"},
			}, false, false},
		{"null rate_limits", `{"usage_report":{"rate_limits":null}}`, nil, false, true},
		{"rate_limits missing", `{"usage_report":{"session":{}}}`, nil, false, true},
		{"no usage_report anywhere", `{"type":"system"}` + "\n" + `{"type":"result"}`, nil, false, true},
		{"empty input", ``, nil, false, true},
		{"missing session window", report(`{"kind":"weekly_all","percent":50}`, ``),
			[]Window{{Kind: WindowWeekly, UsedPct: 50}}, false, false},
		{"unknown kind", report(`{"kind":"weekly_opus","percent":9}`, ``),
			[]Window{{Kind: WindowUnknown, UsedPct: 9, Scope: "weekly_opus"}}, false, false},
		{"row with no kind", report(`{"percent":9}`, ``),
			[]Window{{Kind: WindowUnknown, UsedPct: 9}}, false, false},
		{"percent string skipped", report(`{"kind":"session","percent":"24"},{"kind":"weekly_all","percent":5}`, ``),
			[]Window{{Kind: WindowWeekly, UsedPct: 5}}, false, false},
		{"percent missing skipped", report(`{"kind":"session"},{"kind":"weekly_all","percent":5}`, ``),
			[]Window{{Kind: WindowWeekly, UsedPct: 5}}, false, false},
		{"percent null skipped", report(`{"kind":"session","percent":null},{"kind":"weekly_all","percent":5}`, ``),
			[]Window{{Kind: WindowWeekly, UsedPct: 5}}, false, false},
		{"every row unusable", report(`{"kind":"session","percent":"x"},{"kind":"weekly_all"}`, ``), nil, false, true},
		{"empty limits", report(``, ``), nil, false, true},
		{"percent clamped high and low", report(`{"kind":"session","percent":140},{"kind":"weekly_all","percent":-5}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 100}, {Kind: WindowWeekly, UsedPct: 0}}, false, false},
		{"fraction is not scaled", report(`{"kind":"session","percent":0.5}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 0.5}}, false, false},
		{"resets_at epoch seconds", report(`{"kind":"session","percent":1,"resets_at":1791755399}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 1, ResetsAt: time.Unix(1791755399, 0).UTC()}}, false, false},
		{"resets_at unreadable keeps row", report(`{"kind":"session","percent":1,"resets_at":"soon"}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 1}}, false, false},
		{"resets_at numeric offset to UTC", report(`{"kind":"session","percent":1,"resets_at":"2026-10-11T23:49:59+02:00"}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 1, ResetsAt: mustTime(t, "2026-10-11T21:49:59Z")}}, false, false},
		{"extra_usage missing", report(`{"kind":"session","percent":1}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 1}}, false, false},
		{"extra_usage null", report(`{"kind":"session","percent":1}`, `null`),
			[]Window{{Kind: WindowSession, UsedPct: 1}}, false, false},
		{"extra_usage enabled", report(`{"kind":"session","percent":1}`, `{"is_enabled":true}`),
			[]Window{{Kind: WindowSession, UsedPct: 1}}, true, false},
		{"two scoped models", report(`{"kind":"weekly_scoped","percent":1,"scope":{"model":{"display_name":"A"}}},{"kind":"weekly_scoped","percent":2,"scope":{"model":{"display_name":"B"}}}`, ``),
			[]Window{{Kind: WindowWeeklyModel, UsedPct: 1, Scope: "A"}, {Kind: WindowWeeklyModel, UsedPct: 2, Scope: "B"}}, false, false},
		{"scoped without model", report(`{"kind":"weekly_scoped","percent":1,"scope":null}`, ``),
			[]Window{{Kind: WindowWeeklyModel, UsedPct: 1}}, false, false},
		{"non-json and blank lines first", "not json\n\n   \n[1,2]\n" + report(`{"kind":"session","percent":3}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 3}}, false, false},
		{"big line before report", `{"type":"system","pad":"` + strings.Repeat("x", 200*1024) + `"}` + "\n" + report(`{"kind":"session","percent":3}`, ``),
			[]Window{{Kind: WindowSession, UsedPct: 3}}, false, false},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, err := parseUsageStream([]byte(tt.in), now)
			if tt.wantErr {
				if !errors.Is(err, ErrNoUsageReport) {
					t.Fatalf("err = %v, want ErrNoUsageReport", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got.Windows, tt.want) {
				t.Errorf("Windows = %+v\nwant     %+v", got.Windows, tt.want)
			}
			if got.OverageEnabled != tt.overage {
				t.Errorf("OverageEnabled = %v, want %v", got.OverageEnabled, tt.overage)
			}
			if !got.At.Equal(now) {
				t.Errorf("At = %v, want %v", got.At, now)
			}
		})
	}
}

// TestUsage covers the Provider.Usage seam. Load-bearing rows: argv (the
// probe must stay the zero-cost /usage stream), run_error_empty_stdout (the
// "executable not found" text must reach the user and must not look like a
// shape change), run_error_with_report (a non-zero exit with a good report is
// still a report), and the unsupported providers never calling the runner.
func TestUsage(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	wantArgv := []string{"claude", "-p", "/usage", "--output-format", "stream-json", "--verbose"}
	runErr := errors.New("executable file not found in $PATH")

	t.Run("claude argv and parsed snapshot", func(t *testing.T) {
		var argv []string
		d := UsageDeps{
			Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				argv = append([]string{name}, args...)
				return []byte(fullReport), nil
			},
			Now: func() time.Time { return now },
		}
		snap, err := claude{}.Usage(context.Background(), d)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(argv, wantArgv) {
			t.Errorf("argv = %q, want %q", argv, wantArgv)
		}
		if len(snap.Windows) != 3 || !snap.At.Equal(now) {
			t.Errorf("snapshot = %+v", snap)
		}
	})
	t.Run("claude nil Now uses the real clock", func(t *testing.T) {
		d := UsageDeps{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(fullReport), nil }}
		before := time.Now()
		snap, err := claude{}.Usage(context.Background(), d)
		if err != nil || snap.At.Before(before) {
			t.Fatalf("snap.At = %v err = %v", snap.At, err)
		}
	})
	t.Run("claude run_error empty stdout", func(t *testing.T) {
		d := UsageDeps{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, runErr }}
		_, err := claude{}.Usage(context.Background(), d)
		if !errors.Is(err, runErr) {
			t.Errorf("err = %v, want it to wrap the run error", err)
		}
		if errors.Is(err, ErrNoUsageReport) {
			t.Errorf("run failure must not be ErrNoUsageReport: %v", err)
		}
	})
	t.Run("claude run_error with report on stdout", func(t *testing.T) {
		d := UsageDeps{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(fullReport), runErr }}
		snap, err := claude{}.Usage(context.Background(), d)
		if err != nil || len(snap.Windows) != 3 {
			t.Errorf("snap = %+v err = %v, want the report", snap, err)
		}
	})
	t.Run("claude clean run but no_usage_report", func(t *testing.T) {
		d := UsageDeps{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"type":"result"}`), nil }}
		_, err := claude{}.Usage(context.Background(), d)
		if !errors.Is(err, ErrNoUsageReport) {
			t.Errorf("err = %v, want ErrNoUsageReport", err)
		}
	})
	t.Run("claude nil runner", func(t *testing.T) {
		_, err := claude{}.Usage(context.Background(), UsageDeps{})
		if err == nil || errors.Is(err, ErrNoUsageReport) || errors.Is(err, ErrUsageUnsupported) {
			t.Errorf("err = %v, want a plain error", err)
		}
	})
	for _, name := range []string{CodexName, OpenCodeName} {
		t.Run(fmt.Sprintf("%s unsupported", name), func(t *testing.T) {
			called := false
			d := UsageDeps{Run: func(context.Context, string, ...string) ([]byte, error) {
				called = true
				return []byte(fullReport), nil
			}}
			p, err := Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := p.Usage(context.Background(), d)
			if !errors.Is(err, ErrUsageUnsupported) {
				t.Errorf("err = %v, want ErrUsageUnsupported", err)
			}
			if called {
				t.Error("runner was called")
			}
			if !reflect.DeepEqual(snap, UsageSnapshot{}) {
				t.Errorf("snap = %+v, want zero", snap)
			}
		})
	}
}
