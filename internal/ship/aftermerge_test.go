package ship

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// TestRunAfterMerge runs real commands through a real sh, each in its own
// temp folder.
//
// The load-bearing rows are the two timeout rows and the unwritable-log row.
// "a timeout stops a command that is still running" is the promise that a
// pass cannot wait for ever inside a command. "a timeout kills what the
// command started" is the reason for the process group: with a plain kill of
// the shell that row's background sleep is still alive afterwards, which in
// real use is a compiler still writing into the main checkout after the
// owner was told the command had stopped. "a log that cannot be opened runs
// nothing" matters because the ship state machine writes its start mark
// first: a command that ran with nowhere to put its output would leave a
// failure the owner cannot read.
//
// The exit-code rows guard the mapping the state machine branches on (0 is
// the only success; a timeout is -1 with timedOut, never an exit code), and
// the working-folder row guards "runs in the main checkout".
func TestRunAfterMerge(t *testing.T) {
	t.Setenv("AFK_AFTER_MERGE_TEST", "from-the-daemon")

	tests := []struct {
		name    string
		command string
		timeout time.Duration
		// logPath and dir build the two paths from the row's temp folder;
		// nil means "<tmp>/build/after-merge.log" and "<tmp>/work".
		logPath func(tmp string) string
		dir     func(tmp string) string

		wantCode     int
		wantTimedOut bool
		wantErr      bool
		// wantLog lists text the log must hold; wantNoLog means the log
		// file must not exist at all.
		wantLog   []string
		wantNoLog bool
		// within bounds how long the call may take; zero means two seconds.
		within time.Duration
		check  func(t *testing.T, tmp, work string)
	}{
		{
			name:     "exit 0 with both streams in the log",
			command:  "echo to-stdout; echo to-stderr >&2",
			timeout:  5 * time.Second,
			wantCode: 0,
			wantLog: []string{
				"to-stdout\n", "to-stderr\n",
				"afk: running the after-merge command in ",
				": echo to-stdout; echo to-stderr >&2\n",
				"afk: the command finished with exit code 0\n",
			},
		},
		{
			name:     "a non-zero exit code is passed through",
			command:  "echo boom; exit 3",
			timeout:  5 * time.Second,
			wantCode: 3,
			wantLog:  []string{"boom\n", "afk: the command finished with exit code 3\n"},
		},
		{
			name:     "a command line with && stops at the first failure",
			command:  "echo first && false && echo second",
			timeout:  5 * time.Second,
			wantCode: 1,
			wantLog:  []string{"first\n", "exit code 1\n"},
			check: func(t *testing.T, tmp, work string) {
				// The header line quotes the command, so look for the
				// word on a line of its own.
				if strings.Contains(readLog(t, tmp), "\nsecond\n") {
					t.Errorf("the part after the failed && ran")
				}
			},
		},
		{
			name:     "the working folder is dir",
			command:  "pwd -P; echo here > marker",
			timeout:  5 * time.Second,
			wantCode: 0,
			check: func(t *testing.T, tmp, work string) {
				real, err := filepath.EvalSymlinks(work)
				if err != nil {
					t.Fatal(err)
				}
				if log := readLog(t, tmp); !strings.Contains(log, "\n"+real+"\n") {
					t.Errorf("pwd in the log is not %q:\n%s", real, log)
				}
				if _, err := os.Stat(filepath.Join(work, "marker")); err != nil {
					t.Errorf("the command did not write into dir: %v", err)
				}
			},
		},
		{
			name:     "the daemon's environment is passed on",
			command:  `echo "env=$AFK_AFTER_MERGE_TEST"`,
			timeout:  5 * time.Second,
			wantCode: 0,
			wantLog:  []string{"env=from-the-daemon\n"},
		},
		{
			name:     "standard input is closed, so a command that reads does not wait",
			command:  "cat; echo after-cat",
			timeout:  5 * time.Second,
			wantCode: 0,
			wantLog:  []string{"after-cat\n"},
		},
		{
			name:     "a command ended by a signal reads as 128 plus the signal",
			command:  "kill -TERM $$",
			timeout:  5 * time.Second,
			wantCode: 128 + int(syscall.SIGTERM),
			wantLog:  []string{"exit code 143\n"},
		},
		{
			name:     "the log is emptied at the start of each run",
			command:  "echo fresh",
			timeout:  5 * time.Second,
			wantCode: 0,
			logPath: func(tmp string) string {
				p := filepath.Join(tmp, "build", "after-merge.log")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					panic(err)
				}
				if err := os.WriteFile(p, []byte("left over from the last run\n"), 0o644); err != nil {
					panic(err)
				}
				return p
			},
			wantLog: []string{"fresh\n"},
			check: func(t *testing.T, tmp, work string) {
				if strings.Contains(readLog(t, tmp), "left over") {
					t.Errorf("the old run's output is still in the log")
				}
			},
		},
		{
			name:         "a timeout stops a command that is still running",
			command:      "echo started; sleep 30",
			timeout:      200 * time.Millisecond,
			wantCode:     -1,
			wantTimedOut: true,
			wantLog:      []string{"started\n", "afk: the command was stopped after 200ms\n"},
			within:       5 * time.Second,
		},
		{
			name:         "a timeout kills what the command started",
			command:      "sleep 30 & echo $! > child.pid; wait",
			timeout:      200 * time.Millisecond,
			wantCode:     -1,
			wantTimedOut: true,
			within:       5 * time.Second,
			check: func(t *testing.T, tmp, work string) {
				raw, err := os.ReadFile(filepath.Join(work, "child.pid"))
				if err != nil {
					t.Fatalf("the command did not get as far as writing its child's pid: %v", err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil || pid <= 1 {
					t.Fatalf("child.pid holds %q", raw)
				}
				if !goneSoon(pid) {
					// Clean up what the bug left behind before failing.
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Errorf("the command's background child (pid %d) is still running after the timeout", pid)
				}
			},
		},
		{
			name:    "a log that cannot be opened runs nothing",
			command: "echo ran > marker",
			timeout: 5 * time.Second,
			logPath: func(tmp string) string {
				// A plain file where a folder is needed: no user, root
				// included, can make a folder under it.
				blocker := filepath.Join(tmp, "blocker")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					panic(err)
				}
				return filepath.Join(blocker, "build", "after-merge.log")
			},
			wantCode:  -1,
			wantErr:   true,
			wantNoLog: true,
			check: func(t *testing.T, tmp, work string) {
				if _, err := os.Stat(filepath.Join(work, "marker")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the command ran although its log could not be opened (stat marker: %v)", err)
				}
			},
		},
		{
			name:     "a folder that is not there is an error, and the log says so",
			command:  "echo ran",
			timeout:  5 * time.Second,
			dir:      func(tmp string) string { return filepath.Join(tmp, "no-such-folder") },
			wantCode: -1,
			wantErr:  true,
			wantLog:  []string{"afk: the command could not be started: "},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			work := filepath.Join(tmp, "work")
			if err := os.Mkdir(work, 0o755); err != nil {
				t.Fatal(err)
			}
			dir := work
			if tc.dir != nil {
				dir = tc.dir(tmp)
			}
			// The default log folder does not exist yet: every row that
			// uses it also checks that the runner makes it.
			logPath := filepath.Join(tmp, "build", "after-merge.log")
			if tc.logPath != nil {
				logPath = tc.logPath(tmp)
			}
			within := tc.within
			if within == 0 {
				within = 2 * time.Second
			}

			began := time.Now()
			code, timedOut, err := RunAfterMerge(dir, tc.command, logPath, tc.timeout)
			took := time.Since(began)

			if code != tc.wantCode || timedOut != tc.wantTimedOut || (err != nil) != tc.wantErr {
				t.Errorf("RunAfterMerge = (%d, %v, %v), want (%d, %v, error %v)",
					code, timedOut, err, tc.wantCode, tc.wantTimedOut, tc.wantErr)
			}
			if took > within {
				t.Errorf("the call took %s, want under %s", took, within)
			}
			if tc.wantNoLog {
				if _, err := os.Stat(logPath); err == nil {
					t.Errorf("a log file was written at %s", logPath)
				}
			} else {
				raw, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatalf("reading the log: %v", err)
				}
				for _, want := range tc.wantLog {
					if !strings.Contains(string(raw), want) {
						t.Errorf("the log does not hold %q:\n%s", want, raw)
					}
				}
			}
			if tc.check != nil {
				tc.check(t, tmp, work)
			}
		})
	}
}

