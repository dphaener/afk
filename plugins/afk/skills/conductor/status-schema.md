# afk Session Status Schema (`status.json`)

This document is the **contract** for afk session identity and live session
state. It is a design document: nothing here is implemented by this file.
Consumers implement against it without inventing anything:

| Consumer | Role |
|---|---|
| `/afk:go` conductor (W3.T1, W3.T2) | creates (standalone) or adopts (launched) the file; owns the lifecycle fields |
| Session hooks (W3.T3) | set/clear `awaiting_input`; fire desktop notifications |
| Stop hook (`on-stop.sh`) | writes the `.background-tasks` sidecar (§10) — not a `status.json` field, but governed by this document |
| `afk new` (W4.T3) | seeds the file when launching a session |
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
`claude "/afk:go --session {name}"`. The conductor, seeing `--session`,
**overrides the planner's name-derivation** with `{name}` and adopts the
seeded file.

**Standalone path (`/afk:go` with no `--session`)**: the conductor derives
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
| `session_id` | string | Session id of the conducting agent, in the form its provider resumes by: the Claude Code session UUID (`claude --resume <id>`) or the Codex thread id (`codex resume <id>`). `""` until the conductor persists it at startup. |
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
  "session_id": "a9313053-40f5-4d3c-8081-1202a1520d34",
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
   invalid — and hooks will never match such a file (§7), so violating this
   invariant silently kills all notifications for the session.
3. `loop_count` never decreases within a session.

---

## 3. Who creates the file

Exactly one process creates any given `status.json`:

- **Launched path**: `afk new` creates and seeds it, *before* the Claude
  session exists.
- **Standalone path**: the conductor creates it at startup.

No other component ever creates it. Hooks never create it (a missing file is
simply "no match" — exit 0 silently). The dashboard and parsers never write
it at all.

---

## 4. Field ownership (writer matrix)

Single-writer discipline for every field except `awaiting_input`:

| Field | Seed value | Sanctioned writer(s) after seed |
|---|---|---|
| `session_id` | `""` | Conductor only, written **once** at startup, before any other conductor status write. Hooks NEVER write it (§7). |
| `plan_name` | `{name}` | Nobody — immutable after seed. |
| `phase` | `"planning"` | Conductor only. |
| `branch` | seeder's value | Nobody — set at seed time by whichever process creates the file (`afk new` from its own branch creation; the conductor from the current git branch on the standalone path). |
| `base_branch` | seeder's value | Nobody — set at seed time (`afk new` from its base picker; the conductor from its best trunk determination on the standalone path — per the recorded issue, `git config spice.trunk` presence MUST NOT be the detection heuristic). |
| `loop_count` | `0` | Conductor only. |
| `awaiting_input` | `false` | **Multi-writer, last-writer-wins**: Notification hook (sets true), UserPromptSubmit hook (sets false), conductor (both directions). Complete state machine in §5. |
| `tmux_window` | captured ID or `""` | Nobody — only `afk new` ever writes a non-empty value, at seed time. |
| `updated_at` | seed time | Every writer, on every write. |

