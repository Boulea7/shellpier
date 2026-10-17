# 工具选型与工程取舍

[返回文档目录](README.md) · [路线图](roadmap.md) · [Legacy 指南](legacy-windows.md)

调研日期：2026-10-17。本文记录 ShellPier v0.8的能力与选型取舍；构建、平台验收与发行状态分别以对应提交的 CI 和 Release 为准。外部工具的能力以所链接的一手文档为依据，不随 ShellPier 打包或自动安装。

ShellPier 面向当前用户临时开放的 CLI 与 AI 远程操作：建立可到期、固定主机公钥的访问，再执行命令或传文件。已经有系统 SSH、组织网络或文件同步设施时，复用它们往往更简单。

## 按任务选择工具

| 工具 | 适合解决的问题 | 与 ShellPier 的关系和限制 |
|---|---|---|
| OpenSSH、sftp、scp | 已有 SSH 服务与账户，交互登录、批处理传输、连接复用和跳板 | ShellPier 可生成隔离配置，复用原生客户端。`scp` 使用 SFTP 的模式与 `-O` 旧 SCP 模式不同；不能因为支持 SFTP 就承诺旧 SCP 协议兼容。[SSH 配置][ssh-config]、[sftp 手册][sftp]、[scp 手册][scp] |
| rsync | 反复同步目录、差量传输、丰富的属性与过滤规则 | 远程 shell 路线要求两端安装 rsync。ShellPier 的目录复制和单文件续传不等于 rsync 的差量算法，也不提供镜像删除。[rsync 手册][rsync] |
| rclone | 多种存储后端之间复制、同步和检查 | `copy` 保留目标多余文件，`sync` 可以删除它们。SFTP 后端的 hash 可能需要远端 shell 命令；只读 SFTP 不能因此被视为支持这套能力。[copy][rclone-copy]、[sync][rclone-sync]、[SFTP 后端][rclone-sftp] |
| croc | 用短码在两台机器间传文件或目录，包含中断续传 | PAKE 与中继降低文件交接门槛；它的文件交接目标不同于持续授权远端执行。ShellPier 保留票据与主机身份，不另加短码密钥交换路线。[项目说明][croc] |
| Mosh | 网络短暂中断、切换 IP 后继续交互终端 | 使用 SSH 引导、UDP 同步屏幕状态，可以跳过中间画面；不是恢复完整 SSH 字节流。需要 UDP 可达，不直接替代 WSS 上的命令、文件与转发。[协议与使用说明][mosh] |
| autossh | 监测 SSH 连接并在失败后重启，常用于长期隧道 | 重启客户端不等于保存远端进程。ShellPier 可以吸收失联检测与退避；不能据此自动重放可能已有副作用的命令。[官方 README][autossh] |
| tmate | 多人进入同一终端，分别持有可写或只读入口 | 支持共享终端及公钥访问限制。ShellPier 的旧 `terminal` 使用控制端 worker，新 `terminals` 由 host 托管；两者都不是参与者身份与权限分离的多人协作服务；中继 host/client 角色也不是观察者/操作者权限。[官方发布说明][tmate] |
| Tailscale SSH | 已有 tailnet，按设备身份、用户和访问策略登录 | 集成网络与 SSH 授权，check 模式可要求重新认证；交互式认证策略需要考虑自动化。它与“通过 Tailscale 网络连接普通 OpenSSH”是两种方案。[授权与 check 模式][tailscale]、[两种 SSH 路线][tailscale-ssh] |
| Teleport | 组织统一身份、短期证书、角色策略、会话录制与访问治理 | CA、身份提供方和策略平台有其部署与运维成本。短期证书、空闲超时和证书过期后断开是不同策略；ShellPier 的票据 TTL 不等于完整组织身份系统。[认证架构][teleport-auth]、[角色策略][teleport-roles] |

## 与 OpenSSH 逐项比较

