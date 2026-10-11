package main

import "testing"

// TestMainCheckout: the load-bearing rows are the linked-worktree ones (an
// agent's cwd must resolve to the main checkout, not its own .afk/), the
// subfolder row (the top folder, not dir), and the fallbacks (not a repo and
// a bare repo keep the folder the caller gave us).
func TestMainCheckout(t *testing.T) {
	tests := []struct {
		name      string
		dir       string
		commonDir string
		want      string
	}{
		{"main checkout root", "/home/u/proj", "/home/u/proj/.git", "/home/u/proj"},
		{"subfolder of main checkout", "/home/u/proj/internal/x", "/home/u/proj/.git", "/home/u/proj"},
		{"linked worktree under .claude/worktrees", "/home/u/proj/.claude/worktrees/x", "/home/u/proj/.git", "/home/u/proj"},
		{"linked worktree elsewhere on disk", "/tmp/wt/x", "/home/u/proj/.git", "/home/u/proj"},
		{"not a git repo", "/home/u/plain/", "", "/home/u/plain"},
		{"bare repo common dir", "/srv/proj.git/", "/srv/proj.git", "/srv/proj.git"},
		{"trailing slash on commonDir", "/tmp/wt/x", "/home/u/proj/.git/", "/home/u/proj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mainCheckout(tt.dir, tt.commonDir); got != tt.want {
				t.Errorf("mainCheckout(%q, %q) = %q, want %q", tt.dir, tt.commonDir, got, tt.want)
			}
		})
	}
}

// TestInferSession: the load-bearing rows are none and two, which must give
// "" so the item carries no guessed session; one gives the folder name.
func TestInferSession(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"none", nil, ""},
		{"one", []string{"/wt/.afk/t1-2/status.json"}, "t1-2"},
		{"two", []string{"/wt/.afk/a/status.json", "/wt/.afk/b/status.json"}, ""},
		{"deeper path", []string{"/a/b/c/.afk/my-sess/status.json"}, "my-sess"},
		{"relative path", []string{".afk/solo/status.json"}, "solo"},
		{"trailing separator", []string{"/wt/.afk/trail/status.json/"}, "trail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferSession(tt.paths); got != tt.want {
				t.Errorf("inferSession(%v) = %q, want %q", tt.paths, got, tt.want)
			}
		})
	}
}