Every writer touches **only** its sanctioned fields plus `updated_at`, and
carries every other field through unchanged via the merge protocol in §6.
In particular (mirrors W3.T3's constraint): hooks write nothing but
`awaiting_input` and `updated_at`.

**How the conductor obtains its own session UUID** is an implementation
concern of W3.T1, not of this schema. Candidate mechanisms observed in the
W1.T1 spike evidence: hook payloads carry `session_id` and a
`transcript_path` whose basename is the session UUID; the newest transcript
file under `~/.claude/projects/{encoded-cwd}/` therefore identifies the
running session. Under Codex: `CODEX_THREAD_ID` (fallbacks: conductor
SKILL.md §0.2). Requirements: persist it once, at startup, before any other
status write. It must be the id the session's own provider resumes by:
`afk` restarts a wedged session with that resume command.

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
| S1 | Notification hook | A `Notification` event that passes the targeting rule (§7) AND is either **(a)** `notification_type == permission_prompt` in any matched non-terminal phase, or **(b)** `notification_type == idle_prompt` while `phase == planning` **and the background-tasks sidecar (§10) reports no pending tasks**. Every other Notification (AskUserQuestion display, `idle_prompt` outside planning, `idle_prompt` in planning with ≥1 pending task, any other type) → the hook **writes nothing and toasts nothing**. | W1.T1 trigger table: permission prompts fire an immediate `permission_prompt`; idle fires `idle_prompt` after a delay. The hook's write and toast are now **coupled** — it acts on exactly the two cases §9 assigns to it, and on the same match it both asserts `true` and toasts. Deliberate stops the conductor can see are owned by the conductor (S2–S4); non-planning `idle_prompt` is dropped because the session self-resumes background work (false-positive class observed in the W6.T1 e2e — see §9), and planning `idle_prompt` with the background-tasks sidecar (§10) reporting ≥1 pending task is dropped as the same class. |
| S2 | Conductor | REJECT stop: review verdict is REJECT and the conductor stops to present findings (W3.T1 baseline behavior, before the fix loop exists). | The conductor also fires the toast itself here (§9) — the hook no longer toasts this moment. |
| S3 | Conductor | Fix-loop cap stop: verdict is REJECT and `loop_count` is already `2` — the conductor does not enter another fix cycle; it sets `awaiting_input: true` and stops (W3.T2). | The conductor also fires the toast itself here (§9). |
| S4 | Conductor | Any other deliberate wait for the user — e.g. immediately before surfacing a relayed `NEEDS_INPUT` question via AskUserQuestion, or before halting on a condition it wants the user to resolve. Set in a status write issued *before* the wait begins. | AskUserQuestion display no longer fires S1 (§9 gives deliberate stops to the conductor), so the conductor is the **only** writer that knows this is a genuine block: it sets `true` here AND fires the desktop toast itself (§9). |

**Conductor SET transitions (S2–S4) are also the conductor's toast points.**
Each is a deliberate stop only the conductor can see, so at each of these
writes the conductor invokes `{plugin-root}/hooks/notify.sh` directly (§9),
best-effort. The Notification hook no longer toasts these moments; there is
exactly one toast source per moment (hook *or* conductor, never both).
Autonomous mode (conductor/prompts/autonomous.md): no S3 stop and no S4 relays.

### CLEAR transitions (→ `false`)

| # | Writer | Trigger | Grounding |
|---|---|---|---|
| C1 | UserPromptSubmit hook | The user submits input: a typed prompt **or** an answer to an AskUserQuestion dialog. One clear path covers both. | W1.T1 trigger table: answering an AskUserQuestion fires **UserPromptSubmit and no Notification** — no special-case clear path is needed for question dialogs. |
| C2 | Conductor | Resuming work after a relayed AskUserQuestion is answered: the first status write after receiving the answer asserts `false`. | Belt-and-braces for C1 (covers hooks not installed, or a targeting miss). |
| C3 | Conductor | Startup/resume: when `/afk:go` starts or resumes against a non-terminal status file, its startup write asserts `false` (the session is now actively working; the resume itself was a user action). | — |
| C4 | Conductor | Terminal write: the same write that sets `phase` to `done` or `failed` also sets `awaiting_input: false`. A finished session is not waiting on anyone; hooks stop matching it anyway (§7). | — |

There are no other transitions. `afk new` seeds `false` and never touches
the field again. Parsers and the dashboard never write it.

### Known, accepted staleness window

The W1.T1 table records what fires when an AskUserQuestion is *answered*, but
no event was observed for *granting a permission prompt*. If the user grants
permission and the conductor resumes without immediately writing status,
`awaiting_input` remains `true` until the conductor's next status write (C2/
C3/C4 or any working-state assertion) or the user's next prompt (C1). This
staleness is display-only: the desktop notification already fired correctly
at wait time, no writer acts incorrectly because of it, and it self-heals at
the next write. Dashboards can pair `awaiting_input` with `updated_at` to
present age honestly. We accept this rather than wiring additional hook
events (W3.T3 wires exactly Notification and UserPromptSubmit).

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

