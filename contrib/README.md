# Running the afk daemon as a service

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
