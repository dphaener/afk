# afk Session Status Schema (`status.json`)

This document is the **contract** for afk session identity and live session
state. It is a design document: nothing here is implemented by this file.
Consumers implement against it without inventing anything:

| Consumer | Role |
|---|---|
| `/afk-go` conductor (W3.T1, W3.T2) | creates (standalone) or adopts (launched) the file; owns the lifecycle fields |
| afk plugin event handlers (W3.T3) | set/clear `awaiting_input` |
| `afk new` (W4.T3) — not yet ported | seeds the file when launching a session |
| `afk` state parsers (W5.T1) | read and validate the file for the dashboard |

This schema is also the future GUI API contract. It stays **minimal**: adding
a field requires a named consumer that demonstrably needs it; renaming or
removing a field is a breaking change.

---

## 1. Identity model — one name, one directory

A afk session has exactly **one name**, and that name is the **plan slug**.
Everything about the session derives from it:

| Artifact | Path |
|---|---|
| Status file | `.afk/{name}/status.json` |
| Plan file | `.afk/plans/{name}.md` |
| Execution notepads | `.afk/{name}/learnings.md`, `decisions.md`, `issues.md` |
| Review verdict | `.afk/{name}/review-verdict.md` |
| Branch / worktree (afk-new path) | named `{name}` by `afk new` |

`status.json` is the only file in `.afk/{name}/` with more than one
sanctioned writer; all concurrency rules in this document apply to it alone.

### The two paths to a session

**Launched path (`afk new`)**: the user picks the session name at
`afk new`. That name IS the plan slug — no derivation happens later.
`afk new` validates it as strict kebab-case (`^[a-z0-9][a-z0-9-]*$`),
creates `.afk/{name}/`, **seeds `status.json`** (see §4), and launches
`opencode` running `/afk-go --session {name}`. The conductor, seeing `--session`,
**overrides the planner's name-derivation** with `{name}` and adopts the
seeded file.

**Standalone path (`/afk-go` with no `--session`)**: the conductor derives
the slug itself using the normal naming rules in
`plugins/afk/skills/planner/plan-format.md` (lowercase, kebab-case, ticket
number stays at the front), then **creates the identical layout itself**:
`.afk/{name}/` and `status.json` with the same nine fields.

### Convergence guarantee

Both paths converge on a byte-compatible layout. After the conductor's
startup write, the only observable differences between the two paths are:

- `tmux_window` may be non-empty on the launched path (only `afk new`
  captures it); it is always `""` on the standalone path.
- On the launched path there is a brief startup window during which
  `session_id` is `""` (see §7, startup fallback).

Self-heal rule: if the conductor receives `--session {name}` but
`.afk/{name}/status.json` does not exist (seed lost, manual launch), it
creates the file itself exactly as in the standalone path — the launched path
degrades to standalone, never to a broken state. If the file exists with a
**terminal** phase (`done`/`failed`), that is not a resume: the conductor
surfaces the conflict to the user rather than silently overwriting.

---

## 2. File format

- UTF-8, a single JSON object, no comments, no trailing garbage.
- **All nine keys are always present** — no optional keys. Unset strings are
  `""`. This keeps the W5.T1 parser and any GUI free of key-existence logic.
- No keys beyond the nine below. Parsers MUST reject unknown keys as
  corruption rather than ignore them silently (the file is machine-owned;
  an unknown key means a writer violated this contract).

### Schema

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | OpenCode session ID (`ses_…`) of the conducting session, from the `afk_session` tool. `""` until the conductor persists it at startup. |
| `plan_name` | string | The session name / plan slug. MUST equal the containing directory name (`.afk/{plan_name}/status.json`). |
| `phase` | string | One of `planning` \| `executing` \| `reviewing` \| `fixing` \| `done` \| `failed`. `done` and `failed` are terminal. |
| `branch` | string | The working branch for this session. |
| `base_branch` | string | The branch this work stacks on / merges back into. |
| `loop_count` | integer ≥ 0 | Number of fix cycles entered (see §8). |
| `awaiting_input` | boolean | The session is blocked on the human (see §5). |
| `tmux_window` | string | tmux window ID (e.g. `@12`) captured by `afk new` at launch; `""` when tmux was absent or the session is standalone. |
| `updated_at` | string | RFC 3339 UTC timestamp of the last write, e.g. `"2026-07-09T21:14:03Z"`. Refreshed by **every** writer on **every** write. |

