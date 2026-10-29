package install

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

// ActionKind says what the runner should do with the plan's next step.
type ActionKind string

const (
	// ActionRun: do the returned to-do step.
	ActionRun ActionKind = "run"
	// ActionStop: the returned step is "cannot"; show its fix and stop.
	ActionStop ActionKind = "stop"
	// ActionFinished: nothing is left but the final check, which the runner
	// shows at the end of every run.
	ActionFinished ActionKind = "finished"
)

// Action is Next's answer. Step is the zero value when Kind is ActionFinished.
type Action struct {
	Kind ActionKind
}

// Next picks what to do next, taking steps in plan order. A "cannot" step is
// a stop only when it comes before or at the first to-do step: a to-do step
// ahead of it can still be done (and may even be what makes the cannot step
// clear), and stopping early would leave work undone for no reason. When no
// to-do step is left, any cannot step still stops the run, so a plan that
// cannot finish never reports "finished". StepCheck is never run as an
// action; it is the report at the end.
func Next(p Plan) (Step, Action) {
	for _, s := range p.Steps {
		if s.ID == StepCheck && s.State != Cannot {
			continue
		}
		// The Rex offer is optional: a machine without Rex is not stuck, so a
		// Cannot there must not stop the install before the later steps.
		if s.ID == StepRex && s.State == Cannot {
			continue
		}
		switch s.State {
		case Cannot:
			return s, Action{Kind: ActionStop}
		case ToDo:
			return s, Action{Kind: ActionRun}
		}
	}
	return Step{}, Action{Kind: ActionFinished}
}

// Verify checks one step after its action and a fresh probe. The re-probe is
// the proof: a login the person abandoned, or a command that exited 0 but
// changed nothing, shows up here as a step that is still not done.
func Verify(after Plan, id StepID) error {
	for _, s := range after.Steps {
		if s.ID != id {
			continue
		}
		if s.State == Done {
			return nil
		}
		return fmt.Errorf("%q is still not done. %s", s.Title, s.Reason)
	}
	return fmt.Errorf("step %q is not in the plan", id)
}

// aptNames maps a tool the plan lists to its Ubuntu package. They are the
// same today; the map keeps the day one differs a one-line change.
var aptNames = map[string]string{"git": "git", "gh": "gh", "jq": "jq", "ruby": "ruby"}

