# 从第一次连接到跨网络使用

下面先用单机演练验证操作，再按网络情况选择直接连接或 WSS 中继。示例中的 `./shellpier` 用于 macOS/Linux；PowerShell 换为 `.\shellpier.exe`，并给所有 `@file` 参数加引号。

## 1. 单机演练：不需要外网或中继

在解压目录开两个终端。终端 A：

```sh
./shellpier share --listen 127.0.0.1:0 --ttl 15m --output access.json --json
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
./shellpier doctor
./shellpier share --listen 0.0.0.0:0 --ttl 15m --output access.json --json
```

输出包括候选地址、期限、状态路径。把 `access.json` 私下复制到控制端；控制端执行 `connect @access.json -- whoami`。这一步成功才说明真实网络可达，网卡地址本身不能证明。

有多张网卡时，从 `doctor` 查出名称：目标端 `share --interface NAME` 只筛选公布地址，`--listen` 仍控制实际监听；macOS/Linux 控制端可用 `connect --interface NAME @access.json -- whoami` 约束出站网卡。不要照抄别人的网卡名。Windows 控制端暂不支持接口约束。

校园访客网络的 AP 隔离、手机热点或 NAT 可能阻止直接连接。目标和控制端分处不同 NAT、均无法接受入站时，选择下一节的 WSS 路线。

## 3. 跨网络：控制端发邀请

先从团队或自托管中继运营者取得 HTTPS URL 和创建密钥文件。ShellPier 没有预置公共中继，`relay.example.com` 只是占位符。

控制端：

```sh
./shellpier doctor --relay https://relay.example.com
./shellpier invite --relay https://relay.example.com --token-file ./relay.key --ttl 30m --out ./invitation
```

只把 `invitation/host.ticket` 私下发送给目标端，控制端私钥留在邀请目录。目标端：

```sh
./shellpier doctor --relay https://relay.example.com
./shellpier host @host.ticket
```

控制端：

```sh
./shellpier connect @invitation/access.json -- hostname
./shellpier connect @invitation/access.json -- whoami
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

Windows 7/8 请使用[独立 Legacy 指南](legacy-windows.md)，新流程尚未完成这些机器的实机验收。

## 长期运行与断线

需要长期前台会话时明确选择 `share --ttl 0` 或 `invite --ttl 0`；中继还必须配置 `--allow-persistent`。这不会安装开机服务，也不能跨中继重启恢复。

`--keep-awake` 自愿开启，需要平台机制正常工作，不保证锁屏、合盖、强制睡眠、注销或重启后持续连接。需要服务管理器时由用户另行配置，并记录停止与凭据更新方式。

网络中断可能在有效期内重连，但已经中断的命令不会自动重放。重试写文件、部署等命令前，先检查目标端是否已执行。stop/TTL 不回滚已产生的副作用。
