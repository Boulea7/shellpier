# Security policy

[简体中文](SECURITY.md) · English

ShellPier provides temporary command execution with the target computer's current user permissions. Access files, tickets and private keys are credentials. Protect the relay creation key separately.

## Report vulnerabilities privately

Use [GitHub private vulnerability reporting](https://github.com/Boulea7/shellpier/security/advisories/new) for suspected authentication bypass, credential leaks, host-key verification failures, unauthorized access or failed revocation. Keep exploit details out of public issues and pull requests.

Include the affected version or commit, OS and architecture, a redacted reproduction, expected and actual behavior, and impact. Do not attach valid tickets, private keys, creation keys or identifying information about third parties. Use a dedicated local test environment when example credentials are needed.

## Supported versions

Security fixes target the current development branch and [latest alpha release](https://github.com/Boulea7/shellpier/releases). See [compatibility](docs/compatibility.md) for supported platforms.

## Security boundaries

- SSH encrypts command and file traffic end to end and pins the session host key. TLS protects connections to the relay. The relay can still observe connection metadata and interrupt service.
- Access is not sandboxed. Commands inherit the current user's account and filesystem permissions; PAM is not used to switch users. The working directory does not restrict file access.
- PTY output combines stdout and stderr and may contain terminal control sequences. Persistent `terminal` sessions keep state, configuration and a snapshot of up to 1 MiB of raw output in a private directory on the controller. Optional `--log-limit` adds a plaintext log capped at 64 MiB; a congested asynchronous writer may drop bytes. Snapshots and logs may contain sensitive information; remove owned files when no longer needed. `terminal wait` observes the whole terminal, not one command.
- Host-managed jobs require explicit enablement. They survive SSH disconnects but end with the host process or TTL. Logs are bounded rings. Job IDs and start-operation deduplication live only in that host manager; completed records occupy its bounded table, which refuses new jobs when full. Terminal write IDs are retained for one worker lifetime, up to 256 IDs, then new IDs are refused. Unknown outcomes do not mean an operation never ran.
- Recursive transfer retains exclusions, limits and no-overwrite protection. Resume uses owned partials/journals; checksums and progress do not provide rollback. Partials and journals are plaintext and may contain sensitive contents or paths.
- Forwarding requires an exact host-side destination allowlist and a controller loopback listener. It provides no SOCKS, public listener or reverse forwarding. SSH loss closes forwarding without replaying bytes.
- MCP exposes an explicit tool whitelist using ticket references and explicit state/job IDs; jobs start requires a stable operation ID. Stdio supports 2026-07-28 and legacy 2025-11-25 initialization, with no Web frontend. Unknown outcomes, timeouts and cancellation do not prove that remote work never ran; mutations are not automatically replayed.
- Target aliases reference existing private ticket files without copying or renewing them. User-level service registration, start and optional login autostart are explicit actions. They do not elevate privileges, install system-boot services or renew finite tickets.
- Creation keys, host tokens and client tokens serve different purposes. Do not interchange or publish them.
- TTL and stop revoke this tool's resources. They do not undo executed commands or erase system or network audit records. A lost connection does not prove that a command was not executed; check its effects before retrying.
- Crashes, SIGKILL or power loss may leave state and credentials behind. Unix child processes can escape the managed process group. Legacy expiration depends on Task Scheduler, power and the system clock. See [compatibility](docs/compatibility.md).

For usage issues, see [SUPPORT](SUPPORT.md).
