# Grill Loop

This is the planner's only interview architecture. It is **decide and
escalate**: a dependency-ordered walk through the decisions the plan cannot be
written without, where you decide each one yourself with your recommended
answer and record it as an assumption, and put a decision to the user only when
it meets the escalation rule (section 4). Facts are looked up, never asked.
When no node is left open, plan generation starts at once — there is no
confirmation gate.

**You run this loop yourself, in-session.** Never delegate it to a subagent: a
subagent's `AskUserQuestion` cannot reach the user, so a delegated loop dies
silently and you would generate a plan on invented answers presented as the
user's.

This file is self-contained. Sections 1-8 are the whole methodology — nothing
else needs to be in context for it to run.

---

## 1. Skill Preference (Check This First)

**Grill-skill upgrade**: if the session's available-skills list includes a grill
skill (e.g. `mattpocock-skills:grill-me`), invoke it via the `Skill` tool the
first time a node escalates, and let it shape the wording of escalated
questions only. If no such skill is listed, follow this file's methodology
directly — it is complete on its own. Never guess a skill name: read the
available-skills list, and if nothing matching is there, run the fallback
silently. Do not install anything.

**No grill skill is invoked in autonomous mode, or on a run where no node
escalates.** In both cases nothing reaches the user, so there is no question
for a skill to shape.

**A skill only shapes the wording of escalated questions. It does not relax
this file's contract.** In BOTH modes — skill-driven and fallback — all of the
following still bind:

- the escalation rule of section 4 — a skill never widens what reaches the
  user,
- the four draft-section writes of section 8,
- every required node of section 6,
- the one-decision-per-`AskUserQuestion` mechanics of section 5, including the
  `(Recommended)`-first option list,
- the loop end of section 7 — no confirmation gate.

Assume nothing about what a grill skill covers. A real one is a dozen lines of
style guidance: it writes no draft file, never mentions `AskUserQuestion`, and
names no required nodes; most grill skills ask about everything. Let it drive
unbound and it puts every node to the user and never writes the draft sections
— at which point the handoff TLDR, which reads `## Risk` and
`## Constraints (MUST / MUST NOT)` off the draft, has nothing to read. Bind the
skill to this file, never the reverse.

---

## 2. Build the Decision Tree

Before deciding anything, build the tree from two inputs: the user's request,
and the recon findings already recorded in the draft (codebase research, plus
web research if it ran).

- **Nodes are decisions, never facts.** A node is something no file on disk
  settles — a trade-off, a boundary, a priority, a tolerance. Section 3 draws
  the line.
- **Edges are dependencies.** Draw an edge from A to B when A's answer changes
  B's *option set* — not merely when the two are topically related. If B's
  options read identically whichever way A goes, there is no edge and B can be
  settled in any order.
- **Persist the tree to the draft** under `## Decision Tree`, before you
  settle any node. Each node carries a short stable id, a one-line statement of
  the decision, its dependencies, and a status of `open` or `resolved`.

```markdown
## Decision Tree
- [D1] open — Storage backend for exported files (depends on: —)
- [D2] open — Retention window for stored exports (depends on: D1)
- [D3] resolved — Export runs sync or async (depends on: —)
```

The tree is a live working document, not a plan artifact. Rewrite it as it
changes (section 5) and expect it to look different at the end than it did at
the start.

---

## 3. The Fact / Decision Split

Sort every open question into exactly one of two buckets.

- **Fact** — answerable by reading the repo: source, config, `CLAUDE.md`,
  manifests and lockfiles, migrations, git history, in-repo docs. You MUST look
  it up. Never ask it, and never record it as an assumption. Facts include
  which test runner the repo uses and how it is configured, where tests live
  and how test files are named (a `CONTRIBUTING.md` rule or an existing test
  file settles it), whether a module already exists, what a function returns
  today, which version of a dependency is pinned, what the existing naming
  convention is. A resolution whose answer is a convention you found on disk
  is a fact mis-filed as a decision: record it under `## Research Findings`
  and drop the node, so it never reaches `## Assumptions`.
- **Decision** — cannot be derived from anything on disk, because it depends on
  intent, priority, or taste. Decisions become tree nodes. Decisions include
  which of two viable approaches to take, where the scope boundary sits, what
  to trade away under time pressure, what risk level is acceptable.

If you catch yourself about to decide or ask something you could grep for,
stop, grep for it, and record the answer under the draft's
`## Research Findings` instead.

