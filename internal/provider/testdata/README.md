# Token transcript fixture

`claude-home/` is a trimmed copy of the transcripts of one real, finished, autonomous afk session (the
`t0-4` session, session id `66065044-0f91-4d8d-9972-5f1cd1878494`, recorded on 2026-10-11). It stands in for
`~/.claude` when the token reader is tested: pass `Home: "testdata/claude-home"`.

The transcript shape here was observed on this machine on 2026-10-11. It is not a documented contract.

## Layout

```
claude-home/.claude/projects/fixture-project/
  66065044-0f91-4d8d-9972-5f1cd1878494.jsonl                        main session
  66065044-0f91-4d8d-9972-5f1cd1878494/subagents/agent-*.jsonl      Agent-tool sub-agent (1 file)
  66065044-0f91-4d8d-9972-5f1cd1878494/subagents/workflows/wf_*/agent-*.jsonl   workflow agents (11 files)
  ... each agent-*.jsonl has an agent-*.meta.json beside it
```

13 transcripts (1 main, 1 Agent-tool sub-agent, 11 workflow agents) and 12 meta files (the main session has none).

## Regenerating

```bash
internal/provider/testdata/trim-transcript.sh ~/.claude/projects 66065044-0f91-4d8d-9972-5f1cd1878494 \
  internal/provider/testdata/claude-home/.claude/projects/fixture-project
```

The script takes the projects folder, the session id and an output folder that must not exist. It finds the
main file as `PROJECTS/*/SESSION.jsonl`, never modifies the source, builds the output in a sibling folder and
renames it into place (a missing session leaves nothing behind, and exits 1 with a one-line message). Running
it twice gives byte-identical trees. The source session lives only on the machine that ran it, so regenerating
needs that machine's `~/.claude`.

## What was removed

- Every line that is not `type == "assistant"` with a `message.id`.
- From each kept line everything except `type`, `timestamp`, `isSidechain`, `agentId` (when present),
  `sessionId` and `message.{id, model, role, usage, content}`. Usage keeps only `input_tokens`,
  `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`.
- `content` is emptied, except a `Bash` tool call that writes a phase into `status.json`
  (`.phase = "word"`), which is rewritten to `jq '.phase = "word"' .afk/t0-4/status.json`. No original
  command text is copied. No prompt text, thinking, tool input or output, `cwd`, `gitBranch`, request id
  (`requestId`, `uuid`) or path under `/home`.
- Each `.meta.json` keeps only `agentType`, `description`, `workflowPhase`, `model`. Descriptions are task
  labels (`verify:W1.T1`, `review:qa`, `W2.T1 <task title>`, `Codebase research for <topic>`).
- `journal.jsonl` and the `workflows/wf_*.json` summaries are not copied.

Checks run on the result: no hit for `dphaener`, `/home/`, `req_`, `"text"`, `"thinking"`, `"tool_result"`;
`git check-ignore -v` matches no rule for the fixture files; total size is about 210 KB.

## Expected numbers

Computed with `jq` only, never with Go code from this repo. Run from the repository root. The first script
builds `$W/transitions.jsonl`, `$W/lines.jsonl` (every assistant line), `$W/records.json` (one record per
message id) and `$W/final.json` (records with role and phase). Rules used are those of W1.T2 and the
plan's Assumptions A1 to A3: phases come from the conductor's own `status.json` writes in the main
file; before the first write the phase is `planning`; `planning`, `executing`, `reviewing`, `fixing`, `done`
and `failed` map to `plan`, `execute`, `review`, `fix`, `ship`, `ship`; in the last `reviewing` interval a
main-session record later than the last sub-agent record of that interval is `ship`; a main-session
record is `planner` in phase `plan`, else `conductor`.

