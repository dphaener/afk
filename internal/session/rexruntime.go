package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/rexlayout"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/worktree"
)

// RexDeps are the seams the Rex runtime reaches Rex and the clock through, so
// every test drives it with rex.Fake and never touches a real Rex server.
// Every nil or zero field has a real default, so the zero RexDeps (what
// RuntimeFor hands back for a session already in Rex) talks to the machine's
// own `rex` command.
//
// They live on the runtime rather than on Deps because Stop is called with
// CleanDeps and Undo with Deps: putting the caller on the runtime gives all
// three methods the same Rex, which is what lets a test assert one ordered
// list of calls across a spawn and a stop.
type RexDeps struct {
	// Caller makes the Rex calls. nil runs the `rex` command with the
	// package's per-call deadline.
	Caller rex.Caller
	// LookPath finds the rex command, to tell "not installed" from "not
	// answering". nil is exec.LookPath.
	LookPath func(file string) (string, error)
	// Theme is the dashboard's resolved theme; the window's terminal takes
	// its foreground, background and cursor from it. The zero Theme is tide,
	// the dashboard's own default.
	Theme palette.Theme
	// ConfigErr is a carried `[runtime] backend` error from config.toml. A
	// start with one is refused before any Rex call, never run on afk's host
	// instead: the owner asked for something afk could not read.
	ConfigErr error
	// HostWait bounds the wait for the host inside the new window to write
	// its host.json. Zero is rexHostWait.
	HostWait time.Duration
	// Sleep is the pause between host.json reads while waiting. nil is
	// time.Sleep; a test uses it to stand in for the host coming up.
	Sleep func(time.Duration)
	// Now is the clock the wait is measured on. nil is time.Now.
	Now func() time.Time
}

const (
	// rexHostWait is how long Spawn waits for the new window's host to
	// record itself. The host writes host.json before it starts the agent,
	// so this covers Rex starting a process and afk starting up, not the
	// agent; ten seconds is far past either on a healthy machine and short
	// enough that a window whose command never ran is reported, not hung on.
	rexHostWait = 10 * time.Second
	// rexHostPoll is the pause between reads of host.json while waiting.
	rexHostPoll = 100 * time.Millisecond
	// rexStopBudget bounds the Rex calls around a stop. The host is already
	// stopped by then, so a Rex that does not answer must not hold up clean.
	rexStopBudget = 15 * time.Second
	// rexSpawnBudget bounds every Rex call one Spawn makes together; each
	// call also has the caller's own deadline.
	rexSpawnBudget = 45 * time.Second
	// runtimeMarkerFile is .afk/{name}/runtime. It names the runtime a
	// session was started on, for when host.json is gone: a relaunch after
	// the host has stopped must stay in Rex (docs/PLAN.md T17.8 A7).
	runtimeMarkerFile = "runtime"
)

