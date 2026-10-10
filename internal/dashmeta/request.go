package dashmeta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// requestFilePrefix and requestFileSuffix bracket the pid in a focus-request
// file's basename: request.<pid>.json.
//
// The pid is the TARGET dashboard's, and scoping the file to it is what makes a
// click act on exactly one dashboard. A single shared request.json was honored by
// EVERY running dashboard: each one saw the same write, each one called
// openPaneFor on the same session, and because internal/host serves exactly one
// client the second connection STOLE the session from the first, leaving the
// first with a pane whose host had moved on. That is the same failure W1.T2 had
// already designed Entry storage around — one file per pid — and this is the one
// place the design had regressed to a shared path.
//
// The shape also has to stay out of isEntryFileName's positive match, which
// accepts a bare decimal pid followed by ".json" and nothing else. The "request."
// prefix is non-numeric, so it is rejected there: these files can never be
// mistaken for entries and swept by ReadEntry's dead-pid self-heal.
const (
	requestFilePrefix = "request."
	requestFileSuffix = ".json"
)

// RequestFileName is the basename of the focus request addressed to the
// dashboard running as pid. Exported because the dashboard's fsnotify watcher
// matches events by basename (see internal/ui's request watcher) and a second
// literal there would be a rename waiting to go unnoticed.
func RequestFileName(pid int) string {
	return fmt.Sprintf("%s%d%s", requestFilePrefix, pid, requestFileSuffix)
}

