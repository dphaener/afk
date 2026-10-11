# Glossary

Terms afk uses. Each line comes from `docs/PLAN.md` or the plugin docs under `plugins/afk`. Add a term once; do not rewrite existing lines.

- **Session**: one `claude "/afk:go --session {name}"` process on its own PTY in its own git worktree, with state under `.afk/{name}/`.
- **Session host**: the background `afk _host` process that owns a session's PTY and outlives every UI attached to it.
- **Conductor**: the skill that runs a session's whole lifecycle (plan, execute, review, ship) and records progress in `status.json`.
- **Work plan**: the per-task plan at `.afk/plans/{session}.md`, with waves, `(W1.T2)` task ids and seven subsections per task. Not the master plan.
- **Master plan**: `docs/PLAN.md`, the project-wide plan whose tasks (`T1.1`, `T2.2`) each become one session.
- **Wave**: a group of work-plan tasks that can run in parallel because none depends on another in the same wave.
- **Task agent**: the sub-agent that implements one work-plan task and saves its evidence.
- **Verifier**: an agent that checks a task agent's finished work and, on PASS, marks the task done.
- **Check**: one of the three review passes, `compliance` (including scope), `quality` and `qa`, each returning findings and a verdict.
- **Critic**: the adversarial agent run only on a check with a blocker or major finding, which confirms, downgrades or rejects those findings.
- **Verdict**: the outcome in `.afk/{session}/review-verdict.md`, APPROVE or REJECT, computed from the findings left after the critics.
- **QA scenario**: a task's hands-on test written as tool, steps, expected result and an evidence path.
- **Evidence**: the output a QA scenario saves under `.afk/{session}/evidence/`, so a reviewer can see it ran.
- **Park**: to stop a task because only the owner can do something, setting `awaiting_input` (and, from T1.2, adding an inbox item) rather than guessing.
- **Ledger**: `.afk/build/ledger.json`, the daemon's record of each master-plan task's status; the daemon is its only writer.
- **Inbox**: `.afk/inbox/{id}.json`, the items an agent or the daemon raises for the owner (questions, env vars, external actions); planned for T1.2.
- **Case**: the fifth field of a QA scenario, `happy` for the normal path or `failure` for an error path such as invalid input or a missing file.
- **Failure scenario**: a QA scenario whose case is `failure`; every work-plan task needs one unless it carries a failure waiver.
- **Failure waiver**: the line `No failure case: {reason}` in a task's QA Scenarios, for a task with nothing that can fail (docs only, a rename).
- **Exploratory QA**: the bounded part of the hands-on QA check that follows the scenarios: tasks tried together, then the edge cases that apply to the change.
- **Detach**: a client leaving a session on purpose (ctrl-\ in `afk attach`, or closing a dashboard pane); the session keeps running.
- **Steal**: a second client connecting to a session host, which takes the session from the client already attached.
- **Dropped**: a client's connection ending without the host saying the session ended; the session may still be running.
- **Inbox item**: one JSON file in the inbox, written only by `afk inbox add` (or, later, the daemon), with a kind, a title and whether it blocks.
- **Kind**: what an inbox item is: `question`, `env_var`, `external_action`, `verification`, `failure` or `notice`.
- **Blocking**: an inbox item the task cannot go on without; the task parks until the owner answers it.
- **Open**: an inbox item nobody has answered yet.
- **Assumed**: on a non-blocking question, the answer the agent went ahead with instead of waiting.
- **ntfy**: the push-notification service afk posts to when an inbox item needs the owner, set by `[notify] ntfy_url` in `afk.toml`.
- **Main checkout**: the project's original folder, as opposed to a session's worktree; `.afk/inbox/` lives there.
