package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/worktree"
)

// descriptionReadCap bounds how much of description.md is read to find a task
// id. The id is in the first lines; a description is prose that a person
// pasted, and reading all of an unexpectedly large file for one regexp match
// would be wasted work.
const descriptionReadCap = 10 * 1024

const tokensUsageLine = "Usage: afk tokens [--project PATH] [--record] [SESSION|TASK]"

func printTokensUsage(w io.Writer) {
	fmt.Fprint(w, `afk tokens — show how many tokens a task used

Usage:
  afk tokens [--project PATH] [--record] [SESSION|TASK]

  (no argument)  Totals per task, from the token ledger.
  TASK           One task's ledger lines, for example T1.4.
  SESSION        Read that session's transcripts now and print its token use.
  --record       With a SESSION: also add its lines to the ledger
                 (.afk/log/tokens.jsonl in the main checkout). A session that
                 is already in the ledger is not added twice.
  --project PATH The project to work in. Defaults to the current directory.

Weighted cost is input + 5 x output + 1.25 x cache write + 0.1 x cache read.
It is a rough proxy for use against the plan's limits, not a bill.

Exit codes: 0 done, 1 nothing to show or the session could not be read,
2 the command was written wrongly.
`)
}

// tokensForm says which of the command's three readings an argument list asks
// for.
type tokensForm int

const (
	tokensFormTotals  tokensForm = iota // no argument: ledger totals per task
	tokensFormTask                      // a task id: that task's ledger lines
	tokensFormSession                   // a session name: read transcripts now
)

var tokensTaskArgRe = regexp.MustCompile(`^[Tt]\d+\.\d+[a-z]?$`)

// chooseTokensForm decides the form from the positional words and --record.
//
// It is pure so every branch is a table row. The task shape is tried before
// the session shape because a task id can never be a valid session name (a
// dot is not allowed in one), so the two cannot collide; anything that is
// neither is handed on as a session name and rejected by ValidateName with
// the message that explains session names.
func chooseTokensForm(words []string, record bool) (tokensForm, string, error) {
	switch {
	case len(words) > 1:
		return 0, "", fmt.Errorf("expected at most one session or task, but got %d words.", len(words))
	case len(words) == 0:
		if record {
			return 0, "", errors.New("--record needs a session name, for example: afk tokens --record my-session.")
		}
		return tokensFormTotals, "", nil
	case tokensTaskArgRe.MatchString(words[0]):
		if record {
			return 0, "", errors.New("--record works on a session, not a task id. Give the session name instead.")
		}
		return tokensFormTask, words[0], nil
	default:
		return tokensFormSession, words[0], nil
	}
}

// tokensSession is what the command needs to know about one session. Provider
// is the canonical name ("" for the default agent CLI).
type tokensSession struct {
	Worktree     string
	MainCheckout string
	SessionID    string
	Provider     string
}

// tokensEnv is the command's seam to git and the filesystem, so tests never
// touch a real repository or session.
type tokensEnv struct {
	// Session finds a session by name; found is false when there is none.
	Session func(project, name string) (s tokensSession, found bool, err error)
	// MainCheckout is where the ledger lives for the ledger-only forms.
	MainCheckout func(project string) (string, error)
}

// realTokensEnv finds sessions with discover.Discover and the main checkout
// from the first record of `git worktree list`, which git documents as the
// main worktree. The ledger belongs to the main checkout so that a session's
// lines are found from any worktree of the project.
func realTokensEnv() tokensEnv {
	run := discover.DefaultDeps().Run
	mainOf := func(project string) (string, error) {
		wts, err := worktree.List(run, project)
		if err != nil {
			return "", err
		}
		for _, w := range wts {
			if w.Primary {
				return w.Path, nil
			}
		}
		return "", errors.New("git lists no main checkout")
	}
	return tokensEnv{
		MainCheckout: mainOf,
		Session: func(project, name string) (tokensSession, bool, error) {
			sessions, err := discover.Discover(project, discover.DefaultDeps())
			if err != nil {
				return tokensSession{}, false, err
			}
			for _, s := range sessions {
				if s.Name != name || s.Kind.WorktreeFault() {
					continue
				}
				main, err := mainOf(project)
				if err != nil {
					return tokensSession{}, false, err
				}
				return tokensSession{
					Worktree:     s.WorktreePath,
					MainCheckout: main,
					SessionID:    s.SessionID,
					Provider:     s.Provider,
				}, true, nil
			}
			return tokensSession{}, false, nil
		},
	}
}

