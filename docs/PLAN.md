# afk — Master Plan

> Say "go", walk away. afk is an always-on runner that builds a whole application from a master plan:
> it picks the next ready task, runs a coding agent through plan → build → review, ships on green CI,
> paces itself against the agent subscription's usage limits, and only bothers its owner for things
> only the owner can do.

**How to use this document.** This is the master plan. Each task (`T<milestone>.<n>`) is sized for one
autonomous plan → execute → review cycle. Read **§1–§7 in full** before planning any task: they are the
shared contract every task builds against. Never start a task whose dependencies are not merged.
Where this plan and `docs/design-system.md` disagree, the plan wins on behaviour and the design system
wins on looks. That design system covers the web page only (M8). The terminal dashboard's is
`docs/dash-design.md` (M16), and the same rule holds for it.

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
| Web surface | A small server-rendered page served by `afk daemon` (Go `net/http` + `html/template`, no JS build step). Loopback by default; protected by a password the owner sets with `afk web password` (T8.18; it replaced the shared token of T3.1); meant to sit behind Tailscale or the owner's reverse proxy. |
| Config format | TOML, hand-editable: `afk.toml` committed in each project, `~/.config/afk/config.toml` per machine. |
| One machine settings file | Decided 2026-10-13 (M6): `config.toml` is the machine's only settings file. The dashboard's project switcher lists the `[[project]]` entries the daemon builds, plus any project found under the search folders in its `[dash]` table. `config.json`, carried over from forge, is no longer read; when one is found, afk says once what to move and touches neither file. |
| Dashboard controls | Decided 2026-10-13 (M6): `afk dash` gets pause and resume keys and an inbox count in its header. T3.2 left both out only because its scope did not list them. |
| Dashboard as command center | Decided 2026-10-13 (M7): `afk dash` does the owner's day-to-day work, so the shell is needed only for set-up. It starts all four planning sessions (new, extend, amend, reconcile), approves a plan behind a confirm, has an all-projects screen, adds and removes projects, sets a task's status by hand, shows a task's tokens, shows the planning session and the checker's result, and refreshes the usage reading. The owner profile, installing the plugin, starting and stopping the daemon and `afk ledger init` stay commands. |
| Dashboard keys | Decided 2026-10-13 (M7): one leader key, **ctrl+space**, works everywhere in the dashboard, the agent pane included, except while an overlay, form or confirm is open (`esc` leaves those first), and opens an actions dialog at once listing every action with its key. **Every action is under the leader**: the session list keeps only the keys that move around, and the bare letters, the ctrl chords and the `:` palette go (T7.2). The one exception is `ctrl+c` on the session list, which still quits so a stuck dashboard can always be left. The owner chose this over keeping today's letters as shortcuts. `afk attach` keeps ctrl-\. Replaced 2026-10-21 by the row "Dashboard levels and keys" (M16). |
| Leader key | Decided 2026-10-14 (M9): the leader is **ctrl+]** by default, replacing ctrl+space, which Raycast takes on the owner's Mac (and ctrl+2 and ctrl+@, the same byte, go with it). It is set with `leader = "..."` in the `[dash]` table of `config.toml`; a key the dashboard cannot use falls back to ctrl+] with a line in the header saying what to fix (the owner chose the header over refusing to start). ctrl+m, the owner's first idea, is the same byte as Enter unless the terminal and tmux report it apart, so it was not made the default; the owner uses ctrl+]. Everything else in the row above stands. |
| Dashboard look | Decided 2026-10-21 (M16): `afk dash` is redesigned from `docs/dash-design.md`, drawn with the owner on a Claude Design canvas after allisonhere's terminal apps (tidemail, TideFTP, whatthedock). Three levels, **All projects → Project → Agent**, each drawn over the dimmed one before it, replace the session list beside a right side; the agent gets nearly the whole screen, and a session's details are an overlay (`d`). The dashboard paints its own background from a named theme (`tide`, `fjord`, `paper`, `phosphor`; `[dash] theme` in `config.toml`, default `tide` on a dark terminal and `paper` on a light one), and its colours mean what the web page's do. The owner chose this layout over a restyle of today's two panes and over a three-pane layout with the agent in the middle, and dropped a variant with a session list beside the agent. |
| Dashboard levels and keys | Decided 2026-10-21 (M16), replacing M7's "Every action is under the leader": at All projects and Project a key works alone, because no agent is on screen to take it; in the Agent every key goes to the agent, and the leader (ctrl+], still set in `config.toml`, M9) followed by the same key does the same thing. The same letter does the same thing at every level (`docs/dash-design.md` §5). `enter` goes in a level and `esc` comes back out. `]` and `[` go to the next and previous thing that needs the owner across every project, `/` jumps to any project, session or task, and `:` searches actions by name, where letters always type, so a plan action such as "approve" no longer has to dodge a key. The leader alone shows a small strip of the keys that work after it. M7 put every action under the leader because the list and a live agent shared the screen; with the agent on its own level that reason is gone. `ctrl+c` still quits from every level but the Agent, and `afk attach` keeps ctrl-\. |
| What the dashboard writes | Decided 2026-10-13 (M7): besides inbox answers (T3.2) and pause and resume (T6.3), the dashboard changes afk's state only through the code of the matching command: `afk plan approve` (T7.8; approving from the web page was forbidden here, because others may reach it, and M8 lifts that: see the row "Web page as command center"), `afk project add` and `remove` for `config.toml` (T7.10), and `afk ledger set` for the ledger (T7.11). This replaces "the dashboard never writes the ledger" (T3.2) and "never writes `config.toml`" (T6.1, T6.2) for those actions only. Creating a new plan's folder and its GitHub repository (T7.5, T7.6) has no matching command and is governed by the row below. |
| New plan in a new folder | Decided 2026-10-13 (M7): the dashboard can create the folder for a new plan. A typed name is created under the owner profile's `project location` (From the owner profile, a default: `~/code`) and made a git repository; the dashboard then asks, each time, whether to create a private GitHub repository with the owner's existing `gh` login and set it as `origin`. Never without that yes, per the profile's rule on accounts. |
| Web page as command center | Decided 2026-10-13 (M8): the web page does everything `afk dash` does except attach to a session. It holds a planning session as a chat (the agent's messages, a reply box, a button per offered option), never as a terminal; a reply sent while the dashboard has that session open takes the session and the dashboard pane is dropped with a notice. A planning session waiting for the owner sends an ntfy notification linking to its chat. Every owner-only action the dashboard has comes to the page behind a confirm, approving a plan (T8.11) and creating a new project's folder and private GitHub repository (T8.15) included, which replaces "approving from the web page stays forbidden" (T5.7, T7.8). Of coding sessions, only autonomous ones are started from the page (T8.10), because the owner cannot talk to one there; planning sessions are started from the page and held in the chat view (T8.14, T8.15). The password sign-in (T8.18) and the `Origin` check guard all of it; the reply box is a keyboard into the planning agent, so the page stays behind Tailscale or the owner's proxy. |
| Web page technology | Decided 2026-10-13 (M8): hand-written JavaScript built into the binary, with no build step, no `npm` and no new Go dependency for live updates (server-sent events). Still server-rendered with `html/template`; the page works, by reloading, with JavaScript off. Phone first, and it works well on a desktop. Every screen meets WCAG 2.2 AA. |
| Web page design | Decided 2026-10-13 (M8): redesigned from `docs/design-system.md`, which the owner makes in Claude Design (From the owner profile, a default: design tool) from `docs/design-brief.md` (T8.1). The plan wins on behaviour and the design system on looks. No component library (From the owner profile, a default). |
| Desktop back button | Decided 2026-10-16 (M10): above 760px every page but the overview and the error page shows the phone bar's back chevron and its title in a row under the top bar, going where the phone's back button goes. Built from the design system's existing parts (`icon-btn`, the title style) with no design round. Amended 2026-10-19: the design system now draws it as `app-bar app-bar--back app-bar--desktop` under the `topnav`, with optional meta; T10.3 rebuilds it in that shape. In-page "Back to …" links that only repeat it are removed; a confirm's safe button and a Cancel beside a form stay. |
| Where afk runs, and installing it | Decided 2026-10-17 (M11): afk is an always-on runner whose agents run with permissions bypassed, so the target is **a machine set aside for it**: a cloud server, a spare computer, or a VM on the owner's laptop (Multipass). The docs say so before any install step. A laptop used every day is supported, on Linux and macOS, behind a plain "at your own risk" warning. A **Docker image was considered and set aside**: the agent must build and test the project, so the project's own packages, databases and browsers would have to live in the image or in sidecar containers, the dashboard would only work through `docker exec`, and a VM is a stronger boundary with none of those costs. Install is **`afk install`**, a guided installer built with the charm modules, started by a small bootstrap script that downloads a release binary. It writes Claude Code's `skipDangerousModePermissionPrompt` itself, with no separate prompt to accept (the owner's choice), after its own safety screen. The README is a short overview, and the reference lives in pages under `docs/`. |
| Releases | Decided 2026-10-19 (M12): a release is published **automatically on every merge to `main` that changes the binary**, reversing T11.1's "never publish on a push to `main`", because the first release was never made by hand and the one-line install had nothing to download. A push that changes only docs, the plan, scripts or workflows publishes nothing. The bump is read from the commit messages: a breaking change raises the major, a `feat` the minor, anything else the patch; the first release is `0.1.0`. A tag pushed by hand still publishes, which is how to choose a version such as `1.0.0`. The licence is **MIT**, copyright Darin Haener. A downloaded darwin/arm64 binary runs without `codesign` (release dry run, 2026-10-19). |
| Bug fixes | Decided 2026-10-19 (M13): `afk plan fix` is a fifth planning mode, a change session like amend. It reproduces the bug and finds its cause, then adds **one** task titled with `(fix)`, whose Acceptance is a test that fails today, to a standing milestone titled `Fixes` (created by the first fix). The owner confirms the entry first unless `--yes`. The task that caused the bug gets an Amended line naming the fix. The daemon picks a ready `(fix)` task **ahead of every other ready task**. |
| Planning sessions end themselves | Decided 2026-10-19 (M13): a finished planning session never blocks the next one. `afk plan approve` ends a new session's agent; a change session's agent is ended once it is at `review`, has saved its summary to `.afk/plan/summary.md` and is waiting for the owner, by the daemon on its next pass or by the next `afk plan`. The files in `.afk/plan/` stay until the next session archives them. |
| Native notifications | Decided 2026-10-20 (M15): the web page installs as an app (a manifest, the owner's icons, a service worker) and sends **web push** to every device signed up on a machine-wide Notifications page, which the owner designs in Claude Design (From the owner profile, a default: design tool) and which lists the devices with Remove. It replaces ntfy, which is **removed completely** (T15.8); an `ntfy_url` left in a settings file is ignored without a word (the owner's choice). Push goes only for what blocks on the owner: an inbox item that blocks a task, the urgent notices and a planning session waiting; the idle and plan-ready notices go. Push needs an `https://` address, so over plain `http://` the page says so, and in iPhone Safari it shows the Add to Home Screen steps. The owner makes each icon size by hand (T15.1). afk makes its own push key and keeps it in its state folder. macOS desktop toasts stay, with the new icon in place of the pickaxe. |
| The hub | Decided 2026-10-23 (M17): the build machine is **the hub**. It runs afk's daemon and a Rex server, holds every `.afk/` file, and is reached from anywhere over Tailscale: the owner's laptop through Rex for Mac, a phone through Rex for iOS (in beta) and the web page. **afk is the brain, Rex is the body**: afk keeps the plan, the loop, the policy and every file; Rex holds the terminals, the windows, the remote access and every client. afk shows its state in Rex's own terms (program status records, session and window labels, palette actions), so each Rex client shows it without afk building anything for that client. The laptop runs Rex for Mac and nothing of afk (the owner chose the simplest start; a laptop `afk` that acts on the hub can come later). |
| Rex is optional | Decided 2026-10-23 (M17): afk's own session host stays, and Rex is a second way to run a session, chosen per machine with `[runtime] backend` in `config.toml`. A session is driven by the runtime that made it for its whole life (`host.json` says which). afk is for hobbyists on any machine, and Rex is in beta with its Linux server in testing waves, so nothing afk does may need Rex. Whether Rex becomes the default, and whether the afk-only attach and `internal/termpane` retire, is decided after M17's proof (§10). |
| Agents open in Rex | Decided 2026-10-23 (M17): for a Rex-backed session, going into an agent (the dashboard's Agent level, `afk attach`) hands off to Rex, which shows the agent's own window. Rex already does what the Agent level imitates: many viewers at once, scrollback, sizing, and reaching it from any device. The dashboard keeps All projects and Project and becomes the **control room**, kept running on the hub in a Rex session named `afk` so any Rex client can open it. Sessions run by afk's own host keep today's Agent level. |
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
| `.afk/{session}/host.json`, `host.log` | session host | Liveness and discovery. Independent of `status.json` by design. Two more fields, each omitted when unknown, and neither goes into `status.json`: `agent_status` (`state`, `kind`, `msg`, `at`) is the latest root Program status report (OSC 7501) from the session's own agent, republished at most every 2 seconds on change and at once on a state change; `last_exit` (`code`, `signal`, `at`) is how the previous program ended, written when a restart in place ends its generation. The exit code is also in `host.log`. `runtime` names the runtime that made the session (`afk` today; absent means `afk`), and every caller that starts, stops, restarts, types into or opens a session picks its implementation from it, never from the machine's setting. |
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

Amended 2026-10-14 (T8.15): `being-planned.json` is a file in afk's state folder (beside
`plan-reviews.json`), the projects whose planning was started from the web page's Plan a new project
action and are not approved yet. Writers: the page adds an entry after a started or resumed start
(through `internal/daemonstate`, by atomic rename) and prunes entries whose planning session is gone
when it starts one; `afk plan approve` removes the entry. Readers: the daemon's web page, which shows
the project as being planned and serves its chat. Shape: `{"projects":[{"name","root","at"}]}`, the
list always present and every key always written. `root` is the project's main checkout, cleaned, and
the key an entry is matched by; `name` is the folder name the page serves the project under, with `-2`,
`-3` added when a configured project, a review entry or another entry already has it, and it does not
change when planning is resumed; `at` is when the start last succeeded, RFC 3339 UTC. A session is
"gone" when the project has no `.afk/plan/state.json`, or the session is finished or approved; a damaged
`state.json` is not gone. A missing or damaged file reads as an empty list.

Amended 2026-10-14 (T8.13): `plan-waits.json` is a file in afk's state folder (beside `being-planned.json`), the planning waits the daemon has already sent a notification for. Writer: the daemon only, on each pass, through `internal/daemonstate` by atomic rename; it is written only after a send went out, so a failed send is tried again. Readers: the daemon, on each pass, which skips a wait it has recorded. Shape: `{"waits":[{"root","key","at"}]}`, the list always present and every key always written. `root` is the project's main checkout, cleaned, and the key an entry is matched by; `key` identifies the wait that was notified (the chat's state, its message count, and its last message's time and text, hashed); `at` is when the notification was sent, RFC 3339 UTC. An entry is removed when the project's planning chat reads none (its planning session is gone); it is kept while the session works or cannot be read, so a passing read failure does not notify the same wait twice. A missing or damaged file reads as an empty list.

Amended 2026-10-17 (T8.18): `web-password.json` is a file in afk's state folder (beside `usage.json`). Writer: `afk web password` only, through `internal/webpass`, by a temp file and rename, mode 0600. Readers: the daemon at start (the page is on when the file exists and is a valid record), and the page on every sign-in and every cookie check, so a new password takes effect with no restart. Shape: `{"algorithm","iterations","salt","key"}`, with `algorithm` `pbkdf2-sha256`, `salt` and `key` base64 (the key is 32 bytes), `iterations` the PBKDF2 count the record was made with. No copy of the password is kept. A missing file means the page is off. A damaged or unreadable one is logged by the daemon and treated as no password.

Amended 2026-10-20 (T15.4): push-key.json and push-devices.json are files in afk's state folder (beside `web-password.json`). `push-key.json`: writer is `internal/webpush`, made the first time a push key is needed (by the daemon or the page, T15.5/T15.7) and never rewritten; written by a temp file and rename, mode 0600. Readers: the page (the public key, for subscribing) and the sender (for signing). Shape: `{"private_key","public_key","created_at"}`, with the keys base64url without padding (the private key a 32-byte P-256 scalar, the public key a 65-byte uncompressed point) and `created_at` RFC 3339 UTC. A damaged file is an error that names the file and is never replaced, since replacing it would silently cut off every device that signed up. `push-devices.json`: writers are the page (adds a device on sign-up and removes one, T15.5/T15.6) and the daemon (removes a device the push service reports gone, T15.7), both through `internal/webpush`, by a temp file and rename, mode 0600. Readers: the daemon (who to send to) and the page (the list). Shape: `{"devices":[{"id","endpoint","p256dh","auth","label","at"}]}`, the list always present and every key always written. `id` is a random hex id, `p256dh` and `auth` are the subscription's keys as the browser gives them (base64url), `label` is the browser and system from the user agent, and `at` is when the device signed up (RFC 3339 UTC). There is one entry per endpoint: signing up again replaces that entry and keeps its id. A missing or damaged file reads as an empty list.

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

Amended 2026-10-21 (T15.8): [notify] ntfy_url is no longer read; a file that still has it loads as before and nothing is logged. Push (T15.7) is the only phone notification.

Machine config `~/.config/afk/config.toml`: projects the daemon runs, governor thresholds (§6.3), web
bind address and public address, provider choice (the web password is in the state folder, not here).

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

Amended 2026-10-17 (T8.18): `[web] token` is gone. A `token` key left in the file is ignored, and the daemon logs one line that names the file and says so, never its value. The page is on when the password file exists (see `web-password.json` in §4) and off otherwise; a `bind` other machines can reach with no password stops the daemon, naming `afk web password`. Sign-in is a form on each page: a right password sets the cookie `afk_web` (valid 180 days, marked `Secure` only for an `https://` `public_url`) and sends the browser back only to a local path (`next`, checked by `returnTo`). Without a valid cookie a page shows the sign-in form and every `/api/` address answers a JSON 401. `?token=`, `Authorization: Bearer` and a cookie from the token days no longer let anyone in. Five wrong passwords in a row lock sign-in for one minute, for the whole page; a try made while locked is refused and does not extend the lock. A new password signs every browser out, and `POST /signout` (the Sign out button on both bars) signs out one browser. The `Click` rule: a notification carries a `Click` link only when the page is on (a password set) and `public_url` is set, and the link never holds a password or a cookie.

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
Amended 2026-10-21 (T16.1): [dash] also reads theme (tide, fjord, paper or phosphor; unset means tide on a dark terminal and paper on a light one). A value of the wrong kind lands in DashErr; an unknown name is reported by the dashboard in the [dash] toast and tide is used. Read once at start.

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
| `summary.md` | the planning session, at `review`, in every mode | The summary it prints at `review`, written before it is printed; `afk plan status` prints it at `review`. |

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

Amended 2026-10-19 (T13.1): every mode's session writes `summary.md` at `review` before printing it.
`afk plan status` prints it under its status lines when the phase is `review` and the file is not blank.
A missing or blank file means no summary and is never an error, and the file gates nothing. It is
archived with the rest of the session's files when the next change session starts.

Amended 2026-10-19 (T13.3): `internal/planend.Decide` ends a finished change session's agent (summary saved, chat waiting, host alive) from the daemon's planning hook and from `decidePlanStart` (`OldAgentEnds` / `EndOldAgent`), with `session.StopHost`; `.afk/plan/` is untouched; each end is a `plan_session_ended` line in `.afk/log/decisions.jsonl`.

Amended 2026-10-19 (T13.5): `state.json`'s `mode` may also be `fix` (`afk plan fix`). Its path is amend's: `brief`, `survey`, `draft`, `check`, `review`, with `changes.md` as the `draft` gate and no critic. It is a change session finished at `review` and never approved. `afk plan fix --yes` writes `yes`, as amend does, and `planend` ends it like the other change modes. Its one task goes in the milestone titled `Fixes`, which the first fix creates.

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

Amended 2026-10-19 (T13.4): a task whose title carries `(fix)` or `(fix; …)` (case does not matter, spaces
inside the brackets are allowed) is a fix task, read through `masterplan.Task.Fix()`; the title stays as
written. `(fixes)`, `(fix up)` and `(prefix)` are ordinary prose. A title carrying both `(fix)` and
`(owner)` is a lint error, `fix-and-owner`, which `afk plan lint` reports and exits 1 on.

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

Amended 2026-10-19 (T13.4): a fix task (§5.1) that is ready goes ahead of plan order but behind started work. The
choice is: in-progress (running or shipping) first, then a parked task whose items are all answered, then the
first ready fix task in file order, then the first ready task in plan order. A ready fix task with unmet
dependencies is not ready and does not win. Open-question holds are unchanged: they still come after the choice,
in `decideNext`, so a held fix task is parked like any other.

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
──▶ M11 Install on a fresh machine (added 2026-10-17)
 T11.1 releases    T11.2 install plan    T11.3 Linux    T11.4 macOS    T11.5 bootstrap    T11.6 prove it
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
| `state.json` | `{mode: new\|extend\|amend\|reconcile\|fix, phase, updated_at}` |
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
| Where questions are asked | The terminal, in an attended session on the machine that runs the daemon. Not the inbox. A web interface may answer the same records later (rule 7). Amended 2026-10-13 (M8): the web page holds the session itself as a chat view (T8.12), not through the records. |
| What v1 covers | New project, extend (a new milestone), amend (one task) and reconcile (bring the plan in line with a new document). |
| Approval | Required before the daemon builds anything. The plan is readable on the web page; approving is a terminal command. Amended 2026-10-13 (M8): approving from the web page is allowed, behind a confirm (T8.11). |
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
- Amended 2026-10-13: approving from the web page is no longer forbidden; M8 adds it behind a confirm (T8.11), because the owner chose every owner-only action on the page.

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
- Amended 2026-10-13 (T6.3 done): later tasks may assume the following. (1) `afk dash` pauses with `P` and resumes with `U`, both registered in `inputNormal` and `inputDetail` after `keys.Inbox` (so the palette and saved screens before them did not move) and nowhere else, so in the agent pane they are typed and in the inbox overlay they do nothing. (2) `ui.Deps` has `PauseBuild func(root string) (already bool, err error)` and `ResumeBuild func(root string) (ui.ResumeResult, error)`, with no default, wired only in `dashDeps` to `dashPauseBuild` / `dashResumeBuild` in `dash_pause.go`, which call `pauseProject` / `resumeProject` unchanged; the notice wording is in `internal/ui/buildpause.go`. (3) The keys act on the Build snapshot's `Root` (the main checkout), drop a result from an earlier `projectGen`, and reload the Build snapshot after every result, errors included. (4) A paused build's summary reads `paused: U or afk resume starts it again`. (5) A failed flag or marker write is worded by `dashPauseError`, reason first, because the toast shows about three lines; the web page keeps `webPauseError`. T6.4's header segment should take its place in the header order without moving these keys. See `## T6.3` in `docs/decisions.md`.

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
- Amended 2026-10-13 (T6.4 done): later tasks may assume the following. (1) The header's inbox segment is built in `headerPieces` (`internal/ui/views.go`) from `buildview.OpenCounts(m.build.Items)`: `inbox N`, `inbox N · M blocking`, the short form `M blocking`, and the hint constant `headerInboxHint` (`i: inbox`), which is hidden while the agent pane has the keyboard. T7.2 changes that one constant when `i` moves under the leader. (2) `headerLine` is one drop sequence for every header. Weakest first: the switch hint, the inbox hint, the awaiting names, the long usage, the open count, the badge, the short usage, the awaiting line, the blocking count, and the stale warning last. A new header piece is one more step in that list. See `## T6.4` in `docs/decisions.md`.

### M7 — The dashboard is the command center

**Outcome:** the owner runs afk from `afk dash` without going back to the shell for day-to-day work:
one leader key opens a dialog of every action, and from it they start any planning session (a new plan
in a new folder included), approve a plan, see every project on one screen, add and remove projects,
set a task's status, see a task's tokens and refresh the usage reading.

