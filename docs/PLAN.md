# afk — Master Plan

> Say "go", walk away. afk is an always-on runner that builds a whole application from a master plan:
> it picks the next ready task, runs a coding agent through plan → build → review, ships on green CI,
> paces itself against the agent subscription's usage limits, and only bothers its owner for things
> only the owner can do.

**How to use this document.** This is the master plan. Each task (`T<milestone>.<n>`) is sized for one
autonomous plan → execute → review cycle. Read **§1–§7 in full** before planning any task: they are the
shared contract every task builds against. Never start a task whose dependencies are not merged.

**How afk gets built (bootstrap).** As autonomously as possible, on the VM (§2, T0.0):

1. **Until T0.2 is merged**, tasks run as forge autonomous sessions, the only autonomous path that
   exists today: `forge new --autonomous --description "Implement T0.1 from docs/PLAN.md" t0-1`.
   forge's conductor opens a draft PR; add to the description: "then mark the PR ready, wait for CI with
   `gh pr checks --watch`, and merge it if green".
2. **From T0.2 until T2.2 is merged**, tasks run with afk's own plugin:
   `afk new --autonomous --description "Implement T1.1 from docs/PLAN.md" t1-1`, same ship instruction.
   The description must ask to open the pull request too ("push the branch and open a draft PR, then
   mark it ready, wait for CI…"), because from T0.2 the autonomous conductor stops at reviewed commits
   and no longer pushes or opens one.
3. **Once T2.2 is merged**, afk builds the rest of itself: `afk daemon` runs on this repo and takes the
   remaining tasks from this plan.

---

## 1. Context

### Where this comes from

Two existing systems by the same owner are being merged and replaced:

- **forge** (`~/code/forge-cli`): a Go binary. Detached per-session PTY hosts that outlive every UI, a
  Bubble Tea dashboard, worktree-per-session, three agent providers (Claude Code, Codex, OpenCode), an
  autonomous mode, and a plugin whose workflow scripts schedule tasks by dependency.
- **shipit** (`~/.claude/plugins/marketplaces/flow/plugins/shipit`, v2.0.2): a Claude Code plugin. One
  `/shipit:go` session per task. It built 21 tasks of `~/code/nebari` with no task re-planned or
  restarted.

### What the nebari build taught us (measured from 65 sessions, 317 commits, 2026-09-25 → 10-10)

- The agent loop works. The bottleneck was the human and the laptop: about 31 hours of agent time took
  15 calendar days. Ship gates sat 6–20 hours each; the laptop slept mid-response 17 times.
- Of 241 questions asked, the owner took the recommended option 205 times (85%). "Modify plan" was never
  chosen at a plan gate and a ship gate was never refused. About 20 interventions genuinely blocked work
  (deploy, credentials, an overnight seed run, smoke checks).
- Review is load-bearing: 8 of 21 reviews rejected on at least one dimension and caught real bugs.
- The master plan is a living document: six tasks were added mid-build, one was split, and the plan was
  touched in 31 commits. "Next task" must be re-derived every cycle.
- No subscription usage limit was hit at human pace. Neither forge nor shipit handles one.

### Where the tokens went (measured from the same build: 543 transcripts, 14,523 model calls)

"Cost" below weights token types by API price ratios (§4.7). How a subscription weights them is not
published, so shares are approximate.

- **92% of cost is context being written and re-read**; generated output is under 8%. Cost is driven
  by the number of model calls times the size of the context on each call.
- By role: implementation agents 49%, review and verification agents 18%, orchestrating main loops
  18%, planning and research 9%, post-review fixes and shipping 5%.
- **Running execution as a workflow script neither saved nor cost anything measurable** (0.74M versus
  0.84M per work item, inside task-to-task variation).
- **Idle gaps cost about 14%.** An agent that waits more than five minutes on one command loses its
  cached context and re-sends all of it. It happened 265 times, mostly behind a long test run or a
  sleep-and-poll loop.
- **Agent start-up cost up to 13%.** Each implementation or review agent began with about 45k tokens
  of context before doing anything (17k for lightweight research agents). What the extra 28k contains
  was not determined.
- **Some review never fired.** The scope reviewer rejected nothing in 25 runs; per-item verifiers
  passed 50 of 50; after any rejection all four reviewers were re-run. Code-quality review was the
  cheapest source of real fixes, and hands-on QA the most expensive but the one that found functional
  defects.
- Agents spent a median of seven calls reading before their first edit (about 6% of the build).
- Waking the orchestrator for every finished item cost one task 122 main-loop calls where a similar
  task used 13.

### What afk must do

1. **Run unattended** on an always-on machine: pick the next ready task from a master plan, run it, ship
   it, repeat.
2. **Replace approval gates with policy.** Take the recommended option and record it; never wait on a
   rubber stamp.
3. **Park, don't block.** Anything only the owner can do becomes an inbox item with a push notification;
   the loop moves on to other ready work where it can.
4. **Govern usage.** Read the subscription's usage and reset times, keep a reserve for the owner, pause
   before the limit and resume after the reset.
5. **Recover by itself** from dead or wedged agent sessions.
6. **Be watchable**: the terminal dashboard (primary), plus a small web page for status and answering
   inbox items from a phone.
7. **Move toward determinism.** Orchestration lives in tested code. Every decision an agent still makes
   in the loop is logged so it can later be promoted to code or policy.

### First real job

Finish nebari: 17 tasks remain (M4–M9 of `~/code/nebari/docs/PLAN.md`).

---

## 2. Decisions

| Decision | Value |
|---|---|
| Name | **afk**. One binary, `afk`. The always-on process is `afk daemon`, run as a systemd unit named `afkd`. |
| Repo | `~/code/afk`, private at first (`github.com/dphaener/afk`), **clean history**: forge's code is copied in, not its git log. Non-obvious behaviour that the old history explained is documented in code comments or `docs/`. |
| Audience | Eventually open source, for casual developers building things for themselves (tinkerers, hobbyists). So: single binary, short setup, plain-language docs, and nothing about the owner's homelab baked into code. Homelab specifics live in config and runbooks. |
| Language | Go (version per forge's `go.mod`), charm.land v2 modules for the TUI. Carried over from forge. |
| Base | **forge's Go code and forge's plugin are the base**, renamed. Reason: the binary's parsers, dashboard and tests already implement the forge plugin's on-disk formats, and its hooks are portable shell. shipit's proven strengths are grafted in (T0.2). shipit's Claude Code mods (write-guard, continuation enforcer, band) are **not** carried: they are Claude-only, and afk enforces those things from the daemon instead. |
| No vendor lock-in | The agent CLI stays swappable. Everything provider-specific sits behind `internal/provider`: launch, resume, preflight (exist today) plus usage probe and limit signal (new). v1 implements the new parts for Claude Code only; Codex and OpenCode keep working as session providers with "usage unknown". Nothing outside `internal/provider` may name a provider. |
| Machine | A dedicated always-on **Linux VM** in the owner's homelab, from the start (T0.0). afk itself is built there. forge already cross-compiles for linux/amd64 and linux/arm64 and its CI runs on Ubuntu. The VM runs **Ubuntu Server 26.04 LTS**, and every project afk builds names that release in its CI (`runs-on: ubuntu-26.04`) so a pass on the VM predicts a pass in CI. |
| Permissions | Agents run with permissions bypassed **on the VM only**; the VM is the sandbox. No production secrets on it. A dedicated API key with a low spend cap covers live calls, which are allowed for smoke checks and recording fixtures and must be logged. |
| Ship policy | Every task runs on a feature branch. CI runs in GitHub. **Green CI → merge to main, fully unattended.** Merging deploys (for projects that deploy), and it should. A task is done when the deploy is healthy, not when it merges. |
| Who ships | **The daemon, not the agent.** A session ends with reviewed commits on its branch. Push, pull request, CI wait, merge and deploy check are code (§5.4). |
| Merge gate | The daemon polls the PR's checks itself and merges. It does not depend on GitHub branch protection or auto-merge, which private repos on a free plan may not have. Where available, branch protection is recommended as a second lock. |
| Human gates | None in autonomous mode. Plan approval and ship sign-off are replaced by policy (§5.5). Review stays, including hands-on QA. |
| Usage reserve | **10% kept for the owner on every window** (5-hour session, weekly, per-model weekly): hard pause at 90% used. New tasks stop launching earlier, at 75% of the session window (tunable). Extra usage (paid overage) must be **off**; the daemon refuses to run if it is on. |
| Pacing | None beyond the reserve. The runner may spend the week's allowance early and then idle until the reset. |
| Token economy | **For an always-on runner, tokens are speed**: once it sits at the usage limit, tasks per week is set by tokens per task. So the task cycle is built lean (§5.7) and every task's token use is recorded by phase and role (§4.7), so further cuts are measured, not argued. Review that finds real defects stays. |
| Permission mode | **Passed explicitly at launch**, not left to a settings file. The daemon starts each agent in the provider's unattended mode and refuses to launch if the machine isn't set up for it. (On the VM, a user-settings default that had drifted to another mode silently changed how a task ran.) |
| Concurrency | **One task at a time** in v1. Usage, not wall-clock, is the limit, and sequential tasks never conflict. |
| Notifications | ntfy (plain HTTP POST) for push. Desktop toasts from forge stay as a macOS-only extra. |
| Web surface | A small server-rendered page served by `afk daemon` (Go `net/http` + `html/template`, no JS build step). Loopback by default; protected by a shared token; meant to sit behind Tailscale or the owner's reverse proxy. |
| Config format | TOML, hand-editable: `afk.toml` committed in each project, `~/.config/afk/config.toml` per machine. |
| Rules | `~/code/.claude/rules/*.md` are Rails rules and **do not apply** to this repo. T0.1 writes afk's own `CLAUDE.md`. |

---

## 3. Architecture

```
            owner                                         GitHub            deployed app
   ┌──────────┴───────────┐                                 ▲                    ▲
   │ ssh: afk dash (TUI)  │ phone: web page / ntfy          │ push, PR, checks   │ health check
   └──────────┬───────────┘        ▲          ▲             │ merge              │
              │ files              │ HTTP     │ POST        │                    │
┌─────────────┼────────────────────┼──────────┼─────────────┼────────────────────┼────────┐
│  VM         ▼                    │          │             │                    │        │
│   ┌──────────────────────── afk daemon (one per machine) ─┴────────────────────┴─────┐  │
│   │  build loop (§5)   governor (§6)   shipper (§5.4)   inbox (§4.3)   web   notify  │  │
│   └───────┬──────────────────────────────────────────────────────────────────────────┘  │
│           │ spawns, watches, restarts, prompts                                          │
│           ▼                                                                             │
│   ┌── session host (one per task; outlives daemon and UIs) ──┐                          │
│   │  PTY ── agent CLI ("/afk:go --session t4-3 --autonomous")│   worktree: one branch   │
│   └──────────────────────────────────────────────────────────┘   per task               │
│           │ writes                                                                      │
│           ▼                                                                             │
│   .afk/  status.json · host.json · plans · verdict · inbox · ledger · decision log      │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

Rules that hold everywhere (inherited from forge, do not weaken):

- **Processes never share memory.** State crosses process boundaries only through files, each written
  by re-read-merge plus a same-directory atomic rename. Readers treat a missing or unparseable file as
  stale, never as an error.
- **A session host outlives everything.** Quitting the dashboard, restarting the daemon or dropping an
  SSH connection cannot end a session. The daemon must be restartable at any moment and pick up where
  the files say it was.
- **A wedged client must never stall the agent.** Bounded queues, write timeouts, drop the slow client.
- **Discovery never blocks.** No dialing, no network. The only non-file check is signal-0 liveness.
- **Pure decision functions.** Anything that branches on the environment takes the environment as
  parameters, so the whole matrix is table-tested. The governor (§6.3), next-task selection (§5.2) and
  the ship state machine (§5.4) are all written this way.

### Three layers

| Layer | Driven by | Source |
|---|---|---|
| **Runtime**: session hosts, PTYs, worktrees, liveness, restart | Code | forge `internal/host`, `session`, `provider`, `discover` |
| **Build loop**: next task, launch, watch, ship, park, pace | Code (new) | `internal/daemon`, `ledger`, `ship`, `governor`, `inbox` |
| **Task cycle**: plan → execute → review → fix | Agent, following the plugin; task scheduling inside a cycle is already script | `plugins/afk` |

---

## 4. State on disk

`.afk/` in the project's **main checkout** is the daemon's state. It is gitignored. Per-session state
keeps forge's layout, renamed.

### 4.1 Inherited (rename only; schemas unchanged in v1)

| File | Writer | Notes |
|---|---|---|
| `.afk/{session}/status.json` | conductor; hooks write only `awaiting_input` | The schema document `plugins/afk/skills/conductor/status-schema.md` stays binding. Don't add fields without changing it first. |
| `.afk/{session}/host.json`, `host.log` | session host | Liveness and discovery. Independent of `status.json` by design. |
| `.afk/plans/{session}.md` | planner | The per-task work plan (waves, `(W1.T2)` ids, `**Depends on**`). Not the master plan. |
| `.afk/{session}/review-verdict.md` | conductor | Severity-gated verdict. |
| `.afk/{session}/learnings.md`, `decisions.md`, `issues.md` | agents, append-only | |

Added by T0.2, also per session and gitignored:

- `.afk/{session}/evidence/` — QA evidence. Task agents save each scenario's output at the path the
  work plan names; the hands-on QA check writes its own under `evidence/qa/`.
- `.afk/{session}/.rate-limited` — one line `{"at":"<RFC 3339 UTC>"}`, written by the `StopFailure`
  hook on a rate limit and removed by the next prompt or normal stop. It never touches `status.json`.

### 4.2 Ledger — `.afk/build/ledger.json` (new; daemon is the only writer)

One entry per master-plan task id.

| Field | Meaning |
|---|---|
| `id` | `T4.3` |
| `status` | `pending` · `running` · `shipping` · `parked` · `done` · `failed` · `superseded` |
| `session`, `branch`, `pr` | Set as they come to exist. |
| `attempts` | Sessions started for this task. |
| `parked_on` | Inbox item ids this task waits for. |
| `started_at`, `finished_at`, `merged_sha` | |

`superseded` is for a task kept in the plan only as history (nebari's T4.2, split into T4.2a/b). Such
an entry carries `replaced_by: ["T4.2a", "T4.2b"]`, because other tasks may still name the old id as a
dependency (nebari's T6.1 depends on T4.2). A task absent from the ledger is `pending`. `afk ledger set <id> <status>` edits it by hand; that is how an
existing project (nebari's 21 finished tasks) is seeded.

File shape: `{"tasks": [ … ]}`, entries sorted by id, `pr` a pull request number, times RFC 3339 UTC,
empty fields omitted. Unknown keys and unknown statuses are rejected. A missing file means every task
is pending; a damaged file is an error and is never read as empty. Writers take the lock file
`.afk/build/.ledger.lock`.

### 4.3 Inbox — `.afk/inbox/{id}.json` (new)

| Field | Meaning |
|---|---|
| `id`, `created_at`, `task`, `session` | |
| `kind` | `question` · `env_var` · `external_action` · `verification` · `failure` · `notice` |
| `blocking` | `true` parks the task; `false` is informational or verification debt. |
| `title`, `body` | Plain language. |
| `options`, `recommended` | For `question`. |
| `assumed` | What the agent went ahead with, if it did not wait. |
| `answer`, `answered_at`, `answered_via` | `web` · `cli` · `tui` |

Agents never write these files directly. They call `afk inbox add …`, which validates and writes
atomically. The daemon also adds items (CI failed twice, deploy unhealthy, login expiring).

Amended 2026-10-11 (T1.2 done): every key is always present (`""`, `[]` or `false` when unset), times are RFC 3339 UTC strings, the id is six characters, and only `{id}.json` files in the folder are items.

### 4.4 Decision log — `.afk/log/decisions.jsonl` (new; append-only)

One line per decision anyone or anything made in the loop: `{at, task, session, kind, question,
options, chosen, chosen_by: policy|agent|owner, rule}`. Kinds include planner questions, review
verdicts, fix-loop outcomes, governor actions, ship results. This is the raw material for §9's
"promote to code" work; it must exist from the first daemon run.

### 4.5 Usage snapshot — `~/.local/state/afk/usage.json` (new; per machine, not per project)

Written by the governor: `{at, provider, windows: [{kind, scope, used_pct, resets_at}], overage_enabled,
source}`. `scope` is the model's name on a per-model weekly window and empty otherwise. The path is
`$XDG_STATE_HOME/afk/usage.json` when `XDG_STATE_HOME` is set to an absolute path, otherwise
`~/.local/state/afk/usage.json`. The dashboard and web page read it; nobody else probes.

### 4.6 Project config — `afk.toml` (committed)

```toml
plan = "docs/PLAN.md"

[ship]
base = "main"
merge_method = "merge"          # merge | squash | rebase
ci_reruns = 1                   # automatic re-runs before the failure goes back to the session
ci_fix_cycles = 2               # times a red CI is handed back before the task parks
deploys = true
health_url = ""                 # polled after merge when deploys = true
deploy_timeout = "15m"
required_env_pattern = ""       # regex; a diff adding a match parks the task before merge (§5.4)

[policy]
notes = """
Free text appended to the autonomous policy the agent reads. Example:
Live API calls are allowed for smoke checks and recording fixtures. Log each one.
"""

[notify]
ntfy_url = ""                   # set this in afk.local.toml, not here (see below)
```

**Private values go in `afk.local.toml`** (same folder, same keys, never committed). Amended
2026-10-11: an ntfy topic name works as a password, since anyone who knows it can read and post to
it, so it must not sit in a committed file of a project that may be public. The loader reads
`afk.toml`, then lays `afk.local.toml` over it key by key; either file may be missing. `afk.toml` is
committed and `afk.local.toml` is gitignored. Until T2.1 lands this, `afk.toml` itself is gitignored
in this repo as a stopgap.

Machine config `~/.config/afk/config.toml`: projects the daemon runs, governor thresholds (§6.3), web
bind address and token, provider choice.

### 4.7 Token ledger — `.afk/log/tokens.jsonl` (new; append-only)

One line per task per phase per role, written when a task ends: `{at, task, session, phase:
plan|execute|review|fix|ship, role, model, calls, input, output, cache_write, cache_read, weighted}`.
Roles are the plugin's own (conductor, planner, research, task agent, verifier, each review check,
critic). The numbers come from the provider's session transcripts, read through `internal/provider`.

- **Weighted cost** = `input + 5×output + 1.25×cache_write + 0.1×cache_read`, in input-token
  equivalents. It is a proxy from API price ratios; keep the raw fields so it can be recomputed.
- **Counting rule (Claude Code transcripts):** the same message id appears on several lines (streaming
  chunks, forked transcripts). Count each id once, taking the maximum of each usage field.
- **Baseline to beat:** the nebari build averaged about 16M weighted per master-plan task.

---

## 5. The build loop

This is the core of afk. It is plain Go, heavily table-tested, and lives in `internal/daemon` with its
decisions in pure functions.

### 5.1 The master-plan contract

afk reads a project's master plan and needs only two things from it. The nebari plan must parse
**unchanged**.

- A task is a heading `#### T<milestone>.<n>[letter] — Title` (e.g. `#### T4.2a — The task engine`).
  Accept an em dash, en dash or hyphen.
- Its dependencies are the first line under it matching `- **Depends on:** …`: a comma-separated list
  of task ids, or `—` / `None` for none. Text after the ids on that line is ignored.

Everything else in the plan is for the agent. The plan is **re-read on every cycle**, because tasks get
added and split while the build runs. A dependency naming an unknown task, or a cycle, is a `failure`
inbox item and the loop stops for that project.

### 5.2 Next task (pure function)

Inputs: parsed tasks, ledger, open inbox items. Output: one task id, or a reason there is none.

- Ready = `pending`, with every dependency `done`. A `superseded` dependency is satisfied when every
  task in its `replaced_by` is `done`; a `failed` or `parked` dependency is not satisfied.
- Among ready tasks, plan order wins.
- A `parked` task whose blocking items are all answered becomes ready again and goes first.
- Nothing ready: report why (`all done` · `waiting on owner` with the item ids · `blocked by failed
  T…`). "Waiting on owner" and "blocked" each raise one notification, not one per tick.

### 5.3 One task

1. Governor says launching is allowed (§6.3), else wait.
2. Create the session: branch `afk/t4-3`, its worktree, seeded `status.json`, and a description of
   `Implement T4.3 from docs/PLAN.md`. Ledger → `running`.
3. Spawn the detached session host running the provider's CLI with the autonomous conductor, **in the
   provider's unattended permission mode, named on the command line** (Claude Code:
   `--permission-mode bypassPermissions`). Before the first launch, the provider's preflight checks
   the machine can run that mode without a dialog (Claude Code: the bypass warning has been accepted
   in user settings, and the user is not root); if not, a blocking `failure` item says what to do and
   nothing launches.
4. Watch `status.json` and `host.json` on a ticker (files only):
   - `phase: done` → ship (§5.4).
   - `phase: failed` → ledger `failed`, a `failure` inbox item with the verdict, move on.
   - A blocking inbox item for this task → ledger `parked`. The session is left alive but idle.
   - Host dead or wedged → recover (§5.6).
   - `awaiting_input` set for longer than `stall_after` (default 10 minutes) → `failure` item
     "session is asking something at the terminal". Under bypassed permissions this should not happen;
     when it does, a human needs to look.
5. Record every transition in the decision log.

### 5.4 Ship (state machine in code)

```
committed ──▶ pushed ──▶ pr_open ──▶ ci_green ──▶ merged ──▶ deploy_healthy ──▶ done
                            │ red                               │ unhealthy / timeout
                            ▼                                   ▼
                     rerun (≤ ci_reruns)                 STOP THE LINE
                            │ still red
                            ▼
                  hand back to session (≤ ci_fix_cycles) ──▶ park
```

- **Pre-merge check:** if the branch's diff adds a match for `ship.required_env_pattern` (for nebari:
  a new `${VAR:?}` in `docker-compose.coolify.yml`), park with an `env_var` item naming the variable.
  Merging would break the deploy.
