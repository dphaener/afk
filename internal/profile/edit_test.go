package profile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// templateTopics are the fourteen suggested topics, in the order the template
// lists them.
var templateTopics = []string{
	"project location", "language and framework", "database", "auth", "tests",
	"interface components", "CI", "hosting and deploy", "storage", "email",
	"domain names", "notifications", "design tool", "accounts",
}

// exampleLine matches a commented-out line of TOML in the template, as opposed
// to a line of prose.
var exampleLine = regexp.MustCompile(`(?m)^# (\[\[entry\]\]|(topic|value|strength|kind|reason|rule_files) = )`)

// TestTemplate: "fresh template is an empty profile" is load-bearing, because
// `afk profile init` followed by `afk profile` must not show examples as the
// owner's choices. "every example switched on" is load-bearing too: it proves
// the examples are valid entries that pass the secret check, so an owner who
// removes the "# " gets a working profile and not a list of problems.
func TestTemplate(t *testing.T) {
	tpl := Template()
	uncommented := exampleLine.ReplaceAllString(tpl, "$1")

	t.Run("fresh template is an empty profile", func(t *testing.T) {
		p, unknown, err := Parse([]byte(tpl))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(p.Entries) != 0 || len(p.RuleFiles) != 0 || len(unknown) != 0 {
			t.Fatalf("want an empty profile, got %+v, unknown %v", p, unknown)
		}
		if probs := Validate(p, unknown); len(probs) != 0 {
			t.Fatalf("Validate: %+v", probs)
		}
	})
	t.Run("every line is a comment or blank", func(t *testing.T) {
		for i, l := range strings.Split(tpl, "\n") {
			if l != "" && !strings.HasPrefix(l, "#") {
				t.Fatalf("line %d is live: %q", i+1, l)
			}
		}
	})
	t.Run("names each topic in order", func(t *testing.T) {
		at := 0
		for _, topic := range templateTopics {
			i := strings.Index(tpl[at:], `# topic = "`+topic+`"`)
			if i < 0 {
				t.Fatalf("topic %q is missing or out of order", topic)
			}
			at += i
		}
	})
	t.Run("every example switched on", func(t *testing.T) {
		p, unknown, err := Parse([]byte(uncommented))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(unknown) != 0 {
			t.Fatalf("unknown keys: %v", unknown)
		}
		if probs := Validate(p, unknown); len(probs) != 0 {
			t.Fatalf("Validate: %+v", probs)
		}
		var got []string
		for _, e := range p.Entries {
			got = append(got, e.Topic)
		}
		if !reflect.DeepEqual(got, templateTopics) {
			t.Fatalf("topics\n got %v\nwant %v", got, templateTopics)
		}
		if len(p.RuleFiles) != 1 {
			t.Fatalf("want one example rule file, got %v", p.RuleFiles)
		}
	})
	t.Run("says the things an owner must know", func(t *testing.T) {
		for _, want := range []string{
			`"rule"`, `"default"`, "kind", `"web-app"`, `"cli"`, `"library"`,
			`"no preference"`, "keys, tokens or passwords", "read, not copied",
			"works as a\n# password",
		} {
			if !strings.Contains(tpl, want) {
				t.Errorf("template does not say %q", want)
			}
		}
	})
	t.Run("set works on a fresh template", func(t *testing.T) {
		got, replaced, err := Set(tpl, Entry{Topic: "database", Value: "PostgreSQL"})
		if err != nil || replaced {
			t.Fatalf("Set: replaced %v, err %v", replaced, err)
		}
		if !strings.HasPrefix(got, tpl) {
			t.Fatal("the template text was changed")
		}
	})
}

// setBase is a profile with everything Set must leave alone: a file comment,
// rule files, an unknown key, comments between and after blocks, a header
// with a trailing comment, and a general and a kind-specific entry on one
// topic.
const setBase = `# my profile
rule_files = ["~/rules.md"]
colour = "blue" # a key afk does not know

# the database
[[entry]]
topic = "database"
value = "PostgreSQL"
strength = "rule"
reason = "house rule"
# said after the database

# about CI
[[entry]] # the middle one
topic = "CI"
value = "an example CI service"

[[entry]]
topic = "database"
value = "SQLite"
kind = "cli"

# the end
`