### Example

```json
{
  "session_id": "ses_3f2a9c1b7ffeKq8Zr4TnW1xY0d",
  "plan_name": "turt-1234-add-billing-webhook",
  "phase": "executing",
  "branch": "turt-1234-add-billing-webhook",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "@12",
  "updated_at": "2026-07-09T21:14:03Z"
}
```

### Invariants

1. `plan_name` == the containing directory name. Readers treat a mismatch as
   corruption/staleness.
2. `phase != "planning"` ⇒ `session_id != ""`. The conductor MUST persist
   `session_id` before leaving `planning` (it does so at startup, which is
   earlier). Parsers may treat an empty `session_id` outside `planning` as
   invalid — and the event handlers will never match such a file (§7), so
   violating this invariant silently kills all `awaiting_input` updates for
   the session.
3. `loop_count` never decreases within a session.

---

## 3. Who creates the file

Exactly one process creates any given `status.json`:

- **Launched path**: `afk new` creates and seeds it, *before* the OpenCode
  session exists.
- **Standalone path**: the conductor creates it at startup.

No other component ever creates it. The event handlers never create it (a
missing file is simply "no match" — do nothing). The dashboard and parsers never write
it at all.

---

## 4. Field ownership (writer matrix)

Single-writer discipline for every field except `awaiting_input`:

| Field | Seed value | Sanctioned writer(s) after seed |
|---|---|---|
| `session_id` | `""` | Conductor only, written **once** at startup, before any other conductor status write. The event handlers NEVER write it (§7). |
| `plan_name` | `{name}` | Nobody — immutable after seed. |
| `phase` | `"planning"` | Conductor only. |
| `branch` | seeder's value | Nobody — set at seed time by whichever process creates the file (`afk new` from its own branch creation; the conductor from the current git branch on the standalone path). |
| `base_branch` | seeder's value | Nobody — set at seed time (`afk new` from its base picker; the conductor from its best trunk determination on the standalone path — per the recorded issue, `git config spice.trunk` presence MUST NOT be the detection heuristic). |
| `loop_count` | `0` | Conductor only. |
| `awaiting_input` | `false` | **Multi-writer, last-writer-wins**: the afk plugin's event handlers (both directions, §5 S1/C1 and the permission-reply clear), conductor (both directions). Complete state machine in §5. |
| `tmux_window` | captured ID or `""` | Nobody — only `afk new` ever writes a non-empty value, at seed time. |
| `updated_at` | seed time | Every writer, on every write. |

