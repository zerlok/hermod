## ADDED Requirements

### Requirement: A path map is a set of root pairs
A path map SHALL hold (local root, sandbox root) pairs of absolute roots and SHALL map a path in either direction by
the pair whose root is the longest prefix of the path at a path-segment boundary. A path under no root SHALL be left
unchanged. The map SHALL know nothing about Claude or Air.

#### Scenario: Longest prefix wins
- **WHEN** the map holds `/home/me/dev → /home/u/dev` and `/home/me/dev/api-wt → /home/u/dev/api-wt` and maps `/home/me/dev/api-wt/main.go` to the sandbox
- **THEN** the result is `/home/u/dev/api-wt/main.go`

#### Scenario: Segment boundary respected
- **WHEN** the map holds `/home/me/dev/api → /home/u/dev/api` and maps `/home/me/dev/api2/x` to the sandbox
- **THEN** the path is left unchanged

#### Scenario: Sandbox to local
- **WHEN** the map holds `/home/me/dev/api → /home/u/dev/api` and maps `/home/u/dev/api/x.go` to local
- **THEN** the result is `/home/me/dev/api/x.go`

### Requirement: Map strings, JSON values and stream-json lines
A path map SHALL apply to a single string, to every string inside a JSON value at any depth, and to one
newline-delimited JSON line, leaving keys, non-string values and the line's framing unchanged.

#### Scenario: Nested JSON strings
- **WHEN** a JSON value with `cwd` and `tool_input.file_path` holding sandbox paths is mapped to local
- **THEN** both strings hold local paths and every other field is unchanged

#### Scenario: One stream-json line
- **WHEN** one newline-terminated stream-json line is mapped
- **THEN** the output is one newline-terminated line with its paths mapped

### Requirement: One path map per run
At the start of an agent session Hermod SHALL build the run's path map from the mirrors Mutagen lists for the
sandbox: when the session's directory lies inside a mirrored local root, that mirror's pair SHALL be used; otherwise
the directory SHALL become a new root mirrored to the corresponding sandbox path. The map SHALL stay fixed for the
run and SHALL be the one applied to Claude's arguments, settings, hook bodies and the chat stream.

#### Scenario: Directory inside a mirrored root
- **WHEN** a session starts in `/home/me/dev/api/sub` and `/home/me/dev/api` is already mirrored to the sandbox
- **THEN** the session uses that mirror and no new mirror is created

#### Scenario: Sibling worktree
- **WHEN** a session starts in `/home/me/dev/api-wt`, outside every mirrored root
- **THEN** `/home/me/dev/api-wt` becomes a new root mirrored to its corresponding sandbox path
