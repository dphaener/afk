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
