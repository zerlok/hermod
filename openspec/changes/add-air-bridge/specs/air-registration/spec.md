## ADDED Requirements

### Requirement: Register a sandbox with Air
`hermod air configure claude [<sandbox>]` SHALL create or repair the stand-in link `~/.local/share/hermod/bin/claude`
and merge one entry into `~/.jetbrains/acp.json` under `agent_servers`, keeping every other entry and key. The entry
SHALL run the `hermod` binary by absolute path with `air acp claude --node=<node> --acp=<adapter>`, and its `env`
SHALL hold `HERMOD_SANDBOX=<sandbox>`, `CLAUDE_CODE_EXECUTABLE` set to the link, and `CLAUDE_CONFIG_DIR` set to
`~/.local/share/hermod/air/<sandbox>/claude`. The sandbox SHALL come from the argument or `HERMOD_SANDBOX`, and SHALL
be written only into `env`, never into `args`. With neither, the command SHALL fail before doing anything.

#### Scenario: Entry merged
- **WHEN** `acp.json` holds another agent and the user runs `hermod air configure claude prod-box`
- **THEN** `acp.json` holds the other agent unchanged and an entry `Claude (prod-box)` with `HERMOD_SANDBOX=prod-box` in its `env`

#### Scenario: Link repaired
- **WHEN** the stand-in link is missing or points elsewhere
- **THEN** configure makes it a link to the running `hermod` binary

#### Scenario: Each sandbox lists its own chats
- **WHEN** entries are configured for `box-a` and `box-b`
- **THEN** their `CLAUDE_CONFIG_DIR` values differ

### Requirement: Configure options
`--node` and `--acp` SHALL default to `node` and `claude-agent-acp` found on the user's `PATH` and SHALL be written
as absolute paths. `--name` SHALL set the entry's name, `Claude (<sandbox>)` by default; configuring an existing name
SHALL replace that entry. `--origin=<name>` SHALL write `HERMOD_ORIGIN` into the entry's `env`, `--origin=` writing it
empty; without the flag the `HERMOD_ORIGIN` that `configure` runs with SHALL be written, and with neither the entry
SHALL carry no `HERMOD_ORIGIN`. `-n` SHALL print the entry and change nothing.

#### Scenario: Paths resolved from PATH
- **WHEN** configure runs without `--node` and `--acp`
- **THEN** the entry's `args` carry the absolute paths of `node` and `claude-agent-acp` from `PATH`

#### Scenario: Same name replaces
- **WHEN** configure runs twice with `--name=Remote`
- **THEN** `acp.json` holds one entry named `Remote`

#### Scenario: Empty origin
- **WHEN** configure runs with `--origin=`
- **THEN** the entry's `env` holds `HERMOD_ORIGIN` set to empty

#### Scenario: Dry run
- **WHEN** configure runs with `-n`
- **THEN** the entry is printed and neither `acp.json` nor the link changes

### Requirement: Configure prints the terminal-mode setup
`hermod air configure` SHALL print the stand-in link's path, to be entered as Air's Claude executable, and the
`HERMOD_SANDBOX=<sandbox>` line Air's own environment needs for terminal mode.

#### Scenario: Terminal-mode lines
- **WHEN** configure runs for `prod-box`
- **THEN** it prints the link path and `HERMOD_SANDBOX=prod-box`

### Requirement: Wrap the ACP adapter
`hermod air acp claude [<sandbox>]` SHALL start the adapter as `--node` running `--acp`, with its own environment
unchanged, and SHALL copy ACP messages between its caller and the adapter verbatim in both directions. On a
`_session/archive` for a session, it SHALL close that session on the sandbox as the `agent-session` capability
specifies. On a `_session/unarchive` for a session, it SHALL keep a close of that session still to come from ending
the reopened chat, as the `agent-session` capability specifies.

#### Scenario: Messages copied verbatim
- **WHEN** Air sends `session/new` and the adapter answers
- **THEN** each message passes through unchanged

#### Scenario: Mark done closes the session
- **WHEN** Air sends `_session/archive` for `<uuid>`
- **THEN** `hermod-<uuid>` is closed on the sandbox and the message still reaches the adapter

#### Scenario: Reopen keeps the session
- **WHEN** Air sends `_session/unarchive` for `<uuid>` while a close of it is queued
- **THEN** that close is dropped and the message still reaches the adapter

#### Scenario: Environment reaches every chat
- **WHEN** the adapter spawns the stand-in for a chat
- **THEN** the stand-in sees the entry's `HERMOD_SANDBOX`