func readLog(t *testing.T, tmp string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(tmp, "build", "after-merge.log"))
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	return string(raw)
}

// goneSoon reports whether the process is gone, looking for up to three
// seconds. The look has to be repeated: SIGKILL is carried out by the kernel
// after kill(2) returns, and a killed process whose parent died with it stays
// in the process table, still answering signal 0, until whoever inherits it
// collects it. Neither moment is one this test can wait on directly, because
// the process is not this test's child.
func goneSoon(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTailFile covers the text an inbox item carries from the after-merge
// log.
//
// The load-bearing rows are "longer than the byte limit" (a build log is
// megabytes and the item goes to a phone: the limit must hold and the half
// line at the cut must go), "one line longer than the byte limit" (dropping
// the half line there would return nothing at all for a failure whose only
// output is one long line) and the two invalid-UTF-8 rows (the text goes
// into JSON). The "cut on a line boundary" row guards the one extra byte
// that is read: without it a whole first line is thrown away as if it were
// half of one.
func TestTailFile(t *testing.T) {
	missing := "\x00missing"
	tests := []struct {
		name     string
		content  string
		maxLines int
		maxBytes int
		want     string
	}{
		{name: "missing file", content: missing, maxLines: 20, maxBytes: 2000, want: ""},
		{name: "empty file", content: "", maxLines: 20, maxBytes: 2000, want: ""},
		{name: "only newlines", content: "\n\n\n", maxLines: 20, maxBytes: 2000, want: ""},
		{name: "short file comes back whole", content: "one\ntwo\n", maxLines: 20, maxBytes: 2000, want: "one\ntwo"},
		{name: "no newline at the end", content: "one\ntwo", maxLines: 20, maxBytes: 2000, want: "one\ntwo"},
		{name: "exactly the line limit", content: "a\nb\nc\n", maxLines: 3, maxBytes: 2000, want: "a\nb\nc"},
		{name: "more lines than the limit", content: "a\nb\nc\nd\ne\n", maxLines: 2, maxBytes: 2000, want: "d\ne"},
		{name: "one line asked for", content: "a\nb\nc\n", maxLines: 1, maxBytes: 2000, want: "c"},
		{name: "blank lines inside are lines", content: "a\n\nb\n", maxLines: 2, maxBytes: 2000, want: "\nb"},
		{name: "windows line ends at the end are dropped", content: "a\r\nb\r\n", maxLines: 20, maxBytes: 2000, want: "a\r\nb"},
		{
			name:     "longer than the byte limit drops the half line",
			content:  "first line\nsecond line\nthird\n",
			maxLines: 20, maxBytes: 14,
			// The last 14 bytes are "nd line\nthird\n".
			want: "third",
		},
		{
			name:     "cut on a line boundary keeps the whole first line",
			content:  "first\nsecond\nthird\n",
			maxLines: 20, maxBytes: 13,
			// The last 13 bytes are exactly "second\nthird\n".
			want: "second\nthird",
		},
		{
			name:     "one line longer than the byte limit keeps its end",
			content:  "0123456789abcdef\n",
			maxLines: 20, maxBytes: 5,
			// The last 5 bytes are "cdef\n".
			want: "cdef",
		},
		{
			name:     "a long file is cut by bytes then by lines",
			content:  strings.Repeat("x", 5000) + "\n" + strings.Repeat("line\n", 50),
			maxLines: 3, maxBytes: 100,
			want: "line\nline\nline",
		},
		{name: "invalid bytes are replaced", content: "ok \xff\xfe bad\n", maxLines: 20, maxBytes: 2000, want: "ok \uFFFD bad"},
		{
			name:     "a character split by the cut is replaced and the limit still holds",
			content:  "ééééé\n",
			maxLines: 20, maxBytes: 6,
			// The last 6 bytes are half an é, two whole ones and the
			// newline. The half becomes U+FFFD (3 bytes), which makes 7,
			// so it is dropped again to stay within 6.
			want: "éé",
		},
		{name: "zero lines", content: "a\nb\n", maxLines: 0, maxBytes: 2000, want: ""},
		{name: "zero bytes", content: "a\nb\n", maxLines: 20, maxBytes: 0, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "after-merge.log")
			if tc.content != missing {
				if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := TailFile(path, tc.maxLines, tc.maxBytes)
			if got != tc.want {
				t.Errorf("TailFile = %q, want %q", got, tc.want)
			}
			if len(got) > tc.maxBytes && tc.maxBytes > 0 {
				t.Errorf("TailFile returned %d bytes, over the limit of %d", len(got), tc.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TailFile returned text that is not valid UTF-8: %q", got)
			}
		})
	}

	t.Run("a folder is not a file", func(t *testing.T) {
		if got := TailFile(t.TempDir(), 20, 2000); got != "" {
			t.Errorf("TailFile on a folder = %q, want empty", got)
		}
	})
}