能接入 OpenSSH 客户端，不代表实现了 OpenSSH 服务端的全部功能。下表区分 ShellPier 内置能力、可复用的客户端行为和当前缺口；具体算法与硬件支持还取决于用户安装的 OpenSSH 版本。

| 范围 | OpenSSH 的能力 | ShellPier 当前状态与取舍 |
|---|---|---|
| 登录与认证 | 系统账户登录、多种认证方式、证书与硬件密钥。[ssh][ssh] | 使用票据里的单个授权公钥，在已登录的当前账户运行；没有密码/PAM 登录、CA/principal 管理、FIDO 注册或多用户切换。当前无需维护系统 sshd。 |
| 连接管理 | 建连期限、加密保活、ControlMaster/ControlPersist。[ssh_config][ssh-config] | 提供内置建连期限和 SSH 保活，生成配置可启用 Unix 连接复用；内置每次调用仍单独建连。 |
| 命令与终端 | exec、PTY、交互转义键及多种客户端控制。[ssh][ssh] | 支持 exec、PTY、尺寸同步，另有 Agent terminal/jobs；不解析 OpenSSH 交互转义键，信号能力以 capabilities 为准。原生 ssh 的客户端操作可以继续使用。 |
| 文件访问策略 | `ForceCommand internal-sftp`、目录隔离及转发开关。[sshd_config][sshd-config] | `--root DIR` 自动进入 SFTP-only，将目录映射为虚拟 `/`；可配合 `sftp-readonly`。不需要管理员权限，不隔离 shell 或整个操作系统。 |
| 每个凭据的细粒度策略 | authorized_keys 可限制来源、强制命令、PTY 和转发。[sshd][sshd] | 每个 host 使用单个授权公钥和统一访问模式；没有逐公钥命令规则、多组权限或来源策略语言。 |
| 网络转发 | 本地、远端、动态代理、Unix socket 和隧道设备。[ssh][ssh] | full 模式默认开放 TCP 转发；可选 `--allow-forward` 保留目的地址限制。内置本地转发支持端口简写与 SOCKS5 CONNECT；反向转发兼容原生 `ssh -R`，远端仅监听 loopback，支持端口 0 自动分配。不提供远端 Unix socket、TUN 或 X11/认证 agent 转发。 |
| 跳板与配置 | ProxyJump、Host/Match/Include 等客户端配置。[ssh_config][ssh-config] | `targets` 是票据文件别名；不解析完整 ssh_config，也没有原生多跳编排。`proxy --to` 提供类似 `ssh -W` 的字节桥，可组合用户自己的下一跳认证；没有完整自动多票据 ProxyJump。生成配置的 ProxyCommand 与 ProxyJump 不能直接叠加。 |
| 生命周期 | 连接保活、闲置 channel/连接超时是不同策略。[sshd_config][sshd-config] | 有授权 TTL、命令/任务总时限及 SSH 保活；没有单独的闲置 channel 超时。保活有响应不等于用户仍在操作。 |

认证 agent 指 `ssh-agent` 密钥代理，并非 AI Agent。ShellPier 不增加逐密钥策略语言、CA 或组织权限管理；full 模式按当前账户的远程操作入口设计，默认可使用 jobs 和 TCP 转发。OpenSSH 也明确指出：仍有 shell 权限时，单独关闭转发不能构成可靠隔离。[服务端配置][sshd-config]

### v0.6 新吸收了什么，还缺什么

