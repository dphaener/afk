package host

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/progstatus"
)

// stdio_test.go drives the stdio host two ways. TestStdioWriter runs the
// terminal writer's decisions against a buffer and a fake clock, with no
// process. The rest run a real Serve in stdio mode against a pipe pair
// standing in for the Rex terminal, a stand-in agent written in sh, and the
// real socket, which is why they need unix-socket bind (unsandboxed).
//
// Every end-to-end check reads the terminal stream the way a terminal would:
// splitAfkRecords takes afk's own records out, and what is left must be the
// child's bytes exactly. The children run `stty -opost` first so the PTY does
// not turn \n into \r\n, which makes "exactly" a plain byte comparison.

// mustEncode is progstatus.Encode for records a test knows are valid.
func mustEncode(t *testing.T, r progstatus.Record) []byte {
	t.Helper()
	b, err := progstatus.Encode(r)
	if err != nil {
		t.Fatalf("encoding %+v: %v", r, err)
	}
	return b
}

// splitAfkRecords takes every complete OSC 7501 report with id=afk out of
// stream. It returns what is left, the records in order, and the offset in
// rest each one sat at. Every other byte, the child's own reports included,
// stays in rest untouched.
func splitAfkRecords(stream []byte) (rest []byte, recs []progstatus.Record, at []int) {
	const intro = "\x1b]7501;"
	i := 0
	for i < len(stream) {
		j := bytes.Index(stream[i:], []byte(intro))
		if j < 0 {
			break
		}
		start := i + j
		body := start + len(intro)
		end, term := -1, 0
		for k := body; k < len(stream); k++ {
			if stream[k] == 0x07 {
				end, term = k, 1
				break
			}
			if stream[k] == 0x1b && k+1 < len(stream) && stream[k+1] == '\\' {
				end, term = k, 2
				break
			}
		}
		if end < 0 {
			break
		}
		r, ok := progstatus.Parse(stream[body:end])
		if ok && r.ID == "afk" {
			rest = append(rest, stream[i:start]...)
			recs = append(recs, r)
			at = append(at, len(rest))
		} else {
			rest = append(rest, stream[i:end+term]...)
		}
		i = end + term
	}
	rest = append(rest, stream[i:]...)
	return rest, recs, at
}

