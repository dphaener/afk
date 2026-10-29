package probe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/dphaener/afk/internal/rex"
)

// rexDeadline bounds the one server.status call. A server that does not
// answer in this time counts as not answering, so a hung Rex cannot hang afk.
const rexDeadline = 5 * time.Second

// RexFacts is what ProbeRex read about Rex on this machine.
//
// Revision is only meaningful when RevisionKnown is true: the login-server
// proxy reports revision 0, which means "hidden", not "old".
type RexFacts struct {
	Installed     bool
	ServerAnswers bool
	Revision      int
	RevisionKnown bool
	InsideRex     bool
}

// RexState turns the facts into a probe Result. Rex is optional, so no branch
// is fatal; Present is false only when afk cannot use Rex, and the message
// says why in plain words.
func RexState(f RexFacts) Result {
	switch {
	case !f.Installed:
		return Result{Present: false, DegradationMessage: "Rex is not installed — optional; afk runs its own sessions without it"}
	case !f.ServerAnswers:
		return Result{Present: false, DegradationMessage: "Rex is installed but its server is not running — start it with `rex server start`"}
	case f.RevisionKnown && f.Revision < rex.MinRevision:
		return Result{Present: false, DegradationMessage: "Rex is too old — afk needs api revision 7 or later"}
	case f.InsideRex:
		return Result{Present: true, DegradationMessage: "running inside a Rex terminal"}
	case !f.RevisionKnown:
		return Result{Present: true, DegradationMessage: "Rex is running, but the proxy hides its revision, so afk cannot check that it is recent enough"}
	}
	return Result{Present: true}
}

// RexDeps are the seams ProbeRex reads through, so tests never touch the real
// environment, PATH or a Rex server.
type RexDeps struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Check    func(ctx context.Context) (rex.Server, error)
}

// DefaultRexDeps wires the real environment, PATH and the rex command.
func DefaultRexDeps() RexDeps {
	caller := rex.NewCaller(rex.NewRun(rexDeadline), "rex")
	return RexDeps{
		Getenv:   os.Getenv,
		LookPath: exec.LookPath,
		Check: func(ctx context.Context) (rex.Server, error) {
			return rex.CheckServer(ctx, caller)
		},
	}
}

// ProbeRex reads the machine for Rex. It asks the Rex server a question, so
// it can take up to the deadline: never call it from the dashboard tick or
// from internal/discover, which must not block.
//
// InsideRex needs both REX_SESSION and REX_BLOCK, because either one alone
// can be left over in a shell that is not a Rex terminal. A too-old server
// still answered, so it reports ServerAnswers with its revision; any other
// check error counts as no answer.
func ProbeRex(ctx context.Context, d RexDeps) RexFacts {
	var f RexFacts
	f.InsideRex = d.Getenv("REX_SESSION") != "" && d.Getenv("REX_BLOCK") != ""
	if _, err := d.LookPath("rex"); err != nil {
		return f
	}
	f.Installed = true
	s, err := d.Check(ctx)
	if err != nil && !errors.Is(err, rex.ErrTooOld) {
		return f
	}
	f.ServerAnswers = true
	f.Revision = s.Revision
	f.RevisionKnown = s.Revision != 0
	return f
}
