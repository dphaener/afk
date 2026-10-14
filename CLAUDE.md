Read `docs/PLAN.md` §1–§7 before planning any task. They are the shared contract every task builds against.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. afk began as a copy of the forge-cli project, renamed (see `docs/provenance.md`).

## Install a fresh binary after every verified feature

When a feature is done and verified, the last step is always:

```bash
make install
```

A feature isn't finished until the binary on `$PATH` has it. `~/bin/afk` is what actually gets run day to day, so leaving it stale means the work is invisible no matter what's on `main`. "Verified" means it passes `make check` and its behavior has been confirmed against a built binary, not asserted from reading the diff.

### Never `cp` a Go binary onto `$PATH`

`make install` builds straight into `PREFIX` (default `~/bin`) rather than building then copying, and it has to stay that way. Go gives the binary a linker-signed ad-hoc signature bound to the path it was created at, so on arm64 a copy elsewhere invalidates it and the kernel SIGKILLs the copy. Same bytes, exit 137, no output, empty stderr — it reads as a corrupt binary when it's really just been moved. Recovery for an already-copied one:

```bash
codesign --force --sign - ~/bin/afk
```

### Build test binaries outside `$PATH`, never to `~/bin`

`make install` writes to a **single global slot** that every worktree of this repo
shares. Concurrent sessions in sibling worktrees each run it, so the binary at
`~/bin/afk` belongs to whichever one installed last — and that is usually not
yours. For any mid-work verification, build to a scratch path instead:

```bash
go build -o /tmp/afk-check . && /tmp/afk-check <args>
```

Reserve `make install` for the end of a verified feature (per the section above),
and re-verify it immediately rather than trusting an earlier install.

**Why this matters more than it sounds.** A clobbered `~/bin/afk` is
indistinguishable from a code regression by observation: the feature is simply
absent, tests stay green because the source is fine, and every symptom points at
your own diff. It cost a full misdiagnosis cycle in this repo — a subcommand
answered `Unknown subcommand`, a file the code demonstrably wrote never appeared,
and the "broken" binary turned out to be byte-identical to a sibling worktree's
build. Before concluding that working code broke, confirm the binary under test
is the code under test:

```bash
grep -c -a <a-symbol-your-change-introduced> ~/bin/afk   # 0 means stale
shasum -a 256 ~/bin/afk $(git rev-parse --show-toplevel)/afk
```

## Commands

```bash
make            # build ./afk in the repo root
make install    # build into ~/bin (PREFIX=... to override)
make check      # fmt-check + vet + test + plugin-test + hook-test, run by .github/workflows/ci.yml
make test       # go test ./...
make plugin-test  # Ruby parser test, prompt size-cap check, plugin node tests (plugins/afk and plugins/afk-opencode)
make hook-test  # plugin hook shell tests
make clean      # remove the local ./afk
make help       # list targets
```

Narrower loops:

```bash
go test ./internal/host/                     # one package
go test -run TestRouteArgs -v .              # one test
go test -run 'TestRouteArgs$/leading_flag' -v .  # one subtest ($ anchors the parent; underscores for spaces)
go test -count=1 .                           # bypass the cache
./afk new --dry-run --base main scratch    # exercise session creation, no side effects
```

**Mutation testing requires `-count=1`.** The cache survives edits to the file under test, so a mutation that should turn the suite red can report `ok (cached)` — indistinguishable from a genuine pass, which makes the whole exercise worthless.