| 机制 | 原工具已有行为 | ShellPier 本轮落地与剩余差异 |
|---|---|---|
| 动态转发 | OpenSSH `-D` 在客户端解析 SOCKS4/5，再通过 SSH 请求远端连接。[ssh][ssh] | v0.5 的服务端已经支持原生 `ssh -D` 所需的 direct-tcpip；v0.6 补齐不依赖外部 ssh 的 `forward --dynamic` 入口，只做 SOCKS5 CONNECT 与目标端域名解析。没有 SOCKS4、BIND、UDP 或透明路由。 |
| stdio 跳板 | OpenSSH `-W HOST:PORT` 把 stdin/stdout 转为远端 TCP 连接，可组合代理配置。[ssh][ssh] | `proxy --to HOST:PORT` 复用现有 SSH 认证、期限与目的地址限制，无需额外监听。下一跳身份验证仍由调用程序负责；不实现完整 ssh_config 解释器或多票据自动链路。 |
| 文件管道 | rclone `rcat` 从 stdin 上传，`cat` 输出文件内容；croc 也支持 stdin/stdout。[rcat][rclone-rcat]、[cat][rclone-cat]、[croc 管道][croc] | `upload TICKET - REMOTE` / `download TICKET REMOTE -` 省去本地中间文件，沿用 SFTP，默认不覆盖。不能把不可回放的 stdin 当作可自动重试来源，不与续传或递归等文件状态功能混用。rclone rcat 可覆盖目标，这一点不照搬。 |

可组合跳板的例子：`ssh -o 'ProxyCommand=shellpier proxy target:lab --to %h:%p' user@host`。这里第一跳使用 ShellPier 票据，最终主机仍用普通 SSH 账户与主机密钥验证。生成配置已有的 ProxyCommand 不与 ProxyJump 自动合并。[SSH 配置][ssh-config]

OpenSSH 仍有更完整的系统账户认证、证书/FIDO、逐密钥策略、远端 Unix socket/TUN/X11/认证 agent 转发、客户端配置与交互控制。ShellPier 继续复用原生客户端能力；不把这些缺口统称为已兼容。SFTP 也有独立命令和扩展：例如 OpenSSH `sftp -b -` 的 stdin 是批处理命令，并不是上传 payload；服务端拷贝、权限/所有者操作、fsync 等需逐项确认协议扩展，不能从“能连 sftp”推导全部支持。[sftp][sftp]

v0.6 先补了可直接复用 SSH 数据流的三项功能。v0.7 继续吸收多监听、清单输入和终端输出组合；普通 share 即可使用，不增加前置检查或许可流程，普通传输仍直接复制，不增加额外 SHA 或逐密钥策略配置。

### v0.7 的选择：组合已有链路，保留明确边界

| 比较 | 高置信事实 | 本轮工程取舍与剩余缺口 |
|---|---|---|
| OpenSSH / Chisel 多转发 | OpenSSH 可在一个连接中配置多个转发，Chisel 也提供多端口复用。[ssh][ssh]、[Chisel][chisel] | 重复 `forward --local` 补齐一个 SSH 下的多本地固定监听；先全部绑定再 ready，单目的失败隔离到 stream。v0.7 没有增加运行时动态修改监听、远端动态 SOCKS 或通用路由；共享容量仍有界。 |
| rsync / rclone 文件清单 | rsync `--files-from` / `--from0` 支持文件选择，但影响相对路径与递归语义；rclone 的过滤清单有自己的空白、注释和 raw 规则。[rsync][rsync]、[rclone filtering][rclone-filtering] | 只吸收“明确选文件”：有界 LF/CRLF 或 NUL 清单、普通文件、保留相对层级。ShellPier 不裁剪空格、不认注释，拒绝路径越界与大小写冲突；不声称与任一工具的清单完全兼容，不加入差量或镜像删除。 |
| tmux / Mosh / tmate 的会话 | tmux detach/attach 由远端服务保存会话；Mosh 同步屏幕状态；tmate 支持共享会话及参与者入口。[tmux][tmux]、[Mosh][mosh]、[tmate][tmate-readme] | `terminal tail` 只把现有 worker 的有界字节输出交给管道工具，可跟随并报告丢失。它不是 host 托管 PTY、Mosh 漫游或多人共享。 |

