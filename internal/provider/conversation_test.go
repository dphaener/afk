package provider

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// planRoot is the main checkout every fixture transcript was written in. It
// never exists on disk: the reader only encodes it into a folder name and
// compares it with each line's cwd.
const planRoot = "/work/project"

// planStart is the planning host's start time the fixtures are read against.
var planStart = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

// chatAt is a fixture timestamp on the fixtures' day, "10:00:03.200" style.
func chatAt(t *testing.T, clock string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02T15:04:05.000Z", "2026-10-14T"+clock+"Z")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fixtureHome copies testdata/plan-chat/<name> into a fresh home's
// .claude/projects folder and returns the home. The fixtures are not kept
// under a literal .claude folder because a common global gitignore ignores
// .claude/, which would drop them from a commit without a word (see
// testdata/README.md). Every copied file gets an mtime an hour after
// planStart, so the reader's mtime cut never depends on when the repository
// was checked out.
func fixtureHome(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	dst := filepath.Join(home, ".claude", "projects")
	src := filepath.Join("testdata", "plan-chat", name)
	mtime := planStart.Add(time.Hour)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, mtime, mtime)
	})
	if err != nil {
		t.Fatal(err)
	}
	return home
}

// TestPlanConversation reads every fixture in testdata/plan-chat and pins the
// entries the claude provider makes of it.
//
// Load-bearing rows:
//   - "pending question" and "answered question": the web page's choice
//     buttons and the waiting-for-a-choice state rest on the question id,
//     the options and the recommended flag (marked only in a label, in any
//     case), and on the answer carrying the same id. "answered question" also
//     holds the fallback: a declined question has no answers object and
//     still reads as answered, from the tool result's text.
//   - "plain": two text blocks of one message are one entry, a turn that
//     ends with stop_hook_summary alone still ends, one ending with both
//     lines ends once, and the meta skill body, the planning command and a
//     system reminder are never shown as the owner speaking.
//   - "background": a background agent and a background command each start
//     with their own id, and only the command's finishes, which is what
//     keeps the session "working" after its turn ends.
//   - "truncated": a half-written last line is normal while the agent
//     writes, so the earlier entries come back with no error.
//   - "no transcript", "older", "cwd differs": each must be ErrNoTranscript,
//     never another session's conversation shown as this one's.
//   - "non-planning" and "subagent and sidechain": a newer transcript in the
//     same folder that is not the planning session, or that is a sub-agent's,
//     must never be chosen, and sidechain lines never shown.
func TestPlanConversation(t *testing.T) {
	platform := Question{
		Question: "Where should it run?", Header: "Platform",
		Options: []Option{
			{Label: "A web page (recommended)", Description: "Opens in any browser.", Recommended: true},
			{Label: "A phone app", Description: "Needs an app store."},
		},
	}
	extras := Question{
		Question: "Which extras?", Header: "Extras", MultiSelect: true,
		Options: []Option{
			{Label: "Reminders (Recommended)", Description: "A nudge before a job is due.", Recommended: true},
			{Label: "Sharing", Description: "Lists the whole family can edit."},
		},
	}
	name := Question{
		Question: "What should it be called?", Header: "Name",
		Options: []Option{
			{Label: "Chores", Description: "Short and plain."},
			{Label: "Family list", Description: "Says who it is for."},
		},
	}
	tests := []struct {
		name    string
		fixture string
		want    func(t *testing.T) []ConversationEntry
		wantErr error
	}{
		{
			name: "plain exchange", fixture: "plain",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "Hello. What do you want to build?\n\nTell me in a sentence or two.", At: chatAt(t, "10:00:03.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:04.000")},
					{Kind: EntryText, Who: WhoOwner, Text: "A todo app for my family.", At: chatAt(t, "10:00:30.000")},
					{Kind: EntryText, Who: WhoAgent, Text: "Got it.", At: chatAt(t, "10:00:31.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:32.500")},
				}
			},
		},
		{
			name: "pending question", fixture: "pending-question",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "A few questions first.", At: chatAt(t, "10:00:04.000")},
					{Kind: EntryQuestion, Who: WhoAgent, At: chatAt(t, "10:00:05.000"), ID: "toolu_q1", Questions: []Question{platform, extras}},
				}
			},
		},
		{
			name: "answered question", fixture: "answered-question",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryQuestion, Who: WhoAgent, At: chatAt(t, "10:00:05.000"), ID: "toolu_q1", Questions: []Question{platform, extras}},
					{Kind: EntryAnswer, Who: WhoOwner, At: chatAt(t, "10:00:20.000"), ID: "toolu_q1", Answers: []Answer{
						{Question: "Where should it run?", Header: "Platform", Label: "A web page (recommended)"},
						{Question: "Which extras?", Header: "Extras", Label: "Reminders (Recommended), Sharing"},
					}},
					{Kind: EntryText, Who: WhoAgent, Text: "Good, a web page.", At: chatAt(t, "10:00:21.000")},
					{Kind: EntryQuestion, Who: WhoAgent, At: chatAt(t, "10:00:22.000"), ID: "toolu_q2", Questions: []Question{name}},
					{Kind: EntryAnswer, Who: WhoOwner, At: chatAt(t, "10:00:40.000"), ID: "toolu_q2", Answers: []Answer{
						{Question: "What should it be called?", Header: "Name", Label: "The user doesn't want to proceed with this tool use."},
					}},
					{Kind: EntryInterrupted, Who: WhoOwner, At: chatAt(t, "10:00:40.100")},
				}
			},
		},
		{
			name: "waiting for typed text", fixture: "waiting-text",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "Here is the outline. Does it look right?", At: chatAt(t, "10:00:05.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:06.100")},
				}
			},
		},
		{
			name: "still working", fixture: "working",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "Let me look at the code.", At: chatAt(t, "10:00:05.000")},
					{Kind: EntryTool, Who: WhoAgent, Text: "Bash", At: chatAt(t, "10:00:06.000"), ID: "toolu_b1"},
					{Kind: EntryTool, Who: WhoAgent, Text: "Read", At: chatAt(t, "10:00:09.000"), ID: "toolu_r1"},
				}
			},
		},
		{
			name: "truncated last line", fixture: "truncated",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "First answer.", At: chatAt(t, "10:00:05.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:06.100")},
					{Kind: EntryText, Who: WhoOwner, Text: "Next please.", At: chatAt(t, "10:00:20.000")},
				}
			},
		},
		{
			name: "background agent pending", fixture: "background",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "I will look into two things in the background.", At: chatAt(t, "10:00:04.000")},
					{Kind: EntryTool, Who: WhoAgent, Text: "Agent", At: chatAt(t, "10:00:05.000"), ID: "toolu_a1"},
					{Kind: EntryBackgroundStarted, At: chatAt(t, "10:00:06.000"), ID: "a111"},
					{Kind: EntryTool, Who: WhoAgent, Text: "Bash", At: chatAt(t, "10:00:07.000"), ID: "toolu_b1"},
					{Kind: EntryBackgroundStarted, At: chatAt(t, "10:00:08.000"), ID: "b222"},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:09.100")},
					{Kind: EntryBackgroundDone, At: chatAt(t, "10:00:30.000"), ID: "b222"},
					{Kind: EntryText, Who: WhoAgent, Text: "The tests passed. Still waiting on the research.", At: chatAt(t, "10:00:31.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:32.000")},
				}
			},
		},
		{name: "no transcript", fixture: "no-transcript", wantErr: ErrNoTranscript},
		{name: "older planning transcript ignored", fixture: "older", wantErr: ErrNoTranscript},
		{
			name: "non-planning transcript ignored", fixture: "non-planning",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "Planning here.", At: chatAt(t, "10:00:05.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:06.100")},
				}
			},
		},
		{
			name: "subagent and sidechain ignored", fixture: "subagent-sidechain",
			want: func(t *testing.T) []ConversationEntry {
				return []ConversationEntry{
					{Kind: EntryText, Who: WhoAgent, Text: "Main thread.", At: chatAt(t, "10:00:05.000")},
					{Kind: EntryTurnEnd, At: chatAt(t, "10:00:08.100")},
				}
			},
		},
		{name: "cwd differs ignored", fixture: "cwd-differs", wantErr: ErrNoTranscript},
	}
	p, err := Lookup(ClaudeName)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := fixtureHome(t, tt.fixture)
			got, err := p.PlanConversation(ConversationQuery{Home: home, Root: planRoot, StartedAt: planStart})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("PlanConversation error = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("PlanConversation entries = %+v, want none", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanConversation error = %v", err)
			}
			want := tt.want(t)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("PlanConversation entries:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// TestPlanConversationChoosesByStartAndCommand builds a project folder of
// several transcripts and checks which one is read. Each file's first message
// text names it, and the reader returns that text first.
//
// Load-bearing rows:
//   - "latest planning start wins": a /clear starts a new file, so of two
//     planning transcripts begun after the host the one begun later is the
//     live one, whatever the file names' order.
//   - "newer non-planning file loses": a later chat in the same checkout
//     is never taken for the planning session; the command decides.
//   - "slack before start": the host's started_at may land a moment after
//     the CLI's first line, so a start up to a minute earlier still counts,
//     and "too early" pins where the minute ends.
//   - "stale mtime skipped": a file not written since before the host
//     started is passed over without being read.
//   - "zero start time": a host.json with no started_at still finds the
//     planning session.
func TestPlanConversationChoosesByStartAndCommand(t *testing.T) {
	type file struct {
		name     string
		start    time.Duration // first line, relative to planStart
		mtime    time.Duration // relative to planStart
		planning bool
	}
	tests := []struct {
		name      string
		files     []file
		startedAt time.Time
		want      string // the chosen file's name, or "" for ErrNoTranscript
	}{
		{
			name: "latest planning start wins",
			files: []file{
				{"b-early", 10 * time.Second, time.Hour, true},
				{"a-late", 60 * time.Second, time.Hour, true},
			},
			startedAt: planStart, want: "a-late",
		},
		{
			name: "newer non-planning file loses",
			files: []file{
				{"plan", 10 * time.Second, time.Hour, true},
				{"chat", 5 * time.Minute, time.Hour, false},
			},
			startedAt: planStart, want: "plan",
		},
		{
			name:      "slack before start",
			files:     []file{{"plan", -50 * time.Second, time.Hour, true}},
			startedAt: planStart, want: "plan",
		},
		{
			name:      "too early",
			files:     []file{{"plan", -61 * time.Second, time.Hour, true}},
			startedAt: planStart, want: "",
		},
		{
			name: "stale mtime skipped",
			files: []file{
				{"plan", 10 * time.Second, time.Hour, true},
				{"stale", 30 * time.Second, -2 * time.Minute, true},
			},
			startedAt: planStart, want: "plan",
		},
		{
			name:      "zero start time",
			files:     []file{{"plan", 10 * time.Second, time.Hour, true}},
			startedAt: time.Time{}, want: "plan",
		},
		{
			name:      "only a non-planning file",
			files:     []file{{"chat", 10 * time.Second, time.Hour, false}},
			startedAt: planStart, want: "",
		},
	}
	p, err := Lookup(ClaudeName)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".claude", "projects", "-work-project")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				first := `"Fix the login bug."`
				if f.planning {
					first = `"<command-message>afk:master-plan</command-message>\n<command-name>/afk:master-plan</command-name>"`
				}
				head := `"isSidechain":false,"cwd":"/work/project","sessionId":"` + f.name + `"`
				ts := planStart.Add(f.start).Format("2006-01-02T15:04:05.000Z")
				ts2 := planStart.Add(f.start + time.Second).Format("2006-01-02T15:04:05.000Z")
				body := `{"type":"user",` + head + `,"timestamp":"` + ts + `","message":{"role":"user","content":` + first + `}}` + "\n" +
					`{"type":"assistant",` + head + `,"timestamp":"` + ts2 + `","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"` + f.name + `"}]}}` + "\n"
				path := filepath.Join(dir, f.name+".jsonl")
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				mt := planStart.Add(f.mtime)
				if err := os.Chtimes(path, mt, mt); err != nil {
					t.Fatal(err)
				}
			}
			got, err := p.PlanConversation(ConversationQuery{Home: home, Root: planRoot, StartedAt: tt.startedAt})
			if tt.want == "" {
				if !errors.Is(err, ErrNoTranscript) {
					t.Fatalf("error = %v, want ErrNoTranscript", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(got) == 0 || got[0].Text != tt.want {
				t.Errorf("read %+v, want the file %q", got, tt.want)
			}
		})
	}
}