#### T7.1 — Leader key and actions dialog
- **Depends on:** T6.3, T6.4
- **Goal:** pressing ctrl+space anywhere in `afk dash` outside an overlay, form or confirm, the agent pane included, opens a dialog that
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
- Amended 2026-10-13 (T7.1 done): later tasks may assume the following. (1) The leader is `keys.Leader` (ctrl+space) in `defaultKeyMap`, a rule `{keys.Leader, actionOpenLeader}` in `inputNormal`, `inputDetail` and `inputPane` only, and a new `inputLeader` mode above `inputPalette`; `placeholderKey` lets it through on the agent placeholder. (2) The dialog's rows come from `leaderRows` (`internal/ui/leader.go`), which merges the `inputNormal` and `inputDetail` rules by action and leaves out movement; every listed action needs a line in `leaderHelp` and a reason in `leaderBlockedReason`, or a test fails. T7.2 moves the action letters off the list, so it must keep each action reachable as a rule the dialog reads (or give `leaderRows` another source), and it should relabel the rows whose key the dialog keeps for itself (`esc`, `enter`). (3) A picked row runs through `applyKeyAction` in the mode it came from. (4) ctrl+@ and ctrl+2 are the same byte and also open the dialog; an inbox question asks the owner about it. See `## T7.1` in `docs/decisions.md`.

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
- Amended 2026-10-13 (T7.2 done): later tasks may assume the following. (1) Dialog rows come from `leaderTable` in `internal/ui/leader.go` (the key after the leader, the action, and where it reaches), not from the list's rules. A new action is one table row plus a line in `leaderNames`, `leaderHelp` and `leaderBlockedReason`, and needs no key rule in `mode.go`. (2) `leaderChord(action)` names a key as `ctrl+space X` and `leaderHelpBinding` makes a help entry from it. Every hint and notice goes through them. (3) The `:` palette, `inputPalette`, `actionPalette` and `actionPaneUnfocus` are gone. The agent pane's only rule is the leader, so every other key reaches the agent. (4) The session list's rules are movement, `enter`, `esc` (close help, then clear filter), `ctrl+c` and the leader. A removed letter shows the notice from `removedKeyNotice`. T7.3–T7.7 add their actions as leader rows. See `## T7.2` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.3 done): later tasks may assume the following. (1) The terminal-free start is `startPlanForSurface(planSurfaceRequest{Mode, Project, Text}, planDeps) planSurfaceResult` in `plan_start_surface.go` (outcomes `started`, `resumed`, `refused`; Mode `""` is a resume-only request that opens an open session and never starts a new plan); `afk plan` shares its look, decision and refusal text (`lookAtPlanProject`, `planRefusal`), so T7.4 extends it with the `new` mode rather than writing a second path, and T8.14 can call it. (2) The dashboard seam is `ui.Deps.StartPlan(root, mode, text) PlanStartResult` (`internal/ui/planstart.go`), wired by `dashStartPlan` in `dash_plan.go`, which waits for `host.json` before returning so the pane opens on the first try. (3) By-name dialog rows have `key: ""` in `leaderTable`; a name starting with a leader key letter is reached through that key first, which is why reconcile is typed as `document`. (4) A one-line form mode `inputPlanText` and the open-it confirm `inputConfirmPlanOpen` exist. See `## T7.3` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.4 done): later tasks may assume the following. (1) `startPlanForSurface` takes Mode `planSurfaceNew` ("new"): `Project` is the typed folder (`~` and `~/...` expanded with the home folder, other relative paths refused), it is looked at as a bare `afk plan` (so a non-git folder is initialised and an open session is resumed), and a folder with a plan and nothing open is refused with a sentence pointing at the dialog's extend, amend and document actions; T7.5 decides name-versus-path before calling it and T8.15 can call it as is. (2) The dialog row is "start a new plan", by name only (`actionPlanNew`, `ui.PlanModeNew`); the master plan's "plan a new project" was not used because `p` opens the switcher. (3) The UI calls `StartPlan(folder, "new", "")`; on a started or resumed result for another folder it adds the root to `Model.startedProjects` (a separate source in `mergeProjects`, never `Pinned`, never written to `config.toml`), switches, and only then opens the pane. (4) The dashboard's adapter resolves the new mode's root through symlinks. See `## T7.4` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.5 done): later tasks may assume the following. (1) The decision and the create step are `internal/planfolder`: `Decide(text, LocationFrom(profile, found, loadErr, home), os.Stat)` gives `KindUse` (text with `/` or `~`, passed to T7.4's start unchanged), `KindCreate` (`<project location>/<name>`) or `KindRefuse` (with `Exists` for a folder already there), and `Create(path, mkdir, gitInit)` makes the folder and its repository and removes nothing on failure; T8.15a can call both as is. (2) The dashboard reaches them through `ui.Deps.PlanFolder` and `ui.Deps.CreatePlanFolder` (wired by `dashPlanFolder` and `dashCreatePlanFolder` in `dash.go`); a name's yes runs the create (`planFolderCreatedMsg`) and only then `planStartCmd(path, "new", "")`, so T7.6 puts its GitHub question in that `planFolderCreatedMsg` step, for the create case only (the exists offer never creates). (3) The confirm is the new mode `inputConfirmPlanFolder`; `TestActiveInputModeModalAlwaysWins` counts modal modes. See `## T7.5` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.6 done): later tasks may assume the following. (1) The offer, its wording and the create step are `internal/ghrepo`: `Offer(lookErr, authErr, login, loginErr, folder)` (ask, or why not with the notice), `OutcomeText`, and `Create(gh, git, dir, repo)` behind its own `GH`/`Git` interfaces (`NewGH`/`NewGit` over `ship.RunFunc`), which runs `gh repo create <account>/<folder> --private` then `git remote add origin` itself, never pushing; `ship.GH` is unchanged. T8.15 can call it as is. (2) The dashboard seams are `ui.Deps.GitHubOffer` and `ui.Deps.CreateGitHubRepo` (wired by `dashGitHubOffer`/`dashCreateGitHubRepo` in `dash_plan.go`); both nil keeps T7.5's behaviour. The question is asked only from `applyPlanFolderCreated` (modal `inputConfirmGitHubRepo`, nine modal modes now). (3) A notice that must outlast a pane opening goes through `planStartedMsg.lead` and `Model.pendingPaneLead`, which the pane-arrival notice puts first, once. (4) A repository with no commits now lists as no branches rather than a failed branch listing. See `## T7.6` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.7 done): later tasks may assume the following. (1) The seam is `ui.Deps.LoadPlanStatus(repo) ui.PlanStatus` (`internal/ui/planstatus.go`), wired by `dashLoadPlanStatus` in `dash_planstatus.go`; it reads the main checkout's files only and carries `Session`, `Damaged`, `Mode`, `Phase`, `Waiting`, `Problems`, `OpenQuestions`, `PlanExists`, `PlanPath`, `Summary` (the checker's counts) and `Findings`, so T7.8 enables approve when `Session && Mode == "new" && Phase == "review"` and shows `Summary` in its confirm. (2) The model keeps it in `Model.planStatus` (`Known` once read), refreshed by `planStatusCmd` on every refresh and dropped on a project switch; read it there, never call the seam from a view. (3) The dialog row "check the plan" (`actionPlanCheck`, non-modal overlay `inputPlanCheck`, still nine modal modes) is reached by typing `errors`: a by-name row's typed word must start with a letter no row uses as its key (`a d e h j k m s u v y z`), because the first letter typed runs that key's row. See `## T7.7` in `docs/decisions.md`.

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
- Amended 2026-10-13: the Must not "approve from the web page" holds for this task only; T8.11 adds approving to the web page behind a confirm, calling this task's entry point.
- Amended 2026-10-14 (T7.8 done): later tasks may assume the following. (1) The entry point is `approvePlanForSurface(projectFlag string, deps approveDeps) approveResult` in `plan_approve_surface.go` (outcomes approved, already approved, refused, no project, failed, with `Reason`, `Code`, `Failed`/`Done`/`Left`, `Counts`, `DaemonRunning`, `Push` and the command's `Lines`); `afk plan approve` replays its `Lines`, so T8.11 calls it as is and words it with `approveResultText` in `dash_approve.go` or its own function. (2) The dashboard seam is `ui.Deps.ApprovePlan(root) ui.PlanApproveResult{Outcome, Text}`, wired by `dashApprovePlan`; the dialog row is "approve the plan" (`actionPlanApprove`), reached by typing `approve`, and the confirm is the modal `inputConfirmPlanApprove` (ten modal modes now). (3) A task adding a leader row also edits `internal/ui/leader_wiring_test.go` and the golden `dash-120x40-leader.txt`. (4) A long notice is cut by the toast, so the approved result's last sentences can be hidden (a scope idea). See `## T7.8` in `docs/decisions.md`.

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

- Amended 2026-10-14 (T7.9 done): later tasks may assume the following. (1) The pure rows are `internal/projectrows`: `Build(Input) Row` and the stable `Sort` (needs-owner first, then `Order`, config.toml order with the unlisted start-up project last); it imports neither `internal/web` nor `internal/ui`, so T8.5 can call it as is, filling `Input` the way `loadDashAllProjects` in `dash_allprojects.go` does (`web.Summarize`, `buildview.OpenCounts`, `daemonstate`, `ReadReviews`, and `discover.Discover` only for a project with a running task). `KindText` and `StoppedText` live there now and `afk daemon status` delegates to them, output unchanged; the waiting kinds include `owner_task`, the daemon's real string. (2) The dashboard seam is `ui.Deps.LoadAllProjects func() ui.AllProjects{Rows, ConfigPath, Err}`, wired by `dashLoadAllProjects(launchMainCheckout)`, read only while the screen is open (on opening and on each tick), results dropped once closed or stale (`allProjectsGen`). (3) The screen is the non-modal mode `inputAllProjects`, opened by the leader row `A` "all projects" (`actionAllProjects`); over it the dialog runs only pause, resume, all projects, mouse and quit, and `pauseBuild`/`resumeBuild` branch to the selected row's root. T7.10's add and remove belong on this screen as leader rows allowed over it, with the rows re-read after each change. (4) `enter` switches only to a root already in the switcher's list; the empty-list line appears when nothing is listed in config.toml. See `## T7.9` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.10 done): later tasks may assume the following. (1) The entry points are `addProjectForSurface(env, dir, name)` and `removeProjectForSurface(env, key)` in `project_surface.go`, returning `projectEditResult{Outcome, Name, Root, Text, Code, ToStderr}` (added, already listed, removed, not listed, refused, failed) with the command's words and no `afk project …:` prefix; `afk project add` and `remove` print through `printProjectResult`, output unchanged, and `addProjectToConfig` wraps config-file failures in `configIOError` so a caller can tell failed from refused (`afk plan approve` unchanged). T8.16 calls them as is. (2) The dashboard seams are `ui.Deps.AddProject(dir, name)` and `ui.Deps.RemoveProject(root) ui.ProjectEditResult`, wired by `dashAddProject`/`dashRemoveProject` in `dash_project.go`; remove finds the `[[project]]` matching the row's root (symlinks resolved) and removes it by its listed name. (3) The leader rows are `+` "add a project" (two modal steps, `inputProjectAddFolder` and `inputProjectAddName`) and `-` "remove the project" (modal `inputConfirmProjectRemove`), runnable only over the all-projects screen; thirteen modal modes now. `projectrows.Row` carries `Listed`. See `## T7.10` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.11 done): later tasks may assume the following. (1) The Build tab's cursor is `m.buildCursorID`/`m.buildCursorIndex`, shown only when `buildCursorShown()` (right side focused, detail drawn, Build tab, rows present); pure helpers `moveBuildCursor` and `landBuildCursor` are in `internal/ui/buildcursor.go`, and `buildTabLinesAt(..., cursor)` draws a two-column `›` gutter (`buildTabLines` is unchanged). T7.12's "selected task" is `m.buildCursorID`, and its row total must fit inside that narrower row. (2) The entry point is `setLedgerStatusForSurface(root, ids, status, replacedBy, replacedGiven)` in `ledger_surface.go`, returning `ledgerSetResult` (changed, already, refused, failed, each id's real old and new status, the command's words without prefix); `afk ledger set` prints through `printLedgerSetResult`, output unchanged. T8 can call it as is. (3) The seam is `ui.Deps.SetTaskStatus(root, id, status, replacedBy) ui.TaskStatusResult`, wired by `dashSetTaskStatus` in `dash_ledger.go`. (4) The leader row is `S` "set this task's status" (a right-side row), with three new modal modes (`inputPickTaskStatus`, `inputTaskStatusReplacedBy`, `inputConfirmTaskStatus`), sixteen modal modes now. See `## T7.11` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.12 done): later tasks may assume the following. (1) The entry point is `taskTokensForSurface(root, task)` in `tokens_surface.go`, returning `taskTokensResult` (state recorded, none or unreadable; `NoLedger`; `Lines`, exactly what `afk tokens <task>` prints; `Compact`, a narrower table from `tokens.RenderCompact`; the weighted total and `Short`, e.g. `2.9M weighted`, from `tokens.Short`); it reads only `.afk/log/tokens.jsonl` and never records. `afk tokens <task>` prints through it, output unchanged. T8's page can call it as is. (2) The seam is `ui.Deps.LoadTaskTokens(root, task) ui.TaskTokens`, wired by `dashLoadTaskTokens` in `dash_tokens.go`, read on each refresh and when the Build cursor lands on a new task; the dashboard holds one record, the cursor task's. (3) The leader row is `T` "show this task's tokens" (right side, after `S`), opening a read-only overlay (a non-footer input mode, like the plan check's); the modal count stays sixteen. `buildRowLines` became `buildRowLayout`, and `buildTabLinesAt` wraps `buildTabLinesWith`. See `## T7.12` in `docs/decisions.md`.

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
- Amended 2026-10-14 (T7.13 done): later tasks may assume the following. (1) The entry point is `refreshUsageForSurface(p, gov, d)` in `usage_surface.go`, returning `usageRefreshResult` (outcome refreshed, unsupported, failed or save failed; the snapshot and the file written; the raw error; `Text`, a plain sentence with no prefix and no provider name); it writes `usage.json` only, never `governor.json`, and makes one probe with no login check. `afk usage` probes and saves through it, output unchanged; `realUsageDeps()` in `usage_cmd.go` builds the real deps (probe run in afk's state folder). T8.8's page can call it as is, guarding one reading at a time itself. (2) The seam is `ui.Deps.RefreshUsage() ui.UsageRefreshResult`, wired by `dashRefreshUsage` in `dash_usage.go` with the default provider, as `afk usage --refresh` uses. (3) The leader row is `R` "refresh usage" (list and overview, after `U`), no new modal mode; the result is never dropped on a project switch, and the header shows the reading by re-reading `usage.json` through `LoadBuild`. See `## T7.13` in `docs/decisions.md`.

### M8 — The web page does everything the dashboard does, except attach

**Outcome:** from a phone, the owner runs afk on the web page as they do in `afk dash`: every project
on one screen, inbox, pause and resume, tasks and their status and tokens, usage, sessions and their
actions, the plan checker, approving a plan, adding and removing projects, and every planning session,
held as a chat. The page is redesigned from a design system the owner made in Claude Design. The one
thing it does not do is attach to a session.

#### T8.1 — Design system (owner)
- **Depends on:** —
- **Goal:** the web page has a design system, made by the owner in their design tool, that every M8
  task with an interface builds from.
- **Scope:** Take `docs/design-brief.md` to Claude Design (From the owner profile, a default), make the
  design system there, and save the result at `docs/design-system.md`. Then run
  `afk plan reconcile docs/design-system.md` and see that session through, so the M8 tasks name the
  components and screens it defines.
- **Acceptance:** `docs/design-system.md` is committed and holds the tokens, the components and the
  screens the brief lists.

#### T8.2 — Live updates and the page's own JavaScript
- **Depends on:** —
- **Goal:** an open page updates by itself within a few seconds when a project's files change, using
  hand-written JavaScript built into the binary, with no build step; today's pages are the first to
  use it.
- **Background:** the page has no JavaScript today (`docs/decisions.md`, `## T3.1`, A6). The owner
  chose "Hand-written JavaScript built into the binary, no build step" for the redesign, which needs
  live updates for the chat view (T8.12) and the screens after T8.5. The dashboard re-reads files on a
  2-second tick; the page must not read any faster or any differently (§3: files only, never block).
- **Scope:** static files (`.js`, and room for `.css`) embedded in the binary and served under one
  path with long-lived caching keyed by a content hash, and a `Content-Security-Policy` that allows
  scripts only from the page's own origin. A server-sent events stream (standard library only), behind
  the same token check as every page, that sends a small event naming the project and the part that
  changed (inbox, ledger, pause state, sessions, planning session, usage) when the files behind it change,
  found by a pure comparison of what was read on one tick with what was read on the last. The
  JavaScript re-fetches the changed part of the page and swaps it in, and reconnects after a dropped
  stream; with JavaScript off or the stream refused by a proxy, the page still works by reloading.
  One reader serves every open stream, so ten open tabs read the files once a tick. Record the event
  names and the stream's address in `docs/decisions.md`. README: the web page section.
- **Must not:** add a JavaScript build step, `npm`, or a Go dependency for the stream; read files
  more often than every 2 seconds; let a slow or stuck browser hold up the reader (drop it, as the
  host drops a slow client).
- **Acceptance:** a table test for the pure change detection, one row per part, plus no change and an
  unreadable file. Handler tests: the stream refuses a request with no token, sends an event after a
  file changes and nothing when none does, and a full browser queue drops that browser and no other.
  A test that the static files are served with the policy and cache headers. Against a scratch build
  with a scratch config and a throwaway repository: an inbox item added with `afk inbox add` appears
  on an open project page in a browser within 5 seconds with no reload. `make check` is green.
- Amended 2026-10-14 (T8.2 done): later tasks may assume the following. (1) The stream is `GET /events` (token-guarded, no `/api` twin): SSE events of type `change` with data `{"project","part"}`, parts `inbox`, `ledger`, `pause` (pause flag, stop marker, `daemon.json`), `sessions`, `planning` (`.afk/plan/state.json`, `questions.jsonl`, the `master-plan` session's `host.json`/`status.json`; T8.3 or T8.12 adds the transcript in `readProject` in `internal/web/live.go`) and `usage` (project ""); one hub reader on `Deps.Ticker` (2 s), 16-event queues, 32 streams. (2) Static files live in `internal/web/static/`, served at `/static/<hash>/<file>` through the template func `asset`; T8.5 adds `afk.css` there and the CSP already allows `style-src 'self'`. (3) A page region is live when it has an `id`, `data-live` (space-separated parts) and `data-project`; `live.js` swaps it by id, adds `is-changed`, skips a region with focus or a form, and refetches every region on each stream open; the bar's `<span class="live" data-live-status>` shows Live or `live--offline`. See `## T8.2` in `docs/decisions.md`.

#### T8.3 — Read a planning session's conversation
- **Depends on:** —
- **Goal:** `GET /api/p/{project}/plan/chat` returns the project's open planning session as a list of
  messages (the agent's and the owner's, in order), and, when the session is waiting for the owner,
  whether it waits for a typed reply or for a choice among offered options, with those options.
- **Background:** the owner chose "Chat view only: planning sessions as messages with a reply box; no
  terminal on the page". The planning session is the `master-plan` session (`internal/session`,
  `PlanSessionName`); it has a `host.json` and a `planning` marker but no `status.json`, so it records
  no conversation id, which is also why `afk tokens` cannot count it (T5.8, inbox `9pxewe`). The agent
  CLI keeps the conversation in its own transcript files, which `internal/provider/tokens.go` already
  finds and reads for task sessions.
- **Scope:** a provider method behind `internal/provider` that, for a project's main checkout and the
  planning host's start time, finds the planning session's transcript and returns it as provider-neutral
  messages: who, the text, the time, and for a question tool call its question and options. Decide how
  the transcript is found when no conversation id is recorded, and record why in `docs/decisions.md`;
  if recording the id is the sound way, it is written beside the planning marker by the code that
  starts the session, and §4 gains it in the same change. A pure function turns the messages into the
  chat: tool calls other than questions and the session's own housekeeping are left out or shown as
  one short line each, and "waiting" is decided from the last entries. Providers with no planning
  session return "no planning session" as `PlanArgv` does. The address returns the empty state when no
  planning session is open and a plain reason when the transcript cannot be read. README: the JSON API
  list.
- **Must not:** name a provider outside `internal/provider`; read a transcript on the dashboard's or
  the page's refresh tick unless the planning host is alive; write any file under `.afk/plan/`.
- **Acceptance:** table tests over saved transcript fixtures (written by hand, no real agent): a plain
  exchange, a pending question with options, an answered question, a session waiting for typed text,
  a session still working, a truncated last line, and no transcript. A handler test for each state.
  Against a scratch build with a stand-in planning host and a fixture transcript in a scratch home:
  the address returns the messages and the pending question. `make check` is green.
- Amended 2026-10-14 (T8.3 done): later tasks may assume the following. (1) `GET /api/p/{project}/plan/chat` (only under `/api`; T8.12 adds the page twin) returns `{"project","state","messages","questions","problem"}`, `state` one of `none`, `working`, `waiting_for_text`, `waiting_for_choice`, `unknown`; a message is `{"who":"agent"|"owner","text","at","questions"}`, a question `{"question","header","multi_select","options":[{"label","description","recommended"}]}`, and one question call may carry several questions. (2) The decision is `internal/planchat` (`Read(project, root, Deps)` and the pure `Build`), which T8.4's refusal and T8.13's notify decision should reuse; the transcript comes from `Provider.PlanConversation` (codex and opencode return `ErrNoPlanSession`), found by the host's `started_at`, the checkout and the planning command, with no conversation id recorded and §4 unchanged. (3) The transcript is read only while the planning host's pid answers signal 0, and is not yet part of the `planning` live part in `internal/web/live.go`: T8.12 adds it. (4) Only an answer (a tool result) clears a pending question, so text typed while a question is open still reads as `waiting_for_choice`; T8.4 should confirm against the real CLI. See `## T8.3` in `docs/decisions.md`.

#### T8.4 — Send a reply to a planning session
- **Depends on:** T8.3
- **Goal:** `POST /api/p/{project}/plan/chat` sends the owner's reply into the open planning session:
  typed text, or the choice of one offered option, and the next read of T8.3 shows it.
- **Background:** a session host serves one client at a time (`internal/host`): a new client takes
  the session and the old one is told `Stolen`. The owner chose "Send it anyway": the reply goes in,
  the dashboard pane is dropped with a notice, and the owner reopens it with `enter`. How the agent
  CLI's question dialog takes a choice by keystrokes is not documented.
- **Scope:** an entry point that dials the planning host with `host.Dial`, sends the reply as `Data`
  frames, then `Detach`, never `Exit` or `Restart`. A choice is turned into keystrokes by a provider
  method behind `internal/provider`; find out how the dialog takes a choice by trying it against the
  real CLI in a scratch session during QA, save what was found as evidence, and record it in
  `docs/decisions.md`. If no reliable way is found, stop and raise an inbox item; do not guess. Typed
  text is sent as a bracketed paste followed by enter, so a multi-line reply is one message. A reply
  is refused, with nothing sent, when no planning session is open, when the session is not waiting
  for the owner (T8.3), or when the chosen option is not one of those offered. The dashboard's notice
  for a pane taken over by the page says so in plain words and how to reopen it. The POST is guarded
  as every POST is (token, `Origin`, 64 KB). README: the JSON API list and the dashboard's notices.
- **Must not:** send `Exit` or `Restart`; keep the connection open after the reply; name a provider
  outside `internal/provider`; let a slow host stall the request past a short timeout.
- **Acceptance:** table tests for the refusal decision, every branch a row, and for the keystrokes of
  a choice and of typed text. A test with a fake host listener that a reply sends the expected frames
  and a `Detach`, and nothing else. A UI model test for the dashboard's taken-over notice. Against a
  scratch build with a stand-in agent in a planning host: a typed reply reaches the stand-in's input;
  then, once, against the real agent CLI in a throwaway repository (live check, logged): a choice
  posted to the address answers a question dialog. `make check` is green.
- Amended 2026-10-14 (T8.4 done): later tasks may assume the following. (1) `POST /api/p/{project}/plan/chat` (only under `/api`) takes JSON `{"text":"..."}` or `{"choices":[["label"],...]}` (one list per pending question, in order) and answers 200 `{"project","sent":"text"|"choice"}`; refusals send nothing and answer 409 (`no_session`, `not_waiting`, `wrong_kind`, `unsupported`) or 400 (`bad_reply`, `not_offered`) with `{"error"}` only (no `reason` code in the body yet: T8.12 may want one), 502 when the host cannot be reached. (2) The decision is `planchat.Check` and the text keys `planchat.TextKeys` (bracketed paste, then Enter as its own write); choice keys come from `Provider.PlanChoiceKeys` (claude: the option's digit, Tab after multi-select ticks, `1` on a review screen; more than 4 options is `ErrChoiceUnsupported`); the send is `host.SendKeys` (Data chunks, then Detach, 2s dial wait). (3) The open question is only in the transcript because the plugin (5.7.4) registers a no-op `PreToolUse` hook for `AskUserQuestion`; claude 2.1.299 writes the question before the dialog opens only when such a hook exists (undocumented). A session run with an older plugin reads `working` while a dialog is open, so T8.12 and T8.13 depend on the updated plugin being installed; if a later CLI stops flushing, the fallback is a hook-written sidecar (schema first). (4) The dashboard's taken-over notice is one shared sentence that names "another window or the web page", because `Stolen` does not say who took the session. See `## T8.4` in `docs/decisions.md`.

#### T8.5 — New layout and the all-projects overview
- **Depends on:** T8.1, T8.2, T7.9
- **Goal:** every page wears the design system, and the home page lists every project the daemon
  builds, the ones needing the owner first.
- **Background:** the home page today is `/` (`internal/web`, templates `layout.html` and
  `overview.html`). T7.9 builds the dashboard's all-projects rows (current task, build state in the
  words `afk daemon status` uses, inbox counts, plan waiting for approval) as a pure row builder; the
  page uses the same rows so the two surfaces agree, as `web.Summarize` already makes them agree. The
  page shows the daemon's last good project list and never reads `config.toml` itself (T5.7).
- **Scope:** the design system's tokens and layout in `layout.html` and the static CSS, with the
  navigation the design system defines; every existing page keeps working inside it. The home page
  becomes the all-projects screen built from T7.9's rows, with the design system's components, sorted
  as T7.9 sorts, updating live through T8.2. Phone first: it is designed at phone width and works
  well on a desktop. Every screen meets WCAG 2.2 AA (contrast, keyboard use, focus, labels, target
  size). Component names follow `docs/design-system.md`.
  From the design system (reconciled 2026-10-13): serve its reference stylesheet (§2, `afk.css`) as it
  stands through T8.2's static path, dropping only the canvas frame `.afk.screen` and the canvas-only
  `.is-hover`, `.is-focus` and `.is-active` rules; `class="afk"` on `<body>`, dark following the
  device. Phone screens have the `app-bar` (the `wordmark` on the overview, otherwise back, title and
  `live`); the desktop has the `topnav` (Projects with its `count`, Usage, `live`) over a `shell` and
  `split`, with the selected project on the right of the overview. The `live` indicator shows Live,
  Working or Offline ("Can't reach afk, retrying") from T8.2's stream; a lost stream is never an error
  page, and the last data stays on screen. A region T8.2 swaps in gets `is-changed` once, and the
  inbox list and the project state card are `aria-live="polite"`. The shared `confirm-sheet` (a native
  `<dialog>`, focus on the safe button, Escape closes it, the action button loading until the server
  answers) and `toast` are built here for the later tasks; with JavaScript off a confirm is its own
  page with the same title, sentence and buttons. The overview follows the design's Overview screen:
  an `h1` naming how many projects need the owner ("Nothing needs you" when none), a **Needs you**
  section of `row--needs-you` rows, then **Everything else**, each row with its state `badge`, its
  current task and phase or why nothing runs (in T7.9's words), `progress` and its inbox counts. The
  error page and the page that refuses a request with no token take the design's look; the refused
  page stays fixed text that echoes nothing and says how to open the page with the token, with no Sign
  in button, because afk has no sign-in.
  README: the web page section.
- **Must not:** add a JavaScript build step; drop an address the page serves today; change the JSON
  API's existing fields.
- **Acceptance:** handler tests for the home page with each kind of row (T7.9's list) and the empty
  state. A test that every page renders inside the new layout. Against a scratch build with a scratch
  config listing two throwaway repositories, one paused with an open inbox item, opened in a browser
  at 390 px and at 1280 px wide: the paused one is first with its count, and an automated WCAG 2.2 AA
  check of the home page (run in QA, not in `make check`) reports no violation. `make check` is green.
- Amended 2026-10-14 (T8.5 done): later tasks may assume the following. (1) Every page links `afk.css` (the design system's sheet as shipped, canvas-only rules dropped) and `page.css` (afk's own rules and work-arounds, e.g. `.afk a.<component>` colours, `[hidden]`, the 760 px split) from `internal/web/static/`; no template has a `<style>` or `style=`, and the CSP string is unchanged (`'unsafe-inline'` still allowed). Only those two sheets are served without the token, for the refused page. (2) `renderPage` executes the layout with `pageData{View, Chrome}`; page templates still get their view as `.`, and the pure `chromeFor` decides the phone `app-bar` / desktop `topnav`; a page can add markup after its content through `{{block "foot" .Chrome}}`. (3) Shared parts live in `templates/parts.html`: the `confirm-sheet` `<dialog>` (opened by `static/confirm.js` from a link with `data-confirm`, whose href is an HTML-only `GET /p/{project}/confirm/{action}` page, today `pause` and `resume`) and the toast, shown only for a whitelisted `?done=` code (`toastTexts` in `pages.go`); draw the sheet only on pages that use it (the plan page must stay free of forms and buttons). (4) The home page is built from `web.Deps.ProjectRows` (filled by `webProjectRows` in `daemon_web.go` from the daemon's last good list through `buildListedRows`, shared with the dashboard); `GET /api/` gains `rows`, the old fields unchanged; the pure layout is `overviewPageFor`, badges `stateBadge`. (5) `live.js` shows Live, Working (any `data-working="true"` on the page) and Offline. A row changes in place but does not move between Needs you and Everything else until a reload, and nothing fires for `plan-reviews.json`. See `## T8.5` in `docs/decisions.md`.

#### T8.6 — Inbox, pause and resume in the new design
- **Depends on:** T8.5
- **Goal:** the project page, the inbox list, an item and its answer form, and the pause and resume
  buttons are rebuilt from the design system, with the same behaviour as today, except that pause and resume now ask first (Scope).
- **Background:** these exist (T3.1): `project.html`, `item.html`, the POSTs to `…/answer`, `pause` and
  `resume`, `inbox.Store.Answer(…, inbox.ViaWeb)`, and `pauseProject`/`resumeProject` in
  `pause_cmd.go`.
- **Scope:** the templates rebuilt with the design system's components; the inbox count and the
  build's state update live through T8.2; answering and pausing work without a full reload when
  JavaScript is on and with one when it is off. An item's options are buttons and its free text a
  field, as today; typed text still wins over a selected option (`docs/decisions.md`, `## T3.1`).
  From the design system (reconciled 2026-10-13): the Project screen is a state `card` (state `badge`,
  "Updated 12 s ago", an `h2` headline in words, one line of detail, `progress`, and the one next
  action as the primary button: **Answer in the inbox**, **Resume**, **Read and approve the plan**, or
  none), **Running now** rows, **Pause project** or **Resume project**, a Plan row, a `nav-list` to
  the project's screens (each item shown once the task that builds its screen has landed) and a `kv`
  of Folder and Repository. The inbox is its own screen: a `notice--attention` saying how many items
  hold a task up, rows with blocking ones first (`badge--blocking`, `tid`, the question, source and
  age), and the `empty` "Nothing needs you". An item shows its question as an `h2`, its context as
  `prose`, its options as `option` buttons (the recommended one first and marked) and a `textarea` "Or
  write your own"; **Send answer** is disabled until something is picked or written; a `toast` says
  what happened and the page goes back to the list. An owner task's item shows its instruction and a
  single **Mark as done**, which records the same answer today's form records for it. Pause and resume
  each ask first in a `confirm-sheet`, as the owner chose on 2026-10-13 and as the §2 row "Web page as
  command center" says of every owner-only action: pausing says the running task finishes and nothing
  new starts, resuming says what starts again. A pause or resume POST without the confirm changes
  nothing, and today's tests of those POSTs gain the confirm. Toasts and confirm sentences say only
  what really happened ("ready again" only when no other item holds the task).
  README: the web page section where it describes these screens.
- **Must not:** change what an answer, a pause or a resume writes; write the ledger.
- **Acceptance:** handler tests for each screen and each POST as today's tests, kept green, and a pause or resume POST without the confirm changing nothing. Against a
  scratch build in a throwaway repository with an open blocking item, in a browser at phone width:
  answering it marks it answered from the web, pause creates `.afk/build/paused` and resume removes
  it, and the page shows each change without a manual reload. An automated WCAG 2.2 AA check of the
  three screens reports no violation. `make check` is green.
- Amended 2026-10-14 (T8.6 done): later tasks may assume the following. (1) Every owner action POST can follow the pause/resume rule: the server needs `confirm=yes` in the form body (or `{"confirm": true}` in JSON) and answers 400 changing nothing without it, on the page and under `/api`; the address `?confirm=` does not count. Both confirm sheets are drawn outside every live region. (2) `GET /p/{project}/inbox` is now its own page (template `inbox.html`, JSON twin unchanged); an answer redirects there with a whitelisted `?done=answered|ready|done&item=<id>`, `toastFor` takes the task as a third argument, and `readyAgain` and `isOwnerTask` live in `internal/web`. (3) The project page is built by the pure `projectPageFor` into the page-only `ProjectView.Page` (`json:"-"`, filled only on the page GET); its live regions are `project-badge`, `project-state`, `project-running`, `project-pause`, `project-plan`, `project-nav`, and its `nav-list` lists only Inbox, Plan and Usage, so T8.7, T8.9 and T8.12 add their own links; Running now rows are not links until T8.9; `Deps.Repository(root)` reads `origin` with credentials dropped. (4) `static/submit.js` posts any form with `data-enhance` by `fetch`, swaps the whole `.shell` and fires `afk:swapped` (live.js and confirm.js listen); a new script must also be added to `ownScript` in `pages_test.go`. The progress partial is `progress` in `parts.html`. See `## T8.6` in `docs/decisions.md`.

#### T8.7 — Build tasks: set a task's status, see its tokens
- **Depends on:** T8.5, T7.11, T7.12
- **Goal:** a project's page lists its master-plan tasks with their status, and the owner can set a
  task's status behind a confirm and see a task's tokens, with the same effect and words as
  `afk ledger set` and `afk tokens <task>`.
- **Background:** T7.11 adds an entry point beside `afk ledger set` that returns its outcome without
  printing, and T7.12 a seam that returns a task's token record; the page calls the same code. The
  owner chose all owner-only actions on the page, each behind a confirm.
- **Scope:** a task list on the project's Tasks screen, linked from the project page's `nav-list` (id, title, status, unmet dependencies, as `afk tasks`
  prints them), updating live. For a task: its tokens (totals and the split by phase and role, or "no
  tokens recorded for this task yet") and a "set status" action that lists the statuses the command
  accepts, asks for the replacing ids for `superseded`, and confirms in one sentence what will change,
  saying so when an agent is working on the task. The same under `/api`.
  From the design system (reconciled 2026-10-13): the Tasks screen has `progress` for the whole plan,
  a `segmented` Open / Done / All filter, and one `section` per milestone (its title and "2 of 7
  done") of task rows (`tid`, title, status `badge`, "Waits for T3.4" for unmet dependencies). A
  task's screen shows its status `badge`, an `h2` title, one line on why it is in that state, and the
  Tokens `card`: the total, a `split-bar` and `legend` by phase using the token ledger's own phases
  (§4.7: plan, execute, review, fix, ship), and a `table` by role. **Set status…** opens a
  `confirm-sheet` with a `choice-list` of every status `afk ledger set` accepts (none disabled), a
  field for the replacing ids when `superseded` is picked, and one sentence that changes with the
  choice; the action button reads the choice ("Mark done").
  README: the web page section.
- **Must not:** write the ledger any other way; record tokens; add `afk ledger init` to the page.
- **Acceptance:** handler tests: the list, a task's tokens with a record and without, the status
  confirm, each outcome of the set (including `superseded` with and without ids and an unknown id), and
  a POST with no confirm that changes nothing. Against a scratch build in a throwaway repository with a
  five-task plan and a ledger: setting a `failed` task to `pending` from a browser changes
  `.afk/build/ledger.json` and the row. `make check` is green.
- Amended 2026-10-14 (T8.7 done): later tasks may assume the following. (1) The addresses are `GET /p/{project}/tasks[?show=open|done|all]` (open by default, any other value reads as open), `GET /p/{project}/tasks/{id}` and `POST /p/{project}/tasks/{id}/status` (form `status`, `replaced_by`, `confirm=yes`; JSON `{"status","replaced_by":[…],"confirm":true}`), all with `/api` twins, plus the HTML-only no-JavaScript confirm `GET /p/{project}/tasks/{id}/status`; the POST answers the task plus `result{outcome,old,new,text}` (200 changed or already, 400 refused with the command's words or no confirm, 500 failed) and the page POST redirects to the task with `?done=status|already`. (2) New `web.Deps` seams `TaskTokens(root, task) web.TaskTokens` and `SetTaskStatus(root, id, status, replacedBy) web.TaskStatusResult`, filled by `webTaskTokens`/`webSetTaskStatus` in `daemon_web_tasks.go`; the request body is read once by `readPostBody` (handlers.go) and `confirmedRequest` wraps it, so a later owner-action POST with its own fields should use `readPostBody`. (3) Shared pieces: `internal/taskstatus` holds the status words (`Choices`, `ConfirmText`, `Consequence`, `Already`, `ActionLabel`, `SplitIDs`) for the dashboard and the page; `tokens.Summarize` gives total, phase and role figures; `planlint.MilestoneHeading` is the one milestone rule. (4) The task screen's live region is the status (`ledger` part); the tokens card is read on load only. The task screen has no Open its session link yet (T8.9 adds it); the project `nav-list` is now Inbox, Tasks, Plan, Usage. `confirm.js` drives a sheet marked `data-status-sheet` and finds an action button outside its form by `[form=<id>]`. The route count in `TestAuthEveryRouteNeedsTheToken` is 12. See `## T8.7` in `docs/decisions.md`.

#### T8.8 — Usage, with a refresh button
- **Depends on:** T8.5, T7.13
- **Goal:** the usage screen is rebuilt from the design system and has a button that takes a fresh
  reading, with the same effect as `afk usage --refresh`.
- **Background:** T7.13 adds an entry point beside `runUsage` that takes and saves a reading and
  returns it or the reason there is none, one reading at a time.
- **Scope:** `usage.html` rebuilt; a POST that calls T7.13's entry point off the request's critical
  path where it would block, with a "Refreshing" state shown until the reading lands through T8.2;
  a second press while one runs does nothing; a failure is shown in plain words and the old reading
  stays. The same under `/api`.
  From the design system (reconciled 2026-10-13): a governor `card` ("What afk is doing", a `badge`
  naming the decision, Run, Launch nothing new, Pause, Hold or Refuse, and one sentence), a usage
  windows `card` with one `meter` per window (percent used and reset time; `meter--warn` from 80%,
  `meter--full` at 100%), **Refresh usage** (secondary) whose loading label reads "Refreshing" and
  then "Checked just now", and a `<details>` that explains the five decisions.
  README: the web page section and `afk usage`.
- **Must not:** probe on a timer or on page load; name a provider outside `internal/provider`.
- **Acceptance:** handler tests with a fake entry point: a reading, each failure, a second press while
  one runs. No test probes real usage. Against a scratch build with a scratch state folder and a
  stand-in for the agent's CLI on a scratch `PATH`: the button writes `usage.json` and the screen
  shows the stand-in's numbers. `make check` is green.
- Amended 2026-10-14 (T8.8 done): later tasks may assume the following. (1) `POST /usage/refresh` and `/api/usage/refresh` (no confirm: a reading changes no owner state) start one reading in the background through the new `web.Deps.RefreshUsage func() web.UsageRefreshResult` (outcomes `refreshed`, `unsupported`, `failed`), wired by `webRefreshUsage` in `daemon_web_usage.go` over `refreshDefaultAgentUsage`, the step the dashboard's refresh shares; the API answers 202 `{"outcome":"started"}` or 200 `{"outcome":"already"}`, the page 303 to `/usage?done=refreshing|already_refreshing`. (2) The busy flag and last result live in memory in `internal/web/usage.go` (forgotten on a daemon restart); its generation is part of the `usage` live fingerprint, so a start, a landing and a failure each send a `usage` event. `GET /api/usage` gains `refresh` (`state`, `text`, `at`) only when the seam is set; nil seam means no button and 404 POSTs. (3) `live.js` swaps a region marked `data-live-buttons` (a form of buttons only) even with focus on its button, restoring focus by id; meters use `w-N` width classes in `page.css`; the governor badge words are Run, Launch nothing new, Pause, Hold, Refuse. The route count in `TestAuthEveryRouteNeedsTheToken` is 13. See `## T8.8` in `docs/decisions.md`.

#### T8.9 — Sessions: the list and each session's overview
- **Depends on:** T8.5, T8.7
- **Goal:** a project's sessions are listed on the page, each with what the dashboard's overview shows
  (phase, loop count, work-plan progress, the review verdict, whether it waits for the owner, whether
  its host is alive), read-only.
- **Background:** the dashboard finds sessions with `internal/discover` (git worktrees plus
  `status.json` and `host.json`, read-only, never blocking) and draws the overview in `internal/ui`.
  The page cannot attach to a session (the owner chose "Chat view only").
- **Scope:** a sessions screen per project and a session screen, from `discover` and the same readers
  the dashboard's overview uses, updating live; a one-off session and the planning session are listed
  with what they are; a session's screen says that its terminal is opened from `afk dash` or
  `afk attach`, and for the planning session links to the chat view once T8.12 exists. The same under
  `/api`.
  From the design system (reconciled 2026-10-13): session rows with the `tid` and task title, phase
  and progress, the review verdict `badge`, a "Waits for you" `badge`, and the session id in
  `row__aside`; a session's screen with its `tid`, status and verdict badges, an `h2` title, a `kv`
  (phase, loop count, started, label, copy path, whether its host is alive) and, as **Summary** in
  `prose`, what the dashboard's overview shows. Rows the design does not draw (a stale `status.json`,
  a one-off session, the planning session, a dead host) use the same row with a badge and a line
  saying what they are. The task screen of T8.7 gains an **Open its session** link to its session's
  screen.
  README: the web page section.
- **Must not:** dial a host; write any file; block on a stale `host.json`.
- **Acceptance:** handler tests over scratch session folders: running, waiting for the owner, stale
  `status.json`, a one-off session, the planning session, a dead host. Against a scratch build in a
  throwaway repository with a stand-in session: the screen shows its phase, and changing its
  `status.json` changes the screen within 5 seconds. `make check` is green.
- Amended 2026-10-14 (T8.9 done): later tasks may assume the following. (1) The addresses are `GET /p/{project}/sessions` and `GET /p/{project}/sessions/{name}`, each with an `/api` twin (snake_case JSON, every key present: `kind`, `phase`, `waits_for_you`, `loop_count`, `host_running`, `wedged`, `one_off`, `planning`, `plan_total/done/failed`, `verdict`, `verdict_checks`, …); a name shared by a planned and a one-off session opens the first row discovery finds, status-backed first. The route count in `TestAuthEveryRouteNeedsTheToken` is 15 and `routeTarget` maps `{name}` to `t1-2`. (2) `web.SessionView` is the one seam and the `sessions` live part's fingerprint: it now carries the overview's facts (label, branch, session id, loop count, progress, verdict and checks, read errors, wedged, started and updated times, worktree path, `Scratch`, `Planning`, `Problem`), with `Stale` still folding a one-off; it must never hold a value that changes every tick. The root fills it with `webSessionViewsWithStart` (discovery plus `withStartedAt`, a host.json read from the row's worktree) for the page and the all-projects rows. (3) The session screen (`session.html`, live regions `session-badges` and `session-state`) has no forms or buttons, so T8.10 adds its Actions and T8.12 the planning session's chat link; the list is `sessions.html` (`sessions-list`). `taskPageFor` takes the discovered session names as a fifth argument and draws Open its session only for a session still found; the project `nav-list` is Inbox, Tasks, Sessions, Plan, Usage, and Running now rows link to their session. See `## T8.9` in `docs/decisions.md`.

#### T8.10 — Sessions: clean, restart, label, and start a new autonomous session
- **Depends on:** T8.9
- **Goal:** the dashboard's session actions work from the page, each behind a confirm: clean a
  session, restart a stopped or wedged one, set its label, and start a new autonomous session.
- **Background:** the dashboard does these through `internal/session` (clean, create), the host's
  `Restart` frame and its label writer (`WriteLabel` in `internal/ui`). Only autonomous sessions are
  started from the page, because the owner cannot talk to a coding session there; one-off sessions
  are not offered, because they are only a terminal.
- **Scope:** an entry point beside each command (`afk clean`, `afk new --autonomous`) that returns the
  outcome without printing, the command keeping its output by calling it, where none exists yet;
  restart and label through the same code the dashboard uses. Each action asks first, in the
  dashboard's words: clean names the session and what is removed and refuses what `afk clean` refuses;
  restart says what will happen to a running agent; the new-session form takes a name and a
  description, as `afk new --autonomous` does, and refuses what it refuses. The same under `/api`.
  From the design system (reconciled 2026-10-13): the session screen's **Actions** rows (Restart,
  Label, and Clean in danger text), each opening a `confirm-sheet` with the safe button focused, and
  **Start a new autonomous session…** opening a `card` form (Name in `input--mono`, a "What should it
  do?" `textarea`, **Start session**) that is confirmed before it starts. The confirm sentences are
  the dashboard's and say what `afk clean` really removes; the design's sample sentence is not used
  where it differs.
  README: the web page section.
- **Must not:** start an attended or one-off session; send `Exit` to a host; delete a branch the
  command would not; act without the confirm.
- **Acceptance:** table tests for each entry point with fakes for git and the host: each outcome and
  each refusal. Handler tests: each confirm, and a POST without one changing nothing. Against a scratch
  build in a throwaway repository with a stand-in agent: a new autonomous session started from a
  browser appears in the list with a live host; its label set from the page shows in `afk dash`;
  cleaning it removes its worktree. `make check` is green.
- Amended 2026-10-14 (T8.10 done): later tasks may assume the following. (1) The entry points are `cleanSessionForSurface` in `clean_surface.go` (`afk clean` prints through it, output unchanged; `CheckPhase`/`PhaseSeen` refuse a clean whose phase moved since it was shown) and `newAutonomousForSurface`, `restartSessionForSurface` and `setLabelForSurface` in `session_surface.go`, each returning an outcome with no printing. (2) The seams are `web.Deps.CleanSession(root, name, phaseSeen)`, `RestartSession(root, name)`, `SetSessionLabel(root, name, label)` and `NewSession(root, name, description)`, each returning `web.SessionActionResult{outcome done|refused|failed, text}`, wired in `daemon_web_sessions.go`; nil means no button and a 404 POST. The routes are `POST /p/{project}/sessions` and `/sessions/{name}/clean`, `/restart`, `/label`, each with an `/api` twin and the confirm required, so `TestAuthEveryRouteNeedsTheToken` counts 19; the no-JavaScript confirms are `GET /p/{project}/sessions/{name}/confirm/{action}` and `GET /p/{project}/sessions/new` (which shadows a session named `new` on the page, not under `/api`). (3) The page never forces a clean, never deletes a branch, and starts sessions exactly as `afk new --autonomous` (not unattended); restart is offered only for a planned session whose host stopped or went quiet, never a one-off or the planning session. `web.SessionView` gains `Stranded`; toast codes `cleaned`, `restarted`, `labelled`, `started` are shown only on the session screens; `confirm.js` opens a sheet from a form marked `data-confirm-form`. T8.12 adds the planning session's chat link to `session.html` beside the Actions. See `## T8.10` in `docs/decisions.md`.

#### T8.11 — Plan status, the checker and approve
- **Depends on:** T8.5, T7.8
- **Goal:** the project page shows the planning status and the plan checker's counts and findings, and
  the owner can approve a plan at `review` behind a confirm, with the same effect as
  `afk plan approve`.
- **Background:** T7.7 adds a seam that returns the planning status and the checker's summary and
  findings, and T7.8 an entry point beside `afk plan approve` that returns what was done. Approving
  from the page was forbidden (T5.7, T7.8) because others might reach it; the owner has now chosen
  all owner-only actions on the page, each behind a confirm, which lifts that rule.
- **Scope:** the plan line (mode, phase, what it waits for) and a findings screen (counts, one finding
  per line, errors first, worded as `afk plan lint` words them), beside the existing read-only plan
  view (`/p/{project}/plan`). An approve action, shown only for a new plan at `review` and otherwise
  absent with the reason; its confirm shows the project, the plan's path and the counts, and has no
  approve button when the plan has an error. The result in plain words: approved and what the daemon
  does next, the refusal, or the step that failed and what to run to finish. A plan waiting for
  approval on the review list (`plan-reviews.json`) can be approved too. The same under `/api`.
  From the design system (reconciled 2026-10-13): the Plan screen puts the checker `card` first (a
  `badge`, `stats` of tasks, milestones, errors and warnings, and the `finding` list, errors first,
  each linking to its line in the plan below), then **Approve the plan…** or, with errors, a
  `notice--error` in its place ("Fix 2 errors before approving. Ask the agent in the chat."), then the
  plan as `prose`. On the desktop a `split` holds the checker on the left and the plan at 68ch on the
  right. Approve opens a centred `confirm-sheet` naming the project, the plan's path and the counts,
  with **Not yet** focused and **Approve and start**.
  README: the web page section and `afk plan approve`.
- **Must not:** approve without the confirm; approve a plan with errors; change what
  `afk plan approve` prints or does.
- **Acceptance:** handler tests with a fake entry point: the plan line for each mode, the findings
  with none, warnings only and errors, the approve button's absence away from `review` and with
  errors, each outcome, and a POST without the confirm changing nothing. Against a scratch build in a
  throwaway repository with a new plan at `review`, a scratch config folder and a scratch state
  folder: approving from a browser leaves the plan committed, the project in the scratch
  `config.toml` and `state.json` at `approved`. `make check` is green.
- Amended 2026-10-14 (T8.11 done): later tasks may assume the following. (1) The seams are `web.Deps.PlanStatus(root) web.PlanStatus{known, session, damaged, mode, phase, line, approvable, problems}` (filled by `webPlanStatus` in `daemon_web_plan.go` from `loadDashPlanStatus` with no git lookup; `line` is the dashboard's wording through the new `ui.PlanStatus.Line()`) and `web.Deps.ApprovePlan(root) web.PlanApproveResult{outcome approved|already|refused|failed, text}` (filled by `webApprovePlan`, calling `approvePlanForSurface` and worded by `approveResultText`); nil means no line or no button and a 404 POST. (2) The routes are `POST /p/{project}/plan/approve` with an `/api` twin (confirm required; `TestAuthEveryRouteNeedsTheToken` counts 20) and the HTML-only no-JavaScript confirm `GET /p/{project}/plan/approve`; the project is found among configured projects, then the review list. The page POST answers a result page (`plan_result.html`) with the whole text, never a toast, and the handler extends its own write deadline to 3 minutes for the push. (3) `PlanView` (and `/api/p/{project}/plan`) gains `status` and `approve{available, reason}`, decided by the pure `planApproveFor`; the confirm words come from `planApproveSheetFor`; the plan page is the design's checker `card` (badge, four `stats`, `finding` rows errors first, a finding with a task linking to `#<task>`) in a `split` beside the plan as `prose`, and `TestPlanPageCannotApprove` became a test of the approve area. The project page's plan row takes the plan line as its meta. The error notice says to ask the planning agent or edit the plan, so T8.12 may reword it to point at the chat. (4) Known gaps: the plan page has no live region and the `planning` fingerprint does not cover the plan file; after approving a review-list plan, its plan address can answer 404 until the daemon re-reads `config.toml`. See `## T8.11` in `docs/decisions.md`.

#### T8.12 — The planning chat view
- **Depends on:** T8.2, T8.4, T8.5
- **Goal:** a project's open planning session is shown as a chat: the agent's and the owner's
  messages, a reply box, and a button per offered option when the agent asks a question with options.
- **Background:** T8.3 reads the conversation and T8.4 sends a reply. The owner chose "Chat view only".
- **Scope:** a chat screen per project, built from the design system's components, fed by T8.3 and
  updated live through T8.2. A question with options shows its options as buttons with the
  recommended one first and marked, and a field for an answer of the owner's own; a plain question
  shows the reply box. A reply posts to T8.4 and shows as sent at once; a refusal is shown in plain
  words. When the session is working the reply box says so and is disabled; when no planning session
  is open the screen says so (T8.14 and T8.15 add the links that start one). The agent's markdown is
  rendered with the page's existing renderer (`internal/web/markdown.go`), never as raw HTML. Linked
  from the project page, the plan line and the planning session's row on the sessions screen.
  From the design system (reconciled 2026-10-13): an `app-bar` titled with the project, a second line
  giving the mode and the phase as `state.json` records them, and `live`; a `thread` of agent `msg`s
  as full-width `prose` and owner `msg`s as bubbles; `question` cards with `option`s (the recommended
  one first, marked "Recommended") and an own-answer `field`; picking an option sends it as the reply
  and marks it `is-chosen`; the `composer` pinned to the bottom; while the agent works,
  `live--working` in the bar and the `working` line where the next message will appear. On the desktop
  a `split` shows on the left the `steps` of the session's own mode (§4.8), the folder and the
  repository, and on the right the thread at 720px with the composer under it. With no planning
  session, the `empty` "No planning session".
  README:
  the web page section.
- **Must not:** attach to or show the session's terminal; render the agent's text as HTML.
- **Acceptance:** handler tests over T8.3's fixtures for each state (working, waiting for text,
  waiting for a choice, none). A test that markup in an agent message is escaped. Against a scratch
  build with a stand-in planning host and a fixture transcript, in a browser at phone width: the
  options show as buttons and pressing one sends the stand-in its keystrokes. An automated WCAG 2.2
  AA check of the screen reports no violation. `make check` is green.
- Amended 2026-10-14 (T8.12 done): later tasks may assume the following. (1) The page twins are `GET` and `POST /p/{project}/plan/chat`, registered beside the `/api` chat routes outside the shared route table (count stays 20); the page POST takes form fields `text`, `choice-<i>` (repeated for multi-select) and `other`, answers 303 to `?done=sent` or redraws the chat with the refusal sentence and the API's status; no confirm. The view is `web.ChatView` (embeds `planchat.Chat`, page-only `Page` from the pure `chatPageFor`), and the JSON GET is byte-identical; JSON refusals now carry `reason` beside `error`. (2) `planchat.Reply` gains `other` (an own answer, one single-select question only, blank refused), sent with `Provider.PlanOtherKeys` (claude 2.1.300: digit N+1, a bracketed paste, Enter; others unsupported). (3) The `planning` live part folds in the chat (`readProject` now takes the project name); the thread is `#chat-thread` (no form, `data-working` while working) and the reply area `#chat-reply` uses live.js's new `data-live-form` rule (swapped unless a field has focus or holds text); `static/chat.js` posts through the JSON API and shows a pending owner message. (4) The no-session block says to start one with `afk dash`; T8.14 and T8.15 put their start buttons there and should replace its "later" clause. The project nav, the plan line (when a session is open), the sessions list's Planning row and the planning session's screen link to the chat. T8.13 links to `/p/{project}/plan/chat` and can reuse `planchat.Read`. See `## T8.12` in `docs/decisions.md`.

#### T8.13 — Notify when a planning session waits for you
- **Depends on:** T8.3, T8.12, T8.15
- **Goal:** when a planning session starts waiting for the owner, an ntfy notification goes out that
  opens its chat view.
- **Background:** the owner chose "Yes, with a link to the chat". Notifications carry a `Click` link
  only when `[web] token` and `public_url` are set (T3.1). Planning sessions are attended, so today
  nothing tells the owner one is waiting.
- **Scope:** the daemon checks, on each pass, the planning session of every project the page lists
  (including a project whose planning was started from the page, T8.15) using T8.3's "waiting"
  decision, only while the planning host is alive, and sends one notification per wait: once when it
  starts waiting and not again until it has been answered and waits again. The notification names the
  project and the first line of the question, and links to `public_url/p/{project}/plan/chat` when a
  link can be sent. Which waits were notified is kept in afk's state folder, so a daemon restart does
  not repeat one; §4 gains the file in the same change. README: notifications.
- **Must not:** notify more than once per wait; read a transcript of a planning session whose host is
  not alive; send a token in a link.
- **Acceptance:** a table test for the pure notify decision (new wait, same wait, answered then
  waiting again, host gone, no link settings). A test with a fake sender and a fake clock across a
  simulated restart. Against a scratch build with a stand-in ntfy server and a stand-in planning host
  whose fixture transcript ends in a question: one notification with the chat link arrives, and none
  on the next pass. `make check` is green.
- Amended 2026-10-14 (T8.13 done): later tasks may assume the following. (1) The daemon runs `daemon.Deps.PlanWaits` once per pass in `Run` and once in `Once` (paused or not); `plan_wait_notify.go` checks every project the page lists (configured, review list, being-planned, in the page's precedence) through `planchat.Read` and the pure `planwait.Decide`, and records sent waits in `plan-waits.json` in afk's state folder (§4.2a). (2) A wait is keyed by state, message count and the last message's time and text; a record is cleared only when the chat reads `none`, and a failed send is retried next pass. (3) The link is `notify.ChatLink` (`/p/{name}/plan/chat`) for configured and being-planned projects and `notify.PlanLink` for a review-only one, whose chat the page 404s; a page change to which projects serve a chat must change `planWaitTargets` too. (4) Only the project's own `ntfy_url` is used, so a project being planned in a new folder is not notified until it has one; `afk daemon --once` with no configured project skips the check. See `## T8.13` in `docs/decisions.md`.

#### T8.14 — Start extend, amend or reconcile from the page
- **Depends on:** T8.12, T7.3
- **Goal:** the project page starts a planning session that changes the project's plan, with the same
  effect as `afk plan extend`, `afk plan amend "<what>"` and `afk plan reconcile FILE`, and opens its
  chat view.
- **Background:** T7.3 adds an entry point beside the command that does everything `afk plan <mode>`
  does up to the attach and returns started, resumed or refused with the reason, without printing.
- **Scope:** three actions on the project page, each behind a confirm: extend; amend, which asks for
  the sentence; reconcile, which asks for a file path relative to the project. On success the page
  opens the chat view. A refusal is shown in the command's own words and nothing is started; when a
  planning session is already open the actions say so and link to its chat. The same under `/api`.
  From the design system (reconciled 2026-10-13): a **Change the plan** section on the project page
  with action rows Extend, Amend and Reconcile, each with one line of plain explanation, and the same
  three as buttons on the chat view's empty state.
  README: the web page section and `afk plan`.
- **Must not:** offer `--yes` for amend; start a session for a project with no plan; name a provider
  outside `internal/provider`.
- **Acceptance:** handler tests with a fake entry point: each mode started, a session resumed, each
  refusal, an empty sentence or path refused before the call, and a POST without the confirm changing
  nothing. Against a scratch build in a throwaway repository with a plan and a stand-in agent: extend
  from a browser creates `.afk/plan/state.json` in mode `extend` and lands on the chat view; amend
  leaves its sentence in `.afk/plan/brief.md`. `make check` is green.
- Amended 2026-10-14 (T8.14 done): later tasks may assume the following. (1) The seam is `web.Deps.StartPlan(root, mode, text)`, returning a `PlanStartResult` with `Outcome` (`started`, `resumed` or `refused`), `Reason`, `Open`, `OpenMode` and `OpenPhase`; the root wires it in `daemon_web_plan.go` as `webStartPlan` / `webStartPlanWith`, and it waits for the planning host on the project root it was given. (2) The route is `POST /p/{project}/plan/change` with an `/api` twin, so the shared route table has 21 routes (the count is asserted at `auth_test.go`); the page-only `GET /p/{project}/plan/change` is registered in `build()` outside the table and draws the form (amend, reconcile), the Extend confirm card, or the none and open-session pages. (3) The change page's decision is `changePageFor`; a start answers 303 to the chat with `?done=started` or `?done=resumed`, and the chat shows its own toast for each (`chatToastTexts`, the project page's `toastFor` is unchanged). (4) The chat's empty state holds the three buttons, and the project page's Change the plan section (`id="project-change"`, `data-live="planning"`) holds the rows; both come from `changeSectionFor`. T8.15 may assume that a planning session started from the page lands on the chat, and that the page never offers `yes` for amend.

#### T8.15 — Plan a new project in an existing folder
- **Depends on:** T8.14, T7.4
- **Goal:** the overview starts a new plan, as bare `afk plan` does, in a folder the owner types, opens
  its chat view, and keeps that project on the overview while it is being planned.
- **Background:** T7.4 extends T7.3's entry point to the new mode, with the same refusals as
  `afk plan` (a folder that does not exist, one that already has a plan or an open session). The page
  shows only projects the daemon lists or the review list names, so a new project would otherwise
  vanish from the page until it is approved.
- **Scope:** a "plan a new project" action on the overview: a field for a folder path (`~/` expands),
  a confirm showing the full path, then the planning session through the entry point and the chat
  view. A project whose planning was started here stays on the overview, marked as being planned,
  until it is approved or its planning session is gone, through a small list in afk's state folder
  that §4 gains in the same change. The same under `/api`.
  From the design system (reconciled 2026-10-13): **Plan a new project** is the primary button in the
  overview's footer and in the desktop `topnav`; it opens one form that unfolds: the **Where** `field`
  (`input--mono`, errors in `field__error`), then as the confirm a `card` "afk will create and plan
  in" with the full `path` and a `badge` saying which kind of folder it is, then **Start planning**
  (primary, with a loading state) and **Cancel** (quiet).
  README: the web page section.
- **Must not:** create a folder (T8.15a); start anything without the confirm; write `config.toml`.
- **Acceptance:** handler tests with a fake entry point: an existing empty repository, a folder with a
  plan (refused, pointing at the change actions), a folder that does not exist (refused, naming it),
  and a POST without the confirm changing nothing. A table test for the being-planned list: added on
  start, dropped on approve, dropped when the planning session is gone. Against a scratch build with a
  stand-in agent: typing the path of an empty throwaway repository and confirming leaves
  `.afk/plan/state.json` in mode `new`, lands on the chat view, and shows the project on the overview.
  `make check` is green.
- Amended 2026-10-14 (T8.15 done): later tasks may assume the following. (1) The page form is `GET /plan/new` (page-only, the Where field, then with `?path=` the confirm card from the read-only seam `web.Deps.CheckPlanFolder(text)` with kinds `repo`, `folder`, `open`, `has_plan` and `refused`), and the start is `POST /plan/new` with its `/api/plan/new` twin, so the shared route table has 22 routes; nothing starts without `confirm=yes`. The root package's check shares `startPlanForSurface`'s new-mode look at the folder (`lookAtPlanProject` with writers swapped for refusals), so its refusal sentences are the command's; T8.15a should give the page its own wording for the not-found case it now handles by creating. (2) `web.PlanStartResult` carries `Root` (symlinks resolved for `new`). (3) A started or resumed project is added to `being-planned.json` in afk's state folder (§4.2a) through `web.Deps.AddBeingPlanned`; the page lists entries through `Deps.BeingPlanned` (gone ones filtered), shows them on the overview with a "Being planned" badge and in `GET /api/` as `being_planned`, and the chat routes resolve them (configured projects first, then the review list, then this list); `plan/change` and approve still use their own finders. Approve removes the entry when the folder is on the review list; otherwise the approved phase hides it and the next start prunes it. (4) The overview footer and the desktop topnav carry **Plan a new project** linking `/plan/new`. The card says "afk will plan in"; T8.15a adds "afk will create and plan in" and the "New, empty folder" badge. See `## T8.15` in `docs/decisions.md`.

#### T8.15a — Create the folder for a new plan
- **Depends on:** T8.15, T7.5
- **Goal:** the "plan a new project" field also takes a project name, and the page makes the folder
  under the profile's project location, makes it a git repository and starts planning in it.
- **Background:** T7.5 gives the dashboard this as a pure decision from the typed text, the profile's
  `project location` and the disk (use this folder, create this folder, or refuse with a reason),
  and makes the repository the way `afk plan` does.
- **Scope:** T8.15's field takes a name as well as a path (text with no `/` and no `~` is a name),
  through T7.5's decision and refusals: a name that is not a plain folder name (saying which
  characters are allowed), a folder that already exists (offering to plan in it as T8.15 does), and a
  name with no `project location` in the profile (saying how to set one with `afk profile set`, or to
  type a full path). The confirm shows the full path before anything is created.
  From the design system (reconciled 2026-10-13): the field's hint names the profile's `project
  location` ("A name makes a new folder in …"), never a fixed folder, and the confirm card's badge
  reads "New, empty folder".
  A failure part-way
  says what was left on disk and removes nothing. Then planning starts as in T8.15. The same under
  `/api`. README: the web page section.
- **Must not:** create a folder outside the project location from a bare name; create anything
  without the confirm; delete anything; create anything on GitHub (T8.15b).
- **Acceptance:** handler tests with a fake decision and a fake filesystem: a name created, a name with
  no project location, a name with bad characters, a folder that exists, and a POST without the
  confirm creating nothing. Against a scratch build with a scratch profile whose location is a
  scratch folder and a stand-in agent: typing `demo` and confirming leaves `<scratch>/demo/.git`,
  `.afk/plan/state.json` in mode `new`, and `demo` on the overview. `make check` is green.
- Amended 2026-10-14 (T8.15a done): later tasks may assume the following. (1) `web.PlanFolderCheck` gains kind `create` (badge "New, empty folder", heading "afk will create and plan in") and a `Name` field set for any bare name; two optional seams, `web.Deps.CreatePlanFolder(text) (path, error)` and `web.Deps.PlanLocation() string`, are wired in `daemon_web_plan.go` (`webCreatePlanFolder`, `webPlanLocation`, the name check `webCheckPlanFolderNamesWith`). (2) The create confirm posts the typed name, not a path, and the create seam decides again from it, so only a bare name under the project location is ever made; it runs only after `confirm=yes`, and `StartPlan` gets the created path. T8.15b's GitHub question goes between the create and `StartPlan` in `planNew`. (3) `POST /api/plan/new` success gains `created`; a failed create answers 400 with reason `create_failed` and the `path` left on disk (empty when nothing was made), and starts nothing. No route was added (the table stays at 22). See `## T8.15a` in `docs/decisions.md`.

#### T8.15b — Offer a GitHub repository for the new folder
- **Depends on:** T8.15a, T7.6
- **Goal:** straight after T8.15a makes a folder, the page asks whether to create a private GitHub
  repository for it with the owner's existing `gh` login, and on yes sets it as the folder's `origin`.
- **Background:** T7.6 gives the dashboard the pure decision (ask, or do not ask and why) and the
  create call behind a `gh` interface with a fake. The owner's profile has the rule "accounts: use the
  accounts I already have, and ask before opening a new one", which is why this is a question every
  time and never a setting.
- **Scope:** after the folder is made and before the chat view opens, one yes-or-no naming the
  repository that would be created (the folder's name, under the account `gh` is logged in as) and
  saying it will be private; no is the default and changes nothing.
  From the design system (reconciled 2026-10-13): the question is a `choice-list` ("Create a private
  GitHub repository for it?") with **No** checked, asked after the folder is made and not inside the
  form before it.
  Yes creates it and adds it as
  `origin` through T7.6's code; nothing is pushed. When `gh` is missing or not logged in the question
  is not asked and the page says why and what to run. A failure is shown with its reason and planning
  starts all the same. The same under `/api`. README: the web page section.
- **Must not:** create a public repository; create anything without the owner's yes; push; ask for a
  folder that existed before T8.15a's form; store a token.
- **Acceptance:** handler tests with a fake `gh`: yes makes exactly one private create and sets
  `origin`, no makes none, `gh` missing, not logged in, name taken and a network error. No test calls
  GitHub. Against a scratch build with a scratch profile, a stand-in agent and a stand-in `gh` on a
  scratch `PATH`: typing `demo`, confirming and answering yes makes the stand-in record one private
  create and `git remote -v` in the new folder show `origin`. `make check` is green.
- Amended 2026-10-14 (T8.15b done): later tasks may assume the following. (1) Two optional seams, `web.Deps.GitHubOffer(path) web.GitHubOffer` and `web.Deps.CreateGitHubRepo(path, repo) web.GitHubOutcome`, are wired in `daemon_web_plan.go` over `internal/ghrepo`; the check order is shared with the dashboard through `gitHubDecisionWith` in `dash_plan.go`. Both nil keeps T8.15a's behaviour. (2) After a create, `POST /plan/new` draws the question (API: 200 with outcome `github_question`, nothing started); the answer is the new `POST /plan/new/github` with its `/api` twin, so the shared route table has 23 routes. The server keeps the folders it just made and the repository name it offered in memory (one hour, at most 32, one-shot); an answer for any other folder is refused with reason `not_offered`, and the name is never taken from the request. (3) Planning starts after the answer whatever the outcome, through `startPlanNew`, the one shared tail of both POSTs; a declined answer redirects to the chat, any other outcome draws the started page with its line. Answers carry a `github` object (`outcome`, `repo`, `text`, or `not_offered` with `reason`). See `## T8.15b` in `docs/decisions.md`.

#### T8.16 — Add and remove projects
- **Depends on:** T8.5, T7.10
- **Goal:** the overview adds a folder to the daemon's project list and removes one, with the same
  effect and words as `afk project add` and `afk project remove`.
- **Background:** T7.10 adds entry points beside the two commands that return what happened without
  printing; they edit `config.toml` as text, so comments and the `[dash]` table survive. The page
  shows the daemon's last good list, which picks up the change on its next pass.
- **Scope:** an add action with a folder field and an optional name, and a remove action
  behind a confirm that names the project and says its folder, plan and sessions are not touched and
  a running task is left to finish. Refusals in the command's words; a `config.toml` that cannot be
  read or written is said so, naming the file. The row appears or goes when the daemon next reads the
  list, and the page says so until then. The same under `/api`.
  From the design system (reconciled 2026-10-13): **Add a project** is an `icon-btn` in the overview's
  `app-bar` and a secondary button in its footer, opening a form with a Folder `field`, an optional
  Name `field` (hint "Defaults to the folder name") and **Add project**. **Remove a project** (quiet,
  in the footer) opens a screen with a `choice-list` of the projects and **Remove…**, which opens the
  `confirm-sheet` with **Remove** (danger) and **Keep it** (focused).
  README: the web page section and
  `afk project`.
- **Must not:** write `config.toml` any other way; remove without the confirm; delete a folder, a
  session or a ledger.
- **Acceptance:** handler tests with fake entry points: each outcome and each refusal, and a POST
  without the confirm changing nothing. Against a scratch build with a scratch config folder: adding a
  throwaway repository from a browser makes `afk project list` (same scratch folder) print it and the
  row appear; removing it takes both away. `make check` is green.
- Amended 2026-10-14 (T8.16 done): later tasks may assume the following. (1) Two optional seams, `web.Deps.AddProject(folder, name) web.ProjectEditResult` and `web.Deps.RemoveProject(name)`, are wired by `webAddProject`/`webRemoveProject` in `daemon_web_projects.go` over T7.10's entry points, serialised by one mutex; add expands `~` and refuses other relative paths, remove takes a name the server finds in the daemon's own list. (2) `POST /projects/add` and `POST /projects/remove` with `/api` twins (25 routes), both needing the confirm; page-only GETs `/projects/add`, `/projects/remove` and `/projects/remove/confirm?name=`. JSON answers `{"result":{outcome,name,root,text},"pending"}` (root is empty after a remove). (3) `OverviewView` gains `pending` (sentences, never null) from an in-memory list (32 entries, 10 minutes) that clears when the daemon's list shows the change; toasts `project_added`, `project_removed`, `project_unchanged` show on the overview only. (4) The remove sheet reuses the session-sheet partial; `confirm.js` fills `{folder}` and skips unchecked radios. See `## T8.16` in `docs/decisions.md`.

#### T8.17 — Prove it on a phone (owner + agent)
- **Depends on:** T8.6, T8.7, T8.8, T8.10, T8.11, T8.13, T8.15, T8.16, T8.15b, T8.15a
- **Goal:** the owner runs afk from their phone for one whole plan, and every screen meets WCAG 2.2 AA.
- **Scope:** the owner, on their phone through their usual way of reaching the page: plan a small
  throwaway project from the overview (folder and private repository included), hold the planning
  session in the chat view to `review`, approve it, answer an inbox item, pause and resume, set a
  task's status and refresh usage. The session for this task waits for them (an inbox item), then
  runs an automated WCAG 2.2 AA check of every screen against a scratch build, fixes what it finds,
  and writes up in `docs/decisions.md`: what the owner had to do in a terminal or the dashboard
  instead, every fault met and its cause, and the check's results. Faults in code that this task
  cannot fix are filed in the inbox.
- **Acceptance:** the throwaway project's plan was approved from the phone and its first task merged
  by the daemon; the automated check reports no violation on any screen; the write-up names every
  place the owner had to leave the page.
- Amended 2026-10-16 (T8.17 done): later tasks may assume the following. (1) `make check` now also runs `make web-js-test` (`node --test internal/web/jstest/*.test.mjs`); a pure function in `internal/web/static/chat.js` is exported for node only when `module` exists, and its tests live outside `static/` because `//go:embed static` would serve them. (2) Pending "Sent" bubbles in the chat are pruned by owner-message count, not by text. (3) `internal/ship` picks the push-failure hint with `pushHint`, which names the GitHub workflow-scope refusal. (4) Afk never writes the agent CLI's trust record; a brand-new folder still stops at the trust prompt (inbox vbkr6s). (5) Every web screen passes axe WCAG 2.2 AA at 390 and 1280 px in both schemes; the harness is not in the repo (see `docs/decisions.md` `## T8.17`), so a later task re-checks with its own run.

#### T8.18 — Sign in to the web page with a password
- **Depends on:** —
- **Goal:** the owner turns the web page on with `afk web password` and signs in on a form with that
  password; the browser stays signed in for 180 days and has a sign-out button. The token, `?token=`
  and the `Authorization: Bearer` header are gone, so there is one way in and nothing to paste into
  `config.toml` or an address bar.
- **Background:** the owner asked on 2026-10-17: "The token based authentication for web got us off the
  ground but it isn't a tenable path forward for public release. This is intended for a single person
  use so I think we can make it simple". Offered a password, a one-time sign-in link from the terminal,
  a passkey, or leaving sign-in to Tailscale or a proxy, they chose the password, and chose to remove
  the token rather than keep it for scripts. Today's sign-in is `internal/web/auth.go` (the `afk_web`
  cookie as a hash of the token, set by a `?token=` visit; the Bearer header; the `Origin` check),
  `build` in `internal/web/web.go` refuses an empty token, and `daemon_cmd.go` starts the page only when
  `[web] token` is set (§4, "Amended 2026-10-12 (T3.1)").
- **Scope:**
  - **`afk web password`**: asks for a password twice without echo and saves only a slow, salted hash
    of it, never the password, in a file in afk's state folder (`$XDG_STATE_HOME/afk/`, else
    `~/.local/state/afk/`), mode 0600, written atomically. Not in `config.toml`, so that file stays
    free of secrets. At least 8 characters; a mismatch or a short one says so and saves nothing. It
    refuses in plain words when not run in a terminal. Setting a new password signs every browser out.
    Record the hash chosen (and why) in `docs/decisions.md` under `## T8.18`.
  - **The page is on when the password file exists.** `afk daemon` reads it when the page starts; a
    `bind` other machines can reach with no password stops the daemon with a message naming
    `afk web password` (it named `web.token` before). With no password the log line says to run
    `afk web password`. The page reads the file again on each sign-in and cookie check, so a new
    password takes effect without restarting the daemon.
  - **The sign-in page**: any request without a valid cookie gets a sign-in form (a page; under `/api/`
    a 401 with a JSON error), built from components `docs/design-system.md` already has and working
    with JavaScript off. A right password sets the cookie (180 days, HttpOnly, SameSite Lax, Secure as
    today) and goes on to the address first asked for, never to another site. The cookie's value is
    tied to the stored hash, so it is useless once the password changes. Wrong passwords are slowed
    down: after 5 wrong in a row the form refuses for a minute, in memory, and says so. The form's POST
    passes the existing `Origin` check.
  - **Sign out**: a button on every signed-in page (in the bars' actions, from existing components) that
    POSTs, clears the cookie and shows the sign-in page.
  - **The token goes**: `[web] token`, `?token=`, the Bearer header, the cookie-from-token path and
    their tests. A `[web] token` still in `config.toml` is not an error: the daemon ignores it and logs
    one line saying the page no longer uses a token, to remove it and run `afk web password`.
    Notifications carry the `Click` link when the page is on and `public_url` is set, as before.
  - **Docs**: README's web section ("Turn it on", "Where it listens", "Sign in once", the `curl` Bearer
    example) and `afk web password` in the command list; §4's `[web]` contract and the Decisions rows
    "Web surface" and "Web page as command center" amended in place to say a password, not a token.
- **Must not:** store or log the password or anything that can be reversed into it; put a secret in
  `config.toml`; add a JavaScript requirement for signing in or out; change the `Origin` check's
  rules; add multi-user accounts, a "remember me" choice or a password reset by email.
- **Not in scope:** removing the password (`afk web password --remove`), a passkey, TLS of the page's
  own.
- **Acceptance:** table tests for the pure sign-in decisions: no cookie, a good cookie, a cookie from
  before a password change, a right and a wrong password, the sixth wrong try within the minute, a
  `?token=` visit and a Bearer header (both now refused), a return address pointing at another site
  (sent to `/` instead); `/api/` without a cookie gets a JSON 401. Table tests for the daemon's start-up
  choice: no password on loopback (page off), no password on a reachable bind (error), a password,
  a leftover `[web] token` (logged, ignored). `afk web password` tested with an injected terminal:
  mismatch, too short, not a terminal, saved (the file holds no copy of the password, mode 0600).
  Against a scratch build (scratch `XDG_STATE_HOME` and `XDG_CONFIG_HOME`, `afk web password`, then
  `afk daemon`), in headless Chrome at 390px and 1280px wide: the overview shows the sign-in form; a
  wrong password is refused; the right one reaches the overview; sign out returns to the form; with
  JavaScript off the same works; an axe run on the sign-in page reports no violation. `curl` with
  `Authorization: Bearer` gets 401. `make check` is green.
- Amended 2026-10-17 (T8.18 done): later tasks may assume the following. (1) The page's one secret is `web-password.json` in afk's state folder, written only by `afk web password` and read through `internal/webpass` (PBKDF2-SHA256, 600,000 rounds); the page is on when that file exists at daemon start, and an address other machines can reach with no readable password stops the daemon, `--once` included. (2) Every request without the `afk_web` cookie gets the sign-in form (JSON 401 under `/api/`); there is no token, `?token=` or Bearer header, so scripts and tests sign in with a cookie made from a record (`api_test.go` and the daemon rig have low-round helpers). (3) `web.Deps.Password` is read on every cookie check; only `POST /signin` hashes, under one mutex with the five-try lockout. (4) Both bars carry a plain sign-out form posting to `/signout`; page tests that forbid forms strip it with `noSignOut`. (5) `webPublicURL` and the notification link builders take a page-on bool. See `## T8.18` in `docs/decisions.md`.

### M9 — Choose your own leader key

**Outcome:** the dashboard's leader is ctrl+], which no common launcher, tmux default or agent takes,
and anyone who wants another key sets it in `config.toml`.

#### T9.1 — The default leader becomes ctrl+]
- **Depends on:** T7.11, T7.12, T7.13 (all three edit `internal/ui`, and tasks in one Go package collide at merge)
- **Goal:** ctrl+] opens the actions dialog everywhere ctrl+space did, and the dashboard names ctrl+]
  wherever it named ctrl+space. ctrl+space, ctrl+@ and ctrl+2 no longer open it.
- **Background:** the owner asked: "Change the leader - ctrl-space is owned by Raycast, I forgot. I can
  use ctrl-2 for the time being but it needs to be something different." ctrl+space, ctrl+@ and ctrl+2
  are one byte (NUL), which is why ctrl+2 works today (`docs/decisions.md`, `## T7.1`, "How ctrl+space
  arrives"). ctrl+] is its own byte (0x1D) in the ordinary encoding, and tmux's default prefix is ctrl+b;
  the owner's is ctrl+a. The key is the constant `leaderKey` in `internal/ui/leader.go`; hints and help
  go through `leaderChord` and `leaderHelpBinding`, so most text follows it. The pass-through (leader
  twice in the agent pane types the leader to the agent) is in `internal/ui/messages.go`.
- **Scope:** read in the module cache how ultraviolet decodes 0x1D and how a kitty-protocol terminal's
  ctrl+] arrives, and record the key string bubbletea reports for both (`docs/decisions.md`, `## T9.1`).
  Change the leader to that key. Pressing it twice in the agent pane types one ctrl+] (0x1D) to the
  agent. ctrl+space stops being the leader and becomes an ordinary key: in the agent pane it reaches
  the agent as NUL, as every non-leader key does (T7.2), so the pane's encoding of it stays. Update
  every code comment, test, saved screen
  (`internal/ui/testdata/golden/`, regenerated with the tests' update flag), `dash.go`, `README.md`,
  `CONTEXT.md` and the leader's line in the README that says ctrl+@ also opens it.
- **Must not:** change any key after the leader, `ctrl+c` quitting from the session list, or
  `afk attach`'s ctrl-\.
- **Acceptance:** `go test ./internal/ui/` passes with a table row each that ctrl+] opens the dialog
  from the list, the detail side and the agent pane, that ctrl+space opens no dialog in any of them
  (in the agent pane it types one NUL to the agent), and that ctrl+] twice in the pane sends one 0x1D.
  Outside the pane's key encoding and its tests, `/usr/bin/grep -rn 'ctrl+space' --exclude-dir=.git --exclude-dir=.afk
  --exclude-dir=.claude . | grep -v -e docs/PLAN.md -e docs/decisions.md` prints nothing. Against a
  scratch build in a throwaway repo, inside tmux with the default prefix: `tmux send-keys C-]` opens the
  dialog from the list and from a focused agent pane, and twice leaves one `^]` at a stand-in agent.
  `make check` is green.
- Amended 2026-10-14 (T9.1 done): the leader is `leaderKey = "ctrl+]"` in `internal/ui/leader.go`, but two places still name ctrl+] literally and T9.2 must derive them from the configured key: the pass-through in `leaderComponentKey` (`internal/ui/model.go`) forwards `{Code: ']', Mod: ModCtrl}`, and `projectEditOpenScreenFirst` in `internal/ui/projectedit.go` is a const naming `ctrl+] A`. Tests press the leader through `leaderKeyPress` (`leader_wiring_test.go`), and `TestLeaderIsCtrlBracket` checks the bytes the agent reads through a real x/vt emulator. Comments and the README write the old key as "Ctrl+Space" so the acceptance grep stays clean.

#### T9.2 — Set the leader in config.toml
- **Depends on:** T9.1
- **Goal:** `leader = "<key>"` in the `[dash]` table of `config.toml` makes that key the dashboard's
  leader; with no such line it is ctrl+].
- **Background:** the owner asked that the leader "should be configurable, as some people remap keys",
  and chose `[dash]` in `config.toml` over a flag. `internal/daemonconfig` already reads `[dash]`, and a
  bad `[dash]` value is carried as `Config.DashErr`, which the dashboard reports and never treats as a
  load failure.
- **Scope:** a `Leader` field on `daemonconfig.Dash`, checked by a pure function in `internal/ui` (or a
  small package it imports) that takes the configured string and returns the key to use and, when it
  refuses, why. It refuses an empty-after-trim value as "unset" (use the default, no warning), and
  refuses with a reason: a name bubbles/key would never report, a key with no modifier (a plain letter,
  digit or symbol), and the keys the dashboard keeps for itself without the leader (`enter`, `esc`,
  `ctrl+c`, the movement keys, `tab`). A refused key falls back to ctrl+] and the header shows one line
  saying what is wrong and what to put in `config.toml`. The line stays while the dashboard runs; it
  is not the toast the folder search raises for its own `[dash]` mistakes (§4.6), because a leader
  that silently differs from the one configured would leave the owner pressing a key that does nothing.
  `leaderKey` becomes a value the model holds, so every hint, help line, the dialog's title
  and the pass-through name and use the configured key; the pass-through types to the agent what x/vt
  encodes for that key. README: the `[dash]` settings, the leader section, and a note that a key sharing
  a byte with another (ctrl+m with Enter, ctrl+i with Tab, ctrl+h with Backspace, ctrl+[ with Esc) works
  only in a terminal that reports it apart (kitty protocol, as Ghostty does) and, under tmux, with
  `set -s extended-keys on`.
- **Must not:** read the setting anywhere but `config.toml`; stop the dashboard from starting over a
  bad value.
- **Acceptance:** a table test of the pure check with a row for each refusal and for `ctrl+]`,
  `ctrl+m`, `alt+space` and an unset value; a `daemonconfig` test that `[dash] leader` is read and a
  non-string value lands in `DashErr`; UI model tests that a configured `ctrl+o` opens the dialog and
  its hints say `ctrl+o`, and that `leader = "q"` leaves ctrl+] working with the header line shown.
  Against a scratch build with a scratch config folder: `leader = "ctrl+o"` opens the dialog on ctrl+o
  through tmux `send-keys C-o`, and `leader = "enter"` shows the header line and ctrl+] still works.
  `make check` is green.
- Amended 2026-10-14 (T9.2 done): later tasks may assume the following. (1) `daemonconfig.Dash.Leader` is the trimmed raw string ("" when unset); only a non-string value becomes `DashErr`, and the key itself is checked by the pure `checkLeader` in `internal/ui/leaderconfig.go` (exported for other packages as `ui.LeaderName`). (2) `afk dash` reads it once at start through `startupLeader` in `dash.go` into `ui.Deps.Leader`, best effort (a load error or `DashErr` means the default); there is no live reload, so a change applies on restart. (3) The model holds the checked key as `Model.leaderKey` (a `leaderSetting`: name, decoded `tea.Key`, refusal); the old const is `defaultLeaderKey` and is used only as the fallback, so new hint text must take the leader from the model (free helpers take it as a parameter, panels through `panelContext.Leader`). (4) Pressed twice in the agent pane, the decoded key goes straight to the pane's `SendKey` for x/vt to encode. (5) A refused key shows a persistent header line (`leaderHeaderForms` in `views.go`, three lengths) ahead of every other header piece. See `## T9.2` in `docs/decisions.md`.

