# Pane preview latency: measured budget and unpursued fixes

Status: **deferred, not blocked.** The left pane's auto-preview (see
`README.md`'s keybindings and `internal/ui/messages.go`'s `previewDelay`) is
good enough to use. This records where the remaining latency actually goes and
what could be done about it, so a future pass starts from measurement instead of
re-deriving it.

Nothing here is a committed plan. Each section says what it would cost and what
invariant it puts at risk, because two of the three ideas touch
`internal/host` — the package the preview feature deliberately never modified.

## The budget

What separates a cursor keypress from readable content in the right pane:

| Step | Cost | Where |
|---|---|---|
| Cursor settle (`previewDelay`) | **300ms** | `internal/ui/messages.go` |
| Unix socket dial (`host.Dial`) | ~5ms | `internal/ui/hostpane.go` → `internal/host/client.go` |
| Host redraw nudge (`resizeNudgeDelay`) | 25ms | `internal/host/host.go` |
| Child (`claude`) repainting after SIGWINCH | ~100-300ms, not controlled here | the child |

`previewDelay` started at 750ms and came down to 300ms once the split above was
measured — the settle was roughly 80% of the wait. That was the cheap win and it
is already taken.

**The 2s discovery ticker is not in this path.** It was the first suspect and it
is wrong: `attachPaneCmd` dials as soon as the preview timer fires, and pane
content arrives on the pane's own `paneRedrawMsg` subscription
(`paneEventCmd`), independent of `loadCmd`. The ticker only refreshes list
metadata — phase, progress, notepad tails, diff stats. Slowing or speeding it
changes nothing about how fast a pane fills.

## Why a fresh pane starts empty

This is the root of the remaining delay, and it is a deliberate design
property, not an oversight.

`pumpPTY` (`internal/host/host.go`) reads the child's PTY unconditionally and
**discards** output when no client is attached — no buffering. The doc comment
states the reason: buffering would be unbounded memory, and *not* reading would
let a full PTY buffer block the child. So a newly connected client has nothing
to receive until the child writes again.

`resize` bridges that gap by nudging the terminal size (change the column count,
sleep `resizeNudgeDelay` = 25ms, put it back) to provoke SIGWINCH and make the
child repaint. That nudge is why attaching shows anything at all. The remaining
perceived delay is the child deciding to repaint — outside this codebase.

## Unpursued fixes

### 1. Bounded scrollback replay in the host

**Idea.** Keep a bounded ring buffer of recent PTY output in the host and send
it to a client on connect, so a pane renders immediately instead of waiting for
the child's repaint.

**Why it is attractive.** It removes the largest remaining term (the child's
repaint) rather than shaving a constant. It would also make reattach feel
instant everywhere, not just in preview — `afk attach` pays the same cost.

**What it risks.** Three things, in order of severity:

- **The wedged-client invariant.** `internal/host`'s whole design is that a slow
  or wedged client must never stall the child: bounded per-client queues
  (`clientQueueDepth` = 256), write timeouts, and a slow client gets dropped
  rather than pinning the PTY pump. A replay buffer adds a second place where
  client-facing state accumulates. It must be filled from the existing pump
  without adding a synchronization point that a slow reader can block on.
- **Unbounded memory** is the reason discarding exists. A ring buffer answers
  that, but the size becomes a real decision: too small and it replays a
  fragment of a screen (worse than nothing — a half-painted TUI reads as
  corruption); too large and a long-running session holds meaningful memory per
  host.
- **Correctness of replayed bytes.** The child's output is a terminal byte
  stream with cursor positioning and mode switches, not lines. Replaying an
  arbitrary suffix can start mid-escape-sequence. The consumer is a real
  emulator (`internal/termpane`, `charmbracelet/x/vt`) so it will not crash, but
  it can render garbage. Replaying from a screen-clear boundary, or having the
  host keep an emulator of its own and ship a rendered snapshot, are the two
  honest options — the second is much more work and duplicates state.

**Scope.** `internal/host` (protocol + host runtime), `internal/ui/hostpane.go`,
possibly a new frame type. This is the one idea here that is a real feature, not
a tuning change.

### 2. Warm the pane before the settle expires

**Idea.** Dial on the cursor move and hold the connection unrendered, so the
settle overlaps the dial instead of preceding it.

**Why it is unattractive.** It defeats the reason the debounce exists.
Connecting **steals** the session — a host serves exactly one client, and the
displaced client gets a `Stolen` frame. Dialing before the settle means
scrolling past a row steals it, which is precisely what `previewDelay` prevents.
The dial is also only ~5ms; there is almost nothing to overlap. Recorded so
nobody re-proposes it: the cost it saves is the smallest term in the budget and
the safety it spends is the largest.

### 3. Lower the settle further

**Idea.** Drop `previewDelay` below 300ms.

**What it buys and costs.** Each millisecond off the settle comes off the
perceived latency directly, and held-key scrolling stays safe at any value —
key repeats arrive every ~30-90ms and re-arm the timer, so only the row the
cursor *rests* on ever dials. The exposure is deliberate slow stepping: a user
moving one row at a time, slower than the settle, connects to (and steals) each
row in turn. At 300ms that takes a visibly unhurried pace; below ~150ms it
becomes ordinary navigation speed.

**Cheap, reversible, no new invariants.** One constant in
`internal/ui/messages.go`. No test hardcodes the value — the suite drives
`previewTimerMsg` directly rather than waiting on a real clock — so changing it
is a one-line edit plus a rebuild.

## What is deliberately not a performance problem

- **`loadCmd`'s per-session work** (`ReadTail` ×2, diff stats) already avoids the
  expensive path for non-selected rows: `computeDiffStats` runs the full
  `git diff` only for the focused session and a cheap `--numstat` aggregate for
  every other one. That dual-cost pattern is in `internal/ui/diff.go` and its doc
  comment says the non-focused path never calls `fullDiff` — "that is the entire
  point of the pattern".
- **Discovery being uncached** is a correctness requirement, not an oversight:
  the writers mutate these files continuously and a cache would show the wrong
  phase. See `CLAUDE.md` on `internal/discover`.
- **Emulator scrollback** is already set to 0 in `internal/termpane`
  ("don't pay for scrollback") — the pane renders the live screen only. Raising
  it would cost memory without helping the connect path, since the emulator is
  created fresh per pane and has nothing to scroll back through.

## If you pick this up

Measure before changing anything — the numbers in the budget table were taken
against real sessions on this machine and the child's repaint term is the one
most likely to differ elsewhere. Start by confirming which term dominates for
you. If it is still the settle, idea 3 is a one-line change. If it is the
child's repaint, idea 1 is the only lever, and its first question is whether a
rendered snapshot or a raw byte replay is the honest shape.
