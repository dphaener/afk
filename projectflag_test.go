package main

import (
	"flag"
	"path/filepath"
	"testing"
)

// TestResolveProject covers the precedence, path expansion, error cases, and
// path normalization for the shared --project flag resolution logic. The
// flag-beats-cwd row and the both-empty error row are load-bearing: W2.T1 and
// W2.T2 depend on that precedence. The "root path", ".. segments", and
// "duplicate separators" rows are load-bearing for W2.T2's dedupe-by-resolved-path
// contract: internal/scan returns cleaned paths, so resolveProject must also
// return fully cleaned paths to match them when deduplicating scan results.
func TestResolveProject(t *testing.T) {
	absHome := "/home/user"
	absCwd := "/home/user/repo"

	tests := []struct {
		name      string
		flagValue string
		cwd       string
		home      string
		want      string
		wantErr   bool
	}{
		{
			name:      "flag set and cwd set -> flag wins",
			flagValue: "/explicit/path",
			cwd:       "/home/user/repo",
			home:      absHome,
			want:      "/explicit/path",
			wantErr:   false,
		},
		{
			name:      "flag empty -> cwd used",
			flagValue: "",
			cwd:       absCwd,
			home:      absHome,
			want:      absCwd,
			wantErr:   false,
		},
		{
			name:      "both empty -> error",
			flagValue: "",
			cwd:       "",
			home:      absHome,
			want:      "",
			wantErr:   true,
		},
		{
			name:      "tilde expansion in flag",
			flagValue: "~/projects/myrepo",
			cwd:       absCwd,
			home:      absHome,
			want:      filepath.Join(absHome, "projects/myrepo"),
			wantErr:   false,
		},
		{
			name:      "relative flag value made absolute",
			flagValue: "projects/myrepo",
			cwd:       "/home/user",
			home:      absHome,
			want:      filepath.Join("/home/user", "projects/myrepo"),
			wantErr:   false,
		},
		{
			name:      "trailing slash normalized",
			flagValue: "/explicit/path/",
			cwd:       absCwd,
			home:      absHome,
			want:      "/explicit/path",
			wantErr:   false,
		},
		{
			name:      "tilde expansion with trailing slash",
			flagValue: "~/projects/myrepo/",
			cwd:       absCwd,
			home:      absHome,
			want:      filepath.Join(absHome, "projects/myrepo"),
			wantErr:   false,
		},
		{
			name:      "relative path with trailing slash",
			flagValue: "projects/myrepo/",
			cwd:       "/home/user",
			home:      absHome,
			want:      filepath.Join("/home/user", "projects/myrepo"),
			wantErr:   false,
		},
		{
			name:      "root path normalized to /",
			flagValue: "/",
			cwd:       absCwd,
			home:      absHome,
			want:      "/",
			wantErr:   false,
		},
		{
			name:      "path with .. segments cleaned",
			flagValue: "/a/b/../c",
			cwd:       absCwd,
			home:      absHome,
			want:      "/a/c",
			wantErr:   false,
		},
		{
			name:      "path with duplicate separators cleaned",
			flagValue: "/a//b//",
			cwd:       absCwd,
			home:      absHome,
			want:      "/a/b",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveProject(tt.flagValue, tt.cwd, tt.home)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveProject(%q, %q, %q) error = %v, wantErr %v",
					tt.flagValue, tt.cwd, tt.home, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("resolveProject(%q, %q, %q) = %q, want %q",
					tt.flagValue, tt.cwd, tt.home, got, tt.want)
			}
		})
	}
}

// TestAddProjectFlag pins the shared flag registration. The helper returns a
// pointer to a string var that can be read after Parse, ensuring each subcommand
// gets consistent --project flag naming and help text.
func TestAddProjectFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	proj := addProjectFlag(fs)

	if proj == nil {
		t.Errorf("addProjectFlag returned nil")
	}

	err := fs.Parse([]string{"--project", "/repo/path"})
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if *proj != "/repo/path" {
		t.Errorf("after Parse, *proj = %q, want %q", *proj, "/repo/path")
	}
}
