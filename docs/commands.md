# 命令参考

连接步骤见[工作流](workflows.md)，本页按需查阅。下文 `shellpier` 假设程序已加入 PATH；否则 macOS/Linux 用 `./shellpier`，PowerShell 用 `.\shellpier.exe`，CMD 用 `shellpier.exe`。

PowerShell 的票据参数必须加引号，例如 `'@access.json'`。`@file` 表示从本地文件读取凭据，避免把长票据写进命令历史。参数名和默认值可用 `shellpier <命令> --help` 查看。

先用这三条完成分享、连接和传文件（目标端运行第一条，控制端使用收到的票据）：

```sh
shellpier share --output access.json
shellpier shell @access.json
shellpier upload @access.json ./local.txt remote.txt
```

只分享一个目录时，将第一条换成 `shellpier share --root ./shared --output access.json`，控制端直接用 upload/download 或 SFTP；不必额外配置访问模式。

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
| `check @access.json --json` | 实际建立 SSH；完整模式同时检查目标账户身份 |
| `upload @access.json local.txt remote.txt` | 上传文件或显式递归目录，拒绝覆盖 |
| `download @access.json remote.txt local.txt` | 下载文件或显式递归目录，拒绝覆盖 |
| `keygen --out legacy-keys --json` | 新建 Ed25519 SSH 密钥对，无需系统 ssh-keygen |
| `completion bash` | 输出补全脚本，支持 Bash/Zsh/Fish/PowerShell 7+ |
| `version --json` | 查看版本及 OS/架构/Go/VCS 构建信息 |

`proxy @access.json` 是生成的 OpenSSH `ProxyCommand` 使用的传输入口。加 `--to HOST:PORT` 可经目标端连接指定 TCP 服务，类似 OpenSSH `ssh -W`；不加 `--to` 时保持原有 SSH 传输入口行为。两种形式的 stdin/stdout 都是原始协议字节，不输出 JSON，不适合直接在终端观察。

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
| `--root DIR` | 只分享此目录，自动选择 SFTP；远端路径从 `/` 开始 |
| `--access-mode full` | 默认完整功能；`sftp` 仅文件操作，`sftp-readonly` 仅只读文件操作 |
| `--reconnect-min 1s` / `--reconnect-max 30s` | share/host 的中继重连等待范围 |

`host` 从票据继承中继和有效期，支持 `--json`、`--shell`、`--work-dir`、`--keep-awake` 和上述重连参数。例如：

```sh
shellpier host @host.ticket --shell sh --work-dir /home/you/work
```

`share`、`host` 与 `service plan/install` 都支持 `--access-mode full|sftp|sftp-readonly`。非 `full` 模式拒绝 exec、shell、PTY、jobs 和端口转发；不要同时显式开启 jobs 或配置转发。完整模式默认可用 jobs 与 TCP 转发。`--root DIR` 自动选择 SFTP 专用模式，远端路径从虚拟 `/` 开始；加 `--access-mode sftp-readonly` 可只读分享。未设 root 的只读模式仍可读取当前账户有权访问的路径；`--work-dir` 只是起始目录。通过 `capabilities` 的 `access_mode` 与 `sftp_read_only` 查看目标策略。

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

### 建连期限与 SSH 保活

采用通用建连参数的命令还支持以下选项，包括 `terminal open`；终端 worker 会保存这些设置。

| 参数 | 默认与含义 |
|---|---|
| `--connect-timeout 10s` | 每个候选连接的建立期限，正数且最多 5 分钟；不是命令执行期限 |
| `--keepalive-interval 15s` | SSH 探测间隔，`0` 关闭，最大 1 小时 |
| `--keepalive-timeout 45s` | 单次已发送探测的响应期限；启用探测时须为正数且最多 1 小时 |

目标对探测答复 `false` 仍表示连接存活；只有未答复或连接错误才关闭传输。保活超时后远端结果可能未知，不自动重新执行命令、发送终端输入或重启传输。

### OpenSSH 配置中的保活与连接复用

