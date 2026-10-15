# Changelog

## v0.5.0-alpha.1 — 2026-10-15

- 简化普通传输：删除 `--verify`、`files checksum` 及公开摘要验证结果，不再默认重复读取整个文件计算 hash。续传内部仍自动绑定来源和已有部分内容，无需手动校验。
- 将 `--skip-identical` 替换为 `--skip-existing`：已有普通文件直接跳过，不比较内容；已有目录可继续递归，类型冲突仍报错，默认仍不覆盖。上述预览版 CLI 删除是有意简化，旧脚本需调整参数。
- 新增 `--root DIR` 目录共享：自动启用虚拟 `/` 下的 SFTP，无需管理员；该模式不提供命令、终端、jobs 或转发。
- 普通 full 模式默认支持有界后台任务与 TCP 转发，无需额外许可参数；`--allow-jobs=false` 可关闭任务，`--allow-forward` 保留为可选目标限制。
- 新增内置反向转发与标准 SSH `-R`，远端 loopback 监听可连接控制端服务；目录共享仍禁用所有转发。
- `files read` 支持读取普通文件的符号链接，目录共享仍在服务端约束实际访问范围。
- 精简首页、安装步骤和 Agent skill；诊断与高级限制按需使用，保留凭据保护和未知结果不重放的边界。

## v0.4.0-alpha.1 — 2026-10-15

- 新增连接建立超时与 SSH 应用层保活，可发现直连和中继内的无响应连接；配置可随持久 terminal 保存，断线不自动重放远端操作。
- 新增服务端 `full`、`sftp`、`sftp-readonly` 访问模式，覆盖 share、host 和用户级服务；受限模式拒绝命令、PTY、jobs 与转发，只读模式限制 SFTP 写入路径。
- 新增传输 `--dry-run`、`--verify`、`--preserve-times`、`--skip-identical` 和 `--bwlimit`，提供只读计划、独立内容校验、秒级 mtime 保留、相同文件跳过与共享限速，继续拒绝覆盖不同内容。
- OpenSSH 配置支持可调保活与可选 Unix ControlPersist，每张票据使用私有连接复用 socket；保持主机公钥固定及票据到期约束。
- check、doctor 与 capabilities 识别受限访问模式，区分 SSH 认证成功与远端账户身份检查；同步 schema、补全、MCP 参数和 Agent skill。
- 扩充 OpenSSH、rsync、rclone、croc、Mosh、autossh、tmate、Tailscale SSH 与 Teleport 的官方资料对比，记录已吸收能力和实现边界。

## v0.3.0-alpha.1 — 2026-10-15

- 新增 host 托管的 `jobs`：SSH 断线后继续、有界任务与日志、operation ID 去重、查询、等待和取消。
- 新增目录上传下载、排除规则、SHA-256、JSONL 进度，以及基于私有 journal 的单文件续传；继续拒绝覆盖已有目标。
- 新增精确目标允许列表的 loopback `forward`，并由目标端显式启用。
- 新增 `mcp serve`，通过 stdio 暴露有界命令工具，支持当前和兼容版协议、取消、输出限制与明确的未知提交状态。
- 新增 `targets` 私有票据引用、`doctor --target` 分阶段诊断和用户级 `service` 管理；自启需显式启用，服务不延长票据期限。
- 完善 terminal 结束原因、等待、输入去重/查询与可选私有日志；更新命令 schema、补全、Agent skill 和功能边界文档。

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
