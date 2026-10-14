# afk web page — design follow-up 1

This is a follow-up to the afk web page design system (`docs/design-system.md`). The master plan was
reconciled with it on 2026-10-13. The rule between them: **the plan wins on behaviour, the design system
wins on looks.** Every point below is one where the design shows behaviour the plan does not allow, so the
design has to change. Keep every token, component and class name as it is; change only what each point
says.

For each point: what the design shows, what the plan requires, and what to hand back.

## 1. Chat view: the reply box while the agent works

- **The design shows:** "composer still accepts typing, Send disabled until the agent is done".
- **The plan requires:** while the agent works, the reply box says so and is disabled. Nothing can be
  typed until the agent is waiting for the owner.
- **Hand back:** the `composer` in its working state: textarea disabled, with a short line in it or
  beside it saying the agent is working, and Send disabled.

## 2. Chat view: the second line of the app-bar

- **The design shows:** "New plan · Rounds, 2 of about 4".
- **The plan requires:** only what the session records: the mode (new plan, extend, amend, reconcile) and
  the phase it is in. afk has no "2 of about 4".
- **Hand back:** the line as mode · phase, for example "New plan · Rounds" and "Reconcile · Survey".

## 3. Chat view (desktop): the phase steps

- **The design shows:** `steps` brief, rounds, slices, draft, check, review.
- **The plan requires:** the phases of the session's own mode. A new plan: brief, survey, model, rounds,
  slices, draft, check, review, with a design step between check and review when the project has an
  interface. Extend: brief, survey, rounds, slices, draft, check, review. Amend: brief, survey, draft,
  check, review. Reconcile: brief, survey, rounds, draft, check, review.
- **Hand back:** the `steps` list drawn for a new plan with all nine phases, and one for reconcile, so the
  component is seen at both lengths.

## 4. Plan a new project: when the GitHub question is asked

- **The design shows:** the GitHub `choice-list` inside the form, before anything is created, with "Yes,
  create darin/<name>" listed first, for any folder.
- **The plan requires:** the question comes after afk has made the new folder, as its own step before the
  chat opens; **No** is checked by default; and it is never asked for a folder that already existed.
- **Hand back:** the form ending at **Start planning** (Where, then the folder card), and a separate step
  after it, "Create a private GitHub repository for it?", with No checked and a Continue button. Draw
  also the state when `gh` is missing or not logged in: no question, a line saying why and what to run.

## 5. Plan a new project: the hint under the Where field

- **The design shows:** "A name makes a new folder in ~/code."
- **The plan requires:** the folder comes from the owner profile's project location, which may be
  anything, or may be unset.
- **Hand back:** the hint with the location as a filled-in value, and the error shown when a name is typed
  but the profile has no project location ("Set one with afk profile set, or type a full path").

## 6. Set a task's status

- **The design shows:** a `choice-list` of statuses with the ones afk sets itself disabled, and no place
  for the tasks that replace a superseded one.
- **The plan requires:** every status the command accepts is offered (pending, running, shipping, parked,
  done, failed, superseded), none disabled; picking superseded asks for the ids of the tasks that replace
  it; and the confirm says so when an agent is working on the task.
- **Hand back:** the confirm-sheet with all seven enabled, the superseded state with an `input--mono`
  field for the replacing ids, and the warning line for a task an agent is working on.

## 7. A task's tokens

- **The design shows:** the `split-bar` by phase as plan, build and review.
- **The plan requires:** the token ledger's phases: plan, execute, review, fix, ship.
- **Hand back:** the split-bar and legend with five phases.

## 8. Sessions

- **The design shows:** session rows and a session screen without the loop count or whether the
  session's host is alive, and only the normal running row; the screen has a "Summary".
- **The plan requires:** the loop count and whether the host is alive are shown; the list also holds a
  session whose status file is stale, a one-off session (a terminal with no plan), the planning session
  (which links to the chat view) and a session whose host is dead; the Summary is what the dashboard's
  overview shows (phase, loop count, work-plan progress, review verdict, whether it waits for the owner).
- **Hand back:** the session `kv` with loop count and host alive added, and one row for each of the four
  extra kinds, each with its badge and a line saying what it is.

## 9. Remove a project: the confirm

- **The design shows:** "afk stops watching it. These are not touched: the folder, its plan, its
  sessions."
- **The plan requires:** it also says a running task is left to finish.
- **Hand back:** the confirm-sheet body with that sentence added.

## 10. Approve the plan: the confirm

- **The design shows:** the project, the number of tasks and milestones, the first task, and the
  checker's counts.
- **The plan requires:** the confirm also shows the plan's path.
- **Hand back:** the confirm-sheet with the path as a `path` line.

## 11. Sign-in prompt

- **The design shows:** "This page is private", one line, and a **Sign in** button.
- **The plan requires:** afk has no sign-in. The page is opened with the token, through a one-time visit
  to a link with `?token=` on it.
- **Hand back:** the screen with no button: the heading, one line saying to open the page from the link
  with the token that afk gives, and the meta line. Nothing about projects.

## 12. Sample sentences

- **The design shows:** "Answer sent. T3.4 is ready again." and "Its copy of the project is deleted. The
  summary and token counts stay."
- **The plan requires:** toasts and confirms say only what really happened: a task is "ready again" only
  when no other item holds it, and the clean confirm says what `afk clean` really removes.
- **Hand back:** the inbox toast in both forms ("Answer sent." alone and "Answer sent. T3.4 is ready
  again."), and the clean confirm with its body marked as text filled in by afk.

## What to hand back

One updated `docs/design-system.md`, with the same three parts (tokens, components, screens) and the
changes above in place. Component and class names that already exist stay the same; a new one, if
needed, follows the same naming (`block`, `block__part`, `block--variant`, `is-state`).