// recordWord is a record as "state kind msg", the shape the tables compare.
func recordWord(r progstatus.Record) string {
	parts := []string{string(r.State)}
	for _, p := range []string{string(r.Kind), r.Msg} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

// TestStdioWriter pins the terminal writer's decisions as a table, with a
// fake clock and a buffer for the terminal. Every row also checks that
// taking afk's records out of the output leaves the child's bytes exactly, so
// no row can pass by tearing or reordering the agent's stream.
//
// The load-bearing rows: "an unended sequence holds the record" (the host
// must never write into an agent's open sequence, and logs the wait once,
// not on every tick), "the agent's root clear re-sends the record after it"
// (Claude Code's exit clear removes afk's record, T17.1), "unchanged inside
// the heartbeat" (no churn in the agent's stream) and "unreadable status
// writes nothing" (a stale file never invents a state).
func TestStdioWriter(t *testing.T) {
	rootClear := string(mustEncode(t, progstatus.Record{State: progstatus.StateClear}))
	executing := &PhaseView{Readable: true, Phase: "executing"}
	reviewing := &PhaseView{Readable: true, Phase: "reviewing"}
	awaiting := &PhaseView{Readable: true, Phase: "executing", AwaitingInput: true}
	unreadable := &PhaseView{}

	type step struct {
		view    *PhaseView // set the view first, when not nil
		advance time.Duration
		chunk   string
		host    string
		tick    bool
		final   bool
	}
	tests := []struct {
		name  string
		steps []step
		// wantRest is the output with afk's records taken out.
		wantRest string
		// wantRecs is afk's records in order, as recordWord.
		wantRecs []string
		// wantAt is where each record sat in wantRest; nil skips the check.
		wantAt   []int
		wantWait int
	}{
		{
			name:     "first record at the chunk's last safe offset",
			steps:    []step{{view: executing, chunk: "hello\n"}},
			wantRest: "hello\n",
			wantRecs: []string{"working sess: executing"},
			wantAt:   []int{6},
		},
		{
			name:     "unreadable status writes nothing",
			steps:    []step{{view: unreadable, chunk: "a"}, {tick: true}, {advance: time.Minute, tick: true}},
			wantRest: "a",
		},
		{
			name: "unreadable after a record keeps the last one",
			steps: []step{
				{view: executing, chunk: "a"},
				{view: unreadable, tick: true},
				{advance: StdioHeartbeat, chunk: "b"},
			},
			wantRest: "ab",
			wantRecs: []string{"working sess: executing", "working sess: executing"},
		},
		{
			name: "unchanged inside the heartbeat is not re-sent",
			steps: []step{
				{view: executing, chunk: "a"},
				{advance: StdioHeartbeat - time.Second, chunk: "b"},
				{tick: true},
			},
			wantRest: "ab",
			wantRecs: []string{"working sess: executing"},
		},
		{
			name: "unchanged after the heartbeat is re-sent",
			steps: []step{
				{view: executing, chunk: "a"},
				{advance: StdioHeartbeat, chunk: "b"},
			},
			wantRest: "ab",
			wantRecs: []string{"working sess: executing", "working sess: executing"},
			wantAt:   []int{1, 2},
		},
		{
			name: "a changed phase is sent on the next tick",
			steps: []step{
				{view: executing, chunk: "a"},
				{view: reviewing, tick: true},
			},
			wantRest: "a",
			wantRecs: []string{"working sess: executing", "working sess: reviewing"},
		},
		{
			name: "awaiting input is blocked with a question",
			steps: []step{
				{view: awaiting, chunk: "a"},
			},
			wantRest: "a",
			wantRecs: []string{"blocked question sess is waiting for you"},
		},
		{
			name: "the agent's root clear re-sends the record after it",
			steps: []step{
				{view: executing, chunk: "a"},
				{chunk: "x" + rootClear + "y"},
			},
			wantRest: "ax" + rootClear + "y",
			wantRecs: []string{"working sess: executing", "working sess: executing"},
			wantAt:   []int{1, 3 + len(rootClear)},
		},
		{
			name: "an unended sequence holds the record and logs the wait once",
			steps: []step{
				{view: executing, chunk: "a"},
				{chunk: "\x1b]0;never ends"},
				{view: reviewing, tick: true},
				{advance: StdioSafeWait - time.Second, tick: true},
				{advance: time.Second, tick: true},
				{advance: time.Second, tick: true},
				{chunk: " still open"},
				{advance: time.Minute, tick: true},
			},
			wantRest: "a\x1b]0;never ends still open",
			wantRecs: []string{"working sess: executing"},
			wantWait: 1,
		},
		{
			name: "a held record goes out once the sequence ends",
			steps: []step{
				{view: executing, chunk: "a"},
				{chunk: "\x1b]0;title"},
				{view: reviewing, tick: true},
				{chunk: "\x07after"},
			},
			wantRest: "a\x1b]0;title\x07after",
			wantRecs: []string{"working sess: executing", "working sess: reviewing"},
			wantAt:   []int{1, len("a\x1b]0;title\x07after")},
		},
		{
			name: "a sequence split mid-chunk takes the record before it",
			steps: []step{
				{view: executing, chunk: "ab\x1b]0;ti"},
				{chunk: "tle\x07"},
			},
			wantRest: "ab\x1b]0;title\x07",
			wantRecs: []string{"working sess: executing"},
			wantAt:   []int{2},
		},
		{
			name: "a host line waits for a safe offset",
			steps: []step{
				{view: unreadable, chunk: "\x1b]0;t"},
				{host: "\r\n[afk] restart failed\r\n"},
				{tick: true},
				{chunk: "\x07z"},
			},
			wantRest: "\x1b]0;t\x07z\r\n[afk] restart failed\r\n",
		},
		{
			name: "final clears afk's record at a safe offset",
			steps: []step{
				{view: executing, chunk: "a"},
				{final: true},
			},
			wantRest: "a",
			wantRecs: []string{"working sess: executing", "clear"},
		},
		{
			name: "final writes no clear into an open sequence",
			steps: []step{
				{view: executing, chunk: "a"},
				{chunk: "\x1b]0;open"},
				{final: true},
			},
			wantRest: "a\x1b]0;open",
			wantRecs: []string{"working sess: executing"},
		},
		{
			name: "final writes no clear when nothing was sent",
			steps: []step{
				{view: unreadable, chunk: "a"},
				{final: true},
			},
			wantRest: "a",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			var view PhaseView
			w := &termWriter{
				out:      &out,
				log:      log.New(&logs, "", 0),
				name:     "sess",
				now:      func() time.Time { return now },
				view:     func() PhaseView { return view },
				safeWait: StdioSafeWait,
				sc:       progstatus.NewScanner(),
			}
			for _, s := range tc.steps {
				if s.view != nil {
					view = *s.view
				}
				now = now.Add(s.advance)
				if s.chunk != "" {
					w.take(termChunk{b: []byte(s.chunk)})
				}
				if s.host != "" {
					w.take(termChunk{b: []byte(s.host), host: true})
				}
				if s.tick {
					w.tick()
				}
				if s.final {
					w.final()
				}
			}
			rest, recs, at := splitAfkRecords(out.Bytes())
			if string(rest) != tc.wantRest {
				t.Errorf("child bytes = %q, want %q", rest, tc.wantRest)
			}
			var got []string
			for _, r := range recs {
				got = append(got, recordWord(r))
			}
			if strings.Join(got, " | ") != strings.Join(tc.wantRecs, " | ") {
				t.Errorf("afk records = %q, want %q", got, tc.wantRecs)
			}
			if tc.wantAt != nil && fmt.Sprint(at) != fmt.Sprint(tc.wantAt) {
				t.Errorf("records at offsets %v, want %v", at, tc.wantAt)
			}
			if n := strings.Count(logs.String(), "has waited"); n != tc.wantWait {
				t.Errorf("logged the wait %d times, want %d; log:\n%s", n, tc.wantWait, logs.String())
			}
		})
	}
}