// cmdTokens implements `afk tokens`.
func cmdTokens(args []string) {
	// A failed Getwd or UserHomeDir is passed on as "": resolveProject only
	// needs them when --project is absent or starts with ~, and reports the
	// case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	os.Exit(runTokens(args, os.Stdout, os.Stderr, cwd, home, time.Now, realTokensEnv()))
}

// runTokens implements `afk tokens` and returns the exit code. cwd, home, the
// clock and env are parameters so the whole command is testable against a
// temp directory and a fixture home without a real session.
//
// Flags are accepted before and after the argument, parsed in a loop as
// runPlanCheck does, because the flag package stops at the first non-flag.
func runTokens(args []string, stdout, stderr io.Writer, cwd, home string, now func() time.Time, env tokensEnv) int {
	fs := flag.NewFlagSet("tokens", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := addProjectFlag(fs)
	record := fs.Bool("record", false, "Add the session's lines to the ledger")
	fs.Usage = func() { printTokensUsage(stderr) }

	var words []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}

	usage := func(msg string) int {
		fmt.Fprintf(stderr, "afk tokens: %s\n%s\n", msg, tokensUsageLine)
		return 2
	}

	form, arg, err := chooseTokensForm(words, *record)
	if err != nil {
		return usage(err.Error())
	}
	if form == tokensFormSession {
		if err := session.ValidateName(arg); err != nil {
			return usage(fmt.Sprintf("%q is not a session name or a task id. Session names use lowercase letters, digits and hyphens; task ids look like T1.4.", arg))
		}
	}

	project, err := resolveProject(*proj, cwd, home)
	if err != nil {
		return usage("cannot tell which project to use. Pass --project with the folder that holds .afk/.")
	}

	switch form {
	case tokensFormSession:
		return runTokensSession(arg, *record, project, stdout, stderr, home, now, env)
	default:
		return runTokensLedger(form, arg, project, stdout, stderr, env)
	}
}

// runTokensLedger prints the totals per task or one task's lines from the
// ledger.
func runTokensLedger(form tokensForm, task, project string, stdout, stderr io.Writer, env tokensEnv) int {
	main, err := env.MainCheckout(project)
	if err != nil {
		fmt.Fprintf(stderr, "afk tokens: cannot find the project's main checkout: %v\nCheck that %s is a git repository, or pass --project.\n", err, project)
		return 1
	}
	if form == tokensFormTask {
		res := taskTokensForSurface(main, task)
		switch res.State {
		case tokensUnreadable:
			fmt.Fprintf(stderr, "afk tokens: %s\n", res.Err)
			return 1
		case tokensNone:
			fmt.Fprintf(stderr, "afk tokens: the ledger at %s has nothing for %s.\nTo add a session's token use to it, run: afk tokens --record SESSION\n", res.Path, task)
			return 1
		}
		for _, l := range res.Lines {
			fmt.Fprintln(stdout, l)
		}
		return 0
	}
	path := tokens.LedgerPath(main)
	lines, err := tokens.Read(path)
	if err != nil {
		fmt.Fprintf(stderr, "afk tokens: %v\n", err)
		return 1
	}
	if len(lines) == 0 {
		fmt.Fprintf(stderr, "afk tokens: the ledger at %s is empty or does not exist yet.\nTo add a session's token use to it, run: afk tokens --record SESSION\n", path)
		return 1
	}
	if err := tokens.RenderTasks(stdout, lines); err != nil {
		fmt.Fprintf(stderr, "afk tokens: could not print the table: %v\n", err)
		return 1
	}
	return 0
}

