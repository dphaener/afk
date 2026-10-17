# Runbook: a project on the VM

## What this is

The steps that take an always-on Linux machine from "afk runs here" to "afk can
build this project". The machine in mind is the VM: Ubuntu 26.04, with afk
already running as a service (see [`contrib/README.md`](../contrib/README.md)).

The worked example throughout is **nebari**, a Rails app at `dphaener/nebari`
whose remaining tasks are in its `docs/PLAN.md`. For another project, keep the
order of the sections and swap the nebari details (its packages, its checks,
its `afk.toml`) for your own.

Do the sections in order. Section 8 is the one that starts unattended work, so
it comes last on purpose.

Who does what:

| Section | What | Who |
|---|---|---|
| 1. Before you start | Check afk and the daemon are in place | Machine step (read-only checks) |
| 2. Project prerequisites | Install the system packages the project needs | Machine step (needs `sudo`) |
| 3. Get the project and run its checks | Clone, trust the folder, run `bin/ci` | Machine step; the trust question is yours |
| 4. The capped API key | Create a key with a spend cap and store it | Owner step |
| 5. Prepare the project for afk | `afk.toml`, `.gitignore`, CI runner, ledger | Machine step, on a local branch |
| 6. Owner checks before the first unattended run | Backups, auto-deploy, the pull request, the phone check | Owner step |
| 7. The web page over a tunnel | Turn the page on and reach it from a phone | Owner step |
| 8. Add the project to the daemon | The `[[project]]` lines | Owner step, at the supervised first run |

A "machine step" is one an agent session can do on the VM without you. An
"owner step" needs your accounts, your phone or your judgement, and nothing
here tries to do it for you.

## 1. Before you start

These were set up when the VM was built and when `afkd` was installed. This
section only checks them. If one is missing, [`contrib/README.md`](../contrib/README.md)
has the install steps for the service, and the main [`README.md`](../README.md)
has the rest.

What should already be true:

- afk is installed with `make install`, so the binary is at `~/bin/afk`.
- The agent CLI is installed and you have logged in to it by hand, at its own
  prompt.
- Extra usage is switched **off** on the agent account. With it on, afk starts
  nothing.
- You have run the agent CLI once by hand in its unattended mode and accepted
  the warning.
- The afk plugin is installed: `afk plugin install --target claude`.
- `afkd` is enabled as a user unit, and linger is on so it runs when you are
  logged out.
- The unit's `PATH` line finds the agent CLI, `git`, `gh` and the project's
  language tools. A user unit does not read your shell profile, so a tool your
  shell finds can still be missing for the daemon. For nebari the language tool
  is Ruby, which on the VM comes from mise, so the line needs
  `%h/.local/share/mise/shims`.

Check each one:

1. The service is running:

   ```bash
   systemctl --user status afkd
   ```

   Expect `active (running)`.

2. The daemon says what it is doing:

   ```bash
   afk daemon status
   ```

   Expect a first line saying the daemon is running, then a block for each
   project it already builds.

3. afk can read usage:

   ```bash
   afk usage
   ```

   Expect a line for each usage window and a last line saying what the
   governor would do. If it says extra usage is on, switch it off before going
   further.

4. Linger is on:

   ```bash
   loginctl show-user "$USER" -p Linger
   ```

   Expect `Linger=yes`.

5. The daemon's `PATH` has what the project needs:

   ```bash
   systemctl --user show afkd -p Environment
   ```

   Expect a `PATH=` value that lists `~/bin`, the folder the agent CLI is in,
   and (for nebari) the mise shims folder. If something is missing, see the
   last row of the table in section 10.

## 2. Project prerequisites (nebari)

These commands were run from this page on the VM (Ubuntu 26.04.1, amd64) on
2026-10-12, in this order, and each one worked as written. The versions quoted
are the ones that day's run installed.

Nebari needs four things on the machine: Ruby, PostgreSQL, a few system
libraries, and a headless Chrome. Step 6 is not one of nebari's needs but
afk's: the libraries bring a program along that afk mistakes for git-spice.

Two things to know about `apt-get` on this machine before you start:

- Each install ends with a short report that can include `User sessions
  running outdated binaries` and name `afkd.service`. It is information only.
  Nothing needs restarting, and you should not restart `afkd` because of it.
- An agent session has no terminal to answer a package's question in. None of
  these packages asked one, but a session should still put
  `DEBIAN_FRONTEND=noninteractive` after `sudo` (as in
  `sudo DEBIAN_FRONTEND=noninteractive apt-get install -y ...`) so a question
  takes its default instead of waiting for ever. That is how they were run.

It needs **no Node and no Redis**. Its JavaScript is served through import
maps, and Tailwind comes as a ready-built program inside a gem, so nothing is
compiled with Node. Its job queue, cache and live updates are all kept in
PostgreSQL (Solid Queue, Solid Cache and Solid Cable), so there is no Redis to
run.

