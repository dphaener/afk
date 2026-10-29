# Installing afk

This guide takes a machine from nothing to a running afk: the build daemon going as a background service, and the web page open in your browser. Most of it is done by `afk install`, a guided installer that asks its questions as it goes. Allow half an hour, most of it spent signing in to things.

## Read this first: afk's agents run with permissions bypassed

afk's daemon works while nobody is watching, so nobody is there to answer the agent's "may I run this command?" prompts. It therefore starts every agent with those prompts turned off (for Claude Code, `--permission-mode bypassPermissions`), and `afk install` turns on the setting that allows it. An agent can run **any command** as your user: install packages, delete files, read anything your user can read, and use any login stored on the machine.

So afk belongs on a machine that is there for afk and nothing else:

- **No personal files, SSH keys or browser logins** that you would not hand to an agent.
- **No production secrets.** If a project needs a paid API key for its tests, give it a key with a low spend cap.
- **A normal user, not root.** The agent refuses its unattended mode as root, and `afk install` refuses to run as root.

## Where to run it

| Where | Good for | How safe |
|---|---|---|
| **A small cloud server** (a VPS from Hetzner, DigitalOcean, Linode…) | The intended setup: always on, reachable from your phone | Isolated from your own machine |
| **A spare computer or home server** running Ubuntu | The same, at home | Isolated, as long as nothing else of yours lives on it |
| **A virtual machine on your laptop** (Multipass) | Trying afk out, or running it while the laptop is on | The VM is the sandbox; your laptop's files stay out of reach |
| **Your laptop itself** (Linux or a Mac) | Only if you accept the risk | **Not isolated.** The agents can reach everything your user can. `afk install` makes you type `I accept the risk` |

afk is built and tested on **Ubuntu Server 26.04**. `afk install` works on any Linux with `apt-get` and systemd, and on a Mac with Homebrew.

## 1. Get a machine

**A cloud server or spare computer.** Install Ubuntu Server 26.04, make a normal user with `sudo`, and log in to it over `ssh`. Two CPUs, 4 GB of memory and 30 GB of disk are plenty for afk; your projects may want more.

