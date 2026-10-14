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
)

// This file is the conversation side of the provider seam (docs/PLAN.md
// T8.3): the planning session's conversation as provider-neutral entries, for
// the web page's chat view (T8.12), its reply box (T8.4) and the "waiting for
// you" notice (T8.13). Turning entries into a chat, and deciding whether the
// session is waiting, is internal/planchat's job; this file only finds and
// reads the transcript.
//
// Like tokens.go, everything about the Claude Code transcript below was
// observed on one machine (2026-10-14) and is not a documented contract, so
// the reader is tolerant: a line it cannot parse is skipped, a field it does
// not find is empty, and every rule about the format stays in this package.

// EntryKind says what one conversation entry is.
type EntryKind string

// The entry kinds. A provider maps whatever its transcript holds onto these,
// and anything that fits none of them is not returned at all.
const (
	// EntryText is a message: the agent's reply text, or text the owner
	// typed.
	EntryText EntryKind = "text"
	// EntryQuestion is the agent asking the owner to choose; ID is the
	// question's id and Questions holds what was asked.
	EntryQuestion EntryKind = "question"
	// EntryAnswer is the owner's choice for the question with the same ID.
	EntryAnswer EntryKind = "answer"
	// EntryTool is any other tool call; Text is the tool's name.
	EntryTool EntryKind = "tool"
	// EntryTurnEnd is the agent finishing its turn and handing back to the
	// owner.
	EntryTurnEnd EntryKind = "turn_end"
	// EntryInterrupted is the owner stopping the agent mid-turn.
	EntryInterrupted EntryKind = "interrupted"
	// EntryBackgroundStarted is the agent starting work that runs on after
	// its turn (a background agent or command); ID names it.
	EntryBackgroundStarted EntryKind = "background_started"
	// EntryBackgroundDone is that work finishing; ID matches the start.
	EntryBackgroundDone EntryKind = "background_done"
)

// Who said an entry.
const (
	WhoAgent = "agent"
	WhoOwner = "owner"
)

// ConversationQuery names the planning session whose conversation is wanted.
type ConversationQuery struct {
	// Home is the user's home directory. It is a parameter, not read from
	// the environment, so tests never touch the real one.
	Home string
	// Root is the project's main checkout, where the planning host runs.
	Root string
	// StartedAt is when the planning host started (host.json started_at).
	// A transcript begun well before it belongs to an earlier planning
	// session and is never chosen.
	StartedAt time.Time
}

// ConversationEntry is one thing that happened in the conversation, in order.
type ConversationEntry struct {
	Kind EntryKind
	// Who is WhoAgent or WhoOwner; empty for kinds nobody "said".
	Who string
	// Text is the message text, or the tool name for EntryTool.
	Text string
	// At is when it happened, in UTC; zero when the transcript did not say.
	At time.Time
	// ID ties a question to its answer and a background start to its end.
	ID        string
	Questions []Question
	Answers   []Answer
}

// Question is one question of an EntryQuestion. The JSON tags are the web
// page's API names, so the chat can be encoded without a second type.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	MultiSelect bool     `json:"multi_select"`
	Options     []Option `json:"options"`
}

// Option is one choice of a Question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	// Recommended is true for the option the agent suggested.
	Recommended bool `json:"recommended"`
}

// Answer is the owner's choice for one question.
type Answer struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Label    string `json:"label"`
}

func (codex) PlanConversation(ConversationQuery) ([]ConversationEntry, error) {
	return nil, ErrNoPlanSession
}

func (opencode) PlanConversation(ConversationQuery) ([]ConversationEntry, error) {
	return nil, ErrNoPlanSession
}

// startSlack is how much earlier than the host's start a transcript may
// begin and still be this session's. The host records started_at before the
// agent CLI writes its first line, so the slack is only for clock rounding;
// a minute is far more than that and far less than the gap between two
// planning sessions an owner could start.
const startSlack = time.Minute

// PlanConversation finds the planning session's transcript and returns its
// entries.
//
// No conversation id is recorded for a planning session (it has host.json
// but no status.json), so the transcript is found by where and when it was
// started instead: Claude Code keeps a working directory's transcripts in one
// folder, and the planning session is the one there that began at the host's
// start, in the main checkout, with the planning command. Of several such
// files the one that began last is taken, because a /clear inside the session
// starts a new file in the same folder.
func (c claude) PlanConversation(q ConversationQuery) ([]ConversationEntry, error) {
	if q.Home == "" {
		return nil, errors.New("conversation query has no home directory")
	}
	if q.Root == "" {
		return nil, errors.New("conversation query has no project folder")
	}
	argv, err := c.PlanArgv()
	if err != nil {
		return nil, err
	}
	// The command is taken from the argv the session was started with, so
	// the text that marks a planning transcript can never drift from it.
	command := argv[len(argv)-1]
	root := filepath.Clean(q.Root)
	dir := filepath.Join(q.Home, ".claude", "projects", projectFolder(root))
	path, err := findPlanTranscript(dir, root, command, q.StartedAt.Add(-startSlack))
	if err != nil {
		return nil, err
	}
	return readConversation(path, command)
}