// syncBuf collects the terminal stream from the read end of the Out pipe.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// waitFor polls the stream until ok says yes, failing after testBudget.
func (b *syncBuf) waitFor(t *testing.T, what string, ok func(stream []byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if s := b.bytes(); ok(s) {
			return s
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("the terminal never showed %s; it got %q", what, b.bytes())
	return nil
}

// stdioHarness is a Serve in stdio mode with a pipe pair for its terminal.
type stdioHarness struct {
	*serveHarness
	out   *syncBuf
	inW   *os.File
	winch chan struct{}
	size  chan [2]uint16 // the size Size reports next, one slot
	dir   string         // .afk/<name>, where status.json lives
}

// compressStdioTiming shortens the stdio host's timings for one test. Like
// compressRestartLadder it must be called before the host starts, so the
// restore runs after the host has stopped.
func compressStdioTiming(t *testing.T) {
	t.Helper()
	prevStatus, prevTick, prevWait, prevWindow := stdioStatusEvery, stdioTick, stdioSafeWait, windowCallDeadline
	t.Cleanup(func() {
		stdioStatusEvery, stdioTick, stdioSafeWait, windowCallDeadline = prevStatus, prevTick, prevWait, prevWindow
	})
	stdioStatusEvery = 40 * time.Millisecond
	stdioTick = 20 * time.Millisecond
	stdioSafeWait = 300 * time.Millisecond
	windowCallDeadline = 300 * time.Millisecond
}

// startStdio runs script as the child of a stdio host whose terminal is a
// pipe pair at 30x100. edit, when not nil, changes the config first.
func startStdio(t *testing.T, script string, edit func(*ServeConfig)) *stdioHarness {
	t.Helper()
	compressStdioTiming(t)
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	h := &stdioHarness{out: &syncBuf{}, inW: inW, winch: make(chan struct{}, 1), size: make(chan [2]uint16, 1)}
	h.size <- [2]uint16{30, 100}
	// Registered before the host starts, so they run after it has stopped.
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outW.Close()
		<-done
		_ = inR.Close()
		_ = outR.Close()
	})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				h.out.mu.Lock()
				h.out.buf.Write(buf[:n])
				h.out.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	var mu sync.Mutex
	cur := [2]uint16{30, 100}
	cfg := serveConfigFor(t, script)
	cfg.Stdio = true
	cfg.Terminal = &Terminal{
		In:  inR,
		Out: outW,
		Size: func() (uint16, uint16, bool) {
			mu.Lock()
			defer mu.Unlock()
			select {
			case cur = <-h.size:
			default:
			}
			return cur[0], cur[1], true
		},
		Winch: h.winch,
	}
	if edit != nil {
		edit(&cfg)
	}
	h.dir = filepath.Dir(cfg.MetaPath)
	h.serveHarness = startServeWithConfig(t, cfg)
	// The socket answers before host.json is written, and SendInput and
	// RequestRestart find the socket through host.json.
	waitForFile(t, cfg.MetaPath)
	return h
}

// writeStatus writes a well-formed status.json in h.dir. It carries a
// session_id: outside planning, internal/state reads an empty one as stale
// (status-schema.md invariant 2), and a stale file gives no record at all.
func (h *stdioHarness) writeStatus(t *testing.T, phase string, awaiting bool) {
	t.Helper()
	body := fmt.Sprintf(`{
  "session_id": %q,
  "plan_name": %q,
  "phase": %q,
  "branch": "sess",
  "base_branch": "main",
  "loop_count": 1,
  "awaiting_input": %t,
  "tmux_window": "",
  "updated_at": "2026-10-28T12:00:00Z"
}
`, restartSessionID, filepath.Base(h.dir), phase, awaiting)
	// Atomic, so the watcher never reads half a file.
	tmp := filepath.Join(h.dir, ".status.json.tmp")
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", h.dir, err)
	}
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(h.dir, "status.json")); err != nil {
		t.Fatalf("renaming status.json into place: %v", err)
	}
}

