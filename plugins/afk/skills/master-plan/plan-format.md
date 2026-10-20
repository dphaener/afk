# afk Master Plan Format

This document defines the format of a master plan: the file a project keeps at `docs/PLAN.md` that
lists every task the project will be built from. It is not the per-task work plan (`.afk/plans/{name}.md`),
which has its own format document. One master plan has many tasks; each task later gets its own work plan.

A master plan is **pure markdown**: no frontmatter, no JSON.

---

## 1. What a master plan is and who reads it

Two readers use a master plan.

- **The daemon** (`afk daemon`) reads only two things: the task headings and each task's `Depends on`
  line. From them it works out which task is ready to run next. It re-reads the file on every cycle,
  because tasks get added and split while the build runs.
- **The agent that runs a task** reads everything else: the context, the decisions, the shared contract
  sections and the task's own entry.

So the headings and the `Depends on` lines must be exact, and the rest must be good enough that a fresh
agent with no memory of the planning can do the task. A mistake in a heading or a `Depends on` line is
the kind the daemon refuses or silently misreads, which is why a checker exists (section 9).

---

## 2. The parts of the file, in order

1. **The title**: a single `# ` heading.
2. **A bold `**How to use this document.**` paragraph**, before the first `##` section. It tells a reader
   (and the agent) how to read the plan, which sections bind and how changes are made. In a project
   with an interface it also says which document wins when the plan and the design system disagree:
   "Where this plan and `docs/design-system.md` disagree, the plan wins on behaviour and the design
   system wins on looks." The words `docs/design-system.md`, "wins on behaviour" and "wins on looks"
   are what the planning session's gate looks for there.
3. **`## Context`**: what the project is and why. Quote the owner's original request word for word, so a
   later reader can tell what was asked from what was decided.
4. **`## Decisions`**: a table of choices already made, one per row (for example `Topic | Decision`). Every
   row that came from the owner profile says so in the row (for example "From the owner profile").
5. **The shared contract sections** the project needs: architecture, domain model, standards, and the
   like. Every task reads these, so they are part of its contract. Name them for the project; the
   checker does not require any.
6. **`## Milestones`**: the tasks, grouped by milestone (section 3).
7. **`## Deferred`**: things deliberately left for later, each with the reason.
8. **`## Open questions`**: a table of questions still open, each tied to the tasks that need the answer
   (section 6).
9. **`## Definition of done`**: what every task must satisfy before it counts as done.

Section titles may carry a number (`## 8. Milestones`) and extra words after the name
(`## 9. Deferred / later`). The checker drops a leading number and dot, ignores case, and matches when
the title starts with the name.

The checker looks for the "How to use" paragraph (a line containing `**How to use`, before the first
`##` heading) and six `##` sections: Context, Decisions, Milestones, Deferred, Open questions and
Definition of done. A missing one is a warning, not an error.

---

## 3. Milestones

A milestone is a heading `### M<n> — Title`, where `<n>` is a number, followed by one line that states
the outcome:

```
### M1 — A working skeleton

**Outcome:** the app starts and answers one request.
```

The tasks of the milestone follow, each under its own `####` heading. The dash after `M<n>` may be an em
dash, an en dash or a hyphen.

---

## 4. Tasks

A task heading is:

```
#### T<milestone>.<n>[letter] — Title
```

- Exactly **four** hashes. Three or five is not a task heading.
- A capital `T`, then the milestone number, a dot and the task number: `T2.3`.
- An optional single lower-case letter for a task added between two others: `T2.2a`.
- An em dash, an en dash or a hyphen, with a space on each side, then the title.

Ids are **never renumbered and never reused**. If a task is split or another is needed between two
existing ones, give the new one a letter. Other plans, notes and the daemon's ledger refer to these ids.

A title that carries `(owner)` or `(owner; …)` marks an **owner task**: something only the owner can do
(set up a server, create an account). No agent is started for it. Case does not matter and spaces inside
the brackets are allowed. `(owner + agent)` is an ordinary task that an agent does with the owner's help,
and prose such as `(owner approval)` is ordinary too.

A title that carries `(fix)` or `(fix; …)` marks a **fix task**: a bug fix that a `fix` planning session
adds to the plan. Case does not matter and spaces inside the brackets are allowed; prose such as `(fixes)`
or `(fix up)` is ordinary. The daemon picks the first ready fix task in file order ahead of the other ready
tasks. It still waits for the task's `Depends on` line, and an open question that names it still holds it.
A title may not carry both `(fix)` and `(owner)`: an agent builds the first and the owner does the second.
Fix tasks live in a standing milestone titled exactly `Fixes`, created by the first `afk plan fix` with the
outcome "bugs found after a task shipped are fixed"; later fixes are added at its end.

