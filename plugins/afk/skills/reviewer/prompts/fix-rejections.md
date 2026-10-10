# Fix Rejections Flow

This block runs ONLY after an overall **REJECT** verdict, once the combined
review report exists. It is self-contained: it drives a gated, capped fix loop
that delegates every code change to sub-agents and re-reviews after each pass.
Everything below refers to tiers by name (`light`, `balanced`, `heavy`,
`reasoning`) and resolves them through the afk Model Registry loaded in this
command's context — never write a literal model alias.

---

## 1. Rejection Gate (blocking multi-select)

After presenting the REJECT report, open a **blocking** `AskUserQuestion`
(multi-select). List each REJECT bullet as one option, plus one explicit
decline option: **"Don't fix — I'll handle these myself."** Option LABELS are
short (task ID / file + a few words); the full rejection reason goes in the
option DESCRIPTION. Frame the question so the user understands the cost: **each
fix pass triggers a full re-review — all three checks (compliance, quality, hands-on QA), with a critic
wherever one finds a blocker or major — not a spot-check of the fixed item.**

Follow the light-path Step 2 gate discipline exactly: the gate is **blocking**,
has **no default**, and does **no auto-proceed**. No fix work begins until the
user answers.

**≤4-option arithmetic.** `AskUserQuestion` allows at most 4 options per
question, and the decline option consumes one slot. When rejections number
MORE THAN 3, group deterministically until rejection options are ≤3 (decline is
the 4th):

1. Group by **task ID** first (all rejections for one `(WN.TM)` become one
   option). If still >3,
2. group by **wave** (all `(WN.*)` together). If still >3,
3. group by **check** (Check 1 compliance / Check 2 quality / Check 3 hands-on QA buckets).

A grouped option's DESCRIPTION enumerates its member rejections. If even
check-level grouping yields more than 3 options, **split across multiple
sequential `AskUserQuestion` calls** — the decline option appears on the
**first call only**.

**Selection semantics.**
- Decline selected **alone** on the first call → short-circuit any remaining
  calls, skip all fixes, and fall through to the report's manual re-run
  guidance (reference it by intent — do not quote its wording).
- Multi-call case where **no rejections are selected on any call** → same
  outcome: skip all fixes, fall through to the manual guidance.
- Decline selected **alongside** rejections in the same call → ignore the
  decline; fix the selected rejections.

---

## 2. Task Resolution Rule

Map each selected rejection to a plan task so the fix agent gets the right
task block and tier.

- **Check 1 / Check 3** (compliance / hands-on QA) rejections key directly to `(WN.TM)`.
- **Check 2** findings are keyed BY FILE (its output is `CODE ISSUES BY
  FILE`). So Step 3's synthesized Check 2 rejection bullets MUST always name
  the flagged file, using the deterministic additive variant:
  - `- [Check 2] (WN.TM, file: {path}): [reason]` when `{path}` maps to
    exactly one plan task's **Files** list, or
  - `- [Check 2] (file: {path}): [reason]` when `{path}` maps to zero or
    multiple tasks.

  This is the ONE sanctioned change to the rejection bullet format. Do not
  alter any other bullet.