func (r RexDeps) withDefaults() RexDeps {
	if r.Caller == nil {
		r.Caller = rex.NewCaller(rex.NewRun(rex.DefaultTimeout), "rex")
	}
	if r.LookPath == nil {
		r.LookPath = exec.LookPath
	}
	if r.Theme.Bg == "" {
		r.Theme = palette.Tide
	}
	if r.HostWait == 0 {
		r.HostWait = rexHostWait
	}
	if r.Sleep == nil {
		r.Sleep = time.Sleep
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	return r
}

// NewRexRuntime is the Rex runtime with its seams set. RuntimeFor returns the
// same runtime with the zero RexDeps for a session already in Rex; a caller
// starting a new session with backend "rex" uses this one, so it can carry
// the theme and a carried config error.
func NewRexRuntime(d RexDeps) Runtime { return rexRuntime{rex: d} }

// rexRuntime is a session whose host runs inside a Rex terminal
// (`afk _host --stdio`). It reuses afkRuntime for everything the host's
// socket still serves (restart, exit, typing), because a stdio host keeps
// its socket for exactly those requests. It differs in three places: Spawn
// opens a Rex window instead of forking a host, Stop also closes that window,
// and Dial refuses, because the terminal is the Rex block and a socket client
// attaching to it would be a second screen on a session that has one.
type rexRuntime struct {
	afkRuntime
	rex RexDeps
}

func (rexRuntime) Name() string { return host.RuntimeRex }

// Dial refuses before looking at the socket, so the sentence is the same
// whether or not the host is still up. It uses the host's own sentence so a
// client that reaches the socket and one that goes through the seam read the
// same words.
func (rexRuntime) Dial(string, string, time.Duration) (net.Conn, error) {
	return nil, errors.New(host.RefuseAttachLine)
}

// Spawn opens one window in the project's Rex session running the session's
// host, and returns once that host has recorded itself in host.json.
//
// The order is what keeps a refusal free of side effects: the carried config
// error, then whether rex is installed, then whether its server answers, all
// before anything is written or created. A refusal never falls back to afk's
// own host, because the owner chose Rex and a session quietly running
// somewhere else is the thing they would not find.
//
// The project's Rex session is the one a sibling session's host.json
// recorded, and only if session.list still shows that id; it is never looked
// up by its label, because labels are free text and two sessions can share
// one (T17.1). With none recorded or listed a new one is created, named after
// the project. An empty session is never destroyed (A6), so a later start
// finds it again through the next host.json.
func (r rexRuntime) Spawn(spec HostSpawn, in Deps) (HostProcess, error) {
	d := in.withDefaults()
	rd := r.rex.withDefaults()
	if spec.Name == "" {
		return HostProcess{}, fmt.Errorf("starting a session in Rex: session name is required")
	}
	if spec.Worktree == "" {
		return HostProcess{}, fmt.Errorf("starting session %q in Rex: worktree is required", spec.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), rexSpawnBudget)
	defer cancel()

	if err := rexRefusal(ctx, rd); err != nil {
		return HostProcess{}, err
	}

	// The same three resolutions SpawnHost makes, so a relaunch that names
	// only the session runs the same child here as on afk's own host.
	spec, err := resolveSpawnProvider(spec)
	if err != nil {
		return HostProcess{}, fmt.Errorf("starting session host: %w", err)
	}
	if spec, err = resolveSpawnPlanning(spec); err != nil {
		return HostProcess{}, fmt.Errorf("starting session host: %w", err)
	}
	if spec, err = resolveSpawnUnattended(spec); err != nil {
		return HostProcess{}, fmt.Errorf("starting session host: %w", err)
	}
	self, err := d.Executable()
	if err != nil {
		return HostProcess{}, fmt.Errorf("locating the afk binary: %w", err)
	}
	if self == "" {
		return HostProcess{}, fmt.Errorf("locating the afk binary: got an empty path")
	}
	socket, err := host.SocketPath(spec.Worktree, spec.Name)
	if err != nil {
		return HostProcess{}, err
	}
	command, err := rexHostCommand(self, spec, socket)
	if err != nil {
		return HostProcess{}, err
	}

	project, recorded, err := rexRecordedSessions(d.Run, spec.Worktree)
	if err != nil {
		return HostProcess{}, err
	}
	facts := rexlayout.Facts{ProjectName: project}
	if len(recorded) > 0 {
		sessions, err := rex.ListSessions(ctx, rd.Caller)
		if err != nil {
			return HostProcess{}, fmt.Errorf("listing Rex sessions: %w", err)
		}
		facts.SessionID, facts.SessionListed = pickListedSession(recorded, sessions)
	}
	// WindowHasBlock stays false: the sibling's window only tells Layout
	// whether to flag it as closed by hand, and a new window is opened either
	// way, so asking Rex about it would be a call whose answer changes nothing.
	plan := rexlayout.Layout(facts)
	sessionID := plan.SessionID
	if plan.CreateSession {
		created, err := rex.CreateSession(ctx, rd.Caller, plan.SessionName)
		if err != nil {
			return HostProcess{}, fmt.Errorf("creating the Rex session %q: %w", plan.SessionName, err)
		}
		sessionID = created.ID
	}

	win, err := rex.CreateWindow(ctx, rd.Caller, sessionID, rexWindowLabel(spec), rex.TerminalOptions{
		Command: command,
		Cwd:     spec.Worktree,
		Size:    rex.Size{Columns: rexlayout.TerminalSize.Columns, Rows: rexlayout.TerminalSize.Rows},
		Theme:   terminalTheme(rd.Theme),
	})
	if err != nil {
		return HostProcess{}, fmt.Errorf("opening a Rex window for %s: %w", spec.Name, err)
	}

	markerWritten, err := writeRuntimeMarker(spec.Worktree, spec.Name)
	if err != nil {
		closeRexWindow(ctx, rd.Caller, sessionID, win.WindowID)
		return HostProcess{}, err
	}
	meta, err := waitForRexHost(spec.Worktree, spec.Name, win.BlockID, rd)
	if err != nil {
		closeRexWindow(ctx, rd.Caller, sessionID, win.WindowID)
		if markerWritten {
			_ = os.Remove(runtimeMarkerPath(spec.Worktree, spec.Name))
		}
		return HostProcess{}, err
	}
	h := HostProcess{
		PID:      meta.PID,
		Socket:   meta.Socket,
		LogPath:  hostLogPath(spec.Worktree, spec.Name),
		MetaPath: hostMetaPath(spec.Worktree, spec.Name),
	}
	if h.Socket == "" {
		h.Socket = socket
	}
	return h, nil
}

// Undo closes the window a Spawn opened, for the create rollback. Closing it
// ends the block, which the stdio host reads as an orderly stop (SIGHUP), so
// the host and its agent go with it. A window that is already gone is what
// Undo wanted, so not_found is not an error.
func (r rexRuntime) Undo(h HostProcess, _ Deps) error {
	rd := r.rex.withDefaults()
	meta, err := readMetaFile(h.MetaPath)
	if err != nil {
		return fmt.Errorf("finding the Rex window to close: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rexStopBudget)
	defer cancel()
	sessionID, windowID, err := rexWindowOf(ctx, rd.Caller, meta)
	if err != nil {
		return fmt.Errorf("finding the Rex window to close: %w", err)
	}
	if windowID == "" {
		return nil
	}
	if err := rex.CloseWindow(ctx, rd.Caller, sessionID, windowID); err != nil && !isRexNotFound(err) {
		return fmt.Errorf("closing the Rex window: %w", err)
	}
	return nil
}

// Stop ends the host with afk's own ladder, then closes its window.
//
// The window id is read before StopHost because StopHost removes host.json,
// the only record of where the window is. The window is closed after, not
// before: closing it first would SIGHUP the host mid-ladder and race the
// socket Exit. A window that is gone (the host closed its own block on a
// clean exit) answers not_found, which is ignored. The Rex session is never
// destroyed (A6): a sibling may be opening a window in it right now.
//
// A Rex failure here is a warning, never the result: the host is already
// stopped, which is the part clean must be sure of, and failing clean
// because Rex is down would leave the owner unable to clean at all.
func (r rexRuntime) Stop(worktree, name string, d CleanDeps) (HostStop, error) {
	d = d.withDefaults()
	rd := r.rex.withDefaults()
	ctx, cancel := context.WithTimeout(context.Background(), rexStopBudget)
	defer cancel()

	var sessionID, windowID string
	if meta, err := host.ReadMeta(worktree, name); err == nil {
		var werr error
		sessionID, windowID, werr = rexWindowOf(ctx, rd.Caller, meta)
		if werr != nil {
			fmt.Fprintf(d.Stderr, "warning: could not find the Rex window of %s, so it may stay open: %v\n", name, werr)
		}
	}

	result, err := StopHost(worktree, name, d)
	if err != nil {
		return result, err
	}
	if windowID == "" {
		return result, nil
	}
	if err := rex.CloseWindow(ctx, rd.Caller, sessionID, windowID); err != nil && !isRexNotFound(err) {
		fmt.Fprintf(d.Stderr, "warning: the host of %s stopped, but its Rex window could not be closed; close it in Rex: %v\n", name, err)
	}
	return result, nil
}

// rexRefusal gathers the Rex facts and asks rexlayout.Refusal. The config
// error is checked first so a bad setting makes no call at all. A server
// that answers but is too old is refused with CheckServer's own words, which
// already say to update Rex; Refusal has no fact for it.
func rexRefusal(ctx context.Context, rd RexDeps) error {
	if rd.ConfigErr != nil {
		return rexlayout.Refusal(rexlayout.BackendRex, rd.ConfigErr, false, false)
	}
	_, lookErr := rd.LookPath("rex")
	installed := lookErr == nil
	answers := false
	if installed {
		_, err := rex.CheckServer(ctx, rd.Caller)
		if errors.Is(err, rex.ErrTooOld) {
			return fmt.Errorf("Rex is too old, so this session was not started (%w). Update Rex, or set [runtime] backend = \"afk\" in config.toml to run sessions without it", err)
		}
		answers = err == nil
	}
	return rexlayout.Refusal(rexlayout.BackendRex, nil, installed, answers)
}

// rexHostCommand is the argv the window runs: the afk-runtime host's own
// argv (buildHostCmd runs self with hostArgs) with --stdio after the
// subcommand, so the two runtimes cannot drift on a single flag. No Rex id is
// passed: the block id exists only once the command runs, so the host reads
// REX_SESSION and REX_BLOCK from its environment (A4).
func rexHostCommand(self string, spec HostSpawn, socket string) ([]string, error) {
	args := hostArgs(spec, socket)
	if len(args) == 0 || args[0] != "_host" {
		return nil, fmt.Errorf("building the Rex host command: expected the _host subcommand, got %q", args)
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, self, args[0], "--stdio")
	return append(out, args[1:]...), nil
}

// rexWindowLabel is the window's first label; the host relabels it on each
// phase change. A one-off has no status.json and so no phase, and gets its
// bare name rather than a label ending in a separator.
func rexWindowLabel(spec HostSpawn) string {
	if spec.MasterPlan {
		return rexlayout.WindowLabel(spec.Name, "", true)
	}
	res := state.ReadStatus(filepath.Join(statusDir(spec.Worktree, spec.Name), "status.json"))
	if res.Stale || res.Status.Phase == "" {
		return spec.Name
	}
	return rexlayout.WindowLabel(spec.Name, string(res.Status.Phase), false)
}

// terminalTheme is the window's colours from the dashboard theme: the
// theme's foreground and background, its accent as the cursor, and a scheme
// read from the background's brightness. The 256-colour palette is left to
// Rex: it is each program's choice, not the dashboard's (A8).
func terminalTheme(t palette.Theme) rex.Theme {
	scheme := "dark"
	if isLightHex(t.Bg) {
		scheme = "light"
	}
	return rex.Theme{Foreground: t.Fg, Background: t.Bg, Cursor: t.Accent, Scheme: scheme}
}

// isLightHex reports whether a #rrggbb colour is light, by its perceived
// brightness (the ITU-R BT.601 weights) against the midpoint. A colour that
// does not parse reads as dark, the common terminal.
func isLightHex(c string) bool {
	c = strings.TrimPrefix(c, "#")
	if len(c) != 6 {
		return false
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return false
	}
	r, g, b := float64(v>>16&0xff), float64(v>>8&0xff), float64(v&0xff)
	return 0.299*r+0.587*g+0.114*b > 127.5
}

// rexRecordedSessions returns the project's name (the main checkout's
// folder) and every Rex session id a session's host.json in any of the
// project's worktrees recorded, in worktree order, without repeats. The
// worktrees come from git, never from worktrunk, as discovery does.
func rexRecordedSessions(run func(dir, name string, args ...string) (string, error), repo string) (string, []string, error) {
	wts, err := worktree.List(run, repo)
	if err != nil {
		return "", nil, fmt.Errorf("finding the project's other sessions: %w", err)
	}
	project := filepath.Base(repo)
	if len(wts) > 0 && wts[0].Path != "" {
		project = filepath.Base(wts[0].Path)
	}
	seen := map[string]bool{}
	var ids []string
	for _, wt := range wts {
		if wt.Bare || wt.Path == "" {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(wt.Path, ".afk", "*", "host.json"))
		if err != nil {
			continue
		}
		for _, p := range paths {
			meta, err := host.ReadMeta(wt.Path, filepath.Base(filepath.Dir(p)))
			if err != nil || meta.Runtime != host.RuntimeRex || meta.Rex == nil || meta.Rex.SessionID == "" {
				continue
			}
			if !seen[meta.Rex.SessionID] {
				seen[meta.Rex.SessionID] = true
				ids = append(ids, meta.Rex.SessionID)
			}
		}
	}
	return project, ids, nil
}

// pickListedSession chooses the first recorded id that session.list shows.
// Only ids are compared; the label of a listed session never matters. With
// none listed it returns the first recorded id as not listed, so Layout
// creates the session again.
func pickListedSession(recorded []string, sessions []rex.Session) (string, bool) {
	listed := map[string]bool{}
	for _, s := range sessions {
		listed[s.ID] = true
	}
	for _, id := range recorded {
		if listed[id] {
			return id, true
		}
	}
	return recorded[0], false
}

// waitForRexHost waits, within rd.HostWait, for host.json to name the Rex
// runtime and the block the window was created with. Matching the block id
// is what keeps a stale host.json from an earlier run from passing for the
// host that was just started.
func waitForRexHost(worktree, name, blockID string, rd RexDeps) (host.Meta, error) {
	deadline := rd.Now().Add(rd.HostWait)
	for {
		meta, err := host.ReadMeta(worktree, name)
		if err == nil && meta.Runtime == host.RuntimeRex && meta.Rex != nil && meta.Rex.BlockID == blockID {
			return meta, nil
		}
		if !rd.Now().Before(deadline) {
			return host.Meta{}, fmt.Errorf("the Rex window for %s opened, but its afk host did not start within %s, so the window was closed. Check that afk runs in a Rex terminal (see %s), then try again",
				name, rd.HostWait, hostLogPath(worktree, name))
		}
		rd.Sleep(rexHostPoll)
	}
}

// rexWindowOf returns the session and window a host.json's Rex block sits
// in: the recorded window id when there is one, otherwise asked of Rex from
// the block id. Empty ids with no error mean there is nothing to close (not
// a Rex host, or a block no longer in a window).
func rexWindowOf(ctx context.Context, c rex.Caller, meta host.Meta) (string, string, error) {
	if meta.Rex == nil || meta.Rex.SessionID == "" {
		return "", "", nil
	}
	if meta.Rex.WindowID != "" {
		return meta.Rex.SessionID, meta.Rex.WindowID, nil
	}
	if meta.Rex.BlockID == "" {
		return "", "", nil
	}
	windowID, found, err := rex.WindowOfBlock(ctx, c, meta.Rex.SessionID, meta.Rex.BlockID)
	if err != nil {
		if isRexNotFound(err) {
			return "", "", nil
		}
		return "", "", err
	}
	if !found {
		return "", "", nil
	}
	return meta.Rex.SessionID, windowID, nil
}

// closeRexWindow is the best-effort close of a window Spawn opened and is
// abandoning. The spawn's own error is what the caller reports.
func closeRexWindow(ctx context.Context, c rex.Caller, sessionID, windowID string) {
	_ = rex.CloseWindow(ctx, c, sessionID, windowID)
}

// isRexNotFound reports a Rex not_found or session_not_found answer, both of
// which the rex command exits 3 for (T17.1, checked on the VM). For a close
// either means the window is already gone.
func isRexNotFound(err error) bool {
	var ce *rex.CallError
	return errors.As(err, &ce) && ce.ExitCode() == 3
}

func readMetaFile(path string) (host.Meta, error) {
	if path == "" {
		return host.Meta{}, errors.New("no host record path was kept for the session")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return host.Meta{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var m host.Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return host.Meta{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, nil
}

func runtimeMarkerPath(worktree, name string) string {
	return filepath.Join(statusDir(worktree, name), runtimeMarkerFile)
}

// writeRuntimeMarker writes "rex" to .afk/{name}/runtime and reports whether
// the file is new, so a failed spawn removes only a marker it wrote and
// leaves one that an earlier Rex start of the same session left.
func writeRuntimeMarker(worktree, name string) (bool, error) {
	path := runtimeMarkerPath(worktree, name)
	_, statErr := os.Stat(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(host.RuntimeRex+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("writing the runtime marker %s: %w", path, err)
	}
	return errors.Is(statErr, os.ErrNotExist), nil
}

// readRuntimeMarker returns the word in .afk/{name}/runtime. ok is false when
// the file is absent or unreadable, which reads as afk, exactly as before
// the marker existed.
func readRuntimeMarker(worktree, name string) (word string, ok bool) {
	data, err := os.ReadFile(runtimeMarkerPath(worktree, name))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}
