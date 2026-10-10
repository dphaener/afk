package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbes table-driven tests for all probes.
func TestProbes(t *testing.T) {
	tests := []struct {
		name                string
		setupFunc           func(string) error // Setup PATH with fake executables
		probeFunc           func() Result      // The probe to test
		expectPresent       bool
		expectDegraded      bool   // Whether to expect a non-empty degradation message
		degradationContains string // Substring that should be in the message if degraded
		// degradationEquals pins the WHOLE message. Use it where the message's
		// content is itself the contract — the notifier row names the backends and
		// carries the install command, and a substring check let both drift.
		degradationEquals string
	}{
		// Claude CLI tests
		{
			name: "Claude CLI present with valid version 2.1.205",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeVersion(tmpPath, "claude-code/2.1.205 (darwin/arm64)")
			},
			probeFunc:      ProbeClaudeCLI,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Claude CLI present with valid version 2.2.0",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeVersion(tmpPath, "claude-code/2.2.0")
			},
			probeFunc:      ProbeClaudeCLI,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Claude CLI present but version too old 2.1.204",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeVersion(tmpPath, "claude-code/2.1.204")
			},
			probeFunc:           ProbeClaudeCLI,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "too old",
		},
		{
			name: "Claude CLI present but version too old 2.1.21",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeVersion(tmpPath, "claude-code/2.1.21")
			},
			probeFunc:           ProbeClaudeCLI,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "too old",
		},
		{
			name: "Claude CLI present but version too old 2.0.0",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeVersion(tmpPath, "claude-code/2.0.0")
			},
			probeFunc:           ProbeClaudeCLI,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "too old",
		},
		{
			name: "Claude CLI not found",
			setupFunc: func(tmpPath string) error {
				return nil // Don't create it
			},
			probeFunc:           ProbeClaudeCLI,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "not found",
		},
		{
			name: "Claude CLI --version fails",
			setupFunc: func(tmpPath string) error {
				return createFakeClaudeFailing(tmpPath)
			},
			probeFunc:           ProbeClaudeCLI,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "failed",
		},

		// Worktrunk tests
		{
			name: "Worktrunk (wt) present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "wt")
			},
			probeFunc:      ProbeWorktrunk,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Worktrunk (wt) not found",
			setupFunc: func(tmpPath string) error {
				return nil
			},
			probeFunc:           ProbeWorktrunk,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "not found",
		},

		// GitHub CLI tests
		{
			name: "GitHub CLI (gh) present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "gh")
			},
			probeFunc:      ProbeGh,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "GitHub CLI (gh) not found",
			setupFunc: func(tmpPath string) error {
				return nil
			},
			probeFunc:           ProbeGh,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "not found",
		},

		// Git-Spice tests (both gs and git-spice)
		{
			name: "Git-spice (gs) binary present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "gs")
			},
			probeFunc:      ProbeGitSpice,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Git-spice (git-spice) binary present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "git-spice")
			},
			probeFunc:      ProbeGitSpice,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Git-spice neither gs nor git-spice found",
			setupFunc: func(tmpPath string) error {
				return nil
			},
			probeFunc:           ProbeGitSpice,
			expectPresent:       false,
			expectDegraded:      true,
			degradationContains: "not found",
		},

		{
			name: "Notifier (alerter) present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "alerter")
			},
			probeFunc:      ProbeNotifier,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Notifier (osascript) present",
			setupFunc: func(tmpPath string) error {
				return createFakeBinary(tmpPath, "osascript")
			},
			probeFunc:      ProbeNotifier,
			expectPresent:  true,
			expectDegraded: false,
		},
		{
			name: "Notifier none found",
			setupFunc: func(tmpPath string) error {
				return nil
			},
			probeFunc:      ProbeNotifier,
			expectPresent:  false,
			expectDegraded: true,
			// The whole string, not a fragment. "No notifier" alone passed while
			// the message still named terminal-notifier or omitted the install
			// command — the two things a user reading a degradation notice actually
			// needs. Pinning it exactly is what makes a reword a failing test.
			degradationEquals: "No notifier found (alerter, osascript) — desktop notifications disabled; install with: brew install vjeantet/tap/alerter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "afk-probe-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			// Setup fake binaries
			if err := tt.setupFunc(tmpDir); err != nil {
				t.Fatalf("Setup failed: %v", err)
			}

			// Temporarily override PATH to isolate test environment
			oldPath := os.Getenv("PATH")
			os.Setenv("PATH", tmpDir)
			defer os.Setenv("PATH", oldPath)

			// Run probe
			result := tt.probeFunc()

			// Check presence
			if result.Present != tt.expectPresent {
				t.Errorf("Expected Present=%v, got %v", tt.expectPresent, result.Present)
			}

			// Check degradation message
			if tt.expectDegraded {
				if result.DegradationMessage == "" {
					t.Errorf("Expected degradation message, but got empty string")
				}
				if tt.degradationContains != "" && !contains(result.DegradationMessage, tt.degradationContains) {
					t.Errorf("Expected degradation message to contain %q, but got %q", tt.degradationContains, result.DegradationMessage)
				}
				if tt.degradationEquals != "" && result.DegradationMessage != tt.degradationEquals {
					t.Errorf("degradation message =\n  %q\nwant exactly\n  %q", result.DegradationMessage, tt.degradationEquals)
				}
			} else {
				if result.DegradationMessage != "" {
					t.Errorf("Expected no degradation message, but got %q", result.DegradationMessage)
				}
			}
		})
	}
}