- **CI:** poll the PR's checks through `gh`. Red → re-run failed jobs up to `ci_reruns`. Still red →
  send the failing job names and log tail to the session as a new prompt and return to step 4 of §5.3.
  After `ci_fix_cycles` hand-backs → park with a `failure` item.
- **Merge:** `ship.merge_method`, then delete the remote branch.
- **Deploy:** when `deploys = true`, poll `health_url` until healthy or `deploy_timeout`. Unhealthy →
  **stop the line**: no further task launches for this project, a `failure` item, a high-priority
  notification. The owner clears it with `afk resume <project>`.
- **After `done`:** ledger `done`, clean the session (worktree removed, state archived), log.
- Each state is persisted in the ledger before the action that leaves it, so a daemon restart resumes
  mid-ship without repeating a push or a merge.

### 5.5 Autonomous policy (what replaces the human)

The conductor in autonomous mode follows these, in this order of precedence:

1. **Never wait on a rubber stamp.** No plan-approval gate, no ship sign-off, no "shall I proceed".
2. **Take the recommended option** on any question the planner would have asked, and record it under
   `## Assumptions` in the work plan and via `afk inbox add --kind question --no-block --assumed …`.
3. **Park instead of guessing** when the step is irreversible or outside the machine: a missing
   credential or environment variable, a third-party dashboard, spending real money beyond the logged
   allowance in `policy.notes`, deleting data, anything that needs a physical device. Use
   `afk inbox add --kind … --block`, then stop the turn.
