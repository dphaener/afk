# Running the afk daemon as a service

`afk install` writes the service file and starts it for you, along with the rest
of the machine setup: the unit below on Linux, a launchd agent on a Mac (see "On
macOS" further down). The steps here are the by-hand way, for a machine where
you would rather not use it.

`afkd.service` is a systemd **user** unit that keeps `afk daemon` running on a
Linux machine. It expects the `afk` binary at `~/bin/afk`, which is where
`make install` puts it. Install it as your normal user, not as root: the agent
CLI refuses its unattended mode for root.

1. Make the folder for user units:

   ```bash
   mkdir -p ~/.config/systemd/user
   ```

2. Copy the unit into it (run this from the root of the afk repository):

   ```bash
   cp contrib/afkd.service ~/.config/systemd/user/afkd.service
   ```

3. Tell systemd about it, then start it now and at every login:

   ```bash
   systemctl --user daemon-reload
   systemctl --user enable --now afkd
   ```

4. Let it run when you are not logged in:

   ```bash
   loginctl enable-linger
   ```

Before you start it, open the unit and uncomment the `Environment=PATH=...`
line, then edit it so the agent CLI, `git` and `gh` are all found. A user unit
does not read your shell profile.

Check on it with `systemctl --user status afkd` and `afk daemon status`. The
daemon's log goes to the journal: `journalctl --user -u afkd`. Stopping or
restarting the unit leaves running sessions alone.

## Why the unit restarts the daemon every time

The unit says `Restart=always`. The daemon checks its own program file between
passes, and when `make install` has put a new version there it exits with code
0 on purpose. Only `always` starts a daemon again after a clean exit, so this is
how the daemon picks up a new version by itself. `RestartSec=30` still waits 30
seconds between starts, so a broken `config.toml` (the daemon exits 1) does not
flood the journal. `systemctl --user stop afkd` still stops it for good.

- A daemon you run by hand in a terminal exits on a new version and stays
  stopped. Start it again yourself.
- The daemon leaves only between passes, never in the middle of a ship step, so
  a push, a pull request or a merge is never cut short.
- Running sessions are not touched. `KillMode=process` stops only the daemon,
  and the next start picks the sessions up again.
- Sessions the daemon started are checked and restarted by it while it runs: one that stops
  answering is restarted, and one whose host is gone is started again (see "Recovering a
  session" in the main README).
- A project's `after_merge` command (see `[ship]` in the main README) runs with
  the unit's `PATH`. Whatever it calls must be on the `Environment=PATH=...`
  line. For this repository that is `make`, `go` and `afk` itself.

## On macOS

`afk install` writes `~/Library/LaunchAgents/io.github.dphaener.afk.plist` and
loads it with `launchctl bootstrap`. Run it from a desktop login on the Mac, not
over SSH: launchd only has a `gui` domain for you while you are logged in at the
desktop (this comes from community reports, not from the man page). The by-hand
way, as your normal user:

1. Make the folders:

   ```bash
   mkdir -p ~/Library/LaunchAgents ~/.local/state/afk
   ```

2. Write the plist. Replace `<home>` with your home folder and `<path>` with the
   folders holding `git`, `gh`, `make`, `go` and the agent CLI, separated by
   colons (a launchd job does not read your shell profile). `afk install` puts
   `<home>/bin` and `<home>/.local/bin` first.

   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!-- Written by afk install. afk install replaces this file when it changes. -->
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0">
   <dict>
   	<key>Label</key>
   	<string>io.github.dphaener.afk</string>
   	<key>ProgramArguments</key>
   	<array>
   		<string><home>/bin/afk</string>
   		<string>daemon</string>
   	</array>
   	<key>RunAtLoad</key>
   	<true/>
   	<key>KeepAlive</key>
   	<true/>
   	<key>ThrottleInterval</key>
   	<integer>30</integer>
   	<key>AbandonProcessGroup</key>
   	<true/>
   	<key>EnvironmentVariables</key>
   	<dict>
   		<key>PATH</key>
   		<string><path></string>
   	</dict>
   	<key>StandardOutPath</key>
   	<string><home>/.local/state/afk/daemon.log</string>
   	<key>StandardErrorPath</key>
   	<string><home>/.local/state/afk/daemon.log</string>
   </dict>
   </plist>
   ```

   In a real file, a `&` or `<` inside a value must be written `&amp;` or `&lt;`.

3. Load it. This starts the daemon now and at every login:

   ```bash
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.dphaener.afk.plist
   ```

Check on it with `launchctl print gui/$(id -u)/io.github.dphaener.afk` and
`afk daemon status`. The daemon's log is `~/.local/state/afk/daemon.log`
(`$XDG_STATE_HOME/afk/daemon.log` if you set that). Stop it with:

```bash
launchctl bootout gui/$(id -u)/io.github.dphaener.afk
```

After you change the plist, run that `bootout` first and then the `bootstrap`
again. Stopping the agent leaves running sessions alone.

A session that stops answering is reported on a Mac rather than force-stopped:
the forced stop needs `/proc`, which only Linux has.

### Why each key is there

- `KeepAlive` true is the counterpart of `Restart=always`: the daemon exits 0 on
  purpose when it finds a new version of itself, and must be started again.
- `ThrottleInterval` 30 is the counterpart of `RestartSec=30` (launchd's own
  default is 10 seconds).
- `AbandonProcessGroup` true is the counterpart of `KillMode=process`: sessions
  the daemon started must outlive it.
- `EnvironmentVariables` `PATH` is the counterpart of the `Environment=PATH=...`
  line, and `after_merge` commands run with it.
- `ProgramArguments` starts with an absolute path to `afk`, because launchd
  resolves a relative one with its own default path, not the job's `PATH`.

`docs/decisions.md` (section T11.4) quotes the man page for every key.
