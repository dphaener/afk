# Installing afk

This guide takes a machine from nothing to a running afk: the build daemon going, and the web page open in your browser. It takes about half an hour, most of it logging in to things.

A guided installer, `afk install`, is on the way (`docs/PLAN.md`, M11). It will do steps 2 to 6 for you and ask its questions as it goes. Until it lands, the steps below are the way in.

## Read this first: afk's agents run with permissions bypassed

afk's daemon works while nobody is watching, so nobody is there to answer the agent's "may I run this command?" prompts. It therefore starts every agent with those prompts turned off (for Claude Code, `--permission-mode bypassPermissions`). An agent can run **any command** as your user: install packages, delete files, read anything your user can read, and use any login stored on the machine.

So afk belongs on a machine that is there for afk and nothing else:

- **No personal files, SSH keys or browser logins** that you would not hand to an agent.
- **No production secrets.** If a project needs a paid API key for its tests, give it a key with a low spend cap.
- **A normal user, not root.** The agent refuses its unattended mode as root, and the daemon checks for this.

## Where to run it

| Where | Good for | How safe |
|---|---|---|
| **A small cloud server** (a VPS from Hetzner, DigitalOcean, Linode…) | The intended setup: always on, reachable from your phone | Isolated from your own machine |
| **A spare computer or home server** running Ubuntu | The same, at home | Isolated, as long as nothing else of yours lives on it |
| **A virtual machine on your laptop** (Multipass) | Trying afk out, or running it while the laptop is on | The VM is the sandbox; your laptop's files stay out of reach |
| **Your laptop itself** | Only if you accept the risk | **Not isolated.** The agents can reach everything your user can. Do this at your own risk |

afk is built and tested on **Ubuntu Server 26.04**. Other Linux distributions should work with the same tools from their own package manager. macOS works too, with the differences noted below.

The steps below assume Ubuntu. Run every command as your normal user.

## 1. Get a machine

**A cloud server or spare computer.** Install Ubuntu Server 26.04, make a normal user with `sudo`, and log in to it over `ssh`. Two CPUs, 4 GB of memory and 30 GB of disk are plenty for afk; your projects may want more.

