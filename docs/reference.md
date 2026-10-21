# Command reference

Every `afk` command and flag. For the build daemon see [daemon.md](daemon.md), for the web page [web.md](web.md), for the terminal dashboard [dashboard.md](dashboard.md), and for the settings files [configuration.md](configuration.md).

## Default action — `afk` with no arguments

Running `afk` with no subcommand or options launches the dashboard, equivalent to `afk dash` with no flags.

```bash
afk          # same as: afk dash
```

**Requires a terminal on stdout.** If stdout is piped or redirected (`afk | cat`, `afk > out.txt`), afk prints usage and exits 1 instead of launching. The dashboard is a full-screen Bubble Tea program whose output is meaningless in a pipe, and it does not detect that on its own — it checks stdin only, so without this gate a piped `afk` would start invisibly and sit in raw mode with no visible UI until you interrupt it.

Only a bare `afk` is the default action. `afk dash`'s flags are not top-level flags, so `afk --interval 5s` is an unknown-subcommand error; pass flags to `afk dash` directly.

---

## `afk version` — Show which afk this is

`afk version` prints the version this binary was built from, the commit it was built at, and the day of that commit. `afk --version` prints the same line. Release builds from the release workflow show their version number; a binary built with `make` or `make install` shows `dev` instead.

```bash
afk version
# Release build:   afk 0.1.0 (3f9c2a1b7d4e, 2026-10-17)
# make install:    afk dev (3f9c2a1b7d4e, 2026-10-17)
# Uncommitted work: afk dev (3f9c2a1b7d4e+dirty, 2026-10-17)
```

`+dirty` means the source had uncommitted changes when the binary was built, so the commit alone does not describe what you are running. If the build carries no commit information at all, the line ends in `(unknown)`.

---

## `afk new` — Create a new session

Create an isolated afk session with a dedicated branch and worktree.

```bash
afk new [options] <name>
```

The `<name>` is validated as strict kebab-case (`^[a-z0-9][a-z0-9-]*$`) and becomes the branch name, worktree directory, and plan slug. `plan` and `master-plan` cannot be used as names, because `afk plan` keeps its planning state and its session under them.

**Options:**