```sh
shellpier ssh-config @access.json --out ./ssh-session --server-alive-interval 15s --server-alive-count-max 3 --control-persist 1m
ssh -F ./ssh-session/ssh_config shellpier hostname
ssh -F ./ssh-session/ssh_config -O exit shellpier
shellpier cleanup --dir ./ssh-session
```

`ssh-config` 默认写入 `ServerAliveInterval 15`、`ServerAliveCountMax 3`；间隔用整秒 duration，`0` 关闭。`--control-persist` 默认 `0` 关闭，仅 Unix 可显式设置 1 秒至 1 小时的整秒 duration，socket 位于该票据的私有目录。复用依赖控制端 OpenSSH，不是内置客户端的重连功能。先用对应配置 `-O exit` 关闭 master，再 cleanup；cleanup 不撤销访问，也不会终止身份不确定的 master。

## 按需诊断、文件传输与密钥

```sh
shellpier inspect @access.json --json
shellpier check @access.json --timeout 20s --json
shellpier upload @access.json ./local.txt remote.txt --timeout 5m --json
shellpier download @access.json remote.txt ./received.txt --timeout 5m --json
shellpier keygen --out ./legacy-keys --json
```

这些检查按需使用。`inspect` 仅检查本地票据，不连接目标；`check` 会建立 SSH；完整模式运行身份检查，受限模式不执行 hostname/whoami，JSON 通过 `identity_verified` 与 `access_mode` 区分。受限模式的 `ok=true` 表示 SSH 已认证，不表示执行身份命令或实际探测 SFTP。`check` 默认超时 20 秒，接受 1 秒至 5 分钟。传输默认超时 5 分钟，支持 `--interface NAME` 与 `--json`，默认传单个普通文件，不覆盖目标；目录与续传参数见下表。上传失败可能留下远端部分文件，先核对再处理；下载失败只清理本次创建的本地部分文件。Windows 远端绝对路径写 `/C:/Users/you/file.txt`。

`keygen` 的父目录须存在，输出目录须不存在。它生成私有 `identity` 与 `identity.pub`，stdout 只输出路径和 SHA256 指纹；Legacy 配置使用公钥，私钥留在控制端。未提供 RSA 或密码加密选项。

`completion bash|zsh|fish|powershell` 只打印脚本，不修改 shell profile。检查脚本后按相应 shell 方式显式加载；PowerShell 补全要求 7+。

## 交互终端与命令执行

```sh
shellpier shell @access.json
shellpier exec @access.json --command 'whoami' --timeout 30s --no-stdin --json
shellpier exec @access.json --script ./task.sh --cwd /home/you/work --env MODE=test --json
```

`shell` 使用当前终端输入，自动恢复本地终端设置并传递尺寸变化；`--command` 可在 PTY 中运行指定命令。`exec` 保持 stdout/stderr 分离，默认转发 stdin，`--no-stdin` 发送 EOF。`--timeout 0` 使用票据有效期；`--cwd` 指定本次目录，`--env NAME=VALUE` 可重复。目标能力以 `capabilities` 为准。

`exec --json` 每路默认捕获 1 MiB，`--max-output` 可调至 16 MiB；超出的输出继续排空并标记 `stdout_truncated` / `stderr_truncated`。按 `stdout_encoding` / `stderr_encoding` 读取 UTF-8 或 base64 数据，并检查 `ok`、`exit_code`、`exit_status_known` 和 `termination_reason`。只有 `exit_status_known=true` 才有确定的远端退出状态；未知时保留兼容值 `exit_code=-1`，不能当作远端命令失败。结束原因包括 `exited`、`canceled`、`expired`、`connection_lost`。PTY 合并 stdout/stderr，包含回显和终端控制序列，不适合当作结构化命令结果。

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

`read` 始终返回 JSON，`data` 为 base64，使用返回的 `next_cursor` 继续读取。默认每次 64 KiB，`--limit` 最大 1 MiB，`--wait` 最多 30 秒；缓冲区只保留最新 1 MiB，`dropped` 表示请求位置已丢失的字节数。`closed` 描述整个终端是否结束；`exit_status_known` 与 `termination_reason` 区分已知退出、取消、到期与连接丢失，不推断 shell 内某条命令完成。关闭或到期后保留有界私有输出快照，可继续 read/status；结束后自行删除本次终端目录。

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

