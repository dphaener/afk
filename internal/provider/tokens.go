package provider

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// This file is the token side of the provider seam (docs/PLAN.md §4.7): what
// a session and its sub-agents spent, one record per model call. It lives in
// this package because where an agent CLI keeps its transcripts, and what a
// line of one looks like, is as provider-specific as its argv.
//
// Everything below about the Claude Code transcript layout and line shape is
// what was observed on one machine in Claude Code transcripts of 2026-10-11.
// It is not a documented contract, so the reader is tolerant by design: a
// line it cannot parse is skipped, never an error, and a field it does not
// find counts as zero.

// The five phases a token record can belong to. They are the `phase` values
// of the token ledger (docs/PLAN.md §4.7), not the status.json phases: `ship`
// has no status.json phase of its own, see phaseFor.
const (
	PhasePlan    = "plan"
	PhaseExecute = "execute"
	PhaseReview  = "review"
	PhaseFix     = "fix"
	PhaseShip    = "ship"
)

// The roles a token record can have. They follow the labels the workflow
// scripts and prompts give their agents, so a row in the ledger can be traced
// back to the prompt that spent it.
const (
	// RoleConductor is the main session outside planning.
	RoleConductor = "conductor"
	// RolePlanner is the main session during planning.
	RolePlanner = "planner"
	// RoleResearch is a codebase or web research sub-agent.
	RoleResearch = "research"
	// RoleCrucible is the plan's adversarial pre-review.
	RoleCrucible = "crucible"
	// RoleTask is a sub-agent executing one work-plan task.
	RoleTask = "task"
	// RoleFix is a sub-agent fixing a failed task or a review finding.
	RoleFix = "fix"
	// RoleVerifier is the independent check of one finished task.
	RoleVerifier = "verifier"
	// RoleReviewCompliance, RoleReviewQuality and RoleReviewQA are the three
	// review checks.
	RoleReviewCompliance = "review:compliance"
	RoleReviewQuality    = "review:quality"
	RoleReviewQA         = "review:qa"
	// RoleCritic is the adversarial critic of a review check.
	RoleCritic = "critic"
	// RoleUnknown is a sub-agent whose label could not be read at all.
	RoleUnknown = "unknown"
	// RoleOther is a sub-agent whose label was read and matched no rule. It
	// is kept apart from RoleUnknown because it means a rule is missing
	// here, where RoleUnknown means the metadata is.
	RoleOther = "other"
)

// ErrTokensUnsupported is returned by a provider that has no way to read a
// session's token use yet. It is a sentinel so a caller can say exactly that,
// instead of printing an empty table that reads as "used nothing".
var ErrTokensUnsupported = errors.New("token use cannot be read for this provider")

// ErrNoTranscript is returned when a provider can read token use but finds no
// transcript for the session id asked for: the session never started, its
// transcripts were deleted, or it ran under another home directory.
var ErrNoTranscript = errors.New("no transcript found for this session")

// TokenRecord is one model call: one message id, counted once.
type TokenRecord struct {
	// At is the earliest timestamp seen for the call. It is the zero time
	// when no line of the call carried a readable timestamp.
	At time.Time
	// MessageID is the provider's id for the call.
	MessageID string
	// Model is the model name as the transcript records it.
	Model string
	// Role is one of the Role constants.
	Role string
	// Phase is one of the Phase constants.
	Phase string
	// AgentID is the sub-agent's id, empty for the main session.
	AgentID string
	// Input, Output, CacheWrite and CacheRead are the raw usage fields. They
	// are kept raw, not weighted, so a cost proxy can be recomputed later.
	Input, Output, CacheWrite, CacheRead int64
}

// TokenQuery names the session whose token use is wanted.
type TokenQuery struct {
	// Home is the user's home directory. It is a parameter, not read from
	// the environment here, so tests never touch the real one.
	Home string
	// SessionID is the agent CLI's own session id (status.json session_id),
	// not the afk session name.
	SessionID string
}

func (codex) SessionTokens(TokenQuery) ([]TokenRecord, error) {
	return nil, ErrTokensUnsupported
}

