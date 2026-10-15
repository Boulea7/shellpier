# Security policy

[简体中文](SECURITY.md) · English

ShellPier runs as the target's current user. Default full mode permits commands, file operations, bounded jobs and TCP forwarding. An access ticket grants the corresponding account capabilities. Keep tickets, private keys and relay creation keys private.

## Reports and supported versions

Use [GitHub private vulnerability reporting](https://github.com/Boulea7/shellpier/security/advisories/new) with the version, OS, redacted reproduction and impact, without live credentials. Fixes target the current development line and [latest alpha](https://github.com/Boulea7/shellpier/releases). See [compatibility](docs/compatibility.md) for platform scope.

## Boundaries

- SSH encrypts command/file traffic end to end and pins the session host key. TLS protects relay connections. The relay sees connection metadata and can interrupt service.
- Commands inherit the current OS account's permissions; `--work-dir` is not filesystem isolation. Jobs end with the host or TTL; `--allow-jobs=false` disables them. TCP forwarding needs no destination allowlist by default; `--allow-forward` is an optional restriction.
- `--root DIR` automatically selects SFTP directory sharing with a virtual `/`; the server rejects exec, PTY, jobs and forwarding. It needs no administrator rights, but is not an OS shell sandbox and does not isolate mounts or existing hard-link aliases inside the directory. Optional read-only mode is available when writes must be disabled.
- Local forwarding listens on the controller. Reverse forwarding listens on the target's loopback and reaches a controller-side service; standard SSH `-R` also works. Disconnects close forwarding without replaying data. SOCKS5 supports unauthenticated CONNECT only; other local processes may also reach the loopback listener, and names resolve on the target. `proxy --to` uses the same destination access boundary. Forwarded services retain their own authentication boundaries.
- Transfers refuse overwrites by default. `--skip-existing` skips existing regular files without comparing contents. Resume uses owned partials/journals with internal source binding; failures may retain plaintext content and paths. Ordinary transfers make no additional SHA verification guarantee.
- PTY output combines stdout/stderr and can contain control sequences. Terminal snapshots and optional logs live in a private controller directory and may contain sensitive data; closing does not remove them. `terminal wait` observes the whole terminal, not one command.
- Job-start and terminal-write operation IDs deduplicate only within their manager/worker lifetime, with bounded records. An unknown outcome does not mean nothing ran. Inspect state and effects before retrying instead of guessing with a new ID.
- MCP exposes an explicit tool set over stdio, with no Web frontend. Timeouts/cancellation do not roll back remote effects. Targets reference private tickets without copying or renewing them. User-level service registration and autostart are explicit actions; they do not elevate privileges or extend tickets.
- OpenSSH multiplexing is off by default. Close an enabled master before removing its configuration. `cleanup` removes generated files without revoking existing connections; end sharing with Ctrl+C, stop or revoke. TTL does not undo commands or erase system/network audit records.
- Crashes, SIGKILL or power loss can leave state and credentials. Unix children can escape managed process groups. Legacy expiration depends on Task Scheduler, power and clocks.

For usage issues, see [SUPPORT](SUPPORT.md).