// projectFolder is the name Claude Code gives a working directory's
// transcript folder: the path with every character outside [A-Za-z0-9]
// replaced by "-" (so "/home/me/code/my.app" is "-home-me-code-my-app").
// It was observed only for ASCII paths; a non-ASCII rune is taken as one "-".
func projectFolder(root string) string {
	var b strings.Builder
	for _, r := range root {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// findPlanTranscript returns the planning transcript in dir that began last,
// at or after since. Only the folder's own *.jsonl files are looked at: a
// sub-agent's transcript sits in a {id}/subagents/ folder below and starts
// with the parent's prompt, so descending would find impostors.
func findPlanTranscript(dir, root, command string, since time.Time) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s does not exist", ErrNoTranscript, dir)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", dir, err)
	}
	var best string
	var bestAt time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		// The mtime check is a cheap first cut: a file last written before
		// the host started cannot have been written by it, and an old
		// project folder can hold hundreds of transcripts.
		info, err := e.Info()
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		at, ok := planTranscriptStart(p, root, command)
		if !ok || at.Before(since) {
			continue
		}
		// Strictly after: on a tie the first file in name order stays, so
		// two reads choose the same file.
		if best == "" || at.After(bestAt) {
			best, bestAt = p, at
		}
	}
	if best == "" {
		return "", fmt.Errorf("%w: no planning conversation for %s under %s", ErrNoTranscript, root, dir)
	}
	return best, nil
}

