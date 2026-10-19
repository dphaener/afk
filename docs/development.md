# Developing afk

How to build and test afk, and how the code is laid out. `CLAUDE.md` has the rules every change follows, and `docs/PLAN.md` is the plan the work follows.

## Building and Testing

`make check` is the one command that says whether a change is good. It runs, in order: `gofmt` (formatting), `go vet`, the Go tests, the plugin's Ruby parser test, the prompt size check and Node tests (`plugins/afk` workflows and the `plugins/afk-opencode` port), the plugin's hook shell tests, and `install-script-test` (the shell test of the bootstrap script `install.sh`, then `shellcheck install.sh`). It needs Go, Node, `bash` and `jq`; `shellcheck` is used when installed: without it `make check` says so and skips that lint locally, but fails in CI. CI runs the same command.

```bash
# Build ./afk in the repo root
make

# The full check: gofmt, vet, Go tests, plugin node tests, hook shell tests,
# the install script's test
make check

# Or run the pieces on their own
go build -o afk .
go test ./...
gofmt -l .
go vet ./...
node --test plugins/afk/workflows/test/*.test.mjs
(cd plugins/afk-opencode && npm test)
make install-script-test   # install.sh's shell test, then shellcheck

# Test a dry-run (no side effects)
./afk new --dry-run --base main test-session
```

## Making a release

Releases are automatic. A merge to `main` that changes the binary publishes a new release on its own. The release workflow (`.github/workflows/release.yml`) runs `make check`, builds the four archives (Linux and macOS, Intel and Apple silicon) and a `checksums.txt`, and publishes them as a GitHub release named `afk v<version>`.

**What counts as the binary.** A push starts a release only when it changes the binary: a `.go` file outside `scripts/`, `go.mod`, `go.sum`, or anything under `plugins/`, `contrib/`, `internal/`, `.claude-plugin/` or `.agents/`. A push that touches only docs, the plan, scripts or workflows publishes nothing.

**How the version is chosen.** The version comes from the commit messages since the last release:

- No release yet: `0.1.0`.
- A breaking change raises the major. That is a `!` before the colon (`feat!: ...`) or a `BREAKING CHANGE:` line in the commit body.
- A `feat` commit raises the minor.
- Anything else, including a subject with no type, raises the patch.

Merge commits are skipped, and the commits they bring in are read.

**A commit that already carries a tag.** If the merge commit already has a `v*` tag, the push makes no second release. The tag is the release.

**Choosing a version by hand.** Push a tag yourself, and the release workflow publishes that version on the tagged commit:

```bash
git tag v1.0.0 && git push origin v1.0.0
```

To see what a push to `main` would publish without pushing, run `scripts/release-decide.sh <from-sha> <to-sha>` with the commit before the push and the commit after it. It prints the version, or `skip` when nothing in the binary changed or the commit already has a tag.

**Several pushes in a row.** Pushes to `main` release one after another. A release that is waiting is never cancelled by a newer push, so every merge that changes the binary gets its own release.

**Trying the workflow without publishing.** Run it by hand with `dry_run` on. It builds the same files and keeps them as downloadable artifacts of the run, publishes nothing, and writes the version it would publish (or that it would publish nothing) to the run's summary page:

```bash
gh workflow run release.yml --ref <branch> -f dry_run=true
```

Then download the artifacts from that run in the GitHub Actions page. To build the same archives on your own machine, run `scripts/release.sh <version> <dir>` with a version such as `0.1.0` (no leading `v`) and a new, empty folder. It writes the four archives and `checksums.txt` into that folder.

The script refuses to build when the checkout has uncommitted changes, and it lists them, because those changes would make the binaries say `+dirty`. Commit or remove them first.

The release workflow also has a macOS job that runs the darwin/arm64 binary and writes its answer to the run's summary page. On 2026-10-19 the darwin/arm64 binary was found to run without `codesign`, so you do not need to sign the downloaded `afk` file on your Mac.

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
