package tokens

import (
	"errors"
	"fmt"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// ErrNoCalls is returned by Record when the session's transcript holds no
// model calls yet. It is distinct from provider.ErrNoTranscript (no transcript
// at all) because the fix differs: wait for the session to do some work.
var ErrNoCalls = errors.New("the transcript holds no model calls")

// AppendError reports that the ledger could not be written. Record returns it
// apart from the read errors so a caller can say "nothing was recorded" with
// the ledger path, which a failed read already names in its own message.
type AppendError struct {
	Path string
	Err  error
}

func (e *AppendError) Error() string {
	return fmt.Sprintf("could not write the ledger at %s: %v", e.Path, e.Err)
}
func (e *AppendError) Unwrap() error { return e.Err }

// LedgerReadError reports that the existing ledger could not be read. Its text
// is the underlying error's, which already names the ledger path.
type LedgerReadError struct{ Err error }

func (e *LedgerReadError) Error() string { return e.Err.Error() }
func (e *LedgerReadError) Unwrap() error { return e.Err }

// RecordInput is everything Record needs. Now is taken once from here and the
// clock is never called, so a test (or the daemon's injected clock) decides
// the time stamped on every line.
type RecordInput struct {
	Provider     provider.Provider
	Home         string
	MainCheckout string
	Task         string
	Session      string
	SessionID    string
	Now          time.Time
}

// RecordResult describes what Record found and did. Lines and Calls are set
// whenever the transcript was read, including when AlreadyRecorded is true
// (nothing was appended then).
type RecordResult struct {
	Lines []Line
	Calls int
	// Records are the per-call records the lines were built from. The
	// command's start-up section is computed from them, and handing them
	// back saves reading the transcripts a second time.
	Records         []provider.TokenRecord
	AlreadyRecorded bool
}

// Record reads one session's transcripts and appends its lines to the ledger
// once. It exists so `afk tokens --record` and the build loop share one
// implementation. The provider's sentinel errors come back wrapped (text unchanged, so
// callers can print it as is), so errors.Is(err, provider.ErrTokensUnsupported) and ErrNoTranscript hold; on
// any error nothing is written. The provider is asked exactly once.
func Record(in RecordInput) (RecordResult, error) {
	records, err := in.Provider.SessionTokens(provider.TokenQuery{Home: in.Home, SessionID: in.SessionID})
	if err != nil {
		return RecordResult{}, fmt.Errorf("%w", err)
	}
	if len(records) == 0 {
		return RecordResult{}, ErrNoCalls
	}
	res := RecordResult{
		Lines:   Aggregate(records, in.Task, in.Session, in.Now),
		Calls:   len(records),
		Records: records,
	}
	path := LedgerPath(in.MainCheckout)
	existing, err := Read(path)
	if err != nil {
		return RecordResult{}, &LedgerReadError{Err: err}
	}
	if HasSession(existing, in.Session) {
		res.AlreadyRecorded = true
		return res, nil
	}
	if err := Append(path, res.Lines); err != nil {
		return RecordResult{}, &AppendError{Path: path, Err: err}
	}
	return res, nil
}
