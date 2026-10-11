# afk

afk is a Go command-line tool that runs coding-agent sessions for you. Each session gets its own git branch and worktree, keeps running in the background after you close your terminal, and shows up in a full-screen dashboard where you can watch it, step in, and clean up when it is done. Today that means the session manager and dashboard described below, plus the plugin that drives a plan, build and review cycle inside each session.

Where it is going: afk is meant to become an always-on runner that takes a master plan, picks the next ready task, runs it, ships it and only bothers you for things you must do yourself. That plan lives in [`docs/PLAN.md`](docs/PLAN.md). Nothing from it beyond what this README documents exists yet, so only the commands listed here are available.

afk began as a copy of the forge-cli project, renamed; see [`docs/provenance.md`](docs/provenance.md).

## Installation

Requires **Go 1.25+** (see `go.mod`, `.tool-versions`). The dashboard is built on the charm.land v2 modules (`bubbletea`, `bubbles`, `lipgloss`, `huh`), pulled in automatically via `go build`/`go mod`.

Build from source:

```bash
cd /path/to/afk
go build -o afk .
```

This creates a binary `afk` in the current directory.

### Install the agent plugin

The afk workflow itself (`/afk:go`, `/afk:plan`, `/afk:execute`, `/afk:review`, the conductor, and its hooks) is an agent plugin that ships in this repo under `plugins/` and is embedded in the binary. Install it with the command below, which asks which of your agent CLIs to install into:

```bash
afk plugin install
```

