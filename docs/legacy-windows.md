# Windows 7/8 Legacy 使用指南

[返回文档目录](README.md) · [兼容性与验收](compatibility.md) · [安装指南](install.md)

Windows 7 SP1/8 使用 `legacy/shellpier.cmd`，需要 Windows PowerShell 2.0 或更新版本，以及 .NET Framework 3.5。当前 Go 原生程序不适用于这两个系统。将整个 `legacy` 目录解压到本地文件夹，先执行 `doctor` 检查环境。

CLI 脚本可离线复制，采用 ASCII/CRLF；本地状态使用 PowerShell CLIXML。Bitvise 是需要管理员安装的服务端，不能称为免安装便携服务器。发行包不含 Bitvise 安装程序、厂商许可、真实账户、地址或私钥。

## 命令与权限

| 命令 | 实际行为 | 权限与影响 |
|---|---|---|
| `doctor` | 显示 OS、架构、PowerShell、CLR/.NET、桌面账户和执行账户及 SID、IPv4 网卡、工具、WinHTTP 代理、防火墙状态、既有 Bitvise 服务 | 普通用户，只读 |
| `prepare` | 打印离线 direct 安装步骤 | 仅打印，不安装 |
| `prepare -Mode reverse` | 校验本地 Plink 的 hash 和 Windows 签名，生成固定 relay 主机公钥的反向 SSH 命令 | 仅指南，不建立连接，不管理 Plink TTL |
| `start` | 新建 GUID 命名的 Bitvise 实例，配置目标账户、公钥、精确来源 IPv4、防火墙与 SYSTEM 到期任务 | 管理员；必须显式传 `-AcceptLicense -AllowInstall` |
| `status -Session ID` | 检查会话状态、到期时间、服务及监听进程；识别已过期仍运行、服务或端口缺失 | 只读；读取受保护状态可能需要管理员 |
| `stop -Session ID` | 禁用并停止本会话实例，撤销账户/公钥及来源授权，移除本会话防火墙规则和任务 | 管理员 |

`start` 创建 `ShellPier-<随机 GUID>` 实例，拒绝名称碰撞，不认领默认实例或既有命名实例。安装新实例仍可能注册共享厂商组件，已有其他 Bitvise 版本时应阅读安装器提示。脚本不自动提权、重启、改密码、关闭防火墙、修改电源计划或删除系统审计记录。

## Direct 安装与连接

普通 CMD 先执行：

```bat
shellpier.cmd doctor
shellpier.cmd prepare
```

