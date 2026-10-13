package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/dphaener/afk/internal/profile"
)

// Usage lines are constants so the error messages repeat exactly what the
// help says.
const (
	profileListUsage  = "afk profile [--kind KIND]"
	profileInitUsage  = "afk profile init"
	profileSetUsage   = "afk profile set <topic> <value> [--kind KIND] [--rule|--default] [--reason TEXT]"
	profileCheckUsage = "afk profile check"
)

// profileEnv is everything the profile command touches outside its own
// arguments, so tests run against temp folders and never the real config.
type profileEnv struct {
	profile.Deps
	stat   func(string) (fs.FileInfo, error)
	write  func(path string, data []byte) error
	create func(path string, data []byte) error
}

func realProfileEnv() profileEnv {
	return profileEnv{
		Deps:   profile.DefaultDeps(),
		stat:   os.Stat,
		write:  profile.Write,
		create: profile.Create,
	}
}

// cmdProfile implements `afk profile`.
func cmdProfile(args []string) {
	os.Exit(runProfile(args, os.Stdout, os.Stderr, realProfileEnv()))
}

func printProfileUsage(w io.Writer) {
	fmt.Fprintf(w, `afk profile — how you like to build and host things

Usage:
  %s
  %s
  %s
  %s

With no word, lists what the profile says (--kind narrows it to one kind of
project, such as web-app or cli). init writes a commented template if there is
no profile yet and never overwrites one. set adds or changes one entry and
keeps every other line of the file, comments included; a new entry is a
default unless you pass --rule. check lists what is wrong with the file.

The profile is profile.toml beside afk's config.toml. Keys and tokens are
refused: afk reads secrets from the environment, never from this file.

Exit codes: 0 done, 1 the profile has a problem or could not be read or
written, 2 the command was written wrongly.
`, profileListUsage, profileInitUsage, profileSetUsage, profileCheckUsage)
}

func runProfile(args []string, stdout, stderr io.Writer, env profileEnv) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printProfileUsage(stdout)
		return 0
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runProfileList(args, stdout, stderr, env)
	}
	switch args[0] {
	case "init":
		return runProfileInit(args[1:], stdout, stderr, env)
	case "set":
		return runProfileSet(args[1:], stdout, stderr, env)
	case "check":
		return runProfileCheck(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "afk profile: unknown subcommand %q\n", args[0])
		printProfileUsage(stderr)
		return 2
	}
}

// noProfileHint says there is no file yet. Used by the list and by check.
func noProfileHint(w io.Writer, path string) {
	fmt.Fprintf(w, "There is no profile yet. It would be at %s.\nRun `%s` to write a template you can fill in.\n", path, profileInitUsage)
}

