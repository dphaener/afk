# Pane preview races: known timing-window bugs

Status: **known and accepted, not fixed.** The left-pane auto-preview
(`previewDelay` in `internal/ui/messages.go`) tracks outstanding pane-open dials
in a single slot, and that model cannot describe more than one dial in flight.
Three races follow from it. All of them need a keystroke inside the ~300ms
settle window, and all recover on the next deliberate keypress, so they were
judged not worth blocking on.

This file exists so that hitting one of these later is a lookup rather than a
re-investigation. Each entry has a repro verified against the code, the
mechanism, and what a fix would take.

## The shared root cause

`Model.attachInFlight` is one `inFlightOpen{name, preview, focusOnArrival}`
record. A second open simply overwrites it. Nothing at arrival time can ask
"is this arrival still wanted?", because the only record of what was wanted has
already been replaced.

`Model.previewGen` is the counter that makes superseded *timers* inert
(`schedulePreview` bumps it on every path, `openPaneFor` bumps it as its first
statement). It works, and it is why most of the obvious races do not occur. What
it does not cover is the window between a dial being **dispatched** and its
`paneAttachedMsg` **arriving** — the generation has already advanced by then, so
the arrival is not checked against it.

**A generalization is the honest fix for all three**: stamp an epoch on the
`paneOpenRequest`, carry it to `paneAttachedMsg`, and have the install path drop
an arrival whose epoch is stale. One question, asked once, in one place.

## B1 — a late preview replaces the pane `enter` just opened

**Verified.** Confirmed with faithful message inputs (every `previewTimerMsg`
carrying the generation it was actually armed with).

**Repro** (2+ running sessions):
1. Cursor on `a`. Move down then back up, so the last armed timer targets `a`.
2. Let that timer fire — `a`'s dial is now outstanding but has not arrived.
3. **Within that dial**, move to `b` and press `enter`.
4. `b` arrives first and installs: `pane="b" focused=true` — correct so far.
5. `a`'s stale arrival lands: **`pane="a" focused=false`**.

The pane you explicitly asked for is replaced by one you merely scrolled past,
and your keyboard is no longer in the session.

**Mechanism.** Traced live: `attachInFlight` reads
`{name:a preview:true focusOnArrival:false}` while `a`'s dial is outstanding,
then `enter` on `b` overwrites it with
`{name:b preview:false focusOnArrival:true}`. When `a`'s message arrives, the
install path has no record that `a` was superseded, so it installs.

**Recovery:** one `enter` on the row you wanted — verified to give
`pane="b" focused=true`.

**Fix:** the epoch stamp above. Not a one-liner: it needs the field threaded and
the install path gated on it.

## B3 — a closed pane reopens ~300ms later

**Verified**, but note the correction below — this is a timing-window bug, not
the every-time failure it was first reported as.