// swap returns text with one block of lines exchanged, failing the test when
// the old block is not there exactly once. Rows use it so the expected text
// is "the input with these lines changed and nothing else".
func swap(t *testing.T, text, old, new string) string {
	t.Helper()
	if strings.Count(text, old) != 1 {
		t.Fatalf("test set-up: %q appears %d times", old, strings.Count(text, old))
	}
	return strings.Replace(text, old, new, 1)
}

// TestSet: the rows the master plan asks for are "empty text" (a missing
// file), "new topic appended" and "existing topic replaced". Load-bearing
// beyond those: every "replaced" row compares the whole text, so a comment,
// an unknown key or a neighbouring entry that moved or vanished fails the
// row; "same topic, another kind" and "kind-specific set, general untouched"
// pin that the kind is part of an entry's identity; and the refusal rows pin
// that Set returns the original text with its error, which is what lets the
// command leave the file alone. "header line inside a multi-line string" is
// the reason the guard exists: without the re-parse, that row would write a
// broken file.
func TestSet(t *testing.T) {
	const trap = "[[entry]]\ntopic = \"notes\"\nvalue = \"\"\"\n[[entry]]\ntopic = \"database\"\n\"\"\"\n\n[[entry]]\ntopic = \"database\"\nvalue = \"pg\"\n"
	longHex := strings.Repeat("a1b2c3d4", 4)

	cases := []struct {
		name     string
		text     string
		e        Entry
		want     func(t *testing.T) string // nil when an error is expected
		replaced bool
		errHas   string
	}{
		{
			name: "empty text gets a header and the entry",
			text: "",
			e:    Entry{Topic: "database", Value: "PostgreSQL", Strength: StrengthRule, Reason: "house rule"},
			want: func(t *testing.T) string {
				return setHeader + "\n[[entry]]\ntopic = \"database\"\nvalue = \"PostgreSQL\"\nstrength = \"rule\"\nreason = \"house rule\"\n"
			},
		},
		{
			name: "blank text is treated as empty",
			text: "\n  \n",
			e:    Entry{Topic: "auth", Value: NoPreference},
			want: func(t *testing.T) string {
				return setHeader + "\n[[entry]]\ntopic = \"auth\"\nvalue = \"no preference\"\nstrength = \"default\"\n"
			},
		},
		{
			name: "new topic appended to a file with comments",
			text: setBase,
			e:    Entry{Topic: "email", Value: "sent from mail.example.com", Strength: StrengthDefault},
			want: func(t *testing.T) string {
				return setBase + "\n[[entry]]\ntopic = \"email\"\nvalue = \"sent from mail.example.com\"\nstrength = \"default\"\n"
			},
		},
		{
			name: "appended after extra blank lines leaves one",
			text: "# only a comment\n\n\n\n",
			e:    Entry{Topic: "auth", Value: "x"},
			want: func(t *testing.T) string {
				return "# only a comment\n\n[[entry]]\ntopic = \"auth\"\nvalue = \"x\"\nstrength = \"default\"\n"
			},
		},
		{
			name: "appended to a file with no final newline",
			text: "[[entry]]\ntopic = \"a\"\nvalue = \"1\"",
			e:    Entry{Topic: "b", Value: "2"},
			want: func(t *testing.T) string {
				return "[[entry]]\ntopic = \"a\"\nvalue = \"1\"\n\n[[entry]]\ntopic = \"b\"\nvalue = \"2\"\nstrength = \"default\"\n"
			},
		},
		{
			name: "existing topic replaced, first block",
			text: setBase,
			e:    Entry{Topic: "database", Value: "MariaDB", Strength: StrengthRule, Reason: "house rule"},
			want: func(t *testing.T) string {
				return swap(t, setBase,
					"[[entry]]\ntopic = \"database\"\nvalue = \"PostgreSQL\"\nstrength = \"rule\"\nreason = \"house rule\"\n",
					"[[entry]]\ntopic = \"database\"\nvalue = \"MariaDB\"\nstrength = \"rule\"\nreason = \"house rule\"\n")
			},
			replaced: true,
		},
		{
			name: "replaced block keeps the comment after its last key",
			text: setBase,
			e:    Entry{Topic: "database", Value: "MariaDB", Strength: StrengthDefault},
			want: func(t *testing.T) string {
				return swap(t, setBase,
					"value = \"PostgreSQL\"\nstrength = \"rule\"\nreason = \"house rule\"\n# said after the database\n",
					"value = \"MariaDB\"\nstrength = \"default\"\n# said after the database\n")
			},
			replaced: true,
		},
		{
			name: "middle block replaced",
			text: setBase,
			e:    Entry{Topic: "CI", Value: "another example service", Strength: StrengthDefault},
			want: func(t *testing.T) string {
				return swap(t, setBase,
					"[[entry]] # the middle one\ntopic = \"CI\"\nvalue = \"an example CI service\"\n",
					"[[entry]]\ntopic = \"CI\"\nvalue = \"another example service\"\nstrength = \"default\"\n")
			},
			replaced: true,
		},
		{
			name: "last block replaced, kind-specific set, general untouched",
			text: setBase,
			e:    Entry{Topic: "database", Value: "DuckDB", Strength: StrengthDefault, Kind: "cli"},
			want: func(t *testing.T) string {
				return swap(t, setBase,
					"[[entry]]\ntopic = \"database\"\nvalue = \"SQLite\"\nkind = \"cli\"\n",
					"[[entry]]\ntopic = \"database\"\nvalue = \"DuckDB\"\nstrength = \"default\"\nkind = \"cli\"\n")
			},
			replaced: true,
		},
		{
			name: "same topic, another kind is appended",
			text: setBase,
			e:    Entry{Topic: "database", Value: "none", Strength: StrengthDefault, Kind: "library"},
			want: func(t *testing.T) string {
				return setBase + "\n[[entry]]\ntopic = \"database\"\nvalue = \"none\"\nstrength = \"default\"\nkind = \"library\"\n"
			},
		},
		{
			name: "topic matches whatever its case and spacing",
			text: "[[entry]]\ntopic = \"Hosting  and Deploy\"\nvalue = \"a\"\n",
			e:    Entry{Topic: "hosting and deploy", Value: "b", Strength: StrengthDefault},
			want: func(t *testing.T) string {
				return "[[entry]]\ntopic = \"hosting and deploy\"\nvalue = \"b\"\nstrength = \"default\"\n"
			},
			replaced: true,
		},
		{
			name: "last block of a file with no final newline",
			text: "[[entry]]\ntopic = \"a\"\nvalue = \"1\"",
			e:    Entry{Topic: "a", Value: "2", Strength: StrengthRule},
			want: func(t *testing.T) string {
				return "[[entry]]\ntopic = \"a\"\nvalue = \"2\"\nstrength = \"rule\""
			},
			replaced: true,
		},
		{
			name: "windows line endings are kept",
			text: "# c\r\n[[entry]]\r\ntopic = \"a\"\r\nvalue = \"1\"\r\n",
			e:    Entry{Topic: "a", Value: "2", Strength: StrengthRule},
			want: func(t *testing.T) string {
				return "# c\r\n[[entry]]\r\ntopic = \"a\"\r\nvalue = \"2\"\r\nstrength = \"rule\"\r\n"
			},
			replaced: true,
		},
		{
			name: "a quote, a backslash and a tab round-trip",
			text: "",
			e:    Entry{Topic: "storage", Value: "say \"hi\" in C:\\files\tnow", Strength: StrengthDefault, Reason: "a \\ and a \""},
			want: func(t *testing.T) string {
				return setHeader + "\n[[entry]]\ntopic = \"storage\"\nvalue = \"say \\\"hi\\\" in C:\\\\files\\tnow\"\nstrength = \"default\"\nreason = \"a \\\\ and a \\\"\"\n"
			},
		},
		{
			name:   "a value with a newline is refused",
			text:   setBase,
			e:      Entry{Topic: "database", Value: "one\ntwo", Strength: StrengthRule},
			errHas: "line break",
		},
		{
			name:   "a reason with a carriage return is refused",
			text:   setBase,
			e:      Entry{Topic: "database", Value: "x", Reason: "one\rtwo"},
			errHas: "line break",
		},
		{
			name:   "a newline is refused before the file is read",
			text:   "this is = = not toml",
			e:      Entry{Topic: "database", Value: "one\ntwo"},
			errHas: "line break",
		},
		{
			name:   "an invalid file is refused",
			text:   "[[entry]]\ntopic = \"database\nvalue = 3\n",
			e:      Entry{Topic: "database", Value: "x"},
			errHas: "fix it first",
		},
		{
			name:   "header line inside a multi-line string is refused",
			text:   trap,
			e:      Entry{Topic: "database", Value: "MariaDB", Strength: StrengthDefault},
			errHas: "could not edit the file safely",
		},
		{
			name:   "a block with a key afk does not know is refused",
			text:   "[[entry]]\ntopic = \"database\"\nvalue = \"pg\"\nfuture = \"kept by hand\"\n",
			e:      Entry{Topic: "database", Value: "MariaDB"},
			errHas: "could not edit the file safely",
		},
		{
			name:   "entries written as an inline array are refused",
			text:   "entry = [{topic = \"database\", value = \"pg\"}]\n",
			e:      Entry{Topic: "database", Value: "MariaDB"},
			errHas: "could not edit the file safely",
		},
		{
			name:   "a value that looks like a key is refused",
			text:   setBase,
			e:      Entry{Topic: "deploy key", Value: longHex},
			errHas: "looks like a key or token",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, replaced, err := Set(c.text, c.e)
			if c.want == nil {
				if err == nil {
					t.Fatalf("want an error holding %q, got text:\n%s", c.errHas, got)
				}
				if !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("error %q does not hold %q", err, c.errHas)
				}
				if strings.Contains(err.Error(), longHex) {
					t.Fatal("the error repeats the refused value")
				}
				if got != c.text || replaced {
					t.Fatalf("a refused edit must return the original text and false, got replaced %v and:\n%s", replaced, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set: %v", err)
			}
			if want := c.want(t); got != want {
				t.Fatalf("text\n--- got\n%s\n--- want\n%s", got, want)
			}
			if replaced != c.replaced {
				t.Fatalf("replaced = %v, want %v", replaced, c.replaced)
			}
			// Whatever the row, the entry must read back as given.
			p, _, perr := Parse([]byte(got))
			if perr != nil {
				t.Fatalf("result does not parse: %v", perr)
			}
			i, ok := Find(p, c.e.Topic, c.e.Kind)
			wantE := c.e
			if wantE.Strength == "" {
				wantE.Strength = StrengthDefault
			}
			if !ok || p.Entries[i] != wantE {
				t.Fatalf("entry reads back as %+v (found %v), want %+v", p.Entries, ok, wantE)
			}
		})
	}
}

