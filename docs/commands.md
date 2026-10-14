# 命令参考

连接步骤见[工作流](workflows.md)，本页按需查阅。下文 `shellpier` 假设程序已加入 PATH；否则 macOS/Linux 用 `./shellpier`，PowerShell 用 `.\shellpier.exe`，CMD 用 `shellpier.exe`。

PowerShell 的票据参数必须加引号，例如 `'@access.json'`。`@file` 表示从本地文件读取凭据，避免把长票据写进命令历史。参数名和默认值可用 `shellpier <命令> --help` 查看。

## 常用命令

| 命令 | 用途 |
|---|---|
| `share --ttl 15m --output access.json` | 目标端启动会话，把控制凭据写入新文件 |
| `shell @access.json` | 打开交互终端 |
| `exec @access.json --command whoami --json` | 执行命令并捕获结果 |
| `connect @access.json -- whoami` | 兼容入口；未给命令时执行 `whoami` |
| `schema --json` | 本地发现命令、参数与输入输出契约 |
| `capabilities @access.json --json` | 查询已认证目标的 OS、shell、PTY 等能力 |
| `terminal open @access.json --out terminal-session --json` | 为 Agent 创建可跨调用控制的终端 |
| `files list @access.json . --json` | 用 SFTP 操作远端文件 |
| `sessions --json` | 列出当前用户本地分享会话 |
| `invite --relay URL --token-file relay.key --ttl 30m --out invitation` | 控制端生成邀请目录，保留私钥 |
| `host @host.ticket` | 目标端用邀请票据启动前台会话 |
| `ssh-config @access.json --out ssh-session` | 生成独立 OpenSSH 配置目录 |
| `doctor --relay URL --json` | 查看系统、网卡及中继健康检查结果 |
| `status --state PATH` | 查看指定本机会话状态 |
| `stop --state PATH` | 停止本机会话，并撤销其中继访问 |
| `revoke @host.ticket` | 用目标端票据远程撤销中继会话 |
| `cleanup --dir invitation` | 删除生成目录中清单所属的凭据文件 |
| `relay --token-file relay.key` | 运行自托管中继 |
| `inspect @access.json --json` | 本地校验票据，输出不含凭据的摘要 |
| `check @access.json --json` | 实际建立 SSH，检查目标身份与可执行性 |
| `upload @access.json local.txt remote.txt` | 上传单个文件，拒绝覆盖 |
| `download @access.json remote.txt local.txt` | 下载单个文件，拒绝覆盖 |
| `keygen --out legacy-keys --json` | 新建 Ed25519 SSH 密钥对，无需系统 ssh-keygen |
| `completion bash` | 输出补全脚本，支持 Bash/Zsh/Fish/PowerShell 7+ |
| `version --json` | 查看版本及 OS/架构/Go/VCS 构建信息 |

`proxy @access.json` 是生成的 OpenSSH `ProxyCommand` 使用的传输入口，stdin/stdout 是协议字节，不适合直接在终端观察。

## 目标端：share / host

`share` 默认直接模式，监听 `0.0.0.0:0`（自动选端口）、有效期 3 小时，权限属于当前运行账户。

| 参数 | 行为 |
|---|---|
| `--relay URL` | 改用中继；可由 `SHELLPIER_RELAY_URL` 提供默认值 |
| `--token-file PATH` | 创建中继会话所需的密钥文件；也可用 `SHELLPIER_RELAY_KEY` |
| `--ttl 15m` | 授权期限，正数至少 1 秒；`0` 明确请求长期会话 |
| `--listen 127.0.0.1:0` | 直接模式的实际监听地址；中继模式忽略 |
| `--interface NAME` | 直接模式只公布该网卡候选地址，不改变实际监听地址 |
| `--output access.json` | 把控制端凭据写入新文件，拒绝覆盖已有文件 |
| `--json` | 输出机器可读事件；与 `--output` 搭配时只输出会话元数据 |
| `--shell auto` | 可选 `auto`、`powershell`、`cmd`、`sh` |
| `--work-dir PATH` | 设置命令起始目录，不构成文件系统沙箱 |
| `--keep-awake` | 自愿启用系统防睡眠机制，保持前台进程 |
| `--reconnect-min 1s` / `--reconnect-max 30s` | share/host 的中继重连等待范围 |

`host` 从票据继承中继和有效期，支持 `--json`、`--shell`、`--work-dir`、`--keep-awake` 和上述重连参数。例如：

