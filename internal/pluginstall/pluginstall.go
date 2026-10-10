// Package pluginstall installs the afk agent plugins that ship inside the
// afk binary into the agent CLIs found on this machine: Claude Code and
// Codex through a local marketplace directory, OpenCode by copying its port
// into OpenCode's auto-loaded plugin directory.
//
// The plugin trees are embedded in the binary (see plugin_assets.go at the
// repo root) so the installed plugin always matches the CLI that reads its
// status.json, plan, and verdict files.
package pluginstall

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Target is an agent CLI the plugin can be installed into.
type Target string

const (
	Claude   Target = "claude"
	Codex    Target = "codex"
	OpenCode Target = "opencode"
)

// AllTargets is every supported target, in install order.
var AllTargets = []Target{Claude, Codex, OpenCode}

const (
	// MarketplaceName is the `name` in .claude-plugin/marketplace.json and
	// .agents/plugins/marketplace.json; it must stay in step with both.
	MarketplaceName = "afk"
	PluginName      = "afk"
	PluginID        = PluginName + "@" + MarketplaceName

	// markerFile marks a directory as written by this installer, so a
	// re-install may replace it without --force.
	markerFile = ".afk-install"
)

// marketplaceTrees are the embedded paths copied into the marketplace
// directory. Claude Code reads .claude-plugin/marketplace.json, Codex reads
// .agents/plugins/marketplace.json; both point at ./plugins/afk.
var marketplaceTrees = []string{".claude-plugin", ".agents", "plugins/afk"}

// openCodeTree is the embedded OpenCode port.
const openCodeTree = "plugins/afk-opencode"

// Deps are the environment seams. Production values come from DefaultDeps.
type Deps struct {
	Getenv   func(string) string
	HomeDir  func() (string, error)
	LookPath func(string) (string, error)
	// Run executes a command and returns its combined output.
	Run func(name string, args ...string) (string, error)
	Now func() time.Time
	Out io.Writer
}

// Options select what Install does.
type Options struct {
	Targets []Target
	DryRun  bool
	// Force replaces an OpenCode plugin directory this installer did not
	// write, after moving it to a backup.
	Force bool
}

