package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/session"
)

// inboxNotifyTimeout bounds the one push notification `afk inbox add` sends.
// The command is typed by agents running unattended, so a server that never
// answers must cost seconds, not the session.
const inboxNotifyTimeout = 10 * time.Second

// inboxEnv is everything `afk inbox` takes from the outside world. cmdInbox
// fills it with the real things; tests fill it with a temp folder, a fixed
// clock, scripted ids and fakes, so the whole command, including "which folder
// is the project" and "was a notification sent", runs without git, a network
// or the real clock.
type inboxEnv struct {
	stdout, stderr io.Writer
	cwd, home      string
	now            func() time.Time
	newID          func() string
	// gitCommonDir is `git rev-parse --path-format=absolute --git-common-dir`
	// run in dir, or "" outside a git repo.
	gitCommonDir func(dir string) string
	// repoRoot is the top folder of the worktree dir is in, or "".
	repoRoot   func(dir string) string
	glob       func(pattern string) ([]string, error)
	loadConfig func(root string) (projectconfig.Config, error)
	send       func(ctx context.Context, url string, m notify.NtfyMessage) error
}

// cmdInbox implements `afk inbox`.
func cmdInbox(args []string) {
	// A failed Getwd or UserHomeDir is passed on as "": inboxProject reports
	// the one case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	os.Exit(runInbox(args, inboxEnv{
		stdout:       os.Stdout,
		stderr:       os.Stderr,
		cwd:          cwd,
		home:         home,
		now:          time.Now,
		gitCommonDir: resolveGitCommonDirIn,
		repoRoot:     resolveCwdRepoRoot,
		glob:         filepath.Glob,
		loadConfig:   projectconfig.Load,
		send: func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url}.Send(ctx, m)
		},
	}))
}

func printInboxUsage(w io.Writer) {
	fmt.Fprint(w, `afk inbox — things that need the project's owner

Usage:
  afk inbox                          List the items: ! stops a task, · is open,
                                     ✓ is answered. Items that stop a task come first.
  afk inbox show <id>                Show one item in full.
  afk inbox answer <id> <text>       Answer an item. On an item with options, a
                                     number picks that option. The first answer is kept.
  afk inbox add --kind KIND --title "one line" --block|--no-block
                [--body TEXT] [--task T1.2] [--session NAME]
                [--option TEXT]... [--recommended TEXT] [--assumed TEXT]
                                     Add an item and print its id. Mostly run by the agent.

Every form takes --project DIR to name the project folder.

Kinds:
  question         a choice only the owner can make
  env_var          a credential or variable the owner must set
  external_action  something to do outside the code: a dashboard, a device, a payment
  verification     something to check by hand
  failure          something went wrong and was not fixed
  notice           something to know; nothing to do

Every added item says whether it stops the task: --block (the task waits for
the owner) or --no-block (the task carries on). Give exactly one of them.
--option and --recommended only go with --kind question; --assumed (the answer
the agent went ahead with) only goes with --no-block.

Items live in .afk/inbox/ in the project's main folder, even when the command
runs inside a session's worktree. They are written by this command, not by hand.
When afk.toml in that folder has [notify] ntfy_url, adding an item that needs
the owner also sends a push notification there.

Exit codes: 0 done, 1 the item was not found, was already answered, or a file
could not be read or written, 2 the command was written wrongly.
`)
}

// runInbox implements `afk inbox` and returns the exit code: 0 for success, 1
// when the item was not found, already answered, or a file could not be read
// or written, 2 for a command line that could never work.
//
// The first word picks the subcommand and no word at all is the list. Only
// --project may come before that word, because the other flags belong to
// `add` and mean nothing until the subcommand is known; after the word, each
// subcommand accepts its flags in any position (see parseInboxArgs).
func runInbox(args []string, env inboxEnv) int {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	proj := addProjectFlag(fs)
	fs.Usage = func() { printInboxUsage(env.stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return runInboxList(*proj, env)
	}
	switch rest[0] {
	case "show":
		return runInboxShow(rest[1:], *proj, env)
	case "answer":
		return runInboxAnswer(rest[1:], *proj, env)
	case "add":
		return runInboxAdd(rest[1:], *proj, env)
	case "help":
		printInboxUsage(env.stderr)
		return 0
	default:
		fmt.Fprintf(env.stderr, "afk inbox: unknown subcommand %q\n", rest[0])
		printInboxUsage(env.stderr)
		return 2
	}
}

// parseInboxArgs parses flags that may come before, between or after the
// positional words and returns the words. Go's flag package stops at the
// first non-flag argument, which would turn `afk inbox answer abc234 yes
// --project ~/app` into an answer that ends in "--project ~/app". The loop is
// the one `afk plan check` uses, for the same reason: these commands are
// typed from prompts and from memory, in whatever order comes naturally.
//
// done is true when the caller should return exit at once: 0 after --help
// (the flag package has already printed the usage), 2 after a bad flag.
func parseInboxArgs(fs *flag.FlagSet, args []string) (words []string, exit int, done bool) {
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, true
			}
			return nil, 2, true
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return words, 0, false
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
}

