# The Design Phase

This prompt applies to a session that writes a new plan (the mode `new`). The change modes `extend`, `amend` and `reconcile` have no `design` phase.

## Phase `design` — the design hand-off

The file: `docs/design-brief.md`, in the project's `docs` folder and not under `.afk/plan/`, because the owner takes it out of the project and it is committed with the plan. The gate is that `afk plan lint` reports zero errors, the brief exists, the plan's "How to use this document" paragraph states which document wins on what, the plan has a task titled `Design system (owner)` in its first milestone, and at least one task waits for it.

**Who decides.** This phase comes after `check` and before `review`, and it is reached only when the recorded answer to the question with the id `r1-interface` starts with `yes`. `afk plan advance` reads that answer and decides: with `yes` it moves from `check` to `design`, with `no` it moves from `check` straight to `review`. Never argue with it and never try to enter or skip the phase yourself. If `afk plan status` prints `design`, do this phase; if it never does, the project has no interface and there is nothing here to do.

**What this phase is not.** You do not design. No colours, no type, no spacing, no layouts, no sketches in text, no prototype, and no call to a design tool. A design made in a terminal is a guess the owner then has to undo. The owner makes the design system in their own design tool; this phase hands them a good prompt for it and makes the plan wait for the result.

Do the steps in this order. Each one is safe to do again, so a session that resumes here starts at step 1 and skips what is already done.

### 1. Write the design brief

Write `docs/design-brief.md` with Write. It is a prompt the owner pastes into a design tool, so it is addressed to that tool, it is complete without this conversation, and it is plain. It holds, in this order:

- **What the product is and who it is for**, in a short paragraph, from `.afk/plan/brief.md` and the answers in `.afk/plan/questions.jsonl`.
- **The screens the plan implies**, one line each: the screen's name, what a person does on it, and the id of the task that builds it. Take them from the plan's tasks, not from what a product like this usually has. A screen no task builds is not listed.
- **The domain terms**, from `CONTEXT.md`, each with its meaning, so the design uses the project's own words on its screens.
- **The constraints**, from the owner profile (`afk profile`, and `afk profile --kind <kind>` for the project's kind) and the plan's Decisions table: the stack, the component approach (a component library, plain styles, or whatever the entry says), and the accessibility level. For one the profile and the plan do not state, write "not stated in the profile". Never guess it (rule 1).
- **What to hand back**: exactly one file, to be saved in the project as `docs/design-system.md`, with three parts: the tokens (colours, type, spacing), the components (each with its name and its states) and the screens (each one listed above, built from those components). Say that the names of the components will be used in the code, so they should be the names the design tool would export.

No key, token or password goes into it (rule 9), and nothing about how the plan is built that a design tool does not need.

### 2. Check the header states which document wins

The plan's "How to use this document" paragraph, before the first `##` section, must contain the words `docs/design-system.md`, "wins on behaviour" and "wins on looks". The `draft` phase writes the sentence for a project with an interface, so it is usually there:

```
Where this plan and `docs/design-system.md` disagree, the plan wins on behaviour and the design system wins on looks.
```

Read the paragraph. When the sentence is missing, or lacks one of those three, add it with `afk plan patch` (rule 5): the old file holds the paragraph's last sentence, the new file holds that sentence followed by the one above. Do not reword a sentence that already has all three.

### 3. Add the owner's design task

1. Find the plan's first milestone: the milestone of the first task in the file, which is the number in the first `### M<n>` heading of the Milestones section. It is not always 1.
2. Run `afk plan next-id --milestone <that number>`. It prints the id; use it as printed (rule 6, rule 8).
3. Write the entry to a file in a scratch folder outside the project (`mktemp -d`), in the format of `plan-format.md`:

   ```
   #### <id> — Design system (owner)
   - **Depends on:** —
   - **Goal:** the project has a design system, made by the owner in their design tool, that every task with an interface builds from.
   - **Scope:** Take `docs/design-brief.md` to <the design tool named in the profile>, make the design system there, and save the result at `docs/design-system.md`.
   - **Acceptance:** `docs/design-system.md` is committed and holds the tokens, the components and the screens.
   ```

   The title is exactly `Design system (owner)`: the gate of this phase and afk's build loop both recognise the task by that title, and by nothing else. When the profile names no design tool, write "your design tool" in the Scope; do not pick one. It depends on nothing, so the owner can start at once.
4. Run `afk plan insert --milestone <that number> --entry FILE`.

If the plan already has a task with that title (a session that resumed), do not add another; use its id in the next step.

### 4. Make the interface tasks wait for it

Go through every task in the plan and decide one thing about each: does it build something a person looks at (a page, a screen, a component, a layout, styles)? For each one that does, and for no other, run:

```
afk plan depend <task> <the design task's id>
```

A task that builds no interface (the project set-up, the CI workflow, storage, a background job, an account the owner creates) does not wait: it can be built while the owner designs, which is the point of adding the task now and not earlier. A task that builds both a page and what is behind it waits, because its page cannot be built without the design. Keep a list of which tasks wait and which do not; step 6 gives it to the owner.

### 5. Check and move on

Run `afk plan lint`. Fix any error with `afk plan patch`, one error, one patch, one run, as in the `check` phase. While the plan has an error, `afk plan status` prints `check` and not `design`, because the checker's verdict is the gate of both; that is right, `critic.md` is still there and nothing else is redone, and fixing the error brings the session back here.

Then run `afk plan advance`. When it refuses, it names what is missing (the brief, the sentence in the header, the task, its milestone, or a task that waits for it). Do that step and run it again.

### 6. Tell the owner

In plain words, before the `review` phase prints its summary:

- Where the design brief is (`docs/design-brief.md`), and that it is a prompt to paste into their design tool.
- That the task with the id you were given, "Design system (owner)", is theirs: no agent does it.
- Which tasks wait for it, and which do not and so can be built while they design.
- That once the plan is being built, afk asks for the design system in their inbox, and that the item gives the steps: save the design system at `docs/design-system.md` and commit it, run `afk plan reconcile docs/design-system.md` and see that session through, and only then answer the item. Answering is what lets the interface tasks start, so the reconcile comes first.

Then carry on to `review`.
