package profile

import (
	"strings"
	"testing"
)

// TestLooksSecret: every pattern the net claims to catch has a row, built from
// joined pieces so no file here holds a string a scanner would flag. The false
// rows are load-bearing: an ordinary owner value that trips the check would
// make the profile unusable, and there is no override.
func TestLooksSecret(t *testing.T) {
	alnum := strings.Repeat("a1B2", 9)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"openai style key", "s" + "k-" + strings.Repeat("a1", 12), true},
		{"sk short tail is fine", "s" + "k-learn", false},
		{"github ghp", "gh" + "p_" + alnum, true},
		{"github gho", "gh" + "o_" + alnum, true},
		{"github ghu", "gh" + "u_" + alnum, true},
		{"github ghs", "gh" + "s_" + alnum, true},
		{"github ghr", "gh" + "r_" + alnum, true},
		{"github fine grained", "github" + "_pat_" + alnum, true},
		{"gitlab", "gl" + "pat-" + alnum, true},
		{"slack bot", "xo" + "xb-" + "12345-abcde", true},
		{"slack user", "xo" + "xp-" + "12345-abcde", true},
		{"slack app", "xo" + "xa-" + "12345-abcde", true},
		{"slack session", "xo" + "xs-" + "12345-abcde", true},
		{"google key", "AI" + "za" + strings.Repeat("x9", 11), true},
		{"google short tail", "AI" + "za" + "short", false},
		{"aws akia", "AK" + "IA" + strings.Repeat("A1", 8), true},
		{"aws asia", "AS" + "IA" + strings.Repeat("Z9", 8), true},
		{"private key", "-----" + "BEGIN RSA PRIVATE KEY-----", true},
		{"jwt", "ey" + "Jhbgc.ey" + "JzdWI.sig_nature-1", true},
		{"two part dotted is not jwt", "eyJabc.def", false},
		{"bearer", "Bearer " + alnum, true},
		{"bearer lower case", "bearer " + alnum, true},
		{"bearer short", "Bearer auth", false},
		{"url password", "postgres://user:" + "pw123@db.example.com/x", true},
		{"url without password", "https://user@example.com/x", false},
		{"password assignment", "pass" + "word = hunter2", true},
		{"passwd colon", "PASS" + "WD: hunter2", true},
		{"secret assignment", "sec" + "ret=abc", true},
		{"token assignment", "tok" + "en: abc", true},
		{"api_key assignment", "api" + "_key=abc", true},
		{"api-key assignment", "api" + "-key: abc", true},
		{"apikey assignment", "API" + "KEY=abc", true},
		{"hex", strings.Repeat("0123abcd", 4), true},
		{"hex too short", strings.Repeat("0123abcd", 3), false},
		{"mixed random", "Ab1" + strings.Repeat("x", 29), true},
		{"no preference", "no preference", false},
		{"postgres version", "PostgreSQL 16", false},
		{"docs url", "https://example.com/docs/how-we-deploy", false},
		{"rules path", "~/code/.claude/rules/rails.md", false},
		{"long path", "/home/someone/very/long/path/to/a/folder/with/many/parts/rules.md", false},
		{"long lower case name", "my-long-lower-case-project-name-with-many-words", false},
		{"ci", "GitHub Actions on ubuntu-26.04", false},
		{"token prose", "token-based auth with Devise", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why, got := LooksSecret(c.in)
			if got != c.want {
				t.Fatalf("LooksSecret(%q) = %v (%q), want %v", c.in, got, why, c.want)
			}
			if got && (why == "" || strings.Contains(why, c.in)) {
				t.Errorf("reason %q must be non-empty and not contain the input", why)
			}
			if !got && why != "" {
				t.Errorf("reason %q must be empty when not secret", why)
			}
		})
	}
}
