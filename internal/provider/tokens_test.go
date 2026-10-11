package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// tokenTestSession is the session id every hand-made transcript below uses.
const tokenTestSession = "11111111-2222-3333-4444-555555555555"

// tokenTestBase is minute zero of every hand-made transcript.
var tokenTestBase = time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

// at returns the instant `min` minutes after tokenTestBase.
func at(min int) time.Time { return tokenTestBase.Add(time.Duration(min) * time.Minute) }

// stamp renders at(min) the way the transcripts do: RFC 3339, UTC, with
// milliseconds.
func stamp(min int) string { return at(min).Format("2006-01-02T15:04:05.000Z") }

// usage is the four counted fields of one transcript line.
type usage struct{ in, out, cw, cr int64 }

// asstLine is one assistant transcript line with no content blocks. The usage
// object carries the nested objects and extra counters a real line has, so
// every row also pins that unknown keys are ignored.
func asstLine(id string, min int, model string, u usage) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"isSidechain":false,"sessionId":%q,"uuid":"u-%s","message":{"id":%q,"model":%q,"role":"assistant","usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":%d},"iterations":[{"input_tokens":%d,"type":"message"}]},"content":[]}}`,
		stamp(min), tokenTestSession, id, id, model, u.in, u.out, u.cw, u.cr, u.cw, u.in)
}

// agentLine is asstLine with an agentId, as a sub-agent transcript has.
func agentLine(agent, id string, min int, model string, u usage) string {
	return strings.Replace(asstLine(id, min, model, u), `"isSidechain":false`, `"isSidechain":true,"agentId":"`+agent+`"`, 1)
}

// phaseLine is an assistant line whose one content block is the conductor's
// status write for phase, in the shape the trimmed fixture keeps.
func phaseLine(id string, min int, phase string, u usage) string {
	cmd := `jq '.phase = \"` + phase + `\"' .afk/demo/status.json > tmp && mv tmp .afk/demo/status.json`
	block := `[{"type":"text","text":"moving on"},{"type":"tool_use","name":"Read","input":{"command":["not","a","string"]}},{"type":"tool_use","name":"Bash","input":{"command":"` + cmd + `"}}]`
	return strings.Replace(asstLine(id, min, "opus", u), `"content":[]`, `"content":`+block, 1)
}

// writeTranscript writes lines to rel below the session's project folder in
// home, creating the folders on the way.
func writeTranscript(t *testing.T, home, rel string, lines ...string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "projects", "-some-project", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Paths of the three kinds of transcript, relative to the project folder.
func mainRel() string { return tokenTestSession + ".jsonl" }
func subRel(agent string) string {
	return filepath.Join(tokenTestSession, "subagents", "agent-"+agent+".jsonl")
}
func subMetaRel(agent string) string {
	return filepath.Join(tokenTestSession, "subagents", "agent-"+agent+".meta.json")
}
func wfRel(run, agent string) string {
	return filepath.Join(tokenTestSession, "subagents", "workflows", run, "agent-"+agent+".jsonl")
}
func wfMetaRel(run, agent string) string {
	return filepath.Join(tokenTestSession, "subagents", "workflows", run, "agent-"+agent+".meta.json")
}

// metaJSON is a sub-agent meta file with the given description.
func metaJSON(description string) string {
	return fmt.Sprintf(`{"agentType":"workflow-subagent","description":%q,"workflowPhase":"Execute","model":"sonnet"}`, description)
}

// rec builds an expected record.
func rec(id string, min int, model, role, phase, agent string, u usage) TokenRecord {
	return TokenRecord{At: at(min), MessageID: id, Model: model, Role: role, Phase: phase, AgentID: agent,
		Input: u.in, Output: u.out, CacheWrite: u.cw, CacheRead: u.cr}
}

// TestSessionTokens drives the Claude Code reader over hand-made transcripts
// in a temp home. Load-bearing rows:
//
//   - "split id across three lines counted once with maximum": the plan's
//     counting rule. output_tokens rises line by line, and input_tokens and
//     cache_read are present only on the first line, so "last line wins" and
//     "first line wins" both turn it red; only a per-field maximum passes.
//   - "split id with the largest output on the middle line": the same rule
//     for the field that usually rises, so a last-line reader fails here too.
//   - "same id in main and sub-agent counted once for main": the cross-file
//     dedup. Without it the call is counted twice and one copy is billed to
//     the sub-agent.
//   - "full timeline with ship rule on both sides of last sub-agent record":
//     one record in every interval of planning → executing → reviewing →
//     fixing → reviewing → done. m4 (before the last sub-agent record of the
//     last review) must stay review and m5 (after it) must be ship; m3 is
//     the late main record of the FIRST reviewing interval and must stay
//     review, because a fixing write follows it.
//   - "garbage line and user line between valid ones": the tolerant-reader
//     promise. The transcript shape is not a contract, so a line that does
//     not parse must cost nothing but itself.
//   - "no main file" and "empty session id" must fail differently: the first
//     is the no-transcript sentinel a caller turns into a plain message, the
//     second is a caller bug.
func TestSessionTokens(t *testing.T) {
	one := usage{in: 1, out: 2, cw: 3, cr: 4}
	tests := []struct {
		name      string
		sessionID string
		noHome    bool
		build     func(t *testing.T, home string)
		want      []TokenRecord
		wantErr   error // matched with errors.Is
		wantOther bool  // an error that is not ErrNoTranscript
	}{
		{
			name:      "split id across three lines counted once with maximum",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(),
					asstLine("msg_split", 2, "opus", usage{in: 7, out: 10, cw: 300, cr: 900}),
					asstLine("msg_split", 1, "opus", usage{in: 0, out: 50, cw: 300, cr: 0}),
					asstLine("msg_split", 3, "opus", usage{in: 0, out: 200, cw: 300, cr: 0}),
				)
			},
			want: []TokenRecord{rec("msg_split", 1, "opus", RolePlanner, PhasePlan, "", usage{in: 7, out: 200, cw: 300, cr: 900})},
		},
		{
			name:      "split id with the largest output on the middle line",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(),
					asstLine("msg_mid", 1, "opus", usage{in: 5, out: 10, cw: 0, cr: 40}),
					asstLine("msg_mid", 2, "opus", usage{in: 5, out: 90, cw: 0, cr: 40}),
					asstLine("msg_mid", 3, "opus", usage{in: 5, out: 30, cw: 0, cr: 40}),
				)
			},
			want: []TokenRecord{rec("msg_mid", 1, "opus", RolePlanner, PhasePlan, "", usage{in: 5, out: 90, cw: 0, cr: 40})},
		},
		{
			name:      "same id in main and sub-agent counted once for main",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(),
					asstLine("msg_shared", 5, "opus", usage{in: 10, out: 20, cw: 30, cr: 40}),
				)
				writeTranscript(t, home, wfRel("wf_1", "aaa"),
					agentLine("aaa", "msg_shared", 4, "sonnet", usage{in: 10, out: 25, cw: 30, cr: 40}),
					agentLine("aaa", "msg_own", 6, "sonnet", one),
				)
				writeTranscript(t, home, wfMetaRel("wf_1", "aaa"), metaJSON("W1.T1 Do the thing"))
			},
			want: []TokenRecord{
				// Model, agent and role stay the main file's; the maximum
				// and the earliest timestamp still fold in the other copy.
				rec("msg_shared", 4, "opus", RolePlanner, PhasePlan, "", usage{in: 10, out: 25, cw: 30, cr: 40}),
				rec("msg_own", 6, "sonnet", RoleTask, PhasePlan, "aaa", one),
			},
		},
		{
			name:      "sub-agent with no meta file is unknown",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
				writeTranscript(t, home, subRel("nometa"), agentLine("nometa", "msg_s", 2, "sonnet", one))
			},
			want: []TokenRecord{
				rec("msg_m", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_s", 2, "sonnet", RoleUnknown, PhasePlan, "nometa", one),
			},
		},
		{
			name:      "sub-agent with an unreadable meta file is unknown",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
				writeTranscript(t, home, subRel("badmeta"), agentLine("badmeta", "msg_s", 2, "sonnet", one))
				writeTranscript(t, home, subMetaRel("badmeta"), `{"description": "verify:W1.T1`)
			},
			want: []TokenRecord{
				rec("msg_m", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_s", 2, "sonnet", RoleUnknown, PhasePlan, "badmeta", one),
			},
		},
		{
			name:      "sub-agent description matching nothing is other",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
				writeTranscript(t, home, subRel("odd"), agentLine("odd", "msg_s", 2, "sonnet", one))
				writeTranscript(t, home, subMetaRel("odd"), metaJSON("Tidy the garden shed"))
			},
			want: []TokenRecord{
				rec("msg_m", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_s", 2, "sonnet", RoleOther, PhasePlan, "odd", one),
			},
		},
		{
			name:      "files are read main first then sorted and agent id falls back to the file name",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, wfRel("wf_2", "bbb"), agentLine("bbb", "msg_wf2", 1, "sonnet", one))
				writeTranscript(t, home, wfMetaRel("wf_2", "bbb"), metaJSON("review:qa"))
				writeTranscript(t, home, wfRel("wf_1", "zzz"), agentLine("zzz", "msg_wf1", 1, "sonnet", one))
				writeTranscript(t, home, wfMetaRel("wf_1", "zzz"), metaJSON("verify:W1.T1"))
				// No agentId on the line: the id comes from the file name.
				writeTranscript(t, home, subRel("ccc"), asstLine("msg_sub", 1, "sonnet", one))
				writeTranscript(t, home, subMetaRel("ccc"), metaJSON("Codebase research for the thing"))
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
				// Not transcripts: none of these may be read as one.
				writeTranscript(t, home, filepath.Join(tokenTestSession, "subagents", "workflows", "wf_1", "journal.jsonl"), asstLine("msg_journal", 1, "opus", one))
				writeTranscript(t, home, filepath.Join(tokenTestSession, "subagents", "workflows", "wf_1.json"), `{"summary":true}`)
			},
			want: []TokenRecord{
				rec("msg_m", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_sub", 1, "sonnet", RoleResearch, PhasePlan, "ccc", one),
				rec("msg_wf1", 1, "sonnet", RoleVerifier, PhasePlan, "zzz", one),
				rec("msg_wf2", 1, "sonnet", RoleReviewQA, PhasePlan, "bbb", one),
			},
		},
		{
			name:      "garbage line and user line between valid ones",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(),
					asstLine("msg_a", 1, "opus", one),
					`{"type":"assistant","message":{"id":"msg_broken","usage":{"input_tokens":`,
					`not json at all`,
					``,
					`{"type":"user","timestamp":"`+stamp(2)+`","message":{"id":"msg_user","role":"user","content":"hello","usage":{"input_tokens":999}}}`,
					`{"type":"assistant","timestamp":"`+stamp(2)+`","message":{"id":"","model":"opus","usage":{"input_tokens":999}}}`,
					`{"type":"assistant","timestamp":"`+stamp(2)+`","message":{"id":"msg_badusage","usage":"none"}}`,
					`{"type":"system","subtype":"turn_duration"}`,
					`[1,2,3]`,
					asstLine("msg_b", 3, "opus", one),
					// A final line cut off mid-write, with no newline after it.
					`{"type":"assistant","timestamp":"`+stamp(4)+`","message":{"id":"msg_cut","usa`,
				)
			},
			want: []TokenRecord{
				rec("msg_a", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_b", 3, "opus", RolePlanner, PhasePlan, "", one),
			},
		},
		{
			name:      "a line longer than a default scanner buffer is still counted",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				long := strings.Replace(asstLine("msg_long", 1, "opus", one), `"content":[]`,
					`"content":[{"type":"text","text":"`+strings.Repeat("x", 200_000)+`"}]`, 1)
				writeTranscript(t, home, mainRel(), long, asstLine("msg_after", 2, "opus", one))
			},
			want: []TokenRecord{
				rec("msg_long", 1, "opus", RolePlanner, PhasePlan, "", one),
				rec("msg_after", 2, "opus", RolePlanner, PhasePlan, "", one),
			},
		},
		{
			name:      "full timeline with ship rule on both sides of last sub-agent record",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(),
					asstLine("m1", 0, "opus", one),
					phaseLine("w_exec", 10, "executing", one),
					asstLine("m2", 20, "opus", one),
					phaseLine("w_rev1", 30, "reviewing", one),
					asstLine("m3", 40, "opus", one),
					phaseLine("w_fix", 50, "fixing", one),
					phaseLine("w_rev2", 60, "reviewing", one),
					asstLine("m4", 62, "opus", one),
					asstLine("m5", 70, "opus", one),
					phaseLine("w_done", 80, "done", one),
					asstLine("m6", 85, "opus", one),
				)
				writeTranscript(t, home, wfRel("wf_1", "task"), agentLine("task", "s1", 15, "sonnet", one))
				writeTranscript(t, home, wfMetaRel("wf_1", "task"), metaJSON("W1.T1 Do the thing"))
				writeTranscript(t, home, wfRel("wf_2", "qa"), agentLine("qa", "s2", 35, "sonnet", one))
				writeTranscript(t, home, wfMetaRel("wf_2", "qa"), metaJSON("review:qa"))
				writeTranscript(t, home, wfRel("wf_3", "fixer"), agentLine("fixer", "s3", 55, "sonnet", one))
				writeTranscript(t, home, wfMetaRel("wf_3", "fixer"), metaJSON("fix:W1.T1"))
				writeTranscript(t, home, wfRel("wf_4", "quality"),
					agentLine("quality", "s4", 65, "sonnet", one),
					agentLine("quality", "s5", 90, "sonnet", one),
				)
				writeTranscript(t, home, wfMetaRel("wf_4", "quality"), metaJSON("review:quality"))
			},
			want: []TokenRecord{
				rec("m1", 0, "opus", RolePlanner, PhasePlan, "", one),
				rec("w_exec", 10, "opus", RoleConductor, PhaseExecute, "", one),
				rec("m2", 20, "opus", RoleConductor, PhaseExecute, "", one),
				rec("w_rev1", 30, "opus", RoleConductor, PhaseReview, "", one),
				rec("m3", 40, "opus", RoleConductor, PhaseReview, "", one),
				rec("w_fix", 50, "opus", RoleConductor, PhaseFix, "", one),
				rec("w_rev2", 60, "opus", RoleConductor, PhaseReview, "", one),
				rec("m4", 62, "opus", RoleConductor, PhaseReview, "", one),
				rec("m5", 70, "opus", RoleConductor, PhaseShip, "", one),
				rec("w_done", 80, "opus", RoleConductor, PhaseShip, "", one),
				rec("m6", 85, "opus", RoleConductor, PhaseShip, "", one),
				rec("s1", 15, "sonnet", RoleTask, PhaseExecute, "task", one),
				rec("s2", 35, "sonnet", RoleReviewQA, PhaseReview, "qa", one),
				rec("s3", 55, "sonnet", RoleFix, PhaseFix, "fixer", one),
				rec("s4", 65, "sonnet", RoleReviewQuality, PhaseReview, "quality", one),
				rec("s5", 90, "sonnet", RoleReviewQuality, PhaseShip, "quality", one),
			},
		},
		{
			name:      "phase writes in a sub-agent file are not transitions",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("m1", 0, "opus", one), asstLine("m2", 20, "opus", one))
				writeTranscript(t, home, subRel("rogue"), phaseLine("s1", 10, "executing", one))
				writeTranscript(t, home, subMetaRel("rogue"), metaJSON("Crucible"))
			},
			want: []TokenRecord{
				rec("m1", 0, "opus", RolePlanner, PhasePlan, "", one),
				rec("m2", 20, "opus", RolePlanner, PhasePlan, "", one),
				rec("s1", 10, "opus", RoleCrucible, PhasePlan, "rogue", one),
			},
		},
		{
			name:      "no main file",
			sessionID: tokenTestSession,
			build: func(t *testing.T, home string) {
				// Another session's transcript, and this session's side
				// folder without its main file: neither is a match.
				writeTranscript(t, home, "99999999-0000-0000-0000-000000000000.jsonl", asstLine("msg_x", 1, "opus", one))
				writeTranscript(t, home, subRel("orphan"), agentLine("orphan", "msg_s", 2, "sonnet", one))
			},
			wantErr: ErrNoTranscript,
		},
		{
			name:      "no projects folder at all",
			sessionID: tokenTestSession,
			build:     func(t *testing.T, home string) {},
			wantErr:   ErrNoTranscript,
		},
		{
			name:      "empty session id",
			sessionID: "",
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
				// With an empty id the pattern would be ".jsonl"; a file of
				// that name must not be read as the session.
				writeTranscript(t, home, ".jsonl", asstLine("msg_dot", 1, "opus", one))
			},
			wantOther: true,
		},
		{
			name:      "empty home",
			sessionID: tokenTestSession,
			noHome:    true,
			build:     func(t *testing.T, home string) {},
			wantOther: true,
		},
		{
			name:      "session id that is a path",
			sessionID: "../" + tokenTestSession,
			build: func(t *testing.T, home string) {
				writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", one))
			},
			wantOther: true,
		},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			home := t.TempDir()
			tt.build(t, home)
			q := TokenQuery{Home: home, SessionID: tt.sessionID}
			if tt.noHome {
				q.Home = ""
			}
			got, err := claude{}.SessionTokens(q)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("records = %v, want none with an error", got)
				}
				return
			case tt.wantOther:
				if err == nil {
					t.Fatalf("error = nil, want an error")
				}
				if errors.Is(err, ErrNoTranscript) || errors.Is(err, ErrTokensUnsupported) {
					t.Errorf("error = %v, want a caller error, not a sentinel", err)
				}
				if got != nil {
					t.Errorf("records = %v, want none with an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Errorf("got %d records, want %d", len(got), len(tt.want))
			}
			for i := 0; i < len(got) || i < len(tt.want); i++ {
				switch {
				case i >= len(got):
					t.Errorf("record %d missing, want %+v", i, tt.want[i])
				case i >= len(tt.want):
					t.Errorf("record %d unexpected: %+v", i, got[i])
				case !reflect.DeepEqual(got[i], tt.want[i]):
					t.Errorf("record %d\n got  %+v\n want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSessionTokensUnsupported pins that the two providers with no transcript
// reader say so with the sentinel, checked with errors.Is because a caller
// turns exactly that into "cannot be read for this agent CLI yet". Both rows
// are load-bearing: a provider that returned (nil, nil) instead would print
// an empty table that reads as "this session used no tokens". The home holds
// a perfectly good transcript so a provider that fell through to the reader
// would be caught.
func TestSessionTokensUnsupported(t *testing.T) {
	tests := []struct {
		name string
		p    Provider
	}{
		{"codex", codex{}},
		{"opencode", opencode{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", usage{in: 1}))
			got, err := tt.p.SessionTokens(TokenQuery{Home: home, SessionID: tokenTestSession})
			if !errors.Is(err, ErrTokensUnsupported) {
				t.Fatalf("error = %v, want errors.Is ErrTokensUnsupported", err)
			}
			if got != nil {
				t.Errorf("records = %v, want none", got)
			}
		})
	}
}

// TestSessionTokensThroughInterface pins that the method is reachable through
// Lookup for every provider name, which is how the command calls it. The
// default row is load-bearing: a session with no recorded provider resolves
// to "" and must reach the reader, not the unsupported error.
func TestSessionTokensThroughInterface(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		unsupported bool
	}{
		{"default provider reads the transcript", "", false},
		{"claude reads the transcript", ClaudeName, false},
		{"codex is unsupported", CodexName, true},
		{"opencode is unsupported", OpenCodeName, true},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			home := t.TempDir()
			writeTranscript(t, home, mainRel(), asstLine("msg_m", 1, "opus", usage{in: 1}))
			p, err := Lookup(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.SessionTokens(TokenQuery{Home: home, SessionID: tokenTestSession})
			if tt.unsupported {
				if !errors.Is(err, ErrTokensUnsupported) {
					t.Fatalf("error = %v, want ErrTokensUnsupported", err)
				}
				return
			}
			if err != nil || len(got) != 1 {
				t.Fatalf("got %d records, error %v; want 1 record", len(got), err)
			}
		})
	}
}

// TestRoleFor pins the description-to-role rules. Every role of the plan's
// Assumption A3 that a sub-agent can have has a row (planner is decided by
// phase, see TestSessionTokens). Load-bearing rows:
//
//   - "no meta file" and "description matching nothing" must differ: unknown
//     means afk could not see a label, other means it saw one it has no rule
//     for, and the second is the one that says a rule is missing.
//   - "review with another check name" must be other, not a made-up
//     review:{name} role, so a ledger never grows roles nobody defined.
//   - "task id that only looks like one" guards the task-id prefix against
//     matching any description that starts with a W.
//   - "main session ignores its description" because the main transcript has
//     no meta file and must not fall to unknown.
func TestRoleFor(t *testing.T) {
	tests := []struct {
		name        string
		isMain      bool
		hasMeta     bool
		description string
		want        string
	}{
		{"main session is conductor", true, false, "", RoleConductor},
		{"main session ignores its description", true, true, "verify:W1.T1", RoleConductor},
		{"codebase research", false, true, "Codebase research for flaky host test", RoleResearch},
		{"web research", false, true, "Web research for pty libraries", RoleResearch},
		{"crucible", false, true, "Crucible review of the draft plan", RoleCrucible},
		{"verifier", false, true, "verify:W2.T1", RoleVerifier},
		{"fix by label", false, true, "fix:W2.T1", RoleFix},
		{"fix a discrepancy", false, true, "Fix discrepancy in W2.T1", RoleFix},
		{"review compliance", false, true, "review:compliance", RoleReviewCompliance},
		{"review quality", false, true, "review:quality", RoleReviewQuality},
		{"review qa", false, true, "review:qa", RoleReviewQA},
		{"review qa with trailing words", false, true, "review:qa second pass", RoleReviewQA},
		{"review with another check name", false, true, "review:security", RoleOther},
		{"review with a longer check name", false, true, "review:qaextra", RoleOther},
		{"critic by label", false, true, "critic:quality", RoleCritic},
		{"critic for a check", false, true, "Critic for the compliance check", RoleCritic},
		{"task by work-plan id", false, true, "W1.T2 Add the provider token method", RoleTask},
		{"task by a bare work-plan id", false, true, "W12.T30", RoleTask},
		{"task by execute label", false, true, "Execute (W1.T2)", RoleTask},
		{"task id that only looks like one", false, true, "W1.Tx Something", RoleOther},
		{"task id run into a word", false, true, "W1.T2x Something", RoleOther},
		{"leading space is ignored", false, true, "  verify:W1.T1", RoleVerifier},
		{"no meta file", false, false, "", RoleUnknown},
		{"no meta file wins over a description", false, false, "verify:W1.T1", RoleUnknown},
		{"empty description", false, true, "", RoleOther},
		{"description matching nothing", false, true, "general helper", RoleOther},
		{"prefix is case sensitive", false, true, "VERIFY:W1.T1", RoleOther},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			if got := roleFor(tt.isMain, tt.hasMeta, tt.description); got != tt.want {
				t.Errorf("roleFor(%v, %v, %q) = %q, want %q", tt.isMain, tt.hasMeta, tt.description, got, tt.want)
			}
		})
	}
}

