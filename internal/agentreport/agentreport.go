// Package agentreport is the one rule for reading an agent's own Program
// status report: the `agent_status` record afk's host republishes in
// host.json (docs/PLAN.md §4.1). It decides whether a report says the agent
// is waiting on the owner, whether the report is fresh enough to believe,
// and what text of it is safe to quote.
//
// The daemon's stall rule and the planning chat both import it, so the two
// can never disagree about what a report means (docs/PLAN.md §8 T17.12).
// It is pure: it reads no file and parses no host.json. Callers parse the
// record and pass the fields in. A report never writes status.json; the
// hooks stay the authority for awaiting_input.
package agentreport

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxQuote is the longest text, in runes and counting the trailing ellipsis,
// that Clean returns. A stall item quotes the agent's own words inside a
// longer sentence that also goes into a push message, so the quote is
// bounded rather than carried whole.
const MaxQuote = 200

// Report is the host.json `agent_status` record, already parsed. There is
// no Present flag: the host omits the record when it has none, so a zero At
// stands for "no report" and Fresh treats it as never fresh.
type Report struct {
	State string
	Kind  string
	Msg   string
	At    time.Time
}

// Blocked reports whether r says the agent is waiting on someone.
//
// Every `blocked` record counts, whatever its kind. docs/PLAN.md §8 T17.1
// measured Claude Code sending `blocked` with kind=permission at a
// permission prompt and kind=question at a question, and kind=auth is the
// same kind of wait: the agent cannot go on until a person acts, which is
// what the stall rule means by waiting. No other state counts. In
// particular `working` does not, even with a msg: T17.1 saw a `working`
// record carrying the question's text 29 ms before the question's `blocked`
// arrived, so "working with a msg" is a turn still running. `done` stays set
// while the agent idles, so it says nothing about a wait either.
func Blocked(r Report) bool {
	return r.State == "blocked"
}

// Fresh reports whether r was made by the agent that is running now, and not
// left behind by an earlier one.
//
// A report counts only when its time is strictly after both the host's start
// and the last exit (host.json `last_exit.at`). The host clears
// `agent_status` on a restart, but a record can still outlive its agent: under Rex an agent that crashes under a living host
// leaves its last record behind (docs/PLAN.md §8 T17.1), and a restart in
// place records `last_exit` before the new agent has reported anything
// (docs/PLAN.md §4.1). A record no newer than either bound may belong to the
// agent that is gone.
//
// Equality is stale. host.json's times are RFC 3339 with one-second
// resolution, so a report stamped in the same second as an exit cannot be
// told apart from one sent just before it; trusting it would let a dead
// agent's `blocked` page the owner. Missing it costs at most one second of a
// wait that runs for minutes before it matters.
//
// A zero bound is ignored, because a host with no recorded start or no
// restart has nothing to compare against. A zero At is never fresh, because
// it is the absence of a report.
func Fresh(r Report, hostStarted, lastExit time.Time) bool {
	if r.At.IsZero() {
		return false
	}
	if !hostStarted.IsZero() && !r.At.After(hostStarted) {
		return false
	}
	if !lastExit.IsZero() && !r.At.After(lastExit) {
		return false
	}
	return true
}

// Clean returns the text of an agent's msg that is safe to quote in a stall
// item: every run of white space becomes one space, other control characters
// and bytes that are not valid UTF-8 are dropped, the ends are trimmed, and
// the result is cut to MaxQuote runes with a trailing "…" when cut.
//
// The msg is the agent's own text, passed through the host untouched, so it
// may hold newlines, escape sequences or a whole question with its options.
// The stall item and the push message built from it are one-line, plain
// text, so anything that could move a cursor or break a line goes. White
// space is turned into a space before control characters are dropped,
// because a tab or newline between two words is a word break, and dropping
// it would join the words. An empty or all-white-space msg gives "", so the
// caller can tell "nothing to quote" from a quote.
func Clean(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	space := false
	for i := 0; i < len(msg); {
		r, size := utf8.DecodeRuneInString(msg[i:])
		i += size
		switch {
		case r == utf8.RuneError && size <= 1:
			// Invalid UTF-8: dropped, so the quote is always valid text.
			continue
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) <= MaxQuote {
		return out
	}
	runes := []rune(out)[:MaxQuote-1]
	return strings.TrimRight(string(runes), " ") + "…"
}