```sh
shellpier host @host.ticket --shell sh --work-dir /home/you/work
```

`share` 不指定 `--output` 时会把完整访问票据写到 stdout，包括 JSON 输出；不要把它接入公共日志。`host --json` 不输出控制端私钥，但状态文件本身仍须私下保管。

## 控制端：connect / ssh-config / invite

```sh
shellpier connect @access.json -- hostname
shellpier ssh-config @access.json --out ./ssh-session
ssh -F ./ssh-session/ssh_config shellpier hostname
sftp -F ./ssh-session/ssh_config shellpier
```

生成配置使用固定别名 `shellpier`，通过 `-F` 选择，不改全局 SSH 配置。输出目录必须是新目录，包含 `access.json`、`identity`、`known_hosts`、`ssh_config`、`shellpier-manifest.json`；`invite` 还生成 `host.ticket`，只把这一文件交给目标端。

`connect --interface NAME @access.json -- whoami` 在 macOS/Linux 约束直接 TCP 连接的出站网卡；Windows 暂不支持这个参数的接口约束。它不适用于中继模式。中继通过获准的 HTTP CONNECT 代理出站时可配置 `HTTPS_PROXY`，但不自动处理 PAC、NTLM/Kerberos 或浏览器登录态。

本地参数可与票据混排；远端命令放在 `--` 后，例如 `connect @access.json --timeout 30s -- whoami`。位置参数会拼接成目标 shell 命令，复杂命令优先用 `--command` 精确传递字符串，或用 `--script` 读取本地 UTF-8 脚本。失去响应不代表未执行，重试前检查目标端结果。