```bash
F=internal/provider/testdata/claude-home/.claude/projects/fixture-project
SID=66065044-0f91-4d8d-9972-5f1cd1878494
W=$(mktemp -d)

# (a) phase writes in the main file
jq -c 'select(.type=="assistant") | .timestamp as $t | .message.content[]?
       | select(.type=="tool_use" and .name=="Bash" and (.input.command|contains("status.json")))
       | {at:$t, phase:([.input.command|match("\\.phase = \"(\\w+)\"";"g").captures[0].string]|last)}' \
  $F/$SID.jsonl > $W/transitions.jsonl

# every assistant line of every file, main first, then sub-agent files in sorted order
( echo $SID.jsonl; cd $F && find $SID/subagents -name 'agent-*.jsonl' | LC_ALL=C sort ) > $W/files.txt
: > $W/lines.jsonl
n=0
while read -r f; do
  meta=${f%.jsonl}.meta.json
  desc=$( [ $n -eq 0 ] && echo "" || { [ -f $F/$meta ] && jq -r .description $F/$meta || echo "NOMETA"; } )
  jq -c --arg f "$f" --argjson o $n --arg d "$desc" \
    '{file:$f, ord:$o, main:($o==0), desc:$d, at:.timestamp, id:.message.id, model:.message.model,
      i:.message.usage.input_tokens, o:.message.usage.output_tokens,
      cw:.message.usage.cache_creation_input_tokens, cr:.message.usage.cache_read_input_tokens}' $F/$f >> $W/lines.jsonl
  n=$((n+1))
done < $W/files.txt

# (b) one record per message id: max of each usage field, earliest timestamp, first-seen model and file
jq -s 'group_by(.id) | map(.[0] + {at:(map(.at)|min), i:(map(.i)|max), o:(map(.o)|max),
         cw:(map(.cw)|max), cr:(map(.cr)|max)}) | sort_by(.ord, .at)' $W/lines.jsonl > $W/records.json

# roles, phases, ship rule
jq --slurpfile tr $W/transitions.jsonl '
  def role($main; $d):
    if $main then "main"
    elif $d == "NOMETA" then "unknown"
    elif ($d|test("^(Codebase research for|Web research for)")) then "research"
    elif ($d|startswith("Crucible")) then "crucible"
    elif ($d|startswith("verify:")) then "verifier"
    elif ($d|startswith("fix:")) or ($d|startswith("Fix discrepancy in")) then "fix"
    elif ($d|test("^review:(compliance|quality|qa)$")) then "review:" + ($d|sub("^review:";""))
    elif ($d|startswith("critic:")) or ($d|startswith("Critic for")) then "critic"
    elif ($d|test("^W[0-9]+\\.T[0-9]+ ")) or ($d|startswith("Execute (")) then "task"
    else "other" end;
  def pmap: {planning:"plan",executing:"execute",reviewing:"review",fixing:"fix",done:"ship",failed:"ship"}[.];
  ($tr | sort_by(.at)) as $t
  | map(. as $r | .status = ([$t[] | select(.at <= $r.at)] | last | .phase // "planning"))
  | map(.phase = (.status | pmap // "plan") | .phase = (if .status=="planning" then "plan" else .phase end))
  # last reviewing interval: the last reviewing write that no fixing write follows
  | ([$t[] | select(.phase=="reviewing")] | last) as $rv
  | ($t | map(select(.at > ($rv.at // "")))[0].at // "9999") as $rvend
  | ([.[] | select(.main|not) | select(.at >= $rv.at and .at < $rvend) | .at] | max) as $lastsub
  | map(.phase = (if .main and .status=="reviewing" and .at >= $rv.at and .at < $rvend and $lastsub != null and .at > $lastsub then "ship" else .phase end))
  | map(.role = (if .main then (if .phase=="plan" then "planner" else "conductor" end) else role(false; .desc) end))
' $W/records.json > $W/final.json
echo "work folder: $W"
```

Then the tables (they read `$W`):

```bash
# (b) per file, per role
jq -r 'group_by([.ord,.role])[] | [.[0].file, .[0].role, length, (map(.i)|add), (map(.o)|add), (map(.cw)|add), (map(.cr)|add)] | @tsv' $W/final.json

# (c) per phase, role and model, with the weighted cost and each row's share
jq -r '
  def w: .i + 5*.o + 1.25*.cw + 0.1*.cr;
  def r2: (. * 100 | round) / 100;
  (map(w) | add) as $tot
  | group_by([.phase,.role,.model])
  | map({p:.[0].phase, r:.[0].role, m:.[0].model, n:length, i:(map(.i)|add), o:(map(.o)|add), cw:(map(.cw)|add), cr:(map(.cr)|add)})
  | map(. + {w: w})
  | ((.[] | [.p, .r, .m, .n, .i, .o, .cw, .cr, (.w|r2), ((.w / $tot * 1000 | round) / 10)]),
     ["TOTAL", "", "", (map(.n)|add), (map(.i)|add), (map(.o)|add), (map(.cw)|add), (map(.cr)|add), ($tot|r2), 100]) | @tsv' $W/final.json

# (d) message ids that repeat within a file, and across files
jq -s 'group_by([.file,.id]) | map(select(length > 1))
       | {repeating_ids: length, extra_lines: (map(length-1)|add),
          differing_usage: (map(select(([.[]|[.i,.o,.cw,.cr]]|unique|length) > 1)) | length)}' $W/lines.jsonl
jq -s '[group_by(.id)[] | select(([.[].file]|unique|length) > 1)] | length' $W/lines.jsonl
```