### M10 — Find your way back on the desktop

**Outcome:** on a desktop browser every page but the overview and the error page has a back button and its title under the
top bar, as the phone does, so the browser's own back button is never the only way back.

#### T10.1 — A back row on desktop pages
- **Depends on:** —
- **Goal:** above 760px, every page but the overview shows a row under the top bar holding the phone
  bar's back chevron and the page's title. The chevron goes where the phone's back button goes and has
  the same spoken name.
- **Background:** the owner reported: "The desktop view doesn't seem to have the same back button
  affordances as the mobile view. I have to use the browser back button to navigate back." The cause:
  `internal/web/templates/layout.html` has two bars, `app-bar` (phone: back `icon-btn`, title, live) and
  `topnav` (desktop: wordmark, Projects, Usage, live mirror, actions), and `internal/web/static/page.css`
  hides `.app-bar` above 760px. The back address, its spoken name and the title are already worked out
  per page by the pure `chromeFor` in `internal/web/pages.go` (`chrome.Back`, `BackLabel`, `Title`) and
  table-tested in `TestChromeFor` (`pages_test.go`); the error page is `Bare` and draws no bars. The
  owner chose the phone's chevron and title over a breadcrumb or a plain link, built from components
  the design system already has (`icon-btn`, the title style) with no design round.
