# 路线调研与故障经验

[返回文档目录](README.md) · [Legacy 指南](legacy-windows.md)

资料核验日期：2026-10-14。本页记录方案条件、实现选择和可复用的排障方法；版本升级后重新核验官方条件。替代产品没有随 ShellPier 打包，也没有被自动安装或部署。

## 现成工具能解决什么

| 工具与一手资料 | 合适的已有条件 | 对本项目需求的限制 |
|---|---|---|
| [OpenSSH](https://www.openssh.com/manual.html)、[Windows 官方组件](https://learn.microsoft.com/en-us/windows-server/administration/openssh/openssh-overview) | 已有 sshd 和可达路径，或获准的 reverse forward | Windows 可选组件从 Win10 1809/Server 2019 起提供；系统服务与账户配置不是便携当前用户入口；reverse SSH 本身不提供 HTTPS |
| [Chisel](https://github.com/jpillora/chisel) | 需要 HTTP 上的 TCP/UDP 隧道，可部署双方程序 | 隧道仍需后端 SSH server；需管理 endpoint、认证、指纹与 TLS 配置 |
| [frp](https://gofrp.org/en/docs/) | 长期维护 frps/frpc、多服务暴露与运维配置 | 模式、TLS 和访问控制各有配置；服务转发不自动提供目标账户的 SSH 登录 |
| [tmate](https://github.com/tmate-io/tmate) | 已有适合其运行环境的终端，目标是共享终端会话 | 终端协作语义与独立命令/SFTP 不同；不可据此承诺 Win7 一键服务端 |
| [Upterm](https://github.com/owenthereal/upterm) | 通过 SSH 分享终端，可用其 client/server 基础设施 | 仍须检查实际平台、终端、服务器及授权条件；不是本项目已集成的 fallback |
| [bore](https://github.com/ekzhang/bore) | 向中继暴露现有本地 TCP 端口 | 需要现有 SSH endpoint；隧道认证与 SSH payload 加密是不同层，不能把一个选项当作全部保护 |
| [Cloudflare Tunnel SSH](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-cloudflared-authentication/) | 已有 Cloudflare 组织/域名、Tunnel、Access 策略和 sshd | 官方示例需目标连接器、客户端 cloudflared/SSH 配置及认证；不是无账户的通用公共 SSH relay |
| [Tailscale SSH](https://tailscale.com/docs/features/tailscale-ssh) | 双方加入获准 tailnet，配置访问策略 | 官方 SSH server 为 Linux/macOS 开源 CLI；Windows 端可用 Tailscale 网络接已有 SSH server，不能混淆二者 |
| [Bitvise SSH Server](https://bitvise.com/ssh-server-download) | 旧 Windows 需要真实 SSH server，允许管理员安装 | 有厂商许可、签名和服务实例配置；本项目固定 9.66，厂商支持更旧 OS 不等于本项目支持 |
| [PuTTY/Plink](https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html)、[Plink 手册](https://the.earth.li/~sgtatham/putty/0.85/htmldoc/Chapter7.html) | 已有可达 SSH server 或自有 reverse-forward relay | 是客户端/隧道，不提供目标 SSH server；当前指南固定校验 0.85 w32，其他版本须独立核验 |

这些工具仍然是有效选择。已经拥有某套组织网络或系统 SSH 服务时，复用它通常比增加服务更简单。选型依据是现有条件和授权范围，不是宣称某工具能在所有网络免费穿透。

## 为什么现代路线内置 SSH

目标是解压后由当前用户启动一个临时 endpoint，不要求开启系统 sshd、改系统账户或手输目标用户名。SSH 协议与加密使用 [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh)，SFTP 使用 [pkg/sftp](https://github.com/pkg/sftp)，WebSocket 使用 [coder/websocket](https://github.com/coder/websocket)。它们是协议库，ShellPier 负责访问票据、固定主机公钥、生命周期、角色授权和命令启动；不是自行设计密码学。

中继把 host/client 的 WSS 连接配对，传递 SSH 字节；创建密钥和双方角色 token 分离。中继不解密 SSH 命令与文件，但能看到连接元数据并中断可用性。容量上限、等待超时、撤销和 TTL 必须作用于等待中及已经配对的连接，不能只删 session map。

默认前台运行、不添加开机启动。控制端邀请路线避免把控制私钥传到目标机；既有访问可生成固定主机公钥的 OpenSSH config。当前没有免费公共 relay；自托管需要用户明确选定可信域名、TLS 和操作方，不能代用户在已有 VPS 上部署。

## 从失败模式得到的规则

下表列出代码可复现的失败模式和对应测试；测试名称可在仓库 `internal/*/*_test.go` 或 `legacy/tests` 中查找。真实 OS、网络和厂商服务仍需单独验收。

| 失败模式 | 应对 | 代码/自动化证据 |
|---|---|---|
| TCP 端口通，但连到了同名私网地址的另一台机器 | 固定 SSH host key，再核验 hostname/whoami；接口选择影响实际路由 | `access.TestDialPinAuthenticationAndFallback`、`netutil.TestDarwinLoopbackInterfaceDial` |
| HTTP health 正常，WSS upgrade 被代理拦截 | 分层诊断 HTTP、upgrade、角色认证、ready 和 SSH；不报告连接成功 | `relay.TestUpgradeRejected`、`TestClientReadinessTimeoutWithoutHost` |
| 代理拒绝 CONNECT，或组织 CA 未被信任 | 显式配置获准代理/信任；保留错误原因，不关闭 TLS 检查 | `relay.TestWSSViaCONNECTProxy`、`TestCONNECTProxyRejected`、`TestTLSUntrustedRejected` |
| HTTP 重定向把 token 带往另一地址 | 不跟随带凭据的 API 重定向；检查中继 URL | `relay.TestRedirectNeverForwardsCredentials`、`TestURLSecurity` |
| 权限 token 混用或并发连接错配 | host/client 分离验证、配对队列受限、取消等待 | `relay.TestRoleAuthentication`、`TestConcurrentPairs`、`TestConnectionCapacity` |
| 客户端断线时命令已经执行 | 不自动重放；先检查远端结果 | `cli.TestRelayDisconnectDoesNotReplayCommand` |
| stop/TTL 只改状态，活动连接仍可执行 | 关闭活动/等待连接和受管理命令树，尝试新连接证明拒绝 | `relay.TestRevokeClosesActive`、`TestTTLClosesActiveAndWaiting`、`sshd.TestTTLStopsCommandTree`、`cli.TestDirectShareTTL` |
| SFTP 取消后仍挂起或泄漏连接 | 将取消传到会话和协议处理，关闭资源 | `sshd.TestSFTPCancelAndClose`、`access.TestDialCancellationClosesClient` |
| 删除凭据误伤用户文件，或清单包含路径穿越 | 仅删除可信生成清单中的本会话文件；拒绝越界；失败回滚保留未知文件 | `cli.TestCleanupRejectsTraversalManifest`、`access.TestConfigRollbackPreservesUnknownAndConflict` |
| Windows 路径含空格/特殊字符，ProxyCommand 无法运行 | 按客户端语法引用；拒绝不安全路径；PowerShell ticket 参数加引号 | `access.TestWindowsPathSafety`、`TestProxyCommandPreservesPath`；[安装指南](install.md) |
| 域账户与本地账户 COM 字段不同，写空 domain 也报错 | 先选择账户类型，域字段仅用于域账户；保存后重新读取 | `legacy/tests/bitvise-contract.ps1` |
| 修改默认 Bitvise 实例影响既有用户 | GUID 独立实例；拒绝默认实例；来源地址精确允许后 deny-all | `legacy/tests/bitvise-contract.ps1`；[Legacy 实机边界](legacy-windows.md) |
| 安装包、Plink 来源不明或旧 Windows 签名信任失败 | 固定 SHA-256，验证 Windows Authenticode 发布者；失败停止 | `legacy/tests/core-contract.ps1`；嵌入证书存在不等于 Windows 信任验收 |

还有几项不能靠 mock 消除的现场问题：校园多子网不能假设 `LocalSubnet` 可达；USB/虚拟网卡出现新地址后须重查路由；反向转发到本地服务时来源通常是 loopback；服务启动 GUI 不代表出现在交互桌面。旧 CMD/PowerShell 输出可能分别是本地代码页与 UTF-16，须按实际输出端解码；`-EncodedCommand` 使用 UTF-16LE Base64。

独立实例安装、调度到期、重启后的撤销和完整 Plink reverse 尚未完成对应旧系统的实机验收。交叉编译、parser、mock 与真实运行验证证明的是不同层，不能互相代替。

通过 [GitHub Actions](https://github.com/Boulea7/shellpier/actions) 查询对应提交和 job 的实际结果。Go 检查、现代系统 runner、Legacy parser/mock、发行构建与原生产物运行验证覆盖不同层级；现代 Windows runner 和交叉编译不证明所有旧系统、架构或最低 OS 版本通过。平台条件与验收范围见 [兼容性与验收](compatibility.md)。

## 发布与复现

使用 [安装指南](install.md) 的官方发行包/可信镜像与独立可信 checksum；发布者身份不能仅由同一镜像上的 hash 证明。Bootstrap 不修改全局 PATH，不执行网络下载的安装脚本。报告问题带 OS/架构/版本、脱敏错误和已验证层级；不提交真实地址、用户名、票据、私钥或私人聊天材料。

复现实验先限定机器与账户、授权动作和 TTL，再验证身份；结束时区分撤销连接、凭据清理、厂商实例保留和命令副作用。详细边界见 [兼容性说明](compatibility.md)与[安全策略](../SECURITY.md)。