`connect`、`exec`、`check`、`upload`、`download`、`files` 等建连命令支持 `--retries 2`（默认最多 3 次建连，`0` 关闭重试），以及 `--retry-min 200ms` / `--retry-max 3s`（等待上限不得超过 1 分钟）。这些参数只控制 SSH 建连前的暂时网络错误；命令执行或传输开始后不自动重放。中继断线重连机制见[工作流](workflows.md#长期运行与断线)。

## 按需诊断、文件传输与密钥

```sh
shellpier inspect @access.json --json
shellpier check @access.json --timeout 20s --json
shellpier upload @access.json ./local.txt remote.txt --timeout 5m --json
shellpier download @access.json remote.txt ./received.txt --timeout 5m --json
shellpier keygen --out ./legacy-keys --json
```

这些检查按需使用。`inspect` 仅检查本地票据，不连接目标；`check` 会建立 SSH 并运行身份检查。`check` 默认超时 20 秒，接受 1 秒至 5 分钟。传输默认超时 5 分钟，支持 `--interface NAME` 与 `--json`，只传单个普通文件，不覆盖目标。上传失败可能留下远端部分文件，先核对再处理；下载失败只清理本次创建的本地部分文件。Windows 远端绝对路径写 `/C:/Users/you/file.txt`。

`keygen` 的父目录须存在，输出目录须不存在。它生成私有 `identity` 与 `identity.pub`，stdout 只输出路径和 SHA256 指纹；Legacy 配置使用公钥，私钥留在控制端。未提供 RSA 或密码加密选项。

`completion bash|zsh|fish|powershell` 只打印脚本，不修改 shell profile。检查脚本后按相应 shell 方式显式加载；PowerShell 补全要求 7+。

## 交互终端与命令执行

```sh
shellpier shell @access.json
shellpier exec @access.json --command 'whoami' --timeout 30s --no-stdin --json
shellpier exec @access.json --script ./task.sh --cwd /home/you/work --env MODE=test --json
```

`shell` 使用当前终端输入，自动恢复本地终端设置并传递尺寸变化；`--command` 可在 PTY 中运行指定命令。`exec` 保持 stdout/stderr 分离，默认转发 stdin，`--no-stdin` 发送 EOF。`--timeout 0` 使用票据有效期；`--cwd` 指定本次目录，`--env NAME=VALUE` 可重复。目标能力以 `capabilities` 为准。

`exec --json` 每路默认捕获 1 MiB，`--max-output` 可调至 16 MiB；超出的输出继续排空并标记 `stdout_truncated` / `stderr_truncated`。按 `stdout_encoding` / `stderr_encoding` 读取 UTF-8 或 base64 数据，并检查 `ok`、`exit_code`。PTY 合并 stdout/stderr，包含回显和终端控制序列，不适合当作结构化命令结果。

## Agent 发现与持久终端

```sh
shellpier schema --json
shellpier capabilities @access.json --json
shellpier terminal open @access.json --out ./terminal-session --json
shellpier terminal read --state ./terminal-session/state.json --cursor 0 --wait 5s
shellpier terminal write --state ./terminal-session/state.json --file ./input.txt --json
shellpier terminal resize --state ./terminal-session/state.json --cols 120 --rows 40 --json
shellpier terminal signal --state ./terminal-session/state.json --signal INT --json
shellpier terminal close --state ./terminal-session/state.json --json
```

`schema` 离线列出命令契约，`schema files --json` 可只查一个命令；`capabilities` 通过 SSH 查询目标，不能用本机 OS 推断目标能力。JSON 错误写入 stderr，程序退出码仍需检查；原始终端与传输字节不包装成普通 JSON 文本。

`terminal open` 启动本地控制进程，输出私有状态文件路径；默认终端有效期 3 小时，受票据到期时间限制。支持 `--command`、`--cwd`、重复 `--env`、`--cols`、`--rows` 与 `--term`。状态文件包含控制 secret，不交给目标端或贴入日志。

`read` 始终返回 JSON，`data` 为 base64，使用返回的 `next_cursor` 继续读取。默认每次 64 KiB，`--limit` 最大 1 MiB，`--wait` 最多 30 秒；缓冲区只保留最新 1 MiB，`dropped` 表示请求位置已丢失的字节数。`closed` 与 `exit_code` 描述结束状态。关闭或到期后保留有界私有输出快照，可继续 read/status；结束后自行删除本次终端目录。

`write` 使用 `--text` 或 `--file`，二选一；`--file -` 读取 stdin，单次最多 1 MiB。输入按原始字节发送，执行一行命令时需在文件或文本中包含换行。`status` 查看终端元数据，`close` 只关闭该终端，不撤销分享会话；断线不会自动重放输入或命令。

## 远端文件操作

```sh
shellpier files list @access.json . --limit 100 --json
shellpier files stat @access.json remote.txt --json
shellpier files read @access.json remote.txt --offset 0 --limit 4096 --json
shellpier files mkdir @access.json work/subdir --parents --dry-run --json
shellpier files rename @access.json old.txt new.txt --dry-run --json
shellpier files remove @access.json empty-dir --dry-run --json
```

`list` 默认返回最多 1000 项，最大 10000，`has_more` 表示结果被截断；这是返回条目上限，不是分页或网络/内存读取上限。`stat` 使用 lstat，返回路径、名称、大小、mode、修改时间、目录与符号链接标志。

`read` 默认最多 1 MiB，最大 16 MiB；不带 JSON 输出原始字节，JSON 使用 base64 并返回 `has_more`。`mkdir` 默认单层，`--parents` 才创建父目录。`rename` 先检查目标不存在，但 SFTP v3 的检查与重命名不是原子操作，并发创建目标仍有竞态。`remove` 仅删除文件、链接或空目录，不递归。修改动作支持 `--dry-run`，不执行写入；写入开始后不自动重试。

## 生命周期与中继参数

`share` 和 `host` 打印私有 `state.json` 路径。`sessions --json` 可发现当前用户本机会话；`status` / `stop` 使用对应 `--state FILE`，通过本机回环控制端点操作；停止后状态文件被删除，后续 `status` 失败不一定是故障。

`cleanup` 仅清理 `ssh-config` / `invite` 的生成目录，保留无关文件，不撤销会话，也不删除目录外单独生成的 `access.json`。结束访问使用 stop/revoke，再清理凭据。`revoke --timeout 10s @host.ticket` 可设置撤销超时（默认 10 秒，范围 1 秒至 5 分钟）。

`--ttl 0` 仍是前台进程，不安装开机服务。直接模式允许长期会话；中继同时要求运营者启用 `relay --allow-persistent`。中继重启会丢失会话。

中继命令可用 `--init-key NEW-FILE` 生成创建密钥后退出；`--listen` 默认 `127.0.0.1:8080`，`--max-sessions` 默认 64，`--max-ttl` 默认 24 小时。原生 HTTPS 需要同时传入 `--tls-cert CERT.pem --tls-key KEY.pem`；否则放在 TLS 反向代理后。完整操作见[中继部署](relay.md)。