Why rejected: temporal separation ("hooks only fire while the conductor is
idle, so writes never overlap") is *usually* true but has no enforcement, and
the failure it permits is a correctness kill-shot, not cosmetic — a hook
holding a stale copy read before the conductor's `loop_count` increment
would, on rename, silently revert the increment and break the W3.T2 fix-loop
cap (unbounded review loops). Same shape for `phase` reversion. Against
that risk, re-read-merge costs a few lines in each writer. Temporal
separation remains a nice observed property (defense in depth) — it is just
never *relied on*.

Residual window: two writers can both re-read before either renames; the
second rename then drops the first writer's field. With the merge mandated,
that window is the microseconds between one writer's step 1 and step 4 —
during which the other party (hooks fire only when the session is waiting;
the conductor writes only while working) is almost never writing. If a
collision ever lands in that window, the affected fields self-heal:
`awaiting_input` is last-writer-wins by design and is re-asserted on every
subsequent event/write, and the conductor re-reads `loop_count`/`phase`
before every decision. This residual risk is accepted.

### Rules for readers (W5.T1, dashboard, hooks before deciding to write)

- `rename(2)` is atomic: a reader sees either the old or the new complete
  file, never a torn one, on POSIX same-filesystem renames. W5.T1's
  retry-once-then-stale behavior covers non-conforming writers, exotic
  filesystems, and files truncated by a crashed pre-rename writer.
- Ignore `.status.json.tmp.*` files entirely. A crashed writer may orphan
  one; orphans are inert (unique per-PID names prevent collisions) and
  `afk clean` may remove them.
- A missing or unparseable `status.json` is "no session" to hooks (exit 0
  silently, §7) and "stale" to parsers. Only the conductor may repair or
  recreate a corrupt file (from its own known state); hooks never do.

---

## 7. Hook targeting rule

Hooks receive a JSON payload on stdin carrying `session_id`, `cwd`, and (for
Notification) `notification_type` — shape confirmed by the W1.T1 spike
evidence. A hook decides whether to act as follows, using the payload's
`cwd` (not the process working directory):

```
candidates = parseable files matching {cwd}/.afk/*/status.json
primary    = [s for s in candidates
              if s.session_id == payload.session_id
              and s.phase not in {"done", "failed"}]
fallback   = [s for s in candidates
              if s.session_id == "" and s.phase == "planning"]

if   len(primary) == 1:                      act on primary[0]
elif len(primary) == 0 and len(fallback) == 1: act on fallback[0]
else:                                        exit 0 silently
```

- **Primary match**: `session_id` equality AND non-terminal phase. Unique by
  construction (session UUIDs).
- **Startup fallback** (MANDATORY — dropping it silently kills
  planning-phase notifications for afk-new-launched sessions): a
  afk-new-seeded file has `session_id: ""` until the conductor's startup
  write lands, so an empty-`session_id` file in phase `planning` under the
  payload cwd matches. **This claim is non-persistent: hooks never write
  `session_id`** — the conductor persists it at startup, which closes the
  fallback window within seconds.
- **Ambiguity acts on nothing**: if more than one fallback candidate matches
  (e.g. an abandoned seeded file plus a fresh one in the same directory),
  the hook exits 0 silently. In the designed flow `afk new` gives each
  session its own worktree, so multiple seeded files in one cwd only arise
  from abandoned sessions — exactly the stale files hooks must not touch.
  The cost is a few silent seconds until the conductor persists
  `session_id` and the primary rule takes over.
- **Everything else is silence**: no candidates, only terminal-phase files,
  session_id mismatch, unparseable JSON, no `.afk/` at all — the hook
  exits 0 with **zero writes and zero notifications**. Stale status files
  and non-afk sessions produce nothing, ever.

When a hook acts (i.e. §7 returns a match): the UserPromptSubmit hook always
applies C1; the Notification hook applies S1 **only** in the two cases §9
assigns to it (`permission_prompt` any phase, `idle_prompt` in `planning`) and
toasts on that same match — both via the §6 write protocol, touching only
`awaiting_input` + `updated_at`. On every other Notification the hook exits 0
with no write and no toast (§9).

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

Resume: `/afk:go` invoked where the matched status file shows a
non-terminal phase resumes **from that phase** using disk state (plan
checkboxes, `loop_count`, verdict file) — it never restarts the interview.
Terminal files are never resumed and never matched by hooks; removing them
is `afk clean`'s business.

---

## 9. Notification ownership — who toasts, and who writes `awaiting_input`

Every desktop toast and every `awaiting_input` SET has exactly one owner.
The split follows a single principle: **deliberate stops are toasted by the
conductor (it alone knows when it has blocked); the hook toasts only what the
conductor cannot see.** A Notification event and a conductor SET are the two
toast sources; they never both fire for the same moment. The `awaiting_input`
write is *coupled* to the toast — the same owner asserts the flag `true` and
toasts on the same moment, so the dashboard's "who is blocked" picture and the
toast are consistent by construction (this supersedes the original policy of
an unconditional per-phase write).

| Situation | Phase | Owner | Writes `true`? | Toast |
|---|---|---|---|---|
| `permission_prompt` | any non-terminal | Notification hook (S1a) | yes | yes, immediate |
| `idle_prompt` | `planning`, sidecar (§10) reports no pending tasks | Notification hook (S1b) | yes | yes, after idle delay |
| `idle_prompt` | `planning`, sidecar reports ≥1 task | — (nobody) | no | no — hook exits 0 silently |
| `idle_prompt` | `executing` / `reviewing` / `fixing` | — (nobody) | **no** | **no** — hook exits 0 silently |
| AskUserQuestion display / any other Notification type | any | — (hook does nothing) | no | no |
| REJECT stop | `reviewing` | Conductor (S2) | yes | yes — conductor fires the notifier |
| Fix-loop cap stop | `reviewing` | Conductor (S3) | yes | yes — conductor fires the notifier |
| NEEDS_INPUT relay before AskUserQuestion | `executing` / `reviewing` / `fixing` | Conductor (S4) | yes | yes — conductor fires the notifier |
| any Notification | `done` / `failed` | — | unreachable — terminal phases never match (§7) | — |

**How the conductor toasts**: at each deliberate-stop SET (S2/S3/S4) it
invokes `{plugin-root}/hooks/notify.sh` with the plan name, the worktree
(basename of cwd), and a stop-specific message — best-effort, so a
missing/broken notifier never fails the flow (`notify.sh` exits 0
unconditionally). Content mirrors the hook's (W3.T3): title `afk: {plan}`,
body `{message} ({worktree})`.

**Rationale (grounded in the W1.T1 trigger table and the W6.T1 e2e):**

- **`permission_prompt` is the hook's job, every phase**: a permission prompt
  pauses the very tool call the conductor is trying to make, so the conductor
  cannot self-report it — and it is a genuine block regardless of any
  background work. Immediate toast, always.
- **`idle_prompt` in `planning` is the hook's job only when nothing is
  running in the background**: the planner spawns research and Crucible
  agents during the interview, so idle-in-planning is genuine only when that
  work is quiet too — signaled by the Stop payload's `background_tasks`
  array via the `.background-tasks` sidecar (§10); non-empty sidecar →
  dropped like non-planning idle. `idle_prompt` is the only *delayed*
  Notification type (latency column) — a present user gets zero toasts, an
  absent task-free user gets one reminder. AskUserQuestion display is not
  toasted (one toast per question would be noisy; the walk-away user is
  still covered by `idle_prompt`).
- **`idle_prompt` in `executing`/`reviewing`/`fixing` is dropped entirely**:
  the conductor parks at the prompt while background work runs (delegated task
  subagents, the background Phase-C reviewer) — "idle at the prompt" to Claude
  Code, but the session self-resumes on subagent completion. Toasting and
  setting `awaiting_input` on these produced the false-positive "awaiting
  input" class observed live in the W6.T1 e2e (toasts fired and the dashboard
  indicator flipped while the session was actively working). Every genuine
  non-planning block is a deliberate conductor stop (S2–S4), which the
  conductor toasts itself — so dropping non-planning `idle_prompt` loses no
  real signal.