4. **Fix every confirmed review finding** of blocker or major severity, then re-review. At the
   fix-loop cap, end `failed`.
5. **Verification a machine can't do** (a check on a real phone) never blocks: add a `verification`
   item and carry on.
6. **Unsolicited scope ideas** go to the inbox as `notice`, never into the diff.
7. Project-specific lines from `afk.toml [policy] notes` are appended last and win over 2–6.

### 5.6 Recovery

- **Liveness without a client.** forge probes for a wedged agent only while a client is attached. The
  daemon needs it always: either the host probes on its own schedule, or the daemon holds the client
  slot. T2.3 decides; a human attaching must still be able to take the session.
- **Restart ladder** (exists in forge's host): SIGINT → SIGTERM → SIGKILL, then the provider's resume
  argv with the session id from `status.json`. The daemon triggers it automatically, at most
  `max_restarts` (default 3) per task, with backoff; then the task parks with a `failure` item.
- **Host gone entirely** (VM reboot): relaunch the host with the resume argv.
- **Daemon restart:** reconcile ledger against sessions on disk; adopt running sessions, never start a
  second one for a task that has one.

### 5.7 A lean task cycle

Rules for the plugin, each tied to a measurement in §1. They apply in every mode, not only autonomous.

1. **No long waits inside an agent.** A task agent runs the focused tests for what it touched, never
   the whole suite, and never sleeps or polls. No single command should run longer than about four
   minutes. The full suite runs once per cycle, in the hands-on QA check, and again in CI.
2. **Three review checks, not four:** compliance, code quality, hands-on QA. "Did the change stay in
   scope" is one criterion inside compliance, not its own agent.
3. **A critic runs only on a finding that would trigger a fix** (blocker or major), to confirm it
   before a fix loop starts. A check that passes gets no critic. (Judgement, not measurement: the
   nebari build had no critics. Revisit with the token ledger.)
4. **Re-review only the check that failed**, after a fix.
5. **Verification stays tiered.** A separate verifier runs for risky items only, as forge does today;
   never one per item by default.
6. **No agent does a script's job.** Ticking a work-plan checkbox is a command (`afk plan check`), not
   a model call.
7. **The conductor sleeps while work runs.** Between launching the execute or review workflow and its
   completion, the conductor makes no model call.
8. **Work-plan tasks carry their references.** Each names exact files, and line ranges where it helps,
   so a task agent starts editing sooner. The agent still reads what it edits.
9. **Research is opt-in in autonomous mode.** With a master-plan task as input, web research runs only
   when the task names an external API or library the repo doesn't already use.

---

## 6. Usage governor

### 6.1 Provider interface (additions to `internal/provider`)

```go
// Usage reports how much of each of the provider's usage windows is spent.
// ErrUsageUnsupported means this provider has no way to tell;
// ErrNoUsageReport means the probe ran but its output held no usable report.
// UsageDeps carries the command runner and the clock, as the provider's other
// calls that reach outside do.
Usage(ctx context.Context, d UsageDeps) (UsageSnapshot, error)

type UsageSnapshot struct {
    At             time.Time
    Windows        []Window      // Kind: "session" | "weekly" | "weekly_model" | "unknown"; Scope; UsedPct 0–100; ResetsAt
    OverageEnabled bool
}
```

### 6.2 Claude Code implementation

Verified on Claude Code 2.1.296 with a `/login` credential on a Max plan (2026-10-10); **re-verify by
running it before writing the parser**, and keep the parser tolerant: Anthropic documents this shape as
experimental.

- **Probe:** `claude -p "/usage" --output-format stream-json --verbose`. It costs no model usage
  (`num_turns: 0`). The stream contains a `usage_report` whose `rate_limits.limits[]` rows carry `kind`
  (`session`, `weekly_all`, `weekly_scoped`), `percent` (0–100) and `resets_at` (ISO 8601), plus
  `extra_usage.is_enabled`. `--output-format json` does **not** include it.
- **Credential:** the report is only attached for a real `/login` credential. A `claude setup-token`
  token (inference scope only) does not qualify, so the VM needs an interactive login, which expires.
  `claude auth status` prints JSON and exits 1 when logged out.
- **Don't poll tightly.** The backing request is itself rate limited. Probe at decision points and at
  most every `probe_interval` (default 5 minutes) while a session runs; reuse the snapshot otherwise.
  `rate_limits` may be null.
- **Units differ between Claude Code surfaces** (0–100 vs 0–1; ISO vs epoch seconds). Normalise at the
  provider boundary; nothing above it sees raw values.
- **Stream details found while building (T1.3).** The `limits[]` rows also carry `group`,
  `scope.model.display_name` (the model name on a `weekly_scoped` row), `severity` and `is_active`; only
  `kind`, `percent`, `resets_at` and the model name are used. A `kind` afk does not know is kept as
  `unknown`, with the CLI's word in `Scope`. Run the probe outside a project directory (afk runs it from
  its state directory): the CLI records every run under the directory it starts in.
