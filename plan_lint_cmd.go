package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/projectconfig"
)

const planLintUsageLine = "afk plan lint [--project DIR] [--json] [--summary] [FILE]"

func printPlanLintUsage(w io.Writer) {
	fmt.Fprintf(w, `afk plan lint — check a master plan

Usage:
  %s

Reads the master plan (FILE, or the plan named in afk.toml, which is
docs/PLAN.md by default) and lists what is wrong with it. An error is
something the daemon would refuse or misread; a warning is something a reader
would miss. Warnings do not fail the command.
  --json     print the findings and the counts as one JSON object
  --summary  print only the counts (milestones, tasks, owner tasks, open
             questions, errors, warnings) instead of the findings

Exit codes: 0 no errors, 1 at least one error or the plan could not be read,
2 the command was written wrongly.
`, planLintUsageLine)
}

// runPlanLint implements `afk plan lint` and returns the exit code. It holds
// no rule logic: planlint.Lint decides what is wrong, and this only finds the
// file, calls it and prints, so the checker stays a pure function the daemon's
// reading can be compared against.
//
// The no-FILE path resolves the plan exactly as afk tasks does
// (resolveBuildRoot, then projectconfig.Load) but does not use
// loadBuildState: that reads the ledger and stops at the first problem, while
// lint exists to list all of them.
func runPlanLint(args []string, stdout, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) int {
	flags := flag.NewFlagSet("plan lint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	proj := addProjectFlag(flags)
	asJSON := flags.Bool("json", false, "Print the result as JSON")
	summaryOnly := flags.Bool("summary", false, "Print only the counts")
	flags.Usage = func() { printPlanLintUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "afk plan lint: "+format+"\nUsage: %s\n", append(a, planLintUsageLine)...)
		return 2
	}
	if flags.NArg() > 1 {
		return usage("give one file at most, but got %d.", flags.NArg())
	}

	var path, shown string
	if flags.NArg() == 1 {
		if *proj != "" {
			return usage("give a file or --project, not both")
		}
		shown = flags.Arg(0)
		path = shown
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
	} else {
		root, err := resolveBuildRoot(*proj, cwd, home, gitCommonDir)
		if err != nil {
			return usage("cannot tell which project to use. Pass --project with the folder that holds the master plan.")
		}
		cfg, err := projectconfig.Load(root)
		if err != nil {
			fmt.Fprintf(stderr, "afk plan lint: %v\n", err)
			return 1
		}
		path = cfg.PlanPath(root)
		shown = path
		if cwd != "" {
			if rel, err := filepath.Rel(cwd, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				shown = rel
			}
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "afk plan lint: no master plan at %s; set plan in afk.toml, create the file, or name a file\n", path)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "afk plan lint: cannot read %s: %v\n", path, err)
		return 1
	}

	rep := planlint.Lint(data)
	code := 0
	if rep.Summary.Errors > 0 {
		code = 1
	}

	if *asJSON {
		out := struct {
			File     string             `json:"file"`
			Findings []planlint.Finding `json:"findings"`
			Summary  planlint.Summary   `json:"summary"`
		}{shown, rep.Findings, rep.Summary}
		if out.Findings == nil {
			out.Findings = []planlint.Finding{}
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "afk plan lint: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", b)
		return code
	}

	s := rep.Summary
	if *summaryOnly {
		fmt.Fprintf(stdout, "Milestones: %d\nTasks: %d\nOwner tasks: %d\nOpen questions: %d\nErrors: %d\nWarnings: %d\n",
			s.Milestones, s.Tasks, s.OwnerTasks, s.OpenQuestions, s.Errors, s.Warnings)
		return code
	}
	for _, f := range rep.Findings {
		loc := shown
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", shown, f.Line)
		}
		task := ""
		if f.Task != "" {
			task = f.Task + ": "
		}
		fmt.Fprintf(stdout, "%s: %s: %s%s [%s]\n", loc, f.Level, task, f.Message, f.Rule)
	}
	if s.Errors == 0 && s.Warnings == 0 {
		fmt.Fprintf(stdout, "%s: no problems found\n", shown)
	} else {
		fmt.Fprintf(stdout, "%s: %s, %s\n", shown, plural(s.Errors, "error"), plural(s.Warnings, "warning"))
	}
	return code
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
