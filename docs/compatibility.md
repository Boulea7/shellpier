# 兼容性

[返回文档目录](README.md) · [Legacy 指南](legacy-windows.md)

发行包面向现代 Windows、Linux 和 macOS。旧 Windows 使用独立 Legacy 路线，尚未完成 Win7/8 实机测试。

## 平台选择

| 环境 | ShellPier 入口 | 条件与边界 |
|---|---|---|
| Windows 10/11 | 原生 `shellpier.exe`，amd64/386/arm64 包 | 当前用户执行，无需系统 sshd；PTY 需要 Windows 10 1809+ 的 ConPTY，较早 Windows 10 使用 exec/SFTP |
| Linux | 原生程序，amd64/arm64 包 | Go 1.26 的内核最低要求为 3.2；裁剪系统还需满足运行时要求；WSL1 不在 Go 支持范围内 |
| macOS | `darwin` 包，amd64/arm64 | 使用 Go 1.27.2 构建的发行包最低 macOS 13；Go 1.26 源码构建的工具链下限为 macOS 12，需另行测试 |
| Windows 7 SP1/8 | `legacy/shellpier.cmd` | 独立 Windows PowerShell 2.0/.NET 3.5 路线；Bitvise 9.66；管理员、适用许可和可信签名；尚未完成 Win7/8 实机测试 |
| XP/Vista | 没有受支持的自动路线 | ShellPier 未提供这些系统的自动配置入口 |