**A VM on your laptop.** Install [Multipass](https://multipass.run) (Mac, Windows or Linux), then:

```bash
multipass launch --name afk --cpus 2 --memory 4G --disk 30G
multipass shell afk
```

`multipass shell afk` opens a terminal inside the VM, as its normal user `ubuntu`. Run the rest of this guide there. `multipass find` lists the Ubuntu releases it can make; pick the newest LTS.

## 2. Get afk

Run this one command:

```bash
curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh
```

It finds the latest afk release for your machine (Linux or macOS, on x86_64 or arm64), downloads it, and checks it against the release's list of checksums before it unpacks anything. If the check fails it stops and installs nothing. Then it puts `afk` in `~/bin` and starts `afk install` (step 3), so you can carry straight on. It never uses `sudo`, and it refuses to run as root.

Two settings change what it does. Put them in front of `sh`:

```bash
curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | AFK_VERSION=1.2.3 AFK_NO_INSTALL=1 sh
```

- `AFK_VERSION=1.2.3` installs that release instead of the latest.
- `AFK_NO_INSTALL=1` stops once `~/bin/afk` is in place and does not start `afk install`. Run `~/bin/afk install` yourself when you are ready.

If `~/bin` is not on your `PATH`, the script prints the line to add and the file to put it in (`~/.bashrc`, or `~/.zshrc` on a Mac). It never edits those files itself. Open a new terminal after adding it.

**Building from source instead.** This needs git, Go and make:

```bash
sudo apt-get update
sudo apt-get install -y git golang-go make
git clone https://github.com/dphaener/afk.git ~/afk
cd ~/afk
make install
```

afk needs Go 1.25 or newer; check with `go version`. If your release ships an older one, get Go from [go.dev/dl](https://go.dev/dl/).

`make install` builds afk straight into `~/bin/afk`. If `afk --help` then says `afk: command not found`, add this line to `~/.bashrc` (or `~/.zshrc`) and open a new terminal:

```bash
export PATH="$HOME/bin:$PATH"
```

## 3. Run `afk install`

The one-line command in step 2 has already started it. If you built afk from source, or used `AFK_NO_INSTALL=1`, or want to run it again later:

```bash
afk install
```

It looks at the machine, shows thirteen steps as a checklist with the ones already done ticked, and works through the rest, checking the machine again after each one:

1. **Safety.** What running agents with permissions bypassed means. On a server or a VM a key press accepts; on a computer you use every day it asks you to type `I accept the risk`.
2. **The tools afk needs** (git, gh, jq, ruby), with `sudo apt-get`. `sudo` asks for your password itself.
3. **Claude Code**, the agent afk runs, with its own installer.
4. **Signing in to Claude Code.** The terminal is handed to Claude Code's sign-in. On a machine with no browser it prints a link to open on another device and asks you to paste back a code.
5. **Running without permission prompts.** afk turns on Claude Code's setting for it.
6. **afk's agent plugin.**
7. **Signing in to GitHub** with `gh auth login`, so the daemon can push, open pull requests and merge.
8. **Your git name and email**, for the commits afk makes.
9. **Where new projects go** (default `~/code`). The web page's **Plan a new project** makes its folders here.
10. **The web page's password**, typed twice and never shown. afk keeps only a salted hash of it.
11. **The background service**, so the daemon starts at boot and keeps running when you log out.
12. **Running sessions in Rex**, offered only when `rex` is installed and its server answers. It sets `backend = "rex"` under `[runtime]` in `config.toml`, so each new session opens as a window in Rex that your laptop or phone can reach. On Linux, Rex needs login-server mode so its server keeps running after you log out. If Rex is not there, the step says so and the run goes on without it; afk works without Rex. If you answer no to the offer, the run stops at this step like the others, and running `afk install` again offers it again. The hub's full setup (Tailscale, the control room, your devices) is in [the runbook, section 8](runbook-vm.md#8-the-hub-rex-and-the-control-room).
13. **A last check**, then the web page's address in large type.

Each step asks before it changes anything; answering no stops the run and undoes nothing. A step that fails stops the run and says what to do. Run `afk install` again at any time: it checks every step and does only what is missing, so it is also the way to fix a machine. `afk install --dry-run` shows the checklist and changes nothing. Every flag is in [the command reference](reference.md#afk-install--set-up-this-machine).

**How you reach the web page** is the one real choice it asks for:

- **This machine only** (`http://127.0.0.1:4253`). The safe default.
- **Every address on the machine.** Use this for a Multipass VM: the laptop can reach the VM directly. Find its address with `multipass info afk` (run it on the laptop) and open `http://<that address>:4253`.
- **Tailscale**, offered when `tailscale` is installed. The best choice for a cloud or home server: install [Tailscale](https://tailscale.com) on the server and on your phone and computer first, and afk gives the page an `https://` address that only your own devices can open.

**Never put the page on the open internet as it is.** It speaks plain HTTP, and anyone with the password can drive the daemon.

**One thing `afk install` cannot do:** turn off extra usage (paid overage) on your Claude account. Do it in your account settings. With it on, the daemon starts nothing; afk keeps a share of every usage window for you instead (see [`afk usage`](reference.md#afk-usage--show-how-much-of-your-usage-is-spent)).

## 4. Open the web page

Open the address from `afk install`'s last screen and sign in with the password. Your browser remembers you for 180 days.

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

The service notices the new binary, stops between passes and is started again. Running sessions are not touched. Reinstalling the plugin keeps it in step with the new binary.

## On macOS

A Mac is the "your laptop itself" row above: the agents can reach your files. Run afk in a Multipass VM instead, unless you accept that risk. If you do:

- Install [Homebrew](https://brew.sh) first. afk never installs it, and never uses `sudo` on a Mac.
- Get afk: the one-line command from step 2. Or build it: `brew install git go`, then the `git clone` and `make install` lines from step 2.
- Run `afk install` from a desktop login on the Mac, not over SSH: the service is a launchd agent loaded into your desktop session. Its packages step uses `brew install`.

The service's details, and how to set it up by hand, are in [`contrib/README.md`](../contrib/README.md#on-macos). Desktop notifications (`afk notify`) work only on macOS; on Linux, use web push to your phone (see [`afk inbox`](reference.md#afk-inbox--things-that-need-you)).

## Setting up by hand

`afk install` runs ordinary commands, so you can do the same yourself. On Ubuntu:

```bash
sudo apt-get install -y git gh jq ruby
curl -fsSL https://claude.ai/install.sh | bash     # Claude Code, into ~/.local/bin
claude                                             # sign in, then /exit
claude --permission-mode bypassPermissions         # accept the warning once, then /exit
afk plugin install --target claude
gh auth login && gh auth setup-git
git config --global user.name "Your Name"
git config --global user.email "you@example.com"
mkdir -p ~/code && afk profile set "project location" ~/code
afk web password
```

Then set the page's `[web] bind` in `~/.config/afk/config.toml` (see [configuration.md](configuration.md#configtoml--the-machines-settings)) and install the service with the steps in [`contrib/README.md`](../contrib/README.md). Its `Environment=PATH=...` line must include `~/bin`, `~/.local/bin` (where Claude Code is) and `/usr/bin`.

To try afk without the service, run `afk daemon` in a terminal. It stops when you close that terminal.

## If something goes wrong

- **Run `afk install` again.** It checks every step and says what is missing.
- **The daemon starts nothing, and the inbox has a "failure" item.** The item says what is missing: the plugin, the permission-prompt setting, a login, or extra usage being on. Fix it and answer the item.
- **`afk: command not found`.** `~/bin` is not on your `PATH` (step 2).
- **The page will not open.** Check that the daemon's log says `web page: listening on ...`: `journalctl --user -u afkd -n 20` on Linux, `~/.local/state/afk/daemon.log` on a Mac. Without a password the page is off.

For a fuller worked example of preparing a real project on an always-on machine (its own packages, its checks, a capped API key), see [`runbook-vm.md`](runbook-vm.md).
