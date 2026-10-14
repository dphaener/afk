# The Late Phases: slices, draft, check, review

Each phase ends by writing its file and running `afk plan advance`. The `design` phase, which comes between `check` and `review` for a project with an interface, has its own prompt after this one. Whether the project has an interface is the recorded answer to the question with the id `r1-interface`: it starts with `yes` or with `no`, and three of the phases below read it.

## Phase `slices` — the task breakdown

The file: `.afk/plan/slices.md`.

Before the plan is written, the owner sees and confirms how the work is cut. This is the one place the shape of the whole plan is argued about; after it, the draft is a matter of writing it down.

**The slicing rule.** A task is a thin, complete path through every layer the change touches (schema, logic, interface, tests) that can be shown working by itself, and is small enough for one fresh agent session with no memory of this one. Groundwork that makes a later task easy comes first, as its own task. A mechanical change that touches everything at once (a rename) is the exception: add the new form beside the old, move callers over in batches, then remove the old. "Build the whole database layer, then all the handlers, then all the pages" is the shape to avoid: nothing can be shown working until the last one lands.

Cut the work from the brief, the glossary and the answers, grouped into milestones, each milestone an outcome the owner could see. Then show the breakdown in your message as a numbered list, one task per line:

```
1. Set up the project and the CI workflow — blocked by: nothing — delivers: an empty app that starts, and a pull request check that runs its tests
2. Create the hosting account (owner) — blocked by: nothing — delivers: an account the deploy task can use
3. Sign up with an email — blocked by: 1 — delivers: a visitor can sign up and see their name
```

Each line has three parts: the title, what it is blocked by (the numbers of earlier tasks, or "nothing") and what it delivers in one clause. Mark a task only the owner can do with `(owner)`.

**When the interface answer is yes**, do not put a design task in the list. Say under it, in one or two plain sentences, that one more task of theirs will be added after the plan is checked: making the design system in their design tool, from a brief this session writes, and that the tasks that build the interface will wait for it.

Then ask, in the message, two plain questions: is each task the right size, and are the blocking edges right? Repeat, showing the whole list again with the changes, until the owner says it is right. Do not write the file until they have.

Write `.afk/plan/slices.md`: the confirmed list, as shown, under its milestone headings. Then run `afk plan advance`.

## Phase `draft` — the plan

The file: the master plan, at the path `plan` in `afk.toml` names, which is `docs/PLAN.md` when there is no `afk.toml` or it names none. (The gate is that this file exists and is not blank.)

**One write.** Write the whole plan with one Write call, in the format of `plan-format.md` loaded in this session: the title, the "How to use this document" paragraph, Context, Decisions, the shared contract sections the project needs, Milestones with the tasks, Deferred, Open questions, Definition of done. The task ids follow the confirmed slices in order (`T1.1`, `T1.2`, `T2.1`), and the blocking edges become each task's `Depends on` line. Every task has `Depends on`, `Goal`, `Scope` and `Acceptance`; a task whose Acceptance is a command is better than one whose Acceptance is a sentence.

Every plan has, whatever the project:

- **Context** that quotes the brief word for word.
- **A Decisions table** with one row per settled choice, and **every row that came from the owner profile says so** ("From the owner profile" in the row, with the entry's strength). A row the owner overrode says what the profile said and what was decided instead.
- **An owner task for anything only the owner can set up**: an account, a secret, a domain name, a machine. Its title carries `(owner)`, its Scope says exactly what to do and where the result goes, and the tasks that need it depend on it. An agent never does these and never waits on them silently. The one owner task not written here is the design task: the `design` phase adds it, with its dependencies, after the critic has read the plan.
- **When the interface answer is yes, the rule on which document wins**, in the "How to use this document" paragraph, from this first write: "Where this plan and `docs/design-system.md` disagree, the plan wins on behaviour and the design system wins on looks." Keep the file's path and the words "wins on behaviour" and "wins on looks" as they are; the gate of the `design` phase looks for them. When the answer is no, the plan does not have the sentence and never mentions a design system.
- **An early task that gives the repository a CI workflow** that runs the project's tests on every pull request. afk never merges a pull request with no checks, so without this task nothing after it can ship. It is usually `T1.1` or `T1.2`, and every task that opens a pull request depends on it. Its Acceptance names the commands the workflow runs and says they pass in a fresh clone of the repository; it does not ask the build session to see the workflow pass on a pull request, because afk checks that when it ships the task.
- **Open questions keyed to the task that needs them.** Anything the owner could not settle, or chose to settle later, is a row in the Open questions table with the ids of the tasks that need the answer in "Needed by". Not a guess written into a task.
- **A Definition of done** that names the project's own test command and the check the CI task adds.

Then, in the same phase and with their own Write calls, the project's files:

- **`afk.toml`** at the project root, holding `plan = "docs/PLAN.md"` (or the path you used). Add nothing else to it unless an answer settled it: the file is read by afk's build loop and its other keys are described in afk's README, not here. If the file exists, do not overwrite it; add the `plan` line with Edit only if it has none.
- **`.gitignore`** holding `.afk/` and `afk.local.toml`, each on its own line. If one exists, add the two lines that are missing with Edit.
- **`AGENTS.md`** at the project root: the project's own instructions for any agent, written from the profile, the rule files and the Decisions table. What it holds: the stack and why, the commands to build, test and run, the house rules that apply (restated in the project's own words, not copied from the rule files), and what an agent must never do. Plain, short, and true for this project alone.
- **`CLAUDE.md`** with one line that points at it: `Read AGENTS.md; it holds this project's instructions.`

