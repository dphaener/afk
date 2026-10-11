package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrUsageUnsupported means the provider has no way to report how much of its
// usage windows is spent. Callers treat it as "nothing to govern on", not as a
// failure.
var ErrUsageUnsupported = errors.New("this provider cannot report usage")

// ErrNoUsageReport means the probe ran but its output held no usable report
// (no report line, empty limits, or no readable row). It is distinct from a
// run failure so callers can tell "the CLI changed shape" from "the CLI is
// missing".
var ErrNoUsageReport = errors.New("the usage probe gave no usable report")

// Window kinds. They are afk's own vocabulary, so the rest of the code never
// depends on the CLI's spelling of a kind.
const (
	WindowSession     = "session"
	WindowWeekly      = "weekly"
	WindowWeeklyModel = "weekly_model"
	WindowUnknown     = "unknown"
)

// Window is one usage window as reported by the provider.
type Window struct {
	Kind string
	// UsedPct is how much of the window is spent, 0-100.
	UsedPct float64
	// ResetsAt is when the window resets; the zero time when unknown.
	ResetsAt time.Time
	// Scope is the model's display name on a weekly_model row, the raw kind
	// on an unknown row, and empty otherwise.
	Scope string
}

// UsageSnapshot is every window the probe reported at one moment.
type UsageSnapshot struct {
	At             time.Time
	Windows        []Window
	OverageEnabled bool
}

// CmdFunc runs a command and returns its stdout.
type CmdFunc = func(ctx context.Context, name string, args ...string) ([]byte, error)

// UsageDeps carries the outside world into Usage. The runner is a parameter
// for the same reason Preflight takes one: the package stays free of os/exec
// and the whole matrix is table-testable without a CLI installed.
type UsageDeps struct {
	Run CmdFunc
	Now func() time.Time
}

func (claude) Usage(ctx context.Context, d UsageDeps) (UsageSnapshot, error) {
	if d.Run == nil {
		return UsageSnapshot{}, errors.New("usage probe: no command runner")
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	out, runErr := d.Run(ctx, "claude", "-p", "/usage", "--output-format", "stream-json", "--verbose")
	snap, parseErr := parseUsageStream(out, now())
	if parseErr == nil {
		// A usable report wins even when the process exited non-zero.
		return snap, nil
	}
	if runErr != nil {
		return UsageSnapshot{}, fmt.Errorf("run claude /usage: %w", runErr)
	}
	return UsageSnapshot{}, parseErr
}

func (codex) Usage(context.Context, UsageDeps) (UsageSnapshot, error) {
	return UsageSnapshot{}, ErrUsageUnsupported
}

func (opencode) Usage(context.Context, UsageDeps) (UsageSnapshot, error) {
	return UsageSnapshot{}, ErrUsageUnsupported
}

// rawReport and rawRow decode every field leniently (RawMessage) so one field
// of the wrong JSON type costs that row, never the whole report.
type rawReport struct {
	RateLimits json.RawMessage `json:"rate_limits"`
}

type rawLimits struct {
	Limits     []json.RawMessage `json:"limits"`
	ExtraUsage json.RawMessage   `json:"extra_usage"`
}

type rawRow struct {
	Kind     json.RawMessage `json:"kind"`
	Percent  json.RawMessage `json:"percent"`
	ResetsAt json.RawMessage `json:"resets_at"`
	Scope    json.RawMessage `json:"scope"`
}

// parseUsageStream finds the usage report in the probe's JSON-lines output.
// It splits with bytes.Split rather than bufio.Scanner because the init line
// is over 10KB and grows, and a default Scanner dies at 64KB per line.
func parseUsageStream(data []byte, now time.Time) (UsageSnapshot, error) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		rep := bytes.TrimSpace(obj["usage_report"])
		if len(rep) == 0 || rep[0] != '{' {
			continue
		}
		return buildSnapshot(rep, now)
	}
	return UsageSnapshot{}, fmt.Errorf("%w: no line carries a usage_report", ErrNoUsageReport)
}

func buildSnapshot(report []byte, now time.Time) (UsageSnapshot, error) {
	var rep rawReport
	if err := json.Unmarshal(report, &rep); err != nil {
		return UsageSnapshot{}, fmt.Errorf("%w: unreadable usage_report", ErrNoUsageReport)
	}
	rl := bytes.TrimSpace(rep.RateLimits)
	if len(rl) == 0 || rl[0] != '{' {
		return UsageSnapshot{}, fmt.Errorf("%w: rate_limits is missing or null", ErrNoUsageReport)
	}
	var lim rawLimits
	if err := json.Unmarshal(rl, &lim); err != nil {
		return UsageSnapshot{}, fmt.Errorf("%w: unreadable rate_limits", ErrNoUsageReport)
	}
	if len(lim.Limits) == 0 {
		return UsageSnapshot{}, fmt.Errorf("%w: no limits listed", ErrNoUsageReport)
	}
	snap := UsageSnapshot{At: now, OverageEnabled: overageEnabled(lim.ExtraUsage)}
	for _, r := range lim.Limits {
		if w, ok := parseRow(r); ok {
			snap.Windows = append(snap.Windows, w)
		}
	}
	if len(snap.Windows) == 0 {
		return UsageSnapshot{}, fmt.Errorf("%w: no limit row had a readable percent", ErrNoUsageReport)
	}
	return snap, nil
}

func overageEnabled(raw json.RawMessage) bool {
	var e struct {
		IsEnabled bool `json:"is_enabled"`
	}
	// A wrong-typed extra_usage or is_enabled reads as "not enabled".
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	return e.IsEnabled
}

func parseRow(raw json.RawMessage) (Window, bool) {
	var r rawRow
	if json.Unmarshal(raw, &r) != nil {
		return Window{}, false
	}
	pct, ok := jsonNumber(r.Percent)
	if !ok {
		return Window{}, false
	}
	// Whole percents, clamped; never a 0-1 fraction, since 0.5 is ambiguous.
	pct = math.Max(0, math.Min(100, pct))

	var kind string
	_ = json.Unmarshal(r.Kind, &kind) // wrong type leaves "" and lands in unknown

	w := Window{UsedPct: pct, ResetsAt: parseResetsAt(r.ResetsAt)}
	switch kind {
	case "session":
		w.Kind = WindowSession
	case "weekly_all":
		w.Kind = WindowWeekly
	case "weekly_scoped":
		w.Kind = WindowWeeklyModel
		var s struct {
			Model struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		}
		if json.Unmarshal(r.Scope, &s) == nil {
			w.Scope = s.Model.DisplayName
		}
	default:
		w.Kind = WindowUnknown
		w.Scope = kind
	}
	return w, true
}

// jsonNumber reads raw only if it is a JSON number. Unmarshalling null or a
// string into a float64 would silently succeed or fail differently, so the
// type is checked first.
func jsonNumber(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return 0, false
	}
	return f, true
}

func parseResetsAt(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return time.Time{}
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}
		}
		return t.UTC()
	}
	if f, ok := jsonNumber(raw); ok && f > 0 && f < 1e12 {
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(frac*1e9)).UTC()
	}
	return time.Time{}
}