- **Not used:** the undocumented `api/oauth/usage` endpoint, raw response headers, or token estimates
  from local transcripts.

### 6.3 Decision (pure function, table-tested)

Inputs: latest snapshot (or its absence and age), thresholds, whether a session is running, now.

| Condition (first match wins) | Action |
|---|---|
| `OverageEnabled` | **Refuse to run.** `failure` item: "turn extra usage off". |
| No snapshot newer than `stale_after` (default 30 min) | Hold: launch nothing new; leave a running session alone; notify once. |
| Any window ≥ `pause_at` (default 90) | **Pause** the running session; wake at that window's `ResetsAt` + 1 min. |
| Session window ≥ `launch_below` (default 75), or weekly ≥ `pause_at` − 5 | Launch nothing new; a running session continues. |
| Otherwise | Run. |
| Provider returns `ErrUsageUnsupported` | Follow `on_unknown_usage`: `run` (default) or `hold`. |

As built, the unsupported row is checked first, because such a provider never has a snapshot and "no snapshot" would otherwise always win.

Every action is logged (§4.4) and reflected in `usage.json`.

### 6.4 Pausing and resuming a live session

How to pause a running agent cleanly is **not yet known** and is T1.3/T2.4's spike (§10). Candidates:
interrupt the turn through the PTY and later send a "continue" prompt; or let the provider's own
limit-wait act as a backstop. Known facts to design against:

- Interactive Claude Code (≥ 2.1.234) waits at a usage limit and continues by itself after the reset,
  at most twice in a row and never for a reset more than 24 hours away. Headless `-p` runs just fail.
  So the weekly limit is always the daemon's job.
- A `StopFailure` hook with matcher `rate_limit` fires when a limit ends a turn, with no reset time in
  its input. The plugin should use it to write a marker file the daemon reads; the reset time comes
  from the last snapshot.
- In the nebari build, a response cut off mid-stream needed a human "Please continue". The daemon must
  be able to send that prompt.

### 6.5 Login expiry

Check `claude auth status` hourly. Logged out, or (if detectable) expiring within 3 days → a blocking
`failure` item and a high-priority notification. Nothing launches while logged out.

---

## 7. Engineering standards (every task)

- **`make check` green**: gofmt, `go vet`, `go test ./...`, plus the plugin's node and shell tests.
  CI runs on Linux; the VM is Linux. Anything macOS-only (desktop toasts) must degrade silently there.
- **Table tests** in the style of `new_test.go`: named cases, `t.Run`, and a comment on the test saying
  which rows are load-bearing and why.
- **Tests are hermetic.** Dependencies are injected. No test creates or modifies a real session, calls
  GitHub, runs an agent CLI or probes real usage. `gh`, `git`, the provider CLI and the clock are
  interfaces with fakes.
- **Doc comments explain why**, especially where the code looks wrong without the reason. With no git
  history to consult, this is the only record. A comment asserting a mechanism in a dependency that you
  have not read in that dependency's source is worse than no comment.
- **Never `cp` a Go binary onto `$PATH`** on macOS arm64 (the copy is killed by the kernel); `make
  install` builds straight into the prefix. Build test binaries outside `$PATH`.
- **The status schema is a contract.** A new field means the schema document changes first, in the same
  change.
- **No new provider names outside `internal/provider`.**
- **User-facing text is plain language** for a hobbyist: say what happened and what to do next.

---

## 8. Milestones

```
M0 Foundation ──▶ M1 Primitives ──▶ M2 Daemon ──▶ M3 Surfaces ──▶ M4 Run nebari
 T0.0 VM          T1.1 ledger        T2.1 loop     T3.1 web        T4.1 nebari on the VM
 T0.1 seed        T1.2 inbox         T2.2 ship     T3.2 dash       T4.2 supervised, then unattended
 T0.2 plugin      T1.3 usage         T2.3 recover
 T0.3 QA parity   T1.4 tokens        T2.4 govern
 T0.4 flaky test  T1.5 start-up
 T0.5 host tests
```

T1.1 to T1.4 are independent of each other. T2.2, T2.3 and T2.4 are independent once T2.1 is in.

---

### M0 — Foundation

**Outcome:** an `afk` binary and plugin, renamed from forge, green in CI, running on the VM.

#### T0.0 — Stand up the VM (owner; not an agent task)
- **Depends on:** —
- **Goal:** an always-on Linux VM that afk is built on and later runs from.
- **Scope (owner checklist):** follow `~/code/afk-vm-runbook.html` (a homelab runbook, kept outside
  this repo): an Ubuntu Server 26.04 LTS VM on Proxmox (4 vCPU / 8 GB / 60 GB), the toolchain (git,
  Go, Node, Ruby, `gh`, `jq`, tmux), a clean snapshot, `gh auth login`, Claude Code with an interactive
  `/login`, **extra usage turned off** on the Claude account, bypassed permissions in Claude Code's
  settings, forge built from `~/code/forge-cli` (`make install`) with `forge plugin install --target
  claude`, and a clone of this repo.
- **Acceptance:** over SSH, `forge new --scratch` opens a working Claude Code pane in `forge dash`;
  `claude -p "/usage" --output-format stream-json --verbose` prints a `usage_report` with
  `extra_usage.is_enabled: false`.

