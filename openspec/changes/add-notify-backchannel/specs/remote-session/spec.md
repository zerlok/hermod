## ADDED Requirements

### Requirement: Provide a channel between local and sandbox
The remote session SHALL provide the means to establish a named communication channel between the
local machine and the sandbox, so callers can exchange messages with a sandbox process without
constructing transports themselves. Opening a channel SHALL yield an address for each end; carrying
a channel SHALL forward the sandbox end to the local end for exactly the lifetime of the attach
connection. A session SHALL accept whatever is serving a channel as the thing that has one, rather
than requiring the caller to take it apart. What travels over a channel is the caller's concern, not
the session's.

#### Scenario: A carried channel is forwarded by the attach
- **WHEN** a session is prepared with a channel
- **THEN** the interactive attach connection forwards the sandbox end of that channel to the local end
- **AND** the forward is gone once the attach ends

#### Scenario: The liveness probe never carries a channel
- **WHEN** a session prepared with a channel probes its liveness
- **THEN** the probe connection carries no forward

#### Scenario: No channel is the prior behaviour
- **WHEN** a session is prepared without a channel
- **THEN** the attach connection is exactly what it would be for a session that had no channel concept

### Requirement: A channel endpoint is per sandbox user
A channel's sandbox endpoint SHALL be a unix-domain socket named for the channel inside a `0700`
directory under the sandbox user's runtime directory — one endpoint per sandbox user, shared by
every project on that host, so its address is stable and a sender needs to know only its own user's
socket. The local endpoint SHALL be per sandbox host, so sessions to different hosts do not collide.
Hermod SHALL NOT expose a channel on a loopback TCP port, which every user on a shared host could
reach.

#### Scenario: The same host yields the same endpoint
- **WHEN** channels of the same name are opened for the same sandbox host from two different projects
- **THEN** both name the same sandbox endpoint

#### Scenario: Different hosts stay apart locally
- **WHEN** channels of the same name are opened for two different sandbox hosts
- **THEN** each has its own local endpoint

#### Scenario: An unusable runtime directory refuses the channel
- **WHEN** the sandbox's runtime directory cannot be read, or is not a plain absolute path
- **THEN** no channel is opened and no directory is provisioned
- **AND** the failure is reported to the caller
