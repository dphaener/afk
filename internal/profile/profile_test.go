package profile

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func names(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Topic+"="+e.Value)
	}
	return out
}

// TestSelect: the rows "kind only, asked for another kind" and "general rule
// against a kind-specific default" are load-bearing: scope must outrank
// strength, or a project kind could never override a house-wide rule. The
// either-order rule/default rows pin that file order does not decide
// strength, and the case row pins that topics compare by TopicKey.
func TestSelect(t *testing.T) {
	e := func(topic, value, strength, kind string) Entry {
		return Entry{Topic: topic, Value: value, Strength: strength, Kind: kind}
	}
	cases := []struct {
		name string
		es   []Entry
		kind string
		want []string
	}{
		{"general only", []Entry{e("db", "pg", "default", "")}, "cli", []string{"db=pg"}},
		{"kind only asked for it", []Entry{e("db", "sqlite", "default", "cli")}, "cli", []string{"db=sqlite"}},
		{"kind only asked for another", []Entry{e("db", "sqlite", "default", "cli")}, "web-app", nil},
		{"both kind wins", []Entry{e("db", "pg", "default", ""), e("db", "sqlite", "default", "cli")}, "cli", []string{"db=sqlite"}},
		{"both kind wins listed first", []Entry{e("db", "sqlite", "default", "cli"), e("db", "pg", "default", "")}, "cli", []string{"db=sqlite"}},
		{"unknown kind gets general", []Entry{e("db", "pg", "default", ""), e("db", "sqlite", "default", "cli")}, "game", []string{"db=pg"}},
		{"rule beats default rule first", []Entry{e("db", "a", "rule", ""), e("db", "b", "default", "")}, "", []string{"db=a"}},
		{"rule beats default default first", []Entry{e("db", "b", "default", ""), e("db", "a", "rule", "")}, "", []string{"db=a"}},
		{"general rule loses to kind default", []Entry{e("db", "pg", "rule", ""), e("db", "sqlite", "default", "cli")}, "cli", []string{"db=sqlite"}},
		{"equal later wins", []Entry{e("db", "a", "default", ""), e("db", "b", "default", "")}, "", []string{"db=b"}},
		{"order by first appearance", []Entry{e("a", "1", "default", ""), e("b", "2", "default", ""), e("a", "3", "rule", "")}, "", []string{"a=3", "b=2"}},
		{"empty kind general only", []Entry{e("db", "pg", "default", ""), e("ci", "x", "default", "cli")}, "", []string{"db=pg"}},
		{"topic case and spacing", []Entry{e("Hosting  Deploy", "a", "default", ""), e(" hosting deploy ", "b", "default", "")}, "", []string{" hosting deploy =b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := names(Select(Profile{Entries: c.es}, c.kind))
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestPath: the relative-XDG row is load-bearing, it must fall back to the
// home folder as daemonconfig does so both files share a folder.
func TestPath(t *testing.T) {
	cases := []struct {
		name string
		xdg  string
		home string
		herr error
		want string
	}{
		{"default", "", "/home/u", nil, "/home/u/.config/afk/profile.toml"},
		{"absolute xdg", "/x/cfg", "/home/u", nil, "/x/cfg/afk/profile.toml"},
		{"relative xdg ignored", "cfg", "/home/u", nil, "/home/u/.config/afk/profile.toml"},
		{"no home", "", "", errors.New("none"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Deps{
				Getenv:  func(string) string { return c.xdg },
				HomeDir: func() (string, error) { return c.home, c.herr },
			}
			if got := Path(d); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestLoad: a missing file must be found == false with no error (a fresh
// machine), while an unreadable one must be an error naming the path.
func TestLoad(t *testing.T) {
	cases := []struct {
		name      string
		data      string
		rerr      error
		found     bool
		wantErr   string
		strength  string
		unknown   []string
		wantEntry bool
	}{
		{name: "missing", rerr: fs.ErrNotExist},
		{name: "unreadable", rerr: errors.New("denied"), wantErr: "cannot read the file"},
		{name: "bad toml", data: "[[entry", found: true, wantErr: "not valid TOML, fix the syntax"},
		{name: "absent strength is default", data: "[[entry]]\ntopic='a'\nvalue='b'\n", found: true, strength: StrengthDefault, wantEntry: true},
		{name: "unknown keys", data: "colour='x'\n[[entry]]\ntopic='a'\nvalue='b'\nbogus=1\n", found: true, strength: StrengthDefault, wantEntry: true, unknown: []string{"colour", "entry.bogus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Deps{
				Getenv:  func(string) string { return "/cfg" },
				HomeDir: func() (string, error) { return "/h", nil },
				ReadFile: func(string) ([]byte, error) {
					if c.rerr != nil {
						return nil, c.rerr
					}
					return []byte(c.data), nil
				},
			}
			p, path, found, unknown, err := Load(d)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.HasPrefix(err.Error(), path) {
					t.Fatalf("err = %v, want prefix path and %q", err, c.wantErr)
				}
				return
			}
			if err != nil || found != c.found {
				t.Fatalf("found=%v err=%v", found, err)
			}
			if c.wantEntry && (len(p.Entries) != 1 || p.Entries[0].Strength != c.strength) {
				t.Fatalf("entries = %+v", p.Entries)
			}
			if !reflect.DeepEqual(unknown, c.unknown) {
				t.Fatalf("unknown = %v, want %v", unknown, c.unknown)
			}
		})
	}
}

// TestValidate: one failing profile per rule id plus a passing one. The secret
// rows assert the message never holds the offending text, even when the topic
// itself is the secret; that is the whole point of the check.
func TestValidate(t *testing.T) {
	tok := "gh" + "p_" + strings.Repeat("a1B2", 9)
	ok := Entry{Topic: "database", Value: "PostgreSQL", Strength: StrengthRule}
	with := func(f func(*Entry)) Profile {
		e := ok
		f(&e)
		return Profile{Entries: []Entry{e}}
	}
	cases := []struct {
		name    string
		p       Profile
		unknown []string
		rule    string // "" means no problems
		level   string
	}{
		{"valid", Profile{Entries: []Entry{ok, {Topic: "ci", Value: NoPreference, Strength: StrengthDefault, Kind: "web-app"}}, RuleFiles: []string{"~/a.md", "/b.md"}}, nil, "", ""},
		{"missing topic", with(func(e *Entry) { e.Topic = "" }), nil, "missing-topic", "error"},
		{"missing value", with(func(e *Entry) { e.Value = "" }), nil, "missing-value", "error"},
		{"bad strength", with(func(e *Entry) { e.Strength = "must" }), nil, "bad-strength", "error"},
		{"bad kind", with(func(e *Entry) { e.Kind = "Web App" }), nil, "bad-kind", "error"},
		{"duplicate", Profile{Entries: []Entry{ok, {Topic: " Database ", Value: "x", Strength: StrengthRule}}}, nil, "duplicate-entry", "error"},
		{"same topic other kind ok", Profile{Entries: []Entry{ok, {Topic: "database", Value: "x", Strength: StrengthRule, Kind: "cli"}}}, nil, "", ""},
		{"secret value", with(func(e *Entry) { e.Value = tok }), nil, "secret", "error"},
		{"secret reason", with(func(e *Entry) { e.Reason = tok }), nil, "secret", "error"},
		{"secret topic", with(func(e *Entry) { e.Topic = tok }), nil, "secret", "error"},
		{"secret rule file", Profile{RuleFiles: []string{tok}}, nil, "secret", "error"},
		{"bad rule file", Profile{RuleFiles: []string{"relative.md"}}, nil, "bad-rule-file", "error"},
		{"empty rule file", Profile{RuleFiles: []string{""}}, nil, "bad-rule-file", "error"},
		{"unknown key", Profile{}, []string{"colour"}, "unknown-key", "warning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probs := Validate(c.p, c.unknown)
			if c.rule == "" {
				if len(probs) != 0 {
					t.Fatalf("want none, got %+v", probs)
				}
				return
			}
			found := false
			for _, pr := range probs {
				if pr.Rule == c.rule && pr.Level == c.level {
					found = true
				}
				if strings.Contains(pr.Message, tok) || strings.Contains(pr.Topic, tok) {
					t.Errorf("problem leaks the secret: %+v", pr)
				}
				if pr.Message == "" {
					t.Errorf("empty message: %+v", pr)
				}
			}
			if !found {
				t.Fatalf("no %s %s in %+v", c.level, c.rule, probs)
			}
		})
	}
}

func TestFind(t *testing.T) {
	p := Profile{Entries: []Entry{{Topic: "DB", Kind: ""}, {Topic: "db", Kind: "cli"}, {Topic: "db", Kind: ""}}}
	if i, ok := Find(p, " db ", ""); !ok || i != 2 {
		t.Errorf("general: %d %v", i, ok)
	}
	if i, ok := Find(p, "db", "cli"); !ok || i != 1 {
		t.Errorf("cli: %d %v", i, ok)
	}
	if _, ok := Find(p, "db", "web"); ok {
		t.Error("web should not match")
	}
}

func TestTopicKeyKindExpand(t *testing.T) {
	if got := TopicKey("  Hosting \t and  Deploy "); got != "hosting and deploy" {
		t.Errorf("TopicKey = %q", got)
	}
	for k, want := range map[string]bool{"cli": true, "web-app": true, "3d": true, "": false, "-x": false, "Web": false, "a b": false} {
		if ValidKind(k) != want {
			t.Errorf("ValidKind(%q) != %v", k, want)
		}
	}
	if got := ExpandHome("~/x/y", "/h"); got != "/h/x/y" {
		t.Errorf("ExpandHome = %q", got)
	}
	if got := ExpandHome("/a/~/b", "/h"); got != "/a/~/b" {
		t.Errorf("ExpandHome = %q", got)
	}
}
