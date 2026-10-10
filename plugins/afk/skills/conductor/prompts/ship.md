# Ship Step

This block extends the afk Conductor (`conductor/SKILL.md`). It turns a reviewed session into one commit and a draft PR, with no user stop in either mode. The conductor runs it from Verdict handling:

- **Approved mode**: on `**Verdict**: APPROVE`. Ends with the `done` write (§4).
- **Not-approved mode**: at the fix-loop cap in autonomous mode (`# Autonomous Mode` §4). Same steps, with the §5 changes. The caller writes `phase: "failed"` afterwards; this block never writes `done` in that mode.

Read `name`, `branch`, and `base_branch` from `status.json` (disk, not memory) before step 1. Scratch files (`ship-commit-msg.txt`, `pr-title.txt`, `pr-body.md`) live in `.afk/{name}/`, which no commit ever stages.

**No approval wait.** Draft PRs and their commits publish without the user's approval in both modes, by the user's decision for afk runs (plan decisions D9, D16). When `humanize` runs here, skip its show-and-wait-for-approval step from the user's CLAUDE.md: use its output directly. Never ask the user anything in this block, in either mode.

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
   - **Re-ship** (a resumed session re-runs this block after an earlier ship commit): the fold takes that commit back too, and step 4 writes it again. Harmless locally; an earlier push of it makes §2's push non-fast-forward, which is reported, not forced.
3. **Nothing staged** (`git diff --cached --quiet` exits 0) → skip the commit; report "nothing to commit". Go to §2 only if the branch has commits ahead of `base_branch`; otherwise skip §2 too and report "no PR: nothing to ship".
4. **Write the message.**
   - `commit-style` skill available → invoke it for the format; then `humanize` for the body's voice (approval step skipped, above).
   - Either skill absent → write it directly: a scoped Conventional Commit, `type(scope): Subject` (imperative, capitalized, ≤50 chars, no trailing period); scope from the repo's rules (CLAUDE.md/AGENTS.md, commit-lint config), else the main directory touched; body wrapped at 72 chars, saying what changed and why, drawn from the plan's Context and Objectives.
   - End with the session's commit attribution line, when the session's instructions give one.
   - Write it to `.afk/{name}/ship-commit-msg.txt`, then `git commit -F .afk/{name}/ship-commit-msg.txt`. Hooks run; NEVER `--no-verify`.
5. **Commit rejected** (a hook reformats files, or a commit-message linter fails): re-stage with the same pathspec (`git add -A -- . ':(exclude).afk'`), fix the message if the linter named a rule, and retry once. Still failing: report the hook or linter output verbatim and the manual commands (`git commit -F .afk/{name}/ship-commit-msg.txt` after fixing, then the §2 PR command), skip §2 and §4, and status-write `phase: "failed"`, `awaiting_input: false` (the conductor's Failure Handling): `done` must mean committed, and the session cannot wait in `reviewing` with nothing to wait for. The reviewed work stays staged in the working tree. In not-approved mode the caller's `failed` write is the same write.

---

## 2. Open the draft PR

Never ask. Never `--force`, `--no-verify`, or mark the PR ready. Never push to `base_branch`: if `branch` equals `base_branch`, skip this section and report it. Never run `gs repo sync`, `gs stack restack`, or any rebase.

**Title** = the commit subject from §1 (or the branch's latest commit subject when nothing was committed). Never interpolate the title into a shell command: a subject containing `"`, `$(`, or a backtick would break or inject. Write it to a file and read it back as a quoted substitution:
```bash
git log -1 --format=%s > .afk/{name}/pr-title.txt
# not-approved mode: printf '[not approved] %s\n' "$(git log -1 --format=%s)" > .afk/{name}/pr-title.txt
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

## 3. Learnings (report only)

This step saves nothing. The one learnings save point is the completion step of the Workflow Engine Spec (`orchestrator/prompts/workflow-engine.md` §6): it saves every memory-worthy learning, without asking, when execution finishes. Do not write a second set of memory entries. For the report, list the entries that step saved (none: "none").

---

## 4. Status write (approved mode)

Status-write `phase: "done"`, `awaiting_input: false` (C4), per the Status Write Protocol. Touch no other field but `updated_at`. The commit already exists, so `done` means reviewed and committed. Write `done` even when §2 failed; never when §1 left the work uncommitted (§1.5 wrote `failed` instead).

---

## 5. Not-approved mode (autonomous cap)

Same §1–§3, with these changes (a §1.5 commit failure skips the PR as usual; the caller still writes `failed`):

- PR title is `[not approved] {commit subject}` (written to `pr-title.txt` as shown in §2).
- The body starts with `## Open review findings`: every `blocker` and `major` line from the verdict file's Findings, verbatim, before the summary.
- Skip §4. Return to the caller (`# Autonomous Mode` §4), which writes `phase: "failed"`, `awaiting_input: false`.

---

## 6. Report items

The conductor's report includes: the commit (short SHA + subject) or "nothing to commit"; whether WIP commits were folded; the PR URL or the exact PR failure plus its manual command; learnings saved by the completion step (§3).
