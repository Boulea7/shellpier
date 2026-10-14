# Changelog

## v0.1.0-alpha.2 — 2026-10-14

- 新增 `inspect`、`check`、`upload`、`download`、`keygen` 和 Shell `completion` 命令。
- 支持 `version --json` 与 `revoke --timeout`，统一命令帮助和参数说明。
- 按功能拆分 CLI 实现，便于维护命令及其测试。
- 默认中文 README，保留英文版，补充项目横幅、贡献指南和文档导航。
- 增加文档链接校验和 Fish completion CI 检查。

本版本仍为预览版。平台验证以对应提交的 GitHub Actions 结果为准；Windows 7/8 的新 Legacy 流程仍需旧系统实机验收。

## v0.1.0-alpha.1 — 2026-10-14

- Temporary current-user SSH command execution and SFTP, with direct or WebSocket relay transport.
- Controller-created invitations, pinned SSH configuration, diagnostics and session revocation.
- Separate legacy Windows entrypoint, Agent skill and offline release packaging.
- TLS and HTTP CONNECT proxy diagnostics, bounded relay queues, no credential-bearing redirects, and no automatic command replay.
- Windows Job Object cleanup, SFTP path handling, private file permissions and manifest-based credential cleanup.
- Bilingual README, compatibility and alternative-tool research, contribution guidelines and private vulnerability reporting.

This is the first preview release. Modern binaries are built with Go 1.27.2 for seven OS/CPU targets; macOS binaries require version 13 or newer. CI exercises current Linux, macOS and Windows runners. Each run is linked to its commit in GitHub Actions; it does not validate every older OS or CPU.

Windows 7/8 uses the separate Legacy PowerShell/Bitvise path. Parser and mock contracts do not replace testing a new Bitvise instance, scheduled expiry and cleanup on those systems. XP/Vista is not a supported automatic path. There is no bundled public relay, no PTY or desktop control, and cleanup does not erase audit logs or reverse executed commands.