## 已保存目标与分阶段诊断

```sh
shellpier targets add lab --ticket ./access.json --json
shellpier targets list --json
shellpier targets show lab --json
shellpier doctor --target target:lab --timeout 20s --json
shellpier exec target:lab --command whoami --json
shellpier targets remove lab --json
```

`targets` 保存私有 access 或 host 票据文件的引用，不复制票据；使用命令仍会校验所需角色。移除名称不删除凭据，也不撤销访问。默认配置目录可通过 `SHELLPIER_CONFIG_DIR` 或 `--config-dir` 指定。远端命令接受 `target:NAME`；引用失效时更新票据与名称。`doctor --target` 按阶段报告票据、网络/SSH 与能力检查；受限模式身份阶段显示 `not_available_in_access_mode`，不作为整体诊断失败，也不声称已验证命令身份或实际探测 SFTP。它支持 `--interface`、`--retries`、`--retry-min`、`--retry-max`；本机网卡信息不代表目标可达。

## 目录、续传与传输进度

| 参数 | 边界 |
|---|---|
| `--recursive` | 目录递归，拒绝符号链接与已有目标文件 |
| `--exclude '*.tmp'` | 可重复，相对路径或 basename glob；不支持 `**` |
| `--max-files 10000` | 递归文件数上限，可设 1..100000 |
| `--max-depth 32` | 递归深度上限，可设 0..128 |
| `--resume --resume-state ./transfer.json` | 单文件专用，使用私有本地 journal 与本次部分文件；不与递归合用 |
| `--progress jsonl` | 进度事件写 stderr；`--json` 最终摘要写 stdout |
| `--dry-run` | 只读预检并输出计划，不创建目标或续传状态；不能与 resume 合用 |
| `--preserve-times` | 新建文件和目录保留秒级 mtime；不保留原 atime、权限或所有权 |
| `--skip-existing` | 允许已有目录，直接跳过已有普通文件，不比较内容；不能与 resume 合用 |
| `--bwlimit 1048576` | 整次操作的字节/秒预算，默认 `0` 不限速；复制与续传内部校验共享预算，不含 metadata/SSH framing |


```sh
shellpier upload target:lab ./build remote-build --recursive --exclude '*.tmp' --progress jsonl --json
shellpier download target:lab remote.bin ./received.bin --resume --resume-state ./transfer.json --json
shellpier upload target:lab ./build remote-build --recursive --dry-run --skip-existing --json
shellpier upload target:lab ./build remote-build --recursive --skip-existing --preserve-times --bwlimit 1048576 --json
```

普通传输不要求手工校验摘要；续传所需的内容绑定由程序内部处理。续传只复用匹配本次传输身份的部分文件，不覆盖已完成目标；它不是任意远端文件的追加入口。网络中断后显式再次运行同一续传命令，不自动重放写入。

## 用管道传输文件

```sh
# macOS/Linux：把生成结果直接上传为新文件
printf 'hello\n' | shellpier upload @access.json - hello.txt
# 下载的 stdout 只有原始文件字节
shellpier download @access.json hello.txt - > received.txt
```

上传的本地路径或下载的本地目标写 `-`，分别读取 stdin 或写入 stdout；无需先在控制端创建中间文件。上传仍拒绝覆盖远端已有文件，失败可能留下部分远端文件。下载到 stdout 只接受普通文件，不跟随符号链接；管道的接收程序自行处理已收到的部分数据。

流传输支持期限、`--bwlimit` 与写往 stderr 的 `--progress jsonl`，不能与 `--recursive`、`--resume`、`--resume-state`、`--preserve-times`、`--dry-run` 或 `--skip-existing` 合用。上传可用 `--json` 输出最终摘要；下载到 `-` 不接受 `--json`，也不输出传输摘要，stdout 只有原始文件字节。SSH 保护传输，普通流复制不额外计算 SHA。PowerShell 的文本管道在不同版本可能改变编码；二进制流请使用保持原始字节的调用方式，例如 CMD 的文件重定向，或直接下载到文件。