// IsRequestFileName reports whether name is a focus-request file this package
// owns: the "request." prefix, a decimal pid, then ".json". Pure, so the name
// matrix is table-testable without a directory.
//
// It exists as the mirror of isEntryFileName so the two file kinds sharing
// afk-dash/ are each recognized POSITIVELY rather than by excepting the other.
// A leading '0' is rejected for the same reason isEntryFileName rejects one: no
// pid a liveness check could confirm is written that way, and requiring a digit
// first is what keeps the atomic rename's ".request.<pid>.json.tmp.<pid>" out of
// any scan.
func IsRequestFileName(name string) bool {
	rest, ok := strings.CutPrefix(name, requestFilePrefix)
	if !ok {
		return false
	}
	digits, ok := strings.CutSuffix(rest, requestFileSuffix)
	if !ok || digits == "" || digits[0] == '0' {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// RequestMaxAge is how long a focus request stays actionable. A click the user
// made minutes ago has already missed its moment: honoring it would yank their
// focus out of whatever they moved on to, which is worse than doing nothing. A
// request older than this is consumed and discarded rather than acted on.
//
// 60s is generous on purpose — the dashboard reads the file within milliseconds
// of the write via fsnotify, and within one 2s discovery tick when fsnotify is
// unavailable — so the only requests this rejects are ones nothing was there to
// consume (no dashboard running when the toast was clicked, then a dashboard
// started later and found the file waiting).
const RequestMaxAge = 60 * time.Second

// Request is a "bring this session to the front" message from a clicked
// notification to a running dashboard: the session to open and focus, plus when
// the click happened.
//
// The NAME travels here rather than as keystrokes into the dashboard's pane, and
// that is the whole reason this file exists. A live dashboard has no IPC and no
// name-search keybinding, so the only way to select a named session with
// send-keys is to count j/k presses against the current row order — and that
// order shifts as sessions change phase, so the count races the very thing it is
// trying to target. A name in a file cannot select the wrong session.
type Request struct {
	Name string `json:"name"`
	// RequestedAt is RFC3339, matching Entry.StartedAt. A value that does not
	// parse is treated as stale (see RequestFresh), never as an error.
	RequestedAt string `json:"requested_at"`
}

// Dir is afk-dash/ under the given git common dir, or "" when gitCommonDir is
// not absolute (the same "no location available" answer WriteEntry gives).
func Dir(gitCommonDir string) string {
	if !filepath.IsAbs(gitCommonDir) {
		return ""
	}
	return filepath.Join(gitCommonDir, "afk-dash")
}

// RequestPath is the absolute path of the focus request addressed to pid, or ""
// when gitCommonDir is not absolute.
func RequestPath(gitCommonDir string, pid int) string {
	dir := Dir(gitCommonDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, RequestFileName(pid))
}

// EnsureDir creates afk-dash/ if it does not exist and returns its path. The
// dashboard's watcher needs it because fsnotify watches the DIRECTORY (see
// internal/ui), and a dashboard running outside tmux never wrote an Entry, so
// the directory can legitimately be absent at watch time.
func EnsureDir(gitCommonDir string) (string, error) {
	dir := Dir(gitCommonDir)
	if dir == "" {
		return "", fmt.Errorf("dashmeta: %q is not an absolute git common dir", gitCommonDir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}

// WriteRequest atomically publishes a focus request for name, addressed to the
// dashboard running as targetPID and stamped at.
//
// targetPID is what makes a click act on exactly ONE dashboard. The writer
// (`afk notify`) has no opinion about which dashboard should win, so it passes
// the pid of the entry ReadEntry already picked — the first live one — and every
// other dashboard ignores the file because it is not addressed to them. Without
// this, two dashboards both honored one click and the second's connection stole
// the session out of the first's pane (internal/host serves exactly one client).
//
// Same temp-file + same-directory rename discipline as WriteEntry, for the same
// reason with an extra edge: the reader here is an fsnotify watcher that wakes
// on the write, so a non-atomic write would be observed mid-flush and parse as
// malformed — the one failure mode that would look like a bug rather than a
// no-op. The temp name carries the WRITER's pid, distinct from targetPID in the
// final name, so two clicks landing together cannot truncate each other's temp
// file; the rename then decides the winner, and either one is a correct answer
// (both name a session the user asked for).
//
// An empty name writes nothing: a request that names no session cannot be
// honored, and an unhonorable file on disk only invites a reader to guess. A
// non-positive targetPID writes nothing for the same reason — there is no
// dashboard it could be addressed to.
func WriteRequest(gitCommonDir, name string, targetPID int, at time.Time) error {
	if name == "" || targetPID <= 0 {
		return nil
	}
	dir, err := EnsureDir(gitCommonDir)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(Request{
		Name:        name,
		RequestedAt: at.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dash request: %w", err)
	}
	data = append(data, '\n')

	final := RequestFileName(targetPID)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", final, os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp dash request: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, final)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming dash request into place: %w", err)
	}
	return nil
}

// ReadRequest returns the focus request addressed to pid, or ok=false when there
// is none. Missing, unreadable and unparseable are all "none" — never an error —
// because the only consumer is a dashboard whose correct response to every one
// of them is identical: do nothing.
//
// A dashboard reads ONLY its own pid's file, which is the whole two-dashboard
// fix: a request addressed to another dashboard is not "found" here, so this one
// never opens a session it was not asked to and never steals it out of the pane
// the intended dashboard put it in.
//
// A file that exists but cannot be parsed is REMOVED, mirroring ReadEntry's
// dead-pid self-heal. Otherwise it would sit there being re-read by every
// fallback poll for the life of the repo, and a later writer's atomic rename is
// the only other thing that would ever clear it.
func ReadRequest(gitCommonDir string, pid int) (Request, bool) {
	path := RequestPath(gitCommonDir, pid)
	if path == "" {
		return Request{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Request{}, false
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil || r.Name == "" {
		_ = os.Remove(path)
		return Request{}, false
	}
	return r, true
}

// RemoveRequest deletes the focus request addressed to pid, best-effort. The
// consumer calls this for every request it has decided about — acted on, stale,
// or naming a session it does not know — so a request is honored at most once and
// a request nobody can honor does not linger.
func RemoveRequest(gitCommonDir string, pid int) error {
	path := RequestPath(gitCommonDir, pid)
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SweepStaleRequests deletes request files addressed to dead pids.
//
// It is the counterpart to ReadEntry's dead-pid self-heal, and it is needed for
// the same reason: with requests addressed per dashboard, nothing else would ever
// clear one written for a dashboard that died between the click and the read. A
// live dashboard only ever removes its OWN file, so without this the directory
// accumulates one file per such click, forever.
//
// Only names IsRequestFileName accepts are touched, so an entry file, a temp
// file, or anything else sharing this rendezvous directory is left alone —
// exactly the guarantee whose absence once made ReadEntry delete request.json.
// Liveness is the same advisory signal-0 check entries use.
func SweepStaleRequests(gitCommonDir string) {
	dir := Dir(gitCommonDir)
	if dir == "" {
		return
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range names {
		if ent.IsDir() || !IsRequestFileName(ent.Name()) {
			continue
		}
		pid, ok := requestFilePID(ent.Name())
		if !ok || alive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, ent.Name()))
	}
}

// requestFilePID extracts the target pid from a name IsRequestFileName accepts.
func requestFilePID(name string) (int, bool) {
	if !IsRequestFileName(name) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(name, requestFilePrefix), requestFileSuffix)
	pid, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// RequestFresh reports whether r is recent enough to act on. Pure, so the whole
// cutoff matrix is table-testable without a clock or a filesystem.
//
// An unparseable or empty timestamp is NOT fresh: a request that cannot say when
// it was made cannot be shown to be recent, and the safe direction here is to
// discard.
//
// The comparison uses the ABSOLUTE difference. A request stamped in the future is
// clock skew (writer and reader are the same machine, but not necessarily the
// same second across a filesystem with coarse timestamps), not staleness, and
// treating it as infinitely fresh would let one bad clock hold a request open
// forever.
func RequestFresh(r Request, now time.Time, maxAge time.Duration) bool {
	if r.RequestedAt == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, r.RequestedAt)
	if err != nil {
		return false
	}
	diff := now.Sub(at)
	if diff < 0 {
		diff = -diff
	}
	return diff <= maxAge
}
