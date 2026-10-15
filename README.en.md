<p align="center">
  <img src="assets/banner.png" alt="ShellPier: a pier connecting two terminals" width="960">
</p>

<p align="center"><strong>Give your AI Agent temporary access to another computer.</strong></p>

<p align="center">
  <a href="README.md">简体中文</a> · English
</p>

<p align="center">
  <a href="https://github.com/Boulea7/shellpier/releases"><img src="https://img.shields.io/github/v/release/Boulea7/shellpier?include_prereleases&amp;label=preview" alt="Preview release"></a>
  <a href="https://github.com/Boulea7/shellpier/actions/workflows/ci.yml"><img src="https://github.com/Boulea7/shellpier/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.26+"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Boulea7/shellpier" alt="MIT license"></a>
</p>

ShellPier lets your AI agent run commands and transfer files on another computer for a limited time. Unzip it and run it with your current user permissions.

## Download and use

Download a ZIP for your OS/architecture from [Releases](https://github.com/Boulea7/shellpier/releases) and extract it. Supports Windows 10/11, Linux and macOS 13+; see the [legacy Windows guide](docs/legacy-windows.md) for Windows 7/8. This is a preview release. Installation and launchers are covered in the [installation guide](docs/install.md).

On a network where the two computers can reach each other:

```powershell
# Target computer: share for 3 hours by default; Ctrl+C stops early
.\shellpier.exe share --output access.json

# Controller: receive access.json through a private channel first
.\shellpier.exe connect '@access.json' -- whoami
```

On macOS/Linux, replace `.\shellpier.exe` with `./shellpier`. `access.json` grants access; share it only with the intended person or agent.

For an interactive terminal, use `shellpier shell @access.json`. Agents can use `shellpier --json exec @access.json --command whoami` or `shellpier mcp serve`. Directory sharing, file-list/stream transfers, background jobs, multiple port forwards, SOCKS5 proxies and user services are covered in the [command reference](docs/commands.md); discover interfaces with `shellpier --json schema`.

To share just one folder, add `--root DIR`. The peer gets file access inside that directory without setting up system accounts or an SSH service.

Across NAT, including many campus and hotspot networks, configure your own or your team's [relay](docs/relay.md). See the [workflows](docs/workflows.md) for more examples.

## Documentation and contributions

[Documentation](docs/README.md) · [Commands](docs/commands.md) · [Troubleshooting](docs/troubleshooting.md) · [Development](docs/development.md) · [Contribute](CONTRIBUTING.md) · [Get help](SUPPORT.md)

Supports command execution, interactive terminals and SFTP. Desktop control is not available. See the [security policy](SECURITY.en.md). Detailed manuals are currently in Chinese.

MIT © [Boulea7](https://github.com/Boulea7)
