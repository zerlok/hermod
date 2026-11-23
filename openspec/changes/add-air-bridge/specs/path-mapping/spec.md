## ADDED Requirements

### Requirement: A path map is a set of root pairs
A path map SHALL hold (local root, remote root) pairs of absolute roots and SHALL map a path in either direction by
the pair whose root is the longest prefix of the path at a path-segment boundary. A path under no root SHALL be left
unchanged. The map SHALL know nothing about Claude or Air.

#### Scenario: Longest prefix wins
- **WHEN** the map holds `/home/me/dev → /home/u/dev` and `/home/me/dev/api-wt → /home/u/dev/api-wt` and maps `/home/me/dev/api-wt/main.go` to the remote
- **THEN** the result is `/home/u/dev/api-wt/main.go`

#### Scenario: Segment boundary respected
- **WHEN** the map holds `/home/me/dev/api → /home/u/dev/api` and maps `/home/me/dev/api2/x` to the remote
- **THEN** the path is left unchanged

#### Scenario: Remote to local
- **WHEN** the map holds `/home/me/dev/api → /home/u/dev/api` and maps `/home/u/dev/api/x.go` to local
- **THEN** the result is `/home/me/dev/api/x.go`

### Requirement: Map strings and JSON values
A path map SHALL apply to a single string and to every string inside a JSON value at any depth, leaving keys and
non-string values unchanged.

#### Scenario: Nested JSON strings
- **WHEN** a JSON value with `cwd` and `tool_input.file_path` holding remote paths is mapped to local
- **THEN** both strings hold local paths and every other field is unchanged

#### Scenario: Keys and numbers untouched
- **WHEN** a JSON value whose key is a path and whose other value is a number is mapped
- **THEN** the key and the number are unchanged

### Requirement: The path map is the workspace's file relays
The path map of a session SHALL be the (local root, remote root) ends of its workspace's file relays. The session's
directory SHALL belong to the workspace whose project file relay covers it; else, for a linked git worktree, to the
workspace that covers its main working tree, where the worktree SHALL get a file relay of its own; else to a new
workspace whose project file relay joins the directory to the corresponding remote path. A new root that holds
existing roots SHALL get a file relay too, with a warning, and the inner workspaces SHALL stay. Once the file relay
that covers the session's directory is connected, the map SHALL stay fixed for the run.

#### Scenario: Directory inside a covered root
- **WHEN** a session starts in `/home/me/dev/api/sub` and `/home/me/dev/api` is already relayed to the remote
- **THEN** the session uses that workspace and its relay, and no new file relay is created

#### Scenario: Linked git worktree
- **WHEN** a session starts in `/home/me/dev/api-wt`, a linked git worktree of `/home/me/dev/api`, which is relayed
- **THEN** the session uses the workspace of `/home/me/dev/api`, and `/home/me/dev/api-wt` gets a file relay of its own in it

#### Scenario: Directory under no root
- **WHEN** a session starts in `/home/me/dev/web`, which no file relay covers and which is no linked worktree
- **THEN** a new workspace is made whose project file relay joins `/home/me/dev/web` to its corresponding remote path

#### Scenario: Root holding existing roots
- **WHEN** a session starts in `/home/me/dev` while `/home/me/dev/api` is relayed
- **THEN** `/home/me/dev` gets a file relay of its own, a warning is shown, and the workspace of `/home/me/dev/api` stays

### Requirement: One map for every path that crosses
Paths SHALL cross between the local and the remote workstation in a worker's arguments, ACP messages, hook bodies
and the files themselves, and the first three SHALL be mapped by the one map the files' relays define. Every ACP
message to an adapter SHALL be mapped to the remote's paths, and every ACP message to Air to the local ones.

#### Scenario: A chat opened in a local directory
- **WHEN** Air sends `session/new` with `cwd` `/home/me/dev/api` and the map holds `/home/me/dev/api → /home/u/dev/api`
- **THEN** the adapter receives `session/new` with `cwd` `/home/u/dev/api`

#### Scenario: A tool call reported back
- **WHEN** the adapter sends Air a notification whose tool call names `/home/u/dev/api/main.go`
- **THEN** Air receives it naming `/home/me/dev/api/main.go`

#### Scenario: A terminal worker's arguments
- **WHEN** a terminal session starts Claude with an argument naming `/home/me/dev/api/notes.md`
- **THEN** the worker on the remote is started with `/home/u/dev/api/notes.md`