See [`afk plugin install`](#afk-plugin-install--install-the-agent-plugin) for targets and flags. Claude Code and Codex users can also add this repo as a marketplace directly, without the binary:

```bash
claude plugin marketplace add dphaener/afk && claude plugin install afk@afk
codex plugin marketplace add dphaener/afk && codex plugin add afk@afk
```

## Subcommands

### Default action — `afk` with no arguments

Running `afk` with no subcommand or options launches the dashboard, equivalent to `afk dash` with no flags.

```bash
afk          # same as: afk dash
```

**Requires a terminal on stdout.** If stdout is piped or redirected (`afk | cat`, `afk > out.txt`), afk prints usage and exits 1 instead of launching. The dashboard is a full-screen Bubble Tea program whose output is meaningless in a pipe, and it does not detect that on its own — it checks stdin only, so without this gate a piped `afk` would start invisibly and sit in raw mode with no visible UI until you interrupt it.

Only a bare `afk` is the default action. `afk dash`'s flags are not top-level flags, so `afk --interval 5s` is an unknown-subcommand error; pass flags to `afk dash` directly.

---

### `afk new` — Create a new session

Create an isolated afk session with a dedicated branch and worktree.

```bash
afk new [options] <name>
```

The `<name>` is validated as strict kebab-case (`^[a-z0-9][a-z0-9-]*$`) and becomes the branch name, worktree directory, and plan slug.

**Options:**

- `--base BRANCH` — Stack on (branch off) an explicit base branch. If omitted, an interactive picker shows available branches: in git-spice mode, all local branches (trunk first, then the tracked stack, then every other local branch — so branches checked out in other worktrees, such as prior sessions, appear too); in plain-git mode, the repo's local branches with trunk first. Skipped when stdin is not a TTY or under `--dry-run`, which fall back to the trunk. Propagates to the worktree creation.
- `--description TEXT` — Free-text initial task context for the conductor (stored in `.afk/{name}/description.md`, never interpolated into commands for safety). On an interactive terminal, `afk new` prompts for this when the flag is omitted.
- `--dry-run` — Print the full action list (branch creation, worktree creation, status.json seed, session host launch) without executing anything. Useful for previewing before committing.
- `--no-git-spice` — Force plain-git behavior even if git-spice is detected. Creates the branch via `git worktree add` instead of worktrunk (`wt switch --create`) or git-spice tracking.
- `--scratch [NAME]` — Create a one-off session: no branch, no worktree, no plan — the bare CLI for the chosen provider in the current repo root (`claude` unless `--provider` says otherwise). If `NAME` is omitted, the next available `scratch-N` is allocated. Mutually exclusive with `--base` and `--no-git-spice` (a one-off has no branch, so a base is meaningless). Never prompts for a base or description, even on an interactive terminal.
- `--autonomous` — Run the session in autonomous mode: the conductor works through the plan without stopping to ask you at each gate. Writes an empty marker file at `.afk/{name}/autonomous` (a separate step in the `--dry-run` list) and appends `--autonomous` to the conductor's argv (`claude "/afk:go --session {name} --autonomous"`). The marker is what lets a dashboard restart, which re-runs `/afk:go --session {name}` without the flag, keep the session autonomous. Mutually exclusive with `--scratch` (a one-off has no conductor to be autonomous; exits 2 with a usage error). Requires an afk plugin with autonomy support; an older plugin is expected to ignore the flag and the marker and run in default mode, so check the plugin version if a session still asks you questions. Under `--provider codex` the child is `codex "$afk:conductor --session {name} --autonomous"` instead; under `--provider opencode` it is `opencode --standalone --prompt "/afk-go --session {name} --autonomous"`.
- `--provider claude|codex|opencode` — The agent CLI the session host runs. Default `claude`. `codex` runs the OpenAI Codex CLI and is autonomous-only: it requires `--autonomous` or `--scratch`, otherwise `afk new` exits 2 with `afk new: --provider codex requires --autonomous or --scratch: Codex sessions run autonomous-only`. An unknown name exits 2 before anything is created. Codex sessions also need the afk plugin installed in Codex (`afk plugin install --target codex`); `afk new` checks this with `codex plugin list` as a non-fatal preflight and prints a warning with the install steps when the check fails. `opencode` runs the OpenCode CLI and is not autonomous-only, so interactive and autonomous sessions both work. OpenCode sessions need the afk plugin in `~/.config/opencode/plugins/afk/` (`afk plugin install --target opencode`); `afk new` checks it with `opencode plugin list` as a non-fatal preflight, the same way as codex. The provider is recorded per session (see [Agent provider](#agent-provider)).
- `--no-dash` — Create the session and its host, but do not open the dashboard afterwards; print a one-line pointer instead. This is what the dashboard's own in-TUI creation form passes, so creating a session from the dashboard cannot recursively launch a second one.
- `--project PATH` — Create the session in the repo at `PATH` instead of the current working directory's repo. `PATH` must exist, be a directory, and be a git repo root; `~/` is expanded. See [Multi-project support](#multi-project-support).

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

### `afk dash` — Launch the dashboard

Open an interactive TUI dashboard showing afk sessions, their phases, progress, and live diff stats — for the current repo by default, and for every repo discovered under your configured search roots if you have any (see [Multi-project support](#multi-project-support)). Selection preview shows a running session's agent pane; `ctrl+t` is the explicit agent/view toggle and can attach a running session or restart a stopped host without moving keyboard focus. This is also the default action when you run `afk` with no arguments on a terminal (see [Default action](#default-action--afk-with-no-arguments)). Unlike earlier versions, `afk dash` does not require being run from inside a git repo — see [Launching outside a repo](#launching-outside-a-repo).

```bash
afk dash [options]
```

**Options:**

- `--interval DURATION` — Refresh interval for session discovery (default `2s`; e.g., `500ms`, `1s`, `5s`). File reads only — never blocks the UI.
- `--open <name>` — Open the named session's embedded pane on start, once the dashboard's first discovery load and first terminal resize have both arrived. If the session's host is not running yet (the usual case immediately after `afk new`), it is restarted first. If the named session isn't found, the dashboard starts normally and shows an error as a toast. This is how `afk new` hands its terminal over.
- `--project PATH` — Launch with `PATH`'s repo as the active project instead of the current working directory's. `~/` is expanded; unlike `new`/`clean`, `dash` does not reject a `PATH` that turns out not to be a git repo — it just starts with no active project, the same [empty state](#launching-outside-a-repo) a launch outside any repo produces. Every other discovered project (from search roots) is still available in the switcher (`p`, below) — this only picks which one starts active.

**Keybindings:** Every key below is a real `key.Binding` with its own help text, so nothing here is discoverable only by reading this document — press `?` for the full list in-app, or `:` to search and run any of them directly.

- `↑/k`, `↓/j` — Move selection up/down. **Auto-previews the selected running session in its agent pane** as you move (no focus key needed), but the cursor must settle for 300ms before connecting — so a held key or fast stepping connects only to the row you stop on. Intermediate rows aren't dialed. **This steals the session from any other client attached to it**, including a `afk attach` in another terminal: a host serves exactly one client, and connecting detaches the previous one. Fast browsing is safe (only the settled row connects), but deliberate slow stepping does connect to each row. A **stopped** session isn't restarted by scrolling onto it (cursor movement starts no processes) and shows its detail view instead. While a pane's open, moving onto any row that isn't the paned session shows that row's detail view rather than the previous session's pane. A session being cleaned is never previewed. A preview that fails to connect is completely silent — no error appears from a bare cursor move. The selection wraps: `↑` on the first session lands on the last, and `↓` on the last comes back around to the first.
- `pgup`/`pgdn`, `home`/`end` — Page the session list, or jump to its first/last session. The list windows to the sidebar's height and pages rather than overflowing it, so on a short terminal or with many sessions these are how you reach a row that is off screen. Each one previews the row it lands on, exactly as `↑`/`↓` do. Deliberately narrow keys: bubbles' own defaults for these gestures are `h`/`l`/`f`/`d`/`b`/`u`/`g`/`G`, three of which the dashboard already spends on label, focus-pane and one-off. These don't wrap. `pgup` on the first page stays where it is, and `end` always lands on the last row.
- `shift+↑`/`shift+↓`, `ctrl+u`/`ctrl+d` — Scroll the detail pane up or down, one line at a time (`shift+↑`/`shift+↓`) or one half-page (`ctrl+u`/`ctrl+d`). These keys move through the pane's content when it is taller than the available height; the pane itself never scrolls if content fits. A right-aligned position percentage shows on the pane's last row **only when the content actually overflows**. The keys had to avoid every key the dashboard already spends in its normal mode — `↑/k` and `↓/j` are the session cursor, and `f`, `l`, `o`, `n`, `p`, `c`, `r` are all taken — **and** leave `pgup`/`pgdn`/`home`/`end` on the session list — rather than repointing those keys, a dedicated set was chosen so the list's own paging gestures remained uninterrupted. `ctrl+u`/`ctrl+d` follow `bubbles/viewport`'s own defaults for half-page scroll. They do nothing while a session pane is on screen, since the detail view isn't drawn then — and the offset stays where you left it, so `ctrl+t` back to the summary picks up where you were. These chords are the unfocused path, and only `ctrl+u`/`ctrl+d` carry over: those two are one shared binding, so they behave identically whether or not the detail pane has been focused with `enter`/`f` (below). `shift+↑`/`shift+↓` are normal-mode only — focusing replaces them with the viewport's own keys (`↑/k`, `↓/j`, `pgup`/`pgdn`, `home`/`end`), and a `shift`-arrow pressed while focused is ignored.
- `tab`/`shift+tab`, `1`–`5` — Switch the detail pane's tab: Overview, Waves, Stack, Notes, Diff. `tab` cycles forward and wraps, `shift+tab` goes back, and a digit jumps straight to that position in the strip. The digits index what's actually shown, so in a repo without git-spice (no Stack tab) `3` is Notes. Switching tabs puts the scroll back at the top. These keys do nothing while a session pane is on screen, and `?` and `:` leave them out then too.
- `/` — Filter the session list. Type to narrow it (fuzzy, matching the display label, the on-disk name, and the provider for anything that isn't `claude`, so a labelled session is findable by either and typing `codex` or `opencode` finds those sessions), `enter` applies the filter and hands the keyboard back to the dashboard, `esc` cancels while typing or clears an applied filter afterwards. While a filter is applied every per-session key — `enter`, `c`, `l`, `r` — acts on the filtered selection, and the 2s discovery tick keeps the filter applied rather than dropping it. Switching projects with `p` drops it, since a query written against one project's sessions would silently hide another's. Filtering never connects to a session: `/` moves the selection and every keystroke changes the match set, but no preview is armed while the prompt is open — otherwise typing a search would dial (and steal) whichever session currently matched. Accepting or cancelling the filter previews the row it settled on, as any other selection move does. Like `o`, `l` and `p`, `/` isn't part of the global help line's curated short subset, though `?`'s full help and the `:` palette both list it.
- `enter`/`f` — Focus the view currently drawn in the right-hand region: the session's agent pane when it is shown, or the **detail pane** otherwise — including when the pane is hidden by `ctrl+t` or the cursor moved off the paned row. These keys only transfer keyboard ownership; they never open, reveal, attach, reconnect, switch, or restart an agent. With the agent pane focused, all keyboard input (including Ctrl+C, which the session owns) routes to it, the terminal cursor moves into it and tracks the session's own cursor, and `afk attach` shows it the whole time. With the **detail pane** focused instead, `↑/k`/`↓/j`, `pgup`/`pgdn`, `home`/`end` and `ctrl+u`/`ctrl+d` scroll its viewport and the session-list cursor is frozen — every other key (`enter`, `c`, `n`, `l`, `r`, `p`, `/`, `o`, `:`) is ignored rather than acting, so a scroll gesture can never open, clean or steal a session; `q`/`ctrl+c`, `ctrl+g` and `ctrl+t` keep working, and `tab`/`shift+tab`/`1`–`5` keep switching tabs. `?` full help is unavailable while the detail pane is focused (as it already was while the session pane was); the footer shows the focused keymap live in its place. `ctrl+\` releases either focus; the detail pane's focus also releases with `esc`, which the session pane does not bind for this — and releasing with `esc` does **not** clear an applied filter, it only hands the keyboard back to the session list, so press `esc` again afterwards to clear the filter.
- `ctrl+t` — The explicit agent/view toggle. When the selected agent pane is already shown, it swaps that pane for the selected detail view and back; the session keeps running and stays connected, and the pane keeps tracking terminal size. When no agent pane is shown for the selected session, `ctrl+t` attaches a running host or restarts a stopped host, but leaves keyboard ownership with the session list when the agent surface arrives. It works while you are focused into either right-hand surface; hiding a focused pane returns input to the dashboard. A **stale** status does not block this: a host is reached through `host.json`, independently of `status.json`, and opening the agent pane is often exactly how you find out why a status went stale. **Note:** the dashboard intercepts `ctrl+t`, so a Claude Code session watched through a pane never sees it — its own todo toggle is unavailable from here. Press `ctrl+\` and use `afk attach` for a session that owns the whole terminal.
- `ctrl+\` — Unfocus the pane if you're focused into it, release the detail pane if `enter`/`f` focused that instead, or close the pane if neither is focused. Either way, input returns to the dashboard and the session keeps running in the background. This is the same chord `afk attach` uses to detach—so the gesture for "give me my keyboard back" is consistent everywhere. Closing an unfocused pane is how you clear one that auto-preview opened for you, which matters because `c` won't run while any pane is open.
- `ctrl+g` — Hand the mouse to your terminal, and press it again to take it back. The help line shows the current state, but only while you're focused into the pane. Reachable from anywhere in the dashboard, including the creation form and the clean confirmation, where every other key belongs to the dialog. While either modal is up the toggle still works, but the help line is listing the dialog's keys at that point, so the toast is what tells you where the mouse went.
- `ctrl+w` — Enter resize mode, which moves the divider between the session list and the main region. Inside it, `h`/`←` and `l`/`→` move the divider one percentage point per press, and `H`/`shift+←` and `L`/`shift+→` move it five. The list, the detail view and the embedded pane re-lay out live on each press. `ctrl+w` again saves the split to the state file, so it's restored on the next launch, and `esc` cancels and restores the width from when you entered. Quitting with `q` or `ctrl+c` leaves resize mode without saving, `ctrl+g` still toggles the mouse, and other keys are ignored until you save or cancel. It's unavailable while a session pane is focused, since every key then goes to the session. Press `ctrl+\` to release the pane first.
- `o` — Create a one-off session. This opens a small provider picker with Claude Code preselected, and every open starts on Claude. `↑`/`k` and `↓`/`j` move the highlight and wrap at both ends. `enter` starts the one-off with the highlighted provider. `esc` cancels and creates nothing. The picker lists every registered provider (`claude`, `codex`, `opencode`) whether or not its CLI is installed, same as the `n` form's Provider selector. A missing CLI shows up in the spawn-error dialog. The one-off gets an auto-generated `scratch-N` name, with no branch, worktree, or plan. Its pane opens on the bare CLI for the chosen provider in the repo root. `?`'s full help and the `:` palette both list the key. Disabled with no active project. From a shell, `afk new --scratch --provider codex` (or `opencode`) creates the same kind of one-off directly.
- `p` — Open the project switcher: a filterable list of every discovered project, each row showing its session count and an awaiting-input dot. Rows are ordered in three tiers: first, projects with any session awaiting input; second, projects by frequency of opening (most-opened first); third, alphabetically by display name with full path as a tie-break. A project's open count increments when you switch to it from the switcher, launch the dashboard with `--project PATH`, or launch from that project's directory; counts persist in afk's UI state file (see **Selection memory** below for its exact location) alongside the remembered per-project selection. `/` filters by name, `↑/k`/`↓/j` move (wrapping at both ends, same as the session list), `enter` switches the dashboard to the selected project (session list, waves, stack, and diff panels all repoint at it — a no-op if it's already active), and once the incoming project's session list loads, its first session is auto-previewed exactly as a cursor move would preview it (same 300ms settle, same refusals: a stopped session is not restarted, a fault row or a session being cleaned is never previewed). `R` triggers an immediate rescan of the configured search roots instead of waiting for the next tick, and `esc` clears an active filter first and only then closes the overlay. Two discovered projects that share a basename (e.g. two checkouts both named `api`) are shown with just enough parent path segments to tell them apart; a project with no such collision shows its bare basename. `p` doesn't appear on the global help line below — that line is a deliberately curated subset of the mode's keys, and `p` isn't in it — but it's listed in `?`'s full help and the `:` palette like every other key, plus the footer's help line, which lists the switcher's keys while it's open. The header's right edge also carries a `p: switch projects` hint whenever more than one project is known.
- `:` — Open the command palette: a fuzzy-searchable list of every action normal mode can currently perform, one row per key, each showing its description and its key label. Type to filter (matches the description and the key label, so `:` then `ctrl` finds chord actions and `:` then `new` finds named ones), `↑/↓` move the highlighted row (wrapping at both ends), `enter` runs it — exactly as if you'd pressed that row's own key — and `esc` clears the query first, closing the palette only once there's nothing left to clear. Rows are derived from the live dispatch table, so the palette only ever offers actions that would actually do something right now: a wedged-restart row appears only with a wedged session selected, a close-pane row only with a pane open, and so on — the same gating `?`'s full help uses.
- `?` — Toggle full help. The help line at the bottom normally shows a curated, width-budgeted subset of keys; `?` expands it to every key normal mode binds — paging, the project switcher, label editing, filtering, the command palette, and everything else in this list — and collapses it back on a second press. A key that's currently a no-op for the selection (e.g. `r` with nothing wedged) drops out of both views automatically rather than being listed disabled.
- `n` — Open the in-TUI session-creation form: three fields (name, description, base branch), an `[ ] Autonomous` checkbox below the base list, and a `Provider: ‹ claude ›` selector below that. Tab cycles focus forward through all five; Shift+Tab cycles backward. `space` toggles the checkbox while it has focus (`[x] Autonomous` when on); it types a literal space in the name and description fields and does nothing on the base list. On the Provider selector, `space` or `→` goes to the next provider and `←` to the previous, cycling through `claude`, `codex`, and `opencode` and wrapping at either end. Picking `codex` ticks Autonomous for you, since Codex sessions are autonomous-only. If you untick it and leave `codex` selected, submit fails inline with the same refusal `afk new` gives (`afk new: --provider codex requires --autonomous or --scratch: Codex sessions run autonomous-only`) and nothing gets created. Checked, the form passes `--autonomous` to `afk new` (see above), and a non-default provider adds `--provider <name>` after it (`opencode` sessions can be interactive or autonomous). Enter submits from name, base, the checkbox, or the selector, and inserts a newline in the description field. `/` starts filtering the base list; Esc cancels an active filter before canceling the form. The base-branch list wraps too: `↑` on the first branch lands on the last. Submitting creates the session in the background; the dashboard stays responsive, with a placeholder row standing in until discovery reports the real session. Allowed while a pane is open — the new session gets its own host and does not disturb what you are watching.
- `l` — Edit the display label for the selected session. A single-field prompt lets you rename the session in the dashboard view without changing its on-disk identity — the label is display-only and the directory stays as `.afk/{name}/`. Pressing enter saves, esc cancels. Clear a label by deleting all text and pressing enter. Nothing selected is a no-op. Like `o`, it's advertised through `?`'s full help and the `:` palette rather than the curated short help line.
- `c` — Clean the selected session: every session confirms in the dashboard (no shell-out, no second prompt); the clean then runs in-process. Nothing selected is a no-op. Blocked while a pane is open, since clean can remove the worktree out from under a running session — press `ctrl+\` to close the pane first (that includes a pane auto-preview opened on its own). Also blocked while a clean of that session is already running.
- `d` — From the clean confirmation modal (after pressing `c`), press `d` to clean **and** delete the session's branch. The branch survives by default; `y` cleans without ever touching it, and only `d` opts in to deletion. A merged branch deletes in a single `d`; an unmerged branch asks for confirmation with a second `d` to prevent accidental loss. No effect on scratch sessions (which have no branch). When a clean deleted the branch, the summary line reports its tip SHA as a recovery handle, and — if the branch was git-spice-tracked — names any upstack branches the delete retargeted so you can restack them deliberately.
- `r` — Restart the selected session's child in place, if (and only if) the list already shows it WEDGED — a healthy session ignores the key entirely. Confirms first (y/n), since this kills a live, unrecoverable process. On confirm, the dashboard closes any pane it already has open on that session and sends a single restart request to the host; the host runs the signal ladder and resumes the same conversation (`claude --resume`, `codex resume`, or `opencode --standalone -s`, per the session's provider) on a fresh process — the dashboard never touches the child directly. A toast reports the real outcome — restarted (the toast names the provider, e.g. `codex is resuming its conversation`), failed (with the host's reason, e.g. an unusable `status.json`), or merely requested when an older host reports nothing. Confirming also **disconnects any other client attached to that session** (a `afk attach` in another terminal): the request briefly takes the session's single client slot, and the confirmation prompt says so. Reopen the agent surface with `ctrl+t` once it is back.
- `q` — Quit immediately, with no confirmation. Closing the pane only **detaches** — the session's host is a separate process, so quitting the dashboard cannot end a session. Reattach with `afk dash` or `afk attach <name>`.

**Selection memory:** The dashboard remembers which session you had selected in each project, restored when you switch back or relaunch the dashboard. State is persisted to `$XDG_STATE_HOME/afk/state.json` (default `~/.local/state/afk/state.json`) and keyed by project path. If a remembered session no longer exists, the cursor falls back to the first row. Pane state (scrolling, detail view) is not remembered.

The same file also stores the sidebar split as `sidebar_pct`, an integer percentage of the terminal width given to the session list. The default is 38, and the value is clamped to 15–85. Whenever the terminal is at least 60 columns wide, the session list keeps at least 30 columns and the main region keeps at least 30. On narrower terminals the session list's 30-column floor wins. A missing or zero `sidebar_pct` means the default. Change it with resize mode (`ctrl+w`, above), not by editing the file.

**View panels:**

When more than one project is known, every panel below describes the **active** project only, so switch with `p` to look at another. The header is one row in every state. It puts a `afk Dashboard` badge on the left, the cross-project attention line beside it, and a `p: switch projects` hint on the right, which only appears when more than one project is known. The attention line carries the one thing nothing else on screen can tell you: which *other* projects have sessions waiting on you. It names them while they fit (`● 2 awaiting in gamma · 1 in delta`, in the switcher's own row order) and collapses to a single phrase when they don't (`● 6 awaiting in 4 other projects`, or `● 3 awaiting in 1 other project` when there's just the one). Nothing awaiting elsewhere and it renders nothing at all; there's no `0 awaiting`. The active project's own awaiting sessions aren't in it either, since those already have a dot in the list's flag column a few lines down. These counts come from the ~60s project scan rather than the 2s discovery tick, so they can lag the active project's own list by up to a minute. As the terminal narrows the header sheds pieces in a fixed order: the names collapse to the aggregate phrase, then the hint goes, then the badge, then the attention line itself. The badge going before the attention line is deliberate — you can act on an alert, and the badge only tells you what you already know you launched. When discovery is failing, a `stale · discovery failing 12s` notice sits between the badge and the attention line, and it's the last piece to go as the terminal narrows. The list under it has stopped moving, so you can't trust the other counts on the row either, because the cross-project ones come out of that same discovery pass. It's gone as soon as a pass succeeds. Whatever's left last is the only thing ever truncated, so a project name is never cut mid-word.

With zero or one known project — the unconfigured default — the header is just the badge. No attention line, because there's no other project to report on, and no `p: switch projects` hint. No keybinding hints for `o` or `l` either; those are advertised through `?`'s full help and the `:` palette instead (see `o` and `l` above).

- **Session list** (left, sidebar when a pane is open) — a table with a caption row: host liveness dot (`●` running, `○` stopped), name, `PHASE`, `TASKS` (done/total), and a one-cell flag (`●` awaiting input, `⚠` wedged, `↻` autonomous). The flag cell shows one glyph, by priority: wedged, then awaiting input, then autonomous, so `↻` only appears while the session needs nothing from you and `⚠`/`●` are never hidden by it. Its top edge is labelled with the active project's name, since the box holds exactly that project's sessions; with no project known it falls back to `Sessions`. Sessions on a non-default provider get a dim tag after the name (` codex`, ` opencode`). When the row's too narrow to fit both, the tag goes and the name stays. Columns fold as the sidebar narrows: below 40 cells the phase abbreviates (`EXEC`, `REVW`, `PLAN`…), below 32 the task count goes, and below 24 only the name and flag remain. An 80-column terminal lands on the abbreviated rung. Everything that used to sit on a second line — progress bar, branch, stale reason, wedged age — is in the detail pane's Overview now. Windowed: it shows as many rows as the sidebar's height allows and pages through the rest (`pgup`/`pgdn`, `home`/`end`); when it does page, the box's bottom edge says which page you're on. `/` filters it, and the line under the captions is reserved for the filter prompt (blank until `/` is pressed). Sessions may appear as BROKEN (worktree directory missing) or ORPHAN (worktree present but has no afk session). These fault rows show their fault type in the phase column, are cleanable with `c` (no phase gate), and cannot auto-preview or show an interactive agent pane; `enter`/`f` can still focus their rendered detail view. A standalone `/afk:go` session (status.json in the main checkout, no worktree of its own) is **not** a fault: it renders as an ordinary row with a working detail pane and is cleanable with `c`, but `d` is not offered — the session has no branch of its own. Scratch sessions are likewise unaffected. Note that `d` on an ORPHAN row offers to delete a branch afk never created — that population includes worktrees created outside afk — so the merge-state line and the unmerged double-confirm are what stand between you and someone else's branch.
- **Embedded pane** (main region, when a session is hosted) — the live conductor's terminal output, rendered by the bundled PTY + terminal-emulator bridge (`internal/termpane`) — an experimental spike component, not production-hardened
- **Detail pane** (main region, no pane on screen — either none is open, or one is hidden behind the summary with `ctrl+t`) — the selected session, in tabs (`tab`/`shift+tab`/`1`–`5`, above). The box's top edge names the session and its bottom edge lists the keys that act on it.
  - **Overview** — name (and on-disk name, if labelled), branch → base, phase, loop, updated time, host state, wedged age; an awaiting-input line when the session is blocked on you; progress bar, diff size vs. base, review verdict; a dim `↻ autonomous` tag on the head line for autonomous sessions (shown regardless of the flag-cell priority above), and a dim provider name after it (`codex`, `opencode`) when the session isn't on Claude; the plan's ready work (see below); the branch's path through the stack; and the last three issues. Pretty much what you'd check before deciding whether to open the pane.
  - **Overview task block** — what can start now, derived from the plan's `**Depends on**` lines rather than from a "current wave":
    - `Ready (N)` lists the incomplete tasks whose dependencies are all satisfied, capped at 5 lines then `+N more`. `Ready` includes tasks already in flight, since the plan file does not record which ready task a worker has picked up.
    - `N blocked by failed dependencies` (dim) counts tasks that can never start until a failed dependency is fixed.
    - `Failed (N)` lists `[!]` tasks, capped at 3 lines then `+N more`.
    - When nothing is ready the heading says why: `No tasks ready — waiting on unmet dependencies` while work is still progressing, or `No tasks ready` when every remaining task is blocked by a failure and only a human can unstick it.
    - When nothing is left to start, it is a summary line: `All N tasks complete`, or `X of N complete, Y failed` when every task is terminal but some failed.
    - A plan with no `**Depends on**` lines (legacy) behaves as before: each task depends on every task in the wave below it, a failed (`[!]`) dependency counts as satisfied, and nothing is reported as blocked. A plan that mixes tasks with and without the line is also treated as legacy.
  - **Waves** — the whole plan, every wave and task with its checkbox state. In a plan where every task declares `**Depends on**`, each pending `[ ]` task carries a `← W2.T1, W2.T4` suffix naming its unmet dependencies; completed dependencies are left out, and the suffix is truncated (or dropped) before the title is when the pane is narrow. Legacy and mixed plans show no suffix.
  - **Stack** (git-spice only; the tab isn't there otherwise) — the branch tree with session ownership annotations.
  - **Notes** — the last 10 lines of learnings and issues, and the full review verdict with its findings.
  - **Diff** — the full `git diff` against the base branch for the focused session, structurally coloured (file headers, additions, removals), scrollable with the existing detail-scroll keys, and — like the rest of the detail pane — focusable with `enter`/`f` (above) for the full scroll keymap. Tabs are expanded to 4-column stops, and the diff is capped at 2000 lines with an on-screen marker past that. Unfocused sessions show a one-line summary (`+N/-M across K file(s)`) instead. When the diff is loading you'll see `loading diff…`; this resolves within one `--interval` refresh cycle after a tab switch. If the base branch no longer exists, the Diff tab and the Overview tab's `Diff` line both show `base branch '<name>' no longer exists` instead. This occurs when `afk clean --delete-branch` removes a branch that other sessions still record as their base, since `base_branch` is immutable after session creation.

**Overlays and notices.** The project switcher, the command palette and the new-session form float as centred boxes over the dashboard, so the session list and whatever you had in the main region stay visible around them. Confirmations (clean, restart, the failed-create report) and the label editor are dialogs on the same layer, each with its title on the top edge and its keys on the bottom edge; the footer keeps the help line underneath. Status changes appear as toasts — ephemeral notifications in the top-right corner styled by severity (success, warning, error). Background events toast the same way — a session finishing or failing, one starting to await input, a host dying between ticks, a stack or branch-listing refresh failing. The dashboard also sets the terminal's window title (`afk · api · 2 awaiting input`) and, in terminals that draw one (Ghostty, iTerm2, Windows Terminal), a tab progress bar tracking the fleet's task completion. Colours adapt to a light or dark terminal background, detected on startup.

**Degradation (missing tools):**

- **git-spice absent**: The Stack tab is hidden (no topology to display), and the Overview shows no stack line.
- **A project's rendezvous directory is unwritable** (e.g. a read-only checkout): that one project simply loses click-to-focus (see `afk notify` below) — its sessions still discover, run, and open normally from the switcher or session list, and every other project's click-to-focus is unaffected.
- **State file unwritable** (e.g. a read-only filesystem): selection memory and the switcher's open counts both last only for the current run, so the switcher falls back to awaiting-input-then-alphabetical ordering on the next launch; no error is shown.
**Showing agent sessions.** Run as many sessions as you like; the dashboard shows one agent pane at a time. Use `ctrl+t` to select the highlighted session's agent surface: it attaches a running host or restarts a stopped host without moving keyboard focus. Changing the shown agent session detaches from the outgoing session — its host is a separate process, so it keeps working in the background, and showing it again is another `ctrl+t`. Nothing is restarted unless the selected host is stopped, and no session state is lost.

**Mouse.** The dashboard asks your terminal for mouse reporting and forwards what it gets into the pane, so a scroll wheel over a session scrolls that session — Claude Code's own scrollback, not your terminal's. Clicks and drags go through too.

The catch is that your terminal doesn't see click-drag while the dashboard holds the mouse, so its native text selection is unavailable. `ctrl+g` is the way out: press it and the mouse goes back to the terminal, select what you want, press it again and the pane has it. Nothing detaches either way. The same chord works in `afk attach`.

**One client per session.** A session's host serves exactly **one** connected client. Connecting from a second dashboard (or a `afk attach`) **steals** the session, and the first client is disconnected with a toast saying so. The session itself never notices — it has no idea anyone is watching.

---

### `afk clean` — Reconcile and clean up sessions

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

The dashboard's `c` key routes to the matching path above by session kind, with kind-specific confirmation prompts.

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

### `afk attach` — Attach to a running session

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
# press ctrl+t to show and attach the highlighted session

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

### `afk notify` — Send a desktop notification

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
- **App icon**: Medium-gray pickaxe (⛏) if available, omitted silently if missing
- **Sound**: Backend-specific (`default` for alerter, `Ping` for osascript)

**Click-to-focus:** When `alerter` is present and a `afk dash` is running (which records pane metadata for every project it knows about, not just the one it launched in), the notification includes a Focus button. Clicking it switches the dashboard to the session's project first if that project isn't already active, then switches the tmux client to the dashboard's pane and opens+focuses the named session, so keystrokes reach the Claude session. Without a running dashboard the toast still appears with correct formatting, sound, and grouping — it just has no Focus button. This is correct by design: a button that cannot act is worse than no button.

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

### `afk plugin install` — Install the agent plugin

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

### `afk plan check` — Tick a task in a plan

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

### `afk tasks` — List the master plan's tasks

Show every task in the project's master plan, with its status and what it is still waiting for.

```bash
afk tasks [--project DIR]
```

A master plan is a markdown file where each task is a heading that starts with its id, followed by a `- **Depends on:**` line:

```markdown
#### T4.3 — Care reminders
- **Depends on:** T4.2a, T4.2b
```

- Dependencies are the task ids on that line (`T4.2a, T4.2b`). Text in parentheses is ignored. `all prior` means every task listed earlier in the plan. Any other text with no task ids in it (for example `none` or `M3–M8 as available`) counts as no dependencies, and `afk tasks` prints a note so you can check it.
- Each task has one of seven statuses: `pending` (the default, for a task the ledger does not mention), `running`, `shipping`, `parked` (waiting for you to answer something), `done`, `failed` and `superseded` (kept in the plan only as history, replaced by other tasks).
- The last column lists the dependencies not yet finished. A dependency on a superseded task is finished only when every task that replaced it is.
- `--project DIR` — The project to read. Defaults to the current directory.

The commands read the project's main checkout, so they give the same answer when you run them inside a session's worktree.

**Where the plan is.** The plan path comes from `plan` in the project's `afk.toml` and defaults to `docs/PLAN.md`:

```toml
# afk.toml, in the project's root folder
plan = "docs/PLAN.md"
```

**Where the statuses are kept.** In `.afk/build/ledger.json` under the project. If the file is missing, every task is `pending`. If it is damaged (for example, half-written by hand), the commands stop with a message naming the file instead of guessing; fix the file, or delete it to start again from every task pending.

**Exit codes:** 0 when the table was printed, 1 when the plan, `afk.toml` or the ledger could not be used, 2 when the command was written wrongly.

### `afk next` — Show the task that would run next

Print the one task afk would start now, or say why there is none.

```bash
afk next [--project DIR]
```

When there is a task, the line starts with its id and title, for example `T4.2b  Task UI`. Otherwise the line says what is in the way: something is already running, everything is done, a task waits for you, a task failed and blocks the rest, or the plan can never finish (for example a replaced task whose replacement is missing). Having nothing to start is a normal answer, not an error.

**Exit codes:** 0 whenever it printed an answer (including "nothing to start"), 1 when the plan, `afk.toml` or the ledger could not be used, 2 when the command was written wrongly.

### `afk ledger set` — Record a task's status by hand

Set one task's status in the ledger. This is how you tell afk about work that was done before it was running, and how you correct a mistake.

```bash
afk ledger set [--project DIR] [--replaced-by IDS] <task-id> <status>
```

- `<task-id>` — Must be a task in the plan, so a typo is refused instead of being recorded for no task.
- `<status>` — One of `pending`, `running`, `shipping`, `parked`, `done`, `failed`, `superseded`.
- `--replaced-by IDS` — Comma-separated task ids that replace the task. Required with `superseded`, and refused with any other status.
- `--project DIR` — The project to work in. Defaults to the current directory.

To seed a project that is already part way through its plan:

```bash
afk ledger set T1.1 done
afk ledger set T1.2 done
afk ledger set T4.2 superseded --replaced-by T4.2a,T4.2b
afk next        # names the first task still to do
```

Running the same command twice changes nothing the second time. Several of these commands can run at once; they take turns using a lock file.

**Exit codes:** 0 when the task has the status (changed now or already), 1 when the plan or ledger could not be used or the task is not in the plan, 2 when the command was written wrongly (for example an unknown status).

### `afk usage` — Show how much of your usage is spent

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
- `--refresh` — Read the numbers again even if a recent reading is stored.

A reading less than 5 minutes old is reused, and the report then says how long ago it was read ("less than a minute ago", "3 min ago"). Anything older, or `--refresh`, asks the agent again; that costs no model usage. The probe runs from afk's state directory, not from the folder you ran `afk usage` in, so it leaves no records in your project. The last reading is saved per machine in `usage.json` under `$XDG_STATE_HOME/afk/` when that is set to an absolute path, otherwise `~/.local/state/afk/`. If a probe fails and an earlier reading exists, the report shows that one and exits 1.

The `Governor:` line is the decision afk would make from the reading:

- `run` — Usage is within the limits; afk runs as normal.
- `no_launch` — A window is past the limit for starting new work (75% for the session, 85% for the week). Nothing new starts; a running session carries on.
- `pause` — A window is at or past 90%. A running session would be paused until the window resets, and the line says when.
- `hold` — The reading is missing or more than 30 minutes old. Nothing new starts and a running session is left alone.
- `refuse` — Extra usage is switched on for the account. afk will not run until you turn it off.

A provider with no way to report usage prints "Usage is unknown" and exits 0; the governor then says `run`.

**Exit codes:** 0 when the report was printed, 1 when usage could not be read or the reading could not be saved, 2 when the command was written wrongly (an unknown flag, provider or extra word).

### `afk inbox` — Things that need you

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

**Push notifications.** To get a push on your phone, put your [ntfy](https://ntfy.sh) topic in `afk.toml` in the project's top folder:

```toml
[notify]
ntfy_url = "https://ntfy.sh/your-topic"
```

Only items that need you send one, and how loudly depends on the kind (ntfy priority 5 is the loudest):

| Item | Priority |
|---|---|
| `failure`, stops a task | 5 |
| `failure`, does not stop | 4 |
| any other kind that stops a task | 4 |
| `env_var` or `external_action`, does not stop | 3 |
| `verification`, does not stop | 2 |
| `question` or `notice`, does not stop | no notification |

With no `afk.toml`, or no `ntfy_url` in it, afk sends nothing and that is not an error. The item is always written first. If `afk.toml` cannot be read, or the notification fails, afk prints a warning (`the item was added, but ...`) and still exits 0, so an agent never thinks the item was lost. A notification waits at most 10 seconds and is not retried.

**Exit codes:** 0 when the command did what was asked (including an added item whose notification failed), 1 when the item was not found, was already answered, or a file could not be read or written, 2 when the command was written wrongly (for example neither `--block` nor `--no-block`).

---

## Session Model

afk sessions are **detached processes**: the session host runs independently in the background, surviving dashboard exit, terminal closure, and even `afk attach` disconnections. This allows:

- **N concurrent sessions**: Create and run multiple sessions independently in the same repo
- **Non-exclusive attachment**: Only one client can be connected at a time (a new attach disconnects the previous one), but the session itself is not exclusive — you can detach, do other work, and reattach later
- **Host restart**: If a session's host crashes or is killed, `afk dash` can restart it (press `ctrl+t` on the selected session)
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
- Liveness detects a wedged child by nudging it with SIGWINCH and expecting a repaint. Whether the OpenCode TUI repaints on SIGWINCH is not confirmed. A healthy opencode session can then show as wedged (⚠) until its output resumes. Restart is still a confirmed manual action (`r`), so a false wedge costs a stale cell, not a kill.

A host restart resumes the conversation by its id (the `session_id` in status.json) with the provider's resume argv in the table above. A dashboard relaunch of a stopped session reads the same sidecar, so a stopped Codex or OpenCode session relaunches under its own CLI, not `claude`. A sidecar naming an unknown provider is an error, never a fallback to `claude`.

**From the dashboard.** The `n` form's Provider selector (see `n` above) creates a session under any provider. Picking `codex` ticks Autonomous, and the form won't submit `codex` with Autonomous unticked. The `o` key opens a provider picker that defaults to Claude (see `o` above), and `afk new --scratch --provider codex` (or `opencode`) creates a one-off for another provider from a shell. Non-default sessions show their provider on the row, in the detail head, and in the `/` filter. Claude sessions don't show anything extra.

### One-off Sessions

A one-off session (created via `afk new --scratch` or the dashboard's `o` key) is a lightweight alternative to a planned session:

- **No branch, no worktree, no plan, no `status.json`** — only a `.afk/{name}/` directory containing `host.json` and `host.log` (the host's socket lives outside it, under `os.TempDir()/afk-{uid}/`)
- **Discovered via `host.json`** — rather than through the status.json/branch/worktree reconciliation that planned sessions use, making them legitimate even without a conductor or a plan
- **Bare CLI for the chosen provider in the repo root** — `claude` by default, or `codex` / `opencode --standalone` when created with `--provider` or the `o` picker. Runs with full repo context but no planning workflow, ideal for quick explorations or ad-hoc help
- **Cleaned without phase gate** — `afk clean <scratch-N>` removes it directly with no "is it done?" check, since there is no work to lose
- **Auto-named `scratch-N`** — when created without an explicit name, the system allocates the lowest available slot (reusing gaps from cleaned sessions)
- **Mutable display label** — press `l` in the dashboard to rename it in the UI without changing the on-disk identity at `.afk/{name}/`

---

## Multi-project support

By default `afk dash` only knows about the repo you launched it from. Give it search roots in a config file and it discovers every other afk-enabled repo beneath them too, lets you switch between all of them from inside the dashboard (`p`, see [`afk dash`](#afk-dash--launch-the-dashboard)), and no longer needs a repo to launch from at all.

### Config file

Location: `$XDG_CONFIG_HOME/afk/config.json` if `XDG_CONFIG_HOME` is set, else `~/.config/afk/config.json`. A missing file is not an error — it's the ordinary, no-config-yet case, and afk behaves as if it existed with every key at its default.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `search_roots` | array of strings | `[]` (none) | Directories to scan for other afk projects. `~/` expands to your home directory; a relative path resolves against the working directory afk itself is run from, not the config file's location. |
| `scan_interval` | duration string | `"60s"` | How often the dashboard re-scans `search_roots` for new or removed projects — and re-reads this file, so an edit to `search_roots` or `max_depth` takes effect within one interval, no restart needed. Parsed by `time.ParseDuration`, e.g. `"30s"`, `"2m"` — but a value below a 1s floor (comfortably faster than any real scan can complete, so a smaller number can only ever queue concurrent scans rather than run more often) is a load error, not silently raised to the floor. |
| `max_depth` | integer | `3` | How many directory levels below a search root the scan descends before giving up on that branch. Must be at least 1; `0` or a negative value is a load error, not silently replaced with the default. |

Example:

```json
{
  "search_roots": ["~/ontra", "~/code/side-projects"],
  "scan_interval": "30s",
  "max_depth": 4
}
```

A search root may itself be an afk repo — it registers as exactly that one project, so a single extra project doesn't need a parent directory invented for it just to be discoverable.

**A malformed config is not a startup error.** Invalid JSON, an out-of-range or unparseable `scan_interval`, or a `max_depth` below `1` all make config loading fail — but `afk dash` still launches; it never exits over this. Whatever projects the dashboard already knew about (the launch repo, and anything a prior successful scan found) stay visible and usable. The load error itself appears as a toast in the dashboard rather than on the terminal before launch, so fixing the file is something you do from inside the running dashboard, not before opening it.

### Scan bounds

The scan is bounded by three independent limits, all of which matter on a normal `$HOME`:

- **Depth** — capped at `max_depth` levels below each search root.
- **Repo boundaries** — the walk never descends into a directory once it has qualified as a git repo root, so everything inside a checkout (`node_modules`, `vendor`, build output, `.git` itself) is never walked. Linked worktrees under `.claude/worktrees/` are excluded the same way and never appear as their own project.
- **Symlinks** — never followed. A search root that is itself a symlink is resolved once before the walk starts; any symlink the walk encounters while descending is refused outright, never entered.

A directory counts as a project only if it's a git repo root (a main checkout, not a linked worktree) **and** contains a `.afk/` directory.

### Launching outside a repo

`afk` and `afk dash` no longer require a git repo to launch from. With no active project — because you launched outside any repo, or `--project` named something that turned out not to be a git repo, and no configured search root has found anything (yet) — the dashboard opens to an empty state naming the config file to edit and a one-line example, instead of printing usage and exiting 1. It's replaced by the normal view the moment a scan (or a repo you `cd` into and relaunch from) finds a project.

---

## Dependency Matrix — Degradation Paths

| Tool | Absence Impact | Workaround |
|---|---|---|
| **tmux** | None. afk never uses tmux: sessions are detached host processes rendered in an embedded pane, by both `afk dash` and `afk attach`. | n/a |
| **git-spice** (`gs`) | `afk new`'s base picker offers the repo's local branches with trunk first. New branches are plain git branches. `afk dash`'s stack panel is hidden. | Explicit `--base BRANCH` to `afk new`. Stacking is still possible via `base_branch` in status.json. Use `--no-git-spice` to force plain-git mode. |
| **worktrunk** (`wt`) | **When present (with git-spice also installed):** Branches are created via `wt switch --create` and are tracked in git-spice best-effort. The base picker shows all local branches (trunk, tracked stack, every other local branch). When absent, git-spice creates and tracks the branch when the base is tracked, and falls back to a plain git branch with a warning otherwise. | Worktrees are created at `.claude/worktrees/`. |
| **notifier** (`alerter`, `osascript`) | Session state updates silently (no desktop notifications). Awaiting-input state is still recorded in status.json and visible in the dashboard. Desktop notifications are **macOS-only**. `alerter` provides full-fidelity notifications (sound, per-session replacement, click-to-focus); `osascript` is the reduced-fidelity fallback (formatting only — no click-to-focus). | Install alerter: `brew install vjeantet/tap/alerter` (note: this is a **tap**, not homebrew-core). If a toast vanishes before you can click it, the setting to look at is System Settings → Notifications → **Terminal** (alerter delivers under Terminal's identity, so look for Terminal, not alerter) → style Alerts rather than Banners. **Unverified:** whether that makes these toasts persist was never observed — System Settings cannot be driven programmatically, so it is yours to confirm, and on at least one machine `com.apple.Terminal` is absent from `com.apple.ncprefs.plist` entirely (while delivery still works), so the row may not appear until macOS registers it. Click-to-focus requires a running `afk dash` (which records the pane metadata); without it the toast still appears with formatting, sound, and grouping but has no Focus button. |

**Push notifications** (`afk inbox`) use `ntfy_url` in `afk.toml` and need no desktop notifier, so they work on Linux; with no `ntfy_url` they are silent.

None of these absences cause failures; the CLI degrades gracefully. The only hard requirement is **git** (and **Claude Code CLI**, for `/afk:go`; **Codex CLI**, for `--provider codex` sessions; **OpenCode CLI**, for `--provider opencode` sessions).

---

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
- **internal/inbox**, **internal/projectconfig**, **internal/notify** — The owner inbox behind `afk inbox`, the `afk.toml` reader (`[notify]` only), and the desktop and ntfy notification senders
- **internal/pluginstall** — `afk plugin install`: writes the embedded plugins and drives each agent CLI's install
- **plugins/afk** — The Claude Code and Codex plugin (skills, hooks, Workflow scripts). `plugins/afk/skills/conductor/status-schema.md` is the binding status.json contract.
- **plugins/afk-opencode** — The OpenCode V2 port of the same plugin; see its `PROVENANCE.md` for the porting glossary

Each package is hermetic with injectable dependencies for testability. Sessions are never created/modified in tests; discovery and state parsing are tested against real git repos and fixture files.

---

## Version Notes

This is the session manager and dashboard that afk starts from. Codex and OpenCode run as optional per-session providers (see [Agent provider](#agent-provider)); version parity with the Claude Code plugin is the goal. The always-on runner is planned in [`docs/PLAN.md`](docs/PLAN.md) and is not built yet. Small items on the list:

- Real-run output echoing the repo path (avoid the wrong-cwd misfire)
- Additional notifier backends (currently macOS-only: `alerter` for full-fidelity, `osascript` for fallback)