#### T0.1 — Seed the repo from forge
- **Depends on:** T0.0
- **Goal:** forge's code in this repo under afk's name, green on Linux.
- **Scope:** copy the working tree of `~/code/forge-cli` (no `.git`, no built binary). Rename: module
  `github.com/dphaener/afk`; binary `afk`; state directory `.forge/` → `.afk/`; config, state and
  data directories `forge` → `afk`; plugin `plugins/forge` → `plugins/afk` with commands `/afk:go`
  etc.; `plugins/forge-opencode` → `plugins/afk-opencode`; marketplace manifests; every user-facing
  string. Drop the README section about the Foundry name collision. Keep `.claude/worktrees/` as the
  worktree location (renaming it is deferred). Write afk's `CLAUDE.md` from forge's, updated for the
  new names and for §7. Rewrite `README.md` for afk. CI: Go on `ubuntu-26.04` (named explicitly so CI matches the VM; `ubuntu-latest` still means
  24.04), plus the plugin's node
  tests and hook shell tests.
- **Must not:** change behaviour, schemas or tests' meaning. This task is a rename.
- **Acceptance:** `make check` green locally and in CI; `grep -ri forge` over tracked files returns
  only deliberate provenance notes; on the VM, `afk plugin install --target claude`, then
  `afk new --scratch` opens a pane in `afk dash`; `afk new --dry-run --base main x` prints the action
  list with `.afk/` paths.
- **Amended 2026-10-10 (T0.1 done):** CI is `.github/workflows/ci.yml` (the old `go.yml` is gone) and runs only `make check`, which now also runs `plugin-test` and `hook-test`; the display name is lowercase `afk` everywhere; `docs/decisions.md` lists the other choices.

#### T0.2 — The afk plugin: forge's workflow plus shipit's strengths
- **Depends on:** T0.1
- **Goal:** one plugin whose autonomous run is at least as good as a `/shipit:go` run on nebari.
- **Scope:** read both plugins first (`plugins/afk` and shipit at the path in §1). Graft into the afk
  plugin:
  - **QA scenarios and evidence.** Each work-plan task gains shipit's QA Scenarios (tool, steps,
    expected, evidence path); evidence is written under `.afk/{session}/evidence/`. Update
    `plan-format.md`, `plan_to_args.rb` and the Go plan parser together.
  - **Hands-on QA review check.** Add shipit's F3 (real QA: run the app, exercise the change, run the
    full test suite once) as a check in `review.js`, under the existing severity gate.
  - **The lean cycle of §5.7**, all nine rules. In particular: remove forge's separate scope check and
    make scope a compliance criterion, leaving three checks; run a critic only on a blocker or major
    finding; replace the bookkeeping agent in `execute.js` with a call to a new `afk plan check
    <session> <task-id> done|failed` command (atomic write, safe when two tasks finish together);
    write the test-running and no-polling rules into the task-agent prompt; require file references
    in `plan-format.md`.
  - **Glossary and decisions output.** The planner maintains the project's `CONTEXT.md` and records
    decisions the master plan didn't make in `docs/decisions.md`, as nebari's tasks do today.
  - **Master-plan awareness.** When the session description names a master-plan task, the planner
    reads the sections that plan tells it to read, and at the end the conductor amends that task's
    entry in the master plan ("Amended <date> (T… done): …") in the same branch.
  - **Autonomous policy.** Replace `conductor/prompts/autonomous.md` with §5.5.
  - **Stop before shipping.** In autonomous mode the conductor ends at reviewed commits on the branch
    with `phase: done`; it does not push or open a PR. (Until T2.2 is merged, bootstrap sessions are
    told to ship in their description; see the top of this document.)
  - Add the `StopFailure` hook of §6.4 (marker file only).
- **Not in scope:** shipit's Claude Code mods; the OpenCode port (record in its `PROVENANCE.md` that it
  lags this change; resync is deferred).
- **Acceptance:** plugin node tests and hook tests green, with new cases for: the hands-on QA check;
  exactly three checks with no scope agent; a critic spawned for a major finding and none for a
  passing check; re-review limited to the failed check; no bookkeeping agent in an execute run; the
  QA-scenario fields. Table tests for `afk plan check`, including two concurrent writers.
  `plan_to_args.rb` rejects a task missing QA scenarios or file references. A supervised autonomous
  run of one small real task in a scratch repo ends `phase: done` with evidence files, a verdict file
  and zero terminal questions.