**`internal/host` and `internal/ui` tests fail under the sandbox** with `bind: operation not permitted` (unix-socket `bind()` is blocked; `internal/ui`'s end-to-end and restart-trigger tests run a real `host.Serve`). They pass unsandboxed. Don't chase them as real failures or "fix" them by weakening the tests. Also budget for `internal/ui` taking ~40s: its end-to-end test pays the host's shipped liveness schedule (30s silence + 5s probe deadline), which that package cannot compress. `go test -short ./internal/ui/` skips it — but note `make test`, `make check`, and CI never pass `-short`, so the full run is what gates a change. Sandboxed Go commands also want `GOCACHE=~/.cache/go-build-sandbox`, and writes to the repo root are blocked, so `make build`/`make install` may need to run unsandboxed.

## Architecture

`afk` manages **detached session hosts**: a session is a `claude "/afk:go --session {name}"` process running on its own PTY in its own git worktree, owned by a background `afk _host` process that outlives every UI attached to it. Quitting the dashboard or detaching from `attach` cannot end a session. That single fact drives most of the design below.

Read `README.md` for the user-facing surface (flags, keybindings, exit codes, degradation matrix). What follows is the cross-file structure it doesn't cover.

### Two independent state channels

Nothing in this codebase passes session state in memory between processes. There are exactly two channels, and conflating them causes bugs:

- **`status.json`** — lifecycle state (phase, `loop_count`, `awaiting_input`), written by the conductor and by Claude Code hooks. Multi-writer, so every write is re-read-merge plus a same-directory atomic rename. Readers treat a missing or unparseable file as *stale*, never as an error. Used both for **lifecycle tracking** and as an **existence signal** (a `.afk/{name}/` directory with this file is a planned session with a conductor).
- **`host.json`** — where the session's PTY lives (pid, socket path). Written only by the host. Used for **liveness detection** (attaching and reattaching to running sessions) and as a **discovery source** when no `status.json` is present (a `.afk/{name}/` directory with `host.json` but no `status.json` is a one-off session created via `afk new --scratch`, with no plan and no conductor).

They're deliberately decoupled: a session with a stale `status.json` is still reachable through `host.json`, and opening its pane is often how you find out why the status went stale. A session without `status.json` at all (a one-off) is discovered purely through `host.json` and is not broken—it is the intended shape. Don't add a code path that requires both to agree.

### The status schema is an external contract

`internal/state` and `internal/discover` implement a spec that ships with the plugin in this repo:

```
plugins/afk/skills/conductor/status-schema.md
```

That document is binding — field ownership, the `awaiting_input` state machine, the phase lifecycle, hook targeting rules. Nine files cite it in their package docs. **Don't invent fields or semantics here.** If a change seems to need a new status field, the schema changes first, in the same change (it also governs the conductor and the hooks). The OpenCode port in `plugins/afk-opencode` carries its own copy of the schema and must be kept in step.

### Host/client split (`internal/host`)

A length-prefixed frame protocol over a unix socket. `Data` flows both ways; `Resize`/`Detach`/`Exit` are client→host; `Stolen` is host→client when a second client takes over. A host serves exactly **one** client — connecting steals the session, and the child process never notices.

The design constraint throughout is that **a wedged client must never stall the child**: bounded per-client queues, write timeouts, and a slow client gets dropped rather than pinning the PTY pump (it can reattach; attach forces a redraw). Preserve that when touching the pumps.

### Discovery is read-only and uncached (`internal/discover`)

`Discover` re-reads git and the filesystem from scratch on the dashboard's 2s ticker, because the writers mutate these files continuously and a cache would show the wrong phase. Worktrees come from `git worktree list --porcelain`, never from worktrunk.

**It must never block.** No dialing, no IPC, no network. The one sanctioned non-file check is `HostRunning`, via a non-blocking signal-0 (`syscall.Kill(pid, 0)`) — allowed precisely because it cannot stall. It's also *advisory*: PIDs get recycled, so it can false-positive. Anything that could hang would freeze the whole dashboard for the length of the hang.

### Package roles

`internal/session` (create/clean, and the planning session's `master-plan` host and its `planning` marker), `internal/state` (parsers for plan, status, verdict, and the `.rate-limited` marker the daemon reads), `internal/spice` (git-spice topology), `internal/provider` (the agent CLIs; `PlanArgv` is how a planning session is started), `internal/probe` (tool detection), `internal/ui` (Bubble Tea dashboard), `internal/termpane` (PTY ↔ terminal-emulator bridge that renders a live session inside the dashboard — an experimental spike, not production-hardened), `internal/pluginstall` (`afk plugin install`), `internal/plansession` (reads `.afk/plan/` and holds the pure phase decision behind `afk plan status` and `afk plan advance`), `internal/governor` (the pure usage decision: run, no_launch, pause, hold or refuse), `internal/usagestate` (reads and writes `usage.json`, the last usage reading). `internal/profile` reads, checks and edits the owner profile (`profile.toml` beside `config.toml`): the pure `Select` picks the entries for one kind of project, and `Set` edits one entry as text so comments survive. `internal/inbox` holds owner inbox items, written only through `afk inbox`. `internal/projectconfig` is the `afk.toml` and `afk.local.toml` reader (`plan`, `[notify]` and `[ship]`). `internal/masterplan` reads a master plan's task headings and dependencies (`masterplan.Scan` is the whole-file scan that `Parse` is built on), `internal/planlint` is the pure checker behind `afk plan lint` (plan text to findings, built on that scan), `internal/ledger` reads and writes `.afk/build/ledger.json` (each master-plan task's status), and `internal/nexttask` is the pure choice of the next task to run, or why none. `internal/notify` also holds the ntfy sender. `internal/daemon` is the build loop behind `afk daemon` (a pure `Decide` plus an executor), `internal/daemonconfig` reads the machine's `config.toml`, the one settings file, including the `[dash]` table of the dashboard's search folders (a bad value there is carried as `Config.DashErr`, never as a load error), and edits its `[[project]]` list (`afk project`, `afk plan approve`), `internal/daemonstate` holds the pause flag, the review list (`plan-reviews.json`, the plans waiting for approval), each project's `daemon.json`, the stop marker (`.afk/build/stopped`), the GitHub poll record (`.afk/build/ship-poll`), the machine lock and the machine's `governor.json` (the last usage-probe attempt and the last login check), and `internal/decisionlog` appends to `.afk/log/decisions.jsonl`. `internal/ship` takes a finished session to a merged, deployed task (push, pull request, CI, merge, deploy check) as a pure `Decide` plus an executor behind `git` and `gh` interfaces. `internal/web` is the page `afk daemon` serves (token-guarded HTML and a JSON API for status, the inbox and pause/resume); it reads the same files the build loop writes and never touches the loop's memory, serves a read-only plan view at `/p/<project>/plan`, and `daemon_web.go` wires it in.

### The agent plugins (`plugins/`)

`plugins/afk` is the Claude Code and Codex plugin; `plugins/afk-opencode` is its OpenCode port. Both are embedded in the binary (`plugin_assets.go`) and written out by `afk plugin install`. The root `.claude-plugin/marketplace.json` and `.agents/plugins/marketplace.json` make this repo a marketplace for Claude Code and Codex. When you bump the plugin version, bump `.claude-plugin/plugin.json` and `.codex-plugin/plugin.json` together: Codex caches the plugin by version, and `TestPluginAssets` fails if they differ. A prompt change in `plugins/afk` usually needs the same change in `plugins/afk-opencode`, translated with the glossary in its `PROVENANCE.md`.

Packages are hermetic with injectable dependencies. Tests never create or modify real sessions.

### Keeping the two plugins in step

The plugin lives in `plugins/afk` and the OpenCode port in `plugins/afk-opencode`. They are meant to be kept in step by hand, using the glossary in `plugins/afk-opencode/PROVENANCE.md`, but right now they are not: the OpenCode port lags the T0.2 change to `plugins/afk` (three checks, QA scenarios, `afk plan check`, the autonomous policy, the `StopFailure` hook, the planner's project documents). The lag and the areas it covers are recorded in `PROVENANCE.md`, and the resync is deferred (`docs/PLAN.md` §9). Until it is done, do not assume the port matches `plugins/afk`; a change to `plugins/afk` still needs the matching change in the port, or an entry in that lag note.

## Rules that do not apply here

`~/code/.claude/rules/*.md` are Rails rules. This is a Go project, so they do not apply.

## Conventions

**Pure decision functions for anything branching on the environment.** The impure call (`isTerminal`, git state, TTY-ness) happens in the caller and is passed in as a parameter, so the whole matrix is table-testable without a TTY. Copy `shouldHandOffToDash` in `new.go` or `routeArgs` in `main.go`.

**Doc comments explain why, not what** — especially where the code looks wrong without the reason. Several cite specific upstream mechanisms (bubbletea's stdin-only TTY check; why the emulator's response pump is load-bearing rather than optional) because a future reader would otherwise delete them as redundant. Keep that, and keep the citations accurate: a comment asserting a mechanism you haven't read in the dependency's source is worse than no comment.

**Table tests in the style of `new_test.go`** — named cases, `t.Run`, and a comment on the test function saying which rows are load-bearing and why.

## Engineering standards (from `docs/PLAN.md` §7)

Every task is held to these:

- **`make check` green**: gofmt, `go vet`, `go test ./...`, the plugin node tests and the hook shell tests. CI runs it on Linux, and the VM is Linux, so anything macOS-only (desktop toasts) must degrade silently there.
- **Table tests** in the style of `new_test.go`: named cases, `t.Run`, and a comment on the test saying which rows are load-bearing and why.
- **Tests are hermetic.** Dependencies are injected. No test creates or modifies a real session, calls GitHub, runs an agent CLI or probes real usage. `gh`, `git`, the provider CLI and the clock are interfaces with fakes.
- **Doc comments explain why**, especially where the code looks wrong without the reason. There is no git history to consult, so the comment is the only record. A comment asserting a mechanism in a dependency you have not read in that dependency's source is worse than no comment.
- **Never `cp` a Go binary onto `$PATH`** on macOS arm64 (the kernel kills the copy). `make install` builds straight into the prefix; build test binaries outside `$PATH`.
- **The status schema is a contract.** A new field means the schema document (`plugins/afk/skills/conductor/status-schema.md`) changes first, in the same change.
- **No provider names outside `internal/provider`.**
- **User-facing text is plain language** for a hobbyist: say what happened and what to do next.

## Definition of done (from `docs/PLAN.md` §11)

- `make check` green locally and in CI (Linux).
- New behaviour covered by table tests; decision logic is a pure function with every branch a row.
- No test touches a real session, GitHub, an agent CLI or real usage.
- Nothing outside `internal/provider` names a provider.
- Schemas: any change to a file in §4 updates §4 and, for `status.json`, the schema document, in the same change.
- `README.md` updated for any user-visible command, flag or key.
- `docs/decisions.md` updated when the task decides something the plan didn't, and the plan amended in place ("Amended <date> (T… done): …") when the task changes what a later task should assume.
- Verified against a built binary, not from reading the diff.