// TestSetKeepsEveryOtherLine checks the promise in the plainest way: after an
// append or a replace, every line of the original that was not part of the
// replaced block is still there, in the same order.
func TestSetKeepsEveryOtherLine(t *testing.T) {
	cases := []struct {
		name    string
		e       Entry
		dropped []string // original lines the edit is allowed to remove
	}{
		{"append", Entry{Topic: "email", Value: "x"}, nil},
		{"replace first", Entry{Topic: "database", Value: "x"}, []string{
			`[[entry]]`, `topic = "database"`, `value = "PostgreSQL"`, `strength = "rule"`, `reason = "house rule"`}},
		{"replace middle", Entry{Topic: "CI", Value: "x"}, []string{
			`[[entry]] # the middle one`, `topic = "CI"`, `value = "an example CI service"`}},
		{"replace last", Entry{Topic: "database", Value: "x", Kind: "cli"}, []string{
			`[[entry]]`, `topic = "database"`, `value = "SQLite"`, `kind = "cli"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := Set(setBase, c.e)
			if err != nil {
				t.Fatalf("Set: %v", err)
			}
			gotLines := strings.Split(got, "\n")
			at, dropped := 0, 0
			for _, l := range strings.Split(setBase, "\n") {
				found := -1
				for j := at; j < len(gotLines); j++ {
					if gotLines[j] == l {
						found = j
						break
					}
				}
				if found < 0 {
					dropped++
					if dropped > len(c.dropped) {
						t.Fatalf("line %q was lost", l)
					}
					continue
				}
				at = found + 1
			}
		})
	}
}

// TestWrite: "existing 0644 file keeps 0644" is load-bearing because the temp
// file is born 0600 and only the explicit chmod keeps the owner's mode; and
// the symlink rows are load-bearing because a plain rename over the link
// would turn a dotfiles link into a private copy without a word. Every row
// also checks that the folder holds nothing but the expected names, so a temp
// file left behind fails it.
func TestWrite(t *testing.T) {
	const text = "# new text\n"
	cases := []struct {
		name string
		// setup returns the path to write, the file that must hold the text
		// afterwards, the mode it must have and the names expected in dir.
		setup func(t *testing.T, dir string) (path, holds string, mode fs.FileMode, names []string)
		link  bool
	}{
		{
			name: "new file in a missing folder",
			setup: func(t *testing.T, dir string) (string, string, fs.FileMode, []string) {
				p := filepath.Join(dir, "afk", FileName)
				return p, p, 0o600, []string{"afk"}
			},
		},
		{
			name: "existing 0644 file keeps 0644",
			setup: func(t *testing.T, dir string) (string, string, fs.FileMode, []string) {
				p := filepath.Join(dir, FileName)
				mustWrite(t, p, "old\n", 0o644)
				return p, p, 0o644, []string{FileName}
			},
		},
		{
			name: "existing 0600 file keeps 0600",
			setup: func(t *testing.T, dir string) (string, string, fs.FileMode, []string) {
				p := filepath.Join(dir, FileName)
				mustWrite(t, p, "old\n", 0o600)
				return p, p, 0o600, []string{FileName}
			},
		},
		{
			name: "a symlink stays a symlink and its target is written",
			link: true,
			setup: func(t *testing.T, dir string) (string, string, fs.FileMode, []string) {
				target := filepath.Join(dir, "dotfiles.toml")
				mustWrite(t, target, "old\n", 0o644)
				p := filepath.Join(dir, FileName)
				// A relative link, as a dotfiles tool would make.
				if err := os.Symlink("dotfiles.toml", p); err != nil {
					t.Fatal(err)
				}
				return p, target, 0o644, []string{"dotfiles.toml", FileName}
			},
		},
		{
			name: "a symlink to a file that does not exist yet",
			link: true,
			setup: func(t *testing.T, dir string) (string, string, fs.FileMode, []string) {
				target := filepath.Join(dir, "later.toml")
				p := filepath.Join(dir, FileName)
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
				return p, target, 0o600, []string{"later.toml", FileName}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path, holds, mode, names := c.setup(t, dir)
			if err := Write(path, []byte(text)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := os.ReadFile(holds)
			if err != nil || string(got) != text {
				t.Fatalf("file holds %q, err %v", got, err)
			}
			fi, err := os.Stat(holds)
			if err != nil || fi.Mode().Perm() != mode {
				t.Fatalf("mode %v, want %v (err %v)", fi.Mode().Perm(), mode, err)
			}
			li, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if isLink := li.Mode()&fs.ModeSymlink != 0; isLink != c.link {
				t.Fatalf("path is a symlink: %v, want %v", isLink, c.link)
			}
			assertNames(t, dir, names)
			// The temp file is made beside the file written, which is not
			// always dir itself, so look there too.
			for _, n := range namesIn(t, filepath.Dir(holds), "") {
				if strings.Contains(n, ".tmp-") {
					t.Fatalf("temp file left behind: %s", n)
				}
			}
		})
	}

	t.Run("new folder is private", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "afk", FileName)
		if err := Write(p, []byte(text)); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Dir(p))
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("folder mode %v, err %v", fi.Mode().Perm(), err)
		}
	})
	t.Run("a failed write leaves no temp file and the old state", func(t *testing.T) {
		dir := t.TempDir()
		// A folder where the file should be: the rename cannot succeed.
		p := filepath.Join(dir, FileName)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := Write(p, []byte(text)); err == nil {
			t.Fatal("want an error")
		}
		assertNames(t, dir, []string{FileName})
	})
	t.Run("a loop of links is an error", func(t *testing.T) {
		dir := t.TempDir()
		a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
		if err := os.Symlink(b, a); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(a, b); err != nil {
			t.Fatal(err)
		}
		if err := Write(a, []byte(text)); err == nil {
			t.Fatal("want an error")
		}
		assertNames(t, dir, []string{"a", "b"})
	})
}

// TestCreate: "existing file refused and unchanged" is the row `afk profile
// init` depends on, since init must never overwrite a profile. The dangling
// link row pins that "anything at the path" includes a link with nothing
// behind it, where a stat-based check would see no file and write through.
func TestCreate(t *testing.T) {
	const text = "# template\n"
	t.Run("missing file created", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "afk", FileName)
		if err := Create(p, []byte(text)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := os.ReadFile(p)
		if err != nil || string(got) != text {
			t.Fatalf("file holds %q, err %v", got, err)
		}
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(p))
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("folder mode %v, want 0700", di.Mode().Perm())
		}
	})
	t.Run("existing file refused and unchanged", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		mustWrite(t, p, "mine\n", 0o644)
		err := Create(p, []byte(text))
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("want fs.ErrExist, got %v", err)
		}
		got, _ := os.ReadFile(p)
		fi, _ := os.Stat(p)
		if string(got) != "mine\n" || fi.Mode().Perm() != 0o644 {
			t.Fatalf("file changed: %q, mode %v", got, fi.Mode().Perm())
		}
		assertNames(t, dir, []string{FileName})
	})
	t.Run("existing empty file refused", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		mustWrite(t, p, "", 0o600)
		if err := Create(p, []byte(text)); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("want fs.ErrExist, got %v", err)
		}
		if got, _ := os.ReadFile(p); len(got) != 0 {
			t.Fatalf("file changed: %q", got)
		}
	})
	t.Run("dangling symlink refused and unchanged", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		target := filepath.Join(dir, "nowhere.toml")
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if err := Create(p, []byte(text)); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("want fs.ErrExist, got %v", err)
		}
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the link's target was created (err %v)", err)
		}
		assertNames(t, dir, []string{FileName})
	})
}

func mustWrite(t *testing.T, path, text string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is cut down by the umask; set it for certain.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// assertNames fails unless dir holds exactly these names.
func assertNames(t *testing.T, dir string, want []string) {
	t.Helper()
	got := namesIn(t, dir, "")
	w := append([]string(nil), want...)
	slices.Sort(got)
	slices.Sort(w)
	if !reflect.DeepEqual(got, w) {
		t.Fatalf("%s holds %v, want %v", dir, got, w)
	}
}

// namesIn lists dir, leaving out names that hold skip (when skip is set).
func namesIn(t *testing.T, dir, skip string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, de := range des {
		if skip != "" && strings.Contains(de.Name(), skip) {
			continue
		}
		out = append(out, de.Name())
	}
	return out
}
