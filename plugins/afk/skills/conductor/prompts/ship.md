# Ship Step

This block extends the afk Conductor (`conductor/SKILL.md`). It turns a reviewed session into one commit on the session branch. In default mode it then opens a draft PR; in autonomous mode it stops at the commit — no push, no PR (`docs/PLAN.md` T0.2: the owner ships reviewed commits). No user stop in either mode. The conductor runs it from Verdict handling:

- **Approved mode**: on `**Verdict**: APPROVE`. Runs §0, §1, then §2 (default mode) or §2a (autonomous mode), §3, and ends with the `done` write (§4).
- **Not-approved mode**: at the fix-loop cap in autonomous mode (`# Autonomous Mode` §4). §1 and §3 only, with the §5 changes. The caller writes `phase: "failed"` afterwards; this block never writes `done` in that mode.

Read `name`, `branch`, and `base_branch` from `status.json` (disk, not memory) before step 1. The mode is autonomous when `.afk/{name}/autonomous` exists (`# Autonomous Mode` §1). Scratch files (`ship-commit-msg.txt`, `pr-title.txt`, `pr-body.md`) live in `.afk/{name}/`, which no commit ever stages.

**No approval wait.** The commit, and in default mode the draft PR, publish without the user's approval, by the user's decision for afk runs (plan decisions D9, D16). When `humanize` runs here, skip its show-and-wait-for-approval step from the user's CLAUDE.md: use its output directly. Never ask the user anything in this block, in either mode.

---

## 0. Project documents (approved mode, before the commit)

When `.afk/{name}/description.md` (conductor Step 0.5) names a master-plan task and its plan file (for example "Implement T1.1 from docs/PLAN.md"):

1. **Amend the master plan.** Append one line to that task's entry, `Amended <YYYY-MM-DD> (T… done): …`, saying what a later task should now assume because of this session: a contract that changed, a file that moved, a decision a dependent task would otherwise make again. Draw it from the plan's `## Assumptions` and `.afk/{name}/decisions.md`. If nothing changed for later tasks, the line says so in a few words. One line; never edit the entry's other text.
2. **Check `docs/decisions.md`.** The planner wrote this task's section (its `# Project Documents` rules). Make sure the section exists and covers the plan's `## Assumptions` plus the `## Relayed answers`, `## Verification for the owner` and `## Scope ideas` entries of `.afk/{name}/decisions.md`; add what is missing under the task's heading, in plain language. Create the file with that one section when it is missing.

Both edits are staged by §1 and land in its commit. No description, or one naming no master-plan task → skip this section and say so in the report. Not-approved mode skips it: the task is not done.

---

## 1. Fold the WIP commits and commit