One owner task has a fixed title: `Design system (owner)`, the task in which the owner makes the design
system in their design tool and saves it at `docs/design-system.md`. A project with an interface has it
in its first milestone, and every task that builds part of the interface depends on it. The planning
session's gate and the daemon both recognise the task by that title and nothing else (the title is the
only identity a task has besides its id), so write it exactly; a note after `owner` inside the brackets
is allowed, and any other word in the title makes it an ordinary owner task.

Under the heading, the entry is a list of bullets with these exact labels.

| Label | Needed | What it holds |
|---|---|---|
| `- **Depends on:**` | required | The task ids this task waits for (section 5). |
| `- **Goal:**` | required | The result, in one or two sentences. |
| `- **Scope:**` | required | What to build and where: files, commands, behaviour. |
| `- **Acceptance:**` | required | How to tell the task is done; commands where possible. |
| `- **Background:**` | optional | Context an agent needs that the shared sections do not give. |
| `- **Must not:**` | optional | Things the task must leave alone. |
| `- **Not in scope:**` | optional | Things a reader might expect but that belong to another task. |
| `- **Note:**` | optional | Anything else worth knowing. |

A bullet may wrap onto indented continuation lines.

Later changes to a task are **appended** as lines starting `Amended <date> (T… done): …`, naming the task
whose completion caused the change. The original text is not rewritten, so the history stays readable.

---

## 5. The Depends on line

The daemon reads the **first** line under the heading that matches `- **Depends on:** …`. A second such
line under the same heading is ignored.

- A comma-separated list of task ids: `- **Depends on:** T1.1, T1.2`.
- `—` or `None` for no dependencies. (A hyphen or en dash alone also means none.)
- Text in parentheses is ignored, so `T1.1 (the schema), T1.2` is read as `T1.1, T1.2`. An unclosed
  `(` hides the rest of the line.
- `all prior` means every task that comes earlier in the file.
- Reading stops at the first comma-separated part that does not start with a task id. In
  `T1.1, then T1.2`, only `T1.1` counts.
- Any other text with no task id at all (for example `M3 as available`) is read as **no dependencies**.
  The daemon does not complain, so the checker warns about it (`depends-no-task-id`).

A task with no `Depends on` line at all is an error: the daemon refuses the plan.

---

## 6. Open questions

`## Open questions` holds a table with the columns `Question` and `Needed by`:

| Question | Needed by |
|---|---|
| Which payment provider? | T2.1, T2.3 |
| **Answered 2026-10-14:** the hosting region is Frankfurt. | T1.3 |

- "Needed by" holds the ids of the tasks that need the answer.
- A row is **open** until its Question cell contains `**Answered <date>:** <the answer>`, written into the
  same cell. Answering means editing that cell, not deleting the row.
- Write ids as plain text. `after T2.4` names T2.4, and every id in the cell counts (one written inside
  backticks too). A row whose "Needed by" names no task is never tied
  to a task, so nothing can hold a task back for it; the checker warns.
- A pipe inside a cell is written `\|`, or put inside a backtick code span.

The build daemon holds a task back while an open row names it. It asks the owner in the inbox, one item per
row, and writes the answer into the Question cell as `**Answered <date>:** <answer>`. That is why the
answered marker must be exact.

---

## 7. The slicing rule

A task is a **thin, complete path through every layer the change touches** (schema, logic, interface,
tests) that can be shown working by itself, and is small enough for one fresh agent session.

Groundwork that makes a later task easy comes first, as its own task. A mechanical change that touches
everything at once (a rename) is the exception: add the new form beside the old, move callers over in
batches, then remove the old.

**A good slice.** "A visitor can sign up with an email and see their name on the home page": one column
in the schema, the handler, the page and a test that signs up. It can be demonstrated alone.

**A bad slice.** "Build the whole database layer", then "build all the handlers", then "build all the
pages". Nothing can be shown working until the last one lands, and each is too big for one session. Cut
across the layers instead.

---

## 8. Code fences

A line that starts with three backticks or three tildes opens a fence, and the next such line closes it.
Everything inside a fence is an **example** and is never read: a `#### T9.9` heading or a
`- **Depends on:**` bullet shown inside one is not a task or a dependency. An unclosed fence hides the
rest of the file, so close every fence.

---

## 9. The checker

```
afk plan lint [--project DIR] [--json] [--summary] [FILE]
```

With a `FILE`, it reads that file. With none, it reads the plan the project's `afk.toml` names with
`plan` (`docs/PLAN.md` when it names none), the same file `afk tasks` reads. `FILE` and `--project`
cannot be given together. It prints one finding per line (or JSON with `--json`). `--summary` prints the counts of milestones, tasks, owner tasks, open questions,
errors and warnings.

