---
name: shellpier
description: Establish and verify authorized temporary SSH access to another computer with ShellPier, then revoke the session when work is complete.
---

# ShellPier temporary access

Use the installed `shellpier` CLI and its `--help` as the flag contract. Read the project's command and troubleshooting documentation when diagnosing errors. Modern Go binaries target Windows 10/11, Linux and macOS; Windows 7/8 requires the independent legacy entrypoint and its own requirements.

Run `doctor` first, with the intended relay URL when applicable. A healthy HTTP check is not proof of SSH access. Confirm the target machine/account and intended authorized actions from the user context before executing work. Preserve any existing authorization; a connection ticket does not broaden it.

Use a team-provided relay URL/key or an explicitly authorized self-hosted relay. Do not deploy on an unrelated host, choose an arbitrary public relay, bypass an authentication portal/VPN policy, or disable host-key checking.

For existing access, prefer `@access.json` to putting tokens on the command line. For new access use `share`, or `invite` and send only the target's host ticket through an authorized private channel. Do not send messages solely because this skill is active. Keep controller private keys, access files and tickets out of transcripts, Git and logs.

Generate an isolated directory with `ssh-config`; retain strict host-key pinning and the generated identity/known-hosts paths. Verify `hostname` and `whoami` with the built-in client or generated OpenSSH configuration before consequential commands. If identity differs from expectation, stop. Execute only the user's authorized work; the endpoint runs as the current OS user and is not a sandbox.

A lost connection may follow a successfully executed command. Inspect effects before retrying, and never automatically replay old commands. Keep the foreground host running only for the agreed TTL; `--keep-awake` is opt-in.

At completion, stop this tool's session with Ctrl+C or `stop --state PATH`, inspect `status` before stopping (runtime state is removed after shutdown), and attempt a fresh connection to prove it is refused. TTL acceptance likewise requires a post-expiry connection attempt. For relay invitations, `revoke @host.ticket` is the remote revocation path. After proving refusal, use `cleanup --dir DIR` for generated credentials; cleanup alone does not revoke and preserves unrelated files. Remove only this session's other generated resources when authorized; do not clear system audit records. Report what ran, identity verified, revocation evidence, and platform checks still untested.
