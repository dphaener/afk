# The build daemon

`afk daemon` is the always-on part of afk: it builds a project's master plan one task at a time, ships each finished task, and asks you only for what it cannot decide. Its settings are in `config.toml` (see [configuration.md](configuration.md#configtoml--the-machines-settings)), and the web page it serves is described in [web.md](web.md).

## `afk daemon` — Build the master plan unattended

The daemon works through a project's master plan on its own. It runs one task at a time: it starts a session for the next task that is ready, watches that session, and when the session finishes it records the result and moves on. Nobody has to be at the keyboard.

```bash
afk daemon            # run until stopped
afk daemon --once     # look at every project once, do what is due, and exit
afk daemon status     # say what it is doing in each project, and why
```

**On each pass** (every 5 seconds by default) the daemon reads the project's files from scratch and does the first thing that applies:

- If the plan, the ledger or `afk.toml` cannot be used, it stops for that project and says what to fix. It carries on by itself once the file is fixed.
- If a task is running, it watches that task's session. A finished session moves the task on (see "Shipping" below). A session that ended as failed marks the task `failed` and adds a `failure` item to your inbox. An open blocking inbox item for the task parks it (see "What parks a task").
- If the session has been asking a question at the terminal for longer than `stall_after`, afk first types the session's resume command into it, once, in case the session is only sitting at an empty prompt, when it can and the session's wakes are not used up. If the session is still waiting `stall_after` later, it adds an inbox item saying so, once, and parks the task. The item says whether afk typed the command, or why it did not.
- If the session has stopped answering, it is restarted. If its host process is gone (a reboot, a crash), the session is started again. Each resumes the same conversation, and afterwards afk types the session's resume command so it carries on. See "Recovering a session".
- If nothing is running and the project is not paused, it picks the next ready task (the same answer as `afk next`), checks that this machine can start the agent unattended, and starts a session for it. The session and the branch are both named after the task: `T4.3` becomes `t4-3`.
- If nothing can start, it says why (waiting on you, blocked by a failed task, or all done) and sends one push notification for that, not one per pass.

Stopping the daemon (ctrl-c, or `systemctl --user stop afkd`) never stops a session. Every session keeps running, and the next start picks them up again, because everything the daemon knows is in files. Only one daemon can run on a machine; a second one exits with code 1.

**A new version of afk.** Between passes the daemon checks its own program file. When `make install` has put a new version there, it logs "afk daemon found a new version of itself on disk and is stopping so it can be started again. Every session is still running." and exits with code 0. It never leaves in the middle of a ship step, and it does not touch any session. Under the systemd unit in `contrib/` (`Restart=always`) it starts again by itself; a daemon you started by hand stays stopped until you start it again. `afk daemon --once` does not check. If the daemon cannot read its own program file when it starts, it says so in its log and carries on without the check.

**Recovering a session.** A session can stop answering without ending: the agent hangs, or the machine reboots and takes the session's host with it. For a session the daemon started, the session's host checks by itself, whether or not anyone is attached: after about 35 seconds of silence it asks the agent to repaint, and if that goes unanswered it records that the session is stuck. afk acts only once that has lasted a minute, so an agent that is merely slow is left alone. A session whose host has gone is started again after the host has been gone for a minute.

- afk makes at most `max_restarts` recoveries per task (default 3). The first happens at once and each later one after a wait that starts at `restart_backoff` and doubles. With the defaults that is at once, after one minute, after two minutes, and then the task parks.
- afk also types the session's resume command again, at most `max_wakes` times per task (default 3), when the agent's turn fails. This does not restart anything, and it does not count against `max_restarts`. See "When a turn fails" below.
- At the limit the task parks with an inbox item titled "Session could not be recovered: <task> (<session>)". Answering it gives the task a fresh set of recoveries, and afk types the session's resume command into it as it does for any answered item.
- A recovered session resumes its own conversation. About 30 seconds after the new agent started, afk types the session's resume command (`/afk:go --session <name>`), and the session carries on from its files. A session whose status file could not be read, or that had not yet recorded its conversation, comes back as a fresh conversation that picks its work up from its files.
- A pause does not stop the recovery of a task that is running. Sessions that are parked or shipping are not recovered.
- `afk attach` and the dashboard always work. If afk restarts a session while you are attached, your pane says the session was taken: attach again, and expect afk to type the resume command into it about half a minute later.
- Recovery needs the session to have been started by the daemon. A session you started by hand is not checked while nobody is attached, as before.

