package daemonconfig

import (
	"reflect"
	"strings"
	"testing"
)

// loadText runs text through Load, the reader the daemon uses, so every edit
// is checked against what the daemon would see.
func loadText(t *testing.T, text string) []Project {
	t.Helper()
	cfg, err := Load(deps(nil, map[string]string{home: text}))
	if err != nil {
		t.Fatalf("Load of the edited text: %v\n%s", err, text)
	}
	return cfg.Projects
}

const withWeb = `# my machine
provider = "claude"

[[project]]
path = "/a/one"

# the page
[web]
token = "0123456789abcdef"
`

const three = `[[project]]
path = "/a/one"

[[project]]
path = "/a/two"
name = "second"

# web follows
[[project]]
path = "/a/three"
`

// TestAddProject: load-bearing rows are "empty file" (a first project on a
// fresh machine), "comments and web after projects" (the new block goes at
// the end and everything before is byte-for-byte), "already present by path"
// (approve is repeatable, so a second add must change nothing), "name clash"
// (Load would refuse the file, so the edit must refuse first and keep the
// original text) and "name kept when it differs". Every success row also
// loads through Load.
func TestAddProject(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		p         Project
		wantAdded bool
		wantErr   string
		wantText  string // exact, when set
		wantProjs []Project
	}{
		{"empty file", "", Project{Path: "/x/app"}, true, "", "[[project]]\npath = \"/x/app\"\n",
			[]Project{{"app", "/x/app"}}},
		{"comments and web after projects", withWeb, Project{Path: "/x/app"}, true, "",
			withWeb + "\n[[project]]\npath = \"/x/app\"\n",
			[]Project{{"one", "/a/one"}, {"app", "/x/app"}}},
		{"name kept when it differs", "", Project{Name: "shop", Path: "/x/app"}, true, "",
			"[[project]]\npath = \"/x/app\"\nname = \"shop\"\n", []Project{{"shop", "/x/app"}}},
		{"name equal to folder dropped", "", Project{Name: "app", Path: "/x/app/"}, true, "",
			"[[project]]\npath = \"/x/app\"\n", []Project{{"app", "/x/app"}}},
		{"tilde kept as written", "", Project{Path: "~/code/app"}, true, "",
			"[[project]]\npath = \"~/code/app\"\n", []Project{{"app", "/home/u/code/app"}}},
		{"no final newline", "provider = \"x\"", Project{Path: "/x/app"}, true, "",
			"provider = \"x\"\n\n[[project]]\npath = \"/x/app\"\n",
			[]Project{{"app", "/x/app"}}},
		{"already present by path", withWeb, Project{Path: "/a/one/"}, false, "", withWeb,
			[]Project{{"one", "/a/one"}}},
		{"name clash", withWeb, Project{Path: "/b/one"}, false, `"one"`, withWeb, nil},
		{"explicit name clash", withWeb, Project{Name: "one", Path: "/b/other"}, false, `"one"`, withWeb, nil},
		{"relative path", "", Project{Path: "rel/app"}, false, "relative", "", nil},
		{"empty path", "", Project{Path: " "}, false, "empty", "", nil},
		{"broken file", "[[project", Project{Path: "/x/app"}, false, "mistake", "[[project", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, added, err := AddProject(c.text, c.p)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				if added || got != c.text {
					t.Errorf("on error got added=%v text changed=%v, want original text", added, got != c.text)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if added != c.wantAdded {
				t.Errorf("added = %v, want %v", added, c.wantAdded)
			}
			if c.wantText != "" && got != c.wantText {
				t.Errorf("text = %q, want %q", got, c.wantText)
			}
			if !added && got != c.text {
				t.Errorf("text changed though nothing was added")
			}
			if c.wantProjs != nil && !reflect.DeepEqual(loadText(t, got), c.wantProjs) {
				t.Errorf("Load gives %v, want %v", loadText(t, got), c.wantProjs)
			}
		})
	}
}

// TestRemoveProject: load-bearing rows are "remove the middle one of three"
// (the block ends at the next header and the comment before the next header
// survives), "no match" (removed false, text identical), "remove the only one
// before web" (the other table and its comment stay byte-for-byte) and the
// two lookups, by name and by path. Every success row also loads through Load.
func TestRemoveProject(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		key         string
		wantRemoved bool
		wantText    string
		wantProjs   []Project
	}{
		{"by name", three, "second", true,
			"[[project]]\npath = \"/a/one\"\n\n# web follows\n[[project]]\npath = \"/a/three\"\n",
			[]Project{{"one", "/a/one"}, {"three", "/a/three"}}},
		{"by path", three, "/a/two/", true,
			"[[project]]\npath = \"/a/one\"\n\n# web follows\n[[project]]\npath = \"/a/three\"\n",
			[]Project{{"one", "/a/one"}, {"three", "/a/three"}}},
		{"by folder name", three, "one", true,
			"[[project]]\npath = \"/a/two\"\nname = \"second\"\n\n# web follows\n[[project]]\npath = \"/a/three\"\n",
			[]Project{{"second", "/a/two"}, {"three", "/a/three"}}},
		{"last of three", three, "three", true,
			"[[project]]\npath = \"/a/one\"\n\n[[project]]\npath = \"/a/two\"\nname = \"second\"\n\n# web follows\n",
			[]Project{{"one", "/a/one"}, {"second", "/a/two"}}},
		{"remove the only one before web", withWeb, "one", true,
			"# my machine\nprovider = \"claude\"\n\n# the page\n[web]\ntoken = \"0123456789abcdef\"\n",
			[]Project{}},
		{"no match", three, "nope", false, three, []Project{{"one", "/a/one"}, {"second", "/a/two"}, {"three", "/a/three"}}},
		{"empty file", "", "x", false, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, removed, err := RemoveProject(c.text, c.key)
			if err != nil {
				t.Fatal(err)
			}
			if removed != c.wantRemoved {
				t.Errorf("removed = %v, want %v", removed, c.wantRemoved)
			}
			if got != c.wantText {
				t.Errorf("text = %q, want %q", got, c.wantText)
			}
			if c.wantProjs != nil {
				if gp := loadText(t, got); len(gp) != len(c.wantProjs) || (len(gp) > 0 && !reflect.DeepEqual(gp, c.wantProjs)) {
					t.Errorf("Load gives %v, want %v", gp, c.wantProjs)
				}
			}
		})
	}

	t.Run("empty key", func(t *testing.T) {
		got, removed, err := RemoveProject(three, " ")
		if err == nil || removed || got != three {
			t.Errorf("got removed=%v err=%v, want an error and the original text", removed, err)
		}
	})
	t.Run("broken file", func(t *testing.T) {
		got, removed, err := RemoveProject("[[project", "x")
		if err == nil || removed || got != "[[project" {
			t.Errorf("got removed=%v err=%v", removed, err)
		}
	})
}

// TestProjectRoundTrip: add then remove gets back the original text for a
// file with comments and a web table, the property that makes `afk project
// add` and `remove` safe to repeat.
func TestProjectRoundTrip(t *testing.T) {
	added, ok, err := AddProject(withWeb, Project{Path: "/x/app"})
	if err != nil || !ok {
		t.Fatalf("add: ok=%v err=%v", ok, err)
	}
	back, removed, err := RemoveProject(added, "app")
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	if back != withWeb {
		t.Errorf("round trip = %q, want %q", back, withWeb)
	}
}
