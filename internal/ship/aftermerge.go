package ship

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// afterMergeWaitDelay is the os/exec WaitDelay set on the after-merge
// command. It is a guard, not the mechanism: the command's output goes to an
// *os.File, which os/exec hands to the child as it is (Cmd.writerDescriptor),
// with no pipe and no copying goroutine, so Wait returns as soon as the shell
// itself has ended, whatever a grandchild still holds open. WaitDelay bounds
// the wait on "a child process that exits but leaves its I/O pipes unclosed";
// it is set so that this call stays bounded even if the output is one day
// sent through a pipe.
const afterMergeWaitDelay = 5 * time.Second

// RunAfterMerge runs a project's after-merge command (`[ship] after_merge`,
// PLAN T2.2a) and waits for it, for at most timeout. It is the real, impure
// runner; when to run it, and what its result means, is the ship state
// machine's business.
//
// It answers in one of four ways:
//
//	the command ended with exit 0        (0, false, nil)
//	it ended with another exit code      (code, false, nil)
//	it was still running at the timeout  (-1, true, nil)
//	it never ran                         (-1, false, err)
//
// "Never ran" means the log could not be opened or the shell could not be
// started (a dir that is not there, no sh). A command ended by a signal that
// was not this function's own is reported the way a shell reports it, as 128
// plus the signal's number, so the owner reads "137" and not "-1".
//
// Why a shell: the value is a command line the owner wrote in afk.toml, such
// as "make install && afk plugin install", and `&&`, pipes and variables mean
// something only to a shell. Splitting it into words here would run a
// different command from the one the owner typed in a terminal.
//
// Why a process group: the shell is only the first process. A `make` starts a
// compiler, and killing the shell alone at the timeout would leave the
// compiler running, still writing into the main checkout, after afk has told
// the owner the command was stopped. So the shell is started as the leader of
// a new group (Setpgid), everything it starts is in that group unless it
// leaves on purpose, and the timeout sends SIGKILL to the group. Nothing
// outside that group is ever signalled. One narrow race is left and accepted:
// the group is named by the shell's pid, and if the shell ends and is reaped
// in the instant the timer fires, the signal goes to a group that may no
// longer exist. A group id stays taken while any member lives, so the signal
// can reach a stranger only if the whole group is gone and the kernel has
// already given the same number to a new group leader.
//
// Why the output goes to a file and not to memory: a build can print without
// limit, and the daemon lives for weeks; the file costs nothing while the
// command runs and is still there for the owner afterwards. It also means a
// daemon that dies while the command runs loses none of the output. The log
// is truncated at the start of each run, so it always holds one run. A line
// naming the command and the folder is written before it starts and a line
// with the result after it ends; both start with "afk:".
//
// The command gets the daemon's own environment (Cmd.Env left nil) and the
// null device as its standard input (Cmd.Stdin left nil), so a command that
// asks a question reads end-of-file and fails, and does not wait for an owner
// who is not there.
//
// There is no context on purpose: a daemon told to stop does not kill the
// command. Half a `make install` is worse than a whole one, and the next
// daemon finds a run that started and never finished and stops the line
// (PLAN T2.2a, assumption A12). A timeout of zero or less stops the command
// at once; the project config never allows one.
func RunAfterMerge(dir, command, logPath string, timeout time.Duration) (exitCode int, timedOut bool, err error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, false, fmt.Errorf("could not make the folder for the after-merge log: %w", err)
	}
	// O_APPEND so that afk's own two lines and whatever the command writes
	// each land at the end, even if the command reopens the file itself.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return -1, false, fmt.Errorf("could not open the after-merge log: %w", err)
	}
	defer logFile.Close()

	fmt.Fprintf(logFile, "afk: running the after-merge command in %s: %s\n", dir, command)

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = afterMergeWaitDelay

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(logFile, "afk: the command could not be started: %v\n", err)
		return -1, false, fmt.Errorf("could not start the after-merge command: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		// Setpgid with no Pgid makes the shell's pid the group's id, so the
		// negative pid names the group. An error here means the group is
		// already gone, which is what was wanted.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		fmt.Fprintf(logFile, "afk: the command was stopped after %s\n", timeout)
		return -1, true, nil
	}

	code, err := afterMergeExit(cmd.ProcessState, waitErr)
	if err != nil {
		fmt.Fprintf(logFile, "afk: the command's result could not be read: %v\n", err)
		return -1, false, fmt.Errorf("could not read the after-merge command's result: %w", err)
	}
	fmt.Fprintf(logFile, "afk: the command finished with exit code %d\n", code)
	return code, false, nil
}

// afterMergeExit turns what Wait returned into an exit code. An error that is
// neither "it exited non-zero" nor the WaitDelay notice means the result is
// not known, and that is an error for the caller, never a made-up zero.
func afterMergeExit(state *os.ProcessState, waitErr error) (int, error) {
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil, errors.Is(waitErr, exec.ErrWaitDelay):
		// ErrWaitDelay is returned only for a command that "has otherwise
		// exited with a successful status" (os/exec, Cmd.WaitDelay).
		return 0, nil
	case errors.As(waitErr, &exitErr):
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		if code := state.ExitCode(); code > 0 {
			return code, nil
		}
		// Not signalled and no exit code above zero: nothing a ledger
		// should record as a number.
		return -1, waitErr
	default:
		return -1, waitErr
	}
}

// TailFile returns the end of a file as text for an inbox item: the last
// maxLines lines, never more than maxBytes bytes, with no newline at the end.
// It returns "" when the file is missing, empty, cannot be read, or holds
// only newlines, and when either limit is zero or less.
//
// It reads only the last maxBytes bytes (and one more, to learn whether the
// cut fell on a line boundary), never the whole file: an after-merge log can
// be many megabytes of compiler output and the item needs twenty lines. When
// the cut falls inside a line that half line is dropped, unless it is all
// there is, because half a line of a path reads as a different path.
//
// The result is always valid UTF-8, since it goes into JSON and onto a
// phone: bytes that are not valid UTF-8, including a character the cut
// split, are replaced with U+FFFD. The replacement is three bytes long, so
// the byte limit is applied again afterwards, from the front.
func TailFile(path string, maxLines, maxBytes int) string {
	if maxLines <= 0 || maxBytes <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}

	// One byte before the window says whether the window starts a line.
	want := int64(maxBytes) + 1
	start := info.Size() - want
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return ""
	}
	buf = buf[:n]

	if int64(len(buf)) == want {
		wholeLine := buf[0] == '\n'
		buf = buf[1:]
		if !wholeLine {
			if i := strings.IndexByte(string(buf), '\n'); i >= 0 && strings.Trim(string(buf[i+1:]), "\r\n") != "" {
				buf = buf[i+1:]
			}
		}
	}

	text := strings.TrimRight(string(buf), "\r\n")
	if text == "" {
		return ""
	}

	// Walk back over maxLines line ends; what follows the last one found is
	// the tail.
	cut := len(text)
	for lines := 0; lines < maxLines; lines++ {
		i := strings.LastIndexByte(text[:cut], '\n')
		if i < 0 {
			cut = -1
			break
		}
		cut = i
	}
	text = text[cut+1:]

	text = strings.ToValidUTF8(text, "�")
	for len(text) > maxBytes {
		_, size := utf8.DecodeRuneInString(text)
		text = text[size:]
	}
	return text
}
