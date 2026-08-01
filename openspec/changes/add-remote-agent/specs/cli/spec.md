## ADDED Requirements

### Requirement: Version reporting
The CLI SHALL report the binary's version through a `hermod version` command and a `--version` flag,
and SHALL do so without requiring a sandbox host argument.

#### Scenario: Version command
- **WHEN** the user runs `hermod version`
- **THEN** the binary's version is printed and the command exits successfully

#### Scenario: Version flag with no host
- **WHEN** the user runs `hermod --version`
- **THEN** the binary's version is printed
- **AND** the missing sandbox host is not treated as an error

### Requirement: Agent bootstrap opt-out flag
The CLI SHALL bootstrap the sandbox agent by default and SHALL accept a flag that skips it, for a
sandbox that must not be written to.

#### Scenario: Bootstrap on by default
- **WHEN** the user runs `hermod prod-box`
- **THEN** the resolved intent bootstraps the sandbox agent

#### Scenario: Opted out
- **WHEN** the user runs `hermod prod-box` with the skip flag
- **THEN** the resolved intent does not bootstrap the sandbox agent
