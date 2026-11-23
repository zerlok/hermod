## ADDED Requirements

### Requirement: Register a remote with Air
`hermod air configure claude [<remote>]` SHALL merge one entry into `~/.jetbrains/acp.json` under `agent_servers`,
keeping every other entry and key. The entry's `command` SHALL be the `hermod` binary's absolute path and its `args`
SHALL be `air acp claude <remote>`. The remote SHALL come from the argument, else from `HERMOD_REMOTE`; with neither,
the command SHALL fail before changing anything.

#### Scenario: Entry merged
- **WHEN** `acp.json` holds another agent and the user runs `hermod air configure claude prod-box`
- **THEN** `acp.json` holds the other agent unchanged and an entry `Claude (prod-box)` whose `command` is an absolute path to `hermod` and whose `args` are `air acp claude prod-box`

#### Scenario: Remote from the environment
- **WHEN** configure runs with no remote argument and `HERMOD_REMOTE=prod-box`
- **THEN** the entry's `args` end with `prod-box`

#### Scenario: No remote
- **WHEN** configure runs with no remote argument and `HERMOD_REMOTE` unset
- **THEN** it fails and `acp.json` is unchanged

#### Scenario: Each remote is its own agent
- **WHEN** configure runs for `box-a` and then for `box-b`
- **THEN** `acp.json` holds two entries, `Claude (box-a)` and `Claude (box-b)`

### Requirement: Configure options
`--name` SHALL set the entry's name, `Claude (<remote>)` by default, and configuring an existing name SHALL replace
that entry. `--origin=<name>` SHALL write `HERMOD_ORIGIN` into the entry's `env`, and `--origin=` SHALL write it
empty; without the flag the `HERMOD_ORIGIN` that `configure` runs with SHALL be written, and with neither the entry
SHALL have no `env`. `-n` SHALL print the entry and change nothing.

#### Scenario: Same name replaces
- **WHEN** configure runs twice with `--name=Remote`
- **THEN** `acp.json` holds one entry named `Remote`

#### Scenario: Empty origin
- **WHEN** configure runs with `--origin=`
- **THEN** the entry's `env` holds `HERMOD_ORIGIN` set to empty

#### Scenario: No origin
- **WHEN** configure runs without `--origin` and `HERMOD_ORIGIN` unset
- **THEN** the entry has no `env`

#### Scenario: Dry run
- **WHEN** configure runs with `-n`
- **THEN** the entry is printed and `acp.json` is unchanged

### Requirement: Configure prints the terminal-mode setup
`hermod air configure` SHALL print the `HERMOD_REMOTE=<remote>` line that Air's own environment needs for terminal
mode, and, when it finds the stand-in link (a `claude` on its `PATH` that links to `hermod`, else the link at the
installer's default place), that link's path, to be entered as Air's Claude executable. It SHALL NOT create or
change the link.

#### Scenario: Terminal-mode lines
- **WHEN** configure runs for `prod-box` and the stand-in link exists
- **THEN** it prints the link's path and `HERMOD_REMOTE=prod-box`

#### Scenario: Link left alone
- **WHEN** configure runs and no stand-in link is found
- **THEN** it prints `HERMOD_REMOTE=<remote>` and creates no link

### Requirement: One chat agent per remote
`hermod air acp claude [<remote>]` SHALL be Air's chat agent for the one remote it names, the remote falling back to
`HERMOD_REMOTE`. It SHALL run once per IDE run, from when Air starts the entry's command, and SHALL serve every chat
of every project Air holds on its one ACP connection as the `agent-session` capability specifies.

#### Scenario: Remote from the environment
- **WHEN** Air starts `hermod air acp claude` with no remote argument and `HERMOD_REMOTE=prod-box`
- **THEN** its chats run on `prod-box`

### Requirement: Air ends the agent, not the chats
`hermod air acp` SHALL run until Air closes its stdin or sends SIGTERM. It SHALL then detach every workspace it
holds and exit, leaving every adapter and chat on the remote running.

#### Scenario: IDE quits
- **WHEN** Air closes `air acp`'s stdin
- **THEN** `air acp` exits and every adapter it used still runs on the remote
