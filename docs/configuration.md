# Configuration

afk reads a few plain TOML files. You can edit each by hand.

| File | Where | What it holds |
|---|---|---|
| `config.toml` | `~/.config/afk/` | The machine's settings: the projects the daemon builds, the daemon's timings, the usage limits, the web page, the dashboard's search folders, and where new sessions run (`[runtime]`) |
| `profile.toml` | `~/.config/afk/` | How you build and host things, read by the planning session |
| `afk.toml` | each project's root folder, committed | Where the plan is, how a finished task ships, notifications |
| `afk.local.toml` | each project's root folder, never committed | Private project settings, kept on this machine |

## `config.toml` — The machine's settings

**Machine config.** The daemon reads `config.toml` in afk's config folder (`$XDG_CONFIG_HOME/afk/config.toml` when that is an absolute path, otherwise `~/.config/afk/config.toml`). It is the machine's only settings file: it also holds the dashboard's `[dash]` table, which sets its search folders, leader key and colour theme (see [Multi-project support](dashboard.md#multi-project-support)); the daemon ignores that table. The owner profile, `profile.toml`, lives in the same folder (see [`afk profile`](#afk-profile--how-you-build-and-host-things) below). A missing file is not an error, but with no `[[project]]` the daemon has nothing to build, says so once, and waits: it starts with no projects and picks up one added by `afk project add` or `afk plan approve`. `afk daemon --once` with no project prints that and exits 0. Keys it does not know are ignored.

```toml
# ~/.config/afk/config.toml
provider = "claude"            # the agent to run; leave out for the default

[daemon]
tick = "5s"                    # how often to look at each project (1s or more)
stall_after = "10m"            # how long a session may ask a question at the terminal
max_restarts = 3               # recoveries per task before it parks (0 means never restart)
restart_backoff = "1m"         # wait before the second recovery; each later one doubles it
max_wakes = 3                  # times a session is typed into after a failed turn, per task (0 means never wake one)

[governor]
pause_at = 90                  # percent of a usage window at which running work is stopped (above 5, at most 100)
launch_below = 75              # percent of the session window under which new work may start (above 0, at most pause_at)
stale_after = "30m"            # how old a usage reading may be before it is not trusted (must be longer than probe_interval)
probe_interval = "5m"          # how often the daemon reads usage again (1m or more)
on_unknown_usage = "run"       # "run" or "hold": what to do when the agent cannot report usage at all

[web]
bind = "127.0.0.1:4253"        # host:port the web page listens on (this is the default); the port is 1 to 65535
public_url = "https://afk.example"  # optional: where you open the page from; starts with http:// or https://, no ? or #

[runtime]
backend = "afk"                # where new sessions run: "afk" (the default) or "rex"

[[project]]
path = "~/code/my-app"         # an absolute path, or one starting with ~/
name = "my-app"                # optional; defaults to the folder name

[[project]]
path = "/srv/another-app"
```

The `[dash]` table, the dashboard's search folders, leader key and colour theme (`tide`, `fjord`, `paper` or `phosphor`), is described in [dashboard.md](dashboard.md#config-file).

### `[runtime]` — Where new sessions run

`backend` chooses where every **new** session runs: a task the daemon starts, a one-off from `afk new --scratch`, and a planning session.

