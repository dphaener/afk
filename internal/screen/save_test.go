package screen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/rex"
)

var testBlock = BlockRef{SessionID: "session:s1", WindowID: "window:w1", BlockID: "block:b1"}

func formatReply(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSave: the rows are load-bearing as follows. multi-part proves Save
// goes through Format's blob_read paging and keeps only the tail; the
// not-found row proves the typed error and that no file appears; the
// read-error row proves a failed read keeps the previous page.
func TestSave(t *testing.T) {
	notFound := &rex.CallError{Method: "block.method", Code: 3, Stderr: "block not found"}
	tests := []struct {
		name     string
		script   func(t *testing.T, f *rex.Fake)
		previous string
		wantErr  bool
		gone     bool
		check    func(t *testing.T, page string)
	}{
		{
			name: "single reply gets palette and no script",
			script: func(t *testing.T, f *rex.Fake) {
				f.On("block.method", formatReply(t, "<span>hello</span>"), nil)
			},
			check: func(t *testing.T, page string) {
				if !strings.Contains(page, "<span>hello</span>") || !strings.Contains(page, "--vt-palette-255") {
					t.Errorf("page lacks body or palette")
				}
				if strings.Contains(strings.ToLower(page), "<script") {
					t.Errorf("page has a script")
				}
			},
		},
		{
			name: "multi-part output keeps the tail",
			script: func(t *testing.T, f *rex.Fake) {
				first := strings.Repeat("a", 10) + "\n"
				second := strings.Repeat(strings.Repeat("b", 99)+"\n", 31000) + "THE-END"
				total := len(first) + len(second)
				// Rex's reply to the first call announces more; blob_read
				// parts follow. Fake matches args, so script both shapes.
				f.OnArgs("block.method", map[string]any{
					"session_id": "session:s1", "block_id": "block:b1", "method": "format",
					"args": map[string]any{"max_bytes": 983040, "format": "html"},
				}, mustMarshal(t, map[string]any{"content": first + second[:983040-len(first)], "id": 7, "total": total}), nil)
				got := len(first) + 983040 - len(first)
				for got < total {
					end := got + 983040
					if end > total {
						end = total
					}
					part := (first + second)[got:end]
					f.OnArgs("block.method", map[string]any{
						"session_id": "session:s1", "block_id": "block:b1", "method": "blob_read",
						"args": map[string]any{"id": 7, "offset": got, "max_bytes": 983040},
					}, mustMarshal(t, map[string]any{"data": part, "total": total, "eof": end == total}), nil)
					got = end
				}
			},
			check: func(t *testing.T, page string) {
				if !strings.Contains(page, "THE-END") {
					t.Errorf("tail missing")
				}
				if strings.Contains(page, strings.Repeat("a", 10)) {
					t.Errorf("head should have been cut")
				}
				if strings.Count(page, "b") < 2<<20-100000 {
					t.Errorf("tail too short")
				}
			},
		},
		{
			name:     "not found returns the typed error and writes nothing",
			script:   func(t *testing.T, f *rex.Fake) { f.On("block.method", "", notFound) },
			wantErr:  true,
			gone:     true,
			previous: "",
		},
		{
			name:     "blob_read failure keeps the previous page",
			previous: "OLD PAGE",
			wantErr:  true,
			script: func(t *testing.T, f *rex.Fake) {
				f.OnArgs("block.method", map[string]any{
					"session_id": "session:s1", "block_id": "block:b1", "method": "format",
					"args": map[string]any{"max_bytes": 983040, "format": "html"},
				}, mustMarshal(t, map[string]any{"content": "xx", "id": 1, "total": 100}), nil)
				f.On("block.method", "", &rex.CallError{Method: "block.method", Code: 1, Stderr: "boom"})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if tc.previous != "" {
				if err := os.WriteFile(path, []byte(tc.previous), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f := &rex.Fake{}
			tc.script(t, f)
			err := Save(context.Background(), f, testBlock, dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.gone && !errors.Is(err, ErrBlockGone) {
				t.Errorf("err = %v, want ErrBlockGone", err)
			}
			if !tc.gone && errors.Is(err, ErrBlockGone) {
				t.Errorf("err = %v, must not be ErrBlockGone", err)
			}
			got, rerr := os.ReadFile(path)
			if tc.wantErr {
				if tc.previous == "" && rerr == nil {
					t.Errorf("file written despite error")
				}
				if tc.previous != "" && string(got) != tc.previous {
					t.Errorf("previous page changed: %q", got)
				}
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					if strings.HasSuffix(e.Name(), ".tmp") {
						t.Errorf("temp file left: %s", e.Name())
					}
				}
				return
			}
			if rerr != nil {
				t.Fatal(rerr)
			}
			tc.check(t, string(got))
		})
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