1. **Ruby 4.0.7.** It is already on the VM through mise, and nebari's
   `.tool-versions` asks for exactly that version. Check from inside the
   project folder once it is cloned (section 3):

   ```bash
   ruby -v
   ```

   Expect `ruby 4.0.7`. On the VM it printed that straight after the clone,
   with nothing to install. If mise says the version is not installed, run
   `mise install` in the project folder. Do not change the global mise config
   for one project.

2. **System libraries.** These are what nebari's gems build against and what
   its photo code calls:

   ```bash
   sudo apt-get update
   sudo apt-get install -y build-essential pkg-config libpq-dev libyaml-dev libvips42t64 libheif-plugin-libde265
   ```

   - `build-essential` and `pkg-config` compile the gems that have C code.
   - `libpq-dev` is for the `pg` gem and `libyaml-dev` for Ruby's YAML reader.
     Ubuntu's `libpq-dev` is version 18 while the server in the next step is
     17. That is fine: the newer client library talks to the older server,
     and every spec passed against it.
   - `libvips42t64` is libvips (8.18.0), which resizes photos. Nebari's CI
     asks for it by the name `libvips`; on Ubuntu 26.04 that name is only an
     alias, and `apt-get install libvips` picks this same package.
   - `libheif-plugin-libde265` lets libvips read HEIC photos from a phone.
     Without it the HEIC examples in the specs fail.

   Check libvips with `dpkg -s libvips42t64`. There is no `vips` command to
   run: that is a separate package (`libvips-tools`) nebari does not need.

3. **PostgreSQL 17.** Production and CI both run 17, so the VM should too.
   Ubuntu 26.04 carries PostgreSQL 18 and has no `postgresql-17` package, so
   17 comes from the PostgreSQL project's own package repository. Check first:

   ```bash
   apt-cache policy postgresql-17
   ```

   If that prints no `Candidate:` version, add the repository and install:

   ```bash
   sudo apt-get install -y postgresql-common
   sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
   sudo apt-get install -y postgresql-17
   ```

   The second line is a script that ships with `postgresql-common`. It adds
   the repository and its signing key (as
   `/etc/apt/sources.list.d/pgdg.sources`) and ends with `You can now start
   installing packages from apt.postgresql.org.` The repository has packages
   for Ubuntu 26.04: after the script, `apt-cache policy postgresql-17` shows
   a candidate such as `17.11-1.pgdg26.04+2`.

   Installing `postgresql-17` creates a database server on port 5432, starts
   it, and brings the `psql` program with it. Check the server:

   ```bash
   pg_lsclusters
   ```

   Expect one line: version `17`, cluster `main`, status `online`.

4. **A database role with your Linux user's name.** Nebari's
   `config/database.yml` names no user and no password for development and
   test, so PostgreSQL uses the name of the Linux user running the command.
   That role must exist and be allowed to create databases:

   ```bash
   sudo -u postgres createuser --createdb "$USER"
   ```

   Check it:

   ```bash
   psql --version
   psql -d postgres -c 'select 1'
   ```

   Expect a version starting with 17 (`psql (PostgreSQL) 17.11` on the VM),
   and a one-row table holding `1`. Nothing else is needed for the database:
   no password, no `DATABASE_URL`. Section 3's set-up step creates the
   `nebari_development` and `nebari_test` databases itself.

5. **A headless Chrome.** Nebari's system specs drive a real browser through
   Cuprite. Use Google's `.deb`, not the `chromium-browser` package: on
   Ubuntu that package is only a wrapper that installs a snap, and a snap
   browser is awkward to start from a service.

   ```bash
   curl -fsSLo /tmp/google-chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
   sudo apt-get install -y /tmp/google-chrome.deb
   google-chrome --version
   rm /tmp/google-chrome.deb
   ```

   Expect a line starting `Google Chrome` (`Google Chrome 155.0.8059.39` on
   the VM). The program is `/usr/bin/google-chrome`, which points at
   `/opt/google/chrome/google-chrome`.

   Nothing has to be told where the browser is. Cuprite starts it through the
   Ferrum gem, and Ferrum (0.18.0, in `lib/ferrum/browser/options/chrome.rb`)
   tries these names on the `PATH` in order: `chrome`, `google-chrome`,
   `google-chrome-stable`, `google-chrome-beta`, `chromium`,
   `chromium-browser`, `google-chrome-unstable`. To use a different browser,
   set `BROWSER_PATH` to its full path; Ferrum reads that first.

   The package also adds Google's repository
   (`/etc/apt/sources.list.d/google-chrome.sources`), so a later
   `apt-get upgrade` moves Chrome to a newer version. A system spec that
   passed one week can fail the next for that reason alone.

   Run the specs with `CI=true`. Nebari's `spec/support/cuprite.rb` passes
   `no-sandbox` to Chrome only when `CI` is set, and that is how nebari's CI
   runs them. On this VM it turned out to be optional: a system spec file
   also passed with `CI` unset, because Ubuntu ships an AppArmor profile
   (`/etc/apparmor.d/chrome`) that lets a Chrome installed at
   `/opt/google/chrome` start its own sandbox. Keep `CI=true` anyway, so the
   VM runs the specs the way CI does.

