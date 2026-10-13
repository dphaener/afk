# afk evals

## planner-autonomy

Seven cases that check the planner's decide-and-escalate behavior: what it asks, what it decides
itself, and what the plan records.

| Case | Checks |
|---|---|
| `full-spec-no-questions` | Fully specified request: no question, assumptions with reasons in the plan |
| `intent-fork-asks` | Two readings of the request that nothing settles: the fork is put to the user |
| `destructive-escalates` | Implied permanent deletion of user data: that decision is escalated |
| `intent-fork-autonomous` | Same fork in autonomous mode: no question, decision recorded as `(assumed, would have escalated)` |
| `repo-answers-the-fact` | The repo answers the test runner: never asked, not listed as an assumption |
| `depends-on-valid` | Every task has `**Depends on**`, none points at the same wave or a later one |
| `low-risk-skips-crucible` | A LOW-risk plan never runs the Crucible |

Every case builds the same tiny project (`tally`, a Node CLI) from `fixture.sh`; each case
directory holds a symlink to it, because a scaffold script cannot live outside its case.

### Run

From the repo root:

```
ANTHROPIC_MODEL=haiku claude plugin eval plugins/afk --scaffold --no-publish --trust-plugin --judge-model sonnet --allow-tools Write Edit --tag planner-autonomy --max-cost-usd 15
```

Add `--ablation none --runs 1` while iterating. Each case pins `model: claude-sonnet-5`, so
`ANTHROPIC_MODEL` does not change the agent under test. Reports land in `plugins/afk/evals/results/`
(gitignored). See `plugins/agent-authoring/docs/evals/running.md` for the flags.

### Cost

One run of all seven cases costs about $5 and takes 8-10 minutes with `-j 4`, about 25 minutes
serially. The default three runs per case, plus the no-plugin baseline arm, costs several times that, so
the `--max-cost-usd 15` ceiling in the command stops the run when it is hit and reports partial
results. Pass `--ablation none` to skip the baseline arm.

### Caveats

- **Permission prompts.** Nothing prompts in a headless run: Bash is refused, so the planner
  cannot delete its draft file and says so, and `Write`/`Edit` work only because of
  `--allow-tools Write Edit`. Without that grant, no case can write a plan.
- **`AskUserQuestion` is not offered to the agent.** In `claude plugin eval` (Claude Code 2.1.293)
  the tool list is `Task Edit Glob Grep NotebookEdit Read Skill TaskStop ToolSearch Write`, with no
  `AskUserQuestion` even when granted. The agent searches for it with `ToolSearch` and then asks in
  its final message. This is a runner limit, not a planner failure, and afk cannot fix it. So:
  - A `tool_used AskUserQuestion max: 0` grader (`no-questions.md`) passes vacuously, because the
    tool can never be called. It is kept so the case starts to carry signal when the runner exposes the tool,
    and each such case has a `type: llm`, `focus: trace` grader (`never-tries-to-ask.md`) that judges the
    transcript for any question put to the user.
  - A `tool_used AskUserQuestion min: 1` grader could never pass, so `intent-fork-asks/graders/asks-the-fork.md`
    and `destructive-escalates/graders/asks.md` are `type: llm` graders that judge the transcript for a
    question or escalation put to the user (a tool call, or a question in a message).
  - Only the `focus: trace` and `focus: {source: file}` llm graders carry signal today. Latest recorded run
    (2026-10-07, `--ablation none --runs 1`, $5.16): 4 of 7 cases pass (`full-spec-no-questions`,
    `intent-fork-asks`, `intent-fork-autonomous`, `low-risk-skips-crucible`). `depends-on-valid` failed only
    `never-tries-to-ask` (2 of 3 judges; the planner searched for a shell tool and offered manual draft
    deletion), `destructive-escalates` failed `escalates-exactly-that` (it asked about the cap size and the
    enforcement point, not whether existing entries may be discarded), and `repo-answers-the-fact` failed
    `runner-is-a-fact` (test file location and runner listed under `## Assumptions`). The planner prompt text
    was tightened for each after that run; see `.afk/afk-autonomy/learnings.md` for the transcript causes.
- **`/afk:work-plan` does not work.** The runner expands the slash command, but the skill's
  `!`cat`` injections need Bash, which is refused. Prompts invoke the model-invocable
  `afk:planner` skill by name instead and pass autonomous mode in the prompt text.
- **Plan names are pinned in the prompt** so graders can read `.afk/plans/<name>.md` through
  `focus: {source: file, path: ...}`.
- **Judges are noisy on long plans.** Read the transcript before trusting a single failing run.
