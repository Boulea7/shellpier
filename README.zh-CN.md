# ShellPier

[![CI](https://github.com/Boulea7/shellpier/actions/workflows/ci.yml/badge.svg)](https://github.com/Boulea7/shellpier/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

让 Agent 临时连到另一台电脑：当前用户权限、SSH 端到端加密、会话自动过期。

[下载](https://github.com/Boulea7/shellpier/releases) · [English](README.md) · [使用场景](docs/workflows.md) · [命令说明](docs/commands.md) · [部署中继](docs/relay.md) · [排障](docs/troubleshooting.md) · [下载与离线安装](docs/install.md) · [兼容性](docs/compatibility.md) · [方案调研](docs/research.md)

目标机主动向中继建立 WebSocket，控制端通过同一中继访问内置 SSH。无需管理员权限，也无需安装系统 SSH 服务。默认在前台运行，不添加开机启动；支持执行命令和 SFTP，不提供 PTY 或桌面控制。

| 系统 | 解压后运行入口 |
|---|---|
| Windows 10/11 | PowerShell `.\shellpier.exe`；CMD `shellpier.exe` |
| Windows 7/8 | [独立 Legacy PowerShell/Bitvise 入口](docs/legacy-windows.md) |
| macOS / Linux | `./shellpier`（压缩包中的 darwin 表示 macOS） |

## 三步使用

下方 Unix 风格示例假定程序已在 PATH；未配置时使用表格中的相对路径。Windows PowerShell 可直接复制以下命令，`@file` 必须加引号以避免 splatting：

```powershell
.\shellpier.exe doctor --relay https://relay.example.com
.\shellpier.exe share --relay https://relay.example.com --ttl 1h --output access.json
# 私下转移文件后，在控制端运行：
.\shellpier.exe connect '@access.json' -- hostname
.\shellpier.exe connect '@access.json' -- whoami
.\shellpier.exe ssh-config '@access.json' --out .\session-ssh
# 使用邀请时，目标机运行：
.\shellpier.exe host '@host.ticket'
```

CMD 在解压目录使用 `shellpier.exe connect @access.json -- whoami`。安装脚本不添加 PATH；现代下载脚本要求 PowerShell 5.1+，旧系统走独立 Legacy 入口，不依赖 Invoke-WebRequest。


1. 从本仓库 Releases 下载与你的系统、CPU 对应的压缩包，对照 `checksums.txt` 校验 SHA-256，再解压。没有预置免费公共中继：向团队取得 URL 与创建密钥，或按[部署指南](docs/relay.md)自建一次。
2. 目标机执行：

   ```sh
   shellpier doctor --relay https://relay.example.com
   shellpier share --relay https://relay.example.com --ttl 1h --output access.json
   ```

   私下设置 `SHELLPIER_RELAY_KEY`，或添加 `--token-file /私有目录/relay.key`；`SHELLPIER_RELAY_URL` 可作为默认中继地址。将 `access.json` 私下交给控制端；其中有临时私钥，不要粘贴到聊天、提交到 Git 或输出到 CI 日志。

3. 控制端先核验身份，再执行授权工作：

   ```sh
   shellpier connect @access.json -- hostname
   shellpier connect @access.json -- whoami
   shellpier ssh-config @access.json --out ./session-ssh
   ```

   生成目录内含独立密钥与严格固定主机公钥的 SSH 配置；例如 `ssh -F ./session-ssh/ssh_config shellpier hostname` 或 `sftp -F ./session-ssh/ssh_config shellpier`，也可交给支持 OpenSSH 的 Agent。内置 `connect` 不依赖系统 OpenSSH。

也可由控制端先运行 `shellpier invite --relay https://relay.example.com --ttl 1h --out ./invitation`，只向目标机发送 `host.ticket`，目标机运行 `shellpier host @host.ticket`。控制端保留 `access.json` 与私钥，目标机无需接触控制端私钥。

结束时 Ctrl+C，或执行 `shellpier stop --state <启动时输出的state.json路径>`。停止前可用 `status` 查看本工具状态，结束后运行状态文件会删除；邀请模式也可用 `shellpier revoke @invitation/host.ticket` 从控制端撤销。实际尝试一次新连接确认已拒绝，再用 `shellpier cleanup --dir ./invitation` 或 `./session-ssh` 删除生成凭据。cleanup 保留无关文件，但本身不会撤销仍在运行的会话。断线不会自动重放旧命令。

## 兼容性与边界

| 场景 | 入口 |
|---|---|
| Windows 10/11、Linux、macOS | 原生 Go 程序 |
| Windows 7/8 | [独立 Legacy Windows 入口](docs/legacy-windows.md)，需 PowerShell 与单独 Bitvise 实例；尚待对应系统实机验证 |
| 只允许 HTTPS 出站 | 团队允许的 WSS 443 中继；正常完成认证门户登录 |
| 暂时禁止睡眠 | 自愿添加 `--keep-awake`；系统能力不可用时检查提示 |
| 可接受入站连接的局域网 | `--listen 0.0.0.0:0`，先确认防火墙与可达性 |

原始 SSH 即使使用 443 端口也不等于 HTTPS。ShellPier 不绕过 VPN、认证门户或组织网络规则，不清理系统审计记录；停止只撤销本工具的会话资源。

开发需 Go 1.26+。详见 [CONTRIBUTING](CONTRIBUTING.md)。仓库内附可安装的 [Agent skill](skills/shellpier/SKILL.md)，帮助 Agent 诊断、核验身份和证明撤销生效。MIT © Boulea7。
