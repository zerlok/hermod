<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/branding/hermod-wordmark-dark.svg">
    <img src="docs/branding/hermod-wordmark.svg" alt="Hermod" height="56">
  </picture>
</h1>

A single-command bridge to a remote workstation — **Hermod** for short — that relays your
working directory to a remote and attaches you to a process that keeps running there.

It syncs your project files with the remote (Mutagen), attaches you to a persistent tmux
session over SSH, and leaves the sync suspended when you detach, so you can come back to it.

[![CI](https://github.com/zerlok/hermod/actions/workflows/ci.yml/badge.svg)](https://github.com/zerlok/hermod/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/zerlok/hermod.svg)](https://pkg.go.dev/github.com/zerlok/hermod)
[![Go Report Card](https://goreportcard.com/badge/github.com/zerlok/hermod)](https://goreportcard.com/report/github.com/zerlok/hermod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[//]: # (![Hermod: one command relays your project to a remote and attaches a persistent session]&#40;docs/images/demo.gif&#41;)

> **Status: MVP.** The core loop (sync → attach → detach) and the JetBrains Air bridge come
> first; shell completions beyond bash and per-remote config presets are follow-ons.

## Install

```bash
go install github.com/zerlok/hermod@latest
```

Hermod orchestrates tools you already have — it does not bundle them:

- **Local:** [Mutagen](https://mutagen.io), `ssh`, and an `~/.ssh/config` with host aliases for your remotes.
- **Remote:** `tmux` and whatever agent CLI (or plain shell) you want to run.
- **For JetBrains Air:** on the remote, what [the bridge needs there](docs/air-integration.md#nothing-of-hermods-runs-on-the-remote),
  the Claude ACP adapter among it.

## The problem

Working on a remote means juggling three tools by hand, every session: **Mutagen** to
mirror your files, **SSH** to get a shell, and **tmux** so the session survives a dropped
connection. On top of that you have to thread your **git identity** through, or every commit
is attributed to the box instead of you. Wiring that up — and tearing it down cleanly, without
leaving a sync session bleeding in the background — is fiddly enough that people avoid remote
workstations even when that is where the work should happen.

## What Hermod does

Hermod collapses that setup into one command. The canonical flow:

```
0. you run `hermod attach prod-box` from your project directory
1. the FS relay connects    →  your project syncs with the remote, live, both ways
2. SSH into the remote      →  attach to (or start) a worker: a persistent tmux session
3. git identity carried in  →  GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL given to the worker, so commits are yours
4. you work in tmux         →  the agent (or shell) runs remotely; edits sync back instantly
5. you detach               →  while a worker of the project runs, the sync is SUSPENDED (resumed next attach);
                               with none left, it is flushed and REMOVED
```

The project on one remote is a **workspace**: its relays (the file sync, the session's stdio, any
port relays) and its workers. A session leases the relays while you are attached, and the last one
out decides from the workers whether they stay. The model is in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#the-model).

### Usage

The remote argument is an `~/.ssh/config` host alias (tab-completion offers your configured
hosts). Everything after `--` is passed straight through to the remote command.

```bash
hermod attach prod-box                     # relay cwd, attach to a worker named after the directory
hermod attach prod-box -s my-session       # explicit worker name
hermod attach prod-box -C ~/work/api       # work in a directory other than the current one
hermod attach prod-box -R 8080:localhost:3000   # calls to port 8080 on the remote reach localhost:3000 here
hermod attach prod-box -- claude --model opus   # pass args through to the agent CLI on the remote
hermod attach prod-box -n                  # dry run: print the commands that would change a state, run none
```

`-R PORT:ADDRESS` relays calls to a port on the remote to an address on this machine, for a
listener you run yourself; give it once per port. The relay lasts as long as the session.

The passthrough examples invoke a remote agent CLI (`claude` here), but Hermod is
agent-agnostic — the remote command can be any program available on the remote.

Defaults can come from the environment; an argument always wins:

| Variable            | Used when                                                                                     |
|---------------------|-----------------------------------------------------------------------------------------------|
| `HERMOD_REMOTE`     | no remote argument is given. With neither, a command fails before doing anything.             |
| `HERMOD_REMOTE_CMD` | no `-- COMMAND` is given: a command line run by the remote user's shell.                      |
| `HERMOD_ORIGIN`     | always: the name this machine is given on the remote, the hostname when unset. Set to empty, nothing on the remote names the machine. |

```bash
export HERMOD_REMOTE=prod-box HERMOD_REMOTE_CMD='claude --model opus'
hermod attach                              # same as: hermod attach prod-box -- claude --model opus
```

Every worker Hermod starts is tagged with its workspace and the machine it came from, in the tmux
session options `@hermod-workspace` and `@hermod-origin` (this machine, reduced to letters,
digits, dots and dashes):

```bash
tmux ls -F '#{session_name} #{@hermod-workspace} #{@hermod-origin}'   # on the remote
```

A Claude conversation started with `--session-id <uuid>` or `--resume <uuid>` runs in the worker
`hermod-<uuid>`, so reopening it from anywhere joins the same one; Hermod reads from Claude
whether the conversation still runs and starts, joins or refuses from that
([Session](docs/ARCHITECTURE.md#session)). Arguments that name no conversation, such as
`claude auth status`, run once on the remote with your terminal and start no worker.

### JetBrains Air

Hermod can move the Claude agent of [JetBrains Air](https://www.jetbrains.com/air/) to a
remote. Register each remote once:

```bash
hermod air configure claude prod-box       # -n prints the entry and changes nothing
```

- **Chat mode.** Air lists a new agent, `Claude (prod-box)`, whose chats run on the remote.
  The entry runs `hermod air acp claude prod-box`, which starts one Claude ACP adapter
  (`claude-agent-acp`) per project on the remote and routes Air's chats to them. `--name` names
  the agent, and `--origin` the name its chats give this machine on the remote. Run `configure`
  again after moving `hermod`. Each remote's agent lists only the chats of that remote.
- **Terminal mode.** `make install` links a stand-in: `hermod` under the name `claude`, by default
  at `~/.local/share/hermod/bin/claude` (`make install STANDIN=…` puts it elsewhere). Started as
  `claude ARGS…` it runs `hermod attach -- claude ARGS…`. Enter the path `configure` prints as
  Air's Claude executable, and give Air `HERMOD_REMOTE=prod-box` in the environment it starts
  with (for example in `~/.config/environment.d/`): Air does not read your shell's. Air lists
  terminal-mode chats from `~/.claude`, so with one project on several remotes their chats share
  one list. Avoid several remotes for one project here and when resuming by hand: a conversation
  reopened while another remote is configured, or `hermod attach B -- claude --resume <uuid>` for
  a chat that lives on A, forks it onto that remote
  ([design](openspec/changes/add-air-bridge/design.md#each-remotes-chats-are-listed-by-its-own-agent)).

The commands Air runs, which you can also run yourself:

| Command                                   | What it does                                                                       |
|-------------------------------------------|------------------------------------------------------------------------------------|
| `hermod air configure claude [REMOTE]`    | write the remote's entry into `~/.jetbrains/acp.json`, and print the stand-in link and the variable terminal mode needs |
| `hermod air acp claude [REMOTE]`          | what the chat-mode entry runs: Air's one ACP connection, routed to each project's adapter on the remote |
| `claude ARGS…` (the stand-in link)        | `hermod attach -- claude ARGS…`, the remote from `HERMOD_REMOTE`                    |

Closing a chat's tab or quitting Air only hides the chat: it keeps running on the remote, and
reopening it returns to it. When a project has no chat shown, its workspace detaches. **Mark
done** in chat mode ends the chat; when that leaves the project with no chat shown and no
conversation running, its adapter is closed, and the project's relays are released unless another
worker of the project still runs ([Close](docs/ARCHITECTURE.md#close)). Terminal mode has no such
signal: end a session there with `tmux kill-session -t hermod-<uuid>` on the remote. Steps are
logged to `~/.cache/hermod/air/` (under `$XDG_CACHE_HOME` when set), one file per worker named by
its key: `<uuid>.log` for a terminal session, `hermod-acp-<workspace>.log` for an adapter
([Names and tags](docs/ARCHITECTURE.md#names-and-tags)).

The command line and what `configure` writes, with the reasons, are in the
[design](openspec/changes/add-air-bridge/design.md#command-line); what Air expects of Claude,
what is not yet verified against Air, and a note on subscription use are in
[`docs/air-integration.md`](docs/air-integration.md).

Each remote gets its own FS relay of a directory, so one project can be attached to several
remotes. The relay carries edits both ways and never discards either side's: a file changed on
both sides stays a Mutagen conflict for you to settle (`mutagen sync list` shows it). A directory
inside a relayed one reuses its relay, so sessions in one project share it, and a linked git
worktree joins the workspace of its main working tree. Hermod finds a remote's relays by their
Mutagen labels (`mutagen sync list --label-selector hermod-remote=<remote>`). A Claude session
on a terminal also relays its transcripts with your local Claude config directory, so they can
be listed and resumed locally.

## What Hermod is *not*

- It does **not** reimplement Mutagen, SSH, or tmux — it **orchestrates** them, and gets out of
the way. If you know those tools, nothing here is a black box.
- It is **not** agent-specific. Hermod runs whatever command you point it at on the remote —
Claude Code, Codex, a build, or a bare shell. It only cares about sync + attach.
- It does **not** provision the remote. The box, the agent, and tmux are yours to set up;
Hermod is the connection, not the server.
- It does **not** use git as a transport between hosts. Files move over Mutagen; git identity
is carried only as environment so your commits are attributed correctly on the remote.

## Where Hermod fits

Hermod is the **sync-and-attach layer**, not a place to run your agents. It gets you — your
files and your git identity — onto the box and manages the sync lifecycle around one session.
What you run *on* the box is a separate concern: a bare shell, tmux, a build, or an agent
multiplexer like [herdr](https://herdr.dev). Those own a **different layer**, so they don't
compete with Hermod — they compose with it:

```bash
hermod attach prod-box -- herdr      # Hermod relays + carries identity; herdr runs the herd on the box
```

A multiplexer manages *sessions and agents*; it does not relay your working tree to the box or
attribute your commits. Hermod does exactly those two things and stays out of everything else:

- **Files, not a screen.** Hermod relays your working directory both ways, so your local editor
  and tooling operate on real files — and it **suspends or removes the sync on detach**, keyed
  on whether a worker outlived you. A multiplexer's "remote" mode streams a remote terminal
  and leaves file sync for you to wire up.
- **Commits that are yours.** Hermod carries your git identity into the session, so commits made
  on the box are attributed to you — something a multiplexer never touches.

## Development

**Language:** Go (single binary, no runtime dependencies).

```bash
make build      # compile the hermod binary into ./bin
make test       # run the test suite
make lint       # static checks (gofmt + go vet)
make install    # install to $GOBIN for local use
```

Run `make help` for the full list of targets.

This repository is **spec-driven**: changes start as OpenSpec proposals under `openspec/`
before implementation. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the domain model.

### Project structure

The packages, what each represents and its constraints are in
[Packages](docs/ARCHITECTURE.md#packages), and which may import which in
[Package dependencies](docs/ARCHITECTURE.md#package-dependencies).

```
main.go                  entrypoint
internal/
  # adapters
  cli/                   the process's interface: arguments, environment and streams, resolved once and handed to the core
  acp/                   the ACP wire format: one message per line, each parsed into a type of its own or rejected
  air/                   JetBrains Air's own facts: acp.json, the stand-in link, its hook endpoint, its own ACP messages
  claude/                Claude Code: its arguments, files, settings format and ACP adapter, run through a shell it is given
  git/                   the developer's git facts, read-only
  # application
  session/               a session: decides from the agent's and the worker's states, and routes a chat client's messages
  # domain
  workspace/             the relays and workers of one project on one remote; names and tags them
  relay/                 a channel between the workstations (FS, IO, Net) and its leases
  worker/                a process on the remote that outlives its caller: a tmux session's lifecycle
  agent/                 the contract an agent program offers, and its optional ACP contract
  # foundation
  shell/                 where a command runs (ssh, login, tmux) and how (real or dry-run); tmux's command language
  pathmap/               the map between local and remote paths, in strings and JSON
  env/                   variables as KEY=VALUE pairs
  secret/                a value that must not be printed
```
