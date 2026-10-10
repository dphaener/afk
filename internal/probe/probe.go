package probe

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/notify"
)

// Result represents the probe result for a dependency.
type Result struct {
	Present            bool
	DegradationMessage string
}

// ProbeClaudeCLI detects the Claude Code CLI and validates minimum version 2.1.205.
// Returns a fatal error message if not present or version is too old.
func ProbeClaudeCLI() Result {
	path, err := exec.LookPath("claude")
	if err != nil {
		return Result{
			Present:            false,
			DegradationMessage: "Claude Code CLI not found — install from https://claude.com/claude-code",
		}
	}

	// Get version
	cmd := exec.Command(path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return Result{
			Present:            false,
			DegradationMessage: "Claude Code CLI found but --version failed — install a newer version",
		}
	}

	version := strings.TrimSpace(string(output))
	// Expected format: "claude-code/2.1.205 ..." or similar
	versionStr := parseClaudeVersion(version)
	if versionStr == "" {
		return Result{
			Present:            false,
			DegradationMessage: fmt.Sprintf("Claude Code CLI version unparseable (%s) — install version 2.1.205 or later", version),
		}
	}

	if !semverGte(versionStr, "2.1.205") {
		return Result{
			Present:            false,
			DegradationMessage: fmt.Sprintf("Claude Code CLI version %s is too old — upgrade to 2.1.205 or later", versionStr),
		}
	}

	return Result{
		Present:            true,
		DegradationMessage: "",
	}
}

// ProbeWorktrunk detects the worktrunk (wt) binary.
func ProbeWorktrunk() Result {
	_, err := exec.LookPath("wt")
	if err != nil {
		return Result{
			Present:            false,
			DegradationMessage: "worktrunk (wt) not found — isolated working trees require worktrunk",
		}
	}

	return Result{
		Present:            true,
		DegradationMessage: "",
	}
}

// ProbeGitSpice detects git-spice binary (checks both 'git-spice' and 'gs').
func ProbeGitSpice() Result {
	// Try 'gs' first, then 'git-spice'
	for _, name := range []string{"gs", "git-spice"} {
		_, err := exec.LookPath(name)
		if err == nil {
			return Result{
				Present:            true,
				DegradationMessage: "",
			}
		}
	}

	return Result{
		Present:            false,
		DegradationMessage: "git-spice not found — sessions will use plain branches instead of stacked branches",
	}
}

// ProbeGh detects the GitHub CLI (gh) binary.
func ProbeGh() Result {
	_, err := exec.LookPath("gh")
	if err != nil {
		return Result{
			Present:            false,
			DegradationMessage: "gh (GitHub CLI) not found — PR operations require GitHub CLI",
		}
	}

	return Result{
		Present:            true,
		DegradationMessage: "",
	}
}

// ProbeNotifier detects a desktop-notification backend via notify.Select, which
// is the SAME resolver `afk notify` uses — deliberately, rather than a LookPath
// list of its own. A probe that disagreed with the command about which backend
// wins would report a capability the user cannot get, and the two are ordered
// (alerter beats osascript, because only alerter can report a click).
//
// Absence is reported through Result, never an exit code, exactly like every probe
// above. `afk notify --check` is the one place that inverts this, because
// notify.sh needs something a shell `if` can branch on.
func ProbeNotifier() Result {
	_, _, found := notify.Select(exec.LookPath)
	if found {
		return Result{
			Present:            true,
			DegradationMessage: "",
		}
	}

	return Result{
		Present:            false,
		DegradationMessage: "No notifier found (alerter, osascript) — desktop notifications disabled; install with: brew install vjeantet/tap/alerter",
	}
}

// parseClaudeVersion extracts the version string from claude --version output.
// Expects formats like "claude-code/2.1.205 ..." or "2.1.205 ...".
func parseClaudeVersion(output string) string {
	// Try to match patterns like "X.Y.Z"
	re := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// semverGte returns true if version >= minVersion using semantic versioning.
// Parses major.minor.patch and compares numerically.
// Examples: "2.1.205" >= "2.1.205" -> true, "2.1.21" >= "2.1.205" -> false
func semverGte(version, minVersion string) bool {
	v := parseSemver(version)
	min := parseSemver(minVersion)
	if v == nil || min == nil {
		return false
	}

	if v[0] != min[0] {
		return v[0] > min[0]
	}
	if v[1] != min[1] {
		return v[1] > min[1]
	}
	return v[2] >= min[2]
}

// parseSemver parses a semantic version string into [major, minor, patch].
func parseSemver(version string) []int {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return nil
	}

	result := make([]int, 3)
	for i := 0; i < 3; i++ {
		num, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		result[i] = num
	}
	return result
}
