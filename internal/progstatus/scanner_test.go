package progstatus

import (
	"reflect"
	"strings"
	"testing"
)

// specBodies are the report bodies from the spec's Examples section, in
// order.
var specBodies = []string{
	"state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz",
	"state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz",
	"state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz",
	"state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=",
	"state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
	"state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==",
	"state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/",
	"state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==",
	"state=clear:id=us-east",
	"state=clear:id=eu-west",
	"state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z",
	"state=blocked:kind=permission:app=terraform:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/",
}

// absReport is a report with its End made absolute in the whole stream, so
// scans of the same stream in different chunks can be compared.
type absReport struct {
	Record
	End int
}

// specStream puts every spec example between plain text, CSI colour codes,
// a title OSC and a UTF-8 character, alternating the ST and BEL endings. It
// returns the stream and the reports a scanner must find in it.
func specStream() ([]byte, []absReport) {
	var b strings.Builder
	var want []absReport
	for i, body := range specBodies {
		b.WriteString("line é \x1b[1;31mred\x1b[0m \x1b]0;title\x07 ")
		b.WriteString("\x1b]7501;" + body)
		if i%2 == 0 {
			b.WriteString("\x1b\\")
		} else {
			b.WriteString("\x07")
		}
		r, ok := Parse([]byte(body))
		if !ok {
			panic("spec example does not parse: " + body)
		}
		want = append(want, absReport{r, b.Len()})
		b.WriteString("\r\n")
	}
	return []byte(b.String()), want
}

// scanChunks feeds data split at the given offsets and returns the reports
// with absolute ends.
func scanChunks(s *Scanner, data []byte, cuts ...int) []absReport {
	var got []absReport
	start := 0
	for _, c := range append(cuts, len(data)) {
		res := s.Scan(data[start:c])
		for _, r := range res.Reports {
			got = append(got, absReport{r.Record, start + r.End})
		}
		start = c
	}
	return got
}

// TestScanSpecStream checks that every spec example is found between other
// output with both endings, with the right records and ends. Load-bearing:
// the BEL rows (the spec allows BEL as ST and agents use it) and the title
// OSC before each report (a non-7501 OSC must not swallow the next one).
func TestScanSpecStream(t *testing.T) {
	stream, want := specStream()
	s := NewScanner()
	res := s.Scan(stream)
	var got []absReport
	for _, r := range res.Reports {
		got = append(got, absReport{r.Record, r.End})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reports:\n got %+v\nwant %+v", got, want)
	}
	if res.LastSafe != len(stream) || !s.Safe() {
		t.Errorf("LastSafe = %d, Safe = %v; want %d, true", res.LastSafe, s.Safe(), len(stream))
	}
	brew := Record{State: StateWorking, App: "brew", Msg: "Installing updates"}
	if got[0].Record != brew {
		t.Errorf("first report = %+v, want %+v", got[0].Record, brew)
	}
	region := Record{State: StateWorking, ID: "us-east", Title: "US East", Progress: 40, HasProgress: true, Msg: "Pushing image"}
	if got[5].Record != region {
		t.Errorf("us-east report = %+v, want %+v", got[5].Record, region)
	}
}

// TestScanSplitEveryByte re-feeds the spec stream split at every offset,
// and as single bytes. Load-bearing: the splits that fall between ESC and
// `\` of an ST and inside the "7501;" prefix, which a scanner that looked
// ahead within a chunk would get wrong. The single-byte row also checks
// that every report's end is safe.
func TestScanSplitEveryByte(t *testing.T) {
	stream, want := specStream()
	t.Run("two chunks at every offset", func(t *testing.T) {
		for k := 0; k <= len(stream); k++ {
			got := scanChunks(NewScanner(), stream, k)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("split at %d:\n got %+v\nwant %+v", k, got, want)
			}
		}
	})
	t.Run("single bytes", func(t *testing.T) {
		s := NewScanner()
		var got []absReport
		for i := range stream {
			res := s.Scan(stream[i : i+1])
			for _, r := range res.Reports {
				if r.End != 1 || res.LastSafe != 1 || !s.Safe() {
					t.Fatalf("report ending at %d: End %d, LastSafe %d, Safe %v", i+1, r.End, res.LastSafe, s.Safe())
				}
				got = append(got, absReport{r.Record, i + 1})
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("single bytes:\n got %+v\nwant %+v", got, want)
		}
	})
}

