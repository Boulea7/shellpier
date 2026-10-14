# Install and offline transfer

Choose the archive matching OS and CPU: Windows `amd64` for x64, `386` for x86 and `arm64` for ARM64; Linux/macOS offer amd64 and arm64. Archive names use `darwin` for macOS. On Windows use Settings → System → About to check **system type**; on Unix use `uname -m`. Emulation support depends on the OS, so prefer a native build.

Download from the repository Releases or an explicitly approved mirror. Check SHA-256 against an independently trusted release manifest. A hash served by the same compromised mirror is not proof of publisher identity. Extract into a private, user-writable directory. Run `./shellpier` on macOS/Linux, `.\shellpier.exe` in PowerShell, or `shellpier.exe` in CMD from that directory; the bootstrap does not add it to PATH. In PowerShell always quote ticket arguments beginning with `@`, for example `'@access.json'`, to avoid splatting. no global installation or administrator account is needed.

The optional bootstrap scripts require an explicit HTTPS archive URL and expected SHA-256. They do not choose a mirror, modify PATH, or execute downloaded install scripts. Modern Windows needs PowerShell 5.1+; use the independent legacy package for Windows 7/8. Example:

```powershell
.\scripts\bootstrap.ps1 -ArchiveUrl 'https://your-approved-host/shellpier_VERSION_windows_amd64.zip' -Sha256 '<64-character-trusted-sha256>' -Destination 'C:\Users\you\Tools\ShellPier'
```

The CMD wrapper forwards the same arguments. Quote paths with spaces/non-ASCII characters. For macOS/Linux, invoke the local script with URL, hash and a new directory:

```sh
sh scripts/bootstrap.sh 'https://your-approved-host/shellpier_VERSION_linux_amd64.zip' '<64-character-trusted-sha256>' './ShellPier'
```

Offline transfer: obtain and verify the correct archive on an internet-connected machine, copy the archive and trusted checksum via approved media, verify again on the target, then extract. Release archives include `legacy/`, `skills/`, documentation and MIT license. They do not include third-party Bitvise, PuTTY, OpenSSH or a relay service.

To install the Agent skill, copy the complete `skills/shellpier` folder into your agent's supported skill directory (for Codex, normally `~/.codex/skills/`), then verify it is discoverable in that agent. Folder presence alone is not runtime discovery. The skill uses the same executable and authorization boundaries as manual operation.

Windows archives include `START.cmd`: double-click to start a foreground one-hour session with opt-in sleep prevention, writing `access.json` beside the executable. It uses direct mode unless the approved relay environment is configured; transfer access credentials privately and stop with Ctrl+C. It refuses the modern executable on detected older Windows and runs the legacy doctor instead. `START.cmd` does not install a service. macOS archives include `start.command` with the same foreground behavior; allow execution through your normal OS security process after verifying the archive. Bootstrap downloads never run the extracted executable; PowerShell refuses redirects, and the Unix downloader allows only HTTPS redirects.