6. **git-spice under its full name.** Step 2 also installs Ghostscript, and
   that clashes with afk. `libvips42t64` depends on ImageMagick's library,
   that library recommends the `ghostscript` package (it uses Ghostscript for
   PDF and PostScript files), and apt installs recommended packages by
   default. Ghostscript's program is `/usr/bin/gs`. git-spice's historical
   short name is `gs` too, and afk looks for git-spice under two names,
   `git-spice` first and `gs` second. With no `git-spice` on the PATH, afk
   takes Ghostscript for git-spice: afk's own `make check` then fails in
   `internal/spice` with `Unknown switch --no-prompt` (section 10 has the
   row), and that is how this was found on the VM.

   The fix is to have a program named `git-spice` in `~/bin`, which comes
   before `/usr/bin` on your PATH and on the daemon's (its drop-in lists
   `%h/bin` first; check with `systemctl --user show afkd -p Environment`).
   Then the `gs` name no longer matters, whichever program answers to it.
   These are the commands that were run on the VM on 2026-10-12. They take
   the official release (0.31.2 that day) from GitHub, check its checksum,
   and put the binary in place; the tarball's binary is already named
   `git-spice`:

   ```bash
   mkdir -p /tmp/git-spice && cd /tmp/git-spice
   gh release download v0.31.2 -R abhinav/git-spice -p git-spice.Linux-x86_64.tar.gz -p checksums.txt
   grep git-spice.Linux-x86_64.tar.gz checksums.txt | sha256sum -c -
   tar -xzf git-spice.Linux-x86_64.tar.gz
   install -m 755 git-spice ~/bin/git-spice
   cd ~ && rm -r /tmp/git-spice
   which -a git-spice gs
   git-spice --version
   ```

   Expect `sha256sum` to say `OK`, `which` to list `~/bin/git-spice` first,
   and `git-spice 0.31.2` in the version line. In afk's own repository,
   `go test -count=1 ./internal/spice/` now passes.

   Two things about the VM's state that you do not need to copy: on
   2026-10-12 the `ghostscript` package was also removed there
   (`apt-get remove ghostscript`), and git-spice was added through mise with
   its program renamed to `gs` (`~/.config/mise/config.toml`). Neither is
   needed once `~/bin/git-spice` exists. Removing Ghostscript did not touch
   libvips, which does not link it, and a later `apt-get install` of any
   package that recommends Ghostscript can bring `/usr/bin/gs` back; with
   `~/bin/git-spice` in place that is harmless.

## 3. Get the project and run its checks

1. Clone the project next to afk:

   ```bash
   gh repo clone dphaener/nebari ~/code/nebari
   ```

   If `~/code/nebari` already exists, leave it and carry on. Never delete a
   checkout to start again; it may hold a ledger or unpushed work.

2. **Trust the folder once.** Start the agent CLI by hand in `~/code/nebari`,
   answer its "do you trust this folder?" question, and quit. A new repository
   stops at that question, and a session the daemon starts has nobody to
   answer it. This is yours to do; an agent must not answer it for you.

3. Run the project's checks:

   ```bash
   cd ~/code/nebari
   CI=true bin/ci
   ```

   This takes about 38 minutes on the VM, and about 31 of those are the
   system specs. `bin/ci` sets `CI=true` for its steps by itself, so the
   `CI=true` in front changes nothing; it is there so the line reads the same
   as the single-step commands below.

   **What to expect today:** every step passes except the last. `Tests:
   RSpec` reports `7209 examples, 1 failure, 2 pending`, so `bin/ci` exits
   non-zero. The one failure is `spec/system/task_swipe_spec.rb:266`, which
   fails the same way in nebari's own CI and is not about this machine
   (section 9 has the details). The 2 pending examples save screenshots only
   when asked to and are skipped on purpose. Any other failure is new, and
   the table below says where to look.

`bin/ci` runs these steps in order (they are listed in nebari's
`config/ci.rb`). When it fails, the step's name tells you where to look. The
last column is how long the step took on the VM:

| Step | Command | What a failure usually means | Time |
|---|---|---|---|
| Setup | `bin/setup --skip-server` | Gems could not be built (a missing library from section 2), or the database could not be reached or created (PostgreSQL or the role) | 73 s the first time, when it installs the gems, creates both databases and loads 267 species |
| Style: Ruby | `bin/rubocop` | A style problem in the code | 7 s |
| Security: Gem audit | `bin/bundler-audit` | A gem has a known security problem; this one needs the network | 1 s |
| Security: Importmap vulnerability audit | `bin/importmap audit` | A JavaScript package has a known security problem; needs the network | 2 s |
| Security: Brakeman code analysis | `bin/brakeman --quiet --no-pager --exit-on-warn --exit-on-error` | A security warning in the code | 14 s |
| Assets: Tailwind build | `bin/rails tailwindcss:build` | The stylesheet could not be built | 2 s |
| Tests: RSpec | `bundle exec rspec` | A spec failed. System specs (under `spec/system`) need Chrome; photo specs need libvips | 36 min: 5 for the 6266 examples outside `spec/system`, 31 for the 943 system examples, which drive a real browser |

