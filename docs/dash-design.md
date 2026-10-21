# afk dashboard — design system

The look, layout and keys of `afk dash`, the terminal dashboard. `docs/design-system.md` is the web
page's; this is the terminal's. Where this document and `docs/PLAN.md` disagree, the plan wins on
behaviour and this document wins on looks.

The mockups this was drawn from are on a Claude Design canvas,
<https://claude.ai/artifact/WRR3TiSFkwGGMCTq8dTE95> (private to the owner). Everything a task needs is
written down here, so the canvas is a picture of this document, not a second source.

Direction, in one line: *a quiet instrument panel*, the same as the web page. The owner should read the
state of every project in a second and reach the one thing that needs them without hunting. The look
borrows from allisonhere's terminal apps (tidemail, TideFTP, whatthedock): filled title strips on every
pane, full-width selection, key chips, a top bar and a status bar, and named colour themes.

Rules that run through everything. They are the web page's rules, so the two read as one product:

- **Colour never stands alone.** Every coloured glyph has a word beside it; every bar has a number.
- **Orange = needs you.** Waiting on you, plan to approve, a blocking inbox item, parked, yours to do.
  Nothing else is orange.
- **Red = something broke.** Broken, failed, gone, can't read, stopped, destructive confirms.
- **Blue = afk is doing something, or you can.** Running, executing, shipping, the focused pane, keys.
- **Grey = quiet.** Paused, held for usage, pending, nothing ready, one-off.
- **Green = done.** Done, finished, shipped, progress fill, a healthy host.
- **Plain words.** "asks you", "open it", "pause the build". An ellipsis means a confirm follows.

---

## 1. Themes

Every colour is one of the tokens below. A theme is one value for each token. The dashboard paints its
own background (`bg`) on every cell, so it looks the same in any terminal and an overlay can dim what
is under it (§3.9).

| Token | Used for |
|---|---|
| `bg` | the ground behind everything |
| `panel` | inside panes and overlays |
| `strip` | an unfocused title strip, the top band, the status bar, linked rows |
| `strip-fg` | text on `strip` |
| `line` | an unfocused pane's border, rules |
| `accent` | the focused pane's border and title strip, keys, key chips |
| `on-accent` | text on `accent`, and on an orange alert bar |
| `fg` | body text |
| `muted` | labels, secondary text, quiet states (meets 4.5:1 on `panel`) |
| `faint` | the empty part of a bar, rules, line numbers (decoration only, never text you must read) |
| `sel` / `sel-fg` | the selected row's fill and text |
| `need` | orange: needs you |
| `bad` | red: something broke |
| `work` | blue: afk is working |
| `done` | green: done |
| `meta` | task ids, branch names, inbox ids |

Four themes ship. `tide` is the default on a dark terminal and `paper` on a light one, using the
dashboard's existing background check. `[dash] theme = "<name>"` in `config.toml` picks one.

| Token | tide | fjord | paper | phosphor |
|---|---|---|---|---|
| `bg` | `#0a1222` | `#2b303b` | `#f6f5f1` | `#050a06` |
| `panel` | `#0c1629` | `#2e3440` | `#fdfdfb` | `#07100a` |
| `strip` | `#16274a` | `#3b4252` | `#ebe9e2` | `#0f2414` |
| `strip-fg` | `#a9bde0` | `#d8dee9` | `#4a4740` | `#9fe6ad` |
| `line` | `#22385f` | `#434c5e` | `#d6d3c9` | `#1b3a22` |
| `accent` | `#63b3f2` | `#88c0d0` | `#2f5fb3` | `#46e07a` |
| `on-accent` | `#061020` | `#232831` | `#ffffff` | `#04140a` |
| `fg` | `#d8e3f4` | `#e5e9f0` | `#1c1b18` | `#c4f5cd` |
| `muted` | `#8496b8` | `#a3adbf` | `#66635b` | `#6fae7c` |
| `faint` | `#2a3f66` | `#4c566a` | `#cfccc1` | `#1f4228` |
| `sel` | `#1e4b86` | `#4c566a` | `#dbe5f6` | `#15532a` |
| `sel-fg` | `#ffffff` | `#eceff4` | `#10213f` | `#eaffee` |
| `need` | `#f3a35c` | `#e6a283` | `#a54f00` | `#f5c35a` |
| `bad` | `#f2727c` | `#e8848c` | `#b42318` | `#ff7a6b` |
| `work` | `#63b3f2` | `#88c0d0` | `#2f5fb3` | `#8fd8ff` |
| `done` | `#7fd3a2` | `#a3be8c` | `#2c7a4b` | `#46e07a` |
| `meta` | `#b8a2f5` | `#c9a6c4` | `#6b4fb3` | `#b6efc0` |

