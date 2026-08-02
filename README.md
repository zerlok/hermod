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
0. you run `hermod prod-box` from your project directory
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
hermod prod-box                     # mirror cwd, attach to a tmux session named after the dir path
hermod prod-box -s my-session       # explicit session name for tmux + mutagen
hermod prod-box -C ~/work/api       # mirror a directory other than the current one
hermod prod-box -- claude --model opus     # pass args through to the agent CLI on the sandbox
hermod prod-box -N                  # opt out of the notification back-channel (on by default)
hermod prod-box -n                  # dry run: print the commands it would run, run nothing
```

The passthrough examples invoke a remote agent CLI (`claude` here), but Hermod is
agent-agnostic — the remote command can be any program available on the sandbox.

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
hermod prod-box -- herdr      # Hermod mirrors + carries identity; herdr runs the herd on the box
```

A multiplexer manages *sessions and agents*; it does not mirror your working tree to the box or
attribute your commits. Hermod does exactly those two things and stays out of everything else:

- **Files, not a screen.** Hermod mirrors your working directory both ways, so your local editor
  and tooling operate on real files — and it **pauses or tears the sync down on detach**, keyed
  on whether the session outlived you. A multiplexer's "remote" mode streams a remote terminal
  and leaves file sync for you to wire up.
- **Commits that are yours.** Hermod carries your git identity into the session, so commits made
  on the box are attributed to you — something a multiplexer never touches.

Because Hermod is the process running *locally*, it is also the natural place to bridge signals
back from the box to your desktop — turning a "done" from the remote into a native notification
on the machine in front of you. That is exactly what the notification back-channel does.

## Notifications from the sandbox

Terminal notification escapes (OSC 9/777) don't survive the `tmux → ssh` path, and some terminals
(e.g. Ubuntu's GNOME Terminal) don't implement them at all — so a "your agent is done" from the
box never reaches your desktop. Only a **local** process can raise a desktop notification, and
Hermod is that process, so every session opens a back-channel for it by default.

From anywhere inside the session on the box, send a notification to your local desktop:

```bash
hermod notify done                          # toast on your local machine
hermod notify --urgency critical "blocked"  # e.g. when the agent needs input
```

Wire it into an agent hook (`hermod notify done || true`) so a long run pings you when it finishes
or gets stuck — even if you've switched to your browser.

**How it works.** The attach connection carries an `ssh -R` reverse forward mapping a unix socket
on the box to one on your machine; Hermod serves a small HTTP endpoint on that socket and raises
the toast (via `notify-send` on Linux or `osascript` on macOS) plus a sound. The socket lives in a
`0700` directory on both ends, so only you can reach it, and its path is exported into the session
as `$HERMOD_NOTIFY_SOCK`. It is **one socket per sandbox user**, not per project, so its address is
the same for every session you run on that box.

- **Requirements:** locally, `notify-send` (libnotify) on Linux — macOS needs nothing extra. On the
  box, either the `hermod` binary (for `hermod notify`) or, with no install, anything that speaks
  HTTP to a unix socket:
  `curl --unix-socket "$HERMOD_NOTIFY_SOCK" -d '{"body":"done"}' http://hermod/notify`.
  Installing Hermod on the sandbox is a manual step today; the
  [remote-agent proposal](openspec/changes/add-remote-agent/proposal.md) removes it by shipping
  and upgrading the binary on attach, the way Mutagen does with its agent.
- **Best-effort:** the channel never affects your session — if it can't be opened, or a
  notification can't be raised, Hermod logs it and carries on. Opt out with `-N`/`--no-notify`,
  which is also implied by `--dry-run` (a dry run provisions nothing and binds nothing).

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
  cli/                   parse one invocation into functional options (cobra edge)
  control/               resolve defaults, run sync → attach → pause/teardown, decide the branch
  git/                   read local author identity to carry into the remote
  mirror/                the Mutagen file mirror: open (create/resume), flush, pause, close
  notify/                the local end of the notification back-channel: listen, dispatch, send
  sandbox/               the tmux-over-ssh session: attach-or-create, liveness probe, local↔sandbox channel
  shell/                 where a command runs (ssh/tmux decorators) and how (real vs printed leaf)
```
