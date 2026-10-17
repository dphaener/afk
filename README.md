# afk

afk builds software for you while you are away from the keyboard. You describe a project in a chat with a planning agent; afk turns that into a plan of small tasks, then works through them one at a time on its own: it runs a coding agent for each task, reviews and tests the result, opens a pull request, merges it when the checks pass, and moves on. It only stops for things you have to do yourself, such as setting up an account or making a choice, and it asks for those in an inbox you can answer from your phone.

You use it from a small web page in your browser, or from a full-screen terminal dashboard if you prefer.

The agent afk runs today is [Claude Code](https://claude.com/claude-code), on your own Claude subscription. afk watches your usage and keeps a share of every usage window free for you.

## Before you install: run afk on its own machine

**afk runs its agents with permissions bypassed.** Nobody is there to approve each command, so the agents can run anything as the user afk runs as: install software, delete files, read whatever that user can read, and use any login on the machine. That is what lets it work unattended, and it is why afk belongs on a machine set aside for it:

- a small cloud server, a spare computer, or a virtual machine on your laptop;
- with no personal files, keys or production secrets on it;
- running as a normal user, never root.

Running it directly on the laptop you use every day is possible, but **at your own risk**. [The install guide](docs/install.md#where-to-run-it) compares the options.

## Install

The install guide takes a fresh Ubuntu machine (or a Multipass VM on your laptop) to a running afk, step by step: **[docs/install.md](docs/install.md)**.

In short:

1. Get an Ubuntu machine (or a Mac with Homebrew, at your own risk).
2. Run the one-line command. It puts the latest afk release in `~/bin` and starts `afk install`:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh
   ```

3. `afk install` walks you through the rest: the tools, Claude Code and its sign-in, GitHub, the web page's password and the background service, asking before each step. Run it again at any time to check and fix the machine.

To build afk from its source instead (needs git, Go and make): `git clone https://github.com/dphaener/afk.git && cd afk && make install`, then run `afk install`.

## Your first project, from the browser

1. Open the web page at the address `afk install` showed you, and sign in with your password.
2. Press **Plan a new project**, give it a name, and start planning.
3. Answer the planning agent's questions in the chat. Each one comes with options and a recommendation.
4. Read the plan it writes, and press **Approve the plan…**.
5. That's it. afk builds the plan task by task. Check in on the project screen, and answer the inbox when a task needs you.

The whole walkthrough, and everything the page can do: **[docs/web.md](docs/web.md)**.

## From the terminal

Everything the web page does, the terminal does too. Over `ssh` to afk's machine:

```bash
afk dash             # the full-screen dashboard: every session, live
afk daemon status    # what the daemon is doing in each project, and why
afk inbox            # what needs you
afk attach <name>    # step into a running agent's terminal (ctrl-\ to leave)
```

The dashboard is also the only place to watch an agent work live or type into it, which the web page does not do. See [docs/dashboard.md](docs/dashboard.md).

## Documentation

| Page | What it covers |
|---|---|
| [Install](docs/install.md) | Where to run afk, and every install step |
| [The web page](docs/web.md) | Using afk from a browser, from a new project to a finished one |
| [The dashboard](docs/dashboard.md) | `afk dash`: keys, panels, and the projects it shows |
| [The build daemon](docs/daemon.md) | How afk works through a plan: recovery, shipping, usage pauses, the inbox |
| [Configuration](docs/configuration.md) | `config.toml`, `profile.toml`, `afk.toml` and `afk.local.toml` |
| [Command reference](docs/reference.md) | Every command and flag |
| [Running as a service](contrib/README.md) | The systemd unit for the daemon |
| [Developing afk](docs/development.md) | Building, testing and how the code is laid out |

afk began as a copy of the forge-cli project, renamed; see [`docs/provenance.md`](docs/provenance.md).
