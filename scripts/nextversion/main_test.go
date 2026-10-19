package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const zeros = "0000000000000000000000000000000000000000"

// scratch is a real git repository in a temp folder, shut off from the user's
// git config.
type scratch struct {
	t   *testing.T
	dir string
}

func newScratch(t *testing.T) *scratch {
	s := &scratch{t: t, dir: t.TempDir()}
	s.git("-c", "init.defaultBranch=main", "init", "-q")
	return s
}

func (s *scratch) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes one file and commits it with msg, returning the new sha.
func (s *scratch) commit(path, msg string) string {
	s.t.Helper()
	full := filepath.Join(s.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(msg+"\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", msg)
	return s.git("rev-parse", "HEAD")
}

func (s *scratch) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, s.dir, &out, &errb)
	return code, out.String(), errb.String()
}

// TestRun drives run against real git. Load-bearing rows: the --no-ff merge
// (proves every commit in the range is read, not only the newest, which a
// merge-commit strategy would hide), the two-tag row (the highest tag wins, not
// the newest-created), the vnext row (a non-version tag must not count), and
// the error rows (stdout stays empty so the release script never reads an
// error as a version).
func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(s *scratch) []string // returns args
		wantCode   int
		wantOut    string
		wantErrHas string
	}{
		{"no tag, all-zero from", func(s *scratch) []string {
			s.commit("a.md", "docs: A")
			return []string{zeros, "HEAD"}
		}, 0, "0.1.0\n", ""},
		{"fix on go file after tag", func(s *scratch) []string {
			base := s.commit("a.md", "docs: A")
			s.git("tag", "v0.1.0")
			s.commit("main.go", "fix: B")
			return []string{base, "HEAD"}
		}, 0, "0.1.1\n", ""},
		{"feat in internal after tag", func(s *scratch) []string {
			base := s.commit("a.md", "docs: A")
			s.git("tag", "v0.1.0")
			s.commit("internal/x/x.go", "feat: B")
			return []string{base, "HEAD"}
		}, 0, "0.2.0\n", ""},
		{"only docs after tag", func(s *scratch) []string {
			base := s.commit("a.md", "docs: A")
			s.git("tag", "v0.1.0")
			s.commit("docs/a.md", "feat: B")
			return []string{base, "HEAD"}
		}, 0, "skip\n", ""},
		{"no-ff merge of a feat side branch", func(s *scratch) []string {
			base := s.commit("a.md", "docs: A")
			s.git("tag", "v0.1.0")
			s.git("checkout", "-q", "-b", "side")
			s.commit("internal/y.go", "feat: side")
			s.commit("docs/z.md", "docs: tip")
			s.git("checkout", "-q", "main")
			s.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1 from x/side", "side")
			return []string{base, "HEAD"}
		}, 0, "0.2.0\n", ""},
		{"highest of two tags", func(s *scratch) []string {
			s.commit("a.md", "docs: A")
			s.git("tag", "v0.3.0")
			base := s.commit("b.md", "docs: B")
			s.git("tag", "v0.1.0")
			s.commit("main.go", "fix: C")
			return []string{base, "HEAD"}
		}, 0, "0.3.1\n", ""},
		{"non-version tag ignored", func(s *scratch) []string {
			base := s.commit("a.md", "docs: A")
			s.git("tag", "v0.1.0")
			s.git("tag", "vnext")
			s.commit("main.go", "fix: B")
			return []string{base, "HEAD"}
		}, 0, "0.1.1\n", ""},
		{"bad sha", func(s *scratch) []string {
			s.commit("a.md", "docs: A")
			return []string{"deadbeef", "HEAD"}
		}, 1, "", "nextversion:"},
		{"wrong argument count", func(s *scratch) []string {
			return []string{"onlyone"}
		}, 2, "", "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScratch(t)
			args := tt.setup(s)
			code, out, errOut := s.run(args...)
			if code != tt.wantCode || out != tt.wantOut {
				t.Fatalf("code=%d out=%q stderr=%q; want code=%d out=%q", code, out, errOut, tt.wantCode, tt.wantOut)
			}
			if tt.wantErrHas == "" && errOut != "" {
				t.Fatalf("unexpected stderr %q", errOut)
			}
			if !strings.Contains(errOut, tt.wantErrHas) {
				t.Fatalf("stderr %q lacks %q", errOut, tt.wantErrHas)
			}
		})
	}
}

// TestRunNotARepository: outside a repository the run fails with exit 1 and
// prints nothing on stdout.
func TestRunNotARepository(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{zeros, "HEAD"}, t.TempDir(), &out, &errb)
	if code != 1 || out.Len() != 0 || errb.Len() == 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), errb.String())
	}
}