// AptPackages lists the Ubuntu packages for the tools that are missing, in
// the order of Facts.Tools. A tool with no known package name is skipped
// rather than guessed.
func AptPackages(f Facts) []string {
	var out []string
	for _, t := range f.Tools {
		if t.Found {
			continue
		}
		if name, ok := aptNames[t.Name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// brewNames maps a tool the plan lists to its Homebrew formula. It is a
// separate map from aptNames on purpose: the names are the same today, but the
// day one differs between Ubuntu and Homebrew it is a one-line change here and
// the apt list is not touched.
var brewNames = map[string]string{"git": "git", "gh": "gh", "jq": "jq", "ruby": "ruby"}

// BrewPackages lists the Homebrew formulae for the tools that are missing, in
// the order of Facts.Tools. A tool with no known formula is skipped rather
// than guessed.
func BrewPackages(f Facts) []string {
	var out []string
	for _, t := range f.Tools {
		if t.Found {
			continue
		}
		if name, ok := brewNames[t.Name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// LaunchAgentPath is where afk's launch agent file lives for a home folder.
// The probe reads the same path, so install and probe cannot disagree.
func LaunchAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

// PlistMarker is the second line of a launch agent afk install wrote, right
// after the XML declaration. It is a comment after the declaration because a
// plist is XML and cannot start with "#"; it tells a later run its own file,
// which it may replace, from one the owner wrote.
const PlistMarker = "<!-- Written by afk install. afk install replaces this file when it changes. -->"

// LaunchAgent is what RenderLaunchAgent needs.
type LaunchAgent struct {
	Program string // absolute path to the afk binary
	PATH    string // the PATH the daemon runs with
	LogPath string // file for the daemon's standard output and error
}

func plistString(b *bytes.Buffer, s string) {
	// EscapeText only fails when the writer does, and a bytes.Buffer never does.
	_ = xml.EscapeText(b, []byte(s))
}

// RenderLaunchAgent writes the launchd property list for afk daemon. Each key
// is stated as launchd.plist(5) describes it:
//
//   - ProgramArguments[0] is an absolute path because launchd resolves a
//     relative one with _PATH_STDPATH, not with the job's own PATH.
//   - KeepAlive is plain true, not the SuccessfulExit dictionary, because the
//     daemon exits 0 on purpose when it finds a new version of itself and must
//     be started again.
//   - ThrottleInterval 30 matches the Linux unit's RestartSec=30; launchd's
//     default is 10 seconds.
//   - AbandonProcessGroup is true because launchd otherwise kills every
//     process left in the job's process group when the job dies, and session
//     hosts must outlive the daemon, as with KillMode=process on Linux.
//   - PATH is spelled out in EnvironmentVariables because a launchd job does
//     not read the shell profile.
//
// Every string value is XML-escaped.
func RenderLaunchAgent(a LaunchAgent) []byte {
	var b bytes.Buffer
	str := func(indent, tag, s string) {
		b.WriteString(indent + "<" + tag + ">")
		plistString(&b, s)
		b.WriteString("</" + tag + ">\n")
	}
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(PlistMarker + "\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	str("\t", "string", LaunchAgentLabel)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	str("\t\t", "string", a.Program)
	str("\t\t", "string", "daemon")
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>30</integer>\n")
	b.WriteString("\t<key>AbandonProcessGroup</key>\n\t<true/>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n")
	str("\t\t", "string", a.PATH)
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n")
	str("\t", "string", a.LogPath)
	b.WriteString("\t<key>StandardErrorPath</key>\n")
	str("\t", "string", a.LogPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// LaunchAgentWrittenByAfk reports whether the second line, after the XML
// declaration, is the marker.
func LaunchAgentWrittenByAfk(b []byte) bool {
	lines := bytes.SplitN(b, []byte("\n"), 3)
	return len(lines) >= 2 && strings.TrimRight(string(lines[1]), "\r") == PlistMarker
}

// ServicePATH builds the unit's PATH. A user service does not read the shell
// profile, so every folder holding a tool afk found must be spelled out. The
// first position wins on a duplicate so the order stays predictable.
func ServicePATH(f Facts, home string) string {
	dirs := []string{filepath.Join(home, "bin"), filepath.Join(home, ".local", "bin")}
	for _, t := range f.Tools {
		if t.Found && t.Path != "" {
			dirs = append(dirs, filepath.Dir(t.Path))
		}
	}
	if f.AgentCLI.Found && f.AgentCLI.Path != "" {
		dirs = append(dirs, filepath.Dir(f.AgentCLI.Path))
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/bin")
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ":")
}

// UnitMarker is the first line of a unit afk install wrote. It is how a later
// run tells its own file, which it may replace, from one the owner wrote,
// which it must leave alone.
const UnitMarker = "# Written by afk install. afk install replaces this file when it changes."

// pathLine is the commented line in contrib/afkd.service that RenderUnit
// turns into a real setting.
const pathLinePrefix = "#Environment=PATH="

// RenderUnit puts the marker first and turns the template's commented PATH
// line into a live one. Line endings are kept as the template has them.
func RenderUnit(template []byte, path string) []byte {
	var out bytes.Buffer
	out.WriteString(UnitMarker + "\n")
	for _, line := range bytes.SplitAfter(template, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(pathLinePrefix)) {
			out.WriteString("Environment=PATH=" + path + "\n")
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

// WrittenByAfk reports whether the unit starts with the marker line.
func WrittenByAfk(unit []byte) bool {
	first, _, _ := bytes.Cut(unit, []byte("\n"))
	return strings.TrimRight(string(first), "\r") == UnitMarker
}

// WebAddress is the address to show the person. A bind of 0.0.0.0 is not an
// address anyone can type, so it is swapped for the machine's own address
// when one is known.
func WebAddress(bind, publicURL, hostIP string) string {
	if publicURL != "" {
		return publicURL
	}
	host, port, ok := splitBind(bind)
	if !ok {
		return "http://" + bind
	}
	switch host {
	case "", "0.0.0.0", "::":
		if hostIP != "" {
			host = hostIP
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port
}

func splitBind(bind string) (host, port string, ok bool) {
	i := strings.LastIndex(bind, ":")
	if i < 0 {
		return "", "", false
	}
	host, port = bind[:i], bind[i+1:]
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return host, port, true
}
