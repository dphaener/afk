# Developing afk

How to build and test afk, and how the code is laid out. `CLAUDE.md` has the rules every change follows, and `docs/PLAN.md` is the plan the work follows.

## Building and Testing

`make check` is the one command that says whether a change is good. It runs, in order: `gofmt` (formatting), `go vet`, the Go tests, the plugin's Ruby parser test, the prompt size check and Node tests (`plugins/afk` workflows and the `plugins/afk-opencode` port), and the plugin's hook shell tests. It needs Go, Node, `bash` and `jq`. CI runs the same command.

```bash
# Build ./afk in the repo root
make

# The full check: gofmt, vet, Go tests, plugin node tests, hook shell tests
make check

# Or run the pieces on their own
go build -o afk .
go test ./...
gofmt -l .
go vet ./...
node --test plugins/afk/workflows/test/*.test.mjs
(cd plugins/afk-opencode && npm test)

# Test a dry-run (no side effects)
./afk new --dry-run --base main test-session
```

---

## Architecture

The binary is organized into packages:

- **main package** — CLI entry point, subcommand dispatch
- **internal/session** — Session creation logic (branch, worktree, status.json seeding)
- **internal/discover** — Session discovery (reads worktrees and status.json, parses plan/verdict)
- **internal/state** — Parsers for plan-format.md, status.json, and review-verdict.md
- **internal/spice** — Git-spice detection and stack topology (`gs log short --json`)
- **internal/probe** — Tool availability detection (git-spice, worktrunk, notifiers)
- **internal/ui** — Bubble Tea dashboard (TUI, keybindings, panels)
- **internal/governor** — The usage governor's decision: one pure function from a usage reading to `run`, `no_launch`, `pause`, `hold` or `refuse`
- **internal/usagestate** — Reads and writes `usage.json`, the last usage reading, under afk's per-machine state directory
- **internal/inbox**, **internal/projectconfig**, **internal/notify** — The owner inbox behind `afk inbox`, the `afk.toml` and `afk.local.toml` reader (`plan`, `[planning]`, `[ship]` and `[notify]`), and the desktop and ntfy notification senders
- **internal/daemon** — The build loop behind `afk daemon`: one pure function (`Decide`) picks what to do on a pass, and an executor carries it out
- **internal/daemonconfig** — Reads `config.toml`, the machine's one settings file (projects, tick, stall limit, and the dashboard's `[dash]` search folders)
- **internal/daemonstate** — The pause flag, each project's `daemon.json` and the one-daemon-per-machine lock
- **internal/decisionlog** — The append-only record of every decision, `.afk/log/decisions.jsonl`
- **internal/profile** — The owner profile, `profile.toml` beside `config.toml`: loads and checks it, picks the entries for one kind of project, and edits one entry as text so comments survive
- **internal/pluginstall** — `afk plugin install`: writes the embedded plugins and drives each agent CLI's install
- **plugins/afk** — The Claude Code and Codex plugin (skills, hooks, Workflow scripts). `plugins/afk/skills/conductor/status-schema.md` is the binding status.json contract.
- **plugins/afk-opencode** — The OpenCode V2 port of the same plugin; see its `PROVENANCE.md` for the porting glossary

Each package is hermetic with injectable dependencies for testability. Sessions are never created/modified in tests; discovery and state parsing are tested against real git repos and fixture files.

---

## Known gaps

Codex and OpenCode run as optional per-session providers (see [Agent provider](reference.md#agent-provider)); version parity with the Claude Code plugin is the goal, and only Claude Code can be run by the daemon. What is planned next is in [`docs/PLAN.md`](PLAN.md). Small items on the list:

- Real-run output echoing the repo path (avoid the wrong-cwd misfire)
- Additional notifier backends (currently macOS-only: `alerter` for full-fidelity, `osascript` for fallback)