**Mapping to task block + tier:**
- Unique task match → that task's verbatim block and its declared tier.
- Zero or multiple matches → a file-scoped fix that includes every matching
  task block (or the plan's **Context** section if none match); tier = the
  highest tier among the matches, defaulting to `balanced` when there is no
  match.

---

## 3. Fix Delegation Prompt (reviewer-native)

Each selected rejection is fixed by an `Agent()` call whose prompt is built
ONLY from durable artifacts (never from ephemeral chat state):

- the resolved task's **verbatim block** (ID, title, all six subsections);
- the relevant notepads — `.afk/{plan-name}/learnings.md`, `decisions.md`,
  and `issues.md` if present;
- the specific rejection reason;
- the detailed check finding for this rejection (line numbers, failing
  commands, critic notes — as captured when Step 3 synthesized the verdict),
  so the fix agent does not re-diagnose and fix a different instance;
- the **CURRENT RUN's** prior-pass history from `review-fixes.md` (see §5).

Resolve `model` from the task's tier via the afk Model Registry. **Caller-side
fallback (restated):** if the call is rejected because the alias is unknown or
unavailable, retry the same call once with that tier's Fallback alias from the
registry; this is the caller's job — a note inside the sub-agent prompt cannot
handle it. If the registry is not in context, omit `model` entirely.

The sub-agent prompt MUST instruct the fix agent to:
- fix ONLY the cited rejection;
- respect the task's **Must NOT do**;
- leave the fix **UNCOMMITTED** (committing is the user's call);
- run `git add -N <path>` (intent-to-add) on any NEWLY CREATED file so it
  appears in working-tree diffs;
- report what changed and why.

**Concurrency rule.** Rejections resolving to the same file are handled by a
**single sequential fix agent** (or serialized calls) — never parallel agents
editing one file. Accepted residual risk (note it): two agents may still touch
an unflagged shared file — a test, an import site — concurrently; this is
tolerated.

**Failure rule.** A fix agent that errors or reports it could not fix gets
**exactly ONE retry at the SAME tier**, with its failure report appended to the
prompt. This is a **deliberate non-escalation** — NOT the registry's escalation
chain — because the rejection is narrow and the failure report is the added
leverage; stated here so it is not "corrected" later. If the retry also fails,
mark the rejection **UNFIXED** in the fix log (§5) and carry it into the
re-review report — never silently drop it.

---

## 4. Capped Re-Review Loop

Fixes are UNCOMMITTED working-tree edits, and a commits-only diff would miss
them. Therefore, **before EACH re-review pass**, gather fresh evidence:

1. Run `git status --porcelain`; run `git add -N` on any untracked files
   defensively (evidence must not depend on fix agents remembering the
   instruction).
2. Pass the working-tree-inclusive **two-dot `git diff {base}`** (Step 1's
   base; it includes uncommitted changes and, via `git add -N`, newly created
   files) as the diff command input to every check and critic, in place of
   Step 1's default. Keep Step 1's existing fallback chain. **Never reuse a
   prior pass's diff.**

Accepted risk (note it): `git diff {base}` also sweeps in any pre-existing,
unrelated uncommitted user edits as evidence.

Then re-run the **FULL** three-check review (all three checks, plus a critic on any
check with a blocker or major), not just the failed checks. This is a deliberate safety trade-off:
fixes can regress previously-passing checks. Do not optimize it away.

Loop **gate → fix → re-review** until APPROVE. The **cap counts FIX passes**
(each gate→fix→re-review cycle), **max 3 per invocation**; the initial review
is not a pass.

**Declined-rejection memory.** Rejections the user declined are remembered for
the rest of the invocation and NOT re-presented — matched by **TASK ID + FILE**
(never by reason text, which check agents re-phrase between passes). If all
remaining rejections match declined keys, exit the loop immediately to the
manual guidance — do not burn a pass.

**At the cap.** After pass 3 with rejections remaining, present them and ask
(Crucible cap pattern) whether to **proceed anyway** or **continue manually**.
"Proceed anyway" is **NOT an APPROVE**: the manual-testing-story gate does not
fire, and the user sees the final review report with UNFIXED/declined markers
plus the command's existing closing guidance (PR next steps). In that final
at-cap report, replace the gate-aware closing line (which promises a fix gate
below) with cap wording — the cap is reached and no further gate follows.

---

## 5. Fix Log

`mkdir -p .afk/{plan-name}/` if needed, then **APPEND** to
`.afk/{plan-name}/review-fixes.md`. Mechanism: this command has **Write** and
**Bash** but not Edit, so append via a Bash `>>` heredoc, or via Write using a
read-then-rewrite that preserves existing content — never truncate.

Each `/afk:review` invocation opens a new `## Run {full ISO timestamp}`
section **lazily, at the first fix delegation** — an all-declined invocation
writes nothing. Use a full timestamp (not just the date) so same-day re-runs do
not collide. The **current run** is the LAST section in the file. Under it,
each pass appends its selected rejections, delegated fixes, outcomes, and
UNFIXED/declined markers.

Wisdom-threading (the prior-pass history fed into §3 delegations) reads **ONLY
the current run's section** — the file persists across invocations, and stale
prior-run history must not leak into new delegations. **NEVER write to
`issues.md`.**

---

## 6. Read-Only Restatement

The reviewer remains forbidden from Edit/Write on code files even for a trivial
one-line fix — every code change flows through `Agent()`. The reviewer's own
writes are limited to `.afk/{plan-name}/` artifacts (`review-fixes.md`,
`manual-testing.md`).