## 目标端长任务：jobs

完整模式默认支持 jobs，普通 `share --output access.json` 或 `host @host.ticket` 即可；需要关闭时设置 `--allow-jobs=false`。

```sh
shellpier jobs start target:lab --command 'make test' --operation-id build-001 --json
shellpier jobs operation target:lab build-001 --json
shellpier jobs list target:lab --json
shellpier jobs status target:lab JOB_ID --json
shellpier jobs logs target:lab JOB_ID --stdout-cursor 0 --stderr-cursor 0 --json
shellpier jobs wait target:lab JOB_ID --timeout 30m --json
shellpier jobs cancel target:lab JOB_ID --json
```

`start` 支持 `--script`、`--cwd`、重复 `--env`；默认任务期限 3 小时，仍受目标分享期限约束。`wait` 默认最多 3 小时，其余查询默认 20 秒。目标 host 默认保留 128 个任务记录、同时运行 4 个任务；每个任务 stdout/stderr 各保留最新 1 MiB，日志通过各自绝对 cursor 读取。host 的 `--max-jobs`、`--max-running-jobs`、`--job-log-limit` 可分别调整为最多 1024、64、16 MiB，具体限制见 schema。

SSH 断开后任务继续，目标 host 停止或到期会取消任务；进程重启不恢复任务。提交响应丢失时，用 `jobs operation` 按原 operation ID 只读查询，取得 job ID 后读取状态和日志。同一 host 内保留完成记录，满表拒绝新增；相同 ID、相同内容的 start 返回原任务，不同内容拒绝。此去重不跨 host 重启，也不回滚 shell 副作用。

## TCP 转发、反向转发与 SOCKS5

```sh
# 本地端口连接到目标端的服务
shellpier forward @access.json --to 127.0.0.1:8080 --listen 127.0.0.1:0 --json
# 控制端建立 SOCKS5 代理，连接由目标端发起
shellpier forward @access.json --dynamic --listen 1080 --json
# 目标端的 8080 端口连接到控制端的 3000 服务
shellpier forward @access.json --reverse --listen 8080 --to 127.0.0.1:3000 --json
```

完整模式默认允许 TCP 转发，无需额外开关；转发连接与反向监听都有并发上限。监听仅限 loopback，端口 `0` 自动分配；`--timeout 0` 使用票据期限，停止进程关闭转发。需要精确限制正向目标时，在 share/host 设置可重复的 `--allow-forward HOST:PORT`。

`--dynamic` 支持 SOCKS5 的无认证 CONNECT，接受 IPv4、IPv6 与域名；域名交给目标端解析。客户端应选择远端解析的 SOCKS5 模式，例如 `curl --socks5-hostname 127.0.0.1:1080 https://example.com/`。不支持 SOCKS4、BIND 或 UDP；不能与 `--to`、`--reverse` 合用。它仍受目标端 full 模式、可选目的地址限制与并发容量约束。代理仅监听 loopback，但本机其他进程也可能连接，因此不要把它当成隔离的专用网络。

`proxy @access.json --to 127.0.0.1:8080` 提供单条 stdin/stdout TCP 字节桥，无需开本地监听端口；目的地址从目标端访问，与正向转发使用相同限制。它可用于自定义 `ProxyCommand`，但不自动登录下一跳，也不生成多跳票据。stdin 或远端先结束一个方向时，另一个方向可继续传输；两边结束后还会在有限时间内确认 SSH 连接仍有响应，失联或确认超时按失败返回，不重放字节。这仅确认传输状态，不证明目标服务已经提交业务操作。`--timeout 0` 使用票据期限，建连重试与保活参数只在带 `--to` 时可设置。

原生 OpenSSH 可使用 `-L`、`-D` 和标准 `-R`，例如 `ssh -F ./ssh-session/ssh_config -N -R 8080:127.0.0.1:3000 shellpier`。本地 Unix socket 入口也可使用 `-L /tmp/new-private-dir/service.sock:127.0.0.1:8080`；远端 Unix socket 监听未实现。

