package provider

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestInstructionFiles pins the list `afk plan approve` commits.
//
// Load-bearing checks: the exact list (the two names are what the planning
// session writes, see plugins/afk/skills/master-plan/prompts/phases-late.md,
// and a name missing here is a file that never reaches a task's worktree);
// "every name is a plain file name" (the caller joins each to the project
// root and hands it to git, so a path that climbs out of the project must be
// impossible); and "every pointer file belongs to a known provider" (an entry
// for a name Lookup refuses would be dead and never listed).
func TestInstructionFiles(t *testing.T) {
	got := InstructionFiles()
	want := []string{"AGENTS.md", "CLAUDE.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstructionFiles() = %v, want %v", got, want)
	}

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{"the generic file comes first", func(t *testing.T) {
			if got[0] != genericInstructionsFile {
				t.Errorf("first file = %q, want %q", got[0], genericInstructionsFile)
			}
		}},
		{"every name is a plain file name", func(t *testing.T) {
			for _, f := range got {
				if f == "" || filepath.Base(f) != f || f == "." || f == ".." {
					t.Errorf("%q is not a plain file name", f)
				}
			}
		}},
		{"no name is listed twice", func(t *testing.T) {
			seen := map[string]bool{}
			for _, f := range got {
				if seen[f] {
					t.Errorf("%q is listed twice", f)
				}
				seen[f] = true
			}
		}},
		{"every pointer file belongs to a known provider", func(t *testing.T) {
			for name := range pointerFiles {
				if _, err := Lookup(name); err != nil {
					t.Errorf("pointerFiles names %q: %v", name, err)
				}
			}
		}},
		{"the caller cannot change the list", func(t *testing.T) {
			a := InstructionFiles()
			a[0] = "changed"
			if b := InstructionFiles(); b[0] != genericInstructionsFile {
				t.Errorf("a second call returned %q first; the list is shared", b[0])
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.check)
	}
}
