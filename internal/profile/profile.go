// Package profile reads the owner profile: profile.toml in afk's config
// folder, beside config.toml.
//
// The file is the owner's and belongs to the machine, not to a project. It
// records how the owner likes to build and host things (database, deploy
// tool, component library), so a plan session reads a setup fact from here or
// asks, and never guesses it (docs/PLAN.md M5). It is hand-edited TOML; each
// [[entry]] is a topic, a value, a strength (rule or default), an optional
// kind of project it narrows to and an optional reason.
//
// Folder rule: $XDG_CONFIG_HOME/afk when that variable is an absolute path,
// otherwise ~/.config/afk. This is copied from internal/daemonconfig rather
// than imported so this package stays a leaf with nothing but the standard
// library and the TOML reader; the two must be kept in step so the files land
// side by side.
//
// No secret may be stored here (M5 rule 9). The profile is read into every
// plan session's context and is easy to commit to a dotfiles repository, so
// LooksSecret refuses keys and tokens, and Validate reports any it finds.
package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// StrengthRule marks an entry the plan must follow.
	StrengthRule = "rule"
	// StrengthDefault marks an entry used unless the project says otherwise.
	StrengthDefault = "default"
	// NoPreference as a value means "recommend one and say why".
	NoPreference = "no preference"
	// FileName is the profile's name inside afk's config folder.
	FileName = "profile.toml"
)

// Entry is one stated preference.
type Entry struct {
	Topic    string `toml:"topic"`
	Value    string `toml:"value"`
	Strength string `toml:"strength"`
	Kind     string `toml:"kind"`
	Reason   string `toml:"reason"`
}

// Profile is the whole file.
type Profile struct {
	RuleFiles []string `toml:"rule_files"`
	Entries   []Entry  `toml:"entry"`
}

// Deps is the injectable environment, shaped like daemonconfig.Deps so tests
// never touch the real home or config folder.
type Deps struct {
	// Getenv looks up an environment variable, empty string if unset.
	Getenv func(string) string
	// HomeDir returns the user's home directory.
	HomeDir func() (string, error)
	// ReadFile reads a file. Nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// DefaultDeps returns Deps wired to the real environment.
func DefaultDeps() Deps {
	return Deps{Getenv: os.Getenv, HomeDir: os.UserHomeDir, ReadFile: os.ReadFile}
}

// Path returns where profile.toml lives, or "" when neither XDG_CONFIG_HOME
// nor the home directory can be determined. A relative XDG_CONFIG_HOME is
// ignored, as the XDG spec says.
func Path(d Deps) string {
	if x := d.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "afk", FileName)
	}
	home, err := d.HomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "afk", FileName)
}

// Parse decodes profile text. The second value lists the keys the file has
// that this version does not know, as dotted strings, so a typo is reported
// and a newer file is not refused. The only normalisation is that an entry
// with no strength gets StrengthDefault.
func Parse(data []byte) (Profile, []string, error) {
	var p Profile
	md, err := toml.Decode(string(data), &p)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("not valid TOML, fix the syntax: %w", err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		unknown = append(unknown, k.String())
	}
	for i := range p.Entries {
		if p.Entries[i].Strength == "" {
			p.Entries[i].Strength = StrengthDefault
		}
	}
	return p, unknown, nil
}

// Load reads and parses profile.toml. A missing file is found == false with
// no error, so a fresh machine can run `afk profile`.
func Load(d Deps) (p Profile, path string, found bool, unknown []string, err error) {
	path = Path(d)
	if path == "" {
		return Profile{}, "", false, nil, errors.New("cannot find your home folder, so there is nowhere to keep profile.toml; set HOME or XDG_CONFIG_HOME")
	}
	read := d.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	data, rerr := read(path)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return Profile{}, path, false, nil, nil
		}
		return Profile{}, path, false, nil, fmt.Errorf("%s: cannot read the file: %w", path, rerr)
	}
	p, unknown, perr := Parse(data)
	if perr != nil {
		return Profile{}, path, true, nil, fmt.Errorf("%s: %w", path, perr)
	}
	return p, path, true, unknown, nil
}