// inboxFlagSet builds a subcommand's flag set. projDefault is a --project
// given before the subcommand word (`afk inbox --project DIR show abc234`);
// one given after the word replaces it.
func inboxFlagSet(name, projDefault string, env inboxEnv) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("inbox "+name, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	proj := addProjectFlag(fs)
	*proj = projDefault
	fs.Usage = func() { printInboxUsage(env.stderr) }
	return fs, proj
}

// inboxProject returns the folder whose .afk/inbox/ the command uses.
//
// With --project the folder is used as given: the caller named it, and
// second-guessing a named folder would make the flag useless for the one
// thing it is for. Without it the answer is the project's MAIN checkout, not
// the working folder: agents type `afk inbox add` inside a session's linked
// worktree, and an item written into that worktree's own .afk/ would be
// invisible to the owner and deleted with the worktree.
func inboxProject(flagValue string, env inboxEnv) (string, error) {
	if flagValue != "" {
		return resolveProject(flagValue, env.cwd, env.home)
	}
	if env.cwd == "" {
		return "", errors.New("no --project and no working directory")
	}
	return mainCheckout(env.cwd, env.gitCommonDir(env.cwd)), nil
}

// inboxStore resolves the project and returns it with its store. A false ok
// means the message has been printed and the caller returns 2.
func inboxStore(sub, flagValue string, env inboxEnv) (project string, store inbox.Store, ok bool) {
	project, err := inboxProject(flagValue, env)
	if err != nil {
		fmt.Fprintf(env.stderr, "afk inbox%s: cannot tell which project to use. Pass --project with the project's main folder.\n", sub)
		return "", inbox.Store{}, false
	}
	return project, inbox.Store{Dir: inbox.Dir(project), Now: env.now, NewID: env.newID}, true
}

func runInboxList(projFlag string, env inboxEnv) int {
	_, store, ok := inboxStore("", projFlag, env)
	if !ok {
		return 2
	}
	items, unreadable, err := store.List()
	if err != nil {
		fmt.Fprintf(env.stderr, "afk inbox: could not read the inbox: %v\n", err)
		return 1
	}
	if len(items) == 0 && len(unreadable) == 0 {
		fmt.Fprintln(env.stdout, "The inbox is empty.")
		return 0
	}

	// Column widths come from the items shown, so a list with no task ids
	// has no empty task column.
	kindW, taskW := 0, 0
	for _, it := range items {
		kindW = max(kindW, len(it.Kind))
		taskW = max(taskW, len(it.Task))
	}
	for _, it := range items {
		marker := "·"
		switch {
		case !it.Open():
			marker = "✓"
		case it.Blocking:
			marker = "!"
		}
		line := fmt.Sprintf("%s %s  %-*s  ", marker, it.ID, kindW, it.Kind)
		if taskW > 0 {
			line += fmt.Sprintf("%-*s  ", taskW, it.Task)
		}
		fmt.Fprintln(env.stdout, line+it.Title)
	}
	switch len(unreadable) {
	case 0:
	case 1:
		fmt.Fprintf(env.stdout, "1 inbox file could not be read: %s\n", unreadable[0])
	default:
		fmt.Fprintf(env.stdout, "%d inbox files could not be read: %s\n", len(unreadable), strings.Join(unreadable, ", "))
	}
	return 0
}

func runInboxShow(args []string, projDefault string, env inboxEnv) int {
	fs, proj := inboxFlagSet("show", projDefault, env)
	words, exit, done := parseInboxArgs(fs, args)
	if done {
		return exit
	}
	if len(words) != 1 {
		fmt.Fprintf(env.stderr, "afk inbox show: expected one item id, but got %d word(s).\n", len(words))
		fmt.Fprintln(env.stderr, "Usage: afk inbox show [--project DIR] <id>")
		return 2
	}
	id := words[0]
	_, store, ok := inboxStore(" show", *proj, env)
	if !ok {
		return 2
	}
	it, err := store.Get(id)
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		fmt.Fprintf(env.stderr, "afk inbox show: there is no inbox item %s. Run `afk inbox` to see the ids.\n", id)
		return 1
	case err != nil:
		fmt.Fprintf(env.stderr, "afk inbox show: %v\n", err)
		return 1
	}
	writeInboxItem(env.stdout, it)
	return 0
}

