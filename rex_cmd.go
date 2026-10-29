package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dphaener/afk/internal/rex"
)

const rexUsage = `Usage: afk rex setup --hub LABEL [--print]

Writes afk.lua, a set of afk actions for Rex's palette, beside your Rex init.lua.
It never edits init.lua itself.

  --hub LABEL  the name your Rex gives the machine that runs afk (required)
  --print      print afk.lua to the screen and write nothing

The folder is the one holding the file named by REX_CONFIG when that is set,
else ~/.config/rex.
`

// rexSetup is what `afk rex setup` was asked to do, after parsing.
type rexSetup struct {
	hub   string
	print bool
	// dir is where afk.lua goes. Empty when print is set, because nothing is
	// written then and no folder needs to be worked out.
	dir string
	// help is set when the owner asked for the usage text.
	help bool
}

// planRexSetup turns the arguments and environment into a rexSetup. It is pure
// (env is injected) so the whole matrix is table-testable without touching the
// owner's files. A returned error is a plain sentence for exit code 2.
func planRexSetup(args []string, env func(string) string) (rexSetup, error) {
	fs := flag.NewFlagSet("afk rex setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hub := fs.String("hub", "", "")
	prt := fs.Bool("print", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return rexSetup{help: true}, nil
		}
		return rexSetup{}, fmt.Errorf("%v. Run \"afk rex setup --help\" to see the options", err)
	}
	if fs.NArg() > 0 {
		return rexSetup{}, fmt.Errorf("unexpected argument %q. Run \"afk rex setup --help\" to see the options", fs.Arg(0))
	}
	if *hub == "" {
		return rexSetup{}, errors.New("--hub is needed: it is the name your Rex gives the machine that runs afk, for example --hub vm")
	}
	// RenderPalette is the one judge of a label; ask it so the rule lives once.
	if _, err := rex.RenderPalette(*hub); err != nil {
		return rexSetup{}, fmt.Errorf("--hub: %v", err)
	}
	s := rexSetup{hub: *hub, print: *prt}
	if s.print {
		return s, nil
	}
	if cfg := env("REX_CONFIG"); cfg != "" {
		s.dir = filepath.Dir(cfg)
		return s, nil
	}
	home := env("HOME")
	if home == "" {
		return rexSetup{}, errors.New("cannot find your home folder to locate Rex's settings. Set REX_CONFIG to the path of your Rex init.lua")
	}
	s.dir = filepath.Join(home, ".config", "rex")
	return s, nil
}

// rexWriter is the file write `afk rex setup` does, injected so tests never
// touch real files. It makes dir when missing and writes dir/afk.lua, 0644.
type rexWriter func(dir string, data []byte) (path string, err error)

func osRexWriter(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "afk.lua")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// runRex implements `afk rex` and returns the exit code.
func runRex(args []string, stdout, stderr io.Writer, env func(string) string, write rexWriter) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, rexUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "setup" {
		fmt.Fprintf(stderr, "afk rex: unknown command %q. The one command is \"setup\".\n", args[0])
		return 2
	}
	plan, err := planRexSetup(args[1:], env)
	if err != nil {
		fmt.Fprintf(stderr, "afk rex setup: %v\n", err)
		return 2
	}
	if plan.help {
		fmt.Fprint(stdout, rexUsage)
		return 0
	}
	data, err := rex.RenderPalette(plan.hub)
	if err != nil {
		fmt.Fprintf(stderr, "afk rex setup: %v\n", err)
		return 2
	}
	if plan.print {
		stdout.Write(data)
		return 0
	}
	path, err := write(plan.dir, data)
	if err != nil {
		fmt.Fprintf(stderr, "afk rex setup: could not write afk.lua in %s: %v. Check that you can write to that folder.\n", plan.dir, err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s.\nYour Rex init.lua was not changed. To use it, add this line to init.lua:\n\n  require(\"afk\")\n\nthen run `rex config reload`.\n", path)
	return 0
}

// cmdRex implements `afk rex`.
func cmdRex(args []string) {
	os.Exit(runRex(args, os.Stdout, os.Stderr, os.Getenv, osRexWriter))
}
