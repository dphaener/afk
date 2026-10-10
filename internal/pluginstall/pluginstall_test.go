package pluginstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestParseTargets: "all" must expand to every target in install order, and
// an unknown name must fail rather than be skipped, so a typo never turns into
// a silent partial install.
func TestParseTargets(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []Target
		wantErr bool
	}{
		{"all", "all", AllTargets, false},
		{"one", "codex", []Target{Codex}, false},
		{"list with spaces and dupes", "opencode, claude,opencode", []Target{OpenCode, Claude}, false},
		{"unknown", "claude,cursor", nil, true},
		{"empty", " , ", nil, true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, err := ParseTargets(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "codex" || name == "opencode" {
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	if got := Detect(look); !reflect.DeepEqual(got, []Target{Codex, OpenCode}) {
		t.Errorf("Detect = %v", got)
	}
}

// TestOtherInstalls uses the real output shapes. Load-bearing rows: Claude
// repeats an id once per scope (must dedupe), Codex lists uninstalled
// marketplace plugins (must skip "not installed"), and our own id is never a
// conflict.
func TestOtherInstalls(t *testing.T) {
	claudeOut := `Installed plugins:

  ❯ afk@claude-marche
    Version: 3.7.0
    Scope: user

  ❯ afk@claude-marche
    Scope: project

  ❯ afk@afk
    Scope: user

  ❯ afkish@x
`
	if got := OtherClaudeInstalls(claudeOut); !reflect.DeepEqual(got, []string{"afk@claude-marche"}) {
		t.Errorf("claude = %v", got)
	}

	codexOut := `afk@marche                        installed, enabled  2.10.0   /p
afk@other                         not installed                /q
afk@afk                     installed, enabled  3.7.0    /r
partafk@openai-curated-remote     installed                    x
`
	if got := OtherCodexInstalls(codexOut); !reflect.DeepEqual(got, []string{"afk@marche"}) {
		t.Errorf("codex = %v", got)
	}
}

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		".claude-plugin/marketplace.json":        {Data: []byte(`{"name":"afk"}`)},
		".agents/plugins/marketplace.json":       {Data: []byte(`{"name":"afk"}`)},
		"plugins/afk/.claude-plugin/plugin.json": {Data: []byte(`{}`)},
		"plugins/afk/hooks/on-stop.sh":           {Data: []byte("echo hi\n")},
		"plugins/afk/scripts/plan_to_args.rb":    {Data: []byte("#!/usr/bin/env ruby\n")},
		"plugins/afk/README.md":                  {Data: []byte("readme")},
		"plugins/afk-opencode/index.ts":          {Data: []byte("export default {}")},
		"plugins/afk-opencode/lib/engine.mjs":    {Data: []byte("x")},
	}
}

type fakeEnv struct {
	home, xdgConfig, xdgData string
	onPath                   map[string]bool
	ran                      []string
	listOut                  map[string]string
	out                      bytes.Buffer
}

func newFakeEnv(t *testing.T) *fakeEnv {
	root := t.TempDir()
	return &fakeEnv{
		home:      root,
		xdgConfig: filepath.Join(root, "cfg"),
		xdgData:   filepath.Join(root, "data"),
		onPath:    map[string]bool{"claude": true, "codex": true, "opencode": true},
		listOut:   map[string]string{},
	}
}

func (f *fakeEnv) deps() Deps {
	return Deps{
		Getenv: func(k string) string {
			switch k {
			case "XDG_CONFIG_HOME":
				return f.xdgConfig
			case "XDG_DATA_HOME":
				return f.xdgData
			}
			return ""
		},
		HomeDir: func() (string, error) { return f.home, nil },
		LookPath: func(n string) (string, error) {
			if f.onPath[n] {
				return "/bin/" + n, nil
			}
			return "", errors.New("missing")
		},
		Run: func(name string, args ...string) (string, error) {
			line := name + " " + strings.Join(args, " ")
			if strings.HasSuffix(line, "plugin list") {
				return f.listOut[name], nil
			}
			f.ran = append(f.ran, line)
			return "", nil
		},
		Now: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) },
		Out: &f.out,
	}
}

