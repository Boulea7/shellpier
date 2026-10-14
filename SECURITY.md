# Security policy

ShellPier provides temporary command execution as the target's current OS user. Treat access files and tickets as credentials. Protect the relay creation key separately. Do not expose a session to people who should not have that account's privileges.

## Report a vulnerability

Use this repository's **Security → Report a vulnerability** private reporting feature if enabled. If unavailable, contact [Boulea7](https://github.com/Boulea7) through an available private GitHub contact channel, or open a minimal public issue requesting one without exploit details or secrets. This project does not claim a dedicated security mailbox or guaranteed response time.

Include affected version/commit, OS, a redacted reproduction and impact. Do not send working access credentials. There is no formal LTS policy; fixes target the current development/release line.

## Security boundaries

SSH encrypts command/file traffic end to end and verifies the session host key. TLS protects endpoint-to-relay connections. The relay can see connection metadata and can disrupt availability. Access grants are not a sandbox: commands run with the current user's filesystem and account permissions.

Creation keys permit new relay sessions; target/client role tokens have different purposes. TTL and stop revoke tool resources; they do not undo commands already executed or erase OS/network audit logs. Disconnects do not establish whether a command executed, so check effects before retrying.

Use verified binaries, private credential directories, short TTLs and approved networks. Verify `hostname`/`whoami` before meaningful work. Never turn off host-key verification to make a connection work. Retest a fresh connection after stopping or expiry.