func runProfileList(args []string, stdout, stderr io.Writer, env profileEnv) int {
	flags := flag.NewFlagSet("profile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kind := flags.String("kind", "", "Show the entries for this kind of project")
	flags.Usage = func() { printProfileUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "afk profile: unexpected word %q.\nUsage: %s\n", flags.Arg(0), profileListUsage)
		return 2
	}
	if *kind != "" && !profile.ValidKind(*kind) {
		fmt.Fprintf(stderr, "afk profile: %q is not a kind. Use lower case letters, digits and \"-\", such as web-app.\n", *kind)
		return 2
	}

	p, path, found, _, err := profile.Load(env.Deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk profile: %v\n", err)
		return 1
	}
	if !found {
		noProfileHint(stdout, path)
		return 0
	}
	var errCount int
	for _, pr := range profile.Validate(p, nil) {
		if pr.Level == "error" {
			errCount++
			fmt.Fprintf(stderr, "%s: %s\n", path, pr.Message)
		}
	}
	if errCount > 0 {
		fmt.Fprintf(stderr, "Fix the file, then run `%s` to see everything that is wrong with it.\n", profileCheckUsage)
		return 1
	}

	fmt.Fprintf(stdout, "Profile: %s\n", path)
	if len(p.RuleFiles) > 0 {
		fmt.Fprintln(stdout, "Rule files:")
		for _, f := range p.RuleFiles {
			fmt.Fprintf(stdout, "  %s\n", f)
		}
	}
	entries := p.Entries
	if *kind != "" {
		entries = profile.Select(p, *kind)
		specific := false
		for _, e := range entries {
			if e.Kind == *kind {
				specific = true
			}
		}
		if !specific {
			fmt.Fprintf(stderr, "No entry is specific to %s, so the general entries apply.\n", *kind)
		}
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "The profile has no entries yet. Add one with:\n  %s\n", profileSetUsage)
		return 0
	}
	for _, e := range entries {
		line := fmt.Sprintf("%s  %s: %s", e.Strength, e.Topic, e.Value)
		if e.Kind != "" {
			line += " [" + e.Kind + "]"
		}
		if e.Reason != "" {
			line += " — " + e.Reason
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

func runProfileInit(args []string, stdout, stderr io.Writer, env profileEnv) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "afk profile init: unexpected word %q.\nUsage: %s\n", args[0], profileInitUsage)
		return 2
	}
	path := profile.Path(env.Deps)
	if path == "" {
		fmt.Fprintln(stderr, "afk profile init: cannot find your home folder, so there is nowhere to keep profile.toml. Set HOME or XDG_CONFIG_HOME.")
		return 1
	}
	if err := env.create(path, []byte(profile.Template())); err != nil {
		if errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(stderr, "afk profile init: %s already exists. It was not changed.\nRun `afk profile set` or open the file in an editor to change it.\n", path)
			return 1
		}
		fmt.Fprintf(stderr, "afk profile init: could not write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote a template profile to %s.\nOpen it, uncomment the entries that suit you, or add one with:\n  %s\n", path, profileSetUsage)
	return 0
}

func runProfileSet(args []string, stdout, stderr io.Writer, env profileEnv) int {
	flags := flag.NewFlagSet("profile set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kind := flags.String("kind", "", "Narrow the entry to this kind of project")
	rule := flags.Bool("rule", false, "Make the entry a rule")
	dflt := flags.Bool("default", false, "Make the entry a default")
	reason := flags.String("reason", "", "Why you prefer it")
	flags.Usage = func() { printProfileUsage(stderr) }

	// Flags may come before, between and after the two words; Go's flag
	// package stops at the first non-flag, so parse in a loop (as
	// runPlanCheck does).
	var words []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = flags.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
	reasonGiven := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "reason" {
			reasonGiven = true
		}
	})

	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "afk profile set: "+format+"\n", a...)
		fmt.Fprintf(stderr, "Usage: %s\n", profileSetUsage)
		return 2
	}
	if len(words) != 2 {
		return usage("expected a topic and a value, but got %d word(s).", len(words))
	}
	topic, value := words[0], words[1]
	if strings.TrimSpace(topic) == "" || strings.TrimSpace(value) == "" {
		return usage("the topic and the value must not be empty.")
	}
	if *rule && *dflt {
		return usage("--rule and --default cannot be used together.")
	}
	if *kind != "" && !profile.ValidKind(*kind) {
		return usage("%q is not a kind. Use lower case letters, digits and \"-\", such as web-app.", *kind)
	}

	// The owner sees this check's wording; Set repeats it as a backstop.
	// Neither prints the text that failed.
	for _, f := range []struct{ name, val string }{{"topic", topic}, {"value", value}, {"reason", *reason}} {
		if why, bad := profile.LooksSecret(f.val); bad {
			fmt.Fprintf(stderr, "afk profile set: the %s looks like a key or token (%s). Nothing was saved.\nKeep keys and tokens out of the profile: afk reads secrets from the environment, never from this file.\n", f.name, why)
			return 1
		}
	}

	path := profile.Path(env.Deps)
	if path == "" {
		fmt.Fprintln(stderr, "afk profile set: cannot find your home folder, so there is nowhere to keep profile.toml. Set HOME or XDG_CONFIG_HOME. Nothing was saved.")
		return 1
	}
	read := env.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	var text string
	var p profile.Profile
	data, err := read(path)
	switch {
	case err == nil:
		text = string(data)
		p, _, err = profile.Parse(data)
		if err != nil {
			fmt.Fprintf(stderr, "afk profile set: %s: %v\nNothing was saved.\n", path, err)
			return 1
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		fmt.Fprintf(stderr, "afk profile set: %s: cannot read the file: %v\nNothing was saved.\n", path, err)
		return 1
	}

	entry := profile.Entry{Topic: topic, Value: value, Kind: *kind, Strength: profile.StrengthDefault, Reason: *reason}
	if i, ok := profile.Find(p, topic, *kind); ok {
		entry.Strength = p.Entries[i].Strength
		if !reasonGiven {
			entry.Reason = p.Entries[i].Reason
		}
	}
	if *rule {
		entry.Strength = profile.StrengthRule
	}
	if *dflt {
		entry.Strength = profile.StrengthDefault
	}

	newText, replaced, err := profile.Set(text, entry)
	if err != nil {
		fmt.Fprintf(stderr, "afk profile set: %s: %v\nNothing was saved.\n", path, err)
		return 1
	}
	if err := env.write(path, []byte(newText)); err != nil {
		fmt.Fprintf(stderr, "afk profile set: could not write %s: %v\nThe file was not changed.\n", path, err)
		return 1
	}
	verb := "Set"
	if replaced {
		verb = "Changed"
	}
	scope := ""
	if entry.Kind != "" {
		scope = " [" + entry.Kind + "]"
	}
	fmt.Fprintf(stdout, "%s %s%s as a %s in %s\n", verb, entry.Topic, scope, entry.Strength, path)
	return 0
}