// TestInstallAllTargets pins the end state of a fresh install: the
// marketplace tree with both manifests and executable shell scripts (embed
// drops modes, and Claude Code runs hooks by path), the CLI commands in
// order, and the OpenCode port at plugins/afk with no staging leftovers.
func TestInstallAllTargets(t *testing.T) {
	f := newFakeEnv(t)
	f.listOut["claude"] = "  ❯ afk@claude-marche\n"
	if err := Install(testAssets(), Options{Targets: AllTargets}, f.deps()); err != nil {
		t.Fatalf("Install: %v\n%s", err, f.out.String())
	}

	mp := filepath.Join(f.xdgData, "afk", "marketplace")
	for _, p := range []string{".claude-plugin/marketplace.json", ".agents/plugins/marketplace.json", "plugins/afk/README.md", markerFile} {
		if _, err := os.Stat(filepath.Join(mp, p)); err != nil {
			t.Errorf("marketplace missing %s", p)
		}
	}
	for p, want := range map[string]os.FileMode{
		"plugins/afk/hooks/on-stop.sh":        0o755,
		"plugins/afk/scripts/plan_to_args.rb": 0o755,
		"plugins/afk/README.md":               0o644,
	} {
		st, err := os.Stat(filepath.Join(mp, p))
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s mode = %v (err %v), want %v", p, st.Mode().Perm(), err, want)
		}
	}

	wantRan := []string{
		"claude plugin marketplace add " + mp,
		"claude plugin install afk@afk",
		"codex plugin marketplace add " + mp,
		"codex plugin add afk@afk",
	}
	if !reflect.DeepEqual(f.ran, wantRan) {
		t.Errorf("ran %q\nwant %q", f.ran, wantRan)
	}
	if !strings.Contains(f.out.String(), "afk@claude-marche is also installed") {
		t.Errorf("missing conflict warning:\n%s", f.out.String())
	}

	oc := filepath.Join(f.xdgConfig, "opencode", "plugins", "afk")
	for _, p := range []string{"index.ts", "lib/engine.mjs", markerFile} {
		if _, err := os.Stat(filepath.Join(oc, p)); err != nil {
			t.Errorf("opencode missing %s", p)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.xdgConfig, "opencode", "plugins"))
	if len(entries) != 1 {
		t.Errorf("plugins dir has %d entries, want only afk", len(entries))
	}
}

// TestInstallOpenCodeGuard is the safety contract for OpenCode: a plugin
// directory afk did not write is refused without --force, moved (never
// deleted) with it, and a afk-written one is replaced freely on re-install.
func TestInstallOpenCodeGuard(t *testing.T) {
	f := newFakeEnv(t)
	oc := filepath.Join(f.xdgConfig, "opencode", "plugins", "afk")
	if err := os.MkdirAll(oc, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(oc, "mine.txt"), []byte("user work"), 0o644)
	opts := Options{Targets: []Target{OpenCode}}

	if err := Install(testAssets(), opts, f.deps()); err == nil {
		t.Fatal("want refusal without --force")
	}
	if _, err := os.Stat(filepath.Join(oc, "mine.txt")); err != nil {
		t.Fatal("refused install must leave the directory alone")
	}

	opts.Force = true
	if err := Install(testAssets(), opts, f.deps()); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(f.xdgConfig, "opencode", "plugin-backups", "afk.backup-20261009-120000", "mine.txt")
	if b, err := os.ReadFile(backup); err != nil || string(b) != "user work" {
		t.Fatalf("backup = %q, %v", b, err)
	}

	os.WriteFile(filepath.Join(oc, "stale.txt"), []byte("x"), 0o644)
	if err := Install(testAssets(), Options{Targets: []Target{OpenCode}}, f.deps()); err != nil {
		t.Fatalf("re-install of a managed dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oc, "stale.txt")); err == nil {
		t.Error("re-install must replace the tree, not merge into it")
	}
}

func TestInstallDryRunChangesNothing(t *testing.T) {
	f := newFakeEnv(t)
	if err := Install(testAssets(), Options{Targets: AllTargets, DryRun: true}, f.deps()); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 0 {
		t.Errorf("dry run ran %q", f.ran)
	}
	for _, d := range []string{f.xdgData, f.xdgConfig} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("dry run created %s", d)
		}
	}
}

// TestInstallMissingCLI: a target whose CLI is absent fails on its own; the
// other targets still install.
func TestInstallMissingCLI(t *testing.T) {
	f := newFakeEnv(t)
	f.onPath["codex"] = false
	err := Install(testAssets(), Options{Targets: []Target{Codex, OpenCode}}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.xdgConfig, "opencode", "plugins", "afk", "index.ts")); err != nil {
		t.Error("opencode should still install")
	}
}

func TestOpenCodePermissionNote(t *testing.T) {
	f := newFakeEnv(t)
	f.xdgConfig = filepath.Join(f.home, ".config")
	if err := Install(testAssets(), Options{Targets: []Target{OpenCode}}, f.deps()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), `"~/.config/opencode/plugins/afk/*"`) {
		t.Errorf("want a home-relative permission snippet:\n%s", f.out.String())
	}

	f.out.Reset()
	os.WriteFile(filepath.Join(f.xdgConfig, "opencode", "opencode.jsonc"), []byte(OpenCodePermissionSnippet("~/.config/opencode/plugins/afk/*")), 0o644)
	if err := Install(testAssets(), Options{Targets: []Target{OpenCode}}, f.deps()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.out.String(), "permissions") {
		t.Errorf("note printed although the config already grants it:\n%s", f.out.String())
	}
}