// ParseTargets parses a --target value: "all" or a comma-separated list.
func ParseTargets(s string) ([]Target, error) {
	s = strings.TrimSpace(s)
	if s == "all" {
		return append([]Target(nil), AllTargets...), nil
	}
	var out []Target
	seen := map[Target]bool{}
	for _, part := range strings.Split(s, ",") {
		t := Target(strings.TrimSpace(part))
		if t == "" {
			continue
		}
		if !t.valid() {
			return nil, fmt.Errorf("unknown target %q (want claude, codex, opencode, or all)", t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no targets given")
	}
	return out, nil
}

func (t Target) valid() bool {
	for _, a := range AllTargets {
		if t == a {
			return true
		}
	}
	return false
}

// Detect returns the targets whose CLI is on PATH, in AllTargets order.
func Detect(lookPath func(string) (string, error)) []Target {
	var out []Target
	for _, t := range AllTargets {
		if _, err := lookPath(string(t)); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// MarketplaceDir is $XDG_DATA_HOME/afk/marketplace, else
// ~/.local/share/afk/marketplace. XDG is read by hand for the same reason
// internal/config does: os.UserConfigDir ignores XDG on macOS.
func MarketplaceDir(d Deps) (string, error) {
	base, err := xdgDir(d, "XDG_DATA_HOME", ".local/share")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "afk", "marketplace"), nil
}

// OpenCodeConfigDir is $XDG_CONFIG_HOME/opencode, else ~/.config/opencode.
func OpenCodeConfigDir(d Deps) (string, error) {
	base, err := xdgDir(d, "XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "opencode"), nil
}

func xdgDir(d Deps, env, fallback string) (string, error) {
	if v := d.Getenv(env); v != "" {
		return v, nil
	}
	home, err := d.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, fallback), nil
}

// Install installs the plugin into every target in opts. A failing target
// does not stop the others; the returned error lists every failure.
func Install(assets fs.FS, opts Options, d Deps) error {
	var failed []string
	needMarketplace := false
	for _, t := range opts.Targets {
		if t == Claude || t == Codex {
			needMarketplace = true
		}
	}

	var mpDir string
	if needMarketplace {
		dir, err := MarketplaceDir(d)
		if err == nil {
			err = writeManaged(assets, marketplaceTrees, dir, opts, d, "marketplace")
		}
		if err != nil {
			fmt.Fprintf(d.Out, "marketplace: %v\n", err)
			return fmt.Errorf("write marketplace: %w", err)
		}
		mpDir = dir
	}

	for _, t := range opts.Targets {
		var err error
		switch t {
		case Claude:
			err = installClaude(mpDir, opts, d)
		case Codex:
			err = installCodex(mpDir, opts, d)
		case OpenCode:
			err = installOpenCode(assets, opts, d)
		}
		if err != nil {
			fmt.Fprintf(d.Out, "%s: FAILED: %v\n", t, err)
			failed = append(failed, string(t))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("install failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

func installClaude(mpDir string, opts Options, d Deps) error {
	return installViaCLI("claude", mpDir, opts, d,
		[]string{"plugin", "marketplace", "add", mpDir},
		[]string{"plugin", "install", PluginID},
		OtherClaudeInstalls,
		func(id string) string {
			return "claude plugin uninstall " + id + " (add -s project from that project for project-scope installs)"
		},
	)
}

func installCodex(mpDir string, opts Options, d Deps) error {
	return installViaCLI("codex", mpDir, opts, d,
		[]string{"plugin", "marketplace", "add", mpDir},
		[]string{"plugin", "add", PluginID},
		OtherCodexInstalls,
		func(id string) string { return "codex plugin remove " + id },
	)
}

// installViaCLI registers the marketplace and installs the plugin with a CLI
// that owns its own plugin state. Both steps are idempotent in Claude Code
// and Codex, so a re-install re-runs them unchanged.
func installViaCLI(cli, mpDir string, opts Options, d Deps, addArgs, installArgs []string,
	others func(string) []string, removeHint func(string) string) error {
	if _, err := d.LookPath(cli); err != nil {
		return fmt.Errorf("%s not found on PATH", cli)
	}
	for _, args := range [][]string{addArgs, installArgs} {
		line := cli + " " + strings.Join(args, " ")
		if opts.DryRun {
			fmt.Fprintf(d.Out, "%s: would run: %s\n", cli, line)
			continue
		}
		if out, err := d.Run(cli, args...); err != nil {
			return fmt.Errorf("%s: %v\n%s", line, err, strings.TrimSpace(out))
		}
	}
	if !opts.DryRun {
		fmt.Fprintf(d.Out, "%s: installed %s from %s\n", cli, PluginID, mpDir)
	}
	// Warn, never uninstall: another afk plugin is the user's to remove.
	if out, err := d.Run(cli, "plugin", "list"); err == nil {
		for _, id := range others(out) {
			fmt.Fprintf(d.Out, "%s: warning: %s is also installed and registers the same skills; remove it with: %s\n", cli, id, removeHint(id))
		}
	}
	return nil
}

func installOpenCode(assets fs.FS, opts Options, d Deps) error {
	cfgDir, err := OpenCodeConfigDir(d)
	if err != nil {
		return err
	}
	dest := filepath.Join(cfgDir, "plugins", PluginName)
	if err := writeManaged(assets, []string{openCodeTree}, dest, opts, d, "opencode"); err != nil {
		return err
	}
	if !opts.DryRun {
		fmt.Fprintf(d.Out, "opencode: installed %s (loads at the next OpenCode start)\n", dest)
	}
	resource := dest + "/*"
	if home, err := d.HomeDir(); err == nil && strings.HasPrefix(resource, home+"/") {
		resource = "~" + strings.TrimPrefix(resource, home)
	}
	if !openCodePermissionPresent(cfgDir, resource) {
		fmt.Fprintf(d.Out, "opencode: note: afk sub-agents read prompt files under the plugin. To skip the permission prompts, add to %s:\n%s\n",
			filepath.Join(cfgDir, "opencode.jsonc"), OpenCodePermissionSnippet(resource))
	}
	return nil
}

// OpenCodePermissionSnippet is the opencode.jsonc entry that lets afk's
// sub-agent sessions read the plugin's prompt files without asking.
func OpenCodePermissionSnippet(resource string) string {
	return fmt.Sprintf(`  "permissions": [
    { "action": "external_directory", "resource": %q, "effect": "allow" },
    { "action": "read", "resource": %q, "effect": "allow" },
  ],`, resource, resource)
}

// openCodePermissionPresent is a text check, not a JSONC parse: any config
// that mentions the plugin path is assumed to already grant it.
func openCodePermissionPresent(cfgDir, resource string) bool {
	needle := []byte(strings.TrimSuffix(resource, "/*"))
	for _, name := range []string{"opencode.jsonc", "opencode.json", "config.json"} {
		b, err := os.ReadFile(filepath.Join(cfgDir, name))
		if err == nil && bytes.Contains(b, needle) {
			return true
		}
	}
	return false
}

// writeManaged replaces dest with the embedded trees (each tree's contents
// land at the same relative path under dest, except a single tree, whose
// contents become dest itself). A dest that exists without the marker was not
// written by this installer: it is refused unless opts.Force, and then moved
// to a timestamped backup next to it rather than deleted.
func writeManaged(assets fs.FS, trees []string, dest string, opts Options, d Deps, label string) error {
	_, statErr := os.Stat(dest)
	exists := statErr == nil
	managed := false
	if exists {
		_, err := os.Stat(filepath.Join(dest, markerFile))
		managed = err == nil
	}

	var backup string
	if exists && !managed {
		if !opts.Force {
			return fmt.Errorf("%s exists and was not installed by afk; re-run with --force to back it up and replace it", dest)
		}
		backup = fmt.Sprintf("%s.backup-%s", dest, d.Now().Format("20060102-150405"))
		if label == "opencode" {
			// A sibling under plugins/ would be loaded as a second plugin.
			backup = filepath.Join(filepath.Dir(filepath.Dir(dest)), "plugin-backups", filepath.Base(backup))
		}
	}

	if opts.DryRun {
		switch {
		case backup != "":
			fmt.Fprintf(d.Out, "%s: would move %s to %s\n", label, dest, backup)
		case exists:
			fmt.Fprintf(d.Out, "%s: would replace %s\n", label, dest)
		}
		fmt.Fprintf(d.Out, "%s: would write %s\n", label, dest)
		return nil
	}

	// Stage outside dest's parent when that parent is OpenCode's plugins
	// directory, so a half-written tree is never picked up as a plugin.
	stageParent := filepath.Dir(dest)
	if label == "opencode" {
		stageParent = filepath.Dir(stageParent)
	}
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(stageParent, ".afk-staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	newTree := filepath.Join(stage, "new")
	for _, tree := range trees {
		rel := tree
		if len(trees) == 1 {
			rel = "."
		}
		if err := copyTree(assets, tree, filepath.Join(newTree, rel)); err != nil {
			return fmt.Errorf("extract %s: %w", tree, err)
		}
	}
	marker := "Installed by `afk plugin install` on " + d.Now().Format(time.RFC3339) + ".\nThis directory is replaced on every install; do not edit it.\n"
	if err := os.WriteFile(filepath.Join(newTree, markerFile), []byte(marker), 0o644); err != nil {
		return err
	}

	if backup != "" {
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return err
		}
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("back up %s: %w", dest, err)
		}
		fmt.Fprintf(d.Out, "%s: moved the existing %s to %s\n", label, dest, backup)
	} else if exists {
		if err := os.Rename(dest, filepath.Join(stage, "old")); err != nil {
			return fmt.Errorf("replace %s: %w", dest, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.Rename(newTree, dest)
}

// copyTree writes the embedded tree at root into dst. embed.FS drops file
// modes, so shell scripts (by extension or shebang) get 0755: Claude Code
// runs the hooks by path.
func copyTree(assets fs.FS, root, dst string) error {
	return fs.WalkDir(assets, root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, fileMode(path.Base(p), b))
	})
}

func fileMode(name string, content []byte) os.FileMode {
	if strings.HasSuffix(name, ".sh") || bytes.HasPrefix(content, []byte("#!")) {
		return 0o755
	}
	return 0o644
}

// OtherClaudeInstalls returns the afk@<marketplace> ids in `claude plugin
// list` output other than this installer's own, deduplicated and sorted.
// Claude Code prints one block per install scope, so the same id can repeat.
func OtherClaudeInstalls(out string) []string {
	ids := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, PluginName+"@") && f != PluginID {
				ids[f] = true
			}
		}
	}
	return sortedKeys(ids)
}

// OtherCodexInstalls returns installed afk@<marketplace> ids in `codex
// plugin list` output other than this installer's own. Codex lists every
// available plugin, so rows marked "not installed" are skipped.
func OtherCodexInstalls(out string) []string {
	ids := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], PluginName+"@") || fields[0] == PluginID {
			continue
		}
		status := strings.ToLower(strings.Join(fields[1:], " "))
		if strings.Contains(status, "not installed") || !strings.Contains(status, "installed") {
			continue
		}
		ids[fields[0]] = true
	}
	return sortedKeys(ids)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TargetList renders targets for messages.
func TargetList(ts []Target) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}
