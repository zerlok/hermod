The packages are built bottom-up, in the layers of
[Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies), each against the
[Packages](../../../docs/ARCHITECTURE.md#packages) table and the section a task names. `internal/control`,
`internal/sandbox`, `internal/mirror` and `internal/hook` are not adapted to the API changes below them: they are
deleted in group 13, so until then each group's checks run on the group's own packages
(`go test ./internal/<package>/...`), and group 13 runs them on the whole module.

Every group's tests are table-driven, and the core is tested through a fake `shell.Executor`, as
[Interfaces](../../../docs/ARCHITECTURE.md#interfaces) says.

## 1. Foundation: `env`, `secret`, `shell`, `pathmap`

- [ ] 1.1 Create `internal/env` with `Vars`, the one place that splits and joins `KEY=VALUE`: build from pairs and from a process environment, read a key, select by key, and render for a command. A value may be a `secret.Token`, which prints masked; move `secret.Env`'s behaviour here
- [ ] 1.2 Reduce `internal/secret` to `Token`: keep `NewToken`, `FromString`, `Reveal`, `IsZero`, `Equal` and every masked form; remove `secret.Env` once `env.Vars` replaces it
- [ ] 1.3 In `internal/shell`, turn `Shell.Run` into `Executor.Exec(Command) Execution` ([`shell.Executor`](../../../docs/ARCHITECTURE.md#shellexecutor--a-workstation)): one `Command` with the arguments, directory, `env.Vars` and streams; an `Execution` with the running command's streams and its result. Keep `local`, dry-run, `ssh` (with `WithTty`, `WithoutSharedMaster`, `WithReverseForward`), `login` and `tmux`, `Quote` and `ExitCode`, rewritten to the new types
- [ ] 1.4 In `internal/pathmap`, rename the API to `Map` with `ToRemote` and `ToLocal` and `Place`, built from pairs of a local and a remote root, longest root first, and applied to a string and to every string in a JSON value ([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)); keep the matching at a path-segment boundary, `Under` and `Absolute`
- [ ] 1.5 Tests: `env.Vars` split, join and masking; `secret.Token` in every `fmt` form; each `Executor` rewrite and the nesting of wrappers with dry-run chosen at the innermost one; `pathmap` in both directions, a path under no root, nested roots and JSON values

## 2. Domain: `relay`

- [ ] 2.1 Create `internal/relay` with `Relay`, `Lease`, `State` (Absent, Disconnected, Connected), `Ends` and `Filter` ([`relay.Relay` and `relay.Lease`](../../../docs/ARCHITECTURE.md#relayrelay-and-relaylease--one-channel-and-a-claim-on-it), [Relay](../../../docs/ARCHITECTURE.md#relay)). `Connect` takes the lease, makes an Absent relay, takes it to Connected and starts what serves it in this process; reading a state that fails is an error, not a state
- [ ] 2.2 Leases as [Leases](../../../docs/ARCHITECTURE.md#leases) describes: `Suspend`, `Release`, `Done` and `Err`. Move the locking of `internal/mirror/lease.go`
- [ ] 2.3 FS relay from `Ends`, a name and labels: a Mutagen `two-way-safe` session, `Flush`, and `List` of the relays under a label with their ends and states; check a found relay's remote end before resuming it. Move the `mutagen` argv building, labels and endpoint check of `internal/mirror/mirror.go`
- [ ] 2.4 IO relay from a remote stdio and local streams: copies both ways for a terminal, and for a line stream copies the local input into the FIFO one complete line at a time while tailing the logs ([Line streams](../../../docs/ARCHITECTURE.md#line-streams)); `Done` when the local side leaves or the process ends. Move the line pump of `internal/control/stream.go`
- [ ] 2.5 Net relay from a remote port, a local address and an optional `Filter`: `ssh -R` on a connection that refuses a shared master, to a listener in this process; suspending its last lease removes it, as releasing does. Move the listener of `internal/hook/hook.go`, without its token and path logic, which becomes `session`'s filter (6.4)
- [ ] 2.6 Tests: the state table and diagram of [Relay](../../../docs/ARCHITECTURE.md#relay) for each kind, with two holders in one process; argv for FS create, resume, flush, list and an endpoint mismatch; a partial final line on an IO relay; a Net relay with and without a filter

## 3. Domain: `worker`

- [ ] 3.1 Create `internal/worker` with `Worker`, `State` (Absent, Running, Exited), `Spec` (key, directory, command, files, variables, tags, `Form`), `Form` (terminal, line stream), `Stdio` and `List` by tag ([Worker](../../../docs/ARCHITECTURE.md#worker)); it knows nothing about what its process is. Move the tmux session handling of `internal/sandbox` (start, `has-session`, `kill-session`, `remain-on-exit failed`, tags)
- [ ] 3.2 Embed the [remote scripts](../../../docs/ARCHITECTURE.md#remote-scripts), one that keeps a failed process's pane and one that gives a line stream its FIFO and logs, and send each to the remote's `sh` on stdin with its inputs as arguments and environment
- [ ] 3.3 Write the [worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory) (mode `0700`) over ssh stdin, run every setup command with stdin from `/dev/null`, and close a worker with SIGHUP, waiting on a line stream's process ID
- [ ] 3.4 Tests: the state table and diagram of [Worker](../../../docs/ARCHITECTURE.md#worker); argv for start in each form, list by tag, close and remove; no secret in any argv

## 4. Domain: `agent`

- [ ] 4.1 Create `internal/agent` with the `Agent` interface (`Parse`, `Run`, `Adapter`, `Probe`, `Hooks`) and its values `Call`, `Needs`, `Probe` and `State` (Running, Stopped) ([`agent.Agent`](../../../docs/ARCHITECTURE.md#agentagent--an-agent-programs-contract), [Agent](../../../docs/ARCHITECTURE.md#agent)): an interface and values only, depending on `pathmap`, `env` and `secret`
- [ ] 4.2 Tests: a `Probe` reads an answer into a `State`, and a `Needs` lists exactly what it was built with

## 5. Domain: `workspace`

- [ ] 5.1 Create `internal/workspace` with `Workspace`, `State` (Empty, Detached, Attached) computed on every call, and `Members` ([Workspace](../../../docs/ARCHITECTURE.md#workspace), [Concrete types](../../../docs/ARCHITECTURE.md#concrete-types)): `Open`, `State`, `PathMap`, `AddFS`, `AddNet`, `Start`, `Attach`, `Detach`, `Close`
- [ ] 5.2 `Open` finds [the workspace a directory belongs to](../../../docs/ARCHITECTURE.md#which-workspace-a-directory-belongs-to), with a linked worktree's main working tree given as a value, and leases a relay with the same two ends instead of making a second ([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)). Move the root lookup of `internal/mirror/roots.go`
- [ ] 5.3 Derive every name and tag of [Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags) here and hand them to `relay` and `worker`; read the workspace's workers by `@hermod-workspace` and its FS relays by `hermod-workspace`
- [ ] 5.4 `Attach` connects the relays, the IO relay to one worker's stdio among them, and returns when that relay or an FS relay stops serving; a second reader of a line stream is refused
- [ ] 5.5 `Detach` gives up every lease the process holds, suspending while a worker remains or the workers cannot be read and releasing otherwise, and returns every error together ([Detach](../../../docs/ARCHITECTURE.md#detach)); `Close` connects, closes the worker, or removes it when it is Exited, and detaches ([Close](../../../docs/ARCHITECTURE.md#close))
- [ ] 5.6 Tests: the state table and diagram of [Workspace](../../../docs/ARCHITECTURE.md#workspace); the three rules of which workspace a directory belongs to, including a sibling worktree and a root that holds existing roots; names and labels; `Detach` with a worker, none, and an unreadable list; `Close` with another worker left and with none

## 6. Application: `session`, the attach

- [ ] 6.1 Create `internal/session` with `Attach` and `ExitStatus`, running the steps of [Session](../../../docs/ARCHITECTURE.md#session-hermod-attach-and-claude-on-a-terminal): open the workspace, read the worker's and the agent's states in one remote call, decide from the table, start, attach, detach. Move the decision and the setup order of `internal/control/run.go` and `internal/sandbox/agent.go`
- [ ] 6.2 Exit at once after a detach caused by stdin EOF, a signal or a dropped connection, so the program that started the process never has to kill it
- [ ] 6.3 [One-shot call](../../../docs/ARCHITECTURE.md#one-shot-call): run the command on the remote with the caller's stdio and its exit code, in the project's remote directory when it exists, else in the remote user's home; no workspace, worker or relay
- [ ] 6.4 Implement `relay.Filter` for an agent's hooks from the token and routes the agent declared, mapping the paths in the bodies and substituting the client's current token; run without the Net relay and log it when its port is taken ([Hooks](../../../docs/ARCHITECTURE.md#hooks)). Move the token and path checks of `internal/hook/hook.go`
- [ ] 6.5 Tests: every row of the decision table, and a worker with no agent; the start order (FS relays the agent requires, then the worker's directory, then the worker); a one-shot call's exit code and directory; the filter for the right token, a wrong token, an unnamed route and a mapped body

## 7. Application: `session`, the chat

- [ ] 7.1 Declare `Channel`, `Receiver[T]`, `Sender[T]`, `FuncSender`, `TransformSender`, `Message`, `Kind` (`Pass`, `Open`, `Suspend`, `Close`, `Question`, `Answer`), `MessageID`, `ChatID`, `Path` and `PathMap` ([`session.Channel` and `session.Message`](../../../docs/ARCHITECTURE.md#sessionchannel-and-sessionmessage--a-chat-clients-connection))
- [ ] 7.2 Add the generic `StateMachine[S]` with `SetState` and `Abort` ([Router, sessions and chats](../../../docs/ARCHITECTURE.md#router-sessions-and-chats))
- [ ] 7.3 Implement the chat level: `ChatControl` and the states Unopened, Opening, Open, Suspended and Closed, with `Open`, `Answered`, `Output`, `Suspend` and `Close` as the events ([Chat states](../../../docs/ARCHITECTURE.md#chat-states)); a Suspended chat stores each question it holds through `SetState`, and sends them after the answer when it is opened again
- [ ] 7.4 Implement the session level: `SessionControl` and the states Attached and Detached, counting the chats the client waits on, detaching the workspace first and switching only on success, matching only an adapter message that is not a `Question` against the opening map ([Events](../../../docs/ARCHITECTURE.md#events)), and removing from the shared maps what the session put there ([Session states](../../../docs/ARCHITECTURE.md#session-states), [Shared maps](../../../docs/ARCHITECTURE.md#shared-maps))
- [ ] 7.5 Implement the router `Chat`, given the client's `Channel`, the own adapter's `Channel` and `attach`: the sessions and owners maps, one forwarder per adapter into one loop, routing by [Events](../../../docs/ARCHITECTURE.md#events), and paths `Mapped` by each session's path map
- [ ] 7.6 Renumber each `Question` to `hermod-<n>` on its way to the client and route its `Answer` back under the adapter's own ID ([Request IDs](../../../docs/ARCHITECTURE.md#request-ids))
- [ ] 7.7 Route to the own adapter [outside any workspace](../../../docs/ARCHITECTURE.md#outside-any-workspace) every client message that names no chat: send it the client's `initialize` without the `fs` and `terminal` client capabilities and the chat list, keep the `initialize`, and send it to each workspace's adapter that starts
- [ ] 7.8 Tests: every transition of the chat diagram and a refused event in each state; a question that arrives while the chat is Suspended and is delivered after the answer that opens it again; every transition of the session diagram, including a failed detach; every row of [Events](../../../docs/ARCHITECTURE.md#events) through fake `Channel`s, with two workspaces whose adapters send the same request IDs, and an adapter's `Question` that carries the ID of an `Open` still waiting; the client's connection ending detaches every session

## 8. Adapter: `claude`

- [ ] 8.1 Rewrite `internal/claude` as `Agent`, implementing `agent.Agent` with pure functions ([Agent](../../../docs/ARCHITECTURE.md#agent)): `Parse` from the current classification and session-ID extraction (a conversation and its ID, or a one-shot call), with a chat session no longer a kind of its own
- [ ] 8.2 `Run`: the command with paths mapped, every `--mcp-config` written to the worker's directory as given with only its paths mapped and the option pointed at it, no `--name` ([agent-session: Rewrite Claude's arguments for the remote](specs/agent-session/spec.md)); the files, the names of the declared variables ([agent-session: Only declared variables reach the remote](specs/agent-session/spec.md)), the transcript directories as FS relay ends, and the hooks' port. Move the argv rewrite and environment selection of `rewrite.go` and `env.go`
- [ ] 8.3 `Adapter`: the `Needs` of `claude-agent-acp` on the remote, of the same shape as `Run`'s
- [ ] 8.4 `Probe`: the command `claude agents --json` and how to read a conversation's state from its output. Move the reading of `internal/sandbox/agent.go`
- [ ] 8.5 `Hooks`: the settings document with its hooks aimed at a port and carrying a token. Move `settings.go`
- [ ] 8.6 Tests: `Parse` for every kind of arguments; `Run` and `Adapter` with a path map, and the declared variables with local paths, a parent Claude's variables and an undeclared one in the caller's environment; the probe's reading for Running, Stopped and an unreadable answer; `Hooks` for documents with and without hooks

## 9. Adapter: `acp`

- [ ] 9.1 Create `internal/acp` with `Conn`, implementing `session.Channel` over a pair of streams, one message per line, and its `message` implementing `session.Message`: `Kind` from ACP's shape and standard methods, and a client's own methods from the table it is given ([Air's messages](../../../docs/ARCHITECTURE.md#airs-messages)); `Dir` from `session/new`, `session/load` and `session/resume`; `WithID`, `Mapped` through `pathmap`, `Reply`
- [ ] 9.2 Tests: the `Kind` of every row of Air's messages with Air's table and without it; IDs as numbers and strings; `Mapped` on every path-carrying field; a partial final line; the connection ending closes `Receive` and sets `Err`

## 10. Adapter: `air`

- [ ] 10.1 Rewrite `internal/air` with `Config` (`Load`, `Merge`, `Save`) and `Entry`, keeping every entry and key it does not own and writing `command` as an absolute path ([`hermod air configure claude [REMOTE]`](design.md#hermod-air-configure-claude-remote)); move `ReadACP`, `Merge` and the file replacement of `air.go`
- [ ] 10.2 `Configure`: merge the remote's entry, and return the stand-in link's path when one is found and the `HERMOD_REMOTE=<remote>` line for terminal mode ([Registration](../../../docs/ARCHITECTURE.md#registration-hermod-air-configure))
- [ ] 10.3 `Methods`: Air's table for `acp`, `session/close` as `Suspend` and `_session/archive` as `Close`; move the method names of `archive.go`
- [ ] 10.4 Remove `LinkStandIn` and `StandInPath`'s creation and repair; add the link to `make install`
- [ ] 10.5 Tests: merge into an empty, a full and a linked `acp.json`, idempotent; `--name` and `--origin` set, empty and absent; the methods table

## 11. Adapter: `git`

- [ ] 11.1 In `internal/git`, keep `Identity` and `Read`, and add `MainWorktree`, the main working tree of a linked worktree, as read-only `git` commands
- [ ] 11.2 Tests: identity present and missing; a directory in a main tree, in a linked worktree and outside any repository

## 12. Adapter: `cli`

- [ ] 12.1 Rewrite `internal/cli` for the commands of [Command line](design.md#command-line): `attach [REMOTE] [-s NAME] [-C DIR] [-R PORT:ADDRESS]… [-n] [-- COMMAND…]`, `air configure claude [REMOTE] [--name] [--origin] [-n]` and `air acp claude [REMOTE]`, with `HERMOD_REMOTE`, `HERMOD_REMOTE_CMD` and `HERMOD_ORIGIN` named here and resolved once, an argument winning over its variable ([The command-line seam](../../../docs/ARCHITECTURE.md#the-command-line-seam)); keep the ssh host completion of `sshconfig.go`
- [ ] 12.2 Build what each command needs and hand it into the core: the shells, `claude.Agent`, the git facts, and for `air acp` the client's `acp.Conn` over stdin and stdout with Air's table, the own adapter started over ssh from what `Agent.Adapter` declares, and the `attach` function that starts the workspace's adapter when none runs ([Chat](../../../docs/ARCHITECTURE.md#chat-hermod-air-acp))
- [ ] 12.3 The stand-in: started as `claude`, build the request `attach -- claude ARGS…` builds with the remote from `HERMOD_REMOTE`; send all of Hermod's output, the dry-run plan included, to stderr and the log (mode `0600`); without a terminal run one-shot calls and refuse a conversation, naming `hermod air acp` ([The stand-in](design.md#the-stand-in-hermod-named-claude))
- [ ] 12.4 Tests: every argument and variable combination for each command, a missing remote failing before any side effect, both program names, and the stand-in without a terminal

## 13. Removal

- [ ] 13.1 Delete `internal/control`, `internal/sandbox`, `internal/mirror` and `internal/hook` with their tests
- [ ] 13.2 Delete what of `internal/air`, `internal/claude` and `internal/cli` the groups above did not keep
- [ ] 13.3 Delete `draft/`
- [ ] 13.4 Run `gofmt -l .`, `go vet ./...`, `go test ./...` and `go build ./...` on the whole module, and check that the imports match [Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies)

## 14. Documentation

- [ ] 14.1 Update README.md for remotes: usage of `attach`, `air configure` and `air acp`, the variables, `attach -R`, the stand-in link from `make install`, and the project structure from [Packages](../../../docs/ARCHITECTURE.md#packages); retarget its link to `design.md#agent-session`, a heading design.md does not have, to [Session](../../../docs/ARCHITECTURE.md#session)
- [ ] 14.2 In docs/ARCHITECTURE.md, retarget the Workspace section's link to `design.md#workspace` to [Which workspace a directory belongs to](../../../docs/ARCHITECTURE.md#which-workspace-a-directory-belongs-to) and [How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)
- [ ] 14.3 In docs/air-integration.md, retarget the links to `#hermod-air-configure-claude-remote` and `#an-fs-relay-never-picks-a-winner`, and replace the sandbox, `hermod-<uuid>`, pause and teardown and `--node`/`--acp` wording
- [ ] 14.4 Run `openspec validate add-air-bridge --strict`
