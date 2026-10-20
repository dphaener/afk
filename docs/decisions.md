# Decisions

Choices made while building afk that `docs/PLAN.md` did not make. Newest task last. A later task should read the entry for any task it depends on.

## T0.1

T0.1 seeded this repo from forge-cli and renamed it. These are the decisions the master plan left open.

### Assumptions behind the rename

- **Case.** The name is always lowercase `afk` in prose and on screen, even at the start of a sentence (`afk Dashboard`). Environment variables become `AFK_`. Go and JavaScript identifiers become `Afk` or `afk` as the language needs (`AfkDir`, `afkRepo`).
- **One name.** `forge-cli` became `afk` everywhere it was a marketplace, repo or install name, so the plugin id is `afk@afk` and the install marker file is `.afk-install`.
- **Codename.** The heavy-tier codename `Forgemaster` became `Ironmaster`. It keeps the smithing theme of Spark, Smith and Oracle, and a codename is not a provenance note, so it could not stay.
- **Reworded words.** Words that only contain the letters "forge" (forget, forgets, forgetting, forgery, fire-and-forget) were reworded in comments with the same meaning, so a plain search for the old name finds only real mentions.
- **Allowed mentions.** The old name stays only in `docs/PLAN.md`, `docs/provenance.md`, this file, `plugins/afk-opencode/PROVENANCE.md`, the bootstrap comment and `.forge/` line in `.gitignore`, and one provenance sentence each in `README.md` and `CLAUDE.md`.
- **Ignored state.** `.gitignore` ignores both `.afk/` and `.forge/`, because tasks before T0.2 still run as forge sessions and their state must never be committed.
- **Test goldens.** Fixtures named after the old project were renamed like everything else. UI golden files were regenerated with `-update` and diffed against the originals: only the renamed text and the padding that follows from its length changed.
- **CI.** One workflow, `.github/workflows/ci.yml`, on `ubuntu-26.04`, runs `make check`. `make check` gained `plugin-test` and `hook-test`, so local runs and CI cannot drift apart.
- **What agents did not run.** The checks that need the VM (`afk plugin install --target claude`, `afk new --scratch` opening a pane in `afk dash`) were not run by agents, because they change the owner's real environment. They are owner checks. `afk new --dry-run --base main x` was run against a binary built outside `$PATH`.
- **Tests.** No new tests were added. The existing suites, renamed, were the safety net, with a baseline run recorded before any rename.

### Names the map did not cover

- `FORGE-E2E-OLD` and `FORGE-E2E-RESUMED` test banners became `AFK-E2E-OLD` and `AFK-E2E-RESUMED`.
- The hook shell variable `FORGE_DIR` became `AFK_DIR`, and the hook log variable became `AFK_HOOK_LOG`.
- The notification title `Forge: {plan}` became `afk: {plan}`, and the default session title `forge session` became `afk session`.
- Git test identities and fixtures: the git user `Forge Test` became `afk Test`, and fixture repo paths ending in `forge-cli` became `afk`.
- Plugin ids in fixtures: `forge@claude-marche` and `forge@marche` became `afk@claude-marche` and `afk@marche`. The plugin name changed, the marketplace names did not.
- In the OpenCode port, "Forge Model Registry", "Forge Conductor" and "Forge Orchestrator" became "afk ..." by the display rule, and `non-forge` became `non-afk`.
- `plugins/afk-opencode/PROVENANCE.md` keeps every upstream name, path and SHA as written. A note at the top says so, and only the glossary column that describes this port's own current names was renamed. Its "forge_workflow contract" and Phase 0 sections still show the old tool names on purpose, as the historical record.

### Expectations whose width or order changed

- `afk Dashboard` is 2 cells shorter than the old header badge, so in `internal/ui/emptystate_test.go` the padding literal `strings.Repeat(" ", 25)` became 27 (the header is still padded to width 40), and the truncation case at width 10 now expects `afk Dashb…`.
- In `TestRenderHeaderIsExactlyOneRow` the aggregate-rung width went from 71 to 69 (and the named rung's natural width from 72 to 70), so the test still sits one cell below the rung it is checking. The assertions are unchanged.
- Case-sensitivity fixtures keep mixed case rather than the lowercase display rule, because lowercasing them would erase what they test: `internal/project/project_test.go` uses `Afk` and `afk`, and `internal/provider/provider_test.go` uses `"Afk Plugin\n"`.
- Near-miss name fixtures in `internal/pluginstall/pluginstall_test.go` became `afkish@x` (has the prefix but is not `afk@`) and `partafk@openai-curated-remote` (suffix match). Both must still not be reported.

### Known issues carried forward

- Because the name is lowercase everywhere, some prose in the plugin reads oddly (for example a sentence that starts "afk is a ..."). That is the display rule at work, not a missed rename.

## T0.2

T0.2 grafted shipit's strengths onto the afk plugin and made the task cycle lean (PLAN §5.7). These are the decisions the master plan left open, numbered as in the work plan's assumptions (A1 to A27).

### Not verified, owner check

- **shipit was not readable (A1).** It is not on this machine and the note the session description promised was missing. The QA scenarios (tool, steps, expected, evidence path) and the hands-on QA check were built from the wording of `docs/PLAN.md` T0.2 and §5.7, not from shipit's text.
- **The supervised autonomous run was not done (A14).** T0.2's acceptance asks for one small real task, run in a scratch repo, ending `phase: done` with evidence files, a verdict file and no terminal questions. Agents did not run it, because it installs the plugin into your agent CLI config and spends real usage. Please do it:
  1. `make install`, then `afk plugin install --target claude`.
  2. Make a scratch repo: `git init scratch && cd scratch`, add a small file such as a README, commit it, and give it a `docs/PLAN.md` holding one tiny task.
  3. Run `afk new --autonomous --description "Implement T1.1 from docs/PLAN.md" t1-1` and leave it alone.
  4. When it finishes, check that `.afk/t1-1/status.json` says `"phase": "done"`, that `.afk/t1-1/evidence/` holds files, that `.afk/t1-1/review-verdict.md` exists with `Compliance`, `Quality` and `QA` lines, and that the session never asked a question in the terminal.
  5. Also check that nothing was pushed and no PR was opened, and that `.afk/t1-1/decisions.md` records the choices it made.
  Until this is done, do not treat T0.2 as proven end to end.

### What was built, and why

- **`afk plan check` (A2, A3, A17, A26).** A new package `internal/plancheck` (`internal/state` stays read-only). The agent whose result settles a task runs `done` as its last command (the verifier on PASS when one runs, else the task agent); a workflow script cannot run commands, so no bookkeeping agent exists. The conductor marks `failed` and blocked tasks from the run's result and reconciles `passed`. A failed line keeps its title (`- [!] (ID) title FAILED: reason`), needs a one-line reason, and `done` over it restores the title. `failed` on a task already `[!]` does nothing, so a re-run cannot replace the real reason or duplicate the issues entry.
- **QA scenarios and file references (A4, A21, A22).** `plan_to_args.rb` rejects a task without them; the Go parser stays lenient so the dashboard still shows a plan the engine would reject. Scenarios are read line by line, not from the joined value. A directory may be listed with a trailing `/` beside at least one exact file.
- **Three checks (A5, A6, A20, A25).** Compliance (which now includes scope), quality, and hands-on QA. A critic runs only on a check with a blocker or major finding, sees only those findings, returns the surviving subset and never adds one. The script computes every verdict from the final findings. Go, the dashboard, goldens, fixtures and prose changed together.
- **Deleted scope prompt (A18).** `check-scope-fidelity.md` was removed in the same wave as the line that injects it, so the size check never saw a missing file.
- **Marking rules (A19).** An agent marks `done` only under the script's own pass condition, never from a fix prompt, so a crash cannot leave a false `[x]`.
- **Size cap (A7, A12).** `autonomous.md` kept its title and §7.1 to §7.5 numbering (a new §7.6 was added). Planner additions live in `planner/prompts/project-docs.md`. `check-cat-sizes.sh` and a Ruby test (`plugins/afk/scripts/test/plan_to_args_test.rb`) joined `make plugin-test`, with an explicit Ruby step in CI.
- **Autonomous policy without an inbox (A8).** Rules 2, 5 and 6 of PLAN §5.5 record to `.afk/{session}/decisions.md`. Rule 3 parks with the schema's existing `awaiting_input` write, a toast and a plain statement of what you must do, then ends the turn. T1.2 adds `afk inbox add` and must replace this in the prompt.
- **Stop before shipping (A9, A24).** Autonomous mode commits and writes `phase: done`, with no push and no PR; at the cap it commits and writes `failed`. Default mode keeps its draft PR. Ship instructions in the session description run after the commit and before the `done` write, because `done` is terminal.
- **Rate-limit marker (A10).** `.afk/{session}/.rate-limited`, written by the `StopFailure` hook and cleared by the next prompt or a normal stop. It mirrors the `.background-tasks` sidecar.
- **Planner documents (A11).** The planner writes glossary terms to `CONTEXT.md` and the task's entry in `docs/decisions.md`; the conductor amends the master-plan entry before the final commit.
- **Version (A13).** Both plugin manifests are 4.0.0, since old-format plans are now rejected.
- **Tests and prerequisites (A15, A16).** Tests were written with each change, red first where a test pinned the old behaviour. No prerequisites were missing.
- **Planning process (A27).** The work plan went through three review passes; the one critical finding left in pass 3 was fixed, so none remained open.

### Accepted risks

- A task agent ticks its checkbox just before its result reaches the script, so a crash in that instant can leave a `[x]` the script never confirmed. The conductor's reconcile step corrects it when the run returns; nothing corrects it if the run never returns.
- Two hook behaviours cannot be checked offline and are for T2.4 to confirm against a real limit: that a normal `Stop` does not also fire on a rate-limited turn (it would clear the marker at once), and that `StopFailure` fires when the agent CLI waits out a limit by itself.
- A verdict file written before this change, with a `- Scope:` line, shows as a parse error in the dashboard.

### Things a later task should know

- **OpenCode lags.** `plugins/afk-opencode` still has the old checks, the bookkeeping agent, six-subsection plans and the old autonomous prompt. `PROVENANCE.md` records it; the resync is deferred (PLAN §9).
- **A title containing `FAILED:` cannot round-trip** through `afk plan check`, because the line has no escaping and the parser splits at the first `FAILED:`.
- **The Go plan parser ignores code fences; `plan_to_args.rb` does not.** A task line at column 0 inside a fence counts as a task to the dashboard. `afk plan check` follows the Ruby reading.
- **`[policy] notes` (A8 context).** Until T1.1's loader exists, the agent reads `afk.toml` itself. The notes win over autonomous rules 2 to 6 only, never over rule 1, the schema, the gates or the cap.
- **Master-plan amendment** runs in both modes whenever the description names a master-plan task, and is skipped when the work is not approved.
- **Size headroom.** `conductor/SKILL.md` ends near 27,976 of 28,000 characters and `planner/SKILL.md` near 27,940; expect to trim before adding to either.
- **Known leftovers.** The fix agent may still flip a checkbox by hand in `conductor/SKILL.md`, and `go/SKILL.md` line 73 says the conductor opens a draft PR without qualifying it as default mode.

## T0.3

T0.3 closed two gaps against shipit (failure scenarios, exploratory QA) and fixed two defects T0.2's review reported (the critic could add findings, the Go plan parser read task lines inside code fences). These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **How the critic's findings are matched (A1, A2).** Findings have no id, and the critic is allowed to correct a task ID, so matching on text would drop a finding the critic legitimately refiled. The review script now numbers the findings it hands the critic and the critic returns the number with each one it keeps. From a kept finding the script takes the original text, the critic's task ID if it gives one, and the lower of the two severities. A finding with an unknown, missing or repeated number is dropped. So the critic can confirm, downgrade, refile or reject, and nothing else.
- **`Case` rules (A3).** Every scenario needs `Case`, and it must be exactly `happy` or `failure`. A task always needs one `happy` scenario. The waiver is its own line, `No failure case: {reason}`, inside the task's QA Scenarios, and the reason cannot be empty. Errors about a single field are reported before errors about coverage.
- **A failure scenario and a waiver together (A4)** are accepted. It is redundant, not wrong, and rejecting it would fail a whole unattended session for nothing.
- **The Go parser (A5)** reads `Case` as free text and stores the waiver's reason on the task. It checks neither, so the dashboard still shows a plan the engine would reject. It has to recognise the waiver line, or the line would be read as more of the last scenario's evidence path.
- **Code fences in the Go parser (A6).** A fence now hides everything inside it: task lines, wave headings and section headings. A new task no longer resets the fence state. An unclosed fence hides the rest of the file. This is how `afk plan check` and `plan_to_args.rb` already read a plan, so all three agree.
- **Version (A7).** Both plugin manifests are 5.0.0, because a plan written for 4.0.0 (no `Case`) is now rejected.
- **Where the planner text went (A8).** `planner/SKILL.md` had 60 characters of room, so the rule lives in `plan-format.md` and the skill file only gained the template line and table wording, paid for by trimming.
- **This task's own work plan (A9)** uses `Case` on every scenario and no waiver line. The plugin that ran this session was the installed 4.0.0 copy, whose parser tolerates `Case` but would have rejected a waiver line.
- **Shipping (A10).** The session description asked to push, open a draft PR, mark it ready, wait for CI and merge on green. The conductor did that as written. The ship prompt says never to mark a PR ready, but that guards against doing it unasked, and the master plan's bootstrap section prescribes this exact instruction.
- **`make install` (A11)** is run by the conductor after the review approves, never by a task agent, because `~/bin/afk` is shared by every worktree.
- **Tests and prerequisites (A12, A13).** Tests were written with each change, red first. Nothing had to be in place before the work started.

### Things a later task should know

- **OpenCode lags further.** `plugins/afk-opencode` has no `Case` field, no failure-scenario rule, no exploratory QA and no guard on the critic. `PROVENANCE.md` records it.
- **An old work plan is rejected.** A plan under `.afk/plans/` written before this change has no `Case` lines; `plan_to_args.rb` refuses it and says which task and field.

### Decided while building

- **A fence also hides a title.** The Go parser checks for a fence before it looks for the plan's `# ` title, not only before sections, waves and tasks. A plan whose only title is inside a fence now fails with "missing its top-level title". Fences are tracked outside the Waves section too.
- **The waiver line is matched loosely.** Both parsers accept it with or without a leading `- `, in any letter case, and the last one wins if a task has several. In `plan_to_args.rb` an empty reason is an error even when the task also has a failure scenario. In Go a waiver before the first scenario is still read, and a wrapped second line of the reason is dropped.
- **The two waiver patterns must stay the same.** The Go pattern and `QA_WAIVER_LINE` in `plan_to_args.rb` were written to match; change one, change the other.
- **A fence inside a wrapped value.** A fenced line used to end a wrapped `**Files**` or `**Depends on**` value in the Go parser. It is now skipped, so the value stays open across the fence. No real plan does this and it is not tested either way.
- **The critic's reply.** A number that is not a whole number, or an empty entry, is dropped like an unknown number. A severity the script does not recognise keeps the original.
- **The hands-on QA prompt** now has four duties in order: build fresh and run from a clean state, run every scenario including the failure ones, explore (tasks together, then edge cases), and run the full suite once.
- **The worked example in `plan-format.md`** uses the waiver on its tests-only task and gives every other task a failure scenario.

### The supervised run (owed from T0.2)

This session was the first task run with afk's own plugin: `afk new --autonomous` with the T0.3 description, started on 2026-10-11 and left alone.

- **It asked nothing at the terminal.** Planning recorded 13 assumptions, one of them marked as a decision that would normally have gone to the owner (how to ship).
- **Evidence was saved.** Thirteen scenario evidence files under `.afk/t0-3/evidence/`, plus the QA check's own under `evidence/qa/`.
- **A verdict file was written**: APPROVE, with `Compliance`, `Quality` and `QA` all PASS and four minor findings. No critic ran, because no check reported a blocker or major finding.
- **Execution took one run**: six tasks, all passed first time, eleven agents.
- **One checkbox mark was missed.** The run reported W1.T2 as passed but its line was still unticked. The conductor's reconcile step ticked it with `afk plan check`, which is what that step is for.
- **The known flaky test failed once.** `TestHostLifecycleEndToEnd` in `internal/host` failed in the QA check's first full run and passed when run again. This change does not touch that package (see `## T0.1`).
- **What this run did not prove.** It ran on the installed 4.0.0 plugin, so the new rules were covered by tests and a scratch-plan check against a freshly built binary, not by a whole session. The first session started after `afk plugin install` will be the first to plan with `Case` fields, run the exploratory QA prompt and use the guarded critic.

### Not verified, owner check

- **Install the new plugin.** `make install` updates the binary only. Run `afk plugin install --target claude` (and any other target you use) so new sessions get plugin 5.0.0. The conductor did not run it, because it changes your agent CLI's configuration and would swap the plugin under the running session.
- **Watch the next session's QA check.** Confirm that it starts from a fresh build, runs the failure scenarios, and saves an evidence file for anything it reports from the exploratory step.

## T0.4

T0.4 fixes the flaky `TestHostLifecycleEndToEnd`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Cause before fix (A1).** The first task reproduces the failure and proves the cause with a run. Nothing is fixed from reading the code alone. The fix follows the written diagnosis, wherever it points.
- **Shape of the fix (A2).** The expected fault is in the attach client: it sends the detach message and only afterwards notes that the user asked to detach, so the host's close can be read first and reported as a dropped connection. The fix notes the user's intent first. The host still closes the connection on a detach, and the message format does not change.
- **How it is pinned (A3, A8).** A test forces the bad ordering on purpose instead of waiting for it to happen. It is written first and must fail on the old code.
- **The dashboard pane is in scope (A4).** It sends the detach message and records the reason in the same order as `afk attach`. It gets the same test and, if the test shows the wrong reason, the same fix.
- **Load and long runs (A5).** CPU load comes from shell busy loops, two per CPU, because no stress tool is installed. The 500 and 200 runs are split into batches of 100 so no command runs longer than about four minutes.
- **Which notes go (A6).** The T0.1 bullet in this file and the clause in T0.1's amendment in `docs/PLAN.md` are removed. The line under `## T0.3` about the test failing once stays, as a record of that run. `CLAUDE.md` never had a note about this test.
- **Shipping (A7).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green, with a merge commit. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **Prerequisites (A9).** Nothing had to be in place before the work started.

### The cause

- **What happened, step by step.** When you press ctrl+\ to detach, the client sends the host a detach message, and only after that write returns does it write down "the user asked to leave". The host answers the detach message by closing the connection with nothing in front of the close, which is how a client tells "detached" from "the session ended". Two goroutines then race: the one reading from the host sees the close and records "connection dropped", and the one that sent the detach records "detached by the user". Only the first is kept. Most of the time the sender wins; sometimes the host's close is read first.
- **Where the fault was.** In the client side of the connection, not the host and not the test. It was proven by runs: 95 failures in 650 runs of the test on the unchanged code; a 20ms pause between the send and the record failed 20 of 20; recording before sending failed 0 of 300 with the host untouched. There were two copies of the fault: the attach client in `internal/host/client.go`, and the dashboard's pane (`hostChild.Close` and `Read`) in `internal/ui/hostpane.go`.
- **Could a user have hit it?** Yes, through `afk attach`. It goes through the pane code, not `host.Attach` (which nothing in afk calls). Pressing ctrl+\ could print `afk attach: connection dropped` and exit with code 13 instead of exiting 0. Measured on the shipped code against a real host: 10 wrong in 2000 detaches, about 0.5%. The session was never harmed and kept running. The dashboard stored the same wrong reason but never showed it, because closing a pane clears it and the late message is ignored.
- **What the fix does.** Both places now check whether the user asked to leave when they read the close. The attach client sets a flag just before each detach send (ctrl+\ and end of input), and its reader reports "detached" when the flag is set. The pane's `Read` consults the `closed` flag that `Close` already set before writing the detach. A close with no such request is still reported as a drop, and a `Stolen` or `Exit` frame that arrives first still wins. The host and the message format are unchanged.
- **Tests that pin it.** `TestAttachDetachIsNotReportedAsDropped` in `internal/host/client_test.go` (rows for ctrl+\, end of input, and a control row where no detach was asked for, which must still read as dropped) and `TestHostChildDetachReason` in `internal/ui/hostpane_test.go`. Both force the bad ordering with a wrapped connection instead of waiting for it, were red on the old code and are green on the new. `TestHostChildWriteFrameTimesOutInsteadOfBlockingForever` also asserts that a failed live write still reads as a drop. `TestHostLifecycleEndToEnd` itself is unchanged.

### Decided while building

- **The fix is in the readers, not in the order of statements.** Reporting "detached" before sending would let `attach` return and close the connection while the detach message was still unsent. For the pane, moving the exit mark ahead of the write would also tell `Wait` the connection had ended too early. A recorded intent that the reader consults avoids both.
- **A flag, not a lock.** The attach client uses one atomic boolean because it has one writer and one reader.
- **A failed detach write reads as "detached".** The user asked to leave, so that is the true answer; the session keeps running either way. A failed write of ordinary typed input still reads as a drop.
- **A `Stolen` or `Exit` frame wins over a close the user just asked for,** because it is true and more useful.
- **Forcing the ordering needs two waits in the client test.** Waiting for the read error is not enough, since a gap remains before the reader reports; the wrapper also waits for the connection to be closed by the client's teardown. The wrapper counts how often it forced the ordering and the detach rows assert it did.
- **A detach within about 25ms of attaching hides the race,** because the host is still busy with its attach-time resize. A test must force the ordering.
- **Not verified against a built binary.** It needs a terminal and a live agent session. The owner's check is: attach, press ctrl+\, expect `detached` and exit code 0.
- **Known gaps.** No test drives `ui.AttachPane` to a user detach against a real host. The cross-process rate is unmeasured.
- **README unchanged.** It already describes ctrl+\ as exit 0 and code 13 as a drop; the fix makes the behaviour match.

## T1.1

T1.1 adds the master-plan parser, the ledger, the next-task function and the commands `afk tasks`, `afk next` and `afk ledger set`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where the nebari fixture came from (A1).** Nebari is not on this machine. Its `docs/PLAN.md` was fetched from the owner's private GitHub repo at commit `4b193d1` and committed unchanged as `internal/masterplan/testdata/nebari-plan.md`. It has 40 task headings and 40 dependency lines.
- **How a dependency line is read (A2).** Text in parentheses is dropped first. What is left is split on commas, and each part gives the task id it starts with. Reading stops at the first part that does not start with an id. Only the first `- **Depends on:**` line under a heading is read, so a line that wraps loses nothing but prose.
- **Lines with no task ids (A3).** `—`, `None` and an empty value mean no dependencies. `all prior` (nebari's T9.2) means every task earlier in the plan. Any other text with no ids (`M3–M8 as available`, `nothing new; it can run at any point`) means no dependencies, and `afk tasks` prints the text as a note so you can see it was not understood as ids.
- **A plan with a mistake stops the command (A4).** A task heading that does not fit the pattern, an id used twice, a task with no dependency line, and a plan with no tasks are errors with a line number. A dependency on a task that does not exist, and a cycle, are errors too. A runner that guessed here could start a task too early.
- **The ledger file (A5).** `.afk/build/ledger.json` is `{"tasks": [...]}`, one entry per task, sorted in id order. `pr` is the pull request number. Times are RFC 3339 UTC. Empty fields are left out. A key or a status afk does not know is an error.
- **A damaged ledger is an error, not an empty ledger (A6).** Only a missing file means "nothing recorded yet". Reading a damaged file as empty would make every finished task look pending and run again.
- **Which directory (A7).** The commands use `--project`, or the current directory. If that directory is a git worktree of a project, they use the project's main checkout, because that is where the daemon keeps its state and reads the plan. So `afk tasks` inside a session's worktree shows the plan on the main checkout, not the session's edited copy.
- **`afk ledger set` (A8).** The id must be a task in the master plan, which catches typing mistakes. `superseded` needs `--replaced-by` with the ids that replace the task. Any other status removes `replaced_by`. Nothing else in the entry changes, and running the same command twice changes nothing.
- **One task at a time (A9).** If any task is `running` or `shipping`, `afk next` says so and names no task, because v1 runs one task at a time.
- **Parked tasks before the inbox exists (A10).** The next-task function is told which inbox items are still open. Until T1.2 adds the inbox, the commands treat every item as open, so a parked task stays parked. A parked task that names no items also stays parked; set it back to `pending` by hand to run it.
- **`afk next` output (A11).** One line: the task id and title, or the reason there is none. Exit code 0 for both, 1 when the plan, ledger or `afk.toml` cannot be used, 2 for a mistyped command.
- **`afk.toml` (A12).** Read with the `github.com/BurntSushi/toml` library, the first TOML code in the repo. The loader reads `plan` and ignores everything else, so a file written to the master plan's full example still loads. No file, or no `plan` key, means `docs/PLAN.md`. The path must stay inside the project.
- **Names (A13).** Packages `internal/masterplan`, `internal/ledger`, `internal/nexttask` and `internal/projectconfig`.
- **The plugin is unchanged (A14).** Agents still read `[policy] notes` from `afk.toml` themselves; the loader does not expose it yet.
- **Tests and prerequisites (A15, A16).** Tests are written with each change. Nothing had to be in place before the work started.
- **Shipping (A17).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green, with a merge commit. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **`make install` (A18)** is run by the conductor after the review approves, never by a task agent.
- **The fixture names an AI vendor (A19).** Nebari's plan mentions one in its own prose. The rule against provider names outside `internal/provider` is about afk's code, so the fixture stays as it is.

### Not verified, owner check

- **Before afk is made public,** decide whether nebari's plan may be published with it. It is committed here as a test fixture while both repos are private.

### Decided while building

- **The TOML library is version 1.6.0.** That unknown keys and tables are ignored was confirmed by tests against the master plan's full `afk.toml` example, not by reading the library's source.
- **A ledger file that cannot be read at all** (wrong permissions, a directory in its place) is an error but is not reported as "damaged", so the advice to fix or delete the file is only given when its content is wrong.
- **Key names in the ledger are matched without regard to letter case** (`"Status"` loads as `status`), because Go's JSON reader works that way. The next write puts it back in lowercase.
- **`afk tasks` and `afk next` can disagree in one hand-made case.** If a superseded task's `replaced_by` names a task that is not in the plan but is recorded as done, `afk tasks` shows its dependants with nothing unmet while `afk next` will not start them. `afk ledger set` refuses to write such an entry, so it takes a hand-edited file.
- **"Stuck"** is the reason `afk next` gives when no task can ever become ready: a superseded task with nothing replacing it, or replacements that are not in the plan.
- **The review** approved with all three checks passing and no blocker or major finding, so no critic ran. Its minor notes are the case above, that two tasks wrote tests alongside the code and not before it, and that this file and `CONTEXT.md` were edited outside the work plan's tasks (the planner writes both, by design).

### Folded with T1.2 at merge

T1.2 (the inbox) merged while this task was in review, and both had written the same two things. They were joined when `main` was merged into this branch, after the review had approved.

- **One `afk.toml` reader.** Both tasks had created `internal/projectconfig`, T1.2's for `[notify] ntfy_url` and this one for `plan`. There is now one `Config` with both, read in one pass. Each side's checks and messages are kept. A project with no `afk.toml` now gets the default plan path where T1.2's reader returned an empty config. Where the two worded the same failure differently (a file that cannot be read, invalid TOML), this task's wording is used.
- **A mistake anywhere in `afk.toml` now stops every reader.** A bad `ntfy_url` makes `afk tasks` and `afk next` stop with the message, and a bad `plan` makes `afk inbox add` skip its notification (the item is still added). Fix the line the message names.
- **One `mainCheckout`.** Both tasks had written it. T1.2's version in `maincheckout.go` is kept, with its tests, and this task's copy is gone; `resolveBuildRoot` calls it.
- **Not reviewed by the three checks.** The join was made after the verdict. It was checked with the full `make check` and by CI, not by a second review pass.
- **`afk next` does not read the inbox yet.** The inbox now exists, but the commands still treat every item as open. Wiring the real answer in is T2.1's.

### Things a later task should know

- **Seed afk's own ledger before the daemon runs here.** With an empty ledger `afk next` on this repo names T0.0, which is an owner task. Mark the finished tasks done with `afk ledger set` first.
- **Nebari's T9.1** says "M3–M8 as available". afk reads that as no dependencies and `afk tasks` prints a note. Listing real task ids there (at T4.1) would stop it being picked early.
- **Ideas left out on purpose:** `--json` output for `afk tasks` and `afk next`, and `afk ledger set` taking several ids at once.

## T1.3

T1.3 adds the usage probe, the governor's decision and the `afk usage` command. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The `Usage` method takes its runner (A1).** The master plan shows `Usage(ctx)`. It is built as `Usage(ctx, UsageDeps)`, where `UsageDeps` holds the function that runs the CLI and the clock. Every other provider call that reaches outside takes its runner the same way, and it is what lets the tests run without any agent CLI installed.
- **What a window carries (A2).** Besides its kind, percentage and reset time, a window has a `scope`: the model's name on a per-model weekly row. The CLI's `weekly_all` becomes `weekly` and `weekly_scoped` becomes `weekly_model`. A kind afk does not know is kept as `unknown` with the CLI's own word in `scope`, because dropping it could hide a limit.
- **Odd or missing data (A3).** No report in the stream, empty limits, or no readable row all mean "no usage report", which is an error and not a snapshot. A row without a numeric percentage is skipped. Percentages are read as 0 to 100 and never guessed to be fractions. A reset time can be ISO text or epoch seconds; one that cannot be read is left unknown and the row is kept. A missing extra-usage block reads as off.
- **Where the decision lives (A4).** A new package, `internal/governor`, with one pure function. Its answer is one of `run`, `no_launch`, `pause`, `hold` or `refuse`, with a plain reason, the window that caused it and, for a pause, when to wake.
- **Order of the rules (A5).** "This provider cannot report usage" is checked first, although the master plan's table lists it last: such a provider never has a snapshot, so "no snapshot" would always win otherwise. In the launch rule "weekly" means both the all-models and the per-model weekly windows. An unknown window can pause but cannot stop launches by itself. When several windows are over the pause line, the wake time is the latest reset plus one minute. A snapshot exactly 30 minutes old counts as stale. A setting for unknown usage that afk does not recognise holds.
- **Thresholds are in code for now (A6).** 90% to pause, 75% to stop launching, 30 minutes for stale, 5 minutes between probes, and "run" when usage is unknown. There are no config keys yet: machine config is a JSON file with three unrelated keys, and T2.4, which puts the governor in the daemon, is the place to add them.
- **The snapshot file (A7).** `internal/usagestate` writes `usage.json` under `$XDG_STATE_HOME/afk/` when that is set, otherwise `~/.local/state/afk/`. It has the fields of PLAN §4.5 plus `scope` on a window. `source` is `probe`. The governor's action is not stored yet; T2.4 owns that. A missing or broken file reads as "no snapshot".
- **When `afk usage` probes (A8).** It reuses a stored snapshot younger than 5 minutes and probes otherwise; `--refresh` forces a probe. It exits 0 when it printed a report (including "usage unknown" for a provider with no probe), 1 when the probe or the write failed, and 2 for a bad flag or provider name.
- **Where the probe runs (A9).** In afk's state directory, not the directory you ran `afk usage` from. The agent CLI keeps a record of every run under the directory it started in, and a probe every few minutes would fill whatever project that was.
- **The fixture and the throttling question (A10).** The captured stream is committed with ids zeroed, the start-up line cut to a few harmless keys, and the readable usage text removed. The probe was run three times in a row to see whether limits ever come back empty. Nobody tried to find the point where it gets throttled, because tripping a rate limit on the owner's account on purpose is not a machine's call.
- **Tests and prerequisites (A11, A12).** Tests were written with each change. Nothing had to be in place before the work started.
- **Shipping (A13).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green. The conductor runs `make install` after the review approves.

### The spike

- The probe was run against Claude Code 2.1.296: `claude -p "/usage" --output-format stream-json --verbose`.
- It was run three times back to back. Each run exited 0, printed three JSON lines and contained a `usage_report` whose `rate_limits` was a full object. No throttling showed up. Nobody tried to cause it.
- With `--output-format json` the output is a single result object with no `usage_report`; the text is only in its `result` field, as PLAN §6.2 says.
- What differs from §6.2: nothing that changes the plan. The `limits[]` rows carry more keys than the plan named (`group`, `scope`, `severity`, `is_active`), the model name is under `scope.model.display_name`, and `extra_usage` has a few more keys than `is_enabled`. The probe should run outside a project directory.
- The committed fixture is the first run, with ids zeroed and the readable text removed.

### Decided while building

- **A reading that parses wins over the exit status.** If the CLI exits non-zero but its output holds a usable report, the report is used.
- **The first report in the stream is final.** If its limits are unusable the result is "no usage report"; later lines are not tried.
- **Percentages are checked by type.** A `null` percentage would decode as 0, so the parser rejects it and skips the row. A reset time given as epoch seconds is accepted only below 1e12 (larger is probably milliseconds); otherwise the time is left unknown and the row is kept.
- **A wrong-typed extra-usage block reads as off.**
- **Several windows over a line.** The highest-used one is reported, and ties keep the first. When a pause has a window with no known reset time, the wake time is left empty and the caller must treat that as "no known wake time".
- **Reasons round to one decimal** so 89.9% never reads as 90% next to a "not starting" verdict. A zero stale limit makes every reading stale; callers pass the default thresholds.
- **`usage.json` ignores a relative `XDG_STATE_HOME`**, as the XDG spec requires. The state package takes the provider name as a string, so nothing outside `internal/provider` names one.
- **A reading is reused only if it is in the past.** One stamped in the future (the clock moved back) is not reused; the command probes. A stored reading from a different provider is ignored.
- **A reused reading is never "just now".** It prints "less than a minute ago" so the second run is told apart from a live probe.
- **If the probe fails** and an earlier reading exists, that reading is printed with the error and the exit code is 1. With none, it prints "No usage reading is available." and the governor says `hold`.
- **A pause with no known reset prints "There is no known wake time."** on the Governor line.
- **`afk usage help`** prints the help and exits 0, like `plan help`; any other extra word is exit 2.

### Not verified, owner check

- **Another timing failure in the host tests.** In the review's one full run, `TestServeSIGTERMBehavesLikeExit` in `internal/host` failed with "connection reset by peer". It passed when run again alone, and a second full `make check` was green. This change does not touch that package. It is not the test T0.4 fixed, so it may need its own look.
- **How often it is safe to probe is still unknown.** Three probes in a row were fine. Nothing here shows what happens when the request behind it is throttled; the parser treats empty limits as "no reading" and the governor then holds once the last reading is 30 minutes old.

### Ideas left out

- The agent CLI may have a flag that stops a one-shot run from saving a session record. If it does, the probe could use it and would not need its own working directory. Not looked into.
- `afk usage` has no machine-readable output. The dashboard and web page are meant to read `usage.json` instead.

## T1.2

T1.2 adds the inbox (`afk inbox`) and push notifications through ntfy. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where the inbox lives (A1).** In the main checkout's `.afk/inbox/`, even when the command is run inside a session's worktree. The command finds the main checkout through git. `--project DIR` overrides this and is used as given; outside a git repo the current folder is used. The master plan puts the daemon's state in the main checkout, and agents work in worktrees.
- **Reading `afk.toml` (A2).** A small new package, `internal/projectconfig`, reads only `[notify] ntfy_url` and ignores every other key. It uses the module `github.com/BurntSushi/toml` v1.6.0, afk's first TOML dependency. T1.1 owns the full `afk.toml` loader and was being built at the same time in another worktree, so a separate reader was the way to avoid both tasks editing one file. Whichever lands second should fold the two together.
- **Where the ntfy code lives (A3).** In the existing `internal/notify` package, in its own file, `ntfy.go`. That package already sends desktop toasts and keeps its impure code in named files; `ntfy.go` is the third. The rule for which items notify takes plain values, so `notify` does not depend on `inbox`.
- **Item ids (A4).** Six random characters from letters and digits that are hard to confuse (no `0`, `1`, `i`, `l`, `o`). Short enough to type on a phone. Items are ordered by when they were created, not by id.
- **File format (A5).** Every key of PLAN §4.3 is always present (`""`, `[]` or `false` when unset). Times are RFC 3339 UTC text. A file with a key afk does not know is reported as unreadable and skipped, never fatal. Only files named `{id}.json` count as items, so a stray file in the folder is ignored.
- **What `afk inbox add` accepts (A6).** The kind must be one of the six. A title is required: one line, up to 200 characters. Options and a recommended option are only for `question`; there must be at least two options and the recommended one must be among them. `--assumed` only goes with `--no-block`, because an agent that went ahead is not waiting. `--task` is optional (the daemon will raise items that belong to no task) but must look like `T4.3`.
- **`--block` or `--no-block` is required (A7).** Leaving both out, or giving both, is an error. A forgotten flag should never quietly stop a task, or quietly fail to.
- **The `session` field (A8).** The master plan lists no flag for it. There is an optional `--session NAME`; without it the command uses the one session found in the worktree it is run from, or leaves the field empty.
- **Which items notify, and how loudly (A9).**

  | Item | ntfy priority |
  |---|---|
  | `failure`, blocking | 5 (urgent) |
  | `failure`, not blocking | 4 (high) |
  | any other kind, blocking | 4 (high) |
  | `env_var` or `external_action`, not blocking | 3 (default) |
  | `verification`, not blocking | 2 (low) |
  | `question` or `notice`, not blocking | no notification |

  A question that does not block is an answer the agent already assumed, and there can be a dozen per task. They wait in the inbox without buzzing a phone.
- **A notification can never fail the command (A10).** The item is written first. No `afk.toml`, or no `ntfy_url`, means no notification and no message. A broken `afk.toml`, a bad address or a failed send prints a warning and the command still exits 0. The send waits at most 10 seconds and is not retried.
- **What the notification says (A11).** The title is `afk: ` plus the item's title. The text is the item's body, then `afk inbox show {id}`. It is cut to 3,500 bytes, because ntfy turns a message over 4,096 bytes into a file attachment. A user name and password in the address work, and are never printed. There is no setting for an ntfy access token yet.
- **Answering (A12).** Everything after the id is the answer. On an item with options, a number picks that option; any other text is kept as typed. An item keeps its first answer. Any kind can be answered, which is how an owner says an `env_var` or `external_action` item is dealt with.
- **The list (A13).** `afk inbox` shows every item: open and blocking first, then open, then answered, oldest first in each group. "Open" means not answered.
- **How the autonomous prompt uses the inbox (A14).** A park is now a blocking inbox item. The prompt still sets `awaiting_input`, so today's dashboard shows the session as waiting, and it sends the desktop toast only if `afk inbox add` fails. Answers that would have gone to the owner, owner checks and scope ideas each get a non-blocking item as well as their line in `.afk/{session}/decisions.md`. The lines stay because the ship step reads them and `conductor/SKILL.md` has no room for a change. Ordinary assumptions get no inbox item; they are in the work plan.
- **Plugin version (A15).** 5.1.0 in both manifests. The OpenCode port is unchanged and its lag note says so.
- **Tests and prerequisites (A16, A17).** Tests were written first with each package. Nothing had to be in place before the work started.
- **Shipping (A18).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green, with a merge commit. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.

### Decided while building

- **Reads are stricter about extra text, looser about missing keys.** An item file with anything after its closing brace is unreadable. A file that lacks one of the fourteen keys is still read, with the missing value empty. The command always writes all fourteen.
- **`status.json` in the inbox folder.** "status" happens to be six letters from the id alphabet, so a stray `.afk/inbox/status.json` is reported as an unreadable item rather than ignored. It does no harm.
- **Items added in the same second** are listed in id order, which is random, because times are kept to the second.
- **Flag order.** Only `--project` may come before `show`, `answer` or `add`; after that word, flags can go anywhere. An answer that starts with a dash is read as a flag and refused. `afk plan check` has the same limit.
- **What `add` prints.** The sentence `Added inbox item <id>.`, printed before the notification is tried, so the id is out even when the ntfy server is slow.
- **An address with no host or no `http`/`https` at the front** is refused, both in `afk.toml` and by the sender. Error messages never repeat the address, because it may hold a password.
- **A session guessed from the folder** is dropped when it is not a valid session name, instead of failing the whole command over a value nobody typed.
- **README.** The Degradation Paths list gained a line saying ntfy push needs no desktop notifier, so it works on Linux.

### Things a later task should know

- **T1.1 and the `afk.toml` reader.** `internal/projectconfig` reads only `[notify] ntfy_url`. Fold it into T1.1's loader (or the other way round), and expect a small merge in `go.mod` if both added the TOML module.
- **T2.1 and the stall rule.** A parked session has a blocking inbox item and still sets `awaiting_input`. PLAN §5.3 raises a failure when `awaiting_input` stays set for ten minutes; the daemon must skip that for a session with an open blocking item, or every park will also raise a false failure.
- **The daemon, web page and dashboard** should go through `internal/inbox` (`Store.Add`, `List`, `Get`, `Answer`), with `answered_via` `web` or `tui`. The kinds and the three `via` values are not exported; add constants when a second caller needs them.
- **A session named `inbox`** would share `.afk/inbox/` with the inbox when it runs in the main checkout. Session creation does not refuse the name yet.
- **No ntfy access token.** A protected topic works only with a user name and password in the address.
- **OpenCode lags.** The port's autonomous prompt does not use `afk inbox add`.

### Not verified, owner check

- **A real phone.** Tests and the end-to-end check used a local listener, never a real ntfy server. Put `[notify]` and `ntfy_url = "https://ntfy.sh/<your-topic>"` in a project's `afk.toml`, subscribe to the topic in the ntfy app, and run `afk inbox add --kind env_var --title "Test" --block`. Expect one high-priority notification titled `afk: Test`.
- **Install the new plugin.** `make install` updates the binary only. Run `afk plugin install --target claude` so new sessions get plugin 5.1.0 and its inbox-aware autonomous prompt. This session ran on the installed 5.0.0 plugin, so no session has yet parked through the inbox.
## T1.4

T1.4 adds the token ledger of PLAN §4.7: a provider method that reads a session's token use, `internal/tokens`, and `afk tokens`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where phases come from (A1).** The plan says to take the phase from `status.json` transitions, but that file keeps only the current phase and nothing on disk keeps its history. The conductor changes the phase by running a `jq` command, and the session's main transcript records each of those commands with a timestamp. The reader takes the phase changes from there. Everything before the first one is planning. Nothing was added to `status.json`, so the schema is unchanged.
- **Ship (A2).** The conductor ships while the phase still says `reviewing`, and writes `done` last. So in the last review, calls the main session makes after the last sub-agent has finished count as `ship`, as does anything after `done` or `failed`. This also counts the call or two that write the verdict file as ship. That is accepted.
- **Roles (A3).** `conductor`, `planner`, `research`, `crucible`, `task`, `fix`, `verifier`, `review:compliance`, `review:quality`, `review:qa` and `critic`. The planner and the conductor share one transcript, so the main session is `planner` while the phase is planning and `conductor` afterwards. A sub-agent with no metadata file is `unknown`; one whose description matches nothing is `other`.
- **Names (A4).** The provider method is `SessionTokens`, with `TokenQuery`, `TokenRecord` and `ErrTokensUnsupported`. T1.3 is adding `Usage`, `UsageSnapshot` and `ErrUsageUnsupported` in another branch, and these names stay clear of them.
- **Finding the transcripts (A5).** The reader looks for a file named after the session id in any project folder under `~/.claude/projects/`. It does not rebuild the folder name from the working directory, because that rule was only seen for `/` and `.` and a wrong guess would silently find nothing.
- **The fixture (A6).** The spike asked for one real autonomous task. The finished T0.4 session on this machine is one, so its transcripts were trimmed and no new run was paid for.
- **What `afk tokens` takes (A7).** A session name reads the transcripts as they are now. A task id such as `T1.4` reads the ledger. No argument lists the ledger's total per task.
- **Writing the ledger (A8).** The plan says a line is written when a task ends, which is the daemon's job and the daemon does not exist yet. Until then `afk tokens --record SESSION` appends that session's lines, and does nothing if the session is already in the ledger. The daemon should call the same function in `internal/tokens`.
- **One line per model too (A9).** A role can run on two models after a retry moves it to a stronger one, and a ledger line has one `model` field, so lines are per phase, role and model.
- **Task id (A10).** Taken from the first `T1.4`-shaped id in the session's `description.md`. A session with none has an empty task.
- **Tests and prerequisites (A11, A12).** Tests were written first with each change. Nothing had to be in place before the work started.
- **Shipping (A13).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green, with a merge commit. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **`make install` (A14)** is run by the conductor after the review approves, never by a task agent.

### Things a later task should know

- **The same message id in two files never happened** in the four real sessions looked at (T0.1 to T0.4). The rule is implemented and tested with a hand-made case only.
- **A resumed session is under-counted.** When a session is resumed under a new session id, `status.json` keeps only the new id, and the older transcript is not found.
- **A cleaned session can only be read from the ledger.** `afk clean` removes the worktree, and the lookup from session name to session id goes with it. Record before cleaning.
- **Phase detection depends on the conductor writing the phase with a shell command** that names `status.json`. A conductor that wrote it another way would leave the whole session counted as planning.
- **T1.1 to T1.3 were open in other branches** when this was built and touch some of the same files (`main.go`, `README.md`, this file, `internal/provider/provider.go`).

### Decided while building

- **Error names.** A session with no transcript returns `ErrNoTranscript`. The other two providers return `ErrTokensUnsupported`.
- **The reader lists folders, it does not use a glob pattern.** A `[` or `*` in the home path would be read as pattern syntax. A session id containing a path separator is refused, so a query cannot read outside the projects folder.
- **Only the six phase words of the status schema count as a phase change.** A command that sets `.phase` to anything else is ignored.
- **The call that writes a new phase is billed to the new phase.**
- **A session still in review can show a few conductor calls as `ship`.** The ship rule also applies while the last write is `reviewing` and no `done` has come yet. The numbers settle once the session ends.
- **A sub-agent file that exists but cannot be opened is an error**, because quietly counting less would be worse. A line that cannot be parsed, and a missing or broken metadata file, are skipped as planned.
- **A lock file `tokens.jsonl.lock` sits beside the ledger** and is left in place. All of a session's lines go out in one write.
- **The table puts subtotals and the total after the detail rows.** Shares are of the grand total.
- **`afk tokens` needs the main checkout for every form**, so a ledger recorded from one worktree is read from another.
- **A session with a transcript but no model calls yet exits 1** with a message to try again later, not an empty table.

### Checked, and not checked

- **Checked against a built binary:** `afk tokens t1-4` on this session while it ran printed a full table (146 calls, about 2.9M weighted, well under the 16M baseline of PLAN §4.7 so far). Usage, unknown session, empty ledger and `--record` with no session were run by hand.
- **Not checked by hand:** `--record` on a real project. It is covered by tests against the fixture only. Owner check: run `afk tokens --record SESSION` before cleaning a finished session, then `afk tokens` to see it listed.
- **Known gap:** if a ledger write fails partway (a full disk), the ledger may hold some of a session's lines, and `--record` will then say the session is already recorded.

### Merging with T1.1 to T1.3

- **All three landed on main first**, so main was merged into this branch (a merge commit, no rebase). Five files conflicted and both sides were kept in each: the command table and help text in `main.go`, the provider interface (it now has both `Usage` and `SessionTokens`), and the three documents.
- **One test fake needed the new method.** The fake provider in `usage_cmd_test.go` gained a `SessionTokens` that returns `ErrTokensUnsupported`. A later fake of `provider.Provider` needs both methods.
- **Two ways to find the main checkout now exist.** `afk tokens` takes the first worktree git lists; `maincheckout.go` (from T1.2) works it out from git's common directory. They give the same answer and were left as they are; folding them is a small clean-up for later.

## T1.5

T1.5 measures what a fresh agent carries before its first action and trims what it does not need. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **How the first call is captured (A1).** From the agent's own transcript. The first model call's usage gives the total size. The attachment lines before it give the parts (the skills list, `CLAUDE.md`, connector listings). `claude -p "/context"`, which costs no model usage, gives the split between system prompt, tools, connectors and skills. Nothing is put between the agent CLI and its service, so no login passes through new code.
- **How agents are trimmed (A2, A3).** The plugin gains agent definitions under `plugins/afk/agents/`, each with the tools its role uses: `task` (task and fix agents), `verifier`, `reviewer` (the compliance and quality checks, the critic and the plan review), `qa`, `research` and `web-research`. The hands-on QA check gets a deny list, not an allow list, so every browser tool stays.
- **An unknown agent type falls back (A4).** A session that started before a plugin update does not know the new types. A spawn that is refused for that reason is made again once without a type.
- **Start-up share in `afk tokens` (A5).** A section under the table, for a session read from its transcripts. The token ledger file keeps its shape, so a task read from the ledger shows no start-up section.
- **The baseline is the finished T1.4 session (A6).** It ran on this VM with the afk plugin and has all three agent types, so no new run was paid for to get the before numbers.
- **A trim is kept only if it measures smaller (A7).** A definition that saves nothing is dropped and the reason is recorded here.
- **`CLAUDE.md` stays in every agent (A8).** The plan requires it for agents that write code. Review checks keep it too, because they judge the code against the project's rules.
- **OpenCode is not changed (A9).** Its lag note gains an entry.
- **Plugin version 5.2.0 (A10).**
- **The scratch-repo run is done by the conductor, last (A11).** After the review approves, it runs `make install` and `afk plugin install --target claude`, then one tiny autonomous task in a scratch repo. Installing the plugin swaps it for every session on this machine, which is why it is left to the end. This would normally have gone to the owner.
- **Shipping (A12).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **Tests and prerequisites (A13, A14).** Tests are written first for the Go and workflow-script changes. Nothing had to be in place before the work started.
- **No web research (A15).** The task names no new library or API.

### Start-up context, before

Measured on the finished T1.4 session (`1a10f95c-966b-434c-b2a9-996c9abf3bec`, agent CLI 2.1.296, all three agents on `claude-sonnet-5-5`). Its 13 sub-agents each started at 35,151 to 36,622 tokens, 465,407 in all. The main session's first call was 121,273 tokens, most of it the skill it was started with.

| | Task agent | Review agent (`review:qa`) | Research agent |
|---|---|---|---|
| Agent type | `workflow-subagent` | `workflow-subagent` | `general-purpose` |
| **Start-up size, tokens (measured)** | **35,937** | **35,154** | **35,450** |
| read from cache (measured) | 19,257 | 19,261 | 17,655 |
| written to cache (measured) | 16,678 | 15,891 | 17,793 |
| Total characters (measured) | 130,761 | 128,678 | 127,923 |

The parts, largest first. Characters are measured. Tokens are estimated from each agent's own characters per token (3.61 to 3.66), good to about 20%.

| Part | Task: chars | Task: tokens (est.) | Review: chars | Review: tokens (est.) | Research: chars | Research: tokens (est.) |
|---|---|---|---|---|---|---|
| Tool definitions, all | 84,994 | 23,359 | 84,980 | 23,216 | 78,081 | 21,638 |
| of which the `Artifact` tool | 54,855 | 15,076 | 54,855 | 14,986 | 54,855 | 15,201 |
| of which `SendFeedback` | 9,090 | 2,498 | 9,090 | 2,483 | absent | absent |
| of which the `Agent` tool | absent | absent | absent | absent | 7,873 | 2,182 |
| of which `Bash` | 4,412 | 1,213 | 4,412 | 1,205 | 4,412 | 1,223 |
| of which `Skill` | 3,302 | 907 | 3,302 | 902 | 3,302 | 915 |
| of which `ReportFindings` | 2,832 | 778 | 2,832 | 774 | absent | absent |
| of which `Read` | 2,790 | 767 | 2,790 | 762 | 2,790 | 773 |
| of which `ToolSearch` | 2,497 | 686 | 2,497 | 682 | 2,497 | 692 |
| of which `ListAgents` | 2,009 | 552 | 2,009 | 549 | absent | absent |
| of which `Edit`, `Write`, `StructuredOutput` | 3,207 | 881 | 3,193 | 872 | 2,352 | 652 |
| The skills list | 19,915 | 5,473 | 19,915 | 5,441 | 19,915 | 5,519 |
| `CLAUDE.md` and memory | 14,373 | 3,950 | 14,373 | 3,927 | 14,373 | 3,983 |
| The agent's own prompt | 2,908 | 799 | 1,011 | 276 | 6,418 | 1,779 |
| Connector and on-demand tool names | 2,698 | 741 | 2,698 | 737 | 2,625 | 727 |
| Environment, session and date notes | 4,003 | 1,100 | 3,831 | 1,046 | 3,846 | 1,067 |
| System prompt | 1,870 | 514 | 1,870 | 511 | 2,665 | 739 |

**What the "extra" context turned out to be.** Tools and listings the agents never use.

- **The `Artifact` tool definition: about 15,000 tokens, 42% of every agent's start-up.** One tool that no task, review or research agent uses.
- **The skills list: about 5,500 tokens, 15%.** 42 skills, none of which a sub-agent is meant to invoke.
- **`SendFeedback`: about 2,500 tokens, 7%** (workflow agents only).
- **Other unused tools and the connector names: about 3,700 tokens** (`Skill`, `ReportFindings`, `ToolSearch`, `ListAgents` and the name listing).

That is about 26,700 of the task agent's 35,937 tokens, or 74%. What an agent needs is small: `CLAUDE.md` is about 4,000 tokens, and the tools a task agent works with (`Bash`, `Read`, `Edit`, `Write`, `StructuredOutput`) about 2,900.

### What was trimmed

Six agent definitions in `plugins/afk/agents/`, named `afk:<name>`. All six were kept: each measured smaller than the default agent.

| Definition | Used by | Tool rule | Tools it got in the measurement |
|---|---|---|---|
| `afk:task` | task and fix agents | allow `Read, Edit, Write, Glob, Grep, Bash` | `Bash`, `Read`, `Edit`, `Write` |
| `afk:verifier` | the verifier | allow `Read, Glob, Grep, Bash` | `Bash`, `Read` |
| `afk:reviewer` | the compliance and quality checks, the critic, the plan review | allow `Read, Glob, Grep, Bash` | `Bash`, `Read` |
| `afk:qa` | the hands-on QA check | deny `Agent, NotebookEdit, Artifact, SendFeedback, ReportFindings, ListAgents` | `Bash`, `Skill`, `Read`, `ToolSearch`, `Edit`, `Write` |
| `afk:research` | the planner's codebase research | allow `Read, Glob, Grep, Bash` | `Bash`, `Read` |
| `afk:web-research` | the planner's web research | allow `WebSearch, WebFetch, Read` | `Read`, `WebSearch`, `WebFetch` |

- **An allow list also removes the skills list and the connector names.** They come with the `Skill` and `ToolSearch` tools, which the lists leave out.
- **`Glob` and `Grep` are in the lists but this agent CLI version has neither.** A name it does not know is dropped with no warning. Searching goes through `Bash`. The names stay for a version that has them.
- **`StructuredOutput` is in no list.** A workflow call with a schema adds it by itself.
- **The QA deny list is longer than first planned.** A workflow agent has no `Agent` or `NotebookEdit` tool to begin with, so denying only those would have saved nothing. The four other names are the unused tools from the before table. `Skill` and `ToolSearch` stay, because the browser skills and connector tools need them.
- **Never deny `ToolSearch`.** In the try-out, a deny list that included it made every on-demand tool load in full: 58 tools and a first call of 43,258 tokens, against 15,813 for the default agent.
- **`CLAUDE.md` still reaches every type.** Nothing was removed from it.
- **A refused type falls back.** The workflow scripts make the call again once without the type. The skills tell the session to do the same.

### Start-up context, after

Each type was spawned once through the Agent tool with a small real job, and the default `general-purpose` agent was given the same six jobs as the control. All on `claude-sonnet-5-5`, in a scratch project.

| Agent type | Start-up, tokens (measured) | Control, tokens (measured) | Saving | Did its job | `CLAUDE.md` present |
|---|---|---|---|---|---|
| `afk:task` | 4,547 | 15,984 | 11,437 (71.6%) | yes: wrote a file, ran a command | yes |
| `afk:verifier` | 3,961 | 15,965 | 12,004 (75.2%) | yes: read a file, ran `git status` | yes |
| `afk:reviewer` | 3,961 | 15,965 | 12,004 (75.2%) | yes: read a file, ran `git status` | yes |
| `afk:qa` | 14,075 | 15,979 | 1,904 (11.9%) | yes: ran a command, wrote a file | yes |
| `afk:research` | 3,967 | 15,968 | 12,001 (75.2%) | yes: found the string in both files | yes |
| `afk:web-research` | 3,422 | 15,982 | 12,560 (78.6%) | yes: read a file, no fetch asked | yes |

- **`CLAUDE.md` was checked two ways.** Every agent carried the instructions attachment at the same size as the control (693 characters). The scratch `CLAUDE.md` also held a rule to end the reply with a marker line, and all twelve agents followed it.
- **No tool call failed** in any of the twelve agents.
- **`afk:web-research` does get its web tools.** It had `WebSearch` and `WebFetch` loaded without `ToolSearch`. No page was fetched.
- **These numbers are not the before numbers' twin.** The measurement ran with `claude -p`, as a task agent must. A `-p` session has no `Artifact` or `SendFeedback` tool, and the scratch `CLAUDE.md` is small. That is why the control is about 16,000 tokens here and not 35,000. Compare each type with its control, not with the before table.
- **The QA saving looks small for the same reason.** Most of what its deny list removes does not exist in a `-p` session. Here it lost little more than the `Agent` tool.

What a real afk session should show, worked out from the before table by taking away the parts each definition removes. These are estimates, not measurements:

| Agent | Before (measured) | After (estimated) | Saving (estimated) |
|---|---|---|---|
| Task agent, `afk:task` | 35,937 | about 9,200 | 26,711 (74%) |
| Review agent, `afk:qa` | 35,154 | about 16,400 | 18,792 (53%) |
| Research agent, `afk:research` | 35,450 | about 9,600 | 25,888 (73%) |

### How it was measured

- **The script.** `bash scripts/startup-context.sh [PROJECTS_DIR] SESSION_ID` reads a session's transcripts and prints, for the main session and each sub-agent, the first model call's usage and the size of each part. It prints sizes and names only, never content.
- **What is measured.** Start-up tokens (the first call's input, cache write and cache read, taking the largest value of each over the lines that share the message id) and every part's size in characters.
- **What is estimated.** A part's tokens: its characters divided by that agent's characters per token. The transcripts give tokens only for the whole call. The "after" figures for a real afk session are estimates too.
- **Tool definitions are counted** though the transcript records them just after the first call. They are sent with it.
- **Sessions.** Before: `1a10f95c-966b-434c-b2a9-996c9abf3bec` (T1.4). Try-out: `5e4594c1-446a-49db-924b-d635ddbe08bf` and `d74170c6-0938-4986-aa2d-a772e05f26b0`. After: `a3f299ff-9501-46cf-93c8-1132dcd7b3f9` (the six types) and `2d787edb-164e-4b04-8936-64b7885cebe9` (the control). Unknown type: `da45a97a-85f3-4a58-961e-4ecd3407ef8a`.
- **How the after runs were made.** From a scratch directory outside the repo, with this branch's plugin loaded for that run only: `claude -p "<prompt>" --model sonnet --plugin-dir <worktree>/plugins/afk --permission-mode bypassPermissions --output-format stream-json --verbose`. The installed plugin did not get in the way: the session listed the six `afk:` types and plugin version 5.2.0. Nothing was installed.
- **An unknown type is refused by name.** Asking for `afk:no-such-agent` returned `Agent type 'afk:no-such-agent' not found`, with the list of known types. Nothing was spawned and the session carried on.

Live calls made for the after measurement:

| # | Call | Purpose | Cost |
|---|---|---|---|
| 1 | `claude -p "/context"` with `--plugin-dir` | check that the six types load | 0 tokens |
| 2 | run A, session `a3f299ff…` | the six types, one job each | $0.23: 57,658 cache write, 154,899 cache read, 3,719 output |
| 3 | run B, session `2d787edb…` | the same six jobs on the default agent | $0.38: 106,175 cache write, 266,074 cache read, 5,849 output |
| 4 | session `da45a97a…` | the unknown type | $0.05: 11,931 cache write, 37,335 cache read, 263 output |
| 5 | one session under a pseudo-terminal | measure where `Artifact` exists | 0 tokens: it stopped at the "do you trust this folder" question and was left unanswered |

The try-out's four calls ($0.39 in all) are listed in the evidence. Evidence for every number here is under `.afk/t1-5/evidence/`: `w1-t2-baseline.md`, `w1-t2-script.txt`, `w1-t2-tryout.txt`, `w3-t1-after.txt` and `w3-t1-unknown-type.txt`.

### The run after the trim

After the review approved, the conductor ran `make install` and `afk plugin install --target claude` (plugin 5.2.0), then one throw-away autonomous task as a session of this repo: add `scripts/hello.sh` and its test on a branch that is never merged (session `t1-5-scratch2`, id `8784aa47-531b-492b-90ef-2a5c25e4d7c8`).

- **It ended `phase: done`** with a verdict of APPROVE: `Compliance`, `Quality` and `QA` all ran and all passed. It asked nothing at the terminal.
- **Every agent used its new type**: `afk:task`, `afk:verifier`, `afk:reviewer` (compliance and quality) and `afk:qa`. No call fell back to the default agent.
- **Start-up sizes in a real session, measured.** These replace the estimates above.

| Agent | Before (T1.4 session) | After (this run) | Saving |
|---|---|---|---|
| Task agent, `afk:task` | 35,937 | 11,787 | 24,150 (67%) |
| Verifier, `afk:verifier` | about 36,000 | 11,273 | about 24,700 (69%) |
| Review, compliance and quality, `afk:reviewer` | about 35,150 | 10,300 | about 24,850 (71%) |
| Review, hands-on QA, `afk:qa` | 35,154 | 20,955 | 14,199 (40%) |

- **`afk tokens t1-5-scratch2` shows the start-up share**: 27.2% of the session's weighted cost. Most of that is the main session's own first call (16.9%), which this task does not trim. The five sub-agents' first calls together are 10.3%.

```
start-up (the first call of each agent)
               role  agents  average tokens  weighted  share
            planner       1          122579    122193  16.9%
               task       1           11787     12372   1.7%
           verifier       1           11273     15356   2.1%
  review:compliance       1           10302     14327   2.0%
          review:qa       1           20955     21821   3.0%
     review:quality       1           10300     10514   1.5%
              total       6           31199    196583  27.2%
```

- **The whole run cost about 724,000 weighted tokens** in 38 model calls.
- **A scratch repo did not work, and why.** The first try used a new repository in a scratch folder. The agent CLI stopped at its "do you trust this folder?" question, which is the owner's to answer, so that session was stopped and removed. A session of this repo is already trusted.
- **A first run saved no transcripts.** A session started from inside another agent's shell inherits `CLAUDE_CODE_CHILD_SESSION=1` and keeps no transcript, so `afk tokens` had nothing to read. It still ended `done` with all three checks passed. The run above was started with those variables removed. The daemon will start sessions from its own clean environment, so it is not affected, but anything that starts `afk new` from an agent's shell must clear them.
- **The main session's first call is 122,579 tokens.** That is the conductor's own start-up: the skills it loads in full. It is now the largest single start-up cost and is outside this task.

### Not verified, owner check

- **The research agents in a real session.** The run above planned a tiny task and spawned no research agent, so `afk:research` and `afk:web-research` have only the `claude -p` numbers.
- **The QA check's browser tools.** No browser or connector tool was used in any run. `afk:qa` keeps `Skill` and `ToolSearch`, which is how those tools are reached.
- **A real web fetch.** `afk:web-research` had the tools but was not asked to use them.
- **The plugin was installed for every session on this machine** (`afk plugin install --target claude`, version 5.2.0). A session that was already running keeps working: a spawn with a type it does not know is made again without the type.

## T2.1

T2.1 adds `afk daemon`, the build loop of PLAN §5.3, with `afk daemon status`, `afk pause`, `afk resume`, the decision log, the `afk.local.toml` overlay and a systemd unit. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Session and branch names (A1).** The plan says the branch is `afk/t4-3`. It is built as `t4-3` for both the session and the branch: the task id in lower case with the dot as a hyphen, so `T4.2a` gives `t4-2a`. A second attempt is `t4-3-2`. A session's name is also its branch, its worktree folder and its plan file everywhere in afk, and a name cannot contain `/`. Giving a session a separately named branch would have meant changing creation, discovery, worktree matching and clean-up. This would normally have gone to the owner.
- **How the unattended mode gets onto the command line (A2, A4).** The provider gains two methods. `Unattended` takes a launch or resume command and returns it with the provider's no-questions permission mode added. `UnattendedPreflight` says whether this machine can run that mode without a dialog: the user is not root, and user settings show the bypass warning was accepted. A provider with no such mode says so, and the daemon starts nothing for it.
- **Keeping the mode after a restart (A3, A29).** An empty file, `.afk/{session}/unattended`, marks the session. Session start and the host's restart both read it. Without it, a session restarted from the dashboard or by the host would come back in a mode that stops to ask, with nobody there. Go has to read this marker itself; the autonomous marker is read by the plugin, and a command-line flag cannot be recovered that way.
- **Machine config (A5).** The daemon reads a new file, `config.toml` in afk's config folder: `provider`, `[daemon] tick` and `stall_after`, and one `[[project]]` with a `path` per project. The dashboard's `config.json` is left alone. The master plan chose TOML; joining the two files is a later clean-up.
- **Shape of the loop (A6, A13).** One pure function looks at a snapshot of the disk and returns one action. A second part carries it out. Sessions are read through the same discovery code the dashboard uses, which never touches the network. The ledger is marked `running` before the session is created, so a restart never starts a second session for a task.
- **A stalled session (A7, A9, A30).** If a session has been asking at the terminal for longer than the stall limit and no blocking inbox item explains it, the daemon adds a blocking `failure` item and parks the task on it. The clock runs from the later of the session's last status write and the newest answer to one of its items. It is reported once per attempt.
- **A session whose host is gone (A8, A38).** After a minute's grace, a blocking `failure` item, and the task parks. The item says to open the session in `afk dash`, which starts a stopped host again. T2.3 replaces this with automatic recovery.
- **Un-parking (A10).** When every item a task waited for is answered, the task goes back to `running` with the same session. afk cannot yet type into a session, so someone has to resume it by hand; delivering a prompt is T2.2's.
- **A launch that fails (A11, A12, A35).** The task becomes `failed` with an item giving the error and the two commands that put it right. A `running` task with no session on disk gets its session created again without counting another attempt. A worktree that exists without a session folder counts as no session.
- **Pause (A14).** `afk pause` writes `.afk/build/paused`; `afk resume` removes it. A paused project starts nothing new. A running task carries on.
- **What `afk daemon status` reads (A15, A16).** The daemon writes `.afk/build/daemon.json` for each project when its state or reason changes. Status reads that, the ledger and the pause flag. Notifications for "waiting on owner" and "blocked" are sent when the reason changes, which is how they are sent once and not on every tick. One daemon per machine is enforced with a lock file in afk's state folder.
- **Decision log (A17, A31).** A new package appends one line per transition to `.afk/log/decisions.jsonl`. A state that merely persists adds nothing.
- **Token recording and notification text (A18, A19, A36).** Both moved out of the command files into `internal/tokens` and `internal/notify`, so the daemon and the commands share them. A provider that cannot report tokens never fails a task.
- **Overlay (A20).** A key present in `afk.local.toml` wins, even when empty. Either file may be missing. Every error names its file.
- **systemd (A21).** A user unit with `KillMode=process`, so stopping the daemon never ends a session, and no private temp folder, so `afk attach` still finds the sockets.
- **Defaults (A22).** A 5-second tick, a 10-minute stall limit, and `afk daemon --once` for a single pass.
- **A project that cannot be read (A23, A33, A34).** A broken plan or a damaged ledger raises one `failure` item and the project is skipped; the daemon answers the item itself once the project reads again. A project folder that does not exist gets nothing written under it.
- **Plain git only (A37).** The daemon creates sessions without git-spice, so an unattended launch does not depend on how the main checkout is set up.
- **`afk next` (A24, A39).** It now knows which inbox items are answered, so an answered parked task is named first. `afk tasks` is unchanged.
- **Tests, prerequisites, install (A25, A26, A27).** Tests were written with each change. Nothing had to be in place first. The conductor runs `make install` after the review approves.
- **Shipping this session (A28).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **Planning process (A40).** The work plan went through three review passes. The two critical findings left in pass 3 were mistakes in the plan's wording and were fixed, so none remained open.

### Accepted risks

- **The unattended launch was not run against the real agent CLI.** Tests compare command lines. The flag was read from the CLI's help (version 2.1.296) and the settings key from this machine's settings. That the CLI really starts without a dialog is first shown by the supervised run of T4.2.
- **A daemon killed while a session is being created** leaves a half-made session. The next pass marks the task `failed` and says how to clean up. Automatic recovery is T2.3's.
- **A parked session stays idle after its item is answered** until someone resumes it by hand.

### Decided while building

- **Item titles.** A stall item's title starts "Session waiting at the terminal: " and a stopped-host item's "Session stopped running: ", each followed by the task and session. The fixed opening words are how the daemon recognises its own earlier report.
- **An item cannot name its own id.** The inbox picks the id when it writes the item, so the stall and stopped-host texts say to answer "this item" using the id `afk inbox` shows. The push notification carries the id.
- **Pause also holds back an un-park.** A parked task whose item is answered stays parked until `afk resume`. A paused project starts and resumes nothing.
- **A running task the plan no longer has** stops the project with advice to put the task back in the plan. `afk ledger set` refuses an id that is not in the plan, so the ledger cannot be corrected first.
- **A session whose status file cannot be read and whose host is dead** gets its minute of grace from the task's start time only.
- **`afk daemon status`** decides "running" from the recorded process id. A reused id can make a dead daemon read as running; only the lock is proof.
- **`afk daemon --once`** takes the same lock as the service, so it exits 1 while the service runs.
- **A notification is sent inside a pass.** An ntfy server that never answers holds that pass for up to 10 seconds per message.
- **Inbox files that cannot be read** are not reported by the loop. A task parked on one stays parked.
- **The plan-error item is answered by the daemon** with the text "fixed" once the project reads again. The inbox records it as answered from the command line, because it has no fourth source.
- **The `afk.toml` address check** now names whichever file the bad value came from.

### Checked, and not checked

- **Checked against a built binary** (never `~/bin`): `afk help` lists the three commands; in a throwaway repo, `pause`, `daemon --once`, `daemon status` and `resume` behaved as the README says, a paused project launched nothing, and bad input gave the documented exit codes. The review ran the whole of `make check` once, green.
- **Not checked: a real launch.** No test and no manual check starts an agent. Owner check: on the VM, add one project to `config.toml`, run `afk pause <project>`, `afk daemon --once`, `afk daemon status`, then `afk resume <project>` and one supervised `afk daemon --once` while watching `afk dash`. Expect a session named after the task, started with no permission dialog.
- **Owner check: the main checkout's `afk.toml`.** It is untracked and holds the ntfy address, and it is no longer ignored. Run `mv afk.toml afk.local.toml` there.
- **Not installed: the systemd unit.** `contrib/README.md` has the commands.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes: this file and `CONTEXT.md` were edited outside the work plan's tasks (the planner and conductor write both, by design), and the notification test was mirrored into `internal/notify` rather than moved.

### Ideas left out

- Let `afk ledger set` change an entry for a task the plan no longer has.
- Show the count of unreadable inbox files in `afk daemon status`.
- Send notifications outside the pass, so a slow ntfy server cannot delay it.
- An `afk new --unattended` flag. The mode is reachable only through the daemon today.

### Merging with T1.5

- **T1.5 landed on main first**, so main was merged into this branch (a merge commit, no rebase). `CONTEXT.md` and this file conflicted and both sides were kept.
- **One break git did not flag.** T1.5's start-up section in `afk tokens` reads the per-call records, which this task had moved inside `tokens.Record`. `RecordResult` now hands the records back, so the transcripts are still read once.
- **Not reviewed by the three checks.** The join was made after the verdict. It was checked with the full `make check`, green, and by CI.

## T2.2

T2.2 makes the daemon ship a finished session by itself: push, pull request, CI, merge, deploy check, clean-up. It also stops the daemon starting an agent for a task that is the owner's, and adds `afk ledger init` for a project that is already part-built. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where a ship's progress is kept (A1).** In the ledger entry, in four new fields: `ship`, `ship_at`, `ci_reruns` and `ci_fix_cycles`. The task's status stays `shipping` throughout. A second file could disagree with the ledger.
- **Shape of the code (A2).** One pure function decides the next step from plain facts. A second part gathers the facts, takes one step and saves. The daemon takes one step per pass and never waits inside one.
- **Not doing anything twice (A3).** Before each action the daemon asks whether it already happened: is the branch on GitHub at this commit, is there a pull request for it, is it merged. A daemon killed after an action and before saving then finds the work done.
- **How often GitHub is asked (A4).** At most once every 20 seconds per project. The time of the last look is in a small file, because the daemon keeps nothing in memory.
- **A pull request with no CI checks is never merged (A5, A30).** A conflict with the base is handed back to the session. No checks at all after five minutes parks the task. A cancelled check counts as a failure.
- **Handing a red CI back (A6, A22, A31, A32).** The failing jobs and the end of their log go into a file in the session's folder, and one line is typed into the session asking it to read the file, fix, commit and not push. The session's status file is not touched, because a finished session is finished. The fix counts as ready when the branch has a new commit at least two minutes old and nothing uncommitted. No new commit in an hour parks the task.
- **A newly required environment variable (A7).** Checked just before the merge. The owner sets the variable and answers the item, and the merge follows.
- **A step that fails (A8, A23, A38).** A failed look is tried again; half an hour of failed looks parks the task. A failed push, pull request, merge or update parks it with the error. Answering the item tries again with a fresh set of attempts.
- **After a merge (A9).** The project's own checkout is fast-forwarded to include it, because the next task's worktree is cut from there. Fast-forward only; a refusal parks.
- **Deploy check (A10, A21).** Only when a health address is set. If it never comes up, the line stops: nothing more starts for the project until `afk resume`, and the task then counts as done.
- **Clean-up (A11, A24).** The worktree is removed, the session's folder archived and the merged branch deleted, on GitHub's word that it merged.
- **Waking a parked session (A12, A27).** When the owner answers what a session was waiting for, the daemon types the session's resume command into it.
- **Owner tasks (A13, A33).** A title with `(owner)` or `(owner; …)`. One inbox item; answering it marks the task done. `(owner + agent)` is an ordinary task. A pause does not hold these back.
- **No ledger (A14).** The daemon starts nothing and raises one item showing both forms of `afk ledger init`.
- **Settings (A15).** The `[ship]` defaults: merge commits, one automatic re-run, two hand-backs, a 15-minute deploy wait. The base is the session's base branch unless set.
- **The pull request (A16).** Opened ready, titled with the last commit's subject.
- **A second attempt starts clean (A35).** It does not inherit the first attempt's pull request.
- **Would normally have gone to the owner (A17, A18).** The real run creates a private GitHub repository that this machine's login cannot delete afterwards. And this session ships itself as its description asks: push, draft pull request, mark ready, wait for CI, merge on green.
- **Planning process (A37).** The work plan went through three review passes, which found 7, 3 and 4 critical gaps. All were fixed in the plan; none was left open.

### Accepted risks

- **A real agent has not been seen receiving a typed line.** Tests use a recording listener and the real run uses a script. Whether the agent's terminal submits the line is first shown by the supervised run of T4.2.
- **The session's host must still be running after the session finishes** (A28). If it is not, the hand-back fails and the task parks with an item saying how to reach the session.
- **A re-run can happen twice** if the daemon is killed at exactly the wrong moment. A push, a pull request and a merge cannot.

### The end-to-end run

One real run, on 2026-10-12 (UTC), against a private GitHub repository made for it: `dphaener/afk-ship-e2e-20261011`. Tools: gh 2.102.0, git 2.53.0, on Linux. The full transcripts are in `.afk/t2-2/evidence/w6-t1-red.txt` and `w6-t1-green.txt`.

**What was run.** A binary built from this branch to a scratch folder, with a scratch home, config and state folder, so nothing touched the real ones. The repository held a two-task plan (T0.1, and T0.2 depending on it), a `.gitignore` with `.afk/`, and one CI job on pull requests that runs `test -f greeting.txt`. The project was paused with `afk pause` for the whole run, so the daemon could ship but could never start an agent, and no agent CLI was on the `PATH`. Each task's session was staged by hand the way the daemon leaves one at `phase: done`: a worktree and branch with one commit, a `status.json`, a ledger entry at `shipping` written by hand (`afk ledger set` writes only the status, and the daemon rightly refuses a `shipping` entry with no session), and a real `afk _host` whose child was a shell script standing in for the agent. The script reads lines from its terminal and, on a line naming `ci-failure.md`, commits the missing file. Then `afk daemon --once` was run again and again, at least 20 seconds apart.

**T0.1, red first (pull request #1, merge commit `9c071a51b891020f5ad7349f82302a4e3557b377`).**

- `committed`, `pushed`, `pr_open`: three passes, a few seconds in all. The pull request was opened ready, titled with the commit's subject, its body ending in the task and session.
- Seconds after it opened, `gh pr checks 1` printed `no checks reported on the 't0-1' branch` and exited 1. That is the wording the code expects, now seen from a real gh.
- CI failed (no `greeting.txt`). The daemon re-ran the failed job once (`ci_reruns` 1), waited 90 seconds, saw it fail again, wrote `ci-failure.md` with the job's name and the end of its log, typed one line into the session and saved `handed_back` (`ci_fix_cycles` 1).
- The first time, the typed line did not arrive. See the bug below. After the fix the hand-back was repeated by hand: the old host stopped and a new one started, `ci-failure.md` removed, the ledger entry put back to `pr_open` with both counters at zero. The daemon re-ran, waited and handed back again, and this time the script received the line, once, and committed `greeting.txt` within the same second. Because of that reset the job ran three times on GitHub, not two.
- The daemon waited out the two quiet minutes after the fix commit, pushed it (`pr_open` again), and 23 seconds later read the new commit's check as passed (`ci_green`). It merged on the next pass, and only then: the merge is of commit `184cc11`, whose check passed, not of the commit whose check failed.
- `merged`: it recorded the merge commit, fast-forwarded the project checkout's `main`, deleted the branch on GitHub, found no deploy to check (`deploy_healthy`), cleaned up and marked T0.1 `done`.
- Checked afterwards: one pull request for the branch, merged, one merge commit; the branch gone from GitHub; the worktree and the local branch gone; the session's folder in the archive, `ci-failure.md` included; the host stopped; local `main` equal to `origin/main` at the merge commit.

**T0.2, green from the start, squash merge (pull request #2, merge commit `da66179eb8756e6e7d6904cf879afde3fb74ab89`).** The scratch project's `afk.toml` set `merge_method = "squash"` and a `health_url` on this machine (`http://127.0.0.1:36245/`).

- With T0.1 done, T0.2 pending and the project paused, a pass said "paused" and started nothing.
- `committed`, `pushed`, pull request opened, check passed, squash-merged, `main` fast-forwarded, branch deleted on GitHub.
- Deploy check: with nothing listening on the health address, three passes said "waiting for the deploy to become healthy". A local web server was then started there, the next pass got a 200 and saved `deploy_healthy`, and the pass after that cleaned up and marked T0.2 `done`.
- Checked afterwards: GitHub's `main` is one new commit, `Add a farewell (#2)`, with a single parent. The local branch `t0-2` is gone, though a squashed branch is never an ancestor of `main`: it was deleted because the ledger held the merge commit (A24). The worktree is gone and the archive is there. Nothing was typed into this session.

**The kill test.** During T0.2 the daemon was killed with SIGKILL twice, at the two worst moments, by a wrapper around `gh` that ran the real command and then killed the daemon that had started it.

- Right after `gh pr create` returned. The ledger still said `pushed` with no pull request number. The next pass said "pull request #2 already exists for the branch" and saved `pr_open`. No second pull request was opened.
- Right after `gh pr merge` returned. The ledger still said `ci_green`. The next pass said "the pull request is already merged" and saved `merged` with the merge commit. No second merge was tried.
- At the end: two pull requests in the repository, one per task, each merged once.
- The machine lock did not get in the way after either kill: the next pass started at once.

**Bug found and fixed.**

- **The Enter of a typed line was lost.** `host.SendInput` wrote the text, the Enter and the detach one after another. The host handles a detach at once but writes input to the terminal from a second queue, and it throws away input from a client that has already gone. So the Enter was dropped: the host's log said "discarding 1 bytes of input from a client that no longer holds the session", the line sat typed in the session and never submitted, and the ledger said `handed_back` all the same. With a real agent the task would have waited an hour and parked. Fixed in `internal/host/input.go`: `SendInput` now waits 200 ms after the Enter before it detaches. A new test, `TestSendInputReachesARealChild`, runs a real host with a real child; it failed two times in three before the fix and passed fifteen in fifteen after. The earlier tests could not see this because they stood in for the host. The wait is a margin and not a guarantee, since the host has no way to say "written"; a firmer fix belongs in the host (deliver what a client sent before its own detach) and is left for a later task.

**Seen, and left as it is.**

- The log in `ci-failure.md` carries GitHub's colour codes and a job and step name in front of every line. It reads fine; it is not pretty.
- A daemon killed while `gh pr create` runs leaves one small file, `afk-pr-body-*.md`, in the temp folder.
- A scratch `XDG_CONFIG_HOME` also hides gh's login, so the run pointed `GH_CONFIG_DIR` at the real one. Only a test set-up meets this.
- A `shipping` entry with no session (what `afk ledger set T0.1 shipping` leaves) raised one "afk cannot run this project" item, and the daemon answered it by itself once the entry named a session.

**Not checked.**

- A real agent receiving the typed line. The stand-in is a shell script, which reads a line whenever the Enter arrives. Whether an agent's terminal submits it is first shown by the supervised run of T4.2.
- A real deploy. The health check was a local web server on this machine, started by hand.
- A deploy that never comes up: the stopped line, its top-priority notification and `afk resume` clearing it, against the real binary.
- Any park during a real ship (no checks after five minutes, the hand-back limit, a required environment variable, a conflict with the base, a failed push or merge), and the un-park that follows an answer.
- `afk daemon` left running as a loop. Every pass here was `afk daemon --once`.
- The daemon starting the session itself. Both sessions were staged by hand, so the step from a finished agent to `shipping`, and token recording, were not part of this run.
- A kill after the push and before its save, and a kill during the clean-up.
- What `gh pr checks` exits with while checks are pending or failed, and the `rebase` merge method.
- Push notifications. The scratch project had no ntfy address.

**The repository is left in place.** This machine's GitHub login cannot delete repositories. `dphaener/afk-ship-e2e-20261011` is private, holds nothing but the files above, and can be deleted by the owner at any time.

### Not verified, owner check

- **Delete the test repository.** `dphaener/afk-ship-e2e-20261011` (private) was made for the real run. This machine's login cannot delete repositories.
- **A real agent receiving a typed line.** The real run used a script in place of the agent. On the first supervised run, watch a CI hand-back or an answered inbox item reach the session and be submitted.
- **A flaky host test.** The review's full `make check` failed once on `TestStuckChildStopsWritingOutputWhenToldTo` in `internal/host` while other Go commands were running. It passed alone, and this change does not touch it.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes: the README's old "Shipping" paragraph was replaced because it had become false; a few best-effort calls discard their errors; the stop-the-line and park paths were not reproduced against GitHub.

### Ideas left out

- Have the host deliver a client's queued input before acting on its detach, so `SendInput` needs no pause.
- Strip GitHub's colour codes from the log tail in `ci-failure.md`.
- Record tokens for a session that finishes while its task is parked.
- List `ledger init` in the usage text of `afk` with no arguments.

## T0.5

T0.5 makes every test in `internal/host` and `internal/ui` reliable. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The survey (A1).** Before planning, each package was run many times with the race detector: 20 runs of `internal/host` without load, 20 with eight busy loops on four CPUs, and 10 of `internal/ui -short`, plus about 2,000 runs of single tests. Five host tests failed at least once; no `internal/ui` test did. The full 100, 50 and 50 runs of the acceptance are run once, after the fixes, and double as the last survey.
- **The SIGTERM fault is fixed in the host (A2).** A client that connects just as the session ends could be told "connection dropped" when the truth is "session ended". A user can hit that, so the host is changed: a connection that arrived before shutdown but was not yet attached is told the session ended. The wait for it is bounded, the message format does not change, and the test keeps its assertions.
- **The stuck-child tests wait for an acknowledgement (A3).** The test program checks its switches every 10ms, so one more heartbeat could arrive after the test thought output had stopped. The program now says when it has seen a switch change, and the test waits for that in place of a fixed 200ms pause.
- **The input-flood test (A4).** It sent a fixed amount of input and expected the host to drop some. When the terminal took all of it, nothing was dropped. The task proves that with a run first, then makes the test reach a drop whatever the terminal's capacity.
- **The refused-host test (A5).** It checked for the first host's `host.json` before that host had written it. It now waits for the file first.
- **Pauses that never failed stay (A6).** Other tests use fixed pauses too. None failed in the survey, and the master plan says to prove a cause before changing anything, so they are listed below and left alone.
- **`internal/ui` is unchanged (A7)** unless the acceptance run shows a failure.
- **Who runs the long runs (A8).** The acceptance runs take about four and a half hours. A task agent must not hold a command longer than about four minutes, so the conductor runs them as one background command after the fixes and before the review.
- **Shipping (A9).** As the session description asks: push, open a draft PR, mark it ready, wait for CI and merge on green, with a merge commit. This would normally have gone to the owner; the master plan's bootstrap section prescribes it.
- **Test first for the host fix (A10).** A test forces the bad ordering on purpose and must fail on the old code.
- **Prerequisites (A11).** Nothing had to be in place before the work started.

### The survey

Before planning, `internal/host` and `internal/ui` were run many times with the race detector, with and without eight busy loops standing in for a loaded machine. Five host tests failed at least once. No `internal/ui` test did. "Alone" means the one test run again and again; "package runs" means the whole `internal/host` package run once per count.

| Test | Failing line and text | Rate |
|---|---|---|
| `TestServeSIGTERMBehavesLikeExit` | `host_test.go:442`: `reading frame header: EOF` or `connection reset by peer` | 163 of 1600 alone; 3 of 20 package runs under load |
| `TestStuckChildStopsWritingOutputWhenToldTo` | `stuckchild_test.go:303`: `output grew by 17 bytes after stopWriting` | 37 of 200 alone; 5 of 20 package runs |
| `TestStuckChildStopsBothWhenToldTo` | `stuckchild_test.go:386`: `output grew while both switches were off` | 1 of 20 package runs under load |
| `TestServeStaysLiveWhenTheChildStopsReadingItsInput` | `host_test.go:884`: the host log never contained `bytes of input` | 7 of 40 alone; 2 of 40 package runs |
| `TestServeRefusesWhenALiveHostIsListening` | `host_test.go:517`: `stat .../host.json: no such file or directory` | 1 of 20 package runs under load |

`internal/ui -short` passed 16 of 16 runs, so nothing in `internal/ui` was changed. The line numbers above are from the unchanged code; the tests have moved since. Each fix task also saved its own "before" run, which gives a second, independent count (below). The 500, 200, 100 and 50 acceptance runs are in the last section.

### The causes

- **`TestServeSIGTERMBehavesLikeExit`: a real fault in the host.**
  - *What happened, step by step.* A client dials the host's socket. The dial succeeds as soon as the kernel has queued the connection, which is before the host has even looked at it, so the client believes it is connected. If the host starts shutting down in that gap, the connection is in one of two places. Either the host's accept loop has taken it and is holding it, not yet handed to the serve loop, which has stopped listening: the old code then closed it with nothing in front of the close. Or it is still waiting in the listen queue: it was reset when the host closed the listener. The client reads a bare close or a reset as "connection dropped", when the truth is "the session ended".
  - *How it was proven.* 33 of 500 runs of the test failed on the unchanged host (`.afk/t0-5/evidence/w1-t1-before.txt`): 30 with `reading frame header: EOF` and 3 with `connection reset by peer`. The two texts are the two places a waiting connection can be: `EOF` is the bare close in the accept loop, the reset is a connection still in the queue when the listener closed. The new test below forces both placements and fails on the old host in all four of its rows.
  - *Where the fault was.* In the host (`internal/host/host.go`). The test was right to expect an Exit frame.
  - *Could a user have hit it?* Yes. A user who opens or attaches to a session at the moment it ends, or by a signal, could see `afk attach: connection dropped` and exit code 13, where the truth is "the session ended" and code 11. The session itself was not harmed; it was ending anyway. The rate for a real user is unmeasured: the test hits it about 1 run in 15 to 1 in 10 because it dials on purpose just before shutdown.
  - *What the fix does.* On shutdown the host now stops its accept loop, tells the connection that loop was holding that the session ended (an Exit frame instead of the bare close), then takes every connection still waiting in the listen queue and tells each the same. The flush of all these Exit frames, the attached client's included, shares one wait of at most `clientFlushGrace` (250ms). The frame format is unchanged, and so is the bare close the host sends in answer to a `Detach`, which is how a client tells "detached" from "ended". `TestServeSIGTERMBehavesLikeExit` is unchanged and keeps its assertions.
  - *Which test pins it.* `TestServeTellsUnattachedClientsTheSessionEnded`, new, in `internal/host/host_test.go`. It holds a connection inside the accept loop through a test seam (`afterAccept`, which does nothing in the shipped host) and leaves others in the listen queue, then starts shutdown three ways: a client's Exit request, a signal, and the child exiting on its own. It was written first and failed on the old host (`w1-t1-before.txt`). On the fixed host it passed 100 of 100. Two mutation checks, each run with `-count=1`, turned it red: removing the queue drain fails every "still in the listen backlog" client and none of the "held by the accept loop" ones; putting the bare close back in the accept loop fails the held client in all four rows.
  - *After the fix.* `TestServeSIGTERMBehavesLikeExit` passed 500 of 500, and 200 of 200 under eight busy loops (`w1-t1-after.txt`). One whole-package run followed; the only failures were the two stuck-child tests below, which the next task fixed.

- **`TestStuckChildStopsWritingOutputWhenToldTo` and `TestStuckChildStopsBothWhenToldTo`: a fault in the tests.**
  - *What happened, step by step.* The test program (the "stuck child") checks its on/off switches once every 10ms. A test flips a switch, sleeps a fixed 200ms and then expects the output to stop growing. But the program could be between two checks when the switch flipped, so it wrote one more heartbeat (17 bytes) after the test believed it had stopped, and the test saw the output grow. A second race hid behind the first: heartbeats the program had already written could still be sitting in the terminal's buffer and be read by the test's collector later.
  - *How it was proven.* 100 runs of the two tests on the unchanged code: 14 failures of the first (`output grew by 17 bytes after stopWriting`) and 16 of the second (`output grew while both switches were off`) (`w1-t2-before.txt`). Exactly 17 bytes is one heartbeat.
  - *Where the fault was.* In the tests. The host and the real agent are not involved.
  - *Could a user have hit it?* No. The stuck child exists only in the tests, and the host code it exercises was not at fault.
  - *What the fix does.* The program now says when it has seen a switch change: it writes an acknowledgement file after its own write loop has gone round, carrying how many heartbeats it has written. `setStuckChildFlag` waits for that, and for the output to hold that many heartbeats, in place of the fixed pause. All callers wait now, including the liveness tests. The `20 * stuckChildPollInterval` pause stays in the two tests, but only as the window in which a stray write would have to show up; it no longer synchronises anything, and it was not lengthened.
  - *Which test pins it.* The two tests themselves. After the change: 200 of 200 runs of the three fast stuck-child tests, 200 of 200 of the input-consuming one (run in three parallel chunks, because it holds a 2s window per run), and 100 of 100 and 100 of 100 under load (`w1-t2-after.txt`).

- **`TestServeStaysLiveWhenTheChildStopsReadingItsInput`: a fault in the test.**
  - *What happened, step by step.* The test makes the child stop reading its input, then sends a fixed 256 chunks of input (about 22KB) and expects the host to drop some and say so in its log (`bytes of input`). The host only drops input when its own queue of 64 chunks is full, and that needs the terminal to be full first, with the host's writer stuck in a write. In the failing runs the terminal and the queue together took the whole flood, so nothing was dropped and the line was never logged. The host was behaving correctly.
  - *How it was proven.* 15 of 40 runs of the unchanged test failed (`w1-t3-before.txt`), and none of the 15 failing host logs has any `dropped` or `discarding` line, so the queue never overflowed. The host logs a drop only when the queue is full, and always logs the first one.
  - *Where the fault was.* In the test: it assumed a fixed flood size always overflows, which depends on how much the terminal will take.
  - *Could a user have hit it?* No. A user who types into a session whose child has stopped reading gets the behaviour the test is meant to check: the host stays live. The test's own flood size was the only thing that varied.
  - *What the fix does.* A new helper, `floodUntilInputDropped`, sends chunks until the host's log shows the drop, bounded by the test's time budget, and every assertion after the flood is unchanged. There is no sleep, retry or longer timeout. A bigger fixed flood was rejected (see below).
  - *Which test pins it.* The test itself: 100 of 100 runs, and 50 of 50 under load (`w1-t3-after.txt`). This task did not measure how much the terminal actually holds.

- **`TestServeRefusesWhenALiveHostIsListening`: a fault in the test.**
  - *What happened, step by step.* The test starts a first host and a second one on the same socket, and checks that the refused second host left the first one's `host.json` in place. A host reports ready when its socket accepts connections, but writes `host.json` only after it has started the child, so the test could look for the file before the first host had written it, and report "the refused host removed it" when it had never been there.
  - *How it was proven.* The failure is rare (1 in 20 loaded package runs), so it was proven by forcing it, not by reproducing it under load: with a temporary 300ms delay before `host.json` is written, 3 of 3 runs failed with `no such file or directory` (`w1-t4-before.txt`). The delay was removed by hand afterwards. The refusal path also returns before the code that removes the host's own file is registered, so the refused host cannot be removing the first one's.
  - *Where the fault was.* In the test.
  - *Could a user have hit it?* No. The host's refusal behaved correctly in every run seen.
  - *What the fix does.* The test waits for the first host's `host.json` to exist and parse (`awaitMeta`) before starting the second host.
  - *Which test pins it.* The test itself: 200 of 200 runs, and 200 of 200 under load (`w1-t4-after.txt`). The fixed test was not re-run against the 300ms delay, so the proof that it now waits is the code and the clean runs, not a red-to-green run.

### Decided while building

- **A listener deadline cannot drain the queue.** The work plan offered a deadline as a way to bound the drain. The `net` package's accept checks the deadline before it asks the kernel (`internal/poll`'s `FD.Accept` calls `prepareRead` first), so an expired deadline fails without looking, and a future one makes every shutdown wait that long on an empty queue, which also makes the result depend on the wait being long enough under load. The host asks the kernel directly with a non-blocking `accept(2)` (`acceptQueued`), which returns the moment the queue is empty. An expired deadline is still used for one thing: waking the accept loop out of its parked `Accept` so it can be stopped first.
- **Rejected: a marker connection the host dials to itself** to learn when the queue was empty. If the socket file had been removed and a new host had bound the same path (a temp-file cleaner on a long session), the marker would have connected to the other host and stolen its client.
- **How long shutdown can take.** Every step is bounded by `clientFlushGrace` (250ms): the wait for the accept loop to stop (only if it is starved), the drain (only if a client keeps dialing in a loop) and the flush (as before). Because the flush is shared, it does not grow with the number of clients. A connection missed because a bound ran out is reset as before, and attach reports it as a drop, which discovery then corrects. A whole `-race` package run still took about 61 seconds after the fix (60.7s), so shutdown did not get slower. The new test takes about 0.16s a run.
- **Connections that arrive after shutdown began** but before the drain finishes also get an Exit frame. That is true for them too, so no attempt is made to tell them apart.
- **The test seam is a package variable, `afterAccept`,** copied once into the host's runtime at construction, the same way `livenessTiming` is. A test sets it before starting the host and restores it in a cleanup registered before the host starts.
- **Edits outside the line ranges the work plan listed.** The plan's ranges for the host fix were where to read; the fix also needed the host's runtime struct and constructor, a new `clientConn.end()` beside `endAndWait`, and the new test and its helpers. Files touched: `internal/host/host.go` and `internal/host/host_test.go`. `protocol.go` and `internal/ui` are untouched.
- **A harmless extra log line.** During shutdown a second host probing the socket (it dials and closes at once) can now produce one extra `writing Exit frame to client: broken pipe` line in `host.log`. It is only a log line.
- **The stuck-child acknowledgement covers the write switch only (A3).** A fixture blocked inside a read cannot look at a flag, so waiting for a read acknowledgement would deadlock, and no test asserts on the instant the program stops reading.
- **The input-flood test sends until the drop is logged (A4),** not a bigger fixed flood, because no upper bound on what the terminal absorbs was proven and the moment the fixture stops reading is not synchronised with the test.
- **`awaitMeta` for the refused-host test (A5).** It reads `host.json` the way a reader does, so it waits for a file that parses, not merely one that exists. Only the forced-delay proof was done, as the task allowed for a failure this rare.
- **The execution engine changed part-way.** The first task ran under the workflow script and the rest as plain sub-agents, at the owner's request, after a quoting bug in the work plan's CPU-load command (`trap "kill $P"` expanded `$P` while it was empty) left busy loops running and hung a command. The plan's Verification Strategy now has the corrected command (`\$P`, and the loops redirected to `/dev/null`).
- **Not verified.** The host fix was run on Linux only. It uses `syscall.Accept`, `syscall.ForkLock` and `net.FileConn`, which exist on macOS too, but nobody has run it there yet; the owner's next local `make check` is the first macOS run. Every fix has a "before" run on the old code and clean "after" runs, except that the refused-host failure was shown by a forced 300ms delay, not by reproducing it under load, and the fixed test was not re-run against that delay. No built binary was driven by hand: the user-visible symptom (`afk attach: connection dropped`, exit 13, for a session that ended) was reasoned from the code and the tests, not reproduced with a terminal.
- **README unchanged.** It already says exit code 11 is "the session ended" and 13 is "the host closed the connection without the session ending", and lists the three attach outcomes. After the fix a client in this window gets 11 where it used to get 13, which matches what the README says it should.

### Pauses left alone

These fixed sleeps are still in the tests. None failed in the survey, and the master plan says to prove a cause before changing anything, so they were not touched (A6). Line numbers are from the files as they stand after this task's fixes.

- `internal/host/host_test.go:323`: 50ms for the writer goroutine to take a frame and block in a write that nobody is reading.
- `internal/host/host_test.go:397`: 200ms after a detach, to check the host did not exit.
- `internal/host/stuckchild_test.go:380` and `:466`: `20 * stuckChildPollInterval`, the window in which a stray write would have to show up. These are the pauses the stuck-child fix kept, now only as observation windows (the work plan listed one line here; there are two).
- `internal/host/liveness_test.go:506`, `:539`, `:574`, `:665`: `3 * probeCycle`, the time given for probes to run before checking nothing happened.
- `internal/host/client_test.go:710`: a deliberate delay before a test listener appears.
- `internal/host/backlog_test.go:127`: 1ms to read slowly during a flood, on purpose.
- `internal/ui/hostpane_test.go:149`: 50ms for a write to park inside the connection.
- `internal/ui/focusrequest_test.go:1045`: `cmdDeadline`, 300ms, how long the test helper waits on one command from a batch.

### Acceptance runs

Run once, after all the fixes, with race-built test binaries (`go test -race -c`), each started from its package directory. Load is eight busy loops on the VM's four CPUs. The commands and full output are in `.afk/t0-5/evidence/acceptance/` (`run.sh` is the script, `summary.txt` the counts).

| Run | Result |
|---|---|
| `TestServeSIGTERMBehavesLikeExit`, 500 runs | 500 of 500 passed |
| `TestServeSIGTERMBehavesLikeExit`, 200 runs under load | 200 of 200 passed |
| `internal/host`, 100 package runs | 100 of 100 passed |
| `internal/ui -short`, 50 package runs | 50 of 50 passed |
| `internal/host`, 50 package runs under load | 50 of 50 passed |

- **The 100 host runs and the 50 `internal/ui` runs ran at the same time,** to save about an hour and a half. Each was the other's load, so neither is a quiet-machine run; that is harsher than the acceptance asks for, not easier.
- **The conductor ran these, not a task agent (A8).** They took about two hours and forty minutes.
- **`make check`** is run by the hands-on QA review check and by CI.

## T2.2a

T2.2a makes a merged task's code take effect by itself. A project can name a command that the daemon runs after each merge, and the daemon stops when its own program file changes so that the service manager starts the new one. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **How the command's run is remembered (A1).** Two places. The ledger gets one new ship word, `after_merge`, which says the merge is finished and the command is due. A small file, `.afk/build/after-merge.json`, says for which task and merge the command started and how it ended. The start is written before the command starts.
- **The daemon dies while the command runs (A2, A12).** The next daemon finds a start with no end. It does not know whether the command finished, so it stops the line and tells the owner. It never runs the command a second time: a second `make install` on top of a half-finished one is the worse mistake. Stopping the daemon does not kill the command.
- **The daemon waits for the command (A3).** The command runs inside the daemon's pass, for up to `after_merge_timeout`. Nothing else is looked at meanwhile. With one task at a time this costs nothing, and a command run in the background would need tracking across restarts.
- **A failed command (A4).** It stops the line the same way a failed deploy does, with the same marker. `afk resume` clears it and the task is then marked done. afk does not run the command again and does not check the deploy: clearing the stop is the owner saying it is dealt with.
- **The item does not block (A5).** The master plan asks for a blocking item. The item is raised at top priority but is not blocking, the same as for a failed deploy. What holds the line is the stop marker, and an item the owner could answer would be a second way out that leaves the marker in place.
- **The deploy wait (A6).** When a command ran, the deploy's time limit is counted from the command's end, so a slow command does not use it up.
- **What counts as a new binary (A7, A9, A10).** The program file's size and modification time, at the path found once at start. No hash. `afk daemon --once` does not look. If the file cannot be read at start, the daemon carries on and `afk daemon status` says once that it will not restart by itself.
- **The log line (A8).** One line in each project's decision log, since the log is per project and the daemon is per machine.
- **How the command is run (A11).** Through `sh -c` in the project's main checkout. Its output goes to `.afk/build/after-merge.log`, replaced on each run, and the last 20 lines go into the item when it fails. On a timeout the command and everything it started are stopped.
- **The real check (A13).** Done with a scratch binary, scratch config and state folders and a short-lived systemd user unit, because the real `afkd` unit on this machine was running the session that built this.
- **Installing (A16).** `make install` is run once, after the review approves.

### The real check

One real run, on 2026-10-12 (UTC), on this machine. Tools: go 1.25.14, git 2.53.0, systemd 259, on Linux 7.0. The full transcripts are in `.afk/t2-2a/evidence/w6-t1-after-merge.txt`, `w6-t1-failures.txt` and `w6-t1-restart.txt`.

**What was run.** A binary built from this branch to a scratch folder, with a scratch home, config and state folder, so nothing touched the real ones. The project was a throwaway git repository with one commit, a `.gitignore` with `.afk/`, a master plan of small tasks and a `config.toml` naming it. It had no remote: the `after_merge` state reads neither git nor GitHub. The project was paused for the whole run and the daemon's `PATH` was `/usr/bin:/bin`, so no agent could be started. Each task was put by hand at status `shipping`, ship state `after_merge`, with a made-up merge commit and a session name that had nothing on disk. Then `afk daemon --once` was run again and again.

The ship asks at most once in 20 seconds. In place of waiting, the time in `.afk/build/ship-poll` was moved back 30 seconds between passes; the transcripts mark each place. One pass in each run was made at once, to see the wait.

**The command works (T0.1, `echo ran >> after-merge-ran.txt && pwd`).**

- Pass 1 ran the command and said "the after-merge command finished". The log holds afk's header line, the project folder printed by `pwd`, and "afk: the command finished with exit code 0". The run record holds the task, the merge commit, a start, a finish and exit code 0. The ledger still said `after_merge`.
- Pass 2, at once: "waiting a moment before asking GitHub again". Nothing ran.
- Pass 3 found no deploy to check and saved `deploy_healthy`. Pass 4 cleaned up and marked T0.1 done. A session with nothing on disk counts as cleaned, so the made-up session did not park the task.
- Pass 5: "paused", and nothing started.
- `after-merge-ran.txt` holds one line. It still held one line at the end of every run below.

**The command fails (T0.2 and T0.5, `echo boom; exit 3`).**

- The pass that ran the command said "the after-merge command ended with exit code 3" and wrote the record with exit code 3. It did not stop the line yet.
- The next pass past the 20 seconds wrote the stop marker, saved ship state `stopped` and raised one item, "The after-merge command failed after T0.2", not blocking. Its body carries the end of the log, with "boom" and the exit code, and says what to do. `afk daemon status` said the line is stopped and why.
- Two more passes after the stop said the same and ran nothing. For T0.5 the command also counted its own runs in a file: one line, before and after. The log and the record kept their times.
- `afk resume` named the task and the reason and cleared the stop. The next pass marked the task done. The command did not run again.

**The command runs too long (T0.3 and T0.6, `sleep 30` with `after_merge_timeout = "2s"`).**

- The pass took 2 seconds and said "the after-merge command did not finish within 2s and was stopped". The log ends "afk: the command was stopped after 2s". The record has `timed_out` true and exit code -1. No `sleep 30` was left running.
- The next pass stopped the line with the reason "the after-merge command did not finish within 2s". Two more passes ran nothing (one run counted for T0.6). `afk resume` and one pass finished the task.

**afk was interrupted (T0.4).** A run record with a start and no finish was written by hand for a newly staged task. The first pass stopped the line at once, with the item "The after-merge command was interrupted after T0.4" and the reason "afk was stopped while the after-merge command ran, so it is not known whether the command finished". The command did not run, on that pass or the two after it. `afk resume` and one pass finished the task.

**Restart on a new binary.** User systemd was reachable, so this half ran as planned.

- The scratch binary ran as a short-lived unit, `afkd-t22a-check`, with `Restart=always`, `KillMode=process` and `RestartSec=5` (the unit file says 30; 5 kept the wait short), and a 3 second pass.
- Two real session hosts stood in for running sessions, each a real `afk _host` with a shell script as its child. Host A was started from a shell before the unit. Host B was started by the unit's own start command, so it sat in the unit's control group, where a session started by the real daemon sits. Host A turned out to sit in the real `afkd` unit's group, because this whole session does; only host B tests `KillMode=process`.
- The rebuild wrote the same bytes (same checksum, same size) with a new time, at 16:25:06.21. At 16:25:06.58 the daemon logged "afk daemon found a new version of itself on disk and is stopping so it can be started again. Every session is still running." and exited 0. systemd noted that both of host B's processes "remain running after unit stopped", scheduled a restart, and started the unit again at 16:25:11.66. `MainPID` went from 1271814 to 1272129 and `NRestarts` from 0 to 1.
- The project's decision log gained one `daemon_restart` line. Both hosts and their children were still running with the same pids and start times. `afk daemon status` named the new pid.
- Read twice: once as it happened and once from the journal 15 seconds later. Both agree.
- A control came by accident. The first rebuild did not run, because the go command was started with the scratch folders set and its version manager refused. The file was not touched, and over the next 50 seconds the daemon did not restart.
- At the end the unit was stopped. `systemctl --user stop` left host B running, as it should. Both hosts were then stopped by hand, `systemctl --user list-units 'afkd-t22a*'` listed nothing, and the real `afkd` unit had the same `MainPID` (1240815) and `ActiveEnterTimestamp` before and after. The scratch folders were removed.

**Bugs.** None found. Nothing in the code was changed.

**Seen, and left as it is.**

- **A failed command stops the line one pass later.** The pass that runs the command records how it ended. The marker, the `stopped` ship word and the item come on the next pass, at least 20 seconds on. In between, `afk daemon status` says "shipping" with the failure as the reason. Nothing new can start meanwhile, because the task is still `shipping`. An interrupted run stops on the first pass.
- In `after_merge` the wait between passes still says "waiting a moment before asking GitHub again", although GitHub is not asked.
- A timeout's item is titled "The after-merge command failed". Only the body and the reason name the time limit.
- After `afk resume` clears a failed command, the decision log's `shipped` line asks "T0.2 is merged and its deploy is fine. What now?". The deploy was never checked. The same line follows a cleared deploy stop.
- The failure item stays open after `afk resume`. Five were open at the end. It does not block, so nothing waits on it.
- `afk resume` also lifts a pause. The run paused the project again straight after each one.
- A rebuild that changes nothing restarts the daemon, because only the file's size and time are compared (A7). So does a `make install` with no change.
- On each restart systemd logs "Found left-over process ... This usually indicates unclean termination" for every session host. With `KillMode=process` that is expected.
- The interrupted item showed the end of the previous task's log, because the hand-written record came with no run. A real interruption after the command starts leaves that run's own log.

**Not checked.**

- The real `afkd` unit restarting after a real `make install`. It was running the session that built this. It also still runs the old program and the old unit file (`Restart=on-failure`), so it needs the new unit file and one restart by hand first.
- This repository's own `after_merge` (`make install && afk plugin install --target claude`) run by the daemon after a real merge, including whether `make` and `go` are on the daemon's `PATH` under systemd.
- A merge on GitHub leading into the command. Every entry was staged at `after_merge` by hand, so the merge, the base update and the branch delete before it were not part of this run.
- A daemon killed while a real command runs. The interrupted case was a record written by hand.
- A deploy check after the command, and the deploy time limit counted from the command's end. The project had no health address.
- The 20 second wait itself. The poll time was moved back by hand.
- A program file that cannot be read. That was checked on its own while building (`.afk/t2-2a/evidence/w4-t1-unreadable.txt`), not here.
- macOS and launchd.

### Not verified, owner check

- **Install the new unit file first.** The unit installed on this machine is most likely the old one, with `Restart=on-failure`. With it, a daemon that exits 0 for a new version stays stopped. Copy `contrib/afkd.service` into place, run `systemctl --user daemon-reload` and restart `afkd` once by hand.
- **The real `afkd` unit restarting after a real `make install`.** The check here used a short-lived unit and a scratch binary.
- **This repository's own `after_merge` command run by the daemon after a real merge.** Not tried: `make` and `go` on the daemon's `PATH` under systemd, and a build that takes longer than ten minutes.
- **A merge on GitHub leading into the command.** The checks here started from a task already at the `after_merge` state.

### The review

All three checks passed with no blocker or major finding, so no critic ran. `make check` was run once afterwards and was green. Minor notes, left as they are: two field comments in `internal/daemon/daemon.go` still speak only of a deploy; a pass that waits in the `after_merge` state says it is waiting before asking GitHub again, though that state asks GitHub nothing; a timeout's item names the time limit only in its body; and the item for an interrupted run points at an output file that may not exist.

### Ideas left out

- Say "waiting a moment before the next step" in states that do not read GitHub.
- Close the failure item when `afk resume` clears a stopped line.
- Word the `shipped` log line so it does not say the deploy is fine after a stop the owner cleared.
- Run the command in the background, so a slow one does not hold up other projects.

## T2.3

T2.3 makes the daemon recover a session by itself. A session that has stopped answering is restarted, a session whose host process is gone is started again, and the agent is then told to carry on. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Who checks for a stuck session (A1).** The master plan left two ways open: the session's host checks on its own schedule, or the daemon stays connected to the session. The host checks. A session has room for one connection, and anyone running `afk attach` takes it. A daemon that had to stay connected would lose its view every time someone looked at a session. With the host checking and writing the result to `host.json`, the daemon reads a file, and attaching or detaching changes nothing it reads.
- **How the check is switched on (A2).** A host only checks with nobody attached when it is started with a new option, `--probe-unattached`. Every unattended session gets it, and those are the sessions the daemon starts. A session you start by hand behaves as before, and the existing tests of the check are not touched.
- **How the daemon asks for a restart (A3).** It connects for a moment, sends the same restart request the dashboard's `r` key sends, reads the answer and disconnects. Nothing about the messages between a host and its clients changes.
- **Where the count is kept (A4).** In the task's ledger entry, in three new fields: `restarts`, `restart_at` and `continue_pending`. The daemon keeps nothing in memory.
- **What counts, and when the count starts again (A5).** Every restart request and every new start of a host counts one, and it is written down before the daemon acts, so a daemon killed in the middle cannot restart without limit. The count goes back to zero on a new attempt at the task, when you answer the item a task was parked on, and when the task moves on to shipping, failed or done.
- **How many, and how fast (A6).** Two new settings under `[daemon]` in `config.toml`: `max_restarts` (default 3) and `restart_backoff` (default one minute). The first recovery happens at once. The wait before each later one doubles. With the defaults that is at once, after one minute, after two minutes, and then the task parks. With `max_restarts = 0` nothing is ever restarted and the task parks straight away.
- **Starting a host that is gone (A7).** With the agent's own resume command when the session has recorded its conversation id, so it comes back to the same conversation. With the ordinary start command when it has not got that far. Still only after the minute's grace that already existed.
- **Telling the agent to carry on (A8).** A resumed agent comes back sitting at its prompt. Thirty seconds after the new agent started, the daemon types the session's own resume command, `/afk:go --session <name>`, once. That command makes the session read where it had got to from its files. A plain "continue" would not do that. A session started again with the ordinary start command needs nothing typed.
- **When the limit is reached (A9).** One blocking item, "Session could not be recovered: …", and the task parks. Answering it gives the task a fresh set of restarts.
- **If you are attached when a restart happens (A10).** The daemon restarts a stuck session anyway. Its brief connection takes yours, so your pane says the session was taken and you attach again. The session was frozen, so nothing is lost. Typing a failed CI run back into a session already works the same way.
- **Pause, parked and shipping (A11).** A paused project still has its running task recovered, because a pause lets a running task carry on. Sessions of parked and shipping tasks are not recovered by this task.
- **A daemon without the means (A12).** Tests and other callers that give the daemon no way to restart or start a host get the old behaviour: a stopped host is reported once and the task parks.
- **The pass waits for the restart (A13).** Up to 15 seconds, the same as the dashboard. Nothing else is looked at meanwhile.
- **How it is checked for real (A14).** With a scratch binary, scratch config and state folders, a throwaway repository and real session hosts whose "agent" is a shell script. No real agent is started.
- **Tests and prerequisites (A15, A16).** Tests are written with each change. Nothing had to be in place first.
- **Waiting to be sure (A20).** The host's verdict that a session is stuck clears as soon as the agent prints anything. A restart stops the agent, so the daemon acts only when the verdict has stood for a full minute.
- **Timing the prompt (A21).** The time of a recovery is saved before the daemon acts, so the count is safe, and saved again when the action succeeds, so the thirty seconds run from when the new agent really started.
- **A status file with no conversation id (A18).** afk reads such a file as unreadable unless the session is still planning. Recovery handles both: a stuck session with no id cannot be resumed, so the host refuses and the count runs to the limit; a gone one is started again as a fresh conversation that picks its work up from its files.
- **A time in the ledger that cannot be read (A19).** `restart_at` is not checked when the ledger loads. A bad value reads as "long ago", the same as the entry's other times.
- **Planning process (A17 to A19).** The work plan went through adversarial review. The first pass found three critical gaps, all in the plan's wording, and each was fixed in the plan.

### The real check

One real run, on 2026-10-12 (UTC), on this machine. Tools: go 1.25.14, git 2.53.0, systemd 259, util-linux 2.41.3 (`script`), on Linux 7.0. The full transcripts are in `.afk/t2-3/evidence/w5-t1-wedge.txt`, `w5-t1-relaunch.txt`, `w5-t1-cap.txt`, `w5-t1-attach.txt` and `w5-t1-refused.txt`. The first one also holds every set-up file and the clean-up.

**What was run.** A binary built from this branch to a scratch folder, with a scratch home, temp, config and state folder, so nothing touched the real ones. Three throwaway git repositories, each with one commit, a `.gitignore` with `.afk/`, a two-task plan (T0.1, and T0.2 depending on it) and an `afk.toml` naming the plan. The config set `tick = "1s"` and `restart_backoff = "5s"` and left `max_restarts` at 3. Every project was paused, so the daemon could recover a running task but could never start one.

No agent ran. A shell script named `claude` stood first on the `PATH` of the daemon and of every host. It notes how it was started, appends each line typed into its terminal to a file, and answers a terminal resize with one line of output. With a flag file present it prints nothing, turns terminal echo off and ignores the resize, which is what a wedged agent looks like to the host. A second script is wedged from the start.

T0.1 of each project was staged by hand the way the daemon leaves a running task: a worktree and branch `t0-1`, a `status.json` with the nine keys, the empty `unattended` marker, a ledger entry at `running`, and a real `afk _host --probe-unattached` started in a short-lived systemd user unit with the wedged script as its child. Then `afk daemon --once` was run again and again, 5 or 10 seconds apart, and the files were read between passes.

**1. A wedge with nobody attached (host pid 1331497).**

- The host started at 17:33:30. At 17:34:00 it asked for a repaint and at 17:34:05 it marked the child wedged: `host.json` gained `wedged_since`, 35 seconds after the start. The host's log has no "client attached" line before that. Nothing had ever connected.
- Six passes from 17:34:05 to 17:35:02 said "has stopped answering; waiting to be sure before restarting it" and asked for nothing.
- The pass at 17:35:12, the first one more than a minute after the verdict, restarted the session. The ledger showed `restarts` 1, `restart_at` 17:35:12 and `continue_pending` true. The host's pid was still 1331497. Its child went from 1331517 to 1332076, started as `claude --permission-mode bypassPermissions --resume 11111111-…`, which is the resume command with the staged conversation id and the unattended mode. `wedged_since` was gone.
- Five passes waited. The pass at 17:35:42, thirty seconds after the restart, typed the resume command. The stand-in's file holds `/afk:go --session t0-1` once, and `continue_pending` was gone from the ledger.
- Three more passes changed nothing.
- The decision log gained one `restart` line and one `continue` line.

**2. A host that is gone.** The same task, carried on from scenario 1.

- `status.json` was given a fresh `updated_at`, as an agent's last write would, because the minute's grace runs from the last sign of life. Then the host (1331497) and its child (1332076) were killed with SIGKILL at 17:36:21. The socket file, its lock file and the old `host.json` stayed behind.
- Seven passes from 17:36:23 to 17:37:13 waited. The pass at 17:37:24, 63 seconds after the kill, started the host again: `restarts` 2, `continue_pending` true.
- The new host has pid 1333270. Its own command line carries `--probe-unattached`, taken from the session's marker file, and its child (1333298) is `claude --permission-mode bypassPermissions --resume 11111111-…`.
- The stale socket did not stop it. The new host's log says the socket "exists but does not answer … treating it as stale", and the new host listens on the same path. `host.json` was replaced with the new pid.
- The pass at 17:37:54, thirty seconds on, typed the resume command. The stand-in's file holds it once for this child.

**3. A session that keeps wedging (host pid 1332642).** The flag file was set, so every resumed stand-in was wedged too.

- Restarts at 17:38:05, 17:40:01 and 17:42:01, each followed thirty seconds later by the typed resume command, and each child wedged again (verdicts at 17:38:58, 17:40:59 and 17:43:00). The host kept its pid; the child changed each time.
- The pass at 17:44:00, a minute after the fourth verdict, gave up. The task was `parked` on one blocking `failure` item, "Session could not be recovered: T0.1 (t0-1)". Its body says afk restarted the session 3 times, how to look at it, and how to answer or clean up.
- Three more passes: no restart, no second item.
- `afk resume`, `afk inbox answer` and one pass: the task was `running` again and `restarts`, `restart_at` and `continue_pending` were gone from the entry. The un-park typed the resume command into the session, as an un-park always does. The project was then paused again and the host stopped.
- The decision log has three `restart` lines, three `continue` lines, one `give_up` line and one `unpark` line.
- The backoff waits of 5 and 10 seconds could not be seen here. Each new verdict takes about a minute to appear and another minute to be acted on, which is far longer than the wait. The backoff itself is shown by the unit tests (`TestDecide`, `TestBackoffFor` and `TestTickRecoveryGivesUpAtTheCap`), not by this run.

**4. Attaching while the daemon watches.** The session of scenario 2, healthy, host pid 1333270.

- `afk attach t0-1` was run inside `script`, which gave it a terminal, at 17:39:30. The host logged "client attached". Three passes made while it was attached said "running T0.1, phase executing" and the ledger did not change.
- The detach key was sent at 17:40:10. The client printed "afk attach: detached" and exited 0, and the host logged "client requested detach". Two passes after that read the session as before.
- The flag file was then set at 17:41:01, so the running stand-in stopped answering. The host marked it wedged at 17:41:33. The pass at 17:42:37 restarted it (`restarts` 3, new child 1336084), and a later pass typed the resume command.
- That last prompt came at 17:43:45, 68 seconds after the restart and not 30. The run made no pass for this project in between, because it was busy with scenario 3. The daemon typed it on the first pass it was given.

**5. No conversation to resume (host pid 1332685).** The status file was in phase `planning` with an empty `session_id`.

- The host marked the child wedged at 17:36:58. The pass at 17:38:05 asked for a restart and the host refused. The decision log's `restart` line says "asked its host to restart the agent; the host refused: …status.json records no session_id, so there is no conversation to resume".
- The ledger showed `restarts` 1 and no `continue_pending`. The child's pid (1332710) never changed and nothing was typed: the stand-in's file holds only its start line.
- Two more refused requests followed, at 17:38:15 and 17:38:25, and the pass at 17:38:35 parked the task on one "Session could not be recovered" item. The run stopped there.
- Passes were ten seconds apart, so the 5 second wait cannot be told apart from no wait, and the 10 second wait was met exactly.

**Clean-up.** Every host was stopped with SIGTERM, and each took its child with it. No process with the scratch path was left, no `t23-*` unit was left, the scratch folders and the symlink were removed, and the real `afkd` unit had the same `MainPID` (1240815) and `ActiveEnterTimestamp` before and after.

**Bugs.** None found. Nothing in the code was changed.

**Seen, and left as it is.**

- **A session that wedges again is noticed after about a minute, not 35 seconds.** The host asks for a repaint at most once a minute, and a restart does not reset that timer. So one round of wedge, wait and restart took about two minutes here.
- **A restart the host refuses uses up the limit quickly.** Three refused requests took 20 seconds with a 5 second backoff. With the default backoff it is three minutes. The task then parks, which is the right end for a session that cannot be resumed.
- **While a gone host is inside its grace, the pass says "running T0.1, phase executing".** It does not say the host has stopped until it acts.
- **The grace runs from the last sign of life, not from the moment the host died.** This is read from the code and is why the run refreshed `updated_at` before the kill. A host that dies after a quiet minute is started again on the next pass.
- **`host.json` is a moment behind a restart.** Read straight after the restart it still showed the old child and the old verdict; the host writes the new ones on its next one-second tick. A pass in that moment does not act on the old verdict, because the backoff has not run out. With a `restart_backoff` under a second it could.
- **Answering the item un-parks a session that may still be wedged.** The un-park types the resume command at once, and the next pass starts recovering again from a count of zero. The item's body does say to answer when the session is working again.
- **The host's refusal carries the full path of the status file.** It is long in the decision log and in `afk daemon`'s output.
- **A parked task in a paused project reads as "paused; no new task will start".** The pass does not mention the parked task.
- **An attach from a terminal with no size leaves the session at one row by one column.** It stayed that way after the detach. The stand-in still answered the repaint request.
- **The first pass on a project with no ledger raised a "no task list yet" item, and the daemon answered it by itself when the ledger appeared.** That is from an earlier task and worked as it should.

**Not checked.**

- A real agent. Everything here was a shell script, which dies on the first signal and reads a line whenever the Enter arrives.
- A restart while a person is attached: the pane saying the session was taken, and attaching again (A10). The attach here ended before the wedge.
- A child that survives the interrupt, so the restart takes the full nine seconds or fails.
- The backoff waits, as said under scenario 3.
- `max_restarts = 0`, and a gone host with no conversation id or an unreadable status file (started again as a fresh conversation). Unit tests cover them.
- A daemon killed between the ledger save and the restart request. A unit test covers it.
- `afk daemon` left running as a loop. Every pass here was `afk daemon --once`.
- The daemon starting the session itself. Every session was staged by hand, so the first host was started by the check with `--probe-unattached`, not by `afk new`. The relaunched host in scenario 2 was started by the daemon and did carry the option.
- A wedge in a project that is not paused.
- Push notifications for the parked task. The scratch projects had no ntfy address.
- A real reboot of the VM, and macOS.

### Not verified, owner check

- **A real agent being restarted and resumed by the daemon.** Whether the agent's resume command brings back the same conversation in the unattended mode, and whether a real wedged agent stops on the host's signals.
- **A real agent receiving the typed resume command after a restart.** Whether thirty seconds is long enough for the agent to reach its prompt, and whether it then carries on from its recorded phase. First shown by the supervised run of T4.2.
- **A real agent at a terminal nobody has attached to.** Its host has no terminal size until someone attaches. Whether a healthy agent answers the repaint request there, or is taken for wedged, has not been seen.
- **A real reboot of the VM.** Every host gone at once, the daemon started by systemd, and each running task's session started again after the grace.
- **A restart while you are attached.** Your pane should say the session was taken; attach again.
- **macOS.** Nothing here ran on it.

### The review

All three checks passed with no blocker or major finding, so no critic ran. `make check` was run once afterwards and was green. Minor notes, left as they are: the comment on `attached()` in `internal/host/host.go` still says it alone gates the check for a stuck session, which is no longer the whole story for a host started with `--probe-unattached`; one acceptance command in the work plan counted indirect imports and so could not print 0, though `internal/daemon` imports neither the host nor the provider package itself; and the growing waits between recoveries cannot be seen in a real run, because a new verdict takes longer to appear than the waits last, so only the unit tests show them.

### Ideas left out

- Recover the session of a shipping task whose host is gone, so a CI hand-back after a reboot does not park.
- A way to ask a host for a restart without taking the session from a person who is attached.
- Have the dashboard use `host.RequestRestart`, so there is one sender of the restart request.
- Say in `afk daemon status` that a session's host is inside its grace, where today it only says the task is running.
- After the owner answers a "could not be recovered" item, check the session is answering before typing its resume command.


## T0.6

T0.6 brings forge's rebuilt dashboard focus into afk. The dashboard now has one focus, on the list or on the right side, and the right side shows one of two things, the agent or the overview. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Applied, not re-typed (A1, A2).** The forge change is taken as a diff between the commit afk was copied from and forge's newest commit, with forge's name swapped for afk's by one command, and applied with git. All but one of its 35 files apply as they are. Typing 4,400 lines in again by hand would only add ways for afk to drift from forge. The one file that does not apply is a saved screen whose title afk spells differently, and it is generated again from the tests.
- **One agent name stays in a test (A3).** A saved toast in the dashboard's tests already named the agent that resumes after a restart, because the real toast does. The port changes only the end of that line. No new agent name is added to the code.
- **The word "forget" is not "forge" (A4).** The master plan asks that a search for "forge" finds only notes about where the code came from. The dashboard has a function called `forgetPane`, and five other files use "forget". Those are left alone and the check ignores them.
- **Order of work (A5).** The port goes in first, as one piece, because it only builds as a whole. Two small fixes and the note in `docs/provenance.md` follow. The walk-through by hand comes last, so it tests the finished thing.
- **The walk-through touches nothing real (A6, A7).** It runs a test build of afk in a throwaway repository under `/tmp`, with its own state folders and its own tmux server, and a small script standing in for the agent. No real agent starts, and the afk service on this machine is not touched. The screens are saved with the session's files.
- **No install from this branch (A8).** `make install` replaces the one afk program on this machine, which the running service uses. This branch is not merged yet, and the project's `afk.toml` already installs after a merge. So the session does not install.
- **Shipping (A9).** The session stops at reviewed commits, and the daemon ships them.
- **The OpenCode plugin (A10).** Not touched. The change is in the dashboard only.
- **Tests (A11).** Forge's tests come over with the code. Each of the two small fixes gets its test written first.
- **Prerequisites (A12).** Nothing had to be in place before the work started.

### The walk-through

The dashboard was driven by hand, through tmux, against a test build of this branch (`go build -o /tmp/afk-check .`, go 1.25.14, tmux 3.6, a 120 by 40 screen). Everything ran in a throwaway repository under `/tmp/t06` with its own state folders and its own tmux server. A small shell script stood in for the agent: it prints back each line it is sent, so the screen shows whether typing reached the session. No real agent ran. The afk service on the machine had the same process and the same start time before and after. The screens are saved in `.afk/t0-6/evidence/`, in the four files named below.

There were two one-off sessions, `run-one` (running) and `stop-one` (its host and its agent killed), and later a planned session, `wedge-one`, for the step that needs one.

- **a. The start.** The list has the keyboard. The stopped row shows its overview. The running row shows its agent once the cursor has rested on it for a moment. On the very first launch in a repository the running row shows its overview until the cursor moves or `enter` is pressed, which is how forge has always behaved (see the list of things left alone, below). `w3-t1-walkthrough.txt`, a1 to a3.
- **b. `enter` and `f`.** Each moves the keyboard to the right side and connects the session. Typed text appeared in the stand-in's echo and nowhere in the list. b1 and b2.
- **c. `ctrl+\`.** It gives the keyboard back to the list, and the pane stays on screen. An `x` typed with the list focused never reached the session. c1 and c2.
- **d. `ctrl+t`.** With the right side focused it switches between the agent and the overview, and back, without reconnecting. From the list it does nothing. `esc` from the overview returns to the list. d1 to d5.
- **e. The mouse.** With the list focused, a click on the right side moves the keyboard there, over the overview and over the agent alike. The click was sent as the raw mouse bytes a terminal sends (`tmux send-keys -l` with an SGR press and release), which worked. That a click or a scroll with the list focused is not passed on to the session cannot be seen with this stand-in; `internal/ui/mouse_test.go` covers it. e1 and e2.
- **f. A stopped row.** `enter` moved the keyboard to the right side, which showed the overview, and started nothing: the host's process id was still dead afterwards. `r` restarted it at once, with no question asked, from the list and, in a second run, from the right side, where the keyboard stayed on the right and typing reached the new agent. `w3-t1-stopped.txt`, f1 to f8.
- **g. A second client takes the session.** With the right side focused on `run-one`, `afk attach run-one` was run in a second tmux window. The dashboard kept the keyboard on the right and said "another client took over run-one · enter to reconnect". Then `q`, `j`, `c`, `n`, `p`, `r` and `x` were typed into the dashboard: it did not quit, the cursor did not move, nothing opened, the host was not restarted, and none of the keys reached the session. After the second client left, `enter` reconnected. `w3-t1-steal.txt`, g1 to g7.
- **h. A dropped connection.** Two kinds were staged. In the first the host stays alive: the dashboard was frozen for a few seconds while the session printed 30MB, so the host gave up on it ("client is not consuming output … dropping it" in the host's log). The dashboard kept the keyboard on the right, said "disconnected from stop-one · enter to reconnect", ignored `q`, `j`, `c` and `n`, and reconnected on `enter`. h4 to h6. In the second the host itself is killed: the dashboard reports a dropped connection, keeps the keyboard on the right, and about two seconds later, once it has seen the host is gone, shows the row's overview, where `r` restarts it. h2, h3, h7 and h8; see the first item under "seen and left alone".
- **i. Moving the cursor.** The pane left the screen the moment the cursor left its row, the next row's own pane arrived, and each pane showed only its own session's text. The host's log shows the dashboard letting go of the session on every move away. i1 to i5.
- **j. `afk dash --open`.** For the first row it worked. For any other row it did not, which is the first finding below. After the fix the cursor is on the named session and its pane is on screen, for a running session, for a stopped one (restarted first) and for the real hand-off from `afk new`. A name that does not exist starts the dashboard normally with a message. j1 to j10.
- **k. A wedged row.** The stand-in prints nothing unless typed at, so the host marked it unresponsive after about 35 seconds. On the planned session, `r` asked "Restart "wedge-one"?"; `n` cancelled; `y` restarted it. The same from the right side with the overview focused. With the agent pane focused, `r` is typing, as the banner says. On a one-off session `r` is refused with a message, because a one-off has no conversation to resume. The stand-in records no session id, so one was written into the throwaway session's `status.json` by hand for the restart to go through; without it the dashboard reports the host's refusal. k1 to k14.

The same file has two captures whose first labels were wrong and were corrected afterwards (k2a and k6a), and one slip by the operator, marked as such (j2): text was typed into a dashboard whose list had the keyboard, and the letters, being list keys, created a one-off session in the throwaway repository.

### Found, and to go back to forge

**Fixed here. Forge has both.**

1. **`afk dash --open <name>` showed the wrong session unless it was the first row.** The dashboard connected to the named session but left the cursor on the first row. A pane is only drawn for the selected row, so the screen showed the first row's overview, the message said "opened <name> · enter/f to focus", and `enter` then focused the first row. The connection nobody could see also held the named session's one client slot. This is the hand-off `afk new` uses, so a new session that does not sort first landed the user on a different session. Forge has it at both commits (`maybeOpenInitialPane` in `internal/ui/model.go` is the same at `9c847e1` and `d9c6a62`); the rebuilt focus model only makes it plainer, because its rule is that the pane always belongs to the selected row. The fix: `maybeOpenInitialPane` moves the cursor to the named session before it connects, the same three calls a click-to-focus already makes. Test: `TestStartupPaneBelongsToTheSelectedRow` in `internal/ui/focusmodel_test.go`. `README.md` says so under `--open`.
2. **A message named a key that did nothing.** After restarting a wedged session from the focused overview, the message said "enter/f to reopen the agent". `enter` does nothing in the focused overview; `ctrl+t` is the key that shows the agent there. Forge's rebuild introduced this (`d9c6a62`; at `9c847e1` these messages said `ctrl+t`). The fix: a small function, `agentKeyHint`, picks `ctrl+t` when the overview has the keyboard and `enter/f` otherwise, and the four messages that can appear while the overview is focused use it (a restart that worked, a restart with no answer, the one-off restart refusal, and a connection dropped behind the overview). Test: `TestAgentKeyHintNamesAKeyThatWorks`, which also presses both keys to prove which one works.

Both tests were run with their fix taken out, and failed, and with it in, and passed: `.afk/t0-6/evidence/w3-t1-fix-tests.txt`.

**The two quality fixes from earlier in this task, which forge does not have yet.**

3. **"input goes to X" only when it is true.** A click-to-focus whose focus was not honoured (the user had gone back to the list, or to the overview, while it connected) still said "input goes to X". The wording is now used only when the focus was honoured. Test: `TestFocusRequestArrivalWording` in `internal/ui/focusrequest_test.go`.
4. **One definition of "a restart is in flight".** `agentPlaceholderText` spelled the test out a second time; it now calls `restartingRow`. No change in behaviour. Test: `TestAgentPlaceholderTextNothingSelected` in `internal/ui/views_test.go`.

**Seen and left alone.** Forge has all of these. None breaks a rule of the focus model, and each would be a change of design, not a repair, so they are recorded for forge to decide.

- **When the host itself dies under a focused pane, `q` quits the dashboard two seconds later.** A steal, or a drop with the host alive, shows a placeholder that swallows keys, which is the fix the rebuild was for. But if the host process is killed, the row soon reads "stopped", a stopped row shows its overview, and the overview's keys are live: `q` quits, `r` restarts. Someone typing a sentence at that moment could quit the dashboard. No session is lost by that. It is the same state a session that ends normally leaves behind, and forge chose it on purpose ("a row with no live agent shows its overview"). Changing it means deciding what a dead row's right side should do with stray keys. Evidence: h7 and h8.
- **The first launch in a repository does not show the running row's agent.** The agent is previewed at the start only when the dashboard remembers a row from an earlier run. It has been that way since before the rebuild.
- **A message can outlive what it describes by a few seconds.** "run-one still running in the pane" stays up for its usual few seconds after the cursor has moved to another row and the pane has gone. Evidence: i2.
- **The wedged banner and the help line offer `r` on a one-off session, where `r` is refused.** The refusal explains itself, so nothing is lost, but the offer is misleading. Evidence: k1 and k2.

## T2.4

T2.4 puts the usage governor into the daemon. The daemon now asks it before starting anything and while a task runs, stops a running session when usage is nearly spent and starts it again after the reset, reads the rate-limit marker, and checks that the agent is still logged in. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### The spike

The master plan asked for a spike on the VM before the pause was built. It was run on 2026-10-12 against real interactive sessions of the agent CLI (version 2.1.296, on its cheapest model), in throwaway repositories under tmux. Six short sessions. The screens and commands are in `.afk/t2-4/evidence/spike/`.

Each session was given slow, countable work (create forty files, three seconds apart), in the main conversation and in a background sub-agent, so it was plain whether work had stopped.

| Way to pause | Stops a background sub-agent | Safe to send without looking | Survives a long gap | Verdict |
|---|---|---|---|---|
| Escape typed into the terminal | No. The sub-agent kept creating files. | No. Two in a row open the Rewind menu, or clear typed text. | Yes for the main conversation (tried at six minutes) | Not enough |
| Ctrl+C typed into the terminal | Only when the main conversation is idle. Mid-turn it stops the turn and nothing else. | No. Two within a second quit the CLI. | Yes | Not usable blind |
| Freeze and thaw the process (SIGSTOP, SIGCONT) | Not confirmed | Yes | No. After a six-minute freeze the CLI said "The response stopped arriving". | No |
| End the process (SIGINT or SIGTERM), resume the conversation later | Yes. Everything in the process is gone. | Yes. It works whatever the agent is doing. | Yes. Nothing is held open, and the conversation is on disk. | Chosen |

What ending the process looked like: the CLI exited at once on either signal, no child was left, and the files stopped. Started again with its resume option in the same folder, the CLI restored the whole conversation and printed that a background agent "didn't finish before the previous session ended". One typed line then made it start that sub-agent again from where it had got to.

What is lost is the one call in flight, and any command that was running at that moment.

Not tried: a real usage limit (nobody trips one on purpose), a gap longer than six minutes, and a session running a Workflow script. The freeze was also found not to work at all under tmux, which thaws the process by itself.

The spike agent ended its test sessions with `tmux kill-server`. The tmux server had no other sessions at the time. The real check for this task is told not to do that.

### What was decided, and why

- **How a session is paused (A1, A3, A25).** The daemon asks the session's host to end its agent, the same request `afk clean` sends. The host ends too. If the host is still there a minute later the daemon sends it a terminate signal and waits ten seconds. It never kills the host outright: a host killed while it is still stopping its agent would leave the agent running where afk can no longer reach it. If it is still there after three minutes, one blocking item says the session could not be stopped and may still be spending, and the task parks. This is the only way the spike found that stops background sub-agents, needs no reading of the screen, and survives a wait of hours.
- **How it is started again (A4).** With what T2.3 built for a host that has gone: a new host runs the agent's resume command, and thirty seconds later the daemon types the session's own resume command, `/afk:go --session <name>`. This does not count against the session's restarts. If the new host cannot be started, the pause is dropped and ordinary recovery takes over, with its limit.
- **Where the pause is written down (A2).** In the task's ledger entry: `usage_pause_at` and `usage_wake_at`. The task stays `running`. The pause is saved before the session is stopped, so a daemon killed in between does not see a missing host and start it again.
- **A paused session is left alone.** It is not recovered, not prompted and not reported as stalled. A session that finishes, fails or has a blocking item is still handled first.
- **When it starts again (A5, A28).** When a reading taken after the pause began says there is room (`run` or `no_launch`), the saved wake time has passed, the old host is gone, and the agent is not known to be logged out. The clock alone never decides, and neither does a reading from before the pause.
- **Extra usage on, with a session running (A6).** The master plan says the daemon refuses to run. It also stops the running session, because an agent that carries on could spend real money. It starts again once extra usage is off.
- **The rate-limit marker (A7).** A marker newer than the session's start and last restart pauses the session. The wake time is the governor's when it has one, otherwise one minute after the earliest reset still ahead among the reading's windows, otherwise one hour. The earliest and not the fullest window's, because the marker does not say which limit was hit and the reading may be minutes old; the wake time is only when the governor is next asked, so waking early costs one reading and waking late can cost days (changed after the real check, see below).
- **When the governor is asked (A8).** Only on a pass with a running task, or one about to start, re-create or un-park something. A reading under `probe_interval` old is reused. A probe that fails is not tried again for a minute. An idle project causes no probes.
- **Shape in the code (A9).** The loop gets four optional hooks: usage, login, stop a session, read the marker. The decision stays a pure function. With no usage hook every new rule is off, so everything that existed before behaves as before.
- **Settings (A10).** A `[governor]` table in `config.toml`: `pause_at` (90), `launch_below` (75), `stale_after` (30 minutes), `probe_interval` (5 minutes, at least 1) and `on_unknown_usage` (`run`). The master plan named the keys and not the table.
- **Files (A11).** `usage.json` gains a `governor` object with the action, the reason and the wake time. A new `governor.json` beside it holds when the daemon last tried a probe and what the last login check said.
- **The login check (A12, A13).** A new provider call. Hourly, and every `probe_interval` while logged out so that logging in is noticed soon. A check that cannot run keeps the last answer: not knowing is not the same as logged out. Logged out stops new work and keeps a paused session paused; a session already running is left alone.
- **Is the login's end visible ahead of time?** No. The CLI's status output has no date in it, so there is no warning ahead of time. The master plan asked for one only "if detectable".
- **Messages (A14).** Extra usage on, and logged out, each raise one blocking item in every project the daemon runs with an urgent notification, and the daemon answers the item itself when the cause is gone. A hold and a pause each send one notification. Holding back new work at 75% sends none.
- **Parked and shipping tasks (A15, A34).** An un-park waits like any start, except at the 75% line, where a running session may carry on and so may a parked one. A paused task that parks keeps its pause and is started again, not typed into, when it un-parks. Ship steps are not held back.
- **Commands (A16).** `afk usage` reads the same settings and writes the governor's action. `afk daemon status` gains a `Usage:` line.
- **Without the means (A17).** A daemon given no way to stop or start a session leaves it running. The CLI's own wait at a limit is then the only guard.
- **No install from this session (A18).** `CLAUDE.md` says to run `make install` after every verified feature. This session does not. The daemon on this machine runs from `~/bin/afk` and restarts itself when that file changes, and it installs from main after each merge. Installing a branch that is not merged would put unmerged code under the running daemon. This would normally have gone to the owner.
- **Tests and prerequisites (A19, A20).** Tests are written with each change. Nothing had to be in place first.
- **The spike ran during planning (A21).** The work plan depends on its answer.
- **Planning process (A22 to A33).** The work plan went through three adversarial reviews, the most the process allows. The third found three more gaps, fixed in the plan: wrong advice shown during a long pause, a rule that could never be reached, and a forced stop that could have left an agent running. The first two are described next. The second found three more gaps, also fixed in the plan: an old reading could resume a rate-limited session over and over, the decision log would have gained a line with every reading, and nothing ran the whole of `make check`. The first found five critical gaps, all fixed in the plan: an acceptance command that could never pass, a marker pause that nothing could end, a resume that wrote the ledger after acting, a stop that was retried for ever in silence, and a probe time limit shorter than the one `afk usage` uses.

### Accepted risks

- **A session running a Workflow script was not part of the spike.** The conductor's resume command re-reads the plan from disk and re-runs what is not ticked, so finished work is not redone. Work in flight at the pause is done again.
- **A failed CI run handed back to a finished session is not held back by a pause.** The ship types into the session whatever the governor says.
- **While a task runs, a hold or a stop on new launches is not logged by the loop.** Nothing changes for the running session, so the loop's state does not change. `usage.json` shows it.
- **What a session prints at a real usage limit has still not been seen.**

### The real check

One real run, on 2026-10-12 (UTC), on this machine. Tools: go 1.25.14, git 2.53.0, jq 1.8.1, on Linux 7.0. The full transcripts are in `.afk/t2-4/evidence/w5-t2-pause-resume.txt` (scenarios 1 to 4, every set-up file and the clean-up), `w5-t2-refuse-login.txt` (5 and 6) and `w5-t2-marker.txt` (7).

**What was run.** A binary built from this branch to a scratch folder, in the manner of T2.3's real check: a scratch home, temp, config and state folder, so nothing touched the real ones, and a short path for the temp folder so the hosts' sockets fit. Two throwaway git repositories, each with one commit, a `.gitignore` with `.afk/` and `.claude/`, a two-task plan (T0.1, and T0.2 depending on it), an `afk.toml` naming the plan and an empty ledger made with `afk ledger init`. Each project had its own config and state folder, so the two halves of the check could not see each other's usage reading. The config set `tick = "1s"` and `[governor] probe_interval = "1m"` and left every limit at its default.

No agent ran. A shell script named `claude` stood first on the `PATH` of the daemon and its hosts and answered three ways. For the usage probe it printed the committed fixture (`internal/provider/testdata/usage/captured.jsonl`) with the percentages, the reset times and the extra-usage flag taken from small control files. For `auth status` it printed `{"loggedIn": true}`, or `{"loggedIn": false}` with exit 1, by a control file. Otherwise it was the stand-in agent: it notes how it was started, appends each line typed into its terminal to a file, and prints a dot every two seconds so its host never takes it for stuck. It sets no trap and starts no child, so one SIGTERM ends it. It also wrote a line to a file for every probe and every login check, which is how the run counted them.

Unlike T2.3's check, nothing was staged by hand to start with and no project was paused: the daemon created each session itself. Everything was driven by `afk daemon --once`, a few seconds apart, with the files read between passes. Between scenarios the scratch `usage.json` and `governor.json` were removed where the transcript says so, because the daemon rightly reuses a reading for a minute.

**1. The session window at 80%, nothing running.**

- Three passes, at 20:55:32, :35 and :38, each said "nothing new is started, because of the usage limits (T0.1): usage is close to the limit". No worktree, no host, no ledger entry, no inbox item.
- `afk daemon status` gave the number: "Usage: nothing new is started. The current session is 80% used, at or past the limit of 75% for starting new work".
- `usage.json` held `governor.action` `no_launch`. The three passes cost one login check and one usage probe; the second and third reused the reading.

**2. At 10% (host pid 1487806).**

- The pass at 20:55:46 created the worktree and branch `t0-1` and started a real host. Its child (1487821) is the stand-in, started as `claude --permission-mode bypassPermissions "/afk:go --session t0-1 --autonomous"`, and the host's own command line carries `--probe-unattached`. The ledger entry was `running`, attempt 1.
- The status file was then edited by hand to what a working agent leaves: a conversation id and phase `executing`. Three more passes said "running T0.1, phase executing".

**3. At 95%, resetting 45 seconds later, with that session running.**

- The pass at 20:56:05 said "usage reached the pause line; pausing the session for T0.1 (t0-1)". The ledger entry gained `usage_pause_at` 20:56:05 and `usage_wake_at` 20:57:50, which is the reset time plus one minute. The status stayed `running`.
- The host's log shows the request arrive, the child end on the signal and the host stop, all inside two milliseconds. A second later no process was left, and `host.json` and the socket were gone.
- Thirteen passes from 20:56:05 to 20:57:48 said "paused for usage until Mon 12 Oct 20:57 UTC". None started anything and none raised an item. None of them probed either: the probe log's last line stayed at 20:56:05, which is the rule that a paused session with its wake time ahead is not asked about.
- The decision log has one `usage_pause` line.

**4. Back at 10%, with the reset time (20:56:50) in the past.**

- The control file was set to 10% at 20:56:30, well before the wake time. The passes up to 20:57:48 still waited, so the clock and not the lower number decided when to look.
- The pass at 20:57:51, one second after the wake time, made one new probe, read 10% and started one new host (1488658). Its child (1488670) is `claude --permission-mode bypassPermissions --resume 22222222-…`, the resume command with the staged conversation id.
- The ledger entry lost both usage fields and gained `restart_at` 20:57:51 and `continue_pending` true. It has no `restarts` key: the resume did not count as a restart.
- Six passes waited. The pass at 20:58:22, 31 seconds after the resume, typed the session's resume command. The stand-in's file holds `/afk:go --session t0-1` once, and `continue_pending` was gone. Five more passes typed nothing.
- The decision log has one `usage_resume` line and one `continue` line. The inbox stayed empty through all four scenarios.

**5. Extra usage switched on (second project, nothing running, usage at 10%).**

- Four passes from 20:59:20 to 20:59:29 said "extra usage is switched on for this account; nothing new starts until it is switched off". Nothing was created. One blocking `failure` item appeared on the first pass, "Extra usage is switched on, so afk starts nothing", and the three later passes added no second one. `usage.json` held `governor.action` `refuse`.
- Extra usage was switched off, with the session window put at 80% so that the same pass would not also start the task (scenario 6 needs nothing running). The pass at 20:59:29 answered the item as `fixed` and wrote an `overage_off` line. It then held the launch for the 80%, as in scenario 1.

**6. Logged out (same project, usage at 10%, extra usage off).**

- The stand-in's `auth status` printed `{"loggedIn": false}` and exited 1. Nine passes from 20:59:44 to 21:00:41 said "the agent is logged out on this machine; nothing new starts until you log it in here". Nothing was created. One blocking item, "The agent is logged out on this machine", appeared on the first pass and stayed the only one.
- While logged out the daemon made no usage probe at all (`probe_tried_at` stayed empty), and it checked the login once, not on every pass.
- The control file was set back to logged in at 20:59:51. Six passes after that still waited, because the check was not due. The pass at 21:00:51, 67 seconds after the last check, checked again, answered the item as `fixed`, made its first usage probe, read 10% and started T0.1 (host pid 1490101), all in one pass.

**7. A rate-limit marker in the running session's folder.** The session of scenario 4, running, with the last reading at 10%.

- `.afk/t0-1/.rate-limited` was written at 20:58:49 with the line the plugin's `StopFailure` hook writes, `{"at":"2026-10-12T20:58:49Z"}`. A pass just before it said "running".
- The pass at 20:58:49 said "the session hit a usage limit; pausing the session for T0.1 (t0-1)". The ledger gained `usage_pause_at` 20:58:49 and `usage_wake_at` 2026-10-15T20:56:25Z. The host (1488658) and its child were gone a moment later.
- Four more passes said "paused for usage until Thu 15 Oct 20:56 UTC", with no relaunch and no item. The decision log gained a second `usage_pause` line. The marker file was left in place.
- The wake time is three days away because of how it is chosen. See the first point under "Seen, and left as it is".

**One more, not asked for: extra usage switched on with a session running.** The session scenario 6 had just started.

- The pass at 21:01:06 stopped it ("extra usage is switched on for this account; pausing the session"), with `usage_pause_at` set and no wake time, and raised the extra-usage item again. Three passes waited: "it starts again when a new usage reading allows it".
- With extra usage off, the pass at 21:01:13 answered the item and started the session again. Its status file had no conversation id yet, so it came back as a new conversation, `claude --permission-mode bypassPermissions "/afk:go --session t0-1"`.

**Clean-up.** One host was still running at the end (the last one above). It was stopped with SIGTERM and took its child with it. The scratch folders and the symlink were removed. `pgrep -af` for the scratch path printed nothing. The real `afkd` unit had the same `MainPID` (1240815) and `ActiveEnterTimestamp` before and after, and `~/bin/afk` was not touched.

**Bugs.** None found. No Go file was changed.

**Seen, and left as it is.**

- **A session stopped by its marker can wait far longer than the limit it hit.** The wake time is the reset of the fullest window in the last reading, and that reading may be up to `probe_interval` old. Here the reading was 58 seconds old and said session 10%, week 20%, so the fullest window was the week and the wake time was the weekly reset, three days on. The control file had already been moved to 60% for the session window, but no new probe was due. A session paused with its wake time ahead is not asked about again, so nothing shortens the wait. On a real account the window that was hit will usually be the fullest one, but not when the reading is a few minutes old and a burst of work filled the session window since. Taking a new reading on the pass that sees a new marker would close this. It is as the work plan has it (A7), so it is recorded here and in the task's issues, not changed. Changed after the check: the wake time is now one minute after the earliest reset still ahead, whichever window is fullest, which would have been the session window's reset here; this was verified by unit tests only, not by a second real run.
- **A wake time is shown to the minute.** "Paused until Mon 12 Oct 20:57 UTC" for a wake time of 20:57:50. The session started again at 20:57:51.
- **Leaving a wait writes two lines.** The pass that launches or resumes after a governor wait writes its own line (`launch`, `usage_resume`) and a `governor` line whose answer is that same word.
- **The two standing items read as if a task waited on them.** `afk inbox show` says "blocking: yes (the task is waiting for this)" for an item that names no task. Nothing parks on them.
- **With no reading, `afk daemon status` has no `Usage:` line.** That was the case while logged out in scenario 6. The project's own lines said why nothing started.
- **A new session started after a pause carries no `--autonomous` on its command line.** The session's `autonomous` marker file carries the mode, as it does for a dashboard restart (`internal/session/autonomous.go`).
- **The host logs a broken pipe when it is asked to end.** The daemon sends the request and disconnects, so the host's reply has nowhere to go. The session still ends cleanly.
- **`host.json` and the agent's first output appear a moment after the launching pass returns.** A read in the same second finds neither.

**Not checked.**

- A real agent. Everything here was a shell script, which dies on the first signal and reads a line whenever the Enter arrives.
- A real usage limit, and what a real session does and prints when it meets one.
- A session running the Workflow tool at the moment it is stopped.
- macOS. Nothing here ran on it.
- A session that will not stop: the terminate signal after a minute and the "could not be stopped" item after three. The stand-in ends at once.
- The resume after a marker pause. The wake time was three days away.
- A hold: no reading, a stale reading, or a probe that fails.
- The weekly windows reaching a limit. Only the session window was moved.
- Logged out with a session running, a parked task under the governor, and a pause that starts while a continue prompt is still owed.
- A daemon killed between saving the pause and stopping the host. Unit tests cover it.
- Two projects sharing one reading. Each project here had its own config and state folder.
- `afk daemon` left running as a loop. Every pass here was `afk daemon --once`.
- Push notifications. The scratch projects had no ntfy address.

### Decided while building

- **A reading counts as "after the pause" only by the whole second.** The pause is saved to the second, so a reading taken in that same second does not resume the session. One more probe is the cost.
- **A fresh marker while extra usage is on gets no wake time.** Extra usage is ended by the owner, not by a clock.
- **A logged-out agent holds back an un-park even at the 75% line.** The exception there is about usage only.
- **A governor word the loop does not know holds a launch,** but does not pause a running session or resume a paused one.
- **The extra-usage item is answered only on a reading the governor could judge.** A hold means there is no usable reading, so it does not show that extra usage is off.
- **A project with an open extra-usage or login item is asked about on every pass,** even during a known wait, so the item is answered when its cause goes.
- **An un-park back to shipping clears the pause.** An un-park back to running keeps it, and the session is started again instead of typed into.
- **"This provider cannot report usage" is remembered while the daemon runs.** It is a fact about the provider. A restart asks once more.
- **When `governor.json` cannot be written, no probe is made** and the daemon holds. Without the saved attempt a failing probe would run on every pass.
- **A file that cannot be used is named in the daemon's log, once.** `afk daemon status` shows only a fixed sentence for a hold, so the log is where the file name is.
- **A forced stop checks what the process is first.** It signals only a process whose command line has `_host` and the session's name. A process id left behind by a crashed host and reused by something else is never signalled. This works on Linux only; elsewhere a session that ignores the polite stop is reported after three minutes.
- **The wake time after a rate-limit marker is the earliest reset still ahead (A35).** The work plan said the fullest window. The real check showed that can be the week when only the session limit was hit, a wait of days. Waking early costs one probe.
- **`afk usage --help` still names the default five minutes.** The help is printed before any config is read.

### Not verified, owner check

- **A real agent paused and resumed by the daemon.** The spike did it by hand with signals and the resume option. The daemon's own path (host asked to end, new host, resume command typed) has only run against a stand-in script. First shown by the supervised run of T4.2.
- **A session in the middle of a Workflow run when it is paused.** Whether the resumed conductor picks the run up cleanly from the plan's ticks.
- **A real usage limit.** What the CLI prints, whether the hook writes its marker, and whether the daemon's pause at 90% keeps a session from ever reaching one.
- **A logged-out machine.** The logged-out output was written by hand from the master plan's description, because logging the VM out is not a machine's call.
- **The running `afkd`.** It keeps the old code until this branch is merged and installed. After that, check `afk daemon status` shows a `Usage:` line.

### The review

All three checks passed with no blocker or major finding, so no critic ran. `make check` was run once afterwards and was green. Minor notes: the wake-time change above differs from the work plan's wording and is recorded as A35, and the quality check ran the changed packages' tests and not the whole of `make check`.

### Ideas left out

- Count rounds of marker pauses. If the limit that was hit belongs to a longer window and the next reading still says there is room, the session starts, stops at the limit and is paused again, once per window reset, and nothing counts it.
- Take a new reading on the pass that first sees a marker, without waiting for `probe_interval`.
- Hold back a CI hand-back while usage is paused or extra usage is on.
- Log a hold or a launch stop that begins while a task is running.
- Have `afk inbox show` not say "the task is waiting for this" for an item that names no task.
- One decision-log line, not two, on the pass that leaves a governor wait.

## T3.1

T3.1 adds a small web page served by `afk daemon`: status, usage, the inbox with an answer form, and pause and resume buttons, with the same data as JSON under `/api/`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **When the page is on (A1).** Only when `[web] token` is set in `config.toml`. With no token and a loopback address the page is off and the daemon says so in one log line. With no token and an address other machines can reach, the daemon refuses to start. The master plan says every request needs the configured token, so with none there is nothing it could serve; a page open to everyone on the machine could answer inbox items and pause builds. This would normally have gone to the owner.
- **The `[web]` keys (A2).** `bind` (default `127.0.0.1:4253`), `token` (at least 16 characters, no spaces) and `public_url` (an `http://` or `https://` address, used for links in notifications). An address counts as loopback only when it is a loopback IP or the name `localhost`. A hostname, `0.0.0.0`, `::` or a bare `:4253` can reach other machines.
- **How a request shows it has the token (A3).** Open the page once with `?token=` and the token; the page sets a cookie and sends the browser to the same address without the token, so it does not stay in the address bar. The cookie holds a hash of the token, not the token. A program can send `Authorization: Bearer` instead. The cookie is `SameSite=Lax` so that a tap on a notification still carries it. The token is never written to a log.
- **Protecting the buttons (A4).** Anything that changes something is a POST. The cookie is not sent on a POST from another site, and a POST whose `Origin` is another site is refused. A request body over 64 KB is refused.
- **Addresses (A5).** `/` (every project), `/usage`, `/p/{project}`, `/p/{project}/inbox`, `/p/{project}/inbox/{id}`, and POSTs to `…/answer`, `/p/{project}/pause` and `/p/{project}/resume`. The JSON API is the same path with `/api` in front, served by the same code. Inbox ids are only unique inside a project, so every address names the project.
- **Where the code lives (A6).** A new package, `internal/web`. Templates are built into the binary. Styles are inline and there is no JavaScript at all.
- **What the status words mean (A7).** The current task is the first running or shipping task in plan order, or else the first parked one. Its phase is the session's own phase while it runs, "paused for usage" when the daemon paused it, the ship step while it ships, and "waiting on you" when parked. Done over total counts finished tasks against the plan's tasks, leaving out superseded ones. The last ship result is the stop marker when the line is stopped, or else the most recently finished task with its pull request and merge commit.
- **The buttons and the commands share code (A8).** `afk pause` and `afk resume` were split so the page's buttons do exactly what the commands do, including the lines in the decision log.
- **The link in a notification (A9).** `public_url/p/{project}/inbox/{id}`, with no token in it. The daemon knows the project's name. `afk inbox add` looks the project up in the machine config and sends no link when it is not listed there.
- **Starting and stopping (A10).** The page starts after the daemon has its lock and before the build loop. If the port cannot be opened the daemon exits with a message. It is shut down whenever the daemon stops, including when it stops for a new version. `afk daemon --once` and `afk daemon status` never start it.
- **Answering un-parks on the next pass.** The page only records the answer. The build loop un-parks the task on its next pass, a few seconds later, as it does for `afk inbox answer`. The page never writes the ledger.
- **Tests, prerequisites, install (A11, A12, A13).** Tests are written with each package and request the pages without opening a port. Nothing had to be in place first. The conductor runs `make install` after the review approves.

### Decided while building

- **A POST from the public address is accepted too.** The plan refused any POST whose `Origin` was not the request's `Host`. Behind a reverse proxy that does not pass `Host` on, that would refuse every button, so the scheme and host of `[web] public_url` are accepted as well. Any other origin is still refused.
- **No link without a token.** A notification carries a link only when both `token` and `public_url` are set; with no token the page is off and the link would open nothing.
- **Secrets are hashed before they are compared**, so the time taken does not give away the token's length. A wrong token in one place does not spoil a right one in another, so an old bookmark cannot lock out a browser that holds the cookie.
- **On an item with options, typed text wins over the selected option.** The first option is preselected, so only the typed text is a deliberate act.
- **A project that cannot be read shows no current task and 0/0**, with the reason, and not counts from half-read data. Its pause state and inbox are still shown.
- **A task finished with no time recorded** (a ledger seeded by `afk ledger init --done`) can still be the last ship result; among equals the later task id is shown.
- **Unknown addresses get a plain 404 sentence**, and nothing a request sends is echoed back in an error.
- **If the page stops answering after start-up**, the daemon logs one line and the build carries on. Restarting the daemon brings the page back.
- **A failed pause or resume from the page** is worded as the commands word it and answers with an error, including the case where the change went through and only the decision log could not be written.
- **`afk inbox add` finds the project's name through its main checkout**, as the daemon does, and reads the machine config only when a notification is really going out. Any problem there is silent.

### Things a later task should know

- **T3.2** can reuse `web.Summarize` for the build view's per-project line; it is a pure function over the plan, the ledger, the daemon's state and the inbox.
- **`inbox.ViaWeb`, `ViaCLI` and `ViaTUI`** are now exported constants.
- **`pauseProject` and `resumeProject`** in `pause_cmd.go` do what the commands do without printing; a TUI key should call them.
- **The total counts owner tasks** like any other. If the owner never marks one done, done never reaches total.

### Checked, and not checked

- **Checked against a built binary** (a scratch build, never `~/bin`): in a throwaway paused repo with a scratch config, no token gave 401, the `?token=` visit gave a redirect and a cookie with no token in the address, the pages loaded with the cookie, an item was answered from the page and recorded as answered from the web, `/api/p/<project>` returned JSON with a bearer header, pause and resume changed the pause flag, the port closed on stop, and the daemon's log held no token. A bind of `0.0.0.0` with no token exited 1. `make check` was run in full, green.
- **Not checked: a real phone, a reverse proxy or Tailscale.** Owner check: set `[web] token` (and `public_url` if you use a proxy) in `config.toml`, restart `afkd`, open the page once with `?token=`, then answer an item and press Pause and Resume from the phone. Expect the task to un-park within a few seconds of the answer.
- **Not checked: a tap on a real ntfy notification** opening the item page.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes: this file and `CONTEXT.md` were edited outside the work plan's tasks (the planner and conductor write both, by design), and the quality check read two files in full and skimmed the rest.

### Ideas left out

- Serve the page under a sub-path. `public_url` may hold a path, but redirects and the cookie assume the page is at the root.
- Mention the web page in `afk daemon --help` and the link in `afk inbox --help`.
- Show the page's address in `afk daemon status`.

## T3.2

T3.2 puts the build in the dashboard: a usage segment in the header, a Build tab that lists the master plan's tasks with their status, and an inbox overlay to read and answer items. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where the Build tab lives (A1, A2).** It is a sixth tab in the tab strip on the right side, placed last so the number keys of the other tabs do not move. It appears only when the project has a master plan or a ledger. It is about the project and not the selected session, so it shows the same thing on every row, and it also shows when there is no session at all. A build with nothing running is exactly when the owner wants the task list.
- **The keys (A3).** `b` goes to the Build tab and `i` opens the inbox. Both are ordinary dashboard keys, so `?` and `:` list them without extra work. They also work while the overview on the right has the keyboard. While the agent pane has the keyboard they are typing, like every other letter.
- **Where the data comes from (A4, A5).** One loader reads the plan, the ledger, the daemon's small files, the inbox and the usage reading, on the dashboard's existing refresh, away from the screen-drawing code. It reads the project's main checkout, which it works out from git. The ledger and the inbox live there, and a dashboard started inside a session's worktree would otherwise show an empty build. Nothing is dialled and nothing uses the network.
- **One set of words (A6, A11).** The governor's sentences ("paused until …", "nothing new is started") and the names of the usage windows moved out of the commands into a small shared package, `internal/usagetext`, which the commands and the dashboard both call. Task statuses are the ledger's own words, as `afk tasks` prints them, with a short plain detail beside each.
- **The dashboard does not import the web page's code (A7).** A new package, `internal/buildview`, holds what the dashboard shows and works out the task rows. The summary line still comes from the web page's `Summarize`, called by the loader, so the two surfaces agree.
- **The tightest usage window (A8).** The window with the highest percentage used, because it is the one that stops the build first. On a tie, the one that resets later.
- **An old reading says so (A9).** `usage.json` outlives the daemon. After 30 minutes, which is the governor's own default for a stale reading, the segment says how old the reading is.
- **What the header gives up first (A10).** As the screen narrows: the switch-projects hint, then the usage text shortens, then the afk badge goes, then the short usage text, then the awaiting line, and the "stale" warning last. The short usage text says whether the build is being held back, which matters more than the badge. With no usage reading the header shows nothing new.
- **Answering (A12, A13).** The dashboard only records the answer, marked as answered from the dashboard. A number picks an option, as `afk inbox answer` does. The build loop un-parks the task on its next pass; the dashboard never writes the ledger. The overlay follows the files while it is open and keeps the cursor and any typed text, so an item answered from the phone changes on screen.
- **Paused for usage (A14).** A session the daemon paused for usage has no host, so it used to read as stopped. It now reads "paused for usage". Its keys are unchanged, so `r` still starts it again.
- **Left out (A15).** Pause and resume keys in the dashboard, and a count of inbox items in the header. The master plan's scope for this task does not list them.
- **Tests, prerequisites, install, shipping (A16 to A20).** Tests are written with each change, with three new saved screens. Nothing had to be in place first. This session does not run `make install`: the project's `afk.toml` installs after the merge, and the one `~/bin/afk` is what the running service uses. The session stops at reviewed commits and the daemon ships them. The OpenCode plugin is not touched.

### Decided while building

- **An answer is resolved once.** The work plan said to turn a typed number into its option and then save it. Saving already does that, and doing it twice could pick the wrong option, so the dashboard hands the typed text straight to the inbox.
- **A narrow Build tab trims row by row.** A row that does not fit loses its detail first, and then its title is cut. The id and the status always show.
- **The paused wording is drawn where the row is drawn.** The list row and the overview say a session is paused for usage; the code for that is in the dashboard's view file, not the list file the plan named.
- **While the inbox overlay is open, no key quits the dashboard.** `ctrl+c` and `q` are not quit keys there, because the owner is typing an answer. `esc` or `ctrl+g` leaves it.
- **An inbox folder that cannot be listed** shows as "1 inbox file could not be read". The wording is close but not exact; a field of its own is a small follow-up.
- **After an answer, the row reads "answered" at once.** The count on the Build tab can lag by one refresh, about two seconds.

### Checked, and not checked

- **Checked against a built binary** (a scratch build, never `~/bin`): in a throwaway repository with a five-task plan, a ledger, two inbox items and a scratch usage reading, driven through tmux at 120 by 40 and 60 by 24. The header showed the pause and 91%, `b` showed the five tasks with their statuses, `?` and `:` listed both keys, `i` listed the items, an empty answer was refused with its message, typing `2` saved the second option as answered from the dashboard, and `b` with no plan gave its notice. The hands-on check repeated the walk-through. `make check` was run in full, green.
- **Not checked: the keys beside a live agent.** No real session ran. Owner check: with an agent pane focused, press `b` and `i` and see that the agent gets the letters and the dashboard does nothing.
- **Not checked: a real daemon's files.** The ledger and inbox were made by hand. A task parked by hand has no item recorded against it, so its row says "answered, starting again soon" even while a question is open; a task the daemon parks names its items.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes: this file and `CONTEXT.md` were edited outside the work plan's tasks (the planner and conductor write both, by design); the Build tab's summary says "unknown" for a running task whose session has no status yet, which is not friendly wording; and the quality check ran the short tests only, so the full `make check` was run afterwards by the conductor.

### Ideas left out

- Pause and resume keys in the dashboard.
- A count of open inbox items in the header.
- Let `afk ledger set parked` name the items a task waits for.
- Make `?` work while the right side has the keyboard, as `:` does.
- A word such as "blocks" beside an inbox row that is holding a task up, in place of the bare `!`.
- A friendlier word than "unknown" for a running task whose session has not reported a phase.

## T4.1

T4.1 gets the VM and nebari ready for afk's first real job. It writes `docs/runbook-vm.md` and then follows it on the VM. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The session follows its own runbook (A1).** It installs nebari's system packages with `sudo`, clones nebari to `~/code/nebari` and runs the steps of `bin/ci`. The acceptance says "on the VM", the master plan makes the VM the sandbox, and a runbook nobody has followed is a guess. This one would have been put to the owner if anyone had been there to ask.
- **Nebari's changes stay on a local branch (A2).** `afk.toml`, two `.gitignore` lines and the CI change are committed on `afk-setup` in `~/code/nebari` and not pushed. A session that was not asked to ship does not push, least of all to a second repository. The owner pushes the branch and merges it.
- **Nebari is not added to the daemon yet (A3).** The `[[project]]` lines are the runbook's last step. Adding them now would start nebari tasks unattended, before the supervised first run of T4.2.
- **The web page is the owner's to turn on (A4).** The runbook says how to set `[web]` and reach the page through a tunnel. The session does not edit the running daemon's config or restart it: the tunnel needs the owner's account, and nothing in the work needs the restart.
- **The owner's part is one inbox item that stops the task (A5).** Backups and a restore drill, Coolify's auto-deploy, the nebari pull request, the capped key and the page over the tunnel cannot be done by a machine. After the work is done the session raises one item listing them and waits; the review and the commit follow the answer. That also keeps T4.2 from starting first.
- **The required-variable pattern (A6).** `^\s*[A-Z][A-Z0-9_]*:\s*\$\{([A-Z][A-Z0-9_]*):\?\}\s*$`. The pattern is tried on every added line of every file. Nebari's documents mention `${VAR:?}` in prose, so the pattern is tied to the shape of a line in the compose file. It allows digits, which the README's old example did not (`S3_ENDPOINT`).
- **A deploy of this commit (A7).** Not solved. The health address is `https://nebari.haener.cloud/up`, and nothing in nebari shows which commit is running, so the check means "healthy" and no more. The runbook says so.
- **The capped key stays out of the agent's environment (A8).** It goes in nebari's own local settings on the VM. A key in the daemon's unit or a shell profile would reach the agent CLI and could move it from the subscription to paid calls.
- **Seeding the ledger (A9).** Everything through T4.2b was marked done, and T4.2 superseded by T4.2a and T4.2b. T4.2b's code is merged; only the owner's phone check is open. T4.3 was merged to nebari's main while this task ran, so the owner had it marked done too (`afk ledger set T4.3 done`), and the runbook now seeds through T4.3.
- **Nebari's T9.1 is left as it is (A10).** Its dependency line names milestones, not tasks, so afk reads it as having none. How to read "as available" is the owner's call, so it is raised as a notice.
- **Tests and prerequisites (A11, A12).** No new automated tests: the change is documents, and the runbook is checked by following it. Nothing had to be in place first.

### Decided while building

- **The capped key lives outside the project.** It goes in `~/.config/nebari/live.env` (mode 600) and is loaded for one command in a subshell. Nebari reads only the plain `ANTHROPIC_API_KEY` variable, a file inside the project would be missing from each session's worktree, and a subshell keeps the key out of the agent's and the daemon's environment.
- **PostgreSQL 17 comes from the PostgreSQL project's repository.** Ubuntu 26.04 carries only 18, and production and CI run 17. Chrome is Google's `.deb`, because `chromium-browser` is a snap wrapper there. The runbook names `libvips42t64`, because `libvips` has no install candidate.
- **The runbook pauses the project before adding it to the daemon.** `afk pause ~/code/nebari`, then the `[[project]]` lines, a restart, then `afk resume nebari`. Otherwise the restart would start the next task at once.
- **`bin/ci` was not run as one command.** Its steps add up to about 38 minutes, over a session's four-minute limit, so each step was run on its own and the system specs in groups of about 65 examples. The per-step record is the evidence.
- **The failing spec is recorded, not fixed.** `spec/system/task_swipe_spec.rb:266` fails for a reason inside nebari, and it fails the same way in nebari's own CI. Nothing in nebari was changed for it.
- **The CI install step was left alone.** On 26.04 `libvips` resolves to `libvips42t64` and `libheif-plugin-libde265` exists, so only a comment was added to the workflow change.
- **Sections 6(c) and 9 of the runbook were corrected** once the red spec was known, because they told the owner to expect all four CI jobs to pass.

### Checked, and not checked

- **Checked on the VM, with evidence in `.afk/t4-1/evidence/`:**
  - Packages: PostgreSQL 17 (cluster online), `libvips42t64`, Chrome 155, `pg_lsclusters`, in `w2-t1-prereqs-and-ci.txt`. Chrome starting without `CI` set is in `w2-t1-chrome-without-ci.txt`.
  - Each `bin/ci` step: setup, rubocop, bundler-audit, importmap audit, brakeman, tailwind and the specs, in `w2-t1-prereqs-and-ci.txt`. Every step exits 0 except the specs: 7209 examples, 1 failure (`spec/system/task_swipe_spec.rb:266`), 2 pending.
  - The branch and the pattern: `afk-setup` in `~/code/nebari`, one commit, no upstream, and the pattern matching 15 compose lines and no prose line, in `w3-t1-nebari-ready.txt`. The bad-pattern case is in `w1-t1-bad-pattern.txt`.
  - The ledger and `afk next`: 23 done and T4.2 superseded, `afk next` printing `T4.3  Weather alerts`, in `w3-t1-nebari-ready.txt`; after T4.3 was marked done, `afk next` printing `T4.4  Dashboard & calendar`, in `w3-t1-nebari-ready-after-t4-3.txt`; a second `ledger init` exiting 1 with the file unchanged, in `w3-t1-ledger-init-twice.txt`.
  - `afk usage` printing a reading and `Governor: run.`, in `w3-t1-usage.txt`.
  - The config example loading, in `w1-t1-toml-loads.txt`, and the README pattern and link, in `w1-t2-readme.txt`.
- **Not checked, the owner's:**
  - The `afk-setup` pull request on a hosted `ubuntu-26.04` runner.
  - Coolify's auto-deploy.
  - Backups and the restore drill.
  - The web page through a tunnel and from a phone.
  - The capped key (`ai:smoke` with a real key).
- **Not green: nebari's own checks.** The one failing spec above also fails in nebari's CI on main (run 38169606371), where two more system examples failed that passed on the VM. afk merges only on green CI, so the `afk-setup` pull request and every nebari task stop at the pull request until that is dealt with. This is item six of the owner's inbox item.

### Things a later task should know

- **Nebari is at `~/code/nebari`.** When the work-plan tasks finished it had the unpushed branch `afk-setup` and the machine's `config.toml` was untouched. What changed after that is under "After the owner's steps" below.
- **`afk-setup` must be merged to nebari's main before T4.2.** afk reads `afk.toml` from the main checkout and cuts branches from local `main`, so without it the daemon has nothing to read.
- **Seeding commands**: `afk ledger init --project ~/code/nebari --done-through T4.3` (24 tasks), then mark T4.2 superseded by T4.2a and T4.2b (runbook section 5). A second `ledger init` is refused, so the ledger is seeded once.
- **A hosted-runner pull request will probably be red** until `task_swipe_spec.rb:266` is fixed or set aside in nebari. The touch scroll gesture does nothing in Chrome 155; the same gesture as a mouse scrolls.
- **`afk tasks` prints notes for T9.1, T9.3, T9.4, T9.5 and T9.6**, whose dependency lines name no task ids. They are treated as having no dependencies, so they can be picked early.
- **Health only.** The `/up` check cannot tell this commit from the old container; a revision endpoint in nebari would close that.
- **Timings on the VM**: setup 73 s, the specs outside `spec/system` about 5 minutes, `spec/system` about 31 minutes, the whole `bin/ci` about 38 minutes. A task's QA step should budget for that.
- **Apt reported `afkd.service` running an outdated binary.** It was not restarted.

### After the owner's steps

The owner answered the blocking inbox item with "Ok everything is done" (2026-10-12, from the web page). That answer is the evidence for the owner's half of the acceptance: backups and the restore drill, Coolify's auto-deploy, the capped key, the phone checks. The session could see only the following.

- **`afk-setup` is pushed and is pull request 14 on nebari, still open** when this was written. Nebari's `main` has no `afk.toml` until it merges, and the local checkout is on `afk-setup`. It was rebased onto nebari's `5530f51` before the push.
- **Nebari is in the daemon's config and is paused.** The owner added it and set `[web]` (`bind`, `token`, `public_url`). The session ran `afk pause ~/code/nebari` before the daemon came back up, so no task starts until `afk resume nebari`. Do not resume before pull request 14 is merged and the checkout is on `main`.
- **T4.3 is marked done** in nebari's ledger at the owner's request; it was merged to nebari's main while this task ran. `afk next` prints `T4.4  Dashboard & calendar`.
- **The swipe spec still fails on the VM** after nebari's `5530f51`, which added a wait: the page never scrolls, so the wait ends with `not scrolled yet`. It fails the same way on the hosted runner: nebari's CI run 38327506098 for `5530f51` ended with 7562 examples and that one failure. The other two system examples that had failed there now pass.
- **Two faults found while the owner worked, both fixed at the owner's report:**
  - *The page answered 502.* `config.toml` had nebari's `path` line under the first `[[project]]` header, so the file had two `path` keys and the daemon would not start. Nebari got its own `[[project]]` header. The runbook's troubleshooting table has a row for it.
  - *Every form on the page answered 403.* The page sent `Referrer-Policy: no-referrer`, and under that policy a browser sends `Origin: null` on every POST, the page's own forms included; the cross-site check refuses `null`. The header is now `same-origin` (`internal/web/auth.go`): the page's own forms post with their real origin, and no referrer leaves the site, so the token in an address is still not passed on. T3.1's checks used `curl`, which sends no `Origin`, so they could not see it. The owner's answer above went through the page after the fix.
- **`make install` was run from this branch and `afkd` restarted twice**, so the owner could use the page. The plan had said the session would touch neither the config nor the daemon; the owner asked for both.
- **`gs` on the VM was Ghostscript, and `make check` failed in `internal/spice`.** The review found it on 2026-10-12: `TestGitSpiceTrackContract` ran `/usr/bin/gs --no-prompt repo init --trunk main` and got `Unknown switch --no-prompt`. The runbook's libvips step had brought it in: `libvips42t64` depends on ImageMagick's library, which recommends the `ghostscript` package, whose program is `/usr/bin/gs`; afk looks for `git-spice` first and `gs` second, and the VM had no `git-spice`. Fixed on the machine, not in Go (the plan allows no Go change for this, and the test is right to fail when `gs` is not git-spice): the official git-spice 0.31.2 release, checksum checked, is installed as `~/bin/git-spice`. Both the shell's PATH and the daemon's drop-in PATH list `~/bin` before `/usr/bin`, so afk and the daemon find it first whatever `gs` is; `go test -count=1 ./internal/spice/` and the whole `make check` pass (`.afk/t4-1/evidence/fix1-git-spice-install.txt`, `fix1-spice-test.txt`, `fix1-make-check.txt`). Before that fix, and not by this session, the `ghostscript` package had been removed from the VM and git-spice added through mise under the name `gs`; neither is needed with `~/bin/git-spice` in place, and neither was undone. The runbook has the install in section 2 (step 6), a Known limits entry and a troubleshooting row, and README's web page section now says why the page sends `Referrer-Policy: same-origin`.

### The review

The first pass was rejected on two points, and one fix cycle cleared both.

- **The 403 fix was outside the work plan**, which had said documents only. Because the owner reported the fault in this session, the fix became its own task in the work plan (W5.T1), and stays in this change so a later install from main does not bring the fault back. An inbox notice says so.
- **`make check` failed on the VM** because `gs` was Ghostscript. git-spice 0.31.2 is now at `~/bin/git-spice`, which afk looks for before `gs`, and the runbook has the step, a known limit and a troubleshooting row. `make check` then passed in full.

The second pass approved with minor notes only: `README.md` and `CONTEXT.md` were edited outside the tasks' file lists, and some of the plan's checks can no longer be re-run as written because nebari moved on (`afk next` prints T4.4, `afk-setup` has an upstream).

Still open when this was committed, all on the owner's side: nebari pull request 14 is not merged, nebari's main is red on `task_swipe_spec.rb:266`, and nebari is paused in the daemon.

## T4.2

T4.2 is afk's first real job: one nebari task run from start to a healthy deploy with the owner watching, a write-up of what needed a human, and then the daemon left to carry on by itself. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The owner starts the run, not the session (A1).** When the session began, nebari's pull request 14 was still open, nebari's `main` was red in CI on one system spec, and the project was paused in the daemon. The session merges nothing in nebari, changes nothing there and does not release the pause: a merge to nebari deploys to production, and the task says the owner is watching. This one would have been put to the owner if anyone had been there to ask.
- **The session waits before doing any work (A2).** It raises one inbox item that stops the task, listing what the owner does, and every step of the work comes after the answer. Nothing useful can be written about a run that has not happened.
- **The supervised task is whichever one the daemon starts (A3).** Today that is T4.4. The work does not name it, because the owner may reorder or reseed before the run.
- **Runbook faults are fixed here; code faults are filed (A4).** If the run shows a runbook step was wrong or missing, the runbook is corrected in this change. If it shows afk's code needs to change, that goes to the inbox as a notice and into the write-up. Such a change cannot be sized before the run, and each one deserves its own reviewed change.
- **The run is read from its files (A5).** The session was not attached while the task ran. It reads nebari's ledger, decision log, inbox and token ledger, GitHub's record of the pull request, and the owner's answer for what was done by hand.
- **"Leave the daemon running" is checked, not done (A6).** At the end the session looks at whether `afkd` is active and nebari is released, and writes down what it sees. It never resumes, pauses or restarts anything.
- **Tests and prerequisites (A7, A8).** No new automated tests: the change is documents. The prerequisites are all the owner's and are the inbox item of A2.
- **The session ran `afk resume nebari` once, after the owner's go-ahead (A9).** The owner answered the inbox item with "Everything is ready to go, let's do it." before any task had run, with pull request 14 merged and nebari still paused. The session read that as the go-ahead and typed the one command, which replaces the "never resumes" part of A1 and A6 for that command only. It changed nothing else in nebari or the daemon, said so in inbox item `jjmkb5`, and `afk pause nebari` undoes it. This one would also have been put to the owner if anyone had been there to ask.

### The supervised run

The task was **T4.4** (calendar feed, Today and the garden journal), run by the daemon in session `t4-4` on branch `t4-4`. Times are UTC, from nebari's ledger and decision log.

- **Start.** The owner had paused nebari at 00:52:34. The release (`afk resume nebari`) is logged at 01:27:58 and T4.4 launched a second later, at 01:27:59. The session ran that command itself after the owner's go-ahead (assumption A9), so the log's `chosen_by: owner` names the owner's authority, not who typed it.
- **Build.** The daemon recorded 14 token ledger lines and handed the task to shipping at 04:43:29, 3 hours 15 minutes after the launch.
- **Ship states.** The decision log shows them in order: `committed` (04:43:34), `pushed` (04:43:39), `pr_open` (04:43:44), `ci_green` (05:05:29), `merged` (05:05:52), `deploy_healthy` (05:07:14), then `shipped`, which cleaned up the session and marked T4.4 done at 05:07:19.
- **Pull request.** Number 15, "feat(today): Add Today, garden journal, calendar and feed", created 04:43:46, merged 05:05:56. All eight checks passed; the slowest test shard took 12 minutes 48 seconds.
- **Merge commit.** `fabbb96f9ea64f2fc6736259ef92d0437d5f9cd8` (`fabbb96`), the head of nebari's `main`.
- **Deploy.** The daemon's own check logged `deploy_healthy`, and `https://nebari.haener.cloud/up` answered 200 when this session looked.
- **How long.** 3 hours 39 minutes from launch to done: 3 hours 15 minutes of agent work, 22 minutes from pull request to green CI, and under 2 minutes from green CI to a healthy deploy. One attempt, no retries.
- **After.** Five seconds after T4.4 was done the daemon launched T5.1 in session `t5-1` without anyone asking. That is the unattended part working.

### What needed a human

The owner's answer to the blocking item was "Everything is ready to go, let's do it." It was given before any task had run, so it does not list anything done by hand during or after the run. What follows comes from the files and is only as complete as they are.

- **Before the run: merge nebari's pull request 14, fix the red `task_swipe_spec.rb:266`, and pause and release the project.** afk merges only on green CI and a merge to nebari deploys to production, so the session was not allowed to do these. Cause: by design, the owner's gate on the first production deploy, not a fault. Nebari's `main` now carries the spec fixes and the CI change that were in the way (`7b418e8`, `ded7091`, `35b81f1`), but the files do not say who made them.
- **Starting the run: `afk resume nebari`.** The daemon does not start a paused project, and the pause was the owner's. The session typed the command after the owner's go-ahead and left a non-blocking inbox item (`jjmkb5`) saying so. Cause: by design, the pause is the owner's switch.
- **During the run: nothing visible.** The decision log has no step the owner took. T4.4 left no blocking inbox item in nebari's inbox and none of its ship states was repeated or held, so the daemon never waited for a person. Whether the owner typed into the agent's terminal is not in any file: the owner's answer says nothing about it, so **this session cannot say** whether terminal input was needed. The files show no sign that it was.
- **After the run: one unanswered, non-blocking question.** The session left `kxcvdt` in nebari's inbox, about how the calendar feed is exposed (opt-in, token in the query string). It stopped nothing and the owner has not answered. Cause: it is a product choice that only the owner can make.
- **Not a human step: the daemon restarted itself at 01:21:09** because afk's program file had changed on disk. The log shows it did so by policy, before the run began.
- **A small fault in afk's reporting.** The plan assumed `afk inbox --all` exists to show answered items in another project. It does not (`flag provided but not defined: -all`), so this account was assembled from `afk inbox` and the files in `.afk/inbox/`. This is a gap in the tool, not something the owner had to do.

### Tokens

`afk tokens --project ~/code/nebari T4.4` printed a breakdown (exit 0), so the daemon did record the session. The saved copy is `.afk/t4-2/evidence/tokens.txt`.

| Phase | Calls | Weighted | Share |
|---|---|---|---|
| plan | 111 | 4,264,852 | 24.4% |
| execute | 522 | 11,758,802 | 67.2% |
| review | 46 | 1,367,794 | 7.8% |
| ship | 6 | 115,116 | 0.7% |
| **Total** | **685** | **17,506,564** | 100% |

By role, the largest were the execute `task` role (three model rows: 6,319,909 + 3,048,638 + 402,108), the plan `planner` (1,888,636), the plan `crucible` (1,818,093), the execute `verifier` (1,353,624) and the review `qa` role (1,039,707).

The weighted total is **17,506,564**, against the baseline of about 16M weighted per task in `docs/PLAN.md` §4.7. That is about 1.5M, or 9%, over. One task is one data point, so this is not evidence that the baseline is wrong, only that a large task such as T4.4 (Today, the garden journal, a calendar and a feed) can run past it. Execute is two thirds of the cost.

For comparison, `afk tokens --project ~/code/nebari T0.1`, a task seeded by hand, prints "has nothing for T0.1" and exits 1. No figure is given for any task without ledger lines.

### Fixed or filed

One bullet for each bullet of "What needed a human". No cause was a runbook fault, so `docs/runbook-vm.md` is unchanged.

- **Merge pull request 14, fix the red spec, pause and release the project.** Kind: only the owner can do it. It is the owner's gate on the first production deploy, so there is nothing to fix and nothing to file. The owner's.
- **`afk resume nebari`.** Kind: only the owner can do it. The pause is the owner's switch, and the runbook already says to run it (section 8, line 778). The owner's. The session's own use of it is recorded in inbox item `jjmkb5`.
- **During the run: nothing visible.** Nothing to fix or file. Whether terminal input was needed is not in any file, so no cause can be named.
- **The unanswered calendar-feed question.** Kind: only the owner can do it, because it is a product choice. It is already in nebari's inbox as `kxcvdt`. The owner's.
- **The daemon's self-restart at 01:21:09.** Not a human step, so nothing to fix or file.
- **`afk inbox --all` does not exist.** Kind: afk's code would have to change. Filed as inbox notice `mmt6u7` in this project's inbox.

### Checked, and not checked

Checked, in files and on the live system when this was written:

- Nebari's ledger entry for T4.4: `done`, pull request 15, one attempt, `merged_sha` `fabbb96f9ea64f2fc6736259ef92d0437d5f9cd8`, started 01:27:59 and finished 05:07:19.
- The whole decision log (14 lines), for every ship state and its time.
- GitHub's record of pull request 15 (state `MERGED`, merge commit the same `fabbb96...`) and its eight passing checks.
- `git log` on nebari's `main`, with `fabbb96` at the head.
- `https://nebari.haener.cloud/up` answering 200 now.
- The token breakdown, as printed by `afk tokens`.
- `afk daemon status`: the daemon is running and nebari is on T5.1, in phase planning. This is a status of the moment, not a record of the run.

Not checked, resting on the owner's word or on nothing:

- Whether the owner typed into the agent's terminal or did anything by hand during the run. The owner's answer was given before the run and says nothing about it.
- Who made the spec and CI fixes on nebari's `main`, and whether the other owner steps in runbook section 6 (backups and restore drill, Coolify auto-deploy, the phone check) were done. The run's files do not cover them.
- That the deployed feature works for a person. The deploy check is the `/up` health answer, which says the app is up, not that the calendar feed or the Today view behave.
- The session was not attached during the run, so nothing is known about the run that is not in the files above.

### Left running

Read from `.afk/t4-2/evidence/w2-t1-daemon.txt` at the end of this task.

- `systemctl --user is-active afkd` prints `active`: the daemon is running.
- Nebari is released. `afk daemon status` shows no `Paused:` or `Stopped:` line for it.
- Nebari's build is running a task: T5.1, in session `t5-1`, in phase planning (22:07 on 12 October, local time). Nebari has 24 tasks done, 1 running and 1 superseded. Nothing is waiting on the owner for it; the only open nebari inbox item is the non-blocking `kxcvdt`.
- The daemon also shows this project (afk) running T4.2 in session `t4-2`, which is this work.

## T5.1

The plan format and `afk plan lint`. Decisions the master plan left open, made while planning the task (2026-10-13).

- **One scan for the daemon and the checker.** The master-plan parser stops at the first problem, and the checker has to report all of them. Rather than give the checker its own copy of the parser's rules, the parser's line scan is exposed as `masterplan.Scan`, which collects every problem, and `Parse` is rebuilt on it. What the parser accepts and the errors it returns do not change. Why: a second scanner could call a plan fine that the daemon then refuses.
- **What "a heading that looks like a task" means.** Any heading, of any level, outside a code fence, that starts with `T` or `t` and a digit but is not a valid `#### T<m>.<n>[letter] — Title` heading. The parser already refuses the four-hash ones. The others, such as `### T1.2 — x`, are an error too, because the daemon would silently never run them.
- **Open questions.** The table is the first one with a "Needed by" column under the "Open questions" section. A row is open unless its question cell contains `**Answered`, which is the rule T5.6 states. Only open rows are checked. Any task id in the "Needed by" cell counts, so "after T2.4" names T2.4. The summary counts open rows.
- **Missing Goal, Scope or Acceptance** is one warning per task that names every missing field, not one per field. Plans written before the format lack a Goal on most tasks.
- **Required sections** are Context, Decisions, Milestones, Deferred, Open questions and Definition of done, matched on the section title with any leading number dropped, plus a bold "How to use" paragraph before the first section. That is how both existing plans are written.
- **A plan with no task** is an error (`no-tasks`). The master plan's list leaves it out, but the daemon refuses such a plan.
- **Cycles.** The checker reports the first cycle it finds, as the parser does. Fix it and the next run shows the next one.
- **`FILE` and `--project` together are refused** (exit 2). With a file, that file is checked. With neither, the checker finds the plan the way `afk tasks` does.
- **Output.** One line per finding and a totals line by default; `--summary` prints the six counts instead; `--json` prints one object with `file`, `findings` and `summary`. No afk command had `--json` before, so this is the shape later commands should copy.
- **The daemon's item.** The checker's errors are added to the text of the item the daemon already raises for a plan it cannot read, at the place where the daemon loads the plan. `afk tasks` and `afk next` still print one problem. At most 20 errors are listed, because the text is also sent as a notification, and the text is the same on every pass so the daemon does not raise the item twice.
- **Plugin version 5.3.0**, because the format document is a new plugin file. The OpenCode port gets a line in its lag note, not a copy.
- **The document and the checker are held together by tests**: the document's worked example must produce no findings, and every rule id must appear in the document.

### Warnings on this plan (docs/PLAN.md)

Checked on 2026-10-13 with `afk plan lint --summary docs/PLAN.md`: Milestones 6, Tasks 29, Owner tasks 1, Open questions 9, Errors 0, Warnings 14. The checker reports zero errors. The 14 warnings, grouped by rule:

`missing-task-field` (13): Most tasks were written before the format existed and have no Goal line (T0.0 has no Scope line). The plan is the owner's record and is left as it was written.

```
docs/PLAN.md:790: warning: T0.0: T0.0 has no Scope line. Add "- **Scope:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1018: warning: T1.1: T1.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1031: warning: T1.2: T1.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1043: warning: T1.3: T1.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1056: warning: T1.4: T1.4 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1093: warning: T2.1: T2.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1114: warning: T2.2: T2.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1180: warning: T2.3: T2.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1191: warning: T2.4: T2.4 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1208: warning: T3.1: T3.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1220: warning: T3.2: T3.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1235: warning: T4.1: T4.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
docs/PLAN.md:1249: warning: T4.2: T4.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
```

`open-question-no-task` (1): The "before release" question is needed by the release as a whole, not by one task, so it names no task id.

```
docs/PLAN.md:1589: warning: This open question's "Needed by" names no task, so no task waits for the answer. Name the tasks that need it, or mark the question answered. [open-question-no-task]
```

### Warnings on the nebari fixture

Checked on 2026-10-13 with `afk plan lint --summary internal/masterplan/testdata/nebari-plan.md`: Milestones 10, Tasks 40, Owner tasks 0, Open questions 7, Errors 0, Warnings 39. The checker reports zero errors. The 39 warnings, grouped by rule:

`missing-task-field` (33): The fixture is a frozen copy of nebari's real plan, written before the format existed; its tasks have no Goal line (some lack Scope, one lacks all three), and the fixture is not edited.

```
internal/masterplan/testdata/nebari-plan.md:552: warning: T0.3: T0.3 has no Scope line. Add "- **Scope:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:667: warning: T1.1: T1.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:682: warning: T1.2: T1.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:693: warning: T1.3: T1.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:713: warning: T1.4: T1.4 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:739: warning: T2.1: T2.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:764: warning: T2.2: T2.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:805: warning: T2.3: T2.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:841: warning: T2.4: T2.4 has no Scope line. Add "- **Scope:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:922: warning: T2.5: T2.5 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:946: warning: T2.6: T2.6 has no Scope line. Add "- **Scope:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1015: warning: T3.1: T3.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1045: warning: T3.2: T3.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1078: warning: T3.3: T3.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1114: warning: T3.3a: T3.3a has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1160: warning: T3.4: T3.4 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1280: warning: T4.1: T4.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1316: warning: T4.2: T4.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1349: warning: T4.2a: T4.2a has no Goal, Scope or Acceptance line. Add "- **Goal:**", "- **Scope:**" and "- **Acceptance:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1378: warning: T4.2b: T4.2b has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1435: warning: T4.3: T4.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1450: warning: T4.4: T4.4 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1482: warning: T5.1: T5.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1503: warning: T5.2: T5.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1520: warning: T5.3: T5.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1533: warning: T6.1: T6.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1548: warning: T7.1: T7.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1559: warning: T7.2: T7.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1569: warning: T7.3: T7.3 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1583: warning: T8.1: T8.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1592: warning: T8.2: T8.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1605: warning: T9.1: T9.1 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
internal/masterplan/testdata/nebari-plan.md:1614: warning: T9.2: T9.2 has no Goal line. Add "- **Goal:**" under its heading, so the agent that runs the task knows what to build and when it is done. [missing-task-field]
```

`depends-no-task-id` (5): These Depends on lines say things like "nothing new" or "M3–M8 as available"; the daemon reads them as no dependencies, which is what they mean, and the fixture is not edited.

```
internal/masterplan/testdata/nebari-plan.md:1605: warning: T9.1: T9.1's Depends on line says "M3–M8 as available", which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none. [depends-no-task-id]
internal/masterplan/testdata/nebari-plan.md:1624: warning: T9.3: T9.3's Depends on line says "nothing new; it can run at any point (added 2026-09-30 at the owner's request)", which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none. [depends-no-task-id]
internal/masterplan/testdata/nebari-plan.md:1643: warning: T9.4: T9.4's Depends on line says "nothing new; it can run at any point (added 2026-09-30 at the owner's request)", which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none. [depends-no-task-id]
internal/masterplan/testdata/nebari-plan.md:1667: warning: T9.5: T9.5's Depends on line says "nothing new; it can run at any point (added 2026-10-06 at the owner's request)", which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none. [depends-no-task-id]
internal/masterplan/testdata/nebari-plan.md:1690: warning: T9.6: T9.6's Depends on line says "nothing new; it can run at any point (added 2026-10-06 at the owner's request)", which names no task id, so afk reads it as no dependencies. List the task ids, or write — for none. [depends-no-task-id]
```

`open-question-no-task` (1): One open question names no task in its Needed by cell; it is part of the frozen fixture.

```
internal/masterplan/testdata/nebari-plan.md:1736: warning: This open question's "Needed by" names no task, so no task waits for the answer. Name the tasks that need it, or mark the question answered. [open-question-no-task]
```

### Things a later task should know

- `masterplan.Scan` exists and returns the whole-file outline (tasks, headings, fence map and every problem); `Parse` is built on it and returns the first problem as before.
- The rule ids are a contract with the format document (`plugins/afk/skills/master-plan/plan-format.md`); two tests hold them together (the worked example must produce no findings, and every rule id must appear in the document).
- `--json` prints one object: `file`, `findings` (a list of `level`, `line`, `task`, `rule`, `message`; `task` is `""` and `line` is `0` when unset; the list is `[]` for a clean plan) and `summary` (the six counts).
- An open question row is open unless its question cell contains `**Answered`. An answer written on its own row below the question leaves the question row open, which is true of several rows in `docs/PLAN.md`; T5.6 must allow for it.
- The daemon's bad-plan item lists at most 20 errors, then a line saying how many more.

## T5.2

The owner profile: `profile.toml`, `internal/profile` and `afk profile`. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **`set` edits the file as text (A1).** It replaces one `[[entry]]` block, or adds one at the end, and leaves every other line alone. The TOML library can write a file, but only by dropping every comment and every key it does not know, and the template `afk profile init` writes is mostly comments. After the edit afk reads the result back and checks that it is the old profile with that one entry changed. If it is not, nothing is written and the message says to change the entry by hand.
- **Where the file is (A2).** The same folder as `config.toml`: `$XDG_CONFIG_HOME/afk` when that is an absolute path, otherwise `~/.config/afk`. The profile package has its own copy of that rule so it depends on nothing else in afk, and a test compares the two folders so they cannot drift apart.
- **Topics and kinds (A3).** Two topics are the same when they differ only in upper and lower case or in spacing; the file keeps them as the owner wrote them. A kind is any word of lower-case letters, digits and hyphens. There is no fixed list of kinds, because the master plan names `web-app`, `cli` and `library` only as examples.
- **Which entry applies (A4).** For a kind of project, the entries for that kind and the entries with no kind are considered, and one is chosen per topic. An entry for the kind beats a general one, even when the general one is a rule and the specific one a default: the owner wrote the specific one on purpose. Between two entries of the same scope a rule beats a default, and between two equals the later one in the file wins. Two entries with the same topic and kind are reported by `afk profile check` as an error.
- **`afk profile` with no `--kind` (A5)** prints every entry, with its kind. With `--kind` it prints only what applies to that kind, and says so when nothing in the profile is specific to it.
- **Strength (A6).** A new entry set with neither `--rule` nor `--default` is a default. Setting an existing entry keeps its strength and its reason unless the command gives new ones. An entry in the file with no `strength` line reads as a default; any word other than `rule` or `default` is an error.
- **What looks like a key or token (A7).** The well-known prefixes of access keys and tokens, the first line of a private key, a signed web token, `Bearer` followed by a long word, an address with a password in it, `password = …` and the like, 32 or more hex digits, and any word of 32 or more characters that mixes upper case, lower case and digits. It is a net for mistakes, not a guarantee. There is no way to switch it off: if an ordinary value trips it, reword the value. The message never repeats what was typed. The check covers the topic, the value, the reason, the kind and the rule-file paths.
- **A profile with errors is not printed (A8).** `afk profile` lists the problems and exits 1, so a planning session never plans from a broken profile or reads a secret out of one.
- **Rule files (A9)** are edited by hand; no command writes them. Each path must be absolute or start with `~/`. `afk profile check` warns when one does not exist. This task never reads what is in them; that is the planning session's job (T5.3).
- **Writing (A10).** A new profile is readable only by its owner (mode 0600) and an existing one keeps its mode. The write goes to a temporary file in the same folder and is renamed into place. A profile that is a symbolic link, for example into a dotfiles repository, stays a link: the file it points at is written.
- **The template (A11)** has every example commented out, so a new template is an empty, valid profile and nothing in it is mistaken for the owner's choice. `afk profile set` with no file writes a short header and the entry, not the whole template.
- **Exit codes (A12).** 0 when the command did what was asked, which includes "there is no profile yet"; 1 when the file is wrong, a value looks like a secret, `init` found a file already there, or the write failed; 2 when the command was written wrongly. Warnings alone exit 0.
- **No `--json` (A13).** The master plan does not ask for it, and the planning session can read the plain lines. The text form is kept stable for that reason.
- **Tests (A14)** are written with the code, as table tests. **Nothing had to be in place first (A15).**

### As built

- **Comments inside the entry being changed go with it.** `afk profile set` rewrites one entry from its `[[entry]]` line to its last key, so a comment between those lines is dropped. Every other line of the file, comments included, is kept. The README says so.
- **The secret check runs twice.** The command checks the topic, the value and the reason first, so its message can name which one it was. The package's edit function checks again, so no other caller can write a secret.
- **An entry with a key afk does not know cannot be changed by `set`.** The edit would lose that key, so afk refuses and says to change the entry by hand.
- **A line break is refused in every field**, not only the value.
- **`afk profile check` prints each problem once**, as `<file>: error|warning: <message> [<rule>]`. The message already says which entry, and leaves the topic out when the topic itself looks like a secret.
- **Known wart, not fixed here.** The three-line header `set` writes into a profile it creates mentions `afk profile init`, which refuses once that file exists. The wording should point at the README's list of topics. It is in the inbox as a notice.

### Scope ideas

Not built, recorded for later:

- `afk profile --json` and `afk profile check --json`, in the shape `afk plan lint --json` set, so the planning session and a later web page can read the profile without parsing text.
- A command to add or remove a `rule_files` path, so the profile never has to be opened in an editor.

## T5.3

The planning session: `afk plan`, `internal/plansession` and the `/afk:master-plan` skill. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **The session's name (A1).** The host that runs the planning agent is a session named `master-plan`, with its files in `.afk/master-plan/`. The planning state is in `.afk/plan/`, a different folder. Why: cleaning a one-off session removes its whole folder, and the planning state must survive that. `plan` and `master-plan` can no longer be used as names for ordinary sessions.
- **Starting the skill (A2).** The agent's command line for a planning session comes from a new `PlanArgv` on the provider interface. Why: nothing outside `internal/provider` may name a provider or hold its prompt text.
- **Relaunch (A3).** `afk plan` writes an empty file, `.afk/master-plan/planning`. Every start of that session's host reads it and runs the planning skill. Why: the dashboard restarts a stopped host knowing only the session's name, and would otherwise start a bare agent.
- **The phase (A4).** `state.json` records the phase the session says it reached. The phase afk reports is the earliest one whose file is missing or incomplete, and never later than the recorded one. Why: `model` and `review` have no file of their own, so something has to record them, and a file that is deleted or damaged has to pull the session back to the phase that writes it.
- **Who writes `state.json` (A5).** Only code. `afk plan` creates it and a new command, `afk plan advance`, moves it on by one phase when that phase's file is complete, and says what is missing when it is not. The skill never edits the file. Why: the master plan says gates are checks over files and that code counts and checks.
- **A project that already has a plan (A6).** With no planning session open, `afk plan` stops and says that changing an existing plan is not built yet. A session that is under way is always resumed, including after its draft has been written. Changing an existing plan is T5.4.
- **The terminal (A7).** `afk plan` attaches to the session directly, as `afk attach` does. With no terminal it starts nothing and exits 1, because the session asks the owner questions.
- **A folder that is not a git repository (A8)** gets `git init` and nothing else. The draft phase writes the `.gitignore` (`.afk/` and `afk.local.toml`).
- **Other agents (A9, A10).** Only the default agent has the planning skill. With another one, `afk plan` says planning sessions are not available with that agent yet. The OpenCode port gets the rename (`/afk-work-plan`) and a line in its lag note, not the new skill.
- **The design phase (A11)** is skipped: the session goes from `check` to `review` until T5.5 builds it.
- **The skill's files (A12).** One entry file, four prompt files and the format document from T5.1. Its only sub-agents are fact look-ups and one critic.
- **The project's agent instructions (A13)** are written by the draft phase as `AGENTS.md`, with a one-line `CLAUDE.md` that points at it, so one text serves every agent.
- **Tests (A14, A16).** The plugin tests are Go tests over the plugin files built into the binary, beside the shell size check that already runs. All tests are written with the code, as table tests.
- **Plugin version 5.4.0 (A15)**, because a skill was renamed and one was added.
- **Nothing had to be in place first (A17).**

### As built

- **The two documents the planner wrote stay in the change.** The first review pass objected that this section and the new glossary lines in `CONTEXT.md` were in no task's file list. They were kept, and the work plan now says so, because every task must update this document and the glossary when it adds a decision or a term.
- **`afk plan advance` can leave `rounds` after round one**, as soon as every question asked so far has an answer. Whether another round is needed is the skill's call, so the skill asks the next round before it runs the command.
- **A stale `host.json`** (the machine restarted, or the process id was reused) makes `afk plan` try to attach to a host that is gone. It exits 12 and says the session is not running; `afk clean master-plan` clears it and the planning state in `.afk/plan/` is kept.

### For the owner to check

- A real planning session was not run here: there is no terminal or agent in a task session. Run `afk plan` in an empty folder in a terminal, leave with ctrl-\, run `afk plan` again and see that it comes back to the same session. T5.8 does this in full.

### Scope ideas

Not built, recorded for later:

- `afk plan` could add `.afk/` to `.git/info/exclude` right after `git init`, so the planning state cannot be committed before the draft phase writes the `.gitignore`.
- `afk new` and the dashboard's form could check the reserved names themselves, so the refusal of `plan` and `master-plan` shows before anything else is checked.

## T5.4

Extend, amend and reconcile: `afk plan patch`, `internal/planpatch` and the three ways to change a plan that exists. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **The pull-request setting is `[planning] via_pr` (A1).** The master plan calls it `[plan] via_pr`. `afk.toml` already has `plan = "docs/PLAN.md"`, and one TOML key cannot be both a text value and a table, so every existing `afk.toml` would have stopped loading. A `[plan]` table now gets a message that names the right key.
- **Two parts (A2).** `internal/planpatch` has a pure function that takes the plan's text and returns the patched text or a refusal, and a second function that reads the file, writes it and runs git. Why: the daemon will call the package to answer open questions (T5.6), and every rule can be a row of a table test.
- **What touching a task means (A3).** A task's entry runs from its heading to the line before the next heading. A patch touches a task when it removes a line of the entry or adds lines inside it. New text that begins with a heading belongs to that heading, so a new task can be put directly after a running one. A task that is `running` or `shipping` can only have Amended lines added. A patch that makes any task id disappear is refused whatever the ledger says, because an id is never renumbered.
- **Checked before it is written (A4).** The master plan says the patch is undone if it adds a checker error. Here the patched text is checked in memory and never written when it adds one, so the file on disk is never wrong even for a moment. "Adds" means an error the plan did not already have: a plan with old errors can still be patched.
- **When a patch is committed (A5).** Only the plan file is committed, never anything else that happens to be changed in the folder. It is committed only when git already tracks the plan and the plan had no uncommitted changes; otherwise the patch is written and left uncommitted, with a line saying so. Why: the first draft of a new plan is not committed until it is approved (T5.7), and the planning session patches it before that. The commit is pushed when the repository has an `origin`. A push that fails is a warning with the command to run, not a failure, because the plan on this machine is already right. If `[ship] base` names a branch and the main checkout is on another one, nothing is written.
- **Through a pull request (A6).** With `[planning] via_pr = true` the patch is made in a temporary worktree on the branch `afk-plan-changes`, pushed, and a pull request is opened, or the open one is reused so a second patch builds on the first. The main checkout is never switched to another branch, because the daemon reads and updates it all the time. The plan file in the main checkout changes when the pull request is merged. If the branch has commits and no open pull request, afk stops and asks the owner to check and delete the branch; it never throws commits away.
- **Phases of a change session (A7).** Extend: brief, survey, rounds, slices, draft, check, review. Amend: brief, survey, draft, check, review. Reconcile: brief, survey, rounds, draft, check, review. The draft phase of a new plan is passed when the plan exists, which is no test when it already does, so a change session lists each patch it made in `.afk/plan/changes.md` and cannot leave draft without it. Only extend has a critic; amend and reconcile are checked by `afk plan lint` alone. A reconcile may have no question for the owner.
- **Finished sessions (A8).** A change session is finished when it reaches review: its patches are already in the plan. When the next change session starts, the old session's files are moved to `.afk/plan/archive/<time>/`, because a brief is never edited after it is saved.
- **Choosing the mode (A9).** `afk plan` on a project with a plan asks which of the three, as a numbered list in the terminal. With no terminal it lists the three commands and exits 1.
- **`--yes` (A10)** on `afk plan amend` writes an empty file, `.afk/plan/yes`, which tells the session not to ask the owner to confirm the entry before it is added. A terminal is still needed. Why: the agent of a planning session asks for permission to run commands, and with nobody at the terminal it would wait for ever. Starting an amend with no terminal is left for the surface that needs it.
- **Helper commands (A11).** `afk plan insert` puts a task entry or a milestone in the right place, `afk plan depend` adds a dependency to a task, and `afk plan next-id` prints the id a new task should get. The master plan names only `afk plan patch`. They exist because code should place and count, not the agent, and because they are what the fixture tests of the three modes run: an agent cannot run inside a test, so each mode's test is a scripted list of these operations and an expected plan.
- **Choosing an id (A12).** The next number is the highest number in the milestone plus one, so a gap is never filled and an id is never used twice. A task that must run between two others gets the first free letter after the task it follows (`T2.2a`, then `T2.2b`). After `z` afk says to use the next number.
- **Plugin version 5.5.0 (A13)**, because the skill gains the three modes. The OpenCode port gets a line in its lag note, not the new text.
- **Tests (A14)** are written with the code, as table tests. **Nothing had to be in place first (A15).**
- **`make install` is not run by the task (A16).** This project's after-merge command installs the new binary once the change is merged.

### As built

What the finished code does that differs from, or adds to, the list above (2026-10-13).

- **A bad `via_pr` value names the key.** The value is read loosely and checked afterwards, so a `via_pr = "yes"` gives a message that names the file and `[planning] via_pr` and says to write `true` or `false` without quotes, not a general "not valid TOML".
- **A project with a plan and no open session.** The old refusal ("already has a plan") is gone. A bare `afk plan` asks which change on a terminal, or lists the three commands and exits 1. A bare `afk plan --dry-run` exits 0 and says it would ask. A finished session whose plan file has gone is refused with the place afk looked and how to put it right, because resuming it would start an agent on a finished session. An agent from a finished session that is still running blocks a new change until it is cleaned (`afk clean master-plan`); afk has no way to stop it for the owner.
- **The reconcile file** is looked for in the folder the command was typed in, then in the project. A file found only outside the project, or a folder, is refused. A link that points outside the project is not followed or refused.
- **`state.json` is written last** when a change session starts, after the archive move, `brief.md` and `yes`, so a start that fails part-way leaves no session and the same command can be run again.
- **Held tasks.** Only `running` and `shipping` hold a task; a line after an `Amended` line counts as part of it only when it is not a list item, because an indented `Depends on` bullet would otherwise be read as the task's dependencies. A heading put in the middle of a held task's entry counts as a change to that task, since it moves the rest of the entry under the new heading. Blank lines the plan already has are never removed, because removing one is a removed line of the task above.
- **Builders.** `depend` refuses a `Depends on` line that is prose with no task id, or has a note in brackets that wraps onto the next line, and says to use `afk plan patch`; a note in brackets stays with its id, so the new id goes after it. `insert` takes exactly one task, or one milestone, and refuses a task id or milestone number already in the plan. `next-id --milestone N` refuses a milestone the plan does not have, using the same builder `insert` uses, so the id it prints is one `insert` accepts. Two place flags, or an empty one, are exit 2.
- **Wording.** The commands say `Committed and pushed.` or `Committed, but not pushed.`, with the reason in the note on the next line, not on the same line. A refusal ends with `Nothing was changed.` only when that is true: it is worked out from the plan's bytes and what the outcome shows, and left out in any doubt. A `depend` that changes nothing says the task already waits for the other, and exits 1.
- **Commit and pull request details.** The commit message is one line, passed to git as a single argument with no shell. In pull-request mode the pull request address is printed on its own line and again in the note. The branch is never forced, with one exception: when every commit of `afk-plan-changes` is already in the base, it is reset onto the base, which loses nothing. A branch with commits of its own and no open pull request is refused with both ways out (pull the base, or `git branch -D afk-plan-changes`); the usual cause is a pull request merged on the hosting service that the main checkout has not pulled yet. Nothing is fetched, so a branch that exists only on the remote is not seen. A plan that is not committed is changed in place even with `via_pr`, because a pull request made from a worktree would carry the committed plan, not the owner's edits. A patch refused after the worktree was made leaves the local branch behind at the base; the next patch moves it.
- **The skill.** The survey reads `afk tasks`, since `afk ledger` only writes. A task that is running or shipping is not split or superseded in the session; the session says it must finish first and records that in `changes.md`. The new tasks of a whole new milestone are numbered by the session, since `next-id` needs a milestone that exists. With `via_pr` the session stops and asks for a merge when `next-id` offers an id it already used, because the plan file does not change until then.

### For the owner to check

- A real extend, amend and reconcile session, with a real agent, was not run here. T5.8 does that. The prompts were checked against the phase gates, the messages of the commands and the usage text, and each mode's steps are tested as scripted lists of the commands.
- A real push and a real pull request were tried only against scratch repositories: a bare repository on disk for the push, and a stand-in `gh` script for the pull request. Nothing was sent to a hosting service.
- With `via_pr`, `afk plan lint`, `afk plan next-id` and `afk tasks` read the plan in the main checkout, which has not changed yet, so a second new task in one session can be offered an id the first already took. The session prompt works round it by stopping to ask for a merge; the commands do not.

### Scope ideas

- `planpatch.Run` could return whether it wrote the file, so the command would not have to work that out from the plan's bytes.
- `afk plan patch` could fetch the base and the change branch first, so a merged pull request does not make the next patch refuse.
- `afk plan next-id` and `afk plan lint` could read the plan on the change branch when `via_pr` is set.
- `afk plan` could stop a finished session's agent for the owner instead of asking them to run `afk clean master-plan`.

## T5.5

The design hand-off: the `design` phase of a new planning session, the design brief, the owner's design task and what the daemon tells the owner about it. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **How afk knows the project has an interface (A1).** Round one's interface question now has a fixed id, `r1-interface`, and its recorded answer starts with `yes` or `no`, followed by the kind in the owner's words. Yes means screens a person looks at (a web page, an app); a command line, an API only or none is no, because there is nothing for a design tool to draw. Why: the phase a session moves to is decided by code from the files, and code cannot read a free-text answer. A new session cannot leave `rounds` until that answer is there, so a mistake shows when the question is asked and not five phases later.
- **Entering the phase (A2).** `afk plan advance` goes from `check` to `design` when the answer is yes and to `review` when it is no. The three change modes have no design phase, as before.
- **The gate of the phase (A3)** is checked by code: the plan has no checker errors, `docs/design-brief.md` is written, the plan's header states the precedence rule, the plan has a task titled `Design system (owner)` in its first milestone, and at least one task waits for it. Which tasks build interface is the session's judgement; code only checks that some task waits.
- **One definition of the design task (A4).** A task is the design task when it is an owner task and its title, without the `(owner)` part, is `Design system` (case does not matter). The gate and the daemon both use that one test.
- **What the daemon's item says (A5).** The master plan says the item tells the owner to run `afk plan reconcile docs/design-system.md` when they confirm the task. Here the item for the design task lists the steps in this order: save the design system and commit it, run the reconcile, then answer the item. Why: answering is what lets the interface tasks start, so the plan must already name the design's components by then, and a file that is not committed is not in the worktree an agent builds in. No second item is raised after the answer.
- **The follow-up number is counted by code (A6).** A new command, `afk plan next-followup`, prints the path the next follow-up prompt should be saved at (`docs/design-followup-<n>.md`, one above the highest number there, so a gap is never filled). Why: code counts, and the reconcile fixture needs something a test can run.
- **The precedence rule is in the header from the first write (A7)** when the interface answer is yes; the design phase checks it and patches it in if it is missing.
- **The owner task is added in the design phase (A8)**, with `afk plan next-id`, `afk plan insert` and `afk plan depend`, after the critic has read the draft. The checker runs again as part of the gate.
- **The fixtures (A9)** are scripted lists of the commands and an expected plan, as T5.4's are, because an agent cannot run inside a test.
- **Plugin version 5.6.0 (A10)**, because the skill gains a phase. The OpenCode port gets a line in its lag note.
- **Tests (A11)** are written with the code, as table tests. **Nothing had to be in place first (A12).** **`make install` is not run by the task (A13)**: the after-merge command installs the new binary.

### As built

What the finished code does that differs from, or adds to, the list above (2026-10-13).

- **The interface answer is read by its first whole word.** `yes: a web page` and `No, a command line` are read; `none`, `not sure` and `yesterday` are not, and `afk plan advance` then says how to rewrite the answer. A question with the id and no answer yet is reported as an open question, as any other is.
- **A session started before this change** that is past `rounds` and has no `r1-interface` question is shown at `rounds` again until the question is added. No real session was open when this was built.
- **A checker error during the design phase shows the phase as `check`**, because `check` comes first and has the same rule. The message is the same, and the skill says so, so the session fixes the error and does not run the critic again.
- **The header rule is matched with case and line breaks ignored**, so a wrapped sentence still counts. The header is the text before the first `##` section.
- **"First milestone" is the milestone of the first task in the file**, since a plan may start at M0.
- **The design task's title may carry a note** (`Design system (owner; half a day)`); `Design system for the admin pages (owner)` is an ordinary owner task.
- **`afk plan next-followup` needs no master plan.** A project with no `docs` folder gets `docs/design-followup-1.md`; a project folder that does not exist is exit 1. A folder in `docs/` with a follow-up's name takes its number.
- **Two files outside the work plan's lists were changed**: `plan_cmd.go`, where the command is routed and `afk plan --help` is written, and `main.go`, for the one line in `afk --help`.
- **The task list the owner confirms in `slices` does not show the design task.** The session says one will be added after the check, and the design phase adds it.
- **An unclear interface answer** (a terminal screen, a chat bot) is asked again; the session does not decide it, since it decides whether the owner gets a design task.

### For the owner to check

- A real design phase, with a real agent, was not run here: a task session has no terminal for a planning session. T5.8 does that. The gate, the commands and the daemon's item were run from a built binary against scratch projects, and the prompts were checked against the gate's messages.

### Scope ideas

Not built, recorded for later:

- `afk plan approve` (T5.7) should commit `docs/design-brief.md` with the plan, or the brief stays an uncommitted file.
- The checker could warn when a plan with a design task has a task that names a screen and does not wait for the design task.

## T2.5

Waking a session after a failed turn: the failed-turn marker, the daemon's wake rule and the wake before a stall is reported. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **How the lost connection is staged (A1).** Only the test agent's traffic goes through a small local proxy, and the proxy is what gets cut. Why: cutting the machine's network would also cut every other session running on it and the daemon's own calls.
- **Which failures write the marker (A2).** `server_error`, `overloaded` and `unknown`, plus whatever the staged lost connection is reported as. A turn that ran out of output, an expired login, a billing fault, a bad request and a missing model write nothing, because typing the resume command again cannot fix them. A failed turn that leaves no marker is still caught by the wake before a stall.
- **How the hook tells them apart (A3).** The hook is registered for the rate limit and the reasons above, and the script reads the reason itself. A rate limit writes only `.rate-limited`, a reason worth trying again writes only `.turn-failed`, and anything else, or an input with no reason, writes nothing. One failure never writes both files.
- **How a wake survives a crash (A4).** The daemon saves `wakes`, `wake_at` and `continue_pending` in one write and then types through the continue step T2.3 built. If it dies after the save, the next pass finds the flag and types the line once. A send that fails leaves the flag set and is tried again without counting a second wake. A crash between typing and clearing the flag can type the line twice, as after a restart; the conductor acts from its recorded phase, so that is harmless.
- **Timing (A5).** The first wake comes 30 seconds after the marker. A later one needs `restart_backoff`, doubled each time (one minute, then two), to have passed since the last wake, and 30 seconds since the marker. The 30 seconds are not a setting.
- **The wake before a stall item (A6).** A waiting session has been woken for its current wait when `wake_at` is later than the last write of its `status.json`. The wake counts as activity, so the stall item comes `stall_after` after it when nothing changed. No new field records it.
- **At the limit (A7).** A marker with no wakes left parks the task on one blocking item, "Session kept failing: …", and an answer gives a fresh set. A waiting session with no wakes left gets the stall item as before.
- **`max_wakes = 0` (A8)** means never wake: a marker parks the task at once with words that say afk is set not to wake sessions, and a waiting session gets the stall item at once. It mirrors `max_restarts = 0`.
- **A session whose `status.json` cannot be read (A9)** is not woken.
- **A marker left from before an un-park (A10)** does not wake the session: the marker must also be newer than the answer that un-parked the task, because the un-park already types the resume command.
- **A rate limit is never woken (A11).** A session with a fresh `.rate-limited` marker is not typed to by the stall wake either; the stall item is raised as before.
- **Plugin version (A12)** goes from 5.6.0 to 5.7.0, because the hook's registration changes.
- **The OpenCode port (A13)** gets no hook, since it has no hooks folder. Its copy of the schema gets the same section, and its lag note says a session it runs is woken only by the wake before a stall.
- **The real check (A14)** uses a scratch daemon and a stand-in agent for each behaviour, then one real agent whose connection is cut. What could not be staged is left as an owner check.
- **No `make install` from the session (A15).** This repo's `after_merge` installs the new binary after the merge. Installing unmerged code would make the running daemon restart on it.

Decided while building, and left for later:

- **A failed turn beside a rate limit.** The marker rule also leaves a session alone when its failed-turn marker is no later than a rate-limit marker of the same run, so a rate limit is never woken by either rule, even on a daemon with no usage source. A failed turn later than the rate-limit marker is still woken.
- **What the wake before a stall covers.** The staged run showed `awaiting_input` is set by an idle prompt only in phase `planning`. Outside planning, a failed turn is caught by the marker alone, and one whose reason writes no marker would sit idle. The README says no more than that. Idea left out: a rule for a session idle at its prompt with no marker.
- **Idea left out:** skip both wake rules whenever usage says pause or refuse. Today the usage rule always acts first on the shipped daemon, so it cannot happen.
- **The schema document is at its size cap.** The new block fitted only by trimming existing prose; no rule changed. The next addition will need the cap raised or the document split.
- **For the owner to check:** after the merge and a new plugin install, the next real lost connection should leave `.afk/<session>/.turn-failed`, a `wake` line in the decision log about 30 seconds later, and no inbox item. A woken conductor carrying real task work on has not been seen yet.

### What the agent CLI reports

Part 1 of the task, done 2026-10-13 with version 2.1.296 of the agent CLI. In short: **the `StopFailure` hook does fire for a lost connection, and the reason it gives is `server_error`.** The evidence is in `.afk/t2-5/evidence/` (`w1-t1-docs.txt`, `w1-t1-capture.txt`, `w1-t1-no-connection.txt`, `w1-t1-payloads.jsonl`).

**What the documentation says** (the hooks reference at `https://code.claude.com/docs/en/hooks.md` and the errors reference beside it):

- `StopFailure` runs instead of `Stop` when an API error ends a turn. Its output and exit code are ignored, apart from a `terminalSequence` field.
- The reasons, in the `error` field: `rate_limit`, `overloaded`, `authentication_failed`, `oauth_org_not_allowed`, `account_on_hold`, `billing_error`, `invalid_request`, `model_not_found`, `server_error`, `max_output_tokens`, `cloud_credential_error`, `unknown`. This is the list the work plan started from, with nothing added or missing.
- The input fields: `error`, an optional `error_details` and an optional `last_assistant_message` (the error text shown on screen), plus the common ones. The common ones are more than the work plan listed: `session_id`, `prompt_id`, `transcript_path`, `cwd`, `scratchpad_dir`, `permission_mode`, `effort` and `hook_event_name`.
- The matcher is compared whole, and `|` is its only separator, as long as it holds only letters, digits, `_` and `|`. A hyphen, a space or a comma turns it into a regular expression.
- The hooks page does not say which reason a lost connection gets. The errors page says what the CLI does about one, but not the reason either: it tries a failed request again up to 10 times, except that a connection lost after part of the answer was finished is not tried again, because that could run the same tool call twice. The turn then ends with "Connection lost mid-response. The response above may be incomplete." and the owner is told to reply `continue`.

**What was staged.** One real agent, started by hand in a tmux session of its own, with only its own traffic sent through a small local proxy. Cutting the proxy closed its open connections and refused new ones. `status.json` was seeded with all nine fields and the agent's session id, so the installed plugin's hooks treated it as a session. Four runs:

| Run | What was cut | What the screen showed | Time from the cut to the hook | `error` |
|---|---|---|---|---|
| 1 | The proxy, while the first paragraph of text was still arriving | The text was thrown away, then "Connection refused … Retrying in 8s · attempt 5/9" up to 9/9, then "API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)" | 2 minutes 20 seconds | `server_error` |
| 2 | The proxy, after one paragraph was finished and while a tool call was arriving | "API Error: Connection lost mid-response. The response above may be incomplete." This is the message from the T5.6 session. | None: the same second, with no retry | `server_error` |
| 3 | Nothing to cut: the proxy address had nothing listening from the start | "Connection refused … attempt 10/10", then the same "Connection refused" error | 3 minutes after the prompt | `server_error` |
| 4 | As run 3, with `CLAUDE_CODE_MAX_RETRIES=1` and `status.json` at `planning` | The same error | Under a second | `server_error` |

Run 4 used the one-retry setting to save time, not because the CLI retried for more than five minutes. It never did: the longest was three minutes.

**The captured input**, word for word, for run 2 (the lost connection mid-response):

```json
{"session_id":"b5f3ac8c-bcd1-419a-9814-1b08d83a716a","transcript_path":"/home/dphaener/.claude/projects/-home-dphaener-code-afk-t25-spike/b5f3ac8c-bcd1-419a-9814-1b08d83a716a.jsonl","cwd":"/home/dphaener/code/afk-t25-spike","scratchpad_dir":"/tmp/claude-1000/-home-dphaener-code-afk-t25-spike/b5f3ac8c-bcd1-419a-9814-1b08d83a716a/scratchpad","prompt_id":"f662253b-0aa6-4e51-b3f1-dfad3d7e0038","effort":{"level":"medium"},"hook_event_name":"StopFailure","error":"server_error","last_assistant_message":"API Error: Connection lost mid-response. The response above may be incomplete."}
```

Runs 1, 3 and 4 gave the same fields with `"last_assistant_message":"API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)"`. No run carried `error_details` or `permission_mode`. No `Stop` hook ran in any of them. A `Notification` of type `idle_prompt` ("Claude is waiting for your input") came exactly 60 seconds after each failure.

**What `status.json` showed.** Nothing changed at the failure. With the phase at `executing`, `awaiting_input` stayed `false` before the cut, after the failure and after the idle notification, and `updated_at` stayed at the time the prompt was sent. With the phase at `planning` (run 4), `awaiting_input` was still `false` one second after the failure and became `true` 61 seconds after it, when the idle notification arrived. That is the rule the notification hook already has: an idle prompt counts as waiting only while planning. The installed plugin's own hook log has no `StopFailure` line for any run, and no marker file appeared, because its hook is registered for `rate_limit` only. This matches the T5.6 session: nothing on disk for the failure, then a session shown as waiting, then the stall item ten minutes later.

**The reasons worth trying again** are `server_error`, `overloaded` and `unknown`. The lost connection is `server_error`, which was already on the list, so nothing is added and the hook's matcher is `rate_limit|overloaded|server_error|unknown` as planned.

**What later parts of the task should take from this:**

- **The marker is the only signal outside the planning phase.** A session that is executing, reviewing or fixing never shows `awaiting_input` after a failed turn, so the stall rule never sees it and the wake before a stall item (part 4) cannot cover it. Part 4 is a second net only for a session that is still planning, or one whose `awaiting_input` was already set. A session whose plugin is older than this change, and so writes no marker, is woken only while planning.
- **The CLI has already retried before the hook runs**, unless the connection was lost mid-response. When the network is down for a long time each wake costs the CLI's own retries (two to three minutes) and then a new marker, so three wakes with their waits of 30 seconds, one minute and two minutes take about twelve minutes before the task is parked.
- **A mid-response loss is reported at once**, so the 30 seconds before the first wake are the only wait the network gets to come back. The CLI then retries the woken turn by itself if the network is still down.
- **`last_assistant_message` holds the plain error text.** The marker keeps the reason only, as planned, so the item raised at the limit can name the reason and the time but not this text.

**How the staging differed from the work plan.** A new git repository, both at `/tmp/afk-t25-spike` and under the already trusted `~/code`, stopped at the CLI's "do you trust this folder?" dialog. That dialog is the owner's to answer, so it was not answered and both sessions were ended. A plain folder with no git repository, `~/code/afk-t25-spike`, started with no dialog, and all four runs used it. The hooks find a session from the folder and the session id, not from git, so this changes nothing in the result. The folder was removed afterwards, the tmux sessions were ended by name and the proxy by its pid. The CLI keeps its own record of the folder under `~/.claude/projects/-home-dphaener-code-afk-t25-spike`, which was left alone.

### The real check

One real run, on 2026-10-13 (UTC), on this machine. Tools: go 1.25.14, git, jq 1.8.1, version 2.1.296 of the agent CLI, on Linux 7.0. The full transcripts are in `.afk/t2-5/evidence/`: `w3-t2-wake.txt` (the set-up and scenario 1), `w3-t2-limit.txt` (2), `w3-t2-ratelimit-crash.txt` (4, then 3), `w3-t2-stall-real.txt` (5, the real agent and the clean-up). The scripts used are beside them (`w3-t2-shim-claude.txt`, `w3-t2-lib.sh.txt`, `w3-t2-partb-driver.sh.txt`, `w3-t2-proxy.log`).

**The binary.** Built from this branch (HEAD `bd8acac` plus the uncommitted change) with `go build -o /tmp/afk-t25-check .`, sha256 `1d4ae798…b8a202`. `grep -c -a 'Session kept failing'` on it gives 1, so it holds the change. `~/bin/afk` was not touched.

**What was run.** In the manner of the T2.3 and T2.4 real checks: a throwaway folder, `/tmp/afk-t25-qa`, with a scratch home, temp, config and state folder set only on the afk commands, so nothing touched the real ones. A throwaway git repository with one commit, a two-task plan, an `afk.toml` naming the plan and an empty ledger made with `afk ledger init`. The config set `tick = "1s"` and left `max_wakes` (3) and `restart_backoff` (1 minute) at their defaults. Unlike the earlier checks, the daemon ran as a loop (`afk daemon`, not `--once`), so every time below is the loop's own.

Part A used no agent. A shell script named `claude` stood first on the daemon's `PATH`. It answered the usage probe with the committed fixture at 10%, said "logged in" to the login check, and otherwise was the stand-in agent: it appends each line typed into its terminal to a file and prints a dot every two seconds. The daemon created the session for T0.1 itself at 18:46:49 (host pid 3290308). The status file was then edited by hand to what a working agent leaves: a conversation id and phase `executing`. A "failed turn" in Part A is the worktree's own `plugins/afk/hooks/on-stop-failure.sh` run with the input the CLI gives it, the one captured in "What the agent CLI reports" above with this session's id and folder.

**Part A: the stand-in agent.**

**1. A failed turn wakes the session.**

- At 18:47:09 the hook was given a `server_error` input and wrote `.turn-failed` with `{"at":"2026-10-13T18:47:09Z","reason":"server_error"}`. The daemon's state a second later: "the agent's turn failed in the session for T0.1 (t0-1); waiting a moment before typing its resume command".
- At 18:47:29, twenty seconds in, nothing had been typed and the ledger entry had no `wakes`.
- At 18:47:39, thirty seconds after the marker, the stand-in's file gained `/afk:go --session t0-1`, once. The ledger entry gained `wakes` 1 and `wake_at` 18:47:39. The decision log gained one `wake` line: "type `/afk:go --session t0-1` into it (wake 1 of 3); nothing is restarted".
- The host and the stand-in were the same processes before and after. The host's log shows the daemon attach, type and leave within half a second.

**2. The turn keeps failing, up to the limit.** The stand-in never removes the marker, so each new failure is the hook run again, which rewrites the marker with a new time.

- Second failure at 18:47:56. At 18:48:28 the daemon was still waiting: "waiting before waking it again (1 time so far)". The second line was typed at 18:48:39, one minute after the first. `wakes` 2.
- Third failure at 18:48:40. At 18:49:21 it was still waiting, with the 30 seconds served and the two minutes not. The third line was typed at 18:50:39, two minutes after the second. `wakes` 3.
- So the typed lines came at 18:47:39, 18:48:39 and 18:50:39: gaps of one minute and two minutes, as the backoff says.
- Fourth failure at 18:50:49. On the next pass the task was `parked` on one blocking `failure` item, "Session kept failing: T0.1 (t0-1)". Its body says afk woke the session 3 times, gives the time and the recorded reason (`server_error`), and says to look with `afk attach t0-1` and then answer the item. The decision log gained one `wake_give_up` line. `afk daemon status` said "waiting on owner: T0.1 parked; answer inbox d9znux".
- A fifth failure at 18:50:53, with the task parked: forty seconds later nothing more had been typed and there was still one item.
- The item was answered at 18:51:39 with `afk inbox answer d9znux done`. One second later the task was `running` again, the ledger entry had lost `wakes` and `wake_at`, and `/afk:go --session t0-1` had been typed once (the un-park's own line). The decision log gained one `unpark` line.
- The marker of 18:50:53 was still on disk and older than the answer. Forty-five seconds later it had woken nothing and `wakes` was still absent.

**3. A rate limit is not woken.** Run last, because it ends the session. The task was first put back to plain running (the stall item of scenario 5 answered, the old marker removed by hand).

- At 18:55:29 the hook was given a `rate_limit` input. It wrote `.rate-limited` with `{"at":"2026-10-13T18:55:29Z"}` and no `.turn-failed`.
- On the same pass the daemon's usage rule took it: "the session hit a usage limit; pausing the session for T0.1 (t0-1)". The host and the stand-in were stopped, and the ledger entry gained `usage_pause_at` and `usage_wake_at` (the session window's reset in the stand-in's reading, about four hours on). It gained no `wakes`.
- Forty-five seconds later the stand-in's file still held seven lines, the last being the un-park's of 18:55:24. Nothing was typed for the rate limit and no item was raised.

**4. A daemon that died after saving a wake.** The gap between the save and the typing is a few milliseconds, too short to hit with a signal, so the saved state was written by hand.

- A new failure at 18:52:46, and the daemon killed in the same second, before it acted. The host and the stand-in lived on.
- The ledger entry was edited to what the wake's one save leaves: `wakes` 1, `wake_at` 18:52:48 and `continue_pending` true.
- The daemon was started again at 18:52:48. Its first pass typed `/afk:go --session t0-1` once (18:52:48) and cleared `continue_pending`. `wakes` stayed 1. The decision log gained one `continue` line: "The session for T0.1 (t0-1) is still owed its resume command; typing it".
- Forty-five seconds later there was no second line and `wakes` was still 1: the marker of 18:52:46 is older than the saved wake, so it was not counted again.

**5. A waiting session is woken once before the stall item.** With `stall_after = "40s"` in the scratch config.

- At 18:53:46 the status file was edited to `awaiting_input` true. Thirty seconds later nothing had happened.
- At 18:54:26, forty seconds after that edit, the daemon typed `/afk:go --session t0-1` once. `wakes` went from 1 to 2 and the decision log gained a `wake` line: "has been waiting at the terminal; typing its resume command before telling you".
- At 18:55:01 there was still no item. At 18:55:06, forty seconds after the wake and with the status file unchanged, the task was parked on one blocking item, "Session waiting at the terminal: T0.1 (t0-1)". Its body says "afk typed its resume command (`/afk:go --session t0-1`) to set it going again, and it was still waiting afterwards, so someone has to look."
- Nothing more was typed between the wake and the item.

**Part B: one real agent, its connection cut.**

**How it was staged.** A second throwaway repository, `p2`, with a one-task plan. The scratch daemon launched the session itself, as in Part A. This time the script named `claude` started the real agent CLI, with these differences from a session the real daemon starts:

- **The agent ran in a plain folder, not in the session's worktree.** W1.T1 found that a new git repository stops at the CLI's "do you trust this folder?" dialog, which is the owner's to answer, and that a plain folder under the trusted `~/code` does not. So the agent's folder was `~/code/afk-t25-real`, holding only a link named `.afk` to the worktree's `.afk`. The hooks find a session from the folder and the session id, so they wrote where the daemon reads. The host, its terminal and the daemon's view of the session were the worktree's, unchanged. The direct staging, with the agent in the worktree, was not tried a third time.
- **Only the agent's traffic went through a local proxy** (`HTTPS_PROXY`, W1.T1's proxy script). The machine's network was not touched.
- **The worktree's hooks were registered for it** in the scratch project's own settings file (`p2/.claude/settings.json`, passed with `--settings`): the five entries of `plugins/afk/hooks/hooks.json` with the scripts' full paths, the `StopFailure` one with its new matcher. The installed plugin (5.6.0, old hooks) was left as it is, so its hooks ran too.
- **Nothing of the daemon's environment was passed on**, so no `CLAUDE*` variable reached the agent. An `afk` first on the agent's `PATH` pointed at the scratch binary and folders.
- **The first prompt was a test prompt**, not `/afk:go`: one short paragraph, then a `Write` tool call with a long essay, in one response. The status file was staged before the agent started (the conversation id, phase `executing`).

**What was seen.**

- 18:59:00: the agent started. 18:59:02: the prompt was taken (two `UserPromptSubmit` lines in the hook log, one for each set of hooks).
- 18:59:09: the paragraph was on screen and the tool call was arriving. The proxy was cut. In the same second the screen showed "API Error: Connection lost mid-response. The response above may be incomplete.", the message from the T5.6 session. The hook log has one `StopFailure` line with `error` `server_error`, and `.turn-failed` held `{"at":"2026-10-13T18:59:09Z","reason":"server_error"}`. **So the marker appeared, written by the registered hook from the CLI's own input.**
- 18:59:11: the proxy was brought back. Nothing else was touched; the terminal used for watching was closed.
- 18:59:10 to 18:59:38: the daemon said "the agent's turn failed in the session for T0.1 (t0-1); waiting a moment before typing its resume command".
- 18:59:39, thirty seconds after the marker: **the daemon typed `/afk:go --session t0-1`.** The ledger entry gained `wakes` 1 and `wake_at` 18:59:39, and the decision log one `wake` line (wake 1 of 3).
- 18:59:39: **the agent started a new turn.** The hook log has `UserPromptSubmit` with the prompt `/afk:go --session t0-1`; that hook removed `.turn-failed` and moved the status file's `updated_at` to 18:59:39. The screen showed the typed line as a prompt of its own and then "I am The Conductor. Starting session t0-1: learning my session id and checking for existing session state.", with one shell command run by 18:59:49.
- 18:59:55: the session was ended, sixteen seconds into the new turn. The daemon was stopped first so that it could not start the session again, then the host, which took the agent with it.
- No inbox item was raised at any point.

**Clean-up.** The scratch daemons were stopped by pid (three were started in all: one restarted for scenario 4, one for Part B). The Part A host had already been stopped by the daemon's usage pause; the Part B host was stopped by pid. The proxy was stopped by pid, and the one tmux session, on a tmux server of its own, by name. `/tmp/afk-t25-qa` and `~/code/afk-t25-real` were removed. A last `pgrep`, run from a script file, found nothing of the run, and the check's tmux server was gone. The real `afkd` unit had the same `MainPID` (3085591) and `ActiveEnterTimestamp` before and after. Left on purpose: the scratch binary `/tmp/afk-t25-check`, which is outside `PATH`, and the CLI's own record of the one real conversation under `~/.claude/projects/-home-dphaener-code-afk-t25-real`, which is the owner's.

**Bugs.** None in behaviour. No Go file, test or hook was changed. One wording fault, left for a later change:

- **The give-up sentence reads oddly.** The decision log's question for `wake_give_up` is "The agent's turn failed again in the session for T0.1 (t0-1) after 3 times; giving up on it." It means "after being woken 3 times". The inbox item's own text is fine ("afk woke it 3 times by typing its resume command").

**Seen, and left as it is.**

- **A failure straight after the last wake parks at once.** With no wakes left there is no wait: the fourth failure came ten seconds after the third wake, and the item was raised on the next pass. A real agent takes longer to fail again, but a session that fails at once is handed over at once.
- **The second failure's first wait sentence does not say "again".** Until thirty seconds have passed since the new marker the reason is "waiting a moment before typing its resume command"; after that it is "waiting before waking it again (1 time so far)".
- **The wake before a stall item uses the same count.** In scenario 5 it was "wake 2 of 3", because scenario 4 had left `wakes` at 1.
- **An un-park types the resume command with no line of its own in the decision log.** The `unpark` line is the only record. This is as it was before this task.
- **A rate limit ends in a usage pause, not in a wait at the prompt.** With a usage source the daemon stops the session, so "never typed to" in scenario 3 is partly because there was nothing left to type into. The wake rule's own part was that no `.turn-failed` was written and no wake was counted.
- **With both sets of hooks registered, each prompt is handled twice.** That is the staging, not the change: once the new plugin is installed there is one set.

**Not checked.**

- A real agent in a real session worktree. The agent in Part B ran in a plain folder linked to the worktree's `.afk`.
- The installed plugin. It is still 5.6.0, whose `StopFailure` hook does not run for `server_error`. The hooks used here were the worktree's, registered through a settings file.
- Whether the woken conductor carries the task's work on. The real session was ended sixteen seconds into its new turn, and it had no work plan to carry on with.
- A long outage: the CLI's own two to three minutes of retries, a new marker after each wake, and the task parked after about twelve minutes. The proxy was back within two seconds here.
- The wake before a stall item against a real agent, and what a typed `/afk:go --session X` does to a real agent that is asking a real question in a dialog.
- A real kill between the wake's save and its typing. The saved state was written by hand.
- A wake whose line cannot be typed (the send fails), and `max_wakes = 0`. Unit tests cover both.
- The `overloaded` and `unknown` reasons from a real CLI. Only `server_error` was staged.
- A real rate limit, and a rate limit where the daemon has no usage source and so cannot pause the session.
- A failed turn while the session is running the Workflow tool.
- The OpenCode port, macOS, and push notifications. The scratch projects had no ntfy address.

### Not verified, owner check

- **The next real lost connection, after this branch is merged and the plugin is installed again.** In the session's folder, `.afk/<session>/.turn-failed` should appear at the moment the screen shows "API Error: Connection lost mid-response". About thirty seconds later `.afk/log/decisions.jsonl` in the project should gain a `wake` line, `afk tasks` should show the task still running, and the session should carry on by itself with no inbox item. If "Session waiting at the terminal" appears instead with no wake line before it, the marker was not written: check that `afk plugin install` has been run since the merge and that the session was started after it.
- **A session that keeps failing for real.** Three typed lines over about twelve minutes, then one item, "Session kept failing: …". Answering it should set the session going again.
- **A woken session picking its work up.** The real session here was ended as soon as the conductor's new turn began.
- **The running `afkd`.** It keeps the old code until this branch is merged and installed.

## T5.6

Open questions hold their tasks. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **One reader for the table (A1).** The checker was the only code that read the Open questions table. That reader moves into `internal/masterplan` and the checker calls it, so the daemon and `afk plan lint` can never disagree about which rows are open. The checker's findings and counts do not change. The same package also builds the answered form of a row.
- **How the daemon gets the rows (A2).** The daemon reads the plan file itself on each pass, through a new optional hook. Without the hook nothing is held, which is what every existing test relies on. If the plan cannot be read, a pass that would start a task waits instead.
- **Owner tasks are held too (A3).** The master plan says "the next ready task". An owner task named by an open question gets the question first and its own "Your task" item after the answer.
- **Matching an item to a row (A4).** A row has no id, so the item carries the question's text as the first line of its body, and that line is what is compared. The title is not used, because titles are cut to 200 characters and two long questions could then look the same. The task is not part of the match, so a question needed by two tasks is asked once. To ask a question again, change its wording.
- **What a held task looks like (A5).** It is `parked` in the ledger with no session, waiting on the question items. When they are answered it goes back to `pending`, never to `running`, because there is no session to continue. The two new decisions are `question_hold` and `question_answered`.
- **A row holds until the plan in the main checkout says it is answered (A6).** An answer in the inbox is not enough. The agent that runs the task reads the answer in the plan, so the task must not start before the answer is there.
- **The answered row (A7).** The answer is added to the end of the Question cell as `**Answered <date>:** <answer>` and the question stays. The date is the day the item was answered (UTC). Line breaks in the answer become spaces and a `|` is written `\|`, so the table stays a table.
- **When the answer cannot reach the plan (A8).** With `[planning] via_pr` the patch goes into a pull request and the plan in the main checkout does not change until it is merged and pulled. A patch can also fail (the row is in the plan twice, git refuses). In both cases the daemon raises one blocking item, `Put the answer in the plan: <task>`, that says what to do, and the task waits on it. Answering it makes the daemon try again. No new state file is needed: the inbox and the ledger are the record.
- **A row answered by hand (A9).** If the owner, or a planning session, answers the row in the plan while its item is still open, the daemon closes the item itself with "answered in the plan", as it closes its own items for a missing ledger. Otherwise the task would stay parked on an item nobody needs.
- **Pause, stopped line and usage (A10).** The hold behaves as an owner task does: the question is still asked while the project is paused, a stopped line holds it back, and it never causes a usage reading.
- **The daemon package does not run git (A11).** The patch goes through a second hook that the `afk daemon` command connects to `planpatch.Run`, with the project's `via_pr` and base branch.
- **Plugin version 5.7.1 (A12)**, planned as 5.6.1 and raised when T2.5 took 5.7.0 on `main` first, because the format document's text on open questions changes. The OpenCode port gets a line in its lag note.
- **Tests (A13)** are written with the code, as table tests. **Nothing had to be in place first (A14).**

### As built

Where the code does something the section above does not say (2026-10-13).

- **A row with an empty Question cell holds nothing.** The item is found again by the first line of its body, and an empty first line could never be matched.
- **An item the daemon closed itself is not an answer.** If a row is open again after its item was closed with "answered in the plan", the owner is asked afresh; those words are never written into a row.
- **An unreadable plan also holds back a pending owner task**, not only an agent's. A task that is already parked is not held back by a read error.
- **A hold's follow-up item that is open but not yet in `parked_on`** (a pass died between raising it and writing the ledger) is adopted without a second patch, so a pull-request project does not get two pull requests.
- **One patch per distinct question text in a pass.** Two rows with the same text cannot be told apart, so the second would fail the same way. The follow-up item still lists every row with its own line number.
- **The follow-up item is built row by row.** A row whose patch returned a pull request address says to merge it and pull the base branch; any other row gives the reason and the exact text to write into its Question cell (`**Answered <date>:** <answer>`).
- **The plan is read on every pass**, so a row answered by hand closes its item while another task runs.
- **A task parked on the follow-up item shows as `question_hold`** in the state file, not a third kind: to the owner it is the same wait.
- **A fourth decision-log kind, `question_closed`,** records the daemon answering an item whose row is no longer open.
- **An answer that `AnswerRow` refuses** (for example one with an unmatched backtick that would swallow later pipes in the row) is treated as nothing to write, and the follow-up item then gives the text to write by hand.
- **Usage:** the two new kinds are left out of the list of kinds that cause a usage reading, so a hold never causes one.

### For the owner to check

- Try the whole path once against a real project that has `[planning] via_pr = true`: answer a question and confirm the pull request opens, that merging and pulling it lets the task start after you answer the `Put the answer in the plan: …` item. The tests use a scripted runner, not GitHub.
- Read an `Open question: …` item on a screen and judge whether its wording is clear enough.

## T5.7

Approve and go. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **How a plan can be read on the web page before it is approved (A1).** The page only knows the projects in the machine config, and a project gets there when it is approved. So a small list, `plan-reviews.json` in afk's state folder, names the projects whose plan is waiting. It is written when a new planning session reaches `review` and the entry is dropped by the approve. The page shows the plan view for configured projects and for listed ones. Adding the project to the machine config early was the other way, and it would have needed a new daemon rule to stop the build starting.
- **Who sends "plan ready for review" (A2).** `afk plan advance`, when it moves a new session to `review`, because that is the one place the phase is entered. It is best effort: a notification that cannot be sent never undoes the advance. Change sessions send nothing.
- **How the daemon sees a new project (A3).** It reads the project list from `config.toml` again at the top of every pass. If the file cannot be read it keeps the list it had and says so once. Every other setting is still read once, at the start.
- **A daemon with no projects (A4)** starts and waits, and says how to add one. It used to exit with an error, which on a new machine would mean the first approve finds no daemon to pick the project up.
- **How the plan is rendered (A5).** afk has its own small renderer for the part of markdown a plan uses, and it escapes every piece of text. No markdown library is added.
- **What the approve commits (A6).** The plan, `afk.toml`, `.gitignore`, `CONTEXT.md`, the design brief and the agent instruction files: only those that exist and have changed, and nothing else in the folder. Task sessions work in copies made from committed files, so the instructions and the ignore lines have to be committed for them to apply.
- **Whether it pushes (A7).** Yes, to `origin` when the project has one; a push that fails is a note with the command to run, not a failure. With `[planning] via_pr` it does not push, because a push to the base branch deploys in such a project, and the commit goes out with the first task's pull request.
- **Approving twice, and change sessions (A8).** A plan that is already approved says so and nothing changes. An extend, amend or reconcile session is refused: its changes are already in the plan.
- **The order of the steps (A9):** `afk.toml`, the commit, the ledger, the machine config, the phase, then the review-list entry. The ledger exists before the daemon can see the project, and the phase is written last, so a run that stops half way leaves the session at `review` and running it again finishes it.
- **The project command (A10)** is `afk project add [--name NAME] [DIR]`, `remove <name or DIR>` and `list`. `add` creates `config.toml` when there is none. `remove` changes nothing in the project and stops no session.
- **Tests (A11)** are written with the code, as table tests. **Nothing had to be in place first (A12).**

### As built

Where the code does something the section above does not say (2026-10-13).

- **The daemon knows a project by its folder, not its name.** A project renamed in `config.toml` is the same project and nothing is logged. After a config that could not be read is mended, the daemon says once that the list can be read again.
- **The web page shows the daemon's last good list.** It does not read `config.toml` itself, so a file caught half-saved never empties the page while the daemon still builds.
- **Before any step, the approve also refuses** when the folder is not a git repository, the plan is outside the project folder, the machine config cannot be read, or the daemon would refuse the plan. Each refusal ends with "Nothing was changed."
- **The push only follows a commit made in the same run.** A second run that finds nothing to commit does not push; a push that failed printed the command to run by hand.
- **A review-only plan has a plan view and nothing else:** no project page, no inbox, no pause or resume. An entry is hidden when a configured project has the same folder or the same name.
- **The plan view shows the checker's errors and only counts the warnings.** Open questions are shown as plain text. Numbered lists are drawn too; markdown links, italics and images are not.
- **The review list is written even with no ntfy address;** only the notification needs one. A name in the list is the folder name, with `-2`, `-3` when two folders share it, and it stays the same when a plan goes up for review again.
- **`afk project remove`** takes the name, the path as listed, or a folder that resolves to the project. A word that matches two projects is refused.
- **The agent instruction files** are a list in `internal/provider`, not a new method on the provider interface.
- **Plugin version 5.7.2**, because the planning skill's review step now ends with `afk plan approve`. The OpenCode port gets a line in its lag note.

### Scope ideas

- An approve that stops between writing the phase and dropping the review-list entry leaves the entry behind, and a second run does nothing. It is hidden while the project is configured, and would show as waiting again after `afk project remove`. Letting the already-approved path drop the entry would close it.
- The daemon's reload asks git for each project's main checkout on every pass. With a few projects and a 5 second tick that is small; caching it by path would remove it.

### For the owner to check

- Approve a real plan while `afk daemon` runs and see the first task start with no restart. The test does this with a stand-in for the agent launch.
- Open a plan view on a phone and judge whether the rendered plan reads well, and whether the "Plan ready for review" notification's link opens it.
- `make install` was not run by this session: the daemon's `after_merge` command installs the new afk when this merges.

## T6.1

The switcher lists the projects in `config.toml`. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **How the dashboard gets the list (A1).** Through one new injected reader beside the two it already has for `config.json` and the folder search. The reader lives with the dashboard command, not in the screen code, because working out a project's main checkout asks git, and those helpers live there. It hands back each listed project's main-checkout path, its name and whether its folder is gone, plus the file's path and any error.
- **Listed projects do not wait for the folder search (A2).** They are read by their own small step, at the start, on every scan and on `R`. The folder search answers only when its whole walk is done, and a broken `config.json` stops it early; neither should hide the listed projects.
- **One merge (A3).** A single pure function builds the switcher's list from the start-up project, the last good read of the listed projects and what the last folder search found. The dashboard remembers the last two, because they arrive at different times.
- **The name shown (A4).** A listed project shows the name `afk project list` prints: the `name` key, or the folder name without one. Other projects keep the labels they had. The folder path is still the only thing that identifies a project.
- **Started inside a session's worktree of a listed project (A5).** Still one row. The start-up row keeps the folder the owner opened and stays the active project, and it takes the listed name.
- **What "cannot be read" means (A6).** Anything the daemon's own loader refuses: a file it cannot open, a file that is not valid TOML, or a bad value anywhere in it. The switcher keeps the rows it had and one notice names the file. A second, more forgiving reader was the other way; it would let the dashboard and the daemon disagree about whether the file is good.
- **Session counts for listed projects (A7).** The scan covers them the way it covers the start-up project, so a listed row shows its sessions and click-to-focus works for it.
- **Tests (A8)** are written with the code, as table tests. **Nothing had to be in place first (A9).**

### As built

Where the code does something the section above does not say (2026-10-13).

- **A listed read that changes the listed folders asks for one more scan.** The scan learns which folders to count sessions in when it starts, and at start-up it starts beside the first read of the list. Without the extra scan a listed row would show no session counts for a minute. A read that changes nothing asks for nothing.
- **Started in a worktree of a listed project, the search's own row for that project's main folder is hidden too,** so there is still one row. For a project that is not listed nothing changes.
- **Removing the active project from the list moves the dashboard to the first row,** as it does when a folder search stops finding the active project. This only happens to a project that was in the switcher because it was listed, and is not the one the dashboard was started in.
- **A project renamed in `config.toml` shows its new name the next time the switcher is opened.** An open switcher is rebuilt only when the set of folders changes, so the cursor and the filter are not reset under the owner's hands.
- **A folder that is gone is never asked about in git,** and the scan does not watch it.
- **The notice reads** "Could not read the project list: <the loader's message>. The switcher keeps the projects it had." The loader's message starts with the file's full path.
- **Nothing outside display uses a project's name.** The active project, remembered selections, open counts and session sockets all go by the folder path, so showing the `config.toml` name changes no identity.
- **`afk project add` saves `config.toml` by writing a new file and renaming it,** so the dashboard never reads a half-saved list.

### Scope ideas

- The README's `p` entry has one clumsy sentence left by this change ("A listed project shows the name `afk project list` shows. Each row showing its session count…"). The review marked it minor; it wants a one-line tidy.
- A comment in `internal/ui/focusrequest_test.go` still says "unionProjects order"; the order it describes is unchanged, only the function's name is.

### For the owner to check

- Open `afk dash` on the always-on machine with the real `config.toml` and press `p`: both listed projects should be there under the names `afk project list` prints. The check here used a scratch config folder and throwaway projects.
- `make install` was not run by this session: the daemon's `after_merge` command installs the new afk when this merges.

## T5.8

T5.8 is the proof run for `afk plan`: the owner planned afk's next milestone with `afk plan extend` and a small throwaway project (`~/code/todo`) with `afk plan`, approved it and let the daemon merge its first task. This session read the files both runs left behind and wrote up how they compare with the hand-run nebari session. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The session waited for the owner's two runs before doing any work (A9).** Both are attended terminal sessions, which a task session cannot run. It raised one inbox item that stopped the task (`u76qn2`) and started again when the owner answered.
- **All nine prompt faults the two runs showed are fixed here (A1).** Each one is tied to a real turn the owner had to take or a real piece of rework, so none is a matter of taste.
- **The brief stays unedited when the owner changes the request (A2).** The plan says `brief.md` is never edited after it is saved. So the fix for "the critic reviewed against a request nobody wanted any more" is to hand the critic the owner's later words beside the brief, not to rewrite the brief.
- **Code faults are filed, not fixed (A3).** The task says so. Each of the four found is one non-blocking inbox notice, because each needs its own reviewed change.
- **The two sessions are not run again to prove the prompt fixes (A4).** That would need the owner at a terminal again. The next real planning session is the test, and an inbox item asks the owner to watch for the fixed faults then.
- **Tokens are given as raw counts from the session transcripts, plus the weighted figure of the plan's section 4.7 (A5).** afk records no tokens for a planning session, which is one of the filed faults. Nebari's planning session has no token figure in the plan, so tokens are not compared with it.
- **Where the owner's answer and the files disagree, the write-up gives both and the files decide the facts (A6).** The owner's answer said the new project needed no action; the files show a login and one typed line.
- **The plugin version goes from 5.7.2 to 5.7.3 (A7),** as every earlier change to the planning skill did, so an agent that caches the plugin by version picks up the new prompts.
- **No new automated tests (A8).** The change is prompt text and documents. The plugin's own tests and `make check` cover the prompt files' size and presence.
- **Turns and questions are counted the same way for both sessions, and every number below was counted again for this write-up.** A turn is a message the owner typed or a question dialog the owner submitted, and the two are given separately. A question is anything the session asked the owner to answer after the request itself, whether or not it was saved in `questions.jsonl`. Where a choice was made in a dialog, the quoted words are the label the owner picked, because the saved record words some of them differently.

### The two sessions beside nebari's

Both sessions ran on the evening of 13 October 2026 in the owner's time zone, which is 14 October in UTC. Every time below is UTC.

Nebari's figures are the ones `docs/PLAN.md` gives for its hand-run session (M5's introduction and T5.8's entry). Where the plan gives no figure the cell says "not recorded".

| | Nebari, by hand | Extend (afk's M6) | New project (todo) |
|---|---|---|---|
| Human turns | 35 | 4 typed, 1 dialog submitted, 1 dialog rejected | 6 typed, 3 dialogs submitted |
| Questions asked | 12 | 6, of which 4 were saved | 21, of which 14 were saved |
| Recommendations overridden | 2 of 8, and 1 reworded | 1 of 4, and 1 never answered as asked | 2 of 7, and 1 profile rule |
| Setup facts that had to be asked | not recorded | 0 | 6, and the profile check |
| Rework | the name, the deploy tool, storage three times, the component library, two addresses patched twice | none in the plan; the brief was left out of date | a made-up time, storage asked twice, a third round, two patches, one out-of-date record |
| Checker findings | no checker ran; the draft said 27 tasks and held 31 | 0 errors, 14 warnings, all older than M6 | 0 errors, 0 warnings |
| Critic findings | no critic ran; two spending caps contradicted each other | 0 | 1, fixed |
| Wall-clock time | first draft under 12 minutes; the whole session not recorded | 37 minutes 20 seconds, 27 of them with one dialog left open | 14 minutes |
| Tokens (weighted) | not recorded | 657,052 | 580,154 |

How the counts were made:

- **A turn** is one entry in the planning agent's transcript that came from the owner: a typed message, or a question dialog sent back. The command that started the session and the notices a finished sub-agent sends are not turns. A dialog holds up to three questions and is one turn.
- **The extend session** asked 4 questions in dialogs (all saved) and 2 in plain text when it showed the task breakdown (is each task the right size, are the blocking edges right). Those 2 were not saved.
- **The new session asked 21 questions and saved 14.** The 7 not saved were asked in plain text: 4 about what the words in the request mean, 2 with the task breakdown, and 1 offer to save a fact to the profile. Of the 14 saved, 7 were product questions and 7 were setup questions.
- **Wall-clock time** runs from the first entry of the transcript to the last: 00:39:24 to 01:16:44 for the extend session and 01:18:18 to 01:32:19 for the new one.

### The extend session and the new-project session

#### The extend session

**The request.** The session opened with "What should the next milestone deliver, and what made you want it?" The owner's first reply was a question, "What is available?", and the second was the request: "Ah, I know. I would like the afk tasks output to be available in the dashboard." That sentence was saved as the brief.

**Round one, 1 question.** The survey found that the dashboard already has this: the Build tab (T3.2) lists every task with its status. The session put that to the owner as a dialog, `r1-gap`, asking what should change, with "Try the Build tab first; no new milestone if it covers it" as the recommendation. The dialog opened at 00:42:16 and was rejected at 01:09:23, 27 minutes later. The owner then typed: "Ok nevermind, I didn't know that was there. Let's pivot. I've noticed that the project switcher only shows a single project. It looks like maybe we have two configuration sources now?" That message was saved as the answer to `r1-gap`.

**Round two, 3 questions, one dialog.** Which projects the switcher lists (`r2-switcher-source`), what happens to the old `config.json` (`r2-old-config`), and how much the milestone holds (`r2-scope`). The owner took the first two recommendations.

**Recommendations overridden: one.** For `r2-scope` the session recommended only the settings-file fix. The owner picked "Plus both", which adds pause and resume keys and an inbox count to the dashboard. That one choice doubled the milestone from two tasks to four.

**Setup facts asked: none.** The owner profile and the plan's Decisions table covered everything the milestone needed.

**The task breakdown** was shown once, with two plain questions, and the owner answered "1. Yes 2. Yes".

**What it produced.** Milestone M6, "One settings file, and more in the dashboard", with four tasks (T6.1 to T6.4), added by one `afk plan insert`, and two new rows in the Decisions table, added by one `afk plan patch`. Both commands succeeded first time and were committed and pushed. The plan went from 6 milestones and 30 tasks to 7 and 34.

**One thing the saved file does not show.** The dialog offered short labels ("Plus both", "Drop it, say so once") and `questions.jsonl` holds longer wordings of the same options ("This, plus both of those"). The meaning is the same, but the record is not word for word what the owner saw. The session edits that file by hand, which is filed below as `8szxye`.

#### The new-project session

**The request,** saved word for word as the brief: "We're going to build a simple todo list application, like the ones that are built as demos to show off the hot new framework of the week."

**Before the rounds, 4 plain questions** on what "todo" and "todo list" mean and on two edge cases. The owner answered all four in one message ("1. Correct 2. Just one list 3. Classic demo behavior 4. Gone for good"). The session wrote them into `CONTEXT.md`.

**Round one, 6 saved questions.** One dialog with three product questions: who it is for, what kind of interface, how much of the classic demo. Then three setup questions in plain text: whether any profile entry does not fit, the project's name, and where the repository is hosted.

**Round two, 6 saved questions.** One dialog with three product questions: whether to put it online, when another person's change shows up, and three small behaviours. Then three setup questions: how Postgres runs on the machine, how Ruby is installed and which version, and which project to use as a reference for interface components.

**Round three, 2 saved questions and one offer.** A one-question dialog asking which "local storage" the owner meant (`r3-storage`), then a plain question asking where the reference project is (`r3-nebari`), with an offer to save the Ruby answer to the profile.

**Recommendations overridden: two of seven.**

- `r1-features`: the session recommended the full classic set (edit, filters, a count, complete all). The owner picked "Bare minimum": add, complete, delete, clear completed.
- `r2-deploy`: the session recommended putting it online on the owner's homelab, which is the profile's default for a web app. The owner picked "Only my own machine".

One profile rule was overridden as well: the database (see the next section).

**Setup facts asked, and whether the profile could have held them.**

| Fact | Could the profile have held it? |
|---|---|
| The project's name | No, it belongs to one project. But the folder's name answered it. |
| Where the repository is | No. But the folder's `origin` remote answered it. |
| How Postgres runs on the machine | Yes. In the end it did not matter, because the owner dropped Postgres. |
| How Ruby is installed | Yes, and it does now: the session offered to save it, the owner said "Yes please run it", and `afk profile set` ran. |
| Which Ruby and Rails version | Yes. The owner's answer, "use the latest", reads like a standing preference. |
| The reference project for interface components | Yes. The profile says to look at similar projects and names none. |
| Where the reference project is | Yes. The profile already says projects live in `~/code`. |

The seventh saved setup question is the profile check itself, which is always asked.

Taken from the profile without asking: the framework, the test framework, the CI service, the design tool, the project location, the accounts rule and the "no component library" default.

**The task breakdown** was shown once with two plain questions, and the owner answered "1. Yes 2. Yes".

**What it produced.** A plan with two milestones and six tasks, zero open questions, and one owner task. Beside it: the glossary (`CONTEXT.md`), the agent instruction files, `afk.toml` and `.gitignore`. Then the design phase wrote `docs/design-brief.md`, added the owner's design task, T1.2 "Design system (owner)", with `afk plan insert`, and made T2.1 to T2.4 depend on it with four `afk plan depend` calls. The CI workflow is part of T1.1, not a task of its own.

**Two things the session decided itself, and said so.** The owner named the reference project for interface components and answered only where it was, not what to take from it. The session took how the interface is built, and also the reference project's browser-test driver, test-data library, lint rules and security scanner. It also decided that a completed todo can be marked open again. Both were shown as notes with the task breakdown and the owner confirmed the breakdown, so neither is a guess made in silence, but neither was a question.

### Where the owner had to correct a session

Each is marked as the owner choosing differently, which is the method working, or as a session fault.

- **Asking what was available (extend). Session fault.** The session asked what the next milestone should deliver and listed nothing. The owner had to ask: "What is available?" Only then did the session list the plan's deferred items. Cause: the extend prompt's first question named no candidates.
- **The round-one dialog that sat open 27 minutes (extend). Session fault.** The owner had asked for something that was already built, and the session told them so inside a question dialog. The only honest answer was to go and try the Build tab, and a dialog cannot wait for that. The owner rejected it after 27 minutes. Cause: the prompts had no rule for a request that is already delivered, so it was asked as an ordinary product question.
- **The pivot (extend). The owner choosing differently.** "Ok nevermind, I didn't know that was there. Let's pivot. I've noticed that the project switcher only shows a single project. It looks like maybe we have two configuration sources now?" The owner dropped the request and brought a new one. No session could have guessed it. The survey doing its job is what made it happen: it found the feature before any task was written for it.
- **The scope (extend). The owner choosing differently.** "Plus both", against a recommendation of the smallest milestone. One answer, no back and forth.
- **The features (new). The owner choosing differently.** "Bare minimum", against the full classic set.
- **The deploy (new). The owner choosing differently.** "Only my own machine", against the profile's default. This is a default doing what a default should: offered, and declined in one answer.
- **Postgres, accepted and then overridden (new). Session fault.** In round one the session showed the profile entries, among them "rule database: Postgres", and the owner said "Looks good". At that point nobody had yet asked whether the app would be online or what it would store. In round two the session asked how Postgres runs on the machine, and the owner answered: "No postgres, just use local storage, keep it simple". Cause: the profile check came before the answers the database rule depends on, so the owner approved a rule they were about to drop.
- **The storage question asked again (new). Partly a session fault.** "Local storage" could mean a file on the machine or the browser's own storage, so the session asked a third-round dialog and the owner picked "A SQLite file". Asking was right, because the two build very differently. But the question only existed because of the fault above, and it is the one kind of nebari's rework that came back.
- **The project's name and repository (new). Session fault.** The session asked both as open setup questions. The owner answered: "Todo - repo is todo, you're in it already and it's github origin is set" and "Github, it's all set up already". The folder was named `todo` and its `origin` remote pointed at the repository; the survey had read the folder but had not run `git remote -v`. Cause: the survey step did not say to read the remote or use the folder's name.
- **Where the reference project is (new). Session fault.** Round two asked which project to use as a reference ("Nebari"), and round three asked where it was ("It's in ~/code on this machine"). Cause: the first question did not ask for the location, so one fact cost a turn of its own.

No proposal was refused outright, nothing was interrupted, and neither task breakdown had to be shown twice.

### Rework and its cause

- **A made-up time, fixed by hand (new).** After the first dialog the session wrote `answered_at` as `2026-10-13T18:30:00Z`. The real time was `2026-10-14T01:22:54Z`. It noticed two seconds later and fixed the file with `sed`. Every later answer read the clock. Cause: the prompt showed an example time and did not say how to read the real one.
- **A third round (new).** Two follow-ups that should not have needed a round: which "local storage", and where the reference project is. Cause: the two faults above.
- **Two guarded patches for the critic's finding (new).** The plan's Routes table said T2.1 adds `GET /`, while T1.1's scope adds it and T1.1's acceptance needs it to answer. Two `afk plan patch` calls fixed the table and the rule above it, and the checker was clean afterwards. This is rework of the kind the method is for: found by a check before the owner read the plan, fixed by patches, no task entry rewritten.
- **A record that still says no profile entry was overridden (new).** The saved answer to the profile check, `r1-profile`, reads "Looks good. No entry is overridden." It was true when written and was never corrected after the owner dropped Postgres and the homelab deploy. The plan itself is right: its Decisions table records both departures from the profile. Only the session's own record is out of date.
- **The brief left describing a request the owner had dropped (extend).** `brief.md` still asks for the `afk tasks` output in the dashboard. The pivot is recorded only as the answer to `r1-gap` and in a note in `critic.md`. The plan says the brief is never edited, so the session followed its rules, and the rules left the critic with the wrong request.

Nothing was written to either plan and then written again. No `afk plan` command failed. `afk plan advance` ran once per phase in both sessions, apart from one run in the extend session made on purpose while a dialog was open, which reported that questions were still unanswered.

**Against nebari's rework.** Nebari's session guessed the project's name, guessed a deploy tool from a sibling project, decided storage three times, read a preference for a component library as a strict rule, and patched two machine addresses twice. None of those happened again. Here the name was asked (it should have been read from the folder, but it was not guessed), the deploy was asked as a product question with the profile's default as the recommendation, the component-library entry was shown as "a starting point, not a strict rule", and there were no addresses. The one exception is storage, which was asked twice in the new session: once as Postgres and once as which local storage.

### Checker and critic

- **afk's plan:** `afk plan lint` reports 0 errors and 14 warnings, with 7 milestones, 34 tasks, 1 owner task and 9 open questions.
- **The 14 warnings are older than M6.** The plan as it stood in the commit before M6 was added gives the same 14: 13 `missing-task-field` warnings on tasks from T0.0 to T4.2, all done and written before the format asked for a Goal line, and 1 `open-question-no-task` on the licence question. M6 added none.
- **The todo plan:** 0 errors and 0 warnings, with 2 milestones, 6 tasks, 1 owner task and 0 open questions. It was clean before the critic ran and after the two patches.
- **The critic on M6: zero findings, which says little.** The critic is given the plan and the brief and nothing else. The brief was the dropped request, so the critic judged M6 against a sentence M6 does not try to deliver. Its report says the brief "is delivered by T3.2's Build tab (already built, outside the scope of this review)". It did check M6 against the Decisions table and found nothing, and that part stands. Whether M6 covers what the owner asked for after the pivot was not checked by anyone but the owner.
- **The critic on the todo plan: one finding, and a real one.** The `GET /` conflict described above. Two tasks claiming one route would have had T2.1 breaking the plan's own rule against changing another task's route.
- **Counts came from code.** Both closing summaries were the output of `afk plan lint --summary`. The split of afk's warnings into 13 and 1 in `critic.md` was counted by hand from the checker's listing, and it is right.

### The daemon's first build

The task was **T1.1**, "Set up the Rails app and the CI workflow", in session `t1-1`. Times are from todo's decision log, its ledger and GitHub.

| Time | What happened |
|---|---|
| 01:34:04 | `afk plan approve` committed the plan (`e7ba222`); the session was marked approved at 01:34:09 |
| 01:34:12 | The daemon picked the project up and waited: usage was close to its limit |
| 01:40:17 | The usage window reset and T1.1 was launched |
| 01:51:17 | The build session finished and the task was handed to shipping |
| 01:51:22 | Parked: GitHub's command-line tool was not logged in (inbox `kc3c6m`) |
| 01:51:27 | T1.2 parked as the owner's task (inbox `r5tpxz`) |
| 02:01:15 | The owner answered `kc3c6m`: "Logged in now" |
| 02:05:52 | Pushed; the pull request was open at 02:06:07 |
| 02:06:13 | The first CI run started; it failed at 02:08:26 |
| 02:09:27 | The daemon handed the task back to its session with the failure |
| 02:10:06 | The session committed the fix (`eb16f69`) |
| 02:12:17 | The pull request was open again with the fix; the second CI run passed at 02:13:01 |
| 02:13:48 | Merged, as pull request 1 (`1a11813`) |
| 02:15:07 | T1.1 marked done |

- **How long.** 39 minutes 44 seconds from the approve commit to the merge, and 34 minutes 50 seconds from launch to done. Of that, 6 minutes were the wait for usage, 11 were the build, 10 were the park on the login, and about 12 were shipping with one failed CI run in the middle.
- **The wait for the usage window** was the daemon's own rule and needed nobody.
- **The park on the GitHub login** did need the owner. The stored login had gone invalid. The daemon stopped, said why in the inbox, and went on two seconds after the answer. The item told the owner to run `gh auth login`, while the tool's own message, quoted in the same item, said to run `gh auth refresh -h github.com`.
- **The failed first CI run.** The step "Prepare test database" failed because `db/schema.rb` did not exist. The app has no tables yet, so no schema file had ever been committed, and the build session's own test runs on the machine had not shown it. The daemon typed the failure into the session with the instruction to fix it and not to push, the session committed an empty schema, and the daemon put it on the pull request. Nobody was asked.
- **The merge.** All three checks passed (`scan_ruby`, `lint`, `test`). The log then records `deploy_healthy` although the project is not deployed anywhere; this session did not look into what that step checked.
- **T1.2 is parked on the owner by design.** It is the design task. The daemon started no agent for it and raised `r5tpxz`. T2.1 to T2.4 wait for it, so the build is idle until the owner makes the design system. That is the intended state, not a stall.

**What the owner did by hand, according to the files:**

1. Logged in to GitHub again and answered `kc3c6m`.
2. Typed one line, "push it", into the build session's terminal at 02:13:19. The fix was already on the pull request and its CI run had passed at 02:13:01, so the line changed nothing. Nothing in afk's code types that line into a session, so it was the owner.

**The owner's own answer** to this task's inbox item `u76qn2` was: "All done, no action needed on my part for the new project, everything ran smoothly." The files decide the facts (A6): two things were done by hand. Both were small, and it is fair to say the owner did not have to rescue anything. But "no action needed" is not what the files show, and the login park is 10 of the 40 minutes.

### Tokens

afk recorded nothing for either planning session. `afk tokens --project ~/code/afk master-plan` exits 1 and prints no figures. The numbers below were summed for this write-up from the planning agent's transcripts with the rule of the plan's section 4.7: each message counted once, taking the largest of each field. Weighted is `input + 5×output + 1.25×cache write + 0.1×cache read`.

| Session | Part | Calls | Input | Cache write | Cache read | Output | Weighted |
|---|---|---|---|---|---|---|---|
| Extend | main session | 28 | 66 | 113,475 | 2,827,620 | 20,130 | 525,322 |
| Extend | critic sub-agent | 5 | 132 | 61,694 | 98,212 | 8,932 | 131,731 |
| Extend | **total** | **33** | **198** | **175,169** | **2,925,832** | **29,062** | **657,052** |
| New | main session | 30 | 76 | 82,158 | 2,455,874 | 30,191 | 499,316 |
| New | look-up sub-agent | 6 | 12 | 20,619 | 53,927 | 3,181 | 47,083 |
| New | critic sub-agent | 2 | 34 | 11,517 | 4,595 | 3,773 | 33,755 |
| New | **total** | **38** | **122** | **114,294** | **2,514,396** | **37,145** | **580,154** |

Both sessions kept to "one session, one write": the only sub-agents were one critic each, on the reasoning-tier model, and one fact look-up in the new session that read the reference project.

T1.1, the task the daemon built, as `afk tokens --project ~/code/todo T1.1` prints it:

| Phase | Calls | Weighted | Share |
|---|---|---|---|
| plan | 12 | 457,216 | 33.9% |
| execute | 67 | 600,880 | 44.6% |
| review | 16 | 215,453 | 16.0% |
| ship | 3 | 74,425 | 5.5% |
| **Total** | **98** | **1,347,974** | 100% |

Its raw totals were 202 input, 432,298 cache write, 4,107,097 cache read and 79,338 output.

Planning the whole todo project cost less than half of building its first task. T1.1 cost about a twelfth of the plan's baseline of about 16M weighted per task, but it is a small set-up task in a tiny project, so that says nothing about the baseline.

Nebari's planning session has no token figure in the plan, so there is nothing to compare these with.

### Fixed or filed

**The nine prompt faults, fixed in `plugins/afk/skills/master-plan/prompts/`:**

1. **The extend session asked an open question with no candidates.** `modes.md`: the first question of an extend session now lists, in the same message, the entries of the plan's Deferred section and its open questions.
2. **"Read the master plan in full" cannot be done on a long plan.** `modes.md`: the extend survey now reads the header, the Decisions table, the milestone list and the sections and tasks the request touches, and says a long plan is not read line by line. The session had already done this against its instructions; the instructions now match.
3. **A request that is already built was put in a dialog.** `phases-early.md` and `modes.md`: when the survey finds the request is already built, the session says so in a plain message that names where it is and asks in free text what the owner wants instead. A question the owner cannot answer without trying something never goes in a dialog.
4. **The critic judged against a request the owner had dropped.** `critic.md`, `phases-late.md` and `modes.md`: the critic's prompt has a third slot holding the owner's own later words that replaced or widened the request, quoted exactly, or `none`. The session fills it from the saved answers. The brief itself stays unedited (A2).
5. **A made-up time.** `phases-early.md`: the time of an answer is read with `date -u +%FT%TZ`, never written from memory or from the example.
6. **The name and the repository were asked although the folder answered them.** `phases-early.md`: the survey runs `git remote -v` and notes the folder's name, and shows what it found for the owner to confirm with the profile entries instead of asking open questions.
7. **The reference project's location cost a round.** `phases-early.md`: a setup question that asks for a reference project asks where it is in the same question.
8. **A profile rule was confirmed before the answers it depends on.** `phases-early.md`: entries that depend on an answer still open are marked when the profile is shown and confirmed again once those answers are in. When a later answer overrides an entry, the saved answer to the profile check is corrected.
9. **The CI task asked the build session for something only afk can see.** T1.1's acceptance ended with "the pull request for this task shows the `CI` workflow's three jobs passing". A build session does not push, so T1.1's agent raised two inbox items (`rfsv6p`, `ygtwsp`) about a check it could not make. `phases-late.md`: the CI task's Acceptance now names the commands the workflow runs and says they pass in a fresh clone; it does not ask to see the workflow pass on a pull request, because afk checks that when it ships.

The plugin version went from 5.7.2 to 5.7.3 in both plugin files.

**The four code faults, filed as notices in afk's inbox and not fixed:**

- `9pxewe`: afk records no tokens for a planning session, so `afk tokens` has nothing for one.
- `yrww8c`: inbox items a task's agent raised stay open after the task is merged and done. Todo still shows `ygtwsp`, `rfsv6p` and `jfcu5y` from T1.1.
- `taa7na`: the park message for an invalid GitHub login says to run `gh auth login` where the tool itself asked for `gh auth refresh -h github.com`.
- `8szxye`: `afk plan` has no command to record an answer to a planning question, so the session edits `questions.jsonl` by hand. That is how a time was made up, and how saved options came to differ from the ones shown.

**The owner check:** `wnr9uy`, "Watch the next real afk plan session for the nine prompt fixes". It lists the nine and says they were not proven by a second run.

### What this does and does not show

What it shows:

- **Both plans pass the checker with zero errors.** Met.
- **The throwaway project has one task merged by the daemon.** Met: T1.1, pull request 1, about 40 minutes after the approve.
- **The write-up names every place the owner had to correct a session.** Ten are listed above, five of them session faults, four the owner choosing differently and one partly a fault.
- **The method's main claim held in these two runs.** Guessed setup facts caused almost all of nebari's rework. Here no setup fact was guessed, and the owner's turns fell from 35 to 9 in the session that planned a whole project.

What it does not show:

- **Two sessions is a small sample.** One owner, one evening, one machine.
- **The todo project is far smaller than nebari.** Six tasks against nebari's 31, no accounts, no hosting, no addresses, no file storage. Most of nebari's rework was in exactly the parts this project does not have, so 9 turns against 35 is not a like-for-like comparison.
- **The nine prompt fixes were not proven.** They were written from what went wrong and checked for size and presence. No session has run with them. The next real planning session is the test (`wnr9uy`).
- **The fresh-clone wording may not be enough by itself.** T1.1's acceptance already said "`bin/setup` succeeds on a fresh clone", and the missing schema file still reached CI.
- **The critic's worth is half shown.** It found a real fault in the todo plan. On M6 it was given the wrong request, so its clean report there is not evidence that M6 is right.
- **Not exercised by these two runs:** plan changes that go through a pull request (`[planning] via_pr`), a reconcile against a real design system (T1.2 is still the owner's to do), and a real `afk plan amend --yes`.
- **The session was not attached to either run.** Everything here is from the files, the transcripts and GitHub. What the owner thought or did away from the keyboard is not in them, beyond the one answer quoted above.

### Scope ideas

- The todo project's decision log records `deploy_healthy` although the project is not deployed anywhere. It was noticed and not looked into: it may be a deliberate pass for a project with no deploy check, or a misleading line. Filed as an inbox notice.

### For the owner to check

- Watch the next real `afk plan` session for the nine prompt fixes (inbox `wnr9uy`). None of them has been run by a real session yet.
- `make install` was not run by this session: the daemon's `after_merge` command installs the new afk and refreshes the plugin when this merges. Until then the installed afk still carries the old prompts.

## T6.2

Search folders move into `config.toml` and `config.json` is retired. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **A bad `[dash]` value never fails the load (A1).** The machine config has one loader, and the daemon, `afk project`, `afk usage`, `afk pause` and `afk inbox` all use it. The task says the daemon must not fail over a `[dash]` value, so the loader checks the table but hands a bad value back beside the settings (the dashboard's values fall back to their defaults) and does not refuse the file. A value of the wrong kind, such as a word where a number belongs, is handled the same way. A second loader just for the dashboard was the other way; it would read the file twice with two sets of rules.
- **The dashboard's reader keeps its place (A2).** The screen code still reads no file. It is handed the three search settings by the dashboard command, which now gets them from the machine config's loader.
- **What a bad `[dash]` value does in the dashboard (A3).** The folder search reports it in the notice it already uses for a search that failed, naming the key and the file, and the listed projects still show. The T6.1 amendment expected a bad `[dash]` value to hold the listed rows as a broken file does; that would need the load to fail, which A1 rules out.
- **A relative search folder (A4)** is still taken from the folder `afk dash` was started in, as before. The task says the keys keep their meanings.
- **How the notice is shown (A5).** The full text, with the lines to add, is printed in the terminal before the dashboard opens, so it can be copied after quitting. One short notice inside the dashboard says the old file is no longer read and where the lines are. A notice inside the dashboard cannot hold lines to copy, and text in the terminal alone is not seen until the dashboard is closed.
- **Where the old file is looked for (A6).** Only in the folder `config.toml` lives in. The old reader also accepted a relative `XDG_CONFIG_HOME`, which the machine config never did; a `config.json` reached only that way is not looked for.
- **What the lines to add hold (A7).** The keys the old file had, with folders as they were written. A value the new loader would refuse is left out and named. An old file that cannot be read still gets the notice, with an example of the three keys.
- **The search for `config.json` in the code (A8)** finds one line besides the notice and its tests: the plugin installer looks for OpenCode's own `config.json`. That is another program's file and stays.
- **Tests (A9)** are written with the code, as table tests. **Nothing had to be in place first (A10).**

### As built

- **A bad `[dash]` value resets the whole table to the defaults**, not only the bad key, so the dashboard never runs a search the owner did not write. `Config.DashErr` names the file, then the key (`dash.scan_interval`, `dash.max_depth`, `dash.search_roots[1]`), then what to write instead.
- **The `[dash]` table is decoded loosely** (an `any` field), so a value of the wrong kind, or `dash` not being a table, is a `DashErr` and not a failed load. Invalid TOML syntax still fails the load, as for the rest of the file.
- **The dashboard gets zero settings, not the defaults, when `DashErr` is set**, so no search can start; the empty state names `config.toml`. The scan ticker stays on its 60 second fallback, so a fixed file is picked up within a minute.
- **The notice is shown on every start of the dashboard**, including the hand-off after `afk new`. The terminal text tells the owner to put the keys under an existing `[dash]` line and not add a second one, because a second header would stop every command that reads the file.
- **The in-dashboard toast names the two files by name, not by path**, and is raised alone at start. The full path is in the terminal text. A failed look into the folder counts as no old file, so the notice is never shown without it.
- **The notice echoes only the three keys.** A JSON syntax error is reported as "not valid JSON" without the parser's message, which quotes part of the file.
- **A relative `XDG_CONFIG_HOME` is no longer honoured.** The old reader accepted it; the machine config follows the XDG rule (absolute paths only), so such a setup now uses `~/.config/afk`.

### Scope ideas

- A comment in `internal/uistate/uistate_test.go` (line 93) still says "config.Path's test", naming the package this task removed. It wants a one-line reword.
- The notice inside the dashboard borrows the folder search's notice category. It is always shown alone, so the borrowed label never appears, but a category of its own would be tidier.

### For the owner to check

- On the always-on machine, add a `[dash]` table with a real search folder to `config.toml`, open `afk dash` and press `p`: projects under that folder should be listed. If a `config.json` is still in afk's config folder, the notice should show once, and the lines it prints should load. The check here used a scratch config folder and throwaway projects.
- `make install` was not run by this session: the daemon's `after_merge` command installs the new afk when this merges.

## T6.3

Pause and resume the build from the dashboard. Decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **The keys (A1).** `P` pauses the build and `U` resumes it (un-pause). No other dashboard key is a capital letter, so neither collides with anything: `r` already restarts an agent, `p` opens the project switcher, and `u` and `d` scroll the overview. A capital also makes a pause hard to press by accident.
- **Where they work (A2).** From the session list and from the overview on the right, as `b` and `i` do. While the agent pane has the keyboard they are typing, like every other letter, and while the inbox or any other overlay is open they do nothing.
- **How the dashboard reaches the commands (A3, A5).** Two new hand-offs from the dashboard command to the screen code, one for each key. The screen code still cannot see the commands' own code, so it is handed a small result (was it already paused; was a pause or a stop cleared, and after which task) and words the notice from that. A failure's reason is worded where the failing step is known, beside the commands, with the sentences the web page already uses where they fit.
- **Which folder they act on (A4).** The project's main checkout, taken from the data the Build tab is showing. A dashboard started inside a session's worktree pauses the project, not the worktree.
- **A change that went through without its log line (A6)** is shown as a failure that says the change was made, and the Build tab is refreshed anyway.
- **The Build tab follows at once (A7, A8).** The result of either key reloads the Build tab's data without waiting for the next refresh, and a paused build's summary now names the key: "paused: U or afk resume starts it again".
- **Switching project while a key is still working (A9)** drops the late result, so a notice never describes another project.
- **Before the build data has loaded (A10)** the keys say there is no build, as `b` does.
- **Pausing a paused project (A11)** still adds a `pause` line to the decision log, because `afk pause` does. The task says the keys have the same effect as the commands, so the commands' code is not changed.
- **Tests (A12)** are written with the code, as table tests. **Nothing had to be in place first (A13).**
- **`make install` is not run by this session (A14).** The project's `afk.toml` installs after the merge. **The OpenCode plugin is not touched (A15).**

### As built

- **A dashboard with no pause hand-off** (only in tests) says pause and resume are not available there and names `afk pause` and `afk resume`, instead of failing.
- **No guard against a double press.** Two quick presses of `P` call the pause twice, which is the same as running `afk pause` twice: harmless, with a second `pause` line in the decision log. A guard would have to be released when a result is dropped after a project switch, a new way to get stuck for little gain.
- **The failure wording puts the reason first.** The web page's sentence starts with the long path of a temporary file, and the dashboard's notice shows about three lines, so the actual cause ("permission denied") was cut off in the check against a built binary. The dashboard now says the reason, then the folder to check (`dashPauseError` in `dash_pause.go`); the web page's words are unchanged. A change made without its log line keeps the web page's words, which already say the change was made.
- **A result that names a task but reports no stop** reads as "nothing changed": a task name alone does not mean the line was stopped.
- **Lower-case `p` and `u` keep their meanings**, with test rows that would catch a case-insensitive match.

### Checked, and not checked

- **Checked against a built binary** (a scratch build, never `~/bin`): in a throwaway repository with a small master plan, driven through tmux. `P` wrote `.afk/build/paused` and the Build tab read paused at once; `P` again said already paused; `U` removed the flag; `U` again said nothing changed; a stop marker written by hand and then `U` said the stop after T1.2 was cleared and removed it. The decision log had `pause`, `resume` and `stop_cleared` lines chosen by the owner, and no ledger was written. With `.afk/build` read-only, `P` showed "permission denied" and the folder. In a folder with no master plan both keys said there is no build. This checkout was untouched. `make check` was run in full, green. The review's hands-on check repeated the walk-through.

### For the owner to check

- With an agent pane focused, press `P` and `U` and see that the agent gets the letters and the build is not paused. The check here had no live agent.
- `make install` was not run by this session: the daemon's `after_merge` command installs the new afk when this merges.

### Scope ideas

- The README's Build tab paragraph says the keys work "from this tab"; they work from the list and the overview too.
- The doc comment on `resumeNotice` in `internal/ui/buildpause.go` has one unclear sentence.

## T6.4

T6.4 adds a count of the active project's inbox items to the dashboard's header. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The words (A1).** `inbox 3` when items are open, `inbox 3 · 2 blocking` when some of them hold a task up, and `i: inbox` beside them when there is room. One item reads the same way (`inbox 1 · 1 blocking`). With nothing open the header shows nothing new.
- **What the header gives up first (A2).** As the screen narrows: the switch-projects hint, then the inbox hint, then the awaiting names shorten, then the usage text shortens, then the open count goes (leaving `2 blocking`), then the afk badge, then the short usage text, then the awaiting line, then the blocking count, and the "stale" warning last. The blocking count outlasts the badge, the hint and the short usage because it is a task in this project waiting on the owner. The stale warning stays last, as in T3.2 (A10), because it says nothing else on the row can be trusted.
- **Where it sits (A3).** On the right, before the usage text. The open count is dim; the blocking count uses the same warning colour as the awaiting line.
- **The hint hides while the agent has the keyboard (A4).** There `i` is typed to the agent, so the hint would be wrong.
- **One order for every header (A5).** The header used to have two orders, one with a usage reading and one without. They are now one list in which a missing piece is simply skipped, and every header without inbox items looks exactly as before.
- **Tests and prerequisites (A6, A7).** Tests are written with the change and the saved screens updated. Nothing had to be in place first.

### Decided while building

- **One exception to the order.** With nothing on the right but the switch-projects hint (no usage reading, no open item), the awaiting names shorten before the hint goes, as they always have. Otherwise the order above applies as written. Without the exception, headers with no inbox items would have changed.
- **The last few cells.** Below about 10 columns, when nothing fits whole, the header cuts what is left in this order of preference: the stale warning, the awaiting line, the blocking count, the short usage text, the badge.
- **No new saved screen.** Three saved screens already have open items (the Build tab and the two inbox screens at 120 by 40). Only their first line changed. The narrow widths are covered by a table test.
- **"The agent has the keyboard"** is the dashboard's own test for when keys go to the agent (`paneOwnsKeyboard`).

### Checked, and not checked

- **Checked against a scratch build** (never `~/bin`), in a throwaway repository driven through tmux. With one blocking and one plain item the header read `inbox 2 · 1 blocking  i: inbox`. After the blocking item was answered in the overlay it read `inbox 1  i: inbox`. At 40 columns the hint was gone. At 30 columns only the badge and `1 blocking` were left. A repository with no items showed no inbox text. All three review checks passed.
- **Not checked: the hint beside a live agent.** No real agent ran. Owner check: focus an agent pane and see that `i: inbox` leaves the header while the count stays.

### The review

All three checks passed. These minor notes were left as they are:
- The planner and conductor edited this file and `CONTEXT.md` themselves, as they are meant to.
- The last-cells order puts the awaiting line ahead of the blocking count, the reverse of the main order. It was planned that way.
- One end-to-end host test, in code this change does not touch, timed out once under load and passed when run again.

## T7.1

T7.1 adds the leader key (ctrl+space) and the actions dialog to `afk dash`. These are the decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **How the leader is wired (A1).** ctrl+space is one more rule in the key registry, on the session list, the detail side (the agent placeholder included) and the agent pane. It opens a new dialog mode that sits above the palette. The overlays, forms and confirms do not have the rule, so the leader does nothing there without any extra check.
- **What the dialog lists (A2).** Every action of the session list and the detail side, each once, in the order `?` shows them. Moving around (cursor, paging, scrolling, changing tab) is not an action and is left out, as is the leader itself. Each row has its key, its name and one line of help; a test fails when an action has no help line, so a later action cannot be listed without one.
- **How a picked action runs (A3).** The dialog closes, then the action runs through the same code its own key uses. It is never typed to the agent, and there is no second copy of any action.
- **Keys inside the dialog (A4).** `esc` closes. ctrl+space closes and, when the agent pane had the keyboard, types ctrl+space to the agent. Up and down move the highlight and `enter` runs it. While nothing has been typed, a key that is an action's key runs that action; any other letter starts the name filter, and once a filter is typed every letter goes to it.
- **Dimmed actions (A5).** An action that cannot run where the owner is shows dimmed with the reason (no session selected, close the pane first, no build, and so on), taken from the same checks the action's own key makes. Its key and `enter` do nothing and the dialog stays open.
- **ctrl+space and ctrl+@ (A6).** In the ordinary terminal encoding ctrl+space, ctrl+@ and (on many terminals) ctrl+2 all send the same byte, and the terminal library reports every one of them as ctrl+space. The dashboard and the agents use none of them for anything else, so the task goes ahead with ctrl+space and does not pick another key. An inbox question asks the owner whether that is acceptable. If ctrl+space turned out not to reach the dashboard through tmux at all, the session would stop and ask instead.
- **Help and the palette (A7).** The leader appears in `?` and in the `:` palette, because both are built from the registry. The short help line at the bottom is already full and is not changed.
- **Tests (A8)** are written first for the pure parts (the key decision, the rows, the reasons), then for the dashboard model, with saved screens at 80 by 24 and 120 by 40. **Nothing had to be in place first (A9):** T6.3 and T6.4 are merged.

### How ctrl+space arrives (W3.T1, 2026-10-13)

Read in the module cache (ultraviolet `v0.0.0-20260811164956-006e29f97886`, x/vt `v0.0.0-20260713092006-0d683c34c74b`, bubbletea `v2.0.9`) and confirmed against a built binary.

- **The byte.** A terminal sends ctrl+space, ctrl+@ and (on many terminals) ctrl+2 as the NUL byte (0x00). Ultraviolet's `EventDecoder.parseControl` (`decoder.go`) turns NUL into `KeyPressEvent{Code: KeySpace, Mod: ModCtrl}`, unless the decoder's `Legacy` flags have `flagCtrlAt` set, in which case it returns `{Code: '@', Mod: ModCtrl}`. The legacy key table does the same in `key_table.go` (`buildKeysTable`: `nul := Key{Code: KeySpace, Mod: ModCtrl} // ctrl+@ or ctrl+space`, switched to `'@'` by the same flag), and the kitty map has `kittyKeyMap[ansi.NUL] = Key{Code: KeySpace, Mod: ModCtrl}`.
- **The flag is never set.** The only code that sets `flagCtrlAt` is `LegacyKeyEncoding.CtrlAt(true)` (`decoder.go`). No file in bubbletea `v2.0.9` mentions `Legacy`, `LegacyKeyEncoding` or `CtrlAt` (searched with grep), so the dashboard always gets `ctrl+space`. `Key.String()` in `key.go` writes `ctrl+` and then `space` for `KeySpace`, hence the binding string `"ctrl+space"`.
- **Why ctrl+@ cannot be told apart.** It is the same byte, so after decoding there is nothing left to compare. Both open the dialog; that is documented in the README.
- **Back to the agent.** The pane types ctrl+space to the session through x/vt `key.go`, where `KeyPressEvent{Code: KeySpace, Mod: ModCtrl}` appends `"\x00"`. The stand-in agent showed `^@` for it.
- **tmux.** `man tmux` mentions `C-Space` only as a key binding name (copy-mode `begin-selection`, emacs: `C-Space`), and nothing in it says tmux swallows the key when it is not the prefix. A real check settles it: in tmux 3.6, with the default prefix, `tmux send-keys C-Space` reached the dashboard as ctrl+space, and the dashboard's pane passed one NUL to the stand-in (`hello^@`). The dialog opened from the list and from the focused agent pane. Evidence: `.afk/t7-1/evidence/w3-t1-tmux.txt`. So the plan's stop condition (ctrl+space not reaching the dashboard through tmux) did not occur and ctrl+space stays the leader. A tmux whose prefix is set to C-Space would take the key itself; that is the owner's setting, not something the dashboard can see.
- **Checked end to end** with a scratch build in a throwaway repo (never `~/bin`): the dialog opens over the focused agent pane; `esc` closes it and the stand-in received nothing; `ctrl+space`, `i` ran the inbox action; `ctrl+space` twice left one `^@` at the stand-in; ctrl+space under the inbox overlay and the project switcher changed nothing and opened no dialog (`.afk/t7-1/evidence/w3-t1-tmux-overlay.txt`).

### Decided while building

- **The dialog's box is sized from the terminal.** The dialog draws itself to the terminal's size, so it needed no size function of its own in the layout code.
- **Inside the dialog, `ctrl+c`, `q` and `ctrl+g` are the dialog's rows** (quit and mouse), because the dialog mode has no rules of its own above it. Picking a list action from the agent pane moves the keyboard to the list first; `ctrl+t` and `ctrl+g` stay with the pane.
- **The palette test that lists every mode** (`TestColonIsClaimedOnlyByNormalMode` in `internal/ui/palette_test.go`) got one row for the new mode.

### For the owner to check

- Try ctrl+space in your own terminal (and through tmux if you use it) with an agent pane focused: the dialog should open, and ctrl+space twice should reach the agent.
- Try ctrl+space while the new-session form is open: nothing should happen. A test covers it; the hands-on check could not reach that state by hand.
- The inbox question about ctrl+@ and ctrl+2 also opening the dialog is waiting for your answer.

### Scope ideas

- Three rows show a key the dialog keeps for itself: `clear filter` (`esc`), `back to list` (`esc/ctrl+\`) and focus (`enter/f`). In the dialog `esc` closes and `enter` runs the highlighted row, so `clear filter` can only be run by highlighting it and pressing `enter`. T7.2 could relabel these rows.

## T7.2

T7.2 makes the leader key (ctrl+space) the one way to run a dashboard action. These are the decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **Where the dialog's rows come from (A1).** A leader table of its own, listing each action, its key after the leader and the side it runs on. The session list's and the overview's own key rules no longer carry the actions, so the dialog cannot read its rows from them any more; it runs a picked row through the same code the old key reached.
- **The keys (A2).** Each action keeps its letter where it is free; a chord becomes its letter. The whole table:

  | After ctrl+space | Action | Was |
  |---|---|---|
  | `n` | new session | `n` |
  | `o` | one-off session | `o` |
  | `p` | switch project | `p` |
  | `c` | clean session | `c` |
  | `r` | restart agent | `r` |
  | `l` | edit label | `l` |
  | `/` | filter sessions | `/` |
  | `x` | clear filter | `esc` (still works) |
  | `b` | build tasks | `b` |
  | `i` | inbox | `i` |
  | `P` | pause the build | `P` |
  | `U` | resume the build | `U` |
  | `f` | focus the right side | `f` (`enter` still works) |
  | `t` | agent or overview | `ctrl+t` |
  | `\` | close the pane (list); back to the list (right side, agent pane) | `ctrl+\` |
  | `g` | mouse to the terminal or back | `ctrl+g` |
  | `w` | resize the panes | `ctrl+w` |
  | `?` | help | `?` |
  | `q` | quit | `q` |

  The `:` palette is gone; the dialog's name filter does its job. Kept without the leader: up/k, down/j, pgup, pgdn, home, end, `enter`, `esc`, `tab`, `shift+tab`, the number keys, the overview's scrolling keys, and `ctrl+c`.
- **ctrl+c (A3).** Still quits from the session list and from the overview, so a stuck dashboard can always be left. In the agent pane it goes to the agent, as before.
- **Overlays and resizing (A4).** Keys inside an overlay, a form, a confirm or the resize mode do not change, ctrl+g included where those had it.
- **Help (A5).** `?` is ctrl+space `?`; `esc` also closes the full help on the list.
- **The notice for an old key (A6).** On the session list, each removed letter (`n o p c r l / b i q f ? P U`) and `:` does nothing but show one short notice naming its new key. Chords and keys on the overview get no notice: a chord in the agent pane now belongs to the agent.
- **How hints name a key (A7).** As `ctrl+space X`, for example `ctrl+space i: inbox`, from one helper so the hints cannot drift from the table.
- **Tests (A8)** are table tests for the leader table first, then the existing tests moved onto the leader; the saved screens are regenerated. **Nothing had to be in place first (A9):** T7.1 is merged.

### Decided while building

- **Each action runs where its old key ran it.** The leader table records whether an action belongs to the list, the right side or both. A list action picked from the overview or the agent pane moves the keyboard to the list first, as before. `b`, `i`, `P`, `U`, `r` and `w` run in place on the overview.
- **`\` means back to the list on the right side and close the pane on the list.** Back to the list comes first in the table, and the first row that can run wins. So from inside the agent pane, `\` never closes that pane.
- **`f` is no longer a bare alias of `enter`.** Focus is `enter` alone, and ctrl+space `f` is in the dialog.
- **ctrl+c still quits from inside the dialog**, as an unlisted second key on the quit row.
- **`esc` on the list closes the full help before it clears a filter.** The full help is drawn only on the list, so ctrl+space `?` from the right side moves to the list first.
- **The notices read** `n moved: press ctrl+space then n` for each removed letter, and `: moved: press ctrl+space, then type to filter the actions` for `:`.
- **The footer** shows the movement keys, `enter` and `ctrl+space actions`. While restart works on the selected row it also shows `ctrl+space r restart`, because a stuck agent is when someone reads the footer for a key. The full help lists the keys that still work without the leader, then one `ctrl+space X` entry per action that can run, then `esc close help`.
- **The header's switch hint is `ctrl+space p: projects`**, shorter than before. The longer wording pushed another header piece out at 80 columns, and the T6.4 drop order had to stay.
- **The agent placeholder** answers to `enter` and the leader only. Its toggle, back-to-list, restart and mouse actions run from the dialog, as they do over a live agent.
- **Overlay footers still say `ctrl+g release mouse`.** That key belongs to the overlays, which keep their keys (A4).

### For the owner to check

- Run `afk dash` in your own terminal against a real session with a real agent. Press ctrl+space then `n`, `enter`, ctrl+space `\`, ctrl+space `p`, ctrl+space `q`. Also check that ctrl+t in a focused pane does what you expect in Claude Code. The stand-in agent only showed that the byte arrives.

### Follow-ups

- Some doc comments in `internal/ui/model.go` still call ctrl+t the agent/overview toggle. It is ctrl+space `t` now.

## T7.3

T7.3 lets `afk dash` start a change session (extend, amend, reconcile) and open it in the pane. These are the decisions the master plan left open, made while planning the task (2026-10-13), numbered as in the work plan's assumptions.

- **One start path for the command and the dashboard (A1).** A function beside `afk plan` does everything the command does up to the point where it takes the terminal, and returns what happened (started, resumed, or refused with the reason) instead of printing it. The command uses the same code, so the two cannot drift, and `afk plan` prints and does exactly what it did before.
- **What "resumed" means (A2).** With a mode, `afk plan` never resumes: it refuses while a session is open. So the start function also takes a request with no mode that only opens a planning session that is already open: when its agent is running nothing is started, and when it is not, the agent is started again on the saved state and nothing is rewritten. With no open session that request is refused; it never starts a new plan, which is T7.4's job.
- **Refusals (A3)** use the command's own sentences, without the `afk plan amend:` at the front, built once so the terminal and the dashboard say the same thing.
- **The three actions have no key (A4).** The master plan says "by name only": in the actions dialog, type part of the name (extend, amend, reconcile) and press `enter`.
- **The form (A5).** Amend asks for one sentence and reconcile for a path inside the project, in a one-line form like the label form. Empty text keeps the form open and starts nothing.
- **An open session (A6)** is found by the start itself, after the form, and the dashboard then asks whether to open it; `y` opens it. Each action therefore calls the start once.
- **Which agent (A7).** The default one, as `afk plan` without `--provider`.
- **After a start (A8)** the dashboard selects the `master-plan` row and opens its pane, as `enter` does. A result that arrives after a project switch is dropped.
- **Tests (A9)** are table tests written with the code. **Nothing had to be in place first (A10):** T7.1 and T7.2 are merged.

### Decided while building

These are the choices the code settled differently from the assumptions above, or that the assumptions did not cover.

- **Reconcile is typed as `document`, not `reconcile` (corrects A4's example).** The leader's `r` is the restart key, so a typed `reconcile` restarts the selected session when it is stopped. `extend`, `amend` and `document` begin with no leader key, so they are safe. A4's text is kept as the planner wrote it; this line is the correction. The dashboard rows and README say `document`.
- **One start path, one result, no error return.** The entry point reports every outcome, a spawn failure included, as refused with the command's own sentence. The dashboard's seam has no error return, and `PlanRefused` is its zero value, so a result that is never filled opens nothing.
- **One text field for both questions.** Amend's sentence and reconcile's path share one `Text` field; the mode says which it is.
- **Resume looks for an open session first.** A damaged state still reaches the decision, so the owner sees the "fix state.json by hand" refusal rather than "nothing is open". An open session whose plan file is missing is resumed, as bare `afk plan` does. A finished session is refused as nothing open, so the plan-gone refusal never comes from the entry point.
- **A relative project folder is refused**, not resolved against the folder the dashboard was started in, which the owner did not choose.
- **Two kinds of refusal text.** Refusals before the decision (a project folder that is missing, a bad `afk.toml`) are the command's sentences exactly, so not every one ends in "Nothing was started.". The entry point's own refusals (blank text, unknown mode, relative folder, nothing open) do end that way. The missing-folder refusal still says "pass --project with a folder that exists", which reads oddly in the dashboard; it is the command's wording and was left as it is.
- **The terminal check is asked by the command only.** `lookAtPlanProject` takes the terminal check as a function, asked after the project is read, so `afk plan` asks it where it always did. The dashboard never asks it.
- **Two modal modes.** `inputPlanText` is the one-line form; `inputConfirmPlanOpen` is the open-it offer, where `y` opens and `n` or `esc` cancels. Stray keys do nothing while the offer is up.
- **The form captures its project when it opens.** A submit after a project switch is refused with a notice, so nothing starts in another project. Blank text is checked after trimming spaces, and the trimmed text is what is sent.
- **The pane opens before the row is listed.** The result's root is dialled at once, and `master-plan` is selected once it is listed. That selection is dropped when the keyboard goes back to the list, and on a project switch, so a later tick never moves the owner's cursor.
- **The success notice follows the connect.** It is raised after the dial, so it shows while the pane connects; the pane's arrival then words itself as every connect does.
- **Plan rows reach the list first.** From the agent pane the keyboard moves to the list before the form opens, and a successful start moves it to the planning pane.
- **Help lines are short** so each fits in 120 columns, and each action's name is the only row containing its own word, so a filter leaves exactly one row.
- **`internal/ui/views_test.go` was edited outside the task's Files list.** `TestLeaderChordNamesEveryTableKey` expected `ctrl+space <key>` for every table row. A by-name row has no key, so the test now expects the leader alone for it. That is the only change in that file, and the package cannot pass without it.
- **CONTEXT.md glossary.** "Leader key" and "Actions dialog" were already in CONTEXT.md from T7.1, so no term was added; the existing lines are kept as they are.
- **The start waits for the host to be reachable.** A started or resumed result is held until the planning host has written `host.json` (the same bounded wait `afk plan` uses before attaching). Without it the pane dialled before the host was ready and failed on the first try.

### For the owner to check

- Run `afk dash` in your own terminal on a project with a plan and a real agent: ctrl+space, type `extend`, `enter`; then, after that session is finished or cleaned, `amend` with a sentence and `document` with a path. The hands-on check used a stand-in agent only.

### Scope ideas

- Typing `reconcile` in the actions dialog sends its first `r` to the restart action, which restarts the selected session when it is stopped. The README says to type `document`. Renaming the row, or reading a by-name row's first letter as filter text, would remove the trap.
- A long refusal is cut off in the dashboard's toast (the reconcile "outside the project folder" reason, for example). Wrapping long notices, or showing the whole reason in a small dialog, would show the full sentence.

## T7.4

T7.4 adds the dashboard action that starts a new plan, as bare `afk plan` does. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **The row's name (A1).** The master plan calls it "plan a new project", but a name typed in the actions dialog is reached through its first letter, and `p` opens the project switcher (the trap T7.3 hit with `reconcile`). The row is named "start a new plan", by name only; `s` is not a leader key. Its help line uses the word "folder" so a filter on it finds this row alone.
- **A folder with a planning session open (A2)** is resumed, as bare `afk plan` resumes it: the dashboard switches to that project and opens the pane. No new plan is started over it.
- **A folder that already has a plan (A3)** is refused with a sentence that names the plan and points at the dialog's extend, amend and document actions, because the command's own sentence (run one of these in a terminal) is wrong in the dashboard. Every other refusal keeps the command's words, the missing-folder one included.
- **`~/` (A4)** is expanded by the entry point, with the home folder, the same way `--project` is. Any other relative path is still refused.
- **Keeping the project in the switcher (A5).** A project started from the dashboard is a separate source in the switcher's merged list for the rest of the run, compared by its resolved path. Nothing is written to `config.toml`.
- **The form's default (A6)** is the active project's path when the dashboard knows that project has no plan, and empty otherwise. The action works with no project open, because the folder is typed.
- **After a start (A7)** the project is added to the list, the dashboard switches to it as the switcher's `enter` does, and only then opens the `master-plan` pane, because a switch closes any pane. A start in the active project opens the pane without a switch.
- **Tests (A8)** are table tests written with the code. **Nothing had to be in place first (A9):** T7.3 is merged.

### Decided while building

- **The refusal for a folder with a plan** reads: "this folder already has a plan (<path>). To change it, use the dashboard's extend, amend or document actions: press ctrl+space, then type the name. Nothing was started."
- **`~` is expanded only as `~` or `~/...`.** `~user` stays relative and is refused, as any relative path is.
- **The resolved path.** The dashboard's adapter resolves the started project's root through symlinks for the new mode only, because the switcher compares resolved paths; if resolving fails the root is kept as it was.
- **No preview after the switch.** The switcher's `enter` arms a preview of the first row; the new-plan switch does not, because the planning pane is opened at once and the preview would fight the `master-plan` selection for the cursor. It does record the project as opened, as the switcher does.
- **A dashboard started in a linked worktree.** The start always reports the main checkout. When that is the start-up project's main checkout, it counts as the active project, so no second row appears and the dashboard does not switch away from the folder the owner opened.
- **Where started projects sit in the list.** They are merged after the listed projects and before the start-up row, are never marked as the start-up row, and are scanned for sessions like the others; a newly started folder is scanned at once so its row has counts.
- **Notices.** While starting: "Starting a planning session in <folder>…"; after: "Started a planning session for a new plan in <root>." Without the start seam the row is dimmed and its notice points at bare `afk plan` in a terminal.
- **The form refuses a project switch underneath it,** as the other forms do, because its default came from the project on screen.
- **The leader golden** shows the new row dimmed, because the golden frames are built with no start seam. Only the 120x40 frame changed.

### For the owner to check

- Run `afk dash` on a project with no plan and a real agent: ctrl+space, type `start a new plan`, `enter`. The folder field should already hold that project's path. The hands-on check used a stand-in agent and a project the dashboard could not yet tell had no plan, so the field was empty there.

## T7.5

T7.5 lets the dashboard's "start a new plan" form take a project name as well as a folder: a name is created under the owner profile's `project location`, made a git repository, and planned in. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Where the code lives (A1).** The decision (use this folder, create this folder, or refuse) and the create step are a new package, `internal/planfolder`. The decision is a pure function of the typed text, the profile's location and what is on disk. The dashboard reaches both through two new seams, so the screen never reads the profile or touches the disk itself.
- **What a name may be (A2).** Letters, digits, `.`, `_` and `-`, starting with a letter or digit, at most 100 characters. Anything else is refused with that list, so a name is never a hidden folder or something that looks like a command-line flag.
- **A location that is missing (A3).** If the profile's location is not a full path, or the folder it names does not exist, the name is refused and nothing is created. afk never makes the location folder itself.
- **A name that already exists (A4).** The dashboard says so and asks whether to plan in that folder, which then goes the way a typed path does (T7.4). A file of that name is refused with no offer.
- **A failure part-way (A5).** If the folder was made but `git init` failed, the dashboard says the folder is left on disk, removes nothing, and starts no planning session.
- **Paths (A6).** Text with a `/` or a `~` is a path and goes to T7.4's start unchanged, so the words for a missing or relative folder stay T7.4's.
- **Two steps (A7).** Creating the folder and starting the planning session are separate steps in the dashboard, so T7.6 can ask its GitHub question between them.
- **Tests (A8)** are table tests written with the code. **Nothing had to be in place first (A9):** T7.4 is merged.

### Decided while building

- **Constant names.** The decision's kinds are `KindUse`, `KindCreate` and `KindRefuse` (refuse is the zero value), because `Create` is already the create step's function in the same package.
- **A path is anything with `/` or `~`.** Even a lone `~` goes to T7.4's start unchanged; only a bare word is treated as a name, and it is trimmed first.
- **A broken profile is explained, not called unset.** A profile that fails to load is reported with a pointer to `afk profile check`; a missing profile file simply means no location is set.
- **One notice after a create.** "Made <path> and made it a git repository. Starting a planning session in it…" is one notice, since two in a row would hide the first. Create and start are still two separate steps, which is where T7.6 goes.
- **A create that finishes after a project switch** is not dropped: it says the folder was made and that no planning session was started. A `y` given after a switch creates nothing.
- **An empty path is a refusal.** A create or exists decision without a path never opens a confirm.
- **The second form line** ("A name is made into a folder under your project location, after a yes.") shows only when the folder seam is wired, so a dashboard without it promises nothing.

## T7.6

T7.6 has the dashboard offer a private GitHub repository straight after T7.5 makes a new project's folder. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Where the code lives (A1).** The decision to ask (or why not), the wording of every outcome and the create step are a new package, `internal/ghrepo`, behind its own small `gh` and `git` interfaces with fakes. `internal/ship`'s `gh` interface is left alone, because its fakes would all have to change.
- **How the repository is made (A2).** afk runs `gh repo create <account>/<folder> --private` and nothing else, then sets `origin` itself with `git remote add`. Nothing is pushed.
- **Whether to ask (A3).** After the folder is made, and off the screen, afk checks that `gh` is installed, logged in and can say which account it uses. If any of these fails, the question is not asked and a notice says why and what to run.
- **Keeping the outcome on screen (A4).** The outcome line is shown at the start of the notice that appears when the planning pane opens, because that notice would otherwise replace it.
- **Failures (A5).** A name GitHub already has is reported as taken; any other failure shows `gh`'s own first line. If the repository was made but `origin` could not be set, the notice gives the `git remote add` command. The remote address follows `gh`'s own setting (ssh or https). Planning starts in every case.
- **Tests (A6)** are table tests written with the code. **Nothing had to be in place first (A7):** T7.5 is merged.

### Decided while building

- **The checks stop at the first that fails.** `afk dash` looks `gh` up on the PATH, then runs `gh auth status`, then asks for the account name, and runs a later check only when the earlier one passed. A missing `gh` is never run, and a logged-out one is never asked for its account, so the notice always names the first thing to fix.
- **Each `gh` and `git` call has the shipping step's limits.** They run through the same runner the build loop ships with: at most 30 seconds each, with no keyboard, and with `gh`'s prompts turned off, so a `gh` that wants to ask something fails with its reason instead of waiting behind the dashboard's screen.
- **The outcome line stays once the planning pane opens.** Checked against a scratch build through tmux (2026-10-14), the line was first only on screen until the planning pane connected. The pane's arrival notice ("input goes to master-plan") then replaced it, because a notice keeps no history. The dashboard now keeps the line for the master-plan pane the start opened and puts it before that pane's arrival notice, or before its failure notice if the pane does not connect. It is said once: an arrival for another session, or a project switch, drops it, so a later open of master-plan by hand is not prefixed with an old outcome. The arrival's own words follow it with a capital first letter ("… Nothing was pushed. Input goes to master-plan · …").
- **After a lead, the interim notice leaves out the folder.** The toast is cut at three lines of 48 cells. The yes line and the not-logged-in notice each take about two of them, so "Starting a planning session in <path>…" would push the path off, or with a long `gh` reason cut the reason. With a lead it reads "Starting the planning session…"; the folder is named again in the started notice and the pane's title. The not-installed notice is longer than three lines on its own and is still cut at the end; shortening it belongs in `internal/ghrepo`.
- **A repository with no commits has no branches yet, not a failed listing.** A folder made by name has no commits, so `git for-each-ref` lists nothing and no trunk can be found. The dashboard read that empty list as a failed branch listing and raised "branch listing failed: could not determine a trunk branch" a few seconds after the planning pane opened, over the outcome line. A listing that worked now always reports a list, even an empty one, so only a real `git` failure raises that notice (T7.5's behaviour is otherwise unchanged).

### Scope ideas

- **A shorter "gh not installed" notice.** The notice in `internal/ghrepo` is about 190 characters, longer than the toast's three lines, so its end is cut. Something like "No GitHub repository offered: gh is not installed. Get it from https://cli.github.com, then run `gh auth login`." would fit. Left for a later change.

## T7.7

T7.7 has the dashboard show what `afk plan status` and `afk plan lint` print for the active project. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Its own seam (A1).** The planning status and the checker's result come through a new `ui.Deps.LoadPlanStatus`, read on the dashboard's refresh with its own guard, not as part of the Build tab's snapshot. The `master-plan` session's overview needs the status even when the project has no plan yet, and so no Build tab.
- **The dashboard's own types (A2).** `internal/ui` gets its own plain types for the status, the counts and the findings, filled in by the root package, as it already does for starting a plan.
- **The plan line (A3).** One short line, worded by one function: "no planning session", "extend · rounds · 2 questions have no answer yet", "plan waiting for your approval" for a new plan at review, "amend · finished · the changes are in the plan", "plan approved", or "the planning state file is damaged: …". Other phases show the mode, the phase and what the session waits for. The Build tab leaves the line out when there is no planning session.
- **Reaching "check the plan" (A4).** The action is in the dialog by name. Typing "check" would run clean, because `c` is clean's key, so its help line holds a word no other row has. The work plan chose "warnings", but `w` is resize's key, so the word is "errors": type `errors` and press enter (see "Decided while building"). With no plan it says there is no plan to check, rather than being dimmed.
- **The overlay (A5).** It works like the inbox overlay: it shows what was read when it opened, scrolls, and `esc` closes it. It is not a confirm, so it does not count as a modal.
- **The wording of findings (A6).** Each finding is written as `afk plan lint` writes it, with the plan's path relative to the project, errors first and the plan's order kept within each level, under a line of counts.
- **Tests (A7)** are table tests written before the code. **Nothing had to be in place first (A8):** T7.1 is merged.

### Decided while building

- **The typed word is `errors`, not `warnings` (changes A4).** In the actions dialog the first typed letter runs any row whose key it is, and `w` is the resize key, so `warnings` cannot be typed from an empty box. The row is reached by typing `errors`; its help line holds both words and tests keep both unique to this row.
- **Two extra cases when the action runs.** If the dashboard is run without the reader wired, it says so and points at `afk plan lint`. If reading the plan failed, the notice gives the reason instead of "no plan to check at ".
- **The overlay is not refreshed while open.** A moving list would shift the line being read, and nothing in it can be acted on; reopening shows the latest reading. Findings wrap rather than truncate so the message and rule stay visible.
- **A new plan with a lint error shows phase `check`, not `review`**, because the status uses the same error count as `afk plan status`.

### Verification for the owner

- **Look at the plan line in your own terminal.** Open `afk dash` on a project with an open planning session and go to the Build tab. Check that the `plan:` line ends cleanly before the right border: a tmux capture of the scratch build showed tab characters there, which is probably how the screen was redrawn and not text in the line.

### Scope ideas

- **Singular counts in the plan check.** The overlay's counts line says "1 owner tasks" and "1 open questions". It should use the singular for a count of one, as its "1 error" line already does.

## T7.8

T7.8 lets the owner approve a plan from `afk dash`, with the same effect as `afk plan approve`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One code path for both (A1).** A new entry point, `approvePlanForSurface` in `plan_approve_surface.go`, runs the gate and the steps and returns what happened as data: approved, already approved, refused, no project, or which step failed with what was done and what is left. It also keeps the lines the command prints, in order and with the stream each goes to, so `afk plan approve` prints from it and its output does not change by a byte. The web page (T8.11) can call it as it is.
- **Who words the result (A2).** A plain function in the dashboard's adapter (`dash_approve.go`) turns the result into one notice. The screen code only shows the text, so it still never imports the command's package.
- **The dialog row (A3).** "approve the plan", by name: type `approve`, since `a` is no row's key. It is dimmed, with the reason, unless a new plan is at `review`: no project, status not read yet, no planning session, a damaged state file, already approved, a change session, or another phase.
- **The confirm (A4).** It shows the project, the plan's path and the checker's counts. With one or more errors it has no yes, and says to fix them first and how to see them (`errors`). It is a tenth modal mode.
- **Running it (A5).** `y` closes the confirm, shows a working notice and runs the steps once, away from the screen drawing, because they commit and push. A second `y` while it runs does nothing. The result is shown as a notice and the plan line is read again at once.
- **Long errors (A6).** A notice carries only the first line of what git said, and points at `afk plan approve --project <folder>` for the rest.
- **Tests (A7)** are table tests written with the code; the command's existing tests stay as they are and guard its output. **Nothing had to be in place first (A8):** T7.7 is merged.

### Decided while building

- **Lines are printed after the run, not as each step finishes.** `afk plan approve` now replays the lines `approvePlanForSurface` returns once the run ends. The bytes, their order within each stream and the exit codes are unchanged. Only the timing differs: a run killed half way (during a slow push) prints nothing for the steps it did. Accepted because the run is one bounded push at most, and a re-run reports and finishes what is left.
- **Errors remove the confirm's yes through the mode code, not a second mode.** While the plan has errors, `Model.modes()` swaps the confirm for one without the yes key, so the footer and the dialog hint never offer a key that does nothing. The handler also refuses on errors, as a second guard. A separate "approve with errors" mode was rejected: it would add another modal and another precedence row for one dialog.
- **The row's reason is one function.** The dimmed reason for **approve the plan** comes from one function that both the dialog row and the action read. An action run without the dialog gets the same sentence, "The plan cannot be approved: <reason>."
- **A second approve, or a project switch under an open confirm, does nothing.** While an approve is out, opening the action says "The plan is already being approved. Wait for it to finish." and calls nothing. A `y` under a confirm after a project switch approves nothing and says so, following the GitHub question's rule.
- **Notices carry the first line of a reason, and push notes appear only when a push did not happen.** A refusal shows its first line and "Nothing was changed.", since a notice has no room for the hint line. A failed notice says to approve again here or run `afk plan approve --project <root>`. The push note is added for via-PR, no origin, no branch and a failed push, and says how to push by hand.

### Verification for the owner

- **Approve a real plan from your own dashboard.** The scratch run approved a throwaway repository with no origin. On a project with an origin, check that the notice says the commit was pushed and that a running `afk daemon` starts the first ready task.

### Scope ideas

- **Long results are cut off by the notice.** On a 140-column terminal the approved notice ended at "No afk daemon seems to be running."; the start-the-daemon hint and any push note were cut. Showing the full result in a dialog, or on the Build tab's plan line, would keep it readable.
- **The web page sentence in the README** ("there is no button to approve … or from `afk dash`") reads a little awkwardly; T8.11 will rewrite it when the page gains approving.

## T7.9

T7.9 adds the all-projects screen to `afk dash`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Where the rows are built (A1, A2).** A new pure package, `internal/projectrows`, turns one project's facts into one row. It reads no file and imports neither the web page nor the dashboard, so the web page's overview (T8.5) can use the same rows later. The dashboard's adapter in the root package reads the files and fills in the facts, using `web.Summarize` for the current task and `buildview.OpenCounts` for the inbox counts.
- **The phase of a running task (A3).** For a project with a running task, the adapter lists its sessions the way the web page does (git worktrees and files, never a connection to a session). If that fails the phase reads "unknown".
- **One state per row (A4).** The first that applies wins: gone, can't read, no plan, stopped, paused, waiting on you, held for usage, running, finished, nothing ready. The longer line under the state uses the words `afk daemon status` prints. "Plan waiting for approval" and the inbox counts are shown beside the state, not instead of it.
- **What comes first (A5).** A row needs you when its folder is gone, it cannot be read, its line is stopped, it waits on you, an inbox item holds a task up, or its plan waits for approval. Those rows come first; within each group the order is the order of `config.toml`, with the project the dashboard was started in last when it is not listed.
- **When the files are read (A6).** Through a new `ui.Deps.LoadAllProjects`, only while the screen is open: once when it opens and then on every refresh tick, away from the screen drawing.
- **The screen (A7, A8).** It takes the place of the session list and the detail side until `esc`. It is opened from the actions dialog as "all projects", key `A` after the leader (lower-case `a` stays free, so typing `approve` still works). Over the screen the dialog runs only pause, resume, all projects, mouse and quit; the rest are dimmed with "Close the all-projects screen first (esc)."
- **enter, pause and resume (A9, A10).** `enter` switches to the selected project. On a gone or unreadable row it does not switch and says why. Pause and resume act on the selected row's project, not the active one, and say which project they acted on.
- **Narrow terminals (A11).** Below 100 columns each project takes two lines; long text is cut, not wrapped.
- **No projects, or a broken config.toml (A12).** With nothing listed the screen shows the active project and how to add one with `afk project add`. A `config.toml` that cannot be read keeps the rows already shown and says why, naming the file.
- **One source for the words (A13).** The daemon's state words and the stopped line move into `internal/projectrows`; `afk daemon status` uses them from there and prints exactly what it printed before.
- **Tests (A14)** are table tests for the row builder and its order first, then the dashboard model and saved screens, then the adapter. **Nothing had to be in place first (A15):** T7.1 to T7.8 are merged.

### Decided while building

- **The daemon's owner kind is `owner_task`.** The plan named it `owner`; the row builder uses the string the daemon writes.
- **Wording the plan left open.** A project with no master plan reads "This project has no master plan to build."; a finished one "Every task is done."; nothing ready shows the daemon's own words for what it last did, or "Nothing is ready to start." when it has not looked yet.
- **Pause and resume over the screen are never dimmed.** They act on the selected row and refuse with a notice: a gone row names `afk project remove`, a row with no plan gets T6.3's "no build to pause" sentence with the project's name in front.
- **Opening the screen starts empty.** It shows "Reading the projects…" until the first read lands, rather than rows from an earlier opening that could show a stopped project as running.
- **`enter` on the active project's row closes the screen**, as picking it in the switcher does; a row the switcher does not list yet says to try again in a moment.
- **The cursor stops at the ends** instead of wrapping, so a held key stays on the rows that need you.
- **An inbox folder that cannot be listed makes the row "can't read"**; a single unreadable inbox file is skipped here, as the Build tab counts those.

## T7.10

T7.10 lets the owner add and remove projects from the all-projects screen of `afk dash`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One code path (A1).** The bodies of `afk project add` and `afk project remove` move into two entry points that return what happened (added, already listed, removed, not listed, refused, failed) and the command's own words, without printing. The commands print those words, so their output and exit codes do not change, and the dashboard shows the same sentences.
- **What adding refuses (A2).** Exactly what the command refuses: a folder that does not exist, a folder that is not a git repository, a name already used by another project, and a `config.toml` that cannot be read or written. The command does not check for a master plan, so neither does the dashboard; a project with no plan already shows as such on the screen.
- **Keys (A3, A4).** `ctrl+space +` adds a project and `ctrl+space -` removes the selected one. "remove" could not be typed by name, because `r` is restart's key. Both work only while the all-projects screen is open; elsewhere they are dimmed and say to open it first. Remove is dimmed on the row of a project that is not in `config.toml`.
- **The add form (A5, A6, A8).** Two one-line steps: the folder, then an optional name (empty means the folder's name). The folder starts filled in with the active project when it is not listed yet. `~` and `~/` expand to the home folder; any other relative path is refused, as the new-plan form does.
- **How remove finds the entry (A7).** The dashboard passes the row's folder; the adapter finds the matching `[[project]]` in `config.toml` and removes it by its listed name.
- **After a change (A9).** A short notice, then the screen's rows and the project switcher's list are read again, so the row appears or goes.
- **Tests (A10)** are table tests written with the code. **Nothing had to be in place first (A11):** T7.9 is merged.

### Decided while building

- **Failed versus refused.** `addProjectToConfig` now marks a config file it could not find, read, create or write, so the dashboard can say the file is the problem (naming it) rather than the request. What it prints, and what `afk plan approve` does with it, did not change.
- **A literal `+` is a key, not a chord.** The leader table's checks treated any key containing `+` as a chord like `ctrl+c`; a key that is exactly `+` is now allowed.
- **Removing the last project** leaves `config.toml` as an empty file rather than deleting it, as `afk project remove` always has.
- **Remove on the unlisted start-up row** is dimmed in the dialog, so `ctrl+space -` there does nothing but show the dialog with the reason.

## T7.11

T7.11 puts a cursor on the Build tab of `afk dash` and lets the owner set the selected task's status, with the same effect as `afk ledger set`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One code path (A1).** The body of `afk ledger set` after its argument parsing moves into an entry point that returns what happened (changed, already, refused, failed), each task's real old and new status read inside the ledger's lock, and the command's own words, without printing. The command prints those words, so its output and exit codes do not change, and the dashboard shows the same sentences.
- **Key (A2).** `ctrl+space S`, "set this task's status". Lower-case `s` is not used, because typing it starts the filter for "start a new plan". The action works only on the Build tab while the detail side has the keyboard and a task is selected; elsewhere it is dimmed with the reason.
- **The cursor (A3, A4, A5).** The detail side's movement keys (up and down, `j` and `k`, half page, page, home and end) move a cursor on the Build tab's task rows, and still scroll on the other tabs. The cursor stays on the same task when the list is read again; if that task has left the list, it lands on the row now in the same place. It starts on the task being built, or the first row. It is drawn as `›` in a two-column margin only while the detail side has the keyboard, so the tab looks as before when the session list has it.
- **Choosing a status (A6, A7).** A list of the seven statuses the command accepts, with the current one marked. Choosing the current status writes nothing. Choosing `superseded` asks next for the ids of the tasks that replace it, which the command then checks against the plan.
- **The confirm (A8).** One line saying what changes (`T2.3: failed → pending.`) and what follows from it (`The build loop may start it on its next pass.`). When the task is running or shipping, the confirm first says an agent is working on it and that changing the status does not stop it. Only `y` writes.
- **After a change (A9).** A short notice in the command's words, then the Build tab is read again, so the row shows the new status. One change at a time.
- **Modes (A10), tests (A11), prerequisites (A12).** Three new dialog steps (the list, the replacing ids, the confirm). Table tests are written with the code. Nothing had to be in place first: T7.1 is merged.

Decided while building (T7.11):

- **Words-only rules split out.** `ledgerSetRefusal` holds the checks that depend only on the words typed, so `afk ledger set` still reports the same mistake first when a command has two, and the dashboard's entry point runs the same checks and cannot skip them.
- **The cursor remembers its task by id.** `landBuildCursor` takes the build's current task as an argument, so it stays a pure function. The marker is `›`, not the session list's `▌` rail: the rail means the keyboard is on the left, and the Build cursor only shows with the keyboard on the right.
- **The cursor stays drawn behind the dialogs.** Its gate is "right side focused and detail drawn", not the derived input mode, so the marked row is visible behind the status picker.
- **Where the offset is decided.** `revealBuildCursor` runs at the end of `syncDetailViewport`, so every path that syncs keeps the cursor row on screen.
- **Leader row.** `S` sits after `U` and is a right-side row (the cursor that names the task only exists with the right side focused). Blocked reasons come in this order: no seam, not on the Build tab with the right side focused, no task.
- **Every way out says nothing changed.** Esc on the picker, on the replacing-ids step and on the confirm all say "Nothing was changed.". A `y` after a project switch warns and writes nothing; a stale result is dropped.
- **The adapter.** `dashSetTaskStatus` (`dash_ledger.go`) parses the status itself, passes one id through `setLedgerStatusForSurface` and hands the command's `Text` back unchanged, with no `afk ledger set:` prefix and no usage line.

## T7.12

T7.12 shows the selected Build tab task's token use in `afk dash`, as `afk tokens <task>` prints it. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One code path (A1).** The task form of `afk tokens` moves into an entry point that reads the token ledger and returns the record (recorded, nothing recorded, cannot be read) in the command's own lines, without printing. The command prints those lines, so its output and exit codes do not change, and the dashboard shows the same table.
- **A narrower table when the window is narrow (A2).** The overlay shows the command's table when it fits. When it does not, it shows the same rows with only phase, role, model, calls, weighted and share, so nothing has to scroll sideways and the words and units stay the command's.
- **The short total (A3).** The task's row ends with its weighted total, shortened (`2.9M weighted`): plain below 1000, then `k` and `M` with one decimal. It is the first thing dropped as the tab narrows, before the row's detail and title.
- **Key (A4).** The leader, then upper-case `T`, "show this task's tokens". Lower-case `t` already switches the right side between agent and overview. It works only on the Build tab with the right side focused and a task selected, like `S`.
- **When the dashboard reads (A5, A6).** It reads only the token ledger, on each refresh and when the cursor moves to another task, never on the screen-drawing path and never a session's transcript. A record is kept with the task it belongs to, so the overlay never shows another task's numbers; opening before the first read has landed waits for it.
- **Tests and prerequisites (A7, A8).** Table tests are written with the code. Nothing had to be in place first: T7.11 is merged.

Decided while building (T7.12):

- **`Short` picks its unit after rounding.** 999950 reads `1.0M`, never `1000.0k`; below 1000 it is a whole number, as in the table.
- **No ledger and nothing for the task are the same answer.** The overlay says `no tokens recorded for this task yet` for both. The command keeps its own sentence (`has nothing for <task>`) in both cases, as before.
- **One record is held, the cursor's.** Only the cursor row shows a total, so the dashboard keeps one record keyed by task id. A read that lands for a task the cursor has left is dropped and the cursor's task is read again at once, so moving and pressing `T` does not wait for the next refresh.
- **The total is shown only on a whole row.** The cursor row gets its total only when the id, title and detail all show uncut and the total still fits, so it goes first as the tab narrows and the detail-then-title narrowing is unchanged. With the 120-column test layout the detail pane is too narrow for it; it shows from about 135 columns with short titles.
- **The overlay box is as wide as its widest line (at least 40 cells).** It shows the command's table when that fits the terminal less the modal margins, else the narrower table, chosen again on resize and never in the drawing path. Its keys are the plan check overlay's.
- **A pending open is dropped quietly.** Pressing `T` before the record is held opens the overlay when that task's record arrives, unless the cursor, tab or project moved meanwhile. No "reading" notice is shown.
- **Unreadable ledger.** The overlay shows the reader's own sentence (`reading ledger <path>: …`), wrapped, and the row has no total.

Verification for the owner (T7.12):

- At 80 columns in a real terminal, open the tokens overlay (leader, then `T`) on the Build tab and check the row and the box's border draw cleanly. The hands-on check's tmux capture showed stray tab cells and a shifted border after a resize, most likely tmux redrawing.

## T7.13

T7.13 adds a dashboard action that takes a fresh usage reading, as `afk usage --refresh` does, and shows it in the header. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One code path (A1).** The probe-and-save step of `afk usage` moves into an entry point that takes a reading, saves it and returns it (or the reason there is none) without printing. The command calls it and keeps its reuse window, output and exit codes; the dashboard calls the same code.
- **Which agent is read (A2).** The default one, as `afk usage --refresh` reads when no `--provider` is given.
- **What a failure says (A3).** Four plain cases: the agent's command-line tool was not found; this agent cannot report its usage; usage could not be read (check the tool is installed and logged in); the reading could not be saved. afk does not run a separate login check to tell "logged out" apart, because the command does not either.
- **Only `usage.json` is written (A4).** That is what `--refresh` writes: the reading plus the governor's decision for it. `governor.json` is the daemon's record of its own probes and is left alone, although the master plan's background line mentions it.
- **Key (A5).** The leader, then upper-case `R`, "refresh usage", from the session list or the overview. Lower-case `r` is restart, and a free lower-case letter would run the row from an empty query instead of starting a filter. It is dimmed over the all-projects screen, like the other project actions.
- **A reading that lands after a project switch (A6)** is kept and its notice shown, because usage belongs to the machine, not the project. The header reload it triggers still belongs to whichever project is open.
- **Tests and prerequisites (A7, A8).** Table tests are written with the code. Nothing had to be in place first: T7.1 is merged.

Decided while building (T7.13):

- **The result keeps the raw error and the sentence apart.** The shared entry point returns the error as it was, so `afk usage` prints its own stderr lines unchanged, and a separate plain sentence for the dashboard. The dashboard never sees the command's `afk usage:` prefix or the provider's name.
- **The governor decision is judged before the probe and stamped after it.** The decision is made on the new reading, judged at the time the probe started (as `afk usage` always did), and stamped with the time after the probe, so it is never dated before the reading it came from. The unsupported case keeps its decision in the command, which prints it and stores nothing, so the shared entry point has no printing in it.
- **An empty result sentence is never shown as a success.** If the root package hands back an outcome with no sentence, the dashboard says it could not tell whether usage was read and points at `afk usage`, as the task-status notice does. An outcome it does not know is shown as information, not as a success.
- **One reading at a time.** A second press while a reading is running calls nothing and says so. The running reading also counts as work in progress for the dashboard, so the screen does not treat the program as idle while the agent's command-line tool is being asked.
- **The header catches up on the next tick if a build read is in flight.** The reload after a reading goes through the header's own build read, which skips a second read while one is running. If a tick's build read is still going when the reading lands, the header shows the new numbers on the next tick (at most 2 seconds later) rather than at once. Nothing is lost: the reading is saved either way.
- **The reading is shown from the file, not from the result.** The result carries only an outcome and a sentence, never the numbers. The header re-reads `usage.json`, so it cannot show a reading that was not saved, and a failed reading leaves the old one on screen.

## T8.2

T8.2 gives the web page live updates: static files built into the binary, a server-sent events stream fed by one shared reader, and hand-written JavaScript that swaps the changed parts of a page in place. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions. This amends `## T3.1` A6: the page now has JavaScript, but only from its own static files, never inline, and every page still works by reloading with JavaScript off.

- **The stream (A1).** `GET /events`, behind the same token as every page and not repeated under `/api`. Each change is one event of type `change` whose data is `{"project":"<name>","part":"<part>"}`. The parts are `inbox`, `ledger`, `pause`, `sessions`, `planning` and `usage`; `usage` belongs to the machine, so its project is empty. The first read after a stream opens only sets the starting point and sends nothing.
- **Static files (A2).** Served at `/static/<hash>/<file>`, where the hash is the first 12 hex characters of the file's SHA-256, with a year-long immutable cache. Any other hash, or an unknown file, is not found. The policy on every answer is `default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'`; the `'self'` for styles leaves room for the stylesheet T8.5 adds.
- **What each part reads (A3).** `inbox`: the project's inbox items. `ledger`: `.afk/build/ledger.json`. `pause`: the pause flag, the stop marker and `daemon.json`, together the build's run state. `sessions`: the project's sessions as the page lists them. `planning`: `.afk/plan/state.json`, `.afk/plan/questions.jsonl` and the `master-plan` session's `host.json` and `status.json` (T8.3 can add the conversation). `usage`: `usage.json`. A file that cannot be read counts as what was read, so a file turning unreadable is a change and staying unreadable is not.
- **How the page updates (A4).** Each live region of a page carries `data-live` (its parts) and `data-project`. Changes that arrive together become one fetch of the current page, and each matching region is replaced by the same region of the fresh page and marked `is-changed` once. A region holding the keyboard focus or a form is never replaced. After a lost stream comes back, every region is fetched once, because events may have been missed.
- **Slow browsers (A5).** Each stream has a queue of 16 events. The reader never waits for a browser: a full queue closes that stream, and the browser reconnects. Each write has 5 seconds. At most 32 streams are open at once; one more is refused.
- **One reader on the dashboard's tick (A6).** One reader reads every project every 2 seconds while at least one stream is open, and stops when the last one closes, so ten tabs cost one read. The interval is not a setting. A comment line is sent about every 24 seconds so proxies keep the connection.
- **Stopping the daemon (A7).** Open streams end at once when the daemon stops, and the stream is not cut by the server's 30-second write limit.
- **Tests and prerequisites (A8, A9).** Table tests are written with the code. Nothing had to be in place first.
- **Proved in a browser (W3.T1).** Against a scratch build and headless Chrome, an inbox item added with `afk inbox add` showed on the open project page about 1.3 seconds later with no reload, and the bar read "Live". With JavaScript off the item showed only after a reload and the page had no errors. The page refetches every region when its stream first opens, not only after a reconnect, because the reader's first read is a starting point and a change landing between the page load and the second read would otherwise be missed. The plan page has no live region: it shows no ledger state.

Decided while building (T8.2):

- **The project page's pause and resume button is not live.** It sits outside the swapped summary, so after a pause from elsewhere it can offer the old action until a reload; the pause and resume handlers decide what really happens. T8.5 or T8.6 may move it into a live region.
- **A refused 33rd stream gets the ordinary error page** (503, HTML), not an event stream. `/api/events` is not found.
- **The keep-alive line travels in each stream's queue** like a change, so there is no second timer; it shares the 16 slots.
- **Planning file paths.** `state.json` comes from `internal/plansession`; the other three are written out in `internal/web`, because importing `internal/session` for one name would pull the host packages into the web page.

## T8.3

T8.3 reads a project's open planning session as a chat, `GET /api/p/<project>/plan/chat`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **How the conversation is found (A1).** No conversation id is recorded. The agent's id exists only once it is running, and handing it one at launch would change how every planning host is started, the dashboard's relaunch included. Instead the provider looks in its own transcript folder for the project's main checkout and takes the newest conversation that began no more than a minute before the planning host started, ran in that checkout, and opened with the planning command. The command check keeps the owner's own agent session in the same checkout out of the chat. A conversation cleared inside the planning session no longer opens with the command, so the chat keeps showing the earlier one; that is accepted.
- **One decision for the page and the daemon (A2).** `internal/planchat` holds the pure function that turns the conversation into messages and decides whether the session waits, so T8.13's notifications use the same answer as the page.
- **What the address returns (A3, A4).** `{"project","state","messages","questions","problem"}`. `state` is `none`, `working`, `waiting_for_text`, `waiting_for_choice` or `unknown`. A question call can ask several questions at once, so a message and the pending state carry a list; an option is recommended when its label says "(recommended)". A conversation that cannot be read is answered with 200, state `unknown` and a plain `problem`, as the plan view shows its problems.
- **What shows (A5).** The agent's and the owner's text, questions with their options, and answers as the owner's messages. Other tool calls, the agent's thinking, tool results, the session's own housekeeping (skill text, the planning command, background-task notices, interrupt markers) and sub-agent work are left out.
- **When it waits for typed text (A6).** When its last turn ended and nothing it started in the background is still running; a planning agent that is waiting on its own researchers or critic is working, not waiting. An unanswered question means it waits for a choice.
- **Only a live host is read (A7).** The conversation is read only when the planning host's `host.json` exists and its process answers signal 0; otherwise the state is `none`. A host that started less than two minutes ago with no conversation yet is `working`; later the state is `unknown` with a plain reason.
- **No page yet, not live yet (A8, A9).** The address is served only under `/api`, like `/events`; T8.12 adds the chat page and adds the conversation to the `planning` live part.
- **Tests and prerequisites (A10, A11).** Table tests over hand-written transcripts are written with the code. Nothing had to be in place first.

Decided while building (T8.3):

- **Fixtures are not under a `.claude/` folder.** The owner's global git ignore leaves out `.claude/`, so a fixture laid out as a fake home would never be committed. Each case in `internal/provider/testdata/plan-chat/` holds the transcript folder itself, and the test copies it into a temporary home.
- **The folder name for a checkout** replaces every character outside letters and digits with `-`, as seen on this machine. It is unchecked for very long or non-ASCII paths; if the agent CLI shortens those, the chat says it cannot find the conversation rather than showing the wrong one.
- **Only an answer clears a question.** Text the owner types while a question is open, with no answer recorded for it, still reads as waiting for a choice. A declined question writes an answer, so this is rare; T8.4 should confirm it against the real agent.
- **One turn end per turn.** A turn's two end lines become one end, so a duplicate end line never counts twice.
- **Proved against a built binary (W4.T1).** With a stand-in planning host and a fixture transcript in a scratch home, the address returned `waiting_for_choice` with the messages and both questions' options; after the stand-in stopped it returned `none`; with no token it answered 401.

## T8.4

T8.4 sends the owner's reply into a project's open planning session, `POST /api/p/<project>/plan/chat`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **What the page sends (A1).** JSON only: either `{"text": "..."}` or `{"choices": [["label"], ...]}`, one list of option labels for each question the agent is waiting on, in order. Labels rather than positions, so a page showing an old question cannot quietly pick the wrong option.
- **Text and choices are not interchangeable (A2).** Typed text is sent only when the agent waits for text, and a choice only when it waits for a choice. Anything else is refused and nothing is typed, because text typed into an open question dialog would move its selection in ways nobody can predict.
- **Where the pieces live (A3).** The refusal and the keys for typed text are pure code beside T8.3's reading of the chat; the send is a one-shot sibling of the daemon's existing "type into a session" call; the keys for a choice come from the agent's provider, because only the provider knows its dialog.
- **Several questions at once (A4).** All of a dialog's questions are answered in one reply. A kind of question the agent's dialog cannot be answered for reliably by keys is refused with a sentence saying to answer it in `afk dash`.
- **Short waits and plain answers (A5).** The page waits at most two seconds to reach the session and five to type into it. A refusal answers 409 (nothing open, not waiting, the wrong kind of reply, not supported) or 400 (a bad body or an option that was not offered); a session that cannot be reached answers 502.
- **The dashboard's notice (A6).** When a reply takes the session from an open dashboard pane, the dashboard says the session was taken over by another window or the web page and that enter reopens it. It cannot tell which took it, so it does not claim it was the page.
- **The dialog's keys are found, not guessed (A7).** They are not documented, so they are found by trying the real agent in a throwaway folder and recorded here. If no reliable way is found, the work stops and the owner gets an inbox item.
- **Tests and prerequisites (A8, A9).** Table tests are written with the code. Nothing had to be in place first.

Decided while building (T8.4):

- **The dialog's keys (W1.T1).** Found on `claude` 2.1.299 on 2026-10-14, in a plain throwaway folder with `--model haiku`; the CLI draws the dialog, so the model does not change the keys. A single-select question takes its option's digit, which answers at once. With several questions, a single-select digit moves to the next tab by itself. A multi-select digit only ticks its option, so each chosen option is sent once and then Tab moves on. A review screen (more than one question, or any multi-select) is submitted with `1`. Every key is its own write, 300 ms after the last. A digit past the options does nothing; the rows after them are "Type something." and "Chat about this". The full record is `.afk/t8-4/keystrokes.md`.
- **The shapes the reply can answer.** Questions with 1 to 4 options: one or more single-select questions, and multi-select questions with one or more choices. A question with more than 4 options is refused as unsupported (409, and the sentence points at `afk dash`). Also refused, with nothing sent: an empty choice list for any question, the same option twice, and a choice list that does not match the questions. The trial did not cover an empty multi-select, so no keys were invented for one.
- **A paste into an open dialog (T8.3's note).** Confirmed in part. A bracketed paste sent to an open single-select dialog is ignored, nothing is written, and the question stays open; a following Enter then answers with the highlighted row. So text the owner types does not clear an open question, as T8.3 said. Whether the transcript records a paste into a dialog was not checked, because without the hook below the open question is not in the transcript at all. Through afk, typed text is refused while the page shows the agent waiting for a choice, so a paste plus Enter does not reach an open dialog.
- **The live check (W4.T1, `.afk/t8-4/live-check.md`).** Three attempts. Attempt 1, with the plugin's hooks as they are: a choice POST was refused 409 `not_waiting` and nothing was typed, because the open question was not in the transcript. Attempt 3, with a `PreToolUse` hook for `AskUserQuestion` added to the throwaway folder's `.claude/settings.json`: the same POST answered 200 `{"sent":"choice"}`, the dialog recorded "Python", and the next read showed `working`. Two short trials separated the cause: the hook, not the agent's text before the question. So `claude` 2.1.299 writes the open question to the transcript only when a `PreToolUse` hook for that tool is registered. This is seen on one CLI version and is not documented.
- **Real sessions (W6.T1).** The plugin now registers a no-op `PreToolUse` hook for `AskUserQuestion` (`hooks/on-pre-tool-use.sh`, plugin 5.7.4). Its registration is what makes the CLI write the open question to the transcript before the dialog opens, so a choice in a real planning session is no longer refused. Chosen over a sidecar written by the hook because it needs no schema or Go change; if a later CLI stops flushing, the sidecar (hook writes the pending questions, `PostToolUse` clears them, schema first) is the fallback. The OpenCode lag note records that the port has no such hook.
- **Checked through the plugin itself (W7.T1).** With `claude` 2.1.299 started with `--plugin-dir` pointing at this repo's `plugins/afk` and no hand-written settings, the open question's `tool_use` was on disk 32 seconds after the dialog opened, before any answer, and the option's digit then answered it. Recorded in `.afk/t8-4/live-check.md`, "Attempt 4".
- **Status codes (W2.T2).** `no_session`, `not_waiting`, `wrong_kind` and `unsupported` answer 409. Every other refusal reason, including `bad_reply` and `not_offered`, answers 400. A host that cannot be reached answers 502. A refusal that is wrapped still maps to its status.
- **Known gaps, left for a later task (Go source, outside this one).** A 409 body carries `error` but not the refusal's `reason` code, so a page script cannot branch on the reason. This is for T8.12 or a follow-up.
- **The taken-over sentence (W1.T4).** The notice and the pane's placeholder share one sentence. It says the session was taken over by another window or the web page, and that enter reopens it. It does not name which one took it, because the host's `Stolen` frame does not say.

## T8.5

T8.5 gives every page of the daemon's web page the design system's look, and makes the home page the all-projects screen built from the same rows the dashboard uses. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **The rows come from the daemon's own list (A1).** `web.Deps.ProjectRows` is filled in `daemon_web.go` by `webProjectRows`, from the daemon's last good project list, through the per-project loader taken out of `dash_allprojects.go` (`buildListedRows`). The web page never reads `config.toml`. The web path passes no git common-dir lookup, because the daemon's list already holds the main checkouts.
- **`rows` is a new JSON field (A2).** `GET /api/` gains `rows`, one entry per project in the home page's order, with 13 keys: name, state, label, detail, task, title, phase, done, total, open, blocking, plan_waiting and needs_owner. The folder, the config order and whether the project is listed are left out, as the existing project summary leaves the folder out. `projects`, `reviews` and `usage` are unchanged.
- **A review-only plan is a row, not a new state (A8).** A plan on the review list whose project has no row becomes a Needs you row, "Plan waiting for approval", linking to `/p/{name}/plan`, placed after the project rows that need the owner (those rows have a config order to sort by and this one does not). It is not in `rows`, because projectrows has no such state, and inventing one in the JSON would be a state no other surface uses. A configured project with a waiting plan shows its state badge and a second badge, "Plan waiting for approval". No live part is added for `plan-reviews.json`; that is a scope idea below.
- **Layout data is a wrapper (A4).** `renderPage` executes the layout with `pageData{View, Chrome}`. Page templates still get their own view as `.`, so the page templates and the JSON views did not change. `chromeFor` decides each page's bars, as a pure function with a table test. `retryPath` decides the error page's Try again. The error page stands alone, with no bars and no live script.
- **Styles move to two sheets, and the CSP string is unchanged (A3).** The inline styles in the layout, the page templates and the token-refused page moved to static files. `afk.css` is the design system's reference sheet as shipped, minus the canvas frame and the canvas-only states. `page.css` beside it holds this page's own rules, written with `afk.css`'s tokens. Both are served under hashed addresses through `{{asset}}`. The policy still reads `style-src 'self' 'unsafe-inline'`. Why: tightening it now risks the rendered markdown of the plan page, which nothing here proved free of inline style, and it would also break later tasks of this plan that are not yet done. The scope idea below tightens it once those are settled.
- **Two sheets are served without the token (W2.T1).** The token-refused page must show the design, so its two sheets, `afk.css` and `page.css`, are the only static files served without the token (`isRefusedPageStyle` in `auth.go`, for GET and HEAD only). Every other static address still answers 401, including a stale hash of those two. The sheets hold only looks and ship in the open-source binary. The refused page is fixed text built once in `New`. It echoes nothing from the request, says how to open the page with the token, and has no Sign in button.
- **Bars and the live indicator (A6 and the chrome).** The phone bar has the wordmark on the home page and otherwise Back, the title and the live indicator. The desktop top bar has Projects with its count, Usage and the live indicator. Projects is `aria-current="page"` only on `/`, and Usage only on `/usage`; under a project, Projects gets `aria-current="true"`, so the screen reader does not say "page" on a link to the home page while the owner is under a project. Its count is the quiet grey one, since orange means "needs you".
- **Live indicator: three states (A6).** "Live" while the stream is up, "Working" while it is up and any region on the page is marked `data-working="true"`, and "Can't reach afk, retrying" when the stream drops. A lost stream is never an error page; the page keeps what it had. The open-stream state is read in `live.js`, which drives both bars' indicators.
- **Confirm sheet and the page without JavaScript (A5).** Pause and Resume open one shared native `<dialog>` (`confirm-sheet`, in `parts.html`). Only the project page draws it, so the plan page still has no form or button (its test scans the whole page). `confirm.js` opens the dialog from a link with `data-confirm`. The link's address is `GET /p/{project}/confirm/{pause|resume}`, a page that works with JavaScript off and shows the same words from `confirmSheetFor`. The safe choice is a `method="dialog"` form, so it closes the dialog with no script. Any other action in that address gets the same 404 page as an unknown project.
- **The toast reads a whitelist (A5).** After a change the browser goes back to the project page with `?done=paused` or `?done=resumed`. `toastTexts` maps only those two codes to fixed sentences, with the project's name taken from the server's own list, so a made-up code shows nothing and is never printed. The code is chosen from the POST address's last part, never from the body. The toast hides itself after five seconds.
- **Working comes from `data-working` (A6).** A region marks work in progress with `data-working="true"`, which a project card or row sets while its task is running. live.js looks again after each swap, so the indicator follows the swapped-in parts.
- **Desktop selection (A7).** At 760 px and wider, the home page's right column holds the selected project's card (`id="selected-{name}"`, with the same live parts as its row). The selection is the first row, so the one that needs the owner most. Below 760 px the card is hidden and the selected row drops its highlight, so the phone list does not look as if one row were picked. Rows link to `/p/{name}` at both widths. Only the home page widens its column (to 72 rem).
- **Progress bar without inline style.** A native `<progress class="progress__track">` styled in page.css draws the bar, with the words beside it. Its `aria-label` carries the count, and the visible words are hidden from screen readers so they are not read twice. There is no `style=` and no per-percent class. With no tasks at all no bar is drawn, since `max="0"` is not valid HTML.
- **Lists announce updates politely (W4.T1).** `aria-live="polite"` sits on each list's container, which stays in place, and not on the rows, which live.js replaces. A replaced live region is not reliably announced. The design names no live region for the home page; this follows its rule that a region which changes gets one.
- **Badges for the states the design does not name (W4.T1).** Gone and Can't read use the danger badge, because the owner must fix them. No plan uses the idle badge. An unknown state is drawn as a plain badge with its own word, so a newer projectrows still reads.
- **Titles.** The home page's title and its phone bar's title are "All projects", not "Overview", to match the dashboard's screen.
- **Test strategy and prerequisites (A9, A10).** Table tests and handler tests were written with each task, and `make check` ran at review. T8.1, T8.2 and T7.9 were merged before this work started, so nothing had to be put in place first.

### Scope ideas

- Tighten the policy to `style-src 'self'` (drop `'unsafe-inline'`) once every template and the plan page's rendered markdown are proved free of inline style (A3).
- A live part for `plan-reviews.json`, so a plan that enters or leaves the review list shows without a reload (A8). Today it shows on the next reload or the next change of another part.
- A live part or a page-level region for the home page's list, so a row that starts or stops needing the owner moves between Needs you and Everything else as it changes. Today a row changes in place only, and the heading count and the desktop selection wait for a reload (W4.T1).
- Skip the swap when a live region's fresh HTML equals the old, so a screen reader does not read every row again when the stream connects (W4.T1).
- In `docs/design-system.md` §2, scope the link rule as `.afk a:not([class])` and add `body { margin: 0 }`; `page.css` works around both today (W2.T1, W5.T2).
- The plan page draws two `h1` elements (its own heading and the plan's `# Title`); shift rendered markdown headings down one level (W5.T2).
- The no-JavaScript confirm page for pause still shows on a project that is already paused (and resume on a running one); it could send the owner back to the project page instead (review, W3.T1).

## T8.6

T8.6 rebuilds the web page's project page, the inbox list, an inbox item and its answer form, and the pause and resume buttons from the design system, and makes pause and resume ask first on the server too. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **How a POST proves the confirm (A1, A2).** The confirm sheet's form and the no-JavaScript confirm page carry a hidden field `confirm=yes`; a JSON caller sends `{"confirm": true}`. A pause or resume POST without it, on the page or under `/api`, answers 400 with a plain sentence and changes nothing. The same handler serves both, so the rule is the same everywhere.
- **The inbox is its own screen (A3).** `GET /p/<project>/inbox` is now a page instead of a jump to the project page; its JSON twin is unchanged. An item's Back link goes there, and an answer returns there.
- **No full reload (A4).** A small script built into the binary posts the answer form and the confirm sheet's form with `fetch`, follows the server's redirect and swaps in the page's shell (the bars, the main content and the toast). A network error, a non-2xx answer or a non-HTML answer before the server has answered falls back to a normal form submit. A page that cannot be swapped in after an answer is loaded by its address with a GET, so a post is never sent twice. With JavaScript off the page works by reloading, as before.
- **Toasts say only what happened (A5).** After an answer the inbox shows "Answer sent.", "Answer sent. <task> is ready again." only when the task is parked and no other open item holds it, or "Marked as done." for an owner task. The codes are a whitelist and the task id is read on the server, never from the request.
- **Owner tasks (A6).** An item is an owner task when its kind is `external_action`, it blocks, it names a task and its title starts with the daemon's owner prefix. It shows its instruction and one Mark as done button, which records the answer `done`, the answer `afk inbox answer` uses.
- **Option buttons (A7).** An item's options are buttons, the recommended one first and marked. With JavaScript off a tap sends that option; with it on a tap selects it and Send answer stays disabled until something is picked or written. Typed text still wins over a chosen option.
- **The project screen (A8, A9, A10, A11).** A state card (badge, "Updated … ago", a headline in words, one line of detail, progress, the one next action), Running now, Pause or Resume, a Plan row, links to the inbox, the plan and usage (the other screens arrive with their tasks), and the folder and repository. The repository is read from the project's `origin` remote with a short timeout, and any user name and password in it are dropped. These fields are page-only; the JSON API is unchanged. The "Change the plan" rows wait for T8.14.
- **Test strategy and prerequisites (A12, A13).** Table and handler tests with each task, the scripts checked in a real headless browser at 390 px (answer, pause and resume each shown without a reload; a pause POST without the confirm answered 400 and wrote nothing), and axe-core with the WCAG 2.2 AA rules found no violation on the project, inbox and item screens. T8.5 was merged first, so nothing else had to be in place.

Verification for the owner: not checked on a real phone or behind the owner's proxy. Open the project page from the phone, answer an inbox item, then Pause and Resume through the sheet, and see each change without reloading.

Decided while building:

- **The confirm is read from the body only.** `?confirm=yes` in the address is refused, so a link someone was sent cannot pause a project (tested in W1.T1).
- **The project's headline and next action.** Open blocking items come first, then paused or stopped, then a plan waiting for approval. The headline says "question" only when every blocking item is a question, and counts tasks ("hold up 2 tasks").
- **An unreadable planning state is never a session.** "Planning open" needs the planning state read and the session unfinished; otherwise the Plan row says "Read the plan", with a note when the state could not be read. The Plan row shows "Plan waiting" only from the project's own row, so without that row it says "Read the plan".
- **Usage has no count in the nav-list.** Usage events name no project, so a figure there would go stale on a project page.
- **Repository addresses.** An address with a scheme loses its whole user part, since on https a lone user is often a token. An scp-like address keeps its plain `git@` login. An address that will not parse is cut at its last `@`.
- **Swap the shell, not `<main>`.** The app bar's title and Back link change with the page, and they sit outside `<main>`. Focus moves to the new `<main>` after a swap, and the toast's words go in after a short delay so a screen reader announces them.
- **"Updated N s ago" counts from when the page first saw the time.** The server's clock and the phone's need not agree, so the age is not taken from the `datetime`.
- **A toast names only a task the server read.** `{task}` comes from the inbox item the answer named; an item not in the inbox's own read gets no toast.
- **A network error before any answer comes back re-posts natively.** The server refuses a second answer to an item, and pause and resume are idempotent, so a repeat is harmless.

## T8.7

T8.7 gives the web page a Tasks screen: a project's master-plan tasks with their status, each task's tokens, and a way to set a task's status behind a confirm, with the same effect and words as `afk ledger set` and `afk tokens <task>`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One set of words for the status confirm (A1).** The sentences the dashboard shows when setting a task's status (what changes, what follows, and the warning when an agent is working on the task) move into a small shared package, `internal/taskstatus`, that both the dashboard and the web page use. The web page cannot use the dashboard's code directly, and two copies would drift apart.
- **Token totals in a form a page can draw (A2).** `internal/tokens` gains a summary (the total, each phase, each role) built on the same counting as `afk tokens <task>`, so the page's split bar and role table agree with the command. The phases are the token ledger's own: plan, execute, review, fix, ship.
- **Milestones (A3).** A task belongs to the nearest `### M<n> — Title` heading above it, the same rule the plan checker uses to count milestones. Tasks above any milestone heading, or a plan that cannot be read, are shown in one section called "Tasks".
- **The filter (A4).** Open, Done and All are links (`?show=open`, `done`, `all`), so they work without JavaScript and survive live updates. Open is the default. Done includes superseded tasks, since their work is finished elsewhere. A milestone's "N of M done" leaves superseded tasks out, as the progress bar on the project page does.
- **Addresses (A5).** `GET /p/<project>/tasks`, `GET /p/<project>/tasks/<id>` and `POST /p/<project>/tasks/<id>/status`, each also under `/api`. `GET /p/<project>/tasks/<id>/status` is the page used to confirm without JavaScript.
- **The status sheet (A6).** Every status `afk ledger set` accepts can be chosen; none is disabled, because the plan asks for the command's full set (the design system suggested disabling the ones afk sets itself; the plan wins on behaviour). The sentence and the button ("Mark done") follow the choice. Without JavaScript, choosing a status and confirming it are two steps on their own page. Like pause and resume, the server refuses a status change that does not carry the confirm.
- **The split bar without inline style (A7).** Segment widths are whole-percent classes in `page.css`, rounded so they add up to 100, because the page's templates carry no `style=`.
- **Tokens are not live (A8).** The tokens card is read when the task's page loads. The token ledger only changes when a task ends, so a live part for it is left as a scope idea.
- **Tests and prerequisites (A9, A10).** Table and handler tests are written with each part. T8.5, T7.11 and T7.12 were merged first, so nothing else had to be in place.

Decided while building (T8.7):

- **The current status's sentence is the "already" sentence.** Choosing the status a task already has says "T1.2 is already running.", as the dashboard's picker does, and writes nothing. Superseded is the exception: choosing it again gets the full confirm, because the replacing ids may be what changes.
- **The superseded sentence is the shared words with the typed ids.** The sheet carries the sentence with a placeholder in place of the ids and the script puts the typed ids in, so the page adds no words of its own. With no ids typed it shows the "type the ids" prompt and the action button stays disabled; the page without JavaScript goes back to the first step with that prompt. Ids typed for any other status are not sent from the page; an `/api` caller gets the command's own refusal.
- **Buttons.** The action reads "Mark <status>", the safe button "Leave it as it is", the first step of the page without JavaScript "Continue".
- **One read of the request body.** The confirm check used to consume a JSON body, so the status POST could not read its own fields after it. The body is now read once and both the confirm and the fields come from that read; pause and resume behave as before.
- **What the JSON answers.** The status POST answers the task (read again after the change) plus `result` (`outcome`, `old`, `new`, `text`). A change and an "already" are 200; a refusal is 400 with the command's words; a failed write is 500. The task list carries a `problem` key when the plan or ledger cannot be read; the task screen answers 500 then, because without the plan there is no telling whether the id exists.
- **Shares are percentages at one decimal**, as `afk tokens` prints them. Phase colours come from the design system's tokens (plan accent, execute ok, review attention, fix danger, ship idle, anything else grey).
- **The split bar's gaps.** `afk.css` puts a gap between segments, which pushed the last one out of the bar once the widths add up to 100, so `page.css` draws the divider as an inset edge instead.
- **The dashboard keeps its names.** `internal/ui` keeps small private wrappers that call `internal/taskstatus`, so its tests did not change.

Verification for the owner (T8.7): checked in a headless browser at 390 px, not on a real phone. Open a task from the phone, use Set status…, and see the Tasks list change without reloading.

### Scope ideas

- A live part for `.afk/log/tokens.jsonl`, so an open task screen shows new token figures when a task ends (A8).
- The why line for a superseded task with one replacement reads "which do its work instead"; it should read "which does" (review, minor).

## T8.8

T8.8 rebuilds the usage screen (`/usage`) from the design system and adds a **Refresh usage** button that takes a fresh reading in the background, the same reading as `afk usage --refresh` and the dashboard's `ctrl+space R`. The page shows "Refreshing" while it runs and the new numbers when it lands, without a reload. These are the decisions the master plan left open, numbered as in the work plan's assumptions (2026-10-14).

- **The address (A1).** The button POSTs to `/usage/refresh`, and the same path under `/api` (`/api/usage/refresh`). `GET /usage` never starts a reading, and `POST /usage` is still not a route.
- **No confirm (A2).** A reading changes no owner state, and the dashboard's refresh has no confirm either, so the button and the API POST take no confirm. The POST still reads its body only to apply the 64 KB limit.
- **Where the busy flag lives (A3).** The web server keeps an in-memory refresher: whether a reading is running, the last finished result and its time, and a generation number that goes up when a reading starts and when it finishes. A failed reading writes no file, so the generation is part of the `usage` live fingerprint: a start, a landing and a failure each send one `usage` event to an open page. The refresher is not saved anywhere, so a restart of the daemon forgets the last result and shows the saved reading again.
- **How the button updates (A4).** The button has its own live region, and that region is swapped on a live event unless a typed field has focus, with focus put back on the new button (`data-live-buttons`, and a stable id). So the owner's typing is never swapped out from under them.
- **The answers (A5).** The API answers 202 `{"outcome":"started"}` for a reading that starts, and 200 `{"outcome":"already"}` for a second press while one runs. `GET /api/usage` gains a `refresh` object (`state`, `text`, `at`); the existing fields keep their names and meaning, because the overview page embeds the same view. The page POST answers 303 to `/usage?done=refreshing`, or `?done=already_refreshing` for a second press, and the usage screen shows a toast for each.
- **The badge and the meters (A6).** The governor's decision shows as a badge with a word and a colour: Run (running), Launch nothing new (held), Pause (paused), Hold (idle), Refuse (failed). A meter's width is the window's percentage rounded and held between 0 and 100; it is warn from 80 and full from 100. The templates carry no inline `style`, so widths are `w-N` classes in `page.css`.
- **The seam (A7).** The web package cannot import the root package, so `web.Deps` gets `RefreshUsage func() UsageRefreshResult`: a synchronous function that returns an outcome (`refreshed`, `unsupported` or `failed`) and a sentence. The root wires it to the same step the dashboard uses, with the daemon's own state folder for the probe and the reading. A save that fails is folded into `failed`, so the page shows it in plain words and keeps the old reading.
- **Tests (A8).** Table tests in the package's style, with a fake reading that blocks on a channel, so the busy guard, the failures and a panic are each checked without a real probe.
- **Prerequisites (A9).** None. The dashboard's refresh (T7.13) and the usage screen's data (T8.5) were merged first.

Decided while building (T8.8):

- **A nil seam means no button.** When `Deps.RefreshUsage` is not set, `GET /api/usage` has no `refresh` key, the page draws no button and no region, and both POSTs answer 404. The page then does not offer a refresh that cannot happen.
- **An unknown outcome is a failure.** A seam answer that is not one of the three outcomes shows as failed with a "fault in afk" sentence, so an unexpected answer cannot pass for a success.
- **Plain fallback sentences.** An empty sentence from the seam gets one fallback per outcome. None of them names an agent, in line with the dashboard's wording.
- **A failure notice also covers "unsupported".** An agent that cannot report usage gives the owner nothing new, so the page shows its sentence as a notice too, with "The reading above is the last one that worked." added in the template. Hiding it would leave a press with no visible answer.
- **Toast codes are for the usage screen only.** `refreshing` and `already_refreshing` are in the toast table, but only the usage screen shows them. A hand-typed `?done=refreshing` on a project or the inbox shows nothing.
- **A panic becomes a failure.** A reading that panics leaves the refresher idle with a failed result, "The reading stopped unexpectedly. Try again.", so the button works again.
- **"Checked just now" is for a minute.** A successful reading that landed less than a minute before the page was drawn shows "Checked just now" on the button. A page left open keeps that label until the next live event; a reload after a minute shows "Refresh usage".
- **The probe runs in the state folder.** The web seam uses the same runner and state folder as the daemon, so the probe leaves no records in a project, as `afk usage` does.
- **The daemon does not wait for a reading.** A reading is bounded by the probe's own timeout, so a shutdown does not need to wait for one to finish.
- **The decision text matches the README.** The five decisions on the page use the same words and default numbers (75%, 85% for the week, 90%, 30 minutes) as the `afk usage` section, and say that `[governor]` in `config.toml` can change them.
- **The live rows are run by name.** The live tests are not matched by the usage test pattern, so they are run on their own names as well.

Verification for the owner (T8.8): open `/usage` in a browser, press **Refresh usage**, and see "Refreshing" and then the new numbers without a reload. Then make the agent fail and press again: the plain sentence shows and the old numbers stay.

## T8.9

T8.9 adds a project's sessions to the web page: a Sessions screen (`/p/<project>/sessions`) that lists every session of the project, and a session screen (`/p/<project>/sessions/<name>`) with the overview the dashboard shows, read-only and updating live. Each has a twin under `/api`. The task screen gets an **Open its session** link, and the project's nav gets Sessions. These are the decisions the master plan left open, numbered as in the work plan's assumptions (2026-10-14).

- **One seam (A1).** `web.SessionView` carries the facts the overview needs: the label, branch, base branch, session id, autonomous and planning marks, the stale reason, the work-plan counts, the review verdict and its checks, the read errors, whether the agent has gone quiet, the started and updated times, and the worktree path. The root fills it from discovery, and the started time comes from the session's `host.json`. `internal/web` imports none of discover, host, session or state. No new live part is needed: the existing `sessions` part fingerprints `Deps.Sessions`, so a change to any of these facts already sends an event. The drawing stays in `internal/web` (`sessions.go`) as pure builders over the same facts, because `internal/ui` draws with Lip Gloss and this package cannot import it.
- **Addresses (A2).** `GET /p/{project}/sessions` and `GET /p/{project}/sessions/{name}`, each with its `/api` twin, so fifteen routes in all. A name can belong to two rows: a planned session and a one-off given the same name. Both are listed, and the session's screen opens the first row discovery finds, status-backed rows first. That is the order the dashboard and `afk attach` use, so the page and the terminal agree. The one-off's own facts are on the list and in the list's API answer only.
- **Which rows (A3).** Every row discovery finds is listed, worktree problems included. A row shows its kind badge first (One-off, Planning, Status unknown, Problem, or its phase), then **Waits for you**, then **Stopped** when its host is not running, then its review verdict. A problem row gets no host badge or line, because it is not a session and has no host; "Stopped" beside it would be noise. "Its agent has gone quiet" shows only while the host is running, because a stopped host's `host.json` can keep the mark it had when it stopped, and "not running" already says more. The planning badge is plain, not coloured, because it says what the row is, not its state. The planning check comes before the one-off check, because a planning session also has no `status.json` and "Planning" says more. A one-off is never "Status unknown", since having no `status.json` is its normal shape.
- **What the Summary shows (A4).** The Summary is the overview in sentences: the phase, whether the session waits for you, the work-plan progress, the review verdict for each check, whether the host is running, stopped or has gone quiet, why the status is unknown, and any read error for the work plan or the verdict. It leaves out the git diff stats and the tail of the issues file. Both need git or extra file reads for every session on every live tick. It also leaves out how long an agent has been quiet, because discovery gives only the mark and not when it began. A one-off or problem row says plainly that it has no plan or review, rather than saying "none" for each.
- **Links (A5).** The project nav lists **Sessions** with its count. A Running now row links to its session's screen. The task screen shows **Open its session** only when discovery still finds the session the ledger names, so a cleaned-up session gets no link rather than one to a not-found page.
- **Tests (A6).** Table tests written with the code, in `sessions_test.go`, for the kind rules, the list and screen builders and the handlers. Each table says which rows are load-bearing. The handler tests use `newRig`'s fake sessions and scratch folders with a `host.json`, so nothing touches a real session or agent.
- **Prerequisites (A7).** None. T8.5 and T8.7 were merged first.

Decided while building (T8.9):

- **JSON names.** A one-off is `one_off` in the JSON, not `scratch`, to match the page's word. `kind` says what the row is, and `waits_for_you` applies the dashboard's rule (never for a done or failed session) beside the raw `awaiting_input`, so an API reader does not have to repeat the rule.
- **"None" for empty facts.** For a one-off or problem row, Loop count and Updated read "none" rather than "unknown", because they are not missing: a one-off never has them.
- **Short ids.** The list shows the first eight characters of the session id, with the whole id in a tooltip and in the JSON. The session screen does not repeat the id; its JSON has it.
- **Two live regions.** The state card and the badges beside the title are separate live regions, so a change to either swaps only that part, as the project page does. The Summary heading sits inside the state card, so the whole overview is one region.

Verification for the owner (T8.9): open `/p/<project>/sessions` for a project with a running session, change that session's `status.json` phase, and see the list and its screen change without a reload. Then open a one-off and a worktree problem row and read what each says it is.

## T8.10

T8.10 adds the dashboard's session actions to the web page: Clean, Restart and Label on a session's screen, and Start a new autonomous session… on the sessions list, each behind a confirm, with `/api` twins. These are the decisions the master plan left open, numbered as in the work plan's assumptions (2026-10-14).

- **Clean keeps `afk clean`'s gate (A1).** The page refuses to clean a session that is not done or failed, the way `afk clean` does without `--force`. It offers no force: removing a working session's copy of the project from a phone is the destructive case, so the refusal tells the owner to use `afk clean --force` or the dashboard. (Decided without asking; this would have been put to the owner.)
- **The branch always stays (A2).** The page never deletes a branch. Clean archives the session's folder and removes its worktree, as `afk clean` does by default.
- **A page-started session is autonomous, not unattended (A3).** It is started exactly as `afk new --autonomous` starts one, which is also what the dashboard's form does. It does not run in the agent's unattended permission mode, so a permission prompt waits until someone opens the session from `afk dash`. Running with permissions bypassed from a web page is the owner's call to make, so this change does not make it. (Decided without asking; this would have been put to the owner.)
- **Which sessions can be restarted (A4).** Only a planned session whose host has stopped (its host is started again) or whose agent has gone quiet (the host replaces the agent, which resumes its conversation). A one-off session, the planning session, a worktree problem and a healthy running session are refused, because restarting a one-off or the planning session would start a session the owner has to talk to.
- **A clean that went stale is refused (A5).** The confirm carries the phase the page showed. If the session has moved on since, nothing is cleaned, as in the dashboard.
- **A description is required (A6).** The page refuses a blank description and one over 5000 characters (the dashboard's limit). An autonomous session with no request stops at once, so starting one is never useful.
- **Addresses (A7, A8).** `POST /p/{project}/sessions` starts a session, and `POST /p/{project}/sessions/{name}/clean`, `/restart` and `/label` act on one. Each has an `/api` twin, so there are nineteen routes, and each needs the confirm in its body. The worktree is always found by the server, never taken from the request. Without JavaScript, each action has a confirm page of its own.
- **Tests (A9).** Table tests are written with each piece; `make check` runs once in review.
- **Prerequisites (A10).** None. T8.9 was merged first.

Decided while building (T8.10):

- **Clean words on the page.** A refused clean says the page does not force and names `afk clean --force` and `afk dash`, rather than the command's "pass --force" sentence. A done clean says what was archived and that the branch was kept.
- **A session named `new`.** `GET /p/{project}/sessions/new` is the new-session confirm page, so a session literally named `new` cannot be opened by its page address; its `/api` address still works.

Verification for the owner (T8.10): on a phone, open a session's screen and press Label, Restart and Clean, checking that each sheet opens with the safe button focused; then fill in Start a new autonomous session… and check that its confirm shows the name and description before it starts.

## T8.11

T8.11 brings the plan's status and approving to the web page: a plan line on the project page and the plan page, a checker card with the findings (errors first), and **Approve the plan…**, shown only for a new plan at review with no checker errors, behind a confirm, with the same route under `/api`. A plan on the review list can be approved too. `afk plan approve` and the dashboard are unchanged. These are the decisions the master plan left open, numbered as in the work plan's assumptions (2026-10-14).

- **The page gets the status through its own seam (A1).** `web.Deps.PlanStatus` carries the page's own plain type, filled by the root from the dashboard's status loader. The plan line is worded by the dashboard's own function (exported as `ui.PlanStatus.Line`), so the two surfaces say the same words, and `internal/web` never imports `internal/ui`.
- **The status reads files only (A2).** The root is used as the main checkout, with no git lookup, because a configured or listed project's root already is its main checkout. The page runs no git and no network to draw the plan line.
- **Approving has one entry point (A3).** `web.Deps.ApprovePlan(root)` answers approved, already approved, refused or failed, with the dashboard's wording from `approveResultText`. A plan with no project reads as refused, as it does on the dashboard.
- **Addresses (A4).** `POST /p/{project}/plan/approve` has an `/api` twin, so there are twenty routes. An HTML-only `GET /p/{project}/plan/approve` is the no-JavaScript confirm page. The project is found among the configured projects first, then the plans on the review list. The root is never taken from the request.
- **The answer is a page, not a toast (A5).** The result page carries the whole text with status 200, 400 or 500. A toast cannot say what the daemon does next or name a push problem, and the dashboard's toast cut those lines.
- **When the action shows (A6).** Only with the seam present, a known status that is a new session at review by its effective phase, a readable plan and no checker errors. Otherwise a one-sentence reason stands in its place. With errors, the design's error notice reads "Fix N errors before approving. Ask the planning agent to fix them, or edit the plan and check it with afk plan lint.", because the chat it points to does not exist until T8.12. The POST does not check the plan again; it relies on the entry point's own refusal.
- **A slow approve gets more time (A7).** The approve handler extends its own write deadline to three minutes with `http.ResponseController`, because the push inside approve may take up to two minutes and the server's write limit is thirty seconds.
- **Live updates (A8).** The plan page has no live region and the `planning` fingerprint is unchanged. The project page's plan line updates on the existing `planning` part, and it can lag a plan edit until the page is reloaded.
- **Finding links (A9).** A finding that names a task links to that task's heading in the plan (`#T5.7`). A finding that names no task shows its file and line as plain text, and the renderer gets no line anchors.
- **Confirm words (A10).** The title is "Approve the plan for <project>?". The body names the task and milestone counts and the plan's path, and it lists the checker's errors and warnings. **Not yet** is focused, and **Approve and start** is the other button. The "beginning with T1.1" line is left out, because the next task is the daemon's choice, not the page's.
- **Tests and prerequisites (A11, A12).** Table tests are written with each piece, and `make check` runs once in review. Nothing had to be in place first: T8.5 and T7.8 are merged.

Decided while building (T8.11):

- **A new plan at review whose status says it cannot be approved** is shown as "The planning status could not be read." The two answers disagree, and any other reason would send the owner to the wrong fix.
- **The error notice shows only when the errors are what stop approving.** If the planning session is the reason (not at review, or already approved), its own reason sentence shows instead.
- **An unknown outcome from the entry point** is answered as failed, with a sentence asking for a report, and an empty text gets a plain default sentence for its outcome.
- **The no-JavaScript confirm page** draws its answer from the same plan view as the plan page, so the two never disagree about whether approving is possible.

Closing T7.8's scope idea: the README sentence that said there was no button to approve on the web page is rewritten, so the "reads a little awkwardly" note no longer applies.

Verification for the owner (T8.11): on a phone, open a plan at review, press Approve the plan…, and check that the sheet opens with Not yet focused and the counts shown; then open a plan with errors and check that it shows the red notice instead of the button.

Scope ideas (T8.11): right after approving, the result page's "Back to the plan" link can answer "no project with that name" for a plan that was on the review list, because approving removes it from the review list and adds it to `config.toml`, but the page serves projects from the daemon's last good list until the daemon reads the config again. A later change could let the page find a just-approved project, or point the link at the overview. The glossary now has two **Plan line** entries in `CONTEXT.md` (the dashboard's and the page's), which a docs pass could merge.

## T8.12

T8.12 shows a project's open planning session as a chat on the web page, `GET /p/<project>/plan/chat`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **How the page replies (A1).** The page has its own twins beside the `/api` ones: `GET /p/<project>/plan/chat` draws the screen and `POST /p/<project>/plan/chat` takes an ordinary form, so the page works with JavaScript off. A sent reply goes back to the chat with a "Sent" toast; a refused one draws the chat again with the refusal's own sentence and the same status as the JSON address. The JSON refusal now carries the refusal's `reason` code beside its sentence, which T8.4 left out.
- **Forms and live updates (A2).** The messages are one live region with no form in it. The question cards and the reply box are a second region, which the page's live script now replaces too, but never while the owner has the keyboard in a box or has typed something there, so a half-written reply is never lost. With JavaScript on, a reply is posted in place and shows as the owner's message at once.
- **An answer in the owner's own words (A3).** The master plan asks for a field beside the options, but nothing could send one: typed text is refused while a question is open, and the keys of the agent's "Type something." row were never seen. The reply gains a third kind, `other`, for one single-select question. Its keys are found by trying the real agent in a throwaway folder; if no reliable keys are found, the answer is refused with a sentence pointing at `afk dash`. Decided without the owner (autonomous mode); it would have been asked.
- **Markdown (A4).** The agent's messages use the plan page's renderer in a chat mode: headings get no anchors (several messages would repeat them) and start one level below the page's own headings. The owner's messages are shown as typed, escaped.
- **Links (A5).** The project page gains "Planning chat"; the plan page's plan line links to the chat when a planning session is open, and its error notice now says to ask the planning agent in the chat; the planning session's row on the sessions screen and its own screen link to the chat.
- **Live part (A6).** The `planning` part now includes the conversation as the chat reads it, so a new message or a new question updates an open page. Nothing that changes every tick is part of it.
- **No confirm on a reply (A7).** Replies are not destructive, and asking "are you sure" before every chat message would defeat a chat; every other owner action keeps its confirm.
- **Routes (A10).** The two chat page routes are registered beside the `/api` chat routes, outside the shared route table, with their own token tests, so the table's count stays 20.
- **Tests and prerequisites (A8, A9).** Tests are written with the code; a browser run at phone width and an automated WCAG 2.2 AA check prove it at the end. T8.2, T8.4 and T8.5 were merged before this work started.

### Decided while building (T8.12)

- **Own-answer keys (trial outcome).** The trial on claude 2.1.300 (2026-10-14, a throwaway folder, eight prompts) found the keys reliable. For one open single-select question with N options, the page sends three writes 300 ms apart: the digit N+1, which moves the cursor to "Type something.", then the answer as one bracketed paste, then Enter. The digit was chosen over N Down arrows because it works from any cursor row; the owner may have moved the cursor in `afk dash` before answering. Two fresh dialogs answered in exactly that shape, and the parts (digit, paste, plain text) answered five more. An empty or blank own answer declines the question, so it is refused before any key is sent. Not tried: multi-select, several questions, the review screen, and a digit 5 (four options). Those stay out of the own-answer path. The record is `.afk/t8-12/own-answer-keys.md`. T8.4 wrote that a digit past the options does nothing; on 2.1.300 the digit N+1 does move to the row, and only a digit past "Chat about this" is ignored.
- **Form field names.** The page reply is an ordinary form. The composer's textarea is `text`; the own-answer textarea is `other`; a one-question option button is `choice-0` with the option's label as its value; radio or checkbox options are `choice-<question index>`. Each reply form carries `data-chat-form` and `data-project`, and a refused reply puts the owner's `text` and `other` back in their boxes.
- **The `data-live-form` rule.** The reply area opts in with `data-live-form`. The live script swaps it on a change unless the cursor is in one of its fields or a textarea or text box holds text that is not blank. The message thread has no form, so it always swaps. A refusal therefore never costs the owner's words.
- **Reply codes.** The JSON refusal carries `reason` beside `error`, with the codes `no_session`, `not_waiting`, `wrong_kind`, `not_offered`, `bad_reply` and `unsupported`. An own answer is reported as `sent: "text"`, not `"other"`, and the README says so.
- **Pending questions and the thread.** While a choice is pending, the agent's last question is drawn only in the reply area, so it does not appear twice. Earlier questions stay in the thread, read-only, with "(Recommended)" beside the suggested option.
- **Unknown state.** A conversation the page cannot read shows its problem in an error notice, and the disabled composer says afk cannot tell what the session is waiting for and to answer in `afk dash`.
- **Links.** The project nav always has a Planning chat entry, because the chat page itself explains the no-session case. The plan line links only when a session is open; the sessions list's Planning row and the planning session's screen link to the chat.
- **Verification for the owner.** The own-answer keys reached only a stand-in host in QA. On a real planning session that asks one single-select question, type an answer in "Or answer in your own words", press Send, and check that the agent records that text as its answer.

### Scope ideas (T8.12)

- At 390px the "Recommended" mark sticks out of the option button by about 8px; let it wrap or sit on its own line in `page.css`.
- The option label keeps the agent's "(Recommended)" suffix, which the mark repeats; strip it for display and keep the label for the answer.
- The no-session text says "this page will be able to start one later"; T8.14 and T8.15 should replace that clause with their buttons.
- The comment on `maxChoiceOptions` in `internal/provider/choice.go` still says a digit past the options does nothing, which is no longer true on claude 2.1.300.
- The JSON reply body says `"sent": "text"` for an own answer; it could say `"other"`.

## T8.13

T8.13 makes the daemon send one ntfy notification when a planning session starts waiting for the owner, linking to its chat. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **What counts as one wait (A1).** A wait is known by a key made of the chat's state, its number of messages, and the time and text of its last message. The conversation only grows, so an answer followed by a new question always gives a new key, and reading the same wait again gives the same key.
- **Which projects, and which link (A2).** Every project the page lists is checked: configured projects, plans waiting for approval and projects being planned, merged by folder in the page's own order. The link opens the planning chat where the page serves one; a plan waiting for approval that is not configured has no chat on the page, so its notification links to its plan page instead.
- **When the record is cleared (A3).** A project's record goes when its planning session is gone. It stays while the session works or cannot be read, so a passing read failure does not send the same wait twice.
- **A failed send (A4).** It is not recorded, and the next pass tries again. A delivered notification is recorded at once.
- **When the check runs (A5).** Once per daemon pass, and once in `afk daemon --once`, even while the build is paused: planning is attended work, not building.
- **No ntfy address (A6).** Nothing is sent or recorded, as with every other afk notification. A project being planned in a new folder needs its own `afk.local.toml` with `ntfy_url` to be notified.
- **The message (A7).** Title "afk: <project> is waiting for you", the first line of the question as the body, priority 4 like a blocking inbox item.
- **The record file (A8).** `plan-waits.json` in afk's state folder, written only by the daemon by atomic rename; docs/PLAN.md §4.2a describes it.
- **Tests (A9).** Table tests are written with the code. The decision lives in its own pure package, `internal/planwait`.

### Decided while building (T8.13)

- **The check gets the configured projects through the wiring, not a new field.** `realDaemonDeps` takes the projects function that `runDaemon` already uses, so `Run` and `Once` both read the configured list. `TestRunDaemon` keeps a hermetic rig that never reads a real planning host.
- **Targets are cleaned and de-duplicated across all earlier entries.** A project with an empty folder is skipped, because cleaning it would name the folder the daemon runs in. A later entry with a name or folder already taken by an earlier one is dropped, even inside one list, so two checks never share one record.
- **Records for folders no longer listed are kept.** If the project comes back on the same wait, it is not announced twice. Only a chat that reads none clears a record (A3).
- **A send is bounded by the pass.** Each send runs under 10 seconds, inside the pass context, so a daemon that is stopping cuts a send short and starts no new one.
- **An unreadable project config means no address.** It is logged on each pass while the wait stays unannounced, the same as the other daemon notifications.

### Scope ideas (T8.13)

- `afk daemon --once` returns before `daemon.Once` when no project is configured, so the planning-wait check does not run then for a plan waiting for approval or a project being planned; the running daemon does run it. `--once` could call the check before it returns early.
- A machine-wide ntfy address in `config.toml` would let a project being planned in a new folder be notified before it has its own `afk.local.toml`.

## T8.14

T8.14 lets the web page start a planning session that changes the project's plan (extend, amend, reconcile) and opens its chat. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One address for the three actions (A1).** `POST /p/<project>/plan/change` takes the mode (`extend`, `amend` or `reconcile`), the text and the confirm, and has an `/api` twin, so the shared route table has 21 routes. A page-only `GET /p/<project>/plan/change` draws the form and the confirm.
- **The confirm is a page, not a pop-up (A2).** Each row and each chat button opens the change page. Amend asks for its sentence and reconcile for a path inside the project there, then the page shows what will start and the text, with **Start planning** and **Cancel**. It works with JavaScript off, and nothing typed sits in a part of the page that updates by itself.
- **Answers (A3).** A start goes straight to the chat view with a short notice. A refusal is shown on the change page in the command's own words; when a planning session is already open, it links to that session's chat. The JSON address answers the same with status 200 or 400.
- **What the project page shows (A4).** The three actions when the project has a plan and no planning session is open; a line naming the open session and a link to its chat when one is; one sentence and no actions when there is no plan.
- **No resume button (A5).** The page never asks only to reopen a session. If the start function reports a resumed session, the page treats it like a start.
- **Which projects (A6).** Only projects afk builds. A plan on the review list is a new plan still being planned, so it has nothing to change yet.
- **Tests and prerequisites (A7, A8).** Table tests are written with the code. Nothing had to be in place first: T8.12 and T7.3 are merged.

### Decided while building (T8.14)

- **Chat toasts have their own table.** "started" and "resumed" already meant other things on the project page (a session started, a project resumed), so the chat reads its own sentences from `chatToastTexts`. The project page's `toastFor` is unchanged, so "resumed" there still means a resumed project.
- **The change page's view is `ChangePageView`.** The plan named it `PlanChangeView`, but that name is the API's answer (`PlanChangeView` in `plan_change.go`), so the page's view takes the other name. A first visit to the change page is not told that the field was left blank; only a submitted blank is.
- **Refusals on the page.** Every page-side refusal of the change POST, including a missing confirm, redraws the change page at 400 with the sentence above the form. The notice is left out when the page already says the same sentence (an unknown mode, or an open session). A text longer than 5000 characters is refused before the seam is asked, as the new-session description is. The mode and the text are trimmed first, and extend always sends an empty text, so text that came with extend is never passed on.
- **The API's refusal reasons.** A refusal the page makes itself (a bad mode, a blank sentence or path, a long text, a body that cannot be read, a missing confirm) answers `reason: bad_request`. A refusal from the seam answers `session_open` when a planning session is open and `refused` otherwise.
- **Unknown seam outcomes are refused.** Any outcome that is neither started nor resumed is refused with a plain sentence, and no wait for the planning host runs for it.
- **The host wait uses the root it was given.** `webStartPlanWith` waits on the project root the page passed in, not on the root the entry point returns, because the entry point may leave the root empty on a refusal. The wait sentence is shared with the dashboard as `planHostNotReadyReason`; the dashboard's words are unchanged.
- **A nil plan reader counts as a plan.** When the page has no way to read the plan, it treats the project as having one, so the seam gets the last word on whether a change may start.
- **Chat empty state and project page share the rows.** The chat's buttons come from the same function as the project page's Change the plan section (`changeSectionFor`), so both surfaces say the same words. The section has its own `data-live="planning"` region, so a live swap replaces it whole.
- **Test-only edits outside the Files list.** `pages_test.go` got one allow-list line for the new POST route and one row for the no-JS change page, because the route-table guard and the page-name guard both fail without them. No other behaviour is changed there.
- **No `CONTEXT.md` line.** "Change the plan" is the name of a page section, and extend, amend and reconcile already have their meaning in the README, so no new term was needed.

Verification for the owner (T8.14): on a project with a plan, open `/p/<project>/plan/change?mode=reconcile&text=../../etc/passwd`, press **Start planning**, and check that the page refuses it in the command's own words and starts nothing. The review's hands-on check did not run this case against a live daemon.

Scope ideas (T8.14): the amend sentence travels in the change page's address (`?text=`) until **Start planning** is pressed, so it stays in the browser's history. A form step that posts instead would keep it out.

## T8.15

Decisions the master plan left open, made while planning T8.15 (Plan a new project in an existing folder):

- **The page learns the started folder from the start itself.** `web.PlanStartResult` gains `Root`, copied from the entry point's result, with symlinks resolved for a new plan as the dashboard does. Without it the page could not name the project it just started.
- **The being-planned list is written through a seam.** `web.Deps.AddBeingPlanned` is wired in the root package to `internal/daemonstate`, and it is called only after a start that answered started or resumed. The web package never imports `daemonstate`.
- **"Its planning session is gone" means:** there is no `.afk/plan/state.json` any more, or the session has finished (approved). A damaged `state.json` is not gone, and whether the planning host is alive is not looked at, because a session whose state is still open can be resumed. `afk plan approve` also removes the entry.
- **The confirm card asks the same questions the start asks.** A read-only `Deps.CheckPlanFolder` shares the new-mode checks of `startPlanForSurface`, and it never creates a folder or runs `git init`.
- **Routes.** The form is the page-only `GET /plan/new`. `POST /plan/new` is in the shared route table with its `/api/plan/new` twin, so the table has 22 routes.
- **Names.** A being-planned project is named after its folder, with `-2`, `-3` added when a configured project, a plan waiting for approval or another entry already has that name. A folder already on the review list keeps the review list's name, so `/p/<name>/plan/chat` always means one folder.
- **Card wording.** The card says "afk will plan in", with a badge "Existing folder" or "Existing folder, becomes a git repository". "afk will create and plan in" waits for T8.15a, which is the task that creates folders.

Decided while building (T8.15):

- **The folder check reuses the start's own look at the folder.** It takes the same `planDeps` as the start, with every writer swapped for a refusal, so the confirm card and the start can never disagree and the check never creates a folder, runs `git init` or writes a file.
- **A folder that already has a plan is refused with the page's own sentence**, pointing at Change the plan. The other refusals (not a full path, not found, a file) keep the command's sentences unchanged.
- **Approve removes the being-planned entry only when the folder is on the review list**, which is the normal path. A project that is approved without ever reaching the list is still hidden at once, because its phase reads approved, and it is pruned from the file at the next start from the page.
- **A failure to prune gone entries is not reported.** The read already hides them, and the add's own write reports an unwritable folder.
- **README.** The page's description sits with the other pages under "What each page shows"; the JSON paragraph names `POST /api/plan/new` and `being_planned`.

Verification for the owner (T8.15): on a phone, open the overview, press **Plan a new project**, type the path of an empty repository and press **Start planning**; check that the chat opens and the overview shows the project as "Being planned". The review ran this with curl against a scratch build, not in a browser.

Scope ideas (T8.15): **Start planning** has no loading state yet, because `confirm.js` only animates buttons inside its confirm dialog; letting it also handle a page form with `data-loading` would add one. The refusals for a folder that is not found or is a file mention `--project`, which a page user has no use for; T8.15a replaces the not-found case and could reword the file case.

## T8.15a

Decisions the master plan left open, made while planning T8.15a (Create the folder for a new plan), numbered as in the work plan's assumptions:

- **Seams (A1).** The page's folder check keeps its shape and gains a `create` kind. Two new seams, `Deps.CreatePlanFolder` and `Deps.PlanLocation`, are wired in the root package to `internal/planfolder` and the owner profile, so the web package never reads the profile or touches the disk itself, as with the dashboard.
- **What the confirm sends (A2).** For a new folder the confirm sends the name the owner typed, not the full path, and the create step decides again from that name. A request can therefore only ever make a folder directly under the project location, never one at a path it names.
- **A name that already exists (A3).** The page shows that folder's own card ("Existing folder", and so on) with a note saying a folder of that name is already there, and plans in it on the confirm.
- **The JSON answer (A4).** A successful start says `created: true` or `false`. A create that fails answers 400 with reason `create_failed` and the path left on disk, and no planning session is started.
- **The hint (A5)** names the profile's project location; with none set it says how to set one with `afk profile set`, or to type a full path.
- **Wording (A6).** The page has its own sentences for a typed path that is not found or is a file, without the command's `--project`.
- **No loading animation (A7)**, as in T8.15; only the button's loading label changes.
- **Tests (A8)** are table tests written with the code. **Nothing had to be in place first (A9).**

Decided while building (T8.15a):

- **The name check wraps the path check.** `webCheckPlanFolderWith` stays the T8.15 path-only check; a new `webCheckPlanFolderNamesWith` handles bare names and hands a name whose folder exists to the path check on its full path, with `Name` set so the card can say a folder of that name is already there.
- **The page's own words for a missing path or a file** are chosen only on a refusal for a full path, from a look at that path, because those are the only two refusals about a folder that is not a folder.
- **A create whose `mkdir` failed reports no path**, so the page never names a folder that is not on disk; after a failed `git init` it names the folder left there and says nothing was removed.
- **Tests that really create folders make them only under the temp location**, so a broken guard in a mutation run cannot make folders next to the test.

Verification for the owner (T8.15a): on a phone, open **Plan a new project**, check that the hint names your project location, type a new name such as `demo`, check the card says "afk will create and plan in" with "New, empty folder", and press **Start planning**; the folder appears with a `.git` in it and the chat opens. Also worth one manual try: with a `git` that fails first on the daemon's PATH, a new name should leave the folder on disk, say so, and start nothing. The review checked this only in unit tests.

## T8.15b

Decisions the master plan left open, made while planning T8.15b (Offer a GitHub repository for the new folder), numbered as in the work plan's assumptions:

- **How the question is carried (A1).** The create POST draws the question; the answer goes to a new `POST /plan/new/github` (with its `/api` twin, so there are 23 routes). The server keeps, in its own memory, the folders it has just made and the repository name it offered, for one hour and at most 32 at a time. The answer acts only on those, so no request can get a repository for a folder that was already there, and the name is never taken from the request. Memory rather than a file, because the list exists only to prove that the form made the folder moments ago; after a restart the answer is refused, which is the safe failure, and the folder can still be planned by typing its path.
- **The seams (A2).** `Deps.GitHubOffer` (the read-only check of `gh`) and `Deps.CreateGitHubRepo` (the one create) are both optional. Both must be set for the question to be asked; with either missing, planning starts at once as in T8.15a. The root wires them over `internal/ghrepo`, with the same check order as the dashboard.
- **Where the outcome is shown (A3).** A declined answer goes straight to the planning chat, as before. Every other outcome (made, failed, name taken, no origin, or gh not ready) shows the started page with its line and an **Open its chat** button, because a redirect cannot carry the sentence.
- **The API shape (A4).** The create POST answers 200 with outcome `github_question` and starts nothing. The answer POST takes `{path, github, confirm}`; a missing `github`, or any value other than `true`, means no. Each answer carries a `github` object with `outcome`, `repo` and `text`, or with `outcome` `not_offered` and a `reason` when no question was asked.
- **The tests (A5)** are table tests written with the code, using a fake `gh` and a fake `git`, plus one stand-in `gh` script on a scratch PATH. No test calls GitHub.
- **Prerequisites (A6).** None: T8.15a and T7.6 were already merged.

Why these choices, in plain words:

- **Nothing is made on GitHub without an explicit yes.** No is the checked choice, and a missing or unreadable answer counts as no. The owner's profile says to ask before opening a new repository, so the question is asked every time and the answer is never remembered.
- **Nothing is pushed.** The create makes the repository and sets `origin`; the first push belongs to the build, much later.

Verification for the owner (T8.15b): on a phone, open **Plan a new project**, type a new name such as `demo`, press **Start planning**, and check that the question "Create a private GitHub repository for it?" shows your account and has **No** checked. Answer yes once with a throwaway name and check on GitHub that the repository is private and empty, and that `git remote -v` in the new folder shows `origin`. The review ran this against a scratch build with a stand-in `gh`, not against GitHub.

## T8.16

T8.16 lets the web page add a project to the daemon's list and remove one, with the same effect and words as `afk project add` and `afk project remove`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Addresses (A1).** `POST /projects/add` and `POST /projects/remove`, each with an `/api` twin, so there are 25 routes. The add form, the remove screen and the remove confirm page (for a browser without JavaScript) are pages of their own at `/projects/add`, `/projects/remove` and `/projects/remove/confirm`.
- **Both need the confirm (A2).** Every change the page makes needs `confirm` in the request, so add does too; the add form sends it. A request without it changes nothing.
- **Remove names a listed project (A3).** The page sends only the project's name. The server looks it up in the daemon's own list and shows that folder in the confirm; a name not on the list is refused without touching `config.toml`. The web page still never reads `config.toml` itself.
- **The folder you type (A4).** `~` means your home folder. Any other path must be a full one, because the daemon's own working folder means nothing to you. The same rule as the dashboard's form.
- **Until the daemon reads the list again (A5).** The daemon reads `config.toml` again at the start of every pass, a few seconds apart. Until it does, the overview says the project was added or removed and that its row appears or goes on the next pass. The server keeps these notes in memory only (at most 32, for ten minutes), so a restart forgets them, which is harmless: by then the list has caught up.
- **The JSON answer (A6)** is `{"result":{"outcome","name","root","text"},"pending"}`, with 200 for added, already listed and removed, 400 for refused and not listed, and 500 when `config.toml` could not be read or written.
- **One change at a time (A7).** Adds and removes from the page run one after another, so two requests cannot both read the file and overwrite each other's change.
- **The remove confirm (A8).** It names the project and its folder and says the folder, its plan and its sessions are not touched and a task that is running is left to finish. **Keep it** has the focus.
- **Buttons (A9).** Add a project sits in the phone's top bar, the desktop's top bar and the overview's footer; Remove a project is a quiet button in the footer. Each shows only when the daemon wired its action.
- **Tests (A10)** are table and handler tests written with the code. **Nothing had to be in place first (A11):** T8.5 and T7.10 are merged.

Decided while building (T8.16):

- **The overview's JSON gains `pending`.** The notes about a change the daemon has not picked up yet are a new `pending` list on `GET /api/`, beside the existing keys, never null.
- **The remove sheet reuses the session sheet.** The same partial that draws the session actions' confirm draws the remove question, and the same value draws the page for a browser without JavaScript.
- **`confirm.js` fills the folder and skips unchecked radios.** It now fills `{folder}` as well as `{name}` and `{description}`, and it reads only the checked radio, so the sheet names the project you picked and not the last one in the list.
- **A remove answer has no folder.** `afk project remove` does not report the folder it removed, so the JSON `root` is empty after a remove. The page's note uses only the name.
- **Names match.** The daemon's list uses the `[[project]]` names, so remove passes the listed name straight to the command's code, with no matching by folder.

Verification for the owner (T8.16): in a browser, open the overview, press **Remove a project**, pick a project and press **Remove…**. Check the sheet names the project and its folder and that **Keep it** has the focus. Also check that the **Add a project** button sits right in the phone's top bar and the desktop's top bar. The review checked the page for a browser without JavaScript over HTTP, and nothing in a real browser.

## T8.17

T8.17 proves the web page on a phone: the owner ran one whole plan from it, then this task checked every screen against WCAG 2.2 AA and fixed what it and the owner found. These are the decisions the master plan left open, made while planning the task (2026-10-16), numbered as in the work plan's assumptions.

- **Afk does not answer the agent's trust prompt for you (A1).** A new folder stops the planning session at the agent's "do you trust this folder?" question. Afk could answer it ahead of time by writing the agent's own settings file, but the agent rewrites that file constantly, so afk could lose its changes, and it would grant trust on your behalf. So afk leaves it alone, and the chat view now says the session may be waiting at a question in its terminal and how to answer it. Inbox item vbkr6s asks whether a later task should pre-trust only the folders afk creates.
- **Sent answers are tidied by count, not by text (A2).** The chat shows a "Sent" bubble until your reply appears in the conversation. A choice's reply never matched that bubble's text, so it stayed pinned below the conversation. The bubble now goes as soon as the conversation gains a reply of yours, whatever its words.
- **A push refused for workflow files says why (A3).** When GitHub refuses a push because afk's login cannot change `.github/workflows/`, the inbox item now says so and gives the two fixes (`gh auth refresh -s workflow`, or an SSH origin). Afk still follows your `gh` protocol setting when it creates a repository.
- **The accessibility check is not part of the repository (A4).** It is a script that drives headless Chrome and axe-core 4.13 against a test copy of the page; it lives with this session's files and nothing new is added to the build.
- **Every screen means every page and state (A5):** each address the page serves, each state of the planning chat and the inbox, and each confirm sheet opened, on a phone width (390 px) and a desktop width (1280 px), in light and dark, against axe's WCAG 2.0, 2.1 and 2.2 A and AA rules.
- **Colours may move a little (A6).** Where a contrast failure needs it, a design-system colour changes by the smallest step that passes, and `docs/design-system.md` changes with it.

### The owner's run and what it found

- **The run.** The owner ran one whole plan from a phone through the web page, in a throwaway project, `woodshop` (`/home/dphaener/code/woodshop`). They planned it and approved it from the phone, and the daemon built and merged its first task, T1.1, as pull request #1 (merge commit f2579e0). The report is inbox item sm2qbj.
- **Two places the owner had to leave the page, both for a terminal.**
  1. To answer the agent's "do you trust this folder?" question for the new `woodshop` folder. The planning chat showed "working" for two minutes and then a sentence that never mentioned a terminal prompt.
  2. To switch `woodshop`'s `origin` from https to SSH after GitHub refused the push of T1.1.
- **Fault 1: the trust prompt.** A folder the agent has not seen before stops the planning session at a question in its terminal, before any conversation exists, so the chat has nothing to show. Cause in afk: the sentence for "no conversation after the grace period" only covered a session started by hand or cleared. Fix: that sentence now says the session may be waiting at a question in its terminal, such as whether to trust a new folder, and to answer it in `afk dash` or with `afk attach`. The grace period and the chat's states are unchanged.
- **Fault 2: the answer shown twice.** After a choice answer, the reply showed in the thread and again as a "Sent" bubble pinned under it. Cause: `chat.js` removed the pinned bubble only when an owner message with the same text arrived, but a choice is shown as its label while the server writes `Header: Label`, so the texts never matched. Fix: each pinned bubble remembers how many owner messages the thread held when it was sent, and goes once the thread holds more (the text match stays as an extra way for typed replies). The rule is a pure function with a node table test, run by the new `make web-js-test` and by `make check`.
- **Fault 3: the refused push.** The https origin's `gh` token lacks the `workflow` scope, and the branch added `.github/workflows/ci.yml`, so GitHub refused the push. Afk's inbox item gave the wrong cause ("the branch on GitHub has commits the session does not have"). Fix: a pure function in `internal/ship` recognises GitHub's workflow-scope refusal and the item now says the real cause and the two fixes, `gh auth refresh -s workflow` or an SSH origin. Every other push failure keeps its old hint, byte for byte. Afk still does not pick the protocol for you.

### The accessibility check

- **Method.** axe-core 4.13.0 with the tags `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa` and `wcag22aa` (so WCAG 2.2 AA, including the 24 px target-size rule), driven over the Chrome DevTools protocol in headless Chrome against a test copy of the page seeded with every state. Each screen was checked at 390 px and 1280 px wide, in light and dark. The run first plants an image with no alt text and stops unless axe reports it, and stops unless the contrast and target-size rules ran.
- **Screens.** 82 screens, covering every address the page serves and each state of the planning chat, the inbox, the tasks, the sessions, the usage reading, plan change, plan new (including the GitHub question and the started page) and project add and remove, plus the 404, 400 and refused pages. Each of the 22 confirm sheets was also opened and checked. That is 416 views in all (82 x 4, plus 22 x 4 with a sheet open).
- **Baseline: 6 failing views, 2 distinct problems, both `scrollable-region-focusable`** (a region that scrolls sideways but cannot take keyboard focus, so a keyboard user could not reach the hidden part). Colour contrast, names, labels, landmarks, headings and target size passed everywhere.
  1. The task screen's tokens-by-role table on a phone (390 px, light and dark): five columns do not fit, and the scrolling wrapper could not be focused.
  2. The plan view's code block (both widths, light and dark): a long command line scrolled sideways inside a `pre` that could not be focused.
- **Fixes.**
  1. The tokens table wrapper is now a named, focusable region (`role="region"`, `aria-labelledby` its heading, `tabindex="0"`), so the keyboard can scroll it and the existing focus ring shows. The table was not squeezed to fit, because five columns cannot fit a phone.
  2. Code blocks in the plan view now wrap long lines, as the rest of the page already did, instead of scrolling. The chat's code blocks got the same wrap in advance, since they had the same pattern and a longer line would have failed there too.
  No design colour changed, so `docs/design-system.md` is unchanged. No rule was turned off and no selector excluded.
- **Result.** After the fixes, 416 views, 0 violations, 0 harness errors. Putting one fix back made the check report the violation and fail, so it still catches what it was built to catch.
- **Not covered by automation.** A scripts-off Chrome pass, focus order, the look of the focus ring, reflow at 320 px, and how a screen reader reads a live update. These need a person.

### What remains

- **The trust prompt still needs a terminal.** The chat now tells the owner what is wrong and how to answer it, but a brand-new folder still stops at the agent's question until someone answers it in `afk dash` or `afk attach`. Fixing that for good means afk pre-trusting the folders it creates, which is the open question in inbox item vbkr6s (filed at planning, not filed again).
- **Nothing the check found was left unfixed**, so no inbox item was filed for the accessibility check.

Verification for the owner (T8.17): in a planning chat on your phone, answer a question that has options. Your answer should show once, in its place in the conversation, with no "Sent" bubble left below it. The fix was tested with node tests only, not in a real browser (inbox item k3aq3e).

## T9.1

T9.1 makes ctrl+] the dashboard's default leader in place of ctrl+space. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **One task for the dashboard package (A1).** Changing the leader breaks every dashboard test that presses it, so the key, its comments, the tests and the saved screens change together; the help text, README and glossary follow.
- **Tests (A2).** The existing tests are updated, and one new table test holds the acceptance rows: ctrl+] opens the dialog from the list, the detail side and the agent pane; ctrl+space opens none of them; ctrl+] twice in the pane types one 0x1D.
- **Passing the leader on (A3).** Pressed twice in the agent pane, the dialog hands ctrl+] to the pane, which already encodes it as the byte 0x1D. The pane still encodes ctrl+space as NUL, so ctrl+space now reaches the agent like any other key.
- **Old ctrl+space tests (A4).** A test that checked ctrl+space as the leader now checks that it is an ordinary key, rather than being deleted.
- **Nothing to wait for (A5).** T7.11, T7.12 and T7.13, which also change the dashboard package, are merged.
- **How ctrl+] arrives.** Read in ultraviolet `v0.0.0-20260811164956-006e29f97886`, the decoder bubbletea v2.0.9 reads keys through (`tty.go` calls `uv.NewTerminalReader`). A legacy ctrl+] is the byte 0x1D: `decoder.go` `parseControl` sends bytes `ansi.FS`..`ansi.US` (0x1C–0x1F) to `KeyPressEvent{Code: b+0x40, Mod: ModCtrl}`, so 0x1D becomes `{Code: ']', Mod: ModCtrl}`. A kitty-protocol ctrl+] is `CSI 93;5u`: `decoder.go` hands `CSI … u` to `parseKittyKeyboard`, 93 is not in `kittyKeyMap` (its `init` adds only the C0 codes 0x00–0x1F), so the code is the rune `]`, and modifier 5 becomes `fromKittyMod(4)`, which is `ModCtrl`; with no text field the result is again `{Code: ']', Mod: ModCtrl}`. `key.go` `Key.String()` returns `Keystroke()` when there is no text, which writes `ctrl+` and then the rune, so bubbletea's `KeyPressMsg.String()` is `ctrl+]` for both. NUL still decodes to `{Code: KeySpace, Mod: ModCtrl}`, `ctrl+space`. All three were checked by feeding the bytes to `uv.EventDecoder.Decode` in a throwaway program.
- **The old key in prose.** Comments and the README name the old key "Ctrl+Space", so they can still say why the leader moved while a search for the literal `ctrl+space` finds only the pane's key encoding and the tests that check ctrl+space is now an ordinary key.
- **Tests check bytes.** The new table test sends the fake pane's keys through a real terminal emulator, so "one NUL" and "one 0x1D" are the bytes the agent would read, not key events.
- **The pass-through key is written out.** Pressed twice in the pane, the dialog forwards ctrl+] as a literal key, and the hint for opening the all-projects screen first is a fixed string naming ctrl+] A. T9.2, which makes the leader configurable, must derive both from the configured key.

## T9.2

T9.2 lets the owner set the dashboard's leader with `leader = "<key>"` in the `[dash]` table of `config.toml`. These are the decisions the master plan left open, made while planning the task (2026-10-14), numbered as in the work plan's assumptions.

- **Where the key is checked (A1).** `daemonconfig` stores what is written, trimmed, and only a value that is not a string becomes `DashErr`. The dashboard checks the key itself. A bad `[dash]` value resets the whole table, so checking the key in `daemonconfig` would let a leader typo switch off the folder search too.
- **When it is read (A2).** `afk dash` reads the leader once, when it starts, and a bad or unreadable file means the default with no header line (the existing toast already names the file). A changed leader applies on the next start. The leader has to work from the first keypress, and a key that changes while the actions dialog is open would leave the dialog unsure what closes it.
- **Where it lives (A3).** The dashboard model holds the leader, and every hint, help line, the dialog and the pass-through take it from there. Nothing keeps it in a package-level variable, so tests stay independent of each other.
- **Passing it to the agent (A4).** Pressed twice in the agent pane, the configured key goes straight to the terminal emulator, which encodes it, so ctrl+] still arrives as 0x1D and ctrl+o as 0x0F. The pane's own key table drops keys it does not list, so it is not used for this.
- **Which keys are refused (A5).** In this order: a name the dashboard would never receive (an unknown key name, a modifier that is unknown, repeated or out of order, an upper-case letter, or shift with a printable key, which arrives as typed text); a key the dashboard keeps for itself (enter, esc, tab, shift+tab, ctrl+c, the arrows, pgup, pgdown, home, end, k, j, and ctrl+u and ctrl+d, which scroll the detail pane); and a key with no modifier. Upper and lower case are treated alike.
- **The header line (A6).** A refused key puts one line at the front of the header, saying what is wrong, what to write in `config.toml`, and that ctrl+] is in use. It has a shorter form for narrow windows, because the header must stay one row.
- **Text outside the dashboard (A7).** The help for `afk dash` and `afk` says ctrl+] is the default and how to change it. The message that sends someone to the dashboard to change a plan names the leader they configured.
- **Tests (A8).** Each part is tested as it is written: the check is a table test, `daemonconfig` gets rows for the new key, and model tests cover a configured ctrl+o and a refused `q`.

## T10.1

T10.1 adds a back row under the desktop top bar. These are the decisions the master plan left open, made while planning the task (2026-10-16), numbered as in the work plan's assumptions.

- The `.back-row` this task built was replaced by T10.3; see `## T10.3`.
- **Tests (A3, A4).** A table test renders every page `chromeFor` knows and checks the row on each, written in the same task as the change. A second test checks `page.css` hides the row at the phone width.
- **Nothing to wait for (A5).** No other task had to land first.

## T10.2

T10.2 removes the "Back to …" links in page content that only repeat the bar's back button. These are the decisions the master plan left open, made while planning the task (2026-10-16), numbered as in the work plan's assumptions.

- **No link kept for its address.** Each removed link's address was checked against the bar's back address for the same page, in every branch. All of them matched, so no link stayed because it went somewhere else.
- **Tests (A1).** The tests are written in the same task as the change: a table test of each changed page and branch, plus rows for the links that stay. The older tests that looked for the removed links now look for the bar's back button.
- **Nothing to wait for (A2).** T10.1 had already landed.
- **New plan started with no chat (A3).** On the new-plan page's "started" result, the button row is drawn only when there is a chat to open, so the page never ends in an empty row.
- **One task (A4).** Templates, tests and this note are one task, because every test edit is in the same Go package.

## T10.3

T10.3 draws the desktop back row as the design system's desktop app bar. These are the decisions made while building the task (2026-10-19), numbered as in the work plan's assumptions.

- **The bar as the design draws it.** The desktop back bar is `app-bar app-bar--back app-bar--desktop` under the `topnav`, with the same back button, title and optional meta as the row T10.1 built, on the same pages and to the same back address. The design's three `.app-bar--desktop` rules are in `afk.css`, and `page.css` shows the bar above 760px only. The `.back-row` markup and rule are gone.
- **A nav, not a header (A1).** The design draws the bar as a `header`. It stays a `<nav aria-label="Back">`, so it is still a labelled landmark, and it adds no second visible banner to the page. Only the classes carry the design's look.
- **The chat's meta (A2, A3).** The planning chat's meta is its existing mode and phase line, "New plan · Rounds". `chromeFor` sets it in a new `chrome.Meta` field from the chat page's sub line, so the bar shows the same words as the page, and an empty line draws no meta. The choice is pure, so a table test can check each row.
- **The meta does not update live (A4).** The bar sits outside the live-update regions, so its meta changes on a reload or after a form post, like the rest of the bar. Making it live would need JavaScript, which this task leaves alone.
- **The design-system text (A5).** The new desktop-bar text was an uncommitted edit to `docs/design-system.md` in the main checkout. Its desktop-bar parts were brought into this branch with this task. The sign-in changes that follow-up 2 sends back were left out.
- **The plan page's title (A6).** The plan page's bar title is "Plan · <project>". Its own `<title>` and `<h1>` still say "Plan: <project>", since this task changed only the bar title.
- **For the owner to check.** No browser run had a live planning session, so the chat's meta was checked by tests only. During a planning chat, open its chat page at 1280px wide and check the bar shows the phase line beside "Planning <project>".

## T8.18

T8.18 replaces the web page's shared token with a password: `afk web password` saves a hash of it, the page shows a sign-in form, and a sign-out button sits in the bars. The token, `?token=` and the `Authorization: Bearer` header are gone. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The hash (A1).** PBKDF2 with HMAC-SHA256 from Go's standard library, 600,000 rounds, a 16-byte random salt and a 32-byte result. It is slow and salted, which is what a stored password needs, and it adds no new module. Argon2id or scrypt would be stronger against special hardware, but both need `golang.org/x/crypto`, and for one owner's page behind Tailscale or a proxy, PBKDF2 at this count is enough.
- **Where it is kept (A2).** A new package, `internal/webpass`, used by the command, the page and the daemon. The file is `web-password.json` in afk's state folder. It holds the method, the round count, the salt and the result, never the password. It is written with mode 0600 through a temporary file and a rename. The round count is stored in the file so tests can use a small one.
- **The cookie (A3).** The cookie's value is worked out from the stored hash. A new password gets a new salt and so a new hash, so every browser signed in before is signed out. The value cannot be turned back into the password.
- **Too many wrong passwords (A4).** There is one count for the whole page, because there is one owner. After 5 wrong passwords in a row, sign-in is refused for a minute, even with the right password. A right password resets the count. The count is kept in memory, so restarting the daemon clears it. Each sign-in try waits its turn, so many tries sent at once cannot all be checked before the count catches up.
- **Addresses (A5).** Any address asked for without a valid cookie answers with the sign-in form. After a right password the browser goes back to the address it asked for, but only when that is an address on this page. Anything else goes to `/`. Sign out is a POST to `/signout`, which clears the cookie and goes back to the form.
- **When the page is on (A6, A7).** The page is on when the password file exists. The daemon checks this when it starts. An address other machines can reach with no password stops the daemon. That check moved out of reading `config.toml`, because reading the config file cannot see the state folder. A `[web] token` left in `config.toml` is read and ignored, with one log line. A password file that cannot be read counts as no password, and the daemon's log says which file and to run `afk web password` again.
- **Scripts (A8).** The page's scripts already fall back to a normal page load when an answer is not a page they can show, so a signed-out tab does not draw the form inside another page.
- **Tests and prerequisites (A9, A10).** Tests are written in the same task as the code. Nothing had to be in place first.

### Decided while building

- **The password record is checked strictly.** Reading, writing, signing in and the cookie all use one check: the method must be PBKDF2-SHA256, the round count above 0, the salt not empty and the result exactly 32 bytes. A file that fails it gives a plain "not a valid password record" error, never a sign-in that silently always fails, and a bad record is never saved. No error quotes the file's contents.
- **The state folder** is made with mode 0700 when it is missing. If it already exists, its mode is left alone. The password file is 0600 from the moment it is made.
- **`/signin` can be visited directly.** With no cookie it shows the form (200). When signed in it goes to `/`.
- **What counts toward the lockout**: only wrong passwords, and passwords over 1024 bytes. "No password set", an unreadable file and a broken form do not count, because none of them is a guess. A try while locked does not lengthen the lock, so the wait is never more than a minute from the fifth wrong try.
- **The return address** is checked both as typed and after decoding. It is cleaned before the checks, so `/p/../api/x` also goes to `/`. It never goes to `/signin`, `/signout` or `/api`.
- **Messages on the form.** A wrong password is shown under the field. The lockout, "no password is set" and "the file cannot be read" are shown in a notice above the form. None of them repeats what was sent or what the file holds.
- **An unreadable password file on an address other machines can reach** stops the daemon, the same as a missing one, with a message saying the file cannot be read. The plan said to treat it as no password. Refusing keeps a reachable page from starting in a state no one can sign in to.
- **A `[web] token` set to an empty string is not a leftover token.** Any other value is, and its value is never kept or printed.
- **Notification links** (`afk inbox add`, plan reviews) check for the password file in their own process. In tests that check is a seam, so no test reads the real state folder.
- **The sign-out button.** On the phone it shares the bar's actions area with the Add-a-project icon. On the desktop it is the last item in the top bar. It is a plain form, so it works the same with JavaScript off. Page tests that check a page has no form now ignore the two sign-out forms in the bars.

### Checked, and not checked

- **Checked against a scratch build** (never `~/bin`), with scratch state and config folders:
  - `afk web password` saved a 0600 file holding no copy of the password, and refused a mismatch and input that was not a terminal.
  - `afk daemon` with a leftover token logged the one line and served the sign-in form. `curl` with `Authorization: Bearer` got a JSON 401.
  - A `0.0.0.0` bind with no password exited 1, with a message naming `afk web password`.
  - In headless Chrome at 390 and 1280 px, with JavaScript on and off: the overview showed the form, a wrong password was refused, the right one reached the overview, Sign out returned to the form, and the sixth wrong try showed the one-minute notice.
  - axe (WCAG 2.2 AA) found no violation on the sign-in page, empty and with the error, or on the overview, in both colour schemes.
- **`make check` was run in full**: green.
- **Not checked: a real phone, Tailscale or a reverse proxy.** Owner check: run `afk web password` on the VM, restart `afkd`, remove `[web] token` from `config.toml`, then sign in from the phone, tap a notification link and press Sign out.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes:
- Five test files changed that no task's file list named: only to drop the token from the test setup and to skip the bars' sign-out forms.
- This file and `CONTEXT.md` are written by the planner and conductor, by design.

## T11.1

T11.1 adds `afk version` (and `afk --version`) and a release workflow: pushing a tag `v<semver>` builds afk for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, writes `checksums.txt` and publishes a GitHub release. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Where the task text came from (A1).** When the session started, the T11.1 entry was only in the owner's uncommitted `docs/PLAN.md`. The session followed that text; it has since landed on `main` unchanged in substance.
- **Where the docs went (A2).** `docs/reference.md` and `docs/development.md` were not on `main` when the session started, so the docs first went into `README.md`. Merging `main` brought the README split in, and the docs moved where the task asked: `afk version` in `docs/reference.md`, "Making a release" in `docs/development.md`.
- **The master-plan amendment (A3).** Skipped at first, because `docs/PLAN.md` had no M11 then; added to the T11.1 entry once `main` was merged in.
- **The version string (A4).** It is the tag without its `v`: tag `v1.2.3` gives `afk 1.2.3 (…)` and `afk_1.2.3_linux_amd64.tar.gz`.
- **Commit and date (A5).** The first 12 characters of the commit, with `+dirty` when the tree had uncommitted changes, and the UTC day of the commit. With no build information at all the line is `afk dev (unknown)`.
- **Trying it without publishing (A6).** A hand-started run has `dry_run` (on by default) and an optional `version`, which defaults to `0.0.0-dev.<commit>`. A release is only ever created from a `v*` tag, never from a branch, a pull request or a dry run.
- **The build is a script (A7).** `scripts/release.sh` does the `go build`, `tar` and `sha256sum` steps, so the same build runs on a laptop and in the workflow.
- **The macOS check (A8).** The job runs the downloaded darwin/arm64 binary without `codesign` first, and only if that fails signs it and tries again. It writes which one happened to the run's summary.
- **The GitHub dry run was not run by this session (A9).** The session works without pushing, and the workflow only runs on GitHub. The owner runs it (see below).
- **Arguments and tests (A10, A11, A12).** `afk version` takes no arguments; `--help` prints its usage. Tests were written with the code. Nothing had to be in place first.

### Decided while building

- **`afk --version` is a rewrite, not a second command.** The router turns a leading `--version` into the `version` subcommand, so the subcommand list still has one entry per command.
- **The repo has no LICENSE file**, so the archives hold `afk` and `README.md`. The script adds `LICENSE` when one appears.
- **A commit stamp needs a plain checkout.** A build inside a session worktree nested under the main checkout (`.claude/worktrees/...`) is stamped with the main checkout's commit, often with `+dirty`. Go does the stamping, not afk. CI and a normal clone stamp the right commit.

### Checked, and not checked

- **Checked locally:** the version-line table test and the routing rows pass; `scripts/release.sh 0.0.0-test <dir>` built the four archives and `checksums.txt`, `sha256sum -c` passed, and the extracted linux/amd64 binary printed `afk 0.0.0-test (...)`; bad versions and a non-empty folder are refused. The workflow file parses, and actionlint's only complaint was the `ubuntu-26.04` label that `ci.yml` already uses. `make check` was run in full: green.
- **Not checked:** the workflow on GitHub and the macOS job (below).

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes:
- The "Build the archives" step puts the version straight into its shell line. A hand-run `version` input is checked by the script only after the shell has read the line. Passing it through `env:`, as the next step does, would be safer.
- This file and `CONTEXT.md` are written by the planner and conductor, by design.

### Verification for the owner

- **Answered 2026-10-19: a downloaded darwin/arm64 binary runs without `codesign`.** The dry run on 2026-10-19 (run 39302858785) ran the downloaded darwin/arm64 binary without `codesign`, and it worked. T11.5 depends on this answer. The check of the four archives and `checksums.txt` with `sha256sum -c checksums.txt` is still for the owner.

## T11.2

T11.2 adds `internal/install`: a pure function that turns what afk found out about a machine into the ordered install steps (each done, to do, or cannot, with the reason and what to do instead) and how risky the machine is, plus a read-only probe that finds those facts out. Nothing calls it yet; `afk install` (T11.3) will. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Tests (A1).** Written in the same task as the code, as table tests. Nothing had to be in place first (A2). The master plan's M11 text was not on this branch when the work started, so the session worked from a copy of the T11.2 entry and left `docs/PLAN.md` alone.
- **The agent CLI's name and its unattended setting (A3).** Two small functions in `internal/provider`, beside the provider types, so the install package never names a provider. The existing unattended check answers only yes or no; the installer must tell "not set yet, afk will set it" from "the settings file is broken, afk must not overwrite it", so the new reader gives four answers: set, not set, broken, or not supported by this agent CLI.
- **Risk (A4).** A Mac, or any machine with a desktop session, is a personal computer. A virtual machine or container with no desktop is `vm`. No virtualisation and no desktop is `dedicated`. If the virtualisation check could not run, the risk is `unknown`, which the installer treats like personal.
- **Step states (A5).** An unsupported system and running as root make every step "cannot", with one message. A step that waits on an earlier one stays "to do" and says what it waits for. A broken settings file, an agent CLI with no unattended mode, a problem with the project location and a missing package manager are "cannot", each with what to do instead.
- **`~/bin` (A6).** The service runs `~/bin/afk`, so the service step is "cannot" until afk is there. `~/bin` missing from `PATH` keeps the final check "to do", with how to add it.
- **Commands (A7).** Every command the probe runs has a deadline (10 seconds by default). A command that fails or runs out of time reads as "could not tell", never as "no".
- **macOS service (A8).** The LaunchAgent's label is `io.github.dphaener.afk`, and the probe reads it with `launchctl print`. This is provisional: T11.4 checks it against launchd's documentation.
- **Names (A9).** The type is `Plan` and the function is `Build`, because Go cannot give a type and a function the same name in one package.

### Decided while building

- **What "broken" means for the settings file.** Only a file that cannot be read, or one that is not a JSON object, is broken. A key of the wrong type reads as "not set", because the agent CLI would still show its prompt. An unknown provider name also reads as broken, with the lookup error as the reason.
- **A wrong package manager is no package manager.** `brew` on Linux or `apt-get` on a Mac makes the packages step "cannot", the same as having none.
- **A "cannot" always says what to do.** A broken settings file with no reason from the reader still gets a general fix.
- **Service answers.** Matching output (`enabled`, `active`, `Linger=yes`) is yes, other output is no, and no output with an error is "could not tell", so a missing user bus is never read as a stopped service.
- **macOS service.** When `launchctl print` fails, the agent counts as not loaded. This is provisional until T11.4.
- **The no-provider-names rule is a test.** A test scans every Go file in `internal/install`, tests included.
- **The final check on a personal machine.** On a Mac or a desktop the safety step is always "to do" (the risk must be accepted each run), so the final check step is never "done" there even when everything is installed. T11.3 may count an accepted safety screen as done before it works out the plan again.

### Checked, and not checked

- **Checked:** table tests of `Build` and `Probe` (with fakes, including a command that runs out of time and an unreadable settings file), `make check`, and one real probe of this Linux machine through a temporary test that was deleted afterwards. It finished in about a second and changed nothing.
- **Not checked: a Mac.** Owner check: on a Mac, once `afk install` exists (T11.3/T11.4), run `afk install --dry-run` and see that the plan reads the machine correctly, the service step in particular.

## T11.3

T11.3 adds `afk install`, the guided installer for Linux: it shows the install plan from T11.2 as a checklist, shows a safety screen, and then works through the steps that are still to do, checking the machine again after each one. These are the decisions the master plan left open, numbered as in the work plan's assumptions. The master plan's M11 text was not on this branch when the work started, so the session worked from a copy of the T11.3 entry and left `docs/PLAN.md` alone.

### What was decided, and why

- **Where the code lives (A1).** The pure parts (which step comes next, the package names, the service file's text, its `PATH` line, the web address) are in `internal/install`. Writing the agent CLI's settings file and the agent CLI's install and sign-in commands are in `internal/provider`, so no other package names the agent. The `[web]` edit of `config.toml` is in `internal/daemonconfig`, beside the `[[project]]` edits. The command, its screens and everything that touches the machine are in the root package.
- **The safety screen counts once it is accepted (A2).** The installer remembers, for the rest of the run, that the owner accepted the risk, and tells the install plan so each time it checks the machine again. The probe itself never claims it.
- **The safety screen always shows (A3).** Also with `--yes`. On a personal computer, or a machine afk cannot place, the owner types `I accept the risk`; on a server or a VM a key press is enough.
- **Installing the agent CLI and signing in (A4).** afk runs the install command the agent's maker documents, as the normal user, and then the agent CLI's own sign-in, each with the terminal handed over. Both commands are held in `internal/provider`. If the CLI lands in `~/.local/bin` and that is not on `PATH`, afk adds it for the rest of the run so the next steps can find it. This was decided without the owner (it would have been asked).
- **Packages (A5).** `sudo apt-get update`, then `sudo apt-get install -y` with the missing packages, each with the terminal handed over so `sudo` asks for its password itself. A fresh cloud machine's package list is empty, so the update comes first.
- **The password and the plugin (A6).** The installer runs afk itself (`afk web password`, `afk plugin install`) with the terminal handed over, so the T8.18 prompt and the plugin installer are used as they are. The installer never sees the password.
- **How the web page is reached under `--yes` (A7).** This machine only (`127.0.0.1:4253`), the least exposed choice. Every address and Tailscale are offered but never picked for the owner. This was decided without the owner (it would have been asked).
- **A service file afk did not write (A8).** afk marks the file it writes with a comment on its first line. A file without that line is shown and replaced only if the owner says yes (the default is no), and never under `--yes`. This was decided without the owner (it would have been asked).
- **`--dry-run` needs no terminal (A9).** It only prints, so it can run in CI (T11.4's macOS check). Every other run needs a terminal.
- **The docs pages (A10).** `docs/install.md` and `docs/reference.md`, which the master plan says to rewrite, were not on this branch: they are part of the owner's README split, not yet committed. Making them here would collide with that work, so `afk install` is documented in `README.md` and `contrib/README.md`, and those two pages are left for the owner's split to pick up. This was decided without the owner (it would have been asked).
- **Tests and prerequisites (A11, A12).** Tests are written in the same task as the code. Nothing had to be in place first.

### Decided while building

- **The runner.** The screen is one pointer model whose every slow operation (the probe, a command, a file write, a handover) carries a sequence number, and a result from an earlier number is dropped, so a late answer can never act on a screen the owner has moved past.
- **Every step asks first.** Each step has its own yes or no question (default yes), the logins and the unattended setting included, so there is one rule the owner can predict. The git identity and project location forms count as their own confirmation. Answering no stops the run with "You said no" and undoes nothing. The questions are afk's own y/n/enter prompt, not a form, so they show in the golden files and `--yes` takes the default.
- **The checklist is short on purpose.** It shows the reason only for the step at work and for steps that cannot be done; twelve reasons do not fit a 24-row terminal. `--dry-run` prints every reason, one line per step.
- **Other systems fail in plain words.** Packages on anything but `apt-get`, and the service on anything but Linux, fail the step and say what to do by hand (macOS is T11.4) instead of guessing.
- **The service commands go through the handover too.** `daemon-reload`, `enable --now` and `enable-linger` all get the terminal, because on some systems `enable-linger` asks polkit for permission. The unit is not rewritten when it already matches.
- **A step that turns to do again stops the run.** A step done earlier in the same run that the next check finds undone ends the run instead of looping.
- **A step that fails stops the run, and earlier steps stay.** Nothing is undone.
- **The plugin target** is taken from the plugin installer's own constant, not typed again here.
- **Exit 0 when `~/bin` is not on `PATH`.** The reason is shown above the address; it is advice, not a failure. The "not a terminal" message goes to stderr.
- **The final check reads the stored usage reading only.** It never asks the agent CLI for a fresh one, because starting the agent from the installer is the daemon's job.
- **The settings writer sorts keys.** `~/.claude/settings.json` is rewritten with its keys in alphabetical order; values are kept byte for byte, and a file that is not a JSON object (or is `null`) is never overwritten.
- **`--dry-run` is not silent on the machine.** afk writes nothing, but the probe runs `gh auth status` and the agent CLI's login check, and those tools make their own small files in the home folder. A check that nothing was created has to leave them out of `PATH`.

### What was checked, and what was not

Checked on this branch, against a binary built outside `PATH`: the table tests for the plan, the helpers, the settings writer, `SetWeb` and the runner; golden files for every screen at 80 and 120 columns; `afk install --dry-run` printing the twelve steps and changing nothing; the not-a-terminal and bad-flag exits; and two end-to-end runs in a tmux terminal with a scratch home and stand-ins for `apt-get`, `sudo`, `curl`, the agent CLI, `gh`, `systemctl`, `loginctl` and `systemd-detect-virt`. The first run accepted the safety screen, answered the forms, chose "this machine only" and reached the address screen `http://127.0.0.1:4253`, leaving the settings key, `[web] bind`, the project location, and the marked unit with its `PATH` line, with the commands in the expected order. A second run reported everything in place and ran no changing command. A run where the GitHub sign-in failed stopped at that step, kept the earlier work and ran no service command.

Not checked, and left to the owner: a real Ubuntu machine, a real `sudo` password prompt, real agent and GitHub sign-ins, real Tailscale (`tailscale serve`), and real systemd starting the daemon and keeping it after logout. The stand-ins prove afk runs the right commands in the right order and reads their results; they cannot prove those programs do what afk expects on a real machine.

### Docs left for the owner's split

`docs/install.md` and `docs/reference.md` were not on this branch (A10), so their rewrite waits for the owner's doc split. `afk install` is documented in `README.md` (the Installation section and the subcommand list) and `contrib/README.md` meanwhile.

## T11.4

T11.4 makes `afk install` work on a Mac with Homebrew: the packages step installs the missing tools with `brew install`, and the service step writes a launchd LaunchAgent and loads it, so the daemon runs in the background as it does under systemd on Linux. The Linux path is unchanged. These are the decisions the master plan left open, numbered as in the work plan's assumptions. The master plan's M11 text was not on this branch when the work started, so the session worked from a copy of the T11.4 entry and left `docs/PLAN.md` alone.

### What was decided, and why

- **How the plist is made (A1).** A pure function in `internal/install` writes the plist's XML text itself, escaping every value. A plist library would be a new dependency for nine fixed keys.
- **Which keys (A2).** The plist mirrors the Linux unit and adds nothing else: the label `io.github.dphaener.afk`; `ProgramArguments` running `~/bin/afk daemon` with the full path spelled out; `RunAtLoad`; `KeepAlive` set to true, so launchd starts the daemon again after it exits on purpose for a new version of itself; `ThrottleInterval` 30, the same wait as the Linux unit's `RestartSec=30`; `AbandonProcessGroup`, so the agent sessions outlive the daemon, as `KillMode=process` does on Linux; `EnvironmentVariables` with the `PATH` built exactly as for Linux; and the daemon's output in `daemon.log` in afk's state folder.
- **How afk knows the file is its own (A3).** A comment on the line after the XML declaration, because a plist cannot start with `#` the way the Linux unit's marker does. A plist without it is shown and replaced only if the owner says yes, and never under `--yes`, the same rule as on Linux.
- **Loading it (A4).** afk writes the file, then runs `launchctl bootout` (only when the agent is already loaded, so a changed file takes effect) and `launchctl bootstrap gui/<uid>`. It never uses `sudo` or the older `launchctl load`. When loading fails, afk says to run `afk install` from a desktop login on the Mac, not over SSH.
- **The user id (A5)** comes from a new seam in the installer, so tests use a fixed one.
- **Packages (A6).** `brew install` with the missing tools, after one yes-or-no question. afk never installs Homebrew: with no `brew` the step says to install it from brew.sh and run `afk install` again.
- **Reading launchd's state (A7).** The probe keeps reading `launchctl print`: exit 0 means loaded, `state = running` means running. launchctl's own manual says this output is not a stable interface, but there is nothing stable to read instead, and a wrong reading makes the step fail with its reason when afk checks again, rather than pass quietly. The label `io.github.dphaener.afk`, provisional in T11.2, is kept.
- **Checking on a Mac (A8).** No Mac was available, and the T11.1 release workflow with its macOS runner is not on this branch. The darwin build and `go vet` run here; running `afk install --dry-run` on a real Mac is left to the owner. This was decided without the owner (it would have been asked).
- **The docs (A9).** `contrib/README.md` gains a macOS section beside the Linux steps and `README.md` covers macOS. `docs/install.md` is not on this branch, so its "On macOS" rewrite waits for the owner's doc split, as in T11.3.
- **Tests and prerequisites (A10, A11).** Tests are written in the same task as the code, and the Linux golden screens must not change. Nothing had to be in place first.

### What each launchd key does

Each line below quotes or closely paraphrases launchd.plist(5) (https://keith.github.io/xcode-man-pages/launchd.plist.5.html) and nothing more.

- **Label.** "This required key uniquely identifies the job to launchd." afk uses `io.github.dphaener.afk`.
- **ProgramArguments.** It "maps to the second argument of execvp(3) and specifies the argument vector to be passed to the job when a process is spawned". With no `Program` key, the first element "may be either an absolute path, or a relative path which is resolved using _PATH_STDPATH". afk spells out the absolute path, so the lookup does not depend on a path afk does not control.
- **RunAtLoad.** It is "used to control whether your job is launched once at the time the job is loaded". afk sets it to true so the daemon starts when the agent is bootstrapped and at each login.
- **KeepAlive.** "The default is false and therefore only demand will start the job", and "The use of this key implicitly implies RunAtLoad". afk sets it to plain true, not the `SuccessfulExit` dictionary. `SuccessfulExit` is not used: "If true, the job will be restarted as long as the program exits and with an exit status of zero. If false, the job will be restarted in the inverse condition." The daemon exits 0 on purpose when it finds a new version of itself and must be started again, so afk wants a restart whatever the exit status.
- **ThrottleInterval.** "By default, jobs will not be spawned more than once every 10 seconds." afk sets 30 to match the Linux unit's `RestartSec=30`.
- **AbandonProcessGroup.** "When a job dies, launchd kills any remaining processes with the same process group ID as the job. Setting this key to true disables that behavior." afk sets it to true so the sessions the daemon started outlive it, as `KillMode=process` does on Linux.
- **EnvironmentVariables.** It is "used to specify additional environmental variables to be set before running the job". afk sets `PATH` to the same value the Linux unit gets.
- **StandardOutPath.** "The given path should be mapped to the job's stdout(4)." launchd creates the file if it is missing. The man page says nothing about the folder, so afk makes `daemon.log`'s folder itself before it loads the agent.
- **StandardErrorPath.** The same for stderr. afk points both at the one `daemon.log`.
- **launchctl print.** launchctl(1) says the output of `print` "is NOT API in any sense at all". afk still reads it (exit 0 means loaded, `state = running` means running) because there is nothing stable to read instead, and the label `io.github.dphaener.afk` that T11.2 marked provisional is kept, since nothing in this work gave a reason to change it.
- **Not claimed.** Whether `bootout` kills processes outside the job's process group is not in the man page, so nothing here relies on it. That `launchctl bootstrap gui/<uid>` needs a desktop login, and fails over SSH, comes from community reports, not from the man page; afk's advice to run from a desktop login rests on those reports.

### Verification for the owner

- On a Mac with Homebrew, from a desktop login (not over SSH), run `afk install --dry-run` and check that the packages step names Homebrew and the service step names `~/Library/LaunchAgents/io.github.dphaener.afk.plist`. Then run `afk install` as far as the service, and check that `launchctl print gui/$(id -u)/io.github.dphaener.afk` shows `state = running` and that `~/.local/state/afk/daemon.log` fills.

## T11.5

T11.5 adds `install.sh`, the bootstrap script behind `curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh`: it downloads the latest release's afk, checks it against `checksums.txt`, puts it in `~/bin` and starts `afk install`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Signing on macOS (A1).** T11.1 has not recorded whether a downloaded macOS binary runs without `codesign`, so the script does not assume either answer. It places the binary and runs `afk version`; only on a Mac, and only if that fails, it signs the binary with `codesign --force --sign -` and tries once more. Still failing, it stops and says what to try by hand.
- **Finding the latest release (A2).** The script follows GitHub's redirect from `/releases/latest` and reads the tag from the address it lands on. That needs no `jq` on a fresh machine and does not count against the GitHub API's limit for anonymous callers.
- **Asking questions through a pipe (A3).** `afk install` reads from `/dev/tty`, because the script's own input is the download. A machine with no terminal gets the binary and a line saying to run `~/bin/afk install` from a terminal. The test points the script at a file instead, through `AFK_TTY`.
- **shellcheck (A4).** `make check` runs `shellcheck install.sh` when shellcheck is installed. In CI a missing shellcheck is a failure (GitHub's runners include it); on a laptop without it the check is skipped with a note.
- **Where the test lives (A5).** `scripts/install.test.sh`, run by its own make target, `install-script-test`, so the hook-test loop stays about hooks.
- **Replacing an afk that is already there (A6).** The new binary is written to a temporary name inside `~/bin` and renamed over `~/bin/afk`, so it is always a new file.
- **Tests (A7).** The shell test was written with the script, with stand-in `curl`, `uname`, `id`, `codesign` and `afk`.

### The review

All three checks passed with no blocker or major finding, so no critic ran. `make check` was run in full, with shellcheck: green. Minor notes: this section and the `CONTEXT.md` line are written by the planner and conductor, by design; the Makefile changed, so CI's full run is the last word.

### Verification for the owner

- **The one-line command needs a release.** No `v*` tag has been pushed yet, so `curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh` stops with "could not find the latest afk release" until one is. After tagging, run it on a fresh Multipass VM and on a Mac (T11.6).
- **CI must have shellcheck.** If the first CI run fails with "shellcheck is required in CI and is not installed", add an `apt-get install -y shellcheck` step to `.github/workflows/ci.yml`.

## T12.1

T12.1 makes release builds clean: the release workflow's dry run of 2026-10-19 printed `+dirty` for darwin/arm64 although `main` was clean. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The cause (found while planning).** `make check` leaves a fresh clone clean. The dirt came from `scripts/release.sh` itself: it built the four targets one by one and wrote each archive into its output folder as it went, and the workflow's output folder, `dist`, sits inside the checkout and was not ignored. After the first archive (linux/amd64) landed there, Go saw an untracked file and stamped every later build as modified. The run log agrees: its linux/amd64 line was clean, its darwin/arm64 line was not.
- **The fix (A1).** `/dist/` is ignored, the script builds all four binaries before it writes any archive, and it refuses to archive any binary whose build stamp says the tree was modified. The last check means a dirty release cannot be published whatever changed the tree, while a hand build of a changed tree still says `+dirty`.
- **Outside a git checkout (A2).** `scripts/release.sh` refuses, because it cannot show the tree is clean.
- **The test (A3).** `scripts/release.test.sh` builds a tiny Go program in scratch git repositories and runs from a new `release-script-test` target in `make check`, with shellcheck, the same way `install.sh` is tested.
- **The actions.** Each one moves to its newest major, read from that action's own `action.yml` on 2026-10-19: checkout, setup-go, setup-node and upload-artifact to v7, download-artifact to v8, all on Node.js 24. `ruby/setup-ruby@v1` stays: its moving `v1` tag already runs on Node.js 24.
- **The GitHub dry run (A4).** Not run by this session: an autonomous session does not push, and the workflow only runs on GitHub. The owner runs it (below).
- **Tests and prerequisites (A5, A6).** Tests were written with the code. Nothing had to be in place first.

### Verification for the owner

- **The GitHub dry run.** Push the branch and run `gh workflow run release.yml --ref t12-1 -f dry_run=true`. Check that it is green, that the linux/amd64 and darwin/arm64 version lines have no `+dirty`, and that the run shows no Node.js 20 deprecation warning. Locally, a fresh clone built all four binaries clean into an in-tree `dist/`, and `make check` was green.

## T12.2

T12.2 adds the MIT licence the owner chose on 2026-10-19. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **The licence text (A1).** `LICENSE` uses the layout GitHub and choosealicense.com publish: a "MIT License" title, then `Copyright (c) 2026 Darin Haener`, with plain ASCII quotes. The wording is the same as the text at opensource.org, and GitHub recognises this layout as MIT on the repository page.
- **Where the README says it (A2).** A short `## Licence` section at the end of `README.md`, linking `LICENSE`.
- **The older T11.1 note (A3).** The T11.1 section above says the repo has no `LICENSE` file. It stays: it records what was true when T11.1 was done.
- **Tests and prerequisites (A4, A5).** No new automated test, because only text files change; the release script was run to check that every archive holds `LICENSE`. Nothing had to be in place first.

## T12.3

T12.3 adds `scripts/nextversion`, the small Go program that says whether a push to `main` should make a release and which version. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Two more folders count as the binary (A1).** Besides `plugins/`, `contrib/` and `internal/`, the root `.claude-plugin/` and `.agents/` folders count, because `plugin_assets.go` embeds them into afk. A change there changes what `afk plugin install` writes.
- **Which release is the last one (A2).** The highest `v<major>.<minor>.<patch>` tag that the pushed commit contains, not simply the nearest one, so an older tag on a merged branch can never pull the version backwards. Tags of any other shape are ignored.
- **What the range covers (A3).** Every commit between the two commits, including the commits of a merged branch, and every path the range changed. A renamed file counts under both its old and new name. A push that created the branch (an all-zero start) covers the whole history.
- **Reading commit messages (A4).** `Feat:` counts the same as `feat:`, `BREAKING-CHANGE:` the same as `BREAKING CHANGE:`, and `Merge remote-tracking branch …` is ignored like the other merge subjects.
- **Errors (A5).** A git failure prints one plain line on standard error and exits 1, with nothing on standard output, so the release workflow can never mistake an error for a version.
- **Tests and prerequisites (A6, A7).** Tests were written first; the git side is tested with real git in a temporary folder, shut off from the owner's git settings. Nothing had to be in place first.
- **Docs (A8).** No user page changes here: T12.4 explains automatic releases in `docs/development.md` when it wires this program into the release workflow.

### The review

All three checks passed with no blocker or major finding, so no critic ran. `make check` was run in full, with shellcheck: green. Minor notes: this section and the `CONTEXT.md` line are written by the planner and conductor, by design; a bad commit id's error line carries git's own hint text, which is noisy but still one line on standard error.

## T12.4

T12.4 makes a merge to `main` that changes the binary publish a release on its own. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **What a hand-started run compares against (A1).** A dry run on a branch asks "what would this publish?" from the last release: the highest `v<major>.<minor>.<patch>` tag the branch contains, or the whole history when there is none. That is the same "last release" `scripts/nextversion` uses.
- **Pushes wait their turn (A2).** The workflow's `concurrency` uses `queue: max` with `cancel-in-progress: false`. GitHub's default keeps only one waiting run and cancels it when a newer one arrives, which would lose that push's version bump; with `queue: max` up to 100 runs wait and each runs in order.
- **A failed decision stops the run (A3).** If `scripts/nextversion` fails, `scripts/release-decide.sh` prints nothing on standard output and exits non-zero, so the workflow fails instead of reading an error as a version.
- **One place works out the version (A4).** A new first job, `decide`, works out the version for every kind of run (a push to `main`, a pushed tag, a hand-started run); the build and release jobs read it from there.
- **Dry runs stay dry runs (A5).** A hand-started run still builds with the version you type, or a `0.0.0-dev.<commit>` one; the version it would publish only goes on its summary page.
- **Tests (A6, A7).** `scripts/release-decide.test.sh` runs the real script and the real `nextversion` in scratch repositories, from `make check`. The workflow itself only runs on GitHub, so this session checked it by reading it and parsing it.
- **Prerequisites (A8).** None: T12.1, T12.2 and T12.3 are merged.

### Verification for the owner

- **The dry run.** Push the branch and run `gh workflow run release.yml --ref t12-4 -f dry_run=true`. It should be green, and its summary should say it would publish `0.1.0`.
- **The first release.** It happens only once this task is merged. Then `gh release list` should show `v0.1.0`, and `gh release view v0.1.0` should list four archives and `checksums.txt`. Running the one-line install with `AFK_NO_INSTALL=1` (`curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | AFK_NO_INSTALL=1 sh`) should put a working `afk` in `~/bin` (`~/bin/afk version` prints `afk 0.1.0 …`).

## T13.1

T13.1 makes every planning session save the summary it prints at `review` to `.afk/plan/summary.md`, and makes `afk plan status` print that file at `review`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **What afk keeps of the file (A1).** `internal/plansession` keeps the summary's text as written, or nothing when the file is missing or holds only white space.
- **A file that cannot be read is no summary (A2).** The task says a missing summary is never an error, and the summary gates nothing, so a damaged one does not add a `Problem:` line or make `afk plan status` exit 1.
- **Where it is printed (A3).** After the `Waiting for:` line and before any `Problem:` line, under a line naming the file, `Summary (.afk/plan/summary.md):`, so you know where to find it.
- **Only at `review` (A4).** Not at `approved`, and not when a session recorded at `review` has been pulled back to an earlier phase because a file went missing: then the summary describes work that is no longer there.
- **`afk plan advance` is unchanged (A5).** Only `afk plan status` prints the summary.
- **Tests and prerequisites (A6, A7).** Table tests written with the code; nothing had to be in place first.
- **Coming back to `review` (A8).** The session writes the file again each time it prints the summary, so the file always matches the last thing it printed.

### The review

All three checks passed with no blocker or major finding, so no critic ran. Minor notes: this section and the `CONTEXT.md` line are written by the planner and conductor, by design; one full `make check` run hit a timing failure in `internal/host` (`TestSendInputReachesARealChild`), which this change does not touch and which passed when run again.

## T13.2

T13.2 makes approving a new plan end its planning agent, from `afk plan approve`, the dashboard and the web page alike. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **One stop, the same as `afk clean` (A1).** `internal/session` gets an exported `StopHost` that runs the same steps `afk clean master-plan` uses to stop a host (ask it to exit, then SIGTERM, then SIGKILL) and removes the same three files (the socket, `host.json`, `host.log`), but never the session's folder. So `.afk/master-plan/planning` and everything in `.afk/plan/` stay. T13.3 can use the same function.
- **Only a finished approve stops it (A2).** The stop runs once, after every step, and only when the run ends approved. A refused or failed run, and a second approve of a plan already approved, leave the agent alone: the task says "after `approved` is written".
- **What you see (A3, A4).** The last line becomes "The planning session has ended.", whether the agent was running or had already gone. If it cannot be stopped, the plan is still approved and the exit code is still 0, and one `Note:` line says to run `afk clean master-plan --project <folder>`. The dashboard's and the web page's notice say the same.
- **No stray output (A5).** Warnings from removing the host's files are dropped, so nothing is written over the dashboard's screen.
- **Docs (A6).** `docs/reference.md`, `docs/dashboard.md` and `docs/web.md`. The note in `afk plan` about a finished change session's agent stays for T13.3.
- **Tests and prerequisites (A7, A8).** Table tests written with the code, with a fake stopper; nothing had to be in place first.

### Decided while building

- **A missing stop seam says nothing.** If the code that stops the host is not given to approve, approve treats the stop as not tried and adds no line, instead of crashing. Every real caller and every test sets it.

### The review

All three checks passed with no blocker or major finding, so no critic ran. The minor notes: this section was written by the planner and is in no task's file list, by design, and the missing-seam case above stays silent.

## T13.3

T13.3 ends the agent of a finished change session (extend, amend or reconcile, at `review`, with its summary saved and waiting for you) without you doing anything, from the daemon and from the next `afk plan`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **One small decision of its own (A1).** A new package, `internal/planend`, holds the end-or-leave decision, beside the one that decides the "waiting for you" notification, so each stays about one thing.
- **You are told first, then it ends (A2).** On a daemon pass the "planning session is waiting" notification runs first and the end second, from the same once-per-pass hook. So you still get one message when a change session finishes, with the first line of its summary, and the hook already runs while the daemon is paused. A notification that fails to send does not hold the end back; the summary is still in `afk plan status`.
- **`afk plan` asks the same decision (A3).** When a new change session would have been refused because the finished one's agent is still running, afk asks the decision; if it says end, the old agent is ended and the new session starts. `--dry-run` says it would end the agent.
- **A failed end starts nothing (A4).** If the old agent cannot be ended, nothing is started, because a second planning host must never run beside the first; the message says to run `afk clean --project <folder> master-plan`.
- **Every end is logged (A5).** The daemon and `afk plan` both add a `plan_session_ended` line to `.afk/log/decisions.jsonl`.
- **Tests and prerequisites (A6, A7).** Table tests written first, with fakes; nothing had to be in place first.

### Scope ideas

- The messages and docs T13.2 added for a failed stop say `afk clean master-plan --project <folder>`, an order `afk clean` refuses. They should say `afk clean --project <folder> master-plan`. Logged in the inbox, not changed here.

### The review

All three checks passed with no blocker or major finding, so no critic ran. The minor notes: this section and the `CONTEXT.md` line are written by the planner and conductor, by design; the by-hand `afk clean` line puts `--project` first because that is the order afk accepts.

## T13.4

T13.4 lets a task carry `(fix)` in its title so the daemon builds it ahead of every other ready task, and makes `afk plan lint` refuse a task marked both `(fix)` and `(owner)`. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **`(fix; note)` is allowed (A1).** The marker is read exactly like `(owner)`: `(fix)` or `(fix; …)`, any case, spaces inside the brackets allowed. Words like `(fixes)`, `(fix up)` or `(prefix)` are ordinary prose and mark nothing.
- **Started work still goes first (A2).** A fix task beats every other task that is waiting to start, but a task already running, or a parked task whose questions you have answered, still goes first: it has a session waiting and the plan already says it goes first.
- **A method, like owner (A3).** The flag is `Task.Fix()`, beside `Task.Owner()`; nothing else needed a new field.
- **No plugin version bump (A4).** The master-plan format document is reference text; T13.1 changed the same skill without one.
- **The plan's contract is amended (A5).** §5.1 (the marker and the lint error) and §5.2 (the order) each get an Amended line, so T13.5 builds on them.
- **Tests and prerequisites (A6, A7).** Table tests written first; nothing had to be in place first.

### The review

All three checks passed with no blocker or major finding, so no critic ran. The one minor note was that the quality check left `make check` and a built-binary run to others; both were run before the commit: `make check` is green, and `afk plan lint` on a scratch plan with a `(fix) (owner)` task exits 1 naming `fix-and-owner`, and exits 0 once only `(fix)` remains.

## T13.5

T13.5 adds `afk plan fix`, a planning session that reproduces a bug, finds its cause and adds one `(fix)` task to the plan's `Fixes` milestone. These are the decisions the master plan left open, numbered as in the work plan's assumptions.

### What was decided, and why

- **Fix is choice 4 (A1).** When you type `afk plan` with a plan in place, the list gains `4) fix a bug (fix)` after reconcile, and asks what is broken, as amend asks what to change. The first three choices keep their numbers.
- **The dashboard and web page can already start one underneath (A2).** The shared code they use to start a planning session accepts a fix with its text, the same as amend, so T13.6 and T13.7 only add the buttons. Starting from there never skips the confirm (`--yes` is the command's alone).
- **No plugin version bump (A3).** The prompt change follows T13.1 and T13.4, which changed the same skill without one.
- **Tests and prerequisites (A4, A5).** Table tests written first; nothing had to be in place first.

### The review

All three checks passed with no blocker or major finding, so no critic ran. The minor notes: the quality check ran the touched packages' tests rather than the whole suite (`make check` was run in full before the commit and is green); a few new comment lines run long; and the dashboard, the web page and `docs/web.md` still name only extend, amend and reconcile, which T13.6 and T13.7 change. This section and the two `CONTEXT.md` lines were written by the planner and conductor, by design.
