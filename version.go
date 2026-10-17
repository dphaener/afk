package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"
)

// version is the release version, set by release builds with
// -ldflags "-X main.version=<v>". It stays empty for `go build` and
// `make install`, which print "dev". A linker flag is used rather than
// debug.ReadBuildInfo's Main.Version because that is "(devel)" for a build in
// a checkout and says nothing for a tag the release script chose itself.
var version string

// vcsRevisionChars is how much of the commit hash the version line shows. Twelve
// characters is enough to name a commit in a repository this size without
// ambiguity, and keeps the line short enough to paste into a bug report.
const vcsRevisionChars = 12

// versionLine builds `afk <version> (<commit>, <date>)` from the linker-set
// version and the build information Go stamps into a binary built inside a git
// checkout (vcs.revision, vcs.time, vcs.modified). It is pure: cmdVersion does
// the debug.ReadBuildInfo call and passes the answer in, so every shape of
// build information is table-testable.
//
// A missing time drops the date with its comma, and a missing revision gives
// "(unknown)": a binary built from a source archive has no VCS stamp, and an
// invented value would be worse than saying so.
func versionLine(ldVersion string, info *debug.BuildInfo, ok bool) string {
	v := ldVersion
	if v == "" {
		v = "dev"
	}
	if !ok || info == nil {
		return fmt.Sprintf("afk %s (unknown)", v)
	}

	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	rev := settings["vcs.revision"]
	if rev == "" {
		return fmt.Sprintf("afk %s (unknown)", v)
	}
	if len(rev) > vcsRevisionChars {
		rev = rev[:vcsRevisionChars]
	}
	if settings["vcs.modified"] == "true" {
		rev += "+dirty"
	}

	if t, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
		return fmt.Sprintf("afk %s (%s, %s)", v, rev, t.UTC().Format("2006-01-02"))
	}
	return fmt.Sprintf("afk %s (%s)", v, rev)
}

func printVersionUsage(w io.Writer) {
	fmt.Fprint(w, `afk version — print the version this binary was built from

Usage:
  afk version
  afk --version

Prints "afk <version> (<commit>, <date>)". A build made from a checkout rather
than a release says "dev".

Exit codes: 0 done, 2 the command was written wrongly.
`)
}

// runVersion is the argument decision behind cmdVersion, with the build
// information passed in and the output streams injected so it is testable.
func runVersion(args []string, ldVersion string, info *debug.BuildInfo, ok bool, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		fmt.Fprintln(stdout, versionLine(ldVersion, info, ok))
		return 0
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help"):
		printVersionUsage(stdout)
		return 0
	}
	printVersionUsage(stderr)
	return 2
}

func cmdVersion(args []string) {
	info, ok := debug.ReadBuildInfo()
	os.Exit(runVersion(args, version, info, ok, os.Stdout, os.Stderr))
}