- **`"afk"`** (the default, and what you get when the table or the key is left out) runs each session in afk's own background host. afk makes no call to Rex and does not need it.
- **`"rex"`** runs each session in a window in Rex, a terminal multiplexer that keeps its windows on a server you can reach from a laptop or a phone. Rex must be installed (its `rex` command on your `PATH`) and its server must answer. The windows and their labels are described in [the reference](reference.md#rex-sessions).

The words are lower case only: `"Rex"` or `"REX"` is a mistake, not a synonym. Any other word, or a value that is not text in quotes, is a problem with this file. afk then starts no new session, says which line is wrong, and raises an item in the inbox saying so. It never falls back to `"afk"` on its own, because you asked for Rex.

With `"rex"` and Rex missing or silent, a new session is not started either. The message says which it is: Rex is not installed, or it is installed but its server does not answer. Start Rex and try again, or set `backend = "afk"` to run sessions without it. The same message goes to the inbox as a blocking item, so the daemon does not keep trying until you answer.

The setting is read when a session starts. A session that already exists keeps the runtime that made it (its `host.json` says which), so changing this line never moves a running or stopped session to the other runtime.

---

## `afk profile` — How you build and host things

The owner profile says, once, how you build and host things: the database you use, the deploy tool you run, where new projects go. The planning session that `afk plan` starts reads it, so it asks you less and never guesses a setup fact. `afk profile` shows what it says.

The file is `profile.toml`, in the same folder as `config.toml`: `$XDG_CONFIG_HOME/afk/profile.toml` when `XDG_CONFIG_HOME` is an absolute path, otherwise `~/.config/afk/profile.toml`. You can edit it by hand. The commands below are the other way in.

```bash
afk profile [--kind KIND]
afk profile init
afk profile set <topic> <value> [--kind KIND] [--rule|--default] [--reason TEXT]
afk profile check
```

- `afk profile` — Lists what the profile says, one entry per line. `--kind` shows what applies to one kind of project, such as `web-app` or `cli`. With no file yet, it says where the file would be and that `afk profile init` writes a template, and it exits 0. If the file has a problem, it prints the problems instead of the entries and exits 1.
- `afk profile init` — Writes a commented template. Every example in it is commented out, so a new file says nothing until you uncomment one. It never overwrites a file: if one is there, it says so and leaves it alone.
- `afk profile set <topic> <value>` — Adds the entry for that topic, or changes it, and creates the file if there is none. `--kind` makes the entry apply to one kind of project, `--rule` makes it a rule, `--default` makes it a default, and `--reason` says why. A new entry is a default unless you pass `--rule`. A changed entry keeps its strength and reason unless you pass new ones. Every other line of the file, comments included, is kept. Comments on the lines of the entry being changed go with that entry. Before it saves, afk reads the file again and checks that only that entry changed; if it cannot be sure, it writes nothing.
- `afk profile check` — Lists every problem in the file, one per line as `file: error` or `file: warning`, then the message and a rule name, then a count. Errors exit 1; warnings alone exit 0. A `rule_files` path that does not exist is a warning.

An example file, with placeholder values:

```toml
# ~/.config/afk/profile.toml
rule_files = ["~/notes/house-rules.md"]

[[entry]]
topic = "database"
value = "PostgreSQL"
strength = "rule"
reason = "it is what I run"

[[entry]]
topic = "domain"
value = "example.com"
strength = "default"
kind = "web-app"
```

- `rule_files` — Documents that hold your house rules, each an absolute path or a path starting with `~/`. The profile commands list them and do not open them.
- `topic` — What the entry is about, such as `database`. Topics match ignoring capital letters and extra spaces, so `Database` and `database` are the same topic. There is one entry per topic and kind; a second one is an error.
- `value` — The answer, such as `PostgreSQL`. `"no preference"` means you have none: the planner should recommend one and say why.
- `strength` — `rule` must be followed. `default` is used unless the project says otherwise. A missing strength counts as `default`; any other word is an error.
- `kind` — Optional. Lower-case letters, digits and hyphens, such as `web-app` or `cli`. The entry then applies only to that kind of project. When you ask for a kind, an entry for that kind beats a general entry on the same topic, whatever their strengths.
- `reason` — Optional. Why you prefer the value.

Keys and tokens are refused. `set` refuses a value that looks like one, says which field it was and why, and saves nothing. `check` reports such an entry without printing the text. afk reads secrets from the environment, never from this file, so a key written here would sit in plain text in your config folder.

A walkthrough on a new machine (the paths are the defaults):

```
$ afk profile set database PostgreSQL --rule --reason "house rule"
Set database as a rule in /home/you/.config/afk/profile.toml
$ afk profile set database SQLite --kind cli
Set database [cli] as a default in /home/you/.config/afk/profile.toml
$ afk profile --kind web-app
Profile: /home/you/.config/afk/profile.toml
No entry is specific to web-app, so the general entries apply.
rule  database: PostgreSQL — house rule
```

**Exit codes:** 0 when the command is done, including when there is no file yet, 1 when the profile has a problem or could not be read or written, 2 when the command was written wrongly.

---

## `[ship]` in `afk.toml` — How the daemon ships a finished task

Every key is optional. Put them under `[ship]` in `afk.toml`, or in `afk.local.toml` (a key there wins). A key of the wrong type, a value out of range or a pattern that does not compile is an error that names the file and the key.

```toml
# afk.toml
[ship]
base = "main"
merge_method = "merge"
ci_reruns = 1
ci_fix_cycles = 2
deploys = true
health_url = "https://example.com/health"
deploy_timeout = "15m"
required_env_pattern = ""
after_merge = "make install"
after_merge_timeout = "10m"
```

| Key | Default | Meaning |
|---|---|---|
| `base` | the session's own base branch | The branch pull requests target and that is brought up to date after a merge. If it is empty and the session has no base either, the task parks rather than guessing. |
| `merge_method` | `"merge"` | How the pull request is merged: `merge`, `squash` or `rebase`. |
| `ci_reruns` | `1` | How many times red checks are re-run before the failure goes back to the session. `0` hands back at once. |
| `ci_fix_cycles` | `2` | How many times a red CI (or a conflict) is handed back to the session before the task parks. |
| `deploys` | `true` | Whether merging deploys. With `false` there is no deploy check. |
| `health_url` | `""` (none) | The address asked after the merge. It must start with `http://` or `https://`. Healthy is any 2xx answer. With no `health_url` the deploy is not polled, whatever `deploys` says. It is never printed in a message, because it may hold a password. |
| `deploy_timeout` | `"15m"` | How long after the merge the deploy may take to turn healthy, as a length of time above zero such as `"15m"` or `"1h30m"`. Past it, the line stops. |
| `after_merge` | `""` (none) | A shell command the daemon runs once after a merge, before the deploy check. It runs through `sh -c` in the project's main checkout, and its output goes to `.afk/build/after-merge.log`. A non-zero exit, a command that cannot be started, or running past `after_merge_timeout` stops the line (see "The stopped line"). **It runs unattended, with the daemon's permissions and its `PATH`**, so only put a command here that you would run yourself, and make sure every program it calls is on the daemon's `PATH`. |
| `after_merge_timeout` | `"10m"` | How long the command may run, as a length of time above zero such as `"10m"` or `"1h"`. Past it the command is stopped and the line stops. |
| `required_env_pattern` | `""` (no check) | A regular expression. If the branch adds a line that matches, the task parks before the merge with an item naming the variable (the first capture group of the match, or the whole match if there is no group). For example `\$\{([A-Z][A-Z0-9_]*):\?\}` catches a new required `${VAR:?}`. The pattern is tried on every added line of every file, so a project whose documents mention `${VAR:?}` may want a pattern anchored to the line's shape (the runbook has one). |

A pull request that has no CI checks is never merged (see "Shipping" in the daemon section). The timings that are not here (five minutes for checks to appear, an hour for a fix after a hand-back, two minutes of quiet before a push) are fixed.

## `afk.local.toml` — Private project settings

Put private values in `afk.local.toml`, next to `afk.toml` in the project's top folder. Use it for any `afk.toml` value you do not want committed, such as a `[ship]` command that names a private path.

afk reads `afk.toml` first and then lays `afk.local.toml` over it key by key: a key in the local file wins, and every key it leaves out keeps the committed value. Either file may be missing. This repo's `.gitignore` lists `afk.local.toml`, so it is never committed; add the same line to your own project's `.gitignore`.

A leftover `[notify] ntfy_url` line in either file is ignored without a word: afk no longer sends ntfy notifications, and phone notifications come from web push (see [the web page](web.md#notifications)).
