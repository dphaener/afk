package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/install"
	"github.com/dphaener/afk/internal/profile"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// installNotTerminal is what a non-interactive `afk install` says. It names
// --dry-run because the usual reason to run it without a terminal is a script
// that only wants to see the plan.
const installNotTerminal = "afk install asks questions, so run it in a terminal. To only see the plan, run afk install --dry-run."

// installRunTimeout bounds every command the installer runs without handing
// the terminal over (gh api, gh auth setup-git, git config, tailscale serve).
// Those read no input, so one that does not answer in this time is stuck, and
// a stuck command must not freeze the screen for good.
const installRunTimeout = 60 * time.Second

// installEnv is everything `afk install` touches outside its arguments. Every
// outside effect goes through one of these fields, so the tests drive the
// whole run against fakes that record each command and file write, and no
// test runs a real command, reads the real home or touches systemd.
type installEnv struct {
	// isTerminal reports whether stdin and stdout are both terminals. Asked
	// only when the run is not --dry-run.
	isTerminal func() bool
	// probe reads the machine. It is install.Probe in the real env; the model
	// sets Facts.RiskAccepted itself, Probe never does.
	probe func() install.Facts
	// runProgram runs the screen until it quits and returns the final model.
	runProgram func(m tea.Model) (tea.Model, error)
	// handover runs argv with the terminal handed to it (tea.ExecProcess) and
	// reports the outcome through done. The screen is paused meanwhile.
	handover func(argv []string, done func(error) tea.Msg) tea.Cmd
	// run runs argv without the terminal and returns its stdout.
	run func(argv []string) ([]byte, error)
	// executable is this binary's path, for `afk plugin install` and `afk web
	// password`.
	executable func() (string, error)
	home       string
	goos       string
	getenv     func(string) string
	setenv     func(key, value string) error
	stat       func(string) (fs.FileInfo, error)
	mkdirAll   func(string, fs.FileMode) error
	readFile   func(string) ([]byte, error)
	// writeText replaces a settings file atomically, keeping its mode
	// (profile.Write); used for profile.toml and config.toml.
	writeText func(path string, data []byte) error
	// writeUnit replaces the systemd unit atomically, mode 0644.
	writeUnit func(path string, data []byte) error
	// writeUnattended turns the agent CLI's permission prompts off.
	writeUnattended func() error
	profilePath     string
	configPath      string
	loadConfig      func() (daemonconfig.Config, error)
	// hostIP is the machine's first non-loopback IPv4 address, or "".
	hostIP func() string
	// checkReport is the text of `afk daemon status` and the stored usage
	// reading, shown on the last screen.
	checkReport func() string
}

func realInstallEnv() installEnv {
	// A failed UserHomeDir leaves home empty; runInstall refuses to go on
	// rather than guess where the files belong.
	home, _ := os.UserHomeDir()
	return installEnv{
		isTerminal: func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) },
		probe:      func() install.Facts { return install.Probe(install.DefaultDeps()) },
		runProgram: func(m tea.Model) (tea.Model, error) { return tea.NewProgram(m).Run() },
		handover:   realInstallHandover,
		run:        realInstallRun,
		executable: os.Executable,
		home:       home,
		goos:       runtime.GOOS,
		getenv:     os.Getenv,
		setenv:     os.Setenv,
		stat:       os.Stat,
		mkdirAll:   os.MkdirAll,
		readFile:   os.ReadFile,
		writeText:  profile.Write,
		// daemon_cmd.go's writer: temp file and rename, mode 0644, so
		// systemd never reads a half-written unit on its next reload.
		writeUnit: writeFileAtomic,
		writeUnattended: func() error {
			return provider.WriteUnattended("", provider.DefaultUnattendedWriteDeps(home))
		},
		profilePath: profile.Path(profile.DefaultDeps()),
		configPath:  daemonconfig.Path(daemonconfig.DefaultDeps()),
		loadConfig:  func() (daemonconfig.Config, error) { return daemonconfig.Load(daemonconfig.DefaultDeps()) },
		hostIP:      realHostIP,
		checkReport: realInstallCheckReport,
	}
}

// realInstallHandover prints the command line first, above the paused
// screen, so whatever the command shows next (a sudo password prompt, a
// browser code) is seen next to the reason it appeared.
func realInstallHandover(argv []string, done func(error) tea.Msg) tea.Cmd {
	c := exec.Command(argv[0], argv[1:]...)
	return tea.Sequence(
		tea.Println("\n→ "+strings.Join(argv, " ")),
		tea.ExecProcess(c, done),
	)
}

func realInstallRun(argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), installRunTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("it did not finish within %s", installRunTimeout)
	}
	return out, err
}

