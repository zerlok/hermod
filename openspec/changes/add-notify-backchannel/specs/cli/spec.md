## ADDED Requirements

### Requirement: Notify flag
The CLI SHALL accept a `-N`/`--notify` flag (default off). When set, the resolved intent SHALL be
marked to open the notification back-channel for the run. The flag SHALL compose with dry-run: under
`-n -N` the intent is both dry-run and notify-enabled, so the channel plan is printed rather than
executed.

#### Scenario: Notify requested
- **WHEN** the user runs `hermod prod-box -N`
- **THEN** the resolved intent is marked to open the notification back-channel

#### Scenario: Notify off by default
- **WHEN** the user runs `hermod prod-box` without `-N`
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