// writeInboxItem prints one item as `label: value` lines, leaving out what is
// not set, and ends an open item with the command that answers it.
func writeInboxItem(w io.Writer, it inbox.Item) {
	field := func(label, value string) {
		if value == "" {
			return
		}
		if strings.Contains(value, "\n") {
			fmt.Fprintf(w, "%s:\n  %s\n", label, strings.ReplaceAll(value, "\n", "\n  "))
			return
		}
		fmt.Fprintf(w, "%s: %s\n", label, value)
	}
	field("id", it.ID)
	field("kind", it.Kind)
	if it.Blocking {
		field("blocking", "yes (the task is waiting for this)")
	} else {
		field("blocking", "no (the task carried on)")
	}
	field("task", it.Task)
	field("session", it.Session)
	field("created", it.CreatedAt)
	field("title", it.Title)
	field("body", it.Body)
	if len(it.Options) > 0 {
		fmt.Fprintln(w, "options:")
		for i, o := range it.Options {
			mark := ""
			if o == it.Recommended {
				mark = " (recommended)"
			}
			fmt.Fprintf(w, "  %d. %s%s\n", i+1, o, mark)
		}
	}
	field("assumed", it.Assumed)
	field("answer", it.Answer)
	field("answered at", it.AnsweredAt)
	field("answered via", it.AnsweredVia)
	if it.Open() {
		what := "<text>"
		if len(it.Options) > 0 {
			what = "<number or text>"
		}
		fmt.Fprintf(w, "\nTo answer: afk inbox answer %s %s\n", it.ID, what)
	}
}

func runInboxAnswer(args []string, projDefault string, env inboxEnv) int {
	fs, proj := inboxFlagSet("answer", projDefault, env)
	words, exit, done := parseInboxArgs(fs, args)
	if done {
		return exit
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "afk inbox answer: "+format+"\n", a...)
		fmt.Fprintln(env.stderr, "Usage: afk inbox answer [--project DIR] <id> <text or option number>")
		return 2
	}
	if len(words) == 0 {
		return usage("say which item to answer and what the answer is.")
	}
	id := words[0]
	if len(words) == 1 {
		return usage("the answer is missing. Write it after the id.")
	}
	text := strings.Join(words[1:], " ")

	_, store, ok := inboxStore(" answer", *proj, env)
	if !ok {
		return 2
	}
	it, err := store.Answer(id, text, "cli")
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		fmt.Fprintf(env.stderr, "afk inbox answer: there is no inbox item %s. Run `afk inbox` to see the ids.\n", id)
		return 1
	case errors.Is(err, inbox.ErrAlreadyAnswered):
		fmt.Fprintf(env.stderr, "afk inbox answer: %s was already answered: %s\nThe first answer is kept and nothing changed.\n", id, it.Answer)
		return 1
	case errors.Is(err, inbox.ErrEmptyAnswer):
		return usage("the answer is empty. Write it after the id.")
	case err != nil:
		fmt.Fprintf(env.stderr, "afk inbox answer: could not save the answer: %v\nNothing changed.\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Answered %s: %s\n", it.ID, it.Answer)
	return 0
}

// inboxOptions collects a repeatable --option flag, in the order given.
type inboxOptions []string

func (o *inboxOptions) String() string { return strings.Join(*o, ", ") }

func (o *inboxOptions) Set(v string) error {
	*o = append(*o, v)
	return nil
}