### (a) Phase writes in the main file

| Timestamp | Phase written | Phase it starts |
|---|---|---|
| (start of file) | | `plan` (before the first write) |
| 2026-10-11T17:59:47.600Z | `executing` | `execute` |
| 2026-10-11T18:39:11.919Z | `reviewing` | `review` |
| 2026-10-11T18:45:17.981Z | `done` | `ship` |

There is no `fixing` write, so the one `reviewing` interval is the last one. The last sub-agent call in it is
at 2026-10-11T18:42:10.569Z; the first main-session call after that is at 2026-10-11T18:42:13.060Z. Of the
21 `ship` calls, 5 are in that reviewing tail and 16 are after the `done` write.

### (b) Per file, each message id counted once (maximum of each usage field)

Roles follow W1.T2. The main file holds both `planner` (phase `plan`) and `conductor` (every later phase), so
it has two rows. Columns: calls (unique message ids), input, output, cache write, cache read.

| File (below `fixture-project/`) | Role | Calls | Input | Output | Cache write | Cache read |
|---|---|---:|---:|---:|---:|---:|
| `<sid>.jsonl` | conductor | 29 | 66 | 15606 | 72570 | 4020098 |
| `<sid>.jsonl` | planner | 12 | 26 | 24006 | 164921 | 1788598 |
| `<sid>/s/agent-a544073cd6671e444.jsonl` | research | 7 | 14 | 8114 | 78599 | 330015 |
| `<sid>/sw/wf_42c1357d-787/agent-a56f22f36088de361.jsonl` | review:qa | 9 | 18 | 2417 | 41538 | 438831 |
| `<sid>/sw/wf_42c1357d-787/agent-a6f1c445a2196c536.jsonl` | review:quality | 3 | 6 | 890 | 24209 | 93606 |
| `<sid>/sw/wf_42c1357d-787/agent-aa79cd2f791cc56e4.jsonl` | review:compliance | 4 | 8 | 1554 | 57300 | 139017 |
| `<sid>/sw/wf_d1373d04-06c/agent-a107a5d925f35be8d.jsonl` | task | 9 | 18 | 13762 | 55702 | 495886 |
| `<sid>/sw/wf_d1373d04-06c/agent-a4900e5daf7926b7c.jsonl` | verifier | 5 | 10 | 1339 | 47338 | 170625 |
| `<sid>/sw/wf_d1373d04-06c/agent-a6a5e9c36806f3008.jsonl` | verifier | 5 | 10 | 1012 | 27387 | 187793 |
| `<sid>/sw/wf_d1373d04-06c/agent-a9fb588eb3f6fd633.jsonl` | task | 10 | 20 | 15286 | 60388 | 602817 |
| `<sid>/sw/wf_d1373d04-06c/agent-ad39200502c37ac38.jsonl` | verifier | 8 | 18 | 1431 | 30056 | 321407 |
| `<sid>/sw/wf_d1373d04-06c/agent-adee5f8d935e10b5e.jsonl` | task | 12 | 24 | 5256 | 51831 | 516533 |
| `<sid>/sw/wf_d1373d04-06c/agent-ae2d566959002bdff.jsonl` | task | 25 | 50 | 32991 | 158756 | 1697594 |
| `<sid>/sw/wf_d1373d04-06c/agent-af150b31d609737bd.jsonl` | task | 5 | 10 | 4067 | 34908 | 201250 |

`<sid>` is the session id; `/s/` is `subagents/` and `/sw/` is `subagents/workflows/`. The calls add up to
143, the number of unique message ids.

### (c) Per phase, role and model

Weighted cost = `input + 5 x output + 1.25 x cache_write + 0.1 x cache_read`. Share is the row's part of the
grand total, in percent to one decimal.

