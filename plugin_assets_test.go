package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestPluginSkillFiles protects the rename of the plan skill. Load-bearing:
// the absent rows. A leftover plan/SKILL.md would keep answering the old
// command name next to the new one, and nothing else notices a folder that
// should have gone.
func TestPluginSkillFiles(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		exists bool
	}{
		{"master-plan skill", "plugins/afk/skills/master-plan/SKILL.md", true},
		{"work-plan skill", "plugins/afk/skills/work-plan/SKILL.md", true},
		{"work-plan skill in the OpenCode port", "plugins/afk-opencode/skills/work-plan/SKILL.md", true},
		{"old plan skill is gone", "plugins/afk/skills/plan/SKILL.md", false},
		{"old plan skill is gone from the OpenCode port", "plugins/afk-opencode/skills/plan/SKILL.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fs.Stat(pluginAssets, tt.path)
			if got := err == nil; got != tt.exists {
				t.Errorf("%s: exists = %v, want %v (err %v)", tt.path, got, tt.exists, err)
			}
		})
	}
}

// oldPlanCommand finds the retired command name with one optional character
// after it. Go's regexp has no look-ahead, so oldPlanMatches checks that
// character in code.
var oldPlanCommand = regexp.MustCompile(`/afk[:-]plan([^\n]?)`)

// oldPlanMatches reports whether line uses /afk:plan or /afk-plan as a whole
// command name, that is, not followed by a lower-case letter, a digit or a
// hyphen.
func oldPlanMatches(line string) bool {
	for _, m := range oldPlanCommand.FindAllStringSubmatch(line, -1) {
		if m[1] == "" {
			return true
		}
		c := m[1][0]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return true
		}
	}
	return false
}

// TestPluginOldPlanMatcher protects the matcher the next test relies on. Load-bearing:
// the no-match rows. /afk:planner is a real skill and afk-plan-{name} is a
// session title; a matcher that flagged them would force edits to correct text.
func TestPluginOldPlanMatcher(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"bare colon form", "run /afk:plan", true},
		{"followed by a full stop", "run /afk:plan.", true},
		{"hyphen form then space", "run /afk-plan now", true},
		{"end of line", "/afk-plan", true},
		{"planner", "/afk:planner", false},
		{"work-plan colon", "/afk:work-plan", false},
		{"work-plan hyphen", "/afk-work-plan", false},
		{"session title has no slash", "afk-plan-foo", false},
		{"followed by a digit", "/afk:plan2", false},
		{"followed by a hyphen", "/afk:plan-x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oldPlanMatches(tt.line); got != tt.want {
				t.Errorf("oldPlanMatches(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestPluginsUseNewPlanName protects the rename of /afk:plan to
// /afk:work-plan. A prompt that still names the old command sends the agent
// to a skill that no longer exists, and nothing fails at run time. Every text
// file under plugins/ is checked, plus the README read from disk.
func TestPluginsUseNewPlanName(t *testing.T) {
	check := func(name string, b []byte) {
		if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			return
		}
		for i, line := range strings.Split(string(b), "\n") {
			if oldPlanMatches(line) {
				t.Errorf("%s:%d still uses the old command name: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	err := fs.WalkDir(pluginAssets, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(pluginAssets, p)
		if err != nil {
			return err
		}
		check(p, b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk plugins: %v", err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	check("README.md", readme)
}

// catInclude matches the include lines the Claude Code skill loader expands.
var catInclude = regexp.MustCompile("^!`cat \"\\$\\{CLAUDE_PLUGIN_ROOT\\}/([^\"]+)\"`\\s*$")

// TestPluginMasterPlanIncludes protects the master-plan skill's injected prompts.
// Load-bearing: a path that does not exist injects an error into the prompt
// at run time, and a file over the cap trips scripts/check-cat-sizes.sh only
// in make plugin-test; this pins both in go test. The cap counts characters
// (utf8.RuneCount), the same as wc -m in that script.
func TestPluginMasterPlanIncludes(t *testing.T) {
	const sizeCap = 28000
	b, err := fs.ReadFile(pluginAssets, "plugins/afk/skills/master-plan/SKILL.md")
	if err != nil {
		t.Fatalf("read master-plan SKILL.md: %v", err)
	}
	var paths []string
	for _, line := range strings.Split(string(b), "\n") {
		if m := catInclude.FindStringSubmatch(line); m != nil {
			paths = append(paths, m[1])
		}
	}
	if len(paths) == 0 {
		t.Fatal("master-plan SKILL.md has no include lines")
	}
	hasFormat, hasModes := false, false
	for _, p := range paths {
		if p == "skills/master-plan/plan-format.md" {
			hasFormat = true
		}
		if p == "skills/master-plan/prompts/modes.md" {
			hasModes = true
		}
		t.Run(p, func(t *testing.T) {
			body, err := fs.ReadFile(pluginAssets, "plugins/afk/"+p)
			if err != nil {
				t.Fatalf("included file missing from the embedded plugin: %v", err)
			}
			if n := utf8.RuneCount(body); n > sizeCap {
				t.Errorf("%s is %d characters, over the %d cap", p, n, sizeCap)
			}
		})
	}
	if !hasFormat {
		t.Error("master-plan SKILL.md does not include skills/master-plan/plan-format.md")
	}
	// Without this include the session still starts and still reads its mode
	// from afk plan status, but has no text for extend, amend or reconcile,
	// so it would run the new-plan phases against a plan that is being built.
	if !hasModes {
		t.Error("master-plan SKILL.md does not include skills/master-plan/prompts/modes.md")
	}
}

// TestPluginMasterPlanPatchesByCommand holds the master-plan skill to changing
// a plan through afk's commands and never with the agent's own file edit.
//
// Load-bearing rows: "exact-match Edit" is the wording of the rule the skill
// had before afk plan patch existed, and it is searched for in every file of
// the skill, because one leftover sentence is an instruction to write the plan
// file directly, past the checks that keep a running task's text and the
// checker's verdict intact. The five names in modes.md are the whole surface
// the change modes work through: a prompt that drops one leaves the agent to
// do that step by hand (an id it counted itself, a dependency line it
// rewrote), and no other test reads the prompt's text.
func TestPluginMasterPlanPatchesByCommand(t *testing.T) {
	const skillDir = "plugins/afk/skills/master-plan"
	files := 0
	err := fs.WalkDir(pluginAssets, skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(pluginAssets, p)
		if err != nil {
			return err
		}
		files++
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "exact-match Edit") {
				t.Errorf("%s:%d still tells the session to change the plan with an exact-match Edit: %s", p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", skillDir, err)
	}
	// A walk that read nothing would pass the search above for any text.
	if files == 0 {
		t.Fatalf("no files found under %s in the embedded plugin", skillDir)
	}

	modes, err := fs.ReadFile(pluginAssets, skillDir+"/prompts/modes.md")
	if err != nil {
		t.Fatalf("read modes.md: %v", err)
	}
	for _, want := range []string{
		"afk plan patch",
		"afk plan insert",
		"afk plan depend",
		"afk plan next-id",
		"changes.md",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(string(modes), want) {
				t.Errorf("modes.md does not name %q", want)
			}
		})
	}
}