- **Amended 2026-10-10 (T0.2 done):** later tasks may assume the following. (1) `afk plan check
  [--project DIR] [--reason TEXT] <session> <task-id> done|failed` is the only writer of plan
  checkboxes: it edits one line of `.afk/plans/{session}.md` under a lock, `failed` needs a
  non-empty reason and also appends `## (ID) FAILED` to `.afk/{session}/issues.md`, repeating a call
  is a no-op, exit 1 for an unknown task or missing plan, exit 2 for bad usage. (2) Reviews run three
  checks, keys `compliance`, `quality`, `qa`; the verdict file's lines are `- Compliance:`,
  `- Quality:`, `- QA:`, and findings read `- [severity] ID: (check) text`; an old verdict with a
  `- Scope:` line shows as a parse error. (3) A work-plan task has seven subsections, with
  `**QA Scenarios**` after Acceptance Criteria, and exact file references in `**Files**`. (4) The
  rate-limit marker is `.afk/{session}/.rate-limited`. (5) The autonomous prompt parks through
  `awaiting_input` (the schema's S4 write and a toast) until T1.2 adds `afk inbox add`; T1.2 must
  replace that. (6) `[policy] notes` from `afk.toml` is read by the agent directly until T1.1's loader
  exists. (7) The autonomous conductor ends at reviewed commits with `phase: done` and does not push.
  (8) The supervised scratch-repo run in this entry's acceptance is still owed by the owner; see
  `## T0.2` in `docs/decisions.md`. The OpenCode port lags this change.

#### T0.3 — QA parity with shipit
- **Depends on:** T0.2
- **Goal:** close the two gaps left because T0.2 ran without shipit's text, and fix two defects its
  review reported.
- **Background:** shipit is not on the VM and is not needed. Its relevant text is quoted here in full.
  Its plan template gives every task two scenarios, `Scenario: [Happy path]` and `Scenario:
  [Failure/edge case]` ("Trigger error condition", "Assert handled correctly", expected result
  "Graceful failure"). Its QA reviewer's criteria are: "Start from a clean state. Execute EVERY QA
  scenario from EVERY task. Test cross-task integration. Test edge cases: empty state, invalid input,
  error conditions, rapid actions."
- **Scope:**
  - **Failure scenarios.** Each scenario gains a fifth field, `Case: happy | failure`, written after
    `Tool`. Every task needs at least one `happy` scenario, and at least one `failure` scenario
    (invalid input, a missing file, an error path) unless its QA Scenarios carry the line
    `No failure case: {reason}`, for a task with nothing that can fail (docs only, a rename). Update
    `plan-format.md`, the planner prompt, `plan_to_args.rb` (rejects a task that has neither) and the
    Go plan parser (stays lenient) together.
  - **Exploratory QA.** In `check-hands-on-qa.md`: start from a clean state (a fresh build in a
    scratch path, no state left by the task agents); after the scenarios, try the tasks together where
    one task's output feeds another; then try the edge cases that apply to this change, from shipit's
    list above. Keep it bounded, per §5.7: what a person would try in a few minutes, no new test
    files, and a finding only for behaviour that is broken, each with its evidence file.
  - **The critic cannot add findings.** `review.js` keeps only the critic's findings that were among
    the ones it was given, and never lets a severity rise.
  - **Fenced task lines.** `internal/state/plan.go` ignores a `- [ ] (ID)` line inside a fenced block,
    as `plancheck` and `plan_to_args.rb` already do; remove the `noParseBack` exception in
    `plancheck_test.go`.
- **Not in scope:** the OpenCode port; the other minor follow-ups listed on pull request #2.
- **Acceptance:** `make check` green. New cases: `plan_to_args.rb` rejects a task with no failure
  scenario and no waiver line, and accepts each alone; the Go parser reads `Case`; `review.js` drops a
  finding the critic invented and a severity the critic raised; a fenced task line is not a task in any
  of the three parsers. Plugin version bumped in both manifests.
- **Note:** this is the first task run with afk's own plugin (bootstrap stage 2). Watching it end to
  end is the supervised run still owed from T0.2: it should finish with evidence files, a verdict file
  and no terminal questions. Record the result in `docs/decisions.md`.
- **Amended 2026-10-11 (T0.3 done):** later tasks may assume the following. (1) A QA scenario has five fields; `Case: happy | failure` follows `Tool`, and `plan_to_args.rb` rejects a task without a `happy` scenario, or with neither a `failure` scenario nor the line `No failure case: {reason}`, so a work plan written for plugin 4.0.0 no longer parses. (2) The plugin is 5.0.0; a machine picks it up only after `make install` and `afk plugin install`. (3) `review.js` numbers the findings it gives a critic and keeps only numbered survivors with their original text, never at a higher severity. (4) All three plan parsers skip everything inside a code fence, and in the Go parser an unclosed fence hides the rest of the file. (5) `state.QAScenario.Case` and `state.Task.NoFailureCase` exist and are not validated in Go. (6) The supervised run owed from T0.2 was this session; its result is under `## T0.3` in `docs/decisions.md`. The OpenCode port lags this change too.

#### T0.4 — Fix the flaky host lifecycle test
- **Depends on:** T0.1
- **Goal:** `TestHostLifecycleEndToEnd` passes every time, so a red CI run always means a real fault.
- **Background:** the test fails at `internal/host/lifecycle_test.go:365` with `third attach =
  (connection dropped, <nil>), want (DetachUser, nil)`: a client that stole the session from another
  client, typed, then detached with ctrl-\ is told its connection dropped. T0.1 measured 6 failures
  in 40 runs on forge's untouched source, more often on a busy machine. On 2026-10-11 it failed CI on
  `main` for a docs-only commit and failed T0.3's QA run. Under merge-on-green (§5.4) a flaky test
  either blocks a good change or teaches the loop to re-run until green.
- **Scope:** reproduce it first (`go test -count=200 -run TestHostLifecycleEndToEnd ./internal/host/`,
  with `-race`, and under CPU load) and find the cause before changing anything. Decide from the
  evidence whether the fault is in the host or attach code (a detach racing the steal, or the host
  closing the connection before the client reads the detach result) or only in the test's timing. Fix
  it where it is. If it is in the host, a user can hit it too, so add a test that pins the corrected
  ordering. Record the cause in `docs/decisions.md` and remove the "known flaky test" notes from
  `CLAUDE.md`, `docs/decisions.md` (`## T0.1`) and the amendment on T0.1 above.
- **Must not:** add a retry, a sleep, a longer timeout or a skip to make the test pass; weaken what
  the test asserts; break the rule that a wedged client never stalls the child (`CLAUDE.md`).
- **Acceptance:** 500 consecutive runs of the test pass with `-race`, and 200 pass while the machine
  is under CPU load, with the commands and counts saved as evidence; `make check` green; the decision
  entry names the cause and says whether a user could have hit it.
- **Amended 2026-10-11 (T0.4 done):** later tasks may assume the following. (1) `TestHostLifecycleEndToEnd` is no longer flaky, so a red run of it is a real fault. (2) The fault was in the client, not the host: a user detach could be reported as a dropped connection, in `host.Attach` and in the dashboard pane that `afk attach` really uses (`internal/ui/hostpane.go`); both readers now check whether the user asked to leave. The host still answers a detach with a bare close, and the frame protocol is unchanged. (3) `host.Attach` has no caller in afk; the live client path is `hostChild` in `internal/ui`, and no test yet drives `ui.AttachPane` to a detach against a real host. See `## T0.4` in `docs/decisions.md`.

#### T0.5 — Make every host test reliable
- **Depends on:** T0.4
- **Goal:** no test in `internal/host` or `internal/ui` fails without a real fault, so merge-on-green
  (§5.4) can trust a red run.
- **Background:** `TestServeSIGTERMBehavesLikeExit` (`internal/host/host_test.go`) failed CI on `main`
  after T1.3's merge and once in T1.3's QA run, on changes that do not touch the package. The test
  dials the host, sends the process SIGTERM, then expects an `Exit` frame and after it end of file.
  It has been seen to get `reading frame header: EOF` and `connection reset by peer` in place of the
  frame. That would mean the host can close a client's connection on shutdown before the `Exit` frame
  is delivered, or the dial can race the signal. This is the second timing failure in the package;
  the first (T0.4) was a real bug that users could hit.
- **Scope:** find the others first, then fix. Run each test package many times
  (`go test -count=100 -race ./internal/host/`, and again under CPU load; `internal/ui` with `-short`
  for the stress runs, since its end-to-end test takes about 40 seconds) and list every test that
  fails even once, with its failure text and rate. For each, prove the cause with a run before
  changing anything, as T0.4 did, and fix it where it is: in the host or client if a user could hit
  it, with a test that forces the bad ordering, or in the test if only the test is wrong. Record each
  cause in `docs/decisions.md`, saying whether a user could have hit it.
- **Must not:** add a retry, a sleep, a longer timeout or a skip to make a test pass; weaken what a
  test asserts; break the rule that a wedged client never stalls the child (`CLAUDE.md`); change the
  frame protocol without recording why in `docs/decisions.md` and the plugin's schema if it is
  affected.
- **Acceptance:** `TestServeSIGTERMBehavesLikeExit` passes 500 of 500 runs with `-race` and 200 of 200
  under CPU load; the whole `internal/host` package passes 100 of 100 with `-race` and 50 of 50 under
  load; `internal/ui -short` passes 50 of 50 with `-race`. Commands and counts saved as evidence.
  `make check` green.
- **Amended 2026-10-12 (T0.5 done):** later tasks may assume the following. (1) No test in `internal/host` or `internal/ui -short` is known to be flaky, so a red run is a real fault; five host tests were, and each cause is under `## T0.5` in `docs/decisions.md`. (2) One was a real host fault: on shutdown the host now sends an `Exit` frame to every connection that reached it without being attached (held by the accept loop, or still in the listen queue), so a client connecting as a session ends is told "session ended" (11), not "dropped" (13). The frame protocol and the bare close on `Detach` are unchanged, and shutdown's extra waits share one `clientFlushGrace`. (3) `host.go` has a test seam, the package variable `afterAccept`, and the stuck-child test fixture acknowledges its write switch in `ack-no-write`; `setStuckChildFlag` waits for it. (4) The host fix uses `syscall.Accept` on the listener and has been run on Linux only. (5) A whole `internal/host` run with `-race` takes about a minute and `internal/ui -short` about two, so stress runs of either take hours.

---

### M1 — Primitives

**Outcome:** the three building blocks the daemon needs, each usable from the command line by itself.

#### T1.1 — Master-plan parser and ledger
- **Depends on:** T0.1
- **Scope:** `internal/masterplan` (§5.1) and `internal/ledger` (§4.2), plus the pure next-task function
  (§5.2). Commands: `afk tasks` (table of id, title, status, unmet dependencies), `afk next` (the task
  that would run, or why none), `afk ledger set <id> <status>`. `afk.toml` loader (§4.6) for `plan`.
- **Acceptance:** a copy of nebari's `docs/PLAN.md` in `testdata/` parses to its 40 task headings with
  correct dependencies; table tests for next-task cover: plan order, parked-then-answered goes first,
  failed dependency blocks, a dependency on a superseded task (T6.1 → T4.2) waits for both
  replacements, unknown dependency, cycle, a task added between two
  reads; with the nebari ledger seeded (21 done, T4.2 superseded, T4.2b pending), `afk next` prints
  `T4.2b`.
- **Amended 2026-10-11 (T1.1 done):** later tasks may assume the following. (1) `masterplan.ParseFile` then `masterplan.Validate` give the ordered tasks; a malformed heading, a duplicate id, a task with no `- **Depends on:**` line, an unknown dependency and a cycle are errors, `all prior` means every earlier task, and other text with no ids means no dependencies and is kept in `Task.Note`. (2) `ledger.Update(project, fn)` is the only writer of `.afk/build/ledger.json` (lock, re-read, rename) and the daemon must use it; `ledger.Load` returns an error wrapping `ErrCorrupt` for a damaged file, never an empty ledger. (3) `nexttask.Next(tasks, ledger, openItems)` returns no task with reason `in progress` while any task is `running` or `shipping`, and takes the open inbox item ids as a function; the commands pass nil (every item open), so T2.1 must pass the real one from `internal/inbox`. (4) `afk tasks`, `afk next` and `afk ledger set` work on the project's main checkout even when run inside a session's worktree; `loadBuildState` in `buildstate.go` is the shared loader. (5) `projectconfig.Load` is the one `afk.toml` reader: it was folded with T1.2's at merge and returns `plan` (default `docs/PLAN.md`) and `[notify] ntfy_url`, ignoring other keys, so agents still read `[policy] notes` themselves and T2.2 adds `[ship]` to it; likewise there is one `mainCheckout(dir, commonDir)`, in `maincheckout.go`. (6) On this repo `afk next` names T0.0, an owner task, until the ledger is seeded with the finished tasks. See `## T1.1` in `docs/decisions.md`.

#### T1.2 — Inbox and notifications
- **Depends on:** T0.1
- **Scope:** `internal/inbox` (§4.3) with atomic writes. Commands: `afk inbox` (list, open first),
  `afk inbox show <id>`, `afk inbox answer <id> <text|option>`, `afk inbox add` (for agents; flags
  `--task --kind --title --body --option… --recommended --assumed --block|--no-block`). ntfy backend
  in `internal/notify` (plain HTTP POST; priority from kind and `blocking`), configured by
  `[notify] ntfy_url`. Document `afk inbox add` in the plugin's autonomous prompt.
- **Acceptance:** table tests for validation and for which items notify at which priority; the ntfy
  client is tested against an `httptest` server; adding a blocking item with `ntfy_url` set posts once;
  no notifier configured is not an error.
- **Amended 2026-10-11 (T1.2 done):** later tasks may assume the following. (1) `afk inbox`, `afk inbox show <id>`, `afk inbox answer <id> <text|number>` and `afk inbox add --kind … --title … --block|--no-block [--task --body --option… --recommended --assumed --session --project]` exist; exit 0 done, 1 not found or already answered, 2 written wrongly; exactly one of `--block`/`--no-block` is required. (2) Items live in the **main checkout's** `.afk/inbox/{id}.json` even when the command runs in a session worktree; go through `internal/inbox` (`Store.Add/List/Get/Answer`), never the files. (3) `internal/projectconfig` reads only `[notify] ntfy_url` from `afk.toml`; fold it together with T1.1's loader. (4) `internal/notify` has `NtfyPriority(kind, blocking)` and `Ntfy.Send`; a non-blocking `question` or `notice` sends nothing, and a notification problem never fails `afk inbox add`. (5) The autonomous prompt parks with a blocking inbox item **and still sets `awaiting_input`**, so T2.1's stall rule (§5.3) must skip a session that has an open blocking item. (6) Plugin version is 5.1.0; the OpenCode port lags. See `## T1.2` in `docs/decisions.md`.

#### T1.3 — Usage probe and governor decision
- **Depends on:** T0.1
- **Scope:** the provider interface additions (§6.1), the Claude Code implementation (§6.2) and the
  pure decision function (§6.3). Command: `afk usage` (prints windows, reset times, and what the
  governor would do now). Writes `usage.json` (§4.5). **Spike first:** run the probe on the VM and
  commit the captured stream (with account identifiers removed) as the parser's fixture; record in
  `docs/decisions.md` anything that differs from §6.2.
- **Acceptance:** parser tests on the captured fixture plus hand-made variants (null `rate_limits`,
  missing window, unknown `kind`, no `usage_report` at all); the decision table of §6.3 as a table
  test, one row per condition and each boundary value; Codex and OpenCode return
  `ErrUsageUnsupported`; `afk usage` works on the VM.
- Amended 2026-10-11 (T1.3 done): the method is `Usage(ctx, UsageDeps)` with the runner and clock passed in; a window also has `scope` and may have kind `unknown`; `governor.Decide` checks "unsupported" first and its thresholds are code defaults (`governor.DefaultThresholds()`), so T2.4 adds the config keys and decides whether the action is stored in `usage.json`; `internal/usagestate` owns that file (it follows `XDG_STATE_HOME`); a failed probe returns an error, not a snapshot, and callers fall back to the stored one; run the probe from afk's state directory, never a project.

#### T1.4 — Token ledger
- **Depends on:** T0.1
- **Scope:** §4.7. A provider method that returns per-call usage records for a session and its
  subagents, with each record's role and phase; the Claude Code implementation reads its JSONL
  transcripts (main session, subagent and workflow-agent files) with the counting rule of §4.7, and
  takes role from the agent's metadata and the phase from `status.json` transitions. Codex and
  OpenCode return "unsupported". `internal/tokens` aggregates and appends to the ledger. Command:
  `afk tokens [session|task]` (table by phase and role, weighted total, share of each). **Spike
  first:** run one real autonomous task on the VM and commit a trimmed copy of its transcripts
  (prompts and file contents removed, usage and metadata kept) as the fixture.
- **Acceptance:** parser tests on the fixture and on hand-made cases (a message id split across lines,
  the same id in two files, a subagent with no metadata, an unknown role); the weighted formula as a
  table test; `afk tokens` on the fixture matches numbers computed by hand in the test.
- Amended 2026-10-11 (T1.4 done): the provider method is `SessionTokens(TokenQuery)`, returning `ErrTokensUnsupported` or `ErrNoTranscript`; phases come from the conductor's phase writes in the main transcript, because `status.json` keeps no history (so a daemon must record a session before `afk clean`); ledger lines are per phase, role and model; until T2.1 calls `internal/tokens` at task end, `afk tokens --record SESSION` writes the ledger. See `docs/decisions.md` T1.4.

#### T1.5 — Measure and trim agent start-up context
- **Depends on:** T0.2, T1.4
- **Goal:** know what a fresh agent carries before its first action, and remove what it doesn't need.
- **Scope:** on the VM, with the afk plugin, capture the first model call of a task agent, a review
  agent and a research agent, and itemise their context: tool definitions, plugin skills and agent
  definitions, `CLAUDE.md` and rules files, connectors, anything else. Record the itemised sizes in
  `docs/decisions.md`. Then trim what is safe: restrict each agent type's tools to what its role uses
  (the hands-on QA check keeps its browser tools), and keep project rules that govern the code it
  writes. Re-measure.
- **Must not:** remove project rules from agents that write code, or weaken any review check.
- **Acceptance:** before-and-after sizes per agent type in `docs/decisions.md`; a scratch-repo
  autonomous run after the trim still ends `phase: done` with all three checks run; `afk tokens` for
  that run shows the start-up share.

---

### M2 — Daemon

**Outcome:** `afk daemon` takes a project from "tasks remain" to "all done or waiting on the owner"
with nobody at the keyboard.

#### T2.1 — The build loop
- **Depends on:** T0.2, T0.3, T1.1, T1.2, T1.4
- **Scope:** `afk daemon` implementing §5.3 for the projects in machine config, one task at a time per
  project, launching each agent in the provider's unattended permission mode after the preflight of
  §5.3 step 3. Decision log (§4.4). Token ledger entry for every finished task (§4.7). `afk daemon status` (what it is doing and why). `afk pause <project>`
  and `afk resume <project>` (a flag file the loop honours between tasks). A systemd unit file in
  `contrib/`. The `afk.local.toml` overlay of §4.6, in the one loader every command uses
  (`internal/projectconfig`), with table tests for: local only, committed only, a local key
  overriding a committed one, and a local file that does not parse (an error naming the file). In
  this repo's `.gitignore`, replace `afk.toml` with `afk.local.toml`, and say in the README where
  `ntfy_url` belongs and why. The daemon holds no state in memory that is not on disk; killing and restarting it at
  any point must be safe. Shipping is a stub here: at `phase: done` the task goes to `shipping` and
  waits.
- **Acceptance:** with a fake provider whose "agent" is a script that writes `status.json`
  transitions, integration tests cover: happy path to `shipping`; `phase: failed`; a blocking inbox
  item parks and an answer un-parks; `awaiting_input` stall raises an item; daemon killed mid-task and
  restarted adopts the running session and starts no duplicate; pause honoured; the launch argv
  carries the unattended mode; a failed preflight raises an item and launches nothing; a finished
  task has ledger lines for each phase.

#### T2.2 — Ship in code
- **Depends on:** T2.1
- **Scope:** `internal/ship` implementing §5.4 as a persisted state machine over `git` and `gh`
  interfaces. Delivering a follow-up prompt to an idle session (for CI hand-backs) through the host's
  client protocol; note that this takes the session's single client slot, as the dashboard's restart
  does today. Session clean-up after `done`.
- **Acceptance:** table tests over fake `git`/`gh`: green first time; red then green on re-run; red →
  hand-back → green; park at the hand-back cap; required-env match parks before merge; deploy healthy;
  deploy timeout stops the line and `afk resume` clears it; a crash between every pair of states
  resumes without repeating a push or a merge. Then one real end-to-end run against a throwaway
  private GitHub repo with a trivial CI workflow, recorded in `docs/decisions.md`.

#### T2.3 — Automatic recovery
- **Depends on:** T0.4, T0.5, T2.1
- **Scope:** §5.6. Wedge detection with no client attached; automatic restart with a cap and backoff;
  host relaunch after a reboot; a "continue" prompt after a restart when the session comes back idle
  mid-task.
- **Acceptance:** host tests for client-less probing (keep forge's existing liveness tests green
  unchanged); daemon tests: wedged → restarted → resumes; restart cap reached → parked with an item; a
  human `afk attach` during daemon supervision still gets the session, and the daemon recovers its
  view after they detach.

#### T2.4 — Governor in the loop
- **Depends on:** T2.1, T1.3
- **Scope:** wire §6.3 into launch decisions and a running session; implement pause and resume of a
  live session (§6.4) after a **spike on the VM** that tries the candidates against a real session and
  records the result; the `StopFailure` marker; login-expiry check (§6.5).
- **Acceptance:** daemon tests with a fake usage source and clock: launch refused at 75% session;
  running session paused at 90% and resumed after `ResetsAt`; overage on refuses to run; stale
  snapshot holds; unsupported provider follows `on_unknown_usage`; logged-out blocks launches. The
  spike's finding and the chosen pause mechanism are written up in `docs/decisions.md`.

---

### M3 — Surfaces

**Outcome:** the owner can see what afk is doing and unblock it from a terminal or a phone.

#### T3.1 — Web page
- **Depends on:** T2.1, T1.2
- **Scope:** an HTTP server inside `afk daemon`: status (per project: current task, phase, tasks
  done/total, last ship result), usage (windows, resets, governor state), inbox (list, detail, answer
  form), pause/resume buttons. Server-rendered, works at phone width, no JS build step. Loopback bind
  by default; every request needs the configured token (cookie after a one-time `?token=` visit);
  refuses to bind a non-loopback address without a token. The same handlers serve JSON under `/api/`.
  ntfy notifications link to the relevant inbox item when `web.public_url` is set.
- **Acceptance:** handler tests for auth (no token, wrong token, cookie), each page, answering an item
  (which un-parks its task), and the non-loopback refusal.

#### T3.2 — Dashboard: the build view
- **Depends on:** T2.1
- **Scope:** in `afk dash`: a header segment with governor state and the tightest usage window; a
  Build tab listing master-plan tasks with ledger status; an inbox overlay to read and answer items;
  key bindings registered like every other key so `?` and `:` list them. All from files; no dialing.
- **Acceptance:** UI model tests for the new tab, overlay and header at narrow and wide widths; README
  updated.

---

### M4 — Run nebari

**Outcome:** afk finishes nebari.

#### T4.1 — Nebari on the VM (owner + agent)
- **Depends on:** T2.2, T2.3, T2.4, T3.1
- **Scope:** `docs/runbook-vm.md`: project prerequisites on the VM (for nebari: Ruby, PostgreSQL 17,
  libvips, Chrome for system specs), the capped API key, `afkd` enabled under systemd, web page
  reachable over the owner's tunnel. In nebari: add `afk.toml` (health URL, `required_env_pattern` for
  `${VAR:?}` in `docker-compose.coolify.yml`, policy notes on live calls), seed the ledger, switch its CI to `runs-on: ubuntu-26.04` so it matches the VM, confirm CI
  runs on pull requests (it does) and that a merge to main triggers the Coolify deploy.
  **Owner prerequisites:** enable nebari's database backups to B2 and do the restore drill (deferred
  since nebari T0.3; unattended deploys run migrations with nobody watching); finish or hand over the
  in-flight T4.2b.