// TestScanUnended feeds an OSC 7501 sequence that never ends, followed by
// megabytes of legal body bytes. Load-bearing: the buffer must stay within
// MaxSequence and feeding more must not allocate, or a hostile program
// could grow the host's memory without bound; and a CAN must recover so a
// later report is still found.
func TestScanUnended(t *testing.T) {
	s := NewScanner()
	res := s.Scan([]byte("ok\x1b]7501;state=working:app=x:pad="))
	if res.LastSafe != 2 || s.Safe() || res.Reports != nil {
		t.Fatalf("start: %+v, Safe %v", res, s.Safe())
	}
	chunk := []byte(strings.Repeat("a", 64*1024))
	for i := 0; i < 48; i++ { // 3 MiB
		res := s.Scan(chunk)
		if res.Reports != nil || res.LastSafe != -1 || s.Safe() {
			t.Fatalf("chunk %d: %+v, Safe %v", i, res, s.Safe())
		}
		if c := cap(s.body); c > MaxSequence {
			t.Fatalf("buffer capacity %d over %d", c, MaxSequence)
		}
	}
	if allocs := testing.AllocsPerRun(10, func() { s.Scan(chunk) }); allocs != 0 {
		t.Errorf("feeding an unended body allocates %v times per chunk", allocs)
	}
	res = s.Scan([]byte("\x1b\\"))
	if res.Reports != nil {
		t.Errorf("an oversized report was returned: %+v", res.Reports)
	}

	s = NewScanner()
	s.Scan([]byte("\x1b]7501;state=idle" + strings.Repeat("b", 10000)))
	res = s.Scan([]byte("\x18then\x1b]7501;state=done\x1b\\"))
	want := []Report{{Record{State: StateDone}, 24}}
	if !reflect.DeepEqual(res.Reports, want) || !s.Safe() {
		t.Errorf("after CAN: %+v, Safe %v; want %+v", res.Reports, s.Safe(), want)
	}
}

// sizedReport builds a legal report exactly n bytes long in all, padded
// with an unknown key whose value is legal.
func sizedReport(n int, st bool) string {
	term := "\x07"
	if st {
		term = "\x1b\\"
	}
	head := "\x1b]7501;state=idle:pad="
	return head + strings.Repeat("a", n-len(head)-len(term)) + term
}

// TestScanOversize checks the whole-sequence limit at its edge for both
// endings. Load-bearing: the 4097 ST row (its body fits the BEL-sized
// buffer exactly, so only the terminator check drops it) and the recovery
// row (a dropped report must not take the next one with it).
func TestScanOversize(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		st    bool
		found bool
	}{
		{"4096 with ST found", 4096, true, true},
		{"4096 with BEL found", 4096, false, true},
		{"4097 with ST dropped", 4097, true, false},
		{"4097 with BEL dropped", 4097, false, false},
		{"far over dropped", 9000, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := sizedReport(tt.size, tt.st)
			if len(rep) != tt.size {
				t.Fatalf("built %d bytes, want %d", len(rep), tt.size)
			}
			s := NewScanner()
			stream := rep + "\x1b]7501;state=done\x1b\\"
			res := s.Scan([]byte(stream))
			var want []Report
			if tt.found {
				want = append(want, Report{Record{State: StateIdle}, tt.size})
			}
			want = append(want, Report{Record{State: StateDone}, len(stream)})
			if !reflect.DeepEqual(res.Reports, want) {
				t.Errorf("reports %+v, want %+v", res.Reports, want)
			}
			if c := cap(s.body); c > MaxSequence {
				t.Errorf("buffer capacity %d over %d", c, MaxSequence)
			}
		})
	}
}

// TestScanMalformed checks that sequences which are not good reports give
// nothing and leave the stream safe after them. Load-bearing: the control
// byte row (dropped by the scanner, since Parse never sees it as a pair
// problem), "?" (a query, not a report) and the near-miss prefixes (another
// OSC must never be read as 7501).
func TestScanMalformed(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"no state", "\x1b]7501;app=brew\x1b\\"},
		{"unknown state", "\x1b]7501;state=sleeping\x07"},
		{"bad base64", "\x1b]7501;state=idle:msg=QUJDR\x1b\\"},
		{"control byte in body", "\x1b]7501;state=idle\x01\x1b\\"},
		{"newline in body", "\x1b]7501;state=idle\n:app=x\x07"},
		{"feature detection query", "\x1b]7501;?\x1b\\"},
		{"no semicolon", "\x1b]7501\x1b\\"},
		{"other OSC number", "\x1b]7502;state=idle\x1b\\"},
		{"longer number", "\x1b]75011;state=idle\x1b\\"},
		{"inside DCS body", "\x1bP]7501;state=idle\x1b\\"},
		{"aborted by ESC", "\x1b]7501;state=idle\x1b[m"},
		{"aborted by SUB", "\x1b]7501;state=idle\x1a\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScanner()
			res := s.Scan([]byte(tt.in))
			if res.Reports != nil {
				t.Errorf("got reports %+v, want none", res.Reports)
			}
			if !s.Safe() {
				t.Errorf("stream not safe after %q", tt.in)
			}
		})
	}
}