On a 256-colour terminal the colours are rounded to the nearest the terminal has. On a 16-colour
terminal the dashboard does not paint `bg` or `panel` and maps each token to the nearest named colour,
so it reads like today's dashboard.

---

## 2. The grid

One cell is one character. The reference size is **120×40**; the minimum is **80×24**. Row 1 is the top
bar, the last row is the status bar, and everything else is the current screen.

- Below 100 columns a list row that holds a sentence (the home screen, the inbox) becomes two lines:
  name and numbers, then state and sentence.
- Overlays keep 2–3 columns of the dimmed screen visible on each side when there is room. Below 100
  columns the agent and the details overlay take the full width.
- Long text is cut with `…`, never mid-way through a word in a name.

---

## 3. Parts

### 3.1 Top bar (row 1)

`[ afk ]` badge (`on-accent` on `accent`), then where you are as a path in `muted` with the last part in
bold `fg`: `All projects ▸ afk ▸ feat-auth`. On the right, in this order: what needs you elsewhere
(`● 3 more need you elsewhere`, `need`), the inbox count with its blocking part in `need`, and usage
(`usage running · week 62%`, the decision word coloured by its meaning). As the terminal narrows the
right side gives up pieces in the order the header does today (`docs/dashboard.md`), the attention
count last.

### 3.2 Status bar (last row)

Filled with `strip`. On the left, the single most urgent thing in plain words (`● feat-auth asks you`,
`need`), or a notice after an action. On the right, the keys for this screen as `key` (bold `accent`)
plus a word, separated by two spaces. Always ends with `? keys` where `?` works.

### 3.3 Pane and title strip

A pane has a one-cell border (`line`) and a title strip on its first row: `strip` fill, bold `strip-fg`
text, the pane's name on the left and context on the right. **The pane with the keyboard** has an
`accent` border and an `accent` strip with `on-accent` text. Only one pane has the keyboard at a time.

### 3.4 Section (inside the project view)

