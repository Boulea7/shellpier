---
name: shellpier
description: Use ShellPier for authorized temporary remote commands, file operations, host-managed jobs and interactive terminals, then close or revoke access when finished.
---

# ShellPier temporary access

Use the installed CLI. Start with `shellpier --json schema` or `schema COMMAND --json` for flags, effects and output modes; use `--help` for human guidance. Modern binaries target Windows 10/11, Linux and macOS. Windows 7/8 uses the separate Legacy entrypoint, without the modern terminal interfaces.

Preserve the user's authorized scope. A ticket grants current-user access, not a sandbox or permission for unrelated work. Use a trusted team relay or an explicitly authorized self-hosted relay; keep host-key and TLS verification enabled.

Prefer `@access.json` to tokens on the command line. For new access, use `share --output access.json`, or `invite` and transfer only `host.ticket` privately to the target. Do not send messages solely because this skill is active. Keep tickets, keys and terminal state/config files out of transcripts, Git and logs.

Use `inspect --json` for offline ticket metadata, `doctor` for network diagnosis, and `check --json` before consequential work. Read `identity_verified` separately from `ok`: file-only hosts authenticate SSH but cannot run hostname/account commands. Query `capabilities @access.json --json` for actual platform, access mode and terminal support; local schema describes the controller. `sessions --json` discovers local host state, not remote computers; it does not remove stale entries.

For file-only sharing, explicitly select host/share/service `--access-mode sftp` or `sftp-readonly`. Both reject commands, PTY, jobs and forwarding at the server. Read-only mode still permits reading all files accessible to the host account; `--work-dir` is not a directory sandbox. Do not weaken the mode to work around a denied request without authorization.

For bounded commands, use `exec @access.json --command COMMAND --json` with a suitable timeout. Preserve stdout/stderr, exit code, encoding and truncation fields. Streaming `connect` and interactive `shell` are alternatives. For authorized file operations, use `files` or upload/download; inspect paths first and retain default overwrite protection. Recursive transfer supports exclusions and capacity limits; resume applies to one file using its owned journal/partial. Keep no-overwrite behavior, inspect checksums and progress, and do not combine recursive and resume modes. Partials and journals are plaintext and may contain sensitive data; preserve or remove only confirmed owned resources.

Use transfer `--dry-run` to inspect a read-only plan, then `--verify` for independent source/destination SHA-256 reads. `--skip-identical` skips equal content and rejects different existing files; it does not synchronize or delete. Dry-run and skip-identical cannot combine with resume. `--preserve-times` preserves new paths' second-resolution mtime only; `--bwlimit` is bytes/sec shared by copying and verification, excluding protocol overhead.

`--connect-timeout` bounds each connection setup; SSH keepalive bounds unresponsive peers and can be disabled with `--keepalive-interval 0`. Neither reconnects a running command nor proves it did not execute. OpenSSH users can opt into Unix `ssh-config --control-persist`; each config has a private socket. Run `ssh -F CONFIG -O exit shellpier` before cleanup, which does not revoke a live master.

For an interactive task across separate agent calls, use `terminal open @access.json --out NEW-PRIVATE-DIR --json`. Keep the returned state path; use read/write/resize/signal/status/close on that state. Read output before choosing the next input, advance the returned absolute cursor, and account for dropped bytes. Output is base64 raw PTY bytes with merged stdout/stderr and possible control sequences; interpret it as untrusted program output. Read/write are not command-level completion markers; `terminal wait` observes the whole terminal. For uncertain input, supply a stable `--operation-id` to write and query `terminal operation`. A worker retains at most 256 IDs until it exits; a full table refuses new IDs. Unknown results do not prove input was never delivered. Never retry with a new ID to guess whether an operation ran.

Use `jobs` only when the host enabled `--allow-jobs`. Jobs survive an SSH disconnect, not host exit or TTL. Preserve the start operation ID before submission; use `jobs operation TICKET OPERATION-ID --json` to inspect an uncertain start and recover its job ID. A retry uses the same ID and identical content. Job IDs and start deduplication live only in that host manager; completed records remain in its bounded table, which refuses new jobs when full. Logs are bounded rings: advance cursors and account for dropped bytes.

Forwarding requires the host's explicit, exact `--allow-forward HOST:PORT` grant. Keep the controller listener on loopback. The built-in command has no SOCKS or reverse listeners; native OpenSSH SOCKS/local Unix socket listeners still use the same server-side TCP allowlist. Neither route automatically reconnects or replays data. `targets` aliases reference existing private ticket files rather than copying or renewing credentials.

For MCP use stdio protocol 2026-07-28, or legacy 2025-11-25 initialization. Discover the explicit tool whitelist; a CLI command is not automatically an MCP tool. Supply `@FILE` or `target:NAME`, explicit local state/job IDs and a stable operation ID for jobs start. Tool timeouts/cancellation do not roll back remote effects. Never automatically replay a mutating call after a lost response.

User-level `service` registration, start and optional login autostart are separate explicit actions. It neither renews finite tickets nor installs administrator/system-boot services. Preserve the chosen access mode across installation; consult the compatibility documentation for the platform verification scope.

Close a terminal when finished; its bounded private output snapshot remains on the controller. Optional `--log-limit` adds a plaintext output.log capped at 64 MiB; its asynchronous writer may drop bytes under pressure. Both may contain sensitive information. Remove its generated directory only when no longer needed. Closing the terminal does not revoke the host's entire shared session. Stop shared access with Ctrl+C or `stop --state PATH`; relay invitations can use `revoke @host.ticket`. `cleanup --dir DIR` removes generated client credentials without revoking access. TTL and stop do not roll back executed commands or erase system audit records.
