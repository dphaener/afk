package session

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/dphaener/afk/internal/host"
)

// Runtime is the one seam every caller uses to start, stop, restart, type
// into or open a session. Today there is a single implementation, afk's own
// detached host. T17.7 adds a second one that runs the session inside a Rex
// terminal; it must be a second implementation of this interface, not a
// second set of call sites, or a caller that skipped the seam would drive a
// Rex session as if afk's host owned it.
//
// Callers get their Runtime from RuntimeFor, which reads the session's
// host.json rather than any machine setting, so a session keeps the runtime
// that made it for its whole life (docs/PLAN.md §2, "Rex is optional").
//
// The methods mirror the functions they replace, argument for argument, so
// moving a caller onto the seam changes no behaviour.
type Runtime interface {
	// Name is the word host.json records for this runtime.
	Name() string
	// Spawn starts the session's host (SpawnHost).
	Spawn(spec HostSpawn, d Deps) (HostProcess, error)
	// Undo stops a host that Spawn started, for the create rollback.
	Undo(h HostProcess, d Deps) error
	// Stop ends a session's host and removes its files (StopHost).
	Stop(worktree, name string, d CleanDeps) (HostStop, error)
	// SendInput types text into the session and presses Enter.
	SendInput(worktree, name, text string, wait time.Duration) error
	// SendKeys types raw key chunks into the session.
	SendKeys(worktree, name string, chunks [][]byte, wait time.Duration) error
	// Restart asks the host to restart its child in place. A non-empty
	// refused is the host's own reason for saying no.
	Restart(worktree, name string, wait, reply time.Duration) (refused string, err error)
	// Exit asks the host to end the session.
	Exit(worktree, name string, wait time.Duration) error
	// Dial opens a connection to the session's terminal.
	Dial(worktree, name string, wait time.Duration) (net.Conn, error)
}

// ErrUnknownRuntime is wrapped by the error RuntimeFor returns for a
// host.json that names a runtime this afk does not know.
var ErrUnknownRuntime = errors.New("unknown session runtime")

// afkRuntime is afk's own detached host, the only runtime before T17.7. It
// delegates unchanged to the functions the callers used to call directly.
type afkRuntime struct{}

func (afkRuntime) Name() string { return host.RuntimeAfk }

func (afkRuntime) Spawn(spec HostSpawn, d Deps) (HostProcess, error) {
	return SpawnHost(spec, d)
}

func (afkRuntime) Undo(h HostProcess, d Deps) error { return stopSpawnedHost(d, h) }

func (afkRuntime) Stop(worktree, name string, d CleanDeps) (HostStop, error) {
	return StopHost(worktree, name, d)
}

func (afkRuntime) SendInput(worktree, name, text string, wait time.Duration) error {
	return host.SendInput(worktree, name, text, wait)
}

func (afkRuntime) SendKeys(worktree, name string, chunks [][]byte, wait time.Duration) error {
	return host.SendKeys(worktree, name, chunks, wait)
}

func (afkRuntime) Restart(worktree, name string, wait, reply time.Duration) (string, error) {
	return host.RequestRestart(worktree, name, wait, reply)
}

func (afkRuntime) Exit(worktree, name string, wait time.Duration) error {
	return host.RequestExit(worktree, name, wait)
}

// Dial prefers the socket the host recorded over the derived path, so a host
// listening somewhere unexpected is still reachable.
func (afkRuntime) Dial(worktree, name string, wait time.Duration) (net.Conn, error) {
	meta, err := host.ReadMeta(worktree, name)
	if err != nil {
		return nil, fmt.Errorf("reading the host record for %s: %w", name, err)
	}
	socket := meta.Socket
	if socket == "" {
		socket, err = host.SocketPath(worktree, name)
		if err != nil {
			return nil, err
		}
	}
	return host.Dial(socket, wait)
}

// runtimeNamed maps the word in host.json to its implementation. An empty
// word is afk, so every host.json written before the field existed reads as
// it always did. name is only used to make the refusal say which session.
func runtimeNamed(name, word string) (Runtime, error) {
	switch word {
	case "", host.RuntimeAfk:
		return afkRuntime{}, nil
	}
	return nil, fmt.Errorf("%w: session %s was started by the %q runtime, which this afk does not know. Use the afk that started it, or update afk, and try again",
		ErrUnknownRuntime, name, word)
}

// RuntimeFor chooses the runtime of the session name in worktree from its
// host.json. A missing or unparseable file gives afk's runtime: a new session
// has no host.json yet, and StopHost must keep cleaning a corrupt one. An
// unknown word is refused, so a session a newer afk started is never
// half-handled by one that does not know how.
func RuntimeFor(worktree, name string) (Runtime, error) {
	meta, err := host.ReadMeta(worktree, name)
	if err != nil {
		return afkRuntime{}, nil
	}
	return runtimeNamed(name, meta.Runtime)
}