To run one step by itself, run its command from the table in `~/code/nebari`.
For the specs, keep `CI=true` in front: `CI=true bundle exec rspec`. Running
the steps one at a time is the quicker way to find which one is broken, and it
is how they were checked on the VM: each step as its own command, never
`bin/ci` as a whole.

The specs can be run in pieces too, which matters for an agent session that
must not sit in one command for half an hour:

```bash
CI=true bundle exec rspec spec/models                    # one folder
CI=true bundle exec rspec spec/system/theme_spec.rb      # one file
CI=true bundle exec rspec spec/system/task_swipe_spec.rb:266   # one example
```

Every folder under `spec/` other than `spec/system` finishes in under three
minutes (`spec/requests` is the longest at 2 min 40 s, `spec/models` 1 min
20 s, the rest a few seconds each). `spec/system` does not:
`spec/system/accessibility_spec.rb` alone has 245 examples and takes about
seven minutes. A system example takes about two seconds, so to stay under
four minutes run about 65 examples at a time. `CI=true bundle exec rspec
spec/system --dry-run --format json` lists every example's id without running
anything, and rspec accepts ids as `'spec/system/accessibility_spec.rb[1:1:1,1:1:2]'`.

Two runs of the specs must not overlap on one machine. They share the
`nebari_test` database. Keep other heavy work off the machine while the
system specs run, too: on 2026-10-12 one accessibility example failed while
afk's own `make check` ran beside it, and the same specs pass on a quiet
machine. A system example that fails only under load is a timing failure,
not a broken page; run it again by itself.

The specs need no API key. They replace the AI service with recorded answers
and set a made-up key for themselves.

## 4. The capped API key

Nebari calls an AI service through its own API key. That is separate from the
agent subscription afk builds with, and it costs real money per call. So the
key on the VM is a **separate key with a low spend cap**, used only for the few
commands that make a live call.

This is an owner step.

1. In the AI provider's console, create a new API key for the VM. Do not reuse
   the production key. Give it the lowest spend cap that still lets a smoke
   check run (a workspace with its own monthly limit is the usual way).

2. Put it in a file in your home folder that only you can read:

   ```bash
   mkdir -p ~/.config/nebari
   touch ~/.config/nebari/live.env
   chmod 600 ~/.config/nebari/live.env
   ```

   Open the file in an editor and give it one line:

   ```
   ANTHROPIC_API_KEY=<the capped key>
   ```

   Nebari reads the key from the plain environment variable and from nowhere
   else: not from Rails credentials, and not from a `.env` file (its
   `.gitignore` lists `.env*`, but nothing in the app loads one). The file
   lives outside the project because every afk session works in its own copy
   of the project, and a file inside one copy would not be in the next.

3. Use it for one command at a time. The brackets keep the key inside that one
   command:

   ```bash
   ( set -a; . ~/.config/nebari/live.env; set +a; bin/rails ai:smoke )
   ```

**Where the key must never go:** `afkd`'s unit file, its drop-in, your shell
profile, or anything else that is read when a shell or the daemon starts. The
agent CLI inherits that environment. A key it can see can move the agent off
your subscription and onto paid API calls, and afk's usage governor would not
see the spending. Check that it is in neither place:

```bash
systemctl --user show afkd -p Environment | grep -c API_KEY
env | grep -c API_KEY
```

Expect `0` from both.

**What may use it.** Only live calls, and each one is logged:

- `bin/rails ai:smoke`, which checks the AI wiring with a few small calls.
- `bin/rails nebari:smoke TO=<your address>`, which checks the production
  wiring. It needs the app's other settings too, so it is mostly for the app
  host.
- Recording a new fixture for a spec.

Nebari's `afk.toml` (section 5) tells every session to write each live call in
its `decisions.md`: the command, why, and the cost if known. Tests never need
the key.

## 5. Prepare the project for afk

Everything here happens on a local branch in `~/code/nebari`. Nothing is
pushed; section 6 is where you push and merge it.

1. Make the branch from an up-to-date `main`:

   ```bash
   cd ~/code/nebari
   git switch main
   git pull --ff-only
   git switch -c afk-setup
   ```