Where a lookup comes back genuinely ambiguous — two conflicting conventions
live in the same repo, say — the *fact* is that both exist and the *decision*
is which one this work follows. Look up the first; decide the second (it
escalates only if it meets section 4's rule).

---

## 4. Decide or Escalate

Every decision node gets your recommended answer. Whether the user sees it
depends on one rule, stated here once and referenced everywhere else:

**Escalation rule**: escalate a decision to the user only when it involves an irreversible or destructive action, security or data-loss exposure, scope that conflicts with the user's request, or a genuine fork in user intent that no artifact settles.

Apply it per node, honestly in both directions:

- A node that does not meet the rule is **decided**: you pick your recommended
  answer and record it with the `(assumed)` tag (section 5). Preference,
  taste, naming, ordering, and "both options are defensible" calls are never
  escalated — a defensible default exists, so use it.
- A node that meets the rule is **escalated** in default mode: put it to the
  user per section 5. A "genuine fork in user intent" means two readings of the
  request lead to materially different deliverables and nothing in the
  request, the ticket, the session description, or the repo picks one.
- **A request that implies the destructive step does not settle it.** "Trim",
  "clean up", "reset", "prune", or "remove" existing data asks for an outcome;
  whether existing records, files, or history are permanently discarded (versus
  kept, capped going forward, or archived) is still the user's decision and
  meets the rule. Only a request that names the exact data and says to delete
  it has already decided that. When this node escalates, the question put to
  the user is that one: may existing data be discarded at all (delete it, keep
  it and cap only future writes, or archive it first)? The cap size, the
  enforcement point, and the file format follow from the answer and are your
  own `(assumed)` decisions, never the escalated question.
- `(assumed, would have escalated)` exists only in autonomous mode. In default
  mode a node that meets the rule reaches the user even when `AskUserQuestion`
  is unavailable: put that one decision in your message and stop.

### Autonomous mode

When the invoking skill passes autonomous mode:
**Autonomous mode never calls `AskUserQuestion`** — not for a decision, not
for confirmation, not for anything. Nodes that meet the escalation rule are decided too, with your
recommended answer, and recorded with the `(assumed, would have escalated)`
tag. Prefer the reversible, non-destructive, least-exposure option for those
nodes, because nobody reviews them before execution.

### Assumptions reach the plan

Every `(assumed)` and `(assumed, would have escalated)` resolution is written
into the plan's `## Assumptions` section at plan generation (format in
plan-format.md), numbered `A1`, `A2`, ... in decision-tree order, each entry
keeping its `(assumed)` or `(assumed, would have escalated)` tag. Decisions the
user answered are not assumptions and do not go there.

---

## 5. The Loop

Walk the tree in dependency order. A node is ready only once every node it
depends on is `resolved`. Among ready nodes, take the one that unblocks the
most others. Apply section 4 to it.

### Escalated nodes (default mode only)

1. **One node per `AskUserQuestion` call.** One decision, one call. Never
   bundle a second decision into the same call, and never use the tool's
   multi-question capacity to sneak batching back in. Two decisions in one call
   force the user to commit to B before they know the answer to A.
2. **Give 2-4 concrete options.** Specific, mutually exclusive, real choices —
   never "Option A / Option B / Other". Each option's description states what
   it commits the plan to.
3. **Always recommend.** Put your recommended option FIRST and mark it
   `(Recommended)` in its label; if the label runs long, shorten the rest of
   the label and keep the marker. Give the reason for the recommendation in
   that option's description. "No preference" is not a stance available to
   you.
4. **Wait.** Do not queue the next escalated question, pre-write the
   resolution, or move on an assumed answer for this node.

### After every resolution — decided or answered — in this order

1. **Record the resolution** under `## Resolved Decisions` and flip the node to
   `resolved` in `## Decision Tree`:

   ```markdown
   - [D1] {decision} → {answer} — {reason} (assumed)
   - [D2] {decision} → {answer} — {reason} (assumed, would have escalated)
   - [D3] {decision} → {answer the user chose} — {one-line rationale}
   ```

   The first form is a planner decision, the second an autonomous-mode
   decision on a node that met the escalation rule, the third a user answer.
2. **Promote hard boundaries to constraints.** If — and only if — the
   resolution establishes something the plan must ALWAYS do or must NEVER do,
   append a line to `## Constraints (MUST / MUST NOT)`:

   ```markdown
   - MUST: [what the plan must always do] — [one-line rationale]
   - MUST NOT: [what the plan must never do] — [one-line rationale]
   ```

   This is the ONLY mechanism that populates that section; nothing else writes
   to it, so a boundary you skip here never reaches the plan. Equally, a
   preference, a ranking, or a "probably" is not a hard boundary and must not be
   promoted. Most resolutions promote nothing. An empty constraints section is
   a legitimate outcome, not a sign the loop failed.
