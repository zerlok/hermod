## ADDED Requirements

### Requirement: Opt-in reverse notification channel
Hermod SHALL provide an opt-in back-channel, enabled by a `-N`/`--notify` flag (default off), that
lets a process on the sandbox raise a desktop notification on the local machine. When enabled,
Hermod SHALL open a reverse forward on the interactive attach connection and carry the channel's
address into the session environment; when not enabled, Hermod SHALL open no channel and add nothing
to the attach or the environment.

#### Scenario: Enabled adds the reverse forward and carried address
- **WHEN** the user runs `hermod prod-box -N`
- **THEN** the interactive attach connection carries a reverse forward for the channel
- **AND** the session environment carries the channel's socket address (`HERMOD_NOTIFY_SOCK`) alongside the git identity
- **AND** a message sent to that address from the sandbox raises a desktop notification on the local machine

#### Scenario: Disabled by default
- **WHEN** the user runs `hermod prod-box` without `-N`
- **THEN** no reverse forward is added to the attach
- **AND** the session environment carries no channel address
- **AND** no local listener is started

### Requirement: The channel is best-effort and never fatal
The notification channel SHALL never affect the session or the pause-vs-teardown decision. Any
failure to resolve the address, provision the endpoint, bind the listener, establish the forward,
or raise a notification SHALL be logged and the session SHALL proceed normally without
notifications. The pause-vs-teardown decision SHALL remain keyed solely on remote-session liveness.

#### Scenario: Setup failure degrades to a plain session
- **WHEN** `-N` is set but the channel cannot be provisioned or bound
- **THEN** Hermod logs that notifications are disabled
- **AND** the sync-and-attach flow runs normally without a channel
- **AND** the mirror is still settled by remote-session liveness

#### Scenario: A malformed or oversized message is dropped
- **WHEN** a message that is empty, malformed, or exceeds the size cap arrives on the channel
- **THEN** it is dropped without raising a notification
- **AND** no error escapes to affect the session

### Requirement: Channel endpoint is reachable only by the session user
The channel SHALL use a unix-domain socket inside a `0700` directory on both the local and the
sandbox end, addressed by a per-session unique name, so that only the session user can reach it and
concurrent sessions to the same host do not collide. Hermod SHALL NOT expose the channel on a
loopback TCP port.

#### Scenario: Per-session isolation
- **WHEN** two `-N` sessions run against the same sandbox host
- **THEN** each uses a distinct socket name on each end
- **AND** neither can reach the other's channel

### Requirement: Sandbox-side sender
Hermod SHALL provide a `hermod notify [--title <t>] [--urgency low|normal|critical] <body>`
subcommand that sends one message over the channel using the carried address. When the carried
address is absent from the environment, the subcommand SHALL fail with a clear error and SHALL NOT
affect any other process.

#### Scenario: Send from inside a notify-enabled session
- **WHEN** `hermod notify --urgency critical "agent blocked"` runs in a session started with `-N`
- **THEN** the message is delivered over the channel
- **AND** the local machine raises a notification with that body and urgency

#### Scenario: Send with no channel present
- **WHEN** `hermod notify done` runs where `HERMOD_NOTIFY_SOCK` is unset
- **THEN** the subcommand exits with a clear error
- **AND** nothing else is affected

### Requirement: Dry-run prints the channel plan without binding
Under `--dry-run`, the channel's side effects SHALL be printed as copy-pasteable shell lines and
nothing SHALL be bound or served. The address-resolving probe (a read-only probe) SHALL run for
real so the printed plan carries the true address.

#### Scenario: Dry-run with notify enabled
- **WHEN** the user runs `hermod prod-box -N -n`
- **THEN** the remote directory-creation command and the attach's reverse forward (carrying the real socket address) are printed
- **AND** the session environment line carries the real `HERMOD_NOTIFY_SOCK`
- **AND** no local listener is bound and no notification is raised