2. Create `afk.toml` in the project's top folder with exactly this:

   ```toml
   plan = "docs/PLAN.md"

   [ship]
   base = "main"
   deploys = true
   health_url = "https://nebari.haener.cloud/up"
   deploy_timeout = "15m"
   required_env_pattern = '^\s*[A-Z][A-Z0-9_]*:\s*\$\{([A-Z][A-Z0-9_]*):\?\}\s*$'

   [policy]
   notes = """
   Live API calls are allowed only for smoke checks and for recording fixtures, with the capped key on this machine. Log each one in the session's decisions.md: the command, why, and the cost if known.
   Never touch production: no Coolify action, no production database, no B2 bucket.
   """
   ```

   What each part does:

   - `plan` is where the task list is.
   - `health_url` is the address afk asks after a merge until it answers
     healthy, for at most `deploy_timeout`.
   - `required_env_pattern` catches a task that adds a new required
     environment variable. Nebari's `docker-compose.coolify.yml` writes each
     one as a line like `S3_BUCKET: ${S3_BUCKET:?}`, and a deploy fails until
     the variable is set in Coolify. When a branch adds such a line, afk parks
     the task before the merge and names the variable, so you can set it
     first.
   - `[policy] notes` are extra rules every session in this project follows.

   **Why the pattern is tied to the whole line.** afk tries the pattern on
   every line a branch adds, in every file, not only in the compose file.
   Nebari's documents mention `${VAR:?}` in ordinary sentences, and a pattern
   that only looked for `${NAME:?}` anywhere would park a task for a line of
   prose. This one matches only a line that is nothing but `NAME: ${NAME:?}`,
   which is the compose file's shape. The simpler example the main README used
   to give, `\$\{([A-Z_]+):\?\}`, has a second problem: it allows no digits,
   so it would miss every `S3_*` variable.

3. Add two lines to the end of `.gitignore`:

   ```
   # afk: session files and private settings stay on this machine
   .afk/
   afk.local.toml
   ```

   `.afk/` is where afk keeps the ledger, the inbox and each session's notes.
   `afk.local.toml` is the next step.

4. Create `afk.local.toml` next to `afk.toml`, in the main checkout, for the
   phone notifications. This is an owner step, because the topic is yours:

   ```toml
   [notify]
   ntfy_url = "https://ntfy.sh/<your-topic>"
   ```

   An ntfy topic works as a password: anyone who knows it can read and post to
   it. `afk.toml` is committed and this file is not, so the address goes here
   and never in `afk.toml`.

5. In `.github/workflows/ci.yml`, change `runs-on: ubuntu-latest` to
   `runs-on: ubuntu-26.04` on **all four jobs** (`scan_ruby`, `scan_js`, `lint`
   and `test`). The reason: afk only merges when CI is green, and each session
   checks its work on the VM first. With the same Ubuntu in both places, a
   pass on the VM should predict a pass in CI. `ubuntu-latest` is whatever
   GitHub currently points it at, which is an older release than the VM's.
   Put one comment above the first `runs-on` saying it is named so CI matches
   the VM. Leave the "Install packages" step and its comment about HEVC as
   they are: on 26.04, `libvips` resolves to `libvips42t64` and
   `libheif-plugin-libde265` exists, so the same line installs there.

   While you are in the file, confirm CI runs on pull requests. The top of the
   file should read:

   ```yaml
   on:
     pull_request:
   ```

   afk never merges a pull request that has no checks.

6. Commit on the branch. Do not push yet:

   ```bash
   git add afk.toml .gitignore .github/workflows/ci.yml
   git commit -m "chore(afk): Add afk config and pin CI to Ubuntu 26.04"
   ```

7. **Seed the ledger.** The ledger is afk's record of where each task stands.
   Nebari is part built, so afk has to be told what is already done. Tasks up
   to and including T4.3 are built:

   ```bash
   afk ledger init --project ~/code/nebari --done-through T4.3
   ```

   Expect `Created /home/<you>/code/nebari/.afk/build/ledger.json with 24 task(s) marked done.`

   One of those 24, T4.2, was never built: it was split into T4.2a and T4.2b
   and is kept in the plan only as history. Record that properly:

   ```bash
   afk ledger set --project ~/code/nebari --replaced-by T4.2a,T4.2b T4.2 superseded
   ```

   Expect `T4.2: done → superseded (replaced by T4.2a, T4.2b)`.

8. Check the result:

   ```bash
   afk tasks --project ~/code/nebari
   afk next --project ~/code/nebari
   ```

   `afk tasks` lists 40 tasks: everything through T4.3 is `done`, T4.2 is
   `superseded (by T4.2a, T4.2b)`, and the rest are `pending`. `afk next`
   prints:

   ```
   T4.4  Dashboard & calendar
   ```

   The ledger is in `.afk/`, which git now ignores, so it is not part of the
   commit and survives switching branches.

## 6. Owner checks before the first unattended run

Four things only you can do. Do all of them before section 8.

**(a) Database backups, and one restore drill.** Nebari's production database
has no backup yet. Its `docs/ops.md` says so in section 1, and the open item is
in its `docs/todo.md` under "Operations": install the backup script on the app
host, add the cron line, decide how the dumps reach Backblaze B2, then do the
restore drill in `docs/ops.md` section 3 and write its date in.

Why it matters now: every deploy runs the database migrations when the new
container starts. Until now you were watching when that happened. With afk,
a merge deploys at any hour with nobody watching, and a migration that goes
wrong has no backup behind it. A backup you have never restored is not yet a
backup, so do the drill once for real.

**(b) Coolify deploys by itself on a push to `main`.** Nothing in nebari's
repository tells Coolify to deploy: no workflow calls it, and nebari's deploy
guide says to press Deploy. So look in Coolify, on the nebari resource:

