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

The afk workflow itself (`/afk:go`, `/afk:work-plan`, `/afk:master-plan`, `/afk:execute`, `/afk:review`, the conductor, and its hooks) is an agent plugin that ships in this repo under `plugins/` and is embedded in the binary. Install it with the command below, which asks which of your agent CLIs to install into:

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

Open an interactive TUI dashboard showing afk sessions, their phases, progress, and live diff stats — for the current repo by default, and for every repo discovered under your configured search roots if you have any (see [Multi-project support](#multi-project-support)). Selection preview shows a running session's agent pane. `enter` moves keyboard focus to the right side, and `ctrl+space t` switches that side between the agent and the overview. This is also the default action when you run `afk` with no arguments on a terminal (see [Default action](#default-action--afk-with-no-arguments)). Unlike earlier versions, `afk dash` does not require being run from inside a git repo — see [Launching outside a repo](#launching-outside-a-repo).

```bash
afk dash [options]
```

**Options:**

- `--interval DURATION` — Refresh interval for session discovery (default `2s`; e.g., `500ms`, `1s`, `5s`). File reads only — never blocks the UI.
- `--open <name>` — Open the named session's embedded pane on start, once the dashboard's first discovery load and first terminal resize have both arrived. The cursor moves to that session, so its pane is the one on screen; the keyboard stays on the list until you press `enter`. If the session's host is not running yet (the usual case immediately after `afk new`), it is restarted first. If the named session isn't found, the dashboard starts normally and shows an error as a toast. This is how `afk new` hands its terminal over.
- `--project PATH` — Launch with `PATH`'s repo as the active project instead of the current working directory's. `~/` is expanded; unlike `new`/`clean`, `dash` does not reject a `PATH` that turns out not to be a git repo — it just starts with no active project, the same [empty state](#launching-outside-a-repo) a launch outside any repo produces. Every other project (the ones in `config.toml`'s project list and any found under the search roots) is still available in the switcher (`p`, below) — this only picks which one starts active.

**Focus.** Keyboard focus is on the session list (left) or on the right side. The right side shows one of two views: the **agent** (the selected session's pane) or the **overview** (the detail pane). The list never controls the right side, whatever it shows. `enter` moves focus to the right side; `ctrl+space \` (or `esc` in the overview) moves it back to the list; `ctrl+space t` switches the view, and only while the right side has focus. A row with no live agent (stopped, pending, worktree fault, cleaning) always shows its overview. `enter` never restarts it; `ctrl+space r` does.

**Keybindings:** The dashboard has two kinds of keys. A few keys for moving around work as they are. Every action goes through one key, the **leader**, `ctrl+space`: press it, then the action's key. Because the leader is the only chord the dashboard claims, the agent pane passes every other key to the agent, so nothing you type in a pane is taken by the dashboard. Press `ctrl+space ?` for the full list in-app, or press `ctrl+space` and type to search the actions by name.

**Moving around** (no leader needed):

- `↑/k`, `↓/j` — Move the selection up and down on the list.
- `pgup`/`pgdn`, `home`/`end` — Page the list, or jump to its first or last session.
- `enter` — Move keyboard focus to the right side (see below).
- `esc` — Close the full help, then clear an applied filter; from the overview, give the keyboard back to the list.
- `tab`/`shift+tab`, `1`–`6` — Switch the detail pane's tab (overview focused).
- Scroll keys (`↑/k`, `↓/j`, `ctrl+u`/`ctrl+d`, `pgup`/`pgdn`, `home`/`end`) and the mouse wheel — Scroll the detail pane (overview focused).
- `ctrl+c` — Quit from the session list or the overview, so a stuck dashboard can always be left. In the agent pane it goes to the agent.

**The leader** (`ctrl+space`, then the key). Many terminals send the same byte for `ctrl+space`, `ctrl+@` and `ctrl+2`, so those open the dialog too; the dashboard cannot tell them apart.

| After `ctrl+space` | Action |
|---|---|
| `n` | New session |
| `o` | One-off session |
| `p` | Switch project |
| `A` | All projects |
| `+` | Add a project to the daemon's list (all-projects screen) |
| `-` | Take the selected project off the daemon's list (all-projects screen) |
| `c` | Clean session |
| `r` | Restart agent |
| `l` | Edit label |
| `/` | Filter sessions |
| `x` | Clear filter |
| `b` | Build tasks |
| `i` | Inbox |
| `P` | Pause the build |
| `U` | Resume the build |
| `R` | refresh usage: take a fresh usage reading for the header, as `afk usage --refresh` does |
| `S` | Set the selected task's status (Build tab, right side focused) |
| `T` | Show the selected task's tokens (Build tab, right side focused) |
| `f` | Focus the right side |
| `t` | Switch the right side between agent and overview |
| `\` | Close the pane (from the list); back to the list (from the right side) |
| `g` | Mouse to the terminal, or back |
| `w` | Resize the panes |
| `?` | Help |
| `q` | Quit |
| type `extend`, then `enter` | extend the plan: starts a planning session that adds a milestone (nothing to type) |
| type `amend`, then `enter` | amend the plan: asks for one sentence about the change first |
| type `document`, then `enter` | reconcile the plan with a document: asks for the document's path first |
| type `start a new plan`, then `enter` | start a new plan: asks for a folder, or a new project's name to create under your profile's `project location` (and then offers a private GitHub repository for it) |
| type `errors`, then `enter` | check the plan: lists the master plan's errors and warnings, as `afk plan lint` does, in a scrollable box; with no plan it shows a notice |
| type `approve`, then `enter` | approve the plan: for a new plan at review, asks for a yes first (showing the project, the plan's path and the checker's counts), then runs the same steps as `afk plan approve`; dimmed with the reason otherwise |

The letters that used to run these actions on the list (`n o p c r l / b i q f ? P U`) and `:` now do nothing but show a one-line notice naming the key to use instead, for example `n moved: press ctrl+space then n`. The `:` palette is gone; the dialog's own name filter does its job. In the agent pane a chord such as `ctrl+t` or `ctrl+\` now belongs to the agent.

**Plan actions, by name.** The three change sessions in [`afk plan`](#afk-plan--plan-a-project-with-your-agent) can also be started from the actions dialog. They have no key, so the table above lists them as typed names: press `ctrl+space`, type the name, and press `enter` on the highlighted row.

- **extend the plan** (type `extend`) starts straight away, with nothing to type.
- **amend the plan** (type `amend`) opens a one-line form. Type one sentence about the change and press `enter`. A blank sentence keeps the form open and starts nothing.
- **reconcile the plan with a document** (type `document`) opens a one-line form. Type the document's path inside the project (for example `docs/design-system.md`) and press `enter`. Type `document`, not `reconcile`: the letter `r` is the restart key, so a typed `reconcile` restarts the selected session when it is stopped.
- **start a new plan** (type `start a new plan`) opens a one-line form that asks for a folder or a name, pre-filled with the active project when that project has no plan.
  - **A folder** (a full path, or one starting with `~/`) must exist. A folder that is not a git repository yet is made one, as `afk plan` does. A folder that already has a plan is refused, and the dashboard points you at extend, amend or document instead. An open planning session in that folder is reopened rather than started again.
  - **A name** (no `/` and no `~`) is made into a folder under the `project location` entry in your profile, after a yes/no question that shows the full path (`y` creates it and makes it a git repository; `n` or `esc` creates nothing). A name may use letters, digits, `.`, `_` and `-`, must start with a letter or digit, and may be at most 100 characters. If your profile has no project location, the name is refused and the message says how to set one (`afk profile set "project location" ~/code`) or to type a full path instead. The location folder itself is never created. If a folder with that name already exists, the dashboard offers to plan in it and creates nothing. If making the repository fails part-way, the folder stays on disk, nothing is removed, and no planning session starts; type its full path to plan in it, or remove it yourself.
  - **A GitHub repository, after a name.** Once the folder is made, the dashboard checks that `gh` (the GitHub command) is installed and logged in, then asks whether to make the private repository `<account>/<name>` on GitHub, where the account is the one `gh` is logged in as. `n` or `esc` (the default; `enter` does not answer) makes nothing. `y` makes the repository, always private, and sets it as the folder's `origin`; nothing is pushed. If `gh` is not installed or not logged in, nothing is asked and the notice says so and what to run (`gh auth login`, then `gh repo create --private` in the folder). If GitHub already has a repository with that name, or the create fails, the notice gives the reason and the folder is left as it is. The question is asked every time and only for a folder the dashboard has just made, never for a folder you typed or one that already existed. Planning starts whatever the answer. The outcome is one line at the start of the notice, and it stays at the start of the notice once the planning pane opens. A folder made this way has no commits yet, which the dashboard treats as having no branches yet, not as a failed branch listing.
  - On success the dashboard switches to that project and opens its planning pane.

- **check the plan** (type `errors`) opens a scrollable box titled `Plan check · <plan path>` with a counts line (`3 milestones · 12 tasks · 2 owner tasks · 1 open questions`), then `<plan path>: no problems found` or `<plan path>: 1 error, 2 warnings`, then one finding per line, errors first, worded as `afk plan lint` words them (`docs/PLAN.md:42: error: T1.2: missing Files [rule]`). `↑/↓`, `pgup`/`pgdn`, `home`/`end` scroll and `esc` closes it. The box shows what was read when it opened; open it again to see a newer reading. Type `errors` (or `warnings`, which shares the help line but cannot be typed first because `w` is the resize key). With no plan it shows the notice `There is no plan to check at docs/PLAN.md.` and opens nothing; before the first read it says `The plan has not been read yet; try again in a moment.`

- **approve the plan** (type `approve`) is available only for a new plan that is at review. Otherwise the row is dimmed and says why. Press `enter` and the confirm shows the project, the plan's path and the checker's counts. `y` approves, and `n` or `esc` leaves the plan at review. The steps are the ones `afk plan approve` runs, in the same order, and the commit and push run away from the screen. A plan with one or more errors has no yes: fix them first, and type `errors` to see them. The result is one notice, and a long git error is cut to its first line (run `afk plan approve --project <folder>` for the rest).

A refused start shows the reason in the same words the command prints, and nothing is started. When a planning session is already open, the dashboard says which one it is (its mode and phase) and asks whether to open it: `y` opens it, and `n` or `esc` leaves it closed. After a start, the dashboard selects the `master-plan` row and opens its pane, as `enter` does on that row.

- `↑/k`, `↓/j` — Move selection up/down. **Auto-previews the selected running session in its agent pane** as you move (no focus key needed). That's agent view only: with the overview showing, moving the cursor previews nothing, and the overview stays put until you switch views with `ctrl+space t` from the right side. The cursor must settle for 300ms before connecting — so a held key or fast stepping connects only to the row you stop on. Intermediate rows aren't dialed. **This steals the session from any other client attached to it**, including a `afk attach` in another terminal: a host serves exactly one client, and connecting detaches the previous one. Fast browsing is safe (only the settled row connects), but deliberate slow stepping does connect to each row. A **stopped** session isn't restarted by scrolling onto it (cursor movement starts no processes) and shows its detail view instead. While a pane's open, moving onto any row that isn't the paned session shows that row's detail view rather than the previous session's pane. A session being cleaned is never previewed. A preview that fails to connect is completely silent — no error appears from a bare cursor move. The selection wraps: `↑` on the first session lands on the last, and `↓` on the last comes back around to the first.
- `pgup`/`pgdn`, `home`/`end` — Page the session list, or jump to its first/last session. The list windows to the sidebar's height and pages rather than overflowing it, so on a short terminal or with many sessions these are how you reach a row that is off screen. Each one previews the row it lands on, exactly as `↑`/`↓` do. Deliberately narrow keys: bubbles' own defaults for these gestures are `h`/`l`/`f`/`d`/`b`/`u`/`g`/`G`, three of which the dashboard already spends on label, focus-pane and one-off. These don't wrap. `pgup` on the first page stays where it is, and `end` always lands on the last row.
- `ctrl+u`/`ctrl+d`, `↑/k`/`↓/j`, `pgup`/`pgdn`, `home`/`end` (overview focused) — Scroll the detail pane: half a page (`ctrl+u`/`ctrl+d`), one line, one page, or to the top or bottom. These act only after `enter` has focused the overview; from the list the plain keys move the session cursor and `ctrl+u`/`ctrl+d` do nothing. The content scrolls only when it is taller than the pane, and a right-aligned position percentage shows on the pane's last row **only when the content actually overflows**. The offset stays where you left it, so switching back to the overview picks up where you were.
- `tab`/`shift+tab`, `1`–`6` (overview focused) — Switch the detail pane's tab: Overview, Waves, Stack, Notes, Diff, and Build when the project has a master plan. `tab` cycles forward and wraps, `shift+tab` goes back, and a digit jumps straight to that position in the strip. The digits index what's actually shown, so in a repo without git-spice (no Stack tab) `3` is Notes. Switching tabs puts the scroll back at the top. Like the scroll keys, these act only while the overview has focus; from the list they do nothing.
- `ctrl+space /` — Filter the session list. Type to narrow it (fuzzy, matching the display label, the on-disk name, and the provider for anything that isn't `claude`, so a labelled session is findable by either and typing `codex` or `opencode` finds those sessions), `enter` applies the filter and hands the keyboard back to the dashboard, `esc` cancels while typing or clears an applied filter afterwards. While a filter is applied every per-session key — `enter`, `ctrl+space c`, `ctrl+space l`, `ctrl+space r` — acts on the filtered selection, and the 2s discovery tick keeps the filter applied rather than dropping it. Switching projects with `ctrl+space p` drops it, since a query written against one project's sessions would silently hide another's. Filtering never connects to a session: the filter prompt moves the selection and every keystroke changes the match set, but no preview is armed while the prompt is open — otherwise typing a search would dial (and steal) whichever session currently matched. Accepting or cancelling the filter previews the row it settled on, as any other selection move does. `ctrl+space x` clears an applied filter, as `esc` does.
- `enter`, `ctrl+space f` — Move keyboard focus to the right side. What happens depends on the view the right side is showing:
  - **Agent view, running session with a pane:** focus that pane. Keys go to the session.
  - **Agent view, running session with no pane yet:** connect the session and focus its pane when it arrives. If you go back to the list (`ctrl+space \`) or switch to the overview while it connects, the pane arrives without taking focus. This is the one case where enter connects. It claims the auto-preview already dialing for that row, so a cursor that has just settled does not race it.
  - **Agent view, row with no live agent** (stopped, pending, worktree fault, cleaning): focus the overview. Enter never restarts a host; `ctrl+space r` does.
  - **Overview view:** focus the overview. Its scroll keys (`↑/k`, `↓/j`, `ctrl+u`/`ctrl+d`, `pgup`/`pgdn`, `home`/`end`), the tab keys and `ctrl+c` work, and the leader opens every action. Every other key is ignored, so a scroll gesture can never open, clean or steal a session.

  A connect that fails hands focus back to the list, where enter retries.
- `ctrl+space t` — Switch the right side between the agent and the overview. It works only while the right side has focus, and it never moves focus. Switching to the agent connects the selected running session if it has no pane yet, the same connect `enter` does. Switching to the overview leaves the pane running behind it, and the pane keeps tracking terminal size. On a row with no live agent it does nothing, because that row always shows its overview. From the list it does nothing. **Note:** the dashboard no longer takes `ctrl+t` from the agent pane, so a Claude Code session watched through a pane keeps its own todo toggle.
- `ctrl+space \` — From the right side, return keyboard focus to the list. The pane or overview stays on screen, and `esc` does the same from the overview. From the list, close the open pane if there is one. The session keeps running in the background either way. `afk attach` still detaches with `ctrl+\`. Closing an unfocused pane is how you clear one that auto-preview opened for you, which matters because `ctrl+space c` won't run while any pane is open.
- `ctrl+space g` — Hand the mouse to your terminal, and press it again to take it back. The help line shows the current state, but only while you're focused into the pane. Inside the creation form and the clean confirmation, where every other key belongs to the dialog, `ctrl+g` itself still toggles it. While either modal is up the toggle still works, but the help line is listing the dialog's keys at that point, so the toast is what tells you where the mouse went.
- `ctrl+space w` — Enter resize mode, which moves the divider between the session list and the main region. Inside it, `h`/`←` and `l`/`→` move the divider one percentage point per press, and `H`/`shift+←` and `L`/`shift+→` move it five. The list, the detail view and the embedded pane re-lay out live on each press. `ctrl+w` again saves the split to the state file, so it's restored on the next launch, and `esc` cancels and restores the width from when you entered. Quitting with `ctrl+c` leaves resize mode without saving, `ctrl+g` still toggles the mouse, and other keys are ignored until you save or cancel. It's unavailable while a session pane is focused, since every key then goes to the session. Press `ctrl+space \` to release the pane first.
- `ctrl+space o` — Create a one-off session. This opens a small provider picker with Claude Code preselected, and every open starts on Claude. `↑`/`k` and `↓`/`j` move the highlight and wrap at both ends. `enter` starts the one-off with the highlighted provider. `esc` cancels and creates nothing. The picker lists every registered provider (`claude`, `codex`, `opencode`) whether or not its CLI is installed, same as the `n` form's Provider selector. A missing CLI shows up in the spawn-error dialog. The one-off gets an auto-generated `scratch-N` name, with no branch, worktree, or plan. Its pane opens on the bare CLI for the chosen provider in the repo root. Disabled with no active project. From a shell, `afk new --scratch --provider codex` (or `opencode`) creates the same kind of one-off directly.
- `ctrl+space p` — Open the project switcher: a filterable list of the projects in `config.toml`'s project list (the ones `afk project add` and `afk plan approve` put there), the project the dashboard was started in, and any found under the search folders. A listed project shows the name `afk project list` shows. Each row showing its session count and an awaiting-input dot. Rows are ordered in three tiers: first, projects with any session awaiting input; second, projects by frequency of opening (most-opened first); third, alphabetically by display name with full path as a tie-break. A project's open count increments when you switch to it from the switcher, launch the dashboard with `--project PATH`, or launch from that project's directory; counts persist in afk's UI state file (see **Selection memory** below for its exact location) alongside the remembered per-project selection. `/` filters by name, `↑/k`/`↓/j` move (wrapping at both ends, same as the session list), `enter` switches the dashboard to the selected project (session list, waves, stack, and diff panels all repoint at it — a no-op if it's already active), and once the incoming project's session list loads, its first session is auto-previewed exactly as a cursor move would preview it (same 300ms settle, same refusals: a stopped session is not restarted, a fault row or a session being cleaned is never previewed). `R` triggers an immediate rescan of the configured search roots and re-reads the project list instead of waiting for the next tick, and `esc` clears an active filter first and only then closes the overlay. Two projects that share a basename (e.g. two checkouts both named `api`) are shown with just enough parent path segments to tell them apart; a project with no such collision shows its bare basename. The footer's help line lists the switcher's keys while it's open. The header's right edge also carries a `ctrl+space p: switch projects` hint whenever more than one project is known.
- `ctrl+space A` — Open the **all projects** screen: one screen for every project the daemon builds, so you can see which of them needs you without switching to each. It replaces the session list and the detail pane while it is open. There is one row for each `[[project]]` in `config.toml`, plus the project the dashboard was started in when it is not listed. Each row shows the project's name, how many tasks are done out of how many (`3/9`, or `-` when it has no tasks), a state word, one sentence on what it is doing or why it is not, how many inbox items it has open and how many of them hold a task up ("3 in inbox, 2 hold a task up"), and "plan waiting for approval" when its plan is on the list of plans waiting for you. The state words are the ones `afk daemon status` uses: `running`, `paused`, `stopped`, `waiting on you` (a task is parked on a question or a decision for you), `held for usage`, `finished`, `nothing ready`, `no plan`, and two for a project afk cannot read: `gone` (the folder in `config.toml` does not exist; fix its path or run `afk project remove`) and `can't read` (a build file in the project is damaged; the row says which). Rows that need you come first: `gone`, `can't read`, `stopped`, `waiting on you`, a blocking inbox item, or a plan waiting for approval. Each group keeps the order of `config.toml`, with the unlisted starting project last. A line at the top says "Nothing needs you" or how many rows do. At 100 columns or more each project is one line; below that it is two (name, progress and counts, then the state and its sentence), and long text is cut with an ellipsis. Keys: `↑/k`, `↓/j`, `pgup`/`pgdn`, `home`/`end` move; `enter` closes the screen and switches the dashboard to that project (on a `gone` or `can't read` row it does not switch and says why); `esc` closes it. `ctrl+space P` and `ctrl+space U` pause and resume the build of the selected row's project, not the one the dashboard is on; the notice names the project, and a `gone` or `no plan` row refuses. Other leader actions are dimmed until you close the screen (`esc`).
- `ctrl+space +` — Put a folder on the daemon's list, the same as `afk project add`. It opens a two-step form. The first step asks *Which folder?*: type the folder's full path, or start it with `~/` (for example `~/code/my-app`). A relative path such as `my-app` is refused with a line under the field, and a blank folder keeps the form open. The form starts with the active project's folder already typed in when that folder is not on the list yet. `enter` goes to the second step, *What should it be called?*: type a name, or leave it blank to use the folder's own name, then `enter` adds it. `esc` on either step adds nothing and says so. The folder must be a git repository, and the same refusals as `afk project add` apply (for example a name that is already taken, or a folder that is not a repository); the notice says why and nothing is written. A folder that is already on the list is not an error: the notice says so and nothing changes. If you are still on this screen when the add finishes, the cursor moves to the new row; a result that arrives after you have closed or reopened the screen is shown as a notice only. Like the other actions here, it works only while this screen is open.
- `ctrl+space -` — Take the selected project off the daemon's list, the same as `afk project remove`. A question asks *Remove <name> (<folder>) from the daemon's list?* and says that the folder, its plan and its sessions are not touched, and that a task which is running is left to finish. `y` removes the project from `config.toml`; `n` or `esc` keeps it, and the notice says nothing was removed. The project's own files are never changed. It needs a row that is on the list: the start-up project, shown here although it has no entry in `config.toml`, is refused with "<name> is not in config.toml, so there is nothing to remove." With no row selected it says so, and with the screen closed it asks you to open it first (`ctrl+space A`). With no project in the list the screen shows the starting project and "No projects are listed in <config.toml path>. Add one with afk project add <folder>."; if `config.toml` cannot be read it keeps the last rows and names the file. The screen only reads files, and only while it is open, once every refresh tick.
- `ctrl+space b` — Show the **Build** tab: every task of the project's master plan and where it stands. It works with or without a session selected, because the build belongs to the project, not to a session. It does nothing when the project has no master plan (a short notice says so). Like the other detail keys it moves keyboard focus to the right side; `esc` brings you back to the list.
- `ctrl+space i` — Open the **inbox** overlay: the things the build loop has put in front of you, such as a question it cannot decide alone. See **Overlays and notices** below for its keys. `ctrl+space b`, `i`, `P` and `U` work from the list and the overview, and from the agent pane by way of the list; they never run while the inbox overlay is open.
- `ctrl+space P` — Pause the project's build, the same as `afk pause`: the daemon starts no new task there. A task that is already running finishes; nothing new starts until you press `ctrl+space U`. It works where `ctrl+space b` works. A short notice says what happened, including when the build was already paused.
- `ctrl+space U` — Resume the project's build, the same as `afk resume`: the daemon may start new tasks again, and a stopped line is cleared too. It works where `ctrl+space P` works, and a short notice says whether the build was resumed, whether only the stop was cleared, or whether there was nothing to resume. Neither action stops a session that is running.
- `ctrl+space R` — The refresh usage action takes a fresh usage reading now, the same as `afk usage --refresh`, and show it in the header. It says "Reading usage…" while the agent's command-line tool is asked, which can take up to a minute; the header changes when the reading lands. A second press while one is still running does nothing but say so, so readings never overlap. The reading is taken from the agent you have as the default, and it is saved to `usage.json` only when it succeeds. A failure is a plain notice: the agent's command-line tool was not found, the agent cannot report its usage, usage could not be read (check that the tool is installed and logged in), or the reading could not be saved. In every failure the header keeps the old reading. It works from the list and the overview, and is dimmed over the all-projects screen.
- `ctrl+space` — The leader itself opens the **actions dialog**: a list of every dashboard action with its key and a line saying what it does. It works from the session list, the overview and the agent pane alike, and does nothing while an overlay, a form or a confirmation is open (the inbox, the project switcher, `ctrl+space n`, `ctrl+space l`, `ctrl+space c`, a restart confirmation, the filter prompt, resize mode). Inside the dialog:
  - Press an action's key and it runs once, and the dialog closes. Pressing it from the agent pane moves the keyboard to the list first, so nothing you pick is ever typed to the agent (the few that belong to the right side, `t` and `g`, leave the keyboard there).
  - Type letters that are not an action's key to filter the list by name or help text, `↑`/`↓` move the highlight and `enter` runs the highlighted row.
  - `esc` closes the dialog and sends nothing to the agent.
  - `ctrl+space` a second time closes the dialog. If the agent pane had the keyboard, it also sends ctrl+space to the agent, so the chord is not lost to a program that uses it.
  - An action that cannot run where you are (for example `c` while a pane is open, or `b` in a project with no master plan) is dimmed and says why. Its key and `enter` do nothing and the dialog stays open.



- `ctrl+space ?` — Toggle full help. The help line at the bottom normally shows a curated, width-budgeted subset of keys; `?` expands it to every key that acts on the list, with the leader's actions listed as `ctrl+space X` — and collapses it back on a second press. An action that's currently a no-op for the selection (e.g. `ctrl+space r` with nothing wedged) drops out of both views automatically rather than being listed disabled.
- `ctrl+space n` — Open the in-TUI session-creation form: three fields (name, description, base branch), an `[ ] Autonomous` checkbox below the base list, and a `Provider: ‹ claude ›` selector below that. Tab cycles focus forward through all five; Shift+Tab cycles backward. `space` toggles the checkbox while it has focus (`[x] Autonomous` when on); it types a literal space in the name and description fields and does nothing on the base list. On the Provider selector, `space` or `→` goes to the next provider and `←` to the previous, cycling through `claude`, `codex`, and `opencode` and wrapping at either end. Picking `codex` ticks Autonomous for you, since Codex sessions are autonomous-only. If you untick it and leave `codex` selected, submit fails inline with the same refusal `afk new` gives (`afk new: --provider codex requires --autonomous or --scratch: Codex sessions run autonomous-only`) and nothing gets created. Checked, the form passes `--autonomous` to `afk new` (see above), and a non-default provider adds `--provider <name>` after it (`opencode` sessions can be interactive or autonomous). Enter submits from name, base, the checkbox, or the selector, and inserts a newline in the description field. `/` starts filtering the base list inside the form; Esc cancels an active filter before canceling the form. The base-branch list wraps too: `↑` on the first branch lands on the last. Submitting creates the session in the background; the dashboard stays responsive, with a placeholder row standing in until discovery reports the real session. Allowed while a pane is open — the new session gets its own host and does not disturb what you are watching.
- `ctrl+space l` — Edit the display label for the selected session. A single-field prompt lets you rename the session in the dashboard view without changing its on-disk identity — the label is display-only and the directory stays as `.afk/{name}/`. Pressing enter saves, esc cancels. Clear a label by deleting all text and pressing enter. Nothing selected is a no-op.
- `ctrl+space c` — Clean the selected session: every session confirms in the dashboard (no shell-out, no second prompt); the clean then runs in-process. Nothing selected is a no-op. Blocked while a pane is open, since clean can remove the worktree out from under a running session — press `ctrl+space \` to close the pane first (that includes a pane auto-preview opened on its own). Also blocked while a clean of that session is already running.
- `d` — From the clean confirmation modal (after pressing `ctrl+space c`), press `d` to clean **and** delete the session's branch. The branch survives by default; `y` cleans without ever touching it, and only `d` opts in to deletion. A merged branch deletes in a single `d`; an unmerged branch asks for confirmation with a second `d` to prevent accidental loss. No effect on scratch sessions (which have no branch). When a clean deleted the branch, the summary line reports its tip SHA as a recovery handle, and — if the branch was git-spice-tracked — names any upstack branches the delete retargeted so you can restack them deliberately.
- `ctrl+space r` — Restart the selected session's host. It works from the list, the overview and the agent placeholder. A **stopped** host restarts at once. From the list, focus stays on the list. From the right side in the agent view, the agent lands focused when it is back, unless you have moved focus to the list meanwhile; in the overview view it connects behind the overview, one `ctrl+space t` away. A **running** session restarts only if the list already shows it WEDGED — a healthy session ignores the key entirely. A wedged restart confirms first (y/n), since this kills a live, unrecoverable process. On confirm, the dashboard closes any pane it already has open on that session and sends a single restart request to the host; the host runs the signal ladder and resumes the same conversation (`claude --resume`, `codex resume`, or `opencode --standalone -s`, per the session's provider) on a fresh process — the dashboard never touches the child directly. A toast reports the real outcome — restarted (the toast names the provider, e.g. `codex is resuming its conversation`), failed (with the host's reason, e.g. an unusable `status.json`), or merely requested when an older host reports nothing. Confirming also **disconnects any other client attached to that session** (a `afk attach` in another terminal): the request briefly takes the session's single client slot, and the confirmation prompt says so. Press `enter` to reconnect once it is back.
- `ctrl+space q` — Quit immediately, with no confirmation. Closing the pane only **detaches** — the session's host is a separate process, so quitting the dashboard cannot end a session. Reattach with `afk dash` or `afk attach <name>`.

**Selection memory:** The dashboard remembers which session you had selected in each project, restored when you switch back or relaunch the dashboard. State is persisted to `$XDG_STATE_HOME/afk/state.json` (default `~/.local/state/afk/state.json`) and keyed by project path. If a remembered session no longer exists, the cursor falls back to the first row. Pane state (scrolling, detail view) is not remembered.

The same file also stores the sidebar split as `sidebar_pct`, an integer percentage of the terminal width given to the session list. The default is 38, and the value is clamped to 15–85. Whenever the terminal is at least 60 columns wide, the session list keeps at least 30 columns and the main region keeps at least 30. On narrower terminals the session list's 30-column floor wins. A missing or zero `sidebar_pct` means the default. Change it with resize mode (`ctrl+space w`, above), not by editing the file.

**View panels:**

When more than one project is known, every panel below describes the **active** project only, so switch with `ctrl+space p` to look at another. The header is one row in every state. It puts a `afk Dashboard` badge on the left, the cross-project attention line beside it, and a `p: switch projects` hint on the right, which only appears when more than one project is known. The attention line carries the one thing nothing else on screen can tell you: which *other* projects have sessions waiting on you. It names them while they fit (`● 2 awaiting in gamma · 1 in delta`, in the switcher's own row order) and collapses to a single phrase when they don't (`● 6 awaiting in 4 other projects`, or `● 3 awaiting in 1 other project` when there's just the one). Nothing awaiting elsewhere and it renders nothing at all; there's no `0 awaiting`. The active project's own awaiting sessions aren't in it either, since those already have a dot in the list's flag column a few lines down. These counts come from the ~60s project scan rather than the 2s discovery tick, so they can lag the active project's own list by up to a minute. As the terminal narrows the header gives pieces up in one fixed order, weakest first: the `p: switch projects` hint, then the `i: inbox` hint, then the attention line's names (they collapse to the aggregate phrase), then the long usage text (it shrinks to the short form), then the long inbox text (it shrinks to the blocking count alone, so items that only wait to be read leave the row here), then the badge, then the short usage text, then the attention line, then the blocking count. The stale notice is the very last piece to go. One exception: when the switch hint is the only thing on the right half (no usage reading and no open inbox item), the names collapse before the hint goes, as they always have. The badge going before the attention line is deliberate — you can act on an alert, and the badge only tells you what you already know you launched. The blocking count outlasts the badge and both hints because it asks you to act in this project: a task stays stopped until you answer its item, and nothing else on the row says so. When discovery is failing, a `stale · discovery failing 12s` notice sits between the badge and the attention line, and it's the last piece to go as the terminal narrows. The list under it has stopped moving, so you can't trust the other counts on the row either, because the cross-project ones come out of that same discovery pass. It's gone as soon as a pass succeeds. Whatever's left last is the only thing ever truncated, so a project name is never cut mid-word.

The right edge also carries the **inbox segment**, just before the usage text. It counts the open inbox items of the **active** project, the same items the Build tab's summary line counts, read from the snapshot the dashboard already loads on each refresh. It reads `inbox 3` when three items are open, and `inbox 3 · 2 blocking` when two of them block a task; the blocking part is drawn in the warning colour. It shows nothing when no item is open, and nothing until the first refresh has read the inbox. It does not need a master plan, and inbox files that cannot be read are not counted. Next to it, `ctrl+space i: inbox` names the keys that open the list.

When afk has a usage reading (the last time it checked how much of your plan's allowance is used), the header's right edge also carries a **usage segment**, such as `usage: paused until Thu 15 Oct 15:57 · Week (all models) 91%`. It says what the build loop decided (running, held, paused until a time, or not launching) and shows the tightest usage window, the one closest to its limit. A segment that carries a decision other than "running" is drawn in the warning colour. A reading more than 30 minutes old is marked as stale. As the terminal narrows the usage segment shrinks to a short form (`paused · 91%`) once the switch hint, the inbox hint and the names have gone, outlives the badge, and goes only after that, with the attention line, the blocking count and the stale notice still kept. The header stays one row at every width.

The reading is only taken when you ask for it, never on a timer or at start-up: press `ctrl+space`, then `R` ("refresh usage"), and the header shows `Reading usage…` while the agent's command-line tool is asked. When the new reading lands the header shows its numbers and the decision for them; a failed reading leaves the header on the old reading and says why in a plain notice.

With zero or one known project — the unconfigured default — the header is just the badge, plus the inbox segment when the active project has open items. No attention line, because there's no other project to report on, and no `ctrl+space p: switch projects` hint. No keybinding hints for `ctrl+space o` or `ctrl+space l` either; the actions dialog and `ctrl+space ?` list them (see above).

- **Session list** (left, sidebar when a pane is open) — a table with a caption row: host liveness dot (`●` running, `○` stopped), name, `PHASE`, `TASKS` (done/total), and a one-cell flag (`●` awaiting input, `⚠` wedged, `↻` autonomous). The flag cell shows one glyph, by priority: wedged, then awaiting input, then autonomous, so `↻` only appears while the session needs nothing from you and `⚠`/`●` are never hidden by it. Its top edge is labelled with the active project's name, since the box holds exactly that project's sessions; with no project known it falls back to `Sessions`. Sessions on a non-default provider get a dim tag after the name (` codex`, ` opencode`). When the row's too narrow to fit both, the tag goes and the name stays. Columns fold as the sidebar narrows: below 40 cells the phase abbreviates (`EXEC`, `REVW`, `PLAN`…), below 32 the task count goes, and below 24 only the name and flag remain. An 80-column terminal lands on the abbreviated rung. Everything that used to sit on a second line — progress bar, branch, stale reason, wedged age — is in the detail pane's Overview now. Windowed: it shows as many rows as the sidebar's height allows and pages through the rest (`pgup`/`pgdn`, `home`/`end`); when it does page, the box's bottom edge says which page you're on. `ctrl+space /` filters it, and the line under the captions is reserved for the filter prompt (blank until you open it). Sessions may appear as BROKEN (worktree directory missing) or ORPHAN (worktree present but has no afk session). These fault rows show their fault type in the phase column, are cleanable with `ctrl+space c` (no phase gate), and cannot auto-preview or show an interactive agent pane; `enter` can still focus their rendered detail view. A standalone `/afk:go` session (status.json in the main checkout, no worktree of its own) is **not** a fault: it renders as an ordinary row with a working detail pane and is cleanable with `ctrl+space c`, but `d` is not offered — the session has no branch of its own. Scratch sessions are likewise unaffected. Note that `d` in the clean confirmation of an ORPHAN row offers to delete a branch afk never created — that population includes worktrees created outside afk — so the merge-state line and the unmerged double-confirm are what stand between you and someone else's branch.
- **Embedded pane** (main region, when a session is hosted) — the live conductor's terminal output, rendered by the bundled PTY + terminal-emulator bridge (`internal/termpane`) — an experimental spike component, not production-hardened
- **Detail pane** (main region, when the overview is showing — none is open, or `ctrl+space t` has put the overview over a pane) — the selected session, in tabs (`tab`/`shift+tab`/`1`–`5`, above). The box's top edge names the session and its bottom edge lists the keys that act on it.
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
  - **Build** (only when the project has a master plan; last in the strip, so `5` in a plain repo and `6` with git-spice; `ctrl+space b` jumps to it) — about the whole project, not the selected session, so it looks the same whichever session is highlighted. It starts with a summary line (`1/5 done · T1.2: executing`, then whether the loop is paused or stopped and why, and how many inbox items need you), then one row per master-plan task: its id, a status word and its title, with a detail on the right. The status words are `pending` (not started; the detail says `waiting for T1.2` while a dependency is unfinished, `ready` when it can start, and `yours to do` for a task marked as the owner's), `running` (an agent is working on it; the detail names the session and its phase, or `paused for usage until …`), `shipping` (the work is finished and is on its way to merged: `ci running`, a pull request number), `parked` (set aside; `waiting on you: q-3` names the inbox item holding it, and `answered, starting again soon` means you have answered and the loop will pick it up within a few seconds), `done` (with the pull request and commit), `failed` (with the attempt count), and `superseded` (replaced by other tasks). Sessions that were paused for usage read as paused, not as stopped, in the session list too. The tab needs a master plan (set `plan` in `afk.toml`; the default is `docs/PLAN.md`) and reads the ledger the build loop keeps in `.afk/build/ledger.json`. While the right side has the keyboard, a `›` cursor marks one task row; `↑/k` and `↓/j` move it (and the tab scrolls to keep it in view), `pgup`/`pgdn` move it a page, and `home`/`end` jump to the first and last task. The cursor keeps its task across refreshes. `ctrl+space S` sets that task's status: choose a status, give the replacing task ids for `superseded`, and confirm with `y`; the confirm shows the change (for example `T1.3: failed → pending`) and warns when an agent is working on the task. It does what [`afk ledger set`](#afk-ledger-set--record-a-tasks-status-by-hand) does and refuses in the same words. With the cursor on a task, its row ends with the task's weighted token total (for example `2.9M weighted`) when there is room; it is the first thing dropped as the tab narrows, before the detail and the title. `ctrl+space T` opens the task's tokens as [`afk tokens <task>`](#afk-tokens--show-how-many-tokens-a-task-used) prints them (a narrower table, without the input, output and cache columns, when the window is narrow), or says `no tokens recorded for this task yet`, or why the ledger could not be read. The dashboard only reads the ledger; `afk tokens --record` stays a command. From this tab, `ctrl+space P` pauses the build and `ctrl+space U` resumes it, the same as `afk pause` and `afk resume`. A short notice says what happened: paused, already paused, resumed, the stop cleared, nothing to resume, or why it did not work. The summary line shows the new state straight away. The summary also carries a **plan line** when the project has a planning session: `plan: extend · rounds · 2 questions have no answer yet`, `plan: plan waiting for your approval` for a new plan at review, `plan: plan approved`, or `plan: amend · finished · the changes are in the plan`. A damaged planning state file is reported as such, not as no session. With no planning session the line is left out. The `master-plan` session's overview shows the same line. The dashboard reads it from files on each refresh and never writes under `.afk/plan/`.
  - **Diff** — the full `git diff` against the base branch for the focused session, structurally coloured (file headers, additions, removals), scrollable with the existing detail-scroll keys, and — like the rest of the detail pane — focusable with `enter` (above) for the full scroll keymap. Tabs are expanded to 4-column stops, and the diff is capped at 2000 lines with an on-screen marker past that. Unfocused sessions show a one-line summary (`+N/-M across K file(s)`) instead. When the diff is loading you'll see `loading diff…`; this resolves within one `--interval` refresh cycle after a tab switch. If the base branch no longer exists, the Diff tab and the Overview tab's `Diff` line both show `base branch '<name>' no longer exists` instead. This occurs when `afk clean --delete-branch` removes a branch that other sessions still record as their base, since `base_branch` is immutable after session creation.

**Overlays and notices.** The project switcher and the new-session form float as centred boxes over the dashboard, so the session list and whatever you had in the main region stay visible around them. The **inbox overlay** (`ctrl+space i`) lists the project's inbox items: `!` marks one that holds a task up, `·` one that is open and `✓` one already answered, with the ones that hold a task up first. `↑/k` and `↓/j` move, `enter` reads the highlighted item, and `esc` (or `ctrl+g`) closes the list. Inside an item you see its title, what kind it is, how long ago it was raised, its body and any numbered options (the agent's recommendation is marked). Type an answer and press `enter` to send it, or type just a number to pick that option: `2` sends the second option's text. An empty answer is not sent and shows a message instead. `esc` clears what you typed, and a second `esc` goes back to the list; `pgup`/`pgdn` scroll a long item. While you type, every key is text, so `q` does not quit. The answer is written to the item's file as answered from the dashboard (`"answered_via": "tui"`), and a task that was parked on it carries on a few seconds later, when the build loop next looks. The first answer is the one kept: if the item was already answered (from here, from `afk inbox answer`, or from the daemon's web page), the stored answer is shown and left alone. If the item's file has gone, you get a plain sentence saying so. This is the only thing the dashboard writes into a project. Confirmations (clean, restart, the failed-create report) and the label editor are dialogs on the same layer, each with its title on the top edge and its keys on the bottom edge; the footer keeps the help line underneath. Status changes appear as toasts — ephemeral notifications in the top-right corner styled by severity (success, warning, error). Background events toast the same way — a session finishing or failing, one starting to await input, a host dying between ticks, a pane taken over by another window or the web page (see *One client per session*), a stack or branch-listing refresh failing. The dashboard also sets the terminal's window title (`afk · api · 2 awaiting input`) and, in terminals that draw one (Ghostty, iTerm2, Windows Terminal), a tab progress bar tracking the fleet's task completion. Colours adapt to a light or dark terminal background, detected on startup.

**Degradation (missing tools):**

- **git-spice absent**: The Stack tab is hidden (no topology to display), and the Overview shows no stack line.
- **A project's rendezvous directory is unwritable** (e.g. a read-only checkout): that one project simply loses click-to-focus (see `afk notify` below) — its sessions still discover, run, and open normally from the switcher or session list, and every other project's click-to-focus is unaffected.
- **No usage reading** (afk has not checked usage yet, or the file is missing): the header shows no usage segment and looks as it always did.
- **No master plan** (no `plan` file, or none at the configured path): there is no Build tab, and `ctrl+space b` shows a notice saying the project has no master plan. `ctrl+space P` and `ctrl+space U` say there is no build to pause or resume. The rest of the dashboard is unchanged.
- **A plan or ledger that cannot be read**: the Build tab is still there and shows the reason in plain words (for example the line of a malformed task heading) instead of the task rows. Inbox files that cannot be read are counted and mentioned, not hidden.
- **Empty inbox**: `ctrl+space i` opens the overlay with the line `Nothing in the inbox.`
- **State file unwritable** (e.g. a read-only filesystem): selection memory and the switcher's open counts both last only for the current run, so the switcher falls back to awaiting-input-then-alphabetical ordering on the next launch; no error is shown.
**Showing agent sessions.** Run as many sessions as you like; the dashboard shows one agent pane at a time. Moving the cursor previews a running session in it while the right side shows the agent view, and `enter` focuses it. Changing the shown session detaches from the outgoing one — its host is a separate process, so it keeps working in the background. Neither moving the cursor nor `enter` restarts anything; only `ctrl+space r` does. If another client takes the session (a `afk attach` in another terminal) or the connection drops while the right side has focus, the agent view shows a placeholder that names the cause, and focus stays on the right. `enter` reconnects. When another window or the web page takes the session, the placeholder and the notice both read `<name> was taken over by another window or the web page; press enter to reopen it`. The placeholder answers only `enter` and the leader (`ctrl+space`, then `t`, `\`, `r` or `g`); every other key is ignored, so typing into a lost session never reaches dashboard actions.

**Mouse.** The dashboard asks your terminal for mouse reporting, and the mouse follows keyboard focus. With the list focused, the right side ignores the mouse, and a left click on it is the same as `enter`. With the right side focused, the wheel and clicks over it reach the session (or the overview's scroll), so a scroll wheel over a session scrolls that session — Claude Code's own scrollback, not your terminal's. Clicks and drags go through too. A click in the list returns focus to the list.

The catch is that your terminal doesn't see click-drag while the dashboard holds the mouse, so its native text selection is unavailable. `ctrl+space g` is the way out: press it and the mouse goes back to the terminal, select what you want, press it again and the pane has it. Nothing detaches either way. The same chord works in `afk attach`.

**One client per session.** A session's host serves exactly **one** connected client. Connecting from a second dashboard (or a `afk attach`) **steals** the session, and the first client is disconnected, and the dashboard says so with a notice in the same words as the placeholder: `<name> was taken over by another window or the web page; press enter to reopen it`. The host does not say who took it, so the notice does not name either one; `enter` on that pane reopens it the same way, and takes the session back. The session itself never notices — it has no idea anyone is watching.

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

The dashboard's `ctrl+space c` routes to the matching path above by session kind, with kind-specific confirmation prompts.

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

**Click-to-focus:** When `alerter` is present and a `afk dash` is running (which records pane metadata for every project it knows about, not just the one it launched in), the notification includes a Focus button. Clicking it switches the dashboard to the session's project first if that project isn't already active, then switches the tmux client to the dashboard's pane and focuses the right side on the named session. A running session is connected and its pane takes your keystrokes; a stopped one shows its overview with a hint to press `ctrl+space r` (a click never restarts a host). Without a running dashboard the toast still appears with correct formatting, sound, and grouping — it just has no Focus button. This is correct by design: a button that cannot act is worse than no button.

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

### `afk plan` — Plan a project with your agent

Start an attended agent session that takes your idea to a first draft of the master plan. The session runs the `/afk:master-plan` skill: it asks what you want to build, asks its questions in rounds, proposes how to cut the work into tasks, writes `docs/PLAN.md` and checks it with `afk plan lint`. The dashboard can start a new plan too, with its **start a new plan** action (see [`afk dash`](#afk-dash--launch-the-dashboard)).

```bash
afk plan [--project DIR] [--provider NAME] [--dry-run]
afk plan extend [--project DIR] [--provider NAME] [--dry-run]
afk plan amend [--project DIR] [--provider NAME] [--dry-run] [--yes] "<what>"
afk plan reconcile [--project DIR] [--provider NAME] [--dry-run] FILE
afk plan status [--project DIR]
afk plan advance [--project DIR]
```

- **It needs a terminal.** The session asks you questions, so with no terminal on stdin and stdout `afk plan` starts nothing and exits 1.
- **An empty folder is turned into a git repository.** A folder that is not a git repository yet gets `git init` and nothing else. The session writes the `.gitignore` itself, in the draft phase.
- **A project that already has a plan gets a question, not a new plan.** An open planning session is always resumed. With none open, `afk plan` on a terminal asks which change you want (type `1`, `2` or `3`, or the word `extend`, `amend` or `reconcile`), and any other answer starts nothing and exits 1. Without a terminal it lists the three commands and exits 1. A session that reached `review` is finished: when a new change starts, the finished session's files are moved to `.afk/plan/archive/<time>/` and afk says so. If the finished session's agent is still running, clean it first (`afk clean master-plan`).
- `--project DIR` — The project to plan. Defaults to the current directory. From inside a session's worktree it reaches the project's main checkout, where `.afk/plan/` lives.
- `--provider NAME` — The agent to run: `claude`, `codex` or `opencode`. Planning sessions work with the default agent only for now; any other prints that and exits 1. An unknown name exits 2.
- `--dry-run` — Print what would happen (the project, whether it would run `git init`, whether the session would start, resume or be reattached, and the command it runs) and change nothing.

**Changing a plan that exists.** `extend`, `amend` and `reconcile` start a planning session that changes the plan instead of writing one. They take `--project`, `--provider` and `--dry-run`, and need a terminal. Each refuses a project with no plan (run `afk plan` first) and refuses while another planning session is open (run `afk plan` to go back to it). The session changes the plan only through `afk plan patch`, `insert` and `depend` (see below), so every change is checked before it is written, and it lists each change in `.afk/plan/changes.md`. The same three can be started from `afk dash` by name, from its actions dialog; see [`afk dash`](#afk-dash--launch-the-dashboard), where the rows are described. The dashboard asks for the same sentence or path and refuses for the same reasons. The web page's project screen starts the same three: its **Change the plan** section has a row for each, and each opens a confirm before anything starts (see [the web page](#the-web-page)).

- **`afk plan extend`** adds a milestone. For when the project has grown a new part. The session asks what the milestone is for, then follows the phases `brief`, `survey`, `rounds`, `slices`, `draft`, `check`, `review`. It is the only change with a critic: `critic.md` must exist before `check` passes.
- **`afk plan amend "<what>"`** adds, splits or replaces one task. For a small change you can put in a sentence; the sentence is saved as the brief. The phases are `brief`, `survey`, `draft`, `check`, `review`, with no questions round. The session shows you the entry and asks you to confirm it before adding it; `--yes` skips that confirmation. `--yes` does not remove the need for a terminal.
- **`afk plan reconcile FILE`** brings the plan in line with a document, such as a design system. `FILE` must be a file inside the project folder (a relative path is looked for in the folder you typed it in, then in the project). The phases are `brief`, `survey`, `rounds`, `draft`, `check`, `review`; the questions round may have no question at all. A reconcile that finds nothing to change still writes one line to `changes.md`. When the document is a design system and it disagrees with the plan in ways that need your decision, the follow-up prompts are saved as `docs/design-followup-<n>.md`, numbered from 1.

A change session is finished when it reaches `review`: its changes are already in the plan. With `[planning] via_pr = true` (see `afk plan patch` below) they are in a pull request until it is merged. `afk plan status` shows the session's mode and phase.

**Leaving and coming back.** Leave with ctrl-\. The session keeps running in the background and what it has saved is kept in `.afk/plan/`. Run `afk plan` again to come back: it reattaches to a running session, or starts a fresh agent on the saved state if the session has stopped. Opening it from the dashboard does the same.

**`afk plan status`** prints the session's mode, its phase, what it is waiting for, and one `Problem:` line for each damaged file. It only reads. With no planning session it says so and tells you to run `afk plan`.

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

When a new plan reaches `review`, afk also notes it on a list of plans waiting for approval (so the web page can show it, see "The web page") and, when the project has an `ntfy_url`, sends a "Plan ready for review" notification. That message gives the counts from `afk plan lint --summary`, tells you to read the plan and run `afk plan approve`, and links to the plan's page when the web page is on. The list entry is written even without an `ntfy_url`; a failure to write it or to send is a one-line note and never stops the phase.

**`afk plan approve`** is your "go". Run it in the project, or give `--project DIR`. The dashboard's **approve the plan** action (type `approve` in `afk dash`, see [the actions dialog](#afk-dash--launch-the-dashboard)) runs the same steps behind a confirm. The web page can approve too: a new plan at review gets **Approve the plan…** on its plan page (see "The web page"), behind a confirm that names the project, the plan's path and the counts, and it runs the same steps in the same order.

```bash
afk plan approve [--project DIR]
```

It refuses, and changes nothing, when there is no planning session; when the session is a change session (`extend`, `amend`, `reconcile`: its changes are already in the plan); when the session is not at `review`; when the checker reports errors in the plan (run `afk plan lint`); when the daemon could not build from the project as it is; when the folder is not a git repository; when the plan is outside the project folder; or when afk's `config.toml` cannot be read. Each refusal says why and ends with "Nothing was changed."

Otherwise it does five things, in this order, and prints a line for each one that changed something:

1. Writes `afk.toml` with one line, `plan = "<where the plan is>"`, if the project has none. An existing `afk.toml` is never overwritten.
2. Commits the plan and the project's documents: the plan, `afk.toml`, `.gitignore`, `CONTEXT.md`, `docs/design-brief.md`, and the agent instruction files (`AGENTS.md`, `CLAUDE.md`), whichever exist. Nothing else in the checkout is staged, and git hooks run. The commit message is `docs(plan): Approve the master plan`.
3. Creates the task ledger (`.afk/build/ledger.json`), with no task done.
4. Adds the project to the projects the daemon builds, as `afk project add` does.
5. Marks the planning session `approved`, and takes the plan off the list of plans waiting for approval.

Run it again whenever you like. Each step is done only if it is not done already, so a second run after a full one changes nothing ("The plan is already approved"), and a run that stopped half way (git refused the commit, the disk was full) is finished by running it again. When a step fails, the message names it, says what was done in this run and what is left, and the plan stays at `review` until the last step is done.

When it pushes: after a commit it pushes the current branch to `origin` (never forced). It does not push when the repository has no `origin` or is on no branch, and a failed push is a note with the command to run by hand, never a failure of the approval. With `[planning] via_pr = true` in `afk.toml` it does not push at all, since a push to the base branch can be a deploy: the commit goes out with the first task's pull request.

A running `afk daemon` picks the project up within a few seconds, and starts its first ready task without a restart. The closing lines say whether a daemon appears to be running or tell you to start one with `afk daemon`.

**Where the files are.** The session's state is in `.afk/plan/` in the project's main checkout: `state.json`, `brief.md`, `survey.md`, `questions.jsonl`, `slices.md` and `critic.md`, and in a change session also `changes.md` (the changes made, one line each; its content is the gate for leaving `draft`) and, for `amend --yes`, an empty file named `yes`. `archive/` holds the files of earlier, finished sessions (see `docs/PLAN.md` section 4.8). The agent itself runs as a session named `master-plan`, with its own folder `.afk/master-plan/`, so it shows in the dashboard under that name. Cleaning that session does not delete the planning state.

**Exit codes.** `afk plan` and the three change commands: 0 you left the session (or the dry run printed), 1 the session could not be started here (no terminal, no plan to change, a session already open, an agent that cannot plan, or a project with a plan and no mode chosen), 2 the command was written wrongly, 10 the session was opened somewhere else, 11 the session ended, 12 it is not running. `afk plan status`: 0 printed, 1 one of the session's files is damaged, 2 bad usage. `afk plan advance`: 0 moved, 1 refused, 2 bad usage. `afk plan approve`: 0 approved, or already approved; 1 refused, or a step failed; 2 bad usage.

### `afk plan lint` — Check a master plan

Check a master plan the way the daemon reads it, and list everything wrong with it, not only the first problem. Run it before you leave a plan for the daemon to build.

```bash
afk plan lint [--project DIR] [--json] [--summary] [FILE]
```

- `FILE` — The plan to check. `FILE` and `--project` cannot be combined. With neither, it checks the plan `afk tasks` reads (`plan` in `afk.toml`, `docs/PLAN.md` by default).
- `--project DIR` — The project whose plan to check. Defaults to the current directory.
- `--summary` — Print only the six counts (milestones, tasks, owner tasks, open questions, errors, warnings).
- `--json` — Print one JSON object with `file`, `findings` (each with `level`, `line`, `task`, `rule` and `message`) and `summary`.

An error is something the daemon would refuse or misread: no tasks, a task heading written wrongly, two tasks with the same id, a task with no `Depends on` line, a dependency on a task that is not in the plan, tasks that depend on each other in a loop, and an open question that waits on a task that is not in the plan. A warning is something a reader would miss: a `Depends on` line with no task id in it, a task with no Goal, Scope or Acceptance line, a missing required section, and an open question that names no task. Warnings never fail the command. The daemon uses the same table: an open row that names the next ready task holds it until the row is answered (see **Open questions** under the build loop). See `plugins/afk/skills/master-plan/plan-format.md` for the full format and every rule.

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

### `afk plan patch` — Change a plan by a guarded patch

Change the master plan one checked step at a time. The planning session uses these commands to extend, amend or reconcile a plan, and you can use them to edit a plan by hand with the same checks.

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

### `afk profile` — How you build and host things

The owner profile says, once, how you build and host things: the database you use, the deploy tool you run, where new projects go. The planning session that `afk plan` starts reads it, so it asks you less and never guesses a setup fact. `afk profile` shows what it says.

The file is `profile.toml`, in the same folder as `config.toml`: `$XDG_CONFIG_HOME/afk/profile.toml` when `XDG_CONFIG_HOME` is an absolute path, otherwise `~/.config/afk/profile.toml`. You can edit it by hand. The commands below are the other way in.

```bash
afk profile [--kind KIND]
afk profile init
afk profile set <topic> <value> [--kind KIND] [--rule|--default] [--reason TEXT]
afk profile check
```

- `afk profile` — Lists what the profile says, one entry per line. `--kind` shows what applies to one kind of project, such as `web-app` or `cli`. With no file yet, it says where the file would be and that `afk profile init` writes a template, and it exits 0. If the file has a problem, it prints the problems instead of the entries and exits 1.
- `afk profile init` — Writes a commented template. Every example in it is commented out, so a new file says nothing until you uncomment one. It never overwrites a file: if one is there, it says so and leaves it alone.
- `afk profile set <topic> <value>` — Adds the entry for that topic, or changes it, and creates the file if there is none. `--kind` makes the entry apply to one kind of project, `--rule` makes it a rule, `--default` makes it a default, and `--reason` says why. A new entry is a default unless you pass `--rule`. A changed entry keeps its strength and reason unless you pass new ones. Every other line of the file, comments included, is kept. Comments on the lines of the entry being changed go with that entry. Before it saves, afk reads the file again and checks that only that entry changed; if it cannot be sure, it writes nothing.
- `afk profile check` — Lists every problem in the file, one per line as `file: error` or `file: warning`, then the message and a rule name, then a count. Errors exit 1; warnings alone exit 0. A `rule_files` path that does not exist is a warning.

An example file, with placeholder values:

```toml
# ~/.config/afk/profile.toml
rule_files = ["~/notes/house-rules.md"]

[[entry]]
topic = "database"
value = "PostgreSQL"
strength = "rule"
reason = "it is what I run"

[[entry]]
topic = "domain"
value = "example.com"
strength = "default"
kind = "web-app"
```

- `rule_files` — Documents that hold your house rules, each an absolute path or a path starting with `~/`. The profile commands list them and do not open them.
- `topic` — What the entry is about, such as `database`. Topics match ignoring capital letters and extra spaces, so `Database` and `database` are the same topic. There is one entry per topic and kind; a second one is an error.
- `value` — The answer, such as `PostgreSQL`. `"no preference"` means you have none: the planner should recommend one and say why.
- `strength` — `rule` must be followed. `default` is used unless the project says otherwise. A missing strength counts as `default`; any other word is an error.
- `kind` — Optional. Lower-case letters, digits and hyphens, such as `web-app` or `cli`. The entry then applies only to that kind of project. When you ask for a kind, an entry for that kind beats a general entry on the same topic, whatever their strengths.
- `reason` — Optional. Why you prefer the value.

Keys and tokens are refused. `set` refuses a value that looks like one, says which field it was and why, and saves nothing. `check` reports such an entry without printing the text. afk reads secrets from the environment, never from this file, so a key written here would sit in plain text in your config folder.

A walkthrough on a new machine (the paths are the defaults):

```
$ afk profile set database PostgreSQL --rule --reason "house rule"
Set database as a rule in /home/you/.config/afk/profile.toml
$ afk profile set database SQLite --kind cli
Set database [cli] as a default in /home/you/.config/afk/profile.toml
$ afk profile --kind web-app
Profile: /home/you/.config/afk/profile.toml
No entry is specific to web-app, so the general entries apply.
rule  database: PostgreSQL — house rule
```

**Exit codes:** 0 when the command is done, including when there is no file yet, 1 when the profile has a problem or could not be read or written, 2 when the command was written wrongly.

### `afk tasks` — List the master plan's tasks

Show every task in the project's master plan, with its status and what it is still waiting for.

```bash
afk tasks [--project DIR]
```

To check a plan for mistakes before the daemon reads it, run `afk plan lint`. To change a plan that exists, see `afk plan extend`, `amend` and `reconcile` (above) and `afk plan patch`.

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

A second key, `[planning] via_pr`, sends changes to the plan out as a pull request (see `afk plan patch`):

```toml
[planning]
via_pr = false
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

In the dashboard, `ctrl+space S` on the Build tab does the same for the task under the cursor, through the same code, after a yes. Running the same command twice changes nothing the second time. Several of these commands can run at once; they take turns using a lock file.

**Exit codes:** 0 when the task has the status (changed now or already), 1 when the plan or ledger could not be used or the task is not in the plan, 2 when the command was written wrongly (for example an unknown status).

### `afk ledger init` — Create the ledger for a project that has none

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
- `--refresh` — Read the numbers again even if a recent reading is stored. The dashboard's refresh usage action (`ctrl+space R` in `afk dash`) does the same as `--refresh`, and so does the **Refresh usage** button on the web page (`/usage`, served by `afk daemon`; see the web page section).

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

**Push notifications.** To get a push on your phone, put your [ntfy](https://ntfy.sh) topic in `afk.local.toml` in the project's top folder, not in `afk.toml`: the topic name works as a password and `afk.toml` is committed (see [`afk.local.toml`](#afklocaltoml--private-project-settings)):

```toml
# afk.local.toml
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

With no `ntfy_url` in `afk.local.toml` or `afk.toml`, afk sends nothing and that is not an error. The item is always written first. If those files cannot be read, or the notification fails, afk prints a warning (`the item was added, but ...`) and still exits 0, so an agent never thinks the item was lost. A notification waits at most 10 seconds and is not retried.

**Planning waits.** When a planning session waits for you (a question to answer, or a reply to type), the daemon sends one notification for that wait, at priority 4. Its title names the project, for example `afk: shop is waiting for you`, and its body is the first line of the question, followed by a line saying where to answer. It is sent on the daemon's pass, and it is sent even while builds are paused, because planning is work you do with afk, not a build. The address is the same `ntfy_url` as above, so a project being planned in a new folder needs its own `afk.local.toml` to be told. Once a wait has been sent, it is not sent again. A new question after you answer starts a new wait and gets its own notification. A notification that could not be sent is tried again on the next pass. afk keeps the waits it has sent in `plan-waits.json` in its state folder, so a restart does not repeat them.

**Exit codes:** 0 when the command did what was asked (including an added item whose notification failed), 1 when the item was not found, was already answered, or a file could not be read or written, 2 when the command was written wrongly (for example neither `--block` nor `--no-block`).

### `afk tokens` — Show how many tokens a task used

Show how many tokens a session or a task used, split by phase and by role. afk is limited by the usage allowance of your agent subscription, so tokens per finished task is the number that decides how much it can get done in a week.

```bash
afk tokens [--project PATH] [--record] [SESSION|TASK]
```

- No argument — Totals per task, read from the ledger.
- `TASK` — A task id such as `T1.4`: that task's lines from the ledger. Use this for a session that has been cleaned. `afk dash` shows the same table for the selected Build tab task (`ctrl+space T`).
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

### `afk daemon` — Build the master plan unattended

The daemon works through a project's master plan on its own. It runs one task at a time: it starts a session for the next task that is ready, watches that session, and when the session finishes it records the result and moves on. Nobody has to be at the keyboard.

```bash
afk daemon            # run until stopped
afk daemon --once     # look at every project once, do what is due, and exit
afk daemon status     # say what it is doing in each project, and why
```

**On each pass** (every 5 seconds by default) the daemon reads the project's files from scratch and does the first thing that applies:

- If the plan, the ledger or `afk.toml` cannot be used, it stops for that project and says what to fix. It carries on by itself once the file is fixed.
- If a task is running, it watches that task's session. A finished session moves the task on (see "Shipping" below). A session that ended as failed marks the task `failed` and adds a `failure` item to your inbox. An open blocking inbox item for the task parks it (see "What parks a task").
- If the session has been asking a question at the terminal for longer than `stall_after`, afk first types the session's resume command into it, once, in case the session is only sitting at an empty prompt, when it can and the session's wakes are not used up. If the session is still waiting `stall_after` later, it adds an inbox item saying so, once, and parks the task. The item says whether afk typed the command, or why it did not.
- If the session has stopped answering, it is restarted. If its host process is gone (a reboot, a crash), the session is started again. Each resumes the same conversation, and afterwards afk types the session's resume command so it carries on. See "Recovering a session".
- If nothing is running and the project is not paused, it picks the next ready task (the same answer as `afk next`), checks that this machine can start the agent unattended, and starts a session for it. The session and the branch are both named after the task: `T4.3` becomes `t4-3`.
- If nothing can start, it says why (waiting on you, blocked by a failed task, or all done) and sends one push notification for that, not one per pass.

Stopping the daemon (ctrl-c, or `systemctl --user stop afkd`) never stops a session. Every session keeps running, and the next start picks them up again, because everything the daemon knows is in files. Only one daemon can run on a machine; a second one exits with code 1.

**A new version of afk.** Between passes the daemon checks its own program file. When `make install` has put a new version there, it logs "afk daemon found a new version of itself on disk and is stopping so it can be started again. Every session is still running." and exits with code 0. It never leaves in the middle of a ship step, and it does not touch any session. Under the systemd unit in `contrib/` (`Restart=always`) it starts again by itself; a daemon you started by hand stays stopped until you start it again. `afk daemon --once` does not check. If the daemon cannot read its own program file when it starts, it says so in its log and carries on without the check.

**Recovering a session.** A session can stop answering without ending: the agent hangs, or the machine reboots and takes the session's host with it. For a session the daemon started, the session's host checks by itself, whether or not anyone is attached: after about 35 seconds of silence it asks the agent to repaint, and if that goes unanswered it records that the session is stuck. afk acts only once that has lasted a minute, so an agent that is merely slow is left alone. A session whose host has gone is started again after the host has been gone for a minute.

- afk makes at most `max_restarts` recoveries per task (default 3). The first happens at once and each later one after a wait that starts at `restart_backoff` and doubles. With the defaults that is at once, after one minute, after two minutes, and then the task parks.
- afk also types the session's resume command again, at most `max_wakes` times per task (default 3), when the agent's turn fails. This does not restart anything, and it does not count against `max_restarts`. See "When a turn fails" below.
- At the limit the task parks with an inbox item titled "Session could not be recovered: <task> (<session>)". Answering it gives the task a fresh set of recoveries, and afk types the session's resume command into it as it does for any answered item.
- A recovered session resumes its own conversation. About 30 seconds after the new agent started, afk types the session's resume command (`/afk:go --session <name>`), and the session carries on from its files. A session whose status file could not be read, or that had not yet recorded its conversation, comes back as a fresh conversation that picks its work up from its files.
- A pause does not stop the recovery of a task that is running. Sessions that are parked or shipping are not recovered.
- `afk attach` and the dashboard always work. If afk restarts a session while you are attached, your pane says the session was taken: attach again, and expect afk to type the resume command into it about half a minute later.
- Recovery needs the session to have been started by the daemon. A session you started by hand is not checked while nobody is attached, as before.

**When a turn fails.** Sometimes the agent's turn ends on an error that trying again may fix, such as a lost connection or a fault at the agent's service. The plugin's hook then writes `.afk/<session>/.turn-failed`, and the daemon takes it from there. A rate limit is not one of these: it is handled by the usage pause.

- If the session's host is still running and its agent is answering, afk types the session's resume command about 30 seconds after the marker was written. Nothing is restarted, and the session carries on from its files.
- A second wake waits at least one minute after the first, and a third at least two minutes after the second. The wait doubles from `restart_backoff`, the same as for recoveries.
- afk wakes a session at most `max_wakes` times per task (default 3; `0` means it never wakes one). A session that still fails after that parks its task with an inbox item titled "Session kept failing: <task> (<session>)". The item says what the error was, and what to do: a fault at the agent's service usually passes by itself, while a message about your login or account has to be fixed first.
- Answering that item gives the task a fresh set of wakes, and afk types the session's resume command into it, as for any answered item. If the session is beyond saving, `afk clean <session>` and then `afk ledger set <task> pending` start the task again in a new session.

**Machine config.** The daemon reads `config.toml` in afk's config folder (`$XDG_CONFIG_HOME/afk/config.toml` when that is an absolute path, otherwise `~/.config/afk/config.toml`). It is the machine's only settings file: it also holds the dashboard's `[dash]` table (see [Multi-project support](#multi-project-support)), which the daemon ignores. The owner profile, `profile.toml`, lives in the same folder (see `afk profile`). A missing file is not an error, but with no `[[project]]` the daemon has nothing to build, says so once, and waits: it starts with no projects and picks up one added by `afk project add` or `afk plan approve`. `afk daemon --once` with no project prints that and exits 0. Keys it does not know are ignored.

```toml
# ~/.config/afk/config.toml
provider = "claude"            # the agent to run; leave out for the default

[daemon]
tick = "5s"                    # how often to look at each project (1s or more)
stall_after = "10m"            # how long a session may ask a question at the terminal
max_restarts = 3               # recoveries per task before it parks (0 means never restart)
restart_backoff = "1m"         # wait before the second recovery; each later one doubles it
max_wakes = 3                  # times a session is typed into after a failed turn, per task (0 means never wake one)

[governor]
pause_at = 90                  # percent of a usage window at which running work is stopped (above 5, at most 100)
launch_below = 75              # percent of the session window under which new work may start (above 0, at most pause_at)
stale_after = "30m"            # how old a usage reading may be before it is not trusted (must be longer than probe_interval)
probe_interval = "5m"          # how often the daemon reads usage again (1m or more)
on_unknown_usage = "run"       # "run" or "hold": what to do when the agent cannot report usage at all

[web]
bind = "127.0.0.1:4253"        # host:port the web page listens on (this is the default); the port is 1 to 65535
token = "paste-a-secret-here"  # at least 16 characters, no spaces; leave it out and the page is off
public_url = "https://afk.example"  # optional: where you open the page from; starts with http:// or https://, no ? or #

[[project]]
path = "~/code/my-app"         # an absolute path, or one starting with ~/
name = "my-app"                # optional; defaults to the folder name

[[project]]
path = "/srv/another-app"
```

#### The web page

`afk daemon` can also serve a small web page, so you can see what it is doing and unblock it from a phone. It shows each project's current task, the usage reading and the inbox, lets you answer an inbox item, has Pause and Resume buttons, plans a new project in a folder you name, starts extend, amend or reconcile on a project's plan, and acts on a project's sessions: it starts a new autonomous session, restarts a stopped or stuck one, sets a session's label, and cleans a finished one up. Each session action asks you to confirm first. It also approves a new plan that is waiting for review, behind a confirm too (see `/p/<project>/plan` below). It is off until you set a token.

**Turn it on.** Make a token and put it in the `[web]` table of `config.toml` (see above). `openssl rand -hex 16` prints a good one (32 characters). Restart `afk daemon`; its log then says `web page: listening on http://127.0.0.1:4253`. Without `token` the log says the page is off, no port is opened, and the daemon runs as it did before. The token must be at least 16 characters with no spaces; a shorter or spaced one stops `afk daemon` with a message naming `web.token` and the file. The token is never written to the log.

**Where it listens.** `bind` defaults to `127.0.0.1:4253`, which only this machine can reach. If you bind an address other machines can reach (`0.0.0.0:4253`, `:4253`, a LAN address or any name other than `localhost`), a token is required: with none, `afk daemon` exits 1 naming `web.bind`, `web.token` and `config.toml`. If the port is taken, it exits 1 saying so.

**Plain HTTP.** The page speaks plain HTTP and has no TLS of its own. It is meant to sit behind Tailscale or a reverse proxy; do not put it on the open internet as it is.

**Sign in once.** Open the page with the token on the end of the address, for example `http://127.0.0.1:4253/?token=<your token>`. afk stores a cookie in your browser (kept 180 days) and sends you on to the same page without the token in the address, so it stays out of your history. After that, open the page without it. A request with no token, or the wrong one, gets a 401 page that says what to do. Changing the token signs every browser out.

**What each page shows.**

- `/` — the all-projects screen. Its heading says how many projects need you ("2 projects need you", or "Nothing needs you"). **Needs you** comes first and lists the projects that wait on the owner; **Everything else** lists the rest, in the same order the dashboard uses. Each row shows the project's state as a word (Building, Waiting on you, Paused, Line stopped, Held for usage, Nothing ready, Finished, Gone, Can't read, No plan), the task being worked on and its phase, or in plain words why nothing is running, how many tasks are done as a bar, and how many inbox items are open and how many hold a task up. A plan waiting for approval is its own row, "Plan waiting for approval", and links to the plan. On a desktop-width screen the first row (the one that needs you most) is also shown on the right, with its details and a link to its page; on a phone the list is the whole page. The usage reading is on this page too. The page updates itself while it is open (see *Live updates*).
- `/p/<project>` — the Project screen for one project. A state card shows the daemon's state as a badge, a headline in words (for example, how many inbox questions hold up a task), one line of detail, the progress bar, and the one next action: answer the inbox, resume a paused or stopped project, or read and approve a waiting plan. Below it are **Running now** (the tasks being worked on, with their session; each row links to that session's screen), the **Pause project** or **Resume project** button, a **Plan** row that links to the plan and says where the planning is in one line (the plan line, for example "plan: plan waiting for your approval"), a short list of **Inbox**, **Tasks**, **Sessions**, **Plan** and **Usage** (the inbox with its open count, Tasks as "N of M" done, the same count as the progress bar, and Sessions with how many the project has), and the project's **Folder** and **Repository**. Below the list, the **Change the plan** section has three rows, **Extend**, **Amend** and **Reconcile**, each with one line on what it does; they start a planning session that changes the plan (see the change page below). While a planning session is open the section says so instead and links to its chat, and a project with no plan gets one sentence and no rows. The card, Running now, the button and the list update by themselves (see *Live updates*).
- `/p/<project>/inbox` — the project's inbox as its own screen. A notice says how many open items hold a task up. Open items are listed, blocking ones first, each with its task, its title, what kind of item it is and how long ago it was raised. Answered items sit under "Answered (N)". When nothing is open the screen says "Nothing needs you". After an answer you come back here, and a toast says what happened: "Answer sent.", "Answer sent. T3.4 is ready again." (only when the task was parked and no other open item holds it), or "Marked as done." for a task only you can do.
- `/p/<project>/inbox/<id>` — one inbox item, with a Back link to the inbox. If it is open, the form takes your answer. The options are buttons, the recommended one first and marked "Recommended"; "Or write your own" takes text, and typed text wins over a chosen option. An owner task, one only you can do, shows its instruction and a single **Mark as done** button, which records the answer `done`. The answer is stored with the channel `web` (the item then reads "Given ... through web"), and the daemon un-parks the task on its next pass, exactly as after `afk inbox answer`. A second answer to the same item is refused and the first is kept. With JavaScript on, choosing an option only marks it, and **Send answer** stays disabled until something is chosen or written; sending goes back to the inbox without a reload. With JavaScript off, a tap on an option sends that option at once and the page reloads as before.
- `/p/<project>/tasks` — the project's tasks, as `afk tasks` lists them. Open, Done and All sit at the top (`?show=open`, `?show=done`, `?show=all`); Open is the default, and any other value also shows Open. Each milestone of the plan is a section with its heading and a "N of M done" count that does not change when you switch filters. A row shows the task's id, its title and its status as a word (a superseded task says which tasks replace it). A task that is neither done nor superseded and still waits for others has a "Waits for T3.4, T3.5" line. Tasks above the first milestone heading sit under a plain "Tasks" heading, and an empty filter says why ("No open tasks", "Nothing done yet"). The list updates by itself while it is open. If the plan or the ledger cannot be read, the page says why in a notice instead of the list.
- `/p/<project>/tasks/<id>` — one task's screen, with a Back link to the list. It shows the task's status as a word, one plain line on why the task is in that state, and what it depends on. The **Tokens** card shows the tokens the task used: a bar split by phase with a legend underneath, and a table by role. A task with no record yet says "No tokens recorded for this task yet." A record that cannot be read says so on the card. The **Set status…** button opens the status sheet described under "Set a task's status" below. An id that is not in the plan answers 404 with a plain sentence. If the session the ledger names is still found, the screen has an **Open its session** link to that session's screen; a session that has been cleaned up gets no link.
- `/p/<project>/sessions` — the project's sessions, every session discovery finds for the project, in the dashboard's order. Each row shows the label (or the session's name), its phase as a badge, a progress bar when it has a work plan, a **Waits for you** badge when it is waiting for an answer, a **Stopped** badge when its host is not running, and its review verdict (**Approved** or **Changes asked**) once it has one. A row that is not an ordinary session says what it is in a line under it, with its own badge: **One-off** (a session with no plan, only a terminal), **Planning** (the planning session for the project's master plan), **Status unknown** (its `status.json` could not be read, with the reason) or **Problem** (a worktree that is not a session, with what is wrong). A running host whose agent has gone quiet says so in a line. Each row shows the first eight characters of the session's id, and the whole id is in its tooltip. A project with no sessions says "No sessions yet". Each row links to its session's screen, except the **Planning** row, which links to the planning chat. The list updates by itself while it is open. Above the list, **Start a new autonomous session…** opens a short form (a name and a description) that starts a new session; see *Session actions* below.
- `/p/<project>/sessions/<name>` — one session's overview, with a Back link to the list. A state card shows the phase as a badge with the loop count, when the session started and when it was last updated, its label, branch, folder and host (running, not running, or running but its agent has gone quiet). A **Summary** says in sentences what the session is doing: its phase, whether it waits for you, its work-plan progress, the review verdict for each check, whether its host is running, and why its status is unknown when it is. The page cannot open the session's terminal, so the screen says to open it from `afk dash` or with `afk attach <name>`. A planning session's screen also links to the planning chat. Its **Actions** rows are **Restart**, **Label** and **Clean**, and each opens a confirm sheet (see *Session actions* below). It does not show git diff stats or the tail of the issues file. A name that is not in the project answers 404 with a plain sentence saying it may have been cleaned up. The state card updates by itself while the page is open.
- `/p/<project>/plan` — the project's master plan, drawn as a page. At the top is the plan line (the planning session's mode, phase and what it waits for, in the same words as `afk dash`), then the **checker card**: a badge, the counts from `afk plan lint --summary`, and the checker's findings with the errors first, each worded as `afk plan lint` words it. A finding that names a task links to that task's heading in the plan below; one that names no task shows its file and line as plain text. Then come the open questions and the plan's own text, with each task heading linkable (`#T5.7`). **Approve the plan…** is shown only for a new plan at review with no checker errors. It opens a confirm that names the project, the plan's path and the counts, with **Not yet** focused and **Approve and start** as the other button; with JavaScript off the same confirm is its own page. Approving runs the same steps as `afk plan approve`, and the result page says what was done, what was refused, or which step failed and what to run. A plan with errors shows a red notice that says how many errors to fix, and no approve button; a plan that is not at review says why it cannot be approved. A plan waiting for approval is shown here even though its project is not in `config.toml` yet, and it can be approved the same way; the home page `/` lists it under Needs you as "Plan waiting for approval". If the plan cannot be read, the page says which file and why.
- `/p/<project>/plan/change?mode=extend|amend|reconcile` — the change page, reached from a Change the plan row or button. Extend shows a confirm card that names the project and says a planning agent reads the plan and asks what to add. Amend asks for the sentence that should change ("What should change?"), and Reconcile asks for a document path inside the project, such as `docs/design-system.md`. The next step is a card showing what will start and the text, with **Start planning** and **Cancel**. Nothing starts until **Start planning** is pressed; the page has no `--yes`, so amend always asks. A refusal redraws this page with the reason above the form, in the command's own words and with the text you typed kept. When a planning session is already open the page says so and links to its chat; with no plan it says there is no plan to change yet. An unknown mode answers 400 with a sentence saying so. A start goes straight to the planning chat with a short notice on it. It works with JavaScript off, because the confirm is a page of its own.
- `/plan/new` — Plan a new project, reached from the **Plan a new project** link on the all-projects screen and in the top bar. Its **Where** field takes the full path of a folder, such as `~/code/shop` (`~` works), or a project name. A name makes a new folder under the `project location` in your owner profile (set it with `afk profile set "project location" ~/code`), so `shop` becomes `~/code/shop`. **Continue** shows a confirm card with the full path and what the folder is: an existing git repository, an existing folder that planning will turn into one, or, for a name, a new empty folder that afk will create and make into a git repository. Nothing starts until you press **Start planning**, which makes the folder and its repository (for a name) and then takes you to the planning chat. It refuses, in a sentence, a path that is not a folder, a folder that is not there (it is named in the sentence), a folder that already has a plan (use Change the plan on its project to extend or amend it), a name with characters other than letters, digits, `.`, `_` and `-`, a name when there is no project location (the sentence says how to set one, or to type a full path instead) and a blank or overlong path. A name whose folder already exists is offered as that folder, with its existing card, and creates nothing. A refusal starts nothing and creates nothing. If a planning session is already open in the folder, the card says so and Start planning takes you back to it. A project started here stays on the all-projects screen, marked **Being planned**, until you approve its plan or its planning session is gone. afk keeps that list in `being-planned.json` in its state folder. Nothing is written to `config.toml`. The page creates a folder only for a name, directly under the project location, and only after the confirm; it never creates the project location itself. If making the repository fails part-way, the folder stays on disk, nothing is removed, and no planning session starts. It works with JavaScript off. The server asks for the confirm too: a POST without it answers 400 and starts nothing.
- `/p/<project>/plan/chat` — the planning chat: the conversation with the project's open planning session, read as messages. The agent's messages are drawn as formatted text, and yours appear as you typed them. When the agent asks a question, each option is a button, with the recommended one first and marked "Recommended". "Or answer in your own words" takes your own answer to one single-select question, and the reply box stays off until the question is answered. Otherwise the reply box is at the foot of the page; it is disabled while the agent works, and a line says so. With no planning session open, the page says "No planning session" and has no reply box; it offers the same three buttons as the project screen's Change the plan section (Extend, Amend and Reconcile), and says there is no plan to change yet when the project has no plan. A refused reply says why in plain words and keeps what you typed. The page never shows the terminal; open the planning session from `afk dash` or with `afk attach` to see it. The reply is an ordinary form, so it works with JavaScript off.
- `/usage` — the usage screen. A **What afk is doing** card shows the governor's decision as a badge, with the reason, when it wakes (if it is paused or held) and when it was decided. A **Usage windows** card has one meter per window: how full it is, and when it resets. Below them is a **Refresh usage** button and a note on what the five decisions mean (Run, Launch nothing new, Pause, Hold, Refuse). Pressing **Refresh usage** takes a fresh reading in the background, the same as `afk usage --refresh`. The button reads "Refreshing" while the reading runs, and the new numbers appear on the page without a reload; after a successful reading it reads "Checked just now" for a minute. Only one reading runs at a time, so a second press while one is running does nothing and the page says so. A reading that fails shows a plain sentence saying why, with the reading that worked before still on the page, marked as the last one that worked. Nothing takes a reading on a timer, when the page opens, or when the live stream starts; only the button does.

**Set a task's status.** A task's screen has a **Set status…** button. It opens a sheet that offers every status the ledger takes (pending, running, shipping, parked, done, failed and superseded), with the task's current status checked. Pick one and the sheet says in one sentence what will change, for example "T3.4: failed → pending.", and what the build loop will then do with the task. If the task is running or shipping, the sheet first says that an agent is working on it now and that changing its status does not stop that agent. Choosing superseded asks for the ids of the tasks that replace it, such as T3.4a and T3.4b, and the sentence names them; it is refused without at least one id, and a task cannot replace itself. The sheet's safe button, "Leave it as it is", closes it and changes nothing. The action button names the status ("Mark pending"), and pressing it makes the change without a reload, after which the page shows the task with its new status. With JavaScript off, **Set status…** goes to `GET /p/<project>/tasks/<id>/status`, a two-step page. The first step lists the statuses as radio buttons with the current one marked, and a field for the replacing ids; the second step shows the same sentence as the sheet with a button to confirm. The server asks for the confirm too: a status POST that does not carry it changes nothing and answers 400 with "Nothing changed" and a sentence telling you to open the task and press Set status…. The sheet's button sends it, and so does the second step of the page. The change is written to the ledger by the same code as `afk ledger set`, with the same checks and the same words, so the file `afk tasks` reads is the one the page changes. Whatever the command would refuse, the page refuses with the command's words, for example an id that is not in the plan or an unknown status.

**Session actions.** The Sessions screens have four actions: starting a new autonomous session, Restart, Label and Clean. Each one changes nothing until you press its confirm button. Its sheet says in words what will happen, and its safe choice closes the sheet and changes nothing. A refusal is a plain sentence that says nothing was changed.

- **Start a new autonomous session…** starts a session as `afk new --autonomous` does: a new branch and worktree in the project, running on its own with the description as its work. It runs autonomously, not in unattended permission mode, so when its agent needs a permission it waits for you in `afk dash`. It needs a name (lower-case letters, digits and dashes, as `afk new` takes) and a description that is not blank and is at most 5000 characters. It refuses what `afk new` refuses, such as the reserved name `master-plan` or a name whose branch already exists.
- **Restart** is offered for a planned session only. A stopped session gets its host started again, and its agent resumes from its files. A running host whose agent has stopped answering is asked to restart that agent. A healthy running host is refused, because restart only replaces a stopped or stuck agent. Restart never touches a one-off session (restart that from `afk dash`), the planning session (open it with `afk plan`), or a session whose worktree has a problem. Asking a running host to restart takes its one connection, so an `afk attach` or a dashboard pane open in another terminal is dropped, as the confirm says.
- **Label** saves a new label for the session, which the list shows in place of its name. A label of more than 128 characters is refused, and so is a session whose worktree has a problem, since there is nowhere right to save it.
- **Clean** removes a session once it is done or failed. The page never forces a clean of a session in any other phase: it is refused with a sentence that points to `afk clean --force` or `afk dash`, as `afk clean` refuses it. If the session's host is still running, the clean stops it first, as `afk clean` does. The session's `.afk/<name>/` folder is archived and removed, and its worktree directory is removed too. A standalone session with no worktree of its own only loses its `.afk/<name>/` folder. The branch is always kept, because the page never deletes a branch. The page checks the phase you saw: if the session's phase has changed since the page showed it, the clean is refused and nothing is cleaned.

With JavaScript off, each action goes to a confirm page with the same words and the same choices: `GET /p/<project>/sessions/<name>/confirm/<action>` for Restart, Label and Clean, and `GET /p/<project>/sessions/new` for the new session. The server asks for the confirm too: an action POST that does not carry it changes nothing.

**Live updates.** An open page updates by itself within a few seconds of a change: a new inbox item, a task moving on, a pause, a new usage reading. The Sessions list and each session's screen update the same way, when a session's phase, loop count, work-plan progress, review verdict, label or host changes. The planning chat follows the conversation the same way: a new message or question appears on an open chat, and the question buttons and the reply box are swapped too. A swap waits while the cursor is in the reply box or the box holds text you typed, so a half-written reply is kept. The page opens a stream at `GET /events` (server-sent events, behind the same token as every page) and swaps only the parts that changed, so what you are typing is never lost. The live indicator in the bar has three states. "Live" means the stream is up. "Working" means the stream is up and something on the page says afk is busy on it, such as a project's task in progress. "Can't reach afk, retrying" means the stream dropped; the page keeps what it had, reconnects on its own, and never turns into an error page. With JavaScript off, or behind a proxy that refuses event streams, nothing breaks: reload the page to see the latest. A proxy in front of the page must not buffer `text/event-stream` (for nginx, `proxy_buffering off`; the page already sends `X-Accel-Buffering: no`).

**Pause and resume.** The buttons do what [`afk pause` and `afk resume`](#afk-pause-and-afk-resume--hold-and-release-the-daemon) do, including clearing a stopped line, and write to the decision log in the same way. Pressing Pause or Resume opens a confirm sheet (a dialog on the page) that says what will change and that nothing is deleted. Its safe choice, "Keep it running" or "Keep it paused", closes the sheet and changes nothing. The resume sheet says that afk starts the next ready task again and that a stopped line is cleared. With JavaScript off, the button goes to `GET /p/<project>/confirm/pause` (or `/confirm/resume`), a page with the same words and the same two choices. The server asks for the confirm too: a pause or resume POST that does not carry it changes nothing and answers 400 with a plain sentence. The sheet's own button sends it, and so does the confirm page. With JavaScript on, the change happens without a reload: the page is swapped for the new one. After a change, the project page shows a short notice that it happened ("Paused <project>. Nothing new will start." or "Resumed <project>."), which goes away on its own after five seconds.

**The JSON API.** Every page has a JSON twin under `/api`: `GET /api/`, `GET /api/usage`, `GET /api/p/<project>`, `GET /api/p/<project>/inbox`, `GET /api/p/<project>/inbox/<id>`, and `POST` to `/api/p/<project>/inbox/<id>/answer` (a body of `{"answer": "text"}` with `Content-Type: application/json`), `/api/p/<project>/pause` and `/api/p/<project>/resume`, `GET /api/p/<project>/plan` and `POST` to `/api/p/<project>/plan/approve` (see below), and `GET /api/p/<project>/plan/chat`, `POST /api/plan/new` (the twin of Plan a new project: a body of `{"path": "~/code/shop", "confirm": true}` with `Content-Type: application/json`, or the form fields `path` and `confirm=yes`; `path` takes a full path or a project name, the same as the Where field; without the confirm, or for a folder it refuses, it answers 400 with `{"error", "reason", "path"}` and starts nothing; on success it answers 200 with `path`, `root`, `project`, `outcome`, `chat`, `notice` and `created`, which is `true` when this call made the folder and its repository and `false` when the folder was already there; a name whose folder cannot be made answers 400 with reason `create_failed` and the `path` of the folder left on disk, and starts nothing and removes nothing), and `GET /api/` carries a `being_planned` list beside `projects` and `reviews` with the projects being planned, never null, `GET /api/p/<project>/tasks[?show=open|done|all]`, `GET /api/p/<project>/tasks/<id>`, and `POST` to `/api/p/<project>/tasks/<id>/status`. `POST /api/usage/refresh` starts one usage reading in the background, the same reading as the page's Refresh usage button. It needs no confirm, and it answers 202 with `{"outcome": "started"}`, or 200 with `{"outcome": "already"}` when a reading is already running (a second press does nothing). `GET /api/usage` then carries a `refresh` object with `state` (`idle`, `running`, `refreshed`, `failed` or `unsupported`), `text` (the sentence for a failed or unsupported reading) and `at` (when it started or finished), which is absent when there is no refresh to report; the other fields are as they were. The refresh POST is guarded like every POST: the token and the `Origin` check apply. The pause and resume POSTs need the confirm: a body of `{"confirm": true}` with `Content-Type: application/json`, or the form field `confirm=yes`. Without it they answer 400 with `{"error": ...}` and change nothing. `GET /api/p/<project>/tasks` lists the tasks under the same filter as the page (`show` is open by default, and any other value reads as open), and `GET /api/p/<project>/tasks/<id>` gives one task with its `unmet`, `why` and `tokens`. `GET /api/p/<project>/sessions` lists the project's sessions as the Sessions screen does, and `GET /api/p/<project>/sessions/<name>` gives one session's overview with the same facts. The two GETs only read. The four session actions are POSTs, and each needs the confirm: `POST /api/p/<project>/sessions` starts a new autonomous session with `{"name": "fix-login", "description": "Fix the login form", "confirm": true}`; `POST /api/p/{project}/sessions/{name}/clean` takes `{"phase": "done", "confirm": true}`, where `phase` is the phase the caller last saw (`""` for a one-off, or a session whose status cannot be read); `POST /api/p/{project}/sessions/{name}/restart` takes `{"confirm": true}`; and `POST /api/p/{project}/sessions/{name}/label` takes `{"label": "Login work", "confirm": true}`. Each takes `Content-Type: application/json`. Without the confirm a POST answers 400 with `{"error": ...}` and changes nothing. With it, every answer has the shape `{"project", "session", "result": {"outcome", "text"}}`: `done` answers 200, `refused` (nothing was changed) answers 400, and `failed` (the attempt went wrong) answers 500. `text` is the sentence the page shows, and the API refuses exactly what the page refuses. Each session has `name`, `label`, `kind` (`session`, `one_off`, `planning`, `status_unknown` or `problem`), `phase`, `waits_for_you`, `awaiting_input`, `loop_count`, `host_running`, `wedged`, `status_stale`, `started_at`, `updated_at`, the work-plan counts and the review verdict; every key is always there, and a time is RFC 3339 or `""` when it is not known. A name that is not in the project answers 404 with an `error` sentence. The status POST takes a body of `{"status": "pending", "replaced_by": [], "confirm": true}` with `Content-Type: application/json`, or the form fields `status`, `replaced_by` and `confirm=yes`; `replaced_by` is a list of ids, and only superseded uses it. Without the confirm it answers 400 with `{"error": ...}` and changes nothing, and the ledger file is the same as before. With it, a change answers 200 with the task as it reads now and a `result` with `outcome` (`changed`, or `already` when the task already has that status), `old`, `new` and `text`, the same sentence the command prints. A refused change (an unknown status, superseded with no ids, an id that is not in the plan) answers 400 with the sentence in `error` and changes nothing. A ledger that cannot be written answers 500 the same way. `GET /api/p/<project>/plan` gives the plan page's data: its existing keys are as they were, and two are new. `status` is the planning status, with `known`, `session`, `damaged`, `mode`, `phase`, `line` (the plan line in words), `approvable` and `problems`. `approve` is `{"available": ..., "reason": ...}`, where `reason` is the sentence saying why the plan cannot be approved and is empty when it can. `POST /api/p/<project>/plan/approve` approves the plan, with the same steps as `afk plan approve`, and it needs the confirm: a body of `{"confirm": true}` with `Content-Type: application/json`, or the form field `confirm=yes`. Without it the POST answers 400 with `{"error": ...}` and changes nothing. With it, every answer has the shape `{"project", "result": {"outcome", "text"}}`: `approved` or `already` answers 200, `refused` (nothing was changed) answers 400, and `failed` answers 500. The project is found among the configured projects and then the plans waiting for approval, and the root is never taken from the request. `GET /api/` is the home page's data: `projects`, `reviews` and `usage` as before, and `rows`, one entry per project in the home page's order. Each row has `name`, `state` (one of `running`, `waiting on you`, `paused`, `stopped`, `held for usage`, `nothing ready`, `finished`, `gone`, `can't read` or `no plan`; the badge on the page uses other words for some of these), `label` and `detail` (the words the dashboard shows), `task`, `title` and `phase` (empty when nothing is running), `done` and `total` (tasks), `open` and `blocking` (inbox items), `plan_waiting` and `needs_owner`. The last one returns the project's open planning session as its messages, with `state` (`none` when no planning session is open, `working`, `waiting_for_text`, `waiting_for_choice`, or `unknown`), the pending `questions` with their options, and a `problem` sentence when the conversation cannot be read. It reads the conversation only while the planning session is running, and it has no page yet. `POST /api/p/<project>/plan/chat` sends the owner's reply into that planning session, the one `afk plan` runs in the project's main checkout, from any dashboard pane or from a script. The body is JSON, either `{"text": "your reply"}` for typed text, `{"other": "your own words"}` for an answer in the owner's own words to one single-select question (sent as the keys that type it into the agent's "Type something." row), or `{"choices": [["an option"], ...]}` with one list of option labels for each question the agent is waiting on, in order. Labels rather than numbers, so a page showing an old question cannot pick the wrong option. Typed text goes in as one message and is sent with enter; a choice is sent as the keys that pick the option in the agent's question dialog. Like `afk attach`, the reply takes the session from any dashboard pane that has it open, and that pane is told it was taken over (see *Showing agent sessions*). A sent reply answers 200 with `{"project", "sent"}`, where `sent` is `text` or `choice` (an own answer counts as `text`). A refused reply sends nothing and answers `{"error", "reason"}`: a plain sentence in `error`, and a short code in `reason` (`no_session`, `not_waiting`, `wrong_kind`, `not_offered`, `bad_reply` or `unsupported`) that a script can branch on. The status codes are: 409 when no planning session is open, the session is still working, the reply is the wrong kind for what the agent waits for, or the agent's question cannot be answered this way; 400 when the reply is not one it can use (both or neither kind, an option that was not offered, the same option twice, the wrong number of answers for the questions, or empty text or an empty own answer); 404 for an unknown project; 415 when the body is not sent as `application/json`; 502 when the session cannot be reached within a few seconds, so nothing was typed and the reply can be sent again. A choice reaches the agent's dialog because the afk plugin registers a `PreToolUse` hook for `AskUserQuestion` (plugin 5.7.4), which makes the agent record the open question where the page can read it. A planning session started before you updated the plugin does not have it, so run `afk plugin install` and start the session again; until then a choice is refused with 409. Typed text works either way. The POST is guarded like every POST: the token, the `Origin` check and the 64 KB limit below all apply. `POST /api/p/<project>/plan/change` starts the same three, and needs the confirm: a body of `{"mode": "extend", "confirm": true}`, or `{"mode": "amend", "text": "Add a CSV export task.", "confirm": true}` for amend and `{"mode": "reconcile", "text": "docs/design-system.md", "confirm": true}` for reconcile, with `Content-Type: application/json`. Extend takes no text, and any text sent with it is ignored. Without the confirm it answers 400 with `{"error": ...}` and changes nothing. With it, every answer has `project`, `mode`, `outcome`, `text`, `chat` (the address of the project's planning chat), `session_open`, `open_mode` and `open_phase`. A start or resume answers 200 with `outcome` `started` or `resumed`. A refusal answers 400 with `outcome` `refused`, a plain sentence in `error`, and `reason`: `session_open` when a planning session is already open (then `session_open` is true and `open_mode` and `open_phase` say which), `bad_request` for an unknown mode, a blank amend sentence or reconcile path, text longer than 5000 characters, or a body that cannot be read, and `refused` for any other refusal, such as a project with no plan. A refused change starts nothing. The POST is guarded like every POST: the token, the `Origin` check and the 64 KB limit apply. Send the token in a header:

```bash
curl -H "Authorization: Bearer <your token>" http://127.0.0.1:4253/api/p/my-app
```

Every address needs the token, including ones that do not exist. A POST whose `Origin` header names another site, or no site at all (`null`), is refused with 403, and a body over 64 KB with 413. The page sends `Referrer-Policy: same-origin`, so your browser tells the page that its own forms are its own, and passes the page's address to no other site. It is not `no-referrer` on purpose: under that policy a browser sends `Origin: null` on every POST, the page's own forms included, and every button on the page would answer 403.

**Links in notifications.** When `public_url` is set (and the token is, so the page is on), an inbox notification carries a link to the item at `<public_url>/p/<project>/inbox/<id>`. The link never holds the token; the cookie covers that. A notification about a planning wait links to the project's planning chat, `<public_url>/p/<project>/plan/chat`, the same way. For a plan waiting for approval that the page does not serve a chat for, it links to the plan page, `<public_url>/p/<project>/plan`. Neither link holds the token. Set `public_url` to the address you use from your phone, without a trailing slash. Its scheme also decides whether the cookie is marked Secure: only an `https://` address does that, so a page used over plain HTTP still works.

**Usage.** The daemon asks the usage governor (see [`afk usage`](#afk-usage--show-how-much-of-your-usage-is-spent)) before it starts anything, and while a task runs. What it does for each governor action:

- `run` — Nothing changes. Tasks start and sessions carry on.
- `no_launch` — Nothing new starts: no task is launched, created again or started again. A session that is running carries on, and so does a parked one that is answered. `afk daemon status` says why.
- `hold` — The reading is missing or too old, so the daemon does not know. Nothing new starts, and a running session is left alone. The daemon sends one notification. If it does not clear, read the daemon's log: a usage folder or file that cannot be used is named there once.
- `pause` — A window is at or past `pause_at`. The daemon stops the session's agent and waits for the reset (see "A pause" below).
- `refuse` — Extra usage is switched on for the account. The daemon starts nothing, stops a session that is running (an agent left running could spend real money), and adds one blocking `failure` item to the inbox of every project, "Extra usage is switched on, so afk starts nothing", with an urgent notification. Switch extra usage off. The daemon reads usage again, answers the item itself and carries on.

**A pause.** The daemon saves the pause in the task's ledger entry (`usage_pause_at` and `usage_wake_at`), then asks the session's host to end its agent, the same request `afk clean` sends. The task stays `running`. A paused session is not recovered, not prompted and not reported as stalled. If the host is still there a minute later, the daemon sends it a terminate signal and waits up to ten seconds, and repeats that for two minutes. After three minutes it adds a blocking item, "Session could not be stopped for usage: <task> (<session>)", saying the session may still be spending, and the task parks. Attach and quit it by hand; `afk attach` keeps the saved pause.

The session starts again when a reading taken after the pause began says there is room (`run` or `no_launch`), the saved wake time has passed, the old host is gone, and the agent is not logged out. The clock alone never decides. A new host runs the agent's resume command, so the conversation is the same one, and about 30 seconds later afk types the session's resume command (`/afk:go --session <name>`). This does not count as a restart. Work in progress at the pause (the one call in flight, a command that was running) is lost and done again. If the new host cannot be started, the pause is dropped and ordinary recovery takes over, with its limit.

Someone attached to a session when it is paused sees it taken over and then ended. `afk dash` shows a paused session as one whose agent is not running, the same as any session whose host has gone. A session you started by hand is never paused or held back: the governor looks only at tasks the daemon runs.

**The rate-limit marker.** When the agent stops at a usage limit, the plugin writes `.afk/<session>/.rate-limited`. The daemon reads it on a pass with a running task. A marker newer than the session's start and last restart pauses the session, with no wait for the reading to reach `pause_at`. The wake time is the governor's own when it has one, otherwise one minute after the next reset of any usage window (the soonest one still ahead), otherwise one hour. The wake time is only when afk looks again: if the limit that was hit has not reset by then, the new reading keeps the session paused.

**The failed-turn marker.** When the agent's turn ends on an error worth trying again, the plugin writes `.afk/<session>/.turn-failed` with the error's name in it. The daemon reads it and types the session's resume command, a limited number of times, as described under "Recovering a session". The marker is removed when the session takes its next prompt or finishes a turn. A rate limit never writes this marker.

**The login check.** The daemon checks that the agent is still logged in, about once an hour, and every `probe_interval` while it is logged out, so that logging in again is noticed soon. A check that cannot run keeps the last answer: not knowing is not the same as logged out. The check cannot warn you ahead of time, because the agent's status output has no expiry date; you learn of it when the login has ended. Logged out has two effects:

- Nothing new starts, and a paused session stays paused. A session already running is left alone.
- The daemon adds one blocking `failure` item, "The agent is logged out on this machine", to every project, with an urgent notification. Log in on this machine and the daemon answers the item on its next check.

**When it is checked.** Usage and the login are looked at only on a pass where something is running or could start: a running task, or one about to be launched, created again or un-parked, or while one of the two items above is open. An idle project causes no checks. A reading younger than `probe_interval` is reused, and a failed read is not tried again for a minute.

**Linux only.** The forced stop needs `/proc` to be sure it is signalling the right host. Elsewhere a session that ignores the polite stop is reported after three minutes, as above.

**Pausing.** `afk pause [project]` stops the daemon from starting new tasks in a project; a task that is already running carries on and finishes. `afk resume [project]` lets it start again. See [`afk pause` and `afk resume`](#afk-pause-and-afk-resume--hold-and-release-the-daemon). Neither needs the daemon restarted.

**What parks a task.** When the agent adds a blocking inbox item for a task (a credential to set, a choice only you can make), the daemon marks the task `parked` and leaves its session alone. It is waiting for you, so it is not counted as stalled. To un-park it, answer the item:

```bash
afk inbox                          # find the item
afk inbox answer <id> <text>       # answer it
```

On its next pass the daemon sees every item answered and marks the task `running` again. A stall report, a "Session stopped running" report, a "Session could not be recovered" item and a "Session kept failing" item park the task in the same way: the task is `parked` on the item, and answering it puts the task back to `running` and types the session's resume command into the session. A "Session waiting at the terminal" item means the agent is asking a person something, so attach and answer it first (`afk attach <session>`). By then afk has usually typed the resume command into the session once already, and the item says whether it did and what it saw. A task whose status file cannot be read is parked on an open blocking item too, where it used to wait.

**Shipping.** When a session finishes its task, the daemon takes the work the rest of the way. The task stays `shipping` and the daemon does these in order, saving where it got to after each step, so a restart or a crash never pushes, opens or merges twice:

1. It checks the session left nothing uncommitted and that its branch has a commit the base branch does not.
2. It pushes the branch (never a force push).
3. It opens a pull request against the base branch, titled from the branch's last commit.
4. It waits for the pull request's checks, asking GitHub at most every 20 seconds.
5. Red checks are re-run, up to `ci_reruns` times.
6. Still red, or the branch conflicts with its base: it hands the failure back to the session, up to `ci_fix_cycles` times. It writes what failed to `ci-failure.md` in the session's `.afk/<session>/` folder and types one line into the session telling it to read the file and commit a fix. It does not tell the session to push. The daemon pushes the new commit itself once the session has been quiet for two minutes, and the checks run again.
7. When every check has passed, and the diff adds no match for `required_env_pattern`, it merges the pull request with `merge_method`.
8. After the merge it brings your **project checkout's base branch up to date**: it fetches and fast-forwards the local base to GitHub (never a merge commit, never a reset). The next task is cut from that branch, so it holds the work just merged. It also deletes the merged branch on GitHub.
9. If the project's `afk.toml` sets `after_merge`, it runs that command once, in the project's main checkout (see `[ship]` below). It runs only when the base branch update and the branch delete are done.
10. If `health_url` is set and `deploys` is true, it asks that address until it answers with a 2xx status, or until `deploy_timeout` has passed since the merge, or since the command finished when there was one. Without a `health_url` there is no deploy check.
11. It removes the finished session (the same clean-up as `afk clean`) and marks the task `done`. The next task can then start.

**A pull request with no CI checks is never merged.** If GitHub shows no check on the pull request five minutes after it opened (or only skipped ones), the task parks with an item titled `No checks ran on the pull request for <task>`. It asks you to add a CI workflow that runs on pull requests, or to merge the pull request yourself and run `afk ledger set <task> done`. Answering the item only makes afk wait another five minutes for checks.

**What parks a shipping task.** The same rule as above applies: the daemon raises a blocking inbox item that says what happened and what to do, and the task is `parked` until you answer. It parks when: the GitHub tool is missing or logged out; the session left uncommitted changes or committed nothing; a push, a pull request, a re-run or a merge is refused; the pull request was closed without merging or has vanished; the branch is gone from GitHub or has commits the session does not; no checks ran (above); the session made no commit within an hour of a hand-back; a red CI is still red after `ci_fix_cycles` hand-backs; the diff adds something `required_env_pattern` matches (the item names the variables, and you set them before you answer); your local base branch cannot be fast-forwarded (for example because it is checked out in another worktree); or GitHub could not be read for 30 minutes. **Answering the item continues the ship from where it stopped, with a fresh set of attempts**: the re-run and hand-back counts go back to zero and the timers start again.

**A hand-back or an answered item types into the session.** The daemon has only the session's one terminal to type into, so it takes the session from whoever has it. If a dashboard pane or `afk attach` is open on that session, it is told the session was taken over (for about half a second) and you must open it again. Do not leave an attach open on a session the daemon is shipping if you do not want it interrupted.

**The stopped line.** If the deploy does not answer healthy within `deploy_timeout`, or the `after_merge` command fails (it ends with a non-zero exit code, cannot be started, or runs past `after_merge_timeout`) or is cut off (afk was stopped while it ran, so nobody knows whether it finished), the merge has already happened, and the daemon stops the whole project: it writes `.afk/build/stopped`, raises a `failure` item and sends the notification at urgent priority. No new task starts there, and `afk daemon status` shows a `Stopped:` line. Check the deploy, fix it or roll it back, then run `afk resume`. For a command that failed or was cut off, read `.afk/build/after-merge.log`, fix what went wrong and run the command yourself if it still needs running. That clears the stopped line and any pause together, and the task is marked `done` on the next pass. `afk resume` does not run the command again, whatever the reason for the stop. Answering the item does not clear the stop; only `afk resume` does.

**A pause does not stop a ship that is in progress.** `afk pause` holds back new tasks, and a task being shipped carries on to the end (push, pull request, merge and clean-up).

**Owner tasks.** A task whose heading title carries `(owner)` or `(owner; …)`, such as `T0.0 — Stand up the VM (owner; not an agent task)`, is yours to do. The daemon never starts an agent for it. It raises one blocking inbox item, `Your task: <id> — <title>`, and the task is `parked`. Tasks that depend on it wait. When the work is finished, answer the item (`afk inbox answer <id> done`); the answer marks the task `done`, with no session, branch or review. A title that says `(owner + agent)` is an ordinary task: an agent does it with your help.

**Open questions.** The daemon reads the plan's Open questions table (the first table under that heading with a "Needed by" column). When the next ready task is named in the "Needed by" cell of an open row, the daemon starts nothing for it. It raises one blocking inbox item per open row, titled `Open question: <the question>`, and the task is `parked` with no session. This holds owner tasks too, and the question is asked while the project is paused. Answer with `afk inbox answer <id> "<your answer>"`: the daemon writes `**Answered <date>:** <your answer>` into the row's Question cell (through the same guarded change as `afk plan patch`) and the task goes back to `pending`, so it starts on a later pass. A task named by two open rows gets two items and waits for both; a row needed by two tasks is asked once. A row whose "Needed by" names no task holds nothing. If you answer the row yourself in the plan, the daemon closes its item with "answered in the plan". If the answer cannot reach the plan in the project folder, the daemon raises a second blocking item, `Put the answer in the plan: <task>`, and the task waits on it. With `[planning] via_pr` set, the change is a pull request: merge it and pull the base branch in the project folder. Otherwise afk could not make the change (for example the row appears twice), and the item gives the exact text to write into the Question cell. Answer that item when you are done and the daemon looks again. Without a readable plan the daemon starts nothing and waits.

**A project with no ledger.** If `.afk/build/ledger.json` does not exist, the daemon starts nothing and raises one blocking item, `This project has no task list yet`, with the `afk ledger init` commands for a new project and for a part-built one. It raises it once, not on every pass. You do not need to answer it: the daemon closes it by itself once the ledger exists.

**Unattended agents.** Nobody can answer a permission prompt, so the daemon starts each agent with permissions bypassed (for Claude Code, `--permission-mode bypassPermissions`). The agent can then run any command in its worktree without asking, so run the daemon only on a machine and a project you are happy to give that to. Before it starts a task, the daemon checks that:

- it is not running as root (the agent refuses this mode for root);
- you have run the agent once by hand in that mode and accepted its warning, so that it is remembered in `~/.claude/settings.json`;
- the afk plugin is installed for the agent.

If a check fails, the daemon adds a blocking `failure` item to your inbox that says what to do, and starts nothing until you answer it. A provider with no unattended mode afk knows (Codex and OpenCode today) cannot be used with the daemon.

**What it writes.** In each project's main checkout: `.afk/build/daemon.json` (what it last did and why), `.afk/build/paused` (the pause flag), `.afk/build/stopped` (the stopped line, with the task and the reason), `.afk/build/after-merge.json` (the record of the `after_merge` command: the task, the merge commit, when it started and finished, its exit code and whether it timed out) and `.afk/build/after-merge.log` (the command's output, replaced on each run), `.afk/build/ship-poll` (when GitHub was last asked, so a one-second tick does not make a call each second, and when its reads started failing), `.afk/build/ledger.json` (each task's status) and `.afk/log/decisions.jsonl` (one line for each decision). Each unattended session also has an empty file, `.afk/<session>/unattended`, that keeps the unattended mode on when its host restarts the agent. In afk's state folder, `daemon.lock` and `running.json` enforce the one-daemon rule, and `governor.json` holds when the daemon last tried to read usage (`probe_tried_at`) and what the last login check found (`login_checked_at`, `login_known`, `logged_in`). `plan-waits.json` records which planning waits a notification has been sent for, one entry per project, so a restart does not send the same one again. `usage.json` also gains a `governor` object: the decision (`action`, `reason`, `wake_at` when there is one, and `at`), stored with the reading by the daemon and by `afk usage`. The pause adds `usage_pause_at` and `usage_wake_at` to a task's entry in `ledger.json`; both are cleared when the task starts again, finishes or leaves `running`. The daemon logs to stderr, one line each with a time; under systemd that is the journal. A hand-back writes `ci-failure.md` in the session's `.afk/<session>/` folder, which is removed with the session.

**`afk daemon --once`** does a single pass over every project and prints one line each: `<project>: <what it did> (<task>, session <session>): <why>`. It takes the same lock as the daemon, so it exits 1 while a daemon is running. Use it to try a setup, or from cron.

**`afk daemon status`** prints a line about the daemon (running or not, and since when). While the daemon is running and could not read its own program file, the next line says: "afk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install." Then comes a block for each project:

```text
The afk daemon is running (pid 4121, started Sun 11 Oct 08:02).

my-app (/home/you/code/my-app)
  Doing:   waiting
  Task:    T4.3, in session t4-3
  Why:     running T4.3; its status file cannot be read right now
  Since:   Sun 11 Oct 09:30 (12m ago)
  Running: session t4-3 has been in this state for 12m
  Tasks:   3 done, 1 running
```

When `usage.json` holds a decision, a line under the daemon's own lines says what the daemon is acting on and when it was made, for example `Usage: nothing new is started. A window is past the limit for starting new work. (as of Mon 12 Oct 09:30)`. It is printed also when no project is configured.

A paused project gets a `Paused:` line first. `Doing:` becomes `Last:` when no daemon is running, because then the file is only a record of how things were left. `Problem:` replaces `Tasks:` when the ledger cannot be read, and with nothing configured it prints how to add a project.

To run the daemon as a service on Linux, see [`contrib/README.md`](contrib/README.md). To get a project ready on an always-on machine (its prerequisites, `afk.toml`, the ledger, the web page over a tunnel), see [`docs/runbook-vm.md`](docs/runbook-vm.md).

**Exit codes:** `afk daemon` returns 0 when it was stopped, when it stopped itself because it found a new version of afk on disk, or when `--once` finished, 1 when `config.toml` could not be used or a daemon is already running, and 2 when the command was written wrongly. `afk daemon status` returns 0 when the report was printed, 1 when `config.toml` itself cannot be read, and 2 when the command was written wrongly.

### `afk project` — Choose the projects the daemon builds

```bash
afk project list
afk project add [--name NAME] [DIR]
afk project remove <name or DIR>
```

The daemon builds the projects listed as `[[project]]` in `config.toml` (see "Machine config" above). `afk project` edits that list for you, so you do not have to open the file. Every other line of the file, comments included, is kept, and afk reads the result again before it saves; if it cannot be sure only one project changed, it writes nothing.

- `afk project list` — One line per project, `<name>  <path>`. With none listed, it says so and how to add one.
- `afk project add` — Lists a project. `DIR` is the current folder unless you give one, and from inside a session's worktree it lists the main checkout. The folder must be a git repository. `--name` sets the name it is known by; the default is the folder name. It creates `config.toml` (and its folder) if there is none. A folder that is already listed is not an error: it says so and changes nothing.
- `afk project remove` — Takes a project out of the list by name or folder. The project's own files and any session that is running are left alone.

The dashboard's all-projects screen does the same two jobs: `ctrl+space +` adds a folder and `ctrl+space -` removes the selected project, with the same refusals as `afk project add` and `afk project remove` (see [`afk dash`](#afk-dash--launch-the-dashboard)).

A running `afk daemon` reads the project list again on every look (every tick), so it picks changes up within a few seconds and does not need a restart. The daemon does not read `[dash]`. The project list is the only setting it re-reads: the tick and everything under `[daemon]`, `[governor]`, `[web]` and `provider` are read once at the start, so restart `afk daemon` after changing any of them. If `config.toml` becomes unreadable while the daemon runs, it keeps the projects it had and logs the problem once. The web page shows a new project one look later than the daemon does.

**Exit codes:** 0 when done or there was nothing to do, 1 when the folder is not a repository, the name is taken, the project is not listed, or the config could not be read or written, 2 when the command was written wrongly.

### `afk pause` and `afk resume` — Hold and release the daemon

```bash
afk pause [project]
afk resume [project]
```

`afk pause` writes `.afk/build/paused` in the project's main checkout, so the daemon starts no new task there. A task that is running finishes, and a parked task is not started again until you resume. `afk resume` removes the file, and it also clears a stopped line (see "The stopped line" in the daemon section), printing which of the two it cleared. `project` is a project name from `config.toml` or a folder; with nothing given it is the current folder's main checkout, even from inside a session's worktree.

The Pause and Resume buttons on the daemon's web page (see "The web page" in the daemon section), and `ctrl+space P` and `ctrl+space U` in `afk dash`, do the same thing.

**Exit codes:** 0 when done (resuming a project that was not paused or stopped is still 0), 1 when the project or the config could not be used or the file could not be written or removed, 2 when the command was written wrongly.

### `[ship]` in `afk.toml` — How the daemon ships a finished task

Every key is optional. Put them under `[ship]` in `afk.toml`, or in `afk.local.toml` (a key there wins). A key of the wrong type, a value out of range or a pattern that does not compile is an error that names the file and the key.

```toml
# afk.toml
[ship]
base = "main"
merge_method = "merge"
ci_reruns = 1
ci_fix_cycles = 2
deploys = true
health_url = "https://example.com/health"
deploy_timeout = "15m"
required_env_pattern = ""
after_merge = "make install"
after_merge_timeout = "10m"
```

| Key | Default | Meaning |
|---|---|---|
| `base` | the session's own base branch | The branch pull requests target and that is brought up to date after a merge. If it is empty and the session has no base either, the task parks rather than guessing. |
| `merge_method` | `"merge"` | How the pull request is merged: `merge`, `squash` or `rebase`. |
| `ci_reruns` | `1` | How many times red checks are re-run before the failure goes back to the session. `0` hands back at once. |
| `ci_fix_cycles` | `2` | How many times a red CI (or a conflict) is handed back to the session before the task parks. |
| `deploys` | `true` | Whether merging deploys. With `false` there is no deploy check. |
| `health_url` | `""` (none) | The address asked after the merge. It must start with `http://` or `https://`. Healthy is any 2xx answer. With no `health_url` the deploy is not polled, whatever `deploys` says. It is never printed in a message, because it may hold a password. |
| `deploy_timeout` | `"15m"` | How long after the merge the deploy may take to turn healthy, as a length of time above zero such as `"15m"` or `"1h30m"`. Past it, the line stops. |
| `after_merge` | `""` (none) | A shell command the daemon runs once after a merge, before the deploy check. It runs through `sh -c` in the project's main checkout, and its output goes to `.afk/build/after-merge.log`. A non-zero exit, a command that cannot be started, or running past `after_merge_timeout` stops the line (see "The stopped line"). **It runs unattended, with the daemon's permissions and its `PATH`**, so only put a command here that you would run yourself, and make sure every program it calls is on the daemon's `PATH`. |
| `after_merge_timeout` | `"10m"` | How long the command may run, as a length of time above zero such as `"10m"` or `"1h"`. Past it the command is stopped and the line stops. |
| `required_env_pattern` | `""` (no check) | A regular expression. If the branch adds a line that matches, the task parks before the merge with an item naming the variable (the first capture group of the match, or the whole match if there is no group). For example `\$\{([A-Z][A-Z0-9_]*):\?\}` catches a new required `${VAR:?}`. The pattern is tried on every added line of every file, so a project whose documents mention `${VAR:?}` may want a pattern anchored to the line's shape (the runbook has one). |

A pull request that has no CI checks is never merged (see "Shipping" in the daemon section). The timings that are not here (five minutes for checks to appear, an hour for a fix after a hand-back, two minutes of quiet before a push) are fixed.

### `afk.local.toml` — Private project settings

Put private values in `afk.local.toml`, next to `afk.toml` in the project's top folder. The main case is `ntfy_url`: an ntfy topic name works as a password, because anyone who knows it can read and post to it, and `afk.toml` is committed.

```toml
# afk.local.toml (never committed)
[notify]
ntfy_url = "https://ntfy.sh/your-topic"
```

afk reads `afk.toml` first and then lays `afk.local.toml` over it key by key: a key in the local file wins, and every key it leaves out keeps the committed value. Either file may be missing. This repo's `.gitignore` lists `afk.local.toml`, so it is never committed; add the same line to your own project's `.gitignore`.

**If your project already has an `afk.toml` with an `ntfy_url` in it,** move that line to `afk.local.toml` before you commit `afk.toml`. This repo used to ignore `afk.toml`; it no longer does, so committing the file as it is would publish your topic.

---

## Session Model

afk sessions are **detached processes**: the session host runs independently in the background, surviving dashboard exit, terminal closure, and even `afk attach` disconnections. This allows:

- **N concurrent sessions**: Create and run multiple sessions independently in the same repo
- **Non-exclusive attachment**: Only one client can be connected at a time (a new attach disconnects the previous one), but the session itself is not exclusive — you can detach, do other work, and reattach later
- **Host restart**: If a session's host crashes or is killed, `afk dash` can restart it (press `ctrl+space r` on the selected session)
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
- Liveness detects a wedged child by nudging it with SIGWINCH and expecting a repaint. Whether the OpenCode TUI repaints on SIGWINCH is not confirmed. A healthy opencode session can then show as wedged (⚠) until its output resumes. Restart is still a confirmed manual action (`ctrl+space r`), so a false wedge costs a stale cell, not a kill.

A host restart resumes the conversation by its id (the `session_id` in status.json) with the provider's resume argv in the table above. A dashboard relaunch of a stopped session reads the same sidecar, so a stopped Codex or OpenCode session relaunches under its own CLI, not `claude`. A sidecar naming an unknown provider is an error, never a fallback to `claude`.

**From the dashboard.** The `ctrl+space n` form's Provider selector (see above) creates a session under any provider. Picking `codex` ticks Autonomous, and the form won't submit `codex` with Autonomous unticked. The `ctrl+space o` action opens a provider picker that defaults to Claude (see `ctrl+space o` above), and `afk new --scratch --provider codex` (or `opencode`) creates a one-off for another provider from a shell. Non-default sessions show their provider on the row, in the detail head, and in the `ctrl+space /` filter. Claude sessions don't show anything extra.

### One-off Sessions

A one-off session (created via `afk new --scratch` or the dashboard's `ctrl+space o`) is a lightweight alternative to a planned session:

- **No branch, no worktree, no plan, no `status.json`** — only a `.afk/{name}/` directory containing `host.json` and `host.log` (the host's socket lives outside it, under `os.TempDir()/afk-{uid}/`)
- **Discovered via `host.json`** — rather than through the status.json/branch/worktree reconciliation that planned sessions use, making them legitimate even without a conductor or a plan
- **Bare CLI for the chosen provider in the repo root** — `claude` by default, or `codex` / `opencode --standalone` when created with `--provider` or the `ctrl+space o` picker. Runs with full repo context but no planning workflow, ideal for quick explorations or ad-hoc help
- **Cleaned without phase gate** — `afk clean <scratch-N>` removes it directly with no "is it done?" check, since there is no work to lose
- **Auto-named `scratch-N`** — when created without an explicit name, the system allocates the lowest available slot (reusing gaps from cleaned sessions)
- **Mutable display label** — press `ctrl+space l` in the dashboard to rename it in the UI without changing the on-disk identity at `.afk/{name}/`

---

## Multi-project support

The dashboard lists every project you added with `afk project add` (or started with `afk plan approve`), plus the repo you launched it from. Give it search roots in the `[dash]` table of `config.toml` and it discovers every other afk-enabled repo beneath them too, lets you switch between all of them from inside the dashboard (`ctrl+space p`, see [`afk dash`](#afk-dash--launch-the-dashboard)), and no longer needs a repo to launch from at all.

### Listed projects

The projects in the `[[project]]` list of `config.toml` (the file `afk project list` prints) show up in the switcher on their own. Each shows the name `afk project list` shows, and a project that is also found under a search folder or is the one you started in is one row, not two. The list is read again on every scan and when you press `R` in the switcher, so a project you add appears without restarting the dashboard. A listed folder that no longer exists is left out, with no message. If `config.toml` cannot be read (for example it holds invalid TOML), the dashboard keeps the rows it already shows and a toast names the file so you can fix it. The dashboard changes this file only through the all-projects screen's add (`ctrl+space +`) and remove (`ctrl+space -`), which do what `afk project add` and `afk project remove` do. `afk plan approve` also lists a project, once its plan is approved.

A project started from the dashboard with **start a new plan** stays in the switcher for the rest of that run. After a restart it comes back only through `afk dash --project DIR` or a search folder, until `afk plan approve` lists it in the project list.

### Config file

The search folders are set in the `[dash]` table of `config.toml`, the same file as the project list. Location: `$XDG_CONFIG_HOME/afk/config.toml` when that is an absolute path, otherwise `~/.config/afk/config.toml`. A missing file, or a file with no `[dash]` table, is not an error: afk behaves as if every key were at its default. The daemon ignores this table.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `search_roots` | array of strings | `[]` (none) | Folders to scan for other afk projects. `~/` expands to your home folder; a relative folder is taken from the folder `afk dash` was started in, not from the config file's location. |
| `scan_interval` | duration string | `"60s"` | How often the dashboard scans `search_roots` again for new or removed projects, and reads this file again, so an edit to `search_roots` or `max_depth` takes effect within one interval, with no restart. A Go duration such as `"30s"` or `"2m"`, at least `"1s"`: a smaller number could only queue scans on top of each other. |
| `max_depth` | integer | `3` | How many folder levels below a search root the scan goes down before giving up on that branch. At least 1. |

Example:

```toml
[dash]
search_roots = ["~/ontra", "~/code/side-projects"]
scan_interval = "30s"
max_depth = 4
```

A search root may itself be an afk repo — it registers as exactly that one project, so a single extra project doesn't need a parent directory invented for it just to be discoverable.

**A bad `[dash]` value is not a startup error.** A `scan_interval` that is not a duration or is under `"1s"`, a `max_depth` below 1, or a value of the wrong kind (a word where a number belongs) does not stop `afk dash`, and it does not stop `afk daemon`, `afk project`, `afk usage`, `afk pause` or `afk inbox` either. The folder search does not run, and a toast in the dashboard names the file and the key (for example `dash.scan_interval`) and says what to write instead. The projects in the `[[project]]` list still show, along with the one you started in and anything an earlier search found. Fix the file from inside the running dashboard; the next scan picks it up. A file that is not valid TOML at all is different: the dashboard keeps the rows it shows and a toast names the file, as described under Listed projects.

**The old `config.json`.** Earlier versions kept the search folders in a `config.json` in the same folder. afk no longer reads it. When one is still there, `afk dash` says so once each time it starts: it prints `afk no longer reads <path>.`, says the search settings now live in `config.toml`, and prints the `[dash]` lines to add, built from the old file when it can be read (a value the new file would refuse is left out with a `#` line naming the key). If `config.toml` already has a `[dash]` line, put the new keys under it and do not add a second one. If the old file cannot be read, the notice says so and gives the three keys commented out with their defaults. The text is printed in the terminal before the dashboard opens, so it is still there after you quit, and the dashboard shows a short toast at start that points to it. The same notice shows when `afk new` hands off to the dashboard. Nothing shows when there is no `config.json`. afk changes neither file: delete the old one yourself once the lines are moved.

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
| **gh** (GitHub CLI) | The daemon cannot push-and-merge: a finished task parks with an inbox item saying the GitHub tool is missing or not logged in. Nothing else in afk uses `gh`. | Install `gh` and run `gh auth login`, then answer the item; the ship carries on from where it stopped. Or push, merge and run `afk ledger set <task> done` by hand. |

**Push notifications** (`afk inbox`) use `ntfy_url` (from `afk.local.toml`, or `afk.toml`) and need no desktop notifier, so they work on Linux; with no `ntfy_url` they are silent.

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

## Version Notes

This is the session manager and dashboard that afk starts from. Codex and OpenCode run as optional per-session providers (see [Agent provider](#agent-provider)); version parity with the Claude Code plugin is the goal. The always-on runner is planned in [`docs/PLAN.md`](docs/PLAN.md) and is not built yet. Small items on the list:

- Real-run output echoing the repo path (avoid the wrong-cwd misfire)
- Additional notifier backends (currently macOS-only: `alerter` for full-fidelity, `osascript` for fallback)
