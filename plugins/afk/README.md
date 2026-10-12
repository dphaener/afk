# afk

afk is a structured Plan → Execute → Review workflow for Claude Code. It turns a ticket or feature description into verified code changes through three phases — Plan, Execute, and Review — each checked against a plan file that acts as the contract between them. Small, unambiguous, contained requests can skip the full pipeline: afk announces a routing verdict up front and, on a LIGHT verdict, runs a terse light path instead.

No more one-shot prompts that drift off-spec. afk keeps Claude focused, accountable, and auditable.

---

## Installation

afk ships with the [afk CLI](https://github.com/dphaener/afk). The CLI installs it into Claude Code, Codex, and OpenCode, asking which ones first:

```bash
afk plugin install
```

Or add the afk repo as a marketplace:

```bash
claude plugin marketplace add dphaener/afk
/plugin install afk@afk
```

---

## Prerequisites

- [Claude Code CLI](https://claude.ai/code) installed and configured
- A `CLAUDE.md` in your repo with test, build, and lint commands (strongly recommended)
- Optionally: Jira MCP or Atlassian CLI for ticket integration

---

## Modes: Default and Autonomous

afk runs in one of two modes. Both avoid stops that exist only to ask permission; they differ in what happens when a decision genuinely needs you.

**Default mode: decide and escalate.** afk decides every question it can answer itself, from your repo, the ticket, or a sensible default, and records the decision with its reason in the plan's `## Assumptions` section (`- [A1] {decision} → {answer} — {reason}`). It asks you only when a decision meets the **escalation rule**: an irreversible or destructive action, security or data-loss exposure, scope that conflicts with your request, or a genuine fork in your intent that nothing in the repo or ticket settles. A fully specified request produces zero questions.

**Autonomous mode: no questions at all.** Pass `--autonomous` (`/afk:plan`, `/afk:execute`, `/afk:go`) or create the marker file `.afk/{name}/autonomous`. The `/afk:go` conductor writes the marker when it sees the flag, so a resumed or restarted session keeps the mode; delete the file by hand to return to default mode. Decisions that would have escalated are decided too, preferring the reversible, least-exposure option, and are tagged `(assumed, would have escalated)` so you can review them in the plan. Every other point that could ask is non-interactive: a missing session UUID continues degraded, a sub-agent's `NEEDS_INPUT` is answered by the conductor and logged under `## Relayed answers` in `decisions.md`, a missing plan argument stops with an error, and the Crucible runs without a question.

**Stops that no longer exist (both modes):**

- The LIGHT-path confirmation gate.
- The Workflow engine opt-in question.
- The "generate the plan?" confirmation after the grill.
- The Crucible offer. The Crucible now runs automatically, only when the planner rates the plan's risk HIGH, and afk resolves its CRITICAL findings itself and records each in `## Assumptions`.
- The ship approval: the commit (and, in default mode, the draft PR) happens without asking (see Ship below).

**Stops that remain in default mode only:** an escalation-rule question during planning, a relayed `NEEDS_INPUT` from a sub-agent, and the `/afk:go` fix-loop cap (2 fix cycles exhausted, findings handed to you). Autonomous mode replaces the cap stop with a commit of the unapproved work on the session branch (no push, no PR) and marks the session `failed`.

**The autonomous policy.** In autonomous mode the conductor follows seven rules, written out in `skills/conductor/prompts/autonomous.md`: never wait on a rubber stamp; take the recommended option on any question and record it; park instead of guessing; fix every confirmed blocker and major finding; never block on a check a machine cannot do; log unsolicited ideas instead of building them; and obey the project's `afk.toml` `[policy] notes` lines. **Parking** means the next step is irreversible or needs something only you can supply (a missing credential, a third-party dashboard, real money beyond your allowance): the conductor adds a blocking inbox item with `afk inbox add` (which sends a push notification when the project's `afk.toml` has `[notify] ntfy_url`), sets `awaiting_input`, says what is missing, and ends its turn; the desktop notice is only the fallback if the inbox command fails. Run `/afk:go --session {name}` again to resume. Would-have-escalated answers, owner checks and scope ideas also go to the inbox, readable with `afk inbox`, and stay in `.afk/{name}/decisions.md`.

**Stop before shipping.** An autonomous session ends at a reviewed commit on its own branch with `phase: done`. It does not push and does not open a pull request; the branch is yours to ship. The one exception is a session description that says how to ship (for example "push the branch and open a draft PR"); the conductor follows those instructions.

**What can still pause a run.** Permission prompts from the Claude Code harness are outside afk's control and can pause a run in either mode, including the Workflow tool's own permission dialog the first time a script runs. Pre-approve the tools you trust in your Claude Code permissions if you want truly unattended runs.

---

## Agent definitions

afk ships six agent definitions in `agents/`: `afk:task` (executes and fixes tasks), `afk:verifier` (checks a finished task), `afk:reviewer` (the compliance and quality checks and the critic), `afk:qa` (the hands-on QA check), `afk:research` (codebase research) and `afk:web-research` (web research). Each limits the tools its agent starts with, so a sub-agent loads only what its job needs instead of every tool in your session, which makes it cheaper to start. The planner, reviewer and orchestrator name these types when they spawn agents. If your session predates the plugin update and refuses a type as unknown, the call is made again without it and the agent runs with the default tools.

---

## The Three Phases

### Phase 1: Plan (`/afk:plan`)

Every request gets a **routing verdict**, announced up front: afk classifies the work, then states whether it's taking the **LIGHT** or **FULL** path. A skip phrase like "just write the plan" always elects FULL. FULL proceeds straight into the grill loop with no extra prompt. A LIGHT verdict **executes in-session without a confirmation**: afk announces the verdict and its caveats, tells you the light path is running, and you can interrupt to ask for the full pipeline instead.

**Full path.** A grill loop drives planning: afk builds a dependency-ordered decision tree from the request, then walks it one decision at a time. Each node gets a recommended answer; anything answerable by reading your codebase is looked up, not asked. Nodes that don't meet the escalation rule are decided and recorded in `## Assumptions`; nodes that do are put to you (default mode) or decided and tagged `(assumed, would have escalated)` (autonomous mode). If the `mattpocock-skills:grill-me` skill is installed, afk prefers it for the wording of escalated questions; otherwise it falls back to its own built-in methodology. Either way, the output is a structured plan file at `.afk/plans/{name}.md`.

A **specialized research agent** maps the dependency graph, finds similar implementations in your codebase, identifies naming conventions and test coverage, and flags risk areas. For tasks involving external libraries or APIs, a web research agent gathers official docs and known issues. This research supplies the facts up front, so the grill loop only has to ask about decisions that are genuinely still open.

When the planner rates the plan's risk HIGH, afk runs the **Crucible** automatically — an adversarial pass that stress-tests the finished plan for structural flaws, mismatched model tiers, gaps, and risk. It reads the plan and returns findings; it never edits the plan itself. afk resolves each CRITICAL finding itself and records the resolution in `## Assumptions`. The loop re-runs the Crucible after each round of fixes, capped at 3 passes. On LOW or MEDIUM risk it is skipped.

The plan is the contract. Everything downstream depends on it. Each task carries a `**Depends on**` field (see Execute); a plan with no such field anywhere is a legacy plan and still executes.

**Light path.** For small, unambiguous, contained changes, afk announces the LIGHT verdict under the codename **The Striker** and states the caveats plainly: no plan file gets written; the only artifact is a single line appended to `.afk/light-runs.md` — a LOCAL, gitignored record, not a durable or shared one, and not a substitute for a plan file or a resumable run; the work will not go through `/afk:review`; verification is in-session only, with no independent fresh-session check. If the run is interrupted, it's re-run from scratch, not resumed. The Striker builds a terse goal spec and executes it directly in the same session, using the Workflow engine when present (a minimal execute-then-verify script) and direct in-session execution when it isn't, with the engine choice always announced. There's no afk Model Registry tier resolution here — sub-agents simply inherit the session's model.

### Phase 2: Execute (`/afk:execute`)

Orchestrated task delegation with **real model selection**. The **Workflow engine is the default**: when the Workflow tool is available, afk runs `ruby plugins/afk/scripts/plan_to_args.rb` to turn the plan into engine arguments, then runs `plugins/afk/workflows/execute.js` with no question asked, announcing the engine first. The **Agent-loop** (direct Agent-tool delegation, one task per Agent call) is the fallback when the Workflow tool is absent, `ruby` is missing, or a fresh re-run after a mid-run failure also fails; the fallback is announced. A plan that the parser rejects (exit 1: cycle, unknown dependency, a task depending on its own or a later wave, a dependency on a task that appears later in the file, mixed use of `**Depends on**`) stops the run with the parser's reason; nothing repairs the plan behind your back, because the plan is the contract and the problem is the plan, not the engine. Fix the plan and run again.

**DAG scheduling.** Every task in a newly generated plan has a `**Depends on**` field: a comma-separated list of task IDs from earlier waves, or `none`. The engine starts a task as soon as everything it depends on has passed, so independent tasks overlap across wave boundaries. Tasks whose `**Files**` overlap never run concurrently and run in plan order. A **legacy plan** in which no task has the field falls back to wave barriers: each wave finishes before the next starts. A plan where only some tasks have the field is invalid.

**No bookkeeping agent.** The engine does not spawn an agent to tick checkboxes. The task agent (or the verifier, when it certifies the task) runs `afk plan check --project {root} {plan} {task-id} done`, and a task that fails is recorded by `... failed --reason "{why}"`, which also adds an entry to `.afk/{plan-name}/issues.md`. The command takes a lock, so tasks that finish together cannot overwrite each other's ticks. If the run finishes and a tick did not land, the conductor's reconcile step corrects it from the run result.

**Tiered verification.** Each task agent runs its own acceptance commands and the task's QA scenarios (happy and failure cases), saves each scenario's evidence under `.afk/{plan-name}/evidence/`, ticks its own checkbox with `afk plan check` and self-reports `{status, checks, summary}`. A separate verifier agent runs only when the task is `heavy` or `reasoning` tier, touches a risky path (`.github/`, a `hooks/` directory, a test path), reported no checks, reported a non-zero exit code, or reported anything but `done`. Everything else passes on its self-report. A verifier FAIL triggers a fix agent, and the fix is re-verified. The Agent-loop keeps its orchestrator-run verification of every output.

Either engine sets the `model` parameter to the tier assigned during planning, and each sub-agent runs on the model corresponding to its tier per the afk Model Registry (see `plugins/afk/models.md` for current tier→model mappings), with behavioral guidance tuned to its tier's strengths. The orchestrator never writes code directly. If a task fails, the tier automatically escalates on retry (3 attempts): light → balanced → heavy → reasoning, with reasoning as the ceiling. On fallback, the caller retries with the tier's fallback alias per the registry.

Artifacts land in `.afk/{plan-name}/`.

### Phase 3: Review (`/afk:review`)

Three-check verification:

1. **Plan compliance** — did the implementation match the plan, and was nothing extra built?
2. **Code quality** — does it meet your repo's standards?
3. **Hands-on QA** — does it work when run? This check runs the plan's QA scenarios and each task's acceptance commands itself, and writes its own evidence under `.afk/{plan-name}/evidence/qa/` rather than trusting the task agents' files. It also tries the tasks together and a few edge cases the plan did not list.

Each check runs on the `balanced` tier. **The critic rule:** an adversarial critic on the `reasoning` tier (per the afk Model Registry) runs for a check only when that check returned at least one `blocker` or `major` finding. The critic sees only those findings and confirms, downgrades or rejects each one; `minor` findings pass through untouched. Ends with an explicit **APPROVE** or **REJECT** verdict.

**Severity gate.** Every finding is `blocker`, `major`, or `minor`. A check fails if and only if it has at least one `blocker` or `major` finding, and the verdict is REJECT if and only if any check fails. Minor findings never cause a REJECT; they are listed as follow-ups (and land in the draft PR body when `/afk:go` ships). The gate is applied to the findings themselves, not to the verdict a check or critic returned.

**Workflow review.** When the Workflow tool is present, the checks run through `plugins/afk/workflows/review.js` (check agent, then critic, per check); without it, the reviewer uses the equivalent Agent path.

Both verdicts have an optional, user-gated follow-up — neither runs automatically:

- **On REJECT**, the reviewer offers a blocking, multi-select confirmation gate to fix the selected rejections via sub-agents, then automatically re-runs the **full** three-check review. This fix-then-re-review loop is capped at **3 fix passes** per invocation (the cap in `skills/reviewer/prompts/fix-rejections.md`; the `/afk:go` conductor has its own, lower cap, below); if rejections remain at the cap, they're surfaced so you can decide to proceed anyway or continue manually. Every fix is delegated through the `Agent` tool — the reviewer itself never edits code, not even trivially, subject only to this narrow delegation carve-out. Fix sub-agents' edits go through the normal permission flow, so you may see permission prompts mid-fix-loop, and every fix lands **uncommitted** — committing is still your call. Fix-loop history is logged to `.afk/{plan-name}/review-fixes.md`.
- **On a true APPROVE** (including one reached via the fix loop, but not a "proceed anyway" at the cap), the reviewer offers a separate confirmation gate to generate a detailed manual testing story — a step-by-step walkthrough for a human tester built from the plan's acceptance criteria, its Verification Strategy, and the actual diff. The story is rendered in chat and saved to `.afk/{plan-name}/manual-testing.md`, a **local, gitignored** artifact (`.afk/` is excluded by the repo's `.gitignore`) that won't travel with a branch or PR unless you copy its contents somewhere tracked.

