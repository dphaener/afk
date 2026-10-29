package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dphaener/afk/internal/probe"
	"github.com/dphaener/afk/internal/rex"
)

// rexCheckDeadline bounds the whole `afk _rex-check` run, probe and session
// list together, so a hung Rex server cannot hang the command.
const rexCheckDeadline = 10 * time.Second

// cmdRexCheck implements the hidden `afk _rex-check` subcommand: a live check
// that afk can talk to the local Rex server. It is a developer tool, so it is
// left out of printUsage and docs/reference.md, like `_host`.
func cmdRexCheck(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "afk _rex-check: takes no arguments")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rexCheckDeadline)
	defer cancel()
	d := probe.DefaultRexDeps()
	caller := rex.NewCaller(rex.NewRun(rexCheckDeadline), "rex")
	code := runRexCheck(ctx, os.Stdout, d, caller)
	cancel()
	os.Exit(code)
}

// runRexCheck prints what ProbeRex found in plain words and, when the server
// answers with a usable revision, the sessions it lists. It returns 0 when
// Rex is usable and 1 when it is not installed, does not answer, or is too
// old, after saying what to do next. The seams are parameters so the table
// test never touches a real Rex.
func runRexCheck(ctx context.Context, w io.Writer, d probe.RexDeps, c rex.Caller) int {
	f := probe.ProbeRex(ctx, d)
	fmt.Fprintf(w, "Rex installed: %s\n", yesNo(f.Installed))
	fmt.Fprintf(w, "Rex server answers: %s\n", yesNo(f.ServerAnswers))
	if f.ServerAnswers {
		if f.RevisionKnown {
			fmt.Fprintf(w, "Rex api revision: %d\n", f.Revision)
		} else {
			fmt.Fprintln(w, "Rex api revision: unknown (the proxy hides it)")
		}
	}
	fmt.Fprintf(w, "Inside a Rex terminal: %s\n", yesNo(f.InsideRex))

	switch {
	case !f.Installed:
		fmt.Fprintln(w, "Rex is not installed. It is optional: install it to let afk use it, or carry on without it.")
		return 1
	case !f.ServerAnswers:
		fmt.Fprintln(w, "Rex is installed but its server did not answer. Start it with `rex server start`, then run this again.")
		return 1
	case f.RevisionKnown && f.Revision < rex.MinRevision:
		fmt.Fprintf(w, "Rex is too old: afk needs api revision %d or later. Update Rex, then run this again.\n", rex.MinRevision)
		return 1
	}

	sessions, err := rex.ListSessions(ctx, c)
	if err != nil {
		fmt.Fprintf(w, "Rex answered, but listing its sessions failed: %v\nCheck that the Rex server is healthy, then run this again.\n", err)
		return 1
	}
	if len(sessions) == 0 {
		fmt.Fprintln(w, "Rex has no sessions.")
		return 0
	}
	fmt.Fprintf(w, "Rex sessions (%d):\n", len(sessions))
	for _, s := range sessions {
		fmt.Fprintf(w, "  %s  %s\n", s.ID, s.Label)
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
