# In-TUI session hosting: embedded terminal pane spike — findings

Spike branch: `internal-tmux-exploration`. This document is the findings
deliverable (W9.T1): what was built, how to smoke-test it by hand, what is
known to be risky or unsolved, and a recommendation for whoever
productionizes (or abandons) this approach. Sources: the execution notepads
(`.afk/internal-tmux-exploration/{learnings,decisions,issues}.md`) and the
code as it exists on this branch.

Context: `afk new` used to launch each conductor in a new tmux window and
`afk dash` was a read-only dashboard that jumped to those windows. This
spike replaces the tmux-window launch with an embedded terminal pane — the
`claude` conductor runs as a PTY-attached child rendered by an in-process
terminal emulator inside the dashboard. Web research before the spike found
no production prior art for hosting agent CLIs this way (comparable tools
delegate to tmux), so these findings are first-hand evidence, not a survey.

## 1. What was built

### 1.1 The termpane bridge (`internal/termpane`)

A UI-framework-agnostic package (no bubbletea import; channels and plain
types only) that bridges a PTY-attached child process to a renderable pane:

- `termpane.Start(Config, Deps) (*Pane, error)` — Config carries argv,
  working dir, env overlay, and initial cols/rows. The child is started on a
  PTY (creack/pty `StartWithSize`) with `TERM=xterm-256color` forced via an
  env merge that removes the inherited variable before appending (duplicate
  names in an env block resolve implementation-defined, so append-order is
  not trusted).
- Output pump: a dedicated goroutine copies PTY output into a
  `charmbracelet/x/vt` emulator (`vt.NewSafeEmulator`, scrollback set to 0).
  Never on the caller's goroutine — the Bubble Tea update loop must not
  block.
- **Bidirectional pump (load-bearing)**: the emulator's reply stream is an
  unbuffered `io.Pipe` shared by query responses (DA1, DSR/CPR, OSC 10/11 —
  claude queries the terminal at startup, e.g. background-color detection)
  AND keyboard input (`SendKey`/`SendText`/`Paste` write the same pipe). A
  second goroutine (`pumpResponses`) copies that stream back into the PTY.
  Without it, a child query deadlocks the output pump mid-`Write` — the
  handler blocks on the pipe while holding the emulator's write lock. A DSR
  round-trip is unit-tested: feeding `ESC[6n` as child output yields
  `ESC[1;1R` back on the reply stream.
- Mode-aware input: `SendKey(KeyEvent)` / `SendText(string)` /
  `Paste(string)` delegate all byte encoding to the emulator. Verified
  against the pinned x/vt version: DECCKM application-cursor mode changes
  arrow encoding (`ESC O A` vs `ESC [ A`), and bracketed paste wraps in
  `ESC[200~`/`ESC[201~` only after the child enables `?2004h`. Callers never
  hand-roll escape bytes.
- `Render() string` returns the emulator screen; `Resize(cols, rows)`
  resizes emulator and PTY together (TIOCSWINSZ delivers SIGWINCH to the
  child); `Changed()` is a cap-1 coalescing notification channel;
  `Done()`/`Err()` signal child exit (closed channel — level-triggered, safe
  for re-armed subscribers); `Close()` SIGKILLs the child and reclaims both
  goroutines with a bounded (≤500ms) output-drain window.
- **Crash containment**: both pumps run under `recover()`; an emulator panic
  becomes an exit-with-error signal instead of crashing the dashboard
  process. Both directions are unit-tested (output-pump panic and
  response-pump panic).
- DI seam: `Deps.StartChild func(Config) (Child, error)` returns a `Child`
  interface (Read/Write/Resize/Kill/Wait/Close) abstracting the process and
  its PTY master together, so unit tests fake the whole child with an
  in-memory pipe — no real processes in tests.

### 1.2 Dashboard embedding and message flow (`internal/ui`)

The dashboard talks to the pane through a `paneHandle` interface behind a
`Deps.OpenPane` seam (hermetically fakeable). Message flow, all following the
package's `<Noun>Msg` / `<verb>Cmd` conventions:

