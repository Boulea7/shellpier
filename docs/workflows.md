# 从第一次连接到跨网络使用

可以直接按网络情况选择连接方式；单机演练和诊断仅在需要时使用。默认会话有效期为 3 小时，示例短 TTL 可按需调整。示例中的 `./shellpier` 用于 macOS/Linux；PowerShell 换为 `.\shellpier.exe`，并给所有 `@file` 参数加引号。

## 1. 单机演练：不需要外网或中继

在解压目录开两个终端。终端 A：

```sh
./shellpier share --relay= --listen 127.0.0.1:0 --ttl 15m --output access.json --json
```

保持终端 A 打开。终端 B：

```sh
./shellpier connect @access.json -- whoami
./shellpier connect @access.json -- hostname
```

PowerShell 的终端 B 写法：

```powershell
.\shellpier.exe connect '@access.json' -- whoami
.\shellpier.exe connect '@access.json' -- hostname
```

看到当前账户和机器名后，用终端 A 打印的**实际状态文件路径**停止：

```sh
./shellpier status --state /实际打印的路径/state.json
./shellpier stop --state /实际打印的路径/state.json
./shellpier connect @access.json -- whoami
```

最后一次连接应失败。停止会删除状态文件，所以再查 `status` 可能报告找不到文件。单独的 `access.json` 不会被 `cleanup` 删除，结束后自行删除这个凭据文件。再次演练前选择新的输出文件名或删除旧文件。

## 2. 两台机器：同网段时尝试直接连接

目标端：

```sh
./shellpier share --relay= --listen 0.0.0.0:0 --ttl 15m --output access.json --json
```

输出包括候选地址、期限、状态路径。把 `access.json` 私下复制到控制端；控制端先执行 `connect @access.json -- whoami`，需要交互时运行 `shell @access.json`。

连接失败或有多张网卡时，可按需从 `doctor` 查出名称：目标端 `share --interface NAME` 只筛选公布地址，`--listen` 仍控制实际监听；macOS/Linux 控制端可用 `connect --interface NAME @access.json -- whoami` 约束出站网卡。不要照抄别人的网卡名。Windows 控制端暂不支持接口约束。

校园访客网络的 AP 隔离、手机热点或 NAT 可能阻止直接连接。目标和控制端分处不同 NAT、均无法接受入站时，选择下一节的 WSS 路线。

## 3. 跨网络：控制端发邀请

先从团队或自托管中继运营者取得 HTTPS URL 和创建密钥文件。ShellPier 没有预置公共中继，`relay.example.com` 只是占位符。

控制端：

```sh
./shellpier invite --relay https://relay.example.com --token-file ./relay.key --ttl 30m --out ./invitation
```

只把 `invitation/host.ticket` 私下发送给目标端，控制端私钥留在邀请目录。目标端：

```sh
./shellpier host @host.ticket
```

控制端：

```sh
./shellpier connect @invitation/access.json -- hostname
./shellpier shell @invitation/access.json
ssh -F ./invitation/ssh_config shellpier hostname
sftp -F ./invitation/ssh_config shellpier
```

`ssh` / `sftp` 需要控制端另有 OpenSSH 客户端。内置 `connect` 不需要系统 SSH 客户端。双方通过出站 HTTPS/WSS 联系中继，SSH 字节在两端间加密。

结束后：

```sh
./shellpier revoke @invitation/host.ticket
./shellpier connect @invitation/access.json -- whoami
./shellpier cleanup --dir ./invitation
```

确认中间的新连接被拒绝，再清理凭据。若撤销未确认，保留票据用于重试。`cleanup` 保留目录中的无关文件，只清理清单所属凭据；它不能替代撤销。

## 人工操作与 Agent 自动化

有访问文件后，人工操作直接打开终端：

```sh
./shellpier shell @access.json
```

Agent 先发现命令，再查询目标能力，执行一次有界命令：

```sh
./shellpier schema --json
./shellpier capabilities @access.json --json
./shellpier exec @access.json --command 'whoami' --timeout 30s --no-stdin --json
```

