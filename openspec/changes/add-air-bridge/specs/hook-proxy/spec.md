## ADDED Requirements

### Requirement: A remote port and a token per worker
When a worker runs an agent whose settings carry Air's hooks, Hermod SHALL pick a free port on the remote and
generate a token for that worker, and SHALL write the agent's hook settings with the hook URL set to
`localhost:<port>` and the worker's token in place of Air's. Air's hook token SHALL never reach the remote. The port
and the token SHALL be kept for as long as the worker exists, so a running agent's settings stay valid across
reattaches and IDE restarts, while Air's own port and token change with every IDE run. Hooks carried to Air SHALL
go to one local endpoint, Air's; hooks aimed at any other loopback endpoint SHALL be left out of the settings and
logged by scheme, host and path only, since a URL's user info or query may carry a credential, and the agent SHALL
start without them.

#### Scenario: Air's token stays local
- **WHEN** the caller's settings carry Air's hook URL and token
- **THEN** the settings on the remote carry `localhost:<port>` and the worker's token, and not Air's token

#### Scenario: Settings survive an IDE restart
- **WHEN** Air restarts with a new hook port and token while the agent keeps running, and the agent is attached
  again
- **THEN** the agent's hooks reach Air through the same remote port and token it was started with

#### Scenario: A second local endpoint
- **WHEN** the caller's settings send hooks to Air and to another endpoint on this machine's loopback
- **THEN** the agent starts with Air's hooks carried and the other endpoint's hooks left out, logged

#### Scenario: Concurrent workers
- **WHEN** two workers with hooks run at once on the same remote
- **THEN** each has its own remote port and its own token

### Requirement: Hooks reach Air's current endpoint through a filter
While a session is attached to a worker whose settings carry Air's hooks, a call to the worker's remote port SHALL
reach Air's current hook URL on the local workstation. Hermod SHALL pass on only calls bearing the worker's token,
and of those only `POST`s to the path and query of a hook the settings route. It SHALL map paths in request bodies
from the remote to the local workstation and in response bodies back, through the workspace's path map, and SHALL
forward with Air's current token in the `Authorization` header.

#### Scenario: Hook reaches Air with local paths
- **WHEN** Claude on the remote posts a `PostToolUse` hook naming a remote file
- **THEN** Air receives the hook naming the local file, with Air's current token

#### Scenario: Wrong token refused
- **WHEN** a call reaches the remote port without the worker's token
- **THEN** it is not passed on to Air

#### Scenario: Only the routed hooks pass
- **WHEN** a call bearing the worker's token goes to another path, another query or with another method than a
  routed hook's
- **THEN** it is not passed on to Air

#### Scenario: No Air hooks, no relay
- **WHEN** the caller's settings carry no Air hooks
- **THEN** no remote port is relayed for hooks

### Requirement: Hooks reach Air only while a session is attached
The remote port SHALL reach Air only while a session is attached to the worker. When the last session detaches,
the hook relay SHALL be removed and nothing SHALL be buffered for it: an agent that keeps running without a session
has its hook calls fail, which Air's hooks treat as passing. Attaching again SHALL relay the same port.

#### Scenario: Last tab closed
- **WHEN** Air closes the last chat tab of a project and the adapter's agent then posts a hook
- **THEN** the call does not reach Air and the agent keeps running

#### Scenario: Reattached
- **WHEN** a session attaches again to a worker that kept running
- **THEN** the agent's hooks reach Air again through the same remote port

### Requirement: A taken hook port costs only the hooks
When the worker's remote port cannot be bound for the relay, the session SHALL run without hooks and log it, and the
next session attached to the worker SHALL try the same port again.

#### Scenario: Port taken
- **WHEN** another process holds the worker's hook port on the remote
- **THEN** the session attaches without hooks and the failure is logged

### Requirement: Tokens never print
Hook tokens, Air's and the worker's, SHALL appear in no log line, error or dry-run plan; they SHALL be written only
where they are delivered.

#### Scenario: Dry-run plan hides tokens
- **WHEN** a dry run prints the plan for a session with hooks
- **THEN** neither token appears in the output