OpenSSH 9.2 已引入 `ChannelTimeout` 和 `UnusedConnectionTimeout`，分别处理闲置 channel 与无 channel 连接；它们不同于保活、TTL 和命令总期限。ShellPier 本轮不增加这套超时策略矩阵。[OpenSSH 9.2 发布说明][openssh-92]

SFTP 的扩展名称与后端实现也必须分开。OpenSSH 协议文档定义 `copy-data`，不代表任意 SFTP 服务都支持；当前 pkg/sftp v1.13.11 请求服务器未实现该扩展，ShellPier 不宣称服务端拷贝。rclone 的 SFTP server-side copy 则有 hardlink 路线，硬链接共享底层文件也不同于独立复制，不能照搬为“零成本复制”承诺。[OpenSSH PROTOCOL][openssh-protocol]、[pkg/sftp 源码][pkg-sftp-server]、[rclone SFTP][rclone-sftp]

v0.7 暂缓 host 托管可重连 PTY，v0.8 新增独立 `terminals` 入口；其实现需要独立会话 manager、远端输出/输入状态、去重、TTL 与恢复身份，还要管理 ConPTY 的管道排空和关闭生命周期。Windows 官方文档说明创建、I/O 与关闭均需配套处理；仅重启 SSH 客户端或启用 ControlPersist 不保存远端 PTY。[Microsoft ConPTY][conpty]、[autossh][autossh]。tmate 官方仓库还说明公共服务已于 2025 年 7 月关闭，现需自托管，不能把历史公开入口作为现成依赖。[tmate README][tmate-readme]

### v0.8：保存目标端 PTY，补齐反向 SOCKS 与独立文件复制

| 对照 | 吸收的机制 | 当前实现与保留的缺口 |
|---|---|---|
| tmux / Mosh / tmate | 将进程生命周期与 SSH 连接分开；tmux 服务保存会话，Mosh 同步屏幕，tmate 另有参与者入口。[tmux][tmux]、[Mosh][mosh]、[tmate][tmate-readme] | `terminals` 的 host manager 保留同一 PTY、绝对输出 cursor 和 open/write 去重，断线后用同票据和 ID 继续；不做屏幕模拟、预测回显、UDP 漫游或参与者授权。host 退出/TTL 后不恢复，旧 `terminal` worker 保留。 |
| OpenSSH 反向动态转发 | 远端监听接入 SOCKS，请求由客户端处理；不同于正向代理从服务端出站。[ssh][ssh] | `forward --reverse --dynamic --listen 0` 提供目标端 loopback SOCKS5 CONNECT，控制端解析和出站。`--allow-forward` 约束目标端正向出站，不能声称限制控制端出站；没有 UDP/BIND、Unix socket 或运行时监听管理。 |
| SFTP copy-data / rclone hardlink | 协议定义、服务端实现和文件独立性分别核对。[OpenSSH PROTOCOL][openssh-protocol]、[pkg/sftp][pkg-sftp-server]、[rclone SFTP][rclone-sftp] | `files copy` 使用固定缓冲经控制端读回再写入远端，明确 `transport:client-stream`；不调用 copy-data、不创建硬链接，只复制普通文件且不覆盖、不创建父目录。失败可能留部分目标，不能凭路径盲删。 |

本轮不实现完整 `ChannelTimeout` / `UnusedConnectionTimeout`：闲置 channel、无 channel 连接、授权 TTL、控制调用 timeout 和终端 lifetime 是不同期限。[OpenSSH 9.2][openssh-92]。PTY 的 `--timeout` 只限制控制调用，不表示已提交输入没有执行；未知结果先查原 operation ID。