```
openSessionPane(name, dir, argv)          — single guarded funnel, enforces
  └─ openPaneCmd ────────────────────────── the one-pane rule
       ├─ paneOpenedMsg{pane, name}       — pane live; subscription armed
       └─ paneOpenFailedMsg{name, err}    — error status line, never a dead pane
paneEventCmd (select on Changed/Done, re-armed after each message)
       ├─ paneRedrawMsg{pane}             — one View() render per notification
       └─ paneExitedMsg{pane, err}        — "session ended" status, Close(),
                                            clear state, loadCmd() reload
```

`paneExitedMsg` is deliberately distinct from `execFinishedMsg` (conductor
exit means "session ended", not "command finished"; `execAfk` +
`execFinishedMsg` survive for `clean`). Redraw/exit messages carry the
originating handle and are ignored if it is not the current pane, so a late
token from a closed pane cannot corrupt a successor.

Observed redraw behavior (ticker-vs-redraw): the `Changed()` channel is cap-1
coalescing and the subscription consumes-then-re-arms, so a child streaming
hundreds of writes/sec collapses to roughly one render per scheduler
round-trip, not one per write. Pane redraws are entirely event-driven and
decoupled from the 2s discovery ticker: a busy pane does not increase
discovery frequency and the ticker does not force pane repaints. Selecting on
Changed and Done in one Cmd also orders the final paint before the exit
message — the user sees the conductor's last output before "session ended".

Focus and input routing: `f` (or enter on the hosted session) focuses the
pane; while focused, ALL keys — including Ctrl+C, which is the child's
SIGINT — are forwarded through the mode-aware input API. `ctrl+\`
(KeyCtrlBackslash; arrives as a raw key byte, no SIGQUIT) is the single
reserved escape chord back to the dashboard. Paste goes through the
paste-aware path so bracketed paste works. `tea.WindowSizeMsg` recomputes the
pane region from a single layout function (`layoutDims`, shared by View and
the resize path so the emulator geometry always matches the rendered box) and
resizes PTY+emulator together.

Guards while a pane is active: launching a second pane is blocked (one-pane
rule), `n` (new form) and `c` (clean) are blocked with status messages, and
quit (`q`/ctrl+c when unfocused) requires a confirming second press within 3s
(`paneQuitConfirmWindow`, timestamp-based via `deps.Now()` for testability);
confirmed quit calls `pane.Close()` before `tea.Quit`.

### 1.3 Launch dispositions

`internal/session` no longer executes tmux anywhere. `session.Build` resolves
an exported `Plan.Outcome` (`LaunchNone` zero value / `LaunchDash` /
`ManualInstructions`):

- `DryRun || NoLaunch` → `LaunchNone` (dry-run is side-effect-free and must
  never signal a handoff to a session that was never created; the dry-run
  step *preview* still shows the real disposition — intentional divergence).
- else TTY on stdout (`Deps.IsTTY`, default go-isatty) → `LaunchDash`.
- else → `ManualInstructions`: prints
  `cd <worktree> && claude "/afk:go --session <name>"`, verbatim from the
  retired tmux-absent arm.

`cmdNew` (new.go) calls `Build` + `Plan.Run` and, on `LaunchDash`, starts the
dashboard in-process via the shared `runDash` path (no exec, no recursion)
with `ui.Deps.InitialSessionName` set. `afk dash --open <name>` uses the
same path. The initial pane-open is gated on BOTH the first discovery load
and the first `WindowSizeMsg` (the PTY needs real cols/rows), attempted
exactly once — and skipped (consumed, with a status message) if the user has
already opened the creation form before both gates cleared, so a startup
pane can never open invisibly behind the modal form (fix-cycle-2). Argv selection: empty `SessionID` (planning phase — the
just-created case) → fresh `claude "/afk:go --session <name>"`; non-empty →
`claude --resume <session_id>`. `afk new --no-launch` creates only.

Enter-key behavior: enter on the hosted session focuses the pane
(unconditionally — a live pane trumps on-disk status); on a stale session,
the early-return fires first and never opens a pane; on a different session
with empty `tmux_window` and a known SessionID, it opens a resume pane
(`claude --resume <id>` — bare `--resume` opens claude's picker, so the ID is
mandatory; discovery gained a read-only `SessionState.SessionID`
passthrough); with no SessionID it falls back to guidance text; on a
tmux-hosted session it still jumps to tmux, untouched. The dashboard `n` key
opens an in-TUI creation form (name validated via `session.ValidateName`,
base from git-spice data when detected, else deferred to session's trunk
resolution) that creates via a `Deps.CreateSession` seam in no-launch mode
and opens the new session's pane on success; errors keep the form open for
correction. The nested-dashboard recursion of the old suspend-and-exec `n` is
gone.

New sessions never write `tmux_window` (stays `""`, valid schema state); the
schema and `probe.ProbeTmux` are untouched.

## 2. Manual smoke-test checklist

The hermetic test suite covers all message-flow logic, but the sandboxed
execution environment had no controlling TTY, so the live embedded-pane arm
was never exercised end-to-end during the spike (see §3.9). This checklist is
the required gate before any productionize decision. Run in a real terminal
(≥ 24 rows), in this repo, with `claude` on PATH.

1. **Build**: `go build -o afk .` succeeds; `gofmt -l .` prints nothing;
   `go vet ./...` and `go test ./...` pass.
2. **TTY handoff**: `./afk new scratch-a` — the dashboard appears with a
   pane open for `scratch-a` and the claude conductor rendering inside it
   (colors and box-drawing intact — garbled output means a TERM/emulator
   problem). No tmux window is created (`tmux list-windows` unchanged, if
   tmux is running).
3. **Non-TTY disposition**: `./afk new scratch-b </dev/null | cat` creates
   the session, prints the manual `cd <worktree> && claude "/afk:go
   --session scratch-b"` instructions, and exits 0 without launching
   anything. Also confirm `./afk new --no-launch scratch-c` prints only the
   creation summary + "Launch skipped".
