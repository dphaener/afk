package main

import (
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/dphaener/afk/internal/pluginstall"
)

// TestPluginAssets guards the embed patterns and the manifests that
// pluginstall depends on. Load-bearing: the dot-directory manifests (a
// pattern without `all:` silently drops them), both marketplaces carrying
// pluginstall.MarketplaceName (the install commands name afk@afk),
// and the Claude and Codex manifests agreeing on a version (Codex caches the
// plugin by version, so a stale one never refreshes). The six agent
// definitions are listed because the workflow scripts and the skills name them
// as agent types: one missing from the binary is not an error anywhere, every
// spawn of that type just falls back to the default agent and its full tool
// set, so only this test would notice.
func TestPluginAssets(t *testing.T) {
	for _, p := range []string{
		"plugins/afk/.claude-plugin/plugin.json",
		"plugins/afk/.codex-plugin/plugin.json",
		"plugins/afk/agents/qa.md",
		"plugins/afk/agents/research.md",
		"plugins/afk/agents/reviewer.md",
		"plugins/afk/agents/task.md",
		"plugins/afk/agents/verifier.md",
		"plugins/afk/agents/web-research.md",
		"plugins/afk/hooks/hooks.json",
		"plugins/afk/skills/conductor/status-schema.md",
		"plugins/afk-opencode/index.ts",
		"plugins/afk-opencode/package.json",
	} {
		if _, err := fs.Stat(pluginAssets, p); err != nil {
			t.Errorf("embedded assets missing %s", p)
		}
	}

	read := func(p string) map[string]any {
		t.Helper()
		b, err := fs.ReadFile(pluginAssets, p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		return m
	}
	for _, p := range []string{".claude-plugin/marketplace.json", ".agents/plugins/marketplace.json"} {
		if got := read(p)["name"]; got != pluginstall.MarketplaceName {
			t.Errorf("%s name = %v, want %s", p, got, pluginstall.MarketplaceName)
		}
	}
	cv := read("plugins/afk/.claude-plugin/plugin.json")["version"]
	xv := read("plugins/afk/.codex-plugin/plugin.json")["version"]
	if cv != xv {
		t.Errorf("claude plugin version %v != codex plugin version %v", cv, xv)
	}
}
