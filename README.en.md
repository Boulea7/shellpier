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

<p align="center">
  <a href="https://github.com/Boulea7/shellpier/releases">Download</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="docs/commands.md">Commands</a> ·
  <a href="CONTRIBUTING.md">Contribute</a> ·
  <a href="SUPPORT.md">Get help</a>
</p>

ShellPier provides command execution and file transfer through a temporary SSH session running as the current user. Extract the modern binary and run it without installing a system SSH server. Connect directly on a reachable network, or use your own WSS relay for computers behind campus networks and mobile hotspots.

- **Limited lifetime**: one hour by default, configurable TTL, explicit revocation and an opt-in persistent mode.
- **Less controller setup**: detects the target user and addresses, and generates SSH configuration with a pinned host key.
- **Focused commands**: inspect tickets, check identity, upload/download files and generate keys independently.
- **Agent support**: JSON output, a built-in SSH client and an [Agent skill](skills/shellpier/SKILL.md).

## Download

| System | Package |
|---|---|
| Windows 10 / 11 | `windows_amd64`, `windows_386` or `windows_arm64` |
| Linux | `linux_amd64` or `linux_arm64` |
| macOS 13+ | `darwin_amd64` or `darwin_arm64` |
| Windows 7 / 8 | Separate [Legacy entrypoint](docs/legacy-windows.md) in the archive; the new workflow still needs acceptance on older systems |

Download a ZIP from [Releases](https://github.com/Boulea7/shellpier/releases), verify it against `checksums.txt`, then extract it. See [installation and offline transfer](docs/install.md). ShellPier is currently a preview release.

## Try it locally

Open two **PowerShell** windows on the same Windows computer. This exercise listens only on loopback and needs no relay.

```powershell
# Window A: keep running; press Ctrl+C to stop
.\shellpier.exe share --relay= --listen 127.0.0.1:0 --ttl 30m --output access.json
```

```powershell
# Window B: check the machine and account, then execute a command
.\shellpier.exe check '@access.json'
.\shellpier.exe connect '@access.json' -- whoami
```

On macOS / Linux, replace `.\shellpier.exe` with `./shellpier`. `access.json` is a private credential; remove this session's generated files when finished. More examples are in [workflows](docs/workflows.md).

## Connect to another computer

For a reachable peer, follow the [LAN workflow](docs/workflows.md). Across campus networks, NAT or mobile hotspots, obtain your own or your team's relay URL and creation key first, then invite the target from the controller:

```powershell
# Controller: keep private keys here; send only host.ticket privately to the target
.\shellpier.exe invite --relay https://relay.example.com --token-file .\relay.key --ttl 1h --out .\invitation

# Target
.\shellpier.exe host '@host.ticket'

# Controller
.\shellpier.exe check '@invitation/access.json'
```

You must [deploy a relay](docs/relay.md) or use a trusted team relay; no public service is bundled. The target does not need to enter a Windows username or IP address.

## Common commands

```powershell
.\shellpier.exe inspect '@access.json' --json
.\shellpier.exe upload '@access.json' .\report.txt report.txt
.\shellpier.exe download '@access.json' result.txt .\result.txt
.\shellpier.exe ssh-config '@access.json' --out .\session-ssh
.\shellpier.exe keygen --out .\agent-key
```

Uploads and downloads refuse to overwrite files by default. Keep-awake, persistent sessions, completion for four shells and remote revocation are also available. See the [command reference](docs/commands.md) for their options and boundaries.

## Documentation and contributions

[Workflows](docs/workflows.md) · [Troubleshooting](docs/troubleshooting.md) · [FAQ](docs/faq.md) · [Compatibility](docs/compatibility.md) · [Research](docs/research.md)

The [documentation index](docs/README.md) links to the detailed manuals, currently written in Chinese. Contributions to fixes, platform acceptance and documentation are welcome. Start with [CONTRIBUTING](CONTRIBUTING.md); the [development guide](docs/development.md) explains package responsibilities, command additions and testing. The [roadmap](docs/roadmap.md) distinguishes current features from future work.

The modern endpoint runs with the current user's permissions and supports command execution and SFTP. Interactive terminals and desktop control are not available. Cleanup handles only this tool's resources; it does not remove system audit records or undo commands already executed. Read the [security policy](SECURITY.md).

MIT © [Boulea7](https://github.com/Boulea7)
