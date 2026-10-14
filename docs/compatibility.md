# 兼容性与验收边界

[返回文档目录](README.md) · [Legacy 指南](legacy-windows.md)

本页区分工具链支持、已经实现的路线和实际验收。支持某个 OS 的编译目标，不代表它的每个版本、CPU、网络和权限组合都已测试。

## 平台选择

| 环境 | ShellPier 入口 | 条件与边界 |
|---|---|---|
| Windows 10/11 | 原生 `shellpier.exe`，amd64/386/arm64 包 | 当前用户执行，无需系统 sshd；选与系统架构匹配的包 |
| Linux | 原生程序，amd64/arm64 包 | Go 1.26 的内核最低要求为 3.2；裁剪系统还需满足运行时要求；WSL1 不在 Go 支持范围内 |
| macOS | `darwin` 包，amd64/arm64 | v0.1.0-alpha.1 使用 Go 1.27.2，最低 macOS 13；Go 1.26 源码构建的工具链下限为 macOS 12，需另行验收 |
| Windows 7 SP1/8 | `legacy/shellpier.cmd` | 独立 Windows PowerShell 2.0/.NET 3.5 路线；Bitvise 9.66；管理员、适用许可和可信签名；新实例流程仍需实机验收 |
| XP/Vista | 没有受支持的自动路线 | 厂商工具可能支持这些 OS，但不因此构成 ShellPier 的支持承诺 |

当前源码的构建契约是 [go.mod](../go.mod) 的 Go 1.26+。上述运行时下限来自 [Go 官方最低要求](https://go.dev/wiki/MinimumRequirements)；升级工具链时须重新检查。Windows Server 的 Go 支持与桌面版本不同，本项目尚未给每个 Server 版本做验收承诺。

`scripts/bootstrap.ps1` 是现代 Windows 的下载辅助脚本，要求 PowerShell 5.1+；它不是 Legacy 入口。Legacy 文件可离线复制，但 Bitvise 是需要安装的服务端，不能称为免安装便携服务器。首次安装可能需要重启才能完成无密码 Windows 登录，脚本不自动重启。详见 [Legacy 指南](legacy-windows.md)、[Bitvise 官方下载](https://bitvise.com/ssh-server-download)与[许可](https://bitvise.com/ssh-server-license)。

## 已实现路线

| 路线 | 实现状态 | 用户仍须提供 |
|---|---|---|
| Modern direct | 内置 SSH exec/SFTP，临时公钥、固定主机公钥、TTL、stop | 允许入站的实际网络路径；无需手输目标 OS 用户名 |
| Modern WSS relay | 双方主动出站、host/client 角色分离、连接配对、撤销 | 可信 HTTPS 中继 URL 与创建密钥；无预置免费公共中继 |
| Modern invite | 控制端保留私钥，只发送 host ticket | 授权的私下传递渠道；目标机仍须运行 host |
| OpenSSH-compatible Agent | 生成独立 SSH config/identity/known_hosts，ProxyCommand 使用 ShellPier | Agent 支持 OpenSSH 配置并能找到本地可执行文件 |
| Legacy direct | 独立 Bitvise 实例、精确 IPv4 来源规则、SYSTEM 到期任务 | 管理员确认、官方固定版本安装包、有效许可、控制端公钥与实际客户端 IP |
| Legacy Plink reverse | 校验本地二进制与发布者，打印固定 relay host key 的命令 | 自有 SSH relay、relay 用户/.ppk、公钥指纹、本地 SSH server；执行与 Plink 生命周期由用户监督 |
| 其他替代工具 | 调研/操作指南，尚未集成自动适配 | 各工具自己的安装、账户、许可、服务和安全配置 |

现代 WSS 路线无需填写目标用户名/IP，但中继配置仍须由可信操作方提供。Legacy direct 与 reverse 有不同的输入和外部安装条件，不能承诺统一的一键体验。不要把打印命令称为已连接，也不要把监听端口称为已认证。

## 执行与撤销

当前版本提供当前用户的命令执行和 SFTP；不提供 PTY、桌面控制、系统用户切换或 PAM 登录。访问密钥授予该进程所属账户的执行能力，目录设置不是文件系统沙箱。

正常停止、TTL 或会话关闭会撤销访问并清理受管理的命令进程树。Windows 使用 Job Object；Unix 使用独立进程组。Unix 子进程若主动通过 `setsid` 等方式逃离进程组，不能保证一并终止。这是合作式工具管理边界，不能用来隔离恶意命令。

`cleanup` 只删除生成目录清单中属于本会话的凭据文件，保留无关文件；它不撤销运行中的会话。stop/TTL 不回滚已写入文件、部署或其他命令副作用，不删除系统/网络审计记录。现代运行状态不是重启恢复数据库：SIGKILL、崩溃、断电可能留下状态或凭据；恢复后须检查原资源与已执行操作。Relay 重启会丢失内存会话。Legacy 的保护状态与计划任务具有不同生命周期，仍受系统时钟、供电和调度服务影响。

`--keep-awake` 自愿启用。macOS 使用 `caffeinate`，Linux 使用 `systemd-inhibit`，依赖本机工具、服务和权限，属于尽力而为；Windows 使用 `SetThreadExecutionState`，不修改电源计划。它不保证阻止关机、强制睡眠或用户合盖行为。

## 网络条件

校园网设备隔离、跨网手机 USB 热点和 CGNAT 可能阻止 direct 入站；双方可访问可信 WSS relay 时可使用出站路线。不同热点/运营商的策略必须实际验证。HTTP CONNECT 代理需显式获准并支持目标端口；PAC 自动发现、NTLM/Kerberos 交互认证和浏览器登录态不能假设被 CLI 自动继承。TLS 检查须通过受信任的组织证书配置，不能关闭证书验证。

正常完成校园认证门户登录。HTTP 可达不等于 WebSocket upgrade 或端到端 SSH 可用；TCP 443 上的原始 SSH 也不等于 HTTPS。不自动绕过 VPN、认证门户或组织网络规则，不自动部署用户现有服务器。

## 什么算通过

按顺序保存脱敏证据：版本/OS/CPU → doctor → HTTPS/WSS → `hostname`/`whoami` → 授权命令/SFTP → 新连接在 stop/expiry 后被拒绝。连接失败时先检查副作用，再决定是否重试。

通过 [GitHub Actions](https://github.com/Boulea7/shellpier/actions) 查询对应提交与 job 的实际结果。不同证据覆盖不同范围，不能仅凭配置或构建成功声称运行验收通过：

| 证据类型 | 需要核对的范围 |
|---|---|
| Go 检查与 CI runner | 对应 job 的 OS/架构、检查项目及实际结果 |
| 发行构建与校验 | 目标平台/架构、来源提交和产物 SHA-256；构建不等于运行 |
| 原生产物运行验证 | SSH 命令执行、SFTP 与停止后拒绝新连接 |
| Legacy parser / COM mock | 账户字段、失败处理、默认实例拒绝和二进制校验契约；不执行厂商安装或真实到期任务 |

新 Legacy 独立实例安装、Win7/8 上的完整 CLI、SYSTEM 到期、防火墙清理及完整 Plink 路线仍未实机验收。其他实例的验证不能替代新流程验收。现代 Windows runner 不等于 Windows 7/8 或 Windows 10 各版本通过；交叉编译也不证明所有架构与最低 OS 版本能运行。

后续提交的结果见 [GitHub Actions](https://github.com/Boulea7/shellpier/actions)，应核对具体 commit 和 job，而不是把上述版本的结论套用到所有发行版。