- **Scope:** in `layout.html`, a desktop-only row under `topnav` (not on the overview, not on a `Bare`
  page) with the same back `icon-btn` (its `href` from `.Back`, its `aria-label` from `.BackLabel`, the
  same chevron svg) and the title from `.Title`. Its CSS goes in `page.css` beside the bar rules: shown
  only above 760px, hidden below it, so the phone keeps exactly one back button and the desktop gets
  one. Use afk.css's existing classes and tokens; add no colour, size or spacing value afk.css does not
  already define. The page's own `<h1>` stays as it is. Record in `docs/decisions.md` under `## T10.1`
  that the desktop row reuses the phone bar's parts and why (no design round, the owner's choice), so a
  later reconcile against `docs/design-system.md` does not read it as a stray component.
- **Must not:** change `chromeFor`'s addresses or names (the phone's back button is right today); change
  `topnav`'s links or actions; change the JSON API; add JavaScript for it (the row works with
  JavaScript off).
- **Acceptance:** a table test renders the layout for every page `chromeFor` knows (overview, usage,
  project, plan with and without `WaitingForApproval`, inbox, item, tasks, task, sessions, session,
  set status, each confirm, chat, change, new plan, add, remove, remove confirm, plan result, error)
  and checks that the desktop row is present with the right `href`, `aria-label` and title on every
  page but the overview and the error page, and absent on those two. Against a scratch build serving
  the page (`afk daemon` with scratch state and config folders), in headless Chrome at 1280px wide:
  a project page, a task page and the usage page each show the back chevron, and following it reaches
  the address `chromeFor` names; at 390px wide each page shows exactly one back button (the phone
  bar's). An axe run on a project page at 1280px reports no new violation. `make check` is green.
- Amended 2026-10-16 (T10.1 done): later tasks may assume the desktop back row is `<nav class="back-row" aria-label="Back">` in `layout.html`, right after the `topnav` header, inside the `{{if not .Bare}}` block and only when not `.Overview`, holding the same back `icon-btn` (`.Back`, `.BackLabel`) and `<span class="app-bar__title">{{.Title}}</span>`; `page.css` hides it at 760px and below. It is a labelled nav, not a div, because axe's region rule needs every element in a landmark. `TestDesktopBackRow` and `TestBackRowHiddenOnPhone` in `pages_test.go` cover it; T10.2's tests can check "the bar's back button is still there" against either bar. See `## T10.1` in `docs/decisions.md`.

- Amended 2026-10-16: "every page but the overview" in the Goal means every page but the overview and the error page; the error page is `Bare` and draws no bars, as the Scope and Acceptance already say.
- Amended 2026-10-19: the design system now draws this row as `app-bar app-bar--back app-bar--desktop` (design-system reconcile); T10.3 rebuilds the `.back-row` in that shape, with the same addresses, names and pages.
#### T10.2 — Remove the in-page back links that only repeat it
- **Depends on:** T10.1 (both edit `internal/web`, and tasks in one Go package collide at merge)
- **Goal:** a "Back to …" link in a page's content that goes where the bar's back button goes and does
  nothing else is gone, on the phone and the desktop alike, because both now have the bar's button.
- **Background:** the owner chose to remove the links that only duplicate the back button and keep the
  ones that are the safe choice beside an action.
- **Scope:** remove these, from `internal/web/templates/`: the "Back to the overview" / "Back to
  <project>" paragraph at the top of `plan.html`; the "Back to the plan" button in `plan_result.html`
  and in the cannot-be-approved branch of `plan_confirm.html`; the "Back to <project>" button in the
  `none`, `open` and `unknown` branches of `plan_change.html` (the `open` branch keeps "Open its chat");
  the "Back to all projects" button in the `started` branch of `plan_new.html` (it keeps "Open its
  chat") and in the empty-list branch of `project_remove.html`; the "Back" button in the last
  (`{{else}}`) branch of `session_confirm.html`. Before removing each, check that its `href` equals the
  `chrome.Back` that `chromeFor` gives the same view; a link whose address differs stays and is listed
  in `docs/decisions.md` under `## T10.2` with why. A `btn-row` left empty is removed with it. Update the
  tests that looked for the removed links.
- **Must not:** remove a confirm's safe button (`.Sheet.Safe`), a Cancel beside a form, the "Back" under
  the new-session form in `session_confirm.html`, the "Back" beside the submit in `task_status.html`, or
  any link that goes somewhere other than the bar's back address.
- **Acceptance:** a table test renders each changed page in each changed branch and checks that the
  removed link is gone and the bar's back button is still there with the same `href`; rows for the
  kept links (a confirm's safe button, `task_status.html`'s Back, the new-session form's Back) check
  they are still there. Against a scratch build in headless Chrome at 390px and 1280px wide, the plan
  page and the remove-a-project page with an empty list show one way back, the bar's. `make check` is
  green.
- Amended 2026-10-16 (T10.2 done): later tasks may assume page content has no "Back to …" link that repeats the bar's back address; the bar (`app-bar` on the phone, `back-row` on the desktop) is the only way back on the plan, plan result, cannot-be-approved confirm, plan change (none, open, unknown), new plan started, empty remove-a-project and not-offered session confirm pages. The new-plan "started" page draws its button row only when there is a chat. Every removed link matched `chromeFor`'s `Back`, so none was kept for its address. `TestInPageBackLinksRemoved` in `pages_test.go` covers each branch and the kept links; a body `<a>` whose text starts with "Back" is a content link, since the bar's buttons are icon-only. See `## T10.2` in `docs/decisions.md`.

#### T10.3 — The desktop back bar in the design system's shape
- **Depends on:** T10.2 (both edit `internal/web`, and tasks in one Go package collide at merge)
- **Goal:** the desktop's way back is the design system's `app-bar app-bar--back app-bar--desktop` under the
  `topnav`, drawn as `docs/design-system.md` draws it (`topnav (desktop)` and the `.app-bar--desktop` rules),
  in place of T10.1's `.back-row`. It goes where it goes today, on the same pages, with the same spoken names.
- **Background:** T10.1 built the row from the phone bar's parts with no design round. The owner has since
  had the design system draw it: not sticky, `min-height: 52px`, the back button lined up with the 24px
  content edge, the title, and optional meta. On the desktop the plan page's title is "Plan · <project>" and
  the planning chat's bar shows the phase as meta. The plan wins on behaviour and the design system on looks,
  so only the looks change.
- **Scope:** in `internal/web/templates/layout.html`, replace the `<nav class="back-row">` with the design's
  `app-bar app-bar--back app-bar--desktop` under the `topnav`: the same back `icon-btn` (`.Back`,
  `.BackLabel`, the same chevron svg), the title from `.Title`, and a `meta` when the page has one. Add the
  design's `.app-bar--desktop` rules to `internal/web/static/afk.css` as the design system gives them, and
  remove `.back-row` from `page.css`; `page.css` keeps the desktop bar shown above 760px and hidden at 760px
  and below (the phone `app-bar` stays the one way back there), and the phone bar stays sticky. The plan
  page's title becomes "Plan · <project>" (in `chromeFor`, so both bars say it). The planning chat page gives
  the desktop bar its phase as meta. Keep the bar inside a landmark, as T10.1's nav was, so axe's region
  rule still passes. Update `TestDesktopBackRow`, `TestBackRowHiddenOnPhone` and any test that looked for
  `back-row` or "Plan: ". Replace the `.back-row` lines under `## T10.1` in `docs/decisions.md` with a
  `## T10.3` entry saying the row now follows the design system.
- **Must not:** change `chromeFor`'s addresses or spoken names; show the bar on the overview or a `Bare`
  page; change `topnav`'s links, actions or the Sign out form; change the JSON API; add JavaScript for it
  (the bar works with JavaScript off); add a colour, size or spacing value the design system does not give.
- **Acceptance:** the table test renders every page `chromeFor` knows and checks that the desktop bar has
  the classes `app-bar app-bar--back app-bar--desktop`, the right `href`, `aria-label` and title on every
  page but the overview and the error page, and is absent on those two; a row checks the planning chat's
  bar carries the phase as meta, and one checks the plan page's title is "Plan · <project>". A test checks
  `page.css` hides the desktop bar at 760px and below and no `.back-row` rule is left. Against a scratch
  build serving the page, in headless Chrome at 1280px wide: a project page, a task page and the usage page
  each show the back chevron in a bar that does not stick when the page scrolls, with its left edge on the
  content's 24px edge, and following it reaches the address `chromeFor` names; at 390px wide each page shows
  exactly one back button (the phone bar's). An axe run on a project page at 1280px reports no new
  violation. `make check` is green.
- Amended 2026-10-19 (T10.3 done): later tasks may assume the desktop back bar is `<nav class="app-bar app-bar--back app-bar--desktop" aria-label="Back">` in `layout.html`, right after the `topnav` header, on the same pages as T10.1's row (no `.back-row` is left); it holds the back `icon-btn`, `<span class="app-bar__title">` and, when `chrome.Meta` is set, `<span class="meta">`. `chrome.Meta` is new: `chromeFor` sets it only for the planning chat, to the chat's mode and phase line. The plan page's bar title is "Plan · <project>" (its `<title>` and `<h1>` still say "Plan: "). `page.css` hides `.app-bar:not(.app-bar--desktop)` above 760px and `.app-bar--desktop` at 760px and below; the design's three `.app-bar--desktop` rules are in `afk.css`, and their design-system text is now committed. `TestDesktopBackRow`, `TestDesktopBarMeta` and `TestDesktopBarStyles` in `pages_test.go` cover it. See `## T10.3` in `docs/decisions.md`.

### M11 — Install on a fresh machine

**Outcome:** a hobbyist with a fresh Ubuntu machine (a cloud server, a spare computer, or a Multipass VM
on their laptop) runs one command, answers a few questions, and ends with the daemon running as a
service and the web page's address on screen. A Mac works the same way, behind an "at your own risk"
screen. Running it again checks every step and fixes what is missing.

The owner decided on 2026-10-17 (§2, "Where afk runs, and installing it"): a machine set aside for afk is
the target and the docs say why before any step; Docker was set aside for a VM; the installer is
`afk install`, built with the charm modules, and it writes Claude Code's
`skipDangerousModePermissionPrompt` itself; macOS is supported; release binaries are coming, so a
bootstrap script can download one. `docs/install.md` is the guide each task below keeps up to date.

```
T11.1 releases ──▶ T11.5 bootstrap ──┐
T11.2 install plan ──▶ T11.3 Linux ──▶ T11.4 macOS ──┴──▶ T11.6 prove it (owner)
T8.18 password ──────────┘
```

#### T11.1 — Release builds and `afk version`
- **Depends on:** —
- **Goal:** pushing a tag `v<semver>` publishes a GitHub release holding an afk binary for linux/amd64,
  linux/arm64, darwin/amd64 and darwin/arm64 and a SHA-256 checksums file, and `afk version` (and
  `afk --version`) prints the version a binary was built from.
- **Background:** the bootstrap script (T11.5) needs binaries it can download, so a hobbyist never
  needs Go. afk has no version flag today. afk uses no cgo, and `CGO_ENABLED=0 GOOS=darwin
  GOARCH=arm64 go build` succeeded on the VM on 2026-10-17, so every target builds on one Linux
  runner. `CLAUDE.md` says a Go binary copied to another path on macOS arm64 is killed by the kernel;
  whether a downloaded and extracted release binary runs, or needs `codesign --force --sign -` first,
  has not been checked, and T11.5 depends on the answer.
- **Scope:**
  - `afk version` and `afk --version`: print `afk <version> (<commit>, <date>)`. The version comes from
    `-ldflags "-X main.version=..."` in a release build; a `make install` build prints `dev` and takes
    the commit and its date from `debug.ReadBuildInfo` (`vcs.revision`, `vcs.time`, `vcs.modified`
    shown as `+dirty`). A pure function from (ldflags version, build settings) to the line.
  - `.github/workflows/release.yml`: on a pushed tag matching `v*`, run `make check`, build the four
    targets with `CGO_ENABLED=0` and the version flag, pack each as `afk_<version>_<os>_<arch>.tar.gz`
    (holding `afk`, `LICENSE` if there is one, and `README.md`), write `checksums.txt` (`sha256sum`
    format), and create the release with `gh release create`. A `workflow_dispatch` run with an input
    `dry_run: true` does everything but create the release, and keeps the files as run artifacts.
  - A job on a `macos-latest` runner that downloads the darwin/arm64 archive from the same run, extracts
    it, runs `afk version`, and records in `docs/decisions.md` under `## T11.1` whether it ran without
    `codesign`.
  - Docs: `afk version` in `docs/reference.md`; a "Making a release" section in `docs/development.md`.
- **Must not:** publish anything on a push to `main` or from a pull request; change what `make install`
  does; add a release tool (goreleaser and the like): the workflow is plain `go build`, `tar` and `gh`.
- **Acceptance:** table test of the version line: a release version, `dev` with a commit, `dev` with a
  dirty tree, and no build info at all (`dev (unknown)`). A scratch build prints `dev (<this commit>…)`.
  A `workflow_dispatch` dry run on this branch is green, its four archives and `checksums.txt` are
  attached as artifacts, `sha256sum -c checksums.txt` passes on them, the linux/amd64 binary prints its
  version, and the macOS job's answer is in `docs/decisions.md`. `make check` is green.
- Amended 2026-10-19: no tag was ever pushed, so the workflow never ran and no release exists; the owner's dry run that day was green and the darwin/arm64 binary ran without `codesign`. M12 (T12.1–T12.4) makes a merge to `main` that changes the binary publish a release, reversing this task's "never publish on a push to `main`".
- **Amended 2026-10-17 (T11.1 done):** later tasks may assume the following. The version is the tag without its `v` (tag `v1.2.3` gives `afk 1.2.3 (…)` and `afk_1.2.3_<os>_<arch>.tar.gz`); `scripts/release.sh <version> <dir>` builds the same four archives and `checksums.txt` locally; a hand run of `release.yml` is a dry run unless `dry_run` is off, and publishes only from a `v*` tag. The GitHub dry run and the macOS codesign answer are still the owner's to run and record, so T11.5 must not assume either answer yet.

#### T11.2 — The install plan, as a pure function
- **Depends on:** —
- **Goal:** one pure function takes what afk found out about the machine and returns the ordered list
  of install steps, each marked done, to do, or cannot (with the reason in plain words), and how risky
  the machine is to run afk on.
- **Background:** `CLAUDE.md`'s rule: anything that branches on the environment is a pure decision with
  every branch a table row, and the probing happens in the caller. Most probes already exist: the
  unattended-mode check (`internal/provider/unattended.go`, `UnattendedPreflight`), the plugin
  preflight (`<cli> plugin list`), the login check behind the governor (§6.5), `internal/probe` for
  tools on `PATH`, `internal/profile` for the project location, and T8.18's password file. The step
  list is `docs/install.md` steps 2 to 7.
- **Scope:** a new package `internal/install`:
  - `Facts`: the OS and, on Linux, the distribution and version (`/etc/os-release`); the architecture;
    whether running as root; whether this is a virtual machine or container (`systemd-detect-virt` on
    Linux; on macOS always "no"); whether it looks like a personal computer (a desktop session, such as
    `XDG_CURRENT_DESKTOP` or a display manager, or macOS); the package manager (`apt-get`, `brew`, or
    none); each tool afk needs (`git`, `gh`, `jq`, `ruby`, the agent CLI) and whether it was found;
    whether `~/bin` is on `PATH` and holds this afk; the agent CLI's login, unattended-mode setting
    and afk plugin; the `gh` login and whether git uses it; the git name and email; the project
    location; the web page's password and bind; whether `tailscale` is installed; and the service:
    installed, enabled, running, and (Linux) linger.
  - `Plan(Facts) Plan`: the risk (`dedicated`, `vm`, `personal`, or `unknown`, where unknown is treated
    like personal), and the steps in order: safety, packages, agent CLI, agent login, unattended mode,
    afk plugin, GitHub login, git identity, project location, web page, service, check. Each step has
    its state, the reason for it, and, when it is "cannot", what the person should do instead (for
    example "Run afk install as your normal user, not root" or "Install Homebrew from brew.sh, then run
    afk install again").
  - `Probe(Deps) Facts`: the impure side, with every command, file read and environment read injected.
    Anything that names the agent CLI goes through `internal/provider` (§2, "No vendor lock-in").
- **Must not:** change anything on the machine (this task only reads); call a command that can hang
  without a timeout; name a provider outside `internal/provider`.
- **Acceptance:** table tests of `Plan` with a row for each state of each step, and rows for the risk:
  a cloud VM, a Multipass VM, a Linux desktop, a Mac, root (every step "cannot" with the root
  message), no package manager, a fully installed machine (every step done). Table tests of `Probe`
  with fakes: a missing tool, a command that times out, an unreadable settings file. `make check` is
  green.
- Amended 2026-10-17 (T11.2 done): later tasks may assume `internal/install` holds `Build(Facts) Plan` (the type is `Plan`, the function `Build`: twelve ordered steps and the risk) and `Probe`, which reads the machine through injected seams; every command it runs has a deadline (10 seconds by default) and a failure or timeout reads as "could not tell", never "no". On a personal machine the safety step is always "to do", so the runner counts an accepted safety screen as done itself. The macOS label is `io.github.dphaener.afk`. The work started before M11 was on `main`, from a copy of this entry. See `## T11.2` in `docs/decisions.md`.

#### T11.3 — `afk install` on Linux
- **Depends on:** T11.2, T8.18
- **Goal:** on Ubuntu, as a normal user, `afk install` walks through every step T11.2 lists and ends
  with the daemon running as a service and the web page's address on screen. Run again on a finished
  machine, it says everything is in place and changes nothing.
- **Background:** the owner asked on 2026-10-17 for a guided install with "a nice install experience"
  built on the charm modules, because the `gh` login and others need prompting. The owner decided that
  the unattended-mode setting is written by afk automatically. The service is `contrib/afkd.service`
  and its `README.md` (a user unit, `KillMode=process`, `Restart=always`, a `PATH` line to fill in,
  linger). The web page's password comes from T8.18 (`afk web password`). A `bind` other machines can
  reach needs the password (§4, `[web]`).
- **Scope:**
  - `afk install [--dry-run] [--yes]`, needing a terminal (without one it says so and exits 1). It runs
    `Probe`, shows the plan as a checklist (done steps ticked), and then works through the steps that
    are to do, re-probing after each one. A step that fails stops the run with what happened and what to
    do, and leaves every earlier step in place. `--dry-run` prints the plan and changes nothing.
    `--yes` takes the default at every choice but still needs a terminal for the logins.
  - **Safety first.** A screen before anything changes: afk runs its agents with permissions bypassed,
    what that means, and that afk belongs on a machine set aside for it. On a machine whose risk is
    `personal` or `unknown`, it asks the person to type `I accept the risk`, and suggests a Multipass
    VM. Root is refused outright, with how to make a normal user.
  - **Packages**: one confirm listing the missing packages, then `sudo apt-get install -y ...` with the
    terminal handed over (bubbletea's exec of a child process), so `sudo` asks for its password itself.
    The agent CLI is installed with the command its maker documents (held in `internal/provider`).
  - **Logins**: the terminal is handed to the agent CLI's own login, then to `gh auth login`, then
    `gh auth setup-git` runs. Each is re-probed afterwards; a login left unfinished is a failed step.
  - **Unattended mode**: `internal/provider` writes `"skipDangerousModePermissionPrompt": true` into
    `~/.claude/settings.json`, keeping every other key, with an atomic same-folder rename. A file that is
    not valid JSON is not overwritten: the step fails and says how to fix the file.
  - **Plugin** through `internal/pluginstall` (the code behind `afk plugin install --target claude`).
  - **Git identity**: a form for the name and email, filled in from `gh api user` when it can be.
  - **Project location**: a form, default `~/code`, created if missing, saved with `internal/profile`'s
    `Set`.
  - **Web page**: the password through T8.18's prompt; then a choice of how it is reached: this machine
    only (`127.0.0.1:4253`), every address (`0.0.0.0:4253`, for a Multipass VM), or Tailscale (offered
    when `tailscale` is installed: runs `tailscale serve --bg 4253` after a confirm and saves the address
    it prints as `public_url`). `[web]` keys are written into `config.toml` by a text edit that keeps
    comments, as `internal/daemonconfig` already does for `[[project]]`.
  - **Service**: writes `~/.config/systemd/user/afkd.service` from the unit in `contrib/` (embedded),
    with the `Environment=PATH=` line built from where the tools were actually found, then
    `systemctl --user daemon-reload`, `enable --now afkd` and `loginctl enable-linger`. An existing unit
    file afk did not write is shown and replaced only after a confirm.
  - **Check**: `afk daemon status` and the usage reading, then the web page's address (and, for
    Tailscale, the `public_url`) in large type, and what to do next: open it and press Plan a new
    project.
  - Docs: rewrite `docs/install.md` around `afk install`, keeping the by-hand steps as a section for
    people who want them, and the README's Install list; `afk install` in `docs/reference.md`.
- **Must not:** run anything as root other than through `sudo` with the terminal handed over; install
  or change anything before the safety screen and the step's confirm (or `--yes`); put a secret in
  `config.toml`; remove or undo anything when a step fails; skip the safety screen under `--yes`.
- **Acceptance:** table tests of the step runner with fake commands: each step's success and failure,
  a failure stopping the run, re-probing, `--dry-run` changing nothing, `--yes` still showing the
  safety screen. Tests of the settings.json write: a missing file, a file with other keys (kept), a
  file that is not JSON (left alone). Model tests of the screens at 80 and 120 columns. Against a
  scratch build with scratch `HOME` and `XDG_*` folders and stand-in `apt-get`, `sudo`, `claude`,
  `gh`, `systemctl` and `loginctl` on `PATH`, driven through tmux: a fresh run ends on the address
  screen with the expected files written and commands called, and a second run reports everything
  done. `make check` is green.
- Amended 2026-10-17 (T11.3 done): `afk install` exists for Linux. Later tasks may rely on `install.Facts.RiskAccepted` (set by the runner after the safety screen), `install.Next`, `Verify`, `RenderUnit`, `ServicePATH` and `WebAddress`, `provider.InstallCommand`, `LoginCommand` and `WriteUnattended`, `daemonconfig.SetWeb`, and the unit's marker line `# Written by afk install`. It runs `afk web password` and `afk plugin install` itself with the terminal handed over, and under `--yes` the page is reached from this machine only. `docs/install.md` and `docs/reference.md` were not on `main` yet, so the task documented `afk install` in the README; the README split moved that text into `docs/reference.md` and rewrote `docs/install.md` around `afk install` the same day. See `## T11.3` in `docs/decisions.md`.

#### T11.4 — `afk install` on macOS
- **Depends on:** T11.3
- **Goal:** on a Mac with Homebrew, `afk install` does what it does on Linux, with the daemon running as
  a launchd agent.
- **Background:** the owner decided on 2026-10-17 to support macOS. A Mac is treated as a personal
  computer (T11.2), so the safety screen always asks for `I accept the risk`. The daemon's forced stop
  of a stuck session is Linux-only (it needs `/proc`); on a Mac a stuck session is reported instead
  (`docs/daemon.md`, "Linux only").
- **Scope:**
  - Packages through `brew install` (git, gh, jq, ruby). afk does not install Homebrew: with no `brew`,
    the packages step is "cannot", with the address of brew.sh and "run afk install again".
  - The service is a LaunchAgent, `~/Library/LaunchAgents/<reverse-dns id>.plist`, generated by a pure
    function: `ProgramArguments` running `afk daemon`, `RunAtLoad`, `KeepAlive` (the daemon exits on
    purpose when it finds a new version of itself), `AbandonProcessGroup` so session hosts outlive the
    daemon as `KillMode=process` does on Linux, `EnvironmentVariables` with the `PATH` built as on
    Linux, and the daemon's log in afk's state folder. Loaded with `launchctl bootstrap gui/<uid>`.
    Read `launchd.plist(5)` for each key and record what it does in `docs/decisions.md` under
    `## T11.4`; claim nothing about launchd that the man page does not say.
  - The "Running as a service" docs gain a macOS section beside `contrib/README.md`'s Linux steps, and
    `docs/install.md`'s "On macOS" section is rewritten around `afk install`.
- **Must not:** install Homebrew; run `sudo`; change the Linux path.
- **Acceptance:** table tests of the plist (keys, `PATH`, the log path) and of the macOS rows of the
  step runner. On a Mac (the T11.1 macOS runner, or the owner's Mac in T11.6): `afk install --dry-run`
  prints the plan. `make check` is green.
- Amended 2026-10-17 (T11.4 done): `afk install` works on a Mac with Homebrew. The service is the LaunchAgent `~/Library/LaunchAgents/io.github.dphaener.afk.plist` (`RunAtLoad`, `KeepAlive`, `ThrottleInterval` 30, `AbandonProcessGroup`, the `PATH` the Linux unit gets, and both output streams to `daemon.log` in afk's state folder), marked as afk's by a comment on its second line and loaded with `launchctl bootout` and `launchctl bootstrap gui/<uid>` from a desktop login. It has not run on a real Mac: no Mac was available and T11.1's macOS job was not on the branch, so `afk install --dry-run` on a Mac is an owner check (T11.6, and the open inbox item for T11.4). See `## T11.4` in `docs/decisions.md`.

#### T11.5 — The bootstrap script
- **Depends on:** T11.1
- **Goal:** `curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh` puts the
  latest release's afk in `~/bin` and starts `afk install`.
- **Background:** `afk install` cannot install afk itself, so a few lines of shell get the binary first.
  T11.1 publishes the archives and `checksums.txt`, and records whether a downloaded macOS binary needs
  `codesign --force --sign -` to run.
- **Scope:** `install.sh` at the repository root, POSIX `sh`. It refuses root; detects the OS and
  architecture (and says plainly which ones there are binaries for); finds the latest release, or the
  one named by `AFK_VERSION`; downloads the archive and `checksums.txt` with `curl`; checks the archive
  with `sha256sum` or `shasum -a 256` and stops on a mismatch; extracts into a temporary folder and
  moves `afk` into `~/bin` (making it), signing it on macOS if T11.1 found that necessary; says how to
  put `~/bin` on `PATH` when it is not; and finally runs `~/bin/afk install`, with its input taken from
  `/dev/tty` so the installer can ask its questions although the script itself arrived on a pipe.
  `AFK_NO_INSTALL=1` stops after placing the binary. A shell test in the style of the hook tests, with
  stand-in `curl`, `uname` and `afk`, added to `make check`. The README's Install section leads with
  the one-line command.
- **Must not:** use `sudo`; write anywhere but `~/bin` and a temporary folder; run a binary whose
  checksum did not match.
- **Acceptance:** the shell test covers a Linux amd64 install, a macOS arm64 install, an unknown
  architecture, a checksum mismatch (nothing written to `~/bin`), root (refused), `AFK_VERSION`, and
  `AFK_NO_INSTALL=1`; `shellcheck install.sh` is clean. `make check` is green.
- Amended 2026-10-17 (T11.5 done): `install.sh` exists at the repository root and `make check` runs its shell test (`scripts/install.test.sh`) and `shellcheck install.sh` through the `install-script-test` target (shellcheck is required when `CI` is set, skipped with a note otherwise). Later tasks may assume it finds the latest release through the `/releases/latest` redirect, takes `AFK_VERSION` with or without a `v`, replaces `~/bin/afk` by a same-folder rename, signs on macOS only when `afk version` fails, and runs `afk install` with input from `/dev/tty` (`AFK_TTY` is a test seam). No release is published yet, so the one-line command works only after the owner pushes a `v*` tag; T11.6 checks it on a VM and a Mac. See `## T11.5` in `docs/decisions.md`.

#### T11.6 — Prove it on fresh machines (owner)
- **Depends on:** T11.3, T11.4, T11.5
- **Goal:** the one-line install works for a newcomer, on a VM and on a Mac, from nothing to a first
  task merged.
- **Scope:** an owner check, done by hand:
  1. On a laptop, `multipass launch --name afk-test --cpus 2 --memory 4G --disk 30G` and
     `multipass shell afk-test`.
  2. Run the one-line command from the README, and answer `afk install`'s questions. Note anything
     that was unclear, slow or wrong.
  3. Open the web page from the laptop, press **Plan a new project**, plan something tiny (a one-page
     static site), approve it, and wait for its first task to merge.
  4. On a Mac, run the one-line command and `afk install` as far as the service; check that
     `afk daemon status` says the daemon is running and that a reboot brings it back.
  5. Answer this item with what you noted. Each problem becomes a task with `afk plan amend`.
  6. `multipass delete --purge afk-test`.
- **Acceptance:** on the VM, a first task of the new project merged with no step done by hand outside
  `afk install` and the web page; on the Mac, the daemon running after a reboot; every problem noted is
  a task in the plan.

#### T11.7 — Choose the sandbox for a Mac (owner + agent)
- **Depends on:** T11.6
- **Goal:** a recorded decision on how afk's unattended agents are sandboxed when the owner's machine is a Mac, chosen from a hands-on trial of four options, with the follow-up work named.
- **Background:** the owner asked on 2026-10-20: "Using multi pass on macOS may not have been the best choice. It works but we now have to copy all the code into the van, set everything up, etc. the web ui for afk (and for any web apps running inside the vm) aren’t reachable without some sort of bridge (currently that isn’t set up so the web ui is not reachable). Really we only need a secure sandbox for the agent to work in unattended. Let’s explore our options." The §2 rows "Where afk runs, and installing it" and "Permissions" make a machine set aside for afk (a Multipass VM on a laptop among them) the sandbox, and set Docker aside on 2026-10-17. The daemon's agents run on the Linux VM and cannot try a macOS option themselves, so the owner runs the trial on the Mac.
- **Scope:**
  1. The agent writes `docs/sandbox-trial.md`: for each option, the exact steps to set it up on a Mac with one afk project, and the same checks to run on each. The options: (a) the agent CLI's own OS sandbox on the Mac itself, with no VM, run with permissions bypassed; (b) a VM with folder sharing and port forwarding built in (Lima, and OrbStack); (c) Multipass with the gaps fixed (`multipass mount` of the project folder, the web page on every address or Tailscale, a port forward for a project's web app); (d) a container or devcontainer, reopening the option set aside on 2026-10-17. Every claim in the steps about what a tool does comes from that tool's own documentation, cited; nothing is assumed.
  2. The checks, the same for each option: what an agent can reach outside the project (the home folder, `~/.ssh`, the keychain, other projects, the network); whether the project's code needs copying or setting up again; whether afk's web page and a web app the project runs open in the Mac's browser and on the phone; whether the project's own tools (a database, a browser for system tests) run; whether a task runs unattended start to finish with permissions bypassed; whether the daemon runs as a service and survives a reboot; and the cost or licence.
  3. The owner runs the steps on the Mac and answers the inbox item with what each check showed.
  4. The agent records the results as a table under `## T11.7` in `docs/decisions.md`, the option chosen and why, and the follow-up as plain lines for the owner to start with `afk plan extend` or `afk plan amend` (for example changes to `afk install`, `docs/install.md` and the §2 rows). It adds an Amended line to the §2 row "Where afk runs, and installing it" only when the choice changes it.
- **Must not:** change `afk install`, the daemon or any code; build the chosen option (that is the follow-up); run an agent with permissions bypassed on the owner's Mac outside the trial's steps; put a secret in any file.
- **Acceptance:** `docs/sandbox-trial.md` exists with steps and checks for all four options, each tool claim cited; `docs/decisions.md` has `## T11.7` with every check's result for every option, the choice and its reason, and the follow-up lines; `make check` is green.
- Amended 2026-10-20: drop option (a), the agent CLI's own OS sandbox on the Mac with no VM; the owner said the agent breaks out of it regularly, so it is not a sandbox for unattended agents. Compare the other three options only, and say in `## T11.7` why (a) was left out.

### M12 — Releases publish themselves

**Outcome:** every merge to `main` that changes the binary publishes a GitHub release on its own, the
first one `v0.1.0`, so the one-line install in the README finds a binary to download. Nobody has to
remember to push a tag.

The owner reported on 2026-10-19 that `install.sh` fails because there are no published binaries. A
survey that day found the release workflow (T11.1) is sound but has never been started: it runs only on
a pushed `v*` tag, and no tag was ever pushed. A hand-started dry run on `main` (run 39302858785) was
green; the darwin/arm64 binary ran on the macOS runner **without** `codesign`; the version line read
`afk 0.0.0-dev.66672bd7fd3a (66672bd7fd3a+dirty, 2026-10-18)` although `main` was clean; and GitHub
warned that the workflow's actions target the deprecated Node.js 20 runtime. The owner then decided
(§2, "Releases"): a release on every merge to `main` that changes the binary, the bump read from the
commit messages, the MIT licence, and `0.1.0` as the first version.

```
T12.1 clean builds ──┐
T12.2 licence ───────┼──▶ T12.4 release on every merge
T12.3 next version ──┘
```

#### T12.1 — Clean release builds
- **Depends on:** —
- **Goal:** a release build from a clean checkout prints a version line with no `+dirty`, the release
  workflow's actions no longer run on the deprecated Node.js 20, and the macOS codesign answer T11.1
  left open is recorded.
- **Background:** the version line comes from `version.go`: `+dirty` is `vcs.modified` in
  `debug.ReadBuildInfo`, which Go sets when the work tree has changes at build time. In
  `.github/workflows/release.yml` the build job runs `make check` and then `scripts/release.sh` in the
  same checkout, so something before the build (most likely a file `make check` writes or leaves
  behind) modifies the tree. The dry run on 2026-10-19 (run 39302858785) printed
  `afk 0.0.0-dev.66672bd7fd3a (66672bd7fd3a+dirty, 2026-10-18)`, and its macOS job ran the
  darwin/arm64 binary without `codesign`.
- **Scope:**
  - Find what leaves the tree modified (reproduce locally: a fresh clone, `make check`, then
    `git status --porcelain`) and fix the cause: a file that should be ignored goes in `.gitignore`; a
    tracked file that a step rewrites stops being rewritten. Do not hide it by building with
    `-buildvcs=false` or by stripping `+dirty` in `scripts/release.sh`, because `+dirty` must stay true
    for a real hand build of a changed tree.
  - `scripts/release.sh` refuses to build when `git status --porcelain` is not empty, with a plain
    message naming the changed files, so a dirty release can never be published again.
  - Move every action in `release.yml` and `ci.yml` to the newest major version whose runtime is not
    Node.js 20 (check each action's own release notes for the runtime; claim nothing you have not read).
  - `docs/decisions.md` under `## T11.1`: the dry run's answer, that a downloaded darwin/arm64 binary
    runs without `codesign` (run 39302858785, 2026-10-19). `docs/development.md` "Making a release":
    drop the sentence telling the reader to run `codesign` if the summary says so, and say the binary
    was found to run without it.
- **Must not:** change what `make install` builds or where; change the version line's format.
- **Acceptance:** in a fresh clone of this branch, `make check` followed by `git status --porcelain`
  prints nothing, and `scripts/release.sh 0.0.0-check <new empty dir>` then builds a linux/amd64 binary
  whose `afk version` has no `+dirty`. `scripts/release.sh` run with an uncommitted change refuses and
  names the file (a shell test in `make check`). `gh workflow run release.yml --ref <this branch> -f
  dry_run=true` is green, its log shows no `+dirty` in the linux/amd64 or darwin/arm64 version lines,
  and it shows no Node.js 20 deprecation warning. `make check` is green.
- Amended 2026-10-19 (T12.1 done): the `+dirty` came from `scripts/release.sh` itself (archives written into the unignored in-tree `dist/` between builds), not from `make check`; `/dist/` is now ignored, the script builds every binary before archiving, refuses a changed tree and refuses any binary stamped `vcs.modified=true`, and `make check` runs `release-script-test`. Actions are now checkout/setup-go/setup-node/upload-artifact@v7 and download-artifact@v8. T12.4's workflow edits start from these tags, and its decide job must leave the tree clean before `scripts/release.sh` runs.

#### T12.2 — The MIT licence
- **Depends on:** —
- **Goal:** the repository carries the MIT licence, and every release archive holds it.
- **Background:** the owner chose MIT on 2026-10-19 (§2, "Releases"). The copyright holder is Darin
  Haener, the owner's name. `scripts/release.sh` already copies `LICENSE` into each archive when the
  file exists.
- **Scope:** `LICENSE` at the repository root with the standard MIT text, `Copyright (c) 2026 Darin
  Haener`. A "Licence" line in `README.md` naming MIT and linking the file. In this plan, remove
  "licence" from the Deferred line on the open-source release (the Open questions row is already
  answered).
- **Must not:** add licence headers to source files.
- **Acceptance:** `scripts/release.sh 0.0.0-check <new empty dir>` builds archives that each list
  `LICENSE` (`tar -tzf`), and the file matches the MIT text published at opensource.org. `make check`
  is green.
- Amended 2026-10-19 (T12.2 done): `LICENSE` (MIT, `Copyright (c) 2026 Darin Haener`, GitHub's layout) is at the root, so `scripts/release.sh` now puts it in every archive with no script change; T12.4 needs nothing more for the licence.

#### T12.3 — Decide the next version
- **Depends on:** —
- **Goal:** one command, given the commit range of a push to `main`, prints either `skip` (nothing in
  the binary changed) or the next version to publish, worked out from the last release tag and the
  commit messages.
- **Background:** the owner decided on 2026-10-19 (§2, "Releases"): a push that changes nothing in the
  binary makes no release; the bump comes from the commit messages, which in this repository follow
  `type(scope): Subject` (`feat(install): …`, `fix(install): …`, `docs(plan): …`). Pull requests may be
  merged with a merge commit, a squash or a rebase, so the command reads every commit in the range, not
  only the newest. T11.1 forbids a release tool such as goreleaser, so this is a small Go program.
- **Scope:** a Go program at `scripts/nextversion/` (a `package main` with its logic in a pure function
  and its table tests beside it; it lives under `scripts/` so it is not part of the afk binary).
  - The pure function takes the last release version (or none), the commit messages in the range
    (subject and body), and the paths the range changed, and returns `skip` or a version. The rules:
    - **No release yet**: `0.1.0`, whatever the commits say and whatever paths changed, so the merge
      that switches automatic releases on publishes the first one.
    - **Skip** when no changed path is in the binary. In the binary: a `.go` file outside `scripts/`,
      `go.mod`, `go.sum`, or anything under `plugins/`, `contrib/` or `internal/` (they are compiled or
      embedded in). Not: `docs/`, the root's `*.md` files, `install.sh`, anything under `scripts/` (its
      `.go` files included), `.github/`, `testdata/` at the root.
    - **Otherwise**: a breaking change (a `!` before the colon, as in `feat!:` or `feat(x)!:`, or a
      body line starting `BREAKING CHANGE:`) raises the major and zeroes the rest; else any commit whose
      type is `feat` raises the minor and zeroes the patch; else the patch goes up by one. A commit
      whose subject does not follow the form counts as a patch. Merge commits (`Merge pull request …`,
      `Merge branch …`) are ignored for the bump.
  - The program's impure side: `go run ./scripts/nextversion <from-sha> <to-sha>` finds the last tag
    matching `v<major>.<minor>.<patch>` reachable from `<to-sha>`, reads the commits and the changed
    paths of the range with `git`, and prints one line: `skip` or the version without its `v`. A
    `<from-sha>` of all zeros (a push that created the branch) means the whole history.
- **Must not:** read or write anything but git and standard output; call the network; create a tag.
- **Acceptance:** table tests of the pure function, with rows for: only docs changed (skip), a `.go`
  file changed with a `fix` (patch), a `feat` among `fix`es (minor), `feat!:` (major), a `BREAKING
  CHANGE:` body (major), no release yet (`0.1.0`), only merge commits plus a `chore` (patch), a subject
  with no type (patch), a `plugins/` file with a `docs` commit (patch: the plugin is embedded), only a
  `scripts/nextversion/` Go file changed (skip), and no release yet with only `scripts/` and `.github/`
  changed (`0.1.0`). A test
  of the git side against a scratch repository made in a temporary folder. `make check` is green.
- Amended 2026-10-19 (T12.3 done): `go run ./scripts/nextversion <from-sha> <to-sha>` prints `skip` or the version (no `v`) on stdout and exits 0; a git failure prints one line on stderr, nothing on stdout, and exits 1 (bad usage exits 2), so T12.4's `release-decide.sh` must check the exit status. The root `.claude-plugin/` and `.agents/` also count as the binary (embedded by `plugin_assets.go`), and the last release is the highest strict `vX.Y.Z` tag merged into `<to-sha>`.

#### T12.4 — Release on every merge to main
- **Depends on:** T12.1, T12.2, T12.3
- **Goal:** a push to `main` that changes the binary tags the next version and publishes a release
  with the four archives and `checksums.txt`; a push that does not publishes nothing; and a tag pushed
  by hand still publishes as it does today.
- **Background:** the owner decided on 2026-10-19 (§2, "Releases") that releases happen on every merge,
  which reverses T11.1's "never publish on a push to `main`". A tag created with the workflow's own
  `GITHUB_TOKEN` does not start another workflow run (GitHub's documented rule for that token), so the
  run that decides to release must also build and publish; it cannot push a tag and wait for the tag
  run. `install.sh` (T11.5) finds the newest release through the `/releases/latest` redirect, so once
  one exists the one-line install works with no change to it.
- **Scope:**
  - `scripts/release-decide.sh <from-sha> <to-sha>`: prints `skip` when `<to-sha>` already carries a
    `v*` tag, and otherwise what `go run ./scripts/nextversion <from-sha> <to-sha>` prints.
  - `.github/workflows/release.yml` also runs on a push to `main`. A first job checks out with full
    history and tags, runs `scripts/release-decide.sh "${{ github.event.before }}" "$GITHUB_SHA"`,
    and outputs the version or `skip`; every later job is skipped on `skip`, and the run's summary says
    why in one line. On a version, the existing build and macOS jobs run with it, and the release job
    creates `v<version>` on that commit with `gh release create --target "$GITHUB_SHA"`.
  - A hand-started dry run on any branch also runs the decide job, against the last tag and the
    branch's head, and writes the version it would publish (or `skip`) to the summary.
  - A pushed `v*` tag keeps working as today, and a push to `main` whose commit already carries a
    `v*` tag (one the owner pushed by hand) does not publish a second release.
  - `concurrency` so that two pushes to `main` in quick succession release one after the other, never
    at once and never cancelling each other.
  - `docs/development.md` "Making a release": releases are automatic on a merge that changes the
    binary, with the bump rules from T12.3; a hand tag is how to choose a version yourself (such as
    `v1.0.0`). `docs/install.md` and the README's Install section: no change unless they say a release
    may not exist yet.
- **Must not:** publish from a pull request; push to `main`; change `install.sh`; add a release tool.
- **Acceptance:** a shell test of `scripts/release-decide.sh` in `make check`, against a scratch
  repository, covering `skip` (only docs changed), a version, and a commit that already carries a `v*`
  tag (`skip`). `gh workflow run release.yml --ref <this branch> -f dry_run=true` is green and its
  summary names the version it would publish (`0.1.0`, as no release exists yet). `make check` is green.
- **Note:** the release itself happens only after this task merges, which the ship step's checks cannot
  see. Say in `docs/decisions.md` under `## T12.4` how to confirm it: `gh release list` shows `v0.1.0`
  with four archives and `checksums.txt`, and `AFK_NO_INSTALL=1` with the one-line install puts a
  working `afk` in `~/bin`.
- Amended 2026-10-19 (T12.4 done): `release.yml` now starts with a `decide` job that works out the version for every run (push to `main` via `scripts/release-decide.sh`, a pushed tag, a hand run) and outputs `version`/`publish`/`tag`; `build` is skipped on `skip` and `release` runs only on `publish == 'true'`, creating `v<version>` with `--target` on a main push. Concurrency is `release-${{ github.ref }}` with `queue: max`. The first release happens on this task's merge; nothing in `install.sh` changed.

### M13 — Fix a bug from a planning session, and planning sessions that end themselves

**Outcome:** the owner describes a bug with `afk plan fix` (or from the dashboard or the web page), a
planning session reproduces it, finds its cause and adds one fix task that the daemon builds next; and
no planning session is left running once it is finished, so the next one starts without a hand-run
`afk clean master-plan`.

The owner asked on 2026-10-19: "Add a bug fix planning mode, and a way to exit the planning mode once it
is done and execution starts so the planning session doesn't stick around and block further sessions."
A survey that day found that nothing ends a planning session: `afk plan approve` only prints "You can
close it with afk clean master-plan", the `master-plan` host and its agent stay, and the next
`afk plan <mode>` is refused with "the earlier planning session is finished, but its agent is still
running" (`planRefuseHostBusy` in `plan_session_cmd.go`, shared by the dashboard and the web page
through `startPlanForSurface`). The daemon itself is not blocked. The owner then decided (§2, "Bug
fixes" and "Planning sessions end themselves").

```
T13.1 summary file ──▶ T13.3 change session ends ──┐
T13.2 approve ends it ─────────────────────────────┼──────────────▶ T13.8 prove it
T13.4 (fix) runs next ──┬─▶ T13.5 fix mode ──┬─▶ T13.6 dashboard ─┤
          T13.3 ────────┘                    └─▶ T13.7 web page ──┘
```

#### T13.1 — Save the review summary to a file
- **Depends on:** —
- **Goal:** every planning session, of every mode, saves the summary it prints at `review` to
  `.afk/plan/summary.md` before printing it, and `afk plan status` at `review` prints that file, so the
  summary outlives the agent that wrote it.
- **Scope:**
  - The master-plan prompts (`plugins/afk/skills/master-plan/prompts/`, the late phases and the modes
    prompt): at `review`, write the summary with Write to `.afk/plan/summary.md` first, then print the
    same text. Keep the prompts under their size cap.
  - `internal/plansession`: read `summary.md` (missing means none, never an error) and expose it;
    `afk plan status` prints it under the phase line when the phase is `review` and the file is not
    blank.
  - §4.8 of this plan: a row for `summary.md` and an Amended line. The OpenCode port has no master-plan
    skill, so it needs no change.
  - `docs/reference.md` for `afk plan status`.
- **Must not:** add a gate to `review`; change `state.json`.
- **Acceptance:** table tests in `internal/plansession` for a summary that is there, missing and blank;
  a test of `afk plan status` output at `review` with and without the file; `make check` green; a built
  binary's `afk plan status` in a scratch repository at `review` prints the file.
- Amended 2026-10-19 (T13.1 done): `plansession.SummaryFile` (`summary.md`) and `Files.Summary` (the text, `""` when missing, blank or unreadable) exist; `afk plan status` prints `Summary (.afk/plan/summary.md):` and the text after `Waiting for:` only when the decided phase is `review` (pure helper `planSummaryToShow`); `afk plan advance` output is unchanged. The new-plan and change-mode `review` prompts write the file before printing, and `session.md` lists it.

#### T13.2 — Approving a plan ends its planning agent
- **Depends on:** —
- **Goal:** `afk plan approve`, and the dashboard's and the web page's approve (which share its code in
  `plan_approve_surface.go`), end the `master-plan` agent once the plan is approved, instead of telling
  the owner to run `afk clean master-plan`.
- **Scope:**
  - After `approved` is written, stop the `master-plan` host the way `afk clean master-plan` does,
    leaving `.afk/plan/` and the `planning` marker's meaning untouched (the files are archived by the
    next session, as today). A host that is already gone is fine. A failure to stop it does not undo
    the approval: say in one plain line what to run by hand.
  - The message at `plan_approve_surface.go:289` becomes "The planning session has ended." (or the
    by-hand line above).
  - A dashboard pane or web chat showing that session sees it end as it does for any ended session.
  - `docs/reference.md` (`afk plan approve`), `docs/dashboard.md`, `docs/web.md` where they say to clean.
- **Must not:** remove `.afk/plan/`; end any session other than `master-plan`.
- **Acceptance:** table tests of the approve flow with a fake host stopper covering: host running and
  stopped; host already gone; stop fails (approval stands, the by-hand line is printed). `make check`
  green. With a built binary in a scratch repository and a stand-in planning host, `afk plan approve`
  leaves no `master-plan` host running.
- Amended 2026-10-19 (T13.2 done): `session.StopHost(worktree, name, CleanDeps) (HostStop, error)` is exported from `internal/session/clean.go` (the ladder and file removal `afk clean` uses, never the session folder; `HostGone` or `HostStopped`), and `terminateCleanHost` calls it; T13.3 should stop the host with it. `approveDeps.stopPlanHost` runs it once, only on a run that ends approved, and `approveResult.PlanHost` records `ended`, `gone` or `failed`; a failure leaves the approval and exit 0 and prints one `Note:` line naming `afk clean master-plan --project <root>`. `planRefuseHostBusy` and `docs/reference.md`'s note about a finished change session's agent are unchanged for T13.3.

#### T13.3 — A finished change session's agent ends once it goes quiet
- **Depends on:** T13.1
- **Goal:** a change session (extend, amend, reconcile) at `review`, with `summary.md` saved and its
  agent waiting for the owner, has its agent ended without the owner doing anything; and `afk plan`
  never refuses because such an agent is still running.
- **Scope:**
  - A pure decision (beside `planwait`, or in `internal/plansession`): given the session's state, whether
    `summary.md` is there, the chat's state (`planchat`: waiting for text or for a choice) and whether
    the host is alive, return end or leave. End only when all of: the session is finished, the summary
    is saved, the agent is waiting, the host is alive.
  - The daemon's once-per-pass hook (beside `plan_wait_notify.go`) runs it for each project and stops
    the host the way `afk clean master-plan` does. It runs while the daemon is paused, as the wait
    notification does. Each end is a line in `.afk/log/decisions.jsonl`.
  - `decidePlanStart` (`plan_session_cmd.go`): where it would refuse with `planRefuseHostBusy` and the
    same decision says end, end the old agent and carry on starting the new session, so this works with
    no daemon. When the decision says leave (the agent is still talking), keep the refusal.
  - A finished `new` session is left to T13.2.
  - `docs/daemon.md` and `docs/reference.md`; §4.8 Amended line.
- **Must not:** end a session that is not finished, or one whose agent is mid-reply; touch `.afk/plan/`.
- **Acceptance:** a table test of the decision with every input a row, its comment naming the
  load-bearing rows (unfinished, summary missing, agent replying). Tests of the daemon hook and of
  `decidePlanStart` with fakes. `make check` green. With a built binary and a stand-in planning host at
  `review` with a summary and a waiting chat, `afk daemon --once` (scratch state home) ends the host,
  and `afk plan extend` in another scratch repository in the same state starts without the refusal.
- Amended 2026-10-19 (T13.3 done): `internal/planend.Decide(Input) Decision` (state, summary text, `planchat.State`, host alive; `planend.Rule` is the log rule) ends only a finished extend, amend or reconcile session; T13.5 should treat a finished `fix` session the same by adding it to the change modes there. The daemon's `PlanWaits` hook now runs the wait notifier and then `planEnder.check` (`plan_end_hook.go`), and `decidePlanStart` takes `OldAgentEnds` and returns `EndOldAgent`, acted on by `endFinishedPlanAgent` in `runPlanRequest` and `startPlanForSurface`; a failed stop starts nothing. Each end is a `plan_session_ended` decision line. The by-hand order is `afk clean --project <root> master-plan`; T13.2's lines still give the refused `afk clean master-plan --project` order.

#### T13.4 — Fix tasks run next
- **Depends on:** —
- **Goal:** a task whose title carries `(fix)` is chosen by the daemon ahead of every other ready task,
  and the plan format and the checker know the marker.
- **Scope:**
  - `internal/masterplan`: a `Fix` flag on a task, read from a bracketed `(fix)` in its title, matched
    the way `(owner)` is (case and spaces inside the brackets do not matter). A title may not be both;
    `afk plan lint` reports a task marked both as an error.
  - `internal/nexttask`: among ready tasks, the first fix task in file order wins; otherwise the order
    is as today. A fix task still waits for its `Depends on` and for open questions that name it.
  - `afk tasks` shows `(fix)` tasks as they are titled; no new column.
  - The master-plan format (`plugins/afk/skills/master-plan/` `plan-format.md`): the marker, its
    meaning and the lint rule. `docs/reference.md` and `docs/daemon.md`.
- **Must not:** change how owner tasks are chosen or skipped.
- **Acceptance:** table tests in `internal/masterplan` (marker forms, both markers) and
  `internal/nexttask`, the latter's comment naming the load-bearing rows (a fix task later in the file
  beats an earlier ready task; a fix task that is not ready does not); a `planlint` row for the error;
  `make check` green; `afk plan lint` on a scratch plan with a `(fix) (owner)` task exits 1.
- Amended 2026-10-19 (T13.4 done): `masterplan.Task.Fix()` reads `(fix)` or `(fix; …)` (case and inner spaces ignored, like `Owner()`); `nexttask.Next` picks the first ready fix task in file order after in-progress work and an answered parked task and before plan order, with no new `Decision` field; open-question holds stay in the daemon's `decideNext`. `afk plan lint` has a twelfth rule, the error `fix-and-owner`. T13.5's draft can title its task `<what is fixed> (fix)` and rely on this order.

#### T13.5 — The `fix` planning mode
- **Depends on:** T13.3, T13.4
- **Goal:** `afk plan fix "what is broken"` starts a planning session that reproduces the bug, finds its
  cause, shows the owner one `(fix)` task and, on a yes, adds it to the plan's standing Fixes milestone,
  where the daemon builds it next.
- **Scope:**
  - `internal/plansession`: the mode `fix`, with the phases `brief`, `survey`, `draft`, `check`,
    `review` and the gates of `amend` (`changes.md` for `draft`, no critic). It is a change session:
    it needs a plan, is finished at `review`, and is never approved. `--yes` writes `.afk/plan/yes` as
    for amend.
  - `plan_session_cmd.go`, `plan_start_surface.go`: `afk plan fix [--yes] "TEXT"` saves the text as the
    brief, as `afk plan amend` does; the bare `afk plan` mode chooser offers it.
  - A `fix` section in the master-plan modes prompt:
    - `survey`: read the brief, the code and logs it points at, and `afk tasks`; reproduce the bug with
      the project's own commands, and write to `survey.md` the steps that show it, what was seen, the
      cause (file and line) and the task whose change brought it in, if one can be told. The session
      changes no code file. When it cannot reproduce the bug it asks the owner, in plain words, for what
      is missing.
    - `draft`: one task, titled `<what is fixed> (fix)`, whose Acceptance names a test that fails today
      and passes after the fix. Its id comes from `afk plan next-id`. It goes in the milestone titled
      exactly `Fixes`: when the plan has none, the first fix creates it with
      `afk plan insert --new-milestone` (outcome: "bugs found after a task shipped are fixed"); later
      ones use `--milestone <its number>`. Show the entry and wait for a yes unless `.afk/plan/yes`
      exists. Then an Amended line on the task that caused it, when the survey named one and it is not
      running or shipping. `changes.md` lists every command.
    - `review`: as for the other change modes, `summary.md` included (T13.1).
  - §4.8 and the master-plan format: the mode, its path and the Fixes milestone. `docs/reference.md`.
  - `plugins/afk-opencode/PROVENANCE.md`: a line in the lag note for the `fix` mode.
- **Must not:** let the session edit any file but the plan (through the plan commands) and
  `.afk/plan/`; add a critic.
- **Acceptance:** table tests in `internal/plansession` for the `fix` path and its gates; a routing test
  for `afk plan fix` with and without text and `--yes`; the prompt size test passes; `make check` green.
  In a scratch repository with a plan, a built binary's `afk plan fix "x"` writes the brief and
  `state.json` with mode `fix`; with stand-in files at each phase, `afk plan advance` walks it to
  `review`.
- Amended 2026-10-19 (T13.5 done): `plansession.ModeFix` (`"fix"`) is in `Modes`, walks amend's path and gates through `changesPlan`, and `planend.Decide` ends a finished fix session. `afk plan fix [--yes] "TEXT"` routes like amend (blank text is a usage error, exit 2); the bare `afk plan` chooser offers `4) fix a bug (fix)`. `startPlanForSurface` already accepts `plansession.ModeFix` with a required text ("You did not say what is broken.") and never writes `yes`, so T13.6 and T13.7 only add their keys and forms; their own mode constants and the "extend, amend or reconcile" strings in `internal/ui`, `internal/web` and `docs/web.md` are unchanged. The fix section is in `prompts/modes.md`.

#### T13.6 — Start a bug fix from the dashboard
- **Depends on:** T13.5
- **Goal:** the leader's actions dialog offers "Fix a bug" beside extend, amend and reconcile, asks for
  the description and starts the `fix` session, held in the dashboard like the other planning sessions.
- **Scope:** `internal/ui/planstart.go` (a `PlanMode` and the form, the same shape as amend's, with its
  `--yes` choice), `dash_plan.go`, the leader's key list; `docs/dashboard.md`.
- **Acceptance:** `internal/ui` table tests for the new action and its form, in the style of the amend
  ones; the leader golden updated; `make check` green; driven through tmux against a built binary in a
  scratch project, the action starts a session whose `state.json` says `fix`.
- Amended 2026-10-19 (T13.6 done): the dashboard's actions dialog has a by-name row "fix a bug" (`actionPlanFix`, `ui.PlanModeFix = "fix"`) that opens amend's one-line form ("What is broken? Say it in a sentence.") and calls `Deps.StartPlan` with mode `fix`; there is no `--yes` choice. Its query is `a bug`, because a typed `f` runs focus and `b` build. T13.7 is unaffected.

#### T13.7 — Start a bug fix from the web page
- **Depends on:** T13.5
- **Goal:** the page's plan-change form offers "Fix a bug" with a description box, and the session it
  starts is held in the chat view like the other change modes.
- **Scope:** `internal/web/plan_change.go` and its template, built from the design system's existing
  parts with no design round; the same `Origin` check, sign-in and confirm as amend; `docs/web.md`.
- **Must not:** add a component the design system does not have.
- **Acceptance:** `internal/web` table tests for the form and the POST (accepted, empty description,
  refused origin); the page's JS tests if it changes JS; `make check` green; in headless Chrome against
  a built daemon on a scratch project, the form starts a `fix` session, the chat view opens, and axe
  reports no WCAG 2.2 AA violation on the form.
- Amended 2026-10-19 (T13.7 done): the web page's plan-change flow has a fourth mode, `fix` (`changeFix` in `internal/web/plan_change.go`): `?mode=fix` asks "What is broken?" in a text area, then amend's confirm card ("Fix a bug in {project}?"); a blank description is refused before the seam ("Nothing started: say what is broken."; the page's field error is "Say what is broken."). The project screen and the chat's empty state show a fourth row, "Fix a bug", and a fix session's chat subtitle is "Fixing a bug". The unknown-mode sentence now ends "Use extend, amend, reconcile or fix." No JS, CSS or root-package change; `docs/web.md` and `docs/reference.md` name four modes. T13.8 can start a fix from either surface.

#### T13.8 — Prove it on a real bug (owner + agent)
- **Depends on:** T13.2, T13.3, T13.6, T13.7
- **Goal:** one real bug in a project afk builds goes from `afk plan fix` to a merged fix, and no
  planning agent is left running at the end.
- **Scope:** the owner names a bug (in afk or another project) and starts the session from the
  dashboard or the web page; the agent helps run it. Record in `docs/decisions.md` under `## T13.8`:
  the bug, the fix task's id, whether it ran next, whether the planning agent ended by itself, and
  anything that went wrong. Fixes that the run shows are needed become `(fix)` tasks themselves.
- **Acceptance:** the fix task is `done` in `afk tasks`; once the summary was printed, the dashboard
  shows the `master-plan` session as ended, with no hand-run clean; `docs/decisions.md` has the
  `## T13.8` entry.
- Amended 2026-10-20 (T13.8 done): the `fix` mode was proven on a real bug: T14.1 (fix), pre-trusting folders afk creates for a new plan, was added to the new milestone M14 — Fixes, launched three seconds after its plan commit ahead of other ready work, and merged as pull request #88; the planning agent ended itself 16 seconds after its summary was saved (a `plan_session_ended` line), with no `afk clean`. The run showed no new fault, so no further `(fix)` task was added. Later tasks may rely on the fix flow end to end; the starting surface was not recorded. See `## T13.8` in `docs/decisions.md`.

### M14 — Fixes

**Outcome:** bugs found after a task shipped are fixed.

#### T14.1 — Folders afk creates for a new plan skip the agent's trust prompt (fix)
- **Depends on:** —
- **Goal:** when the dashboard's or the web page's new-plan form makes a new project folder, afk
  records that folder as trusted in the agent CLI's own settings before the planning session starts, so
  the session opens straight into the conversation and the owner never has to go to a terminal to say
  yes.
- **Background:** reproduced 2026-10-19 with Claude Code 2.1.304 (`.afk/plan/survey.md` of that fix
  session). A brand-new git repository stops the CLI before any conversation at "Quick safety check: Is
  this a project you created or one you trust?", whose default is "No, exit"; the planning chat stays
  empty and the owner must answer in `afk dash` or `afk attach`. The CLI decides from
  `projects["<folder>"].hasTrustDialogAccepted` in `$CLAUDE_CONFIG_DIR/.claude.json` (default
  `~/.claude.json`): with that set to `true` beforehand the same start opened at the input prompt, and a
  control run without it showed the question. Trust is per repository, so a new repo under a trusted
  `~/code` still asks. Cause in afk: `planfolder.Create` (internal/planfolder/planfolder.go:173), called
  by `dashCreatePlanFolder` (dash_plan.go:126) and `webCreatePlanFolderWith` (daemon_web_plan.go:260),
  makes the folder and runs `git init` and nothing records trust. This reverses T8.17 decision A1
  (afk never writes the trust record), which the owner asked for in inbox item vbkr6s.
- **Scope:**
  - `internal/provider`: a way to mark one folder trusted, implemented for the default provider and
    "not supported" (a no-op the caller ignores) for the others. It re-reads the settings file, sets
    only that one key for that one folder (the folder's absolute path, as the CLI keys it), keeps every
    other key and value as it was, and writes with a same-directory atomic rename keeping the file's
    mode. It honours `CLAUDE_CONFIG_DIR`. A file that is missing, unreadable or not valid JSON is left
    untouched and returned as an error. The file's location and key are named only here.
  - `dash_plan.go` and `daemon_web_plan.go`: after `planfolder.Create` succeeds, mark the new folder
    trusted. A failure does not stop the plan: the folder stays, the planning session starts as today,
    and the T8.17 "may be waiting at a question in its terminal" message still covers it.
  - Only folders afk itself creates. A folder the owner names that already exists (`KindUse`), and
    `afk plan` run by hand in a folder, are left as they are.
  - `docs/decisions.md`: a `## T14.1` section saying afk now pre-trusts the folders it creates, why
    (inbox vbkr6s, this fix) and what it does when the write fails, and noting T8.17 A1 is reversed for
    those folders. `docs/web.md` and `docs/dashboard.md` where they describe the new-plan form and the
    trust prompt; `docs/runbook-vm.md`'s trust row if it now reads wrong.
- **Must not:** write the settings file in any test (tests use a temp file through an injected path);
  change any key but the one trust flag; trust any folder afk did not just create; name the provider
  outside `internal/provider`.
- **Acceptance:**
  - A test that fails today and passes after the fix: a table test of the folder-creation path in
    `dash_plan.go` and `daemon_web_plan.go` (injected create and trust functions) whose rows show the
    new folder is marked trusted after `planfolder.Create` succeeds, nothing is trusted when creation
    fails or the folder already existed, and a trust failure still returns the created folder.
  - Table tests in `internal/provider` against a temp settings file: the key is added for a new folder,
    other projects and top-level keys survive byte-equal in value, an existing folder entry keeps its
    other fields, `CLAUDE_CONFIG_DIR` is honoured, and a missing or broken file is left untouched with
    an error. The test comment names the load-bearing rows.
  - `make check` green.
  - Against a built binary with a scratch `CLAUDE_CONFIG_DIR` holding a copy of the settings file:
    making a new folder through the dashboard or the web page's new-plan path leaves
    `hasTrustDialogAccepted: true` for that folder and every other key unchanged, and the agent CLI
    started there on a pty shows no "Quick safety check" screen.
- Amended 2026-10-19 (T14.1 done): `provider.TrustFolder(name, folder, provider.TrustDeps)` (internal/provider/trust.go) is the one writer of the trust flag, returning `provider.ErrTrustUnsupported` for non-default agents; it keys the folder by its symlink-resolved absolute path and may reorder the file's top-level keys. Both new-plan create paths call it through `trustNewPlanFolder` in dash_plan.go, and `webCreatePlanFolderWith` now takes a `trust` function after `gitInit`.

### M15 — Install the page as an app, with native notifications

**Outcome:** the owner installs the web page on their phones and computers as an app with its own icon,
and the things that block on them arrive as native notifications from it; ntfy is gone.

#### T15.1 — Make the app icons (owner)
- **Depends on:** —
- **Goal:** the repository holds the app icons, made by the owner, at every size the page and the
  desktop toasts need.
- **Scope:** Make these six files from the icon you designed and put them in
  `internal/web/static/icons/` in the main checkout, then commit them (`scp` them over from your laptop
  first if that is where they are):

  | File | Size | Used for |
  |---|---|---|
  | `icon-512.png` | 512×512 PNG | the app icon on Android and desktop installs, and the macOS toast icon |
  | `icon-192.png` | 192×192 PNG | the smaller app icon Android and desktop browsers ask for |
  | `icon-maskable-512.png` | 512×512 PNG, the artwork inside the middle 80% (a circle 410px across), the rest filled with the background colour | Android, which crops it to a circle or rounded square |
  | `apple-touch-icon.png` | 180×180 PNG, no transparency | iPhone and iPad home screens |
  | `favicon-32.png` | 32×32 PNG | the browser tab |
  | `favicon.svg` | any, optional | a sharp browser tab icon at every size; leave it out if you have none |

  Square, and the same artwork in each. No other file goes in the folder.
- **Acceptance:** `internal/web/static/icons/` is committed on `main` and holds the five PNG files at
  the sizes above, and `favicon.svg` if the owner made one.

#### T15.2 — Install the page as an app
- **Depends on:** T15.1
- **Goal:** the web page can be installed on a phone or a computer as an app that opens with the
  owner's icon, the browser tab shows the icon, and macOS desktop toasts use it in place of the pickaxe.
- **Background:** the page has no favicon, manifest, icon or service worker today, and `layout.html`
  holds only the charset, the viewport and the two stylesheets. `internal/web/static.go` serves only
  `.js` and `.css`, under `/static/<hash>/<file>`, and the sign-in guard (`auth.go`) answers every path
  but `/signin` with the sign-in form. Browsers fetch a manifest and the iPhone home-screen icon
  without the sign-in cookie, and a service worker's scope is limited to the folder it is served from,
  so a worker under `/static/<hash>/` could not control the pages.
- **Scope:**
  - Serve the icons from T15.1 (add `.png`, `.svg` and the manifest's type to the served types).
  - A web app manifest: the name `afk`, `start_url` and `scope` `/`, `display` `standalone`, the
    icons (with `purpose` `maskable` on the maskable one), and `theme_color` and `background_color`
    taken from `docs/design-system.md`'s tokens.
  - `layout.html`: the favicon (`favicon.svg` when present, `favicon-32.png` otherwise), the manifest,
    `apple-touch-icon`, `theme-color`, and the iPhone standalone tags.
  - A service worker served at `/sw.js`, registered by the page's own JavaScript, that does nothing
    yet but exist (no caching of pages: every page is live data). T15.5 gives it its push handler.
    Update the `Content-Security-Policy` to allow it.
  - The manifest, the icons and `/sw.js` are served without sign-in, because they hold no data and
    the browser fetches them without the cookie; every other path stays guarded as today.
  - macOS desktop toasts (`internal/notify`) use `icon-512.png` in place of `pickaxe.png`, which is
    removed. How the toast code gets the bytes (an embedded copy or an exported variable) is this
    task's choice, recorded in `docs/decisions.md` under `## T15.2`.
  - `docs/web.md`: a section on installing the page on a phone and a computer, with the iPhone steps
    (Share, Add to Home Screen) and that installing needs an `https://` address.
- **Must not:** cache pages or API answers in the service worker; make any path that needs sign-in
  today reachable without it, beyond the manifest, the icons and `/sw.js` (the sign-in form's two
  stylesheets stay served without sign-in, as T8.5 left them).
- **Acceptance:**
  - Table tests in `internal/web`: the manifest, each icon and `/sw.js` answer 200 with the right
    content type with no cookie; a page and `/api/...` still answer the sign-in form and 401 with no
    cookie; the manifest parses as JSON and lists every icon that exists in `static/icons/`.
  - `make check` green.
  - Against a built binary with a scratch password: headless Chrome loads the overview, reports the
    page installable (the manifest has no errors) and shows the favicon; the overview still passes
    axe WCAG 2.2 AA at 390 and 1280 px.
- Amended 2026-10-21 (T15.2 done): the worker is `internal/web/static/sw.js`, served at `/sw.js` (scope `/`) by `serviceWorker` in `internal/web/manifest.go` with `Cache-Control: no-store`; it has only install and activate listeners, so T15.5 adds its `push` and `notificationclick` handlers there. Signed-in (non-Bare) pages register it through `static/sw-register.js`. The manifest is built per request at `/manifest.webmanifest` from `static/icons/`, which is served at hashed `/static/<hash>/icons/<file>` addresses (the static route takes a nested name). `isPublicAppFile` in `auth.go` is the only new sign-in exemption (GET and HEAD, exact paths); the CSP gained `img-src`, `manifest-src` and `worker-src 'self'`. A new page script must be added to `ownScript` in `pages_test.go`. `internal/notify` keeps its own `icon-512.png` copy, and `TestAppIconMatchesWebCopy` fails if it drifts from the web one.
- Amended 2026-10-21: the manifest's colours change to `#1C1B18` in T15.10, because `docs/design-system.md` (App icon) gives the icon's ink ground and colour is the design system's call; the `theme-color` tags stay.

#### T15.3 — Design the Notifications page (owner)
- **Depends on:** —
- **Goal:** `docs/design-system.md` defines the Notifications page, so T15.5 and T15.6 build it from
  the design system and not from a guess.
- **Scope:** Open the afk design system in Claude Design (From the owner profile, a default: design
  tool), give it the prompt below, save the result over `docs/design-system.md` and commit it. Then run
  `afk plan reconcile docs/design-system.md` and see that session through.

  The prompt: "Add one screen to the afk web page's design system: **Notifications**, a page for the
  whole machine (not one project), reached from the desktop top bar and the phone bar. Build it from
  the existing tokens and components; add a component only where none fits, and name it as it should
  be named in code. The page holds: (1) this device's state, on or off, with a button to turn
  notifications on or off for this device, and a **Send test** button when it is on; (2) a list of
  every device signed up for notifications, each row showing its browser and system and when it
  signed up, the current device marked, with a **Remove** button behind a confirm; (3) a note shown
  when the page is opened over plain `http://`, saying notifications need an `https://` address and
  linking to the setting in the docs; (4) a note shown in iPhone or iPad Safari before the page is
  added to the home screen, with the steps (Share, then Add to Home Screen, then open afk from the home
  screen); (5) the state when the browser has blocked notifications, saying how to allow them again;
  (6) the empty list. Phone first, works on desktop, WCAG 2.2 AA, both colour schemes. Hand back the
  whole design system with this screen added."
- **Acceptance:** `docs/design-system.md` on `main` defines the Notifications screen and every state
  listed in the prompt, and the reconcile session has finished.

#### T15.4 — The push sender and its key
- **Depends on:** —
- **Goal:** afk can send an encrypted web push message to a signed-up device, with a key it makes for
  itself and a list of devices it keeps, all tested without a real push service.
- **Background:** web push is three standards: the push protocol (RFC 8030), message encryption
  (RFC 8291, `aes128gcm` from RFC 8188) and the server's identity key, VAPID (RFC 8292, an ES256 JWT).
  Go 1.25's standard library has what they need (`crypto/ecdh`, `crypto/ecdsa`, `crypto/hkdf`,
  `crypto/aes`, `crypto/cipher`). The page's technology row in §2 adds no Go dependency for live
  updates; hold push to the same unless `docs/decisions.md` says why one is worth it.
- **Scope:**
  - A package (`internal/webpush`) that: makes the VAPID key pair the first time it is needed and
    stores it in `push-key.json` in afk's state folder (mode 0600, temp file and rename, never in a
    settings file, never logged); encrypts a payload for one subscription; and sends it with the
    `TTL`, `Urgency` and `Authorization` headers, returning whether the device is gone (404 or 410)
    apart from any other failure. The HTTP client and the clock are injected.
  - The payload is JSON: `title`, `body`, `url` (a path on the page, never a full address and never a
    password or cookie) and `tag`.
  - The device list, `push-devices.json` in afk's state folder (through `internal/daemonstate` or the
    new package, atomic rename, mode 0600): one entry per subscription with an id, the endpoint and
    its keys, a label (browser and system, from the user agent) and when it signed up. Add, remove by
    id, remove by endpoint, list. A missing or damaged file reads as an empty list.
  - §4 of this plan: an Amended paragraph for `push-key.json` and `push-devices.json` (writers,
    readers, shape), in the same change.
- **Must not:** reach a real push service in any test; write outside a temp state folder in tests.
- **Acceptance:**
  - A test decrypts the package's output with the receiver's side of RFC 8291 and gets the payload
    back, and a test reproduces the RFC 8291 Appendix A example from its fixed keys and salt.
  - Table tests against an `httptest` push service: the headers are sent, the VAPID JWT verifies with
    the public key and names the endpoint's origin as audience, 201 is success, 404 and 410 report the
    device gone, 429 and 5xx report a failure that is not gone. The test comment names the load-bearing
    rows.
  - Table tests for the key (made once, reused, a damaged file is an error naming the file) and the
    device list (add, remove, missing and damaged files).
  - `make check` green.
- Amended 2026-10-20 (T15.4 done): `internal/webpush` holds it all, with no new Go dependency. Later tasks call `LoadOrCreateKey(stateDir, crypto/rand.Reader, now)` (its `PublicKey()` is the page's `applicationServerKey`), `Sender{Key, Subject}.Send(ctx, sub, Payload{Title, Body, URL, Tag}, Options{TTL, Urgency})`, which returns `gone` true with a nil error for 404 and 410 and an error for any other failure, and `AddDevice`, `RemoveDevice`, `RemoveEndpoint`, `ReadDevices` and `Label(userAgent)` for `push-devices.json`. `Sender.Subject` defaults to afk's repository address; T15.7 may pass `public_url`. A second sign-up from the same endpoint replaces its entry and keeps its id. Payload `url` must be a path; a full address is refused.

#### T15.5 — Turn notifications on for this device
- **Depends on:** T15.2, T15.3, T15.4
- **Goal:** on the Notifications page, the owner turns notifications on or off for the device they are
  using and presses Send test to get a real native notification, on any browser that supports web push.
- **Scope:**
  - The Notifications page as `docs/design-system.md` draws it, at a machine-wide path, linked from the
    desktop top bar and the phone bar. This task builds this device's state, the on/off button, Send
    test, the `http://` note (shown when the page is not a secure context), the iPhone note (shown when
    the browser has no push until the page is added to the home screen) and the blocked state. The
    device list is T15.6.
  - From the design system: the links are the bell `icon-btn` "Notifications" in the Overview
    `app-bar` (beside Add a project) and the Notifications `topnav__link` after Usage. The This device
    card uses `badge--on`, `badge--off` and `badge--blocked`, `notice__steps` for the iPhone and
    blocked steps and `inline-icon` for Safari's Share glyph, and it is an `aria-live="polite"` region.
    The state shown is decided in this order: not a secure context (the `http://` note), then iPhone or
    iPad Safari not opened from the home screen (the iPhone note), then a browser with no web push at
    all (unsupported), then blocked, then on or off. The blocked state has **Check again**, which
    re-reads the permission. The design does not draw the unsupported and no-JavaScript states yet
    (`docs/design-followup-3.md`); until it does, each is a `notice--info` with one plain sentence.
  - Turning on: the page's JavaScript asks for permission, subscribes with the VAPID public key and
    posts the subscription to a guarded endpoint (sign-in and the `Origin` check, as every POST) that
    adds it to the device list. Turning off unsubscribes and removes it. Send test sends one push to
    this device only.
  - The service worker from T15.2 shows the notification from the payload and, when it is tapped,
    focuses an open afk window or opens one at the payload's `url`.
  - With JavaScript off the page says notifications need JavaScript; nothing else on the site changes.
  - `docs/web.md`: the Notifications page, which browsers and systems support it (iPhone and iPad need
    the page added to the home screen first), and that it needs an `https://` address (`public_url`
    in `docs/configuration.md`).
- **Acceptance:**
  - Go table tests: subscribing adds a device, unsubscribing removes it, Send test sends to that
    device only (through a fake sender), and a POST without a cookie or with a foreign `Origin` is
    refused.
  - Node tests in `internal/web/jstest` for the pure parts of the page's script: which state it shows
    (unsupported, needs https, needs the home screen, blocked, off, on) from the browser's answers.
  - `make check` green.
  - Against a built binary served over `https://` (the owner's address or a local test certificate):
    headless Chrome subscribes, Send test reaches a real push service and the service worker shows the
    notification; the page passes axe WCAG 2.2 AA at 390 and 1280 px in both schemes.
- Amended 2026-10-21 (T15.5 done): the page is `/notifications` (`internal/web/push.go`, template `notifications.html`, script `static/notifications.js`), on only when `Deps.PushDir` and `Deps.PushSend` are both set (`daemonWebDeps` sets them); the bell and top-bar link are drawn only then (`chrome.Notifications`). The JSON-only `POST /api/push/subscribe`, `/unsubscribe` and `/test` are registered straight on the mux (`TestAuthPushRoutesNeedACookie`) and match a device by its endpoint, so T15.6 marks this device by comparing the browser's endpoint and should add its list to `GET /api/notifications`. T15.6's list goes in the empty `#push-devices` column; the script's data hooks are documented at the top of `notifications.js`. The worker (`sw.js`) shows the payload with afk's icon (filled in when `/sw.js` is served) and focuses or opens the payload's path on a tap, so T15.7 only needs to send. A Send test answered 404 or 410 drops the browser's subscription; a subscription the server does not list still shows On at page load, which T15.6 can fix from its list.

#### T15.6 — See and remove devices
- **Depends on:** T15.5
- **Goal:** the Notifications page lists every device signed up for notifications, and the owner can
  remove any of them, so an old phone or laptop stops getting them.
- **Scope:** the device list on the Notifications page as `docs/design-system.md` draws it: each
  device's label and when it signed up, the current device marked, and Remove behind a confirm (with
  the no-JavaScript confirm page the other actions have). Removing deletes the entry from
  `push-devices.json`; a removed device that is open later shows itself as off. The JSON API gets the
  list and the remove. `docs/web.md` says how to remove a device.
  - From the design system: this device first, marked with `badge--this-device`, then the others
    newest sign-up first. Each row is a `div`, not a link, with a **Remove** `btn--quiet` whose
    `aria-label` names the device ("Remove Firefox on Windows"). Removing this device is the same as
    Turn off (T15.5), and its confirm says "This device stops getting notifications." An empty list is
    the `empty` "No devices signed up".
  - The list updates by itself through the page's live updates (the owner's choice, reconcile of
    2026-10-21): a device that signs up or is removed elsewhere while the page is open appears or goes
    without a reload, and a new one arrives with `is-changed`.
- **Acceptance:**
  - Table tests: the list shows every entry, this device first and the rest newest first, the current
    device is marked, remove deletes only that entry, removing an id that is gone is not an error, the
    confirm page works with JavaScript off, and adding or removing a device sends a live update.
  - `make check` green.
  - Against a built binary with two devices in a scratch `push-devices.json`: the list shows both,
    removing one leaves the other, and the page passes axe WCAG 2.2 AA at 390 and 1280 px.

- Amended 2026-10-21 (T15.6 done): the list is `NotificationsView.Devices` (`devices` in `GET /api/notifications`: `id`, `label`, `at`, `signed_up`, `this_device`), ordered by `deviceViews` in `internal/web/push.go`. The server knows this device by the `afk_push_device` cookie (the device id only, never the endpoint), set by `POST /api/push/subscribe` and the new JSON-only `POST /api/push/device` lookup (`{endpoint}` in, `{listed,id}` or `{listed:false}` out) and cleared by unsubscribe, a not-listed lookup and removing that id. Remove is `POST /notifications/remove` (with the `/api` twin, `confirm=yes` and `id`; a gone id answers `removed:false`, not an error) and the no-JavaScript confirm page `GET /notifications/remove?id=`. The list is the machine-wide live part `devices` (`partDevices` in `live.go`), so a device T15.7 removes as gone leaves an open page by itself; a later machine-wide part must bump `machineParts` there. `live.js` regions with `data-live-rows` flash only rows whose `data-row-id` is new.

#### T15.7 — The daemon notifies by push
- **Depends on:** T15.4, T15.5
- **Goal:** the things that block on the owner arrive on every signed-up device as a native
  notification that opens the right page when tapped.
- **Background:** ntfy is sent today from `afk inbox add` (`inbox_cmd.go`), the daemon's item, idle and
  urgent notices (`daemon_cmd.go`, `daemonNotifier`), the planning wait (`plan_wait_notify.go`, with
  the decision in `internal/planwait` and its record in `plan-waits.json`) and the plan-ready notice
  (`plan_review_notify.go`). The owner chose (M15) to be notified only of what blocks on them.
- **Scope:**
  - Push is sent, to every device in the list, for: an inbox item that blocks a task (the items ntfy
    sends for today, by the same rule as `NtfyPriority`), the urgent notices (logged out, extra usage
    on, the daemon stopped), and a planning session waiting for the owner. The title and body are the
    ones ntfy sends today; `url` is the path `ItemLink`, `ChatLink` or `PlanLink` builds, without
    `public_url`; urgent notices go with `Urgency: high`.
  - The planning wait keeps its record in `plan-waits.json` exactly as today: written only once a send
    to at least one device went out.
  - A device the push service reports gone is removed from the list; any other failure is logged and
    the device kept.
  - With no device signed up nothing is sent and nothing fails.
  - This task sends push beside ntfy; T15.8 removes ntfy.
  - `docs/reference.md` (push notifications), `docs/daemon.md` and `docs/web.md`: what is sent, when,
    and where tapping it goes.
- **Acceptance:**
  - Table tests, with a fake sender, of each send site: which events push and which do not (a
    non-blocking item, idle and plan-ready do not), the url each one carries, the urgency, the
    gone-device removal, and the plan-wait record written only after a send went out. The test comment
    names the load-bearing rows.
  - `make check` green.
  - Against a built binary with one real subscription from T15.5: `afk inbox add` of a blocking item
    reaches the device, and tapping it opens that item.
- Amended 2026-10-21 (T15.7 done): push goes through `pushTarget.deliver` in `push_notify.go` (every device, a gone one removed with `webpush.RemoveEndpoint`, other failures logged by label, never by endpoint), called from `afk inbox add` (`inboxPush`), `daemonNotifier.item` and `.urgent` (`daemon_cmd.go`, urgent with `Urgency: high`) and `planWaitNotifier.check`. Only blocking items push (not every item `NtfyPriority` sends; the urgent hook always pushes), so T15.8 keeps `NtfyPriority` only for ntfy's own wording if anything. Links are paths from `notify.ItemPath`, `ChatPath`, `PlanPath` (on `ProjectPath`), which the `…Link` builders now wrap; tags are `afk-item-<id>` and `afk-plan-<project>`. A planning wait is recorded once ntfy or at least one device got it, and push no longer needs `ntfy_url`, so T15.8 can drop the ntfy branch of `check` and keep the push count as the record rule. `afk inbox add` for a project not in `config.toml` does not push. See `## T15.7` in `docs/decisions.md`.

#### T15.8 — Remove ntfy
- **Depends on:** T15.7
- **Goal:** ntfy is gone from afk: its code, its setting and its docs; push is the only phone
  notification, and the idle and plan-ready notices are no longer sent.
- **Scope:**
  - Remove the ntfy sender and every call to it, the idle and plan-ready notices with them. Keep what
    push still uses (the link builders, `InboxMessage`, the priority rule), renamed where the name says
    ntfy.
  - `[notify] ntfy_url` in `afk.toml` or `afk.local.toml` is ignored without a word (the owner's
    choice, M15): a file that still has it loads as before and nothing is logged.
  - macOS desktop toasts (`afk notify`) stay.
  - Docs: remove ntfy from `docs/configuration.md`, `docs/reference.md` (the degradation matrix
    included), `docs/web.md`, `docs/daemon.md`, `docs/install.md` and `docs/development.md`; this plan's
    §4 `[notify]` entry gets an Amended line saying `ntfy_url` is no longer read; `docs/decisions.md`
    gets a `## T15.8` section.
- **Must not:** fail to load a project whose settings still hold `ntfy_url`.
- **Acceptance:**
  - `/usr/bin/grep -rni ntfy --include='*.go' --exclude='*_test.go' .` (outside `.claude/worktrees/`)
    finds nothing; tests may still name `ntfy_url` to prove it is ignored.
  - A table test: a project config holding `[notify] ntfy_url` loads with no error and no log line.
  - `make check` green.
  - Against a built binary: a blocking `afk inbox add` sends push and makes no request to the old
    `ntfy_url` (pointed at a local listener that records requests).

- Amended 2026-10-21 (T15.8 done): ntfy is gone. Push messages are `notify.Message` (title and body only) built by `notify.InboxMessage` in `internal/notify/message.go`; which inbox items notify is `notify.ItemNotifies(kind, blocking)`; links are the `…Path` builders (the `…Link` builders and `public_url` in notifications are gone). The plan-ready notice is gone (`recordPlanReady` only writes the review list), the idle notice and `internal/daemon`'s `NotifyIdle` are gone, and `afk inbox add` no longer reads `afk.toml`. A planning wait is recorded once at least one device got it. The non-blocking "afk cannot run this project" item now reaches the inbox only. See `## T15.8` in `docs/decisions.md`.

#### T15.9 — Prove it on phones and computers (owner + agent)
- **Depends on:** T15.6, T15.8, T15.10
- **Goal:** the owner has the app installed and getting real notifications on every kind of device
  they use, and what each one did is written down.
- **Scope:** the session for this task waits for the owner (an inbox item). The owner, at their
  `https://` address: installs the page on an iPhone (Add to Home Screen), on an Android phone if they
  have one, and on a computer (Chrome, Edge or Safari on the Mac); turns notifications on in each and
  presses Send test; removes one device from another; then has the daemon raise a blocking inbox item
  and a planning wait and taps each notification. The session writes up in `docs/decisions.md` under
  `## T15.9`: each device and browser, whether the icon, installing, Send test, a real notification and
  tapping it worked, and every fault met with its cause. It fixes what it can within M15's code and
  files the rest in the inbox.
- **Acceptance:** the write-up covers at least an iPhone and one computer, each with a real
  notification received and tapped; every fault is fixed or filed.
- Amended 2026-10-21 (T15.9 done): the owner found no fault on an iPhone (installed from Safari) or Chrome on a Mac: install, Send test, a real notification and its tap, the device list, a blocking item and a planning wait all worked, so no code changed. No Android phone was tested. The device list comes from the T15.5 answer, and the owner can correct it in inbox item h5hxfs. See `## T15.9` in `docs/decisions.md`.

#### T15.10 — The installed app's colours from the design system
- **Depends on:** T15.2
- **Goal:** the installed app's splash screen and title bar use the icon's ink ground, as
  `docs/design-system.md` (App icon) gives it, in place of the paper colour T15.2 chose.
- **Background:** T15.2 set the manifest's `theme_color` and `background_color` to `#F6F5F1` (`appColor`
  in `internal/web/manifest.go`, `docs/decisions.md` `## T15.2` A7). The design system's App icon section
  gives both as `#1C1B18`, and colour is the design system's call (the reconcile of 2026-10-21, the
  owner's choice).
- **Scope:** `theme_color` and `background_color` in the manifest become `#1C1B18`, with `appColor`'s
  comment saying why (the icon's ground, from the design system). The two per-scheme `theme-color` tags
  in `layout.html` stay as they are. `docs/decisions.md` gets a `## T15.10` section saying it replaces
  T15.2's A7.
- **Must not:** change the `theme-color` tags in `layout.html`, the icons or anything else the manifest
  holds.
- **Acceptance:**
  - A table test in `internal/web` reads the manifest and finds `theme_color` and `background_color`
    both `#1C1B18`.
  - `make check` green.
  - Against a built binary with a scratch password: `curl` of `/manifest.webmanifest` shows both colours
    as `#1C1B18`, and headless Chrome still reports the page installable.
- Amended 2026-10-21 (T15.10 done): `appColor` in `internal/web/manifest.go` is `#1C1B18` and feeds both manifest colours; `TestManifest`'s field table pins them. The `theme-color` tags in `layout.html` still give `#F6F5F1` (light) and `#141412` (dark). Nothing else changed for later tasks; T15.9 can check the dark splash and title bar on devices. See `## T15.10` in `docs/decisions.md`.

### M16 — The dashboard, redesigned

**Outcome:** `afk dash` opens on every project at once, goes into a project and then into an agent with
`enter` and back out with `esc`, gives the agent nearly the whole screen, and wears a theme the owner
picks. Keys work alone wherever no agent is on screen. The look and every key are in
`docs/dash-design.md`, which wins on looks; this milestone wins on behaviour.

Every task in M16 edits `internal/ui`, and tasks in one Go package collide at merge, so they run one
after another. Each regenerates the saved screens (`internal/ui/testdata/golden/`) with the tests'
update flag and rewrites the parts of `docs/dashboard.md` it changes.

#### T16.1 — Themes, the drawing parts and dimming
- **Depends on:** —
- **Goal:** the dashboard paints the theme the owner picks, draws today's screens with the parts in
  `docs/dash-design.md` §3, and dims whatever is under an overlay. No key and no layout changes yet.
- **Background:** colours come from `internal/palette` (`Scheme`, `For(isDark)`), built into a
  `styleSet` in `internal/ui/styles.go` and swapped by `applyTheme` when the terminal answers the
  background-colour request. Today the dashboard leaves the terminal's own background showing. Overlays
  are laid over the frame by `composeModal` (`internal/ui/modal.go`) with lipgloss v2's `Compositor`;
  drawing a layer straight onto a `Canvas` ignores its X and Y. A prototype on 2026-10-21 (lipgloss
  v2.0.6) dimmed a frame by reading every cell with `Canvas.CellAt`, blending its colours toward the
  background and writing it back with `SetCell`, then laid a box on top with the `Compositor`. It
  worked, and cells with no colour of their own were left untouched, which is why the scope below
  treats them as `fg` on `bg` first.
- **Scope:**
  - The tokens and the four themes of `docs/dash-design.md` §1 replace the palette's dark and light
    schemes for the dashboard (`internal/prompt` keeps what it uses). `tide` is the default on a dark
    terminal, `paper` on a light one.
  - `theme = "<name>"` in the `[dash]` table of `config.toml`: a `Theme` field on `daemonconfig.Dash`;
    a value of the wrong kind lands in `DashErr`, and an unknown name is checked by a pure function in
    `internal/ui` and reported in the toast the folder search already raises for `[dash]` mistakes,
    with the default theme used. Read once at start, like `leader`. Amend §4.6 and
    `docs/configuration.md`.
  - The parts of §3 as functions: top bar, status bar, pane with its title strip (with and without the
    keyboard), section, read-only pane, rows with the selection fill and the linked row, group heading,
    the glyph and word for a state (one function, §3.6), progress bar, band, key chip and alert bar.
  - Dimming (§3.9) as one function over a rendered frame, then `composeModal` lays the overlay on top.
    Every dialog, form and overlay the dashboard has today dims what is under it.
  - Today's screens use the parts: the header becomes the top bar (keeping `headerPieces` and its
    narrowing order), the footer the status bar, the two panes get title strips, the session list's
    selection is filled edge to edge, and states use the glyphs and words of §3.6.
- **Must not:** change any key, what any screen shows, or the header's narrowing order.
- **Acceptance:**
  - Table tests: the theme check (each theme, unset, unknown, wrong kind); the state-to-glyph mapping
    (every phase, fault and project state a row); dimming (a coloured cell blended 70%, a default-colour
    cell blended as `fg` on `bg`, bold cleared, a 16-colour profile setting faint instead).
  - The saved screens are regenerated and show the new parts.
  - Against a scratch build in tmux at 120×40: `tmux capture-pane -e` shows the `tide` background on an
    empty cell, the session list's selection filled, and an open dialog over dimmed cells;
    `theme = "paper"` in a scratch `config.toml` shows `paper`'s background; `theme = "nope"` raises
    the toast and draws `tide`.
  - `make check` is green.
Amended 2026-10-21 (T16.1 done): the dashboard's colours come from `palette.Theme` tokens (`internal/palette/theme.go`); the §3 parts are `styleSet` methods in `internal/ui/parts.go` (`topBar`, `statusBar`, `pane`, `row`, `stateLook`, …, with `section`, `readOnlyPane`, `groupHeading`, `progressBar`, `band`, `keyChip` and `alertBar` built but not yet on a screen); a pane's title strip is its first inner row, so pane bodies are one row shorter; `composeModal` takes the colour profile and dims, and `composeModalOver` keeps a dialog over an overlay to one dim; a selected row is drawn by `row`, every word `sel-fg`; `afk clean` keeps its `Scheme` colours, so its scratch-badge drift test now checks only the word.

#### T16.2 — Three levels, and keys that work alone
- **Depends on:** T16.1
- **Goal:** the dashboard is the three levels of `docs/dash-design.md` §5 with `enter` and `esc`
  between them; keys work alone at All projects and Project; in the agent, the leader and then the same
  key; the leader alone shows the key strip; `:` searches actions; `?` shows the keys.
- **Background:** today every action is a row in `leaderTable` (`internal/ui/leader.go`) and runs only
  after the leader; the dialog that opens at once on the leader runs a key the moment it is pressed,
  which is why "approve" is typed and `a` is kept free (`docs/decisions.md`, `## T7.2` and the T7.3
  correction about `reconcile`). Focus moves between the session list and a right side that shows the
  agent or the overview (`focusrequest.go`, `ctrl+] t`, `ctrl+] \`). The all-projects screen
  (`allprojects.go`) replaces the list while it is open. The Decisions row "Dashboard levels and keys"
  replaces M7's leader-only rule; the leader key itself (M9) and its setting stay.
- **Scope:**
  - A level stack (All projects, Project, Agent) and overlays above it, with the move from one to the
    next decided by a pure function of the level, the open overlay and the key.
  - One key table replaces `leaderTable`: each action, its key, the levels it works at, its help line.
    At All projects and Project a key runs its action. In the Agent every key goes to the agent but the
    leader; the leader shows the key strip (§4.8), the next key runs that action, and the leader twice
    sends one to the agent as today.
  - All projects is today's all-projects screen; Project is today's session list with the overview
    beside it; Agent is the session's pane over the dimmed Project at the width of §3.8. (T16.3 to
    T16.5 redraw each.)
  - `:` is the actions search of §4.7 (letters always type; rows with a key show it), `?` the keys
    overlay of §4.9, both built from the key table. Every key `docs/dash-design.md` §5 lists as replaced
    is gone, and nothing answers it with a "moved" notice.
  - Where the dashboard starts (§5, last paragraph), for a plain start, `--project` and `--open`.
  - `docs/dashboard.md`'s keys section and `docs/reference.md` for `--project` and `--open`.
- **Must not:** let any key but the leader reach afk while the agent has the keyboard; change the
  leader setting, its fallback or the pass-through; change `afk attach`.
- **Acceptance:**
  - A table test of the level function with a row for every key in §5 at every level. The load-bearing
    rows: a letter and `esc` in the Agent go to the agent; the leader then `esc` goes back to Project;
    the leader then an unlisted key closes the strip and sends nothing.
  - A test that every action in the key table has a help line and appears in `?` and `:`.
  - Against a scratch build in tmux: from All projects, `enter enter` reaches a stand-in agent,
    `q` typed there reaches the agent, `C-] Escape` returns to Project, `Escape` to All projects, and
    `q` quits.
  - `make check` is green.

Amended 2026-10-21 (T16.2 done): the level is worked out by `currentLevel(m.levelFacts())` (`internal/ui/levels.go`) and keys by the pure `decideKey`; `leaderTable` is gone and every action is a `keyTable` row (`internal/ui/keytable.go`, filled in `init()`), with `:` (actions search) and `?` (`keysoverlay.go`) built from it; Project is still the session list plus overview (`d` focuses the overview), `/` opens the project switcher, `]`, `[`, `a` and `p` at All projects are unbound, and `s`/`t` work on the overview's Build tab until T16.4; the Agent box is `agentBoxRect`/`agentContentDims` (one column in each side from 100 columns), drawn by `composeAgent`; resize mode, the full help, the session filter and the agent/overview toggle are removed (`Deps.RememberSidebarPct` is unused but still wired); the start level is `Deps.StartLevel` from `dashStartLevel` in `dash.go`.

#### T16.3 — The home screen
- **Depends on:** T16.2
- **Goal:** All projects is the screen of `docs/dash-design.md` §4.1, except the activity column and
  the week chart, which T16.8 adds.
- **Background:** the rows, state words and sentences come from `internal/projectrows`, as the
  all-projects screen reads them today, and the order "needs you first" is already worked out there.
- **Scope:** the band, the column heads, the four groups, the rows (two lines below 100 columns), the
  selected project's panel (what it waits on, the question in full, what the answer unblocks, from the
  inbox and the ledger), and the keys `a`, `p`, `n`, `+`, `-` at this level.
- **Must not:** read a file the all-projects screen does not read today, or block on a project that is
  slow to read.
- **Acceptance:** table tests of the grouping and of the selected panel's lines (a parked task, a plan to
  approve, a gone project, nothing waiting); saved screens at 120×40 and 80×24; against a scratch
  build with a scratch `config.toml` listing three throwaway projects, the groups and the panel match
  their files; `make check` is green.

Amended 2026-10-21 (T16.3 done): `projectrows.GroupOf`/`Grouped` give the four groups and `Row.Wait` carries the oldest open blocking inbox item and its task's title, filled by `readDashProjectRow` from the reads it already made (the web ignores it); `m.allProjectsRows` is stored in screen order (`allProjectsOrder`), the pane's rows are shared out by `allProjectsLayoutFor`, the panel is the pure `homePanelLines`, and the chips are `allProjectsChipList`; `p` is one key-table row at All projects and Project (`reasonPauseToggle`, `pauseToggle` reads the selected row there), and `a` (`actionAnswerOldest`, All projects only) switches to the project with the oldest blocking item and opens today's inbox overlay on it through `inboxFollow`, which T16.7 replaces with the answer screen and extends to Project and the Agent. No LAST 24H column or week chart yet (T16.8). See `## T16.3` in `docs/decisions.md`.

#### T16.4 — The project view
- **Depends on:** T16.3
- **Goal:** Project is the screen of `docs/dash-design.md` §4.2: three numbered sections, the selected
  session's read-only pane, and the selected session's task linked in Build.
- **Background:** today's Build tab (`buildtab.go`, `buildcursor.go`) and its `S` and `T` actions, and
  the inbox overlay's list (`inboxoverlay.go`), hold what the Build and Inbox sections show.
- **Scope:** the sections, `tab`, `shift+tab` and `1`–`3`, the focus strip, `j`/`k` inside a section,
  `enter` by what is selected (a session its agent, a task its details, an inbox item the answer
  screen), the read-only pane, the linked row, and `s` and `t` on the Build section. Below 100 columns
  one section fills the body, the three names stay in its strip and the read-only pane is left out (`d`
  shows it). The Build tab goes from the details.
- **Must not:** change how a task's status is set or tokens are read (`afk ledger set`,
  `afk tokens`).
- **Acceptance:** table tests of section focus and of `enter` per selection; the linked row follows the
  selected session (a row each for a session with a task, one without, and a task with no session);
  saved screens at 120×40 and 80×24; against a scratch build, `tab` and `2` move focus and `enter` on a
  task opens its details; `make check` is green.
Amended 2026-10-22 (T16.4 done): Project is drawn by `projectviewrender.go` from the pure functions in `internal/ui/projectview.go` (`groupSessions`, `linkedTaskFor`, `sessionSummaryLines`, `nextSection`, `projectEnterTarget`, `projectBandText`); `buildview.Row.Session` carries the ledger entry's session and is the only session→task link (`status.json` unchanged); the overview's Build tab is gone, so its digits are `1`–`5` (Overview, Waves, Stack, Notes, Diff) and `d` still opens it beside the session list until T16.5; a task's details are the task tokens box with status, title and detail added (`t` and `enter` on a task), the thing T16.5 turns into the details overlay; `enter` on an inbox item opens today's inbox overlay on it until T16.7; `s`/`t` refuse outside the Build section with a notice naming `2`/`tab`; below 100 columns the project view takes the full body width with no overlay margin; `leaderConds.buildTabFocused` keeps its name but now means the Build section is focused.

#### T16.5 — The agent and the details overlay
- **Depends on:** T16.4
- **Goal:** the Agent is the screen of `docs/dash-design.md` §4.3, and `d` opens the details overlay of
  §4.4 anywhere a session is selected.
- **Background:** the agent's terminal is drawn by `internal/termpane` through `hostpane.go`; its
  output is full of cells with the terminal's default colour, which T16.1's dimming already handles.
  The overview and the Waves, Stack, Notes and Diff tabs are today's right side.
- **Scope:** the band (phase, task, diff, loop, host, the next session needing the owner), the pane
  size and resize, `ctrl+] d`, `ctrl+] m`; the details overlay with tabs `1`–`5`, its scrolling, and
  `esc` back to where it was opened; a session with no live agent opening its details with `r restart`
  when restarting is allowed. Below 100 columns both take the full width.
- **Must not:** change the host protocol or what the agent receives, beyond its new size.
- **Acceptance:** table tests of what `enter` on a session opens (running, stopped, pending, cleaning);
  the agent's size follows the terminal; saved screens; against a scratch build with a stand-in agent,
  the agent fills the overlay, `C-] d` opens the details over it dimmed and `Escape` returns to the
  agent with its keys going to it; `make check` is green.
Amended 2026-10-22 (T16.5 done): `d` (and `ctrl+] d`) opens the details overlay (`Model.detailsOpen`, `detailsFrom`, `detailsFor`, input mode `inputDetail`, drawn by `detailsoverlay.go`, 54 columns on the right from 100 columns up, full width below) and the overview beside the session list is gone, though `rightView`/`viewOverview` and a few other unused helpers remain for later clean-up; `sessionEnterTarget` in `projectview.go` is the pure decision of what `enter` opens (Agent for a pane or a running/restarting/dialing host, details otherwise, and always details for pending, cleaning or a worktree fault) and `rowHasAgent` is built on it; `r restart` works inside the details only where its chip shows (bottom border); the Agent box is band 1 row + terminal + chips 1 row (`agentBoxRows` in `agentview.go`), so `agentContentDims` and `paneScreenGeometry` include them while `paneContentOrigin` (shared with `afk attach`) does not; the band's "next needing you" (`nextNeedingYou`) is the first other awaiting session of the same project in screen order, which T16.6 replaces, and no `ctrl+] ]` chip exists until T16.6 adds `]`.

#### T16.6 — Jump, and the next thing that needs you
- **Depends on:** T16.5
- **Goal:** `/` jumps to any project, session or task (§4.6), and `]` and `[` go to the next and
  previous thing that needs the owner across every project.
- **Scope:** a pure function that orders what needs the owner (a session waiting on them, an inbox item
  that blocks a task, a plan to approve, a broken session or a gone project) the same way at every
  level, and where going to each one lands (a waiting session its agent, a blocking item the answer
  screen, the rest their project). The Agent's band and the top bar's attention count use it. The jump
  search over the same readers the home screen uses, things that need the owner first.
- **Must not:** dial, wait on a network or block the dashboard while it reads another project.
- **Acceptance:** table tests of the order and of where each kind lands; a jump test with a project, a
  session and a task matching; against a scratch build with two throwaway projects each with one
  waiting session, `]` from All projects opens the first agent and `C-] ]` from there opens the second;
  `make check` is green.
Amended 2026-10-22 (T16.6 done): the order is the pure `needsYouItems` in `internal/ui/needsyou.go` (kinds needSession, needInbox, needPlan, needBroken, needGone; `stepNeedsYou` steps and wraps from a `needHere` that also carries `At`, the item the last `]`/`[` landed on, cleared by any list move), and the landing code is `needsyoulanding.go`; other projects come from the Model's every-project cache (`m.everyProject`), filled by the home screen's reader on the scan tick at every level and every 2s while All projects is open, and `ui.AllProjects` now carries each project's task list (`JumpTask`) for the jump search; a blocking inbox item lands on today's inbox list on that item, which T16.7's answer screen should replace in `needsyoulanding.go`; the project switcher (`switcher.go`) is gone, `/` opens `jump.go` everywhere, its `R` rescan went with it, and the open counts are still recorded but order nothing.

#### T16.7 — Answer an inbox item
- **Depends on:** T16.6
- **Goal:** `a` and `enter` on an inbox row open the answer screen of `docs/dash-design.md` §4.5.
- **Background:** the inbox overlay (`inboxoverlay.go`) answers through `answerInboxCmd`; an item has
  `options` and `recommended` (§4.3).
- **Scope:** the item screen redrawn: the item's options numbered, the recommended one labelled, a last
  "Write my own answer" with a field, `1`–`n` to choose, `enter` to send, the line on what happens next.
  `a` opens the oldest item that blocks a task: in the project at Project and in the Agent, across every
  project at All projects.
- **Must not:** change how an answer is written or what `afk inbox` reads.
- **Acceptance:** table tests of which item `a` picks at each level and of the answer sent for each
  choice (an option, the recommended one, an own answer, an item with no options); against a scratch
  build, answering a throwaway item writes the same file `afk inbox answer` would; `make check` is
  green.
Amended 2026-10-22 (T16.7 done): the inbox overlay's item screen is the answer screen (`inboxoverlay.go`, pure parts in `internal/ui/answerscreen.go`: `answerChoices`, `answerStartChoice`, `answerTextFor`, `oldestBlockingItem`, `answerNextLine`, `answerItemPosition`), reached from every entrance through `openInboxOn`, so `needsyoulanding.go` needed no change; an option is sent as its digit string for the store to resolve, as `afk inbox answer <id> 2` does; the overlay has two states (choosing, typing) and `fromList` decides where `esc` and a sent answer go back; `a` is one key-table row at every level (`answerOldest` dispatches on the level, `answerOldestHere` for Project and the Agent) and the Project chips end with `a answer`; digits pick options 1–9 only. See `## T16.7` in `docs/decisions.md`.

#### T16.8 — Activity and the week
- **Depends on:** T16.7
- **Goal:** the home screen shows each project's activity over the last 24 hours and the tokens used on
  each day of the week (`docs/dash-design.md` §4.1).
- **Background:** the decision log (§4.4) gets a line for every decision in the loop, with its time;
  the token ledger (§4.7) gets a line per task phase and role when a task ends, with `weighted`.
- **Scope:** the activity line counts a project's decision-log lines in each two hours of the last 24
  hours, drawn with `▁▂▃▄▅▆▇` scaled to the busiest project. The week chart sums `weighted` over every
  listed project's token ledger per day for the last seven days, with the usage line beside it. Both are
  pure functions over lines handed in; the reading is done on the project scan, not the 2s refresh,
  and reads at most the last 1 MB of each file.
- **Must not:** add a field to either file.
- **Acceptance:** table tests of the bucketing (an empty log, a log older than 24 hours, a busy hour, a
  file cut mid-line) and of the per-day sums; against a scratch build with throwaway logs, the line and
  chart match; `make check` is green.
Amended 2026-10-22 (T16.8 done): the counting is the new package `internal/activity` (`Count`, `WeekSums`, `Level`, `Busiest`, and `Tail`, which reads at most `MaxRead` = 1 MB and drops a cut first line); the dashboard reads the logs through its own seam `Deps.LoadActivity(roots, now)` (wired by `dash_activity.go`), started when the every-project rows arrive with a new set of roots and on every 60s project-scan tick, never on the 2s refresh; the result is `ui.HomeActivity` (cells keyed by project root, one `activity.Week`), drawn by `internal/ui/activityview.go` (`activityCells`, `weekChartLines`); the usage line is `usagetext.WeekLine`; `projectrows.Row` and both log formats are unchanged; `allProjectsLayoutFor` takes a `chart` flag and gives the chart's two rows only when the list keeps one project. See `## T16.8` in `docs/decisions.md`.

#### T16.9 — Prove it (owner + agent)
- **Depends on:** T16.8
- **Goal:** the owner has used the new dashboard for a day of real work, and what it did in each
  terminal is written down.
- **Scope:** the session for this task waits for the owner (an inbox item). The owner runs it in
  Ghostty and one other terminal, inside tmux with the default prefix and outside it, at 120×40 and
  80×24, in each theme, and once with only 256 colours (`TERM=xterm-256color`, `COLORTERM` unset) and
  once with 16. The session writes up in `docs/decisions.md` under `## T16.9` what worked and every
  fault met with its cause, fixes what it can within M16's code and files the rest in the inbox.
- **Acceptance:** the write-up covers both terminals, both sizes, all four themes and both reduced
  colour counts; every fault is fixed or filed.
- Amended 2026-10-22 (T16.9 done): the owner met no faults; an agent sweep found eleven (F1–F11 in
  `docs/decisions.md` `## T16.9`). F3, F4, F7, F8's wording, F10 and F11 are fixed. F1 and F2 (colour
  at 256 and 16), F5 (status bar), F6 (top bar), F8's toast placement and F9 (`internal/projectrows`
  "unknown") are filed in the inbox for a later task to pick up. 16 colours cannot be produced inside
  tmux, because the colour library floors at 256 when `TMUX` is set.

### M17 — The hub: afk on Rex

**Outcome:** the build VM is the hub (§2, "The hub"). Its daemon runs each task's agent in a Rex window,
the control room (`afk dash`) runs there in a Rex session of its own, and the owner watches, answers and
steps into any agent from the laptop's Rex for Mac or a phone's Rex for iOS, from anywhere on their
tailnet. Every Rex client shows what each agent and each project is doing, and what needs the owner,
because afk reports it in Rex's own terms. A machine without Rex runs exactly as it does today.

```
                ┌──────────────────── build VM (the hub) ─────────────────────┐
                │  Rex server (login-server mode, Tailscale)                   │
                │    session "afk"      → afk dash (the control room)          │
                │    session "nebari"   → window t4-3, window t4-4, window plan│
                │  afk daemon: loop, governor, ship, inbox, web page           │
                │  .afk/ files: the only record of state, as today             │
                └─────▲──────────────────────▲──────────────────────▲─────────┘
            Tailscale │            Tailscale │                HTTPS │
              Rex for Mac (laptop)    Rex for iOS (beta)     web page (push, answers)
```

**Background** (what was true on 2026-10-23; the community MCP's feedback threads and Rex's changelog
are the sources, and the spike re-checks anything a task leans on):

- Rex is a terminal multiplexer whose server outlives every client. A session holds windows (tabs), a
  window holds blocks (panes), and today the only kind of block is a terminal. The `rex` command talks
  to the server: `rex api call <method> <json>` calls any method (about 18 ms a call on the owner's Mac)
  and `rex events --json` streams events. `rex api list` and `rex api describe` document every method,
  from the server that answers. A terminal block takes a `command`, a `cwd`, a starting `size`, colours
  (`theme`) and whether it closes when its program exits. It has no environment setting, so environment
  goes on the command line through `env`.
- **Program status (OSC 7501)** is an escape sequence a program writes to say what it is doing:
  `idle`, `working`, `done`, `blocked` (with `kind` `permission`, `question` or `auth`) or `error`,
  with a short message (`msg`). A terminal holds one record per `id`, and the root record when there
  is none. The spec is at `superlogical.com/rex/docs/build/program-status`. Claude Code already sends
  it (`app=claude-code`). Rex shows it as tab spinners and badges, in Activity (on iOS too), in hover
  cards and in a palette that finds sessions across every connected host, and Rex for Mac can turn it
  into sounds, macOS notifications and Dock badges. Rex no longer guesses status from the screen.
  Nothing lets one program report status for another program's terminal. Someone asked for that,
  under "Agent lifecycle integration for external orchestrators", and was pointed at Lua.
- Custom actions (`rex.action` in `init.lua`) belong to the **client**: defined in the laptop's
  `init.lua`, they act on sessions on any host, and the hub's own `init.lua` actions do not reach the
  laptop. Rex for iOS has no custom actions yet.
- Restarting a Rex server ends every session on it, except in **login-server mode**, whose
  per-account servers keep running through upgrades. Rex aims to need server restarts rarely, and does
  not plan to restore sessions after one. Rex's own push notifications are accepted but "a little way
  off". Non-terminal blocks (a browser or a diff viewer) and block plugins (`--block-dir`, not
  documented) are not something to build on yet.
- Today afk's host owns each agent's PTY, serves one client that steals the session from the last,
  keeps no scrollback, records no exit code, and is drawn in the dashboard through `internal/termpane`.
  The emulator there swallows OSC 7501, so even inside Rex nothing of an agent's status gets out.

**The design.** A Rex-backed session is afk's own host running **inside** a Rex terminal:
`afk _host --stdio …` is the block's command, so the host's always-attached client is its own stdin
and stdout (the Rex terminal), while its socket still takes the daemon's requests. The host keeps its
inner PTY to the agent, so the wedge check, restarting in place, typing from the daemon and the web
page, and the exit code all stay host code that exists today. The agent's status records pass straight
through to Rex. Because the host sees every byte, it can also write afk's own record (`id=afk`) between
the agent's escape sequences without tearing them, which is the one thing Rex cannot do for an outside
program. Rex is needed only to create, label, show and read terminals. `host.json` stays the file every
reader uses. T17.1 checks this against the alternative (the host as the agent's parent on the one Rex
terminal, typing through Rex's API) and picks.

Tasks in one Go package collide at merge, so the ones that edit `internal/ui` (T17.4, T17.5, T17.10,
T17.15) and the ones that edit `internal/host` (T17.4, T17.6, T17.7) run one after another. Rex is never
called by a test: `internal/rex` is an interface with a fake, as `gh` and `git` are.

#### T17.1 — Spike: afk on Rex (owner + agent)
- **Depends on:** —
- **Goal:** every question the later tasks lean on is answered on the Mac and on the VM, and written in
  `docs/decisions.md` under `## T17.1`.
- **Scope:** the owner installs the Rex Linux server on the VM in login-server mode with Tailscale, and
  adds the VM to Rex for Mac and to Rex for iOS. The session then answers, with a captured example for
  each:
  - What Claude Code reports, record by record, while it works, when a turn ends, at a permission
    prompt, at an AskUserQuestion, when it is idle, and when it exits; and what Codex and OpenCode
    report, if anything.
  - The stdio host against the parent host (the design above): a hand-built `afk _host --stdio`
    prototype in a Rex block runs a real agent; check resizes reach the inner PTY, the agent's records
    reach Rex, an `id=afk` record written between the agent's writes never tears its drawing, a
    restart in place redraws, and the dashboard's existing restart and typing requests work through the
    socket. Then decide between the two and say why.
  - How a block behaves with no client watching (its size, whether the wedge probe's resize nudge still
    makes an idle agent redraw), and after the owner's Mac takes it over at another size.
  - How to put a window on the owner's screen from a program running on the hub (`session.select` on
    the right client, found from `client.list`), and what happens with two clients connected.
  - `com.superlogical.terminal.format` on a busy agent: how long it takes, and how large the HTML of the
    screen alone is, against the screen and its scrollback.
  - What Rex for iOS shows of afk's control room: the columns and rows it gets in portrait and
    landscape, and whether the dashboard's keys can be typed.
  - That a Rex upgrade on the VM in login-server mode keeps a running agent, and what a server restart
    leaves behind for afk to see.
- **Must not:** change afk's code beyond the throwaway prototype, which is not merged.
- **Acceptance:** the write-up answers every point with what was run and seen, names the design chosen,
  writes the answers into the two §10 rows that say "T17.1 answers it", and amends the later tasks of
  M17 where an answer changes them ("Amended 2026-…").

#### T17.2 — Talk to Rex
- **Depends on:** T17.1
- **Goal:** afk can find a Rex server, call it and read its events, and every caller is table-testable.
- **Background:** the `rex` command is the documented way in, and it derives everything from the server
  it talks to; the wire protocol underneath is not documented, so afk does not speak it.
- **Scope:**
  - `internal/rex`: an interface over the `rex` command (`Call(method, args) (json, error)`, the block
    calls, and an event stream that ends cleanly), with a deadline on every call and a fake for tests.
    It checks the server's `api_revision` once and refuses a server older than the one T17.1 tested,
    saying which version is needed.
  - The few calls M17 uses, each typed: create a session or find it by label, open a window with one
    terminal, set labels, list a session's blocks, read a block's program status, `process` and
    `format`, list clients, ask a client to show a window.
  - `internal/probe` learns whether `rex` is installed, whether a server answers, and whether afk is
    itself running inside Rex (`REX_SESSION`, `REX_BLOCK`). `afk doctor` (or the install's check) shows
    the result in plain words.
- **Must not:** be called from `internal/discover`, or block anything that draws a screen.
- **Acceptance:** table tests of the parsing of each call's answer and error, the revision check and the
  probe's every case; against a scratch build on the Mac, a hidden `afk _rex-check` (or a test behind a
  build tag) lists sessions and reads its own pane's status; `make check` is green.

#### T17.3 — Program status, read and written
- **Depends on:** —
- **Goal:** afk can write an OSC 7501 report and pick reports out of a stream of terminal output.
- **Scope:** a pure package, `internal/progstatus`: `Encode(Record) ([]byte, error)` keeps the spec's
  limits (keys, the base64 of `msg` and `title`, `app`, `id`, 4096 bytes in all) and refuses control
  characters; a `Scanner` takes output in chunks of any size, finds every complete OSC 7501 report even
  when one is split across chunks, ends with either ST or BEL, and reports the byte offsets where it is
  safe to insert a sequence of afk's own (never inside any escape sequence). Malformed reports are
  dropped as the spec says.
- **Must not:** allocate without bound on a stream that never ends a sequence; a sequence longer than
  the spec's limit is dropped.
- **Acceptance:** table tests from the spec's own examples, every limit at and over its edge, a report
  split at every byte, BEL and ST endings, an unended sequence, and insertion points around CSI, OSC and
  DCS sequences; `make check` is green.
- Amended 2026-10-23 (T17.3 done): `internal/progstatus` follows spec revision 0.4 and has `Encode(Record) ([]byte, error)` (ends with ST, refuses anything a terminal would drop), `Parse(body) (Record, bool)` and `NewScanner().Scan(chunk) Result{Reports []Report{Record; End}; LastSafe int}` plus `Safe()`. `Record` carries `Progress`/`HasProgress` too. T17.4 keeps the root record from `Reports` with `ID == ""`; T17.7 inserts at `LastSafe` or a report's `End`, and must choose its own wait when a sequence never ends (`Safe()` stays false). The middle of a UTF-8 character counts as unsafe.

#### T17.4 — The host records what its agent reports
- **Depends on:** T17.3
- **Goal:** `host.json` says what the agent last reported about itself and how its last program ended,
  on every machine, with or without Rex.
- **Scope:** the host's output pump passes its output through the scanner and keeps the latest root
  record. `host.json` gains `agent_status` (`state`, `kind`, `msg`, `at`) and `last_exit` (`code`,
  `signal`, `at`), each omitted when unknown, and is republished at most every 2 seconds when the record
  changes (a change of `state` immediately). `internal/provider` turns an `app` word into a provider, so
  nothing else names one. `internal/discover` copies both fields into its rows, and the dashboard shows
  the agent's message in the session row and the band where there is room. §4.1 is amended with the
  new fields.
- **Must not:** slow or stall the pump (a wedged reader of `host.json` cannot hold it); write a field
  into `status.json`.
- **Acceptance:** table tests of the record kept across chunked output and of the republish timing; an
  end-to-end host test with a stand-in agent that prints reports; against a scratch build, a stand-in
  agent's `working … msg=Running tests` shows in the dashboard's row; `make check` is green.
- Amended 2026-10-23 (T17.4 done): `host.Meta` has `AgentStatus *AgentStatus` (`agent_status`: `state`, `kind`, `msg`, `at`) and `LastExit *LastExit` (`last_exit`: `code`, -1 when signalled, `signal` name, `at`); discovery rows carry them as `discover.AgentStatus`/`discover.LastExit`. Only the root record whose `app` is empty or `provider.FromApp` maps to the session's provider is kept (`claude-code` known; `codex`/`opencode` guessed). `last_exit` appears only after a restart in place, because a child's own exit still ends the host and removes `host.json` (the code is in `host.log`). Every `host.json` write goes through one `publish` function in `internal/host`; T17.7 adds its fields there.

#### T17.5 — afk reports its own status
- **Depends on:** T17.3
- **Goal:** a terminal running `afk dash` or `afk daemon` in the foreground tells Rex, and any terminal
  that understands OSC 7501, what needs the owner.
- **Scope:** a pure function from the dashboard's projects and inbox to records: the root record is
  `blocked` (`kind=question`, `msg` "2 things need you: …") while anything waits on the owner, `error`
  while a project's line is stopped, `working` while a task runs and `idle` otherwise, and one child
  record per project (`id=<project>`, `title` the project's name, `msg` the running task and its
  phase). Sent when the answer changes, not on every tick, and cleared on exit. The window title and the
  OSC 9;4 progress the dashboard sends today stay. `afk daemon` run in a terminal sends the same.
- **Acceptance:** table tests of the mapping (nothing running, a task running, an inbox item, a stopped
  line, a paused machine) and of when it sends; against a scratch build in a Rex pane, `rex block call …
  program_status` shows the records and the tab shows the blocked badge; `make check` is green.
- Amended 2026-10-23 (T17.5 done): `internal/afkstatus` has `Records([]projectrows.Row) []progstatus.Record` (root first, one child per project, `app=afk`, child `id` the sanitized project name), `Diff(prev, next) []byte` and `ClearAll() []byte`. `afk dash` sends through `tea.Raw` when `ui.Deps.ProgramStatus` is set and clears after the program ends; `afk daemon` sends only when stdout is a terminal, from its per-pass planning hook (`daemon_status.go`), never for `--once`. T17.7's host record is separate (`id=afk` in an agent's terminal), and T17.11's "next agent that needs the owner" can read the control room's root `blocked` record. The Rex pane check is an owner item: Rex was not installed on the build machine.

#### T17.6 — One seam for every runtime
- **Depends on:** T17.4
- **Goal:** everything that starts, stops, restarts, types into or opens a session goes through one
  interface, so a second runtime is a second implementation, with no change in behaviour.
- **Background:** the callers are `session.SpawnHost`, `session.StopHost` and the rollback in
  `create.go`, `host.SendInput`, `host.SendKeys`, `host.RequestRestart` and `host.RequestExit` (daemon,
  web page, `afk plan`, `plan approve`, the plan-end hook), and the dashboard's and `afk attach`'s pane
  (`internal/ui/hostpane.go`).
- **Scope:** the interface and today's runtime behind it; `host.json` gains `runtime` (`afk` when
  absent, so every existing file reads as today). Callers choose the implementation from `host.json`,
  never from the machine's setting, so a session keeps its runtime. §4.1 is amended.
