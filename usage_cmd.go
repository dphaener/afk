package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/usagetext"
)

// usageProbeTimeout bounds one probe. The agent CLI normally answers in a
// couple of seconds; a minute covers a slow start without letting a wedged
// process hang the command.
const usageProbeTimeout = 60 * time.Second

// usageDeps carries the outside world into runUsage so the whole flow is
// testable with buffers, a fake provider and a temp directory.
type usageDeps struct {
	Lookup func(string) (provider.Provider, error)
	Run    provider.CmdFunc
	Now    func() time.Time
	State  usagestate.Deps
	Local  *time.Location
	// LoadConfig reads the machine config for the [governor] table. Nil means
	// the default limits, which is what a test gets unless it sets one. It
	// returns the file's path so a failure can name it.
	LoadConfig func() (cfg daemonconfig.Config, path string, err error)
}

// realUsageDeps builds the real dependencies of the usage flow: the real
// provider lookup, the probe runner, the clock, afk's state folder and the
// machine config. It is a constructor of its own so the dashboard can reuse it.
func realUsageDeps() usageDeps {
	return usageDeps{
		Lookup: provider.Lookup,
		Run:    realUsageRunner(usagestate.DefaultDeps()),
		Now:    time.Now,
		State:  usagestate.DefaultDeps(),
		Local:  time.Local,
		LoadConfig: func() (daemonconfig.Config, string, error) {
			cd := daemonconfig.DefaultDeps()
			cfg, err := daemonconfig.Load(cd)
			return cfg, daemonconfig.Path(cd), err
		},
	}
}

// cmdUsage implements `afk usage`. The real dependencies are built by
// realUsageDeps and not in runUsage, so tests never reach a real command or the real home.
func cmdUsage(args []string) {
	deps := realUsageDeps()
	os.Exit(runUsage(args, os.Stdout, os.Stderr, deps))
}

// realUsageRunner runs the probe command with its working directory set to
// afk's state directory. The agent CLI records each run under the directory
// it was started in, and a probe every few minutes would otherwise pile
// records into whatever project the owner ran afk from. If the directory
// cannot be resolved or created, the working directory is left unset and the
// probe still runs.
func realUsageRunner(st usagestate.Deps) provider.CmdFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if dir, err := usagestate.Dir(st); err == nil {
			if os.MkdirAll(dir, 0o755) == nil {
				cmd.Dir = dir
			}
		}
		return cmd.Output()
	}
}

func printUsageCmdHelp(w io.Writer) {
	fmt.Fprintf(w, `afk usage — show how much of the agent subscription's usage windows is spent

Usage:
  afk usage [--provider NAME] [--refresh]

Reads the usage windows (session, week, per-model week), prints how full each
one is and when it resets, and says what afk's usage governor would do about it.

Options:
  --provider NAME  Which agent to read (%s). Default: %s.
  --refresh        Read the numbers again even if a recent reading is stored.

A reading less than %d minutes old is reused instead of asking the agent again.
Readings are stored per machine in usage.json under afk's state directory.

Exit codes: 0 the report was printed, 1 usage could not be read or saved,
2 the command was written wrongly.
`, strings.Join(provider.Names(), ", "), provider.Default, int(governor.DefaultProbeInterval/time.Minute))
}

// runUsage implements `afk usage` and returns the exit code.
func runUsage(args []string, stdout, stderr io.Writer, d usageDeps) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provName := fs.String("provider", "", "Which agent to read ("+strings.Join(provider.Names(), ", ")+")")
	refresh := fs.Bool("refresh", false, "Read again even if a recent reading is stored")
	fs.Usage = func() { printUsageCmdHelp(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		if fs.Arg(0) == "help" {
			printUsageCmdHelp(stderr)
			return 0
		}
		fmt.Fprintf(stderr, "afk usage: unexpected argument %q. This command takes only --provider and --refresh.\n", fs.Arg(0))
		return 2
	}

	p, err := d.Lookup(*provName)
	if err != nil {
		fmt.Fprintf(stderr, "afk usage: %v\n", err)
		return 2
	}

	gov := usageGovernorConfig(stderr, d)
	th := gov.Thresholds()

	now := d.Now()
	stored, haveStored := usagestate.Read(d.State)
	if haveStored && stored.Provider != p.Name() {
		haveStored = false
	}
	if haveStored && !*refresh {
		if age := now.Sub(stored.At); age >= 0 && age < gov.ProbeInterval {
			// Nothing is written here, so the decision stored with the
			// reading stays as its writer left it.
			snap := stored.Snapshot()
			printUsageReport(stdout, d, &snap, now, true, th)
			return 0
		}
	}

	res := refreshUsageForSurface(p, gov, d)
	switch res.Outcome {
	case usageRefreshUnsupported:
		dec := governor.Decide(governor.Input{Unsupported: true, Now: now, Thresholds: th})
		fmt.Fprintf(stdout, "Usage is unknown for %s: this provider cannot report how much of its usage is spent.\n", p.Name())
		writeGovernorLine(stdout, d, dec)
		return 0
	case usageRefreshFailed:
		fmt.Fprintf(stderr, "afk usage: usage could not be read: %v\n"+
			"Check that the agent's command-line tool is installed and logged in with an interactive login, then run 'afk usage --refresh'.\n", res.Err)
		var fallback *provider.UsageSnapshot
		if haveStored {
			s := stored.Snapshot()
			fallback = &s
		}
		printUsageReport(stdout, d, fallback, now, true, th)
		return 1
	}

	code := 0
	if res.Outcome == usageRefreshSaveFailed {
		fmt.Fprintf(stderr, "afk usage: the reading could not be saved: %v\nCheck that afk's state folder can be written to.\n", res.Err)
		code = 1
	}
	printUsageReport(stdout, d, &res.Snapshot, now, false, th)
	return code
}

