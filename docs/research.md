# 连接路线与工程约束

[返回文档目录](README.md) · [Legacy 指南](legacy-windows.md)

本页说明连接方案的条件与实现选择。替代产品不随 ShellPier 打包或自动安装。

## 现成工具能解决什么

| 工具与一手资料 | 合适的已有条件 | 对本项目需求的限制 |
|---|---|---|
| [OpenSSH](https://www.openssh.com/manual.html)、[Windows 官方组件](https://learn.microsoft.com/en-us/windows-server/administration/openssh/openssh-overview) | 已有 sshd 和可达路径，或获准的 reverse forward | Windows 可选组件从 Win10 1809/Server 2019 起提供；系统服务与账户配置不是便携当前用户入口；reverse SSH 本身不提供 HTTPS |
| [Chisel](https://github.com/jpillora/chisel) | 需要 HTTP 上的 TCP/UDP 隧道，可部署双方程序 | 隧道仍需后端 SSH server；需管理 endpoint、认证、指纹与 TLS 配置 |
| [frp](https://gofrp.org/en/docs/) | 长期维护 frps/frpc、多服务暴露与运维配置 | 模式、TLS 和访问控制各有配置；服务转发不自动提供目标账户的 SSH 登录 |
| [tmate](https://github.com/tmate-io/tmate) | 已有适合其运行环境的终端，目标是共享终端会话 | 终端协作语义与独立命令/SFTP 不同；不提供本项目所需的 Win7 SSH 服务端 |
| [Upterm](https://github.com/owenthereal/upterm) | 通过 SSH 分享终端，可用其 client/server 基础设施 | 仍须检查实际平台、终端、服务器及授权条件；需单独配置 |
| [bore](https://github.com/ekzhang/bore) | 向中继暴露现有本地 TCP 端口 | 需要现有 SSH endpoint；隧道认证与 SSH 加密需要分别配置 |
| [Cloudflare Tunnel SSH](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-cloudflared-authentication/) | 已有 Cloudflare 组织/域名、Tunnel、Access 策略和 sshd | 官方示例需目标连接器、客户端 cloudflared/SSH 配置及认证；不是无账户的通用公共 SSH relay |
| [Tailscale SSH](https://tailscale.com/docs/features/tailscale-ssh) | 双方加入获准 tailnet，配置访问策略 | 官方 SSH server 为 Linux/macOS 开源 CLI；Windows 端需通过 Tailscale 网络连接另行配置的 SSH server |
| [Bitvise SSH Server](https://bitvise.com/ssh-server-download) | 旧 Windows 需要真实 SSH server，允许管理员安装 | 有厂商许可、签名和服务实例配置；本项目固定 9.66，支持范围见 Legacy 指南 |
| [PuTTY/Plink](https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html)、[Plink 手册](https://the.earth.li/~sgtatham/putty/0.85/htmldoc/Chapter7.html) | 已有可达 SSH server 或自有 reverse-forward relay | 是客户端/隧道，不提供目标 SSH server；当前指南使用 0.85 w32 |

这些工具仍然是有效选择。已经拥有某套组织网络或系统 SSH 服务时，复用它通常比增加服务更简单。按现有条件、授权范围和网络可达性选择。

## 为什么现代路线内置 SSH

目标是解压后由当前用户启动一个临时 endpoint，不要求开启系统 sshd、改系统账户或手输目标用户名。SSH 协议与加密使用 [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh)，SFTP 使用 [pkg/sftp](https://github.com/pkg/sftp)，WebSocket 使用 [coder/websocket](https://github.com/coder/websocket)。它们是协议库，ShellPier 负责访问票据、固定主机公钥、生命周期、角色授权和命令启动。

中继把 host/client 的 WSS 连接配对，传递 SSH 字节；创建密钥和双方角色 token 分离。中继不解密 SSH 命令与文件，但能看到连接元数据并中断可用性。容量上限、等待超时、撤销和 TTL 同时管理等待中与已配对的连接。

默认前台运行，会话 TTL 为 3 小时，不添加开机启动。控制端邀请路线避免把控制私钥传到目标机；既有访问可生成固定主机公钥的 OpenSSH config。当前没有免费公共 relay；自托管需要用户明确选定可信域名、TLS 和操作方。

平台范围见[兼容性说明](compatibility.md)，权限与凭据保护见[安全策略](../SECURITY.md)。
