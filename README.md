<p align="center">
  <img src="assets/banner.png" alt="ShellPier：连接两端终端的栈桥" width="960">
</p>

<p align="center"><strong>让自己的 AI Agent，临时连上另一台电脑。</strong></p>

<p align="center">
  简体中文 · <a href="README.en.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/Boulea7/shellpier/releases"><img src="https://img.shields.io/github/v/release/Boulea7/shellpier?include_prereleases&amp;label=preview" alt="预览版本"></a>
  <a href="https://github.com/Boulea7/shellpier/actions/workflows/ci.yml"><img src="https://github.com/Boulea7/shellpier/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.26+"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Boulea7/shellpier" alt="MIT 许可证"></a>
</p>

<p align="center">
  <a href="https://github.com/Boulea7/shellpier/releases">下载</a> ·
  <a href="docs/README.md">文档</a> ·
  <a href="docs/commands.md">命令</a> ·
  <a href="CONTRIBUTING.md">参与贡献</a> ·
  <a href="SUPPORT.md">获取帮助</a>
</p>

ShellPier 把当前用户的命令执行和文件传输交给临时 SSH 会话。现代版解压即用，无需安装系统 SSH 服务；可在局域网直连，也可通过自建 WSS 中继连接校园网、手机热点后的电脑。

- **连接有期限**：默认一小时，自定义 TTL，支持主动撤销和持续运行。
- **控制端少配置**：自动识别目标用户与地址，生成固定主机公钥的 SSH 配置。
- **常用动作独立**：查看票据、检查身份、上传下载、生成密钥，均可单独调用。
- **适合 Agent**：提供 JSON 输出、内置 SSH 客户端和 [Agent skill](skills/shellpier/SKILL.md)。

## 下载

| 系统 | 选择 |
|---|---|
| Windows 10 / 11 | `windows_amd64`、`windows_386` 或 `windows_arm64` |
| Linux | `linux_amd64` 或 `linux_arm64` |
| macOS 13+ | `darwin_amd64` 或 `darwin_arm64` |
| Windows 7 / 8 | 包内 [Legacy 独立入口](docs/legacy-windows.md)；新流程仍需旧系统实机验收 |

从 [Releases](https://github.com/Boulea7/shellpier/releases) 下载 ZIP，校验 `checksums.txt` 后解压。详见[安装与离线传递](docs/install.md)。当前为预览版。

## 先跑通一次

在同一台 Windows 电脑开两个 **PowerShell** 窗口；这次演练只监听本机，无需中继。

```powershell
# 窗口 A：保持运行，Ctrl+C 结束
.\shellpier.exe share --relay= --listen 127.0.0.1:0 --ttl 30m --output access.json
```

```powershell
# 窗口 B：核验主机和账户，再执行命令
.\shellpier.exe check '@access.json'
.\shellpier.exe connect '@access.json' -- whoami
```

macOS / Linux 将 `.\shellpier.exe` 换成 `./shellpier`。`access.json` 是私密凭据；会话结束后删除本次生成的文件。

## 连到另一台电脑

双方可直连时，按[局域网示例](docs/workflows.md)操作。跨校园网、NAT 或手机热点时，先取得自己或团队的中继 URL 与创建密钥，再从控制端邀请目标机：

```powershell
# 控制端：私钥留在本机，只把 host.ticket 私下交给目标机
.\shellpier.exe invite --relay https://relay.example.com --token-file .\relay.key --ttl 1h --out .\invitation

# 目标机
.\shellpier.exe host '@host.ticket'

# 控制端
.\shellpier.exe check '@invitation/access.json'
```

中继需要[自行部署](docs/relay.md)，没有预置公共服务。目标机不需要手输 Windows 用户名或 IP。

## 几个常用动作

```powershell
.\shellpier.exe inspect '@access.json' --json
.\shellpier.exe upload '@access.json' .\report.txt report.txt
.\shellpier.exe download '@access.json' result.txt .\result.txt
.\shellpier.exe ssh-config '@access.json' --out .\session-ssh
.\shellpier.exe keygen --out .\agent-key
```

上传、下载默认不覆盖文件。还支持 `--keep-awake`、持续会话、四种终端补全和远程撤销，见[命令手册](docs/commands.md)。

## 文档与贡献

[使用场景](docs/workflows.md) · [排障](docs/troubleshooting.md) · [常见问题](docs/faq.md) · [兼容性](docs/compatibility.md) · [方案调研](docs/research.md)

欢迎修复问题、补充平台验收和改进文档。从 [CONTRIBUTING](CONTRIBUTING.md) 开始；[开发指南](docs/development.md)说明目录职责、如何增加命令与相应测试。[路线图](docs/roadmap.md)列出当前能力和待完善项。

现代版以当前用户权限运行，支持命令执行和 SFTP；暂不提供交互终端或桌面控制。清理仅处理本工具资源，不删除系统审计，也不回滚已执行命令。详见[安全策略](SECURITY.md)。

MIT © [Boulea7](https://github.com/Boulea7)