4. **In-TUI form create**: in `./afk dash`, press `n` — the creation form
   opens. Type an invalid name (e.g. `Bad_Name`) and confirm inline
   validation feedback; correct it to a kebab-case name, submit, and confirm
   a real session is created and its conductor pane opens. Esc from a fresh
   form cancels.
5. **Focus toggle + Ctrl+C ownership**: press `f` (or enter on the hosted
   row) to focus the pane; type into claude and confirm keystrokes reach the
   child (arrows, enter, backspace behave). Press Ctrl+C while focused and
   confirm it interrupts the *child* (claude reacts) — the dashboard must not
   quit or show its quit-confirm prompt.
6. **Escape chord**: press `ctrl+\` and confirm focus returns to the
   dashboard (help/footer and pane title reflect the focus change); dashboard
   keys (j/k navigation) work again; the child never sees the chord.
7. **Paste**: focus the pane and paste multi-line text into claude. Confirm
   it arrives as a paste (claude treats it as one block, not line-by-line
   auto-executed input) — this exercises bracketed paste through the
   emulator.
8. **Resize reflow**: with the pane open, resize the terminal window larger
   and smaller. The pane region recomputes and the child re-renders at the
   new size (claude's own layout reflows — SIGWINCH delivered). No stale
   right/bottom edges.
9. **Narrow terminal**: shrink to < ~131 columns. By the layout math (35%
   sidebar with a 24-col floor, plus the pane box's 6 columns of
   border/padding/wrap margin — lipgloss v1's `Width()` includes padding, so
   the box wraps text at `Width − 2` and the emulator is sized to match),
   the pane content is `⌈0.65·W⌉ − 6` columns, which first reaches claude's
   ~80-col preference at W = 131 (sidebar 45, main region 86, content 80;
   at 130 it is 79) — record what claude's UI does (wrap, truncate, or
   break) below that and at what width the layout becomes unusable. This
   is an open design input for productionization (collapse the sidebar?
   minimum-width floor?).
10. **Conductor exit → reload**: let the conductor finish (or exit claude
    manually). Confirm the final screen is painted, then a "session ended"
    status appears (distinct from the `clean` completion message), the pane
    closes, and the session list refreshes with the session's updated phase.
11. **Quit-with-active-pane confirm**: with a pane open (unfocused), press
    `q` once — the dashboard must NOT exit; a "press q again" status appears.
    Wait > 3s and press `q` once more — still no exit (window expired,
    re-armed). Press `q` twice within 3s — the dashboard exits and the hosted
    claude process is gone (`ps` shows no orphan). Also confirm `c` (clean)
    and `n` are blocked with status messages while the pane is active.
12. **Enter-resume on an ended in-TUI session**: after step 10 (session ended,
    non-empty session_id in status.json), select that session and press
    enter. Confirm the status hint ("resuming … — if it's already open
    elsewhere, cancel") appears and the pane opens with `claude --resume
    <id>` resuming the SAME conversation (prior transcript visible), not
    claude's session picker and not a fresh conductor.
13. **dash --open resume arm**: quit, then `./afk dash --open scratch-a` —
    the pane opens resuming that session by ID (same conversation again).
14. **Legacy tmux jump**: on a session with a non-empty `tmux_window` (create
    one by hand-editing a scratch status.json, or use a pre-spike session),
    enter still jumps to the tmux window; the jump-failure fallback text
    names both the in-TUI option and the manual `claude --resume <id>`
    command.
15. **Pane-open failure surfaces**: temporarily shadow `claude` off PATH and
    trigger a pane open (enter-resume or `n`-form). Confirm an error status
    line with the underlying error appears — never a silently dead pane —
    and the dashboard stays usable.

## 3. Known risks and open problems

### 3.1 In-process emulator crash containment — the dashboard is still a single point of failure

Both pump goroutines convert panics into exit-with-error signals
(`containPanic`, unit-tested in both directions), so an emulator panic ends
the *pane*, not the process. But this containment only covers the pump
goroutines: emulator methods called from the UI goroutine (Render, SendKey)
run outside it, and any in-process bug — emulator or otherwise — shares the
dashboard's address space. tmux's process isolation has no analogue here:
with tmux, a rendering bug kills a window; here, a sufficiently bad bug kills
the dashboard and (per §3.2) the hosted conductor with it.

### 3.2 Process-lifetime coupling: dashboard quit/SIGHUP kills the hosted conductor

The fundamental trade-off of in-process hosting. The conductor is a child of
the dashboard: confirmed quit calls `Close()` (SIGKILL), and an unclean
dashboard death (crash, SIGHUP from a closed terminal, SSH drop) takes the
conductor down mid-run. tmux sessions survive all of these. Partial
mitigations in place: the quit-confirm flow prevents accidental quits, and
because conductor state lives in claude's session store, `claude --resume
<id>` (enter-resume, `dash --open`) recovers the *conversation* after a kill
— but not any in-flight work the conductor was executing at that instant.
There is no detach: you cannot close the dashboard and leave the session
running. Related sharp edges recorded during execution:

- **SIGKILL-only termination** — no SIGTERM/grace window; grandchildren the
  conductor spawned may be orphaned. Productionization should add
  SIGTERM + grace + escalation.
- **Synchronous `Close()` on confirmed quit** blocks the UI goroutine for up
  to the 500ms drain window (acceptable when exiting; a production design
  would Close in a Cmd).
- **Bounded 500ms drain on exit**: a grandchild holding the PTY slave open
  loses output after the window — chosen for guaranteed `Close()` liveness.

### 3.3 charmbracelet/x/vt is experimental

Pseudo-versioned off main (`v0.0.0-20260713092006-0d683c34c74b`), no release
tags, no compatibility promises. Concrete maturity evidence from one spike's
worth of use:

- **A real data race found by `-race`**: `Emulator.Close()` writes an
  unexported flag unsynchronized against concurrent `Read()`. At the pinned
  version, exactly three methods bypass SafeEmulator's lock: `Read` (a
  lockless pass-through) and `Close`/`String` (not wrapped at all — they
  fall through to the embedded `*Emulator`); `Width()`/`Height()`/
  `CursorPosition()` ARE wrapped (an earlier notepad entry misidentified
  them, corrected in fix-cycle-2). termpane works around the race by
  retiring the response pump (shutdown flag + wake byte) before
  `emu.Close()`, and obtains plain text via `ansi.Strip(Render())` rather
  than `String()`. Note well: the entire Read/Close safety rests on
  `awaitExit`'s single-function teardown-ordering invariant (now documented
  prominently at that function in termpane.go) — it is invisible to
  internal/ui's tests, which fake the pane behind `paneHandle`, so any
  productionization must preserve that ordering and re-run the race
  detector on internal/termpane after touching it. Anyone extending the
  Pane API must check `safe_emulator.go` before exposing a new emulator
  method.
- **Silently dropped keys**: vt's `SendKey` default arm emits bytes only for
  unmodified keys — ctrl/shift-modified arrows, alt+special, F13+, and
  CSI-u/Kitty extended keys produce nothing. Fine for claude's actual
  interaction set (plain arrows, enter, backspace, Ctrl+C, text, paste,
  shift+tab); ctrl+arrow word-motion etc. would need upstream work or a
  CSI-u encoder.
- The plan's cited method names all existed at the pinned version, but a
  future `go get -u` can rename or reshape any of them without a major
  version signal. Pin deliberately; treat bumps as migrations.

### 3.4 Discovery ticker vs pane redraw — interaction observed, benign

Reported as required, though it turned out well-behaved: pane redraws are
event-driven off the cap-1 `Changed()` channel (roughly one render per
scheduler round-trip under a streaming child, NOT one per write), and the 2s
discovery ticker only drives file-based session discovery. The two never
compound — a busy pane does not increase discovery shell-out frequency, and
the ticker does not force pane repaints. The residual cost is inherent to
Bubble Tea: every ticker tick re-renders the whole View including the pane
region (one extra `Render()` string build every 2s), which was not measurable
as a problem in the spike but has not been profiled under a heavy conductor.

### 3.5 No IPC: enter-resume cannot detect a conductor running elsewhere

Each `afk new` spawns an independent dashboard process; dashboards do not
know about each other (deliberate spike constraint — no IPC built). Discovery
is file-based, so a session's status.json cannot say "a live conductor has
this open in another dashboard's pane". Enter-resume on such a session spawns
a SECOND concurrent `claude --resume <id>` against the same worktree — two
agents mutating one working tree. Mitigated only with a status-line hint
("resuming <name> — if it's already open elsewhere, cancel"). A production
design needs real liveness (lockfile/pidfile in the session dir, or a
socket), plus a decision about whether `afk new` should reuse a running
dashboard instead of spawning a new one.

### 3.6 Single-pane limitation

The spike supports exactly one pane; launches while a pane is active are
blocked with a status message. Multi-pane would require: a pane collection
keyed by session (state, subscriptions, and stale-notification guards per
pane — the message types already carry the originating handle, which helps),
a layout manager beyond the current single sidebar/main split, per-pane focus
routing and escape-chord semantics, resource limits (each pane is a live
claude process + emulator + two goroutines), and revisiting the quit-confirm
and blocked-key rules for N>1. Nothing in termpane itself assumes a
singleton, but everything in the model does.

### 3.7 No transcript capture

Scrollback is deliberately 0 and nothing records the PTY byte stream. Output
that scrolls off the emulator screen is gone; after `paneExitedMsg` only the
final screen was ever visible. Post-mortem debugging of a failed conductor
relies entirely on claude's own session store (`--resume` shows the
conversation, not raw terminal output). Productionization should consider a
tee of the PTY stream to a per-session log file — cheap to add in
`pumpOutput`.

### 3.8 Input/render backpressure can stall the UI goroutine — potentially unbounded, not momentary

(Wording corrected in fix-cycle-2: earlier notepad entries and drafts of this
section called the stall "momentary"; it is not bounded.) Input forwarding is
synchronous in Update, and View() calls the pane's `Render()` on every
update. Under backpressure the stall chains: a child that emits a terminal
query while not draining its stdin leaves the response pump blocked writing
into the full PTY; the emulator's query handler then blocks writing the
unbuffered reply pipe from inside `emu.Write` — while HOLDING SafeEmulator's
write lock — and every subsequent `Render()`, `SendKey`, `SendText`, and
`Paste` (all lock-taking) freezes the UI goroutine with it, including the
update that would process the quit key (View renders the pane on every
update). Nothing bounds the stall except the child itself: it lasts until
the child drains stdin or dies (teardown's `CloseWithError` unblocks all
writers, so the pane *lifecycle* cannot deadlock — but a live, wedged child
freezes the dashboard indefinitely). Normal interactive typing never
approaches this, but it must be weighed as an unbounded-stall risk, not a
momentary one, in any productionize decision: buffer input through a channel
plus a dedicated writer goroutine, and take render snapshots off the UI
path.

### 3.9 The live happy path was never executed during the spike

The execution environment had no controlling TTY, so "afk new lands in
dash+pane with a real claude rendering" was never observed end-to-end — only
the hermetic fake-pane/fake-PTY tests (which are thorough: full lifecycle,
both panic directions, DSR round-trip, race-detector-clean at `-count=3`) and
the non-TTY/dry-run/no-launch arms live. §2 exists precisely to close this
gap; treat the checklist as unexecuted until someone runs it.

**Status as of the post-review fix cycle: still outstanding.** The manual
TTY acceptance clauses of W4.T1 (focused input against a real claude),
W5.T2 (live enter-resume), and W6.T1 (`afk new` landing in dash+pane),
and the plan's own manual Verification Strategy step, have never been
executed on a real terminal by anyone. The §2 checklist remains an open
item for a human operator and is a hard gate before any productionize
decision.

**Update (fix-cycle-2): the gap was hiding a real defect.** The independent
re-review empirically reproduced a rendering blocker that sat exactly
behind this unexecuted checklist: the emulator/PTY were sized 2 columns
wider than the pane box's text-wrap width (lipgloss v1's `Width()` includes
padding), so every full-width child row wrapped and `MaxHeight` truncated
the screen — the live pane would have garbled at §2 step 2. This has been
fixed at code level (`paneContentDims`/`renderPane` now agree on the wrap
width, with a regression test that pushes full-content-width rows through
the real style/geometry path), and §2 step 9's threshold was re-derived
from the corrected constant. The fix is verified only against the
emulator's own output; the human §2 run on a real terminal remains the
gate.

### 3.10 Smaller items recorded during execution

- **Dry-run contract change**: `afk new --dry-run` no longer prints the
  `inside tmux: %v` header line (the field is deleted). Anything scraping
  that output must adapt.
- **go.mod hygiene**: `github.com/mattn/go-isatty` became a direct import
  (session's IsTTY default) while go.mod still carried a stale `// indirect`
  marker — no wave's file boundary included go.mod after the import landed.
  RESOLVED: fix-cycle-1's sanctioned `go mod tidy` promoted it into the
  direct require block; the committed go.mod carries no stale marker.
