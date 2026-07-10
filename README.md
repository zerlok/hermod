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
  sandbox/               the tmux-over-ssh session: attach-or-create, liveness probe
  shell/                 where a command runs (ssh/tmux decorators) and how (real vs printed leaf)
```