func runProfileCheck(args []string, stdout, stderr io.Writer, env profileEnv) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "afk profile check: unexpected word %q.\nUsage: %s\n", args[0], profileCheckUsage)
		return 2
	}
	p, path, found, unknown, err := profile.Load(env.Deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk profile check: %v\n", err)
		return 1
	}
	if !found {
		noProfileHint(stdout, path)
		return 0
	}
	var errCount, warnCount int
	count := func(level string) {
		if level == "error" {
			errCount++
		} else {
			warnCount++
		}
	}
	// Entry problems already name the entry ("Entry 2 (...)") in their
	// message, so the line is path, level, message and rule id.
	for _, pr := range profile.Validate(p, unknown) {
		count(pr.Level)
		fmt.Fprintf(stdout, "%s: %s: %s [%s]\n", path, pr.Level, pr.Message, pr.Rule)
	}
	home, _ := env.HomeDir()
	for _, f := range p.RuleFiles {
		if !strings.HasPrefix(f, "~/") && !strings.HasPrefix(f, "/") {
			continue // already reported as bad-rule-file
		}
		if strings.HasPrefix(f, "~/") && home == "" {
			continue
		}
		// stat only: the contents of a rule file are never read here.
		if _, err := env.stat(profile.ExpandHome(f, home)); err != nil && errors.Is(err, fs.ErrNotExist) {
			count("warning")
			fmt.Fprintf(stdout, "%s: warning: the rule file %s does not exist. Fix the path or remove it from rule_files. [missing-rule-file]\n", path, f)
		}
	}
	if errCount == 0 && warnCount == 0 {
		fmt.Fprintf(stdout, "The profile is fine: %d entries, %d rule files.\n", len(p.Entries), len(p.RuleFiles))
		return 0
	}
	fmt.Fprintf(stdout, "%d errors, %d warnings\n", errCount, warnCount)
	if errCount > 0 {
		return 1
	}
	return 0
}