- **Acceptance:** on the VM, nebari's `bin/ci` passes; `afk next` in nebari names the right task;
  `afk usage` and the web page work; a backup exists and a restore has been tested.

#### T4.2 — Supervised first task, then unattended
- **Depends on:** T4.1
- **Scope:** run one nebari task with the owner watching through `afk dash`, end to end including the
  deploy. Write up what needed a human and fix or file each cause. Then leave the daemon running.
- **Acceptance:** one nebari task merged and deployed with no terminal input; a retrospective in
  `docs/decisions.md`, including that task's `afk tokens` breakdown against the nebari baseline of
  about 16M weighted per task (§4.7); the daemon then runs unattended until the plan is done or it is waiting on the
  owner.

---

## 9. Deferred / later

- **`afk plan`: the master-planning process as a product feature.** Codify the session that produced
  nebari's plan: survey house rules and sibling projects → a handful of decisive questions with
  recommendations → one document with a shared contract (context, decisions table, architecture,
  domain model, core engine, standards), dependency-linked tasks sized for one cycle, deferred list,
  open questions keyed to the task that needs them, definition of done → reconciliation with a design
  system. Source transcript: `~/.claude/projects/-Users-darinhaener-code/43501d06-….jsonl`.
- **Promote decisions to code**, using the decision log: the conductor's phase state machine (its gates
  are already disk predicates), checkbox bookkeeping, verdict rendering, the fix-loop counter. Then
  policy rules learned from repeated identical answers.