func (opencode) SessionTokens(TokenQuery) ([]TokenRecord, error) {
	return nil, ErrTokensUnsupported
}

// SessionTokens reads the session's main transcript and every sub-agent and
// workflow-agent transcript beside it, and returns one record per message id.
//
// The main transcript is found by looking for {SessionID}.jsonl in every
// folder under {Home}/.claude/projects, not by computing the folder name from
// the session's working directory. The folder names seen on this machine are
// the working directory with `/` and `.` turned into `-`, but that rule was
// only seen for a handful of paths; the session id is unique on its own, so
// searching for it needs no rule at all.
//
// Records come back in the order their ids were first seen: the main file
// first, then the sub-agent files in sorted order, each in line order.
func (claude) SessionTokens(q TokenQuery) ([]TokenRecord, error) {
	if q.Home == "" {
		return nil, errors.New("token query has no home directory")
	}
	// The id becomes a file name, so anything that is not a plain file name
	// is refused here instead of being joined into a path. An empty id
	// would otherwise match a file called ".jsonl".
	if q.SessionID == "" {
		return nil, errors.New("token query has no session id")
	}
	if q.SessionID == "." || q.SessionID == ".." || strings.ContainsAny(q.SessionID, `/\`) {
		return nil, fmt.Errorf("token query session id %q is not a plain id", q.SessionID)
	}

	projects := filepath.Join(q.Home, ".claude", "projects")
	mainPath, err := findMainTranscript(projects, q.SessionID)
	if err != nil {
		return nil, err
	}
	side := filepath.Join(strings.TrimSuffix(mainPath, ".jsonl"), "subagents")
	agentPaths, err := agentTranscripts(side)
	if err != nil {
		return nil, err
	}

	c := newTokenCounter()
	if err := c.readFile(mainPath, true, "", RoleConductor); err != nil {
		return nil, err
	}
	for _, p := range agentPaths {
		description, hasMeta := readAgentDescription(strings.TrimSuffix(p, ".jsonl") + ".meta.json")
		agentID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "agent-"), ".jsonl")
		if err := c.readFile(p, false, agentID, roleFor(false, hasMeta, description)); err != nil {
			return nil, err
		}
	}
	return c.finish(), nil
}

// findMainTranscript returns the first {sessionID}.jsonl under any folder of
// projects, in sorted folder order. It walks the folder instead of calling
// filepath.Glob so that a home directory whose own path holds a `[` or `*` is
// not read as part of a pattern.
func findMainTranscript(projects, sessionID string) (string, error) {
	entries, err := os.ReadDir(projects)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s does not exist", ErrNoTranscript, projects)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", projects, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(projects, e.Name(), sessionID+".jsonl")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: no %s.jsonl under %s", ErrNoTranscript, sessionID, projects)
}

// agentTranscripts lists the sub-agent transcripts below a session's
// subagents folder: agent-*.jsonl directly in it (agents started with the
// Agent tool), then agent-*.jsonl in each folder of workflows/ (agents started
// by a workflow run). Each group is sorted, so two reads of the same session
// attribute a message id shared between files to the same one.
//
// Only agent-*.jsonl is matched on purpose: a workflow folder also holds a
// journal.jsonl, and workflows/ holds a wf_*.json summary per run, and neither
// is a transcript. A session with no sub-agents has no folder, which is not
// an error.
func agentTranscripts(side string) ([]string, error) {
	paths, err := agentFilesIn(side)
	if err != nil {
		return nil, err
	}
	workflows := filepath.Join(side, "workflows")
	runs, err := os.ReadDir(workflows)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", workflows, err)
	}
	for _, run := range runs { // os.ReadDir sorts by name
		if !run.IsDir() {
			continue
		}
		more, err := agentFilesIn(filepath.Join(workflows, run.Name()))
		if err != nil {
			return nil, err
		}
		paths = append(paths, more...)
	}
	return paths, nil
}

// agentFilesIn returns the agent-*.jsonl files directly in dir, sorted. A
// missing dir is an empty list.
func agentFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)
	return paths, nil
}

// readAgentDescription returns the description in a sub-agent's meta file.
// ok is false when the file is missing or is not a JSON object with a string
// description, which roleFor turns into RoleUnknown.
func readAgentDescription(path string) (description string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var meta struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", false
	}
	return meta.Description, true
}

// taskLabel matches a description that starts with a work-plan task id, such
// as "W1.T2 Add the provider token method". The trailing \b keeps "W1.T2x"
// out.
var taskLabel = regexp.MustCompile(`^W\d+\.T\d+\b`)

// roleFor maps a transcript to a role from where it is and what its meta file
// calls it. It is pure so every label is a table row.
//
// The main session is always RoleConductor here; whether a given call of it
// was planning is a question of when, not of which file, so SessionTokens
// turns it into RolePlanner once the call's phase is known.
//
// The prefixes are the labels the afk workflow scripts and prompts give the
// agents they start. Matching is case-sensitive and by prefix because those
// labels are written by afk, not typed by a person. A description that matches
// nothing is RoleOther, including a `review:` label for a check this function
// does not know: inventing a role from whatever follows the colon would let
// the ledger grow roles nobody defined.
func roleFor(isMain bool, hasMeta bool, description string) string {
	if isMain {
		return RoleConductor
	}
	if !hasMeta {
		return RoleUnknown
	}
	d := strings.TrimSpace(description)
	has := func(prefix string) bool { return strings.HasPrefix(d, prefix) }
	switch {
	case has("Codebase research for"), has("Web research for"):
		return RoleResearch
	case has("Crucible"):
		return RoleCrucible
	case has("verify:"):
		return RoleVerifier
	case has("fix:"), has("Fix discrepancy in"):
		return RoleFix
	case has("review:"):
		check := strings.TrimPrefix(d, "review:")
		if i := strings.IndexFunc(check, unicode.IsSpace); i >= 0 {
			check = check[:i]
		}
		switch check {
		case "compliance":
			return RoleReviewCompliance
		case "quality":
			return RoleReviewQuality
		case "qa":
			return RoleReviewQA
		}
		return RoleOther
	case has("critic:"), has("Critic for"):
		return RoleCritic
	case taskLabel.MatchString(d), has("Execute ("):
		return RoleTask
	}
	return RoleOther
}

// The status.json phase words (plugins/afk/skills/conductor/status-schema.md
// §8) and the ledger phase each one counts as. `done` and `failed` are both
// `ship`: whatever the main session does after the terminal write is closing
// the task out.
var statusPhases = map[string]string{
	"planning":  PhasePlan,
	"executing": PhaseExecute,
	"reviewing": PhaseReview,
	"fixing":    PhaseFix,
	"done":      PhaseShip,
	"failed":    PhaseShip,
}

// statusReviewing and statusFixing are the two status words the ship rule
// looks at by name.
const (
	statusReviewing = "reviewing"
	statusFixing    = "fixing"
)

// phaseAssignment finds `.phase = "word"` in a shell command, the way the
// conductor's jq filter sets the phase.
var phaseAssignment = regexp.MustCompile(`\.phase = "(\w+)"`)

// phaseWrite reports the status phase a Bash command writes to status.json,
// if it writes one. The command must name status.json and assign the phase a
// literal word; when it assigns twice, the last one is what the file ends up
// holding. A word that is not one of the six status phases is not a
// transition: it is some other text that happened to look like one.
func phaseWrite(command string) (status string, ok bool) {
	if !strings.Contains(command, "status.json") {
		return "", false
	}
	all := phaseAssignment.FindAllStringSubmatch(command, -1)
	if len(all) == 0 {
		return "", false
	}
	status = all[len(all)-1][1]
	if _, known := statusPhases[status]; !known {
		return "", false
	}
	return status, true
}

// phaseTransition is one status write of the conductor: at this instant the
// session's status.json phase became status.
type phaseTransition struct {
	at     time.Time
	status string
}

// shipCutoff returns the instant after which a main-session call in the last
// reviewing interval counts as shipping, or the zero time when no call does.
//
// It exists because the conductor ships (push, pull request, merge) while
// status.json still says `reviewing`: the phase only becomes `done` at the
// very end. The last thing a review does is run sub-agents, so the last
// sub-agent call of the last review is taken as the end of reviewing, and
// what the main session does after it as shipping. This bills the one or two
// calls that record the verdict to ship, which is accepted.
//
// "The last reviewing interval" is the last run of reviewing writes that no
// fixing write follows; it ends at the next write of another phase, or never
// if there is none. A reviewing interval that a fixing write follows was a
// rejected review, and the main session's calls after its sub-agents were
// handling the rejection. With no sub-agent call in the interval there is
// nothing to mark where reviewing ended, so nothing in it counts as ship.
//
// transitions must be sorted by time. subAgentCalls is the time of every
// sub-agent record, in any order.
func shipCutoff(transitions []phaseTransition, subAgentCalls []time.Time) time.Time {
	start := -1
	for i, tr := range transitions {
		switch {
		case tr.status == statusFixing:
			start = -1
		case tr.status == statusReviewing && (start < 0 || transitions[i-1].status != statusReviewing):
			// A repeated reviewing write (a resumed session writes its
			// phase again) continues the interval instead of restarting it.
			start = i
		}
	}
	if start < 0 {
		return time.Time{}
	}
	from := transitions[start].at
	var until time.Time // zero: the interval is still open
	for _, tr := range transitions[start+1:] {
		if tr.status != statusReviewing {
			until = tr.at
			break
		}
	}
	var cutoff time.Time
	for _, t := range subAgentCalls {
		if t.Before(from) || (!until.IsZero() && !t.Before(until)) {
			continue
		}
		if t.After(cutoff) {
			cutoff = t
		}
	}
	return cutoff
}

// phaseFor returns the ledger phase of a call made at `at`.
//
// The phase comes from the transcript, not from status.json, because
// status.json holds only the current phase and keeps no history. The
// conductor's own writes to it are in the main transcript as Bash tool calls
// with timestamps, which gives the history without adding a field to the
// status schema or a log to both plugins.
//
// The call's status phase is that of the latest transition at or before it,
// so the message that performs a write is billed to the phase it starts.
// Before the first transition the session is planning: that is the phase a
// session is created in, and the conductor has had no reason to write it.
//
// cutoff is shipCutoff's result. Only the main session is moved to ship by
// it: a sub-agent that is still running in the last review is reviewing.
//
// transitions must be sorted by time.
func phaseFor(transitions []phaseTransition, cutoff time.Time, at time.Time, isMain bool) string {
	status := "planning"
	for _, tr := range transitions {
		if tr.at.After(at) {
			break
		}
		if _, known := statusPhases[tr.status]; known {
			status = tr.status
		}
	}
	// cutoff lies inside the last reviewing interval, and every earlier
	// reviewing interval ends before that one starts, so "reviewing and
	// later than cutoff" can only be true inside the last one.
	if isMain && status == statusReviewing && !cutoff.IsZero() && at.After(cutoff) {
		return PhaseShip
	}
	return statusPhases[status]
}

// transcriptLine is the part of a transcript line this reader uses. Content
// is left raw because its shape varies (a string on some lines, a list of
// blocks on others) and only the main file's is ever looked into.
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	AgentID   string `json:"agentId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock is one block of an assistant message. Input is left raw
// because every tool has its own input shape, and decoding another tool's
// `command` as a string would fail the whole line.
type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// tokenCounter folds transcript lines into one record per message id.
type tokenCounter struct {
	records     []TokenRecord
	fromMain    []bool // parallel to records
	index       map[string]int
	transitions []phaseTransition
}

func newTokenCounter() *tokenCounter {
	return &tokenCounter{index: map[string]int{}}
}

// readFile folds one transcript file in. agentID is used when a line carries
// none of its own; role is the role of every record first seen in this file.
//
// Lines are read with ReadBytes, which grows to fit, and not with a
// bufio.Scanner: a Scanner stops with an error at its buffer limit (64 KB by
// default), and one line here holds a whole model reply or tool result, which
// is routinely larger. Hitting the limit would silently drop the rest of the
// file from the count.
func (c *tokenCounter) readFile(path string, isMain bool, agentID, role string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reading transcript: %w", err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			// A last line with no newline is parsed like any other. If it
			// is half-written, as it can be while the session is running,
			// it fails to parse and is skipped like any other bad line.
			c.addLine(line, isMain, agentID, role)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading transcript %s: %w", path, err)
		}
	}
}

// addLine folds one line in. Anything that is not an assistant line with a
// message id is skipped without complaint: the transcript also holds user,
// system and bookkeeping lines, and its shape is not a contract.
func (c *tokenCounter) addLine(raw []byte, isMain bool, agentID, role string) {
	var line transcriptLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return
	}
	if line.Type != "assistant" {
		return
	}
	// A timestamp that does not parse leaves the zero time; the line's
	// tokens were still spent, so it is still counted.
	var at time.Time
	if t, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
		at = t.UTC()
	}
	// Phase writes are read from the main file only: status.json belongs
	// to the conductor, and a sub-agent that happened to run a matching
	// command (a test of this very code, say) did not change the phase.
	if isMain && !at.IsZero() {
		c.addTransitions(line.Message.Content, at)
	}
	id := line.Message.ID
	if id == "" {
		return
	}
	u := line.Message.Usage
	i, seen := c.index[id]
	if !seen {
		if line.AgentID != "" {
			agentID = line.AgentID
		}
		c.index[id] = len(c.records)
		c.records = append(c.records, TokenRecord{
			At: at, MessageID: id, Model: line.Message.Model, Role: role, AgentID: agentID,
			Input: u.Input, Output: u.Output, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead,
		})
		c.fromMain = append(c.fromMain, isMain)
		return
	}
	// The same id again, in this file or another. One model call is written
	// as several lines, one per content block, and in the transcripts read
	// for this work the usage on those lines differed: output_tokens rose
	// from line to line (76 of 143 ids in one session). So the lines are
	// neither summed, which would count the call several times, nor is one
	// of them trusted to be complete: each field takes the largest value
	// seen. Model, agent and role stay those of the first line, so a call
	// that shows up in two files is billed once, to the file read first.
	r := &c.records[i]
	r.Input = max(r.Input, u.Input)
	r.Output = max(r.Output, u.Output)
	r.CacheWrite = max(r.CacheWrite, u.CacheWrite)
	r.CacheRead = max(r.CacheRead, u.CacheRead)
	if !at.IsZero() && (r.At.IsZero() || at.Before(r.At)) {
		r.At = at
	}
}

