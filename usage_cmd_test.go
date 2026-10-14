package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// fakeProvider implements provider.Provider; Usage returns a canned snapshot
// or error and counts its calls.
type fakeProvider struct {
	name  string
	snap  provider.UsageSnapshot
	err   error
	calls int
}

func (f *fakeProvider) Name() string                           { return f.name }
func (f *fakeProvider) LaunchArgv(string, bool, bool) []string { return nil }
func (f *fakeProvider) ResumeArgv(string) []string             { return nil }

// PlanArgv is here only to satisfy provider.Provider; the usage command never
// starts a planning session.
func (f *fakeProvider) PlanArgv() ([]string, error) { return nil, provider.ErrNoPlanSession }

// PlanConversation is here only to satisfy provider.Provider; the usage
// command never reads a planning conversation.
func (f *fakeProvider) PlanConversation(provider.ConversationQuery) ([]provider.ConversationEntry, error) {
	return nil, provider.ErrNoPlanSession
}
func (f *fakeProvider) Preflight(string, func(string, string, ...string) (string, error)) (bool, string) {
	return true, ""
}
func (f *fakeProvider) Usage(context.Context, provider.UsageDeps) (provider.UsageSnapshot, error) {
	f.calls++
	return f.snap, f.err
}

// SessionTokens is here only to satisfy provider.Provider; the usage command
// never reads token records.
func (f *fakeProvider) SessionTokens(provider.TokenQuery) ([]provider.TokenRecord, error) {
	return nil, provider.ErrTokensUnsupported
}

// Unattended and UnattendedPreflight are here only to satisfy
// provider.Provider; the usage command never launches a session.
func (f *fakeProvider) Unattended([]string) ([]string, error) {
	return nil, provider.ErrUnattendedUnsupported
}
func (f *fakeProvider) UnattendedPreflight(provider.UnattendedDeps) (bool, string) {
	return false, ""
}

// Login is here only to satisfy provider.Provider; the usage command never
// checks the login.
func (f *fakeProvider) Login(context.Context, provider.LoginDeps) (provider.LoginStatus, error) {
	return provider.LoginStatus{}, provider.ErrLoginUnsupported
}

var usageNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func usageWindows(session, week, model float64) []provider.Window {
	return []provider.Window{
		{Kind: provider.WindowSession, UsedPct: session, ResetsAt: usageNow.Add(2 * time.Hour)},
		{Kind: provider.WindowWeekly, UsedPct: week, ResetsAt: usageNow.Add(50 * time.Hour)},
		{Kind: provider.WindowWeeklyModel, UsedPct: model, Scope: "Fable"},
	}
}

func storedFile(prov string, age time.Duration) usagestate.File {
	return usagestate.FromSnapshot(prov, provider.UsageSnapshot{At: usageNow.Add(-age), Windows: usageWindows(10, 20, 30)})
}