// TestPlanConversationQuery pins the refusals that keep a bad query from
// becoming a path: an empty home would read the working directory's
// .claude, and an empty root would look in a folder for no project.
func TestPlanConversationQuery(t *testing.T) {
	tests := []struct {
		name string
		q    ConversationQuery
		want string
	}{
		{"no home", ConversationQuery{Root: planRoot, StartedAt: planStart}, "no home directory"},
		{"no root", ConversationQuery{Home: t.TempDir(), StartedAt: planStart}, "no project folder"},
	}
	p, err := Lookup(ClaudeName)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.PlanConversation(tt.q)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one saying %q", err, tt.want)
			}
		})
	}
}

// TestProjectFolder pins the folder-name rule. Load-bearing rows: "dots"
// and "worktree" are the shapes seen on a real machine (a "." and a "/"
// each become "-", so ".claude" gives "--claude"); "trailing slash" holds
// that the caller's Root is cleaned before it is encoded.
func TestProjectFolder(t *testing.T) {
	tests := []struct {
		name, root, want string
	}{
		{"plain", "/home/me/code/afk", "-home-me-code-afk"},
		{"dots", "/home/me/code/my.app", "-home-me-code-my-app"},
		{"worktree", "/home/me/code/afk/.claude/worktrees/t8-3", "-home-me-code-afk--claude-worktrees-t8-3"},
		{"underscore and space", "/srv/my_app v2", "-srv-my-app-v2"},
		{"trailing slash", filepath.Clean("/work/project/"), "-work-project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectFolder(tt.root); got != tt.want {
				t.Errorf("projectFolder(%q) = %q, want %q", tt.root, got, tt.want)
			}
		})
	}
}
