# Design brief: the afk web page

This is a prompt for a design tool. It is complete by itself. Please design a design system and the
screens below for the web page of a tool called afk, and hand back one file, described at the end.

## What afk is and who the page is for

afk is an always-on runner that builds a software project from a written plan while its owner is away.
It picks the next ready task, runs a coding agent through plan, build and review, ships the change on
green CI, paces itself against the agent subscription's usage limits, and only asks its owner for
things only the owner can do. It runs on a machine at home, and the owner looks in on it from anywhere.

The page is for one person: the owner, a hobbyist developer, usually on their **phone**, sometimes at a
desktop. Most visits are short: "does anything need me?", answer a question, approve a plan, pause the
build. Some are long: holding a planning conversation with an agent to plan a new project, from the
sofa. The page does everything afk's terminal dashboard does except show a live terminal.

The tone is calm and plain. It should be glanceable: what needs the owner comes first, and the
state of every build is readable in a second. Plain language everywhere, never jargon in a button.

## The screens

Each screen, what the owner does there, and the task in `docs/PLAN.md` that builds it.

- **Overview (home)**: every project on one list, the ones needing the owner first; each row shows
  tasks done of the total, the current task and its phase or why nothing is running (paused, stopped,
  waiting on you, held for usage, nothing ready, finished), open inbox items and how many hold a task
  up, and "plan waiting for approval". Actions here: plan a new project, add a project, remove one.
  (T8.5, T8.15, T8.16)
- **Plan a new project**: a field for a folder path or a project name, a confirm that shows the full
  path, then a yes-or-no for creating a private GitHub repository, then the chat view. (T8.15)
- **Add a project / remove a project**: a small form (folder, optional name) and a confirm that says
  the folder, its plan and its sessions are not touched. (T8.16)
- **Project**: one project's build: its state, current task, pause and resume, the inbox count, the plan
  line (planning mode and phase, or "plan waiting for your approval"), and links to the task list,
  sessions, inbox, usage, the plan and the chat. Actions: extend, amend or reconcile the plan. (T8.6,
  T8.11, T8.14)
- **Inbox list and inbox item**: the items needing the owner, blocking ones marked; an item in full with
  its options as buttons and a free-text answer. (T8.6)
- **Tasks**: the plan's tasks with id, title, status and unmet dependencies; a task's tokens (totals
  and split by phase and role); "set status" with a list of statuses and a one-sentence confirm.
  (T8.7)
- **Usage**: each usage window with how much is used and when it resets, the governor's state, and a
  "refresh" button with a working state. (T8.8)
- **Sessions list and session**: every session with its phase, progress, review verdict and whether it
  waits for the owner; a session's summary; actions: clean, restart, label, start a new autonomous
  session (name and description), each behind a confirm. (T8.9, T8.10)
- **Plan and checker**: the plan rendered as a long readable document; the checker's counts and
  findings, errors first; "approve the plan" with a confirm showing the counts, absent when there are
  errors. (T8.11)
- **Chat view**: a planning conversation: the agent's messages (rich text: headings, lists, code,
  tables) and the owner's replies; a question with options shown as buttons, the recommended one
  first and marked, plus a field for the owner's own answer; a reply box; "the agent is working"
  and "no planning session" states. This is the screen the owner spends longest on. (T8.12, T8.14)
- **Sign-in prompt and errors**: what someone without access sees, and a plain error page.

Every screen updates by itself; give a quiet way to show that something just changed or is
working, and a way to show a confirm before an action.

## The words the screens use

- **Project**: a folder afk builds, with its plan.
- **Master plan / plan**: the project's list of tasks, grouped in milestones.
- **Milestone**: a group of tasks with one outcome.
- **Task**: one piece of the plan, with an id like `T2.3`; its status is pending, running, shipping,
  parked, done, failed or superseded.
- **Owner task**: a task only the owner can do.
- **Session**: one agent at work on one task, in its own copy of the project.
- **Planning session**: the agent conversation that writes or changes a plan, with modes new, extend,
  amend and reconcile, and phases such as brief, rounds, slices, draft, check, review.
- **Inbox item**: something an agent or afk needs the owner to answer; **blocking** when a task waits
  for it.
- **Park**: a task stopped until the owner answers.
- **Pause / resume**: stop starting new tasks for a project, and start again.
- **Stop the line**: afk stopped a project after a bad deploy until the owner resumes.
- **Ship**: push, pull request, CI, merge and deploy check.
- **Usage window**: a limit of the agent subscription that fills up and resets (session, weekly).
- **Governor**: afk's decision from usage: run, launch nothing new, pause, hold or refuse.
- **Approve**: the owner's go-ahead that turns a reviewed plan into a build.
- **Checker**: the plan check, with errors and warnings.
- **Tokens**: how much of the model a task used.

## Constraints

- **Stack**: served by a Go program as server-rendered HTML (Go `html/template`) with hand-written
  JavaScript and plain CSS built into the program. No JavaScript framework, no build step, no `npm`,
  no web fonts from a third party. Design for plain HTML and CSS: components must be buildable as
  templates and CSS classes.
- **Components**: no component library (the owner's default). Define your own small set.
- **Phone first**: designed at about 390 px wide, and it works well on a desktop, where the extra
  width can show a list beside its detail.
- **Accessibility**: WCAG 2.2 AA: contrast, visible focus, everything usable by keyboard, labels for
  every control, touch targets of at least 24 by 24 px (44 preferred on the phone).
- **Light and dark**: follow the device's setting.
- **Content**: long text (plans, agent messages) must stay readable at phone width; task ids and file
  names appear often and suit a monospace face.

## What to hand back

Exactly one file, saved in the project as `docs/design-system.md`, with three parts:

1. **Tokens**: colours (light and dark), type (faces, sizes, weights, line heights), spacing, radii,
   shadows, given as values a CSS file can use directly.
2. **Components**: each with its name and its states (default, hover, focus, active, disabled, loading,
   error where they apply). The names of the components will be used in the code, so use the names
   the design tool would export.
3. **Screens**: each screen listed above, built from those components, at phone width and, where it
   differs, at desktop width.