// TopicKey is the form in which topics are compared: lower case, trimmed,
// runs of white space collapsed to one space. Topics are stored as written.
func TopicKey(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

var kindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidKind reports whether s can be a kind: lower case words joined by "-".
func ValidKind(s string) bool { return kindPattern.MatchString(s) }

// Problem is one thing wrong with the profile.
type Problem struct {
	Level   string // "error" or "warning"
	Entry   int    // index in Entries, -1 for the file
	Topic   string
	Rule    string // stable rule id
	Message string
}

// Validate checks a parsed profile. It is pure: unknown comes from Parse.
// A secret message never repeats the offending text, and when the topic is
// itself the secret the topic is left out too.
func Validate(p Profile, unknown []string) []Problem {
	var out []Problem
	add := func(level string, i int, topic, rule, msg string) {
		out = append(out, Problem{Level: level, Entry: i, Topic: topic, Rule: rule, Message: msg})
	}
	seen := map[string]int{}
	for i, e := range p.Entries {
		n := i + 1
		label := fmt.Sprintf("Entry %d", n)
		if e.Topic != "" {
			label = fmt.Sprintf("Entry %d (%q)", n, e.Topic)
		}
		topicSecret := false
		if why, bad := LooksSecret(e.Topic); bad {
			topicSecret = true
			label = fmt.Sprintf("Entry %d", n)
			add("error", i, "", "secret", fmt.Sprintf("%s: the topic looks like a key or token (%s). Remove it and keep keys and tokens out of the profile.", label, why))
		}
		for _, f := range []struct{ name, val string }{{"value", e.Value}, {"reason", e.Reason}, {"kind", e.Kind}} {
			if why, bad := LooksSecret(f.val); bad {
				topic := e.Topic
				if topicSecret {
					topic = ""
				}
				add("error", i, topic, "secret", fmt.Sprintf("%s: the %s looks like a key or token (%s). Remove it and keep keys and tokens out of the profile.", label, f.name, why))
			}
		}
		if strings.TrimSpace(e.Topic) == "" {
			add("error", i, e.Topic, "missing-topic", fmt.Sprintf("%s has no topic. Add a topic such as \"database\".", label))
		}
		if strings.TrimSpace(e.Value) == "" {
			add("error", i, e.Topic, "missing-value", fmt.Sprintf("%s has no value. Add one, or use %q if you have no preference.", label, NoPreference))
		}
		if e.Strength != StrengthRule && e.Strength != StrengthDefault {
			shown := fmt.Sprintf("the strength %q", e.Strength)
			if _, bad := LooksSecret(e.Strength); bad {
				shown = "a strength that is not valid"
			}
			add("error", i, e.Topic, "bad-strength", fmt.Sprintf("%s has %s. Change it to %q or %q.", label, shown, StrengthRule, StrengthDefault))
		}
		if e.Kind != "" && !ValidKind(e.Kind) {
			if _, bad := LooksSecret(e.Kind); !bad {
				add("error", i, e.Topic, "bad-kind", fmt.Sprintf("%s has the kind %q. Use lower case letters, digits and \"-\", such as \"web-app\".", label, e.Kind))
			} else {
				add("error", i, e.Topic, "bad-kind", fmt.Sprintf("%s has a kind that is not valid. Use lower case letters, digits and \"-\", such as \"web-app\".", label))
			}
		}
		if strings.TrimSpace(e.Topic) != "" {
			key := TopicKey(e.Topic) + "\x00" + e.Kind
			if first, dup := seen[key]; dup {
				add("error", i, e.Topic, "duplicate-entry", fmt.Sprintf("%s repeats entry %d (same topic and kind). Remove one of them.", label, first+1))
			} else {
				seen[key] = i
			}
		}
	}
	for i, f := range p.RuleFiles {
		label := fmt.Sprintf("rule_files item %d", i+1)
		if why, bad := LooksSecret(f); bad {
			add("error", -1, "", "secret", fmt.Sprintf("%s looks like a key or token (%s). Remove it and keep keys and tokens out of the profile.", label, why))
		}
		if f == "" || !(filepath.IsAbs(f) || strings.HasPrefix(f, "~/")) {
			add("error", -1, "", "bad-rule-file", fmt.Sprintf("%s must be an absolute path or start with \"~/\". Change it to the full path of the document.", label))
		}
	}
	for _, k := range unknown {
		add("warning", -1, "", "unknown-key", fmt.Sprintf("The key %q is not one afk knows. Check the spelling, or remove it.", k))
	}
	return out
}

// Find returns the index of the entry with this topic (by TopicKey) and
// exactly this kind; "" matches only a general entry. When duplicated it
// returns the last, the one Select would also prefer.
func Find(p Profile, topic, kind string) (int, bool) {
	key := TopicKey(topic)
	for i := len(p.Entries) - 1; i >= 0; i-- {
		e := p.Entries[i]
		if TopicKey(e.Topic) == key && e.Kind == kind {
			return i, true
		}
	}
	return -1, false
}

// Select returns the entries that apply to a kind of project, one per topic.
// A kind-specific entry beats a general one whatever their strengths, because
// the owner named that kind on purpose; within one scope a rule beats a
// default; between equals the later entry wins, as in a config file. Results
// follow the first appearance of each topic. An empty or unmatched kind
// yields the general entries.
func Select(p Profile, kind string) []Entry {
	best := map[string]int{}
	var order []string
	for i, e := range p.Entries {
		if e.Kind != "" && (kind == "" || e.Kind != kind) {
			continue
		}
		key := TopicKey(e.Topic)
		cur, ok := best[key]
		if !ok {
			order = append(order, key)
			best[key] = i
			continue
		}
		// Scope outranks strength, so compare scope first, then strength.
		if scopeThenStrength(e) >= scopeThenStrength(p.Entries[cur]) {
			best[key] = i
		}
	}
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		out = append(out, p.Entries[best[k]])
	}
	return out
}

func scopeThenStrength(e Entry) int {
	r := 0
	if e.Kind != "" {
		r += 2
	}
	if e.Strength == StrengthRule {
		r++
	}
	return r
}

// ExpandHome turns "~/x" into home/x and leaves anything else unchanged.
func ExpandHome(path, home string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