- **bubbles/textinput was unusable within file boundaries**: it transitively
  requires `atotto/clipboard`, absent from go.sum; the form uses a small
  hand-rolled rune-buffer field instead. If richer input is wanted, add the
  dependency and swap.
- **Reserving `ctrl+\` costs the child that chord** (SIGQUIT on a real tty)
  — some single escape chord had to be sacrificed; this was the least-used
  candidate.
- **No in-flight guard on the creation form's submit**: a second Enter
  pressed while an async create is still running fires a duplicate create,
  and Esc followed by a late success message still opens the pane for the
  session created in the background. Spike-acceptable (the window is one
  `git worktree add` wide); productionization should add a submitting flag
  that disables re-submit and makes a cancelled form drop (not act on) a
  late `sessionCreatedMsg`. The same in-flight race had a second, worse
  flavor at the pane-open layer: two opens already in flight could both
  succeed, and the second `paneOpenedMsg` then overwrote `m.pane`, leaking
  the first pane's live conductor unclosed. FIXED in fix-cycle-2: the
  `paneOpenedMsg` handler now guards on an active pane — a late arrival is
  `Close()`d (never leaked) and surfaced as a blocked status, unit-tested
  with two in-flight opens. The form-submit flavor above remains open.
- **The creation form is modal — Esc is its only exit** (documented and
  tested in fix-cycle-2): while the form is open, `q` types into the name
  field (it is a legitimate session-name character) and ctrl+c is a no-op —
  neither quits the dashboard nor closes the form; the form's help line
  advertises "esc cancel". Standard modal-capture behavior, recorded here
  because it was previously implicit.
- **Dependency surface**: x/vt pulled bumps across the charmbracelet/x
  family plus a new pseudo-versioned `charmbracelet/ultraviolet` transitive —
  two unversioned modules now sit in the graph, not one.

## 4. Recommendation

**Proceed toward productionizing the embedded pane, conditionally — gated on
the §2 checklist, with the hardening list below. Do not pivot to
suspend-and-exec; keep tmux jump as the legacy escape hatch it already is.**

The case, from evidence rather than vibes:

**What the spike proved works.** The hard, novel problem — bridging a PTY
child through an in-process emulator into Bubble Tea — was solved cleanly and
is race-detector-clean under repetition. The two failure modes everyone
feared materialized and were both contained: the bidirectional-pump deadlock
(child terminal queries wedging the output pump) was hit exactly as predicted
and solved with the response pump + a tested DSR round-trip, and the
"experimental emulator crashes the dashboard" risk was reduced to
"experimental emulator ends the pane with an error" by the recover()
containment, tested in both pump directions. Redraw coalescing means a
streaming conductor costs ~one render per scheduler round-trip and is fully
decoupled from the discovery ticker (§3.4) — the performance interaction the
plan flagged turned out benign. The UX goals landed: no nested-dashboard
recursion (`n` is a real form), no tmux dependency for new sessions,
enter-resume reopens the actual conversation by ID, and the whole surface is
hermetically unit-tested through seams, matching the repo's conventions.

**What it costs.** Two structural liabilities, honestly weighed:

1. *Process lifetime* (§3.2): tmux gives detach and survival; in-process
   hosting gives neither. But the spike's own resume path blunts this more
   than expected — the conversation is durable in claude's session store and
   one keypress away (`enter` → `--resume <id>`), so a dashboard death loses
   in-flight work, not the session. For afk's actual usage (interactive,
   attended conductor runs) that is an acceptable trade; for long unattended
   runs it is not, and suspend-and-exec would not fix it either — only
   tmux-style detach does. Verdict: accept the trade-off explicitly and
   document it, rather than pivot.
2. *Emulator maturity* (§3.3): one spike surfaced one real data race and a
   class of silently-dropped keys in x/vt, and both x/vt and ultraviolet are
   pseudo-versioned. This is the strongest argument against proceeding — it
   is a maintenance bet on an unversioned dependency. The mitigations
   (workarounds already encapsulated in termpane, deliberate pinning,
   containment) make it a bounded bet: every x/vt defect found so far
   degraded to a pane error or a dropped key, never a dashboard crash.

**Why not suspend-and-exec.** The pivot alternative (tea's `ExecProcess`:
suspend the dashboard, hand the terminal to claude, resume on exit) avoids
the emulator entirely but gives up the spike's actual wins: no dashboard
chrome during a run (the whole point), no session list beside a live
conductor, and it reintroduces the modal launch UX the in-TUI form was built
to kill. The spike found no evidence-based reason to pay that price: the
emulator risks it would eliminate were all contained in practice.

**Conditions before calling it production.** (a) Execute the §2 checklist on
a real TTY — §3.9 means the interactive happy path is currently
unverified, and a garbled-rendering result at step 2 or an unusable step 9
(narrow terminal) is the one outcome that should reopen the pivot question;
(b) SIGTERM + grace before SIGKILL (§3.2); (c) a liveness lockfile for the
concurrent-resume hole (§3.5); (d) PTY tee to a per-session log (§3.7);
(e) pin x/vt/ultraviolet and treat bumps as migrations, upstreaming the
Close/Read race; (f) decide the multi-pane question deliberately (§3.6) —
the one-pane rule is fine to ship first, but the layout and quit semantics
should be designed with N>1 in mind before the model calcifies.

Net: the architecture earned its keep in the spike — the risks that remain
are known, bounded, and cheaper to harden than the UX is to give up.
