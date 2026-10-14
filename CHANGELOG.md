# Changelog

## v0.1.0-alpha.1 — 2026-10-14

- Temporary current-user SSH command execution and SFTP, with direct or WebSocket relay transport.
- Controller-created invitations, pinned SSH configuration, diagnostics and session revocation.
- Separate legacy Windows entrypoint, Agent skill and offline release packaging.
- TLS and HTTP CONNECT proxy diagnostics, bounded relay queues, no credential-bearing redirects, and no automatic command replay.
- Windows Job Object cleanup, SFTP path handling, private file permissions and manifest-based credential cleanup.
- Bilingual README, compatibility and alternative-tool research, contribution guidelines and private vulnerability reporting.

This is the first preview release. Modern binaries are built with Go 1.27.2 for seven OS/CPU targets; macOS binaries require version 13 or newer. CI exercises current Linux, macOS and Windows runners. Each run is linked to its commit in GitHub Actions; it does not validate every older OS or CPU.

Windows 7/8 uses the separate Legacy PowerShell/Bitvise path. Parser and mock contracts do not replace testing a new Bitvise instance, scheduled expiry and cleanup on those systems. XP/Vista is not a supported automatic path. There is no bundled public relay, no PTY or desktop control, and cleanup does not erase audit logs or reverse executed commands.