**A VM on your laptop.** Install [Multipass](https://multipass.run) (Mac, Windows or Linux), then:

```bash
multipass launch --name afk --cpus 2 --memory 4G --disk 30G
multipass shell afk
```

`multipass shell afk` opens a terminal inside the VM, as its normal user `ubuntu`. Run the rest of this guide there. `multipass find` lists the Ubuntu releases it can make; pick the newest LTS.

## 2. Install the tools

```bash
sudo apt-get update
sudo apt-get install -y git gh jq ruby golang-go make build-essential curl
```

- `git` and `gh` (the GitHub tool) let the daemon push, open pull requests and merge.
- `jq` and `ruby` are used by afk's agent plugin.
- `golang-go` and `make` build afk itself. afk needs Go 1.25 or newer; check with `go version`. If your release ships an older one, get Go from [go.dev/dl](https://go.dev/dl/).

Then install Claude Code, the agent afk runs:

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

It installs into `~/.local/bin`. If `claude --version` is not found afterwards, open a new terminal (or log in again) so that folder is on your `PATH`.

## 3. Install afk

```bash
git clone https://github.com/dphaener/afk.git ~/afk
cd ~/afk
make install
```

`make install` builds afk straight into `~/bin/afk`. Check that `~/bin` is on your `PATH`:

```bash
afk --help
```

If the shell says `afk: command not found`, add this line to `~/.bashrc` (or `~/.zshrc`) and open a new terminal:

```bash
export PATH="$HOME/bin:$PATH"
```

## 4. Log in and set up the agent

1. **Log in to Claude Code.** Run `claude`, follow its sign-in steps, then leave it with `/exit`. On a machine with no browser, it prints a link to open on another device and asks you to paste back a code.

2. **Turn extra usage off** on your Claude account (the paid overage). With it on, afk starts nothing. afk keeps a share of every usage window for you (see [`afk usage`](reference.md#afk-usage--show-how-much-of-your-usage-is-spent)).

3. **Accept the unattended-mode warning once.** Run the agent by hand in the mode the daemon uses, accept the warning it shows, then `/exit`:

   ```bash
   claude --permission-mode bypassPermissions
   ```

   Claude Code remembers the answer, and the daemon checks for it before it starts any task.

4. **Install afk's agent plugin:**

   ```bash
   afk plugin install --target claude
   ```

   Run it again whenever you update afk, so the plugin matches the binary.

5. **Log in to GitHub**, and let git use that login:

   ```bash
   gh auth login
   gh auth setup-git
   ```

6. **Tell git who you are**, so the commits afk makes carry your name:

   ```bash
   git config --global user.name "Your Name"
   git config --global user.email "you@example.com"
   ```

7. **Say where new projects go.** The web page's **Plan a new project** makes new folders here:

   ```bash
   mkdir -p ~/code
   afk profile set "project location" ~/code
   ```

## 5. Turn on the web page

The web page is off until you give it a token, a password-like secret of at least 16 characters. Make one:

```bash
openssl rand -hex 16
```

Put it in `~/.config/afk/config.toml` (make the folder and the file if they are not there):

```toml
[web]
bind = "127.0.0.1:4253"
token = "<the token you just made>"
```

`127.0.0.1` means only this machine can open the page. How you reach it from elsewhere depends on where afk runs:

- **A cloud server or home server: use [Tailscale](https://tailscale.com).** Install it on the server and on your phone and computer, keep the `bind` above, and run `tailscale serve --bg 4253` on the server. It prints an `https://` address that only your own devices can open. Put that address in the same `[web]` table as `public_url = "https://..."` so notifications link to the page.
- **A Multipass VM on your laptop:** the laptop can reach the VM directly. Set `bind = "0.0.0.0:4253"`, find the VM's address with `multipass info afk` (on the laptop), and open `http://<that address>:4253`.

**Never put the page on the open internet as it is.** It speaks plain HTTP, and anyone with the token can drive the daemon. More in [the web page's reference](web.md#reference).

## 6. Run the daemon as a service

On Linux, run the daemon as a systemd user service, so it starts at boot and keeps running when you log out. The steps are in [`contrib/README.md`](../contrib/README.md). They take two minutes. Remember the `Environment=PATH=...` line: it must include `~/bin`, `~/.local/bin` (where Claude Code is) and `/usr/bin`.

To try afk before setting up the service, run it in a terminal instead. It stops when you close that terminal:

```bash
afk daemon
```

## 7. Check it works

```bash
afk daemon status   # the daemon is running, with no projects yet
afk usage           # a line for each usage window, and what afk would do
```

Then open the web page with your token on the end of its address, for example `http://127.0.0.1:4253/?token=<your token>`. Your browser remembers you after that.

## Next: your first project

Everything from here can happen in the browser. Follow [A project from start to finish](web.md#a-project-from-start-to-finish).

If you prefer the terminal, `afk plan` in a project folder starts the same planning session, and `afk dash` shows everything afk is doing. See [the dashboard](dashboard.md).

## Updating afk

```bash
cd ~/afk
git pull
make install
afk plugin install --target claude
```

A daemon running as the service notices the new binary, stops between passes and is started again by systemd. Running sessions are not touched.

## On macOS

macOS works, with three differences:

- Install the tools with [Homebrew](https://brew.sh): `brew install git gh jq ruby go`. Claude Code's install command is the same.
- There is no service file for macOS yet. Run `afk daemon` in a terminal you leave open. `afk install` will add a background service.
- A Mac you use every day is the "your laptop itself" row above: the agents can reach your files. Run afk in a Multipass VM instead, unless you accept that risk.

Desktop notifications (`afk notify`) work only on macOS. On Linux, use push notifications to your phone through ntfy instead (see [`afk inbox`](reference.md#afk-inbox--things-that-need-you)).

## If something goes wrong

- **The daemon starts nothing, and the inbox has a "failure" item.** The item says what is missing: the plugin, the unattended-mode warning (step 4.3), a login, or extra usage being on. Fix it and answer the item.
- **`afk: command not found`.** `~/bin` is not on your `PATH` (step 3).
- **The service starts but cannot find `claude`, `git` or `gh`.** The service does not read your shell's settings. Fix the `Environment=PATH=...` line (step 6).
- **The page will not open.** Check that the daemon's log says `web page: listening on ...`: `journalctl --user -u afkd -n 20` for the service, or the terminal for a hand-run daemon. Without a token the page is off.

For a fuller worked example of preparing a real project on an always-on machine (its own packages, its checks, a capped API key), see [`runbook-vm.md`](runbook-vm.md).