// TestVersionComparison table-driven tests for semver comparison.
func TestVersionComparison(t *testing.T) {
	tests := []struct {
		version    string
		minVersion string
		expectGte  bool
	}{
		// Equal versions
		{"2.1.205", "2.1.205", true},
		{"2.0.0", "2.0.0", true},

		// Greater versions (major)
		{"3.0.0", "2.1.205", true},
		{"2.1.205", "1.9.9", true},

		// Greater versions (minor)
		{"2.2.0", "2.1.205", true},
		{"2.1.206", "2.1.205", true},

		// Greater versions (patch)
		{"2.1.206", "2.1.205", true},

		// Less versions
		{"2.1.204", "2.1.205", false},
		{"2.1.21", "2.1.205", false},
		{"2.0.0", "2.1.205", false},
		{"1.9.9", "2.1.205", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s >= %s", tt.version, tt.minVersion), func(t *testing.T) {
			result := semverGte(tt.version, tt.minVersion)
			if result != tt.expectGte {
				t.Errorf("Expected semverGte(%q, %q) = %v, got %v", tt.version, tt.minVersion, tt.expectGte, result)
			}
		})
	}
}

// TestParseClaudeVersion tests the version parsing from claude --version output.
func TestParseClaudeVersion(t *testing.T) {
	tests := []struct {
		output        string
		expectVersion string
	}{
		{"claude-code/2.1.205 (darwin/arm64)", "2.1.205"},
		{"claude-code/2.2.0", "2.2.0"},
		{"2.1.205", "2.1.205"},
		{"2.1.205 some extra text", "2.1.205"},
		{"version: 2.1.205-beta", "2.1.205"},
		{"invalid version", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			result := parseClaudeVersion(tt.output)
			if result != tt.expectVersion {
				t.Errorf("Expected %q, got %q", tt.expectVersion, result)
			}
		})
	}
}

// Helper functions

// createFakeBinary creates a fake executable in tmpPath.
func createFakeBinary(tmpPath, name string) error {
	binPath := filepath.Join(tmpPath, name)
	return os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755)
}

// createFakeClaudeVersion creates a fake claude binary that outputs a version string.
func createFakeClaudeVersion(tmpPath, versionOutput string) error {
	binPath := filepath.Join(tmpPath, "claude")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo %q\n  exit 0\nfi\nexit 1\n", versionOutput)
	return os.WriteFile(binPath, []byte(script), 0755)
}

// createFakeClaudeFailing creates a fake claude binary that fails on --version.
func createFakeClaudeFailing(tmpPath string) error {
	binPath := filepath.Join(tmpPath, "claude")
	script := "#!/bin/sh\nexit 1\n"
	return os.WriteFile(binPath, []byte(script), 0755)
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