**When a turn fails.** Sometimes the agent's turn ends on an error that trying again may fix, such as a lost connection or a fault at the agent's service. The plugin's hook then writes `.afk/<session>/.turn-failed`, and the daemon takes it from there. A rate limit is not one of these: it is handled by the usage pause.

- If the session's host is still running and its agent is answering, afk types the session's resume command about 30 seconds after the marker was written. Nothing is restarted, and the session carries on from its files.
- A second wake waits at least one minute after the first, and a third at least two minutes after the second. The wait doubles from `restart_backoff`, the same as for recoveries.
- afk wakes a session at most `max_wakes` times per task (default 3; `0` means it never wakes one). A session that still fails after that parks its task with an inbox item titled "Session kept failing: <task> (<session>)". The item says what the error was, and what to do: a fault at the agent's service usually passes by itself, while a message about your login or account has to be fixed first.
- Answering that item gives the task a fresh set of wakes, and afk types the session's resume command into it, as for any answered item. If the session is beyond saving, `afk clean <session>` and then `afk ledger set <task> pending` start the task again in a new session.

**Usage.** The daemon asks the usage governor (see [`afk usage`](reference.md#afk-usage--show-how-much-of-your-usage-is-spent)) before it starts anything, and while a task runs. What it does for each governor action:

- `run` — Nothing changes. Tasks start and sessions carry on.
- `no_launch` — Nothing new starts: no task is launched, created again or started again. A session that is running carries on, and so does a parked one that is answered. `afk daemon status` says why.
- `hold` — The reading is missing or too old, so the daemon does not know. Nothing new starts, and a running session is left alone. The daemon sends one notification. If it does not clear, read the daemon's log: a usage folder or file that cannot be used is named there once.
- `pause` — A window is at or past `pause_at`. The daemon stops the session's agent and waits for the reset (see "A pause" below).
- `refuse` — Extra usage is switched on for the account. The daemon starts nothing, stops a session that is running (an agent left running could spend real money), and adds one blocking `failure` item to the inbox of every project, "Extra usage is switched on, so afk starts nothing", with an urgent notification. Switch extra usage off. The daemon reads usage again, answers the item itself and carries on.

**A pause.** The daemon saves the pause in the task's ledger entry (`usage_pause_at` and `usage_wake_at`), then asks the session's host to end its agent, the same request `afk clean` sends. The task stays `running`. A paused session is not recovered, not prompted and not reported as stalled. If the host is still there a minute later, the daemon sends it a terminate signal and waits up to ten seconds, and repeats that for two minutes. After three minutes it adds a blocking item, "Session could not be stopped for usage: <task> (<session>)", saying the session may still be spending, and the task parks. Attach and quit it by hand; `afk attach` keeps the saved pause.

The session starts again when a reading taken after the pause began says there is room (`run` or `no_launch`), the saved wake time has passed, the old host is gone, and the agent is not logged out. The clock alone never decides. A new host runs the agent's resume command, so the conversation is the same one, and about 30 seconds later afk types the session's resume command (`/afk:go --session <name>`). This does not count as a restart. Work in progress at the pause (the one call in flight, a command that was running) is lost and done again. If the new host cannot be started, the pause is dropped and ordinary recovery takes over, with its limit.

Someone attached to a session when it is paused sees it taken over and then ended. `afk dash` shows a paused session as one whose agent is not running, the same as any session whose host has gone. A session you started by hand is never paused or held back: the governor looks only at tasks the daemon runs.

**The rate-limit marker.** When the agent stops at a usage limit, the plugin writes `.afk/<session>/.rate-limited`. The daemon reads it on a pass with a running task. A marker newer than the session's start and last restart pauses the session, with no wait for the reading to reach `pause_at`. The wake time is the governor's own when it has one, otherwise one minute after the next reset of any usage window (the soonest one still ahead), otherwise one hour. The wake time is only when afk looks again: if the limit that was hit has not reset by then, the new reading keeps the session paused.

**The failed-turn marker.** When the agent's turn ends on an error worth trying again, the plugin writes `.afk/<session>/.turn-failed` with the error's name in it. The daemon reads it and types the session's resume command, a limited number of times, as described under "Recovering a session". The marker is removed when the session takes its next prompt or finishes a turn. A rate limit never writes this marker.

**The login check.** The daemon checks that the agent is still logged in, about once an hour, and every `probe_interval` while it is logged out, so that logging in again is noticed soon. A check that cannot run keeps the last answer: not knowing is not the same as logged out. The check cannot warn you ahead of time, because the agent's status output has no expiry date; you learn of it when the login has ended. Logged out has two effects:

- Nothing new starts, and a paused session stays paused. A session already running is left alone.
- The daemon adds one blocking `failure` item, "The agent is logged out on this machine", to every project, with an urgent notification. Log in on this machine and the daemon answers the item on its next check.

**When it is checked.** Usage and the login are looked at only on a pass where something is running or could start: a running task, or one about to be launched, created again or un-parked, or while one of the two items above is open. An idle project causes no checks. A reading younger than `probe_interval` is reused, and a failed read is not tried again for a minute.

**Linux only.** The forced stop needs `/proc` to be sure it is signalling the right host. Elsewhere a session that ignores the polite stop is reported after three minutes, as above.

**Pausing.** `afk pause [project]` stops the daemon from starting new tasks in a project; a task that is already running carries on and finishes. `afk resume [project]` lets it start again. See [`afk pause` and `afk resume`](#afk-pause-and-afk-resume--hold-and-release-the-daemon). Neither needs the daemon restarted.

**What parks a task.** When the agent adds a blocking inbox item for a task (a credential to set, a choice only you can make), the daemon marks the task `parked` and leaves its session alone. It is waiting for you, so it is not counted as stalled. To un-park it, answer the item:

```bash
afk inbox                          # find the item
afk inbox answer <id> <text>       # answer it
```

On its next pass the daemon sees every item answered and marks the task `running` again. A stall report, a "Session stopped running" report, a "Session could not be recovered" item and a "Session kept failing" item park the task in the same way: the task is `parked` on the item, and answering it puts the task back to `running` and types the session's resume command into the session. A "Session waiting at the terminal" item means the agent is asking a person something, so attach and answer it first (`afk attach <session>`). By then afk has usually typed the resume command into the session once already, and the item says whether it did and what it saw. A task whose status file cannot be read is parked on an open blocking item too, where it used to wait.

**Shipping.** When a session finishes its task, the daemon takes the work the rest of the way. The task stays `shipping` and the daemon does these in order, saving where it got to after each step, so a restart or a crash never pushes, opens or merges twice:

1. It checks the session left nothing uncommitted and that its branch has a commit the base branch does not.
2. It pushes the branch (never a force push).
   If GitHub refuses the push because afk's login over https may not change workflow files (`.github/workflows/`), the inbox item says so and gives two ways out: run `gh auth refresh -s workflow`, or switch the repository's `origin` to SSH. afk changes neither itself. Answer the item and it pushes again.
3. It opens a pull request against the base branch, titled from the branch's last commit.
4. It waits for the pull request's checks, asking GitHub at most every 20 seconds.
5. Red checks are re-run, up to `ci_reruns` times.
6. Still red, or the branch conflicts with its base: it hands the failure back to the session, up to `ci_fix_cycles` times. It writes what failed to `ci-failure.md` in the session's `.afk/<session>/` folder and types one line into the session telling it to read the file and commit a fix. It does not tell the session to push. The daemon pushes the new commit itself once the session has been quiet for two minutes, and the checks run again.
7. When every check has passed, and the diff adds no match for `required_env_pattern`, it merges the pull request with `merge_method`.
8. After the merge it brings your **project checkout's base branch up to date**: it fetches and fast-forwards the local base to GitHub (never a merge commit, never a reset). The next task is cut from that branch, so it holds the work just merged. It also deletes the merged branch on GitHub.
9. If the project's `afk.toml` sets `after_merge`, it runs that command once, in the project's main checkout (see [`[ship]`](configuration.md#ship-in-afktoml--how-the-daemon-ships-a-finished-task)). It runs only when the base branch update and the branch delete are done.
10. If `health_url` is set and `deploys` is true, it asks that address until it answers with a 2xx status, or until `deploy_timeout` has passed since the merge, or since the command finished when there was one. Without a `health_url` there is no deploy check.
11. It removes the finished session (the same clean-up as `afk clean`) and marks the task `done`. The next task can then start.

**A pull request with no CI checks is never merged.** If GitHub shows no check on the pull request five minutes after it opened (or only skipped ones), the task parks with an item titled `No checks ran on the pull request for <task>`. It asks you to add a CI workflow that runs on pull requests, or to merge the pull request yourself and run `afk ledger set <task> done`. Answering the item only makes afk wait another five minutes for checks.

**What parks a shipping task.** The same rule as above applies: the daemon raises a blocking inbox item that says what happened and what to do, and the task is `parked` until you answer. It parks when: the GitHub tool is missing or logged out; the session left uncommitted changes or committed nothing; a push, a pull request, a re-run or a merge is refused; the pull request was closed without merging or has vanished; the branch is gone from GitHub or has commits the session does not; no checks ran (above); the session made no commit within an hour of a hand-back; a red CI is still red after `ci_fix_cycles` hand-backs; the diff adds something `required_env_pattern` matches (the item names the variables, and you set them before you answer); your local base branch cannot be fast-forwarded (for example because it is checked out in another worktree); or GitHub could not be read for 30 minutes. **Answering the item continues the ship from where it stopped, with a fresh set of attempts**: the re-run and hand-back counts go back to zero and the timers start again.

**A hand-back or an answered item types into the session.** The daemon has only the session's one terminal to type into, so it takes the session from whoever has it. If a dashboard pane or `afk attach` is open on that session, it is told the session was taken over (for about half a second) and you must open it again. Do not leave an attach open on a session the daemon is shipping if you do not want it interrupted.

**The stopped line.** If the deploy does not answer healthy within `deploy_timeout`, or the `after_merge` command fails (it ends with a non-zero exit code, cannot be started, or runs past `after_merge_timeout`) or is cut off (afk was stopped while it ran, so nobody knows whether it finished), the merge has already happened, and the daemon stops the whole project: it writes `.afk/build/stopped`, raises a `failure` item and sends the notification at urgent priority. No new task starts there, and `afk daemon status` shows a `Stopped:` line. Check the deploy, fix it or roll it back, then run `afk resume`. For a command that failed or was cut off, read `.afk/build/after-merge.log`, fix what went wrong and run the command yourself if it still needs running. That clears the stopped line and any pause together, and the task is marked `done` on the next pass. `afk resume` does not run the command again, whatever the reason for the stop. Answering the item does not clear the stop; only `afk resume` does.

**A pause does not stop a ship that is in progress.** `afk pause` holds back new tasks, and a task being shipped carries on to the end (push, pull request, merge and clean-up).

**Owner tasks.** A task whose heading title carries `(owner)` or `(owner; …)`, such as `T0.0 — Stand up the VM (owner; not an agent task)`, is yours to do. The daemon never starts an agent for it. It raises one blocking inbox item, `Your task: <id> — <title>`, and the task is `parked`. Tasks that depend on it wait. When the work is finished, answer the item (`afk inbox answer <id> done`); the answer marks the task `done`, with no session, branch or review. A title that says `(owner + agent)` is an ordinary task: an agent does it with your help.

**Open questions.** The daemon reads the plan's Open questions table (the first table under that heading with a "Needed by" column). When the next ready task is named in the "Needed by" cell of an open row, the daemon starts nothing for it. It raises one blocking inbox item per open row, titled `Open question: <the question>`, and the task is `parked` with no session. This holds owner tasks too, and the question is asked while the project is paused. Answer with `afk inbox answer <id> "<your answer>"`: the daemon writes `**Answered <date>:** <your answer>` into the row's Question cell (through the same guarded change as `afk plan patch`) and the task goes back to `pending`, so it starts on a later pass. A task named by two open rows gets two items and waits for both; a row needed by two tasks is asked once. A row whose "Needed by" names no task holds nothing. If you answer the row yourself in the plan, the daemon closes its item with "answered in the plan". If the answer cannot reach the plan in the project folder, the daemon raises a second blocking item, `Put the answer in the plan: <task>`, and the task waits on it. With `[planning] via_pr` set, the change is a pull request: merge it and pull the base branch in the project folder. Otherwise afk could not make the change (for example the row appears twice), and the item gives the exact text to write into the Question cell. Answer that item when you are done and the daemon looks again. Without a readable plan the daemon starts nothing and waits.

**A project with no ledger.** If `.afk/build/ledger.json` does not exist, the daemon starts nothing and raises one blocking item, `This project has no task list yet`, with the `afk ledger init` commands for a new project and for a part-built one. It raises it once, not on every pass. You do not need to answer it: the daemon closes it by itself once the ledger exists.

**Unattended agents.** Nobody can answer a permission prompt, so the daemon starts each agent with permissions bypassed (for Claude Code, `--permission-mode bypassPermissions`). The agent can then run any command in its worktree without asking, so run the daemon only on a machine and a project you are happy to give that to. Before it starts a task, the daemon checks that:

- it is not running as root (the agent refuses this mode for root);
- you have run the agent once by hand in that mode and accepted its warning, so that it is remembered in `~/.claude/settings.json`;
- the afk plugin is installed for the agent.

If a check fails, the daemon adds a blocking `failure` item to your inbox that says what to do, and starts nothing until you answer it. A provider with no unattended mode afk knows (Codex and OpenCode today) cannot be used with the daemon.

**What it writes.** In each project's main checkout: `.afk/build/daemon.json` (what it last did and why), `.afk/build/paused` (the pause flag), `.afk/build/stopped` (the stopped line, with the task and the reason), `.afk/build/after-merge.json` (the record of the `after_merge` command: the task, the merge commit, when it started and finished, its exit code and whether it timed out) and `.afk/build/after-merge.log` (the command's output, replaced on each run), `.afk/build/ship-poll` (when GitHub was last asked, so a one-second tick does not make a call each second, and when its reads started failing), `.afk/build/ledger.json` (each task's status) and `.afk/log/decisions.jsonl` (one line for each decision). Each unattended session also has an empty file, `.afk/<session>/unattended`, that keeps the unattended mode on when its host restarts the agent. In afk's state folder, `daemon.lock` and `running.json` enforce the one-daemon rule, and `governor.json` holds when the daemon last tried to read usage (`probe_tried_at`) and what the last login check found (`login_checked_at`, `login_known`, `logged_in`). `plan-waits.json` records which planning waits a notification has been sent for, one entry per project, so a restart does not send the same one again. `usage.json` also gains a `governor` object: the decision (`action`, `reason`, `wake_at` when there is one, and `at`), stored with the reading by the daemon and by `afk usage`. The pause adds `usage_pause_at` and `usage_wake_at` to a task's entry in `ledger.json`; both are cleared when the task starts again, finishes or leaves `running`. The daemon logs to stderr, one line each with a time; under systemd that is the journal. A hand-back writes `ci-failure.md` in the session's `.afk/<session>/` folder, which is removed with the session.

**`afk daemon --once`** does a single pass over every project and prints one line each: `<project>: <what it did> (<task>, session <session>): <why>`. It takes the same lock as the daemon, so it exits 1 while a daemon is running. Use it to try a setup, or from cron.

**`afk daemon status`** prints a line about the daemon (running or not, and since when). While the daemon is running and could not read its own program file, the next line says: "afk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install." Then comes a block for each project:

```text
The afk daemon is running (pid 4121, started Sun 11 Oct 08:02).

my-app (/home/you/code/my-app)
  Doing:   waiting
  Task:    T4.3, in session t4-3
  Why:     running T4.3; its status file cannot be read right now
  Since:   Sun 11 Oct 09:30 (12m ago)
  Running: session t4-3 has been in this state for 12m
  Tasks:   3 done, 1 running
```

When `usage.json` holds a decision, a line under the daemon's own lines says what the daemon is acting on and when it was made, for example `Usage: nothing new is started. A window is past the limit for starting new work. (as of Mon 12 Oct 09:30)`. It is printed also when no project is configured.

A paused project gets a `Paused:` line first. `Doing:` becomes `Last:` when no daemon is running, because then the file is only a record of how things were left. `Problem:` replaces `Tasks:` when the ledger cannot be read, and with nothing configured it prints how to add a project.

To run the daemon as a service on Linux, see [`contrib/README.md`](../contrib/README.md). To get a project ready on an always-on machine (its prerequisites, `afk.toml`, the ledger, the web page over a tunnel), see [`docs/runbook-vm.md`](runbook-vm.md).

**Exit codes:** `afk daemon` returns 0 when it was stopped, when it stopped itself because it found a new version of afk on disk, or when `--once` finished, 1 when `config.toml` could not be used or a daemon is already running, and 2 when the command was written wrongly. `afk daemon status` returns 0 when the report was printed, 1 when `config.toml` itself cannot be read, and 2 when the command was written wrongly.

---

## `afk project` — Choose the projects the daemon builds

```bash
afk project list
afk project add [--name NAME] [DIR]
afk project remove <name or DIR>
```

The daemon builds the projects listed as `[[project]]` in `config.toml` (see [configuration.md](configuration.md#configtoml--the-machines-settings)). `afk project` edits that list for you, so you do not have to open the file. Every other line of the file, comments included, is kept, and afk reads the result again before it saves; if it cannot be sure only one project changed, it writes nothing.

- `afk project list` — One line per project, `<name>  <path>`. With none listed, it says so and how to add one.
- `afk project add` — Lists a project. `DIR` is the current folder unless you give one, and from inside a session's worktree it lists the main checkout. The folder must be a git repository. `--name` sets the name it is known by; the default is the folder name. It creates `config.toml` (and its folder) if there is none. A folder that is already listed is not an error: it says so and changes nothing.
- `afk project remove` — Takes a project out of the list by name or folder. The project's own files and any session that is running are left alone.

The dashboard's all-projects screen does the same two jobs: `ctrl+] +` adds a folder and `ctrl+] -` removes the selected project, with the same refusals as `afk project add` and `afk project remove` (see [`afk dash`](dashboard.md#afk-dash--launch-the-dashboard)). The web page does the same two jobs with the same words: its **Add an existing project** and **Remove a project** buttons on the overview add a folder and remove a project, and a change shows on the page once the daemon next reads the list (see [the web page](web.md#the-web-page)).

A running `afk daemon` reads the project list again on every look (every tick), so it picks changes up within a few seconds and does not need a restart. The daemon does not read `[dash]`. The project list is the only setting it re-reads: the tick and everything under `[daemon]`, `[governor]`, `[web]` and `provider` are read once at the start, so restart `afk daemon` after changing any of them. If `config.toml` becomes unreadable while the daemon runs, it keeps the projects it had and logs the problem once. The web page shows a new project one look later than the daemon does.

**Exit codes:** 0 when done or there was nothing to do, 1 when the folder is not a repository, the name is taken, the project is not listed, or the config could not be read or written, 2 when the command was written wrongly.

## `afk pause` and `afk resume` — Hold and release the daemon

```bash
afk pause [project]
afk resume [project]
```

`afk pause` writes `.afk/build/paused` in the project's main checkout, so the daemon starts no new task there. A task that is running finishes, and a parked task is not started again until you resume. `afk resume` removes the file, and it also clears a stopped line (see "The stopped line" in the daemon section), printing which of the two it cleared. `project` is a project name from `config.toml` or a folder; with nothing given it is the current folder's main checkout, even from inside a session's worktree.

The Pause and Resume buttons on the daemon's web page (see [the web page](web.md)), and `ctrl+] P` and `ctrl+] U` in `afk dash`, do the same thing.

**Exit codes:** 0 when done (resuming a project that was not paused or stopped is still 0), 1 when the project or the config could not be used or the file could not be written or removed, 2 when the command was written wrongly.