从 [Bitvise 官方下载渠道](https://bitvise.com/ssh-server-download)获取 **SSH Server 9.66** 安装包并保留原始文件。CLI 固定比较以下 SHA-256，同时要求 Windows Authenticode 签名状态为 `Valid`、发布者为 `Bitvise Limited`：

```text
d1407f989d18f4505bf623eb1c33a68cd5c79a483214ebc1ff86bee9821293a6
```

不同版本或损坏文件会在执行前被拒绝。签名信任失败时检查系统日期、证书与合法 Windows 更新，不跳过校验。CLI 不在线下载安装程序。

在控制端生成 SSH 密钥，将单行 OpenSSH **公钥**保存为 `access.pub` 并复制到 Windows；私钥保留在控制端。阅读 [Bitvise 许可](https://bitvise.com/ssh-server-license)，确认你的用途适用哪种授权。ShellPier 的开源许可不覆盖 Bitvise，`-AcceptLicense` 仅表示接受厂商条款，不购买或激活许可证，也不自动认定你符合免费或试用条件。

以管理员身份打开 CMD，替换示例来源地址后执行：

```bat
shellpier.cmd start -Installer .\BvSshServer-Inst.exe -PublicKey .\access.pub -AllowedClient 192.0.2.10 -Port 2222 -TtlMinutes 60 -AcceptLicense -AllowInstall
```

`192.0.2.10` 是文档示例地址，应替换为控制端当前实际 IPv4。TTL 可设为 2–1440 分钟。启动前会检查端口冲突、安装包与公钥格式；安装器使用 `-instance=<name> -acceptEULA`，不传 `-startService`。任何非零退出码都要求检查安装器消息，脚本不会继续声称启动成功。

配置前禁用并停止新实例，配置完成后仅设为手动启动。全局及账户来源策略依次为「精确允许所选 IPv4」「拒绝其他来源」，IPv6 SSH 监听被移除，公钥认证为必需项。保存后重新读取验证；仅为本会话添加限定程序、端口和来源的防火墙规则，实例的自动防火墙管理关闭。

首次 Bitvise 安装可能需要重启，才能完成无密码 Windows 登录。遇到提示时先检查安装结果并清理失败会话，再自行重启、创建新会话。脚本不请求或修改 Windows 密码。它自动识别并显示 `COMPUTER\account` 或 `DOMAIN\account` 及 SID；用另一账户提权时，桌面账户与执行账户可能不同，`doctor` 会同时显示。

保存启动结果中的会话 ID，用于 `status` 和 `stop`。控制端先通过 Bitvise 控制面板或其他可信渠道核对新实例的 SSH 主机公钥，再连接并执行 `hostname`、`whoami` 验证身份。每个新实例都有自己的主机密钥。`LISTENING / remote login unverified` 只说明本地监听，不等于远程认证成功。

```bat
shellpier.cmd status -Session SESSION_ID
shellpier.cmd stop -Session SESSION_ID
```

## 到期与清理

安装前，脚本在 `%ProgramData%\ShellPierLegacy\<session>` 创建受保护目录，复制清理所需脚本并注册 SYSTEM 计划任务。任务按 UTC 到期时间执行，允许电池供电、请求唤醒，并设置错过触发后可用时补跑，因此关闭启动终端不会取消 TTL。目录 ACL 使用 Administrator/SYSTEM 的固定 SID；状态和配置备份不应公开上传。

到期或手动 `stop` 时，先禁用服务启动，再停止服务；删除实例账户及其中导入的公钥，将来源策略改为全部拒绝并回读验证，最后移除本会话的 GUID 防火墙规则与任务。**已安装但停用的 Bitvise 命名实例、受保护状态/备份、厂商日志和 Windows 审计记录保留。** 如需卸载，通过厂商控制面板确认并删除本会话的准确实例名称，不删除默认实例。

TTL 依赖系统时钟、供电和 Task Scheduler 能正常执行。关机、睡眠或调度服务停用时，不能保证在精确时刻清理；恢复后检查 `status`，若显示过期仍运行则执行 `stop`。停用服务后的手动再次启用不属于 TTL 管理范围。此处的 TTL 管理本地 Bitvise 会话，不管理下面指南中的 Plink 进程。

## NAT 下的反向 SSH 指南

需要你控制的 SSH relay、允许回环反向转发的 relay 账户，以及 Windows 上已工作的本地 SSH server。Plink 是出站客户端，不提供 Windows SSH server，也不部署或配置 relay。

若使用 ShellPier 新建本地服务器，给上述 direct `start` 加上 `-ListenAddress 127.0.0.1 -AllowedClient 127.0.0.1`。隧道连接在 Windows 看来来自本地 Plink，若只允许外部控制端 IP，会被拒绝。Windows 登录使用控制端公钥；relay 的 `.ppk` 是另一个凭据，两者私钥均不得放进公开发行包。本地服务器仍有自己的 TTL，Plink 需要单独监督和停止。

本指南固定 **PuTTY/Plink 0.85 w32**。从 [官方版本下载](https://the.earth.li/~sgtatham/putty/0.85/w32/plink.exe)获取原始文件。默认 SHA-256 为：

```text
c7be84cc42565ad4de7e77e99f0a61f3a8dec17c77d5ff1afad79d005af54c93
```

CLI 同时要求 Windows Authenticode 为 `Valid`、发布者为 `Simon Tatham`；只提取内嵌证书不等于通过 Windows 信任校验。使用其他架构/版本时，先独立核验官方来源及签名，再提供经过核验的 `-PlinkSha256`，不能为了通过校验而直接取未知下载文件的 hash。

通过可信的独立渠道取得 relay 的 SHA-256 主机公钥指纹，并准备获准的 PuTTY `.ppk`。替换下面示例中的主机、账户和指纹；命令不接受密码参数：

```bat
shellpier.cmd prepare -Mode reverse -Plink .\plink.exe -RelayHost relay.example.com -RelayUser relay-user -RelayHostKey SHA256:TRUSTED_FINGERPRINT -RelayPrivateKey .\relay.ppk -RelayPort 443 -RemotePort 2222 -Port 2222
```

该命令**仅生成操作指南，不启动连接**。输出使用 `-batch -noagent -noshare -N -hostkey` 及 `-R 127.0.0.1:2222:127.0.0.1:2222`。检查 relay 的 `AllowTcpForwarding` 和允许监听设置，保持远端回环绑定；需要通过获准 relay 会话访问转发端口，并再次验证 Windows SSH 主机公钥。详见 [Plink 官方手册](https://the.earth.li/~sgtatham/putty/0.85/htmldoc/Chapter7.html)。结束后停止 Plink；此指南没有自动重连或 Plink 到期任务。

TCP 443 上的 SSH **不是 HTTPS**。允许出站 TCP 的普通 NAT 可使用这条路线，但 HTTP-only 代理、认证门户或阻断 SSH 的网络可能不允许。先正常完成校园网认证。WinHTTP 代理只是一个配置面，环境变量、应用代理和 VPN 路由需要分别检查。

## 常见排障

- 同一私网地址经默认路由和 Wi-Fi 可能到达不同主机。核对 SSH 主机公钥及身份，并选择当前实际接口；`--noproxy` 不绕过 VPN 路由。
- `LocalSubnet` 不覆盖所有校园子网。控制端 IP 变更后，停止旧会话并新建精确来源授权。
- 实验网、校园网和虚拟网卡可同时有 IPv4；不能直接选择第一个地址。使用 doctor 的接口和网关信息判断可达路径。
- 本地 Bitvise 账户的 `winDomain` 字段可能连读取或写空值都不允许。域字段只用于域账户；公钥控制使用导入的 key 和 `auth.publicKeyAuth`。
- `-EncodedCommand` 使用 UTF-16LE Base64。中文 Win7 的 CMD 原生命令输出可能是 GB18030，PowerShell transcript 可能是带 BOM 的 UTF-16；按实际输出端解码。
- 服务会话与交互桌面不同，SSH 启动 GUI 不等于用户桌面出现窗口。Win7 CMD 通常通过右键菜单粘贴，不能假设 Ctrl+V 可用。

## 验收范围

Legacy parser 和严格 COM mock 检查已通过，覆盖本地/域账户字段、导入失败时不保存并解锁、默认实例拒绝，以及 hash/IP/端口/签名错误拒绝。**新命名实例安装、Win7/8 上的完整 CLI 执行、SYSTEM 到期、防火墙清理及完整 Plink reverse 尚未实机验收。** 现代 Windows CI runner 也不等于这些旧系统通过；对应证据见 [兼容性与验收](compatibility.md)。