- **Deliberate stops (S2/S3/S4) belong to the conductor**: it is the only
  party that knows it has stopped *on purpose* to wait for the user, and
  (unlike a permission prompt) it is running when it stops, so it can invoke
  the notifier itself. Giving these to the conductor is why the hook can
  safely drop non-planning `idle_prompt` without missing a real block.

---

## 10. Background-work sidecar (`.background-tasks`)

Planning-phase `idle_prompt` (§5 S1b, §9) needs a signal distinguishing "user
walked away" from "planner idle while background research/Crucible agents
run" — the Stop payload's `background_tasks` array, kept in a sidecar file,
not a `status.json` field.

- **Path**: `.afk/{plan_name}/.background-tasks` — beside `status.json`,
  dot-prefixed like `.awaiting-permission`.
- **Content**: the `Stop` payload's `background_tasks` array, written as
  compact JSON — `jq -c '.background_tasks // []'`; a payload missing the
  field writes `[]`.
- **Writer**: the `Stop` hook (`on-stop.sh`), on every §7-matched `Stop`
  event. Atomic: write `.background-tasks.tmp.$$` in the same directory,
  then `mv -f` over the sidecar. No match → no write. Never touches
  `status.json`.
- **Clearer**: the `UserPromptSubmit` hook (`on-user-prompt-submit.sh`) does
  `rm -f` on the sidecar alongside its C1 `awaiting_input` clear — user
  present → new turn → the next `Stop` rewrites it.
- **Reader**: the `Notification` hook, read only in the `idle_prompt` +
  `planning` branch (§5 S1b, §9). Pending = sidecar exists AND parses as a
  JSON array AND `length > 0`. Pending → no write, no toast, no
  `.awaiting-permission` sentinel. Missing or unparseable → not pending →
  S1b's existing behavior (toast + write).
- **Not a `status.json` field**: single writer, swept by `afk clean`
  alongside other `.afk/{name}/` sentinels, never read by `afk`.
- **Unchanged**: `permission_prompt` handling, non-planning `idle_prompt`
  (still dropped), the `.awaiting-permission` sentinel, and `status.json`'s
  nine fields.

### Opt-in hook event log (`AFK_HOOK_LOG`)

A `hook_log` helper in `lib.sh`, opt-in via `AFK_HOOK_LOG`:

- When set to a non-empty path, every hook appends one line per invocation:
  ```
  [<RFC 3339 UTC timestamp>] EVENT=<hook_event_name or "?"> PAYLOAD=<compact JSON>
  ```
- Called immediately after the payload passes `jq -e .` validation and
  **before** `find_target` runs, so unmatched events are logged too.
- **Opt-in, deliberately**: the logged line is the raw payload, which can
  contain prompt text (e.g. `UserPromptSubmit`). Never enabled by default.
- All failures are silent: unset → nothing written; a write failure (bad
  path, permissions) is swallowed, never surfaced, never fails the session.
