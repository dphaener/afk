package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/profile"
)

// Usage lines are constants so the error messages repeat exactly what the
// help says.
const (
	projectListUsage   = "afk project list"
	projectAddUsage    = "afk project add [--name NAME] [DIR]"
	projectRemoveUsage = "afk project remove <name or DIR>"
)

// projectEnv is everything the project command touches outside its own
// arguments, so tests run against temp folders and never the real config.
type projectEnv struct {
	daemonconfig.Deps
	// write is the atomic writer (profile.Write: same-folder temp file, then
	// rename), so a crash never leaves half of config.toml behind.
	write        func(path string, data []byte) error
	mkdirAll     func(path string, perm fs.FileMode) error
	cwd          func() (string, error)
	gitCommonDir func(dir string) string
	isDir        func(path string) bool
}

func realProjectEnv() projectEnv {
	return projectEnv{
		Deps:         daemonconfig.DefaultDeps(),
		write:        profile.Write,
		mkdirAll:     os.MkdirAll,
		cwd:          os.Getwd,
		gitCommonDir: resolveGitCommonDirIn,
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
	}
}

// cmdProject implements `afk project`.
func cmdProject(args []string) {
	os.Exit(runProject(args, os.Stdout, os.Stderr, realProjectEnv()))
}

func printProjectUsage(w io.Writer) {
	fmt.Fprintf(w, `afk project — the projects the daemon builds

Usage:
  %s
  %s
  %s

list shows the projects in afk's config.toml. add puts a project there (the
current folder unless you give one; --name sets the name it is known by), and
remove takes one out by name or folder. remove never touches the project's own
files or a session that is running. A running afk daemon picks changes up
within a few seconds.

Exit codes: 0 done or nothing to do, 1 refused or the config could not be read
or written, 2 the command was written wrongly.
`, projectListUsage, projectAddUsage, projectRemoveUsage)
}

func runProject(args []string, stdout, stderr io.Writer, env projectEnv) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printProjectUsage(stdout)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "afk project: say what to do: list, add or remove.")
		printProjectUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return runProjectList(args[1:], stdout, stderr, env)
	case "add":
		return runProjectAdd(args[1:], stdout, stderr, env)
	case "remove":
		return runProjectRemove(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "afk project: unknown subcommand %q\n", args[0])
		printProjectUsage(stderr)
		return 2
	}
}

func runProjectList(args []string, stdout, stderr io.Writer, env projectEnv) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "afk project list: unexpected word %q.\nUsage: %s\n", args[0], projectListUsage)
		return 2
	}
	cfg, err := daemonconfig.Load(env.Deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk project list: %v\n", err)
		return 1
	}
	if len(cfg.Projects) == 0 {
		fmt.Fprintf(stdout, "No projects are listed yet. Add one with `%s`.\n", projectAddUsage)
		return 0
	}
	for _, p := range cfg.Projects {
		fmt.Fprintf(stdout, "%s  %s\n", p.Name, p.Path)
	}
	return 0
}

// readConfigText returns config.toml's text; a missing file is empty text.
func readConfigText(env projectEnv, path string) (string, error) {
	read := env.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: cannot read the file: %w", path, err)
	}
	return string(data), nil
}

// configIOError marks a failure to find, read, create or write the config
// file, as opposed to a refusal by daemonconfig.AddProject about the edit
// itself. It prints exactly as the wrapped error, so `afk plan approve` shows
// the same text as before; only addProjectForSurface tells the two apart.
type configIOError struct{ err error }

func (e configIOError) Error() string { return e.err.Error() }
func (e configIOError) Unwrap() error { return e.err }

// addProjectToConfig lists the project at root in config.toml. It is the one
// code path `afk project add` and `afk plan approve` share, so both make the
// same edit and report the same name. name may be empty (the folder name is
// used); the returned name is the one the project is known by, which for a
// project that was already listed is the listed one. added is false when the
// folder was already there and nothing was written.
func addProjectToConfig(env projectEnv, root, name string) (added bool, listed string, err error) {
	path := daemonconfig.Path(env.Deps)
	if path == "" {
		return false, "", configIOError{errors.New("cannot find afk's config folder: set XDG_CONFIG_HOME to an absolute path or make sure your home directory is known")}
	}
	text, err := readConfigText(env, path)
	if err != nil {
		return false, "", configIOError{err}
	}
	listed = strings.TrimSpace(name)
	if listed == "" {
		listed = filepath.Base(root)
	}
	newText, added, err := daemonconfig.AddProject(text, daemonconfig.Project{Name: name, Path: root})
	if err != nil {
		return false, "", err
	}
	if !added {
		// Already listed, perhaps under another name: report that one.
		if cfg, lerr := daemonconfig.Load(env.Deps); lerr == nil {
			for _, p := range cfg.Projects {
				if filepath.Clean(p.Path) == filepath.Clean(root) {
					listed = p.Name
				}
			}
		}
		return false, listed, nil
	}
	if err := env.mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, "", configIOError{fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)}
	}
	if err := env.write(path, []byte(newText)); err != nil {
		return false, "", configIOError{fmt.Errorf("could not write %s: %w. The file was not changed", path, err)}
	}
	return true, listed, nil
}

// parseWords parses flags that may sit before, between or after the words
// (Go's flag package stops at the first non-flag).
func parseWords(flags *flag.FlagSet, args []string) (words []string, code int, ok bool) {
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, false
			}
			return nil, 2, false
		}
		rest = flags.Args()
		if len(rest) == 0 {
			return words, 0, true
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
}

func runProjectAdd(args []string, stdout, stderr io.Writer, env projectEnv) int {
	flags := flag.NewFlagSet("project add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "The name the project is known by (default: the folder name)")
	flags.Usage = func() { printProjectUsage(stderr) }
	words, code, ok := parseWords(flags, args)
	if !ok {
		return code
	}
	if len(words) > 1 {
		fmt.Fprintf(stderr, "afk project add: expected one folder, but got %d words.\nUsage: %s\n", len(words), projectAddUsage)
		return 2
	}
	flagDir := ""
	if len(words) == 1 {
		flagDir = words[0]
	}
	cwd, err := env.cwd()
	if err != nil {
		fmt.Fprintf(stderr, "afk project add: cannot tell which folder you are in: %v. Pass the folder.\n", err)
		return 1
	}
	home, _ := env.HomeDir()
	dir, err := resolveProject(flagDir, cwd, home)
	if err != nil {
		fmt.Fprintf(stderr, "afk project add: cannot tell which folder to add. Pass the folder.\nUsage: %s\n", projectAddUsage)
		return 2
	}
	res := addProjectForSurface(env, dir, *name)
	printProjectResult(stdout, stderr, "afk project add: ", res)
	return res.Code
}

func runProjectRemove(args []string, stdout, stderr io.Writer, env projectEnv) int {
	flags := flag.NewFlagSet("project remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printProjectUsage(stderr) }
	words, code, ok := parseWords(flags, args)
	if !ok {
		return code
	}
	if len(words) != 1 {
		fmt.Fprintf(stderr, "afk project remove: expected a name or a folder, but got %d word(s).\nUsage: %s\n", len(words), projectRemoveUsage)
		return 2
	}
	res := removeProjectForSurface(env, words[0])
	printProjectResult(stdout, stderr, "afk project remove: ", res)
	return res.Code
}