A pane inside a pane, numbered in its strip: `1 Sessions`, `2 Build`, `3 Inbox`. Same focus rule as a
pane. A **read-only pane** (the selected session's summary) has a plain strip: no fill, `muted` text, a
rule under it. It never takes the keyboard and `tab` skips it.

### 3.5 Rows and selection

One row per line, padded one cell each side. The selected row is filled edge to edge with `sel` and its
text is `sel-fg` (coloured words on it turn `sel-fg` too, so the fill always reads). A **linked row**, one
that belongs to the selection in another section, is filled with `strip` and ends with `◂ name` in
`accent`.

Group headings are bold, coloured by the group, with a fold mark: `▾ Needs you`, `▾ Working`, `▾ Done`
(`▸` when folded), and a count on the right.

### 3.6 Status glyphs and words

| Glyph | Colour | Means | Words that go with it |
|---|---|---|---|
| `●` | `need` | needs you | asks you, waiting on you, plan to approve, parked, yours to do |
| `✕` | `bad` | broke | broken, failed, gone, can't read, stopped |
| `◐` | `work` | working | running, executing, reviewing, planning, shipping |
| `○` | `muted` | quiet | paused, held for usage, pending, nothing ready, one-off |
| `✓` | `done` | done | done, finished |

The mapping from a session's phase or a project's state to a glyph and colour lives in one function, not
at each call site.

### 3.7 Band, bars and key chips

- **Band**: one row filled with `strip` that sums up a screen (`● 4 need you   ◐ 2 running …`).
- **Progress bar**: `█` in `done` for the done part, `░` in `faint` for the rest, then `3/12`. Usage
  bars are the same with `62%`.
- **Key chip**: the key in bold `on-accent` on `accent` with a cell of padding, then the action in
  `muted`: `[enter] open agent  [d] details`. Chips sit on a pane's last row.
- **Alert bar**: a full-width row in `need` with `on-accent` bold text, for the one thing on a screen
  that is waiting on you.

### 3.8 Overlays

An overlay is a pane drawn over the current screen, with the `accent` border. `esc` closes it. Sizes:
the project view is the screen less 2 columns each side; the agent less 1; the details overlay is 54
columns on the right; inbox, jump and actions are 76–84 columns, centred near the top; the key strip is
54 columns in the bottom-right corner.

### 3.9 Dimming

Whatever is under an overlay is dimmed: every cell's text and background colour is blended **70% of the
way to `bg`**, and bold is turned off. This is done on the rendered frame, cell by cell, with
lipgloss's `Canvas` (`CellAt`/`SetCell`), and the overlay is then laid on top with the `Compositor`, as
`composeModal` does today. A cell with no colour of its own (the terminal's default, common in the
agent's output) is treated as `fg` on `bg` before blending. On a 16-colour terminal the faint attribute
is used instead. The key strip dims the agent less (50%), because it is open for a moment.

---

## 4. Screens

### 4.1 All projects (home)

The screen `afk dash` opens on when it is not started inside a project. One pane, `All projects`.

1. Band: counts by group (`● 4 need you ◐ 2 running ○ 2 quiet ✓ 1 finished`), then inbox (`inbox 5 ·
   3 blocking`) and usage as a bar.
2. Column heads: `PROJECT STATE TASKS NOW LAST 24H INBOX`.
3. Rows grouped `NEEDS YOU`, `RUNNING`, `QUIET`, `FINISHED`, in that order; inside a group, the order of
   `config.toml`. A row: glyph, name, state word, progress bar, one sentence on what it is doing or why
   not, a 12-cell activity line for the last 24 hours (`▁▂▃▄▅▆▇`), and inbox (`1 · blocks`).
4. Under a rule, the selected project: what it waits on, the question in full, and what the answer
   unblocks.
5. `THIS WEEK`: the tokens used each day of the last seven, as a bar per day, and the usage line
   (`week 62% · 5-hour 28% · resets Thu 15:57`).
6. Chips: `enter open project`, `a answer`, `p pause / resume`, `n new plan`, `+ add`, `- remove`.

The state words and sentences are the all-projects screen's today (`docs/dashboard.md`).

### 4.2 Project

`enter` on a project. An overlay over the dimmed home screen. Strip: name and folder, `esc · all
projects`. Band: the project's state, tasks done, build state, inbox, open pull requests.

- Left, 50 columns: **`1 Sessions`**, grouped `Needs you`, `Working`, `Done`; a row is glyph, name,
  phase, tasks, and `asks you` / `open it` on the right. Under it, the **read-only pane** for the
  selected session: branch, phase and loop, task, the question it asks (in full, `need` label), diff,
  verdict, host.
- Right: **`2 Build`**, one row per master-plan task (id, status word, title, detail), the selected
  session's task as a linked row; **`3 Inbox`**, one row per open item (`!` in `need` when it blocks,
  kind, task, title).
- Chips: `tab`/`1-3` section, `j/k` move, `enter` open agent, `d` details, `a` answer, `n` new session,
  `c` clean…, `p` pause.

`enter` does what the selection suggests: a session opens its agent, a task opens its details, an inbox
item opens the answer screen.

Below 100 columns one section fills the body, its strip names all three (`1 Sessions · 2 Build · 3
Inbox`, the focused one bold), and the read-only pane is left out; `d` shows it.

### 4.3 Agent

`enter` on a session. The agent's live terminal, nearly full screen, over the dimmed project view.
Strip: `Agent · <session>` and `keys go to the agent · ctrl+] esc back`. Band: phase, task, diff,
loop, host, and on the right `next needing you: <session>`. Then the agent's terminal, untouched. Chips:
`ctrl+] esc back`, `ctrl+] ]` next that needs you, `ctrl+] d` details, `ctrl+] m` mouse, `ctrl+]
ctrl+]` send ctrl+] to the agent.

A session with no live agent (stopped, pending, cleaning) shows its details overlay instead, with
`r restart` when restarting is possible.

### 4.4 Details

`d` anywhere a session is selected, `ctrl+] d` in the agent. 54 columns from the right, over the
dimmed screen. Tabs `1 Overview 2 Waves 3 Stack 4 Notes 5 Diff` (the active tab filled with `strip`).
Overview: `SESSION` (phase, host, branch, stack, updated), `PROGRESS` (tasks bar, diff, verdict),
`READY` (the next tasks), `ISSUES`. The other tabs hold what today's detail tabs hold. `j/k` scroll,
`esc` back to where you were. The agent keeps running behind it.