// runTokensSession reads one session's transcripts, prints its table and, with
// record, appends its lines to the ledger once.
func runTokensSession(name string, record bool, project string, stdout, stderr io.Writer, home string, now func() time.Time, env tokensEnv) int {
	sess, found, err := env.Session(project, name)
	if err != nil {
		fmt.Fprintf(stderr, "afk tokens: could not list the sessions of %s: %v\n", project, err)
		return 1
	}
	if !found {
		fmt.Fprintf(stderr, "afk tokens: no session named %q found in %s.\nRun `afk clean` to list sessions. A session that was already cleaned can only be read from the ledger, by its task id, for example: afk tokens T1.4\n", name, project)
		return 1
	}
	if sess.SessionID == "" {
		fmt.Fprintf(stderr, "afk tokens: session %q has no session id yet, so it has not started.\nRun this again once the session is running.\n", name)
		return 1
	}
	p, err := provider.Lookup(sess.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "afk tokens: %v\n", err)
		return 1
	}
	task := readTaskID(filepath.Join(sess.Worktree, ".afk", name, "description.md"))
	var lines []tokens.Line
	var records []provider.TokenRecord
	var calls int
	var res tokens.RecordResult
	var err2 error
	if record {
		res, err2 = tokens.Record(tokens.RecordInput{Provider: p, Home: home, MainCheckout: sess.MainCheckout, Task: task, Session: name, SessionID: sess.SessionID, Now: now()})
		lines, calls, records = res.Lines, res.Calls, res.Records
	} else {
		records, err2 = p.SessionTokens(provider.TokenQuery{Home: home, SessionID: sess.SessionID})
		if err2 == nil && len(records) == 0 {
			err2 = tokens.ErrNoCalls
		}
		if err2 == nil {
			lines, calls = tokens.Aggregate(records, task, name, now()), len(records)
		}
	}
	path := tokens.LedgerPath(sess.MainCheckout)
	var appendErr *tokens.AppendError
	var readErr *tokens.LedgerReadError
	switch {
	case errors.Is(err2, provider.ErrTokensUnsupported):
		fmt.Fprintf(stderr, "afk tokens: token use cannot be read for this session's agent CLI yet.\n")
		return 1
	case errors.Is(err2, provider.ErrNoTranscript):
		fmt.Fprintf(stderr, "afk tokens: no transcript found for session %q (session id %s).\nThe transcripts may be on another machine or have been deleted. Details: %v\n", name, sess.SessionID, err2)
		return 1
	case errors.Is(err2, tokens.ErrNoCalls):
		fmt.Fprintf(stderr, "afk tokens: session %q has no model calls in its transcript yet.\nRun this again once the session has done some work.\n", name)
		return 1
	case errors.As(err2, &appendErr):
		// The table is not printed: Record found the ledger unwritable
		// before anything could be shown, and asking the provider again
		// to rebuild it would read the transcripts twice.
		fmt.Fprintf(stderr, "afk tokens: could not write the ledger at %s: %v\nNothing was recorded. Fix the problem above and run the command again.\n", appendErr.Path, appendErr.Err)
		return 1
	case errors.As(err2, &readErr):
		fmt.Fprintf(stderr, "afk tokens: %v\nNothing was recorded.\n", err2)
		return 1
	case err2 != nil:
		fmt.Fprintf(stderr, "afk tokens: could not read the transcripts of session %q: %v\n", name, err2)
		return 1
	}

	taskLabel := task
	if taskLabel == "" {
		taskLabel = "no task"
	}
	fmt.Fprintf(stdout, "Session %s, task %s, %d calls\n", name, taskLabel, calls)
	if err := tokens.RenderTable(stdout, lines); err != nil {
		fmt.Fprintf(stderr, "afk tokens: could not print the table: %v\n", err)
		return 1
	}
	// The start-up section comes from the First marks on the records, so it
	// exists only for a session read from its transcripts; the ledger keeps
	// no such field and the ledger forms never print it.
	var total float64
	for _, l := range lines {
		total += l.Weighted
	}
	if rows := tokens.StartUp(records); len(rows) > 0 {
		fmt.Fprintln(stdout)
		if err := tokens.RenderStartUp(stdout, rows, total); err != nil {
			fmt.Fprintf(stderr, "afk tokens: could not print the start-up section: %v\n", err)
			return 1
		}
	}
	if !record {
		return 0
	}
	if res.AlreadyRecorded {
		fmt.Fprintf(stdout, "already recorded: the ledger at %s has lines for session %s, so nothing was added.\n", path, name)
		return 0
	}
	fmt.Fprintf(stdout, "Recorded %d lines for session %s in %s\n", len(lines), name, path)
	return 0
}

// readTaskID returns the task id in a description file, or "" when the file is
// missing, unreadable or names none. A session without a description is
// normal (a one-off session has none), so none of those is an error.
func readTaskID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf, _ := io.ReadAll(io.LimitReader(f, descriptionReadCap))
	return tokens.TaskID(string(buf))
}