// hasRecord says whether the stream holds an afk record that matches want
// (as recordWord) at or after offset from in the child's bytes.
func hasRecord(stream []byte, want string, from int) bool {
	_, recs, at := splitAfkRecords(stream)
	for i, r := range recs {
		if recordWord(r) == want && at[i] >= from {
			return true
		}
	}
	return false
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestStdioPipePair runs a stand-in agent that prints 300 numbered lines with
// its own OSC 7501 reports among them, then a root clear, under a stdio host.
// It checks, from the terminal stream alone: the first child was born at the
// terminal's size; afk's record says what status.json says (working, then
// blocked); afk's record comes back after the agent's root clear; the child's
// bytes are all there, in order, untouched (the gap check); and shutdown
// clears afk's record.
func TestStdioPipePair(t *testing.T) {
	work := t.TempDir()
	sizeFile := filepath.Join(work, "size")
	clearFlag := filepath.Join(work, "clear")
	childReport := mustEncode(t, progstatus.Record{State: progstatus.StateWorking, App: "claude-code", Msg: "child report"})
	rootClear := mustEncode(t, progstatus.Record{State: progstatus.StateClear})
	writeFile(t, filepath.Join(work, "report"), childReport)
	writeFile(t, filepath.Join(work, "rootclear"), rootClear)

	script := fmt.Sprintf(`stty size > %[1]s
stty -opost
i=1
while [ $i -le 300 ]; do
  printf 'line %%d\n' $i
  if [ $((i %% 60)) -eq 0 ]; then cat %[2]s; fi
  if [ $((i %% 10)) -eq 0 ]; then sleep 0.01; fi
  i=$((i+1))
done
while [ ! -f %[3]s ]; do sleep 0.02; done
cat %[4]s
printf 'after clear\n'
exec cat
`, sizeFile, filepath.Join(work, "report"), clearFlag, filepath.Join(work, "rootclear"))

	var expected bytes.Buffer
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&expected, "line %d\n", i)
		if i%60 == 0 {
			expected.Write(childReport)
		}
	}
	beforeClear := expected.Len()
	expected.Write(rootClear)
	expected.WriteString("after clear\n")

	h := startStdio(t, "", func(cfg *ServeConfig) { cfg.Argv = []string{"/bin/sh", "-c", script} })
	h.writeStatus(t, "executing", false)

	if got := waitForFile(t, sizeFile); got != "30 100" {
		t.Errorf("the first child was born at %q, want the terminal's 30 100", got)
	}
	h.out.waitFor(t, "afk's working record", func(s []byte) bool { return hasRecord(s, "working sess: executing", 0) })
	h.out.waitFor(t, "line 300", func(s []byte) bool { return bytes.Contains(s, []byte("line 300\n")) })

	h.writeStatus(t, "executing", true)
	h.out.waitFor(t, "afk's blocked record", func(s []byte) bool {
		return hasRecord(s, "blocked question sess is waiting for you", 0)
	})

	touch(t, clearFlag)
	afterClear := beforeClear + len(rootClear)
	h.out.waitFor(t, "afk's record after the agent's root clear", func(s []byte) bool {
		return hasRecord(s, "blocked question sess is waiting for you", afterClear)
	})
	h.out.waitFor(t, "the child's last line", func(s []byte) bool { return bytes.Contains(s, []byte("after clear\n")) })

	h.stopQuietly()
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	stream := h.out.waitFor(t, "the final clear of afk's record", func(s []byte) bool {
		_, recs, _ := splitAfkRecords(s)
		return len(recs) > 0 && recordWord(recs[len(recs)-1]) == "clear"
	})

	rest, recs, at := splitAfkRecords(stream)
	if !bytes.Equal(rest, expected.Bytes()) {
		t.Errorf("the child's bytes changed in the terminal:\n got %q\nwant %q", rest, expected.Bytes())
	}
	// The gap check, on its own so a failure says which line went missing.
	nums := regexp.MustCompile(`line (\d+)\n`).FindAllSubmatch(rest, -1)
	for i, m := range nums {
		if n, _ := strconv.Atoi(string(m[1])); n != i+1 {
			t.Fatalf("line %d is %q: a numbered line is missing or out of order", i+1, m[0])
		}
	}
	if len(nums) != 300 {
		t.Errorf("the terminal showed %d numbered lines, want 300", len(nums))
	}
	var words []string
	for i, r := range recs {
		words = append(words, fmt.Sprintf("%s@%d", recordWord(r), at[i]))
	}
	t.Logf("afk's records in the terminal: %s", strings.Join(words, " | "))
	t.Logf("gap check: %d numbered lines in order, child bytes identical (%d bytes)", len(nums), len(rest))
}

// TestStdioRefusesAttach checks that a socket client's opening Resize, which
// is how an interactive attach starts, is refused with the Rex sentence and
// a bare close (no Exit: the session has not ended, no Stolen: nothing was
// taken), while the child keeps running and keeps reaching the terminal.
// host.json says runtime rex and carries the block ids the spawner passed.
func TestStdioRefusesAttach(t *testing.T) {
	work := t.TempDir()
	pidFile := filepath.Join(work, "child.pid")
	script := fmt.Sprintf("echo $$ > %s\ni=0\nwhile true; do i=$((i+1)); echo tick $i; sleep 0.05; done\n", pidFile)
	rex := &RexBlock{Server: "default", SessionID: "sess-1", WindowID: "win-2", BlockID: "blk-3"}
	h := startStdio(t, script, func(cfg *ServeConfig) { cfg.Rex = rex })
	pid := readPIDFile(t, pidFile)

	meta, err := ReadMeta(h.cfg.Worktree, h.cfg.Name)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Runtime != RuntimeRex {
		t.Errorf("host.json runtime = %q, want %q", meta.Runtime, RuntimeRex)
	}
	if meta.Rex == nil || *meta.Rex != *rex {
		t.Errorf("host.json rex = %+v, want %+v", meta.Rex, rex)
	}

	conn := h.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(40, 120))
	got := readUntil(t, conn, RefuseAttachLine)
	expectEOF(t, conn)
	t.Logf("the attach was refused with %q and closed without Exit", strings.TrimSpace(got))

	if !Alive(pid) {
		t.Fatalf("the child %d died when the attach was refused", pid)
	}
	seen := len(h.out.bytes())
	h.out.waitFor(t, "more child output after the refusal", func(s []byte) bool {
		return len(s) > seen && bytes.Contains(s[seen:], []byte("tick"))
	})
	if h.returned() {
		t.Fatalf("the host stopped after refusing an attach: %v", h.err)
	}
	awaitLogLine(t, h.cfg.LogPath, "refused a socket attach")
	if data, _ := os.ReadFile(h.cfg.LogPath); bytes.Contains(data, []byte("resized pty to 40x120")) {
		t.Errorf("the refused client's Resize reached the PTY")
	}
	t.Logf("the child %d kept running and its output kept reaching the terminal", pid)
}

