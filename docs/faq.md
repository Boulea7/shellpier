# 常见问题

[文档首页](README.md)

### 需要管理员权限或 Windows 密码吗？

现代版以启动它的当前用户运行，不需要系统 sshd 或 Windows 密码。Legacy Bitvise 路线需要管理员安装与适用许可。

### 手机热点、校园网可以直接用吗？

先完成正常网络认证。双方能访问你的 HTTPS/WSS 中继时，可走出站连接；同一 Wi-Fi 也可能隔离客户端。没有预置公共中继，详见[使用场景](workflows.md)。

### Codex / Claude Code 怎么连接？

先用 `schema --json` 发现命令，再用 `capabilities @access.json --json` 查询目标。一次命令使用 `exec --json`；需要保留交互上下文时使用 `terminal open/read/write/close`。也可生成 `ssh-config` 交给支持 OpenSSH 的工具。参数见[命令手册](commands.md)，安装仓库中的 [skill](../skills/shellpier/SKILL.md) 后确认 Agent 已加载。

### 怎么打开交互终端？

运行 `shell @access.json`。目标需要支持 PTY；Windows 要求 10 1809+，旧 Windows 10 仍可执行命令和传文件。Legacy 使用独立流程。PTY 输出合并 stdout/stderr，Agent 获取结构化结果时用 `exec --json`。

### 终端关闭后，输出与分享会话还在吗？

`terminal close` 只关闭该终端；目标分享仍需 stop/revoke。终端的有界私有输出快照保留供 read/status 查询，结束后删除本次终端目录。状态文件包含 secret，不能公开；停止操作不回滚远端命令。

### 文件传输中断怎么办？

先检查目标文件和命令副作用，再重试。上传可能留下部分文件，需要核实后处理；下载失败会尝试清理本次新建的本地文件。传输不自动重放、不覆盖已有文件，也不随会话到期删除成功传输的文件。

### 到期后是否完全不留痕？

到期或停止会撤销会话。生成的凭据可按[命令手册](commands.md)清理；系统审计、网络日志以及远程命令已经修改的文件仍会保留。

### 默认多久，可以一直运行吗？

默认有效期 3 小时。`--ttl 0` 请求持续会话；中继还需开启 `--allow-persistent`。进程仍在前台，开机启动与服务管理需另行配置。

### 下载或运行被拦截怎么办？

使用[安装指南](install.md)中的发行下载，或离线复制发行包。通过系统或组织的正常审查流程处理拦截；当前发行没有 Windows 代码签名或 macOS 公证。