// TestRunUsage covers the whole command. Load-bearing rows: "stored 2 min"
// (reuse must not call the probe, or every dashboard refresh would spend a
// probe), "stored 6 min" and "other provider" (the reuse window and provider
// match are both part of the key), "unsupported" (writes nothing: there is no
// reading to store), "probe error with stored" and "with nothing" (a failure
// still prints the governor line, which is hold when nothing fresh is known),
// "write fails" (the report must survive a failed save) and the exit-2 rows
// (a command written wrongly never probes). The "config" rows pin that the
// limits come from the machine config: "sets launch_below" changes the
// verdict for the same reading and stores that verdict, "longer probe
// interval" reuses a reading the default would have replaced and leaves the
// file (and the decision in it) untouched, and "does not load" still prints
// the report on the defaults with one line saying why.
func TestRunUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stored     *usagestate.File
		fake       fakeProvider
		failWrite  bool
		wantCode   int
		wantCalls  int
		wantOut    []string
		wantErr    []string
		wantNoOut  []string
		wantFile   bool
		wantFileSt bool // file content is the stored one, untouched
		// config is what the fake machine config answers; nil leaves
		// LoadConfig unset. configErr makes it fail instead.
		config    *daemonconfig.Governor
		configErr error
		// wantGov is the action stored with the reading; "" means no check.
		wantGov   string
		wantNoErr []string
	}{
		{
			name:      "fresh probe",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			wantCalls: 1,
			wantOut:   []string{"just now", "Session", "30%", "Week (all models)", "71%", "Week (Fable)", "13%", "reset time unknown", "Extra usage", "off", "Governor: run"},
			wantFile:  true,
			wantGov:   "run",
		},
		{
			name:      "config sets launch_below",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			config:    &daemonconfig.Governor{PauseAt: 90, LaunchBelow: 25, StaleAfter: 30 * time.Minute, ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"},
			wantCalls: 1,
			wantOut:   []string{"30%", "Governor: no_launch", "limit of 25%"},
			wantFile:  true,
			wantGov:   "no_launch",
			wantNoErr: []string{"default limits"},
		},
		{
			name:       "config sets a longer probe interval",
			stored:     ptr(storedFile("fake", 6*time.Minute)),
			fake:       fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(99, 99, 99)}},
			config:     &daemonconfig.Governor{PauseAt: 90, LaunchBelow: 75, StaleAfter: 30 * time.Minute, ProbeInterval: 10 * time.Minute, OnUnknownUsage: "run"},
			wantCalls:  0,
			wantOut:    []string{"6 min ago", "10%"},
			wantFile:   true,
			wantFileSt: true,
		},
		{
			name:      "config holds an unsupported provider",
			fake:      fakeProvider{name: "fake", err: provider.ErrUsageUnsupported},
			config:    &daemonconfig.Governor{PauseAt: 90, LaunchBelow: 75, StaleAfter: 30 * time.Minute, ProbeInterval: 5 * time.Minute, OnUnknownUsage: "hold"},
			wantCalls: 1,
			wantOut:   []string{"cannot report how much", "Governor: hold"},
		},
		{
			name:      "config that does not load means the defaults and one line",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			configErr: errors.New("/cfg/afk/config.toml: governor.pause_at is 3; write a percent above 5 and at most 100"),
			wantCalls: 1,
			wantOut:   []string{"Governor: run"},
			wantErr:   []string{"/cfg/afk/config.toml", "governor.pause_at", "default limits"},
			wantFile:  true,
			wantGov:   "run",
		},
		{
			name:       "stored 2 min is reused",
			stored:     ptr(storedFile("fake", 2*time.Minute)),
			fake:       fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(99, 99, 99)}},
			wantCalls:  0,
			wantOut:    []string{"2 min ago", "10%", "Governor: run"},
			wantFile:   true,
			wantFileSt: true,
		},
		{
			name:      "stored 2 min with --refresh probes",
			args:      []string{"--refresh"},
			stored:    ptr(storedFile("fake", 2*time.Minute)),
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			wantCalls: 1,
			wantOut:   []string{"just now", "30%"},
			wantFile:  true,
		},
		{
			name:      "stored 6 min probes",
			stored:    ptr(storedFile("fake", 6*time.Minute)),
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			wantCalls: 1,
			wantOut:   []string{"just now"},
			wantFile:  true,
		},
		{
			name:      "stored from another provider probes",
			stored:    ptr(storedFile("other", 1*time.Minute)),
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			wantCalls: 1,
			wantOut:   []string{"just now"},
			wantFile:  true,
		},
		{
			name:      "unsupported",
			fake:      fakeProvider{name: "fake", err: provider.ErrUsageUnsupported},
			wantCalls: 1,
			wantOut:   []string{"cannot report how much", "Governor: run"},
			wantFile:  false,
		},
		{
			name:       "probe error with stored 10 min",
			stored:     ptr(storedFile("fake", 10*time.Minute)),
			fake:       fakeProvider{name: "fake", err: errors.New("boom")},
			wantCode:   1,
			wantCalls:  1,
			wantOut:    []string{"10 min ago", "Session", "10%", "Governor:"},
			wantErr:    []string{"could not be read", "boom", "installed and logged in", "afk usage --refresh"},
			wantFile:   true,
			wantFileSt: true,
		},
		{
			name:      "probe error with nothing stored",
			fake:      fakeProvider{name: "fake", err: errors.New("boom")},
			wantCode:  1,
			wantCalls: 1,
			wantOut:   []string{"Governor: hold"},
			wantNoOut: []string{"Session"},
			wantErr:   []string{"could not be read"},
		},
		{
			name:      "weekly at 92 pauses with wake time",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(10, 92, 13)}},
			wantCalls: 1,
			wantOut:   []string{"Governor: pause", "wake at Thu 12 Mar 14:01"},
			wantFile:  true,
			wantGov:   "pause",
		},
		{
			name:      "overage on refuses",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(10, 20, 13), OverageEnabled: true}},
			wantCalls: 1,
			wantOut:   []string{"Governor: refuse", "Extra usage", "on"},
			wantFile:  true,
		},
		{
			name:     "unknown provider",
			args:     []string{"--provider", "nosuch"},
			fake:     fakeProvider{name: "fake"},
			wantCode: 2,
			wantErr:  []string{"nosuch", "valid:"},
		},
		{
			name:     "positional argument",
			args:     []string{"extra"},
			fake:     fakeProvider{name: "fake"},
			wantCode: 2,
			wantErr:  []string{"unexpected argument"},
		},
		{
			name:     "unknown flag",
			args:     []string{"--bogus"},
			fake:     fakeProvider{name: "fake"},
			wantCode: 2,
		},
		{
			name:      "write fails but report printed",
			fake:      fakeProvider{name: "fake", snap: provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}},
			failWrite: true,
			wantCode:  1,
			wantCalls: 1,
			wantOut:   []string{"just now", "Session", "Governor: run"},
			wantErr:   []string{"could not be saved"},
		},
		{
			name:    "--help",
			args:    []string{"--help"},
			fake:    fakeProvider{name: "fake"},
			wantErr: []string{"afk usage", "--refresh", "--provider"},
		},
		{
			name:    "help word",
			args:    []string{"help"},
			fake:    fakeProvider{name: "fake"},
			wantErr: []string{"afk usage"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := usagestate.Deps{Getenv: func(k string) string {
				if k == "XDG_STATE_HOME" {
					return dir
				}
				return ""
			}}
			if tt.stored != nil {
				if err := usagestate.Write(st, *tt.stored); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "afk", "usage.json")
			before, _ := os.ReadFile(path)
			if tt.failWrite {
				st.WriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
			}
			fake := tt.fake
			d := usageDeps{
				Lookup: func(n string) (provider.Provider, error) {
					if n == "" || n == "fake" {
						return &fake, nil
					}
					return nil, errors.New("unknown provider \"" + n + "\" (valid: fake)")
				},
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("real runner reached")
					return nil, nil
				},
				Now:   func() time.Time { return usageNow },
				State: st,
				Local: time.UTC,
			}
			if tt.config != nil || tt.configErr != nil {
				d.LoadConfig = func() (daemonconfig.Config, string, error) {
					if tt.configErr != nil {
						return daemonconfig.Config{}, "/cfg/afk/config.toml", tt.configErr
					}
					return daemonconfig.Config{Governor: *tt.config}, "/cfg/afk/config.toml", nil
				}
			}
			var out, errb bytes.Buffer
			code := runUsage(tt.args, &out, &errb, d)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, out.String(), errb.String())
			}
			if fake.calls != tt.wantCalls {
				t.Errorf("probe calls = %d, want %d", fake.calls, tt.wantCalls)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("stdout missing %q:\n%s", w, out.String())
				}
			}
			for _, w := range tt.wantNoOut {
				if strings.Contains(out.String(), w) {
					t.Errorf("stdout has %q:\n%s", w, out.String())
				}
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(errb.String(), w) {
					t.Errorf("stderr missing %q:\n%s", w, errb.String())
				}
			}
			for _, w := range tt.wantNoErr {
				if strings.Contains(errb.String(), w) {
					t.Errorf("stderr has %q:\n%s", w, errb.String())
				}
			}
			if tt.configErr != nil {
				if n := strings.Count(errb.String(), "\n"); n != 1 {
					t.Errorf("stderr has %d lines, want one:\n%s", n, errb.String())
				}
			}
			after, err := os.ReadFile(path)
			if tt.failWrite {
				return
			}
			if tt.wantGov != "" {
				f, ok := usagestate.Read(st)
				switch {
				case !ok || f.Governor == nil:
					t.Errorf("usage.json holds no governor object, want action %q", tt.wantGov)
				case f.Governor.Action != tt.wantGov || f.Governor.Reason == "" || !f.Governor.At.Equal(usageNow):
					t.Errorf("stored governor = %+v, want action %q with a reason, made at %s", *f.Governor, tt.wantGov, usageNow)
				case (tt.wantGov == "pause") != (f.Governor.WakeAt != nil):
					t.Errorf("stored wake_at = %v for action %q", f.Governor.WakeAt, tt.wantGov)
				}
			}
			if (err == nil) != tt.wantFile {
				t.Errorf("usage.json exists = %v, want %v", err == nil, tt.wantFile)
			}
			if tt.wantFileSt && !bytes.Equal(before, after) {
				t.Errorf("usage.json changed; it should be untouched")
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
