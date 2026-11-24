The packages are built bottom-up, in the layers of
[Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies), each against the
[Packages](../../../docs/ARCHITECTURE.md#packages) table and the section a task names. `internal/control`,
`internal/sandbox`, `internal/mirror` and `internal/hook` are not adapted to the API changes below them: they are
deleted in group 13, so until then each group's checks run on the group's own packages
(`go test ./internal/<package>/...`), and group 13 runs them on the whole module.

Every group's tests are table-driven, and the core is tested through a fake `shell.Executor`, as
[Interfaces](../../../docs/ARCHITECTURE.md#interfaces) says.

## 1. Foundation: `env`, `secret`, `shell`, `pathmap`

- [x] 1.1 Create `internal/env` with `Vars`, the one place that splits and joins `KEY=VALUE`: build from pairs and from a process environment, read a key, select by key, and render for a command. A value may be a `secret.Token`, which prints masked; move `secret.Env`'s behaviour here
- [x] 1.2 Reduce `internal/secret` to `Token`: keep `NewToken`, `FromString`, `Reveal`, `IsZero`, `Equal` and every masked form; remove `secret.Env` once `env.Vars` replaces it
- [x] 1.3 In `internal/shell`, turn `Shell.Run` into `Executor.Exec(Command) Execution` ([`shell.Executor`](../../../docs/ARCHITECTURE.md#shellexecutor--a-workstation)): one `Command` with the arguments, directory, `env.Vars` and streams; an `Execution` with the running command's streams and its result. Keep `local`, dry-run, `ssh` (with `WithTty`, `WithoutSharedMaster`, `WithReverseForward`), `login` and `tmux`, `Quote` and `ExitCode`, rewritten to the new types
- [x] 1.4 In `internal/pathmap`, rename the API to `Map` with `ToRemote` and `ToLocal` and `Place`, built from pairs of a local and a remote root, longest root first, and applied to a string and to every string in a JSON value ([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)); keep the matching at a path-segment boundary, `Under` and `Absolute`
- [x] 1.5 Tests: `env.Vars` split, join and masking; `secret.Token` in every `fmt` form; each `Executor` rewrite and the nesting of wrappers with dry-run chosen at the innermost one; `pathmap` in both directions, a path under no root, nested roots and JSON values

## 2. Domain: `relay`

- [x] 2.1 Create `internal/relay` with `Relay`, `Lease`, `State` (Absent, Disconnected, Connected) and `Filter` ([`relay.Relay` and `relay.Lease`](../../../docs/ARCHITECTURE.md#relayrelay-and-relaylease--one-channel-and-a-claim-on-it), [Relay](../../../docs/ARCHITECTURE.md#relay)). `Connect` takes the lease, makes an Absent relay, takes it to Connected and starts what serves it in this process; reading a state that fails is an error, not a state
- [x] 2.2 Leases as [Leases](../../../docs/ARCHITECTURE.md#leases) describes: `Suspend`, `Release`, `Done` and `Err`. Move the locking of `internal/mirror/lease.go`
- [x] 2.3 FS relay from its two roots, a name and labels: a Mutagen `two-way-safe` session and `Flush`; check a found relay's remote end before resuming it. Move the `mutagen` argv building, labels and endpoint check of `internal/mirror/mirror.go`
- [x] 2.4 IO relay from a remote stdio and local streams: copies both ways for a terminal, and for a line stream copies the local input into the FIFO one complete line at a time while tailing the logs ([Line streams](../../../docs/ARCHITECTURE.md#line-streams)); `Done` when the local side leaves or the process ends. Move the line pump of `internal/control/stream.go`
- [x] 2.5 Net relay from a remote port, a local address and an optional `Filter`: `ssh -R` on a connection that refuses a shared master, to a listener in this process; suspending its last lease removes it, as releasing does. Move the listener of `internal/hook/hook.go`, without its token and path logic, which becomes `session`'s filter (6.4)
- [x] 2.6 Tests: the state table and diagram of [Relay](../../../docs/ARCHITECTURE.md#relay) for each kind, with two holders in one process; argv for FS create, resume, flush and an endpoint mismatch; a partial final line on an IO relay; a Net relay with and without a filter

## 3. Domain: `worker`

- [x] 3.1 Create `internal/worker` with `Worker`, `State` (Absent, Running, Exited), `Form` (terminal, line stream), `Stdio` and `List` by tag ([Worker](../../../docs/ARCHITECTURE.md#worker)); it knows nothing about what its process is
- [x] 3.2 Embed the [remote scripts](../../../docs/ARCHITECTURE.md#remote-scripts), one that keeps a failed process's pane and one that gives a line stream its FIFO and logs, and send each to the remote's `sh` on stdin with its inputs as arguments and environment
- [x] 3.3 Write the [worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory) (mode `0700`) over ssh stdin, run every setup command with stdin from `/dev/null`, and close a worker with SIGHUP, waiting on a line stream's process ID
- [x] 3.4 Tests: the state table and diagram of [Worker](../../../docs/ARCHITECTURE.md#worker); no secret in any argv

## 4. Domain: `agent`

- [x] 4.1 Create `internal/agent` with `Conversation` and `State` (Running, Stopped) ([Agent](../../../docs/ARCHITECTURE.md#agent))

## 5. Domain: `workspace`

- [x] 5.1 Create `internal/workspace` with `Workspace`, `State` (Empty, Detached, Attached) computed on every call, and `Members` ([Workspace](../../../docs/ARCHITECTURE.md#workspace), [Concrete types](../../../docs/ARCHITECTURE.md#concrete-types)): `Open`, `Map`, `State`, `PathMap`, `AddFS`, `AddNet`, `Attach`, `Detach`, `Close`
- [x] 5.2 `Open` finds [the workspace a directory belongs to](../../../docs/ARCHITECTURE.md#which-workspace-a-directory-belongs-to), with a linked worktree's main working tree given as a value, and leases a relay with the same two ends instead of making a second ([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)). Move the root lookup of `internal/mirror/roots.go`
- [x] 5.3 Derive every name and tag of [Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags) here and hand them to `relay` and `worker`; read the workspace's workers by `@hermod-workspace` and its FS relays by `hermod-workspace`, and for `Map` every FS relay of the remote by `hermod-remote`, a directory under none of their roots mapped by the rule of a new workspace in either direction
- [x] 5.4 `Attach` connects the relays, the IO relay to one worker's stdio among them, and returns when that relay or an FS relay stops serving; a second reader of a line stream is refused
- [x] 5.5 `Detach` gives up every lease the process holds, suspending while a worker remains or the workers cannot be read and releasing otherwise, and returns every error together ([Detach](../../../docs/ARCHITECTURE.md#detach)); `Close` connects, closes the worker, or removes it when it is Exited, and detaches ([Close](../../../docs/ARCHITECTURE.md#close))
- [x] 5.6 Tests: the state table and diagram of [Workspace](../../../docs/ARCHITECTURE.md#workspace); the three rules of which workspace a directory belongs to, including a sibling worktree and a root that holds existing roots; names and labels; `Detach` with a worker, none, and an unreadable list; `Close` with another worker left and with none; `Map` with no relay on the remote, one workspace, two workspaces with nested roots, and a directory under no root in both directions

## 6. Application: `session`, the attach

- [x] 6.1 Create `internal/session` with `Attach` and `ExitStatus`, running the steps of [Session](../../../docs/ARCHITECTURE.md#session-hermod-attach-and-claude-on-a-terminal): open the workspace, decide from the table, attach, detach. Move the decision of `internal/control/run.go` and `internal/sandbox/agent.go`
- [x] 6.2 Exit at once after a detach caused by stdin EOF, a signal or a dropped connection, so the program that started the process never has to kill it
- [x] 6.3 [One-shot call](../../../docs/ARCHITECTURE.md#one-shot-call): run the command on the remote with the caller's stdio and its exit code, in the project's remote directory when it exists, else in the remote user's home; no workspace, worker or relay
- [x] 6.4 Implement `relay.Filter` for hooks, accepting only the worker's token and the routes it is given, mapping the paths in the bodies and substituting the client's current token; run without the Net relay and log it when its port is taken ([Hooks](../../../docs/ARCHITECTURE.md#hooks)). Move the token and path checks of `internal/hook/hook.go`
- [x] 6.5 Tests: every row of the decision table, and a worker with no agent; a one-shot call's exit code and directory; the filter for the right token, a wrong token, an unnamed route and a mapped body

## 7. Application: `session`, the chat

- [x] 7.1 Declare `Channel`, `Receiver[T]`, `Sender[T]`, `FuncSender`, `TransformSender`, `Message`, `Kind` (`Pass`, `Connect`, `List`, `Cancel`, `Open`, `Suspend`, `Close`, `Question`, `Answer`), with `Reply` carrying an answer and `Fail`, `MessageID`, `ChatID`, `Path` and `PathMap` ([`session.Channel` and `session.Message`](../../../docs/ARCHITECTURE.md#sessionchannel-and-sessionmessage--a-chat-clients-connection))
- [x] 7.2 Add the generic `StateMachine[S]` with `SetState` and `Abort` ([Router, sessions and chats](../../../docs/ARCHITECTURE.md#router-sessions-and-chats))
- [x] 7.3 Implement the chat level: `ChatControl` and the states Unopened, Opening, Open, Suspended and Closed, with `Open`, `Answered`, `Output`, `Suspend` and `Close` as the events, `Suspend` and `Close` reporting whether the client waited on the chat, and a located chat made in Suspended or Unopened from its conversation's state ([Chat states](../../../docs/ARCHITECTURE.md#chat-states)); a Suspended chat stores each question it holds through `SetState`, and sends them after the answer when it is opened again
- [x] 7.4 Implement the session level: `SessionControl` and the states Attached and Detached, counting the chats the client waits on, detaching the workspace first and switching only on success, and after a Close that leaves no chat waited on, recording its ID and, at the adapter's answer and still waiting on no chat, closing the adapter worker when the agent's `List` for the workspace's root shows no conversation of the workspace, in the root or under it, running and detaching otherwise, matching only an adapter message that is not a `Question` against the opening map ([Events](../../../docs/ARCHITECTURE.md#events)), and removing from the shared maps what the session put there ([Session states](../../../docs/ARCHITECTURE.md#session-states), [Shared maps](../../../docs/ARCHITECTURE.md#shared-maps))
- [x] 7.5 Implement the router `Chat`, given the client's `Channel`, the agent, the remote and `attach`, which returns the workspace, its root, its path map, the adapter's `Channel` and whether it started the adapter: the sessions, owners and pending maps, one forwarder per adapter into one loop, routing by [Events](../../../docs/ARCHITECTURE.md#events), and paths `Mapped` by each session's path map
- [x] 7.6 Renumber each `Question` to `hermod-<n>` on its way to the client and route its `Answer` back under the adapter's own ID ([Request IDs](../../../docs/ARCHITECTURE.md#request-ids))
- [x] 7.7 Answer what names no chat ([The client's connection](../../../docs/ARCHITECTURE.md#the-clients-connection)): `Connect`, keeping it and sending it first to each adapter that `attach` reports it started, its answer matched by the `Connect`'s ID and dropped; `List`, keeping the conversations of exactly the directory asked, paths mapped by `workspace.Map`; `Cancel` routed by the pending request it cancels; and locate a chat no session holds on a `Suspend`, a `Close` or an `Open` that names it, answering a chat the agent program does not have with `Fail` ([Events](../../../docs/ARCHITECTURE.md#events))
- [x] 7.8 Tests: every transition of the chat diagram and a refused event in each state; a question that arrives while the chat is Suspended and is delivered after the answer that opens it again; every transition of the session diagram, including a failed detach; every row of [Events](../../../docs/ARCHITECTURE.md#events) through fake `Channel`s, with two workspaces whose adapters send the same request IDs, and an adapter's `Question` that carries the ID of an `Open` still waiting; a located chat Running and Stopped, and one the agent program does not have; a located chat's `Suspend` detaching the session attached for it; the last Close with a conversation still running in the session's directory, with one running only in a subdirectory of the workspace, with none, and with an `Open` arriving before the adapter's answer to it; the adapter's answer to the kept `Connect` dropped; a `List` for a directory no relay covers; the client's connection ending detaches every session

## 8. Adapter: `claude`

- [x] 8.1 Rewrite `internal/claude` for Claude Code's arguments: the classification and session-ID extraction (a conversation and its ID, or a one-shot call); the arguments with paths mapped, every `--mcp-config` kept as given with only its paths mapped, no `--name` ([agent-session: Rewrite Claude's arguments for the remote](specs/agent-session/spec.md)); the declared variables ([agent-session: Only declared variables reach the remote](specs/agent-session/spec.md)); and the reading of which conversations exist, with each one's directory (the transcript's `cwd`), title and last update, and which run (`claude agents --json`). Move the argv rewrite and environment selection of `rewrite.go` and `env.go`, and the reading of `internal/sandbox/agent.go`
- [x] 8.2 Tests: every kind of arguments; the arguments with a path map; the declared variables with local paths, a parent Claude's variables and an undeclared one in the caller's environment; the reading of conversations, including a Stopped one that has a transcript but is not listed by `claude agents`

## 9. Adapter: `acp`

- [x] 9.1 Create `internal/acp` with `Conn`, implementing `session.Channel` over a pair of streams, one message per line
- [x] 9.2 Tests: a partial final line; the connection ending closes `Receive` and sets `Err`

## 10. Adapter: `air`

- [x] 10.1 Rewrite `internal/air` with `Config` (`Load`, `Merge`, `Save`) and `Entry`, keeping every entry and key it does not own and writing `command` as an absolute path ([`hermod air configure claude [REMOTE]`](design.md#hermod-air-configure-claude-remote)); move `ReadACP`, `Merge` and the file replacement of `air.go`
- [x] 10.2 `Configure`: merge the remote's entry, and return the stand-in link's path when one is found and the `HERMOD_REMOTE=<remote>` line for terminal mode ([Registration](../../../docs/ARCHITECTURE.md#registration-hermod-air-configure))
- [x] 10.4 Remove `LinkStandIn` and `StandInPath`'s creation and repair; add the link to `make install`
- [x] 10.5 Tests: merge into an empty, a full and a linked `acp.json`, idempotent; `--name` and `--origin` set, empty and absent

## 11. Adapter: `git`

- [x] 11.1 In `internal/git`, keep `Identity` and `Read`, and add `MainWorktree`, the main working tree of a linked worktree, as read-only `git` commands
- [x] 11.2 Tests: identity present and missing; a directory in a main tree, in a linked worktree and outside any repository

## 12. Adapter: `cli`

- [x] 12.1 Rewrite `internal/cli` for the commands of [Command line](design.md#command-line): `attach [REMOTE] [-s NAME] [-C DIR] [-R PORT:ADDRESS]… [-n] [-- COMMAND…]`, `air configure claude [REMOTE] [--name] [--origin] [-n]` and `air acp claude [REMOTE]`, with `HERMOD_REMOTE`, `HERMOD_REMOTE_CMD` and `HERMOD_ORIGIN` named here and resolved once, an argument winning over its variable ([The command-line seam](../../../docs/ARCHITECTURE.md#the-command-line-seam)); keep the ssh host completion of `sshconfig.go`
- [x] 12.2 Build what each command needs and hand it into the core: the shells, the git facts, and for `air acp` the client's `acp.Conn` over stdin and stdout, the remote, and the `attach` function that starts the workspace's adapter when none runs and reports whether it did ([Chat](../../../docs/ARCHITECTURE.md#chat-hermod-air-acp))
- [x] 12.3 The stand-in: started as `claude`, build the request `attach -- claude ARGS…` builds with the remote from `HERMOD_REMOTE`; send all of Hermod's output, the dry-run plan included, to stderr and the log (mode `0600`); without a terminal run one-shot calls and refuse a conversation, naming `hermod air acp` ([The stand-in](design.md#the-stand-in-hermod-named-claude))
- [x] 12.4 Tests: every argument and variable combination for each command, a missing remote failing before any side effect, both program names, and the stand-in without a terminal

## 13. Removal

- [x] 13.1 Delete `internal/control`, `internal/sandbox`, `internal/mirror` and `internal/hook` with their tests
- [x] 13.2 Delete what of `internal/air`, `internal/claude` and `internal/cli` the groups above did not keep
- [x] 13.3 Delete `draft/` and `air-probe/`
- [x] 13.4 Run `gofmt -l .`, `go vet ./...`, `go test ./...` and `go build ./...` on the whole module, and check that the imports match [Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies)

## 14. Documentation

- [x] 14.1 Update README.md for remotes: usage of `attach`, `air configure` and `air acp`, the variables, `attach -R`, the stand-in link from `make install`, and the project structure from [Packages](../../../docs/ARCHITECTURE.md#packages); retarget its link to `design.md#agent-session`, a heading design.md does not have, to [Session](../../../docs/ARCHITECTURE.md#session)
- [x] 14.2 In docs/ARCHITECTURE.md, retarget the Workspace section's link to `design.md#workspace` to [Which workspace a directory belongs to](../../../docs/ARCHITECTURE.md#which-workspace-a-directory-belongs-to) and [How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)
- [x] 14.3 In docs/air-integration.md, retarget the links to `#hermod-air-configure-claude-remote` and `#an-fs-relay-never-picks-a-winner`, and replace the sandbox, `hermod-<uuid>`, pause and teardown and `--node`/`--acp` wording
- [x] 14.4 Run `openspec validate add-air-bridge --strict`

## 15. `shell` and `worker`: tmux in `shell`

- [ ] 15.1 In `internal/shell`, give the tmux wrapper all of tmux's command language: making a session with its options, quoting what goes on tmux's command line, and listing the sessions with their options in one format ([`shell.Executor`](../../../docs/ARCHITECTURE.md#shellexecutor--a-workstation)). Move `tmuxWord`, the session creation and the `ls` format out of `internal/worker`
- [ ] 15.2 Reduce `internal/worker` to lifecycle: key, tags, state, start, close, remove and its [scripts](../../../docs/ARCHITECTURE.md#remote-scripts), through `shell`'s tmux, building no tmux command; `Spec` is key, directory, command, variables, tags and `Form`, its variables written to the worker's `env`; the worker's directory is given to its process as the variable whose name the `env` package declares, `HERMOD_WORKER_DIR` ([Worker](../../../docs/ARCHITECTURE.md#worker), [A worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory))
- [ ] 15.3 Tests: tmux argv for a session with options, quoting, and the listing; `worker` start, close, remove and list through a fake `shell.Executor`; no secret in any argv

## 16. `relay`

- [ ] 16.1 Rename `Ends` to `Roots` and move it into the FS relay's file ([`relay.Relay` and `relay.Lease`](../../../docs/ARCHITECTURE.md#relayrelay-and-relaylease--one-channel-and-a-claim-on-it))
- [ ] 16.2 FS `List` reads the relays under a label, with their ends and states, from Mutagen's structured listing (`--template`) instead of its text ([Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags))
- [ ] 16.3 The FS relay takes a writer for Mutagen's output; move `cli`'s Mutagen output display into it
- [ ] 16.4 Start the Net relay's goroutines with `sync.WaitGroup.Go`
- [ ] 16.5 Tests: `List` from structured output with no relay, several, a paused one and labels; Mutagen's output reaching the writer

## 17. `agent` contract, `claude`, `session` delegation, hooks

- [ ] 17.1 In `internal/agent`, declare `Agent` (`Classify`, `List`, `Lookup`, `Start`, `StateDir`) and the optional `ACP` (`Adapter`, `Connect`) with `Invocation`, `Launch`, `Hook`, `Handshake` and `ErrUnknown` ([`agent.Agent` and `agent.ACP`](../../../docs/ARCHITECTURE.md#agentagent-and-agentacp--an-agent-programs-contract), [Agent](../../../docs/ARCHITECTURE.md#agent)). Delete the `Call`, `Needs`, `Probe`, `Listing` and `Lookup` values and `query.go`
- [ ] 17.2 Add `workspace.Host(key, form)`, a `shell.Executor` that starts the worker for each command executed on it and ends its `Execution` once the worker runs; it replaces `Workspace.Start` ([Concrete types](../../../docs/ARCHITECTURE.md#concrete-types))
- [ ] 17.3 Build `claude.Agent` with a `shell.Executor`, implementing `agent.Agent` and `agent.ACP`: `List` and `Lookup` run `conversations.sh` through it; `StateDir` asks its workstation for Claude's config directory; `Start` and `Adapter` take the declared variables of the `Launch`, map the arguments, carry the settings and the MCP configuration as variables that the start command writes into the directory named by `env`'s `HERMOD_WORKER_DIR`, and execute through the executor they are handed. It names no workstation, port, token or Air ([A worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory))
- [ ] 17.4 Hooks: `claude` applies the `Launch`'s function to each hook, a URL and its headers, of its settings and keeps only Claude's settings format; `air.IsHook` says which hook is Air's; `session` builds the function from it, the worker's port and token, aims the client's hooks, leaves other loopback hooks out and logs them, and gives its filter the routes it aimed; it picks the port through `workspace.FreePort`, records the port, token and aimed routes in the worker's directory through the `Host` the worker starts on, and on attaching to a running worker reads them back through `workspace.Look` to build the same filter ([Hooks](../../../docs/ARCHITECTURE.md#hooks))
- [ ] 17.5 `session` delegates to the agent: drop the `query` type; `Attach` asks `Classify` and `Lookup`, adds the FS relay between the local and the remote agent's `StateDir`, and has the agent `Start` through the workspace's `Host`; a command that is not an agent's is executed on the `Host` by `session`, and a one-shot call runs through `session`'s own shell ([Session flow](../../../docs/ARCHITECTURE.md#session-hermod-attach-and-claude-on-a-terminal), [One-shot call](../../../docs/ARCHITECTURE.md#one-shot-call)); the chat asks `List`, `Lookup` and the `ACP`'s `Connect` and `Adapter` ([Chat](../../../docs/ARCHITECTURE.md#chat-hermod-air-acp))
- [ ] 17.6 Tests: `claude` through a fake executor: `Classify` for every kind of arguments, `List` and `Lookup` readings with `ErrUnknown` for a conversation the program does not have, `StateDir`, and the command `Start` and `Adapter` execute with a path map and the declared variables; the hook function for documents with Air's hooks, another loopback endpoint's and none; `session`'s start order (the `StateDir` relay, then `Start` on the `Host`) with a fake agent
- [ ] 17.7 Tests: `workspace.Host` through a fake `shell.Executor`: the `Spec` built from an executed command (key, directory, command, variables, `Form`), its variables in `env` and its record's files in the worker's directory, none on a command line, and the `Execution` ending once the worker runs; `session` re-attaching to a hooked worker builds its filter from the recorded port, token and routes, and runs without the hooks' relay when none is recorded

## 18. `acp` and `air` serializers, `session.Message`

- [ ] 18.1 `session.Message` loses `Handshake` and agent-typed replies: `Reply` takes a `Result` (`EmptyResult`, `HandshakeResult`, `ListResult`) and `Result` reads one back; remove `WithoutClientCapabilities`, and keep the client's `Connect` as given ([`session.Channel` and `session.Message`](../../../docs/ARCHITECTURE.md#sessionchannel-and-sessionmessage--a-chat-clients-connection))
- [ ] 18.2 Declare `acp.Serializer` (`Decode`, `Encode`) and build `acp.Conn` with one; `Send` takes any `session.Message`; only parsed messages reach `Receive`; a rejected request is answered with `method not found` or `invalid params`, and any other rejected line is logged and dropped ([`acp.Serializer`](../../../docs/ARCHITECTURE.md#acpserializer--acps-messages-as-types))
- [ ] 18.3 `acp.Standard`: a Go type for each of ACP's messages with typed JSON-RPC IDs, an answer parsed by the type of the request it answers, each type's `Kind`, `Chat`, `Dir` and path mapping over the fields it declares, and ACP's `_meta` declared on every type and carried with every string in it mapped by the path map; a `Conn` built for an adapter writes `initialize` without the client's `fs` and `terminal` capabilities ([The client's connection](../../../docs/ARCHITECTURE.md#the-clients-connection)). Delete `id.go`, the `silent` flag and the method table
- [ ] 18.4 `air.Serializer`, extending `acp.Standard` with Air's own messages: `session/close` as `Suspend`, `_session/archive` as `Close`, `_session/unarchive` and `_session/rename` as `Pass` ([Air's messages](../../../docs/ARCHITECTURE.md#airs-messages)). Delete `air.Methods`
- [ ] 18.5 Tests: each message type read and written; IDs as numbers and strings; an unknown request, an unknown notification and a malformed request; the `Kind` of every row of Air's messages with Air's serializer and with the standard one; `Mapped` on every declared path field and on a path inside `_meta`; `initialize`'s `_meta.jetbrains.air` reaching an adapter, and its `fs` and `terminal` client capabilities not; every `Reply` and `Fail`, and the `Result` of an answer to `initialize`

## 19. `session` through `workspace`, `cli` wiring

- [ ] 19.1 `session` reaches workers and relays only through `workspace`, and imports `relay` only to implement `relay.Filter`: `workspace` declares the worker key, the stdio `Form`, a worker's state, the attach streams and the errors an attach or a connect lease ends with, which `session` uses ([Concrete types](../../../docs/ARCHITECTURE.md#concrete-types)) ([Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies))
- [ ] 19.2 `cli` only wires: it builds the executors, a local and a remote `claude.Agent`, Air's serializer and `air.IsHook`, hands `agent.ACP` to the chat path only when the agent implements it, and redefines no other package's constant, such as Claude's config directory variable ([The command-line seam](../../../docs/ARCHITECTURE.md#the-command-line-seam))
- [ ] 19.3 Tests: each command's wiring through fakes
- [ ] 19.4 Run `gofmt -l .`, `go vet ./...`, `go test ./...` and `go build ./...` on the whole module, and check that the imports match [Package dependencies](../../../docs/ARCHITECTURE.md#package-dependencies)

## 20. Documentation

- [ ] 20.1 Check README.md, docs/ARCHITECTURE.md and docs/air-integration.md against the code, each package against its [Packages](../../../docs/ARCHITECTURE.md#packages) row
- [ ] 20.2 Run `openspec validate add-air-bridge --strict`