## Terminal 请求确认、等待与可选日志

```sh
shellpier terminal open target:lab --out ./terminal-session --log-limit 8388608 --json
shellpier terminal write --state ./terminal-session/state.json --file ./input.txt --operation-id input-001 --json
shellpier terminal operation --state ./terminal-session/state.json --operation-id input-001 --json
shellpier terminal wait --state ./terminal-session/state.json --wait 30s --json
```

`write --operation-id` 返回 `status`、`bytes_written` 与 `dedup_scope=worker_lifetime`：`completed` 只表示字节已交给 SSH，不能证明命令执行完成；`uncertain` 不应自动重发。活 worker 最多保留 256 个 ID（包括完成记录），满后拒绝新 ID；同 ID 不同内容拒绝，相同内容返回已有结果。worker 关闭或重启后不保证去重，失踪记录不代表输入未送达。

`wait` 默认等待 30 秒，范围 0..30 秒；超时返回 `closed=false`，仅等待整个终端结束。`--log-limit` 默认 0 关闭，最大 67108864；日志固定为新终端目录中的私有 `output.log`，不覆盖、不轮转、不恢复。达到上限或慢盘队列饱和时丢弃日志记录并设置 `log.truncated`，错误写入 `log.error`，SSH 输出继续。`log.complete` 表示已接受的队列完成写入与关闭，不表示没有截断。进程崩溃可能留下部分日志，默认内存环形缓冲仍为 1 MiB。

## MCP stdio 接入

```sh
shellpier mcp serve --timeout 1m --max-concurrent 4 --max-output 262144
```

通过 stdin/stdout 承载 MCP 协议，支持 `2026-07-28` 与 `2025-11-25`；不要向协议 stdout 混入普通日志。MCP 的上传/下载只接受普通本地路径，明确拒绝本地 `-`，以免读取或污染 JSON-RPC 协议流。工具按有界命令契约执行，远端操作必须使用显式 `@FILE` 或 `target:NAME`，不传内联凭据。2026 模式无状态、每个请求携带协议元数据；2025 模式执行初始化握手。`jobs start` 工具必须提供稳定 `operation-id`。单次工具默认最多 1 分钟（最大 5 分钟）、默认 4 个并发（1..16）、输出默认 256 KiB（1 KiB..1 MiB）。取消调用不回滚已经产生的副作用。


## 用户级服务：service

```sh
shellpier targets add host-lab --ticket ./invitation/host.ticket
shellpier service plan lab --ticket target:host-lab --json
shellpier service install lab --ticket target:host-lab --autostart --json
shellpier service start lab --json
shellpier service status lab --json
shellpier service stop lab --json
shellpier service uninstall lab --json
```

`plan` 只输出计划，`install` 创建用户级配置但不立即启动；`start` 才启动当前会话。`--autostart` 显式选择以后登录时启动，不指定则不设置登录自启。`plan/install` 还接受 `--shell`、`--work-dir`、`--keep-awake`、`--allow-jobs=false`、`--root`、`--access-mode`；所有动作支持 `--dry-run`、`--json` 和 `--config-dir`。

服务复用已有私有 host 票据，不创建或续期授权；有限票据仍会到期。start 校验安装时绑定的票据身份，换成另一会话 ID 后拒绝启动，需要显式卸载、重新安装。停止或卸载不删除票据；中继撤销未确认时用原 host 票据执行 `revoke`。这是当前用户的生命周期管理，不保证注销、睡眠或系统重启后的所有任务恢复。

MCP 输出被截断时检查 `truncated`，不要把截断文本当完整结果。取消或超时不证明远端没有执行；任务提交返回 `outcome=unknown` 时保留原 operation ID，查询状态或用同 ID、同内容确认原提交，不生成新 ID 重放。

### 文件目录边界

`--root` 使用 Go `os.Root` 限制 SFTP 路径，不是 shell 或操作系统沙箱。现有指向 root 内部的相对符号链接可读取，不支持创建链接；特殊文件被拒绝。root 不隔离目录中已有的硬链接别名或挂载点。需要命令执行时使用默认完整模式。