Every writer touches **only** its sanctioned fields plus `updated_at`, and
carries every other field through unchanged via the merge protocol in §6.
In particular (mirrors W3.T3's constraint): the event handlers write nothing
but `awaiting_input` and `updated_at`.

The conductor obtains its own session ID by calling the `afk_session` tool
and persists it once, at startup, before any other status write.

---

## 5. `awaiting_input` — complete state machine

**Semantic**: `awaiting_input` means *"this session believes it is blocked on
the human."* Every sanctioned writer asserts the **current truth** as it
observes it; the field is last-writer-wins by design. No writer ever refuses
to write because of the current value — same-value writes are permitted
idempotent no-ops (they still refresh `updated_at`).

The conductor's standing rule, from which its transitions below derive:
**every conductor status write asserts its work state** — `false` whenever it
is actively working, `true` exactly when it is deliberately stopping to wait
for the user. This makes ordinary conductor writes (phase transitions,
loop_count increments) self-healing against any stale `true`.

### SET transitions (→ `true`)

| # | Writer | Trigger | Grounding |
|---|---|---|---|
| S1 | Event handlers | An event that passes the targeting rule (§7) AND is either **(S1a)** `permission.asked` in any matched non-terminal phase — the handler also creates the sentinel `.afk/{name}/.awaiting-permission`, or **(S1b)** `form.created` (a `question` tool form is shown) while `phase == planning`. A shown question in any other phase → the handler **writes nothing**. | A permission ask pauses the very tool call the conductor is trying to make, so the conductor cannot self-report it. During planning the planner's questions are genuine blocks the conductor does not flag itself. Outside planning, deliberate stops are owned by the conductor (S2–S4, §9). |
| S2 | Conductor | REJECT stop: review verdict is REJECT and the conductor stops to present findings (W3.T1 baseline behavior, before the fix loop exists). | Deliberate stop the conductor owns (§9). |
| S3 | Conductor | Fix-loop cap stop: verdict is REJECT and `loop_count` is already `2` — the conductor does not enter another fix cycle; it sets `awaiting_input: true` and stops (W3.T2). | Deliberate stop the conductor owns (§9). |
| S4 | Conductor | Any other deliberate wait for the user — e.g. immediately before surfacing a relayed `NEEDS_INPUT` question via the `question` tool, or before halting on a condition it wants the user to resolve. Set in a status write issued *before* the wait begins. | A shown question outside planning does not fire S1 (§9 gives deliberate stops to the conductor), so the conductor is the **only** writer that knows this is a genuine block. |

Autonomous mode (conductor/prompts/autonomous.md): no S3 stop and no S4 relays.

### CLEAR transitions (→ `false`)

| # | Writer | Trigger | Grounding |
|---|---|---|---|
| C1 | Event handlers | The user submits input: a user prompt admitted to the session (the plugin's `prompt` hook) **or** a `question` form answered (`form.replied`). | The user is present and the session is working again. |
| C2 | Conductor | Resuming work after a relayed `question` is answered: the first status write after receiving the answer asserts `false`. | Belt-and-braces for C1 (covers the plugin not loaded, or a targeting miss). |
| C3 | Conductor | Startup/resume: when `/afk-go` starts or resumes against a non-terminal status file, its startup write asserts `false` (the session is now actively working; the resume itself was a user action). | — |
| C4 | Conductor | Terminal write: the same write that sets `phase` to `done` or `failed` also sets `awaiting_input: false`. A finished session is not waiting on anyone; the event handlers stop matching it anyway (§7). | — |
| C5 | Event handlers | A permission is granted or denied (`permission.replied`): set `false` **only if** the `.afk/{name}/.awaiting-permission` sentinel exists, then delete the sentinel. | The sentinel gate means a permission reply clears only what S1a set — never a conductor's deliberate stop (S2–S4). |

There are no other transitions. `afk new` seeds `false` and never touches
the field again. Parsers and the dashboard never write it.

### Permission-grant staleness window (closed)

Granting or denying a permission fires `permission.replied`, so C5 clears the
flag S1a set as soon as the user answers; no stale `true` waits for the
conductor's next write. Dashboards can still pair `awaiting_input` with
`updated_at` to present age honestly.

---

## 6. Write protocol and concurrency

### Atomic replace with mandatory re-read-merge

**Every writer, no exceptions**, performs writes as follows:

1. **Re-read** `.afk/{name}/status.json` and parse it. This freshly-read
   copy is the merge base — carrying every non-sanctioned field through
   unchanged IS the merge.
2. **Modify only your sanctioned fields** (§4) in that fresh copy, and set
   `updated_at` to now (RFC 3339 UTC).
3. **Write the full object to a unique temp file in the same directory**,
   e.g. `.afk/{name}/.status.json.tmp.{pid}`. Same directory is mandatory:
   `rename(2)` is only atomic within one filesystem.
4. **Rename** the temp file over `status.json`.

Steps 1–4 run back-to-back with **no other work interleaved** — the re-read
must be *immediately* before the rename, not at the start of the writer's
logic. A writer whose decision depends on current state (e.g. the conductor
checking `loop_count` against the cap) makes its decision first, then runs
steps 1–4 applying its change to the *fresh* copy — for `loop_count` the
increment is applied to the freshly re-read value, which is exactly W3.T2's
"re-read before every increment".

### Resolution of the whole-file-replace lost-update hazard

**Decision: re-read-merge immediately before rename is MANDATED for every
writer (W3.T1, W3.T2, W3.T3, W4.T3). The temporal-separation assumption is
explicitly rejected as a safety argument.**

Why rejected: temporal separation ("event handlers only fire while the
conductor is idle, so writes never overlap") is *usually* true but has no
enforcement, and the failure it permits is a correctness kill-shot, not
cosmetic — an event handler holding a stale copy read before the conductor's `loop_count` increment
would, on rename, silently revert the increment and break the W3.T2 fix-loop
cap (unbounded review loops). Same shape for `phase` reversion. Against
that risk, re-read-merge costs a few lines in each writer. Temporal
separation remains a nice observed property (defense in depth) — it is just
never *relied on*.

Residual window: two writers can both re-read before either renames; the
second rename then drops the first writer's field. With the merge mandated,
that window is the microseconds between one writer's step 1 and step 4 —
during which the other party (event handlers fire only when the session is waiting;
the conductor writes only while working) is almost never writing. If a
collision ever lands in that window, the affected fields self-heal:
`awaiting_input` is last-writer-wins by design and is re-asserted on every
subsequent event/write, and the conductor re-reads `loop_count`/`phase`
before every decision. This residual risk is accepted.

### Rules for readers (W5.T1, dashboard, event handlers before deciding to write)

- `rename(2)` is atomic: a reader sees either the old or the new complete
  file, never a torn one, on POSIX same-filesystem renames. W5.T1's
  retry-once-then-stale behavior covers non-conforming writers, exotic
  filesystems, and files truncated by a crashed pre-rename writer.
- Ignore `.status.json.tmp.*` files entirely. A crashed writer may orphan
  one; orphans are inert (unique per-PID names prevent collisions) and
  `afk clean` may remove them.
- A missing or unparseable `status.json` is "no session" to the event
  handlers (do nothing, §7) and "stale" to parsers. Only the conductor may
  repair or recreate a corrupt file (from its own known state); the event
  handlers never do.

---

## 7. Event-handler targeting rule

The afk plugin's event handlers first resolve the event's session to its
**root session**: follow `parentID` up until there is none, so a permission
asked inside a sub-agent's child session counts for the conductor. The root
session's `location.directory` plays the role of `cwd`. A handler then
decides whether to act as follows:

```
root       = root session of the event's session
cwd        = root.location.directory
candidates = parseable files matching {cwd}/.afk/*/status.json
primary    = [s for s in candidates
              if s.session_id == root.id
              and s.phase not in {"done", "failed"}]
fallback   = [s for s in candidates
              if s.session_id == "" and s.phase == "planning"]

if   len(primary) == 1:                      act on primary[0]
elif len(primary) == 0 and len(fallback) == 1: act on fallback[0]
else:                                        do nothing
```

- **Primary match**: `session_id` equality AND non-terminal phase. Unique by
  construction (session IDs).
- **Startup fallback** (MANDATORY — dropping it silently kills
  planning-phase `awaiting_input` updates for afk-new-launched sessions): a
  afk-new-seeded file has `session_id: ""` until the conductor's startup
  write lands, so an empty-`session_id` file in phase `planning` under the
  root session's directory matches. **This claim is non-persistent: the event
  handlers never write `session_id`** — the conductor persists it at startup,
  which closes the fallback window within seconds.
- **Ambiguity acts on nothing**: if more than one fallback candidate matches
  (e.g. an abandoned seeded file plus a fresh one in the same directory),
  the handler does nothing. In the designed flow `afk new` gives each
  session its own worktree, so multiple seeded files in one directory only
  arise from abandoned sessions — exactly the stale files the handlers must
  not touch. The cost is a few silent seconds until the conductor persists
  `session_id` and the primary rule takes over.
- **Everything else is silence**: no candidates, only terminal-phase files,
  session_id mismatch, unparseable JSON, no `.afk/` at all — the handler
  makes **zero writes**. Stale status files and non-afk sessions produce
  nothing, ever.

When a handler acts (i.e. §7 returns a match): `permission.asked` applies
S1a (and creates `.awaiting-permission`); `form.created` applies S1b only in
`planning`; `permission.replied` applies C5 (sentinel-gated); an admitted
user prompt or `form.replied` applies C1. Every handler write uses the §6
write protocol, touches only `awaiting_input` + `updated_at`, and never
creates the file.

---

## 8. Phase lifecycle

`phase` is owned by the conductor and gated **exclusively on disk
artifacts** (never conversational memory):

| Phase | Meaning | Exit gate (disk artifact) → next phase |
|---|---|---|
| `planning` | Interview + plan generation | `.afk/plans/{name}.md` exists → `executing` |
| `executing` | Wave-by-wave delegation | Every plan task checkbox is `[x]` or `[!]` → `reviewing` |
| `reviewing` | Fresh reviewer subagent running | `.afk/{name}/review-verdict.md` written → verdict handling: APPROVE → `done`; REJECT → `fixing` (W3.T2, if under cap) or cap/REJECT stop (S3/S2, phase stays `reviewing`) |
| `fixing` | Fix-delegation cycle (W3.T2) | Fixes delegated, fresh re-review spawned → `reviewing` (each re-review overwrites `review-verdict.md`) |
| `done` | APPROVE received | Terminal |
| `failed` | Irrecoverable error | Terminal |

`loop_count` semantics: number of fix cycles **entered**. Starts at `0`;
the conductor increments it (re-read-increment, §6) each time it transitions
`reviewing` → `fixing`. On a REJECT verdict when `loop_count` is already
`2`, the conductor does not enter `fixing`: it stops, sets
`awaiting_input: true` (S3), and presents the remaining findings; `phase`
remains `reviewing`.

Resume: `/afk-go` invoked where the matched status file shows a
non-terminal phase resumes **from that phase** using disk state (plan
checkboxes, `loop_count`, verdict file) — it never restarts the interview.
Terminal files are never resumed and never matched by the event handlers; removing them
is `afk clean`'s business.

---

## 9. Ownership of the `awaiting_input` SET

Every `awaiting_input` SET has exactly one owner. The split follows a single
principle: **deliberate stops are flagged by the conductor (it alone knows
when it has blocked); the event handlers flag only what the conductor cannot
see** — permission asks in any phase and questions shown during planning.

| Situation | Phase | Owner | Writes `true`? |
|---|---|---|---|
| `permission.asked` | any non-terminal | Event handlers (S1a) | yes |
| `question` form shown (`form.created`) | `planning` | Event handlers (S1b) | yes |
| `question` form shown (`form.created`) | `executing` / `reviewing` / `fixing` | — (handler does nothing) | no |
| REJECT stop | `reviewing` | Conductor (S2) | yes |
| Fix-loop cap stop | `reviewing` | Conductor (S3) | yes |
| NEEDS_INPUT relay before the `question` tool | `executing` / `reviewing` / `fixing` | Conductor (S4) | yes |
| any event | `done` / `failed` | — | unreachable — terminal phases never match (§7) |

**Rationale:**

- **Permission asks are the handlers' job, every phase**: a permission ask
  pauses the very tool call the conductor is trying to make, so the conductor
  cannot self-report it.
- **Questions shown in `planning` are the handlers' job**: the planner asks
  through the `question` tool during the interview, and the conductor does not
  flag those stops itself.
- **Deliberate stops (S2/S3/S4) belong to the conductor**: it is the only
  party that knows it has stopped *on purpose* to wait for the user, and
  (unlike a permission ask) it is running when it stops, so it writes the
  flag itself. That is why a question shown outside planning needs no
  handler write.

---

## 10. Failed-turn marker (`.turn-failed`)

The port has no hooks, so a session run by the port never writes this marker. The writer is the
`StopFailure` hook in `plugins/afk` (see PROVENANCE.md, lag note).

- **Path/content**: `.afk/{plan_name}/.turn-failed`, one line `{"at":"<RFC 3339 UTC>","reason":"<the CLI's error word>"}`.
- **Writer**: `StopFailure` hook (`on-stop-failure.sh`), for the reasons worth retrying; never for `rate_limit`, which writes only `.rate-limited`. Via temp file and `mv -f`, on a §7 match. Never touches `status.json`.
- **Clearer**: `UserPromptSubmit` and a matched `Stop` do `rm -f`.
- **Reader**: the daemon, which types the session's resume command.
