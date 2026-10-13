# Changing a Plan That Exists: extend, amend, reconcile

`afk plan status` prints the session's mode on its first line. This prompt applies when the mode is `extend`, `amend` or `reconcile`: the project already has a master plan, and the session changes it. When the mode is `new`, skip this prompt; the phase prompts above are the whole session.

The rules of the session all still hold. In these three modes the phase prompts above apply only where this prompt says "as in a new session"; everything else about a phase is given here.

## What is different in these modes

- **The plan is changed only by three commands**: `afk plan patch`, `afk plan insert` and `afk plan depend`. Never with Write, never with Edit, never by any other command that writes the file. The plan is being built from while you work: the build loop reads it all the time, and the commands are what keep a half-made change from ever being on disk.
- **An id comes from `afk plan next-id`**, never from counting the plan yourself (rule 6, rule 8). The one exception is the tasks of a whole new milestone, which the `extend` section covers.
- **A task that is running or shipping can only get an Amended line.** An agent is building it from the text it was given, so its text stays as it is. The commands refuse anything else and say so. `afk tasks` prints each task's status.
- **Every change is listed in `.afk/plan/changes.md`**, one line each, written as soon as the command has succeeded: what was changed, which task or section, and what the command printed (committed and pushed, committed only, written but not committed, or the pull request's address). For example:

  ```
  - Added task T2.2a "Export notes as text" after T2.2 — committed and pushed
  - T2.3 now waits for T2.2a — committed and pushed
  - Amended line on T2.2 saying T2.2a was split out of it — committed and pushed
  ```

  This file is the gate of the `draft` phase and the record the owner reads at the end. A change that is not in it was not made, as far as a resumed session can tell, and would be made twice.
- **The session ends at `review`**, with the summary from `afk plan lint --summary`. There is nothing to approve: each change was in the plan (or in its pull request) the moment its command succeeded.
- **There is no `model` phase.** A new term the change brings in is settled with the owner where the mode asks its questions and added to `CONTEXT.md` with Edit, as the `model` phase describes.
- **The project's other files are left alone.** `afk.toml`, `.gitignore`, `AGENTS.md` and `CLAUDE.md` were written when the plan was first drafted; a change session does not write them again.

## The commands

Text for a command goes in a file, and the file goes in a scratch folder **outside the project**: make one with `mktemp -d` at the start of the `draft` phase and use it for every file below. A scratch file inside the project folder could be committed by mistake or picked up by the build.

| To do this | Run |
|---|---|
| Replace some text of the plan with other text | `afk plan patch --old FILE --new FILE --message "TEXT"` |
| Add one task after a named task | `afk plan insert --after ID --entry FILE` |
| Add one task at the end of a milestone | `afk plan insert --milestone N --entry FILE` |
| Add a whole new milestone at the end | `afk plan insert --new-milestone --entry FILE` |
| Make a task wait for another | `afk plan depend TASK ON` |
| Get the id for a new task | `afk plan next-id --milestone N` or `afk plan next-id --after ID` |

- **`afk plan patch`**: the old file holds the text to replace, copied from the plan exactly, with the same spaces and line breaks, and enough of it that it occurs in the plan exactly once. The new file holds what takes its place. `--message` is one short line saying what the change is; it becomes the commit's subject.
- **`afk plan insert`**: the entry file holds one task, starting with its heading (`#### T2.3 — Title`) and then its bullets, in the format of `plan-format.md`. With `--new-milestone` it holds one milestone: its heading (`### M3 — Title`) and every task under it. The command finds the place; you never work out where in the file the text goes.
- **`afk plan depend TASK ON`** adds `ON` to the `Depends on` line of `TASK`. Use it and not a patch for this: it keeps the rest of the line as it is.
- **`afk plan next-id`** prints one id on one line and changes nothing. `--milestone N` gives the next number in milestone N. `--after ID` gives the id with a letter (`T2.2a`) for a task that must run between `ID` and the task after it.

Each of the three commands that change the plan checks the result before it writes anything: the plan must have no error it did not have before, and no id may disappear. Then it writes the file, commits only the plan file and pushes. It prints what it changed, then whether it was committed and pushed, then any note for the owner (for example the command to push by hand when the push failed). Read what it printed; do not assume.

### When a command refuses

A refusal exits 1, says why, and ends with `Nothing was changed.` The plan is exactly as it was. **The answer is always to fix the text and run the command again, never to edit the plan file yourself.** What the common ones mean:

| It says | What to do |
|---|---|
| The old text is not in the plan | The old file does not match. Read the lines again from the plan and copy them exactly. |
| The old text is in the plan more than once | Add the line before or after it to the old file (and the same line to the new file), so it matches one place. |
| The old and the new text are the same | The new file was not changed. Put the changed text in it. |
| A task is running or shipping | Leave its text alone. Add an Amended line instead (below), or tell the owner the change has to wait until the task is finished. |
| The patch removes a task | An id is never removed (rule 8). Keep the entry and add an Amended line saying what replaced it. |
| The patch adds an error | The findings are printed as `afk plan lint` prints them. Fix the new text so the finding does not arise. |
| The id is already in the plan | Run `afk plan next-id` again and use what it prints. |

If a command fails for a reason that is not about the text (git, the network, a file that cannot be read), show the owner the message as it is and stop. Do not try another way to write the file.

### An Amended line

An Amended line records a later change to a task without rewriting it. It is the last line of the task's entry:

```
- Amended 2026-10-13: split into T2.2a and T2.2b, because the export and the import turned out to be separate pieces of work.
```

It starts with `- Amended`, then today's date as `date +%F` prints it, then a colon and what changed and why, in one line. Add it with `afk plan patch`: the old file holds the last line or two of the entry (enough to occur once), and the new file holds those same lines followed by the Amended line. Nothing else in the entry changes, which is why this is allowed on a task that is running.

### When changes go through a pull request

When `afk.toml` has `via_pr = true` under `[planning]`, the commands do not change the plan file in the project folder. Each one puts its change on a branch and prints the address of a pull request; later changes in the same session are added to the same pull request. **The plan file changes only when the owner merges it.** So:

- Tell the owner the address the first time it is printed, and again at `review`.
- Write the address in each line of `changes.md`.
- The file you read is the plan before your changes. Do not patch the same lines twice: a second patch of lines you already changed is refused, because the old text is no longer what the pull request holds. If you must correct a change you made, tell the owner what to correct and let them decide whether to merge first.
- `afk plan next-id` also reads the plan before your changes. If it prints an id that `changes.md` shows this session has already used, stop: tell the owner to merge the pull request and run `afk plan` again, and carry on from there. Do not pick another id yourself.
- `afk plan lint` reads the plan file too, so in the `check` phase it reports on the plan before your changes. That is still the gate; each command already checked its own change before it was accepted. In `extend`, the critic cannot find the new milestone in the plan file, so add one more line to its prompt giving the path of the scratch file that holds the milestone.
- `afk ledger set` checks ids against the plan file, so a ledger change for a task that is only in the pull request waits for the merge. Give the owner the exact command to run afterwards, and write it in `changes.md`.

---

## Mode `extend` — a new milestone

Phases, in order: `brief`, `survey`, `rounds`, `slices`, `draft`, `check`, `review`.

**`brief`.** As in a new session: if `$ARGUMENTS` holds text it is the request; if not, ask one plain question, what the next milestone should deliver and what made them want it. Save the owner's words to `.afk/plan/brief.md` as they wrote them. Run `afk plan advance`.

**`survey`.** Read, in this order: the master plan in full; `docs/decisions.md` if the project has one; the output of `afk tasks`, which lists every task with its status from the ledger; and the owner profile with `afk profile` (and `afk profile --kind <kind>` for the project's kind), with the stop rule of the new session's survey when it exits non-zero. Write `.afk/plan/survey.md`: the plan's milestones, one line each, with how far each has got; the Decisions rows and decisions that bear on the request; the profile entries that apply; the number of the plan's last milestone, read from its `### M<n>` headings; and what the request needs that nothing you read settles. Run `afk plan advance`.

**`rounds`.** As in a new session, but only for the new milestone. Round one's three fixed questions are not asked again: who the project is for, its interface and the profile entries were settled when the plan was drafted, and the plan's Decisions table holds the answers. Never re-ask anything the plan already decides. Ask what the new milestone leaves open, product questions with a recommendation and setup facts as plain questions, recorded in `.afk/plan/questions.jsonl` as the `rounds` phase describes. The gate needs at least one answered question; if the brief and the plan truly settle everything, ask the one that matters most: what the owner must be able to see working when the milestone is done. Run `afk plan advance`.

**`slices`.** As in a new session, but only the new milestone's tasks. A "blocked by" may name a task the plan already has, by its id. Show the list, ask the two questions, repeat until the owner agrees, then write `.afk/plan/slices.md` and run `afk plan advance`.

**`draft`.** The new milestone's number is one more than the plan's last milestone. Its tasks are numbered `T<n>.1`, `T<n>.2` and so on, in the order of the confirmed slices; this is the one place ids are not asked for, because a new milestone has no tasks to collide with, and the command refuses a number that is taken.

1. Write the milestone to a file in the scratch folder: the heading `### M<n> — Title`, the line or two saying what the owner can see when it is done, then each task with `Depends on`, `Goal`, `Scope` and `Acceptance`, in the format of `plan-format.md`. A task only the owner can do carries `(owner)` and the tasks that need it depend on it.
2. Run `afk plan insert --new-milestone --entry FILE`.
3. Add what else the milestone settled or left open, each with its own `afk plan patch`: a row in the Decisions table for each choice the rounds settled, a line in Deferred for what was left for later, a row in Open questions (with the new tasks' ids in "Needed by") for what the owner could not settle.
4. Write `.afk/plan/changes.md`, one line per command that succeeded.

Run `afk plan advance`.

**`check`.** Run `afk plan lint`. Fix every error with `afk plan patch`, one error, one patch, one run. Then the critic, one Agent call as the `check` phase describes, with `{plan-path}` and `{brief-path}` filled in and this one line added at the end of the prompt, with the milestone's number in it:

```
Review only milestone M<n> and its tasks against the brief. The rest of the plan was reviewed before and is partly built; read it only to understand what M<n> depends on, and report nothing about it.
```

Deal with each finding as the `check` phase says, fixed with `afk plan patch` or answered, add a line to `changes.md` for each patch, write `.afk/plan/critic.md` and run `afk plan advance`.

---

## Mode `amend` — one task added, split or replaced

Phases, in order: `brief`, `survey`, `draft`, `check`, `review`. There are no rounds and no critic: it is one change, and the owner confirms it.

**`brief`.** `afk plan amend` already saved what the owner asked for in `.afk/plan/brief.md`. Read it, do not rewrite it, and run `afk plan advance`.

**`survey`.** Find the task the brief is about. Read its milestone in full and the milestones before and after it, and run `afk tasks` for the statuses. Write `.afk/plan/survey.md`: the task or tasks concerned with their statuses, the tasks that depend on them, and which of the three changes this is. If the brief does not say clearly enough which task or what change, ask the owner one plain question in the message and add the answer to `survey.md` (the brief is never edited). Run `afk plan advance`.

**`draft`.** Decide which of three it is:

- **Add**: a new task the plan lacks.
- **Split**: one task becomes two or more smaller ones.
- **Supersede**: a task is replaced by a different one.

A task that is running or shipping is not split or superseded here. Tell the owner it has to finish or be stopped first, write that in `changes.md` as the one line, and go on to `check`.

Then, in this order:

1. **Get the id.** `afk plan next-id --milestone N` when the task can go at the end of its milestone. `afk plan next-id --after ID` when it must run between two tasks that are already there; it prints an id with a letter. The parts of a split go after the task they come from: `--after OLD` for the first, and for each further part ask again after the one before has been inserted.
2. **Write the entry** to a file in the scratch folder, in the plan's own format: the heading `#### <id> — Title`, then `Depends on`, `Goal`, `Scope` and `Acceptance`. The `Depends on` line names what the new task waits for.
3. **Show it and ask.** Print the entry, say where it goes and which tasks will be made to wait for it, and ask the owner to confirm, as a plain question. **Skip the question when `.afk/plan/yes` exists**: the owner started the session with `--yes`. You never create or remove that file.
4. **Insert it**: `afk plan insert --after ID --entry FILE` or `afk plan insert --milestone N --entry FILE`, the same placement the id was asked for with.
5. **Make later tasks wait**: `afk plan depend TASK NEWID` for each task that must now wait for the new one. Never for a task that is running or shipping: the command refuses, and the right thing is to tell the owner that the task was started without it.
6. **For a split or a supersede**, after every part is inserted: add an Amended line to the old task with `afk plan patch`, naming the ids that replace it. Then, and only once that patch has succeeded, run `afk ledger set --replaced-by A,B OLD superseded`, with the new ids in place of `A,B`. The order matters: the ledger checks the ids against the plan, so the new tasks must be in it first. Tasks that waited for the old task are made to wait for the part that delivers what they need, with `afk plan depend`.
7. **Write `.afk/plan/changes.md`**, one line per command that succeeded, the ledger command among them.

Run `afk plan advance`.

**`check`.** Run `afk plan lint`. Fix every error with `afk plan patch` and add a line to `changes.md` for each. Then run `afk plan advance`.

---

## Mode `reconcile` — bring the plan in line with a document

Phases, in order: `brief`, `survey`, `rounds`, `draft`, `check`, `review`. No critic.

**`brief`.** `afk plan reconcile` already saved the brief, which names the document. Read it and run `afk plan advance`.

**`survey`.** Read the named document in full and the master plan in full, and run `afk tasks`. Write `.afk/plan/survey.md` with three lists, each item naming the place in the document and the task or section of the plan:

1. **Conflicts**: the document and the plan say different things about the same matter, and it is not plain which is right.
2. **Additions**: things the document holds that the plan should absorb, such as a screen no task builds or a constraint no task mentions.
3. **Contradictions**: things the plan says that the document shows to be wrong.

An empty list is written as "none". Mark each item as either a **choice** for the owner or a **patch** you can make without asking. It is a patch when the document plainly settles it and no task that is done, running or shipping is affected; it is a choice when two readings are possible, when work already built would have to change, or when it changes what the product does. Run `afk plan advance`.

**`rounds`.** Every item marked as a choice goes to the owner as one round: product questions with options and a recommendation (rule 2), recorded in `.afk/plan/questions.jsonl` as the `rounds` phase describes. Round one's three fixed questions are not asked. If no item is a choice, ask nothing and write no file; the gate passes without one. Run `afk plan advance`.

**`draft`.** Make the changes, one command each: every item marked as a patch, and every choice as the owner answered it. Changed text goes through `afk plan patch`; a task the document calls for goes through `afk plan next-id` and `afk plan insert` as in `amend` (steps 1, 2, 4 and 5, with no confirmation question: the owner already chose); a task that is done, running or shipping gets an Amended line and, where the work must be redone, a new task. Write `.afk/plan/changes.md`, one line per command that succeeded. If nothing needed changing, write the one line "Nothing changed: the plan and the document agree." so the record says so. Run `afk plan advance`.

**When the document is the design system** (`docs/design-system.md`, or the owner says that is what it is), one rule decides every conflict: **the plan wins on behaviour, and the design system wins on looks.** What a screen does, which steps a flow has and what data it holds come from the plan. Colours, type, spacing, component names and how a screen is laid out come from the design system.

- Where the design system wins, patch the plan: the tasks that build interface name the components and screens the design system defines.
- Where the plan wins, the plan stays as it is and the design has to change. Do not ask the owner to choose; the rule already has. Write what the design must change as a follow-up prompt the owner can take to their design tool, at `docs/design-followup-<n>.md`, where `<n>` is the next number no such file has yet (list `docs/` to find it). The prompt says, for each point, what the design shows, what the plan requires and what to hand back. Write it with Write; it is a new file, not the plan.
- Name the follow-up file to the owner, add a line for it to `changes.md`, and say that running `afk plan reconcile` on the design system again, once it is updated, finishes the job.

**`check`.** Run `afk plan lint`. Fix every error with `afk plan patch` and add a line to `changes.md` for each. Then run `afk plan advance`.

---

## Phase `review` in these modes

No file. Print, in this order:

1. The output of `afk plan lint --summary`, as it is. Its lines are the only counts you give.
2. The changes, read from `.afk/plan/changes.md`, each in one plain line.
3. Where the changes are. If they were committed and pushed, say so. If a command said it did not commit or did not push, repeat what it said to do. If they went through a pull request, give its address and say that the plan in the project folder changes when that pull request is merged, and name any command `changes.md` says to run afterwards.
4. Any follow-up file written, and anything left for the owner: a change that has to wait for a running task, an open question added to the plan.
5. That the session is finished, and that running `afk plan` again starts another change.

Then stop. Do not run `afk plan advance` again; it refuses from this phase. Do not make another change in this session, however small: a new change is a new session, so that it has its own brief and its own record.
