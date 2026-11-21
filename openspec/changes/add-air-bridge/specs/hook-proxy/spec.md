## ADDED Requirements

### Requirement: A sandbox port and token per session
When an agent session is created, Hermod SHALL pick a free sandbox port and generate a token for it, store them in
the session directory's `hookport` and `token`, and write the caller's hook settings into `settings.json` with the
hook URL set to `localhost:<hookport>` and the session's token in place of Air's. Air's hook token SHALL never reach
the sandbox. A running Claude's settings SHALL stay valid across reattaches and IDE restarts. The forward carries one
local endpoint: Air's, else the first in the settings. Hooks to any other loopback endpoint SHALL be left out of
`settings.json` and logged by scheme, host and path, since a URL's user info or query may carry a credential; the
session SHALL start without them.

#### Scenario: Air's token stays local
- **WHEN** the caller's settings carry Air's hook URL and token
- **THEN** the sandbox's `settings.json` carries `localhost:<hookport>` and the session token, and not Air's token

#### Scenario: A second local endpoint
- **WHEN** the caller's settings send hooks to Air and to another endpoint on this machine's loopback
- **THEN** the session starts with Air's hooks routed and the other endpoint's hooks left out, logged

#### Scenario: Concurrent sessions
- **WHEN** two sessions run at once
- **THEN** each has its own sandbox hook port

### Requirement: The hook proxy forwards to Air's current endpoint
When the caller's settings carry Air's hooks, Hermod SHALL start a hook proxy on `127.0.0.1:0` aimed at Air's
current hook URL and token, and SHALL carry `-R <hookport>:127.0.0.1:<proxy port>` on the attach connection, which
refuses a shared master connection. The proxy SHALL accept only requests bearing the session's token, and of those
only `POST`s to the path and query of a hook the session's settings route through it. It SHALL map request bodies
sandbox to local and response bodies local to sandbox through the run's path map, and SHALL forward with Air's current
token in the `Authorization` header.

#### Scenario: Hook reaches Air with local paths
- **WHEN** Claude on the sandbox posts a `PostToolUse` hook naming a sandbox file
- **THEN** Air receives the hook naming the local file, with Air's token

#### Scenario: Wrong token refused
- **WHEN** a request reaches the proxy without the session's token
- **THEN** it is not forwarded to Air

#### Scenario: Only the routed hooks forwarded
- **WHEN** a request bearing the session's token goes to another path, another query or with another method than a routed hook's
- **THEN** it is not forwarded to Air

#### Scenario: No Air hooks, no proxy
- **WHEN** the caller's settings carry no Air hooks
- **THEN** no hook proxy starts and the attach carries no hook forward

### Requirement: A taken hook port costs only the hooks
When the sandbox's hook port cannot be bound by the reverse forward, the attach SHALL run without hooks and log it,
and the next attach SHALL try the same port again.

#### Scenario: Port taken
- **WHEN** another process holds the session's hook port on the sandbox
- **THEN** the session attaches without hooks and the failure is logged

### Requirement: Tokens never print
Hook tokens, Air's and the session's, SHALL be held as an opaque type whose printed form does not reveal them, so no
log line, error or dry-run plan contains one.

#### Scenario: Dry-run plan hides tokens
- **WHEN** a dry run prints the plan for a session with hooks
- **THEN** neither token appears in the output