// lineHead is the part of a transcript line that says what the line is.
type lineHead struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	// IsCompactSummary and IsVisibleInTranscriptOnly mark the summary
	// Claude Code writes as a user line after compacting: the owner did not
	// type it.
	IsCompactSummary          bool `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool `json:"isVisibleInTranscriptOnly"`
	Origin                    struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// ToolUseResult is raw because its shape depends on the tool, and on
	// a failed tool it is a plain string.
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// planTranscriptStart reports when the transcript at path began, and whether
// it is a planning session in root: its first message line ran in root and
// its first non-meta owner line is the planning command. It reads only up to
// that owner line, so even a huge transcript costs a few lines. A file that
// cannot be read is not a candidate rather than an error, so one unreadable
// file cannot hide the planning session beside it.
func planTranscriptStart(path, root, command string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var start time.Time
	seen := false
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			var h lineHead
			if json.Unmarshal(raw, &h) == nil && !h.IsSidechain && (h.Type == "user" || h.Type == "assistant") {
				if !seen {
					t, perr := time.Parse(time.RFC3339Nano, h.Timestamp)
					if perr != nil || filepath.Clean(h.Cwd) != root {
						return time.Time{}, false
					}
					start, seen = t.UTC(), true
				}
				// The agent speaking before any owner line means the
				// session was not started with a command at all.
				if h.Type == "assistant" {
					return time.Time{}, false
				}
				if !h.IsMeta {
					for _, s := range contentTexts(h.Message.Content) {
						if strings.Contains(s, command) {
							return start, true
						}
					}
					return time.Time{}, false
				}
			}
		}
		if err != nil {
			return time.Time{}, false
		}
	}
}

// convBlock is one block of a message's content, for every block type
// this reader looks at. (contentBlock in tokens.go holds only what the token
// reader needs and is left as it is.)
type convBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// contentTexts returns the text of a content value: the string itself, or
// the text of each text block in a list. Anything else has no text.
func contentTexts(content json.RawMessage) []string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []string{s}
	}
	var blocks []convBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			out = append(out, b.Text)
		}
	}
	return out
}

// readConversation reads the transcript at path into entries.
//
// Lines are read with ReadBytes and not a bufio.Scanner for the reason
// tokens.go gives: one line holds a whole tool result and can be far over a
// Scanner's limit, which would end the read early without an error worth
// showing.
func readConversation(path, command string) ([]ConversationEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading conversation: %w", err)
	}
	defer f.Close()
	c := &conversation{command: command, questions: map[string][]Question{}}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			// A half-written last line, normal while the agent is
			// writing, fails to parse and is skipped like any bad line.
			c.addLine(raw)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading conversation %s: %w", path, err)
		}
	}
	c.flushEnd()
	return c.entries, nil
}

// conversation folds transcript lines into entries.
type conversation struct {
	command string
	entries []ConversationEntry
	// questions maps an asked question's id to what was asked, so its
	// answer can be told from any other tool result and matched to headers.
	questions map[string][]Question
	// pendingEnd is a turn end seen as stop_hook_summary and not yet
	// emitted: a turn_duration usually follows it and is the one kept, so
	// a turn yields one EntryTurnEnd, not two.
	pendingEnd *ConversationEntry
	// textMsgID is the message id of the last entry when it is agent text,
	// so the next text block of the same message joins it.
	textMsgID string
}

// emit appends an entry, first flushing a turn end it follows.
func (c *conversation) emit(e ConversationEntry, msgID string) {
	if e.Kind != EntryTurnEnd {
		c.flushEnd()
	}
	if e.Kind == EntryTurnEnd && len(c.entries) > 0 && c.entries[len(c.entries)-1].Kind == EntryTurnEnd {
		return
	}
	// One assistant message can be written as several lines, one per
	// block; two text blocks in a row of one message are one message.
	if e.Kind == EntryText && e.Who == WhoAgent && msgID != "" && msgID == c.textMsgID {
		last := &c.entries[len(c.entries)-1]
		last.Text += "\n\n" + e.Text
		return
	}
	c.textMsgID = ""
	if e.Kind == EntryText && e.Who == WhoAgent {
		c.textMsgID = msgID
	}
	c.entries = append(c.entries, e)
}

func (c *conversation) flushEnd() {
	if c.pendingEnd == nil {
		return
	}
	e := *c.pendingEnd
	c.pendingEnd = nil
	c.emit(e, "")
}

func (c *conversation) addLine(raw []byte) {
	var h lineHead
	if json.Unmarshal(raw, &h) != nil || h.IsSidechain {
		return
	}
	var at time.Time
	if t, err := time.Parse(time.RFC3339Nano, h.Timestamp); err == nil {
		at = t.UTC()
	}
	switch h.Type {
	case "system":
		switch h.Subtype {
		case "turn_duration":
			c.pendingEnd = nil
			c.emit(ConversationEntry{Kind: EntryTurnEnd, At: at}, "")
		case "stop_hook_summary":
			if c.pendingEnd == nil {
				c.pendingEnd = &ConversationEntry{Kind: EntryTurnEnd, At: at}
			}
		}
	case "assistant":
		c.addAssistant(h, at)
	case "user":
		c.addUser(h, at)
	}
}

func (c *conversation) addAssistant(h lineHead, at time.Time) {
	var s string
	if json.Unmarshal(h.Message.Content, &s) == nil {
		if strings.TrimSpace(s) != "" {
			c.emit(ConversationEntry{Kind: EntryText, Who: WhoAgent, Text: strings.TrimSpace(s), At: at}, h.Message.ID)
		}
		return
	}
	var blocks []convBlock
	if json.Unmarshal(h.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				c.emit(ConversationEntry{Kind: EntryText, Who: WhoAgent, Text: t, At: at}, h.Message.ID)
			}
		case "tool_use":
			if b.Name == "AskUserQuestion" {
				// A question that does not parse cannot be answered
				// by choosing, so it is shown as a plain tool call
				// rather than as a choice with nothing to choose.
				if qs := parseQuestions(b.Input); len(qs) > 0 {
					c.questions[b.ID] = qs
					c.emit(ConversationEntry{Kind: EntryQuestion, Who: WhoAgent, At: at, ID: b.ID, Questions: qs}, "")
					continue
				}
			}
			c.emit(ConversationEntry{Kind: EntryTool, Who: WhoAgent, Text: b.Name, At: at, ID: b.ID}, "")
		}
		// thinking blocks and anything else are not part of the chat.
	}
}

// parseQuestions reads an AskUserQuestion input. The recommended option is
// marked only by its label (for example "A web page (recommended)"), so that
// is what Recommended is read from.
func parseQuestions(input json.RawMessage) []Question {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []Question
	for _, q := range in.Questions {
		nq := Question{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect, Options: []Option{}}
		for _, o := range q.Options {
			nq.Options = append(nq.Options, Option{
				Label:       o.Label,
				Description: o.Description,
				Recommended: strings.Contains(strings.ToLower(o.Label), "(recommended)"),
			})
		}
		out = append(out, nq)
	}
	return out
}

// toolUseResult is the part of a user line's toolUseResult this reader uses.
type toolUseResult struct {
	IsAsync          bool                       `json:"isAsync"`
	Status           string                     `json:"status"`
	AgentID          string                     `json:"agentId"`
	BackgroundTaskID string                     `json:"backgroundTaskId"`
	Answers          map[string]json.RawMessage `json:"answers"`
}

// taskID finds the id in a background task's completion notice.
var taskID = regexp.MustCompile(`<task-id>([^<]*)</task-id>`)

// wrappers start user-line text Claude Code writes itself rather than the
// owner typing it.
var wrappers = []string{"<task-notification>", "<system-reminder>", "<local-command-", "<command-"}

func (c *conversation) addUser(h lineHead, at time.Time) {
	if h.IsMeta || h.IsCompactSummary || h.IsVisibleInTranscriptOnly {
		return
	}
	// A background task's completion arrives as a user line. Its origin
	// marks it; the content check also catches one written without it.
	if h.Origin.Kind == "task-notification" || hasNotification(h.Message.Content) {
		for _, s := range contentTexts(h.Message.Content) {
			if m := taskID.FindStringSubmatch(s); m != nil {
				c.emit(ConversationEntry{Kind: EntryBackgroundDone, At: at, ID: strings.TrimSpace(m[1])}, "")
			}
		}
		return
	}
	var s string
	if json.Unmarshal(h.Message.Content, &s) == nil {
		c.ownerText(s, at)
		return
	}
	var blocks []convBlock
	if json.Unmarshal(h.Message.Content, &blocks) != nil {
		return
	}
	var res toolUseResult
	_ = json.Unmarshal(h.ToolUseResult, &res) // a string or other shape leaves it empty
	for _, b := range blocks {
		switch b.Type {
		case "text":
			c.ownerText(b.Text, at)
		case "tool_result":
			c.toolResult(b, res, at)
		}
	}
}

func hasNotification(content json.RawMessage) bool {
	for _, s := range contentTexts(content) {
		if strings.HasPrefix(strings.TrimSpace(s), "<task-notification>") {
			return true
		}
	}
	return false
}

// ownerText adds text from a user line, unless Claude Code wrote it.
func (c *conversation) ownerText(s string, at time.Time) {
	t := strings.TrimSpace(s)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "[Request interrupted by user") {
		c.emit(ConversationEntry{Kind: EntryInterrupted, Who: WhoOwner, At: at}, "")
		return
	}
	for _, w := range wrappers {
		if strings.HasPrefix(t, w) {
			return
		}
	}
	// The planning command as plain text, should a version write it
	// without the <command-…> wrapper: the owner did not type it.
	if strings.HasPrefix(t, c.command) {
		return
	}
	c.emit(ConversationEntry{Kind: EntryText, Who: WhoOwner, Text: t, At: at}, "")
}

func (c *conversation) toolResult(b convBlock, res toolUseResult, at time.Time) {
	if qs, ok := c.questions[b.ToolUseID]; ok {
		c.emit(ConversationEntry{Kind: EntryAnswer, Who: WhoOwner, At: at, ID: b.ToolUseID, Answers: answersFor(qs, res.Answers, b.Content)}, "")
		return
	}
	switch {
	case (res.IsAsync || res.Status == "async_launched") && res.AgentID != "":
		c.emit(ConversationEntry{Kind: EntryBackgroundStarted, At: at, ID: res.AgentID}, "")
	case res.BackgroundTaskID != "":
		c.emit(ConversationEntry{Kind: EntryBackgroundStarted, At: at, ID: res.BackgroundTaskID}, "")
	}
}

// answersFor pairs the chosen labels with the questions asked. The answers
// object is keyed by question text; a key that matches no question (the text
// was changed somehow) is still kept, without a header. With no answers
// object at all (a declined or failed question) the tool result's own text
// stands as the one answer, so the question still reads as answered.
func answersFor(qs []Question, answers map[string]json.RawMessage, content json.RawMessage) []Answer {
	var out []Answer
	used := map[string]bool{}
	for _, q := range qs {
		if raw, ok := answers[q.Question]; ok {
			out = append(out, Answer{Question: q.Question, Header: q.Header, Label: answerLabel(raw)})
			used[q.Question] = true
		}
	}
	var rest []string
	for k := range answers {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, Answer{Question: k, Label: answerLabel(answers[k])})
	}
	if len(out) > 0 {
		return out
	}
	text := strings.TrimSpace(strings.Join(contentTexts(content), "\n"))
	a := Answer{Label: text}
	if len(qs) == 1 {
		a.Question, a.Header = qs[0].Question, qs[0].Header
	}
	return []Answer{a}
}

// answerLabel reads one answer value: a label, or a list of labels for a
// multi-select question, joined with ", ".
func answerLabel(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, ", ")
	}
	return strings.TrimSpace(string(raw))
}
