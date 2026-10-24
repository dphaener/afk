package progstatus

import (
	"reflect"
	"testing"
)

// FuzzScanner checks the property the host relies on: for any output and
// any split point, scanning it in two chunks finds the same reports at the
// same absolute ends as scanning it whole, ends in the same safe state, and
// never holds more than MaxSequence bytes. The seeds (spec examples, both
// endings, the size edges, unended and aborted sequences, split UTF-8) run
// under plain `go test`.
func FuzzScanner(f *testing.F) {
	stream, _ := specStream()
	seeds := []string{
		string(stream),
		sizedReport(4096, true),
		sizedReport(4097, true),
		sizedReport(4096, false),
		sizedReport(4097, false),
		"\x1b]7501;state=working:app=x:pad=aaaaaaaa",
		"\x1b]7501;state=idle\x18\x1b]7501;state=done\x1b\\",
		"\x1b]7501;?\x07",
		"\x1b]7501;state=idle\x01\x1b\\",
		"\x1b[31mred\x1b[0m\x1bPq\x07\x1b\\\x1b_G\x1b\\",
		"caf\xc3\xa9 \xf0\x9f\x98\x80 \xe2\x1b[m\x9b",
		"\x1b]7501;state=idle\x1b\x1b]7501;state=done\x07",
	}
	for _, s := range seeds {
		for _, k := range []int{0, 1, len(s) / 2, len(s) - 1} {
			if k >= 0 && k <= len(s) {
				f.Add([]byte(s), uint16(k))
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, split uint16) {
		k := int(split)
		if k > len(data) {
			k = len(data) % (k + 1)
		}
		whole := NewScanner()
		want := scanChunks(whole, data)
		parts := NewScanner()
		got := scanChunks(parts, data, k)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split at %d:\n got %+v\nwant %+v", k, got, want)
		}
		if whole.Safe() != parts.Safe() {
			t.Fatalf("split at %d: Safe %v, whole %v", k, parts.Safe(), whole.Safe())
		}
		if cap(whole.body) > MaxSequence || cap(parts.body) > MaxSequence {
			t.Fatalf("buffer capacity %d / %d over %d", cap(whole.body), cap(parts.body), MaxSequence)
		}
		for _, r := range got {
			if r.End < 1 || r.End > len(data) {
				t.Fatalf("End %d outside the input of %d bytes", r.End, len(data))
			}
		}
	})
}