// TestStdioUnterminatedSequence runs a child that opens an OSC sequence and
// never ends it, then changes the phase so afk's record is due. The record
// must wait (the host never closes or splits the agent's sequence), the wait
// must be logged once however long it lasts, the child's bytes after the
// opening must reach the terminal unchanged, and shutdown must not write its
// clear into the open sequence.
func TestStdioUnterminatedSequence(t *testing.T) {
	work := t.TempDir()
	openFlag := filepath.Join(work, "open")
	script := fmt.Sprintf(`stty -opost
printf 'before\n'
while [ ! -f %s ]; do sleep 0.02; done
printf '\033]0;never ends '
i=1
while true; do printf 'x%%d ' $i; i=$((i+1)); sleep 0.02; done
`, openFlag)
	h := startStdio(t, script, nil)
	h.writeStatus(t, "executing", false)
	h.out.waitFor(t, "afk's first record", func(s []byte) bool { return hasRecord(s, "working sess: executing", 0) })

	touch(t, openFlag)
	h.out.waitFor(t, "the open sequence's content", func(s []byte) bool { return bytes.Contains(s, []byte("x3 ")) })
	h.writeStatus(t, "reviewing", false)
	awaitLogLine(t, h.cfg.LogPath, "has waited")
	// Well past the wait again: still exactly one line.
	time.Sleep(4 * stdioSafeWait)
	data, _ := os.ReadFile(h.cfg.LogPath)
	if n := bytes.Count(data, []byte("has waited")); n != 1 {
		t.Errorf("the wait was logged %d times, want once", n)
	}

	h.stopQuietly()
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	stream := h.out.bytes()
	open := []byte("\x1b]0;never ends ")
	idx := bytes.Index(stream, open)
	if idx < 0 {
		t.Fatalf("the child's sequence never reached the terminal: %q", stream)
	}
	tail := stream[idx+len(open):]
	if bytes.Contains(tail, []byte("\x1b")) {
		t.Errorf("something was written into the open sequence: %q", tail)
	}
	// Byte-identical: the tail is x1 x2 … with no gap and nothing else.
	var want bytes.Buffer
	for i := 1; want.Len() < len(tail); i++ {
		fmt.Fprintf(&want, "x%d ", i)
	}
	if !bytes.Equal(tail, want.Bytes()[:len(tail)]) {
		t.Errorf("the child's bytes changed inside its sequence:\n got %q\nwant %q", tail, want.Bytes()[:len(tail)])
	}
	if hasRecord(stream, "working sess: reviewing", 0) {
		t.Errorf("afk's reviewing record was written although the agent's sequence never ended")
	}
	awaitLogLine(t, h.cfg.LogPath, "not clearing afk's record at shutdown")
	t.Logf("the record waited, the wait was logged once, and %d bytes of the child's open sequence reached the terminal unchanged", len(tail))
}

// TestStdioInputAndHangup checks the three ways in: what the terminal types
// and what a socket's SendInput types both reach the child; the terminal's
// input closing is logged and the host keeps serving; and SIGHUP, the Rex
// block closing, stops the host in order, so host.json and the socket go.
func TestStdioInputAndHangup(t *testing.T) {
	// Our own registration for the whole test, so a SIGHUP that lands after
	// Serve's handler is gone cannot kill the test binary.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	defer signal.Stop(guard)

	work := t.TempDir()
	pidFile := filepath.Join(work, "child.pid")
	h := startStdio(t, fmt.Sprintf("echo $$ > %s; exec cat", pidFile), nil)
	pid := readPIDFile(t, pidFile)

	if _, err := h.inW.Write([]byte("typed at the terminal\r")); err != nil {
		t.Fatalf("typing at the terminal: %v", err)
	}
	h.out.waitFor(t, "the terminal's typing", func(s []byte) bool { return bytes.Contains(s, []byte("typed at the terminal")) })

	if err := SendInput(h.cfg.Worktree, h.cfg.Name, "sent over the socket", testBudget); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	h.out.waitFor(t, "the socket's typing", func(s []byte) bool {
		// cat echoes it back, so it shows twice once the child has read it.
		return bytes.Count(s, []byte("sent over the socket")) >= 2
	})

	_ = h.inW.Close()
	awaitLogLine(t, h.cfg.LogPath, "the terminal's input closed")
	if h.returned() {
		t.Fatalf("the host stopped when its terminal input closed: %v", h.err)
	}
	conn, err := net.Dial("unix", h.cfg.Socket)
	if err != nil {
		t.Fatalf("the socket stopped answering after the terminal input closed: %v", err)
	}
	_ = conn.Close()
	if !Alive(pid) {
		t.Fatalf("the child died when the terminal input closed")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("sending SIGHUP: %v", err)
	}
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve after SIGHUP = %v, want nil", err)
	}
	if Alive(pid) {
		t.Errorf("the child %d is still alive after SIGHUP", pid)
	}
	assertGone(t, h.cfg.Socket)
	assertGone(t, h.cfg.MetaPath)
	t.Logf("input closed: host kept serving; SIGHUP: host stopped, host.json and socket removed")
}