**Repro** (3+ running sessions):
1. Press `j`, wait for the pane to open (say on `b`).
2. Press `j` again (now on `c`). This arms a real timer.
3. **Within 300ms**, press `ctrl+\` to close the pane — which is what you do to
   reach `c` (clean), since `c` is refused while a pane is open.
4. The pane closes correctly, but `previewGen` is unchanged, so the armed timer
   is still valid.
5. ~300ms later it fires and installs a pane on `c`.
6. Press `c` → refused again, because a pane is open.

**Mechanism.** `closePane` does not bump `previewGen`. Every other
pane-lifecycle transition does.

**Correction to an earlier report of this bug.** It was initially described as
needing only "one arrow + `ctrl+\`", failing every time. That was wrong, and the
error is worth recording because it is easy to repeat: the probe delivered a
`previewTimerMsg` carrying the *current* generation, which no keypress can
produce. When a timer fires, `openPaneFor` bumps the counter — so a real
in-flight timer is always stamped with an older generation than the Model holds.
A hand-built message using the current generation is a message the program cannot
emit. **When probing this state machine, always deliver a timer with the
generation captured at arm time.**

**Fix:** one line — bump `previewGen` in `closePane` — plus a regression test.
The cheapest item here, and it does not need the epoch rework.

## B2 — `enter` joining a failed preview dial is silent (UNVERIFIED)

**Reported by review; not reproduced or disproved here.** Recorded as claimed.

**Claim:** when `enter` joins an already-outstanding preview dial for the same
session (`attachSelected` has a join path rather than dialing twice) and that
dial then *fails*, nothing observable happens — no pane, no focus, no error, and
the status stays at `"opening b…"` indefinitely. The asserted cause is
`applyPaneAttached`'s `if msg.preview` early return firing before the computed
`joinedFocus` is used.

**If real, the invariant it breaks** is that an `enter` must always produce an
observable outcome. A preview failing silently is deliberate (a bare cursor move
must not print errors); an `enter` failing silently is not.

**Before fixing, verify.** The preview-failure silence was added on purpose, so
the question is specifically whether the *joined* case is caught by it.

## B4 — preview dead spot (UNVERIFIED, did not reproduce)

**Reported by review; a probe of the described path did NOT reproduce it.**

**Claim:** `schedulePreview` arms a timer while another preview's dial is
outstanding, but `applyPreviewTimer` drops on *any* non-empty `attachInFlight`,
so the timer for the row the cursor settled on is discarded and never re-armed —
leaving the cursor on `c`, the pane on `b`, and nothing pending.

**What the probe found:** dial for `b` outstanding → cursor to `c` → `b` lands →
`c`'s timer fires → `c` previewed successfully. So either the finding needs a
different interleaving, or the two guards do not actually contradict.

**If picking this up:** work out the real state machine before writing code.
`schedulePreview` returning nil while a dial is outstanding and
`applyPreviewTimer` dropping on a non-empty record may be consistent rather than
contradictory. Do not fix what you cannot reproduce.

## Related: an invisible pane can block `c`

A preview whose dial lands *after* the cursor moved on installs a pane that
`shouldRenderPane()` (`internal/ui/views.go`) will not draw — the cursor is
elsewhere and the pane is unfocused. Meanwhile `c` is refused because
`m.pane != nil`, and the refusal tells the user to press `ctrl+\` to close a
pane that is not on screen.

The epoch fix for B1 would close this too: an arrival whose row is no longer
selected is superseded, so it is never installed. The alternative — make the `c`
refusal test what is actually rendered rather than `m.pane != nil` — is smaller
but leaves an unrendered pane holding a host client slot.

## Invariants any fix must preserve

Carried from the plan and from `internal/host`'s design; breaking one of these is
worse than any race above.

1. An explicit `enter` open wins over any preview, in either arrival order.
2. An `enter` always produces an observable outcome; never a frozen status.
3. `c` never runs while a pane is open on the session being cleaned, and a pane
   is never installed on a session whose clean is in flight — `openPaneFor` gates
   dispatch, `applyPaneAttached` re-checks at arrival. **Both** gates stay.
4. An arrival you decline to install must still `Close()` its pane, or the host
   keeps a client with no handle to release it. This is why the same-name-surplus
   exit closes the surplus connection.
5. A wedged client never stalls the child. Do not modify `internal/host`.
6. Cursor movement never spawns a process — a stopped session is not restarted by
   scrolling onto it.

## Note on the doc comments (resolved)

The comments in `applyPreviewTimer` and on
`TestEnterDuringAPreviewDialJoinsItInsteadOfDialingAgain` asserted an "at most one
attach in flight" invariant. That was false — two explicit opens for different
sessions both dispatch and the second overwrites the record, which is precisely how
B1 happens — and `CLAUDE.md` forbids comments asserting a mechanism that does not
hold. Both now say what actually holds (at most one dial *per session*, gated for
previews at fire time) and point here for B1. `Model.attachInFlight`'s own comment
never made the claim.

`TestHelpLineAdvertisesPreviewWithoutGrowing`'s `widthBefore` was 92 against a
measured 90, banking two columns of unnoticed slack. It is now 90, with the reason
in the test's comment.
