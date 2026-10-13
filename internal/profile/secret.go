package profile

import (
	"regexp"
	"strings"
)

// LooksSecret reports whether s looks like a key, token or password, and if so
// says why in a short plain phrase. The phrase never contains s: the caller
// prints it, and printing the secret would defeat the check.
//
// This is a net for mistakes, not a guarantee. It catches the shapes people
// paste by accident (well-known key prefixes, private keys, JWTs, passwords in
// URLs, "token = ..." lines, long random-looking words) and will miss a secret
// in a shape it does not know. There is deliberately no override flag: a
// legitimate profile value is a plain preference ("PostgreSQL 16"), so a
// false positive is cheap to rephrase, while an override would be the first
// thing a rushed owner reached for.
func LooksSecret(s string) (why string, secret bool) {
	if strings.Contains(s, "-----BEGIN") {
		return "it looks like a private key or certificate", true
	}
	if urlPassword.MatchString(s) {
		return "it looks like a web address with a password in it", true
	}
	if bearer.MatchString(s) {
		return "it looks like an access token", true
	}
	if assignment.MatchString(s) {
		return "it looks like a password or token being set", true
	}
	for _, w := range strings.FieldsFunc(s, isWordBreak) {
		if r, ok := wordLooksSecret(w); ok {
			return r, true
		}
	}
	return "", false
}

var (
	urlPassword = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)
	bearer      = regexp.MustCompile(`(?i)bearer\s+[^\s]{16,}`)
	assignment  = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key|api-key|apikey)\s*[=:]\s*\S`)
	awsKey      = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{16}`)
	hexWord     = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	randomWord  = regexp.MustCompile(`^[A-Za-z0-9+=_-]{32,}$`)
	jwtPart     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func isWordBreak(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '"', '\'', ',', ';', '(', ')', '<', '>':
		return true
	}
	return false
}

// keyPrefixes are the well-known key prefixes and the least number of
// characters that must follow each. Short tails are skipped so ordinary
// words that happen to start the same way ("sk-" in "sk-learn") pass.
var keyPrefixes = []struct {
	prefix string
	tail   int
	why    string
}{
	{"sk-", 16, "it starts like an API key"},
	{"ghp_", 1, "it starts like a GitHub token"},
	{"gho_", 1, "it starts like a GitHub token"},
	{"ghu_", 1, "it starts like a GitHub token"},
	{"ghs_", 1, "it starts like a GitHub token"},
	{"ghr_", 1, "it starts like a GitHub token"},
	{"github_pat_", 1, "it starts like a GitHub token"},
	{"glpat-", 1, "it starts like a GitLab token"},
	{"xoxb-", 1, "it starts like a Slack token"},
	{"xoxp-", 1, "it starts like a Slack token"},
	{"xoxa-", 1, "it starts like a Slack token"},
	{"xoxs-", 1, "it starts like a Slack token"},
	{"AIza", 20, "it starts like a Google API key"},
}

func wordLooksSecret(w string) (string, bool) {
	for _, k := range keyPrefixes {
		if strings.HasPrefix(w, k.prefix) && len(w)-len(k.prefix) >= k.tail {
			return k.why, true
		}
	}
	if awsKey.MatchString(w) {
		return "it starts like a cloud access key", true
	}
	if isJWT(w) {
		return "it looks like a signed login token", true
	}
	if hexWord.MatchString(w) {
		return "it is a long run of hex digits, like a key", true
	}
	if randomWord.MatchString(w) && hasUpper(w) && hasLower(w) && hasDigit(w) {
		return "it is a long random-looking string, like a key", true
	}
	return "", false
}

func isJWT(w string) bool {
	parts := strings.Split(w, ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "eyJ") {
		return false
	}
	for _, p := range parts {
		if !jwtPart.MatchString(p) {
			return false
		}
	}
	return true
}

func hasUpper(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0
}
func hasLower(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
}
func hasDigit(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0
}