| Phase | Role | Model | Calls | Input | Output | Cache write | Cache read | Weighted | Share % |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| execute | conductor | claude-opus-5-5 | 6 | 14 | 4496 | 16195 | 1196648 | 162402.55 | 5.7 |
| execute | task | claude-opus-5-5 | 44 | 88 | 62039 | 274846 | 2796297 | 933470.2 | 32.5 |
| execute | task | claude-sonnet-5-5 | 17 | 34 | 9323 | 86739 | 717783 | 226851.05 | 7.9 |
| execute | verifier | claude-sonnet-5-5 | 18 | 38 | 3782 | 104781 | 679825 | 217906.75 | 7.6 |
| plan | planner | claude-opus-5-5 | 12 | 26 | 24006 | 164921 | 1788598 | 505067.05 | 17.6 |
| plan | research | claude-sonnet-5-5 | 7 | 14 | 8114 | 78599 | 330015 | 171834.25 | 6.0 |
| review | conductor | claude-opus-5-5 | 2 | 4 | 723 | 8023 | 428189 | 56466.65 | 2.0 |
| review | review:compliance | claude-sonnet-5-5 | 4 | 8 | 1554 | 57300 | 139017 | 93304.7 | 3.2 |
| review | review:qa | claude-sonnet-5-5 | 9 | 18 | 2417 | 41538 | 438831 | 107908.6 | 3.8 |
| review | review:quality | claude-sonnet-5-5 | 3 | 6 | 890 | 24209 | 93606 | 44077.85 | 1.5 |
| ship | conductor | claude-opus-5-5 | 21 | 48 | 10387 | 48352 | 2395261 | 351949.1 | 12.3 |
| TOTAL |  |  | 143 | 298 | 127731 | 905503 | 11004070 | 2871238.75 | 100.0 |

`conductor` runs in three phases (`execute`, `review`, `ship`) and `task` runs on two models in `execute`:
those are the rows that show a role split across phases and a phase split across models.

### (d) Repeated message ids

284 assistant lines carry 143 unique message ids. Within a single file, 103 ids appear on more than one line
(141 extra lines in all), and 76 of those 103 have differing usage between their lines (the output count
rises as the message streams in). No message id appears in two different files.

# Planning conversation fixtures

`plan-chat/` holds hand-written transcripts for the planning conversation reader (`conversation.go`,
`TestPlanConversation`). They were written by hand to the shape seen in Claude Code transcripts on 2026-10-14
(see `conversation.go` and the T8.3 plan's Context); none is copied from a real session. The shape is not a
documented contract.

## Layout

Each folder `plan-chat/<case>/` stands for a home's `.claude/projects/` folder, and the test copies it into
`{t.TempDir()}/.claude/projects/` before reading. Inside it, `-work-project/` is the transcript folder of the
fixed root `/work/project` (the folder-name rule turns every `/` into `-`), and every line's `cwd` is
`/work/project`. That root never exists on disk: the reader only encodes it and compares it.

The fixtures are copied, rather than kept under a literal `.claude/` folder like `claude-home/`, for two
reasons. A common global gitignore ignores `.claude/`, which would leave new fixture files out of a commit
without a word. And the copy lets the test set every file's mtime to an hour after the host start it reads
against, so the reader's "not written since the host started" cut never depends on when the repository was
checked out.

## Cases

| Folder | What it holds |
|---|---|
| `plain` | two turns of text: one message written as two text blocks, one turn ending with `stop_hook_summary` alone, one with both end lines, a meta skill body and a `<system-reminder>` line |
| `pending-question` | an `AskUserQuestion` with two questions and no answer; recommended labels in two cases |
| `answered-question` | one question answered with `toolUseResult.answers`, one declined (no answers object) and the interrupt line after it |
| `waiting-text` | the agent's last turn ended |
| `working` | a tool call answered, then another one made, with no turn end |
| `truncated` | the last line is half-written, with no newline |
| `background` | a background agent and a background command started; only the command finishes |
| `no-transcript` | only another project's folder (`-work-other`) |
| `older` | a planning transcript begun 30 minutes before the host |
| `non-planning` | the planning transcript plus a newer plain chat and a newer `/afk:go` session in the same folder |
| `subagent-sidechain` | sidechain lines in the planning transcript, and a later planning-looking `plan/subagents/agent-a1.jsonl` |
| `cwd-differs` | a planning transcript in this folder whose `cwd` is `/work.project` (same folder name, other folder) |

Expected entries are written into the test table by hand, from reading these files.