当前源码的构建契约是 [go.mod](../go.mod) 的 Go 1.26+。上述运行时下限来自 [Go 官方最低要求](https://go.dev/wiki/MinimumRequirements)；升级工具链时须重新检查。Windows Server 各版本尚未逐一测试。

`scripts/bootstrap.ps1` 是现代 Windows 的下载辅助脚本，要求 PowerShell 5.1+；它不是 Legacy 入口。Legacy 文件可离线复制；Bitvise 服务端需要安装。首次安装可能需要重启才能完成无密码 Windows 登录，脚本不自动重启。详见 [Legacy 指南](legacy-windows.md)、[Bitvise 官方下载](https://bitvise.com/ssh-server-download)与[许可](https://bitvise.com/ssh-server-license)。

## 已实现路线

| 路线 | 实现状态 | 用户仍须提供 |
|---|---|---|
| Modern direct | 内置 SSH exec/SFTP、PTY、临时公钥、固定主机公钥、TTL、stop | 允许入站的实际网络路径；无需手输目标 OS 用户名 |
| Modern WSS relay | 双方主动出站、host/client 角色分离、连接配对、撤销 | 可信 HTTPS 中继 URL 与创建密钥；无预置免费公共中继 |
| Modern invite | 控制端保留私钥，只发送 host ticket | 授权的私下传递渠道；目标机仍须运行 host |
| OpenSSH-compatible Agent | 生成独立 SSH config/identity/known_hosts，ProxyCommand 使用 ShellPier | Agent 支持 OpenSSH 配置并能找到本地可执行文件 |
| Legacy direct | 独立 Bitvise 实例、精确 IPv4 来源规则、SYSTEM 到期任务 | 管理员权限与显式安装授权、官方固定版本安装包、有效许可、控制端公钥与实际客户端 IP |
| Legacy Plink reverse | 校验本地二进制与发布者，打印固定 relay host key 的命令 | 自有 SSH relay、relay 用户/.ppk、公钥指纹、本地 SSH server；执行与 Plink 生命周期由用户监督 |
| 其他替代工具 | 调研/操作指南，尚未集成自动适配 | 各工具自己的安装、账户、许可、服务和安全配置 |

现代 WSS 路线无需填写目标用户名/IP；Legacy 需要额外安装条件。反向 Plink 仅生成指南，仍需建立隧道并完成 SSH 认证。

## 执行与撤销

会话默认 TTL 为 3 小时。当前版本提供当前用户的命令执行、SFTP 与 PTY；不提供桌面控制、系统用户切换或 PAM 登录。Unix 使用系统 PTY，Windows 使用 ConPTY；目标是否可用以 `capabilities` 返回为准，Legacy 不提供本项目的 PTY 入口。访问密钥授予该进程所属账户的执行能力，目录设置不是文件系统沙箱。

`capabilities` 的 `pty_signals` 列出目标支持的信号。Windows 的 `INT` 向终端写入 Ctrl+C，由前台程序处理；`TERM` / `KILL` 终止受管理的 Job。普通 `exec` 不提供会话信号接口，可用超时或关闭连接结束。

正常停止、TTL 或会话关闭会撤销访问并清理受管理的命令进程树。Windows 使用 Job Object；Unix 使用独立进程组。Unix 子进程若主动通过 `setsid` 等方式逃离进程组，不能保证一并终止。这是合作式工具管理边界，不能用来隔离恶意命令。

`cleanup` 只删除生成目录清单中属于本会话的凭据文件，保留无关文件；它不撤销运行中的会话。stop/TTL 不回滚已写入文件、部署或其他命令副作用，不删除系统/网络审计记录。SIGKILL、崩溃或断电可能留下状态或凭据；恢复后须检查原资源与已执行操作。Relay 重启会丢失内存会话。Legacy 的保护状态与计划任务具有不同生命周期，仍受系统时钟、供电和调度服务影响。

`--keep-awake` 自愿启用。macOS 使用 `caffeinate`，Linux 使用 `systemd-inhibit`，依赖本机工具、服务和权限，属于尽力而为；Windows 使用 `SetThreadExecutionState`，不修改电源计划。它不保证阻止关机、强制睡眠或用户合盖行为。

交互终端合并 stdout/stderr，输出可能包含控制序列和回显。Agent 持久终端在控制端保存最多 1 MiB 私有输出快照，可能包含敏感内容；关闭或到期后仍可读取，结束后自行删除。可选 `--log-limit` 明文 output.log 最高 64 MiB，异步写入拥塞时会丢日志字节；它不替代 ring cursor。`terminal wait` 观察整个终端结束；write 的 operation ID 在同一 worker 生命周期保留最多 256 条，满时拒绝新 ID。未知结果不能视为输入未送达。终端关闭不撤销分享，也不回滚已执行命令。

## 新能力的生命周期

| 能力 | 有效范围与限制 |
|---|---|
| Jobs | host 须启用 `--allow-jobs`；跨 SSH 连接断开继续，host 退出或 TTL 到期结束；stdout/stderr ring 有界，cursor 可报告丢失字节 |
| Job/start 操作 ID | 同一 host manager 生命周期；完成记录不自动淘汰，默认任务总表 128、并发 4，满表拒绝新任务；同操作 ID 必须对应相同内容 |
| Terminal write 操作 ID | 同一 worker 生命周期，最多 256 个；满表拒绝新增，worker 消失后不能用旧 ID 证明结果；wait 等整个终端结束 |
| 递归/续传 | 递归支持排除与容量限制，续传为单文件；保持不覆盖，支持 checksum 与进度；owned partial/journal 明文保存内容或路径，失败不自动回滚已传文件 |
| Forward | host 精确 `--allow-forward HOST:PORT`，控制端 loopback 监听；无 SOCKS、公开监听或反向转发；断线关闭，不重放数据 |
| MCP | stdio 2026-07-28 / 2025-11-25 初始化；显式工具白名单，票据用 `@FILE` / `target:NAME` 引用；jobs start 必须有稳定 operation ID，不自动重放变更；无 Web 前端 |
| Targets | 本地别名引用已有私有票据文件，不复制、不续期；移除别名不撤销远端访问 |
| User service | Linux systemd user、macOS LaunchAgent、Windows 当前用户计划任务；注册与启动分开，可选登录自启；不做管理员或系统启动服务，不续期有限票据 |

一次 SSH 调用取消与 host 生命周期不是同一边界：长任务须用 jobs 自己的 status/logs/wait/cancel 观察和结束。start 响应丢失时保留原操作 ID，先查询或用同 ID、同内容重试，不另造 ID 重复提交。日志、PTY 输出和传输进度均不代表命令级事务提交。

## 网络条件

校园网设备隔离、跨网手机 USB 热点和 CGNAT 可能阻止 direct 入站；双方可访问可信 WSS relay 时可使用出站路线。不同热点/运营商的策略必须实际验证。HTTP CONNECT 代理需显式获准并支持目标端口；PAC 自动发现、NTLM/Kerberos 交互认证和浏览器登录态不能假设被 CLI 自动继承。TLS 检查须通过受信任的组织证书配置，不能关闭证书验证。

正常完成校园认证门户登录。HTTP 可达不等于 WebSocket upgrade 或端到端 SSH 可用；TCP 443 上的原始 SSH 也不等于 HTTPS。不自动绕过 VPN、认证门户或组织网络规则，不自动部署用户现有服务器。

## 已测试范围

项目测试覆盖 SSH 命令/SFTP、访问授权、连接撤销、TTL 和进程树清理；Windows 使用 Job Object，Unix 使用进程组。Legacy parser/COM mock 覆盖账户字段、失败处理、默认实例拒绝和二进制校验。

用户级服务通过三平台隔离管理器测试和 Windows 交叉编译。macOS 另以真实 LaunchAgent 验证计划、安装、启动、SSH 连接、状态、重复启动、停止和卸载，测试资源已清理；Linux/Windows 的真实服务管理器及各平台登录自启仍待实机验收。CI 状态以对应提交的实际运行结果为准。

自动化测试见 [GitHub Actions](https://github.com/Boulea7/shellpier/actions)。最低系统版本、各 CPU 架构和真实校园代理/热点组合未全部实机测试；Legacy 的新实例安装、SYSTEM 到期、防火墙清理及完整 Plink 路线尚未在 Win7/8 上完成端到端测试。