// TestStdioWinch checks that a terminal resize reaches the PTY through the
// serve loop.
func TestStdioWinch(t *testing.T) {
	h := startStdio(t, "exec cat", nil)
	h.size <- [2]uint16{40, 120}
	h.winch <- struct{}{}
	awaitLogLine(t, h.cfg.LogPath, "resized pty to 40x120")
}

// TestStdioRequestRestart checks that a socket RequestRestart works on a
// stdio host and that its answer goes back to the requester, not to the
// screen. The rows: a restart that respawns the child (the resumed stand-in
// reaches the terminal, and the requester hears success), and one refused
// for want of a conversation to resume (the requester hears why, the
// terminal shows the [afk] line, and the child is left running).
func TestStdioRequestRestart(t *testing.T) {
	tests := []struct {
		name       string
		withStatus bool
		wantRefuse string // "" means a successful restart
		wantScreen string
	}{
		{name: "restarts the child", withStatus: true, wantScreen: "RESUMED-STAND-IN"},
		{name: "refusal returns to the requester", wantRefuse: "status.json", wantScreen: "[afk] restart failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			compressRestartLadder(t)
			bin := t.TempDir()
			writeFile(t, filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho RESUMED-STAND-IN \"$@\"\nexec cat\n"))
			if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			work := t.TempDir()
			pidFile := filepath.Join(work, "child.pid")
			h := startStdio(t, fmt.Sprintf("echo $$ > %s; echo FIRST-CHILD; exec cat", pidFile), nil)
			pid := readPIDFile(t, pidFile)
			if tc.withStatus {
				writeStatusFile(t, h.dir, "executing", restartSessionID)
			}

			refused, err := RequestRestart(h.cfg.Worktree, h.cfg.Name, testBudget, testBudget)
			if err != nil {
				t.Fatalf("RequestRestart: %v", err)
			}
			switch {
			case tc.wantRefuse == "" && refused != "":
				t.Fatalf("the restart was refused: %s", refused)
			case tc.wantRefuse != "" && !strings.Contains(refused, tc.wantRefuse):
				t.Fatalf("refusal = %q, want it to mention %q", refused, tc.wantRefuse)
			}
			h.out.waitFor(t, tc.wantScreen, func(s []byte) bool { return bytes.Contains(s, []byte(tc.wantScreen)) })
			if tc.wantRefuse != "" && !Alive(pid) {
				t.Errorf("a refused restart killed the child %d", pid)
			}
			if tc.wantRefuse == "" && Alive(pid) {
				t.Errorf("the first child %d is still alive after the restart", pid)
			}
			if h.returned() {
				t.Fatalf("the host stopped: %v", h.err)
			}
			t.Logf("requester heard %q; terminal shows %q", refused, tc.wantScreen)
		})
	}
}

// TestStdioWindowLabel pins when the host sends its Rex window a new label,
// as a table over nextWindowLabel. Load-bearing rows: "the same phase sends
// none" (a status write every few seconds must not become a Rex call every
// few seconds), "unreadable keeps the last label" (a stale file says nothing,
// so the window must not be relabelled from it), "failed maps to the failed
// word" (the owner finds a failed session by its window), and "planning is
// plan" (the planning window carries no session prefix whatever its phase).
func TestStdioWindowLabel(t *testing.T) {
	tests := []struct {
		name     string
		planning bool
		view     PhaseView
		last     string
		sent     bool
		want     string
		wantSend bool
	}{
		{name: "the first reading sends one", view: PhaseView{Readable: true, Phase: "executing"}, want: "sess · build", wantSend: true},
		{name: "a phase change sends one", view: PhaseView{Readable: true, Phase: "reviewing"}, last: "sess · build", sent: true, want: "sess · review", wantSend: true},
		{name: "the same phase sends none", view: PhaseView{Readable: true, Phase: "executing"}, last: "sess · build", sent: true},
		{name: "awaiting input alone sends none", view: PhaseView{Readable: true, Phase: "executing", AwaitingInput: true}, last: "sess · build", sent: true},
		{name: "unreadable keeps the last label", view: PhaseView{}, last: "sess · build", sent: true},
		{name: "unreadable before any label sends none", view: PhaseView{}},
		{name: "failed maps to the failed word", view: PhaseView{Readable: true, Phase: "failed"}, last: "sess · build", sent: true, want: "sess · failed", wantSend: true},
		{name: "fixing maps to fix", view: PhaseView{Readable: true, Phase: "fixing"}, last: "sess · review", sent: true, want: "sess · fix", wantSend: true},
		{name: "planning is plan", planning: true, view: PhaseView{Readable: true, Phase: "executing"}, want: "plan", wantSend: true},
		{name: "planning sends plan once", planning: true, view: PhaseView{Readable: true, Phase: "reviewing"}, last: "plan", sent: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, send := nextWindowLabel("sess", tc.planning, tc.view, tc.last, tc.sent)
			if got != tc.want || send != tc.wantSend {
				t.Errorf("nextWindowLabel = (%q, %v), want (%q, %v)", got, send, tc.want, tc.wantSend)
			}
		})
	}
}

