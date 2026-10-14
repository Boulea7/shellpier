# Changelog

## v0.2.0-alpha.3 — 2026-10-15

- 新增 `shell` 交互终端：Unix PTY、Windows ConPTY、尺寸同步、信号与退出清理。
- 新增 `terminal` 持久终端控制，支持分次读取、输入、调整尺寸、发信号和关闭；默认最长 3 小时，有界输出与私有状态，退出后可读取最终快照。
- 新增 `exec` 精确命令、脚本、环境变量、工作目录、超时和有界 JSON 结果；保留远端退出码。
- 新增 SFTP `files` 操作、`sessions` 会话发现和 `capabilities` 目标能力查询。
- 新增离线 `schema`、全局 JSON 错误、分层命令补全，统一参数解析与管理命令输出。
- 补充原生终端、进程回收、真实二进制和 Agent 操作测试，更新中文手册与 Agent skill。

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