Nothing else is written here. Then run `afk plan advance`.

## Phase `check` — the checker and the critic

The file: `.afk/plan/critic.md`. The gate is that `afk plan lint` reports zero errors and `critic.md` exists.

**First the checker.** Run `afk plan lint`. It prints one finding per line, errors and warnings, and exits 1 when there is an error. Every error is fixed before anything else: find the exact text at fault, change it with `afk plan patch` (rule 5), and run `afk plan lint` again. One error, one patch, one run, until it prints no error. Warnings are read and fixed when they are right (a task with no Acceptance is a real gap); a warning you leave is mentioned in `critic.md` with the reason.

**Then the critic.** One Agent call, once the checker is clean:

```
Agent(description: "Critic: the draft plan", subagent_type: "afk:reviewer", model: "{the reasoning alias from the model registry}", prompt: "{the critic prompt loaded in this session, with {plan-path} replaced by the plan's path, {brief-path} by .afk/plan/brief.md and {request-changes} as described below}")
```

Before the call, fill `{request-changes}` from `questions.jsonl`: the owner's own later words, quoted exactly, from the answers in which they replaced or widened the request; the word `none` when no answer did. The prompt holds those three things and nothing else about this session: not the other answers, not the glossary, not this conversation. The critic reads the plan and the brief as a stranger would, which is the point; a critic that knows what you meant cannot see what you wrote.

**Each finding is dealt with**, one of two ways:

- **Fixed**: an `afk plan patch` to the plan (rule 5: the old text in one file, the new text in another), then `afk plan lint` again. An id never changes (rule 8); a task that must be split keeps its id and the new one gets a letter.
- **Answered**: when the finding is wrong, or the owner's answers settle it the other way, say why in `critic.md` and leave the plan as it is.

Ask the owner only when a finding needs a choice they have not made (two of their answers contradict each other; a part of the brief they may have dropped on purpose). That is a question in the message, and its answer goes into `questions.jsonl` as a new line in the next round number, so the record stays whole.

Write `.afk/plan/critic.md`: every finding the critic reported, in its words, and under each what was done, fixed (with what changed) or answered (with why). A finding left out of this file was not dealt with. Then run `afk plan advance`. It prints the phase it moved to: `design` when the interface answer is yes, in which case do the design prompt next, and `review` when it is no.

## Phase `review` — hand the draft to the owner

No file. The session stops here for the owner.

Print, in this order:

1. The output of `afk plan lint --summary`, as it is. Its lines are the only counts you give: milestones, tasks, owner tasks, open questions, errors, warnings.
2. The open questions, each with the tasks that need it, read from the plan's Open questions table.
3. Where the plan is (its path), and that `CONTEXT.md`, `AGENTS.md`, `afk.toml` and `.gitignore` were written beside it.
4. When the `design` phase ran: where the design brief is (`docs/design-brief.md`), and the id of the task titled `Design system (owner)`, read from the plan, with one line saying it is theirs and that the tasks that build the interface wait for it.
5. That the draft is ready for them to read: they read the plan (on the daemon's web page, at `/p/<project>/plan`, when they have it on; otherwise the file itself), and when they are happy they run `afk plan approve` in the project folder, which is what lets the build loop start on it. Tell them that; the session stops here. Never run `afk plan approve` yourself: approving is the owner's decision. Running `afk plan` again comes back to this point.

Then stop. Do not start a new round, do not edit the plan, and do not run `afk plan advance` again: it refuses from this phase, and says so.
