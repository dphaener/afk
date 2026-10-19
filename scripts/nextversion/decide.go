// Command nextversion decides the version of the next release. This file is
// the pure rule set: no git, no files, no clock, so every branch is a row in a
// table test. The git side that feeds it lives in main.go.
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// version is a release number. Tags are strict v<major>.<minor>.<patch>.
type version struct{ major, minor, patch int }

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// versionTag is strict on purpose: no prerelease, no build suffix, and no
// leading zeros (so two spellings never name one version).
var versionTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// parseVersion reads a release tag such as "v1.2.3". Anything else is not a
// release tag (for example "vnext" or "v1.2.3-rc1") and reports false.
func parseVersion(tag string) (version, bool) {
	m := versionTag.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	var n [3]int
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil { // digits too large for an int
			return version{}, false
		}
		n[i] = v
	}
	return version{n[0], n[1], n[2]}, true
}

// subjectForm is "type(scope)!: Subject". The type is compared
// case-insensitively by the caller.
var subjectForm = regexp.MustCompile(`^([A-Za-z]+)(\([^)]*\))?(!)?: `)

// isMerge reports whether a message is a merge commit. Merge commits carry
// the pull request title or branch name, not a change of their own, and a
// merge body can quote text from the branch; the merged commits are read
// directly (a merge-commit strategy keeps them in the range), so the merge
// itself adds nothing.
func isMerge(msg string) bool {
	for _, p := range []string{"Merge pull request ", "Merge branch ", "Merge remote-tracking branch "} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

type bump int

const (
	bumpPatch bump = iota
	bumpMinor
	bumpMajor
)

// messageBump is the bump one non-merge commit asks for.
func messageBump(msg string) bump {
	subject, body, _ := strings.Cut(msg, "\n")
	m := subjectForm.FindStringSubmatch(subject)
	if m != nil && m[3] == "!" {
		return bumpMajor
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:") {
			return bumpMajor
		}
	}
	if m != nil && strings.EqualFold(m[1], "feat") {
		return bumpMinor
	}
	return bumpPatch
}

// inBinary reports whether a changed path can alter the afk binary.
//
// plugin_assets.go embeds the plugin folders with
// //go:embed .claude-plugin .agents all:plugins, so those two root folders and
// plugins/ ship inside the binary even though they hold no Go. contrib/ is
// listed likewise as part of the shipped tree. Go files under scripts/ (this
// tool included) and the root testdata/ never reach the binary.
func inBinary(path string) bool {
	switch path {
	case "go.mod", "go.sum":
		return true
	}
	for _, dir := range []string{"plugins/", "contrib/", "internal/", ".claude-plugin/", ".agents/"} {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	if strings.HasPrefix(path, "scripts/") || strings.HasPrefix(path, "testdata/") {
		return false
	}
	return strings.HasSuffix(path, ".go")
}

// decide returns "skip" or the next version without its "v".
//
// last is the highest release tag reachable from the pushed commit, valid only
// when hasLast. messages are full commit messages (subject, then body) of every
// commit in the push range: a merge-commit strategy hides the real commits
// behind the merge, so reading only the newest commit would miss a feat.
func decide(last string, hasLast bool, messages []string, paths []string) string {
	// The first release publishes whatever the range holds.
	if !hasLast {
		return "0.1.0"
	}
	prev, ok := parseVersion(last)
	if !ok {
		// Unreachable from the git side, which filters tags with parseVersion;
		// treat a bad tag as no release rather than invent a number.
		return "0.1.0"
	}
	shipped := false
	for _, p := range paths {
		if inBinary(p) {
			shipped = true
			break
		}
	}
	if !shipped {
		return "skip"
	}
	level := bumpPatch
	for _, msg := range messages {
		if isMerge(msg) {
			continue
		}
		if b := messageBump(msg); b > level {
			level = b
		}
	}
	switch level {
	case bumpMajor:
		return version{prev.major + 1, 0, 0}.String()
	case bumpMinor:
		return version{prev.major, prev.minor + 1, 0}.String()
	}
	return version{prev.major, prev.minor, prev.patch + 1}.String()
}
