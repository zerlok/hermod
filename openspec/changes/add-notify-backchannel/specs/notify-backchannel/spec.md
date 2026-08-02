## ADDED Requirements

### Requirement: Notification channel on by default
Hermod SHALL open, by default, a back-channel that lets a process on the sandbox raise a desktop
notification on the local machine, and SHALL provide a flag to opt out. When enabled, Hermod SHALL
ask the sandbox session for a channel and carry that channel's address into the session environment;
when opted out, Hermod SHALL open no channel and add nothing to the attach or the environment.

#### Scenario: Enabled by default
- **WHEN** the user runs `hermod prod-box`
- **THEN** the interactive attach connection carries a forward for the notification channel
- **AND** the session environment carries the channel's socket address (`HERMOD_NOTIFY_SOCK`) alongside the git identity
- **AND** a message sent to that address from the sandbox raises a desktop notification on the local machine

#### Scenario: Opted out
- **WHEN** the user runs `hermod prod-box` with the opt-out flag
- **THEN** no channel is opened and no forward is added to the attach
- **AND** the session environment carries no channel address
- **AND** no local listener is started

### Requirement: The channel is best-effort and never fatal
The notification channel SHALL never affect the session or the pause-vs-teardown decision. Any
failure to resolve the address, provision the endpoint, bind the listener, establish the forward,
or raise a notification SHALL be logged and the session SHALL proceed normally without
notifications. The pause-vs-teardown decision SHALL remain keyed solely on remote-session liveness.
Because it is best-effort, being on by default costs a session that cannot use it one log line.

#### Scenario: Setup failure degrades to a plain session
- **WHEN** the channel cannot be provisioned or bound
- **THEN** Hermod logs that notifications are disabled
- **AND** the sync-and-attach flow runs normally without a channel
- **AND** the mirror is still settled by remote-session liveness

#### Scenario: A malformed or oversized message is dropped
- **WHEN** a message that is empty, malformed, or exceeds the size cap arrives on the channel
- **THEN** it is dropped without raising a notification
- **AND** the sender is told it was refused
- **AND** no error escapes to affect the session

#### Scenario: A wedged desktop tool does not wedge the channel
- **WHEN** raising a notification does not complete within its time limit
- **THEN** the attempt is abandoned and the channel goes on serving later messages

### Requirement: Sandbox-side sender
Hermod SHALL provide a `hermod notify [--title <t>] [--urgency low|normal|critical] <body>`
subcommand that sends one message over the channel using the carried address, and SHALL report to
the sender whether the message was accepted. When the carried address is absent from the
environment, the subcommand SHALL fail with a clear error and SHALL NOT affect any other process.

#### Scenario: Send from inside a session
- **WHEN** `hermod notify --urgency critical "agent blocked"` runs in a session with the channel open
- **THEN** the message is delivered over the channel
- **AND** the local machine raises a notification with that body and urgency

#### Scenario: Send with no channel present
- **WHEN** `hermod notify done` runs where `HERMOD_NOTIFY_SOCK` is unset
- **THEN** the subcommand exits with a clear error
- **AND** nothing else is affected

### Requirement: The channel is reachable without the Hermod binary
The channel SHALL speak a protocol that commonly available tools can already produce, so a sandbox
with no Hermod binary can still raise a notification. The address SHALL be all a sender needs.

#### Scenario: Sending from a box with no Hermod installed
- **WHEN** a generally available tool sends a well-formed message to the carried address
- **THEN** the local machine raises the notification, exactly as it would for `hermod notify`

### Requirement: Concurrent senders are bounded but not lost
The channel SHALL bound the work it does at once, so a burst of senders cannot grow the local
process without limit. Senders beyond that bound SHALL wait rather than lose their messages.

#### Scenario: More senders than the channel serves at once
- **WHEN** more senders than the concurrency bound send at the same time
- **THEN** every message is delivered
- **AND** no sender reports a failure

### Requirement: Dry-run opens no channel
Under `--dry-run` Hermod SHALL NOT open the notification channel: it SHALL neither probe nor
provision the sandbox endpoint, nor bind a local listener, and the planned session SHALL carry no
channel. Hermod SHALL note the omission so the printed plan is not read as complete.

#### Scenario: Dry run
- **WHEN** the user runs `hermod prod-box -n`
- **THEN** no endpoint is provisioned on the sandbox and no local listener is bound
- **AND** the printed attach carries no forward and no channel address
- **AND** Hermod notes that the back-channel was not opened under dry-run
