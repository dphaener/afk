package usagestate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// testDeps points every path at dir, so no test reaches the real home.
func testDeps(dir string) Deps {
	return Deps{
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return dir
			}
			return ""
		},
		HomeDir: func() (string, error) { return filepath.Join(dir, "home"), nil },
	}
}

// TestPath: the relative-XDG row is load-bearing, since a relative value must
// be ignored and fall back to the home path; the unset row pins the default.
func TestPath(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		want string
	}{
		{"xdg set", "/state", "/state/afk/usage.json"},
		{"xdg unset", "", "/h/.local/state/afk/usage.json"},
		{"xdg relative is ignored", "rel/state", "/h/.local/state/afk/usage.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Deps{
				Getenv:  func(string) string { return tt.xdg },
				HomeDir: func() (string, error) { return "/h", nil },
			}
			got, err := Path(d)
			if err != nil || got != tt.want {
				t.Fatalf("Path = %q, %v; want %q", got, err, tt.want)
			}
			dir, _ := Dir(d)
			if dir != filepath.Dir(tt.want) {
				t.Errorf("Dir = %q", dir)
			}
		})
	}
}

func TestPathHomeError(t *testing.T) {
	d := Deps{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	if _, err := Path(d); err == nil {
		t.Fatal("want error")
	}
}

func sample() provider.UsageSnapshot {
	reset := time.Date(2026, 10, 11, 18, 0, 0, 0, time.FixedZone("x", 3600))
	return provider.UsageSnapshot{
		At: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
		Windows: []provider.Window{
			{Kind: provider.WindowSession, UsedPct: 30, ResetsAt: reset},
			{Kind: provider.WindowWeekly, UsedPct: 71}, // zero reset
			{Kind: provider.WindowWeeklyModel, UsedPct: 13, ResetsAt: reset, Scope: "Fable"},
		},
		OverageEnabled: true,
	}
}

// TestRoundTrip: the zero-reset and scope rows are load-bearing; a zero time
// must survive as zero (not year 1 on disk) and a scope must not be dropped.
func TestRoundTrip(t *testing.T) {
	in := sample()
	f := FromSnapshot("prov", in)
	if f.Source != SourceProbe || f.Provider != "prov" {
		t.Fatalf("meta = %+v", f)
	}
	if f.Windows[1].ResetsAt != nil {
		t.Error("zero reset should be nil")
	}
	if f.Windows[0].ResetsAt.Location() != time.UTC {
		t.Error("reset not UTC")
	}
	out := f.Snapshot()
	if !out.At.Equal(in.At) || out.OverageEnabled != in.OverageEnabled || len(out.Windows) != 3 {
		t.Fatalf("out = %+v", out)
	}
	for i, w := range in.Windows {
		g := out.Windows[i]
		if g.Kind != w.Kind || g.UsedPct != w.UsedPct || g.Scope != w.Scope || !g.ResetsAt.Equal(w.ResetsAt) {
			t.Errorf("window %d = %+v, want %+v", i, g, w)
		}
	}
	if empty := FromSnapshot("p", provider.UsageSnapshot{At: in.At}); empty.Windows == nil {
		t.Error("empty windows should be a non-nil slice")
	}
}

func TestWriteReadAndKeys(t *testing.T) {
	dir := t.TempDir()
	d := testDeps(dir)
	f := FromSnapshot("prov", sample())
	if err := Write(d, f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "afk", "usage.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Error("missing trailing newline")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"at", "provider", "windows", "overage_enabled", "source"} {
		if _, ok := top[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if len(top) != 5 {
		t.Errorf("top-level keys = %d, want 5", len(top))
	}
	var ws []map[string]any
	_ = json.Unmarshal(top["windows"], &ws)
	if _, ok := ws[1]["resets_at"]; ok {
		t.Error("nil resets_at should be omitted")
	}
	if _, ok := ws[0]["scope"]; ok {
		t.Error("empty scope should be omitted")
	}
	if ws[2]["scope"] != "Fable" || ws[0]["kind"] != "session" || ws[0]["used_pct"] != 30.0 || ws[0]["resets_at"] == nil {
		t.Errorf("windows = %v", ws)
	}

	got, ok := Read(d)
	if !ok {
		t.Fatal("Read false after Write")
	}
	if !got.At.Equal(f.At) || len(got.Windows) != 3 || got.Windows[2].Scope != "Fable" || got.Windows[1].ResetsAt != nil {
		t.Errorf("got = %+v", got)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "afk"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestReadBadFiles: every row must report "no snapshot" rather than an error;
// the unknown-key row must succeed so an older binary reads a newer file.
func TestReadBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		content *string
		want    bool
	}{
		{"missing file", nil, false},
		{"garbage", ptr("not json {{"), false},
		{"empty file", ptr(""), false},
		{"zero at", ptr(`{"at":"0001-01-01T00:00:00Z","provider":"p","windows":[]}`), false},
		{"absent at", ptr(`{"provider":"p","windows":[]}`), false},
		{"unknown key", ptr(`{"at":"2026-10-11T12:00:00Z","provider":"p","windows":[],"future":{"a":1}}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			d := testDeps(dir)
			if tt.content != nil {
				if err := os.MkdirAll(filepath.Join(dir, "afk"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "afk", "usage.json"), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, ok := Read(d)
			if ok != tt.want {
				t.Errorf("Read ok = %v, want %v", ok, tt.want)
			}
		})
	}
}

// TestWriteRenameFailure: the failed rename must surface an error and remove
// the temp file, or each failure would leave litter in the state directory.
func TestWriteRenameFailure(t *testing.T) {
	dir := t.TempDir()
	d := testDeps(dir)
	d.Rename = func(string, string) error { return errors.New("rename boom") }
	err := Write(d, FromSnapshot("prov", sample()))
	if err == nil || !strings.Contains(err.Error(), "rename boom") {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "afk"))
	if len(entries) != 0 {
		t.Errorf("files left after failed rename: %v", entries)
	}
	if _, ok := Read(d); ok {
		t.Error("no snapshot expected after failed write")
	}
}

func ptr(s string) *string { return &s }