// realHostIP is the first non-loopback IPv4 address. It is what a bind of
// 0.0.0.0 is reached at from the computer that started a Multipass VM.
func realHostIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() {
			continue
		}
		if v4 := ipn.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// realInstallCheckReport reuses the commands' own reports, so the last
// screen says exactly what `afk daemon status` and `afk usage` would. The
// usage reading is the stored one only: asking the agent for a fresh one
// would start the agent CLI from the installer, which is the daemon's job.
func realInstallCheckReport() string {
	var b bytes.Buffer
	runDaemonStatus(&b, &b, realDaemonEnv())
	ud := realUsageDeps()
	if f, ok := usagestate.Read(ud.State); ok {
		snap := f.Snapshot()
		gov := usageGovernorConfig(io.Discard, ud)
		printUsageReport(&b, ud, &snap, ud.Now(), true, gov.Thresholds())
	} else {
		b.WriteString("No usage reading yet. The first one comes once the daemon runs.\n")
	}
	return b.String()
}

// cmdInstall implements `afk install`.
func cmdInstall(args []string) {
	os.Exit(runInstall(args, os.Stdout, os.Stderr, realInstallEnv()))
}

func printInstallUsage(w io.Writer) {
	fmt.Fprint(w, `afk install — set up this machine for afk, step by step

Usage:
  afk install [--dry-run] [--yes]

Looks at this machine, shows every step (the tools, the agent CLI and its
sign-in, the afk plugin, GitHub, git, where projects live, the web page and the
background service) and does the ones not done yet, checking each one after it
runs. A safety screen comes first, every time: afk runs agents with permission
prompts turned off, so it belongs on a machine set aside for it.

Running it again checks everything and only does what is missing. A step that
fails stops the run; the steps before it stay done.

Options:
  --dry-run  Only show the plan. Changes nothing and needs no terminal.
  --yes      Take the default answer to every question instead of asking.
             The safety screen is still shown and still needs your answer.

Exit codes: 0 everything is in place, 1 a step failed or was stopped,
2 the command was written wrongly.
`)
}

func runInstall(args []string, stdout, stderr io.Writer, env installEnv) int {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "Only show the plan; change nothing")
	yes := flags.Bool("yes", false, "Take the default answer to every question")
	flags.Usage = func() { printInstallUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		if flags.Arg(0) == "help" {
			printInstallUsage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "afk install takes no arguments (got %q). Use --dry-run or --yes.\n", flags.Arg(0))
		return 2
	}
	if *dryRun {
		// No terminal check here on purpose: a CI runner calls --dry-run to
		// see the plan, and printing it touches nothing.
		fmt.Fprint(stdout, renderInstallPlanText(install.Build(env.probe())))
		return 0
	}
	if !env.isTerminal() {
		fmt.Fprintln(stderr, installNotTerminal)
		return 1
	}
	if env.home == "" {
		fmt.Fprintln(stderr, "afk install could not find your home folder, so it cannot tell where anything goes. Make sure HOME is set, then run it again.")
		return 1
	}
	final, err := env.runProgram(newInstallModel(env, *yes))
	if err != nil {
		fmt.Fprintf(stderr, "afk install: the screen stopped: %v. The steps already done stay done; run afk install again to go on.\n", err)
		return 1
	}
	if m, ok := final.(*installModel); ok {
		return m.exitCode
	}
	return 1
}

// installMarkText is the plain mark for a step in the --dry-run list.
func installMarkText(s install.State) string {
	switch s {
	case install.Done:
		return "[done]  "
	case install.Cannot:
		return "[cannot]"
	}
	return "[to do] "
}

// installRiskText says in plain words what kind of machine this is.
func installRiskText(r install.Risk) string {
	switch r {
	case install.RiskDedicated:
		return "This machine looks set aside for afk: no desktop, and not a virtual machine."
	case install.RiskVM:
		return "This is a virtual machine or a container, a good place for afk."
	case install.RiskPersonal:
		return "This looks like a computer you use every day."
	}
	return "afk could not tell what kind of machine this is, so it treats it like a computer you use every day."
}

// renderInstallPlanText is the --dry-run output: one line per step, with no
// colour, so it reads the same in a CI log as in a terminal.
func renderInstallPlanText(p install.Plan) string {
	var b strings.Builder
	b.WriteString("afk install would do this (nothing was changed):\n\n")
	b.WriteString(installRiskText(p.Risk) + "\n\n")
	for _, s := range p.Steps {
		line := installMarkText(s.State) + " " + s.Title + " — " + s.Reason
		if s.State == install.Cannot && s.Fix != "" {
			line += " To fix: " + s.Fix
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nRun afk install in a terminal to do the steps marked to do.\n")
	return b.String()
}
