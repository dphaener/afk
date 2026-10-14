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
| One machine settings file | Decided 2026-10-13 (M6): `config.toml` is the machine's only settings file. The dashboard's project switcher lists the `[[project]]` entries the daemon builds, plus any project found under the search folders in its `[dash]` table. `config.json`, carried over from forge, is no longer read; when one is found, afk says once what to move and touches neither file. |
| Dashboard controls | Decided 2026-10-13 (M6): `afk dash` gets pause and resume keys and an inbox count in its header. T3.2 left both out only because its scope did not list them. |
| Dashboard as command center | Decided 2026-10-13 (M7): `afk dash` does the owner's day-to-day work, so the shell is needed only for set-up. It starts all four planning sessions (new, extend, amend, reconcile), approves a plan behind a confirm, has an all-projects screen, adds and removes projects, sets a task's status by hand, shows a task's tokens, shows the planning session and the checker's result, and refreshes the usage reading. The owner profile, installing the plugin, starting and stopping the daemon and `afk ledger init` stay commands. |
| Dashboard keys | Decided 2026-10-13 (M7): one leader key, **ctrl+space**, works everywhere in the dashboard, the agent pane included, and opens an actions dialog at once listing every action with its key. **Every action is under the leader**: the session list keeps only the keys that move around, and the bare letters, the ctrl chords and the `:` palette go (T7.2). The owner chose this over keeping today's letters as shortcuts. `afk attach` keeps ctrl-\. |
| What the dashboard writes | Decided 2026-10-13 (M7): besides inbox answers (T3.2) and pause and resume (T6.3), the dashboard writes only through the code of the matching command: `afk plan approve` (T7.8; approving from the web page stays forbidden, because others may reach it), `afk project add` and `remove` for `config.toml` (T7.10), and `afk ledger set` for the ledger (T7.11). This replaces "the dashboard never writes the ledger" (T3.2) and "never writes `config.toml`" (T6.1, T6.2) for those actions only. |
| New plan in a new folder | Decided 2026-10-13 (M7): the dashboard can create the folder for a new plan. A typed name is created under the owner profile's `project location` (From the owner profile, a default: `~/code`) and made a git repository; the dashboard then asks, each time, whether to create a private GitHub repository with the owner's existing `gh` login and set it as `origin`. Never without that yes, per the profile's rule on accounts. |
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
- `.afk/{session}/.turn-failed` — one line `{"at":"<RFC 3339 UTC>","reason":"<the CLI's error word>"}`,
  written by the `StopFailure` hook for a failed turn worth retrying (never a rate limit), and removed
  by the next prompt or normal stop. The daemon reads it to type the resume command. It never touches
  `status.json`.

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

Amended 2026-10-11 (T2.2): an entry also carries four ship fields, each omitted when empty or zero.
`ship` is where the ship has got to, one of `committed`, `pushed`, `pr_open`, `handed_back`,
`ci_green`, `merged`, `deploy_healthy` or `stopped` (§5.4); `ship_at` is when that state was entered
(RFC 3339 UTC; the timed rules measure from it, and it moves on a re-run and on a push of a newer
head); `ci_reruns` counts re-runs of failed checks for the current head; `ci_fix_cycles` counts
hand-backs to the session. All four go back to empty or zero when the task is done. An unknown `ship`
word is rejected like an unknown status. `afk ledger set` takes several ids before the status, and
`afk ledger init [--done-through ID] [--done ID,ID]` creates a missing ledger and never changes an
existing one.

Amended 2026-10-12 (T2.2a): the `ship` words now include `after_merge`, between `merged` and
`deploy_healthy`: `committed`, `pushed`, `pr_open`, `handed_back`, `ci_green`, `merged`, `after_merge`,
`deploy_healthy`, `stopped`. It is saved only when the project's `afk.toml` sets `[ship] after_merge`;
without that key the ship goes from `merged` to `deploy_healthy` as before.

Amended 2026-10-12 (T2.3): an entry also carries `restarts`, `restart_at` and `continue_pending`, each
omitted when empty, zero or false. `restarts` counts the automatic recoveries of the current attempt
and is saved before each one, so a crash mid-recovery still counts it; a negative value is refused on
load. `restart_at` is when the last recovery finished (RFC 3339 UTC); the backoff, the new host's
grace and the wait before the continue prompt are all timed from it. `continue_pending` is true from a
recovery until the session's resume command has been typed into the new agent, and is cleared by that.
All three are cleared when the task starts a new attempt or gets a new session, is un-parked, or
leaves `running` (done, failed, shipping). Giving up leaves `restarts` in the parked entry; the
un-park clears it.

Amended 2026-10-12 (T2.4): an entry also carries `usage_pause_at` and `usage_wake_at`, each omitted when
empty. `usage_pause_at` is when the daemon paused the running session for usage (RFC 3339 UTC); it is
saved before the session is stopped. `usage_wake_at` is the earliest time to start it again, or empty
when none is known. Both are cleared when the task starts a new attempt or gets a new session, when the
session is started again after the pause, and when the task leaves `running` for `done`, `failed` or
`shipping`. An un-park back to `running` keeps them and a pause that parks keeps them, so the session
is started again, not typed into.

Amended 2026-10-13 (T2.5): an entry also carries `wakes` and `wake_at`, each omitted when zero or empty.
`wakes` counts the times the daemon typed the resume command to wake the session, and is saved before
each one; a negative value is refused on load. `wake_at` is when the last wake was saved (RFC 3339 UTC).
Both are cleared where `restarts` is cleared. A wake is not counted as a restart.

Amended 2026-10-13 (T5.6): a task held for an open question (§5.2) is `parked` with no session, and `parked_on`
names the `Open question: …` items of its rows, or the `Put the answer in the plan: …` item when an answer
could not be put into the plan. When the answers are in the plan it goes back to `pending`, not `running`.

### 4.2a Daemon files (new)

Amended 2026-10-11 (T2.1): three small files the build daemon adds, each with one writer and none
inside `status.json`, the ledger or `usage.json`.

| File | Writer | Shape |
|---|---|---|
| `.afk/build/paused` | `afk pause` (removed by `afk resume`) | `{"at":"<RFC 3339 UTC>"}`. The daemon reads only whether the file exists, so a damaged flag still pauses. |
| `.afk/build/daemon.json` | the daemon | `{"at","kind","task","session","reason"}`, every key always present. It says what the daemon last did for the project and why, and `at` is when the project entered that state, not when it was last looked at. A missing or damaged file means "not looked at yet". |
| `.afk/{session}/unattended` | session creation (`session.Config.Unattended`) | An empty file. Its existence means the session's agent runs in the provider's unattended permission mode; every host spawn and every restart of its agent reads it. |

The machine also holds `daemon.lock` and `running.json` in afk's state folder (beside `usage.json`);
they make the daemon one per machine and are not per project.

Amended 2026-10-11 (T2.2): two more files in `.afk/build/`, each written only by the daemon through
`internal/daemonstate`, by atomic rename.

| File | Writer | Shape |
|---|---|---|
| `.afk/build/stopped` | the daemon, when a deploy does not turn healthy in `deploy_timeout` (removed by `afk resume`) | `{"at":"<RFC 3339 UTC>","task","reason"}`. Existence is the signal, so a damaged marker still stops the line. It is written before the ledger saves the `stopped` ship state. |
| `.afk/build/ship-poll` | the daemon | `{"at":"<RFC 3339 UTC>","failing_since":"<RFC 3339 UTC or empty>"}`, both keys always present. `at` is when GitHub was last asked (the ship asks at most every 20 seconds); `failing_since` is set while those reads keep failing and cleared when one works. A missing or damaged file means "never asked". |

Amended 2026-10-12 (T2.2a): two more files in `.afk/build/`, written only by the daemon through
`internal/daemonstate`, by atomic rename, and a new key in `running.json`.

| File | Writer | Shape |
|---|---|---|
| `.afk/build/after-merge.json` | the daemon, around each run of the `after_merge` command (written once at the start and once at the end) | `{"task","merged_sha","started_at","finished_at","exit_code","timed_out"}`, every key always present, times RFC 3339 UTC. A run that has started and not finished has `finished_at` `""`. It is matched on the task and the merge commit, so a record of an earlier task is ignored. |
| `.afk/build/after-merge.log` | the command's output, opened by the daemon for each run | Plain text, replaced on each run. The first line is `afk: running the after-merge command in <dir>: <command>` and the last is `afk: the command finished with exit code N`, `afk: the command was stopped after <timeout>` or `afk: the command could not be started: <error>`. |