- `--base BRANCH` — Stack on (branch off) an explicit base branch. If omitted, an interactive picker shows available branches: in git-spice mode, all local branches (trunk first, then the tracked stack, then every other local branch — so branches checked out in other worktrees, such as prior sessions, appear too); in plain-git mode, the repo's local branches with trunk first. Skipped when stdin is not a TTY or under `--dry-run`, which fall back to the trunk. Propagates to the worktree creation.
- `--description TEXT` — Free-text initial task context for the conductor (stored in `.afk/{name}/description.md`, never interpolated into commands for safety). On an interactive terminal, `afk new` prompts for this when the flag is omitted.
- `--dry-run` — Print the full action list (branch creation, worktree creation, status.json seed, session host launch) without executing anything. Useful for previewing before committing.
- `--no-git-spice` — Force plain-git behavior even if git-spice is detected. Creates the branch via `git worktree add` instead of worktrunk (`wt switch --create`) or git-spice tracking.
- `--scratch [NAME]` — Create a one-off session: no branch, no worktree, no plan — the bare CLI for the chosen provider in the current repo root (`claude` unless `--provider` says otherwise). If `NAME` is omitted, the next available `scratch-N` is allocated. Mutually exclusive with `--base` and `--no-git-spice` (a one-off has no branch, so a base is meaningless). Never prompts for a base or description, even on an interactive terminal.
- `--autonomous` — Run the session in autonomous mode: the conductor works through the plan without stopping to ask you at each gate. Writes an empty marker file at `.afk/{name}/autonomous` (a separate step in the `--dry-run` list) and appends `--autonomous` to the conductor's argv (`claude "/afk:go --session {name} --autonomous"`). The marker is what lets a dashboard restart, which re-runs `/afk:go --session {name}` without the flag, keep the session autonomous. Mutually exclusive with `--scratch` (a one-off has no conductor to be autonomous; exits 2 with a usage error). Requires an afk plugin with autonomy support; an older plugin is expected to ignore the flag and the marker and run in default mode, so check the plugin version if a session still asks you questions. Under `--provider codex` the child is `codex "$afk:conductor --session {name} --autonomous"` instead; under `--provider opencode` it is `opencode --standalone --prompt "/afk-go --session {name} --autonomous"`.
- `--provider claude|codex|opencode` — The agent CLI the session host runs. Default `claude`. `codex` runs the OpenAI Codex CLI and is autonomous-only: it requires `--autonomous` or `--scratch`, otherwise `afk new` exits 2 with `afk new: --provider codex requires --autonomous or --scratch: Codex sessions run autonomous-only`. An unknown name exits 2 before anything is created. Codex sessions also need the afk plugin installed in Codex (`afk plugin install --target codex`); `afk new` checks this with `codex plugin list` as a non-fatal preflight and prints a warning with the install steps when the check fails. `opencode` runs the OpenCode CLI and is not autonomous-only, so interactive and autonomous sessions both work. OpenCode sessions need the afk plugin in `~/.config/opencode/plugins/afk/` (`afk plugin install --target opencode`); `afk new` checks it with `opencode plugin list` as a non-fatal preflight, the same way as codex. The provider is recorded per session (see [Agent provider](#agent-provider)).
- `--no-dash` — Create the session and its host, but do not open the dashboard afterwards; print a one-line pointer instead. This is what the dashboard's own in-TUI creation form passes, so creating a session from the dashboard cannot recursively launch a second one.
- `--project PATH` — Create the session in the repo at `PATH` instead of the current working directory's repo. `PATH` must exist, be a directory, and be a git repo root; `~/` is expanded. See [Multi-project support](dashboard.md#multi-project-support).

**Launch disposition:** `afk new` always spawns a **detached session host** for the new session (the session runs regardless of how you got there — see [Session model](#session-model)). What differs is whether this terminal is handed to the dashboard, decided by whether both stdin and stdout are interactive terminals — **no tmux window is ever created**:

- **TTY** (interactive terminal): hands the terminal to `afk dash --open <name>`, so you land in the dashboard with the new session live in an embedded pane. Quitting only detaches; the session keeps running.
- **Non-TTY** (piped/scripted, e.g. `afk new x </dev/null | cat`) or **`--no-dash`**: launches no dashboard. Prints a one-line pointer instead:
  ```
  session <name> created and running — open `afk dash` to attach
  ```

**Example:**

```bash
afk new --base main feat-awesome-feature
# Interactive picker (if git-spice is detected):
#   * 1) main
#     2) feat/prep-data
#   Choice [default main]: 1
# Creates: branch feat-awesome-feature, worktree .claude/worktrees/feat-awesome-feature, status.json,
# spawns the session host, and lands in the dashboard with the session live in an embedded pane.

afk new --no-dash scratch-session
# Creates the session and its host; prints a one-line pointer instead of opening the dashboard.

afk new headless-session </dev/null | cat
# Non-TTY: same as --no-dash — the session runs, the pointer is printed, no dashboard.
```

**What happens:**

1. Validates the session name as kebab-case
2. Detects git-spice (via `git rev-parse --verify refs/spice/data`)
3. Displays an interactive base-branch picker (unless `--base` was supplied, stdin is not a TTY, or `--dry-run` is set)
4. Creates a branch (via worktrunk if present, else git-spice if present, else plain git)
5. Creates a worktree at `.claude/worktrees/{name}` (or `.claude/worktrees/{name}-2`, `-3`, etc. to avoid collisions)
6. Seeds `.afk/{name}/status.json` with session state (phase, session_id, branch, base_branch, etc. — `tmux_window` is always seeded empty; no code path in `afk new` ever writes a tmux window ID)
7. Writes the description to `.afk/{name}/description.md` (if provided via flag or prompt, after trimming trailing newlines)
8. Spawns a detached session host process that runs the selected provider's CLI: `claude "/afk:go --session {name}"` by default, `codex` under `--provider codex`, or `opencode --standalone --prompt "/afk-go --session {name}"` under `--provider opencode`. For a one-off (`--scratch`) the CLI runs bare, with no conductor
9. On a terminal: hands the terminal to `afk dash --open <name>`, so you land in the dashboard with the session live in an embedded pane. Pass `--no-dash` (or redirect stdio, as a script would) to get a one-line pointer instead
10. Prints the session creation status and exits

**Degradation (missing tools):**

- **git-spice absent**: The picker offers the repo's local branches with trunk first; trunk is resolved from `origin/HEAD`, then a `main`/`master` match, then the current branch. Non-interactive runs take the trunk with no prompt.
- **worktrunk absent**: Uses `git worktree add` instead of `wt switch --create`. With git-spice present, the branch is created through `gs branch create` and tracked in the stack when the base is tracked (or can be tracked from its session's status.json); otherwise it is created as a plain git branch with a warning, the same rule as the worktrunk-present case below.
- **worktrunk present** (with git-spice also present): Branches are created via `wt switch --create` and are tracked in the git-spice stack best-effort with the base branch, enabling stacking via the base picker. An untracked base is tracked first via `gs branch track --base <parent>` when the parent is known from the base session's status.json and itself tracked; otherwise the branch is left untracked (created by worktrunk or plain git when worktrunk is absent) with a warning the CLI prints and the dashboard displays as a toast — never an error, never a guessed parent.
- **Codex CLI or afk plugin absent** (`--provider codex` only): The preflight runs `codex plugin list` and never blocks creation. When it finds no installed `afk@` entry, `afk new` prints a preflight warning with the install steps and continues.
- **OpenCode CLI or afk plugin absent** (`--provider opencode` only): The preflight runs `opencode plugin list` and never blocks creation. When the check fails (no `afk` row, or the CLI cannot run), `afk new` prints a preflight warning naming `~/.config/opencode/plugins/afk/` and `opencode plugin list` and continues.
- **stdin/stdout not terminals**: Skips the dashboard hand-off and prints a pointer instead (the session is still created and running; reach it with `afk attach <name>` or `afk dash --open <name>`)

**Important:** `afk new` operates on the repo of the **current working directory** by default. Pass `--project PATH` to create a session in a different repo without `cd`-ing there first. (Real-run output does not yet echo which repo is being acted on — recommended as a v1.1 improvement.)

---

## `afk clean` — Reconcile and clean up sessions

List all discovered sessions, or clean a specific session.

```bash
afk clean                # List sessions with phase
afk clean [options] <name>  # Archive and remove the session's worktree
```

**Options:**

- `--force` — Skip the phase gate. Normally `afk clean <name>` refuses to clean sessions not in `done` or `failed` phase (to prevent accidental loss of active work). `--force` bypasses this check. Worktree-side faults (broken entries) do not require this flag — they clean without it. When a broken entry shares its name with a real session (scratch included), `afk clean <name>` always resolves to the real session — the broken entry loses that name and is cleaned from the dashboard instead, where the confirmation is bound to the exact row you selected. A broken entry whose name is *unambiguous* is still nameable and cleans normally; only the collision de-prioritizes it.
- `--delete-branch` — Delete the session's branch after cleaning. By default, the branch is kept for history and future reference. Pass this flag to opt in to deletion. A branch already merged into the base deletes immediately; an unmerged branch requires interactive confirmation on a TTY and is refused otherwise (no silent deletion from a script). One exception to "merged deletes immediately": if the worktree was dirty, the snapshot commit lands *before* the delete-time re-check, so the branch is no longer merged by then and is kept — reported in the summary rather than deleted silently. Has no effect (and errors) on a scratch session or a session without a worktree of its own (a standalone `/afk:go` session, or a worktree whose branch no longer matches the session name), neither of which has a branch. When the branch is tracked by git-spice, the delete is coordinated via `gs branch delete --no-prompt --restack=none` to avoid rebasing branches you didn't name.
- `--project PATH` — Operate on sessions in the repo at `PATH` instead of the current working directory's. `PATH` must exist, be a directory, and be a git repo root; `~/` is expanded.

**Listing (no arguments):**

Prints all discovered sessions (with phase, awaiting-input status, and task progress) and any broken sessions (worktrees missing their directory, or missing a status.json).

**Cleaning a session:**

For a planned session (one created via `afk new <name>`):

1. Checks the phase gate (must be `done`, `failed`, or `--force` passed)
2. Snapshot-commits any dirty tracked changes with message `chore(afk): snapshot session '{name}'`
3. Archives `.afk/{name}/` (gitignored notes, status, verdict) to `~/.local/share/afk/archive/{repo}-{name}-{date}/` (or `{repo}-{name}-{date}-2`, `-3`, etc. if same-day re-archives occur)
4. Verifies the archive byte-for-byte and re-reads every file to confirm
5. Removes the archived source directory
6. Removes the worktree via `git worktree remove`
7. Prunes git's worktree registry
8. **Branch deletion** — By default the branch survives for history and future reference. Pass `--delete-branch` to delete it. A merged branch deletes immediately; an unmerged branch requires interactive confirmation on a TTY (refused without one). A merged-but-dirty session is the exception: its snapshot commit advances the branch past the base, so the delete-time re-check finds it unmerged and keeps it. If the branch is tracked by git-spice, deletion is coordinated via `gs branch delete --no-prompt --restack=none`, and the summary names any upstack branches the delete retargeted so you can restack them deliberately.

For a one-off session (created via `afk new --scratch`):

- No phase gate (scratch sessions have no plan, so there is no work to lose)
- No archive (only `host.json` and `host.log` are present under `.afk/{name}/`; the host's socket lives outside it, under `os.TempDir()/afk-{uid}/`; nothing of permanent value)
- Stops the running session host
- Removes `.afk/{name}/` completely
- **Never** removes any worktree or branch (a scratch session has neither)

For a broken session (missing worktree, or worktree without status):

- No phase gate — these are cleanable without `--force`
- Stops the running session host if one exists
- For a missing-directory worktree: skips archive (nothing to save) and removes only the git registry entry
- For a worktree without an afk session: if clean, removes the worktree; if dirty, refuses to commit or remove a stranger's work
- Branch handling as above for planned sessions

For a session without a worktree of its own (a status directory whose carrying worktree is checked out on a different branch — a standalone `/afk:go` session on a checkout not on the plan's branch, or a worktree whose branch was switched):

- Phase gate applies like a planned session (must be `done`, `failed`, or `--force` passed)
- Stops the running session host
- Archives `.afk/{name}/` to `~/.local/share/afk/archive/` and verifies the archive, then removes it
- **Never** touches the worktree or any branch (the session had no worktree of its own)
- Leaves any `.afk/plans/{name}.md` in the carrier worktree in place (it is the user's checkout)
- `--delete-branch` is refused with an error (the session has no branch of its own)

When a stranded entry and a broken worktree share a name, `afk clean {name}` cleans the stranded entry first (it runs no git, so it is the safer path) and reports that another entry with that name remains, so a second `afk clean {name}` reaches the fault.

The dashboard's `ctrl+] c` routes to the matching path above by session kind, with kind-specific confirmation prompts.

**Example:**

```bash
afk clean
# Lists:
# Phase: planning  session-a              [1/3 tasks]
# Phase: executing session-b              [2/3 tasks] awaiting input
# Phase: done      session-c              [3/3 tasks]

afk clean session-a
# Checks: phase is planning (terminal gate: NOT done/failed)
# Error: phase 'planning' is not terminal; pass --force to clean anyway

afk clean --force session-a
# Snapshot-commits, archives, removes worktree, prunes, exits 0
# Output: Kept: branch/archive  Removed: worktree
```

---

## `afk attach` — Attach to a running session

Reattach the current terminal to a running session's PTY. The session must be created and its host running.

```bash
afk attach [options] <name>
```

Attaches directly without opening the dashboard. Only one client can be attached at a time; if another client is already attached, that client is disconnected.

**Options:**

- `--project PATH` — Look for `<name>` in the repo at `PATH` instead of the current working directory's. `~/` is expanded.

**Detaching:**

Press `ctrl+\` to detach and return to your shell. The session keeps running in the background.

**Mouse:**

Scrolling and clicking reach the session, same as in the dashboard's pane. `ctrl+g` hands the mouse back to your terminal when you want its own selection, and again to return it. While the terminal has it the title line says so, since there's no help line here to put it on.

**Examples:**

```bash
# Attach directly
afk attach my-session

# Attach via the dashboard (same effect, but with session list visible)
afk dash
# press enter to focus the highlighted running session's agent

# Open a named session's pane on dashboard start
afk dash --open my-session
```

**Exit codes:**

- `0` (ExitDetached) — Detached normally with `ctrl+\`
- `10` (ExitStolen) — Another client attached to the session while this one was connected
- `11` (ExitSessionEnded) — The session ended (the host process exited)
- `12` (ExitNotRunning) — The session is not running (host not found or dial timeout)
- `13` (ExitDropped) — The host closed the connection without the session ending, usually because this client fell too far behind its output. The session may still be running; attach again
- `1` — Other errors (invalid arguments, permission issues, etc.)

---

## `afk notify` — Send a desktop notification

Send a desktop notification about a session event (used primarily by hooks). Desktop notifications are **macOS-only**.

```bash
afk notify --plan <name> --message <text> [options]
```

**Options:**

- `--plan NAME` — Session/plan name (required)
- `--message TEXT` — Notification body (required)
- `--repo TEXT` — Repository name (optional; derived from git common dir if omitted)
- `--dry-run` — Print the assembled toast (title, subtitle, body, group, sound, backend, and the resolved pane, client-tty and terminal-app) without sending the notification
- `--check` — Probe for notifier availability and print capability report; exits 0 when a notifier is present, non-zero otherwise. Used by hook shims to determine whether `afk notify` should be called.

**Behavior:**

Spawns a detached watcher that delivers the notification via `alerter` (full-fidelity: sound, per-session replacement, click-to-focus) or `osascript` (reduced-fidelity fallback: formatting only). Returns immediately (~20ms) without blocking the caller. Toast format:

- **Title**: `afk · {repo}` (or `afk` if repo is empty)
- **Subtitle**: `{session}`
- **Body**: The message text (no prefix)
- **App icon**: afk's app icon (the same picture the installed web page uses) if available, omitted silently if missing
- **Sound**: Backend-specific (`default` for alerter, `Ping` for osascript)

**Click-to-focus:** When `alerter` is present and a `afk dash` is running (which records pane metadata for every project it knows about, not just the one it launched in), the notification includes a Focus button. Clicking it switches the dashboard to the session's project first if that project isn't already active, then switches the tmux client to the dashboard's pane and focuses the right side on the named session. A running session is connected and its pane takes your keystrokes; a stopped one shows its overview with a hint to press `ctrl+] r` (a click never restarts a host). Without a running dashboard the toast still appears with correct formatting, sound, and grouping — it just has no Focus button. This is correct by design: a button that cannot act is worse than no button.

**Degradation:** If no notifier is found, exits 0 silently (hook contract: notification absence is not an error). Always exits 0, even on failure (broken pane metadata, stale PID, etc.) — failed notifications do not fail the caller.

**Examples:**

```bash
# Send a notification (typical hook use)
afk notify --plan my-session --message "Needs input to continue"

# Check notifier availability (exit 0 = present, non-zero = absent)
afk notify --check

# Preview the assembled toast without sending
afk notify --plan my-session --message "Test" --dry-run
```

## `afk install` — Set up this machine

```bash
afk install [--dry-run] [--yes]
```

Guided setup for a Linux machine (Ubuntu or another system with `apt-get` and systemd) or a Mac with Homebrew (the background service is a launchd LaunchAgent). It looks at the machine, shows the twelve steps as a checklist, and does the ones that are not done yet, checking the machine again after each one.

- **On a Mac,** the safety screen always asks you to type `I accept the risk`, because a Mac is a computer you use every day. The packages step runs `brew install` for the missing tools. With no Homebrew, the step tells you to install it from brew.sh and run `afk install` again: afk never installs Homebrew and never uses `sudo` on a Mac. Run `afk install` from a desktop login on the Mac, not over SSH, because the service is loaded into your desktop session.

- **The safety screen comes first, every time.** afk runs agents with their permission prompts turned off, so it belongs on a machine set aside for it. On a virtual machine or a server, a key press accepts. On a computer you use every day, or one afk cannot place, you must type `I accept the risk`. Nothing changes before you accept.
- **Each step asks first.** The default answer is yes; answering no stops the run and undoes nothing. Commands that need to talk to you (`sudo`, the agent's sign-in, `gh auth login`, `afk web password`) get the terminal until they end.
- **A step that fails stops the run.** The steps before it stay done, and the screen says what to do. Run `afk install` again to go on.
- **Running it again checks and fixes.** Every step is checked against the machine, and only what is missing is done. When everything is in place it says so and changes nothing.
- **The last screen shows the web address.** You choose this machine only (`http://127.0.0.1:4253`), every address on the machine, or Tailscale. The choice is saved as `[web] bind` in `config.toml`.
- **The service file.** On Linux afk writes `~/.config/systemd/user/afkd.service`, with a first line that marks it as afk's. On a Mac it writes `~/Library/LaunchAgents/io.github.dphaener.afk.plist`, marked by the comment on its second line (a plist cannot start with `#`), and loads it with `launchctl bootstrap`. A file without the marker is shown and replaced only if you say yes (never under `--yes`).
- `--dry-run` prints the plan, one line per step, and changes nothing. It needs no terminal, so it works in a script or CI.
- `--yes` takes the default answer to every question. The safety screen is still shown and still needs your answer, and the web page is set to this machine only.
- Anything else needs a terminal: without one, `afk install` exits 1 and says so.

Exit codes: 0 everything is in place, 1 a step failed or was stopped (or there was no terminal), 2 the command was written wrongly. [`contrib/README.md`](../contrib/README.md) has the by-hand way to set up the service.

## `install.sh` — Get afk on a new machine

```bash
curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh
```

`afk install` cannot install afk itself, so this script, at the top of the repository, does that first. It refuses to run as root, picks the release archive for the machine (Linux or macOS, `amd64` or `arm64`), downloads it with its `checksums.txt`, and stops unless the checksum matches. Only then does it put `afk` in `~/bin` (never using `sudo`), check that it runs, and start `afk install` with its input from the terminal. If `~/bin` is not on your `PATH` it prints the line to add. On a Mac it runs `codesign --force --sign -` on the binary only if the binary does not run without it.

| Variable | Effect |
|---|---|
| `AFK_VERSION` | Install this release (`1.2.3` or `v1.2.3`) instead of the latest. |
| `AFK_NO_INSTALL=1` | Stop once `~/bin/afk` is in place; do not start `afk install`. |

Exit codes: 0 afk is in place (and `afk install` finished, or was not wanted, or there was no terminal to run it in: run `~/bin/afk install` from a terminal), 1 nothing was installed because of a refusal or a failure (root, an unsupported machine, no release found, a failed download, a checksum that did not match, or a binary that would not run), and otherwise the exit code of `afk install`. The message says which, in plain words.

## `afk plugin install` — Install the agent plugin

Install the afk plugin into the agent CLIs on this machine.

```bash
afk plugin install [--target claude,codex,opencode|all] [--dry-run] [--force]
```

Without `--target`, it lists the agent CLIs it finds on `PATH` and asks which ones to install into. Nothing is pre-selected; confirming an empty selection installs nothing. Asking needs a terminal, so in a script or CI pass `--target` (exit 2 otherwise). An explicit `--target` is taken as your choice and installs without asking. The plugin installed is the one embedded in this binary, so it always matches the status.json, plan, and verdict formats this binary reads. Re-run it after upgrading afk.

| Target | Source in this repo | What it does |
|---|---|---|
| `claude` | `plugins/afk` | Writes a local marketplace to `$XDG_DATA_HOME/afk/marketplace` (default `~/.local/share/afk/marketplace`), then runs `claude plugin marketplace add <dir>` and `claude plugin install afk@afk`. Claude Code loads the plugin in place, so a re-install takes effect at the next session start or `/reload-plugins`. |
| `codex` | `plugins/afk` | Same marketplace directory, then `codex plugin marketplace add <dir>` and `codex plugin add afk@afk`. |
| `opencode` | `plugins/afk-opencode` | Copies the OpenCode port to `$XDG_CONFIG_HOME/opencode/plugins/afk` (default `~/.config/opencode/plugins/afk`), which OpenCode loads on start. Prints the `opencode.jsonc` permission entry to add if your config doesn't already grant read access to that directory. |

**Options:**

- `--target LIST` — Comma-separated `claude`, `codex`, `opencode`, or `all`. Skips the prompt. A named target whose CLI is missing fails on its own; the others still install (`opencode` needs no CLI, only the directory).
- `--dry-run` — Print the directories it would write and the commands it would run, and change nothing. It still runs the read-only `plugin list` to report conflicts.
- `--force` — Replace a plugin directory afk did not write. The existing directory is moved to a timestamped backup (OpenCode: `~/.config/opencode/plugin-backups/`), never deleted.

**Safety:** afk marks the directories it writes with a `.afk-install` file and replaces them on every install, so don't edit them. A directory without the marker, such as a hand-installed OpenCode plugin, is refused unless you pass `--force`. If another afk plugin is installed (for example `afk@claude-marche`), it prints a warning with the uninstall command. It never uninstalls anything itself; two afk plugins register the same skills, so remove the old one.

**Exit codes:** 0 when every selected target installed (or nothing was selected), 1 when any target failed or the prompt was cancelled, 2 on a flag error or a missing `--target` without a terminal.

## `afk plan` — Plan a project with your agent

Start an attended agent session that takes your idea to a first draft of the master plan. The session runs the `/afk:master-plan` skill: it asks what you want to build, asks its questions in rounds, proposes how to cut the work into tasks, writes `docs/PLAN.md` and checks it with `afk plan lint`. The dashboard can start a new plan too, with its **start a new plan** action (see [`afk dash`](dashboard.md#afk-dash--launch-the-dashboard)).

```bash
afk plan [--project DIR] [--provider NAME] [--dry-run]
afk plan extend [--project DIR] [--provider NAME] [--dry-run]
afk plan amend [--project DIR] [--provider NAME] [--dry-run] [--yes] "<what>"
afk plan reconcile [--project DIR] [--provider NAME] [--dry-run] FILE
afk plan fix [--project DIR] [--dry-run] [--provider NAME] [--yes] "<what is broken>"
afk plan status [--project DIR]
afk plan advance [--project DIR]
```

- **It needs a terminal.** The session asks you questions, so with no terminal on stdin and stdout `afk plan` starts nothing and exits 1.
- **An empty folder is turned into a git repository.** A folder that is not a git repository yet gets `git init` and nothing else. The session writes the `.gitignore` itself, in the draft phase.
- **A project that already has a plan gets a question, not a new plan.** An open planning session is always resumed. With none open, `afk plan` on a terminal asks which change you want (type `1`, `2`, `3` or `4`, or the word `extend`, `amend`, `reconcile` or `fix`), and any other answer starts nothing and exits 1. Without a terminal it lists the four commands and exits 1. A session that reached `review` is finished: when a new change starts, the finished session's files are moved to `.afk/plan/archive/<time>/` and afk says so. A new change session ends a finished change session's agent first when that session's summary is saved and it is waiting for you; `--dry-run` says it would end the agent. If that agent is still replying, or the earlier session is a new plan that is not approved yet, `afk plan` still refuses and says to run `afk clean master-plan`. If ending the agent fails, nothing is started and afk says what to run.
- `--project DIR` — The project to plan. Defaults to the current directory. From inside a session's worktree it reaches the project's main checkout, where `.afk/plan/` lives.
- `--provider NAME` — The agent to run: `claude`, `codex` or `opencode`. Planning sessions work with the default agent only for now; any other prints that and exits 1. An unknown name exits 2.
- `--dry-run` — Print what would happen (the project, whether it would run `git init`, whether the session would start, resume or be reattached, and the command it runs) and change nothing.

**Changing a plan that exists.** `extend`, `amend`, `reconcile` and `fix` start a planning session that changes the plan instead of writing one. They take `--project`, `--provider` and `--dry-run`, and need a terminal. Each refuses a project with no plan (run `afk plan` first) and refuses while another planning session is open (run `afk plan` to go back to it). The session changes the plan only through `afk plan patch`, `insert` and `depend` (see below), so every change is checked before it is written, and it lists each change in `.afk/plan/changes.md`. The same four can be started from `afk dash` by name, from its actions dialog; see [`afk dash`](dashboard.md#afk-dash--launch-the-dashboard), where the rows are described. The dashboard asks for the same sentence or path and refuses for the same reasons. The web page's project screen starts the same four: its **Change the plan** section has a row for each, and each opens a confirm before anything starts (see [the web page](web.md#the-web-page)).

- **`afk plan extend`** adds a milestone. For when the project has grown a new part. The session asks what the milestone is for, then follows the phases `brief`, `survey`, `rounds`, `slices`, `draft`, `check`, `review`. It is the only change with a critic: `critic.md` must exist before `check` passes.
- **`afk plan amend "<what>"`** adds, splits or replaces one task. For a small change you can put in a sentence; the sentence is saved as the brief. The phases are `brief`, `survey`, `draft`, `check`, `review`, with no questions round. The session shows you the entry and asks you to confirm it before adding it; `--yes` skips that confirmation. `--yes` does not remove the need for a terminal.
- **`afk plan reconcile FILE`** brings the plan in line with a document, such as a design system. `FILE` must be a file inside the project folder (a relative path is looked for in the folder you typed it in, then in the project). The phases are `brief`, `survey`, `rounds`, `draft`, `check`, `review`; the questions round may have no question at all. A reconcile that finds nothing to change still writes one line to `changes.md`. When the document is a design system and it disagrees with the plan in ways that need your decision, the follow-up prompts are saved as `docs/design-followup-<n>.md`, numbered from 1.
- **`afk plan fix "<what is broken>"`** reproduces the bug, finds its cause and adds one `(fix)` task to the plan's `Fixes` milestone. The first fix creates that milestone. The session shows you the entry and waits for your yes; `--yes` skips the wait. The daemon builds the task next, ahead of other ready tasks. The phases are `brief`, `survey`, `draft`, `check`, `review`, with no questions round and no critic. It needs a terminal, as the other change commands do, and the session changes no code file, only the plan.

A change session is finished when it reaches `review`: its changes are already in the plan. With `[planning] via_pr = true` (see `afk plan patch` below) they are in a pull request until it is merged. `afk plan status` shows the session's mode and phase.

**Leaving and coming back.** Leave with ctrl-\. The session keeps running in the background and what it has saved is kept in `.afk/plan/`. Run `afk plan` again to come back: it reattaches to a running session, or starts a fresh agent on the saved state if the session has stopped. Opening it from the dashboard does the same.

**`afk plan status`** prints the session's mode, its phase, what it is waiting for, and one `Problem:` line for each damaged file. At `review` it also prints the summary the session saved in `.afk/plan/summary.md`, so the summary can be read after the session has ended. It only reads. With no planning session it says so and tells you to run `afk plan`.

**`afk plan advance`** moves the session to the next phase once the current phase's work is saved, and says what is missing when it is not. It is the command the planning session itself runs when a phase is done; you rarely need it. The agent never writes the phase by hand, so it cannot skip a step.

The nine phases, in order:

1. `brief` — your request is saved word for word.
2. `survey` — the session reads the folder and your profile.
3. `model` — you settle what the project's terms mean.
4. `rounds` — questions in rounds, each with options and a recommendation where it is your choice.
5. `slices` — you confirm how the work is cut into tasks.
6. `draft` — the plan is written.
7. `check` — `afk plan lint` reports no errors and a critic has reviewed the draft.
8. `design` — only for a project with an interface (you answer the question `r1-interface` with `yes`; with `no` the session goes from `check` straight to `review`). It leaves behind `docs/design-brief.md`, which tells you what to ask a design tool for, and a plan task called `Design system (owner)` in the first milestone, with the tasks that build the interface waiting for it. The daemon then puts that task in your inbox; follow its steps, which end in `afk plan reconcile docs/design-system.md`.
9. `review` — you read the draft. When it is a new plan, the session ends here with the checker's summary, and you approve it with `afk plan approve` (below).
10. `approved` — written by `afk plan approve` and by nothing else, last of all its steps. A session at this phase has a project the daemon is building.

When a new plan reaches `review`, afk also notes it on a list of plans waiting for approval (so the web page can show it, see [the web page](web.md)). Nothing is sent to your phone for it. A failure to write the entry is a one-line note and never stops the phase.

**`afk plan approve`** is your "go". Run it in the project, or give `--project DIR`. The dashboard's **approve the plan** action (type `approve` in `afk dash`, see [the actions dialog](dashboard.md#afk-dash--launch-the-dashboard)) runs the same steps behind a confirm. The web page can approve too: a new plan at review gets **Approve the plan…** on its plan page (see [the web page](web.md)), behind a confirm that names the project, the plan's path and the counts, and it runs the same steps in the same order.

```bash
afk plan approve [--project DIR]
```

It refuses, and changes nothing, when there is no planning session; when the session is a change session (`extend`, `amend`, `reconcile`, `fix`: its changes are already in the plan); when the session is not at `review`; when the checker reports errors in the plan (run `afk plan lint`); when the daemon could not build from the project as it is; when the folder is not a git repository; when the plan is outside the project folder; or when afk's `config.toml` cannot be read. Each refusal says why and ends with "Nothing was changed."

Otherwise it does five things, in this order, and prints a line for each one that changed something:

1. Writes `afk.toml` with one line, `plan = "<where the plan is>"`, if the project has none. An existing `afk.toml` is never overwritten.
2. Commits the plan and the project's documents: the plan, `afk.toml`, `.gitignore`, `CONTEXT.md`, `docs/design-brief.md`, and the agent instruction files (`AGENTS.md`, `CLAUDE.md`), whichever exist. Nothing else in the checkout is staged, and git hooks run. The commit message is `docs(plan): Approve the master plan`.
3. Creates the task ledger (`.afk/build/ledger.json`), with no task done.
4. Adds the project to the projects the daemon builds, as `afk project add` does.
5. Marks the planning session `approved`, and takes the plan off the list of plans waiting for approval.

Once the planning session is marked `approved`, approve also ends the planning session's agent, the `master-plan` session, the way `afk clean master-plan` does. It stops the agent and prints "The planning session has ended." It keeps `.afk/plan/` and the `master-plan` folder, so nothing you planned is lost. A session that is already gone is fine. If the agent cannot be stopped, the plan is still approved and the command still exits 0; a `Note:` line on stderr says to run `afk clean master-plan --project <folder>` by hand.

Run it again whenever you like. Each step is done only if it is not done already, so a second run after a full one changes nothing ("The plan is already approved"), and a run that stopped half way (git refused the commit, the disk was full) is finished by running it again. When a step fails, the message names it, says what was done in this run and what is left, and the plan stays at `review` until the last step is done.

When it pushes: after a commit it pushes the current branch to `origin` (never forced). It does not push when the repository has no `origin` or is on no branch, and a failed push is a note with the command to run by hand, never a failure of the approval. With `[planning] via_pr = true` in `afk.toml` it does not push at all, since a push to the base branch can be a deploy: the commit goes out with the first task's pull request.

A running `afk daemon` picks the project up within a few seconds, and starts its first ready task without a restart. The closing lines say whether a daemon appears to be running or tell you to start one with `afk daemon`.

**Where the files are.** The session's state is in `.afk/plan/` in the project's main checkout: `state.json`, `brief.md`, `survey.md`, `questions.jsonl`, `slices.md` and `critic.md`, and in a change session also `changes.md` (the changes made, one line each; its content is the gate for leaving `draft`) and, for `amend --yes` or `fix --yes`, an empty file named `yes`. `archive/` holds the files of earlier, finished sessions (see `docs/PLAN.md` section 4.8). The agent itself runs as a session named `master-plan`, with its own folder `.afk/master-plan/`, so it shows in the dashboard under that name. Cleaning that session does not delete the planning state.

**Exit codes.** `afk plan` and the four change commands (`extend`, `amend`, `reconcile`, `fix`): 0 you left the session (or the dry run printed), 1 the session could not be started here (no terminal, no plan to change, a session already open, an agent that cannot plan, or a project with a plan and no mode chosen), 2 the command was written wrongly, 10 the session was opened somewhere else, 11 the session ended, 12 it is not running. `afk plan status`: 0 printed, 1 one of the session's files is damaged, 2 bad usage. `afk plan advance`: 0 moved, 1 refused, 2 bad usage. `afk plan approve`: 0 approved, or already approved; 1 refused, or a step failed; 2 bad usage.

## `afk plan lint` — Check a master plan

Check a master plan the way the daemon reads it, and list everything wrong with it, not only the first problem. Run it before you leave a plan for the daemon to build.

```bash
afk plan lint [--project DIR] [--json] [--summary] [FILE]
```

- `FILE` — The plan to check. `FILE` and `--project` cannot be combined. With neither, it checks the plan `afk tasks` reads (`plan` in `afk.toml`, `docs/PLAN.md` by default).
- `--project DIR` — The project whose plan to check. Defaults to the current directory.
- `--summary` — Print only the six counts (milestones, tasks, owner tasks, open questions, errors, warnings).
- `--json` — Print one JSON object with `file`, `findings` (each with `level`, `line`, `task`, `rule` and `message`) and `summary`.

An error is something the daemon would refuse or misread: no tasks, a task heading written wrongly, two tasks with the same id, a task with no `Depends on` line, a dependency on a task that is not in the plan, tasks that depend on each other in a loop, an open question that waits on a task that is not in the plan, and a task whose title carries both `(fix)` and `(owner)` (`fix-and-owner`: a fix task is built by an agent, an owner task is yours to do, so keep one marker). A warning is something a reader would miss: a `Depends on` line with no task id in it, a task with no Goal, Scope or Acceptance line, a missing required section, and an open question that names no task. Warnings never fail the command. The daemon uses the same table: an open row that names the next ready task holds it until the row is answered (see **Open questions** under the build loop). See `plugins/afk/skills/master-plan/plan-format.md` for the full format and every rule.

When the daemon cannot read a project's plan, the inbox item it raises lists every error `afk plan lint` found (at most 20, then a line saying how many more).

A real run, on this project's plan:

```
$ afk plan lint --summary docs/PLAN.md
Milestones: 6
Tasks: 29
Owner tasks: 1
Open questions: 9
Errors: 0
Warnings: 14
```

**Exit codes:** 0 when the plan has no errors (warnings are allowed), 1 when it has at least one error or the file could not be read, 2 when the command was written wrongly (for example `FILE` together with `--project`).

## `afk plan patch` — Change a plan by a guarded patch

Change the master plan one checked step at a time. The planning session uses these commands to extend, amend, reconcile or fix a plan, and you can use them to edit a plan by hand with the same checks.

```bash
afk plan patch [--project DIR] --old FILE --new FILE [--message TEXT]
afk plan insert [--project DIR] (--after ID | --milestone N | --new-milestone) --entry FILE [--message TEXT]
afk plan depend [--project DIR] [--message TEXT] TASK ON
afk plan next-id [--project DIR] (--milestone N | --after ID)
afk plan next-followup [--project DIR]
```

- `patch` replaces the text in the `--old` file with the text in the `--new` file. The old text must be in the plan exactly once; none or two places is a refusal that says so.
- `insert` adds the task in the `--entry` file directly after task `ID` (`--after`), or at the end of milestone `N` (`--milestone`), or adds the milestone in the file after the last one (`--new-milestone`). Give exactly one of the three; two or none is exit 2. The file holds one task, or one milestone for `--new-milestone`.
- `depend` makes `TASK` wait for `ON` by adding `ON` to its `Depends on` line. If it already waits, nothing is changed. It refuses a `Depends on` line that is prose or runs onto a second line; use `patch` on that line.
- `next-id` prints the id a new task should take and nothing else: the next number in milestone `N`, or with `--after ID` the first free letter after that task (`T2.2a`, then `T2.2b`). A gap in the numbers is never filled and an id is never used twice. It only reads, and it refuses a milestone the plan does not have.
- `next-followup` prints where the next design follow-up prompt goes, for example `docs/design-followup-1.md`: the path, relative to the project folder, of the first number not yet used in `docs/`. It needs no plan. Exit 0 when it printed, 1 when the project folder or its `docs` folder could not be read, 2 when the command was written wrongly.
- `--message TEXT` — The commit subject, one line. Without it a default such as `docs(plan): Patch the master plan` or `docs(plan): Add T1.1a` is used.
- `--project DIR` — The project whose plan to change. Defaults to the current directory. The plan is the one named by `plan` in `afk.toml` (`docs/PLAN.md` by default). The `--old`, `--new` and `--entry` files are read relative to the folder you typed the command in.

The rules, in plain words:

- Nothing is written if the changed plan would have an error that `afk plan lint` does not report on the plan now. A plan that already has errors can still be patched. The refusal lists the new findings, with the line numbers the changed plan would have had.
- A task whose status is `running` or `shipping` can only be given a line that starts `Amended <date>:` at the end of its entry (and lines indented under it). Any other change to its entry is refused. A task with any other status can be changed.
- A task id is never removed or renamed. A patch that would make an id disappear is refused.
- A refusal changes nothing, and its last line is `Nothing was changed.`

When a change is accepted, only the plan file is committed, never anything else that is changed in the folder. It is committed when git already tracks the plan and the plan had no uncommitted changes, and the commit is pushed when the repository has an `origin`. A plan that is not committed yet (a first draft), or a folder that is not a git repository, is changed in place and not committed; a line says so. A push that fails is a note with the command to run, not a failure, because the plan on this machine is right. If `[ship] base` names a branch and the main checkout is on another one, nothing is written. The command does not fetch, so after a pull request is merged, pull the base branch before changing the plan again.

```
$ afk plan patch --old old.md --new new.md
Patched docs/PLAN.md. Tasks changed: T2.1.
Committed and pushed.
```

Other lines you can see: `Patched docs/PLAN.md. No task's entry was changed.`, `Added T1.1a to docs/PLAN.md.`, `T1.2 now waits for T1.1a in docs/PLAN.md.`, and `Committed, but not pushed.` followed by a line that says why.

**Through a pull request.** Put this in `afk.toml` (or `afk.local.toml`):

```toml
[planning]
via_pr = true   # default false
```

With it, a change is made in a temporary worktree on the branch `afk-plan-changes`, pushed, and a pull request is opened with the `gh` command; if one is already open for that branch it is reused, so a second change builds on the first. Your checkout is never switched to another branch and its files are not touched. The plan file in your checkout therefore changes only when the pull request is merged, and the command says so. Until then `afk plan lint`, `afk plan next-id` and `afk tasks` still read the old plan. A plan that is not committed is still changed in place. If the branch has commits of its own and no open pull request, afk stops and tells you to pull the base branch or delete the branch; it never throws commits away. The key is `[planning]` because `plan` in `afk.toml` is already the plan's path.

**Exit codes:** 0 when the plan was changed (or `next-id` printed), 1 when the change was refused or failed, 2 when the command was written wrongly (for example `insert` with two places, or `depend` with a word that is not a task id).

## `afk plan check` — Tick a task in a plan

Record the outcome of one task in a session's plan. The agent plugin runs this itself after each task, so a plan's checkboxes are ticked by a command rather than by a helper agent. You can also run it by hand to correct a plan.

```bash
afk plan check [--project DIR] [--reason TEXT] <session> <task-id> done|failed
```

- `<session>` — The session (plan) name. The plan file is `.afk/plans/<session>.md` under the project.
- `<task-id>` — The task, written `W1.T2` or `(W1.T2)`.
- `done` — Turns the task's line into `- [x] (W1.T2) title`.
- `failed` — Turns the line into `- [!] (W1.T2) title FAILED: reason` and adds a `## (W1.T2) FAILED` entry, with the time and reason, to `.afk/<session>/issues.md`. Needs `--reason`.
- `--project DIR` — The project to work in. Defaults to the current directory.

Running the same command twice changes nothing the second time, and a task already marked failed keeps its first reason. Several of these commands can run at once on one plan; they take turns using a lock file.

**Exit codes:** 0 when the task was marked (or there was nothing to do), 1 when the plan or the task was not found or the file could not be written, 2 when the command was written wrongly (for example `failed` with no `--reason`).

---

## `afk tasks` — List the master plan's tasks

Show every task in the project's master plan, with its status and what it is still waiting for.

```bash
afk tasks [--project DIR]
```

To check a plan for mistakes before the daemon reads it, run `afk plan lint`. To change a plan that exists, see `afk plan extend`, `amend`, `reconcile` and `fix` (above) and `afk plan patch`.

A master plan is a markdown file where each task is a heading that starts with its id, followed by a `- **Depends on:**` line:

```markdown
#### T4.3 — Care reminders
- **Depends on:** T4.2a, T4.2b
```

- Dependencies are the task ids on that line (`T4.2a, T4.2b`). Text in parentheses is ignored. `all prior` means every task listed earlier in the plan. Any other text with no task ids in it (for example `none` or `M3–M8 as available`) counts as no dependencies, and `afk tasks` prints a note so you can check it.
- A title with `(fix)` or `(fix; …)` marks a fix task. It is listed under its title as written, and when it is ready it runs first: the first ready `(fix)` task in file order goes ahead of every other ready task, behind only work already running and a parked task whose answers are in (see **Fix tasks** under the build loop in `daemon.md`).
- Each task has one of seven statuses: `pending` (the default, for a task the ledger does not mention), `running`, `shipping`, `parked` (waiting for you to answer something), `done`, `failed` and `superseded` (kept in the plan only as history, replaced by other tasks).
- The last column lists the dependencies not yet finished. A dependency on a superseded task is finished only when every task that replaced it is.
- `--project DIR` — The project to read. Defaults to the current directory.

The commands read the project's main checkout, so they give the same answer when you run them inside a session's worktree.

**Where the plan is.** The plan path comes from `plan` in the project's `afk.toml` and defaults to `docs/PLAN.md`:

```toml
# afk.toml, in the project's root folder
plan = "docs/PLAN.md"
```

A second key, `[planning] via_pr`, sends changes to the plan out as a pull request (see `afk plan patch`):

```toml
[planning]
via_pr = false
```

**Where the statuses are kept.** In `.afk/build/ledger.json` under the project. If the file is missing, every task is `pending`. If it is damaged (for example, half-written by hand), the commands stop with a message naming the file instead of guessing; fix the file, or delete it to start again from every task pending.

**Exit codes:** 0 when the table was printed, 1 when the plan, `afk.toml` or the ledger could not be used, 2 when the command was written wrongly.

## `afk next` — Show the task that would run next

Print the one task afk would start now, or say why there is none.

```bash
afk next [--project DIR]
```

When there is a task, the line starts with its id and title, for example `T4.2b  Task UI`. Otherwise the line says what is in the way: something is already running, everything is done, a task waits for you, a task failed and blocks the rest, or the plan can never finish (for example a replaced task whose replacement is missing). Having nothing to start is a normal answer, not an error.

**Exit codes:** 0 whenever it printed an answer (including "nothing to start"), 1 when the plan, `afk.toml` or the ledger could not be used, 2 when the command was written wrongly.

## `afk ledger set` — Record a task's status by hand

Set the status of one or more tasks in the ledger. This is how you tell afk about work that was done before it was running, and how you correct a mistake.

```bash
afk ledger set [--project DIR] [--replaced-by ID,ID] <task-id>... <status>
```

- `<task-id>...` — One or more tasks, each of which must be in the plan, so a typo is refused instead of being recorded for no task. Every id is checked before anything is written, so one wrong id changes nothing.
- `<status>` — One of `pending`, `running`, `shipping`, `parked`, `done`, `failed`, `superseded`.
- `--replaced-by IDS` — Comma-separated task ids that replace the task. Required with `superseded`, and refused with any other status. It names the replacements of one task, so it cannot be combined with several task ids.
- `--project DIR` — The project to work in. Defaults to the current directory.

To seed a project that is already part way through its plan:

```bash
afk ledger set T1.1 T1.2 T1.3 done
afk ledger set T4.2 superseded --replaced-by T4.2a,T4.2b
afk next        # names the first task still to do
```

In the dashboard, `ctrl+] S` on the Build tab does the same for the task under the cursor, through the same code, after a yes. Running the same command twice changes nothing the second time. Several of these commands can run at once; they take turns using a lock file.

**Exit codes:** 0 when the task has the status (changed now or already), 1 when the plan or ledger could not be used or the task is not in the plan, 2 when the command was written wrongly (for example an unknown status).

## `afk ledger init` — Create the ledger for a project that has none

A project with no ledger file is one afk cannot judge: it cannot tell a new project from one that is already part built, so the daemon starts nothing and raises an inbox item (see the daemon section). `afk ledger init` creates the file.

```bash
afk ledger init [--project DIR] [--done-through ID] [--done ID,ID]
```

- No flags — Creates an empty ledger. Every task is `pending`, so a new project starts from its first task.
- `--done-through ID` — Marks every task up to and including `ID`, in the plan's order, as `done`. Use it for a project that is part built.
- `--done ID,ID` — Marks the listed tasks `done`. It can be combined with `--done-through`, for finished tasks after that point.
- `--project DIR` — The project to work in. Defaults to the current directory.

```bash
afk ledger init                                      # a new project
afk ledger init --done-through T1.4                  # T0.0 to T1.4 are built
afk ledger init --done-through T1.4 --done T2.1,T2.3
```

An id that is not a task in the plan is refused, and nothing is written. If the ledger already exists the command leaves it alone, says so and exits 1; use `afk ledger set` to change an existing ledger. On success it prints `Created <path> with N task(s) marked done.`

If `--done-through` covers a task that was never built because other tasks replaced it (one kept in the plan only as history), it is marked `done`, which is not true of it. Follow with `afk ledger set {id} superseded --replaced-by …` to record it properly.

**Exit codes:** 0 when the ledger was created, 1 when the plan could not be used, an id is not in the plan, the ledger already exists or the file could not be written, 2 when the command was written wrongly.

## `afk usage` — Show how much of your usage is spent

Print how full each of the agent subscription's usage windows is (the current session, the week, and the week for each model), when each one resets, and what afk's usage governor would do about it.

```bash
afk usage [--provider NAME] [--refresh]
```

```
Usage read just now:
  Session                 36% used, resets Sun 11 Oct 14:49 (in 1h 41m)
  Week (all models)       72% used, resets Mon 12 Oct 06:59 (in 17h 51m)
  Week (Fable)            13% used, resets Mon 12 Oct 06:59 (in 17h 51m)
  Extra usage             off
Governor: run. Usage is within the limits, so afk is running.
```

- `--provider NAME` — Which agent to read. Defaults to the default provider.
- `--refresh` — Read the numbers again even if a recent reading is stored. The dashboard's refresh usage action (`ctrl+] R` in `afk dash`) does the same as `--refresh`, and so does the **Refresh usage** button on the web page (`/usage`, served by `afk daemon`; see the web page section).

A reading less than 5 minutes old is reused, and the report then says how long ago it was read ("less than a minute ago", "3 min ago"). Anything older, or `--refresh`, asks the agent again; that costs no model usage. The probe runs from afk's state directory, not from the folder you ran `afk usage` in, so it leaves no records in your project. The last reading is saved per machine in `usage.json` under `$XDG_STATE_HOME/afk/` when that is set to an absolute path, otherwise `~/.local/state/afk/`. If a probe fails and an earlier reading exists, the report shows that one and exits 1.

The `Governor:` line is the decision afk would make from the reading:

- `run` — Usage is within the limits; afk runs as normal.
- `no_launch` — A window is past the limit for starting new work (75% for the session, 85% for the week). Nothing new starts; a running session carries on.
- `pause` — A window is at or past 90%. A running session would be paused until the window resets, and the line says when.
- `hold` — The reading is missing or more than 30 minutes old. Nothing new starts and a running session is left alone.
- `refuse` — Extra usage is switched on for the account. afk will not run until you turn it off.

The limits in that list (75%, 85% for the week, 90%, 30 minutes) are the defaults. `afk usage` and the daemon both read them from the `[governor]` table in `config.toml` (see the daemon section), so a change there changes both.

A provider with no way to report usage prints "Usage is unknown" and exits 0; the governor then says `run`.

**Exit codes:** 0 when the report was printed, 1 when usage could not be read or the reading could not be saved, 2 when the command was written wrongly (an unknown flag, provider or extra word).

## `afk inbox` — Things that need you

The inbox is where afk puts anything only you can do: a credential to set, a dashboard to click through, a choice to make, something to check by hand. Instead of stopping a whole session to wait for you, the agent adds an item and either waits on that one task (a blocking item) or carries on (a non-blocking one). You read the list when you are ready.

```bash
afk inbox                                 # list the items
afk inbox show <id>                       # show one item in full
afk inbox answer <id> <text or number>    # answer an item
afk inbox add --kind KIND --title "one line" --block|--no-block [flags]
```

`add` is mostly run by the agent, but you can run it too. It prints `Added inbox item <id>.` Every added item must say whether it stops the task, with exactly one of `--block` or `--no-block`; leaving both out, or giving both, is refused so a forgotten flag never parks a task by accident.

- `--kind KIND` — Required. One of the six kinds below.
- `--title TEXT` — Required. One line, at most 200 characters, saying what you have to do or know.
- `--block` — The task waits for you.
- `--no-block` — The task carries on.
- `--body TEXT` — The detail.
- `--task ID` — The master-plan task the item belongs to, like `T1.2`.
- `--session NAME` — The session the item came from. When left out, afk uses the session whose `.afk/<name>/status.json` is in the folder you run the command from.
- `--option TEXT` — One choice for a question. Repeat it for each choice (at least two). Only with `--kind question`.
- `--recommended TEXT` — The option the agent recommends. It must match one of the options. Only with `--kind question`.
- `--assumed TEXT` — The answer the agent went ahead with. Only with `--no-block`.
- `--project DIR` — The project to work in. Works with every form and defaults to the current project.

The six kinds:

- `question` — A choice only you can make.
- `env_var` — A credential or variable you must set.
- `external_action` — Something to do outside the code: a dashboard, a device, a payment.
- `verification` — Something to check by hand.
- `failure` — Something went wrong and was not fixed.
- `notice` — Something to know; nothing to do.

**The list.** Each line is a marker, the id, the kind, the task (when any item has one) and the title. `!` is an open item that stops a task, `·` is an open item that does not, and `✓` is an answered one. Items that stop a task come first. A file in the inbox folder that afk cannot read is counted on a last line and never stops the list.

**Answering.** Everything after the id is the answer, so quotes are not needed. On an item with options, a whole number in range picks that option; anything else is kept as you typed it. An empty answer is refused. The first answer is kept: answering an item twice changes nothing and exits 1.

**Where items live.** One file per item, `.afk/inbox/<id>.json`, in the project's main folder, even when the command runs inside a session's worktree (so an item is never lost when the worktree is removed). The files are written by this command, not by hand.

**Push notifications.** Every device signed up on the [Notifications page](web.md#notifications) gets a push for an item that stops a task, whatever its kind. Items that do not stop a task send nothing. The notice carries the item's title and body, and tapping it opens that item on the web page. With no device signed up nothing is sent, and that is not an error. The item is always written first. The project must be listed in `config.toml`; if it is not, afk prints a warning (`the item was added, but ...`) and sends nothing, because the tap would have nowhere to open. A device whose browser no longer accepts notifications is taken off the list. Any other failure prints a warning and keeps the device. None of this changes the exit code, so an agent never thinks the item was lost. A leftover `[notify] ntfy_url` in `afk.toml` or `afk.local.toml` is ignored without a word.

**Planning waits.** When a planning session waits for you (a question to answer, or a reply to type), the daemon sends one push for that wait to every device signed up on the Notifications page. Its title names the project, for example `afk: shop is waiting for you`, and its body is the first line of the question, followed by a line saying where to answer. It is sent on the daemon's pass, and it is sent even while builds are paused, because planning is work you do with afk, not a build. Once a wait has been sent, it is not sent again. A new question after you answer starts a new wait and gets its own push. A wait counts as sent once at least one device has got it, so with no device, or if every send fails, the next pass tries again. afk keeps the waits it has sent in `plan-waits.json` in its state folder, so a restart does not repeat them. A tap opens the planning chat, or the plan page for a project that is only waiting for you to review its plan.

**Exit codes:** 0 when the command did what was asked (including an added item whose notification failed), 1 when the item was not found, was already answered, or a file could not be read or written, 2 when the command was written wrongly (for example neither `--block` nor `--no-block`).

## `afk tokens` — Show how many tokens a task used

Show how many tokens a session or a task used, split by phase and by role. afk is limited by the usage allowance of your agent subscription, so tokens per finished task is the number that decides how much it can get done in a week.

```bash
afk tokens [--project PATH] [--record] [SESSION|TASK]
```

- No argument — Totals per task, read from the ledger.
- `TASK` — A task id such as `T1.4`: that task's lines from the ledger. Use this for a session that has been cleaned. `afk dash` shows the same table for the selected Build tab task (`ctrl+] T`).
- `SESSION` — A session name: reads the session's transcripts now, including its sub-agents, and prints a header (session, task or `no task`, number of calls) and the table. The task id is the first `T<n>.<n>` in `.afk/<session>/description.md`.
- `--record` — Only with a session name. After printing, adds the session's lines to the ledger. A session that already has lines in the ledger is not added again; the command prints `already recorded` and changes nothing.
- `--project PATH` — The project to work in. Defaults to the current directory.

**The columns:** one row per phase (`plan`, `execute`, `review`, `fix`, `ship`), role (for example `conductor`, `task`, `verifier`, `review:qa`) and model. `calls` is the number of model calls, `input`, `output`, `cache write` and `cache read` are the token counts the agent CLI reports, and `share` is the row's part of the table's weighted total. Each phase has a `(subtotal)` row and the table ends with a `total` row.

**The start-up section:** when a session is read from its transcripts, a second table follows the first: `start-up (the first call of each agent)`. Every agent begins with a block of context (its instructions, tool list, skills and `CLAUDE.md`) before it does anything, and the first call of each agent carries all of it. The section shows, per role, how many agents there were, the average size of that first call in tokens (`input + cache write + cache read`), its weighted cost, and its share of the whole session. The `total` row's share is how much of the session went on starting agents. It is not shown for a task read from the ledger, because the ledger does not keep it.

```
start-up (the first call of each agent)
               role  agents  average tokens  weighted  share
            planner       1          121269    120841   4.2%
           research       1           34921     44366   1.5%
               task       5           35927    165003   5.7%
           verifier       3           37087     97801   3.4%
  review:compliance       1           35151     45273   1.6%
          review:qa       1           35152     22699   0.8%
     review:quality       1           35149     22716   0.8%
              total      13           42503    518699  18.1%
```

**Weighted cost** is `input + 5 x output + 1.25 x cache write + 0.1 x cache read`. It is a proxy built from the usual ratios between token prices, not a bill, and a subscription is not metered per token; use it to compare tasks with each other. The raw counts are kept in the ledger, so the weights can be changed later.

**The ledger** is `.afk/log/tokens.jsonl` in the project's main checkout, one JSON line per task, phase, role and model. It is added to only by `--record`. Several recordings can run at once; they take turns using a lock file beside the ledger.

**Exit codes:** 0 when the table was printed (or the session was already recorded), 1 when there was nothing to show or the session could not be read (no such session, it has not started, no transcript found, the agent CLI is not supported yet, or the ledger has nothing for the task), 2 when the command was written wrongly. `-h` and `--help` print the usage and exit 0.

---

## `afk web password` — Set the password for the web page

`afk web password` sets the password that signs you in to the web page `afk daemon` serves (see [the web page](web.md)). Run it in a terminal on the machine afk runs on. It asks for the password twice without showing it, and saves only a salted hash of it, in `web-password.json` in afk's state folder, readable only by you. The password itself is never saved, and `config.toml` never holds it.

```bash
afk web password
```

- The password must be at least 8 characters, and the two answers must match. A short or mismatched one saves nothing and says why.
- Running it again replaces the password, and every browser that was signed in is signed out.
- A password set while `afk daemon` runs turns the page on only when the daemon next starts, so restart it. A page that is already on reads the file again on every sign-in, so changing the password needs no restart.
- It must run in a terminal. In a script or from a pipe it refuses and saves nothing, so a password is never echoed into a log.
- Exit codes: 0 saved, 1 nothing was saved, 2 the command was written wrongly (for example with extra words after `password`).

---

## Session Model

afk sessions are **detached processes**: the session host runs independently in the background, surviving dashboard exit, terminal closure, and even `afk attach` disconnections. This allows:

- **N concurrent sessions**: Create and run multiple sessions independently in the same repo
- **Non-exclusive attachment**: Only one client can be connected at a time (a new attach disconnects the previous one), but the session itself is not exclusive — you can detach, do other work, and reattach later
- **Host restart**: If a session's host crashes or is killed, `afk dash` can restart it (press `ctrl+] r` on the selected session)
- **Persistence across tool availability**: Whether or not git-spice, worktrunk, or notifiers are installed, sessions are created and remain runnable

The three attach outcomes are:

1. **Detached** (`ctrl+\`) — You pressed the detach key; the session is still running
2. **Stolen** — Another client attached to the same session; your client is disconnected but the session is still running
3. **Exited** — The session's child process exited; the host process also exits at that point and can be reaped/cleaned up

### Agent provider

Each session records the agent CLI its host runs: `claude` (default), `codex`, or `opencode`. The host does not guess it:

- **`.afk/{name}/provider`** — written only for a non-default provider, so Claude sessions leave no file. A missing file means `claude`.
- **`host.json`** — the `provider` field, omitted for `claude`. The host restarts its child with this provider.

| Provider | Planned session launch | Wedged-restart resume | Preflight | Autonomous-only |
|---|---|---|---|---|
| `claude` (default) | `claude "/afk:go --session {name}"` | `claude --resume <id>` | `claude plugin list` | no |
| `codex` | `codex "$afk:conductor --session {name} --autonomous"` | `codex resume <id>` | `codex plugin list` | yes |
| `opencode` | `opencode --standalone --prompt "/afk-go --session {name}[ --autonomous]"` | `opencode --standalone -s <id>` | `opencode plugin list` | no |

A one-off (`--scratch`) runs the bare CLI for its provider: `claude`, `codex`, or `opencode --standalone`.

**OpenCode.** afk passes `--standalone` so the session lives in the PTY child. Without it, the OpenCode background service owns the session, and killing or restarting the child would not control the conversation. The same flag goes on the resume argv. afk adds no permission-bypass flag and never passes `--auto`; that stays your call. The preflight passes when `opencode plugin list` shows a row whose id is exactly `afk`. The conductor learns its `ses_…` session id through the plugin's `afk_session` tool, and the session_id in status.json is what a restart resumes. OpenCode sessions can run interactive or autonomous.

**Unverified against the real OpenCode TUI** (OpenCode 2.0.24 is beta):

- The `--prompt "/afk-go …"` launch is not confirmed. The afk port's prompt hook expands a leading `/afk-go`, which should cover it either way. The prompt lives in one const, `opencodeConductorPrompt` in `internal/provider/provider.go`.
- Liveness detects a wedged child by nudging it with SIGWINCH and expecting a repaint. Whether the OpenCode TUI repaints on SIGWINCH is not confirmed. A healthy opencode session can then show as wedged (⚠) until its output resumes. Restart is still a confirmed manual action (`ctrl+] r`), so a false wedge costs a stale cell, not a kill.

A host restart resumes the conversation by its id (the `session_id` in status.json) with the provider's resume argv in the table above. A dashboard relaunch of a stopped session reads the same sidecar, so a stopped Codex or OpenCode session relaunches under its own CLI, not `claude`. A sidecar naming an unknown provider is an error, never a fallback to `claude`.

**From the dashboard.** The `ctrl+] n` form's Provider selector (see [the dashboard](dashboard.md)) creates a session under any provider. Picking `codex` ticks Autonomous, and the form won't submit `codex` with Autonomous unticked. The `ctrl+] o` action opens a provider picker that defaults to Claude (see `ctrl+] o` above), and `afk new --scratch --provider codex` (or `opencode`) creates a one-off for another provider from a shell. Non-default sessions show their provider on the row, in the detail head, and in the `ctrl+] /` filter. Claude sessions don't show anything extra.

### One-off Sessions

A one-off session (created via `afk new --scratch` or the dashboard's `ctrl+] o`) is a lightweight alternative to a planned session:

- **No branch, no worktree, no plan, no `status.json`** — only a `.afk/{name}/` directory containing `host.json` and `host.log` (the host's socket lives outside it, under `os.TempDir()/afk-{uid}/`)
- **Discovered via `host.json`** — rather than through the status.json/branch/worktree reconciliation that planned sessions use, making them legitimate even without a conductor or a plan
- **Bare CLI for the chosen provider in the repo root** — `claude` by default, or `codex` / `opencode --standalone` when created with `--provider` or the `ctrl+] o` picker. Runs with full repo context but no planning workflow, ideal for quick explorations or ad-hoc help
- **Cleaned without phase gate** — `afk clean <scratch-N>` removes it directly with no "is it done?" check, since there is no work to lose
- **Auto-named `scratch-N`** — when created without an explicit name, the system allocates the lowest available slot (reusing gaps from cleaned sessions)
- **Mutable display label** — press `ctrl+] l` in the dashboard to rename it in the UI without changing the on-disk identity at `.afk/{name}/`

---

## Dependency Matrix — Degradation Paths

| Tool | Absence Impact | Workaround |
|---|---|---|
| **tmux** | None. afk never uses tmux: sessions are detached host processes rendered in an embedded pane, by both `afk dash` and `afk attach`. | n/a |
| **git-spice** (`gs`) | `afk new`'s base picker offers the repo's local branches with trunk first. New branches are plain git branches. `afk dash`'s stack panel is hidden. | Explicit `--base BRANCH` to `afk new`. Stacking is still possible via `base_branch` in status.json. Use `--no-git-spice` to force plain-git mode. |
| **worktrunk** (`wt`) | **When present (with git-spice also installed):** Branches are created via `wt switch --create` and are tracked in git-spice best-effort. The base picker shows all local branches (trunk, tracked stack, every other local branch). When absent, git-spice creates and tracks the branch when the base is tracked, and falls back to a plain git branch with a warning otherwise. | Worktrees are created at `.claude/worktrees/`. |
| **notifier** (`alerter`, `osascript`) | Session state updates silently (no desktop notifications). Awaiting-input state is still recorded in status.json and visible in the dashboard. Desktop notifications are **macOS-only**. `alerter` provides full-fidelity notifications (sound, per-session replacement, click-to-focus); `osascript` is the reduced-fidelity fallback (formatting only — no click-to-focus). | Install alerter: `brew install vjeantet/tap/alerter` (note: this is a **tap**, not homebrew-core). If a toast vanishes before you can click it, the setting to look at is System Settings → Notifications → **Terminal** (alerter delivers under Terminal's identity, so look for Terminal, not alerter) → style Alerts rather than Banners. **Unverified:** whether that makes these toasts persist was never observed — System Settings cannot be driven programmatically, so it is yours to confirm, and on at least one machine `com.apple.Terminal` is absent from `com.apple.ncprefs.plist` entirely (while delivery still works), so the row may not appear until macOS registers it. Click-to-focus requires a running `afk dash` (which records the pane metadata); without it the toast still appears with formatting, sound, and grouping but has no Focus button. |
| **web push** (devices signed up on the Notifications page) | No device signed up: nothing is sent, and that is not an error. A push service that fails: afk prints a warning, the item or the planning wait is still written, and the device stays on the list. A device the service reports gone is removed from the list by itself, and it stops getting notifications. | Turn notifications on again on that device from the [Notifications page](web.md#notifications). |
| **gh** (GitHub CLI) | The daemon cannot push-and-merge: a finished task parks with an inbox item saying the GitHub tool is missing or not logged in. Nothing else in afk uses `gh`. | Install `gh` and run `gh auth login`, then answer the item; the ship carries on from where it stopped. Or push, merge and run `afk ledger set <task> done` by hand. |

**Push notifications** (`afk inbox`) go to devices signed up on the Notifications page and need no desktop notifier, so they work on Linux; see the **web push** row above.

None of these absences cause failures; the CLI degrades gracefully. The only hard requirement is **git** (and **Claude Code CLI**, for `/afk:go`; **Codex CLI**, for `--provider codex` sessions; **OpenCode CLI**, for `--provider opencode` sessions).