- Auto Deploy is switched on.
- The resource is connected through the GitHub App, or a webhook from the
  repository reaches Coolify. A deploy key by itself only lets Coolify read
  the code; it does not tell Coolify that something was pushed.

The pull request in (c) is a good test: merge it, then watch Coolify start a
deployment without being asked.

What happens if it does not deploy by itself: after a merge afk asks
`health_url`, the old container is still running and answers healthy at once,
and afk marks the task done. The task list then says done for work that was
never deployed. afk cannot tell the difference (see section 9).

**(c) Push the set-up branch and merge it.**

```bash
cd ~/code/nebari
git push -u origin afk-setup
gh pr create --base main --head afk-setup --fill
gh pr checks --watch
```

Expect all four jobs (`scan_ruby`, `scan_js`, `lint`, `test`) to pass on
`ubuntu-26.04`. **As of 2026-10-12 `test` will not pass, on any runner:**
nebari's `main` already fails it (the first entry in section 9). Fix that in
nebari first, or this pull request cannot go green and neither can any task
afk builds. Once it is fixed, if `test` fails on `ubuntu-26.04` and passed on
`ubuntu-latest`, the package install step in the workflow is the first place
to look. Then merge
and bring the checkout back to `main`:

```bash
gh pr merge --merge
git switch main
git pull --ff-only
```

The checkout must be on `main`, with this merge in it, before section 8. afk
reads `afk.toml` from the main checkout and cuts each task's branch from the
local `main`.

