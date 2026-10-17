package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(kv ...string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	for i := 0; i+1 < len(kv); i += 2 {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
	}
	return info
}

// TestVersionLine pins the line `afk version` prints. Load-bearing rows: "no
// build info" (a binary built outside a checkout must say unknown, not crash or
// invent a commit), "dirty tree" (a binary with uncommitted changes must not
// pass as the commit), "commit with no time" (the comma goes with the date),
// and "short revision" (the 12-character cut must not slice past the end).
func TestVersionLine(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name string
		ld   string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"release version with build info", "1.2.3", buildInfo("vcs.revision", rev, "vcs.time", "2026-10-17T08:30:00Z", "vcs.modified", "false"), true, "afk 1.2.3 (0123456789ab, 2026-10-17)"},
		{"dev with a commit and date", "", buildInfo("vcs.revision", rev, "vcs.time", "2026-10-17T08:30:00Z"), true, "afk dev (0123456789ab, 2026-10-17)"},
		{"dev with a dirty tree", "", buildInfo("vcs.revision", rev, "vcs.time", "2026-10-17T08:30:00Z", "vcs.modified", "true"), true, "afk dev (0123456789ab+dirty, 2026-10-17)"},
		{"dev with no build info", "", nil, false, "afk dev (unknown)"},
		{"release version with no build info", "1.2.3", nil, false, "afk 1.2.3 (unknown)"},
		{"build info without a revision", "", buildInfo("vcs.time", "2026-10-17T08:30:00Z"), true, "afk dev (unknown)"},
		{"commit with no time", "", buildInfo("vcs.revision", rev), true, "afk dev (0123456789ab)"},
		{"commit with an unparseable time", "", buildInfo("vcs.revision", rev, "vcs.time", "yesterday"), true, "afk dev (0123456789ab)"},
		{"revision shorter than 12 characters", "", buildInfo("vcs.revision", "abc123", "vcs.time", "2026-10-17T08:30:00Z"), true, "afk dev (abc123, 2026-10-17)"},
		{"time in another zone is shown as the UTC day", "", buildInfo("vcs.revision", rev, "vcs.time", "2026-10-17T23:30:00-05:00"), true, "afk dev (0123456789ab, 2026-10-18)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionLine(tt.ld, tt.info, tt.ok); got != tt.want {
				t.Errorf("versionLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVersionArgs pins the exit codes: usage on --help goes to stdout with 0,
// anything extra goes to stderr with 2 and prints no version line.
func TestVersionArgs(t *testing.T) {
	info := buildInfo("vcs.revision", "0123456789abcdef", "vcs.time", "2026-10-17T00:00:00Z")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string
		wantErrHas string
	}{
		{"no arguments", nil, 0, "afk dev (0123456789ab, 2026-10-17)\n", ""},
		{"--help", []string{"--help"}, 0, "afk version —", ""},
		{"-h", []string{"-h"}, 0, "afk version —", ""},
		{"an extra argument", []string{"extra"}, 2, "", "Usage:"},
		{"--help with an extra argument", []string{"--help", "x"}, 2, "", "Usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := runVersion(tt.args, "", info, true, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			if tt.wantOut != "" && !strings.HasPrefix(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want prefix %q", out.String(), tt.wantOut)
			}
			if tt.wantOut == "" && out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if !strings.Contains(errb.String(), tt.wantErrHas) {
				t.Errorf("stderr = %q, want it to contain %q", errb.String(), tt.wantErrHas)
			}
		})
	}
}
