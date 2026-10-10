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

- `TestHostLifecycleEndToEnd` in `internal/host` is flaky. It failed 6 times in 40 runs on an untouched copy of the source before the rename, and sometimes fails with "connection dropped" instead of a user detach, more often when the machine is busy. It is not caused by the rename and was not fixed, because T0.1 may not change behaviour. If `make check` fails on it, run it again before looking for a real problem.
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
