package main

import (
	"os"

	"github.com/mattn/go-isatty"
)

// isTerminal reports whether f is an interactive terminal. A bare
// char-device check is not enough: /dev/null is a character device, so
// `afk new </dev/null` would otherwise count as interactive.
func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