// TestPhaseWrite pins which Bash commands count as the conductor's status
// write. Load-bearing rows: "last match wins" (one command can set the phase
// twice, and the file ends up with the last), "does not name status.json"
// (a jq over some other file is not a transition), and "unknown phase word"
// (the six words of the status schema are the whole vocabulary; anything
// else is some other text that happened to match).
func TestPhaseWrite(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
		wantOK  bool
	}{
		{"plain write", `jq '.phase = "executing"' .afk/x/status.json`, "executing", true},
		{"write among other fields", `jq --arg now "$N" '.phase = "done" | .awaiting_input = false | .updated_at = $now' .afk/$N/status.json > t && mv t .afk/$N/status.json`, "done", true},
		{"last match wins", `jq '.phase = "reviewing"' status.json; jq '.phase = "fixing"' status.json`, "fixing", true},
		{"failed", `jq '.phase = "failed"' status.json`, "failed", true},
		{"planning", `jq '.phase = "planning"' status.json`, "planning", true},
		{"does not name status.json", `jq '.phase = "executing"' other.json`, "", false},
		{"names status.json but sets no phase", `cat .afk/x/status.json`, "", false},
		{"reads the phase", `jq '.phase == "executing"' status.json`, "", false},
		{"phase from a variable", `jq --arg p executing '.phase = $p' status.json`, "", false},
		{"unknown phase word", `jq '.phase = "napping"' status.json`, "", false},
		{"empty command", ``, "", false},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, ok := phaseWrite(tt.command)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("phaseWrite(%q) = %q, %v; want %q, %v", tt.command, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestPhaseFor pins the phase of a record against a list of transitions and
// the ship cutoff computed from the sub-agent records. Load-bearing rows:
//
//   - "first reviewing interval late main records stay review": a fixing
//     write follows that interval, so the calls after its last sub-agent are
//     the conductor handling a REJECT, not shipping.
//   - "last review main after last sub-agent is ship" against "last review
//     main before last sub-agent stays review" and "... at the same instant
//     stays review": the two sides and the edge of Assumption A2's rule.
//   - "last review with no sub-agent stays review": with nothing to mark
//     where the review ended, calling everything ship would move the whole
//     review into ship.
//   - "sub-agent in the last review is never ship": the rule is about the
//     main session only.
//   - "session that ends in fixing has no ship": a fixing write after the
//     last reviewing write means no interval qualifies.
//   - "at the instant of a write is the new phase": the write's own message
//     is billed to the phase it starts.
func TestPhaseFor(t *testing.T) {
	full := []phaseTransition{
		{at(10), "executing"}, {at(30), "reviewing"}, {at(50), "fixing"}, {at(60), "reviewing"}, {at(80), "done"},
	}
	// Sub-agent records: one per interval, the last review's at minute 65.
	fullSubs := []time.Time{at(15), at(35), at(55), at(65), at(90)}
	tests := []struct {
		name        string
		transitions []phaseTransition
		subs        []time.Time
		at          time.Time
		isMain      bool
		want        string
	}{
		{"before the first write is plan", full, fullSubs, at(5), true, PhasePlan},
		{"no writes at all is plan", nil, nil, at(5), true, PhasePlan},
		{"at the instant of a write is the new phase", full, fullSubs, at(10), true, PhaseExecute},
		{"executing is execute", full, fullSubs, at(20), true, PhaseExecute},
		{"sub-agent in executing is execute", full, fullSubs, at(15), false, PhaseExecute},
		{"first reviewing interval is review", full, fullSubs, at(32), true, PhaseReview},
		{"first reviewing interval late main records stay review", full, fullSubs, at(45), true, PhaseReview},
		{"fixing is fix", full, fullSubs, at(55), true, PhaseFix},
		{"last review main before last sub-agent stays review", full, fullSubs, at(62), true, PhaseReview},
		{"last review main at the same instant stays review", full, fullSubs, at(65), true, PhaseReview},
		{"last review main after last sub-agent is ship", full, fullSubs, at(70), true, PhaseShip},
		{"sub-agent in the last review is never ship", full, fullSubs, at(65), false, PhaseReview},
		{"late sub-agent in the last review is never ship", full, append([]time.Time{at(64)}, fullSubs...), at(64), false, PhaseReview},
		{"after done is ship", full, fullSubs, at(85), true, PhaseShip},
		{"sub-agent after done is ship", full, fullSubs, at(90), false, PhaseShip},
		{"after failed is ship", []phaseTransition{{at(10), "executing"}, {at(20), "failed"}}, nil, at(25), true, PhaseShip},
		{"last review with no sub-agent stays review", full, []time.Time{at(15), at(35), at(55), at(90)}, at(70), true, PhaseReview},
		{"last review still open applies the ship rule", []phaseTransition{{at(10), "executing"}, {at(30), "reviewing"}}, []time.Time{at(35)}, at(40), true, PhaseShip},
		{"session that ends in fixing has no ship", []phaseTransition{{at(10), "executing"}, {at(30), "reviewing"}, {at(50), "fixing"}}, []time.Time{at(35), at(55)}, at(45), true, PhaseReview},
		{"repeated reviewing write does not restart the interval", []phaseTransition{{at(30), "reviewing"}, {at(40), "reviewing"}, {at(80), "done"}}, []time.Time{at(35)}, at(38), true, PhaseShip},
		{"planning written explicitly is plan", []phaseTransition{{at(1), "planning"}}, nil, at(5), true, PhasePlan},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			cutoff := shipCutoff(tt.transitions, tt.subs)
			if got := phaseFor(tt.transitions, cutoff, tt.at, tt.isMain); got != tt.want {
				t.Errorf("phaseFor(at %s, main %v) = %q, want %q (cutoff %v)",
					tt.at.Format("15:04"), tt.isMain, got, tt.want, cutoff)
			}
		})
	}
}

// TestPhaseAndRoleNames pins the strings that end up in the ledger. They are
// a file format (docs/PLAN.md §4.7): renaming a constant must be a visible
// decision, not a side effect of a refactor. Every row is load-bearing for
// that reason alone.
func TestPhaseAndRoleNames(t *testing.T) {
	tests := []struct{ got, want string }{
		{PhasePlan, "plan"}, {PhaseExecute, "execute"}, {PhaseReview, "review"}, {PhaseFix, "fix"}, {PhaseShip, "ship"},
		{RoleConductor, "conductor"}, {RolePlanner, "planner"}, {RoleResearch, "research"},
		{RoleCrucible, "crucible"}, {RoleTask, "task"}, {RoleFix, "fix"}, {RoleVerifier, "verifier"},
		{RoleReviewCompliance, "review:compliance"}, {RoleReviewQuality, "review:quality"},
		{RoleReviewQA, "review:qa"}, {RoleCritic, "critic"}, {RoleUnknown, "unknown"}, {RoleOther, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("constant = %q, want %q", tt.got, tt.want)
			}
		})
	}
}