`running.json` (in afk's state folder) also holds `"binary_unknown"`, a boolean written `false` by every
lock and set to `true` when the daemon could not read its own program file at the start. `afk daemon
status` reads it to say the daemon will not restart by itself on a new version.

Amended 2026-10-12 (T2.4): one more file in afk's state folder (beside `usage.json`), written only by the
daemon through `internal/daemonstate`, by atomic rename.

| File | Writer | Shape |
|---|---|---|
| `governor.json` | the daemon | `{"probe_tried_at","login_checked_at","login_known","logged_in"}`, every key always present. The two times are RFC 3339 UTC or `""` for never. `probe_tried_at` is when a usage read was last attempted (stamped before the attempt, so a failing read is not tried on every pass). `login_known` says whether `logged_in` is a real answer; a check that could not run leaves the last answer. A missing or damaged file means "never tried" and "not known". |

Amended 2026-10-13 (T5.3): `.afk/master-plan/planning` is an empty file written by `afk plan` (on every
start, because cleaning the session removes its folder). Its existence makes every spawn of that
session's host, a relaunch from the dashboard included, run the provider's planning skill instead of
the conductor or a bare agent. Only its existence is read.

Amended 2026-10-13 (T5.7): `plan-reviews.json` is a file in afk's state folder (beside `usage.json`),
the plans waiting for the owner's approval. Writers: `afk plan advance` adds an entry when a `new`
session reaches `review` (through `internal/daemonstate`, by atomic rename), and `afk plan approve`
removes it. Readers: the daemon's web page, which serves the plan view for an entry whose project is
not in `config.toml` yet. Shape: `{"reviews":[{"name","root","at"}]}`, the list always present and every
key always written. `root` is the project's main checkout, cleaned, and the key an entry is matched by;
`name` is the folder name the page serves the plan under (`/p/<name>/plan`), with `-2`, `-3` added when
two roots share a name, and it does not change when the plan is put up again; `at` is when it was last
put up, RFC 3339 UTC. A missing or damaged file reads as an empty list.

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

Amended 2026-10-12 (T2.4): the file also holds a `governor` object, `{"action","reason","wake_at","at"}`:
the governor's decision for the reading (`run`, `no_launch`, `pause`, `hold` or `refuse`), the reason in
plain words, when to wake a paused session (omitted when none is known) and when the decision was made.
The daemon and `afk usage` both store it with the reading. A file without the key reads as no decision.

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
after_merge = ""                # shell command run in the main checkout after a merge (T2.2a)
after_merge_timeout = "10m"

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
committed and `afk.local.toml` is gitignored.

Amended 2026-10-11 (T2.1): the overlay is in. `internal/projectconfig` lays `afk.local.toml` over
`afk.toml` key by key (a key set in the local file wins, even to an empty value), and this repo's
`.gitignore` now lists `afk.local.toml` and no longer lists `afk.toml`. A project that already has an
`afk.toml` holding an `ntfy_url` must move that line to `afk.local.toml` before committing the file.

Machine config `~/.config/afk/config.toml`: projects the daemon runs, governor thresholds (§6.3), web
bind address and token, provider choice.

Amended 2026-10-13 (T5.7): the `[[project]]` list is edited by `afk project add` and `afk project remove`
and by `afk plan approve`, and the running daemon reads it again on every pass. It is the only setting
that is: the tick, `[daemon]`, `[governor]`, `[web]` and `provider` are read once at the start and need
a restart. A daemon starts with no projects and waits; a config that cannot be read mid-run leaves the
list it had.

Amended 2026-10-11 (T2.1): T2.1 reads these keys from `config.toml` (in `$XDG_CONFIG_HOME/afk` when
that is an absolute path, otherwise `~/.config/afk`): `provider` (the agent's name; empty means the
default), `[daemon] tick` (default `"5s"`, at least `"1s"`), `[daemon] stall_after` (default `"10m"`)
and one `[[project]]` per project with `path` (absolute, or starting with `~/`) and an optional `name`
(defaults to the folder name, and must be unique). Unknown keys are ignored, so later tasks add the
governor and web keys to the same file. The dashboard's `config.json` is a separate file with its own
keys (`search_roots`, `scan_interval`, `max_depth`); the two are not merged.

Amended 2026-10-11 (T2.2): `[ship]` is read by `internal/projectconfig` (from `afk.toml`, then
`afk.local.toml` over it, key by key) with these defaults when a key is absent: `merge_method`
`"merge"`, `ci_reruns` 1, `ci_fix_cycles` 2, `deploys` true, `deploy_timeout` `"15m"`, and `base`,
`health_url` and `required_env_pattern` empty. An empty `base` falls back to the session's own base
branch (`base_branch` in its `status.json`); if that is empty too, the task parks and nothing is
guessed. The deploy is polled only when `deploys` is true and `health_url` is not empty; with no
`health_url` the ship goes straight from the merge to `deploy_healthy`. `health_url` must be empty or
an `http://` or `https://` address, `merge_method` one of `merge`, `squash` or `rebase`, the two counts
at least 0, `deploy_timeout` above zero, and `required_env_pattern` a regular expression that
compiles; anything else is an error naming the file and the key, and the address is never echoed.

Amended 2026-10-12 (T2.2a): `[ship]` also reads `after_merge` (default empty: no command; any other
value is the text of a shell command, run through `sh -c` in the main checkout) and
`after_merge_timeout` (default `"10m"`; a length of time above zero, else an error naming the file and
`[ship] after_merge_timeout`). The command's text is never echoed in an error, a stop reason, an item
title or a notification; the log tail in a failure item may show it.

Amended 2026-10-12 (T2.3): `[daemon]` also reads `max_restarts` (default 3; a whole number of
recoveries per task, at least 0, where 0 means never restart; a negative value is an error naming
`daemon.max_restarts`) and `restart_backoff` (default `"1m"`; a length of time above zero, else an
error naming the key). They are machine config only; `afk.toml` is not read for them.

Amended 2026-10-12 (T2.4): `config.toml` also reads a `[governor]` table: `pause_at` (default 90; above 5
and at most 100), `launch_below` (default 75; above 0 and at most `pause_at`), `stale_after` (default
`"30m"`; above zero and longer than `probe_interval`), `probe_interval` (default `"5m"`; at least `"1m"`)
and `on_unknown_usage` (default `"run"`; `"run"` or `"hold"`). Anything else is an error naming the file
and the key, and `afk daemon` and `afk usage` both read the table. The weekly launch line is
`pause_at` minus 5.

Amended 2026-10-13 (T2.5): `[daemon]` also reads `max_wakes` (default 3; a whole number of times per task
that afk types the session's resume command into a session whose agent's turn failed, or that has sat
waiting at the terminal; at least 0, where 0 means never wake a session; a negative value is an error
naming `daemon.max_wakes`). It is machine config only, like `max_restarts`: an absent key reads as the
default, and a written 0 reads as 0.

Amended 2026-10-12 (T3.1): `config.toml` also reads a `[web]` table: `bind` (default `"127.0.0.1:4253"`;
`host:port` with a port from 1 to 65535), `token` (default empty, which turns the page off and opens no
port; at least 16 characters, no white space) and `public_url` (default empty; an `http://` or `https://`
address with a host and no `?` or `#`, trailing slashes dropped). A `bind` that is not loopback (an
IP literal that is loopback, or `localhost`) with no token is an error naming the file and `web.bind`
and `web.token`; any other bad value is an error naming the file and the key, and none repeats the
token. With a token every route, `/api/` included, answers 401 without it. The token is accepted as the
`afk_web` cookie (set, as a hash of the token, by a `?token=` visit that then redirects without it), an
`Authorization: Bearer` header or `?token=`. A notification for an inbox item carries a `Click` link to
`<public_url>/p/<project>/inbox/<id>` only when the page is on and `public_url` is set, and never the
token.

Amended 2026-10-13 (T5.2): `profile.toml` is the owner profile. It sits in the same folder as `config.toml` (absolute `XDG_CONFIG_HOME` or `~/.config`, the same rule). It reads `rule_files` (a list; each item an absolute path or one starting with `~/`) and `[[entry]]` tables with `topic`, `value`, `strength` (`rule` or `default`; absent reads as `default`), an optional `kind` (lower-case letters, digits and hyphens) and an optional `reason`. Topics compare with case and runs of white space ignored, and are stored as written. There is one entry per topic and kind, and a kind-specific entry beats a general one on the same topic. An unknown key is a warning. No secrets: a value that looks like a key or token is an error and is never printed. `afk profile set` is the only command that edits the file, one `[[entry]]` at a time as text so comments survive, and `afk profile init` creates it once from a commented template.

Amended 2026-10-13 (T5.4): `afk.toml` also reads `[planning] via_pr` (a boolean, default false; a value of
another type is an error that names the file and the key). The table is `planning` because `plan` is
already the key for the master plan's path, and one TOML key cannot be both a string and a table. The
M5 decision's `[plan] via_pr` means this key; a `[plan]` table is refused with a message that names
`[planning]`. With it set, `afk plan patch` makes each change on the branch `afk-plan-changes` in a
temporary worktree and opens a pull request, and the main checkout is not touched.

Amended 2026-10-13 (T6.2): `config.toml` also reads a `[dash]` table for the dashboard's folder search:
`search_roots` (array of strings, default none; `~/` expands and a relative folder is taken from the
folder `afk dash` was started in), `scan_interval` (a duration, default `"60s"`, at least `"1s"`) and
`max_depth` (a whole number, default 3, at least 1). `daemonconfig.Load` carries a bad `[dash]` value
(or a value of the wrong kind) as `Config.DashErr`, with `Config.Dash` back at the defaults, and never
returns it as the load error, so the daemon and every other reader of the file are unaffected; only the
dashboard's folder search reports it, in a toast that names the file and the key. The sentence about a
separate `config.json` in the T2.1 amendment no longer holds: `config.json` is no longer read, and
`afk dash` says once per start what to move.

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

### 4.8 Plan session — .afk/plan/ (new)

Amended 2026-10-13 (T5.3): the state of a planning session (`afk plan`) lives in `.afk/plan/` in the
project's main checkout. It is gitignored. The agent itself runs as the session `master-plan`, whose
own folder is `.afk/master-plan/`, so cleaning that session never deletes the planning state; `plan`
and `master-plan` are refused as names for ordinary sessions. `internal/plansession` reads the folder.

| File | Writer | Holds |
|---|---|---|
| `state.json` | `afk plan` seeds it; `afk plan advance` is its only other writer | `{mode, phase, updated_at}` |
| `brief.md` | the planning session | The owner's request, word for word. |
| `survey.md` | the planning session | What was read before asking, and the profile entries that apply. |
| `questions.jsonl` | the planning session | One line per question. |
| `slices.md` | the planning session | The task breakdown the owner confirms before the draft. |
| `critic.md` | the planning session | The critic's findings on the draft and what was done about each. |

`state.json` holds `mode` (`new`, `extend`, `amend` or `reconcile`), `phase` (the furthest phase the
session was allowed to reach) and `updated_at` (RFC 3339, UTC). An unknown key, mode or phase is
refused. A missing file means there is no planning session; a damaged one is an error, never read as
"no session". The phase shown by `afk plan status` is the earliest phase whose gate is not met, never
later than the recorded one, so a deleted file pulls the session back.

A `questions.jsonl` line has the keys `id`, `round`, `kind` (`product` or `setup`), `question`,
`options`, `recommended`, `answer` and `answered_at`. The line is written when the question is asked and
rewritten with the answer; an empty `answer` means it is still open.

The gate of each phase, as built (`afk plan advance` refuses to leave a phase until it is met):

| Phase | Gate |
|---|---|
| `brief` | `brief.md` exists and is not empty. |
| `survey` | `survey.md` exists and is not empty. |
| `model` | None. Held by the recorded phase alone. |
| `rounds` | `questions.jsonl` exists, has at least one question, has no faulty line, and every question has an answer. |
| `slices` | `slices.md` exists and is not empty. |
| `draft` | The master plan exists and is not blank. |
| `check` | `afk plan lint` reports no errors on the plan, and `critic.md` exists and is not empty. |
| `design` | Not built. `afk plan advance` goes from `check` to `review`. |
| `review` | None. Approving the plan is not built yet. |

Amended 2026-10-13 (T5.4): a session that changes a plan that exists (`afk plan extend`, `amend` or
`reconcile`) adds two files to `.afk/plan/` and one folder. `changes.md` is written by the planning
session: one line for each change it made to the plan, and the `draft` gate of the three change modes
is that it exists and is not blank (a reconcile that changes nothing still writes one line). `yes` is an
empty file written by `afk plan amend --yes`, which tells the session not to ask the owner to confirm
the entry first. `archive/<time>/` holds the files of an earlier, finished session, moved there when
the next change session starts. Phase paths and gates: `extend` is `brief`, `survey`, `rounds`,
`slices`, `draft`, `check`, `review`, with the gates above except `draft`, and its `check` needs
`critic.md`; `amend` is `brief`, `survey`, `draft`, `check`, `review`, and `check` needs only a plan
with no lint errors; `reconcile` is `brief`, `survey`, `rounds`, `draft`, `check`, `review`, its
`rounds` gate passes with no `questions.jsonl` at all (a faulty line or an unanswered question still
holds it), and `check` needs no critic. A change session is finished at `review`, because its changes
are already in the plan (in a pull request until it is merged, with `[planning] via_pr`).

Amended 2026-10-13 (T5.5): the `design` phase is built. The `rounds` gate of a `new` session also needs
a question with the id `r1-interface` whose answer starts with `yes` or `no`. `afk plan advance` goes
from `check` to `design` on yes and to `review` on no; change sessions have no `design` phase. The
`design` gate is: no checker errors, `docs/design-brief.md` is not blank, the plan's header states the
precedence rule (the plan wins on behaviour, `docs/design-system.md` wins on looks), a task titled
`Design system (owner)` is in the first milestone, and at least one task waits for it. The brief is in
`docs/`, not `.afk/plan/`, because it is a project document the owner takes to a design tool. This
supersedes the `design` row above.

Amended 2026-10-13 (T5.7): the `review` row above now has no gate and `afk plan approve` is its only way
on. A `new` session at `review` that `plansession.Approve` accepts (no checker errors, not a change
session) is moved to the tenth phase, `approved`, and only by approve: `afk plan advance` still refuses
to leave `review`. `approved` is written last, after the other steps, so on disk it means a project the
daemon is building. Approve commits only the project's documents, by path: the plan, `afk.toml`,
`.gitignore`, `CONTEXT.md`, `docs/design-brief.md` and the agent instruction files `AGENTS.md` and
`CLAUDE.md` (whichever exist), with the subject `docs(plan): Approve the master plan`. It then pushes
the current branch to `origin` unless `[planning] via_pr` is set, there is no origin or the checkout is
on no branch.

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

Amended 2026-10-11 (T2.2): a task whose title carries `(owner)` or `(owner; …)` (case does not
matter, and spaces inside the brackets are allowed) is the owner's to do and is never launched.
`(owner + agent)` is an ordinary task, and so is prose such as `(owner approval)`.

Amended 2026-10-13 (T5.6): the daemon reads a third thing from the plan, the Open questions table (the first
table under that heading with a "Needed by" column), through `masterplan.Questions`, the same reader
`afk plan lint` uses. A row is open until its Question cell holds `**Answered <date>:** <answer>`.

### 5.2 Next task (pure function)

Inputs: parsed tasks, ledger, open inbox items. Output: one task id, or a reason there is none.

- Ready = `pending`, with every dependency `done`. A `superseded` dependency is satisfied when every
  task in its `replaced_by` is `done`; a `failed` or `parked` dependency is not satisfied.
- Among ready tasks, plan order wins.
- A `parked` task whose blocking items are all answered becomes ready again and goes first.
- Nothing ready: report why (`all done` · `waiting on owner` with the item ids · `blocked by failed
  T…`). "Waiting on owner" and "blocked" each raise one notification, not one per tick.

Amended 2026-10-11 (T2.2): an owner task (§5.1) that is `pending` and ready is chosen like any other,
but the daemon raises one blocking item (`Your task: T0.0 — …`) and parks it instead of launching;
the owner's answer marks it `done`. A project with no ledger file launches nothing and raises one
blocking item, `This project has no task list yet`, until `afk ledger init` creates the file.

Amended 2026-10-13 (T5.6): when the task chosen is named in the "Needed by" cell of an open row of the Open
questions table (§5.1), `Decide` returns `question_hold` and starts nothing: it raises one blocking `question`
item per row (`Open question: …`, found again by its body's first line) and parks the task. Owner tasks are held
the same way, before the owner rule. A held task whose items are all answered gets `question_answered`: the
daemon writes each answer into its row through a guarded plan patch and sets the task from `parked` to
`pending`, never `running`. A row answered by hand closes its item. A row that names no task holds nothing.

### 5.3 One task

1. Governor says launching is allowed (§6.3), else wait.
2. Create the session: branch `afk/t4-3`, its worktree, seeded `status.json`, and a description of
   `Implement T4.3 from docs/PLAN.md`. Ledger → `running`.
   Amended 2026-10-11 (T2.1): the session and the branch are both `t4-3` (the task id in lower case
   with the dot turned into a hyphen), not `afk/t4-3`. A session's name, its branch and its worktree
   folder are one name everywhere else in afk (`afk attach t4-3`, `.afk/t4-3/`), and a name with a
   slash is not a valid session name. A later attempt at the same task appends `-2`, `-3` and so on.
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

   Amended 2026-10-12 (T2.3): the stopped-host item ("Session stopped running: …") is now raised only
   when recovery is not available (the loop has no means to relaunch a host); when recovery is available
   and has used its `max_restarts`, the task parks on a "Session could not be recovered: …" item
   instead (§5.6). A stall item, like both, now parks the task, so answering it un-parks the task.

   Amended 2026-10-13 (T2.5): a session that is asking something at the terminal for longer than
   `stall_after` is first sent its resume command, once. The wait is measured from the latest of the
   session's last status write, its start, its last restart and its last wake. The command is typed only
   when afk can type into a session, `max_wakes` is above 0, the session's wakes are not used up, no usage
   limit is what it hit, and no earlier line is still owed (`continue_pending`). If the session is still
   waiting `stall_after` after that wake, the item above is raised. When the wake is not sent, the item is
   raised at `stall_after` as before, and its text says why. The wake shares its count with a failed turn
   (§5.6).

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

Amended 2026-10-11 (T2.2): as built, the saved states are `committed`, `pushed`, `pr_open`,
`handed_back`, `ci_green`, `merged`, `deploy_healthy` and `stopped`. `handed_back` is a red CI or a
conflict given to the session, with a new commit awaited for up to an hour (then the task parks);
the daemon pushes that commit itself once it is two minutes old. `stopped` is a deploy that did not
turn healthy: the daemon writes `.afk/build/stopped` first, then saves the state, and `afk resume`
removing the marker is what lets the task finish as `done`. A pull request with no checks, or only
skipped ones, is never merged: five minutes after it opened the task parks, and answering the item
waits another five. After the merge the daemon fast-forwards the local base branch (never a merge
commit) and deletes the remote branch before it looks at the deploy. A pause does not stop a ship
that is already in progress.

Amended 2026-10-12 (T2.2a): as built, the saved states gain `after_merge`, between `merged` and
`deploy_healthy`, and only when `[ship] after_merge` is set. In it the daemon writes the run record
(`.afk/build/after-merge.json`, §4.2a) before it starts the command and again when the command ends,
runs the command once in the project's main checkout with a limit of `after_merge_timeout`, and saves
nothing to the ledger while it runs. Exit 0 moves the ship to `deploy_healthy`; the deploy timeout is
measured from the end of the command, not from the merge, so a slow command does not use up the deploy's
time. A non-zero exit, a command that could not be started, or a timeout stops the line: the marker
`.afk/build/stopped` is written, a `failure` item carries the end of `.afk/build/after-merge.log`, and
`afk resume` then lets the task finish as `done` without running the command again. A crash during the
command leaves a record with a start and no finish: the next pass stops the line with an item saying it is
not known whether the command finished, and never runs it a second time. A crash before the start mark
leaves no record, so the command runs once. If the owner removes the command from `afk.toml` while the
ship waits in `after_merge` with no record, the ship carries on to the deploy check.

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

Amended 2026-10-12 (T2.3): how recovery was built.

- The host probes by itself when it is started with `--probe-unattached`, which every unattended
  session's host gets. It publishes `wedged_since` in `host.json` after about 35 seconds of silence and
  an unanswered repaint request, and clears it on any output. The daemon only reads that field; it never
  dials the host to find out, and never holds the client slot, so a human attaching can always take the
  session.
- A wedge verdict must have stood for a minute before the daemon acts, because it clears on any output
  and a restart interrupts the agent. A host that is gone must have been gone for the one-minute grace.
- To restart, the daemon asks the host with a short connection (a restart request, with the host's
  refusal returned as text); the host runs the stop ladder and starts the provider's resume command.
  To relaunch a gone host, the daemon starts a new host with the resume command when `status.json` has
  a session id, and a fresh launch of the same session when it does not (or the file cannot be read),
  which picks its work up from the files.
- The continue prompt is the session's resume command (`/afk:go --session <name>`) typed 30 seconds
  after the new agent started, with `continue_pending` saved first and cleared after. Typing it again is
  safe because the conductor acts from its recorded phase.
- At most `max_restarts` recoveries per task (default 3), the first at once and each later one after
  `restart_backoff` doubled each time (1m, 2m with the defaults). `restarts` is saved before the action
  and counts a refused or failed attempt. At the limit the task parks on an item titled "Session could
  not be recovered: …"; answering it gives a fresh set.
- Parked and shipping tasks are not recovered; a pause does not stop the recovery of a running one.

Amended 2026-10-13 (T2.5): a failed turn is answered by a wake, which types the resume command and
restarts nothing. The rule (`decideWake`) runs after recovery and before the stall rule. It types the
command only when all of these hold: the host is running with no wedge verdict, the executor can type,
no continue prompt is pending, the `.turn-failed` marker is later than the attempt's start, its last
restart, its last wake and the owner's last answer to an item about the task, and the marker is not
behind a rate-limit marker of the same run. Then the marker must be at least 30 seconds old (`WakeAfter`,
not a setting), and the last wake at least the backoff old: `restart_backoff` for the second wake, doubled
for each one after. The count is `wakes` and the time is `wake_at` in the ledger entry (§4.2). A wake is
not a restart, so it does not touch `restarts`. At `max_wakes` (default 3; 0 means never wake) the task
parks on a blocking `failure` item titled "Session kept failing: <task> (<session>)". Answering that item
clears the wakes (the un-park does it, as for recoveries) and types the resume command.

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

Amended 2026-10-12 (T2.4): as built, the daemon asks the governor only on a pass with a running or
shipping task, or one where the loop is about to launch, create again or un-park a task, or while the
extra-usage or login item is open; an idle project causes no probe. A reading younger than
`probe_interval` is reused, and a failed probe is not tried again for a minute. `no_launch`, `hold` and
`refuse` stop every start (an un-park, too, except that at `no_launch` a parked task may carry on); `pause`
stops the running session. `refuse` also stops a running session, which the table above does not say,
because an agent left running could spend real money. The decision is stored in `usage.json` (§4.5) and
a change of it is written to the decision log (§4.4), not every reading.

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

Amended 2026-10-12 (T2.4): the spike was run and the answer is to end the session's agent and start it
again later. The daemon saves the pause in the ledger (§4.2), asks the session's host to end its agent
(a minute later it sends the host a terminate signal, and after three minutes it raises a blocking item
and parks the task), and after the reset starts a new host with the agent's resume command and types the
session's resume command 30 seconds later. The other ways tried (Escape, Ctrl+C, freezing the process)
and the evidence are in `docs/decisions.md` under `## T2.4`. The `StopFailure` marker is read by the
daemon, which pauses the session when it is newer than the session's start and last restart.

Amended 2026-10-13 (T2.5): the `StopFailure` hook (`plugins/afk/hooks/on-stop-failure.sh`) writes one of
two markers, picked by the payload's `error`. `rate_limit` writes `.rate-limited`, the marker above. The
retryable reasons `overloaded`, `server_error` and `unknown` write `.turn-failed` as `{at, reason}`. The
other reasons write nothing: logins, billing, bad requests and a missing model cannot be fixed by trying
again. The hooks.json matcher is the same list, and the script checks `error` as well, so a wider matcher
cannot write a marker the list does not allow. A rate limit is never a failed turn: it writes only
`.rate-limited`, and the wake rule (§5.6) stands back from a rate-limit marker of the same run. A lost
connection reports `server_error` (captured 2026-10-13). The stall rule (§5.3) sees a failed turn only
when the session shows as waiting, which an agent in `executing` does not, so the marker is what catches
a failed turn.

### 6.5 Login expiry

Check `claude auth status` hourly. Logged out, or (if detectable) expiring within 3 days → a blocking
`failure` item and a high-priority notification. Nothing launches while logged out.

Amended 2026-10-12 (T2.4): as built, the check runs hourly, and every `probe_interval` while logged out
so that a new login is noticed soon. A check that cannot run keeps the last answer. Expiry is not
detectable: the status output of `claude auth status` has no date, so there is no early warning, only the
logged-out state. Logged out raises one blocking `failure` item in every project with an urgent
notification, starts nothing and keeps a paused session paused; a running session is left alone. The
daemon answers the item itself on the first check that finds the agent logged in.

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
 T0.6 dash focus

──▶ M5 Master planning (added 2026-10-13, after M4)
 T5.1 format and lint     T5.4 extend, amend, reconcile    T5.7 approve and go
 T5.2 owner profile       T5.5 design hand-off             T5.8 prove it
 T5.3 planning session    T5.6 open-question gates
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

#### T0.6 — Bring over forge's dashboard focus rebuild
- **Depends on:** T0.4, T0.5
- **Goal:** `afk dash` has forge's two-pane focus model, so a key or a click always goes where the
  screen says it will.
- **Background:** afk was copied from forge at `9c847e1` (`docs/provenance.md`). One commit has landed
  in forge since: `d9c6a62`, "Rebuild dashboard focus as two panes" (forge pull request #1, merged
  2026-10-12). The old model kept four focus flags that could disagree, so keys reached the wrong
  place (a steal or a dropped connection sent the user's typing into dashboard bindings such as `q`),
  `enter` raced the 300ms auto-preview, and the dashboard held a session's one client slot for a pane
  nobody could see. The new model is one focus (list or right side) and one view for the right side
  (agent or overview). The owner uses `afk dash` to watch and unblock the daemon's sessions, and T2.3
  and T3.2 both build on this code, so it goes in before them. The forge change was covered by tests
  but its author had not yet walked through the dashboard by hand, so that falls to this task.
- **Scope:** a port, not a redesign. Run `git -C ~/code/forge-cli fetch`, then read
  `git -C ~/code/forge-cli diff 9c847e1 d9c6a62` (35 files: `dash.go`, `dash_test.go`, `README.md`,
  and the rest in `internal/ui`, with its golden files). Apply it here with afk's names (`afk`,
  `.afk/`, `/afk:go`). afk's own changes to these files since the copy are small and must survive:
  T0.4's detach fix in `internal/ui/hostpane.go` and its tests in `hostpane_test.go`, and whatever
  T2.3 has merged by the time this task starts. The behaviour to end up with, as forge's README
  section on `dash` now states it:
  - `enter`/`f` moves focus to the right side, and connects a running session that has no pane yet.
    `ctrl+\` returns to the list, and so does `esc` from the overview.
  - `ctrl+t` switches the right side between agent and overview, only while the right side has focus.
  - With the list focused, nothing reaches the right side, by key or by mouse; a click there focuses
    it.
  - A row with no live agent shows its overview. `enter` never restarts a host; `r` does, from either
    side, at once for a stopped host and after a confirmation for a wedged one.
  - A steal or a dropped connection while the right side has focus keeps focus there and shows a
    message saying what happened; `enter` reconnects.
  - The pane always belongs to the selected row.

  Also fix the two quality findings forge's own review left open: the arrival toast says "input goes
  to X" for a click-to-focus whose focus was not honoured (gate that wording on the focus having been
  honoured), and `agentPlaceholderText` spells the "restart in flight" test inline in place of calling
  `restartingRow`. Update `README.md` and `dashUsage` to match, and add the forge commit to
  `docs/provenance.md`.
- **Must not:** change the frame protocol, `internal/host`, the status schema or the daemon; delete a
  test for behaviour that still exists (a test of removed behaviour is rewritten for the new
  behaviour, as forge did); bring over anything else from forge; name a provider outside
  `internal/provider` (forge's text does in places, so check what is carried over).
- **Acceptance:** `make check` green, including forge's new key-sequence matrix
  (`internal/ui/focusmodel_test.go`) and the regenerated golden files. `grep -ri forge` over tracked
  files still returns only deliberate provenance notes. None of the four old flags is left in
  `internal/ui` outside comments. New cases for the two quality fixes. Then a walk-through against a
  built binary (`go build -o /tmp/afk-check .`, in a throwaway repo, driven through tmux) with the
  screens saved as evidence: each bullet above, with one running and one stopped scratch session; a
  second `afk attach` stealing the session while the pane has focus, after which typed keys do not
  reach dashboard bindings; and `afk dash --open <name>`. Anything the walk-through finds that forge
  has too is fixed here and listed in `docs/decisions.md` so it can go back to forge.
- **Amended 2026-10-12 (T0.6 done):** later tasks may assume the following. (1) The dashboard has one focus (`paneFocus`: `focusList` or `focusRight`) and one right-side view (`rightView`: `viewAgent` or `viewOverview`); the four old flags are gone, and `internal/ui/focusmodel_test.go` holds the key-sequence matrix a new key or view should add rows to. (2) A pane is drawn only for the selected row, so anything that opens a session by name (`afk dash --open`, the hand-off from `afk new`) moves the cursor to it first; that was a bug forge still has, fixed here with a second one (messages naming `enter/f` where only `ctrl+t` works, now chosen by `agentKeyHint`). (3) Four things were seen and left for forge to decide, the main one being that when a session's host dies under a focused pane the row turns to its overview about two seconds later and `q` typed then quits the dashboard; T3.2 should not assume a dead row swallows keys. (4) `afk dash` can be driven end to end through tmux with a stand-in script in place of the agent; the recipe and every finding are under `## T0.6` in `docs/decisions.md`.

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
- Amended 2026-10-11 (T1.5 done): later tasks may assume the following. (1) The plugin (5.2.0) ships agent definitions in `plugins/afk/agents/` (`afk:task`, `afk:verifier`, `afk:reviewer`, `afk:qa`, `afk:research`, `afk:web-research`); the workflow scripts and the skills spawn every agent with one, and a refused type is retried once without it. A task agent now has only `Bash`, `Read`, `Edit` and `Write`: no skills, no connector tools. (2) A sub-agent starts at about 10,000 to 12,000 tokens (QA about 21,000), down from about 35,000; the main session's own first call, about 120,000 tokens, is now the largest start-up cost and is untrimmed. (3) `provider.TokenRecord.First` marks each agent's first call and `afk tokens SESSION` prints a start-up section from it; the ledger file is unchanged. (4) `scripts/startup-context.sh [PROJECTS_DIR] SESSION_ID` itemises a session's start-up context. (5) A session started from an agent's shell inherits `CLAUDE_CODE_CHILD_SESSION=1` and saves no transcripts, and a new repository stops at the agent CLI's trust question, so T2.1 must launch from a clean environment and T4.1 must have the owner trust the project folder once. (6) Use allow lists in agent definitions; a deny list that removes `ToolSearch` loads every connector tool in full. See `## T1.5` in `docs/decisions.md`.

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
- Amended 2026-10-11 (T2.1 done): later tasks may assume the following. (1) Session and branch are both the task id in lower case with a hyphen (`t4-3`, then `t4-3-2` on a later attempt), not `afk/t4-3`. (2) `internal/daemon` is a pure `Decide(Observation) Action` plus `Tick`/`Once`/`Run` with every outside call in `daemon.Deps`; there is no daemon object and nothing is kept between ticks, so T2.2 to T2.4 add a rule to `Decide` and a dep, not state. (3) At `phase: done` the ledger goes to `shipping` and stays there; `shipping` blocks every other launch for the project until T2.2 moves it (by hand: merge, then `afk ledger set <id> done`). (4) The provider has `Unattended(argv)` and `UnattendedPreflight`; a session is unattended when `session.Config.Unattended` is set or `.afk/{session}/unattended` exists, and the host's restart and the dashboard's relaunch keep the mode, so T2.3 need not pass it. (5) The daemon cannot type into a session: an un-parked task returns to `running` but its agent stays idle until T2.2 delivers a prompt, and a stall or a stopped host is reported once per attempt as a blocking `failure` item the task parks on (T2.3 replaces the stopped-host item with recovery). (6) Machine config is `config.toml` (`provider`, `[daemon] tick`, `stall_after`, `[[project]] path`), read by `internal/daemonconfig`; T2.4 adds its governor keys there. (7) `internal/decisionlog.Append` writes PLAN §4.4 lines and the daemon logs transitions only; `tokens.Record` and `notify.InboxMessage` are the shared helpers; `daemonstate` owns `.afk/build/paused`, `.afk/build/daemon.json` and the machine lock. (8) `afk pause` also holds back an un-park, and `afk resume` only removes the pause flag (T2.2 adds clearing a stopped line). (9) The daemon's real wiring (`realDaemonDeps`: preflight, `session.Create`, token recording, ntfy) has never run against a real agent; its first run is supervised (T4.2). See `## T2.1` in `docs/decisions.md`.

#### T2.2 — Ship in code
- **Depends on:** T2.1
- **Scope:** `internal/ship` implementing §5.4 as a persisted state machine over `git` and `gh`
  interfaces. Delivering a follow-up prompt to an idle session (for CI hand-backs) through the host's
  client protocol; note that this takes the session's single client slot, as the dashboard's restart
  does today. Session clean-up after `done`.
  Two gaps found on the daemon's first real run (2026-10-11), when it was started on this repo with
  no ledger, took all 19 tasks as pending and launched a session for T0.0, the owner's VM checklist:
  - **Owner tasks are never launched.** A task whose title carries `(owner)` or `(owner; …)` is the
    owner's to do. When it is the next task, the daemon launches nothing: it raises one blocking
    `external_action` item naming the task and parks it, and answering the item marks the task
    `done`. A title such as `(owner + agent)` is an ordinary task. Add the rule to §5.1 and to the
    pure next-task and `Decide` functions.
  - **Starting on a project that is already part-built.** New command `afk ledger init [--project
    DIR] [--done-through ID] [--done ID,ID]`: creates the ledger, marking as `done` every task up to
    and including `--done-through` in plan order, plus the listed ids; with no flags it creates an
    empty ledger (a new project). It refuses when a ledger already exists. `afk ledger set` accepts
    several task ids before the status. With **no ledger file**, the daemon launches nothing for that
    project and raises one blocking item that says to run `afk ledger init` and shows both forms.
- **Acceptance:** table tests over fake `git`/`gh`: green first time; red then green on re-run; red →
  hand-back → green; park at the hand-back cap; required-env match parks before merge; deploy healthy;
  deploy timeout stops the line and `afk resume` clears it; a crash between every pair of states
  resumes without repeating a push or a merge. Table tests for: an owner task raises an item and
  launches nothing, and its answer marks it `done`; `(owner + agent)` launches; no ledger file
  launches nothing and raises one item, once; `afk ledger init` with each flag, with both, with none,
  with an unknown id, and over an existing ledger. Then one real end-to-end run against a throwaway
  private GitHub repo with a trivial CI workflow, recorded in `docs/decisions.md`.
- Amended 2026-10-12 (T2.2 done): later tasks may assume the following. (1) `internal/ship` is a pure `Decide(Facts) Step` plus `Advance`, one step per daemon pass, behind `Git` and `GH` interfaces; the daemon reaches it through the optional `Deps.Ship`, `SendPrompt`, `Clean` and `NotifyUrgent`. (2) The ledger entry carries `ship`, `ship_at`, `ci_reruns` and `ci_fix_cycles` while its status is `shipping`; a new attempt clears them with `pr` and `merged_sha`. (3) The daemon can type into a session with `host.SendInput` (it takes the client slot for about half a second); T2.3 can use it for its "continue" prompt, and an un-parked session is sent its resume command. (4) After a merge the daemon fast-forwards the project checkout's base branch, cleans the session and deletes the merged branch. (5) A pull request with no checks, or a cancelled check, is never merged; the task parks. (6) A stopped line is `.afk/build/stopped`, cleared by `afk resume` together with a pause; a pause does not stop a ship in progress. (7) With no ledger file the daemon starts nothing: T4.1 seeds nebari with `afk ledger init --done-through`. Owner tasks (`(owner)` or `(owner; …)` in the title) are confirmed in the inbox, never launched. (8) Not yet seen: a real agent receiving a typed line, a real deploy check, a park or a stopped line against GitHub; the supervised run of T4.2 is the first. See `## T2.2` in `docs/decisions.md`.

#### T2.2a — Pick up new code after a merge
- **Depends on:** T2.2
- **Goal:** while afk builds itself, each merged task is installed and running before the next one
  starts, with nothing done by hand.
- **Background:** a running daemon keeps the code it started with. After every merge to this repo the
  owner has had to run `make install`, `afk plugin install --target claude` and `systemctl --user
  restart afkd`. A project that is not afk itself needs none of this.
- **Scope:**
  - **`after_merge`.** New key under `[ship]` in `afk.toml` (§4.6): a shell command, empty by
    default. The ship state machine runs it in the project's main checkout after the merge and the
    fast-forward of the base branch, and before the deploy check. It is a persisted ship state like
    the others, so a crash never runs it twice for one merge and never skips it. Its output goes to a
    file under `.afk/build/`. A non-zero exit or a timeout (`after_merge_timeout`, default `10m`)
    stops the line (§5.4) with a blocking `failure` item carrying the end of the output.
  - **Restart on a new binary.** At start the daemon records the identity of its own executable
    (size and modification time, or a hash). At the top of each pass, if the file on disk differs, it
    writes a decision-log line and exits 0; the service manager starts the new one. It exits only
    between passes, never in the middle of a ship step. Unknown or unreadable executable: carry on
    and say so once in `afk daemon status`.
  - **The unit file.** `contrib/afkd.service` changes to `Restart=always`, keeping `RestartSec=30`
    and `KillMode=process`; `contrib/README.md` says why, and that a daemon run by hand in a terminal
    simply exits on a new binary and must be started again.
  - **This repo.** Commit an `afk.toml` with `after_merge = "make install && afk plugin install
    --target claude"`. `afk.local.toml` stays uncommitted.
- **Must not:** restart or signal session hosts; run `after_merge` for a task that did not merge;
  run it inside a session's worktree.
- **Acceptance:** table tests over the fake `git`/`gh`: `after_merge` runs once after a merge and
  before the deploy check; empty means no step; a failure stops the line and `afk resume` clears it;
  a crash before, during and after the command resumes without a second run. Tests for the binary
  check: unchanged carries on, changed exits 0 between passes with one log line, unreadable carries
  on. Then one real check on the VM, recorded in `docs/decisions.md`: with the daemon running under
  systemd, rebuild the binary with `make install` and see the unit restart on the next pass with
  running sessions untouched.
- **Note:** the daemon that runs this task has neither feature, so after it merges the owner installs
  and restarts by hand one last time.
- Amended 2026-10-12 (T2.2a done): later tasks may assume the following. (1) The ship states gain `after_merge`, between `merged` and `deploy_healthy`; a project with no `[ship] after_merge` never enters it. (2) The command runs through `sh -c` in the main checkout, inside the daemon's pass, which waits for it up to `after_merge_timeout`; `.afk/build/after-merge.json` records its start and end and `.afk/build/after-merge.log` holds its output. (3) A failed, timed-out or interrupted command stops the line with the same marker as a failed deploy; the item is not blocking, and `afk resume` marks the task done without running the command again. A daemon killed while the command runs never runs it a second time. (4) `daemon.Run` returns `ErrNewBinary` when the program file's size or modification time changed, checked only at the top of a pass, and `afk daemon` then exits 0; T2.3's restart work must keep that exit path and must not treat it as a crash. (5) `running.json` carries `binary_unknown`. (6) The unit file is `Restart=always`; the unit installed on the VM must be replaced by hand once. See `## T2.2a` in `docs/decisions.md`.

#### T2.3 — Automatic recovery
- **Depends on:** T0.4, T0.5, T2.1, T2.2a
- **Scope:** §5.6. Wedge detection with no client attached; automatic restart with a cap and backoff;
  host relaunch after a reboot; a "continue" prompt after a restart when the session comes back idle
  mid-task.
- **Acceptance:** host tests for client-less probing (keep forge's existing liveness tests green
  unchanged); daemon tests: wedged → restarted → resumes; restart cap reached → parked with an item; a
  human `afk attach` during daemon supervision still gets the session, and the daemon recovers its
  view after they detach.
- Amended 2026-10-12 (T2.3 done): later tasks may assume the following. (1) A session host started with `--probe-unattached` checks for a wedge with nobody attached, and `session.SpawnHost` passes that flag for every unattended session; a host started before this change, or by hand, does not. (2) The daemon never holds a session's client slot: it reads `wedged_since` from `host.json`, and connects only for a moment to ask for a restart (`host.RequestRestart`) or to type a line (`host.SendInput`). (3) For a `running` task, a wedge that has stood for a minute is restarted and a host gone past its grace is relaunched (`HostSpawn.ResumeID`: the resume command when `status.json` has a session id, else the launch command); 30 seconds after the agent is back the daemon types `/afk:go --session <name>`. Parked and shipping sessions are not recovered. (4) The ledger entry carries `restarts`, `restart_at` and `continue_pending`, cleared on a new attempt, on an un-park and when the task leaves `running`; `[daemon] max_restarts` (default 3) and `restart_backoff` (default `"1m"`, doubling) are in `config.toml`, and at the cap the task parks on a blocking item titled "Session could not be recovered: …". (5) `daemon.Deps.Restart` and `Relaunch` are optional; without them a gone host is reported as before. T2.4 can reuse `Restart`, `SendPrompt` and the continue step for its pause and resume, and must clear or respect `continue_pending` if it stops a session itself. (6) Recovery runs under a pause. (7) Not yet seen: a real agent restarted, resumed or typed to by the daemon, a real reboot, and a healthy agent probed at a terminal nobody has attached to (its size is 0x0); the supervised run of T4.2 is the first. See `## T2.3` in `docs/decisions.md`.

#### T2.4 — Governor in the loop
- **Depends on:** T2.1, T1.3
- **Scope:** wire §6.3 into launch decisions and a running session; implement pause and resume of a
  live session (§6.4) after a **spike on the VM** that tries the candidates against a real session and
  records the result; the `StopFailure` marker; login-expiry check (§6.5).
- **Acceptance:** daemon tests with a fake usage source and clock: launch refused at 75% session;
  running session paused at 90% and resumed after `ResetsAt`; overage on refuses to run; stale
  snapshot holds; unsupported provider follows `on_unknown_usage`; logged-out blocks launches. The
  spike's finding and the chosen pause mechanism are written up in `docs/decisions.md`.
- Amended 2026-10-12 (T2.4 done): later tasks may assume the following. (1) The daemon consults the governor on a pass with a running task or one about to start, re-create or un-park something, through optional `daemon.Deps` (`Usage`, `Login`, `StopSession`, `RateLimited`); with no `Usage` every governor rule is off. (2) A pause ends the session's agent and host (`host.RequestExit`, then SIGTERM, never a kill) and is saved first in the ledger entry as `usage_pause_at` and `usage_wake_at`; the task stays `running`, and a resume is a relaunch in the same conversation followed by the continue prompt, not counted as a restart. A paused session has no host, so T3.1 and T3.2 should show it as paused and not as stopped. (3) New state kinds in `daemon.json`: `usage_wait`, `login_wait`, `usage_pause`, `usage_paused`, `usage_stuck`, `usage_resume`. (4) `usage.json` carries a `governor` object (`action`, `reason`, `wake_at`, `at`), which is what T3.1 and T3.2 read for the governor's state; `governor.json` beside it is the daemon's own bookkeeping. (5) Thresholds are `[governor]` in `config.toml`, read by `daemonconfig` and by `afk usage`. (6) The provider has `Login`; there is no warning ahead of a login's expiry because the CLI gives no date. (7) Extra usage on and logged out each raise one blocking item per project with no task, answered by the daemon. (8) Not yet seen: a real agent paused or resumed by the daemon, a real limit, a pause during a Workflow run; the supervised run of T4.2 is the first. See `## T2.4` in `docs/decisions.md`.

---

#### T2.5 — Wake a session after a failed turn
- **Depends on:** T2.3, T2.4
- **Goal:** when an API error ends an agent's turn (for example "API Error: Connection lost
  mid-response"), the daemon sets the session going again by itself. The owner hears about it only
  when waking it has not worked.
- **Background:** on 2026-10-13 the session for T5.6 lost its connection mid-response and sat idle at
  its prompt. Nothing recovered it: the host was alive and answered the wedge probe, so §5.6 did not
  apply, and the `StopFailure` hook is registered for the `rate_limit` reason only, so nothing was
  written to disk. Ten minutes later the stall rule of §5.3 raised "Session waiting at the terminal"
  and parked the task; the owner attached and typed the resume command by hand. §6.4 already says the
  daemon must be able to send that prompt, and T2.3 built the means (`daemon.Deps.SendPrompt`, the
  continue step and `continue_pending`), used so far only after a restart. Typing the session's
  resume command again is safe, because the conductor acts from its recorded phase.
- **Scope:**
  1. **Find out what the agent CLI reports.** Read the CLI's own documentation for the reasons its
     `StopFailure` hook is given, and stage a lost connection against a real session on the VM (cut
     the network mid-response) to capture the hook's input and what `status.json` shows afterwards.
     Write both down in `docs/decisions.md` under `## T2.5`. If no hook fires for a lost connection,
     say so there; part 4 then carries the whole fix.
  2. **A marker for a failed turn.** The `StopFailure` hook also runs for the reasons that are worth
     trying again (a lost connection, a server error, an overloaded service; the list comes from part
     1) and writes `.afk/{session}/.turn-failed`, one line `{"at":"<RFC 3339 UTC>","reason":"…"}`,
     removed by the next prompt and by a normal stop, like `.rate-limited`. It never touches
     `status.json`. The rate-limit marker and its handling stay as they are. A reason that trying
     again cannot fix (a login that has expired, a billing fault) writes no marker. Describe the file
     in the status schema document and in §4.1 first, in the same change. `internal/state` reads it.
  3. **A new rule in the pure `Decide`.** For a `running` task whose host is alive and not wedged,
     with no usage pause and no continue already pending: a marker newer than the session's start,
     its last restart and its last wake means type the session's resume command, through the
     continue step T2.3 built. The first wake comes 30 seconds after the marker, each later one after
     `restart_backoff` doubled each time, and at most `max_wakes` times per task (default 3, under
     `[daemon]` in `config.toml`). The ledger entry gains `wakes` and `wake_at`, saved before the
     prompt is typed and cleared where `restarts` is cleared; §4.2 is updated in the same change. A
     wake is not counted as a restart. Each one is written to the decision log.
  4. **Wake before reporting a stall.** When a running session has been waiting at its prompt for
     longer than `stall_after`, the daemon types the resume command once (counted in `wakes`) and
     raises the stall item only if the session is still waiting `stall_after` later. This covers a
     failed turn that left no marker.
  5. **At the limit** the task parks on a blocking item that says, in plain words, that the session
     kept failing, the last reason and time, and what to do next. Answering it gives a fresh set, as
     for a recovery item. Change the stall item's text, which says "afk cannot yet wake a session by
     itself".
  6. `README.md`: the new setting and what the daemon now does. The OpenCode port gets the same hook
     change, or a line in the lag note of its `PROVENANCE.md`.
- **Must not:** restart or relaunch a host that is alive and not wedged; type into a session that is
  paused for usage, parked or shipping; treat a rate limit as a failed turn; name a provider outside
  `internal/provider`; weaken the wedge probe or the stall rule for a session that really is asking
  something.
- **Acceptance:** table tests for `Decide`: a marker newer than the start and the last wake types the
  prompt after 30 seconds; a marker older than the last wake does nothing; the backoff holds a second
  wake; `max_wakes` reached parks with one item; a rate-limit marker still pauses and never wakes; a
  usage-paused, parked or shipping task is never typed to; a stalled session is woken once before the
  stall item is raised; a crash between saving `wakes` and typing the prompt neither loses the wake
  nor types twice in a row. Hook tests: a reason worth trying again writes the marker, one that is
  not writes nothing, the next prompt and a normal stop remove it. `make check` passes. On the VM
  with a built binary: a session whose connection is cut mid-response carries on with nobody
  touching it, and the evidence is in `docs/decisions.md`.
- Amended 2026-10-13 (T2.5 done): later tasks may assume the following. (1) The `StopFailure` hook is registered for `rate_limit|overloaded|server_error|unknown` and reads the input's `error`: a rate limit writes only `.afk/{session}/.rate-limited`, the other three write only `.afk/{session}/.turn-failed` (`{"at","reason"}`, read by `state.ReadTurnFailed`), and anything else writes nothing. A lost connection is reported as `server_error`, after two to three minutes of the agent CLI's own retries. (2) `Decide` has two new actions, `wake` and `wake_give_up` (both are `daemon.json` kinds and decision-log kinds), reached only for a running task with a live, un-wedged host, no usage pause, no park and no continue pending; the daemon reads the marker through the optional `daemon.Deps.TurnFailed`. (3) The ledger entry carries `wakes` and `wake_at`, saved with `continue_pending` before the line is typed and cleared wherever `restarts` is; `[daemon] max_wakes` (default 3, 0 means never) is in `config.toml`, the first wake comes 30 seconds after the marker and later ones after `restart_backoff` doubled, and at the limit the task parks on a blocking item titled "Session kept failing: …". (4) A session shown as waiting past `stall_after` is typed to once before the stall item, which then comes `stall_after` later; `awaiting_input` is set by an idle prompt only in phase `planning`, so outside planning a failed turn is caught by the marker alone. (5) T3.1 and T3.2 should give the two new kinds their own words. (6) Plugin version is 5.7.0; the OpenCode port has no hook and its sessions are woken only by the stall wake. (7) Not yet seen: a woken conductor carrying real task work on, and a long real outage using up the wakes. See `## T2.5` in `docs/decisions.md`.

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
- Amended 2026-10-12 (T3.1 done): later tasks may assume the following. (1) The page is `internal/web`, started by `afk daemon` only when `[web] token` is set in `config.toml` (`bind` defaults to `127.0.0.1:4253`; a bind other machines can reach with no token stops the daemon); it is not started for `--once` or `status`. (2) Pages are `/`, `/usage`, `/p/{project}` and `/p/{project}/inbox/{id}`, with POSTs to `…/answer`, `/p/{project}/pause` and `/p/{project}/resume`; the JSON API is the same path under `/api`, with a bearer header. (3) An answer from the page is `inbox.Store.Answer(…, inbox.ViaWeb)` and nothing else: the build loop un-parks the task on its next pass, and the page never writes the ledger. (4) `web.Summarize` is the pure per-project summary (current task, phase, done/total, last ship result) that T3.2 can reuse, and `pauseProject`/`resumeProject` in `pause_cmd.go` are the pause and resume actions without printing. (5) Notifications carry a `Click` link to the item only when both `token` and `public_url` are set. (6) Not yet seen: the page behind a real proxy or tunnel, or from a real phone; T4.1 sets that up. See `## T3.1` in `docs/decisions.md`.

#### T3.2 — Dashboard: the build view
- **Depends on:** T2.1, T0.6
- **Scope:** in `afk dash`: a header segment with governor state and the tightest usage window; a
  Build tab listing master-plan tasks with ledger status; an inbox overlay to read and answer items;
  key bindings registered like every other key so `?` and `:` list them. All from files; no dialing.
- **Acceptance:** UI model tests for the new tab, overlay and header at narrow and wide widths; README
  updated.
- Amended 2026-10-12 (T3.2 done): later tasks may assume the following. (1) `afk dash` has `b` (the Build tab, last in the right side's tab strip, shown only when the project has a master plan or a ledger) and `i` (the inbox overlay); both are normal-mode rules, so `?` and `:` list them, and neither acts while the agent pane has the keyboard. (2) The header shows the governor's stored decision and the tightest usage window from `usage.json`, and nothing when there is no reading. (3) The dashboard reads the build through two opt-in `ui.Deps` seams, `LoadBuild` and `AnswerInbox`, wired in `dash_build.go`; the loader reads the project's main checkout on the refresh tick and never dials. (4) `internal/usagetext` holds the governor and usage-window wording shared with `afk daemon status` and `afk usage`, and `internal/buildview` holds the snapshot and the pure task rows. (5) An answer from the dashboard is `inbox.Store.Answer(…, inbox.ViaTUI)` and nothing else; the dashboard never writes the ledger. (6) A session paused for usage reads as paused in the list and the overview. (7) Not yet seen: the new keys beside a live agent, or a real daemon's files; the supervised run of T4.2 is the first. See `## T3.2` in `docs/decisions.md`.

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
- Amended 2026-10-12 (T4.1 done): later tasks may assume the following. (1) `docs/runbook-vm.md` is the set-up guide for a project on the VM; nebari is at `~/code/nebari` with PostgreSQL 17, libvips and Chrome installed, and its whole `bin/ci` takes about 38 minutes there. (2) Nebari's ledger is seeded through T4.3 (T4.2 superseded by T4.2a and T4.2b), so `afk next` names T4.4, not T4.3: T4.3 was built outside afk while this task ran, and T4.2's supervised run should start from T4.4. (3) Nebari is in the daemon's `config.toml` and is paused; `[web]` is set and the page answers at the owner's public address. Resume only after nebari pull request 14 (`afk.toml`, CI on `ubuntu-26.04`) is merged and the checkout is on `main`. (4) Nebari's main is red in CI on `spec/system/task_swipe_spec.rb:266`, on the VM and on the hosted runner; no nebari pull request can merge until that is fixed or set aside. (5) The page sends `Referrer-Policy: same-origin`; with `no-referrer` a browser posts `Origin: null` and every form was refused. (6) git-spice is installed as `~/bin/git-spice`, because Ghostscript's program is also called `gs`. (7) The deploy check means healthy and no more (`/up`); backups, the restore drill, Coolify's auto-deploy, the capped key and the phone checks rest on the owner's answer to the inbox item, not on anything the session saw. See `## T4.1` in `docs/decisions.md`.

#### T4.2 — Supervised first task, then unattended
- **Depends on:** T4.1
- **Scope:** run one nebari task with the owner watching through `afk dash`, end to end including the
  deploy. Write up what needed a human and fix or file each cause. Then leave the daemon running.
- **Acceptance:** one nebari task merged and deployed with no terminal input; a retrospective in
  `docs/decisions.md`, including that task's `afk tokens` breakdown against the nebari baseline of
  about 16M weighted per task (§4.7); the daemon then runs unattended until the plan is done or it is waiting on the
  owner.
- Amended 2026-10-12 (T4.2 done): later tasks may assume the following. (1) Nebari's T4.4 was the supervised task: pull request 15, merge commit `fabbb96`, deploy healthy, one attempt, 3 hours 39 minutes, with no blocking inbox item. Whether the owner typed into the agent's terminal is not recorded anywhere. (2) It cost 17,506,564 weighted tokens against the §4.7 baseline of about 16M, two thirds of it in execute; one task does not move the baseline. (3) Nebari is released and the daemon went on to T5.1 by itself. The session ran `afk resume nebari` once after the owner's go-ahead. (4) `afk inbox` has no way to list another project's answered items (no `--all` flag); filed as inbox notice `mmt6u7`, not fixed. (5) No runbook step was found wrong, so `docs/runbook-vm.md` is unchanged. See `## T4.2` in `docs/decisions.md`.

### M5 — Master planning (`afk plan`)

**Outcome:** an owner goes from an idea to an approved plan that the daemon is building, without
writing `docs/PLAN.md` by hand, and changes a plan mid-build without patching it by hand.

**Every T5 task reads this whole introduction first.** It is M5's shared contract, in addition to
§1–§7.

**Where M5 comes from**

Two hand-run planning sessions are the source: the one that wrote nebari's plan (2026-09-24, transcript
read in full on 2026-10-13) and the one that wrote this plan. What they showed:

- **The first draft is fast and cheap.** Nebari's took under 12 minutes: a one-minute survey, one batch
  of four questions, one 890-line write, no sub-agents. Keep it that way.
- **Product questions with a recommendation work.** Of eight, the owner overrode two and reworded one,
  each in a single answer.
- **Guessed setup facts caused almost all the rework.** The session chose the project's name itself,
  assumed a deploy tool because a sibling project used it, and treated a house preference (a component
  library) as a strict rule. Storage was decided three times and two machine addresses were patched
  twice.
- **Multiple choice fails for open-ended setup topics.** The owner rejected a four-question deploy batch
  and settled it in five free-text turns.
- **Nothing checked the draft.** It reported 27 tasks and had written 31, and shipped two spending caps
  that contradicted each other.
- **Most of the session came after the first draft:** two reconciliation passes against a design system
  made in a separate design tool (from a prompt the session wrote), open questions resolved just before
  their task, and runbooks for steps only the owner could do.
- **Every change after the first write was a guarded patch** (the old text had to be present), never a
  rewrite. This plan was amended the same way seven times in three days while it was being built.

**Rules for every part of M5**

1. **Facts are read or asked, never guessed.** A setup fact (name, hosting, accounts, addresses, stack)
   comes from the owner profile or from the owner. It is never inferred from a sibling project.
2. **Two kinds of question.** A *product* choice is asked with options and one recommendation, worded
   so that "yes" accepts the recommendation. A *setup* fact missing from the profile is asked as a plain
   question with no options.
3. **Questions go in rounds.** A round holds every question whose prerequisites are settled; a question
   that depends on an open one waits for a later round.
4. **One session, one write.** The first draft is written in one pass by the main session. The only
   sub-agents are fact look-ups and one critic.
5. **After the first write, only guarded patches.** Every later change goes through `afk plan patch`
   (T5.4), which requires the old text to occur exactly once and runs the checker afterwards.
6. **Code counts and checks.** Any number in a summary comes from `afk plan lint --summary`. The model
   never reports a count it could have computed.
7. **State lives on disk** under `.afk/plan/`, so a session can be left and resumed, and so a later web
   interface can answer the same records the terminal does. Gates between phases are predicates over
   those files, as the conductor's are.
8. **Existing ids never change.** A task id, once written, is never renumbered or reused. A task that is
   `running` or `shipping` in the ledger is never edited except to append an "Amended" line.
9. **No secrets** in the profile, the plan, the brief or any file under `.afk/plan/`.

**The plan session's files (`.afk/plan/`, gitignored)**

| File | Holds |
|---|---|
| `state.json` | `{mode: new\|extend\|amend\|reconcile, phase, updated_at}` |
| `brief.md` | The owner's request, word for word. Never edited after it is saved. |
| `survey.md` | What was read before asking anything, and the profile entries that apply. |
| `questions.jsonl` | One line per question: `{id, round, kind: product\|setup, question, options, recommended, answer, answered_at}` |
| `slices.md` | The proposed task breakdown the owner confirms before the draft is written. |
| `critic.md` | The critic's findings on the draft and what was done about each. |

Phases, in order: `brief` → `survey` → `model` → `rounds` → `slices` → `draft` → `check` → `design`
(only when the project has an interface) → `review` → `approved`.

**The master-plan format**

T5.1 writes this down in full. In short, a master plan has: a "how to use" header; **Context** with the
original request quoted; a **Decisions** table, in which every row that came from the owner profile
says so; the shared contract sections the project needs (architecture, domain model, standards);
**Milestones** made of tasks; **Deferred**; **Open questions** as a table with a "Needed by" column of
task ids; and a **Definition of done**. A task entry has `Depends on`, `Goal`, `Scope` and
`Acceptance`, and may have `Background`, `Must not`, `Not in scope` and `Note`.

**The slicing rule.** A task is a thin, complete path through every layer the change touches (schema,
logic, interface, tests) that can be shown working by itself, and is small enough for one fresh agent
session. Groundwork that makes a later task easy comes first, as its own task. A mechanical change that
touches everything at once (a rename) is the exception: add the new form beside the old, move callers
over in batches, then remove the old.

Both this plan and nebari's must pass the checker with no errors, unchanged.

**Decisions already made for M5**

| Topic | Decision |
|---|---|
| Where questions are asked | The terminal, in an attended session on the machine that runs the daemon. Not the inbox. A web interface may answer the same records later (rule 7). |
| What v1 covers | New project, extend (a new milestone), amend (one task) and reconcile (bring the plan in line with a new document). |
| Approval | Required before the daemon builds anything. The plan is readable on the web page; approving is a terminal command. |
| Names | `afk plan` is the master plan. The per-task skill `/afk:plan` is renamed `/afk:work-plan`. The new skill is `/afk:master-plan`. |
| Owner profile | Machine-wide, `profile.toml` beside `config.toml`. Each entry is a **rule** or a **default**, and may apply to one kind of project. It includes the preferred stack. |
| Design | A phase of the session for projects with an interface. afk writes a brief, the owner runs it in any design tool and saves the result, afk reconciles. The plan wins on behaviour; the design system wins on looks. |
| Borrowed method | Domain modelling, questions in rounds and the slicing rule are adapted from Matt Pocock's skills (`github.com/mattpocock/skills`, MIT). afk ships its own text and credits the source in `docs/provenance.md`; it never requires that plugin. |
| Where plan changes are committed | Straight to the base branch of the main checkout, then pushed, as this plan's amendments were. `[plan] via_pr = true` in `afk.toml` sends them through a pull request for projects where a push to the base branch deploys. |

#### T5.1 — The plan format and `afk plan lint`
- **Depends on:** T2.2
- **Goal:** one written format for a master plan, and a checker in code that says whether a file
  follows it.
- **Scope:** `plugins/afk/skills/master-plan/plan-format.md`, the full format with the slicing rule and
  a short worked example. `internal/planlint`: a pure function from plan text to findings
  `{level: error|warning, line, task, rule, message}`, built on `internal/masterplan`. Command `afk
  plan lint [--project DIR] [--json] [--summary] [FILE]`: exit 0 with no errors, 1 with errors, 2 for
  bad usage. `--summary` prints the counts of milestones, tasks, owner tasks, open questions and
  findings.
  - **Errors** (the daemon would refuse or misread the plan): a duplicate task id; a dependency naming
    an unknown task; a cycle; a task with no `Depends on` line; a heading that looks like a task but
    whose id does not parse; an open-question row whose "Needed by" names an unknown task.
  - **Warnings:** a `Depends on` line with text but no task id (today afk silently reads it as "none");
    a task with no `Goal`, `Scope` or `Acceptance`; a missing required section; an open-question row
    whose "Needed by" names no task.
  - The daemon logs the checker's errors with the `failure` item it already raises for a bad plan.
- **Must not:** change what `internal/masterplan` accepts, or how the daemon picks a task.
- **Acceptance:** table tests with one row per rule, each with a passing and a failing plan. This plan
  and the nebari fixture produce zero errors; their warnings are listed in `docs/decisions.md`. `afk
  plan lint --summary` on this plan prints the same task count as `afk tasks`.
- Amended 2026-10-13 (T5.1 done): the checker is `planlint.Lint`, built on a new `masterplan.Scan` that returns every problem, the headings and the fenced lines (`Parse` is unchanged in what it accepts); its eleven rule ids are listed in the format document and are a contract with it; `--json` prints `{file, findings, summary}`; `FILE` and `--project` cannot be combined; an open-question row is open unless its question cell contains `**Answered`, so an answer written as its own row below leaves the question open (several rows in §10 are like that, which T5.6 must allow for); the daemon's bad-plan item lists at most 20 checker errors.

#### T5.2 — The owner profile
- **Depends on:** T2.2
- **Goal:** the owner states how they build and host things once, and every plan session reads it.
- **Scope:** `profile.toml` in afk's config folder. Shape: `rule_files = [paths]` (existing house-rule
  documents to read, not copy), and entries `[[entry]]` with `topic`, `value`, `strength = "rule" |
  "default"`, optional `kind` (for example `web-app`, `cli`, `library`; absent means every kind) and
  optional `reason`. A value of `"no preference"` is allowed and means "recommend one and say why".
  Suggested topics, in the template: project location, language and framework, database, auth, tests,
  interface components, CI, hosting and deploy, storage, email, domain names, notifications, the design
  tool, and the accounts the owner already has. `internal/profile`: loader and a pure
  `Select(profile, kind)` that returns the entries that apply, a `kind`-specific entry beating a general
  one on the same topic. Commands: `afk profile` (print what applies, `--kind` to filter), `afk profile
  init` (write a commented template; refuses to overwrite), `afk profile set <topic> <value>
  [--kind K] [--rule|--default] [--reason TEXT]`, `afk profile check`.
- **Must not:** store a secret. `afk profile check` and `set` refuse a value that looks like a key or
  token and say to keep it out of the profile.
- **Acceptance:** table tests for `Select` (general only, kind only, both, unknown kind, a rule and a
  default on one topic), for `set` on a missing file, a new topic and an existing one, and for the
  secret check. `afk profile` with no file says how to create one and exits 0.
- Amended 2026-10-13 (T5.2 done): the profile is read with `afk profile --kind K`, one line per entry on stdout as `<strength>  <topic>: <value> [<kind>] — <reason>` (kind and reason only when set), and there is no `--json`; a profile with an error prints no entries and exits 1, so T5.3's survey must stop on a non-zero exit and never plan from part of a profile; `afk profile set` keeps an entry's strength and reason unless told otherwise, refuses anything that looks like a key or token (no override) and cannot replace an entry that holds a key afk does not know; `rule_files` are listed and checked for existence but never read here, so reading them is T5.3's; the file's contract is in §4.6 and the decisions in `docs/decisions.md` under `## T5.2`.

#### T5.3 — The planning session
- **Depends on:** T5.1, T5.2
- **Goal:** `afk plan` in a project folder runs the session that turns the owner's idea into a checked
  first draft of `docs/PLAN.md`.
- **Scope:**
  - **Command.** `afk plan [--project DIR]` starts, or reattaches to, an attended session (never
    unattended) running `/afk:master-plan` in the project's main checkout, with the mode `new` when the
    project has no plan. A folder that is not a git repository is initialised. `afk plan status` prints
    the phase and what it is waiting for, from a pure function over `.afk/plan/`.
  - **Rename** `/afk:plan` to `/afk:work-plan` everywhere (skill, README, tests, prompts).
  - **The skill**, phase by phase, each ending by writing its file:
    1. `brief`: save the owner's words unchanged.
    2. `survey`: read the profile (`afk profile --kind …`), its rule files and what is in the folder.
       List the profile entries that apply. Read nothing from sibling projects unless the owner names
       one.
    3. `model`: domain modelling with the owner. Challenge a vague or overloaded term when it is used,
       try each relationship against an invented edge case, and write each settled term into
       `CONTEXT.md` at once. The glossary holds meanings only, never implementation.
    4. `rounds`: rules 2 and 3. Round one always asks who the project is for, whether it has an
       interface, and shows the profile entries being applied so the owner can override one. A setup
       fact the owner supplies is offered back with `afk profile set`.
    5. `slices`: show the task breakdown as a numbered list (title, blocked by, what it delivers) and
       ask whether the size and the blocking edges are right. Repeat until confirmed.
    6. `draft`: one write of `docs/PLAN.md` in the format of T5.1, plus `afk.toml`. Every generated
       plan has: an owner task for anything only the owner can set up (accounts, secrets, machines); an
       early task that gives the repository a CI workflow, because afk never merges a pull request with
       no checks; open questions keyed to the task that needs them; and the project's own agent
       instructions written from the profile.
    7. `check`: `afk plan lint` must report zero errors. Then one critic sub-agent reads only the draft
       and the brief and reports contradictions between sections, tasks that trace to nothing in the
       brief, parts of the brief no task covers, and slices that break the slicing rule. Each finding is
       fixed by a guarded patch or answered in `critic.md`.
    8. `review`: print the summary from `afk plan lint --summary` and the open questions, and stop for
       the owner (T5.7 adds approval).
  - Credit the adapted method in `docs/provenance.md`.
- **Not in scope:** the design phase (T5.5); plans that already exist (T5.4); approval (T5.7). Until
  T5.4 is merged the skill patches with exact-match replacement by hand.
- **Acceptance:** table tests for the phase function (each phase reached only when the previous
  phase's file exists and is complete; a resumed session lands on the right phase). Plugin tests: the
  skill stays within the size cap, every file it includes exists, and no prompt still says `/afk:plan`.
  `afk plan --dry-run` prints what it would start. The real session is proven in T5.8, not here.
- Amended 2026-10-13 (T5.3 done): the planning agent runs in a session named `master-plan` (files in `.afk/master-plan/`, with an empty `planning` marker that makes every start of its host run the skill), apart from the state in `.afk/plan/`, and `plan` and `master-plan` are refused as names for other sessions; `state.json` is seeded by `afk plan` and moved only by the new `afk plan advance`, one phase at a time and only when that phase's file is complete, so T5.7's approve should set `approved` through `internal/plansession` and T5.5 must stop `advance` skipping `design` (today `check` goes straight to `review`, and `advance` refuses to leave `review`); the reported phase is the earliest one whose file is missing or incomplete, never later than the recorded one (`plansession.Decide`), and the files' contract is §4.8; `afk plan` on a project that has a plan and no open session exits 1, which is where T5.4's mode choice goes; the agent's command line comes from `PlanArgv` on the provider interface and only the default agent has one; the OpenCode port has `/afk-work-plan` but no master-plan skill; the skill patches by exact-match edit until `afk plan patch` exists; a real start and attach was not run here and is T5.8's; the decisions are in `docs/decisions.md` under `## T5.3`.

#### T5.4 — Extend, amend and reconcile
- **Depends on:** T5.3
- **Goal:** change a plan that already exists, by the same guarded patches used by hand so far.
- **Scope:**
  - **`afk plan patch [--project DIR] --old FILE --new FILE [--message TEXT]`**, in `internal/planpatch`:
    the old text must occur exactly once; the write is atomic; the checker runs afterwards and the
    patch is undone if it adds an error; rule 8 is enforced against the ledger. It commits and pushes
    per the M5 decision on where plan changes go. Every mode below, and the planning session, use it.
  - **`afk plan` on a project that has a plan** asks which mode, or takes `afk plan extend`, `afk plan
    amend "<what>"` or `afk plan reconcile <file>`.
  - **extend:** a new milestone. A short session: brief, a survey of the plan, `docs/decisions.md` and
    the ledger, rounds, slices, then the milestone appended by patch.
  - **amend:** one task added, split or superseded. It writes the entry in the plan's own format,
    picks the id (the next number in the milestone, or a letter suffix such as `T2.2a` when it must
    run between two existing tasks), sets its dependencies and adds it to any task that must now wait
    for it. A split marks the old task `superseded` in the ledger with `--replaced-by`. `--yes` skips
    the confirmation so a later surface can call it.
  - **reconcile:** read the named document against the plan and list: conflicts, things the document
    adds that the plan should absorb, and things in the plan the document contradicts. Real choices go
    to the owner as a round; the rest are patched. When the document is the design system, apply the
    precedence rule and write a follow-up prompt for whatever the design must change.
- **Acceptance:** table tests for `planpatch` (no match, two matches, one match, a patch that adds a
  lint error is undone, a patch touching a running task's entry is refused, appending an "Amended" line
  to one is allowed) and for id choice (next number, letter suffix, a suffix already taken). Each mode
  has a fixture plan and an expected patched plan for a scripted set of answers.
- Amended 2026-10-13 (T5.4 done): the pull-request setting is `[planning] via_pr`, not `[plan] via_pr`, because `plan` is already the path key (§4.6); `planpatch.Run` is the entry point for code that patches a plan (T5.6's row patch should call it, with an `Edit` built from the text it reads), and it checks the patched text before writing, so nothing is ever undone on disk; a patch is committed only when the plan is tracked and had no uncommitted changes, so a first draft that T5.7 has not committed yet is patched in place; with `via_pr` the plan file in the main checkout changes only when the pull request merges and the base is pulled, nothing is fetched, and `afk plan lint`, `next-id` and `afk tasks` read the unmerged file; there are three helper commands the plan did not name, `afk plan insert`, `afk plan depend` and `afk plan next-id`; a change session takes a shorter phase path per mode, lists its patches in `.afk/plan/changes.md` and is finished at `review` (§4.8), so T5.5's `design` phase and T5.7's `approved` concern `new` sessions only, and `plansession.Finished` and `PhasesFor` are what to ask; a bare `afk plan` on a project with a plan now asks which change instead of exiting 1; `afk plan amend --yes` still needs a terminal; a real change session was not run here and is T5.8's; the decisions are in `docs/decisions.md` under `## T5.4`.

#### T5.5 — The design hand-off
- **Depends on:** T5.4
- **Goal:** a project with an interface gets a design system made in the owner's design tool, without
  the planning session pretending to design in a terminal.
- **Scope:** the `design` phase of `/afk:master-plan`, entered only when round one said the project has
  an interface.
  - **The brief.** Write `docs/design-brief.md`, a prompt for a design tool: what the product is and who
    it is for, the screens the plan implies, the domain terms from `CONTEXT.md`, the constraints from
    the profile (stack, component approach, accessibility level) and exactly what to hand back: one
    `docs/design-system.md` with tokens, components and screens.
  - **The owner task.** Add `Design system (owner)` to the plan's first milestone. Its entry says to
    take the brief to the design tool named in the profile and save the result at
    `docs/design-system.md`. Every task that builds interface depends on it; tasks that do not can run
    while the owner designs.
  - **Coming back.** When the owner confirms that task in the inbox, the daemon's item says to run `afk
    plan reconcile docs/design-system.md` (T5.4). Follow-up prompts are saved as
    `docs/design-followup-<n>.md`.
  - The generated plan's header states the precedence rule.
- **Not in scope:** generating a design, calling a design tool, prototypes.
- **Acceptance:** a fixture session with an interface ends with the brief, the owner task and the
  dependencies in place and zero lint errors; one without an interface has none of them. A reconcile
  fixture with a design system that conflicts with the plan produces the expected patch and a follow-up
  prompt.
- Amended 2026-10-13 (T5.5 done): round one's interface question has the fixed id `r1-interface` and an answer that starts with `yes` or `no`, and a `new` session cannot leave `rounds` without it; `afk plan advance` goes from `check` to `design` on yes and to `review` on no, and the `design` gate is code (`plansession`, with the plan facts filled in by `designFacts`; contract in §4.8), so T5.7's approve finds a `new` session at `review` either way; the design task is recognised by its title through `masterplan.Task.DesignSystem()`, the one test the gate and the daemon share; the daemon's item for it tells the owner to save and commit `docs/design-system.md`, run the reconcile, and only then answer, and no second item is raised after the answer; there is a helper command the plan did not name, `afk plan next-followup`; `docs/design-brief.md` is not committed by anything yet, so T5.7's approve should commit it with the plan; a real design phase was not run here and is T5.8's; the decisions are in `docs/decisions.md` under `## T5.5`.

#### T5.6 — Open questions hold their tasks
- **Depends on:** T5.1
- **Goal:** an open question is put to the owner before the task that needs it starts, so nobody has
  to remember it.
- **Scope:** `internal/masterplan` reads the Open questions table: a row is open unless its question
  cell contains `**Answered`. A new rule in the pure `Decide`: when the next ready task is named in the
  "Needed by" of an open row, the daemon launches nothing for it, raises one blocking `question` item
  per row (once; the row is recognised again on later passes) and parks the task. When the item is
  answered, the daemon patches the row to `**Answered <date>:** <the answer>` (through `afk plan patch`
  once T5.4 is merged; until then with the same exact-match rule in code) and the task is ready again.
  A row whose "Needed by" names no task never holds anything.
- **Background:** this plan and nebari's both have such tables, so rows naming tasks not yet built will
  start to hold them. That is the intent.
- **Acceptance:** table tests for `Decide`: an open row holds its task and raises one item; an answered
  row does not; two rows on one task raise two items and the task waits for both; a row naming no task
  holds nothing; answering patches the row once and only once across a daemon restart.
- Amended 2026-10-13 (T5.6 done): the table reader is `masterplan.Questions` (the checker uses it too) and `masterplan.AnswerRow` builds the answered row; a held task is `parked` with no session and goes back to `pending`, so T5.7's "first ready task" may be held on its first pass and its integration test should use a plan with no open row naming that task; the row holds until the plan in the main checkout reads answered, so with `[planning] via_pr` the task waits on a second item, `Put the answer in the plan: <task>`, until the pull request is merged and pulled; owner tasks are held too; a row answered by hand closes its item; the daemon reads and patches the plan through two optional hooks (`Deps.ReadPlan`, `Deps.PatchPlan`) that `afk daemon` sets, and a daemon built without them holds nothing; `afk next` and `afk tasks` do not show a hold before the daemon has parked the task; the via-PR path was tested with a scripted runner only and is T5.8's to try for real; the decisions are in `docs/decisions.md` under `## T5.6`.

#### T5.7 — Approve and go
- **Depends on:** T5.3, T5.6
- **Goal:** the "go" moment: one command turns a reviewed plan into a build the daemon is running.
- **Scope:** `afk plan approve [--project DIR]`. It refuses unless the session is at `review` and the
  checker reports zero errors. Then, each step safe to repeat: write `afk.toml` if missing; commit the
  plan; `afk ledger init`; add the project to the machine config (new command `afk project add|remove|
  list` if none exists); set the phase to `approved`. The daemon must pick up a project added while it
  runs, without a restart. The web page gains a read-only plan view per project: the plan rendered, the
  checker's summary and the open questions, linked from the "plan ready for review" notification.
- **Must not:** start a build for a plan with errors; approve from the web page.
- **Acceptance:** table tests: approve at the wrong phase refuses; with lint errors refuses; run twice
  does nothing the second time; a crash between any two steps is finished by running it again. An
  integration test with the fake provider: approve a fixture project and see the daemon launch its
  first ready task with no restart. The plan view renders this plan.
- Amended 2026-10-13 (T5.7 done): `afk plan approve` works on a `new` session at `review` only, writes `approved` last and does nothing on a second run; it commits the plan, `afk.toml`, `.gitignore`, `CONTEXT.md`, the design brief and the agent instruction files (named by `provider.InstructionFiles`) and nothing else, and pushes to `origin` unless `[planning] via_pr` is set, so in T5.8's throwaway project the owner needs a git identity and, for the daemon to ship, a remote; the project list is edited with `afk project add|remove|list` and re-read by the daemon on every pass (the only setting that is; the rest still need a restart), and `afk daemon` now starts with no projects; a plan waiting for approval is listed in `plan-reviews.json` in afk's state folder (§4.2a), written by `afk plan advance` on reaching `review`, which also sends the "Plan ready for review" notification; the plan view is `/p/<project>/plan`, read-only, drawn by afk's own renderer (no markdown library); a real approve against a running daemon and a real notification were not run here and are T5.8's; the decisions are in `docs/decisions.md` under `## T5.7`.

#### T5.8 — Prove it (owner + agent)
- **Depends on:** T5.4, T5.5, T5.7
- **Goal:** two real plans made with `afk plan`, and an honest account of how they compare with the
  hand-run sessions.
- **Scope:** the owner runs both in a terminal; the session for this task waits for them, then reads
  the files and writes up.
  1. **Extend:** plan afk's next milestone with `afk plan extend`, from the candidates in §9.
  2. **New:** plan a small throwaway project from a one-paragraph idea, approve it, and let the daemon
     merge its first task.
  The write-up, in `docs/decisions.md`: human turns, questions asked, recommendations overridden,
  setup facts that had to be asked, every piece of rework and its cause, checker and critic findings,
  and the session's tokens. Compare with nebari's session: 35 human turns, 12 questions, and rework on
  the name, the deploy tool, storage (three times), the component library and two addresses. Faults in
  the prompts are fixed here; faults in code are filed in the inbox.
- **Acceptance:** both plans pass the checker with zero errors; the throwaway project has one task
  merged by the daemon; the write-up names every place the owner had to correct the session.
- Amended 2026-10-13 (T5.8 done): both real runs happened (`afk plan extend` added M6 here; `afk plan` planned `~/code/todo`, whose T1.1 the daemon merged) and the account is in `docs/decisions.md` under `## T5.8`; nine prompt faults they showed are fixed in the planning skill (plugin 5.7.3) but not yet proven by another session; the critic's prompt now takes a third slot, `{request-changes}`, for the owner's later words, and `brief.md` still is never edited; four code gaps were filed and not fixed (inbox `9pxewe` no tokens recorded for a planning session, `yrww8c` a task's inbox items stay open after it merges, `taa7na` the GitHub login park text, `8szxye` no command records a planning answer), so a later task should not assume `afk tokens` covers planning sessions; plan changes through a pull request (`[planning] via_pr`), a reconcile against a real design system and `afk plan amend --yes` have still not been run for real.

---

### M6 — One settings file, and more in the dashboard

**Outcome:** `afk dash` started in any folder lists every project in `config.toml` in its project
switcher, the machine has one settings file, and the owner can pause the build and see how many inbox
items need them without leaving the dashboard.

#### T6.1 — The switcher lists the projects in config.toml
- **Depends on:** T3.2, T5.7
- **Goal:** the dashboard's project switcher (`p`) shows every project `afk project list` prints, on a
  machine with no `config.json` at all.
- **Background:** the machine has two settings files. The daemon reads `config.toml`
  (`internal/daemonconfig`), where `afk project add` and `afk plan approve` list projects. The
  dashboard reads `config.json` (`internal/config`, carried over from forge), which holds only folders
  to search. With no `config.json` the switcher shows just the project `afk dash` was started in, so
  the owner's two listed projects never appeared together.
- **Scope:** the dashboard reads the `[[project]]` list from `config.toml` through `daemonconfig` and
  merges it into the switcher's list with the project it was started in and whatever the search finds.
  One row per project, matched by main-checkout path; a `name` set in `config.toml` is the name shown.
  The list is read again on every scan tick, so a project added with `afk project add` appears without
  a restart, and the listed projects appear without waiting for a folder search to finish. A listed
  folder that no longer exists is left out and is not an error. A `config.toml` that cannot be read
  keeps the list the switcher had and says why in plain words, naming the file. The read goes through
  an injected `ui.Deps` seam like `LoadConfig` and `Scan`, and the merge is a pure function. Search
  folders still come from `config.json` in this task. README: the switcher's paragraph.
- **Must not:** dial, use the network or block (§3: discovery is read-only); write `config.toml`;
  change what the daemon reads.
- **Acceptance:** table tests for the merge: listed only, found only, both (one row), the start-up
  project not listed, a listed folder that is gone, a name from `config.toml`, an unreadable file. A
  UI model test: with two listed projects and no search folders, the switcher shows both. Against a
  scratch build with a scratch config folder holding two `[[project]]` entries and no `config.json`,
  `p` lists both. `make check` is green.
- Amended 2026-10-13 (T6.1 done): later tasks may assume the following. (1) The dashboard reads the project list through the `ui.Deps` seam `LoadListed` (entries of `ui.ListedProject{Path, Name, Gone}`, the file's path and the load error), wired only in `dashDeps` to `dashLoadListed` in `dash_projects.go`, which calls `daemonconfig.Load` and resolves each folder to its main checkout; `internal/ui` still reads no file. (2) The switcher's list is built by the pure `mergeProjects` in `internal/ui/listed.go` from the start-up row, the last good listed set (`m.listed`) and the last search (`m.found`); `unionProjects` is gone. (3) Listed projects are read by their own command at start, on every scan tick and on `R`, apart from the `config.json` read, so T6.2 can move the search folders without touching it; search folders still come from `config.json` through `LoadConfig`. (4) An unreadable `config.toml` is any error from `daemonconfig.Load`, so a bad value anywhere in the file keeps the held rows and raises one toast naming the file; T6.2's `[dash]` table will be checked by the same load. (5) `Deps.LaunchMainCheckout` carries the start-up project's main checkout, so a dashboard started in a session's worktree of a listed project shows one row. See `## T6.1` in `docs/decisions.md`.

#### T6.2 — Search folders move into config.toml and config.json is retired
- **Depends on:** T6.1
- **Goal:** one settings file per machine. The dashboard's search settings live in `config.toml`, and
  afk no longer reads `config.json`.
- **Scope:** `config.toml` gains a `[dash]` table with `search_roots`, `scan_interval` and
  `max_depth`, with the meanings, defaults and limits they have in `config.json` today (no roots, 60
  seconds with a one-second floor, depth 3; `~/` expands). `daemonconfig` reads it, the dashboard uses
  it, and `internal/config` is removed with its tests moved or dropped. The daemon ignores the table.
  `afk project add|remove` keep the table and its comments as they keep every other line. When a
  `config.json` is still in afk's config folder, `afk dash` says so once per start, in plain words: afk
  no longer reads that file, and here are the `[dash]` lines to put in `config.toml`, built from the
  old file when it can be read. It never edits or deletes either file. README: the dashboard's
  configuration section describes `[dash]` in `config.toml`, and the sentence saying the two files
  are different files goes. §4.6 of this plan gets an Amended line with the new table.
- **Must not:** read `config.json` for settings; write `config.toml` on the owner's behalf; make the
  daemon restart or fail over a `[dash]` value.
- **Acceptance:** table tests for the `[dash]` keys: absent table, each key, a `scan_interval` under
  the floor (an error naming the key and the file), a bad depth, `~/` expansion. A test that
  `afk project add` and `remove` leave a `[dash]` table byte for byte as it was. A test for the notice:
  shown once with a `config.json` present, never without one, and still shown when the old file cannot
  be parsed. Against a scratch build: a search folder set in `[dash]` adds a found project to the
  switcher, and an old `config.json` beside it changes nothing but the notice. `/usr/bin/grep -rn
  'config\.json' --include='*.go' .` finds only the notice and its tests. `make check` is green.
- Amended 2026-10-13 (T6.2 done): later tasks may assume the following. (1) `internal/config` is gone; the dashboard's search settings are `daemonconfig.Config.Dash` (`SearchRoots`, `ScanInterval`, `MaxDepth`), read from the `[dash]` table of `config.toml` by the one `daemonconfig.Load`. (2) A bad `[dash]` value never fails that load: it is carried as `Config.DashErr` with the table reset to its defaults, so the daemon and every other reader of the file are untouched, and a new reader of `config.toml` (T6.3, T6.4) need not think about `[dash]`. This replaces point (4) of the T6.1 amendment: a bad `[dash]` value does not hold the listed rows, only the folder search reports it. (3) `ui.Deps.LoadConfig` keeps its name and now returns the ui-local `ui.SearchSettings{Roots, Interval, MaxDepth}`; `loadDashConfig` in `dash.go` converts, and `internal/ui` still reads no file. (4) `ui.Deps.StartupNotice` is a plain string raised as one toast when the dashboard starts; `dash_notice.go` builds the old-file notice and is, with its test, the only afk code that names `config.json` (`internal/pluginstall` names OpenCode's own file of that name). See `## T6.2` in `docs/decisions.md`.

#### T6.3 — Pause and resume the build from the dashboard
- **Depends on:** T3.1, T3.2
- **Goal:** the owner can pause and resume the active project's build from `afk dash`, with the same
  effect as `afk pause` and `afk resume`.
- **Background:** T3.2 left these keys out because its scope did not list them (`docs/decisions.md`,
  `## T3.2`). The web page already has the buttons. `pauseProject` and `resumeProject` in
  `pause_cmd.go` are the actions without printing, and both log the owner's decision.
- **Scope:** two normal-mode keys in `afk dash`, registered like every other key so `?` and `:` list
  them, and typed to the agent as letters while the agent pane has the keyboard. Pick keys nothing
  else uses and record them in `docs/decisions.md`. They act on the active project's main checkout
  through an injected `ui.Deps` seam wired to `pauseProject` and `resumeProject`, off the
  screen-drawing path. The dashboard says what happened in a short notice: paused (a running task
  finishes, nothing new starts), already paused, resumed, the stop cleared (naming the task), or
  nothing to resume. A failure is shown with its reason. The Build tab's summary line shows the new
  state on the next refresh. In a project with no master plan the keys say there is no build to
  pause, as `b` does. README: the key list and the Build tab paragraph.
- **Must not:** write the ledger; stop or kill a running session; act while the agent pane or the
  inbox overlay has the keyboard.
- **Acceptance:** a table test for the notice wording, one row per outcome of `pauseProject` and
  `resumeProject`, errors included. UI model tests: each key calls its seam once with the active
  project's root, does nothing in agent focus, and appears in `?` and `:`. Against a scratch build in
  a throwaway repository (not this checkout): the pause key creates `.afk/build/paused` and the Build
  tab reads paused; the resume key removes it; `.afk/log/decisions.jsonl` has a line for each.
  `make check` is green.

#### T6.4 — Inbox count in the dashboard's header
- **Depends on:** T3.2
- **Goal:** the dashboard's header shows how many inbox items need the owner in the active project,
  and how many of them are holding a task up, whichever tab is open.
- **Background:** T3.2 left this out for the same reason as the pause keys. The count is already
  worked out for the Build tab's summary line by `buildview.OpenCounts`, from the snapshot the
  dashboard loads on its refresh tick.
- **Scope:** a header segment from the same snapshot, with no new read: nothing when no item is open,
  otherwise the open count and, when any item blocks a task, that number in plain words (for example
  `inbox 3 · 2 blocking`), with a hint of the `i` key when there is room. Give it a place in the
  order in which the header gives things up as the screen narrows (`docs/decisions.md`, `## T3.2`,
  A10): a blocking count outlasts the afk badge and the switch-projects hint. Record the order in
  `docs/decisions.md`. Inbox files that cannot be read are not counted here; the Build tab already
  mentions them. README: the header's description.
- **Must not:** add a read, a dial or a second inbox loader; show another project's count.
- **Acceptance:** table tests for the segment's text (no items, open only, open and blocking, one
  item) and for the header at narrow and wide widths, with the saved screens updated. Against a
  scratch build in a throwaway repository with two open items, one blocking: the header shows both
  numbers on the Overview tab, and answering the blocking item in the overlay changes the header
  within one refresh. `make check` is green.

### M7 — The dashboard is the command center

**Outcome:** the owner runs afk from `afk dash` without going back to the shell for day-to-day work:
one leader key opens a dialog of every action, and from it they start any planning session (a new plan
in a new folder included), approve a plan, see every project on one screen, add and remove projects,
set a task's status, see a task's tokens and refresh the usage reading.

#### T7.1 — Leader key and actions dialog
- **Depends on:** T6.3, T6.4
- **Goal:** pressing ctrl+space anywhere in `afk dash`, the agent pane included, opens a dialog that
  lists every action the dashboard has, each with its key; the next key runs that action, and typing
  filters the list by name.
- **Background:** the owner asked for "an actions menu (dialog)" and "a leader key much like tmux and
  opencode", and chose ctrl+space, the dialog opening at once, and (T7.2) every action under the
  leader. Today actions are single letters on the session list, a few ctrl chords (`ctrl+t`, `ctrl+w`,
  `ctrl+g`, `ctrl+\`) and the `:` palette, all registered in `newModeRegistry` (`internal/ui/mode.go`)
  and `defaultKeyMap` (`internal/ui/model.go`). While the agent pane has the keyboard every key but
  those chords is typed to the agent. T6.3 adds the pause and resume keys and T6.4 the header's inbox
  count, which is why this task waits for both.
- **Scope:** one leader key, ctrl+space, handled before any key is passed to the agent, in the session
  list, the detail side and the agent pane. It opens the actions dialog: every registered action with
  its name, its key after the leader and one line of help, built from the same registry the `?` help
  and the `:` palette read, so an action added later appears with no extra work. In the dialog a key
  runs its action and closes the dialog; typing letters that match no key filters by name and `enter`
  runs the highlighted row; `esc` closes it; ctrl+space a second time closes it and sends the key
  itself to the agent when the agent pane has the keyboard. An action that cannot run where the owner
  is (for example clean with no session selected) is shown dimmed with the reason and does nothing.
  The leader is ignored while another overlay or form is open (the inbox overlay, the project
  switcher, the new-session form, a confirm); `esc` leaves those first. Every key the dashboard has
  today keeps working in this task; T7.2 removes them. Read in the terminal library's source how
  ctrl+space arrives (it is not the same on every terminal) and record what was found, and what tmux
  does to it, in `docs/decisions.md`. If ctrl+space cannot be told apart from another key in the
  owner's terminal or through tmux, stop and raise an inbox item; do not pick another key. README: a
  section on the leader key and the dialog.
- **Must not:** remove or change an existing key (T7.2 does); block the screen-drawing path; swallow
  ctrl+space so the agent can never receive it.
- **Acceptance:** a table test for the pure decision of what a key does in the dialog (run, filter,
  close, pass through), every branch a row. UI model tests: the leader opens the dialog from the list,
  the detail side and the agent pane; a key runs its action once; a dimmed action does nothing; the
  leader is ignored under each overlay; leader twice in the agent pane types the key to the agent.
  Saved screens at narrow and wide widths. Against a scratch build driven through tmux with a stand-in
  agent in the pane: ctrl+space opens the dialog while the agent has the keyboard, `esc` returns to
  it with nothing typed, and an action picked there runs. `make check` is green.

#### T7.2 — Every action moves under the leader
- **Depends on:** T7.1
- **Goal:** the leader is the one way to run a dashboard action. The session list keeps only keys
  that move around.
- **Background:** the owner was offered three ways to treat today's keys and chose "Everything moves
  under the leader; the session list keeps only the movement keys and enter", having said "I'm ok
  with re-mapping existing keys".
- **Scope:** remove the bare action letters from the session list and the detail side (`n`, `o`, `p`,
  `c`, `r`, `l`, `/`, `b`, `i`, `q`, `f`, `?`, the pause and resume keys of T6.3) and the chords
  `ctrl+t`, `ctrl+w`, `ctrl+g` and `ctrl+\` of the dashboard, and remove the `:` palette, whose job
  the dialog now does. Each becomes leader then a key; keep the letter each had where it is free, and
  record the whole table in `docs/decisions.md`. What stays without the leader is moving around only:
  up, down, page, home and end, `enter` to open the pane, `esc` to go back, `tab`, `shift+tab` and the
  number keys to change tab, and scrolling. `ctrl+c` on the session list still quits, so a stuck
  dashboard can always be left. Keys inside an overlay or form (typing an answer, `y`/`n` in a
  confirm, the switcher's filter) are not actions and stay as they are. A removed letter pressed on
  the session list raises one short notice naming the new key, for the letters removed here and no
  others. `afk attach` is not the dashboard and keeps ctrl-\. The footer's help line, the header's
  hints (the switcher hint, T6.4's inbox hint) and every notice that names a key are updated. README:
  the key reference is rewritten around the leader, and every place that names an old key is fixed.
- **Must not:** change what any action does; change a key inside an overlay; change `afk attach`.
- **Acceptance:** a table test with one row per action that the leader key runs it and the old key
  does not. UI model tests: a removed letter on the list does nothing but raise its notice; in the
  agent pane `ctrl+t`, `ctrl+g` and `ctrl+\` are typed to the agent; `ctrl+c` on the list quits. Saved
  screens for the footer and header are updated. `/usr/bin/grep -n` over README.md finds no old key
  named as a way to run an action. Against a scratch build through tmux: start a session, open its
  pane, go back to the list, switch project and quit, all through the leader. `make check` is green.

#### T7.3 — Start extend, amend and reconcile from the dashboard
- **Depends on:** T7.1
- **Goal:** three actions start a planning session that changes the active project's plan, with the
  same effect as `afk plan extend`, `afk plan amend "<what>"` and `afk plan reconcile FILE`, and open
  it in the dashboard's pane.
- **Background:** the dashboard can open a `master-plan` session that exists but cannot start one.
  The start logic is in `plan_session_cmd.go` (`decidePlanStart`, `runPlanRequest`,
  `startPlanSession`, `resolveReconcileFile`); it needs a terminal today because it ends by attaching
  the terminal to the session (`finishPlanAttach`). The agent's command line comes from the provider
  interface (`PlanArgv`).
- **Scope:** an entry point beside the command that does everything `afk plan <mode>` does up to the
  attach and returns the result (started, resumed, or refused with the reason) without printing and
  without needing a terminal; the command keeps its behaviour by calling it. A `ui.Deps` seam wired
  to it, run off the screen-drawing path. Three actions in the dialog, by name only: extend the plan;
  amend the plan, which first asks for the sentence in a one-line form; reconcile the plan with a
  document, which first asks for the file's path, taken as relative to the project. They act on the
  active project's main checkout. On success the dashboard selects the `master-plan` session and
  opens its pane, as `enter` would. A refusal is shown in the command's own plain words (no plan yet,
  another planning session open, the file is outside the project, no agent that can plan) and nothing
  is started. When a planning session is already open, the actions say so and offer to open it.
  README: the `afk plan` section and the dashboard section.
- **Must not:** name a provider outside `internal/provider`; start a session for a project with no
  plan (T7.4 does new plans); offer `--yes` for amend; change what `afk plan` prints or does in a
  terminal.
- **Not in scope:** answering the planning session's questions anywhere but in its pane.
- **Acceptance:** table tests for the entry point with a fake provider and a fake host starter: each
  mode started, a session resumed, and each refusal, with nothing written on a refusal. UI model
  tests: each action calls the seam once with the active project's root, the mode and the typed text;
  an empty sentence or path is refused in the form; a refusal is shown and no pane opens. Against a
  scratch build in a throwaway repository with a plan and a stand-in agent, through tmux: the extend
  action creates `.afk/plan/state.json` with the mode `extend` and the stand-in is in the pane; the
  same for amend with its sentence in `.afk/plan/brief.md`. `make check` is green.

#### T7.4 — Start a new plan from the dashboard
- **Depends on:** T7.3
- **Goal:** an action starts a new plan, as bare `afk plan` does, for the active project or for a
  folder the owner types, and the dashboard shows that project with the planning session open.
- **Scope:** one more action in the dialog, "plan a new project", built on T7.3's entry point and
  seam. It asks which folder in a one-line form, filled in with the active project's path when that
  project has no plan; `~/` expands. The folder must exist; what `afk plan` does with a folder that is
  not yet a git repository, or that already has a plan or an open session, the action does too, with
  the same words. On success the dashboard switches to that project, as the switcher's `enter` does,
  and opens the `master-plan` pane. A project started this way is kept in the switcher's list for the
  rest of that run even when it is neither listed in `config.toml` nor under a search folder; say in
  the README that after a restart `afk dash --project DIR` or a search folder brings it back, until
  `afk plan approve` lists it.
- **Must not:** create a folder (T7.5 does); write `config.toml`.
- **Acceptance:** table tests for the new mode through the entry point: an existing empty repository,
  a folder with a plan (refused, pointing at the change actions), a folder that does not exist
  (refused, naming it). UI model tests: the form's default, a typed path, the switch to the new
  project and its row in the switcher. Against a scratch build through tmux, with a stand-in agent:
  typing the path of an empty throwaway repository leaves the dashboard on that project with the
  stand-in in the pane and `.afk/plan/state.json` in mode `new`. `make check` is green.

#### T7.5 — Create the folder for a new plan
- **Depends on:** T7.4
- **Goal:** the owner types a project name and the dashboard makes the folder, makes it a git
  repository and starts planning in it.
- **Background:** the owner chose "The dashboard can also create the folder and make it a git
  repository", and "a typed name, created under your profile's project location". The location is the
  owner profile's `project location` entry, read through `internal/profile`. `gitInitIn` in
  `plan_session_cmd.go` is how `afk plan` makes a repository.
- **Scope:** T7.4's form takes a name as well as a path: text with no `/` and no `~` is a name. A name
  is created as a folder under the profile's project location, made a git repository, and then
  planned as in T7.4. The decision is a pure function from the typed text, the profile's entry and
  what exists on disk to one of: use this folder, create this folder, or refuse with a reason. It
  refuses a name that is not a plain folder name (say which characters are allowed), a folder that
  already exists (say so and offer to plan in it as T7.4 does), and a name typed when the profile has
  no `project location` (say how to set one with `afk profile set`, or to type a full path). Before
  creating anything it shows the full path and asks yes or no. A failure part-way (the folder made but
  `git init` failed) says what was left on disk and removes nothing. README: the new-plan action.
- **Must not:** create a folder outside the profile's project location from a bare name; delete
  anything; create anything on GitHub (T7.6).
- **Acceptance:** a table test for the decision, every branch a row: a name with a location, a name
  with none, a name with bad characters, a folder that exists, a full path. A test with a scratch
  folder as the location: the folder is created with a `.git` inside it. UI model tests for the
  confirm (yes creates, no creates nothing). Against a scratch build through tmux with a scratch
  profile whose location is a scratch folder: typing `demo` and `y` leaves `<scratch>/demo/.git` and
  `<scratch>/demo/.afk/plan/state.json`, with the stand-in agent in the pane. `make check` is green.

#### T7.6 — Offer a GitHub repository for the new folder
- **Depends on:** T7.5
- **Goal:** straight after T7.5 makes a folder, the dashboard asks whether to create a private GitHub
  repository for it with the owner's existing login, and on yes sets it as the folder's `origin`.
- **Background:** the owner chose "it also offers to create a private GitHub repository with your
  existing login". Their profile has the rule "accounts: use the accounts I already have, and ask
  before opening a new one", which is why this is a question every time and never a setting.
  `afk plan approve` pushes to `origin`, and the daemon cannot ship without one, so a project made
  here with no remote needs one before its build. `internal/ship` already talks to `gh` behind an
  interface with a fake.
- **Scope:** after the folder is made and before the planning pane opens, one yes-or-no question
  naming the repository that would be created (the folder's name, under the account `gh` is logged in
  as) and saying it will be private. No is the default and changes nothing. Yes creates the
  repository and adds it as `origin`, through a `gh` interface with a fake, off the screen-drawing
  path; nothing is pushed, because there is nothing to push yet. When `gh` is missing or not logged
  in the question is not asked and a short notice says why and what to run. A failure (the name is
  taken, the network is down) is shown with its reason, the folder is left as it is, and planning
  starts all the same. The outcome is one line in the notice area. README: the new-plan action.
- **Must not:** create a public repository; create anything without the owner's yes; push; ask for a
  folder that already existed before T7.5's form (only for one it has just made); store a token or
  read one from anywhere but `gh`'s own login.
- **Acceptance:** a table test for the pure decision (ask, do not ask and why) and for the outcome
  wording, every branch a row: yes and created, no, `gh` missing, not logged in, name taken, network
  error. A test with the fake `gh` that yes makes exactly one create call, private, and sets `origin`,
  and that no makes none. UI model tests for the question and each notice. No test calls GitHub.
  Against a scratch build through tmux with a stand-in `gh` on a scratch `PATH`: answering `y` makes
  the stand-in record one private create and `git remote -v` in the new folder shows `origin`.
  `make check` is green.

#### T7.7 — Show the planning session and the plan checker's result
- **Depends on:** T7.1
- **Goal:** the dashboard shows what `afk plan status` and `afk plan lint` print for the active
  project: the planning session's mode, phase and what it is waiting for, and the checker's counts
  and findings.
- **Background:** `internal/plansession` holds the pure status (`plansession.Decide`, read through
  `loadPlanSession` in `plan_session_cmd.go`) and `internal/planlint` the pure checker; both only
  read files. The Build tab shows one plan "problem" line today and runs no check.
- **Scope:** a `ui.Deps` seam, wired in the root package beside `dash_build.go`, that returns for a
  project's main checkout the planning status (none, or mode, phase, waiting-for and any `Problem`
  lines) and the checker's summary and findings for the plan `afk.toml` names. It is read on the
  dashboard's refresh, off the screen-drawing path, from files only. Shown in two places: a plan line
  in the overview of the `master-plan` session and in the Build tab's summary block (`extend · rounds
  · 2 questions have no answer yet`, or `plan waiting for your approval` for a new plan at `review`),
  and an action in the dialog, "check the plan", that opens a scrollable overlay with the summary
  counts and one finding per line, errors first, worded as `afk plan lint` words them. With no plan
  the action says there is no plan to check. README: the Build tab paragraph and the new action.
- **Must not:** dial, use the network or block (§3); move the session's phase or write any file under
  `.afk/plan/`.
- **Acceptance:** a table test for the plan line's wording, a row per case: no session, each mode in
  a middle phase, a new plan at `review`, a finished change session, a damaged `state.json`. UI model
  tests for the overview line, the Build tab line and the overlay with zero findings, warnings only
  and errors, at narrow and wide widths. Against a scratch build in a throwaway repository whose plan
  has one error and an open `extend` session at `rounds`: the Build tab shows the line and the
  overlay lists the error. `make check` is green.

#### T7.8 — Approve a plan from the dashboard
- **Depends on:** T7.7
- **Goal:** an action approves the active project's plan, with the same effect as `afk plan approve`,
  behind a confirm that shows the checker's counts.
- **Background:** the owner chose "Yes, behind a confirm step". T5.7 forbids approving from the web
  page, which other people might reach; the dashboard is the owner at their own terminal.
  `runPlanApprove` in `plan_approve_cmd.go` runs the steps (write `afk.toml`, commit, push, start the
  ledger, list the project in `config.toml`, write `approved`), each safe to repeat, and refuses with
  a reason ending "Nothing was changed.".
- **Scope:** an entry point beside the command that runs the same steps and returns what was done,
  what was refused and why, or which step failed and what is left, without printing; the command
  keeps its output by calling it. A `ui.Deps` seam wired to it. An action in the dialog, "approve the
  plan", enabled only when T7.7's status is a new plan at `review`; otherwise it is dimmed with the
  reason. It opens a confirm showing the project, the plan's path and the counts from the checker's
  summary; with one or more errors the confirm has no yes and says to fix them. On yes the steps run
  off the screen-drawing path, since they commit and push, with a "working" notice; the result is
  shown in plain words: approved and what the daemon will do next, or the refusal, or the step that
  failed with what to run to finish. The plan line of T7.7 and the all-projects screen pick up the new
  state on the next refresh. README: `afk plan approve` and the dashboard section.
- **Must not:** approve without the owner's yes in the confirm; approve a plan with errors; approve
  from the web page; change what `afk plan approve` prints or does.
- **Acceptance:** table tests for the entry point with fakes for git and the config file: approved,
  each refusal, a failure at each step, and a second run that does nothing. A table test for the
  result wording, one row per outcome. UI model tests: the action is dimmed away from `review`, the
  confirm shows the counts, errors remove the yes, `n` and `esc` call nothing, `y` calls the seam
  once. Against a scratch build in a throwaway repository with a new plan at `review`, a scratch
  config folder and a scratch state folder: `y` leaves the plan committed, the project in the scratch
  `config.toml` and `state.json` at `approved`. `make check` is green.

#### T7.9 — The all-projects screen
- **Depends on:** T7.1
- **Goal:** one screen lists every project the daemon builds, each with its current task, the state
  of its build and how many inbox items need the owner, and `enter` switches the dashboard to it.
- **Background:** the owner chose "An all-projects screen". `afk daemon status` prints this per
  project today. `web.Summarize` is the pure per-project summary the web page and the Build tab
  share, `buildview.OpenCounts` counts inbox items, the dashboard already reads the `[[project]]`
  list through the `LoadListed` seam (T6.1), and `plan-reviews.json` lists plans waiting for approval.
- **Scope:** an action in the dialog, "all projects", that opens a full-width screen with one row per
  project in `config.toml`'s list, plus the active project when it is not listed. Each row: the name
  the switcher shows; tasks done of the total; the current task with its phase, or why nothing is
  running, in the words `afk daemon status` uses (paused, stopped and why, waiting on you, held for
  usage, nothing ready, finished); open inbox items and how many are holding a task up; and "plan
  waiting for approval" when it is on the review list. Rows that need the owner sort first. `enter`
  switches to the project, `esc` closes the screen, movement keys move. The rows come from a
  `ui.Deps` seam wired in the root package that reads each project's files on the refresh tick, off
  the screen-drawing path; a project whose files cannot be read gets a row saying so and never hides
  the others; a listed folder that is gone is shown as gone. With the screen open, T6.3's pause and
  resume actions act on the selected row. With no listed project the screen says how to add one.
  README: a section for the screen.
- **Must not:** dial, use the network or block (§3); write anything but through the pause and resume
  actions; replace the project switcher.
- **Acceptance:** a table test for the pure row builder: running, paused, stopped, parked on an inbox
  item, held for usage, finished, no plan, plan waiting for approval, unreadable, gone; and for the
  sort order. UI model tests: the screen at narrow and wide widths, `enter` switching project, pause
  on the selected row calling the seam with that row's root. Against a scratch build with a scratch
  config folder listing two throwaway repositories, one paused with an open inbox item: both rows
  show, the paused one first with its count, and `enter` switches to it. `make check` is green.

#### T7.10 — Add and remove projects from the all-projects screen
- **Depends on:** T7.9
- **Goal:** on the all-projects screen the owner can add a folder to the daemon's project list and
  remove one, with the same effect as `afk project add` and `afk project remove`.
- **Background:** `addProjectToConfig` and `runProjectRemove` in `project_cmd.go` edit `config.toml`
  as text, so comments and the `[dash]` table survive. The daemon re-reads the list on every pass.
  Until now the dashboard was forbidden to write `config.toml` (T6.1, T6.2); this task is the one
  exception, and it writes only through that code.
- **Scope:** entry points beside the two commands that return what happened (added, already listed,
  removed, not listed, refused and why) without printing; the commands keep their output by calling
  them. Two `ui.Deps` seams. Two actions, enabled on the all-projects screen: add a project, with a
  one-line form for the folder (filled in with the active project when it is not listed) and an
  optional name; remove the selected project, behind a confirm that names it and says the folder, its
  plan and its sessions are not touched and a running task is left to finish. The outcome is a short
  notice and the row appears or goes on the next refresh. What `afk project add` refuses (not a git
  repository, no plan the daemon could build from, whatever the command checks) is refused here in
  the same words. A `config.toml` that cannot be read or written is said so, naming the file.
  README: the screen's section and `afk project`.
- **Must not:** write `config.toml` any other way; delete a folder, a session or a ledger; remove
  without the confirm.
- **Acceptance:** table tests for the two entry points with a scratch config file: each outcome, and
  that comments and a `[dash]` table are byte for byte as they were. UI model tests: the form's
  default, add calling its seam once, remove asking first and calling its seam only on `y`, each
  notice. Against a scratch build with a scratch config folder: adding a throwaway repository shows
  its row and `afk project list` (same scratch folder) prints it; removing it takes both away.
  `make check` is green.

#### T7.11 — Pick a task on the Build tab and set its status
- **Depends on:** T7.1
- **Goal:** the Build tab has a cursor on its task rows, and an action sets the selected task's
  status by hand, with the same effect as `afk ledger set`.
- **Background:** the Build tab (`internal/ui/buildtab.go`, rows from `internal/buildview`) is a
  read-only list with no cursor. T3.2 decided the dashboard never writes the ledger; the owner has now
  asked for "set a task's status by hand" in the dashboard, so this task is the one exception, and it
  writes only through the code behind `afk ledger set` (`runLedgerSet` in `ledger_cmd.go`), which
  checks the ids against the plan.
- **Scope:** a cursor on the Build tab's task rows when the detail side has the keyboard, moved with
  the movement keys, kept on the same task id across refreshes, and scrolled into view; the tab
  still shows the same thing whichever session is selected. An entry point beside the command that
  sets one task's status and returns the outcome without printing; the command keeps its output by
  calling it. A `ui.Deps` seam. An action, "set this task's status", enabled on the Build tab with a
  task selected: it lists the statuses the command accepts with the current one marked, asks for the
  replacing ids when the choice is `superseded`, and then confirms in one sentence what will change
  ("T2.3: failed → pending. The build loop may start it on its next pass."). A refusal from the
  command is shown in its words. The row shows the new status on the next refresh. README: the Build
  tab paragraph.
- **Must not:** write the ledger any other way; change a task that is running or shipping without
  saying in the confirm that an agent is working on it; add `afk ledger init` to the dashboard.
- **Acceptance:** a table test for the cursor's pure movement and for where it lands when its task
  leaves the list. Table tests for the entry point with a scratch ledger: each status, `superseded`
  with and without replacements, an unknown id, a ledger that cannot be written. UI model tests: the
  cursor at narrow and wide widths, the status list, the confirm, `n` calling nothing and `y` calling
  the seam once with the id and status. Against a scratch build in a throwaway repository with a
  five-task plan and a ledger: setting a `failed` task to `pending` changes
  `.afk/build/ledger.json` and the row. `make check` is green.

#### T7.12 — Tokens for the selected task
- **Depends on:** T7.11
- **Goal:** the Build tab shows how many tokens the selected task used, as `afk tokens <task>` prints
  it.
- **Background:** the token record is kept with the ledger (§4.7) and printed by `runTokensLedger` in
  `tokens_cmd.go`. T5.8 found that planning sessions record no tokens, so only master-plan tasks
  have a record.
- **Scope:** a `ui.Deps` seam, wired in the root package, that returns the selected task's token
  record from the files the command reads, on the refresh, off the screen-drawing path. An action,
  "show this task's tokens", enabled on the Build tab with a task selected, opens a small overlay with
  the totals and the split by phase and role, in the command's own words and units; a task with no
  record says "no tokens recorded for this task yet". The Build tab's row for the selected task also
  carries the total when there is room, and gives it up first as the tab narrows. README: the Build
  tab paragraph.
- **Must not:** record or change tokens (`--record` stays a command); read a session's transcript on
  the refresh.
- **Acceptance:** a table test for the overlay's rows and the short total: a full record, one phase
  only, no record, an unreadable record. UI model tests at narrow and wide widths. Against a scratch
  build in a throwaway repository whose ledger holds tokens for one task: the overlay for that task
  matches what `afk tokens <task>` prints there, and another task says none recorded. `make check`
  is green.

#### T7.13 — Refresh the usage reading from the dashboard
- **Depends on:** T7.1
- **Goal:** an action takes a fresh usage reading, with the same effect as `afk usage --refresh`, and
  the header shows it.
- **Background:** the header shows the last reading in `usage.json` and says when it is old (T3.2).
  `runUsage` in `usage_cmd.go` takes a reading through the provider interface and saves it; the
  probe starts the agent's CLI and takes a second or two, and `governor.json` records the last
  attempt.
- **Scope:** an entry point beside the command that takes and saves a reading as `--refresh` does and
  returns the reading or the reason there is none, without printing; the command keeps its output by
  calling it. A `ui.Deps` seam, run off the screen-drawing path with a "reading usage" notice. An
  action, "refresh usage", in the dialog. One reading at a time: the action does nothing while one is
  running. The header updates when it lands; a failure (the agent's CLI is missing, not logged in,
  the provider has no usage reading) is a notice in plain words and the old reading stays. README:
  the header's paragraph and `afk usage`.
- **Must not:** name a provider outside `internal/provider`; probe on the refresh tick or on any
  timer (only on the owner's action); change the governor's thresholds or the daemon's own probing.
- **Acceptance:** table tests for the entry point with a fake provider and a fake clock: a reading
  saved, each failure, and that the files written are the ones `afk usage --refresh` writes. UI model
  tests: the action calls the seam once, a second press while it runs calls nothing, each notice, and
  the header after a reading. No test probes real usage. Against a scratch build with a scratch state
  folder and a stand-in for the agent's CLI on a scratch `PATH`: the action writes `usage.json` and
  the header shows the stand-in's numbers. `make check` is green.

## 9. Deferred / later

- **`afk plan`** moved to M5 (2026-10-13).
- **Answering plan questions on the web page** instead of the terminal; M5 keeps them as records on
  disk so this needs no change to the planner.
- **Prototypes during planning** (a throwaway page or state model to answer a design question), after
  Matt Pocock's prototype skill.
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
| **Answered 2026-10-12 (T2.4):** end the session's agent and start it again with its resume command; the spike and the other ways tried are in `docs/decisions.md` under `## T2.4`. Amended 2026-10-12 (T2.4) | T2.4 |
| What exactly a session emits when it hits a usage limit (text, hook, whether a reset time is present). Never observed; capture it the first time it happens. | T2.4 |
| **Amended 2026-10-12 (T2.4): still not observed.** Nobody trips a limit on purpose, so the daemon relies on the `StopFailure` marker (which carries no reset time) and the usage reading. | T2.4 |
| Is the login-expiry warning detectable ahead of time, or only the logged-out state? | T2.4 |
| **Answered 2026-10-12 (T2.4): no.** The status output has no date in it, so only the logged-out state is detectable (§6.5). Amended 2026-10-12 (T2.4) | T2.4 |
| Liveness with no client: host-side schedule, or daemon holds the client slot? | T2.3 |
| **Answered 2026-10-12** (the row above): host-side schedule. The host probes by itself when started with `--probe-unattached`, because a daemon holding the client slot would lock out a human who attaches. | T2.3 |
| Does `claude -p "/usage"` behave when its backing request is throttled, and how often is safe to probe? **Answered 2026-10-11:** not observed. The probe was run three times back to back (Claude Code 2.1.296) and each time exited 0 with a full report, in about 1.5 seconds. Nobody tried to provoke throttling, because tripping a rate limit on the owner's account on purpose is not a machine's call, so what it does when throttled is still unknown. `afk usage` reuses a reading under 5 minutes old, and the governor treats a reading over 30 minutes old as stale and holds. | T1.3 |
| Nebari's health-check URL, and how to tell a deploy of *this* commit finished (as opposed to the old container still answering). | T4.1 |
| **Answered 2026-10-12 (T4.1):** the health URL is `https://nebari.haener.cloud/up`. A deploy of this commit cannot be told apart from the old container, because nothing in nebari shows the revision (a migrating deploy answers 503 first), so the check means "healthy" and no more. A revision endpoint in nebari would close it. | T4.1 |
| Does a merge to nebari's main trigger the Coolify deploy today, or does a webhook need adding? | T4.1 |
| **Answered 2026-10-12 (T4.1):** not answerable from the repository. No workflow or webhook calls Coolify, and nebari's docs say to press Deploy. It is an owner check in `docs/runbook-vm.md`, to be done before T4.2. | T4.1 |
| What the 28k of extra start-up context in implementation and review agents consists of. **Answered 2026-10-11:** tools and listings the agents never use. On the T1.4 session every sub-agent started at about 35,000 tokens, and about 26,700 of a task agent's were removable: the `Artifact` tool definition (about 15,000), the skills list (5,500), `SendFeedback` (2,500) and other unused tools and connector names (3,700). Part sizes are estimated from measured character counts. The plugin's agent definitions now leave them out, and the itemised tables are in `docs/decisions.md` under `## T1.5`. | T1.5 |
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
