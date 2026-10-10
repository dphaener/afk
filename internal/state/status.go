// status.go reads and validates `.afk/{name}/status.json` per
// plugins/afk/skills/conductor/status-schema.md (in this repo).
// That doc is the binding contract; this file implements its reader rules
// verbatim (§2 file format, §6 "Rules for readers") and invents no new
// fields or semantics.
//
// Concurrent writers exist (status-schema.md §6): every writer uses
// re-read-merge + same-directory atomic rename, so a reader should never
// observe a torn file on POSIX rename semantics — but non-conforming
// writers, exotic filesystems, or a writer that crashed mid-write (before
// its rename) can still leave a truncated or unparseable file behind. This
// reader retries once, then surfaces staleness rather than panicking or
// returning a hard error: per the schema, "a missing or unparseable
// status.json is ... 'stale' to parsers."
package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Phase is one of the six lifecycle phases in status-schema.md §2/§8.
type Phase string

const (
	PhasePlanning  Phase = "planning"
	PhaseExecuting Phase = "executing"
	PhaseReviewing Phase = "reviewing"
	PhaseFixing    Phase = "fixing"
	PhaseDone      Phase = "done"
	PhaseFailed    Phase = "failed"
)

// Terminal reports whether phase is one of the two terminal phases
// (status-schema.md §2: "done and failed are terminal").
func (p Phase) Terminal() bool {
	return p == PhaseDone || p == PhaseFailed
}

func validPhase(s string) (Phase, bool) {
	switch Phase(s) {
	case PhasePlanning, PhaseExecuting, PhaseReviewing, PhaseFixing, PhaseDone, PhaseFailed:
		return Phase(s), true
	default:
		return "", false
	}
}

// Status mirrors the nine status.json fields exactly as defined in
// status-schema.md §2. Field order matches the schema table.
type Status struct {
	SessionID     string `json:"session_id"`
	PlanName      string `json:"plan_name"`
	Phase         Phase  `json:"phase"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	LoopCount     int    `json:"loop_count"`
	AwaitingInput bool   `json:"awaiting_input"`
	TmuxWindow    string `json:"tmux_window"`
	UpdatedAt     string `json:"updated_at"`
}

// StaleReason enumerates why ReadStatus surfaced a result as stale instead
// of a valid Status. Every case here is grounded directly in
// status-schema.md: unknown keys, phase mismatches, and name mismatches are
// all explicitly called "corruption/staleness" by the schema; missing/
// unparseable/trailing-data files fall under "unparseable ... is ...
// 'stale' to parsers".
type StaleReason string

const (
	StaleNone         StaleReason = ""
	StaleMissing      StaleReason = "missing"
	StaleUnreadable   StaleReason = "unreadable"
	StaleUnparseable  StaleReason = "unparseable"
	StaleUnknownField StaleReason = "unknown-field"
	StaleTrailingData StaleReason = "trailing-data"
	StaleInvalidPhase StaleReason = "invalid-phase"
	StaleNameMismatch StaleReason = "plan-name-mismatch"
	StaleInvariant    StaleReason = "invariant-violation"
)

// ReadResult is the outcome of ReadStatus. When Stale is true, Status is the
// zero value and callers must not trust it — Reason/Detail explain why.
type ReadResult struct {
	Status Status
	Stale  bool
	Reason StaleReason
	Detail string
}

// statusRetryDelay is the pause between the first failed read and the
// single retry, giving a writer's atomic rename (§6: re-read, modify,
// write-temp, rename, back-to-back with nothing interleaved) a brief window
// to land. It is intentionally small: the write protocol's residual window
// is measured in microseconds, not milliseconds.
var statusRetryDelay = 2 * time.Millisecond

// ReadStatus reads and validates the status.json at path.
//
// It never panics and never returns a Go error for a corrupt or torn file —
// per status-schema.md §6, that is "stale" to parsers, not a hard failure,
// because concurrent writers are a normal and expected condition. A read or
// parse failure is retried exactly once (the "retry-once-then-stale"
// behavior called for by the task); if the retry also fails, the final
// attempt's staleness is returned.
func ReadStatus(path string) ReadResult {
	res := readStatusOnce(path)
	if res.Stale {
		time.Sleep(statusRetryDelay)
		res = readStatusOnce(path)
	}
	return res
}

func readStatusOnce(path string) ReadResult {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ReadResult{Stale: true, Reason: StaleMissing, Detail: err.Error()}
		}
		return ReadResult{Stale: true, Reason: StaleUnreadable, Detail: err.Error()}
	}

	var st Status
	dec := json.NewDecoder(bytes.NewReader(data))
	// status-schema.md §2: "No keys beyond the nine below. Parsers MUST
	// reject unknown keys as corruption rather than ignore them silently."
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return ReadResult{Stale: true, Reason: staleReasonForDecodeErr(err), Detail: err.Error()}
	}
	// §2: "a single JSON object, no comments, no trailing garbage." A
	// second value on the stream (or any non-whitespace trailer) violates
	// that.
	if dec.More() {
		return ReadResult{Stale: true, Reason: StaleTrailingData, Detail: "trailing data after the JSON object"}
	}

	if _, ok := validPhase(string(st.Phase)); !ok {
		return ReadResult{Stale: true, Reason: StaleInvalidPhase, Detail: fmt.Sprintf("phase %q is not one of planning|executing|reviewing|fixing|done|failed", st.Phase)}
	}

	// §2 invariant 1: plan_name MUST equal the containing directory name.
	wantName := filepath.Base(filepath.Dir(path))
	if st.PlanName != wantName {
		return ReadResult{Stale: true, Reason: StaleNameMismatch, Detail: fmt.Sprintf("plan_name %q does not match containing directory %q", st.PlanName, wantName)}
	}

	// §2 invariant 2: phase != "planning" ⇒ session_id != "". The schema
	// explicitly permits (but does not require) parsers to treat a
	// violation as invalid; we do, since a session_id lost outside
	// planning silently kills hook targeting (schema's own words) and a
	// dashboard should say so rather than display a phantom session.
	if st.Phase != PhasePlanning && st.SessionID == "" {
		return ReadResult{Stale: true, Reason: StaleInvariant, Detail: "session_id is empty outside phase planning (invariant 2)"}
	}

	return ReadResult{Status: st}
}

// staleReasonForDecodeErr distinguishes an unknown-field rejection
// (DisallowUnknownFields) from any other decode failure (truncated/torn
// read, invalid syntax, type mismatch) for diagnostic purposes. Both are
// staleness, per the schema; only the reported reason differs.
func staleReasonForDecodeErr(err error) StaleReason {
	if strings.Contains(err.Error(), "unknown field") {
		return StaleUnknownField
	}
	return StaleUnparseable
}