// addTransitions records every status write among a line's content blocks.
// Content that is not a list of blocks holds no tool calls and is ignored.
func (c *tokenCounter) addTransitions(content json.RawMessage, at time.Time) {
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name != "Bash" {
			continue
		}
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(b.Input, &input); err != nil {
			continue
		}
		if status, ok := phaseWrite(input.Command); ok {
			c.transitions = append(c.transitions, phaseTransition{at: at, status: status})
		}
	}
}

// finish gives every record its phase, and the main session's records their
// role, now that all transitions and all sub-agent calls are known. Both need
// the whole session: a record's phase depends on writes that may come later
// in the file than the record, and the ship rule on the sub-agent files.
func (c *tokenCounter) finish() []TokenRecord {
	// Stable, so two writes with the same timestamp keep their file order.
	sort.SliceStable(c.transitions, func(i, j int) bool {
		return c.transitions[i].at.Before(c.transitions[j].at)
	})
	var subAgentCalls []time.Time
	for i, r := range c.records {
		if !c.fromMain[i] && !r.At.IsZero() {
			subAgentCalls = append(subAgentCalls, r.At)
		}
	}
	cutoff := shipCutoff(c.transitions, subAgentCalls)
	for i := range c.records {
		r := &c.records[i]
		r.Phase = phaseFor(c.transitions, cutoff, r.At, c.fromMain[i])
		if c.fromMain[i] && r.Phase == PhasePlan {
			r.Role = RolePlanner
		}
	}
	return c.records
}
