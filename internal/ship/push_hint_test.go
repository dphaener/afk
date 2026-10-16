package ship

import (
	"errors"
	"strings"
	"testing"
)

// TestPushHint checks which hint a failed push gets.
//
// Load-bearing rows: "woodshop refusal" (the real line GitHub sent, which
// must name the workflow scope and not send the owner to pull commits that
// do not exist), and "non-fast-forward" plus "nil error" (anything else must
// read byte-for-byte as it did before, so the usual-cause text is compared
// exactly, not by fragment).
func TestPushHint(t *testing.T) {
	const usual = "The usual cause is that the branch on GitHub has commits the session does not have. " +
		"Pull them into the session's branch, then answer this item and afk pushes again."
	tests := []struct {
		name string
		err  error
		// exact is the whole hint; contains is checked when exact is empty.
		exact    string
		contains []string
	}{
		{
			name:     "woodshop refusal",
			err:      errors.New("! [remote rejected] t1-1 -> t1-1 (refusing to allow an OAuth App to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)"),
			contains: []string{"gh auth refresh -s workflow", "git remote set-url origin git@github.com:OWNER/REPO.git", ".github/workflows/", "answer this item and afk pushes again"},
		},
		{
			name:     "scope phrase alone",
			err:      errors.New("remote: refusing to push; needs the Without `Workflow` Scope"),
			contains: []string{"gh auth refresh -s workflow"},
		},
		{
			name:     "oauth app wording with workflow, different tail",
			err:      errors.New("remote rejected: Refusing to allow an OAuth App to update workflow files"),
			contains: []string{"gh auth refresh -s workflow"},
		},
		{
			name:  "oauth app refusal that is not about workflows",
			err:   errors.New("refusing to allow an OAuth App to create a repository"),
			exact: usual,
		},
		{
			name:  "non-fast-forward",
			err:   errors.New("! [rejected] t1-1 -> t1-1 (fetch first)"),
			exact: usual,
		},
		{
			name:  "unrelated error",
			err:   errors.New("fatal: unable to access: Could not resolve host"),
			exact: usual,
		},
		{
			name:  "nil error",
			err:   nil,
			exact: usual,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pushHint(tt.err)
			if tt.exact != "" && got != tt.exact {
				t.Errorf("hint = %q, want exactly %q", got, tt.exact)
			}
			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("hint %q lacks %q", got, s)
				}
			}
			if tt.exact == "" && strings.Contains(got, "usual cause") {
				t.Errorf("a workflow refusal still got the usual-cause hint: %q", got)
			}
		})
	}
}
