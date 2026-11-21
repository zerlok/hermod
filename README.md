<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/branding/hermod-wordmark-dark.svg">
    <img src="docs/branding/hermod-wordmark.svg" alt="Hermod" height="56">
  </picture>
</h1>

A single-command bridge to a remote dev box — **Hermod** for short — that mirrors your
working directory to a remote sandbox and drops you into a persistent session running there.

It syncs your project files to the sandbox (Mutagen), attaches you to a persistent tmux
session over SSH, and pauses the sync when you detach so you can resume it later.

[![CI](https://github.com/zerlok/hermod/actions/workflows/ci.yml/badge.svg)](https://github.com/zerlok/hermod/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/zerlok/hermod.svg)](https://pkg.go.dev/github.com/zerlok/hermod)
[![Go Report Card](https://goreportcard.com/badge/github.com/zerlok/hermod)](https://goreportcard.com/report/github.com/zerlok/hermod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[//]: # (![Hermod: one command mirrors your project to a remote box and attaches a persistent session]&#40;docs/images/demo.gif&#41;)

> **Status: MVP.** The core loop (sync → attach → pause/teardown) comes first; shell
> completions and per-sandbox config presets are follow-ons.

## Install

```bash
go install github.com/zerlok/hermod@latest
```

Hermod orchestrates tools you already have — it does not bundle them:

- **Local:** [Mutagen](https://mutagen.io), `ssh`, and an `~/.ssh/config` with host aliases for your sandboxes.
- **Remote:** `tmux` and whatever agent CLI (or plain shell) you want to run.
- **For JetBrains Air:** Node ≥ 22 and the Claude ACP adapter locally (chat mode); on the sandbox,
  what [the bridge needs there](docs/air-integration.md#nothing-of-hermods-runs-on-the-sandbox).

## The problem

Working on a remote sandbox means juggling three tools by hand, every session: **Mutagen** to
mirror your files, **SSH** to get a shell, and **tmux** so the session survives a dropped
connection. On top of that you have to thread your **git identity** through, or every commit
is attributed to the box instead of you. Wiring that up — and tearing it down cleanly, without
leaving a sync session bleeding in the background — is fiddly enough that people avoid remote
sandboxes even when the sandbox is where the work should happen.

## What Hermod does

Hermod collapses that setup into one command. The canonical flow:

```
0. you run `hermod attach prod-box` from your project directory
1. Mutagen session starts   →  your working dir mirrors to the sandbox, live, both ways
2. SSH into the sandbox      →  attach to (or create) a persistent tmux session
3. git identity carried in   →  GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL exported, so commits are yours
4. you work in tmux          →  the agent (or shell) runs remotely; edits sync back instantly
5. you detach                →  if the tmux session persists, sync PAUSES (resume it later);
                             if the session is gone, sync TEARS DOWN cleanly
```                              
### Usage

The sandbox argument is an `~/.ssh/config` host alias (tab-completion offers your configured
hosts). Everything after `--` is passed straight through to the remote command.

```bash
hermod attach prod-box                     # mirror cwd, attach to a tmux session named after the dir path
hermod attach prod-box -s my-session       # explicit tmux session name
hermod attach prod-box -C ~/work/api       # mirror a directory other than the current one
hermod attach prod-box -- claude --model opus     # pass args through to the agent CLI on the sandbox
hermod attach prod-box -n                  # dry run: print the commands it would run, run nothing
```

The passthrough examples invoke a remote agent CLI (`claude` here), but Hermod is
agent-agnostic — the remote command can be any program available on the sandbox.

Defaults can come from the environment; an argument always wins:

| Variable             | Used when                                                                                     |
|----------------------|-----------------------------------------------------------------------------------------------|
| `HERMOD_SANDBOX`     | no sandbox argument is given. With neither, a command fails before doing anything.            |
| `HERMOD_SANDBOX_CMD` | no `-- COMMAND` is given: a command line run by the sandbox user's shell.                     |
| `HERMOD_ORIGIN`      | always: the name this machine is given on the sandbox, the hostname when unset. Set to empty, nothing on the sandbox names the machine. |

```bash
export HERMOD_SANDBOX=prod-box HERMOD_SANDBOX_CMD='claude --model opus'
hermod attach                              # same as: hermod attach prod-box -- claude --model opus
```

Every tmux session Hermod creates says where it came from, in the session options
`@hermod-mode`, `@hermod-origin` (this machine, reduced to letters, digits, dots and dashes)
and `@hermod-dir`:

```bash
tmux ls -F '#{session_name} #{@hermod-mode} #{@hermod-origin} #{@hermod-dir}'   # on the sandbox
```

A Claude session started with `--session-id <uuid>` or `--resume <uuid>` runs in tmux session
`hermod-<uuid>`, so reopening it from anywhere joins the same session; see the
[Air bridge design](openspec/changes/add-air-bridge/design.md#agent-session).

### JetBrains Air

Hermod can move the Claude agent of [JetBrains Air](https://www.jetbrains.com/air/) to a
sandbox. Register each sandbox once:

```bash
hermod air configure claude prod-box       # -n prints what it would write and changes nothing
```

- **Chat mode.** Air lists a new agent, `Claude (prod-box)`, whose chats run on the sandbox.
  The entry runs `hermod air acp claude`, which runs the Claude ACP adapter
  (`npm install -g @agentclientprotocol/claude-agent-acp`) with Node ≥ 22, both found on your
  `PATH` when you run `configure` unless `--node` and `--acp` name them, and written into the
  entry as absolute paths. `--name` names the agent, and `--origin` the name its chats give this
  machine on the sandbox. Run `configure` again after moving `hermod`, Node or the adapter.
  Each sandbox's agent keeps a Claude config directory of its own, so that it lists only the
  chats of that sandbox.
- **Terminal mode.** Enter the path `configure` prints as Air's Claude executable, and give Air
  `HERMOD_SANDBOX=prod-box` in the environment it starts with (for example in
  `~/.config/environment.d/`): Air does not read your shell's. Air lists terminal-mode chats
  from `~/.claude`, so with one project on several sandboxes their chats share one list.

The commands Air runs, which you can also run yourself:

| Command                                              | What it does                                                        |
|------------------------------------------------------|---------------------------------------------------------------------|
| `hermod air configure claude [SANDBOX]`              | create the `claude` stand-in link and write the sandbox's entry into `~/.jetbrains/acp.json` |
| `hermod air acp claude [SANDBOX] [--node] [--acp]`   | what the chat-mode entry runs: the adapter, with every ACP message passed through as it is |
| `claude ARGS…` (the stand-in link)                   | `hermod attach -- claude ARGS…`, the sandbox from `HERMOD_SANDBOX` |

Closing a chat or quitting Air detaches: the session keeps running on the sandbox. Reopening a
chat-mode chat that is still running restarts its Claude with `--resume`, which costs at most the
turn in flight; in terminal mode the reopen joins the running session. **Mark done** in chat mode closes the chat's session on the sandbox (its tmux session
goes; its files under `~/.hermod/s/<uuid>/` stay, so reopening the chat resumes it) and settles
its mirrors as a detach does. Terminal mode has no such signal: end a session there with
`tmux kill-session -t hermod-<uuid>` on the sandbox. Each session's steps are logged to
`~/.cache/hermod/air/<uuid>.log`.

The command line and what `configure` writes, with the reasons, are in the
[design](openspec/changes/add-air-bridge/design.md#command-line); what Air expects of Claude,
what is not yet verified against Air, and a note on subscription use are in
[`docs/air-integration.md`](docs/air-integration.md).

Each sandbox gets its own mirror of a directory, named `hermod-<sandbox>-<path slug>`, so one
project can be attached to several sandboxes. The mirror carries edits both ways and never
discards either side's: a file changed on both sides stays a Mutagen conflict for you to settle
(`mutagen sync list` shows it). When a mirror is first created, any other Mutagen session that
syncs the same directory, such as one made by an earlier Hermod version or by hand, is listed
on stderr and left for you to keep or terminate.

A directory inside a mirrored one reuses its mirror, so sessions in one project share it, and
Hermod finds a sandbox's mirrors by their Mutagen labels
(`mutagen sync list --label-selector hermod-sandbox=<sandbox>`). A Claude session also mirrors
its transcripts back from the sandbox, one way, into your local Claude config directory, so they
can be listed and resumed locally. A mirror is paused or closed only by the last Hermod using
it, and only closed once no tmux session on the sandbox works under its root.

## What Hermod is *not*

- It does **not** reimplement Mutagen, SSH, or tmux — it **orchestrates** them, and gets out of
the way. If you know those tools, nothing here is a black box.
- It is **not** agent-specific. Hermod runs whatever command you point it at on the remote —
Claude Code, Codex, a build, or a bare shell. It only cares about sync + attach.
- It does **not** provision the sandbox. The box, the agent, and tmux are yours to set up;
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
hermod attach prod-box -- herdr      # Hermod mirrors + carries identity; herdr runs the herd on the box
```

A multiplexer manages *sessions and agents*; it does not mirror your working tree to the box or
attribute your commits. Hermod does exactly those two things and stays out of everything else:

- **Files, not a screen.** Hermod mirrors your working directory both ways, so your local editor
  and tooling operate on real files — and it **pauses or tears the sync down on detach**, keyed
  on whether the session outlived you. A multiplexer's "remote" mode streams a remote terminal
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

Packages are layered; dependencies only ever point downward.

```
main.go                  entrypoint
internal/
  cli/                   parse one invocation and its environment into functional options (cobra edge)
  control/               resolve defaults, run sync → attach → pause/teardown, decide the branch; wrap Air's ACP adapter
  claude/                Claude Code's command-line contract as pure functions (classify and rewrite an argv, settings, environment)
  air/                   JetBrains Air's files: the stand-in link and the agent entry in acp.json; Air's mark-done message
  hook/                  the local hook proxy: forward a session's hooks to Air with the session's token checked
  git/                   read local author identity to carry into the remote
  mirror/                the Mutagen mirrors: open (create/resume), flush, pause, close; list by label; shared leases
  sandbox/               the tmux-over-ssh session: attach-or-create, liveness of a root; one-shot runs; agent sessions by UUID
  pathmap/               map paths between local and sandbox roots, in strings and JSON
  secret/                credentials as an opaque type that never prints
  shell/                 where a command runs (ssh/tmux decorators) and how (real vs printed leaf)
```