---

## Quick Start

Plan and Execute can share a session, or run in separate ones — either works, since the plan file on disk (re-read from scratch) is the contract Execute follows, not conversational memory. A fresh session between them is still fine, and preferable once you're getting close to the context limit, but it's no longer required. Review is the same: it can run in the same session. Its independence comes from fresh Workflow check agents (and a fresh critic for any check that found a blocker or major issue) that never see the planning or execution history, so it checks the diff against the plan file rather than against what any session remembers deciding. If a REJECT verdict opens the fix-rejections loop, the re-review passes that follow run inside that *same* review session — which necessarily remembers the fixes it just orchestrated — rather than starting fresh again on every pass.

```bash
# Plan
/afk:plan TURT-1234

# Create a feature branch before executing
git checkout -b feat/turt-1234-add-billing-webhook

# Execute (same session as Plan is fine, or a fresh one)
/afk:execute turt-1234-add-billing-webhook

# Review (same session is fine; the checks run as fresh agents)
/afk:review turt-1234-add-billing-webhook
```

The plan name (e.g. `turt-1234-add-billing-webhook`) is what you choose during the Plan phase. It becomes the filename at `.afk/plans/turt-1234-add-billing-webhook.md`.

---

## Session Naming

Each afk phase follows the kebab-case convention **`afk-{phase}-{plan-name}`** for session names (e.g. `afk-plan-turt-1234-add-billing-webhook`, `afk-execute-turt-1234-add-billing-webhook`, `afk-review-turt-1234-add-billing-webhook`). Once the plan name is known, the phase prints a copy-paste **`/rename afk-{phase}-{plan-name}`** tip, and the Plan handoff additionally suggests launching Execute in a pre-named fresh session if you want one, e.g. `claude -n "afk-execute-{plan-name}"`. Review needs no fresh session.