- **Must not:** change any behaviour, protocol or file other than the new field.
- **Acceptance:** the existing tests pass unchanged; a table test of the runtime chosen for each
  `host.json` (absent field, `afk`, an unknown word refused with a plain message); `make check` is green.
- Amended 2026-10-23 (T17.6 done): the seam is `session.Runtime` in `internal/session/runtime.go` (`Spawn`, `Undo`, `Stop`, `SendInput`, `SendKeys`, `Restart`, `Exit`, `Dial`), chosen by `session.RuntimeFor(worktree, name)` from `host.json`: a missing or unparseable file, or an empty `runtime`, is afk; an unknown word returns an error wrapping `session.ErrUnknownRuntime` and nothing is signalled or removed. The afk host writes `"runtime": "afk"` (`host.RuntimeAfk`, set on `baseMeta` in `Serve`, so every `publish` keeps it). T17.7 adds its runtime to `runtimeNamed` and writes its own word; new sessions still default to afk until T17.8 passes the machine's backend. `runtime_seam_test.go` fails on any non-test call outside `internal/host` and `internal/session` to `session.SpawnHost`/`StopHost` or `host.SendInput`/`SendKeys`/`RequestRestart`/`RequestExit`/`Dial`/`Attach`. Left for T17.7: the daemon's forced stop (`daemonStopSession`) still sends SIGTERM to the host pid itself after the polite `Exit` through the seam.