3. **Re-derive the tree.** The answer may prune branches that no longer apply
   (delete them, naming the answer that pruned them) and may open branches that
   only exist now (add them as `open`). Never carry a stale tree forward.

Then take the next ready node and repeat.

---

## 6. Required Nodes (Always Present)

Three nodes appear in the tree on every run, whatever the request looks like.
Add them when you build the tree, not when you notice they are missing. You
decide all three; the first two escalate only under section 4's rule, and the
third never does.

### Test strategy

Detect first:

1. Read `CLAUDE.md` if present — look for test, build, and lint commands.
2. Check the project manifests to identify the stack and runner:
   `package.json`, `Makefile`, `pyproject.toml`, `Gemfile`, `go.mod`,
   `Cargo.toml`, or the equivalent.

Everything those two steps establish is a fact: the runner, its command and
config, and where test files live and how they are named go under
`## Research Findings`, and the resolution of this node never restates them.
Then decide only whether this work carries automated tests and when they get
written, from what detection supports:

- **TDD (test first)** — the default when a runner is detected; tasks
  structured write-test-first, then implement
- **Tests after implementation** — when the code under change is hard to
  specify before it exists; test tasks land in a later wave
- **No automated tests** — when no runner is detectable; Acceptance Criteria
  still carry manual verification steps, and the reason names the missing
  runner

### Prerequisites

What must already be true before any of this work can start. Look for pending
migrations, config or environment changes, dependency and version upgrades,
credentials or access, and ordering constraints against work already in flight
(open branches, the git-spice stack). Decide from what you find; when lookups
turn up nothing, record `None found — this work can start immediately` as the
assumed answer. Resolutions here promote to a `MUST:` constraint more often
than any other node — "MUST: land the column migration before the backfill
task." A prerequisite that needs a destructive or irreversible step, or access
you cannot verify, meets the escalation rule.

### Closing risk

The last node — rated once every other node is `resolved`. This is a planner
self-rating, never a question: weigh blast radius, reversibility, security
surface, and how many decisions were assumed on nodes that met the escalation
rule. Write it to the draft's `## Risk` section, level token first, then
`(planner-rated)`:

```markdown
## Risk
MEDIUM (planner-rated) — [one-line rationale citing blast radius and reversibility]
```

A HIGH rating makes the Crucible run automatically after plan generation (see
the planner skill's PHASE 4). Do not lower a rating to avoid that.

---

## 7. Loop End

The loop ends when no node in `## Decision Tree` is still `open`. Plan
generation starts at once: announce the transition with a compact recap — the
count of assumed decisions, any escalated answers, the recorded constraints,
the planner-rated risk — and do not wait for a reply. There is no confirmation
question and no exit gate.

**Skip phrases end escalation.** If the user uses a skip-directly-to-plan
phrase (see the planner skill's list), stop escalating: decide every remaining
open node yourself with the `(assumed)` tag — including nodes that meet the
escalation rule, which the user just declined to be asked about — rate the
closing risk, and generate. No risk question, no resumed loop. Only the user
can elect this; you never may.

---

## 8. Draft Sections This Loop Owns

This loop writes exactly four sections of `.afk/drafts/{topic-slug}.md`. The
heading strings are a contract with the planner skill's draft template and with
the handoff TLDR that reads the draft afterwards — reproduce them character for
character, spaces and slash included.

| Section | Written | Contents |
|---|---|---|
| `## Decision Tree` | section 2, rewritten on every re-derivation | one line per node: id, decision, dependencies, `open` / `resolved` |
| `## Resolved Decisions` | section 5, after every resolution | node id, decision, answer, reason, and the `(assumed)` or `(assumed, would have escalated)` tag when the planner decided it |
| `## Constraints (MUST / MUST NOT)` | section 5, only for resolutions that establish a hard boundary | `MUST:` / `MUST NOT:` lines, each with its rationale |
| `## Risk` | section 6's closing node | the planner-rated `LOW` / `MEDIUM` / `HIGH` level, `(planner-rated)`, then a one-line rationale |

`## Risk` is not a place for a risk essay. `## Constraints (MUST / MUST NOT)`
is the source the handoff TLDR reads for its key constraints, and plan
generation reads it for task `**Must NOT do**` boundaries.
`## Resolved Decisions` is the source plan generation reads for the plan's
`## Assumptions` section.

Every other draft section belongs to the planner skill. Do not repurpose one,
and do not invent a fifth section here.
