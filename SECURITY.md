# 安全策略

简体中文 · [English](SECURITY.en.md)

ShellPier 提供目标机当前用户权限下的临时命令执行。访问文件、票据和私钥是凭据，中继创建密钥须单独保护。

## 私密报告漏洞

仓库已启用 [GitHub 私密漏洞报告](https://github.com/Boulea7/shellpier/security/advisories/new)。疑似认证绕过、凭据泄漏、主机公钥验证失败、越权访问或撤销失效，请通过该入口报告，避免在公开 issue/PR 中披露利用细节。

提供受影响版本或 commit、OS/架构、脱敏复现、预期/实际行为及影响范围。不要附上有效票据、私钥、创建密钥或第三方真实标识；需要示例凭据时使用专门的本地测试环境。

## 支持的版本

安全修复面向当前开发线与[最新 alpha 版本](https://github.com/Boulea7/shellpier/releases)。支持平台见[兼容性说明](docs/compatibility.md)。

## 安全边界

- SSH 对命令/文件流量端到端加密并固定会话主机公钥；TLS 保护端点到中继的连接。中继仍可观察连接元数据并中断服务。
- 访问权限不是沙箱：命令继承当前用户的账户与文件系统权限，不通过 PAM 切换系统用户。工作目录不限制文件访问范围。
- PTY 终端输出合并 stdout/stderr，并可能包含控制序列。持久 `terminal` 会话在控制电脑的私有目录保存状态、配置和最多 1 MiB 原始输出快照；可选 `--log-limit` 日志最高 64 MiB，写入拥塞时可能丢字节。两者均是可能含敏感信息的明文，关闭后由使用者清理。`terminal wait` 等待整个终端结束，不表示某条命令完成。
- `jobs` 需 host 显式启用；任务跨 SSH 断线继续，但随 host 退出或 TTL 结束。日志是有界 ring。任务与 start 操作 ID 仅在同一 host manager 生命周期有效，完成记录占用有界表，满时拒绝新任务。终端 write 操作 ID 仅在同一 worker 生命周期保留最多 256 个，满时拒绝新增；未知结果不能推断为未执行。
- 递归传输保留排除、容量和不覆盖保护；续传依赖本次拥有的 partial/journal，校验与进度不代表远端动作可回滚。partial 与 journal 是明文，可能含敏感文件内容或路径。
- 转发须 host 精确目标 allowlist；控制端只监听 loopback，不提供 SOCKS、公开监听或反向转发。SSH 断线关闭转发，不自动重放数据。
- MCP 仅提供显式工具白名单，使用明确票据引用、state/job ID；jobs start 要求稳定操作 ID。stdio 支持 2026-07-28 与旧版 2025-11-25 初始化，不提供 Web 前端。未知结果、超时或取消均不保证远端未执行，不自动重放变更。
- `targets` 只引用现有私有票据，不复制或续期。user-level `service` 注册、启动与可选登录自启均须显式选择，不提升权限、不注册系统启动服务，也不续期有限票据。
- 创建密钥、host token 和 client token 用途不同，不应互换或公开。
- TTL/stop 撤销本工具资源，不回滚已执行命令，不删除系统/网络审计。连接丢失不能证明命令未执行，重试前检查副作用。
- 崩溃、SIGKILL 或断电可能留下状态和凭据；Unix 子进程可逃离受管理进程组。Legacy 到期受 Task Scheduler、供电与时钟影响。详见[兼容性边界](docs/compatibility.md)。

普通使用故障见[获取帮助](SUPPORT.md)。
