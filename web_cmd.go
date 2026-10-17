package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	xterm "github.com/charmbracelet/x/term"

	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpass"
)

const webPasswordUsage = "afk web password"

// webEnv is everything `afk web` touches outside its arguments, so tests
// run against a temp state folder with a scripted terminal and never prompt
// or write the real password file.
type webEnv struct {
	state usagestate.Deps
	// isTerminal reports whether stdin is a terminal. It is asked before any
	// prompt: a password read from a pipe would be echoed into a log or a
	// shell history, and a half-answered prompt would hang a script.
	isTerminal func() bool
	// readPassword reads one answer without echo.
	readPassword func() ([]byte, error)
	rand         io.Reader
	iterations   int
}

func realWebEnv() webEnv {
	return webEnv{
		state:        usagestate.DefaultDeps(),
		isTerminal:   func() bool { return isTerminal(os.Stdin) },
		readPassword: func() ([]byte, error) { return xterm.ReadPassword(os.Stdin.Fd()) },
		rand:         rand.Reader,
		iterations:   webpass.DefaultIterations,
	}
}

// cmdWeb implements `afk web`.
func cmdWeb(args []string) {
	os.Exit(runWeb(args, os.Stdout, os.Stderr, realWebEnv()))
}

func printWebUsage(w io.Writer) {
	fmt.Fprintf(w, `afk web — the password for the web page that afk daemon serves

Usage:
  %s

Asks for a new password twice, without showing it, and saves a salted hash of
it in afk's state folder (web-password.json, readable only by you). The
password itself is never saved. It must be at least %d characters and the
command must be run in a terminal.

Setting a password again replaces the old one and signs every browser out. A
running afk daemon whose page was off needs a restart to turn the page on.

Exit codes: 0 saved, 1 nothing was saved, 2 the command was written wrongly.
`, webPasswordUsage, webpass.MinLength)
}

func runWeb(args []string, stdout, stderr io.Writer, env webEnv) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printWebUsage(stdout)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "afk web: say what to do.")
		printWebUsage(stderr)
		return 2
	}
	if args[0] != "password" {
		fmt.Fprintf(stderr, "afk web: unknown command %q.\n", args[0])
		printWebUsage(stderr)
		return 2
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "afk web password takes no arguments (got %q).\n", args[1])
		return 2
	}
	return runWebPassword(stdout, stderr, env)
}

func runWebPassword(stdout, stderr io.Writer, env webEnv) int {
	if !env.isTerminal() {
		fmt.Fprintln(stderr, "afk web password must be run in a terminal, because it asks for the password without showing it. Nothing was saved.")
		return 1
	}
	ask := func(prompt string) (string, bool) {
		fmt.Fprint(stderr, prompt)
		b, err := env.readPassword()
		// No echo means the cursor stays on the prompt line.
		fmt.Fprintln(stderr)
		if err != nil {
			fmt.Fprintf(stderr, "Could not read the password: %v. Nothing was saved.\n", err)
			return "", false
		}
		return string(b), true
	}
	first, ok := ask("New password: ")
	if !ok {
		return 1
	}
	second, ok := ask("Type it again: ")
	if !ok {
		return 1
	}
	if first != second {
		fmt.Fprintln(stderr, "The two passwords did not match. Nothing was saved.")
		return 1
	}
	// Checked here as well as in webpass.New so the message can be plain; an
	// empty answer lands here too.
	if utf8.RuneCountInString(first) < webpass.MinLength {
		fmt.Fprintf(stderr, "The password needs at least %d characters. Nothing was saved.\n", webpass.MinLength)
		return 1
	}
	rec, err := webpass.New(first, env.rand, env.iterations)
	if err != nil {
		if errors.Is(err, webpass.ErrTooShort) {
			fmt.Fprintf(stderr, "The password needs at least %d characters. Nothing was saved.\n", webpass.MinLength)
		} else {
			fmt.Fprintf(stderr, "Could not make the password record: %v. Nothing was saved.\n", err)
		}
		return 1
	}
	if err := webpass.Write(env.state, rec); err != nil {
		path, perr := webpass.Path(env.state)
		if perr != nil {
			path = webpass.FileName
		}
		fmt.Fprintf(stderr, "Could not save the password to %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintln(stdout, "The web page password is saved.")
	fmt.Fprintln(stdout, "Every browser that was signed in before is now signed out.")
	fmt.Fprintln(stdout, "If afk daemon is running and its page was off, restart the daemon to turn the page on.")
	return 0
}
