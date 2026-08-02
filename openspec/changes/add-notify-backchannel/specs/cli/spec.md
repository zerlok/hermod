## ADDED Requirements

### Requirement: Notify opt-out flag
The CLI SHALL open the notification back-channel by default and SHALL accept a `-N`/`--no-notify`
flag that suppresses it. The flag SHALL compose with dry-run, which opens no channel of its own
accord.

#### Scenario: Notify on by default
- **WHEN** the user runs `hermod prod-box`
- **THEN** the resolved intent opens the notification back-channel

#### Scenario: Opted out
- **WHEN** the user runs `hermod prod-box --no-notify`
- **THEN** the resolved intent opens no notification back-channel

### Requirement: Notify sender subcommand
The CLI SHALL provide a `hermod notify [--title <t>] [--urgency low|normal|critical] <body>...`
subcommand that sends one notification over the back-channel of the enclosing session. It SHALL join
the positional arguments into the message body and read the channel address from the environment.
This subcommand is independent of the root run command and its flags.

#### Scenario: Body from positional arguments
- **WHEN** the user runs `hermod notify build finished`
- **THEN** the sent message body is `build finished`

#### Scenario: Title and urgency options
- **WHEN** the user runs `hermod notify --title CI --urgency critical failed`
- **THEN** the sent message carries title `CI`, urgency `critical`, and body `failed`