#### T17.7 — The host inside a Rex terminal
- **Depends on:** T17.2, T17.6
- **Goal:** a session can run as `afk _host --stdio` in a Rex block (or as T17.1 decided), with every
  request the daemon, the web page and the dashboard make still working.
- **Scope:**
  - The host's client is its stdin and stdout: never stolen, never dropped, sized from its terminal on
    every SIGWINCH. A socket client asking to attach is refused with "This session runs in Rex; open it
    there", and the restart, exit and typing requests work as today.
  - The host writes afk's own record, `id=afk`, at safe offsets from T17.3: the task and its phase from
    `status.json` (`working`), `blocked` while `awaiting_input` is set or the task is parked on the
    owner, and `error` for a failed phase, re-sent after the agent's own reports clear it.
  - `host.json` records `runtime: "rex"` and the block it lives in (`rex.server`, `rex.session_id`,
    `rex.window_id`, `rex.block_id`), so readers stay on files and signal-0 on the host's pid as today.
  - The wedge check keeps its schedule; with no Rex client watching, it uses what T17.1 found works.
  - A Rex server that goes away takes the host with it: that is the "host gone" the daemon already
    relaunches (§5.6).
- **Must not:** dial Rex from `internal/discover`; change the frame protocol for the afk runtime.
- **Acceptance:** table tests of when afk's record is sent and what it says; an end-to-end test of the
  stdio host with a stand-in agent on a pipe pair (no Rex); against a scratch build in Rex, a stand-in
  agent restarted from the dashboard redraws in its Rex window and the daemon's typed line reaches it;
  `make check` is green.