**(d) Nebari's T4.2b phone check.** T4.2b's code is merged and the ledger
counts it as done, but your check of the task screens on a real phone is still
open (the last T4.2b entry in nebari's `docs/decisions.md`). Do it before T4.4
starts, so a problem found on the phone is not built on top of.

## 7. The web page over a tunnel

The daemon can serve a small page that shows what it is doing, lets you answer
inbox items, and has Pause and Resume buttons. It is off until you set a
password. This is an owner step.

1. Set a password. On this VM, in a terminal, run:

   ```bash
   afk web password
   ```

   It asks for the password twice without showing it. It must be at least
   8 characters. afk keeps only a salted hash, in its state folder: never the
   password, and never in the config file.

2. Add a `[web]` table to `~/.config/afk/config.toml`:

   ```toml
   [web]
   bind = "127.0.0.1:4253"
   public_url = "https://<the address you open from your phone>"
   ```

   - `bind` stays `127.0.0.1:4253`, which only this machine can reach.
   - `public_url` is the address you will use from outside, with no slash on
     the end. Inbox notifications link to it.

   A `token` line left over from an older version is ignored, so delete it.
   The daemon logs one line saying so.

3. Restart the daemon so it reads the file. Running sessions keep running:

   ```bash
   systemctl --user restart afkd
   journalctl --user -u afkd -n 20
   ```

   Expect a line saying `web page: listening on http://127.0.0.1:4253`. The
   password is never written to the log.

4. Sign in once. Open the page in a browser:

   ```
   http://127.0.0.1:4253/
   ```

   It shows a sign-in form. Type the password from step 1 and press **Sign
   in**. The browser keeps a cookie for 180 days, so after that the page opens
   without asking. **Sign out** on the page's bar signs this browser out.

5. **Reach it from outside.** The page speaks plain HTTP and has no encryption
   of its own, so it must sit behind something that does. Which one is your
   choice. Two short examples:

   - **Tailscale**, with the bind left on loopback. On the VM:

     ```bash
     tailscale serve --bg 4253
     ```

     It prints an `https://` address on your tailnet that forwards to the
     loopback port. Put that address in `public_url`.

   - **A reverse proxy with TLS**, such as the Caddy you already run. The
     proxy is on another machine, so the page must listen on an address that
     machine can reach: set `bind` to the VM's LAN address and port, and
     point the proxy at it:

     ```
     afk.example.com {
         reverse_proxy <the VM's LAN address>:4253
     }
     ```

   **The rule:** a bind that other machines can reach needs a password. With
   none, `afk daemon` refuses to start and says so. Never put the page on the
   open internet as plain HTTP.

6. **Check it from your phone.** Open `<public_url>/` on the phone and sign in
   with the password. Then:

   - answer an inbox item from the page, and expect its task to carry on
     within a few seconds;
   - press Pause, see `afk daemon status` show a `Paused:` line for the
     project, then press Resume and see it go.

## 8. Add the project to the daemon

**This is the last step, and it starts unattended work.** Once the daemon
knows about the project it starts the next ready task by itself. Do it only
when sections 1 to 7 are done, and do it as part of the supervised first run,
when you are there to watch.

1. Hold the project first, so nothing starts until you say:

   ```bash
   afk pause ~/code/nebari
   ```

2. Add the project to `~/.config/afk/config.toml`:

   ```toml
   [[project]]
   path = "~/code/nebari"
   ```

3. Restart the daemon so it reads the file. Running sessions keep running:

   ```bash
   systemctl --user restart afkd
   afk daemon status
   ```

   Expect a `nebari` block with a `Paused:` line and a `Tasks:` line reading
   `23 done, 1 superseded`.

4. When you are ready to watch the first task, open the dashboard in one
   terminal and release the project in another:

   ```bash
   afk dash
   ```

   ```bash
   afk resume nebari
   ```

   The daemon starts T4.4 in a session named `t4-4`.

## 9. Known limits

- **Nebari's checks do not pass today, and that blocks every merge.** Found
  on 2026-10-12 at commit `4b193d1`. On the VM one example fails every time:
  `spec/system/task_swipe_spec.rb:266` ("a vertical drag scrolls the page and
  triggers nothing"), with `expected: > 1000, got: 900`. The cause is inside
  nebari, not this machine: the same example fails with the same numbers in
  nebari's own CI on `main`, on GitHub's `ubuntu-24.04` runner. The spec asks
  Chrome for a touch scroll (`Input.synthesizeScrollGesture` with
  `gestureSourceType: "touch"`) and the page does not move. Measured on the
  VM, the page has room to scroll and the same gesture sent as `"mouse"`
  moves it 240 px, so it is the touch gesture that does nothing in this
  Chrome (155). Why is not known. That CI run also failed two more examples
  that passed on the VM (`spec/system/accessibility_spec.rb[1:1:4]` and
  `spec/system/quick_log_spec.rb:504`), which looks like timing on a slower
  runner. What follows: afk merges only when CI is green, so until nebari's
  `main` is green again every task afk builds will stop at its pull request.
  Fixing the spec is a change to nebari and is the owner's call; nothing here
  changed nebari.
- **Nebari's full check run takes about 38 minutes on the VM**, 31 of them in
  the system specs. A session that runs `bin/ci` as one command waits that
  long for an answer. Section 3 shows how to run it in pieces.
- **The deploy check knows "healthy", not "this commit".** After a merge afk
  asks `https://nebari.haener.cloud/up` until it answers healthy. Nothing in
  nebari shows which commit is running, so afk cannot tell the new container
  from the old one. Two things follow. If the deploy has not started yet, the
  old container answers healthy and the task is marked done at once. And a
  deploy with a migration answers 503 for a short while first, which afk
  waits out. An address in nebari that shows the running revision would close
  this; it is raised as an idea, not built.
- **Three nebari tasks can be picked earlier than their place in the plan.**
  afk orders tasks by the ids on each task's "Depends on" line. T9.1 says
  "M3–M8 as available", which names milestones and no ids, so afk reads it as
  depending on nothing. T9.5 and T9.6 say "nothing new; it can run at any
  point", which is meant. `afk tasks` prints a note for each. If T9.1 should
  wait, write the task ids on its line.
- **The agent's login can run out with no warning.** afk checks about once an
  hour that the agent is still logged in, but nothing reports when the login
  will end. You find out when it has: the daemon starts nothing new and raises
  an item titled "The agent is logged out on this machine". Log in again on
  the VM and it carries on.
- **A line of code can look like a required variable.** The pattern in
  section 5 matches any added line that is exactly `NAME: ${NAME:?}`, in any
  file. If a task adds such a line somewhere other than the compose file, it
  parks once. Answer the item and it carries on. Checked against nebari on
  2026-10-12: the pattern matches all 14 required lines of
  `docker-compose.coolify.yml` plus the database service's
  `POSTGRES_PASSWORD` line (15 in all), and does not match the optional
  `NOMINATIM_CONTACT: ${NOMINATIM_CONTACT:-}` line. It matches no line of
  `docs/deploy-coolify.md`, `docs/PLAN.md` or `spec/deploy/compose_file_spec.rb`
  (the spec's lines are Ruby, such as `expect(...).to eq("${POSTGRES_PASSWORD:?}")`,
  so none is exactly `NAME: ${NAME:?}`). A task that adds a spec line of that
  exact shape would still park once.
- **Ghostscript and git-spice share the short name `gs`.** Installing libvips
  (section 2) brings the `ghostscript` package along, as a recommended
  package of ImageMagick's library, and its program is `/usr/bin/gs`. afk
  looks for git-spice as `git-spice` first and `gs` second, so on a machine
  with no `git-spice` on the PATH it takes Ghostscript for git-spice. Found
  on 2026-10-12, when afk's `make check` failed in `internal/spice`. The fix
  is section 2, step 6: a `~/bin/git-spice`, which afk finds first whatever
  `gs` is. Any later `apt-get install` of a package that recommends
  Ghostscript can put `/usr/bin/gs` back; that does no harm once
  `~/bin/git-spice` exists. afk does not warn about the clash; the test
  failure or a `gs --version` that prints `GPL Ghostscript` is how you see it.

## 10. If something goes wrong

| What you see | Why | What to do |
|---|---|---|
| An inbox item titled `This project has no task list yet`, and nothing starts | The project has no ledger file, so afk cannot tell a new project from a part-built one | Run the two `afk ledger` commands in section 5, step 7. The item closes by itself once the ledger exists |
| An inbox item titled `No checks ran on the pull request for <task>` | GitHub showed no CI check on the pull request within five minutes. The workflow does not run on pull requests, or the runner name in `runs-on` does not exist | Check the top of `.github/workflows/ci.yml` has `pull_request:` and the jobs say `runs-on: ubuntu-26.04`. Fix it on `main`, then answer the item; afk waits another five minutes. Or merge the pull request yourself and run `afk ledger set <task> done` |
| `afk daemon status` shows a `Stopped:` line and no new task starts | A merge happened, but the deploy did not answer healthy within `deploy_timeout`. afk stops the whole project so nothing is built on a broken deploy | Look at the deploy in Coolify. Fix it or roll it back. When `https://nebari.haener.cloud/up` answers healthy, run `afk resume nebari`. Answering the inbox item does not clear the stop; only `afk resume` does |
| A task parks with an item naming a variable such as `S3_BUCKET` | The branch adds a line that `required_env_pattern` matches: a new required variable in the compose file | Set the variable in Coolify, then answer the item. The ship carries on from where it stopped |
| `afk tasks: afk.toml: [ship] required_env_pattern does not compile as a regular expression (...)` | The pattern in `afk.toml` was mistyped. Until it is fixed the daemon does nothing for the project | Copy the pattern from section 5 again, inside single quotes. Double quotes change what the backslashes mean |
| System specs fail, and the error says the browser could not be started or could not be found | Chrome is not installed, is not under a name Ferrum looks for, or its sandbox cannot start. On this VM none of these happened: the specs start Chrome with `CI` set and with it unset, so there is no error text from here to quote | Run `google-chrome --version`. If that fails, redo section 2, step 5. If the browser is somewhere else, set `BROWSER_PATH` to its full path. If it is installed and still will not start, run the specs with `CI=true` in front, which starts Chrome without its sandbox |
| `bin/ci` fails at `Tests: RSpec` with exactly one failure, `spec/system/task_swipe_spec.rb:266`, `expected: > 1000, got: 900` | A known failure inside nebari, the same in its own CI. Not this machine | Nothing to fix on the VM. See the first entry in section 9 |
| A spec run stops part-way with no failure listed, or two runs give different database errors | Two spec runs overlapped (they share the `nebari_test` database), or a time limit cut the run short. rspec prints a normal-looking `0 failures` summary when it is stopped early, so check the exit code and the example count | Run one at a time, in pieces small enough to finish (section 3) |
| The web page answers 502 through the tunnel right after you added a project, and `systemctl --user status afkd` shows it restarting | `config.toml` is not valid: usually the new `path` line was put under the existing `[[project]]` header, so one block has two `path` keys. `journalctl --user -u afkd -n 20` shows `Key 'project.path' has already been defined` | Give each project its own `[[project]]` header, as in section 8. The daemon starts again by itself within 30 seconds |
| `apt-get` ends with `User sessions running outdated binaries` and names `afkd.service` | A library the daemon has loaded was updated. Information only | Nothing. Do not restart `afkd` for it; it picks the new library up the next time it is restarted for another reason |
| `bin/ci` stops at Setup with `connection to server on socket ... failed` or `role "<you>" does not exist` | PostgreSQL is not running, or there is no role with your Linux user's name | `systemctl status postgresql` (start it with `sudo systemctl start postgresql`). Then `sudo -u postgres createuser --createdb "$USER"`, and check with `psql -d postgres -c 'select 1'` |
| A session works in your terminal but fails under the daemon with `bundle: command not found` or `gh: command not found`; or a task parks with an item saying the GitHub tool is missing | The daemon's `PATH` does not include the folder that tool is in. A user unit does not read your shell profile | Find the folder with `command -v bundle` (on the VM, `~/.local/share/mise/shims`). Run `systemctl --user edit afkd`, and in the `[Service]` part set `Environment=PATH=...` with that folder added (write your home folder as `%h`). Then `systemctl --user restart afkd` and check with `systemctl --user show afkd -p Environment` |
| `make check` in afk's repository fails in `internal/spice` with `/usr/bin/gs --no-prompt repo init --trunk main: Unknown switch --no-prompt`, or `gs --version` prints `GPL Ghostscript` | The `gs` on the PATH is Ghostscript, installed with libvips (section 2), and there is no program named `git-spice`, so afk takes Ghostscript for git-spice | Install git-spice under the name `git-spice` in `~/bin` (section 2, step 6), then check that `which -a git-spice gs` lists `~/bin/git-spice` first. Removing the `ghostscript` package is not needed |
| A new session sits at "do you trust this folder?" and afk reports it waiting at the terminal | The project folder was never trusted in the agent CLI | `afk attach <session>`, answer the question, and detach. Section 3, step 2 avoids this |
| `afk usage` says extra usage is on, and the daemon starts nothing | Extra usage would let the agent spend real money past the subscription | Switch extra usage off on the agent account. The daemon reads usage again and carries on by itself |
