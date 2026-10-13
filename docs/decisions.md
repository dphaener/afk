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
- **Plugin version 5.6.1 (A12)**, because the format document's text on open questions changes. The OpenCode port gets a line in its lag note.
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