// TestScanSafe checks LastSafe per chunk and Safe at the end. Load-bearing:
// the rows whose chunk ends inside a sequence or a character (each must
// leave Safe false and put LastSafe before it), the ST split between ESC
// and `\`, the BEL inside DCS (data, not a terminator) and the C1 byte
// (data in UTF-8, not CSI).
func TestScanSafe(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		last   []int
		safe   bool
	}{
		{"plain text", []string{"hello"}, []int{5}, true},
		{"empty chunk", []string{""}, []int{0}, true},
		{"mid CSI", []string{"ab\x1b[31", "m"}, []int{2, 1}, true},
		{"chunk wholly inside CSI", []string{"\x1b[", "31", "m"}, []int{0, -1, 1}, true},
		{"ends mid CSI", []string{"x\x1b[3"}, []int{1}, false},
		{"mid OSC 0 title", []string{"x\x1b]0;tit", "le\x07y"}, []int{1, 4}, true},
		{"mid OSC 7501", []string{"\x1b]7501;sta", "te=idle\x1b\\z"}, []int{0, 10}, true},
		{"ST split", []string{"\x1b]7501;state=idle\x1b", "\\"}, []int{0, 1}, true},
		{"mid DCS", []string{"\x1bPq#0;", "data\x1b\\z"}, []int{0, 7}, true},
		{"BEL inside DCS is data", []string{"\x1bPq\x07", "x\x1b\\"}, []int{0, 3}, true},
		{"mid APC", []string{"\x1b_Gf=1", ";x\x1b\\"}, []int{0, 4}, true},
		{"mid SOS and PM", []string{"\x1bXa\x1b\\\x1b^b", "\x1b\\"}, []int{5, 2}, true},
		{"ESC alone at end", []string{"abc\x1b"}, []int{3}, false},
		{"ESC intermediate", []string{"\x1b(", "B"}, []int{0, 1}, true},
		{"ESC final", []string{"\x1b7x"}, []int{3}, true},
		{"e acute split", []string{"caf\xc3", "\xa9!"}, []int{3, 2}, true},
		{"4-byte emoji split", []string{"\xf0\x9f", "\x98\x80"}, []int{0, 2}, true},
		{"emoji split after lead", []string{"a\xf0", "\x9f\x98", "\x80"}, []int{1, -1, 1}, true},
		{"malformed continuation resets", []string{"\xc3("}, []int{2}, true},
		{"truncated lead then ESC sequence", []string{"\xe2\x1b[m"}, []int{4}, true},
		{"overlong lead byte is data", []string{"\xc0\xaf"}, []int{2}, true},
		{"ED lead owes bytes", []string{"\xed", "\x9f\xbf"}, []int{0, 2}, true},
		{"surrogate is malformed", []string{"\xed", "\xa0"}, []int{0, 1}, true},
		{"stray continuation", []string{"\x80a"}, []int{2}, true},
		{"C1 CSI byte is data", []string{"\x9b31m"}, []int{4}, true},
		{"CAN aborts CSI", []string{"\x1b[31\x18"}, []int{5}, true},
		{"SUB aborts DCS", []string{"\x1bPq\x1a"}, []int{4}, true},
		{"ESC inside OSC restarts", []string{"\x1b]0;t\x1b[1", "m"}, []int{0, 1}, true},
		{"C0 inside CSI keeps it", []string{"\x1b[3\r", "1m"}, []int{0, 2}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScanner()
			for i, c := range tt.chunks {
				if got := s.Scan([]byte(c)).LastSafe; got != tt.last[i] {
					t.Errorf("chunk %d %q: LastSafe = %d, want %d", i, c, got, tt.last[i])
				}
			}
			if s.Safe() != tt.safe {
				t.Errorf("Safe = %v, want %v", s.Safe(), tt.safe)
			}
		})
	}
}

// TestScanAllocations pins that a stream with no 7501 report allocates
// nothing however its sequences run, and that a scanner reuses one body
// buffer for every report. Load-bearing: the host calls Scan on every write
// the agent makes.
func TestScanAllocations(t *testing.T) {
	plain := []byte("text é \x1b[1;31mred\x1b[0m \x1b]0;" + strings.Repeat("t", 5000) + "\x07 \x1bP" + strings.Repeat("d", 5000) + "\x1b\\")
	s := NewScanner()
	if allocs := testing.AllocsPerRun(20, func() { s.Scan(plain) }); allocs != 0 {
		t.Errorf("non-7501 output allocates %v times per chunk", allocs)
	}
	s.Scan([]byte("\x1b]7501;state=idle\x07"))
	first := &s.body[:1][0]
	s.Scan([]byte("\x1b]7501;state=done\x07"))
	if &s.body[:1][0] != first {
		t.Error("the body buffer was reallocated for a second report")
	}
}