需要连续终端上下文时使用 `terminal open → read → write`，结束后 close；读写、光标、尺寸和信号参数见[命令手册](commands.md#agent-发现与持久终端)。优先用 `exec --json` 获取独立 stdout/stderr 与退出码，PTY 输出包含回显和控制序列。跨调用的终端状态文件与输出快照留在控制端，私下保管。

文件管理用 `files`，上传下载仍用 `upload` / `download`；用 `sessions --json` 查找本机分享会话，再按返回的 state 路径执行 status/stop。关闭终端不会撤销目标分享，会话结束仍需 stop/revoke。

## Windows 目标与文件传输

目标端 PowerShell 示例：

```powershell
& 'C:\Users\you\Tools\ShellPier\shellpier.exe' host '@C:\Users\you\Downloads\Session Files\host.ticket' --shell powershell --work-dir 'C:\Users\you\工作目录'
```

CMD 的文件参数不需要 PowerShell 的单引号；路径有空格时使用双引号：

```bat
shellpier.exe host "@C:\Users\you\Downloads\Session Files\host.ticket" --shell cmd
```

SFTP 内使用正斜杠。相对路径最容易操作：`put local.txt remote.txt`。Windows 绝对路径写 `/C:/Users/you/remote.txt`，带前导 `/`，不是 `C:\Users\you\remote.txt`；有空格时按 SFTP 客户端规则加引号。

`--shell` 选择的是目标 shell，命令转义也跟随目标 shell。先验证简单命令，再上传已检查脚本或执行复杂命令。当前账户决定权限，`--work-dir` 不限制可访问的文件范围。

Windows 7/8 请使用[独立 Legacy 指南](legacy-windows.md)，支持范围见[兼容性说明](compatibility.md)。

## 长期运行与断线

需要长期前台会话时明确选择 `share --ttl 0` 或 `invite --ttl 0`；中继还必须配置 `--allow-persistent`。这不会安装开机服务，也不能跨中继重启恢复。

`--keep-awake` 自愿开启，需要平台机制正常工作，不保证锁屏、合盖、强制睡眠、注销或重启后持续连接。需要用户级后台管理时使用 `service plan/install/start`，安装与启动是两个独立动作。

`share`/`host` 中继网络中断后，在有效期内按有界指数退避与抖动重连，默认等待范围 1–30 秒，连接稳定 10 秒后复位。取消、到期、认证失败或撤销会立即停止。控制端仅在 SSH 建连前对暂时网络错误有限重试；已经开始的命令和传输不自动重放。参数见[命令手册](commands.md)。重试写文件、部署等操作前，先检查目标端副作用。stop/TTL 不回滚已产生的副作用。

## 反复使用同一目标

```sh
./shellpier targets add lab --ticket ./access.json
./shellpier doctor --target target:lab --json
./shellpier exec target:lab --command whoami --json
```

名称引用私有票据文件，不延长授权。`targets remove lab` 只移除名称，结束访问仍用 stop/revoke。

## 构建任务与终端上下文的选择

希望断开控制端 SSH 后任务仍继续时，目标端保持默认完整模式，用 `jobs start → status/logs/wait → cancel`。保存 start 返回的任务 ID；重连后用 ID 查询，不重新提交命令。任务留在目标 host 内存中，host 停止、到期或重启都会结束这条生命周期。

需要 shell 上下文时使用 `terminal`：它由控制端 worker 保持 SSH，支持连续输入；`terminal wait` 只等待整个终端关闭。输入响应丢失时，使用原 `--operation-id` 查询；不要从“没收到响应”推断未执行。去重只覆盖同一活 worker，最多 256 条 ID。

大目录传输用 `--recursive --exclude '*.tmp'`，大单文件中断恢复用 `--resume`；两者不混用。`--progress jsonl` 的事件在 stderr，可与 stdout 的最终 JSON 摘要分别保存。具体命令见[命令手册](commands.md)。


## 把已有 host 票据交给用户级服务

```sh
./shellpier service plan lab --ticket ./invitation/host.ticket --json
./shellpier service install lab --ticket ./invitation/host.ticket --json
./shellpier service start lab --json
```

需要以后登录自动启动时，仅在安装命令加 `--autostart`；安装本身不立即启动。也可先把 host 票据登记为 target，再传 `--ticket target:NAME`。服务不续期票据，有限授权到期后仍停止；更新成不同会话 ID 的票据需卸载后重新安装，避免服务悄悄指向另一目标。

MCP 客户端可启动 `shellpier mcp serve` 接入同一组有界工具。2026 模式逐请求携带元数据，2025 模式先初始化；远端操作显式指定票据引用。提交 jobs 时固定 operation ID，遇到 unknown 先查询原提交，输出截断或取消均不能证明操作没有副作用。


## 只分享文件与先看传输计划

只需提供文件读取时，目标端可直接限制入口：

```sh
./shellpier share --root ./shared --output access.json
```

控制端用 upload/download 或 SFTP 访问虚拟 `/` 中的文件；无需再指定访问模式。只允许下载时加 `--access-mode sftp-readonly`。此目录分享不提供命令执行、shell、jobs 或转发。

对已有目标目录，先只读预检，再明确执行：

```sh
./shellpier upload target:lab ./build remote-build --recursive --skip-existing --dry-run --json
./shellpier upload target:lab ./build remote-build --recursive --skip-existing --preserve-times --bwlimit 1048576 --json
```

已有普通文件直接跳过，不比较内容；默认仍拒绝覆盖。预检本身不锁定后续目录内容。`--preserve-times` 只保留秒级 mtime，不复制权限、所有权或原 atime。单文件 `--resume` 不与 dry-run 或 skip-existing 合用。

SSH 默认每 15 秒探测存活，单次探测最多等 45 秒，单候选建连默认 10 秒。可按网络条件调整相应参数，关闭保活用 `--keepalive-interval 0`。探测失败会结束连接，不会重新执行命令；断线后先查询任务或终端结果，再决定下一步。外部 OpenSSH 连接复用与关闭步骤见[命令参考](commands.md#openssh-配置中的保活与连接复用)。


## 把控制端开发服务临时提供给目标端

```sh
./shellpier forward @access.json --reverse --listen 8080 --to 127.0.0.1:3000 --json
```

目标端访问自己的 `127.0.0.1:8080`，连接会到达控制端的 `127.0.0.1:3000`。保持 forward 进程运行，结束时停止即可；默认完整模式不需要再配置允许开关。
