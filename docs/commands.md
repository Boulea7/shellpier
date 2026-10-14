# 命令参考

先从[工作流](workflows.md)完成一次 `share → connect → stop`，再按需要查本页。下文 `shellpier` 假设程序已加入 PATH；否则 macOS/Linux 用 `./shellpier`，PowerShell 用 `.\shellpier.exe`，CMD 用 `shellpier.exe`。

PowerShell 的票据参数必须加引号，例如 `'@access.json'`。`@file` 表示从本地文件读取凭据，避免把长票据写进命令历史。参数名和默认值可用 `shellpier <命令> --help` 查看。

## 常用命令

| 命令 | 用途 |
|---|---|
| `share --ttl 15m --output access.json` | 目标端启动会话，把控制凭据写入新文件 |
| `connect @access.json -- whoami` | 控制端执行命令；未给命令时默认执行 `whoami` |
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

`share` 默认直接模式，监听 `0.0.0.0:0`（自动选端口）、有效期一小时，权限属于当前运行账户。

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

`host` 从票据继承中继和有效期，支持 `--json`、`--shell`、`--work-dir`、`--keep-awake`。例如：

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

命令参数会拼接为目标端 shell 命令，带空格或元字符时按**目标端** shell 规则处理。失去响应不代表未执行，重试前检查目标端结果。支持 SSH 命令执行和 SFTP，不提供 PTY、图形桌面或端口转发。

## 分步检查、文件传输与密钥

```sh
shellpier inspect @access.json --json
shellpier check @access.json --timeout 20s --json
shellpier upload @access.json ./local.txt remote.txt --timeout 5m --json
shellpier download @access.json remote.txt ./received.txt --timeout 5m --json
shellpier keygen --out ./legacy-keys --json
```

`inspect` 仅检查本地票据，不连接目标；`check` 会建立 SSH 并运行身份检查。`check` 默认超时 20 秒，接受 1 秒至 5 分钟。传输默认超时 5 分钟，支持 `--interface NAME` 与 `--json`，只传单个普通文件，不覆盖目标。上传失败可能留下远端部分文件，先核对再处理；下载失败只清理本次创建的本地部分文件。Windows 远端绝对路径写 `/C:/Users/you/file.txt`。

`keygen` 的父目录须存在，输出目录须不存在。它生成私有 `identity` 与 `identity.pub`，stdout 只输出路径和 SHA256 指纹；Legacy 配置使用公钥，私钥留在控制端。未提供 RSA 或密码加密选项。

`completion bash|zsh|fish|powershell` 只打印脚本，不修改 shell profile。检查脚本后按相应 shell 方式显式加载；PowerShell 补全要求 7+，不是 Legacy PowerShell 2.0 入口。

## 生命周期与中继参数

`share` 和 `host` 打印私有 `state.json` 路径。`status` / `stop` 必须使用对应文件，通过本机回环控制端点操作；停止后状态文件被删除，后续 `status` 失败不一定是故障。

`cleanup` 仅清理 `ssh-config` / `invite` 的生成目录，保留无关文件，不撤销会话，也不删除目录外单独生成的 `access.json`。先 stop/revoke，再用新连接验证拒绝，最后清理凭据。`revoke --timeout 10s @host.ticket` 可设置撤销超时（默认 10 秒，范围 1 秒至 5 分钟）。

`--ttl 0` 仍是前台进程，不安装开机服务。直接模式允许长期会话；中继同时要求运营者启用 `relay --allow-persistent`。中继重启会丢失会话。

中继命令可用 `--init-key NEW-FILE` 生成创建密钥后退出；`--listen` 默认 `127.0.0.1:8080`，`--max-sessions` 默认 64，`--max-ttl` 默认 24 小时。原生 HTTPS 需要同时传入 `--tls-cert CERT.pem --tls-key KEY.pem`；否则放在 TLS 反向代理后。完整操作见[中继部署](relay.md)。
