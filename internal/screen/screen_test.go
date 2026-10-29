package screen

import (
	"fmt"
	"strings"
	"testing"
)

// TestTail: the mid-line row is load-bearing, since a split tag would render
// as text; the boundary row checks a window that starts exactly on a line.
func TestTail(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"empty", "", 10, ""},
		{"shorter than limit", "abc\ndef", 100, "abc\ndef"},
		{"exact limit", "abc\ndef", 7, "abc\ndef"},
		{"mid line cuts forward", "aaaa\nbbbb\ncccc", 8, "cccc"},
		{"window starts on line", "aaaa\nbbbb", 4, "bbbb"},
		{"no newline in window", "aaaa\nbbbbbbbb", 5, "bbbbb"},
		{"zero limit", "abc", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Tail([]byte(c.in), c.limit)
			if got == nil || string(got) != c.want {
				t.Fatalf("Tail(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
			}
		})
	}
}

func TestPalette(t *testing.T) {
	p := Palette()
	for i := 0; i < 256; i++ {
		if !strings.Contains(p, fmt.Sprintf("--vt-palette-%d: #", i)) {
			t.Fatalf("palette %d missing", i)
		}
	}
	for _, want := range []string{"--vt-palette-255: #eeeeee", "--vt-palette-16: #000000", "--vt-palette-231: #ffffff", "--vt-palette-9: #ff0000"} {
		if !strings.Contains(p, want) {
			t.Errorf("palette lacks %q", want)
		}
	}
}

func TestDocument(t *testing.T) {
	out := string(Document([]byte("<b>hi</b>")))
	for _, want := range []string{`<meta charset="utf-8">`, `name="viewport"`, "--vt-palette-255", "<b>hi</b>"} {
		if !strings.Contains(out, want) {
			t.Errorf("document lacks %q", want)
		}
	}
	if strings.Contains(out, "<script") {
		t.Error("document contains a script")
	}
}