SFTP 扩展不能只看 advertise：pkg/sftp 的 `statvfs` 声明不代表 ShellPier rooted handler 已实现 `StatVFSFileCmder`；客户端存在 fsync 方法也不代表内置请求服务器实现 `fsync@openssh.com`。本轮不承诺 statvfs、fsync 或 copy-data 服务端能力，不添加普通复制 hash。[pkg/sftp 服务端源码][pkg-sftp-server]、[客户端源码](https://github.com/pkg/sftp/blob/v1.13.11/client.go)

源码、自动化测试、平台原生运行、发行包验收和发布状态分别记录，不把交叉编译视为 Windows ConPTY 运行验收。

### 目录限定与反向监听为什么这样做

OpenSSH 的 `ChrootDirectory` 要求路径各层由 root 拥有且不可被组或其他用户写入；`internal-sftp` 可免去 jail 内的 shell 和支持文件。ShellPier 吸收“只传文件时只提供 SFTP”的思路，用 `--root DIR` 避免系统账户和管理员配置；仅设置工作目录不足以限定访问。[OpenSSH 目录与子系统配置][sshd-config]

目录访问使用 Go `os.Root`，约束路径及符号链接解析；它不是进程沙箱，也不阻止挂载点、bind mount 或 Unix 设备访问。目录中已有硬链接和系统特殊文件仍需要考虑。Go 文档还列出 Windows 设备名限制、Unix 部分元数据方法的竞态及 JS 的路径竞态限制，不能据此宣称任意平台都有相同隔离保证。[Go os.Root][go-root]

反向转发选择 loopback，沿用 OpenSSH `GatewayPorts=no` 的默认方向，不增加公网绑定选项。`PermitListen` 管监听地址和端口，不是连接目的地址；OpenSSH 对 `localhost` 与显式 `127.0.0.1`、`::1` 的处理也有区别。这些区别应由实现兼容处理，而不是交给用户配置一套策略矩阵。[OpenSSH 监听配置][sshd-config]

## 已有连接与执行能力

ShellPier 已有直连与 WSS 中继、邀请和撤销、TTL、SSH exec/PTY/SFTP，以及面向 Agent 的 schema、JSON 输出和 MCP stdio。中继配对双方并传递 SSH 字节，不解密命令或文件；中继运营者仍能观察连接元数据或中断连接。当前没有免费公共 relay，自托管需要自行选定域名、TLS 和运营方。

已有票据可生成固定主机公钥的 OpenSSH 配置，使用原生 `ssh -F` 或 `sftp -F … -b …` 执行命令与批量传输。无需为批处理再造一套文件操作语言。WSS 配置使用 `ProxyCommand`；OpenSSH 明确规定它与 `ProxyJump` 竞争，先设置的选项会阻止另一项生效，不能简单叠加。[SSH 配置][ssh-config]、[sftp 批处理][sftp]

后台 jobs 与新 `terminals` 可在 SSH 连接断开后继续运行，并通过 operation ID 查询提交结果；范围限于同一个 host 进程。旧 `terminal` 保留控制端 worker、远端 PTY 和有界输出，可脱离当前调用继续操作；它不保证 SSH 断线后恢复原 PTY。两种终端都不跨 host 重启恢复。对于结果未知的命令，应查询原 operation，而不是自动再执行一次。

文件传输提供目录遍历与排除、单文件续传。续传所需的身份与内容绑定自动在内部处理，用户不需要先生成或核对摘要；它不是可跨任意目标复用的缓存。

## 吸收的机制与简化

当前实现包含以下能力；实际参数与示例见命令帮助和[命令参考](commands.md)。

| 改进 | 用户价值 | 保证的边界 |
|---|---|---|
| SSH 连接超时与应用层保活 | 限制建立连接的等待，发现连接已经失效 | 保活只检测存活，不恢复原连接、不重放命令。WSS 心跳与 SSH 保活位于不同层。[OpenSSH 的超时与保活][ssh-config] |
| `--access-mode full\|sftp\|sftp-readonly` | 只需要传文件时，不同时授予命令执行与转发 | sftp 与 sftp-readonly 模式在服务端拒绝 shell、jobs 和 forward；只读模式还限制特殊 OPEN 与 hardlink 等写入路径。不带 `--root` 时仍可读取当前账户有权读取的文件；需要限定目录时直接加 `--root DIR`。 |
| transfer 的预览、时间保留、跳过已有文件与限速 | 减少操作步骤和重复传输，让 Agent 先查看计划 | `--dry-run` 不创建目标或续传状态；`--preserve-times` 保留秒级 mtime，不保留所有者和权限；`--skip-existing` 直接跳过已有文件，不读取内容作比较，也不证明相同；`--bwlimit` 限制整次操作的数据读取速率，不含元数据和协议开销。普通复制不额外计算 hash，不再提供 `files checksum`、`--verify` 或 `--skip-identical`。 |
| `ssh-config` 的保活与可选 `--control-persist` | 原生客户端可减少重复连接的成本 | 复用默认关闭；Unix 使用每票据私有 socket。复用已有 SSH 连接不创建持久终端，也不延长票据 TTL；不承诺 Windows 提供同等 socket 复用行为。[OpenSSH 复用配置][ssh-config] |

这些选择吸收的是可单独使用的机制。没有照搬 rsync 的完整同步策略或 rclone 的存储后端体系，也没有因为 autossh 能重启 SSH 就加入远端操作重试。

## 不能混淆的保证

**保活、重连、漫游和持久化。** 保活发现无响应；重连建立新连接；Mosh 漫游同步终端屏幕；持久进程在连接之外保存工作。OpenSSH ControlMaster/ControlPersist 复用连接，autossh 监测和重启连接，都不能单独保证远端任务或 PTY 存活。[SSH 配置][ssh-config]、[autossh][autossh]、[Mosh][mosh]

**内容一致、原子可见和落盘持久。** hash 比较回答读取到的内容是否相同；atomic rename 回答读者是否会看到中间文件；fsync 请求把数据提交到存储。三者不能互相替代，也不能把整个目录复制变成事务。OpenSSH 的 SFTP 上传落盘请求依赖服务端 `fsync@openssh.com` 扩展；SSH 的加密与 MAC/认证加密保护传输，不证明目标文件已经可靠落盘。普通复制省去额外内容摘要是减少读盘和参数负担的选择，不是把传输保护说成存储校验。源文件仍可能被其他进程修改，预览不是文件系统快照。[sftp 的落盘选项][sftp]、[rsync 的更新与同步选项][rsync]

**跳过与同步。** rsync 默认 quick check 使用大小与修改时间，不代表传输过程完全不使用校验；`--ignore-existing` 则跳过已有文件而不证明内容相同。rclone 也提供 `--ignore-existing`，常规 `copy` 的比较会结合后端支持的大小、时间或摘要。ShellPier 采用简单的 `--skip-existing`：已有文件就跳过，目录仍继续遍历。rclone 的 `sync` 可能删除目标额外内容；ShellPier 不加入隐式镜像删除。[rsync][rsync]、[rclone copy][rclone-copy]、[rclone flags][rclone-flags]、[rclone sync][rclone-sync]

**终端输出与审计。** ShellPier 的有界输出快照和可选本机 terminal 日志便于继续工作，不是完整操作审计。Tailscale 的会话录制记录终端输出；Teleport 将审计事件与会话录制分别处理。终端画面也不等于所有实际执行的命令，且可能包含敏感数据。[Tailscale 录制][tailscale-recording]、[Teleport 录制][teleport-recording]、[Teleport 审计事件][teleport-audit]

## 暂不照搬的方向

- **UDP 漫游与预测回显：** Mosh 为交互屏幕选择 UDP 和状态同步。ShellPier 需要代理可达的 WSS、完整命令结果和文件传输，本轮不增加另一套传输与状态恢复协议。[Mosh][mosh]
- **多人共享终端：** tmate 的只读入口、Teleport 的 observer/peer/moderator 涉及参与者身份与加入权限。ShellPier 当前没有这套共享授权，不把多次读取同一 terminal 当成多人协作。[tmate][tmate]、[Teleport 会话加入][teleport-join]
- **完整 SSO、CA 与 RBAC：** Tailscale 和 Teleport 依赖组织身份及策略基础设施。ShellPier 保持票据、公钥固定、到期和撤销；共享凭据不能归因到不同的人，不宣称人级审计。[Tailscale SSH][tailscale]、[Teleport 认证][teleport-auth]
- **操作审计日志与集中录制：** 需要另行定义事件覆盖、保留、敏感信息处理及记录失败时的行为；本轮不实现，也不以已有输出日志替代。[Tailscale 录制][tailscale-recording]、[Teleport 审计][teleport-audit]
- **公网监听与通用网络入口：** 本地和反向监听保留 loopback，不增加 GatewayPorts 策略矩阵或组织 VPN。
- **镜像删除、完整元数据复制和云存储后端：** 这些属于 rsync/rclone 的主要使用场景；需要时优先使用对应工具并检查服务端依赖，不以几个同名 flag 宣称兼容。

以上是当前取舍，不是后续功能承诺。[路线图](roadmap.md)单独记录已实现能力与尚待验证的范围。

## 其他连接路线

[Chisel][chisel] 支持单连接多个端口、HTTP/WebSocket 传输和反向转发，值得吸收的是明确端口即可建立连接的入口；ShellPier 复用已有 SSH 协议，已有 SOCKS5 CONNECT，本轮增加同连接多本地固定监听。需要长期维护多服务暴露时，可选择 [frp](https://gofrp.org/en/docs/)；它的服务端/客户端配置与公网端口管理不应成为临时连接的前置条件。

[sshuttle 的系统要求][sshuttle]包括本地防火墙操作所需权限和远端 Python。它适合将网段流量透明地送入 SSH，代价是本机网络配置与运行环境要求；ShellPier 只吸收按需连通的思路，不引入透明路由、DNS 接管或远端 Python 依赖。

已有组织设施也可考察 [Cloudflare Tunnel SSH](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-cloudflared-authentication/)。这些隧道仍需 SSH endpoint，并各有认证、TLS 和运营条件，不会自动提供目标账户登录。

旧 Windows 是独立路线：系统 OpenSSH 可选组件的支持范围见 [Microsoft 文档](https://learn.microsoft.com/en-us/windows-server/administration/openssh/openssh-overview)；Bitvise 与 Plink 的固定版本、许可、签名及安装要求见 [Legacy 指南](legacy-windows.md)。现代路线不要求开启系统 sshd 或新建系统账户。平台与权限边界分别见[兼容性说明](compatibility.md)和[安全策略](../SECURITY.md)。

[Magic Wormhole][wormhole] 用 PAKE 建立短码配对，适合临时文件交接，但引入另一套配对与中继基础设施。ShellPier 继续使用已有私有票据与固定主机公钥，不将短码作为新的认证路线。

## 一手资料

以下资料按本次调研日期读取；在线手册和主分支文档会变化，不能据此推断用户已安装版本的行为。

1. [OpenSSH：ssh_config][ssh-config]，连接超时、保活、代理与复用。
2. [OpenSSH：sftp][sftp]，批处理、续传、属性与 fsync 扩展。
3. [OpenSSH：scp][scp]，SFTP 与旧 SCP 模式。
4. [rsync 手册][rsync]，差量、quick check、远端依赖与同步选项。
5. [rclone copy][rclone-copy]，复制与目标保留。
6. [rclone sync][rclone-sync]，同步与删除语义。
7. [rclone SFTP][rclone-sftp]，远端 shell 与摘要能力。
8. [croc 项目说明][croc]，PAKE、文件交接与续传。
9. [Mosh 协议与使用说明][mosh]，UDP、漫游与屏幕状态同步。
10. [autossh 官方 README][autossh]，监测、重启和退避。
11. [tmate 官方发布说明][tmate]，只读/可写会话与公钥限制。
12. [Tailscale SSH][tailscale]，身份、策略与 check 模式。
13. [SSH over Tailscale][tailscale-ssh]，普通 SSH 与 Tailscale SSH 的区别。
14. [Tailscale SSH 会话录制][tailscale-recording]，输出、敏感信息与录制失败策略。
15. [Teleport 认证架构][teleport-auth]，CA、身份与短期证书。
16. [Teleport 角色策略][teleport-roles]，会话 TTL、过期处理与权限。
17. [Teleport 会话录制架构][teleport-recording]，录制位置与信任边界。
18. [Teleport 审计事件][teleport-audit]，事件与录制数据。
19. [Teleport 会话加入][teleport-join]，参与角色与授权。
20. [OpenSSH：ssh][ssh]，登录认证、交互控制和转发类型。
21. [OpenSSH：sshd_config][sshd-config]，服务端权限、目录隔离和闲置期限。
22. [OpenSSH：sshd][sshd]，authorized_keys 的逐密钥限制。
23. [Go os.Root][go-root]，目录访问边界与平台差异。
24. [rclone flags][rclone-flags]，跳过已有文件等传输选项。
25. [Chisel 项目说明][chisel]，多端口、代理和反向隧道。
26. [sshuttle 系统要求][sshuttle]，本地权限与远端依赖。

[ssh-config]: https://man.openbsd.org/ssh_config
[ssh]: https://man.openbsd.org/ssh.1
[sshd-config]: https://man.openbsd.org/sshd_config.5
[sshd]: https://man.openbsd.org/sshd.8
[sftp]: https://man.openbsd.org/sftp
[scp]: https://man.openbsd.org/scp
[rsync]: https://download.samba.org/pub/rsync/rsync.1
[rclone-copy]: https://rclone.org/commands/rclone_copy/
[rclone-sync]: https://rclone.org/commands/rclone_sync/
[rclone-sftp]: https://rclone.org/sftp/
[croc]: https://github.com/schollz/croc
[mosh]: https://mosh.org/
[autossh]: https://www.harding.motd.ca/autossh/README.txt
[tmate]: https://github.com/tmate-io/tmate/releases
[tailscale]: https://tailscale.com/kb/1193/tailscale-ssh
[tailscale-ssh]: https://tailscale.com/docs/reference/ssh-over-tailscale
[tailscale-recording]: https://tailscale.com/kb/1246/tailscale-ssh-session-recording/
[teleport-auth]: https://goteleport.com/docs/reference/architecture/authentication/
[teleport-roles]: https://goteleport.com/docs/reference/access-controls/roles/
[teleport-recording]: https://goteleport.com/docs/reference/architecture/session-recording/
[teleport-audit]: https://goteleport.com/docs/reference/deployment/monitoring/audit/
[teleport-join]: https://goteleport.com/docs/zero-trust-access/access-controls/guides/joining-sessions/

[go-root]: https://pkg.go.dev/os#Root
[rclone-flags]: https://rclone.org/flags/
[chisel]: https://github.com/jpillora/chisel
[sshuttle]: https://sshuttle.readthedocs.io/en/stable/requirements.html

[rclone-rcat]: https://rclone.org/commands/rclone_rcat/
[rclone-cat]: https://rclone.org/commands/rclone_cat/
[tmux]: https://github.com/tmux/tmux/wiki/Getting-Started#attaching-and-detaching
[wormhole]: https://magic-wormhole.readthedocs.io/en/latest/welcome.html

[openssh-92]: https://www.openssh.org/txt/release-9.2
[openssh-protocol]: https://github.com/openssh/openssh-portable/blob/master/PROTOCOL
[rclone-filtering]: https://rclone.org/filtering/
[pkg-sftp-server]: https://github.com/pkg/sftp/blob/v1.13.11/request-server.go
[tmate-readme]: https://github.com/tmate-io/tmate
[conpty]: https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session