func runInboxAdd(args []string, projDefault string, env inboxEnv) int {
	fs, proj := inboxFlagSet("add", projDefault, env)
	task := fs.String("task", "", "Master-plan task id, like T4.3")
	kind := fs.String("kind", "", "question, env_var, external_action, verification, failure or notice")
	title := fs.String("title", "", "One line saying what the owner has to do or know")
	body := fs.String("body", "", "The detail")
	var options inboxOptions
	fs.Var(&options, "option", "One choice for a question (repeat for each)")
	recommended := fs.String("recommended", "", "The option the agent recommends")
	assumed := fs.String("assumed", "", "The answer the agent went ahead with")
	block := fs.Bool("block", false, "The task waits for the owner")
	noBlock := fs.Bool("no-block", false, "The task carries on")
	sessionFlag := fs.String("session", "", "The session the item came from")

	words, exit, done := parseInboxArgs(fs, args)
	if done {
		return exit
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "afk inbox add: "+format+"\n", a...)
		fmt.Fprintln(env.stderr, "Nothing was added. Run `afk inbox --help` to see how the command is written.")
		return 2
	}
	if len(words) > 0 {
		return usage("%q is not part of this command. Everything is given with a flag; put a title or body with spaces in quotes.", words[0])
	}
	// A forgotten flag must never silently park a task, or silently fail to:
	// there is no safe default, so the caller has to say.
	if *block == *noBlock {
		return usage("say whether this item stops the task: add --block (the task waits for the owner) or --no-block (the task carries on).")
	}
	if *kind == "" {
		return usage("the item needs a kind. Add --kind with one of question, env_var, external_action, verification, failure or notice.")
	}
	if strings.TrimSpace(*title) == "" {
		return usage("the item needs a title. Add --title \"one line saying what the owner has to do or know\".")
	}

	project, store, ok := inboxStore(" add", *proj, env)
	if !ok {
		return 2
	}

	sessionName := *sessionFlag
	if sessionName == "" {
		sessionName = inboxInferSession(env)
	}

	item, err := store.Add(inbox.NewItem{
		Task:        *task,
		Session:     sessionName,
		Kind:        *kind,
		Blocking:    *block,
		Title:       *title,
		Body:        *body,
		Options:     options,
		Recommended: *recommended,
		Assumed:     *assumed,
	})
	var ve *inbox.ValidationError
	switch {
	case errors.As(err, &ve):
		return usage("%s", ve.Message)
	case err != nil:
		fmt.Fprintf(env.stderr, "afk inbox add: could not write the item: %v\nNothing was added. Fix the problem above and run the command again.\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Added inbox item %s.\n", item.ID)

	// From here on the item is on disk and the exit code is 0 whatever
	// happens. The caller is usually an agent that is about to park on this
	// item: a non-zero exit would tell it the item was not recorded, and it
	// would add it again or carry on without it.
	cfg, err := env.loadConfig(project)
	if err != nil {
		fmt.Fprintf(env.stderr, "afk inbox add: the item was added, but afk.toml could not be read, so no notification was sent: %v\n", err)
		return 0
	}
	msg, ok := inboxNotification(item, cfg.Notify.NtfyURL)
	if !ok {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), inboxNotifyTimeout)
	defer cancel()
	if err := env.send(ctx, cfg.Notify.NtfyURL, msg); err != nil {
		fmt.Fprintf(env.stderr, "afk inbox add: the item was added, but the notification could not be sent: %v\n", err)
	}
	return 0
}

// inboxInferSession names the session the command is running in, or "".
//
// It looks in the top folder of the WORKTREE the command runs in, not in the
// project's main checkout where the item is written: a session's worktree
// holds that session's status.json and nobody else's, while the main checkout
// may hold several.
//
// A folder name that is not a session name is dropped rather than passed on:
// the store would refuse the whole item over a value the caller never typed.
func inboxInferSession(env inboxEnv) string {
	top := env.repoRoot(env.cwd)
	if top == "" {
		top = env.cwd
	}
	matches, err := env.glob(filepath.Join(top, ".afk", "*", "status.json"))
	if err != nil {
		return ""
	}
	name := inferSession(matches)
	if session.ValidateName(name) != nil {
		return ""
	}
	return name
}

// inboxNotification decides whether a newly added item sends a push
// notification, and builds it. It is pure so the whole "who gets bothered,
// and with what" table is testable without a server.
//
// No address configured is silence, not an error: most projects have no
// afk.toml. Which kinds notify, and how loudly, is notify.NtfyPriority's
// table. The message ends with the command that shows the item, because a
// notification is read on a phone and acted on at a terminal later.
func inboxNotification(item inbox.Item, ntfyURL string) (notify.NtfyMessage, bool) {
	if ntfyURL == "" {
		return notify.NtfyMessage{}, false
	}
	prio, ok := notify.NtfyPriority(item.Kind, item.Blocking)
	if !ok {
		return notify.NtfyMessage{}, false
	}
	body := item.Body
	if body != "" {
		body += "\n\n"
	}
	body += "afk inbox show " + item.ID
	if item.Task != "" {
		body += " (" + item.Task + ")"
	}
	return notify.NtfyMessage{Title: "afk: " + item.Title, Body: body, Priority: prio}, true
}