#### T17.8 — Sessions in Rex
- **Depends on:** T17.7
- **Goal:** with `[runtime] backend = "rex"`, every session afk starts (a task, a one-off, a planning
  session) opens in Rex, laid out and labelled so the owner can find it from any client.
- **Scope:**
  - `[runtime] backend` (`afk` or `rex`, default `afk`) in `config.toml`, read by `internal/daemonconfig`;
    `rex` with no Rex server answering refuses to start a session with a plain message and a blocking
    `failure` item, never falls back silently. §4.6 (or the `config.toml` section) and
    `docs/configuration.md` are amended.
  - One Rex session per project, labelled with the project's name and found again from the block ids
    `host.json` records (labels are not unique in Rex, so afk never trusts a label alone). One window
    per afk session, labelled `t4-3 · review` and relabelled on each phase change by the host; the
    planning session's window is `plan`. Each terminal starts at 160×48 with the dashboard's theme
    colours, and closes when its host exits unless the session failed.
  - Cleaning a session closes its window after the host has stopped.
- **Must not:** create a Rex session the owner did not ask for when the backend is `afk`.
- **Acceptance:** table tests of the layout decision (first session of a project, a second, a planning
  session, a Rex session closed by hand, a label clash), of the label for each phase and of the refusal;
  against a scratch build on the Mac with a stand-in agent, `afk new` opens the window and `afk clean`
  closes it; `make check` is green. README: `docs/reference.md` (the runtime), `docs/configuration.md`.

