# Running the afk daemon as a service

`afk install` writes this unit and starts it for you, along with the rest of the
machine setup. The steps below are the by-hand way, for a machine where you
would rather not use it.

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
