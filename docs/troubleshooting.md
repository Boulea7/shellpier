# 排查连接问题

遇到连接问题再按需诊断，无需每次连接先跑完整检查。先判断失败发生在哪一层：程序能否运行 → 目标进程是否仍在 → 网络是否可达 → HTTPS/WSS 是否建立 → SSH 命令是否完成。

## 最短排查路径

```sh
./shellpier version
./shellpier doctor --json
```

用[单机演练](workflows.md#1-单机演练不需要外网或中继)验证 `share → connect`。本机成功后，直接模式从另一台机器连接；中继模式在两端分别检查：

```sh
./shellpier doctor --relay https://relay.example.com --json
```

然后运行真正的 `host` 和 `connect`。`relay.example.com` 是占位符，替换为运营者提供的地址。PowerShell 使用 `.\shellpier.exe`，票据参数写成 `'@access.json'`。

## 常见现象

| 现象 | 下一步 |
|---|---|
| PowerShell 不识别 `shellpier` | 进入解压目录使用 `.\shellpier.exe`；程序不会自动加入 PATH |
| PowerShell 的 `@access.json` 报语法错误 | 加引号：`connect '@access.json' -- whoami` |
| 输出文件或目录已存在 | 换新名字，或确认旧会话已停止后清理；程序不会覆盖凭据 |
| 单机可连，另一台不能直连 | 检查选中地址、监听地址、目标防火墙与 AP 隔离；不同 NAT 使用 WSS |
| Mac 选了 Wi-Fi 地址却走 VPN | 查 doctor 的实际网卡名，直接模式尝试 `connect --interface NAME @access.json -- whoami`；DNS 仍可能走系统解析/VPN |
| 校园网返回认证页面 | 先按正常流程完成门户认证，再重试；浏览器成功不等于 CLI 长连接可用 |
| HTTPS 健康检查成功，连接失败 | 检查 WebSocket Upgrade、反向代理空闲期限、目标 host 是否启动和 CLI 认证方式 |
| 创建会话返回 401/403 | 核对创建密钥与 URL；host/client 角色票据不能代替创建密钥 |
| host/connect 返回 401/403 | 核对票据角色与中继策略，重新获取有效票据；认证错误不应无限重试 |
| 会话返回 404 | 可能已到期、被撤销或因中继重启丢失，重新邀请并启动目标端 |
| TLS 证书不受信任 | 核对域名、证书链及组织根证书配置，保持证书验证开启 |
| HTTP CONNECT 代理拒绝 | 核对获准的代理、目标端口和认证方式；PAC、NTLM/Kerberos 不能假设自动支持 |
| 客户端等不到目标端 | 确认 host 前台进程与同一会话；客户端 ready 等待最多 15 秒 |
| 目标端突然消失 | 检查 TTL、前台窗口、睡眠与中继重启；查看 `--keep-awake` 的实际平台结果 |
| `status` 找不到状态文件 | 停止会删除状态文件；用新连接验证是否已经关闭 |
| 主机公钥校验失败 | 先独立确认目标身份，再创建新会话；不要禁用主机公钥校验 |
| SFTP 找不到 Windows 文件 | 先尝试相对路径；绝对路径用 `/C:/Users/you/file.txt`，空格按客户端规则加引号 |
| 命令没有响应 | 先检查目标端是否已产生副作用，再决定重试；不会自动重放中断命令 |
| 想立即看到建连失败 | 使用 `./shellpier connect --retries 0 @access.json -- whoami`；PowerShell 写 `.\shellpier.exe connect --retries 0 '@access.json' -- whoami`。其他支持重试的命令见[命令手册](commands.md) |
| Win7/8 无法运行现代 EXE | 使用[独立 Legacy 流程](legacy-windows.md)，支持范围见[兼容性说明](compatibility.md) |

`share --interface` 筛选公布地址，`--listen` 才决定实际监听；两者不要混淆。直接接口约束当前支持 macOS/Linux，Linux 可能因 `SO_BINDTODEVICE` 权限失败，错误会明确返回，Windows 暂不支持。

## 如何读 doctor

网卡信息给出本机配置，不验证另一台机器能到达。门户认证、AP 隔离、DNS、IPv4、IPv6 是不同问题，可以分别失败。代理环境变量只显示已设置的变量名，不显示密码；变量为空不能证明系统代理或 VPN 未启用。

系统代理、VPN 路由、浏览器认证与 CLI 的代理配置并不等价。中继使用标准 HTTP 代理机制，直接 TCP 不自动经过 HTTP 代理；`ALL_PROXY` 出现在诊断里也不代表它已被当前传输采用。组织 TLS 检查必须通过可信根证书配置验证，不关闭 TLS 校验。

## 报告问题

提交 Issue 时提供 OS/CPU、`version`、失败命令的参数结构、退出码和脱敏诊断，注明单机、直接还是中继模式，以及两端分别在哪一步失败。使用 `[票据文件]` 等占位符，不附 `access.json`、`host.ticket`、identity、创建密钥、私钥或完整 Authorization。

系统与架构要求见[兼容性说明](compatibility.md)。

## 当前传输不适用时

| 工具 | 合适场景 | 额外条件 |
|---|---|---|
| [OpenSSH](https://www.openssh.com/manual.html) | 已有允许的 SSH 服务和可达路由 | 需要服务端/防火墙配置；443 上的原始 SSH 不等于 HTTPS |
| [chisel](https://github.com/jpillora/chisel) | 通过 HTTP/WebSocket 访问已有 TCP 服务 | 另需 SSH 服务、中继和认证配置 |
| [frp](https://github.com/fatedier/frp) | 统一管理长期反向代理 | 运营配置更多，按实际模式检查 TLS 和认证 |
| [Bitvise SSH Server](https://bitvise.com/ssh-server) | Legacy Windows 的独立服务端 | 核对版本、OS 支持和许可，通常需要管理员安装 |
| [PuTTY](https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html) | Windows 的现有 SSH 客户端连接 | PuTTY/Plink 本身不让目标变得可达，也不把原始 SSH 变成 HTTPS |

这些工具需从官方来源单独下载和配置。