These are only suggestions — **renaming a session is a user action**. Agents cannot rename their own Claude Code session; you have to run the printed command yourself.

---

## Single-Step Orchestration (`/afk:go`)

For complex work that needs the full three-phase pipeline (plan, execute, review), but in a single conducting session with disk-gated phase transitions, use the **Conductor** via `/afk:go`.

### How It Works

The Conductor runs all three phases as one session:

```bash
/afk:go TICKET-123
```

The Conductor:

1. **Plans** (Phase A) — researches your codebase, decides what it can and asks you only escalation-rule decisions (default mode) or nothing (autonomous mode), and produces a plan file at `.afk/{plan-name}/plans/{name}.md`
2. **Executes** (Phase B) — runs the Workflow engine by default (Agent-loop fallback, announced), applies smart model selection, delegates tasks to the appropriate tier, and verifies by tier
3. **Reviews** (Phase C) — makes a WIP commit, then runs three independent checks (compliance, quality, hands-on QA), each as a fresh agent isolated from the session history, with an adversarial critic on the `reasoning` tier for any check that found a blocker or major issue
4. **Ships** — in default mode, on APPROVE, folds the WIP commits into one commit and opens a draft PR. In autonomous mode it stops before shipping (see below)

Pass `--autonomous` to run the whole pipeline with no user stops (see Modes above): `/afk:go --autonomous TICKET-123`.