// TestStdioCloseDecision pins whether the host closes its window as it
// exits, as a table over closeWindowOnExit. Load-bearing rows: "exit under
// failed does not close" (the spec keeps a failed session's window open to be
// read), "a fresh reading beats the last one" (the agent's final write lands
// after the watcher's last tick) and "unreadable at exit falls back to the
// last reading" (a file mid-rewrite at exit must not undo a failure).
func TestStdioCloseDecision(t *testing.T) {
	failed := &PhaseView{Readable: true, Phase: "failed"}
	executing := &PhaseView{Readable: true, Phase: "executing"}
	tests := []struct {
		name string
		now  PhaseView
		last *PhaseView
		want bool
	}{
		{name: "exit under failed does not close", now: *failed, last: failed, want: false},
		{name: "exit under done closes", now: PhaseView{Readable: true, Phase: "done"}, last: executing, want: true},
		{name: "exit under executing closes", now: *executing, last: executing, want: true},
		{name: "exit under reviewing closes", now: PhaseView{Readable: true, Phase: "reviewing"}, want: true},
		{name: "a fresh reading beats the last one", now: *failed, last: executing, want: false},
		{name: "a fresh non-failed reading beats a failed last one", now: *executing, last: failed, want: true},
		{name: "unreadable at exit falls back to the last reading", now: PhaseView{}, last: failed, want: false},
		{name: "unreadable at exit after executing closes", now: PhaseView{}, last: executing, want: true},
		{name: "never readable closes", now: PhaseView{}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeWindowOnExit(tc.now, tc.last); got != tc.want {
				t.Errorf("closeWindowOnExit = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeWindow is a WindowControl that records what the host asked of it.
// With hang set, every call blocks until the test ends, standing in for a
// Rex server that never answers.
type fakeWindow struct {
	mu       sync.Mutex
	labels   []string
	closes   int
	err      error
	hang     chan struct{}
	metaPath string
	// metaAtClose says host.json still existed when CloseSelf ran.
	metaAtClose bool
}

func (f *fakeWindow) SetLabel(label string) error {
	f.mu.Lock()
	f.labels = append(f.labels, label)
	f.mu.Unlock()
	if f.hang != nil {
		<-f.hang
	}
	return f.err
}

func (f *fakeWindow) CloseSelf() error {
	f.mu.Lock()
	f.closes++
	if _, err := os.Stat(f.metaPath); err == nil {
		f.metaAtClose = true
	}
	f.mu.Unlock()
	if f.hang != nil {
		<-f.hang
	}
	return f.err
}

func (f *fakeWindow) snapshot() ([]string, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.labels...), f.closes, f.metaAtClose
}

// waitLabels polls until the window has been asked for n labels.
func (f *fakeWindow) waitLabels(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if l, _, _ := f.snapshot(); len(l) >= n {
			return
		}
		time.Sleep(testPollStep)
	}
	l, _, _ := f.snapshot()
	t.Fatalf("the window was asked for %d labels, want %d: %q", len(l), n, l)
}

// TestStdioWindowControl runs a real stdio host with a fake WindowControl
// and a child that ends when told to. Load-bearing rows: "done closes the
// window last" (labels follow the phases, an unchanged phase sends none, and
// the close comes after host.json is gone, so nothing points at a closed
// block); "failed leaves the window open" (and the host passes the child's
// status on, which is what the Rex block shows); "a failing Rex is logged
// once" (Rex that is not reachable fails every call; one line says so).
func TestStdioWindowControl(t *testing.T) {
	tests := []struct {
		name       string
		phases     []string
		exit       int
		err        error
		wantLabels []string
		wantCloses int
		wantErr    *ChildExited
		wantLog    string
		wantLogN   int
	}{
		{name: "done closes the window last", phases: []string{"executing", "executing", "reviewing", "done"},
			wantLabels: []string{"sess · build", "sess · review", "sess · done"}, wantCloses: 1, wantLog: "closing this session's Rex window", wantLogN: 1},
		{name: "failed leaves the window open", phases: []string{"executing", "failed"}, exit: 3,
			wantLabels: []string{"sess · build", "sess · failed"}, wantErr: &ChildExited{Code: 3}, wantLog: "leaving its Rex window open", wantLogN: 1},
		{name: "a failing Rex is logged once", phases: []string{"executing", "reviewing"}, err: errors.New("rex: no server"),
			wantLabels: []string{"sess · build", "sess · review"}, wantCloses: 1, wantLog: "Rex is not reachable", wantLogN: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flag := filepath.Join(t.TempDir(), "end")
			script := fmt.Sprintf("echo started\nwhile [ ! -f %s ]; do sleep 0.02; done\nexit %d\n", flag, tc.exit)
			fw := &fakeWindow{err: tc.err}
			h := startStdio(t, script, func(cfg *ServeConfig) {
				cfg.Window = fw
				fw.metaPath = cfg.MetaPath
			})
			want := 0
			for i, p := range tc.phases {
				h.writeStatus(t, p, false)
				if i > 0 && p == tc.phases[i-1] {
					// Several watcher ticks on the unchanged phase: none
					// of them may send a label.
					time.Sleep(4 * stdioStatusEvery)
					continue
				}
				want++
				fw.waitLabels(t, want)
			}
			touch(t, flag)
			err := h.waitDone(t)
			var ce *ChildExited
			switch {
			case tc.wantErr == nil && err != nil:
				t.Errorf("Serve = %v, want nil", err)
			case tc.wantErr != nil && (!errors.As(err, &ce) || *ce != *tc.wantErr):
				t.Errorf("Serve = %v, want %v", err, tc.wantErr)
			}
			labels, closes, metaAtClose := fw.snapshot()
			if strings.Join(labels, " | ") != strings.Join(tc.wantLabels, " | ") {
				t.Errorf("labels = %q, want %q", labels, tc.wantLabels)
			}
			if closes != tc.wantCloses {
				t.Errorf("CloseSelf called %d times, want %d", closes, tc.wantCloses)
			}
			if metaAtClose {
				t.Errorf("CloseSelf ran while host.json still existed; the close must be the host's last act")
			}
			data, _ := os.ReadFile(h.cfg.LogPath)
			if n := bytes.Count(data, []byte(tc.wantLog)); n != tc.wantLogN {
				t.Errorf("the log has %q %d times, want %d; log:\n%s", tc.wantLog, n, tc.wantLogN, data)
			}
			t.Logf("labels %q, closes %d, Serve = %v", labels, closes, err)
		})
	}
}

// TestStdioWindowDeadline stands in for a Rex server that never answers:
// every WindowControl call blocks for the rest of the test. The child's
// output must still reach the terminal at full speed while a relabel hangs
// (no Rex call sits on the pump or the writer), a phase change made while it
// hangs must not start a second call, and the host must still end, bounded
// by windowCallDeadline, when its close at exit hangs too.
func TestStdioWindowDeadline(t *testing.T) {
	work := t.TempDir()
	flag, endFlag := filepath.Join(work, "go"), filepath.Join(work, "end")
	script := fmt.Sprintf(`stty -opost
echo started
while [ ! -f %s ]; do sleep 0.02; done
i=1
while [ $i -le 500 ]; do printf 'line %%d\n' $i; i=$((i+1)); done
while [ ! -f %s ]; do sleep 0.02; done
`, flag, endFlag)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	fw := &fakeWindow{hang: hang}
	h := startStdio(t, script, func(cfg *ServeConfig) {
		cfg.Window = fw
		fw.metaPath = cfg.MetaPath
	})
	h.writeStatus(t, "executing", false)
	fw.waitLabels(t, 1)

	start := time.Now()
	touch(t, flag)
	h.out.waitFor(t, "line 500", func(s []byte) bool { return bytes.Contains(s, []byte("line 500\n")) })
	pumped := time.Since(start)
	if pumped > windowCallDeadline {
		t.Errorf("the child's 500 lines took %s to reach the terminal while a relabel hung, longer than the %s deadline", pumped, windowCallDeadline)
	}

	// A phase change while the first relabel hangs, given time to pass the
	// deadline and several watcher ticks: it must wait, not start a call.
	h.writeStatus(t, "reviewing", false)
	time.Sleep(windowCallDeadline + 4*stdioStatusEvery)
	exitAt := time.Now()
	touch(t, endFlag)
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	ended := time.Since(exitAt)
	// The child's exit, one hung close bounded by the deadline, and the
	// writer's flush grace; anything near the test budget means a hung call
	// held the host.
	if limit := windowCallDeadline + stdioFlushGrace + 2*time.Second; ended > limit {
		t.Errorf("the host took %s to end with a hung close, want under %s", ended, limit)
	}
	labels, closes, _ := fw.snapshot()
	if len(labels) != 1 {
		t.Errorf("labels = %q, want only the first: a second call started while the first hung", labels)
	}
	if closes != 1 {
		t.Errorf("CloseSelf called %d times, want 1", closes)
	}
	awaitLogLine(t, h.cfg.LogPath, "did not answer within")
	t.Logf("500 lines in %s with a relabel hung; host ended %s after the child was told to exit, with its close hung", pumped, ended)
}
