# Six-Section Prompt Template

## 1. TASK
[Quote the EXACT task description from the plan. Include the task ID (WN.TM), title,
What to do, Must NOT do, Files, and Acceptance Criteria verbatim.]

## 2. EXPECTED OUTCOME
- [ ] Files created/modified: [exact paths from plan's Files section]
- [ ] Functionality: [what the code must do, derived from Acceptance Criteria]
- [ ] Verification: [specific command or check to confirm correctness]

## 3. REQUIRED TOOLS
- read: [which existing files to examine for patterns before writing anything]
- glob/grep: [what to search for to understand conventions]
- write/edit: [files to create or modify]
- shell: [commands to run for verification]

## 4. MUST DO
- Follow patterns established in [reference file] — read it first
- Append findings to notepad: `.afk/{plan-name}/learnings.md`
- Append decisions to: `.afk/{plan-name}/decisions.md`
- Append issues to: `.afk/{plan-name}/issues.md`
- NEVER overwrite notepad files — always append
- Run verification command before declaring done

## 5. MUST NOT DO
- Do NOT modify files outside: [list from plan's Files section + test files if applicable + `.afk/{plan-name}/*.md` notepad files]
- Do NOT add dependencies without explicit justification
- Do NOT leave stubs, TODOs, or placeholder implementations
- Do NOT skip the notepad append step

## 6. CONTEXT
### Working Directory
[absolute path to project root]

### Notepad Paths
- READ: `.afk/{plan-name}/*.md`
- WRITE: Append findings to appropriate notepad (never edit/overwrite)

### Inherited Wisdom
[From notepads: conventions discovered, gotchas found, decisions made in prior tasks.
If notepads are empty: "No prior wisdom — you are the first task."]

### Dependencies
[What prior tasks in this plan built that this task depends on. E.g.,
"W1.T1 created the schema — use it as-is, do not modify it."]

### Input Relay Protocol (NEEDS_INPUT)
Your parent session is interactive and can relay questions to the user.
If this task requires a decision or information that ONLY the user can
provide (a choice the plan leaves open, a credential, an ambiguity the
notepads don't resolve):

1. **Surface it BEFORE substantive work.** Scan the task for user-only
   decisions FIRST, before reading code or making changes. If one exists,
   stop and return immediately — a restart is cheap before work begins.
2. Return a final message whose FIRST line is exactly `NEEDS_INPUT`,
   followed by:
   ```
   question: <one-line question for the user>
   context: <what is blocked and why the answer is needed>
   options:
   - <option 1> — <implication>
   - <option 2> — <implication>
   ```
   (2–4 options when the answers are enumerable; omit the options block
   for open-ended questions.)
3. If the need is only discovered mid-work: append your progress so far to
   the notepads (so the restart loses nothing), then return `NEEDS_INPUT`
   the same way.
4. If a `### Relayed Answer` subsection is present below, a previous
   attempt already asked. Treat it as the user's binding answer and do
   NOT return `NEEDS_INPUT` for the same question again.

Do not use `NEEDS_INPUT` for anything you can resolve yourself from the
plan, the codebase, or the notepads.

If the delegation states autonomous mode, never return NEEDS_INPUT: decide, and record the decision and reason in decisions.md.

### Relayed Answer
[Present ONLY on re-delegation after a NEEDS_INPUT round-trip: the
question asked and the user's answer, both verbatim. Omit this
subsection entirely on first delegation.]
