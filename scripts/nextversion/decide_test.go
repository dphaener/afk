package main

import "testing"

// TestDecide covers every rule of the release decision.
//
// Load-bearing rows: "no release yet with only scripts and .github" (the
// first release ignores the binary check, so a repo whose first push is only
// tooling still gets 0.1.0); "plugins file with a docs commit" (plugins are
// embedded in the binary, so the commit type cannot hide a change that ships);
// "only scripts go file" (the release tool's own source is not the binary);
// "merge commit body BREAKING CHANGE" (a merge commit's text is not a change,
// the commits it merged are read on their own); "feat among fixes" (the
// highest bump in the range wins, not the newest commit's).
func TestDecide(t *testing.T) {
	goFile := []string{"internal/x/x.go"}
	tests := []struct {
		name     string
		last     string
		hasLast  bool
		messages []string
		paths    []string
		want     string
	}{
		{"only docs changed", "v1.4.2", true, []string{"docs: Fix a typo"}, []string{"docs/a.md", "README.md"}, "skip"},
		{"go file with fix", "v1.4.2", true, []string{"fix: Handle nil"}, goFile, "1.4.3"},
		{"feat among fixes", "v1.4.2", true, []string{"fix: A", "feat: B", "fix: C"}, goFile, "1.5.0"},
		{"feat bang", "v1.4.2", true, []string{"feat!: Drop the flag"}, goFile, "2.0.0"},
		{"feat scope bang", "v1.4.2", true, []string{"feat(x)!: Drop the flag"}, goFile, "2.0.0"},
		{"breaking change body", "v1.4.2", true, []string{"fix: A\n\nSome text\nBREAKING CHANGE: gone"}, goFile, "2.0.0"},
		{"breaking-change hyphen body", "v1.4.2", true, []string{"fix: A\n\nBREAKING-CHANGE: gone"}, goFile, "2.0.0"},
		{"no release yet", "", false, []string{"fix: A"}, goFile, "0.1.0"},
		{"only merges plus chore", "v1.4.2", true, []string{"Merge pull request #5 from a/b", "Merge branch 'x'", "chore: Tidy"}, goFile, "1.4.3"},
		{"only merges", "v1.4.2", true, []string{"Merge pull request #5 from a/b"}, goFile, "1.4.3"},
		{"merge remote-tracking only", "v1.4.2", true, []string{"Merge remote-tracking branch 'o/main'"}, goFile, "1.4.3"},
		{"subject with no type", "v1.4.2", true, []string{"Update stuff"}, goFile, "1.4.3"},
		{"plugins file with docs commit", "v1.4.2", true, []string{"docs: Reword a prompt"}, []string{"plugins/afk/skills/x.md"}, "1.4.3"},
		{"only scripts go file", "v1.4.2", true, []string{"feat: Tool"}, []string{"scripts/nextversion/decide.go"}, "skip"},
		{"no release yet with only scripts and .github", "", false, []string{"ci: Add"}, []string{"scripts/a.go", ".github/workflows/r.yml"}, "0.1.0"},
		{"go.mod only", "v1.4.2", true, []string{"chore: Bump"}, []string{"go.mod"}, "1.4.3"},
		{"go.sum only", "v1.4.2", true, []string{"chore: Bump"}, []string{"go.sum"}, "1.4.3"},
		{"claude-plugin only", "v1.4.2", true, []string{"docs: Edit"}, []string{".claude-plugin/marketplace.json"}, "1.4.3"},
		{"agents folder only", "v1.4.2", true, []string{"docs: Edit"}, []string{".agents/plugins/marketplace.json"}, "1.4.3"},
		{"contrib file", "v1.4.2", true, []string{"docs: Edit"}, []string{"contrib/afkd.service"}, "1.4.3"},
		{"root testdata go file only", "v1.4.2", true, []string{"feat: Fixture"}, []string{"testdata/x.go"}, "skip"},
		{"root go file counts", "v1.4.2", true, []string{"fix: A"}, []string{"main.go"}, "1.4.3"},
		{"nested testdata go file counts", "v1.4.2", true, []string{"fix: A"}, []string{"internal/x/testdata/y.go"}, "1.4.3"},
		{"non-go root files skip", "v1.4.2", true, []string{"feat: A"}, []string{"install.sh", "Makefile", "CONTEXT.md"}, "skip"},
		{"merge body breaking is ignored", "v1.4.2", true, []string{"Merge pull request #5 from a/b\n\nBREAKING CHANGE: x", "fix: A"}, goFile, "1.4.3"},
		{"merge subject bang is ignored", "v1.4.2", true, []string{"Merge branch 'a'\n\nBREAKING CHANGE: x"}, goFile, "1.4.3"},
		{"uppercase Feat", "v1.4.2", true, []string{"Feat: Add"}, goFile, "1.5.0"},
		{"major zeroes minor and patch", "v1.4.2", true, []string{"feat!: A"}, goFile, "2.0.0"},
		{"minor zeroes patch", "v1.4.2", true, []string{"feat: A"}, goFile, "1.5.0"},
		{"feat word without colon is patch", "v1.4.2", true, []string{"feature: A", "feat A"}, goFile, "1.4.3"},
		{"no paths skips", "v1.4.2", true, []string{"feat: A"}, nil, "skip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.last, tt.hasLast, tt.messages, tt.paths); got != tt.want {
				t.Errorf("decide() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseVersion pins the strict tag form. The leading-zero row is load
// bearing: git sorts "v01.2.3" and "v1.2.3" apart as strings, so accepting it
// would let a stray tag outrank a real one. We reject it.
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want version
		ok   bool
	}{
		{"valid", "v1.2.3", version{1, 2, 3}, true},
		{"zero", "v0.1.0", version{0, 1, 0}, true},
		{"multi digit", "v10.20.30", version{10, 20, 30}, true},
		{"no v", "1.2.3", version{}, false},
		{"two parts", "v1.2", version{}, false},
		{"four parts", "v1.2.3.4", version{}, false},
		{"prerelease", "v1.2.3-rc1", version{}, false},
		{"leading zero", "v01.2.3", version{}, false},
		{"empty", "", version{}, false},
		{"just v", "v", version{}, false},
		{"non digit", "v1.x.3", version{}, false},
		{"negative", "v1.-2.3", version{}, false},
		{"trailing newline", "v1.2.3\n", version{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseVersion(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseVersion(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
