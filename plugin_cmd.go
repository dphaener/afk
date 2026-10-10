package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/dphaener/afk/internal/pluginstall"
	"github.com/dphaener/afk/internal/prompt"
)

// cmdPlugin implements `afk plugin <subcommand>`.
func cmdPlugin(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printPluginUsage(os.Stderr)
		if len(args) == 0 {
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "install":
		interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
		os.Exit(runPluginInstall(args[1:], os.Stdout, os.Stderr, defaultPluginDeps(os.Stdout), interactive, pickTargets))
	default:
		fmt.Fprintf(os.Stderr, "afk plugin: unknown subcommand %q\n", args[0])
		printPluginUsage(os.Stderr)
		os.Exit(1)
	}
}

func printPluginUsage(w io.Writer) {
	fmt.Fprint(w, `afk plugin — install the afk agent plugin

Usage:
  afk plugin install [--target claude,codex,opencode|all] [--dry-run] [--force]

Without --target, asks which of the agent CLIs found on PATH to install
into. That needs a terminal; in a script, pass --target.
`)
}

func defaultPluginDeps(out io.Writer) pluginstall.Deps {
	return pluginstall.Deps{
		Getenv:   os.Getenv,
		HomeDir:  os.UserHomeDir,
		LookPath: exec.LookPath,
		Run: func(name string, args ...string) (string, error) {
			b, err := exec.Command(name, args...).CombinedOutput()
			return string(b), err
		},
		Now: time.Now,
		Out: out,
	}
}

// pickTargets asks which detected CLIs to install into.
func pickTargets(detected []pluginstall.Target) ([]pluginstall.Target, error) {
	opts := make([]string, len(detected))
	for i, t := range detected {
		opts[i] = string(t)
	}
	picked, err := prompt.PickMany(
		"Install the afk plugin into which agent CLIs?",
		"Found on PATH. Space toggles, enter confirms.",
		opts,
	)
	if err != nil {
		return nil, err
	}
	out := make([]pluginstall.Target, len(picked))
	for i, p := range picked {
		out[i] = pluginstall.Target(p)
	}
	return out, nil
}

// errNeedTarget is resolveInstallTargets' refusal to choose targets without
// a person to ask.
var errNeedTarget = errors.New("no terminal to ask which agent CLIs to install into; pass --target claude,codex,opencode or --target all")

// errPickAborted wraps a failed or cancelled (Ctrl-C) prompt.
var errPickAborted = errors.New("aborted, no changes made")

// resolveInstallTargets decides what to install into. An explicit --target is
// the user's choice and is used as given. Without one, afk never picks for
// the user: it asks on a terminal (pick is the prompt), and refuses
// otherwise, so a script that omits --target cannot silently install into
// every CLI on the machine. Pure apart from pick, so the matrix is
// table-testable without a TTY.
func resolveInstallTargets(targetFlag string, detected []pluginstall.Target, interactive bool,
	pick func([]pluginstall.Target) ([]pluginstall.Target, error)) ([]pluginstall.Target, error) {
	if targetFlag != "" {
		return pluginstall.ParseTargets(targetFlag)
	}
	if len(detected) == 0 {
		return nil, errors.New("no agent CLI found on PATH (claude, codex, opencode); pass --target to choose one")
	}
	if !interactive {
		return nil, errNeedTarget
	}
	picked, err := pick(detected)
	if err != nil {
		return nil, errPickAborted
	}
	return picked, nil
}

// runPluginInstall parses the install flags and runs the install, returning
// the exit code. Split from cmdPlugin so tests run it with fake deps.
func runPluginInstall(args []string, stdout, stderr io.Writer, d pluginstall.Deps, interactive bool,
	pick func([]pluginstall.Target) ([]pluginstall.Target, error)) int {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "Comma-separated agent CLIs to install into (claude, codex, opencode) or \"all\"; default: ask")
	dryRun := fs.Bool("dry-run", false, "Print what would happen without changing anything")
	force := fs.Bool("force", false, "Back up and replace a plugin directory that afk did not install")
	fs.Usage = func() {
		printPluginUsage(stderr)
		fmt.Fprintln(stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "afk plugin install: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	var detected []pluginstall.Target
	if *target == "" {
		detected = pluginstall.Detect(d.LookPath)
	}
	targets, err := resolveInstallTargets(*target, detected, interactive, pick)
	if err != nil {
		fmt.Fprintf(stderr, "afk plugin install: %v\n", err)
		if errors.Is(err, errPickAborted) {
			return 1
		}
		return 2
	}
	if len(targets) == 0 {
		fmt.Fprintln(stdout, "Nothing selected; nothing installed.")
		return 0
	}

	opts := pluginstall.Options{Targets: targets, DryRun: *dryRun, Force: *force}
	if err := pluginstall.Install(pluginAssets, opts, d); err != nil {
		fmt.Fprintf(stderr, "afk plugin install: %v\n", err)
		return 1
	}
	return 0
}
