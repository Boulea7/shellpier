# 排查连接问题

遇到连接问题再按需诊断，无需每次连接先跑完整检查。先判断失败发生在哪一层：程序能否运行 → 目标进程是否仍在 → 网络是否可达 → HTTPS/WSS 是否建立 → SSH 命令是否完成。

## 最短排查路径

```sh
./shellpier version
./shellpier doctor --json
./shellpier doctor --target @access.json --timeout 20s --json
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

## 新命令的状态边界

| 现象 | 处理 |
|---|---|
| `target:NAME` 找不到票据 | 用 `targets show NAME --json` 核对引用，保留票据私密；名称本身不保存新授权 |
| exec 返回 `exit_code=-1` | 看 `exit_status_known` 与 `termination_reason`；取消、到期、连接丢失不等于已知远端退出码 |
| terminal 输入响应丢失 | 有 operation ID 时先 `terminal operation` 查询；`completed` 只确认字节提交，未知状态不自动重发 |
| operation 缓存满 | 同一 worker 的 256 条记录不淘汰；已有 ID 仍可查询，新 ID 被拒绝；不要用重启来掩盖未知输入结果 |
| terminal wait 返回 `closed=false` | 有界等待结束，终端可能仍运行；继续查询，不把输出安静当命令完成 |
| `output.log` 比终端输出短 | 看 `log.truncated/error`；容量或慢盘会丢日志，但不阻塞 SSH；默认环形缓冲也只留最新 1 MiB |
| jobs 功能被拒绝 | 完整模式默认允许；查看是否设置 `--allow-jobs=false`，或使用了只分享文件的 root/受限模式 |
| 断线后找不到 job | 任务仅跨 SSH 断线保留，不跨 host 停止、TTL 或进程重启恢复 |
| forward 目标被拒绝 | 完整模式默认允许；若 host 配置了 `--allow-forward` 限制，核对精确目标；并发连接受全局 8 槽限制 |
| 续传拒绝目标或 journal | 保留部分文件与私有 journal 核对身份；不改写为强制覆盖；递归传输不能使用 resume |

诊断输出只分享脱敏阶段信息。票据、终端状态 secret、日志与任务输出均可能包含敏感内容，不附入公开 Issue。


## 服务与 MCP

| 现象 | 处理 |
|---|---|
| service install 后没有进程 | 安装不启动；执行 `service start NAME`；`--autostart` 只选择以后登录自启 |
| 服务票据仍在却无法启动 | 检查 host 角色与期限；服务不会续期。会话 ID 已换时显式卸载、重新安装 |
| service stop/uninstall 未确认撤销 | 保留 host 票据，执行 `revoke` 确认中继撤销；卸载不删除票据 |
| MCP 协议版本不匹配 | 使用支持的 2026-07-28 无状态请求或 2025-11-25 初始化流程，不混用握手 |
| MCP 拒绝远端工具参数 | 显式传 `@FILE` 或 `target:NAME`；jobs start 还需稳定 operation ID |
| MCP 输出截断或任务 outcome unknown | 截断不是完整结果，取消不是回滚；保留原 operation ID 查询或确认，不新建 ID 重新提交 |


## 保活、访问模式与传输选项

| 现象 | 处理 |
|---|---|
| 建连先于命令 timeout 结束 | `--connect-timeout` 限制单候选建连，默认 10 秒；重试次数和命令总期限另算 |
| SSH 保活超时后连接关闭 | 核对网络与 `--keepalive-interval/--keepalive-timeout`；结果可能未知，先查询副作用，不自动重跑 |
| 服务端不支持保活请求 | SSH 请求返回 false 也算存活，不应因此关闭连接；不要将应用错误当作网络超时 |
| check 成功但 identity_verified=false，或 doctor 身份阶段不可用 | 受限模式只完成 SSH 认证与能力广告，不运行身份命令；这不等于实际探测 SFTP，也不是整体连接失败 |
| SFTP 能用而 exec/shell/jobs/forward 被拒绝 | 查看 `capabilities` 的 `access_mode`；目标选择非 full 模式时这是策略限制 |
| 只想分享一个目录 | 使用 `--root DIR`，不必同时设置访问模式；`--work-dir` 只是起始目录 |
| 想保留已有文件继续传其他文件 | 使用 `--skip-existing`，已有普通文件直接跳过，不判断内容是否相同 |
| dry-run/resume 参数冲突 | dry-run 不建立续传状态；单文件 resume 不能与 dry-run 或 skip-existing 合用 |
| 传输后时间或权限不同 | preserve-times 仅新建文件/目录的秒级 mtime，不保留原 atime、权限或所有权 |
| 使用 control-persist 后准备清理 | 先用原配置 `ssh -F PATH/ssh_config -O exit shellpier` 显式关闭后再清理；cleanup 不撤销、不杀不确定 master |

保活、传输计划和连接复用不延长票据期限。保持原 operation ID 查询不确定的提交结果，不因连接关闭自动生成新的写入请求。


`--root` 分享中远端路径以 `/` 为起点；不要再拼接目标端真实目录前缀。反向转发的 `--to` 是控制端地址，监听端口在目标端。目录边界的高级限制见[命令参考](commands.md#文件目录边界)。

## SOCKS5 与管道传输

- SOCKS5 程序应选择远端域名解析，例如 curl 的 `--socks5-hostname`；仅 TCP CONNECT 可用，UDP、SOCKS4 和 BIND 会被拒绝。受限文件分享不提供代理。
- `--dynamic` 不与固定 `--to` 或 `--reverse` 合用；需要目标端固定端口监听时使用反向转发。
- MCP 上传/下载拒绝本地 `-`，请改用文件路径；MCP 的 stdin/stdout 保留给 JSON-RPC。
- download 到 `-` 的 stdout 是文件内容，不含传输摘要，不与 `--json` 合用。进度写 stderr，接收方仍须处理失败前已收到的数据。
- PowerShell 旧版文本管道可能重编码二进制；改为普通文件路径，或使用保持字节的重定向方式。上传流被中断时可能留下远端部分文件，重新上传不会覆盖它。