1. **Stage** with the afk pathspec (it replaces commit-style's plain `git add -A`; it keeps plan, status, and notepad files out of commits in every repo):
   ```bash
   git add -A -- . ':(exclude).afk'
   ```
   When `.afk` is gitignored (common), git prints `The following paths are ignored ...` and exits 1, yet stages everything else. That message alone is not a failure: judge staging by `git diff --cached`, never by this exit code, and never chain the next command with `&&`.
2. **Fold the WIP commits** from Phase C.2 into one:
   ```bash
   if [ -f .afk/{name}/wip-base ] && git merge-base --is-ancestor "$(cat .afk/{name}/wip-base)" HEAD; then
     git reset --soft "$(cat .afk/{name}/wip-base)"
   fi
   ```
   - `--soft` keeps every change staged; nothing in the working tree moves.
   - Never reset to anything but `wip-base`, and never past it: commits made before afk started stay untouched.
   - `wip-base` missing (no review pass committed) or not an ancestor of HEAD (history was rewritten by hand): skip the fold and commit on top. Say which in the report.
   - **Re-ship** (a resumed session re-runs this block after an earlier ship commit): the fold takes that commit back too, and step 4 writes it again. Harmless locally; an earlier push of it makes a later push non-fast-forward, which is reported, not forced.
3. **Nothing staged** (`git diff --cached --quiet` exits 0) → skip the commit; report "nothing to commit". Default mode: go to §2 only if the branch has commits ahead of `base_branch`; otherwise skip §2 too and report "no PR: nothing to ship". Autonomous mode: go to §2a.
4. **Write the message.**
   - `commit-style` skill available → invoke it for the format; then `humanize` for the body's voice (approval step skipped, above).
   - Either skill absent → write it directly: a scoped Conventional Commit, `type(scope): Subject` (imperative, capitalized, ≤50 chars, no trailing period); scope from the repo's rules (CLAUDE.md/AGENTS.md, commit-lint config), else the main directory touched; body wrapped at 72 chars, saying what changed and why, drawn from the plan's Context and Objectives.
   - End with the session's commit attribution line, when the session's instructions give one.
   - Write it to `.afk/{name}/ship-commit-msg.txt`, then `git commit -F .afk/{name}/ship-commit-msg.txt`. Hooks run; NEVER `--no-verify`.
5. **Commit rejected** (a hook reformats files, or a commit-message linter fails): re-stage with the same pathspec (`git add -A -- . ':(exclude).afk'`), fix the message if the linter named a rule, and retry once. Still failing: report the hook or linter output verbatim and the manual commands (`git commit -F .afk/{name}/ship-commit-msg.txt` after fixing, then, in default mode, the §2 PR command), skip §2, §2a and §4, and status-write `phase: "failed"`, `awaiting_input: false` (the conductor's Failure Handling): `done` must mean committed, and the session cannot wait in `reviewing` with nothing to wait for. The reviewed work stays staged in the working tree. In not-approved mode the caller's `failed` write is the same write.

---

## 2. Open the draft PR (default mode only)

Autonomous mode never runs this section (§2a). Never ask. Never `--force`, `--no-verify`, or mark the PR ready. Never push to `base_branch`: if `branch` equals `base_branch`, skip this section and report it. Never run `gs repo sync`, `gs stack restack`, or any rebase.

**Title** = the commit subject from §1 (or the branch's latest commit subject when nothing was committed). Never interpolate the title into a shell command: a subject containing `"`, `$(`, or a backtick would break or inject. Write it to a file and read it back as a quoted substitution:
```bash
git log -1 --format=%s > .afk/{name}/pr-title.txt
```
**Body**, written to `.afk/{name}/pr-body.md` (humanized when the skill is available, approval step skipped):

1. Summary: what the change does and why, 2–5 lines from the plan's Context and Objectives.
2. `## Assumptions`: the plan's `## Assumptions` bullets, verbatim (or "None recorded.").
3. `## Follow-ups`: every `- [minor] ...` line from `.afk/{name}/review-verdict.md`, verbatim (or "None.").
4. The session's PR attribution line, when the session's instructions give one.

**Tool selection:**

- **git-spice** (`git rev-parse --verify --quiet refs/spice/data` succeeds):
  ```bash
  git cat-file -e "refs/spice/data:branches/{branch}" 2>/dev/null \
    || gs --no-prompt branch track {branch} --base {base_branch}
  gs --no-prompt branch submit --draft --title "$(cat .afk/{name}/pr-title.txt)" --body "$(cat .afk/{name}/pr-body.md)"
  ```
  `gs branch submit` pushes the branch and creates the PR, or updates the branch's existing PR. If a flag errors, re-check it with `gs branch submit --help`; never fall back to an interactive prompt.
- **Otherwise** (plain git + `gh`):
  ```bash
  git push -u origin {branch}
  gh pr create --draft --base {base_branch} --head {branch} --title "$(cat .afk/{name}/pr-title.txt)" --body-file .afk/{name}/pr-body.md
  ```
  Run the `gh` line only if the push succeeded.

**PR failure** (network, no `origin` remote, `gh`/`gs` missing or unauthenticated, permission denied, a "needs restack" refusal, a rejected push): do not retry, do not work around it. Record the exact error and the command to run by hand (the failed command above, filled in). A rejected push, such as non-fast-forward after a re-ship fold, is reported with the manual push command; the user decides whether to force it. Then continue to §4: a PR failure never blocks `done`.

---

## 2a. Stop at the commit (autonomous mode only)

No push, no PR, no `gs branch submit`, no remote operation of any kind: the reviewed commit stays on `branch` for the owner to ship. The report names the branch and says it is unpushed.

**The one exception — ship instructions in the session description.** When `.afk/{name}/description.md` tells this session how to ship (a bootstrap session, until the ship step of `docs/PLAN.md` T2.2 exists — for example "push the branch and open a draft PR against main"), carry those instructions out now: after §1's commit and before §4, because `done` is terminal and nothing after it is recorded. Follow them as written, under §2's constraints (never `--force`, `--no-verify`, a ready PR, a push to `base_branch`, or a rebase); §2's commands and title/body files may be used for the parts the instructions ask for. Never infer a ship from a description that gives no instruction. A failure there is reported with the exact error and the command to run by hand and, like a PR failure in default mode, does not block `done`.

---

## 3. Learnings (report only)

This step saves nothing. The one learnings save point is the completion step of the Workflow Engine Spec (`orchestrator/prompts/workflow-engine.md` §6): it saves every memory-worthy learning, without asking, when execution finishes. Do not write a second set of memory entries. For the report, list the entries that step saved (none: "none").

---

## 4. Status write (approved mode)

Status-write `phase: "done"`, `awaiting_input: false` (C4), per the Status Write Protocol. Touch no other field but `updated_at`. The commit already exists, so `done` means reviewed and committed. Write `done` even when §2 failed or a §2a ship instruction failed; never when §1 left the work uncommitted (§1.5 wrote `failed` instead).

---

## 5. Not-approved mode (autonomous cap)

Same §1 and §3, with these changes (a §1.5 commit failure writes `failed` as usual; the caller's write is the same):

- Skip §0 (the task is not done), §2, §2a (no push and no PR, whatever the description says: unapproved work is never shipped) and §4.
- The unapproved commit stays on `branch`; the report carries every `blocker` and `major` line from the verdict file's Findings, verbatim.
- Return to the caller (`# Autonomous Mode` §4), which writes `phase: "failed"`, `awaiting_input: false`.

---

## 6. Report items

The conductor's report includes: the commit (short SHA + subject) or "nothing to commit"; whether WIP commits were folded; the master-plan line added by §0 (file and task) or "no master-plan task"; default mode: the PR URL or the exact PR failure plus its manual command; autonomous mode: "no PR (autonomous mode)", the branch, and the outcome of any description ship instruction; learnings saved by the completion step (§3).
