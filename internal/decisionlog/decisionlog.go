// Package decisionlog is the append-only record of every decision the build
// loop makes: planner questions, review verdicts, fix-loop outcomes, governor
// actions and ship results (docs/PLAN.md §4.4). It lives at
// .afk/log/decisions.jsonl in the project's main checkout, one JSON object per
// line.
//
// The log exists because the decisions made by the loop are the raw material
// for promoting them into code later (§9): a decision that was taken by a
// policy or by the owner, with the rule that took it, is something that can be
// written down as a rule. That only works if the record is complete and
// trustworthy, so the log is append-only. Nothing rewrites or truncates it; a
// mistaken entry is corrected by a later entry, never by editing an old one.
//
// It is shared by every session and by the daemon, so an append takes an
// exclusive flock on a lock file beside the log, the same way internal/tokens
// does for the token ledger. The lock is a separate file so the log holds only
// data lines. Each entry goes out in a single write call under the lock, so
// two writers never interleave bytes inside a line.
//
// Read is strict where the token ledger is lenient. A token ledger line that
// does not parse is skipped, because a torn tail after a crash must not hide
// the rest. A decision line that does not parse is an error naming its line
// number, because a silently missing decision is worse than a loud one: the
// point of the log is that it is complete.
package decisionlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The three values of ChosenBy. A policy is a rule in the project's config, the
// agent is the session's own judgement, and the owner is a person answering an
// inbox item.
const (
	ChosenByPolicy = "policy"
	ChosenByAgent  = "agent"
	ChosenByOwner  = "owner"
)

// Entry is one decision. The JSON keys are fixed by docs/PLAN.md §4.4 and every
// key is always present: an unset string is "", an empty Options is [], so a
// reader never has to tell a missing key from an empty value.
type Entry struct {
	At       string   `json:"at"`
	Task     string   `json:"task"`
	Session  string   `json:"session"`
	Kind     string   `json:"kind"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Chosen   string   `json:"chosen"`
	ChosenBy string   `json:"chosen_by"`
	Rule     string   `json:"rule"`
}

// Path is where the decision log lives in a project's main checkout.
func Path(mainCheckout string) string {
	return filepath.Join(mainCheckout, ".afk", "log", "decisions.jsonl")
}

// Validate reports whether an entry may be written. It checks only what every
// reader relies on: a timestamp that sorts as a UTC instant, a kind, and a
// known chosen_by. Everything else is free text and may be empty.
func Validate(e Entry) error {
	if e.At == "" {
		return errors.New("decision entry has no time")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.At); err != nil {
		return fmt.Errorf("decision entry time %q is not RFC 3339: %w", e.At, err)
	}
	if !strings.HasSuffix(e.At, "Z") {
		return fmt.Errorf("decision entry time %q is not in UTC (it must end in Z)", e.At)
	}
	if e.Kind == "" {
		return errors.New("decision entry has no kind")
	}
	switch e.ChosenBy {
	case ChosenByPolicy, ChosenByAgent, ChosenByOwner:
	default:
		return fmt.Errorf("decision entry chosen_by %q is not one of policy, agent, owner", e.ChosenBy)
	}
	return nil
}

// Append adds one entry to the log, creating the folder and the file as needed.
// An invalid entry is refused before anything is written. The entry's Options
// may be nil; it is written as [].
func Append(path string, e Entry) error {
	if err := Validate(e); err != nil {
		return err
	}
	if e.Options == nil {
		e.Options = []string{}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encoding decision entry for %s: %w", path, err)
	}
	line := append(b, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating decision log folder for %s: %w", path, err)
	}
	// The lock file is left in place for the same reason tokens.Append gives:
	// removing it would let a waiting writer lock a deleted inode.
	lp := path + ".lock"
	lock, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening decision log lock %s: %w", lp, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lp, err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening decision log %s: %w", path, err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("writing decision log %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing decision log %s: %w", path, err)
	}
	return nil
}

// Read returns every entry in the order written. A missing file is an empty
// log, not an error. A line that does not parse, including a blank line in the
// middle of the file, is an error that names the line, and nothing after it is
// returned: a reader must not act on a log it cannot read whole.
func Read(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading decision log %s: %w", path, err)
	}
	text := string(data)
	// A file written by Append ends in a newline, which leaves one empty
	// element after the split. Anything else empty is a damaged line.
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, nil
	}
	var out []Entry
	for i, raw := range strings.Split(text, "\n") {
		var e Entry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, fmt.Errorf("decision log %s line %d is damaged: %w", path, i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}