- **Parallel tasks** across independent milestones.
- **A fresh session per phase.** In one long session the review phase's main-loop calls carried 47–69k
  tokens each, against 19–23k when review ran as its own session (about 3% of the build). The daemon
  launching plan, execute and review as separate sessions also moves the conductor toward code.
- **A thinner cycle for small master-plan tasks**: one agent, one review pass, no wave plan. Decide
  with ledger data, not before it.
- **Usage probes for Codex and OpenCode**; per-provider limit signals.
- **API-key billing** as a first-class alternative to a subscription (and a spend governor for it).
- **OpenCode port resync** with the T0.2 plugin changes.
- **Ledger reconciliation from GitHub** (rebuild status from merged PRs after losing `.afk/`).
- **Plan and diff review in a browser** (Plannotator's `annotate --gate --json` / `review --json`) as an
  optional human gate, mainly for a new master plan.
- **A socket API with an event stream** (herdr's model) if a second kind of client ever needs it; until
  then the HTTP JSON API is the one control surface.
- **Daemon-side write-guard and continuation enforcement**, replacing shipit's Claude-only mods.
- Rename `.claude/worktrees/` to a provider-neutral location.
- Open-source release: licence, install script, Homebrew tap, docs site.

## 10. Open questions to resolve before the relevant task

| Question | Needed by |
|---|---|
| Does forge's autonomous flow hold up on real tasks as well as `/shipit:go` did on nebari? **Answered 2026-10-10: yes.** The owner has run forge's autonomous mode many times and it works well, so T0.2 grafts shipit's strengths onto it and does not rework it. | T0.2 |
| How to pause a live agent session cleanly and resume it (§6.4). Spike on the VM. | T2.4 |
| What exactly a session emits when it hits a usage limit (text, hook, whether a reset time is present). Never observed; capture it the first time it happens. | T2.4 |
| Is the login-expiry warning detectable ahead of time, or only the logged-out state? | T2.4 |
| Liveness with no client: host-side schedule, or daemon holds the client slot? | T2.3 |
| Does `claude -p "/usage"` behave when its backing request is throttled, and how often is safe to probe? **Answered 2026-10-11:** not observed. The probe was run three times back to back (Claude Code 2.1.296) and each time exited 0 with a full report, in about 1.5 seconds. Nobody tried to provoke throttling, because tripping a rate limit on the owner's account on purpose is not a machine's call, so what it does when throttled is still unknown. `afk usage` reuses a reading under 5 minutes old, and the governor treats a reading over 30 minutes old as stale and holds. | T1.3 |
| Nebari's health-check URL, and how to tell a deploy of *this* commit finished (as opposed to the old container still answering). | T4.1 |
| Does a merge to nebari's main trigger the Coolify deploy today, or does a webhook need adding? | T4.1 |
| What the 28k of extra start-up context in implementation and review agents consists of. | T1.5 |
| How a subscription weights cache reads, cache writes and output against its limits. Unpublished; compare the token ledger with the usage percentages the governor records. | after T2.4 |
| Licence for the open-source release. | before release |

## 11. Definition of done (every task)

- `make check` green locally and in CI (Linux).
- New behaviour covered by table tests; decision logic is a pure function with every branch a row.
- No test touches a real session, GitHub, an agent CLI or real usage.
- Nothing outside `internal/provider` names a provider.
- Schemas: any change to a file in §4 updates §4 and, for `status.json`, the schema document, in the
  same change.
- `README.md` updated for any user-visible command, flag or key.
- `docs/decisions.md` updated when the task decides something this plan didn't, and this plan amended
  in place ("Amended <date> (T… done): …") when the task changes what a later task should assume.
- Verified against a built binary, not from reading the diff.