Phase transitions are **gated on disk artifacts**, not memory:

- Leaving `planning` → `executing` requires the plan file to exist (`.afk/{plan-name}/plans/{name}.md`)
- Leaving `executing` → `reviewing` requires the diff to exist (tracked by git)
- The review verdict is persisted to `.afk/{plan-name}/review-verdict.md`

### WIP Commits, Severity Gate, and Subset Re-Review

Before every review pass the conductor makes a **WIP commit** (`chore(afk): WIP {name} review pass {n}`), because the review diff compares committed branches. The first pass records the pre-review HEAD in `.afk/{name}/wip-base`. The WIP commit stages everything in the working tree except `.afk/`, **including edits that were already uncommitted before afk started**; commit or stash unrelated work first if you don't want it in the PR. Hooks run; `--no-verify` is never used.

The verdict follows the severity gate above. On REJECT the conductor runs up to **2 fix cycles** (the conductor's cap, tracked as `loop_count` in `status.json`; never a third). Each cycle delegates the findings verbatim to fix sub-agents, makes a new WIP commit, and **re-runs only the checks that failed**; passing checks keep their previous result and findings (the subset merge). This differs from standalone `/afk:review`, whose fix loop is capped at 3 passes and re-runs all three checks every time.

### Ship: Commit and Draft PR

On APPROVE the conductor commits without asking. It folds the WIP commits (reset `--soft` to `wip-base`) into one scoped Conventional Commit, using the `commit-style` and `humanize` skills when installed. In **default mode** it then opens a **draft PR** (with `gs branch submit --draft` in a git-spice repo, otherwise `gh pr create --draft`). The PR body carries a summary, the plan's `## Assumptions`, and the review's minor findings as follow-ups. Drafts and their commits publish **without your approval**; it never force-pushes, marks a PR ready, or pushes to the base branch. In **autonomous mode** it stops at the commit: no push, no PR (see Stop before shipping above). Only after that does the session reach `done`. When your description names a master-plan task, the ship step also adds an `Amended <date>` line to that task's entry and checks `docs/decisions.md`. Scratch files stay in `.afk/{name}/`, which no commit stages.

At the fix-loop cap in autonomous mode, the same step runs in **not-approved mode**: it commits the work on the session branch with no push and no PR, and the session ends `failed` with the unresolved blocker and major findings in the report.

### Auto-Saved Learnings

When execution finishes, the Workflow Engine Spec's completion step saves every learning from the session's `learnings.md` that passes the filter to Claude Code's persistent memory **without a prompt**. A candidate must be non-obvious, durable beyond this plan, and not derivable from the repo. This is the single save point: the ship step only reports what was saved and writes no entries of its own. The LIGHT path applies the same filter when it finishes. **Risk:** memory entries are written under your account without review, so a wrong or over-broad entry can mislead later sessions. Review saved entries in your memory directory (`MEMORY.md` and its files) and delete any you don't want. afk never writes to `CLAUDE.md`, settings, or other user configuration.

If a session is interrupted, resume it by name:

```bash
/afk:go session-name
```

The Conductor reads the disk state (plan, status, verdict) and resumes from the recorded phase — it never restarts the interview.

### Launching via `afk new`

For fork-based workflows (parallel sessions, isolated worktrees), use `afk new` to create a dedicated worktree and launch the Conductor within it:

```bash
afk new session-name
```

`afk new` creates a worktree, seeds `.afk/{name}/status.json`, and launches `/afk:go --session session-name` in a new tmux window. The status file persists session identity, phase, loop count, and awaiting-input state across the entire lifecycle.

### Session State and Notifications

Session state lives in `.afk/{plan-name}/status.json` — the contract file consumed by the Conductor, the dashboard, and any future GUI. See [`skills/conductor/status-schema.md`](skills/conductor/status-schema.md) for the full specification.

Notifications fire when:

- **Permission prompt** (any phase) → immediate toast, session marked as awaiting input
- **Idle prompt during planning** → toast after idle delay (the user walked away mid-question); suppressed if planning completes before the delay
- **REJECT verdict** (review completes) → toast, session marked as awaiting input for findings presentation
- **Fix-loop cap** (2 fix cycles exhausted, default mode) → toast, session marked as awaiting input. In autonomous mode there is no toast and no awaiting-input state
- **NEEDS_INPUT relay** (executor asks a question mid-interview, default mode) → toast, session marked as awaiting input

**Rate-limit marker.** When the agent CLI stops because of a rate limit, the `StopFailure` hook (`hooks/on-stop-failure.sh`, registered for the `rate_limit` reason only) writes `.afk/{name}/.rate-limited`, one line `{"at":"<time>"}`, for the matching session. It never touches `status.json`. The next prompt you submit, or the next normal stop, removes the file. The build daemon (`afk daemon`) reads the marker and pauses that session until the usage limit resets. See `status-schema.md` section 10.

Background work (delegated task subagents, background Phase-C review) runs without notifications — the session self-resumes on completion. The dashboard's "awaiting input" indicator pairs with `updated_at` to show age.

### Dashboard

Once a session is running, view all sessions in the repo with:

```bash
afk dash
```

The dashboard shows:

- **Session list** — name, phase, task progress (completed/total), awaiting-input status
- **Waves panel** — the plan's structured tasks with their states
- **Git-spice stack panel** (if applicable) — branching topology, annotated with session ownership
- **Diff stats panel** — lines added/removed vs. the base branch (focused session only)
- **Notepad tails** — last 10 lines of learnings/issues/verdict

**Keybindings**:

- `↑/k`, `↓/j` — move selection
- `enter` — jump to the selected session's tmux window (by window ID, rename-safe)
- `n` — run `afk new` interactively
- `c` — run `afk clean` on the selected session (gated to done/failed phases unless `--force` is passed)
- `q` — quit

### Session Cleanup

Sessions accumulate worktrees and branches. Clean them up with:

```bash
afk clean                  # List all sessions and orphans
afk clean session-name     # Archive and remove the worktree; keep the branch
```

`afk clean` applies the claude-squad pattern: snapshot-commits any dirty changes, archives `.afk/{name}/` to `~/.local/share/afk/archive/`, removes the worktree, and prunes — the branch survives for history. The phase gate (done/failed) prevents cleaning active sessions; pass `--force` to override.

---

## Plan File Format

Plan files are structured markdown documents that capture the full context of a feature: goals, constraints, tasks, acceptance criteria, and file scope.

Every task carries seven subsections: `**What to do**`, `**Must NOT do**`, `**Files**`, `**Depends on**`, `**Acceptance Criteria**`, `**QA Scenarios**` and `**Model**`. `**Files**` must name exact file paths. Each QA scenario gives a tool, a case (`happy` or `failure`), steps, the expected result and an evidence path under `.afk/{plan-name}/evidence/`. Every task needs at least one failure scenario (bad input, a missing file, an error path), or a line `No failure case: {reason}` for a task with nothing that can fail, such as a docs-only change; `N/A` is not allowed for Files or QA Scenarios, and the plan parser rejects a plan that breaks these rules.

The planner also keeps two project documents. It reads `CONTEXT.md` (the project's glossary) during research and adds each new domain term as one line, and when your description names a master-plan task it writes that task's decisions into `docs/decisions.md`. Those are the only files it writes outside `.afk/`.

See [`skills/planner/plan-format.md`](skills/planner/plan-format.md) for the full spec.

---

## Customizing for Your Repo

### CLAUDE.md

afk discovers your test, build, and lint commands from `CLAUDE.md`. Add a section like:

```markdown
## Commands

- **Test**: `make test`
- **Build**: `bin/build`
- **Lint**: `bin/rubocop`
- **Type check**: `bin/sorbet`
```

afk reads this during Execute and Review to run the right commands after making changes.

### `.claude/rules/`

Drop markdown files in `.claude/rules/` to encode repo-specific conventions. afk loads these automatically. Good candidates:

- Naming conventions
- Architecture patterns
- What not to touch
- PR checklist items

---

## Key Principles

**Dual-mode routing.** Every request gets an explicit verdict — LIGHT or FULL — announced before anything else happens. FULL is the pipeline described above and proceeds with no extra prompt. LIGHT is a fast, terse path for small, unambiguous, contained changes that executes in-session without a confirmation (you can interrupt to ask for the full pipeline); it skips the plan file and `/afk:review`, verifies in-session only, and leaves just one line in `.afk/light-runs.md` — a local, gitignored breadcrumb, not a durable or shared artifact.

**Phase isolation.** The plan file is the handoff between phases, re-read from disk as the contract rather than recalled from memory. Plan, Execute, and Review may share a session — a fresh one is still fine, and preferable near the context limit. Review's independence comes from its check and critic agents, which start fresh with no memory of the previous phases and never see execution history, notepads, or learnings; that keeps Review's compliance check honest. Within a standalone `/afk:review` invocation, the fix-rejections loop's re-review passes run inside that same review session, which necessarily remembers the fixes it just orchestrated.

**Plan as contract.** The Execute phase cannot deviate from the plan. If something unexpected comes up, stop and re-plan. The Review phase checks compliance against the plan, not against intent.

**Trust but verify.** Execute delegates to sub-agents and verifies their output by tier. Review independently checks the full diff. Two layers of verification before anything ships.

**Decide, don't ask.** afk answers what it can from the repo and records each decision in the plan's `## Assumptions`; it asks only when the escalation rule says a wrong guess is costly.

**Orchestrator never codes.** The Execute orchestrator reads, delegates, and verifies. It doesn't write code itself. This keeps the orchestrator's context clean and makes failures easier to diagnose.

**Smart model selection.** Tasks are assigned to one of four tiers (light, balanced, heavy, reasoning) based on complexity and reasoning depth. The planner decides; the orchestrator executes with the actual model for each tier per the afk Model Registry, using the Workflow engine by default, with the Agent-loop when the tool is absent. Escalation is automatic. (The light path skips tier resolution entirely — its sub-agents just inherit the session's model.)

**Stack-agnostic.** afk doesn't assume a language or framework. It reads your `CLAUDE.md` to discover commands and adapts accordingly.

---

## Tests and Evals

### Parser and engine tests

Run from the repo root:

```bash
ruby plugins/afk/scripts/test/plan_to_args_test.rb       # plan_to_args.rb parser: DAG, legacy waves, QA scenarios and the failure-case waiver, errors
node --test plugins/afk/workflows/test/*.test.mjs        # execute.js and review.js on a virtual clock
for t in plugins/afk/hooks/*.test.sh; do bash "$t"; done # hook scripts
```

`node --test plugins/afk/workflows/test/` (directory form) works only on Node 20; on Node 22+ use the glob form above. CI runs the node tests in `.github/workflows/bin-tests.yml`.

### Evals

`plugins/afk/evals/planner-autonomy/` holds seven cases that check the planner's decide-and-escalate behavior (what it asks, what it decides, what the plan records). They cost real money (about $5 for one run of all seven). Run:

```bash
ANTHROPIC_MODEL=haiku claude plugin eval plugins/afk --scaffold --no-publish --trust-plugin --judge-model sonnet --allow-tools Write Edit --tag planner-autonomy --max-cost-usd 15
```

Add `--ablation none --runs 1` while iterating. The runner does not offer `AskUserQuestion`, so the two cases that expect a question (`intent-fork-asks`, `destructive-escalates`) judge the transcript for a question put to the user instead of counting tool calls. See [`evals/README.md`](evals/README.md) for the case list, the latest recorded run, and caveats.

---

## Injection Size Guard

afk commands use ``!`cat "${CLAUDE_PLUGIN_ROOT}/..."`` directives to inject prompt files into the Claude Code harness context at command load time. The harness caps each injection at approximately **30,000 characters**. To provide a safety margin, a size check enforces a **28,000-character limit** per referenced file (measured in characters via `wc -m`, matching the harness cap unit — not bytes).

### CI gate (authoritative)

The check runs as a step in the `validate` job of `.github/workflows/validate-plugins.yml`. Any PR that introduces or grows a referenced file past 28,000 chars will fail CI before merge.

### Local pre-commit hook (best-effort)

A `pre-commit` stage hook is defined in `.pre-commit-config.yaml`. The standard `pre-commit install` that `CONTRIBUTING.md` prescribes installs the `pre-commit` stage by default, so this hook runs on every local commit for any contributor who followed that setup (`pre-commit install --hook-type pre-commit` is the equivalent explicit form). **CI is the authoritative gate** — the local hook is an immediate-feedback safety net, and contributors who never ran `pre-commit install` simply won't have it.

### Running the check manually

```bash
bash plugins/afk/scripts/check-cat-sizes.sh
```

The script scans every `plugins/*/skills/*/SKILL.md` file for ``!`cat`` directives, resolves `${CLAUDE_PLUGIN_ROOT}` to the plugin directory, and reports pass/fail per file. Run it from any directory — it always resolves paths relative to the repo root.

---

## Model Selection

afk uses four model tiers to balance cost, speed, and reasoning depth across tasks. The **afk Model Registry** (`plugins/afk/models.md`) is the single source of truth for tier→model mappings. All three afk skills (plan, execute, review) load this registry into context to ensure consistent tier resolution.

> **Note:** `plugins/afk/models.md` IS the afk Model Registry. When the skills or this README refer to "the afk Model Registry," they mean that file. Updating a tier's model alias there propagates immediately to all three afk phases.

### Task Tiers

- **Light** — Pattern-following, zero judgment calls. Efficient and fast. See the registry for the current model alias.
- **Balanced** — Judgment required; the default. Most tasks fit here. See the registry for the current model alias.
- **Heavy** — Demanding execution where the path is discoverable. Complex implementations, high-stakes changes. See the registry for the current model alias.
- **Reasoning** — Novel reasoning, deep synthesis, or hard architectural thinking. Escalation target from `heavy`, or directly assigned for novel problems. See the registry for the current model alias.

### How It Works

Model selection is automatic and real — each sub-agent runs on the actual model for its tier via the Agent tool's `model` parameter. The registry currently maps light → haiku, balanced → sonnet, heavy → opus (fallback sonnet), and reasoning → fable (fallback opus), but only `models.md` is authoritative.

During Phase 1 (Plan), the planner grills the user, reads the code, and assigns a tier to each task based on complexity, blast radius, and risk. During Phase 2 (Execute), the orchestrator delegates each task to the tier assigned in the plan, with tier-specific behavioral guidance. No manual model selection is needed.

Each of the reviewer's three checks (Phase 3: compliance, quality, hands-on QA) runs on the `balanced` tier. A check that finds a blocker or major issue gets an adversarial critic on the `reasoning` tier to challenge those findings, so a finding that would trigger a fix has had a skeptical second opinion.

### Escalation & Fallback

When a task fails, afk automatically escalates the tier on retry: light → balanced → heavy → reasoning. Reasoning is the ceiling; a reasoning task that exhausts retries is marked failed. Adopting a future model is a one-line registry edit: update the model alias for the tier, and all three afk skills (and any tool that loads the registry) inherit the change immediately. Older Claude Code versions without the `fable` alias will fall back per the registry's fallback column, with the caller retrying once (a note in the sub-agent's prompt cannot handle rejection, which happens before the sub-agent runs).

---

## Version and Scope

This is **v1** of the afk plugin and CLI. The following are out of scope for v1:

- **Codex (VizCode) mirroring**: The `.codex-plugin/plugin.json` manifest exists for version parity only. Codex-specific skills, commands, and hooks are not implemented for v1; only the version number is kept in sync with the Claude Code plugin.

Future versions will expand capability across both platforms as demand grows and the tooling stabilizes.
