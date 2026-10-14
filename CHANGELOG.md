# Changelog

## v0.1.0-alpha.3 — 2026-10-14

- 新增 `inspect`、`check`、`upload`、`download`、`keygen` 和 Shell 补全，支持 `version --json` 与 `revoke --timeout`。
- 默认会话时长改为 3 小时；建连和主机重连支持有限重试、指数退避与抖动，不重放远程操作。
- 简化下载、启动和凭据准备流程，修复 Windows 私有文件权限与 Fish 补全检查。
- 精简中英文 README，提供双语安全说明、模块指引和扩展开发指南。
- 增加最低 Go 版本、实际程序退出码、重试恢复与永久错误测试。

## v0.1.0-alpha.1 — 2026-10-14

- 首个预览版：当前用户权限下的临时 SSH 命令执行与 SFTP，支持直连和 WSS 中继。
- 提供邀请、独立 OpenSSH 配置、诊断、撤销、防睡眠和会话清理。
- 提供 Windows 7/8 独立 Legacy 入口、Agent skill 和七种系统/架构的离线包。

系统要求与测试范围见[兼容性说明](docs/compatibility.md)。