| Exit code | Meaning |
|---|---|
| 0 | No errors (warnings may be present). |
| 1 | The plan has errors, or could not be read. |
| 2 | Bad usage. |

**Errors** are what the daemon would refuse or misread. **Warnings** are what a reader would miss.

| Rule | Level | Meaning |
|---|---|---|
| `no-tasks` | error | The plan has no task heading. |
| `bad-task-heading` | error | A heading of any level that starts with T or t and a digit but is not a valid task heading (wrong number of hashes, lower-case t, no dash). |
| `duplicate-task` | error | Two headings use the same task id. |
| `no-depends-line` | error | A task has no `Depends on` line. |
| `unknown-dependency` | error | A task depends on an id that is not a task in the plan. |
| `dependency-cycle` | error | Tasks depend on each other in a loop. Only the first cycle is reported. |
| `open-question-unknown-task` | error | An open row's "Needed by" names a task that is not in the plan. |
| `fix-and-owner` | error | A task title carries both `(fix)` and `(owner)`. Keep one marker. |
| `depends-no-task-id` | warning | A `Depends on` line has text but no task id, so it is read as no dependencies. |
| `missing-task-field` | warning | A task has no Goal, Scope or Acceptance. One warning per task, naming what is missing. |
| `missing-section` | warning | A required section, or the "How to use" paragraph, is missing. |
| `open-question-no-task` | warning | An open row's "Needed by" names no task. |

A plan that exists is changed with `afk plan patch`, `afk plan insert` and `afk plan depend`, which run
this checker on the changed text and write nothing when it adds an error. Those commands, the owner
profile and the planning session are not part of this format and are not described here.

---

## 10. A worked example

A small complete plan: two milestones, five tasks, one owner task, one open question and one answered.
The checker reports nothing for it.

<!-- worked-example -->
````markdown
# Notes App Plan

**How to use this document.** Read Context and Decisions first. The Milestones section lists the tasks;
do the one you are given and nothing else. A task's Depends on line lists what must be done before it.
Changes after the first draft are appended as `Amended` lines, never rewritten.

## Context

The owner asked: "I want a small app where I can save notes and find them again."

## Decisions

| Topic | Decision |
|---|---|
| Storage | SQLite file in the project directory. From the owner profile. |
| Interface | A command line, no web page. |

## Architecture

One binary. A `notes` table holds an id, a title, a body and a created time. Commands: `add`, `list`,
`find`.

## Milestones

### M1 — Save and list notes

**Outcome:** a person can add a note and see it in a list.

#### T1.1 — Set up the project and the notes table
- **Depends on:** None
- **Goal:** an empty command line program that opens the database and creates the `notes` table.
- **Scope:** `main.go`, `store.go` and a migration that creates the table.
- **Acceptance:** `go test ./...` passes and running the program twice creates the table once.

#### T1.2 — Add and list notes
- **Depends on:** T1.1
- **Goal:** `notes add "title" "body"` saves a note and `notes list` prints every note.
- **Scope:** the `add` and `list` commands, the store methods behind them and their tests.
- **Acceptance:** a test adds two notes and sees both in the list, newest first.
- **Must not:** change the table from T1.1.

#### T1.3 — Create the release account (owner)
- **Depends on:** None
- **Goal:** the owner has an account on the package host, so releases can be published.
- **Scope:** the owner signs up and saves the access token where T2.2 expects it.
- **Acceptance:** the owner confirms the token is saved.

### M2 — Find and publish

**Outcome:** notes can be searched, and a release can be published.

#### T2.1 — Find notes by a word
- **Depends on:** T1.2
- **Goal:** `notes find word` prints the notes whose title or body contains the word.
- **Scope:** the `find` command, a store method and tests.
- **Acceptance:** a test finds one of three notes by a word in its body.
- **Note:** matching ignores case.

#### T2.2 — Publish a release
- **Depends on:** T2.1, T1.3 (the account)
- **Goal:** a tagged release is built and published to the package host.
- **Scope:** the release script and the steps to run it.
- **Acceptance:** a dry run of the script builds the binary and stops before publishing.

## Deferred

- Editing and deleting notes: not needed for the first version.

## Open questions

| Question | Needed by |
|---|---|
| Should `find` match whole words only? | T2.1 |
| **Answered 2026-10-14:** releases go to the owner's own account. | T2.2 |

## Definition of done

- The task's Acceptance line passes.
- `go test ./...` passes.
- The change is described in the README when it adds a command.
````
