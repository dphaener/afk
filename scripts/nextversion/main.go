// Command nextversion prints "skip" or the next release version for a push.
//
// Usage: go run ./scripts/nextversion <from-sha> <to-sha>
//
// It reads only git. It lives under scripts/ so it is not part of the afk
// binary.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr))
}

// run is main without the process exit, so tests can drive it. dir is the
// working directory for every git command. Nothing is written to stdout unless
// the answer is complete: the release script must never read an error as a
// version.
func run(args []string, dir string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: nextversion <from-sha> <to-sha>")
		return 2
	}
	from, to := args[0], args[1]

	out, err := git(dir, "tag", "--merged", to, "--list", "v*")
	if err != nil {
		fmt.Fprintln(stderr, "nextversion: cannot list release tags:", err)
		return 1
	}
	// The highest tag, not the nearest one: it is the top of the version line.
	var last string
	var lastV version
	hasLast := false
	for _, tag := range strings.Fields(out) {
		v, ok := parseVersion(tag)
		if !ok {
			continue
		}
		if !hasLast || newer(v, lastV) {
			last, lastV, hasLast = tag, v, true
		}
	}

	var logOut, pathOut string
	if isAllZeros(from) {
		// A first push has no base commit: the whole history is the range.
		logOut, err = git(dir, "log", "-z", "--format=%B", to)
		if err == nil {
			pathOut, err = git(dir, "ls-tree", "-r", "--name-only", to)
		}
	} else {
		logOut, err = git(dir, "log", "-z", "--format=%B", from+".."+to)
		if err == nil {
			// --no-renames lists both sides of a rename as changed paths.
			pathOut, err = git(dir, "diff", "--name-only", "--no-renames", from, to)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "nextversion: cannot read the commit range:", err)
		return 1
	}

	var messages []string
	for _, m := range strings.Split(logOut, "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			messages = append(messages, m)
		}
	}
	var paths []string
	for _, p := range strings.Split(pathOut, "\n") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	fmt.Fprintln(stdout, decide(last, hasLast, messages, paths))
	return 0
}

func newer(a, b version) bool {
	if a.major != b.major {
		return a.major > b.major
	}
	if a.minor != b.minor {
		return a.minor > b.minor
	}
	return a.patch > b.patch
}

func isAllZeros(s string) bool {
	return s != "" && strings.Trim(s, "0") == ""
}

// git runs one read-only git command in dir. On failure the error carries
// git's own message.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], strings.ReplaceAll(msg, "\n", " "))
	}
	return string(out), nil
}