### 4.5 Answer an inbox item

`a` (the oldest item that blocks a task, in the project or across all of them on the home screen), or
`enter` on an inbox row. Strip `Inbox · <project>`, `1 of 3 · ↑↓ other items`. The kind, id, the task
it holds up, who asked and when; the title in bold; the body; `CHOOSE ONE` with the item's options
numbered, the recommended one labelled `recommended`, and a last option `Write my own answer` that
opens a field. `enter` sends. A line says what happens next ("T2.2 starts again within a few seconds of
your answer").

### 4.6 Jump

`/` anywhere (`ctrl+] /` in the agent). A search box over projects, sessions and tasks across every
project; things that need you sort first, matched letters underlined. A row: glyph, name, project,
kind, state. `enter` goes there: a project opens its view, a session its agent, a task its project with
the task selected.

### 4.7 Actions by name

`:` (`ctrl+] :` in the agent). A search box over every action, including the ones with no key (the plan
actions: extend, amend, fix a bug, document, errors, approve, start a new plan). Letters always type
into the box. A row: the name with the match underlined, its key if it has one, and one line of help;
an action that cannot run now is `muted` and its line says why.

### 4.8 The `ctrl+]` key strip

`ctrl+]` in the agent shows a small pane in the bottom-right corner listing the afk keys that work
there, two to a row. The next key runs one; `esc` or any key not on it closes the strip and goes back
to the agent with nothing sent. The status bar says `waiting for an afk key`.

### 4.9 Keys

`?`. A large overlay: the path `All projects ─enter▸ Project ─enter▸ Agent ◂esc`, the agent rule in an
alert band, then the keys in columns by level (§5). `/` searches it.

---

## 5. Navigation and keys

There are three levels, **All projects → Project → Agent**, and overlays that open over any of them.
`enter` goes in a level, `esc` comes back out one, and `esc` closes an overlay before anything else.

- **At All projects and Project, keys work alone.** Nothing else can take them.
- **In the Agent, every key goes to the agent.** Press `ctrl+]` (the leader, set in `config.toml`)
  first, then the same key as anywhere else. `ctrl+]` twice sends one `ctrl+]` to the agent.
- **The same letter does the same thing at every level**, on whatever is in view.
- `ctrl+c` quits from every level but the Agent, where it goes to the agent.

| Key | Where | Does |
|---|---|---|
| `j` `k` `↑` `↓`, `pgup` `pgdn`, `home` `end` | everywhere | move |
| `enter` | everywhere | go in, open |
| `esc` | everywhere | go back, close |
| `]` `[` | everywhere | next / previous thing that needs you, across every project |
| `/` | everywhere | jump to a project, session or task |
| `:` | everywhere | search actions by name |
| `a` | everywhere | answer the oldest item that blocks a task |
| `i` | everywhere | inbox |
| `d` | everywhere | details of the selected session |
| `R` | everywhere | read usage now |
| `?` | everywhere | keys |
| `q` | everywhere | quit (sessions keep running) |
| `p` | All projects, Project | pause or resume that project's build |
| `n` | All projects | start a new plan |
| `+` `-` | All projects | add or remove a project |
| `tab` `shift+tab`, `1` `2` `3` | Project | change section |
| `n` | Project | new session |
| `o` | Project | one-off session |
| `c` | Project | clean the session… |
| `r` | Project | restart a stopped agent |
| `l` | Project | label the session |
| `s` | Project, Build section | set the task's status… |
| `t` | Project, Build section | the task's tokens |
| `1`–`5` | Details | change tab |
| `m` | Agent (after `ctrl+]`) | hand the mouse to the terminal, or take it back |
| `y` `n` | a confirm | yes, no |

What it replaces: `ctrl+] t` is `d`; `ctrl+] \` and `ctrl+] f` are `esc` and `enter`; `ctrl+] p` and
`ctrl+] A` are `/` and `esc`; `ctrl+] P` and `U` are `p`; `ctrl+] g` is `m`; `ctrl+] /` and `x` are `/`
and `esc`; `ctrl+] b` is the project view's Build section; `ctrl+] w` goes, because nothing is split.

Where `afk dash` starts: inside a project, on that project's view (with All projects behind it, so `esc`
goes there); anywhere else, on All projects. `--project PATH` starts on that project's view, and
`--open <name>` on that session's agent.