// usageGovernorConfig gives the usage limits `afk usage` judges by: the
// machine config's [governor] table, the same one the daemon reads, so the
// two never disagree about what a reading means.
//
// A config that does not load is not a reason to refuse: the command is how
// the owner looks at usage, often while fixing that very file. It says so in
// one line and goes on with the defaults. A missing file is not a failure at
// all (daemonconfig.Load answers the defaults for it).
func usageGovernorConfig(stderr io.Writer, d usageDeps) daemonconfig.Governor {
	th := governor.DefaultThresholds()
	def := daemonconfig.Governor{PauseAt: th.PauseAt, LaunchBelow: th.LaunchBelow, StaleAfter: th.StaleAfter,
		ProbeInterval: governor.DefaultProbeInterval, OnUnknownUsage: th.OnUnknownUsage}
	if d.LoadConfig == nil {
		return def
	}
	cfg, path, err := d.LoadConfig()
	if err != nil {
		// The loader's own errors start with the file's path; the path is
		// added only for one that does not, so it is never said twice.
		msg := err.Error()
		if path != "" && !strings.Contains(msg, path) {
			msg = path + ": " + msg
		}
		fmt.Fprintf(stderr, "afk usage: the machine config could not be used, so the default limits are used: %s\n", msg)
		return def
	}
	return cfg.Governor
}

// printUsageReport writes the report. A nil snapshot prints no
// windows but still prints the governor line (it reads "hold").
func printUsageReport(w io.Writer, d usageDeps, snap *provider.UsageSnapshot, now time.Time, reused bool, th governor.Thresholds) {
	if snap == nil {
		fmt.Fprintln(w, "No usage reading is available.")
	} else {
		fmt.Fprintf(w, "Usage read %s:\n", ageText(now.Sub(snap.At), reused))
		for _, win := range snap.Windows {
			fmt.Fprintf(w, "  %-22s %3.0f%% used, %s\n", windowLabel(win), win.UsedPct, resetText(win.ResetsAt, now, d.Local))
		}
		extra := "off"
		if snap.OverageEnabled {
			extra = "on"
		}
		fmt.Fprintf(w, "  %-22s  %s\n", "Extra usage", extra)
	}
	dec := governor.Decide(governor.Input{Snapshot: snap, SessionRunning: false, Now: now, Thresholds: th})
	writeGovernorLine(w, d, dec)
}

func writeGovernorLine(w io.Writer, d usageDeps, dec governor.Decision) {
	line := fmt.Sprintf("Governor: %s. %s", dec.Action, dec.Reason)
	if dec.Action == governor.ActionPause {
		if dec.WakeAt.IsZero() {
			line += " There is no known wake time."
		} else {
			line += " It would wake at " + dec.WakeAt.In(d.Local).Format("Mon 2 Jan 15:04") + "."
		}
	}
	fmt.Fprintln(w, line)
}

func windowLabel(w provider.Window) string {
	return usagetext.WindowLabel(w.Kind, w.Scope)
}

// ageText says how long ago the numbers were read. A live probe's age is
// under a minute, which reads as "just now"; a reused or fallback reading
// (reused is true) is never "just now", because the numbers were not read for
// this command.
func ageText(age time.Duration, fallback bool) string {
	m := int(age / time.Minute)
	if m < 1 {
		if fallback {
			return "less than a minute ago"
		}
		return "just now"
	}
	return fmt.Sprintf("%d min ago", m)
}

func resetText(at, now time.Time, loc *time.Location) string {
	if at.IsZero() {
		return "reset time unknown"
	}
	until := at.Sub(now)
	when := at.In(loc).Format("Mon 2 Jan 15:04")
	if until <= 0 {
		return "resets " + when + " (already passed)"
	}
	return "resets " + when + " (in " + untilText(until) + ")"
}

func untilText(d time.Duration) string {
	mins := int(d / time.Minute)
	switch {
	case mins >= 24*60:
		return fmt.Sprintf("%dd %dh", mins/(24*60), mins%(24*60)/60)
	case mins >= 60:
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}
