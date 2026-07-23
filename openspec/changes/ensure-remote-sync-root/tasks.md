## 1. Provision the remote sync root

- [x] 1.1 In `internal/mirror/mirror.go`, in the `NewMutagenSession` create branch (`st == Absent`), run `ssh <host> mkdir -p <remotePath>` through the dry-run-aware `exec` shell **before** `mutagen sync create`. Build the argv directly (`["ssh", cfg.Host, "mkdir", "-p", cfg.RemotePath]`); return any error.
- [x] 1.2 Ensure the resume branch does not provision.

## 2. Tests

- [x] 2.1 In `internal/mirror/mirror_test.go`, extend the create case so the recorded exec argv show the remote `mkdir -p` issued before `mutagen sync create` (table-driven, ordered-argv assertion, no literal expected values inline beyond the argv table).
- [x] 2.2 Assert the resume path records no `mkdir` command.
- [x] 2.3 Assert dry-run prints the `mkdir` line and does not execute it (leaf-swap behavior already covered by the shell layer; verify the command flows through `exec`, not `probe`).

## 3. Validate

- [x] 3.1 Run `make test` and `make lint`.
- [ ] 3.2 On the sandbox host, manually verify: from a local dir whose remote parent does not exist, `hermod <host> -- pwd` now syncs the tree and lands in the mirrored remote directory (not `$HOME`).
- [x] 3.3 Run `openspec validate ensure-remote-sync-root`.
