# 安装与离线传递

先下载与系统、CPU 匹配的 ZIP，解压后直接运行。ShellPier 不需要安装系统 SSH 服务，也不会自动加入 PATH。

## 1. 选择下载包

本页使用 [v0.1.0-alpha.2](https://github.com/Boulea7/shellpier/releases/tag/v0.1.0-alpha.2) 的预发行文件名。可下载版本以 [Releases 列表](https://github.com/Boulea7/shellpier/releases)为准，不要依赖 `/releases/latest`；[alpha.1](https://github.com/Boulea7/shellpier/releases/tag/v0.1.0-alpha.1) 保留供查阅。

| 系统 | 选择 |
|---|---|
| Windows 10/11 x64 | `windows_amd64.zip` |
| Windows 10/11 32 位 | `windows_386.zip` |
| Windows ARM64 | `windows_arm64.zip` |
| Intel macOS | `darwin_amd64.zip` |
| Apple Silicon macOS | `darwin_arm64.zip` |
| Linux x86_64 / ARM64 | `linux_amd64.zip` / `linux_arm64.zip` |

完整文件名形如 `shellpier_v0.1.0-alpha.2_windows_amd64.zip`。Windows 在“设置 → 系统 → 关于 → 系统类型”查看架构；macOS/Linux 运行 `uname -m`。优先选原生架构。

这一版用 Go 1.27.2 构建，原生 Windows 入口面向 Windows 10 及更新系统，macOS 最低为 13。更完整的范围见[兼容性说明](compatibility.md)。Windows 7/8 使用[独立 Legacy 流程](legacy-windows.md)，不是运行现代 EXE；新 Legacy 流程仍需对应机器验收。

## 2. 校验、解压、运行

从发行页面同时取得 ZIP 与 `checksums.txt`。在下载目录校验 ZIP 的 SHA-256，与清单中同名文件的值比较：

```powershell
Get-FileHash .\shellpier_v0.1.0-alpha.2_windows_amd64.zip -Algorithm SHA256
```

macOS 用 `shasum -a 256 文件名.zip`，Linux 用 `sha256sum 文件名.zip`。使用镜像或离线介质时，从可信发行渠道另取校验清单；同一个镜像上的 ZIP 和哈希无法独立证明来源。

解压到自己的用户目录，例如 `C:\Users\you\Tools\ShellPier`。进入该目录：

```powershell
.\shellpier.exe version
.\shellpier.exe doctor
```

macOS/Linux：

```sh
./shellpier version
./shellpier doctor
```

CMD 中使用 `shellpier.exe version`。PowerShell 中，带空格的完整程序路径要用 `&` 调用，例如 `& 'C:\Users\you\My Tools\shellpier.exe' version`。如需在任意目录运行，可自行配置 PATH；解压和下载脚本都不会替你配置。

随后按[工作流](workflows.md)完成单机演练。普通下载包没有公共中继服务；跨网络使用需要团队提供或自行部署的中继 URL 和创建密钥。

## 3. 双击启动与 Agent 使用

Windows 包含 `START.cmd`，macOS 包含 `start.command`：启动前台一小时会话，并显式启用防睡眠，每次在程序旁写入新的 `access-*.json`。实际路径显示在窗口中，重复启动不会覆盖旧凭据。默认直接连接；已配置中继环境变量时使用中继。窗口必须保持打开，Ctrl+C 停止。它们不安装开机服务。macOS 按正常系统流程允许运行已核验来源的文件。

访问文件是私密凭据，不能贴进日志、Issue 或公开聊天；会话结束后只删除本次生成的文件。以下 `access.json` 请换成窗口显示的文件名。PowerShell 中始终给 `@file` 加引号：

```powershell
.\shellpier.exe connect '@access.json' -- whoami
```

把完整 `skills/shellpier` 目录复制到 Agent 支持的技能目录即可安装技能，例如 Codex 的 `~/.codex/skills/`。再确认 Agent 确实能够发现技能；仅复制目录不能证明已加载。技能仍依赖本地可执行文件和相同的访问凭据。

## 可选：下载辅助脚本与离线传递

下载脚本要求你提供明确的 HTTPS ZIP 地址和可信 SHA-256，不自动选择镜像、不运行下载后的程序。现代 Windows 需要 PowerShell 5.1+：

```powershell
.\scripts\bootstrap.ps1 -ArchiveUrl 'https://your-approved-host/shellpier_v0.1.0-alpha.2_windows_amd64.zip' -Sha256 '<可信的64位SHA-256>' -Destination 'C:\Users\you\Tools\ShellPier'
```

CMD 可通过 `scripts\bootstrap.cmd` 传递同样的参数。macOS/Linux：

```sh
sh scripts/bootstrap.sh 'https://your-approved-host/shellpier_v0.1.0-alpha.2_linux_amd64.zip' '<可信的64位SHA-256>' './ShellPier'
```

这些 URL 和哈希占位符需要替换，目标目录须不存在。PowerShell 下载拒绝重定向，Unix 下载只允许 HTTPS 重定向。

离线机器：联网机器取得并校验 ZIP → 通过允许的介质复制 ZIP 和可信清单 → 目标机器再校验 → 解压运行。ZIP 包含文档、技能、Legacy 文件和 MIT 许可证；不捆绑 Bitvise、PuTTY、OpenSSH 或中继服务。
