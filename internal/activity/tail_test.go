package activity

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTail: the cut rows are load-bearing. A cut in mid-line must drop the
// partial first line (it would decode wrongly), while a cut landing exactly
// after a newline must keep the whole first line.
func TestTail(t *testing.T) {
	cases := []struct {
		name    string
		content string
		max     int64
		want    []string
	}{
		{"small file", "aaa\nbbb\n", 100, []string{"aaa", "bbb"}},
		{"empty file", "", 100, nil},
		{"cut mid-line", "aaaa\nbbbb\ncccc\n", 8, []string{"cccc"}},
		{"cut right after newline", "aaaa\nbbbb\ncccc\n", 10, []string{"bbbb", "cccc"}},
		{"cut right after newline keeps whole line", "aaaa\nbbbb\ncccc\n", 15, []string{"aaaa", "bbbb", "cccc"}},
		{"exactly max", "aaaa\nbbbb\n", 10, []string{"aaaa", "bbbb"}},
		{"no trailing newline", "aaaa\nbbbb\ncc", 100, []string{"aaaa", "bbbb", "cc"}},
		{"cut and no trailing newline", "aaaa\nbbbb\ncc", 6, []string{"cc"}},
		{"cut with no newline at all", "aaaaaaaaaaaa", 4, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "log")
			if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Tail(p, c.max)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		got, err := Tail(filepath.Join(t.TempDir(), "nope"), 100)
		if got != nil || err != nil {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("directory is an error", func(t *testing.T) {
		if _, err := Tail(t.TempDir(), 100); err == nil {
			t.Error("want an error")
		}
	})
}