#### T17.9 — The hub
- **Depends on:** T17.8
- **Goal:** the VM runs as the hub, and the owner reaches it from the laptop and the phone.
- **Scope:**
  - `docs/runbook-vm.md` gains the hub: Rex's Linux server in login-server mode (so upgrades keep the
    agents), Tailscale with access for the owner only and never `--tailscale-allow-tailnet-members`,
    `afkd` ordered after the Rex service, `[runtime] backend = "rex"`, and adding the VM in Rex for Mac
    and Rex for iOS. It says plainly that anyone who can connect to the Rex server can run anything as
    afk's account, which is why only the owner may.
  - The control room: the daemon keeps a Rex session named `afk` with one window running `afk dash`,
    and starts the window again when it is closed or its program ends (at most once a minute).
  - `afk install` offers the Rex runtime when it finds `rex` with a server answering, and explains
    login-server mode on Linux; `docs/install.md` and `docs/daemon.md` say what changes.
- **Acceptance:** table tests of the control room's keep-alive decision and the install step's offer;
  on the VM, the owner opens the control room and a running agent from the laptop and from the phone,
  and a Rex upgrade does not end the agent; `make check` is green.

#### T17.10 — Going into an agent opens it in Rex
- **Depends on:** T17.8
- **Goal:** for a Rex-backed session, `enter` on its row and `afk attach` show the agent's own Rex
  window on the owner's screen, and a key brings the owner back to the control room.
- **Scope:** a pure decision (`afk` or `rex` runtime; the dashboard inside Rex or not; one or several
  Rex clients connected) of what going in does: inside Rex, ask the client showing the control room to
  show the agent's window (`session.select`); outside Rex, say in the band "This agent runs in Rex: open
  `<project>` › `t4-3` in Rex" and copy nothing. Restart, details (`d`) and every other key keep working
  on the row. Sessions on afk's own host keep today's Agent level unchanged. `docs/dashboard.md` and
  `docs/dash-design.md` say what the Agent level does for each runtime.
- **Must not:** change the Agent level for sessions on afk's own host; draw a Rex-backed agent through
  `termpane`.
- **Acceptance:** table tests of the decision, every row a case; against a scratch build in Rex, `enter`
  on a Rex-backed row brings its window forward and the action from T17.11 returns to the control room;
  `make check` is green.

#### T17.11 — afk in Rex's palette
- **Depends on:** T17.10
- **Goal:** the laptop's Rex for Mac has afk's own actions in its palette and on keys.
- **Background:** custom actions belong to the client, so they are installed on the laptop and act on
  the hub's sessions through the hub's label in `rex.servers()`.
- **Scope:** `afk rex setup` writes `afk.lua` beside the laptop's Rex `init.lua` and prints the one
  `require` line to add, never editing `init.lua` itself. Its actions, each in the palette under `afk`:
  open the control room; go to the next agent that needs the owner (the first `blocked` record across
  the hub's sessions, then the first `error`, in the hub's session order); pause and resume the build
  (each runs `afk pause` or `afk resume` in a short-lived terminal on the hub that closes when done).
  Keys are suggested in comments, not bound. It works on a laptop with no `afk` installed, by passing
  the hub's label (`afk rex setup --print --hub vm` writes the file to stdout).
- **Acceptance:** table tests of the generated file for each option, and a Lua syntax check of it in
  `make check`; on the laptop, each action works against the hub; `make check` is green. README:
  `docs/reference.md`, and a short "afk in Rex" section in `docs/dashboard.md`.

#### T17.12 — Program status as a signal
- **Depends on:** T17.4
- **Goal:** the daemon and the planning chat use what the agent reports about itself, beside the hooks.
- **Scope:** `agent_status` from `host.json` becomes an input to the pure decisions that ask whether an
  agent is waiting on someone: the daemon's stall rule (§5.3: `blocked` for longer than `stall_after`
  counts as waiting, as `awaiting_input` does, whichever came first) and `planchat` (a `blocked` record
  with `kind=question` means waiting even before the transcript has the question). The hooks stay
  the authority for `awaiting_input`; a report never writes `status.json`. A stall item quotes the
  agent's `msg`.
- **Acceptance:** new rows in the stall-rule and `planchat` tables (the hook alone, the report alone,
  both, a stale report, a report from an earlier agent before a restart); `make check` is green.

#### T17.13 — Keep the agent's last screen
- **Depends on:** T17.8
- **Goal:** when a Rex-backed task fails, parks or stalls, what was on its terminal is saved and linked,
  so the owner can see what happened from a phone.
- **Scope:** the daemon saves the block's screen and scrollback with `format` as HTML to
  `.afk/{session}/terminal.html` (the last 2 MB) when it raises the item, and again when the session is
  cleaned after a failure. The inbox item and the web page's session screen link to it, served through
  the page's sign-in, with its colours kept and its scripts none. §4.1 is amended.
- **Must not:** read a block on the dashboard's 2-second refresh, or fail the item when the read fails
  (the item says the screen could not be saved).
- **Acceptance:** table tests of when the screen is saved and of trimming to the limit; a web test that
  the page is behind the sign-in and its HTML carries no script; against a scratch build, a stand-in
  agent's failure leaves the file and the link; `make check` is green. README: `docs/web.md`.

#### T17.14 — Watch an agent from the web page
- **Depends on:** T17.13
- **Goal:** the web page shows a Rex-backed agent's screen, live and read-only, and a reply to the
  planning chat no longer takes the session from anyone.
- **Background:** a remote terminal on the page was set aside in M8 (§9) because a host serves one
  client and a live keyboard into an agent with permissions bypassed is too much. A read-only view needs
  neither.
- **Scope:** on the session screen, "Watch" shows the agent's screen (`format`, HTML, the screen only)
  refreshed over the existing event stream at most every 2 seconds while someone is watching, and never
  read otherwise. A planning reply to a Rex-backed session is typed through its host as today, and the
  rule that it takes the session from the dashboard (M8, §2 "Web page as command center") no longer
  applies to it, because Rex shows the agent to every client. `docs/web.md` says so.
- **Must not:** send a key to an agent from the watch view.
- **Acceptance:** table tests of when the screen is read (watching, not watching, two watchers, a gone
  block); a JS test of the view; against a scratch build on a phone-width browser, the screen follows a
  stand-in agent; `make check` is green.

#### T17.15 — The control room at phone width
- **Depends on:** T17.10
- **Goal:** the control room is easy to use in Rex for iOS, at the sizes T17.1 measured.
- **Scope:** All projects and Project at the narrowest portrait size Rex for iOS gives: which columns
  and parts go first, the status bar's chips, the answer screen, and the keys that can be typed on the
  phone's keyboard and its symbol bar. `docs/dash-design.md` gets the narrow layout, drawn with the
  owner first if it changes the look.
- **Acceptance:** saved screens at the measured portrait and landscape sizes; table tests of what each
  width keeps; the owner answers an inbox item and opens an agent from the phone; `make check` is green.

#### T17.16 — Notices that open the right window
- **Depends on:** T17.2
- **Goal:** a desktop notice from afk opens the Rex window it is about, and afk does not repeat a
  notice Rex for Mac already gives.
- **Scope:** `afk notify` learns the Rex ids `internal/dashmeta` records beside tmux's, and a click on a
  toast asks Rex to show that window (falling back to today's tmux focus). When the session is
  Rex-backed and Rex for Mac is connected, the toast for an agent waiting on the owner is left to Rex's
  own program notification, which says the same thing; web push (M15) is unchanged.
- **Acceptance:** table tests of the decision (tmux, Rex, both, neither; Rex for Mac connected or not);
  against a scratch build on the Mac, clicking the toast brings the window forward; `make check` is
  green.

#### T17.17 — Prove the hub (owner + agent)
- **Depends on:** T17.9, T17.11, T17.12, T17.14, T17.15, T17.16
- **Goal:** the owner has run real tasks on the hub for several days, steering from the laptop and the
  phone, and what worked is written down.
- **Scope:** the session waits for the owner (an inbox item). The owner sets the VM to the Rex runtime
  and lets the daemon build at least three tasks, answering at least one inbox item from the phone and
  stepping into a running agent from both devices. The session writes up in `docs/decisions.md` under
  `## T17.17` every fault met with its cause, fixes what it can within M17's code and files the rest,
  and records the numbers the open question in §10 needs (sessions recovered, Rex restarts met,
  anything only afk's own host could have done).
- **Acceptance:** the write-up covers both devices and every M17 surface; every fault is fixed or filed.

## 9. Deferred / later

- **`afk plan`** moved to M5 (2026-10-13).
- **Answering plan questions on the web page** instead of the terminal: moved to M8 (2026-10-13), as a
  chat view of the planning session (T8.12).
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
- **The owner profile, installing the plugin, and starting and stopping the daemon in the dashboard**:
  left as commands in M7, because each is run once per machine or rarely.
- **M7's controls on the web page** (start a planning session, add and remove projects, set a task's
  status, refresh usage): moved to M8 (2026-10-13).
- **A remote terminal on the web page** (attach to any session from a browser): left out of M8 by the
  owner. It would take the session from the dashboard (a host serves one client), is awkward at phone
  width, and is a live keyboard into an agent with permissions bypassed. Watchers beside the typing
  client would need a change to the host. Amended 2026-10-23 (M17): for Rex-backed sessions, Rex gives
  every client the live terminal and T17.14 gives the page a read-only view; a keyboard on the page
  stays out.
- **A laptop `afk` that acts on the hub** (`afk --hub vm inbox`, carried by the daemon's JSON API or by
  running the command on the hub through Rex): left out of M17 by the owner (2026-10-23), who starts
  with Rex for Mac alone.
- **Rex for iOS in place of the web page** (answers and push): left open by the owner (2026-10-23)
  until Rex for iOS is out of beta and Rex's push notifications exist.
- **A native afk block in Rex** (a control room that is not a terminal UI): waits on Rex documenting
  block plugins or adding non-terminal blocks; on 2026-10-23 neither was planned short-term.
- **Reporting afk's status into an agent's Rex terminal from outside it**, if Rex adds a way for one
  program to report status for another's terminal. The stdio host (T17.7) does it from inside today.
- **Found projects on the web page** (those the dashboard finds under its `[dash]` search folders but
  the daemon does not build): left out of M8 by the owner (2026-10-13). The dashboard shows them
  mainly to open their sessions' terminals, which the page cannot do; T8.16 adds a folder to the build.
- **Daemon-side write-guard and continuation enforcement**, replacing shipit's Claude-only mods.
- Rename `.claude/worktrees/` to a provider-neutral location.
- Open-source release: Homebrew tap, docs site. (The install script moved to M11, 2026-10-17.)
- **Push for notices that do not block the owner** (idle, all done, a plan ready for review): dropped
  in M15 by the owner (2026-10-20), who chose to be notified only of what blocks on them.
- **A Docker image**: set aside 2026-10-17 in favour of a VM (see the Decisions row "Where afk runs, and
  installing it"). Worth revisiting only as an optional extra for people who already run everything in
  containers.

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
| Licence for the open-source release. **Answered 2026-10-19:** MIT, copyright Darin Haener (§2, "Releases"). | T12.2 |
| Is the repository public, with release binaries anyone can download without logging in to GitHub? The bootstrap script cannot be tried for real until it is. The owner said on 2026-10-17 that this is "on the table soon". **Answered 2026-10-18:** The repo is now public | T11.6 |
| Which way does a Rex-backed session run: afk's host inside the Rex terminal with its own inner PTY (the stdio host the plan assumes), or as the agent's parent on the one Rex terminal, typing through Rex's API? T17.1 answers it. | T17.7 |
| Which program status records does each agent CLI send (Claude Code at a permission prompt, at an AskUserQuestion, at the end of a turn and when idle; Codex and OpenCode at all)? T17.1 answers it. | T17.12 |
| Does Rex become the default runtime where a Rex server answers, and do the afk-only Agent level and `internal/termpane` retire? Decide from T17.17's write-up. | after T17.17 |

## 11. Definition of done (every task)

- `make check` green locally and in CI (Linux).
- New behaviour covered by table tests; decision logic is a pure function with every branch a row.
- No test touches a real session, GitHub, an agent CLI or real usage.
- Nothing outside `internal/provider` names a provider.
- Schemas: any change to a file in §4 updates §4 and, for `status.json`, the schema document, in the
  same change.
- The user docs updated for any user-visible command, flag or key: the page under `docs/` that covers it
  (`reference.md` for a command or flag, `dashboard.md` for a key, `web.md` for the web page, `daemon.md`
  for the build loop, `configuration.md` for a settings file), and `README.md` only when the overview or
  the first-project path changes. Amended 2026-10-17: the README was split into those pages that day, so
  a task written before then that says "README: …" means the page that now holds that section.
- `docs/decisions.md` updated when the task decides something this plan didn't, and this plan amended
  in place ("Amended <date> (T… done): …") when the task changes what a later task should assume.
- Verified against a built binary, not from reading the diff.
