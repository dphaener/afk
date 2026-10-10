# Manual Testing Story

This block defines the post-APPROVE flow: an optional, user-gated manual
testing story that lets a human hand-verify the changes. It is entirely
separate from the fix-rejections flow — it fires only on a true APPROVE.

## When this block fires

Fires on ANY final APPROVE verdict, including one reached after one or more
passes through the `# Fix Rejections Flow` loop. It does NOT fire when the
fix loop exhausts its 3-pass cap and the user chooses "proceed anyway" —
that outcome is not an APPROVE, and this gate must be skipped entirely in
that case. If the verdict is a true APPROVE, this gate always runs, whether
it was the first pass or the result of the fix loop.

## Step 1 — Gate (blocking, no default)

Immediately after presenting the Review Report with an APPROVE verdict, use
`AskUserQuestion` with options:

- **Yes — generate the testing story**: Synthesize a step-by-step manual
  verification narrative and save it to `.afk/{plan-name}/manual-testing.md`
- **No — skip it**: Finish here; no story is generated

This is a single, blocking question. There is no default and no
timeout-based auto-proceed — do not generate the story until the user
explicitly picks "Yes." Keep this a separate, single-purpose
`AskUserQuestion` call: never merge it with the fix-rejections gate, even
when both could theoretically apply to the same invocation (they cannot,
since this gate is APPROVE-only and the fix gate is REJECT-only, but the
two must never be combined into one question or one flow regardless).

If the user picks "No," stop here — do not generate or write anything.

## Step 2 — Story Generation

Only after explicit "Yes" in Step 1, synthesize a step-by-step manual
verification narrative from THREE sources. Do not skip any of the three;
do not substitute one for another:

1. **The plan's per-task `**Acceptance Criteria**`** — read every task
   block in the plan file gathered during Step 1 of the reviewer flow
   (`.afk/plans/{plan-name}.md`) and pull out what each task claims to
   prove.
2. **The plan's `## Verification Strategy` section** — especially its
   **Manual check** line, which is often written by the plan's author
   specifically to describe how a human should confirm the work by hand.
3. **The actual diff** — ALWAYS gather this as a working-tree-inclusive
   two-dot diff:

   ```
   git diff main
   ```

   Use two-dot, never three-dot (`git diff main...HEAD`): fix-loop fixes
   are uncommitted working-tree edits, and a three-dot diff only sees
   commits, so it would silently exclude them. Supplement this with
   untracked files:

   ```
   git status --porcelain
   ```

   Any line starting `??` is an untracked file — read its contents
   directly, since it won't appear in the diff at all. If `git diff main`
   is unavailable for any reason, fall back to Step 1's existing fallback
   chain (recently modified files, or asking the user to describe what
   changed) rather than inventing a different diff command.

**Synthesize, do not copy.** The story is NOT a re-listing of the plan's
Acceptance Criteria bullets verbatim — it is a narrative that combines what
the plan promised, what the plan's author said to check by hand, and what
actually landed in the diff, into something a human can follow start to
finish without reading the plan or the code first.

**Ordering** — structure the story as a user journey, in this order:

1. **Setup / preconditions** — what must be true before starting (env vars,
   seed data, a server running, a feature flag, being on the right branch).
2. **Numbered walk-through steps** — concrete commands to run, URLs to
   visit, or UI actions to take, one per step. Each step must name EXACTLY
   what to click, run, or request — not "test the login flow" but
   "run `npm run dev`, open `http://localhost:3000/login`, enter
   `test@example.com` / `password123`, click Sign In."
3. **Expected observation per step** — what correct looks like for that
   specific step, concrete enough that a tester with no implementation
   knowledge can tell pass from fail without guessing (an exact status
   code, a visible UI element, a specific log line, a file's new content).
4. **Edge cases worth poking** — one or two things adjacent to the happy
   path that the diff or plan suggests could break (an empty input, a
   second concurrent request, a boundary value).
5. **Cleanup** — how to undo setup/preconditions if they leave the
   environment in a nonstandard state (revert a seed, unset an env var,
   stop a server).

Write for a human tester who did NOT implement the changes — assume no
familiarity with the code, only with the product. Avoid implementation
jargon (variable names, internal function names) unless it's literally
what appears in a command or URL the tester must type.

## Step 3 — Output

Produce the story in BOTH places:

1. **Render the full story in chat** — the complete narrative, not a
   summary or a link to the file.
2. **Write it verbatim to disk**:

   ```
   mkdir -p .afk/{plan-name}/
   ```

   then write the same content, unabridged, to
   `.afk/{plan-name}/manual-testing.md`.

In the completion report, explicitly state that this file is a **local,
gitignored artifact** — `.afk/` is excluded by the repository root
`.gitignore` — and will NOT travel with a branch or pull request unless
the user manually copies its content somewhere tracked. Do not let the
user believe this file will show up in a PR diff or survive a `git push`.

## Constraints

- The story generator writes ONLY `.afk/{plan-name}/manual-testing.md`.
  It never modifies code files, the plan file, or any other file — this
  step is pure synthesis and reporting, not execution.
- Never auto-generate the story without the Step 1 gate being answered
  affirmatively — an APPROVE verdict alone is not consent.
- The story is a synthesized narrative built from three sources, never a
  verbatim restatement of the plan's Acceptance Criteria.
- This gate is always a separate, single-purpose `AskUserQuestion` — never
  merged with the `# Fix Rejections Flow` gate.
