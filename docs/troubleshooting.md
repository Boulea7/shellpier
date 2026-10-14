# Troubleshooting and alternatives

Start with `shellpier doctor --relay https://relay.example.com --json`. Record OS version, CPU architecture, CLI version, exit code and redacted diagnostics. Never attach access files, private keys, tickets, creation keys or full authorization headers to an issue.

| Symptom | Check and action |
|---|---|
| HTTPS works, session fails | Check WebSocket upgrades, proxy timeouts and CLI-compatible authentication. A browser login may not authorize a CLI socket. |
| Captive portal or login page | Complete normal login, then rerun doctor. Do not disable security or tunnel around the portal. |
| 401/403 | Verify the correct creation key for session creation; host and client role tokens are not interchangeable. |
| Host-key verification fails | Stop and verify the target independently. Recreate the session if intended; never disable strict checking. |
| Target disappears | Check sleep, process exit, TTL and relay restart. `--keep-awake` can help when allowed; inspect its platform result. |
| Command result lost | Inspect target-side effects before retrying; the old command is not automatically replayed. |
| Cannot execute modern exe on Windows 7/8 | Use the separate legacy package and its requirements, not the modern Go binary. |
| Direct connection refused | Check target listener, selected address, firewall and NAT. Do not expose an unrestricted system SSH server as a quick fix. |
| SFTP cannot find config/key | Keep the generated directory intact; quote paths containing spaces or non-ASCII text. |
| Antivirus blocks the executable | Verify release checksum and provenance, then use your organization's review process. Do not globally disable protection. |

## Compatibility evidence

Modern release targets are Windows amd64/386/arm64, Linux amd64/arm64 and macOS amd64/arm64. A successful cross-compilation only proves compilation. CI runners do not cover every OS version or CPU. Go's OS minimums can change: consult [Go minimum requirements](https://go.dev/wiki/MinimumRequirements). Legacy Windows uses a separate implementation and needs explicit Windows 7/8 acceptance tests.

## Alternatives when the transport does not fit

| Tool | When useful | Costs and limits |
|---|---|---|
| [OpenSSH](https://www.openssh.com/manual.html) | Existing allowed SSH server and reachable route | Service/firewall setup; reverse SSH on 443 is still SSH, not HTTPS |
| [chisel](https://github.com/jpillora/chisel) | HTTP/WebSocket transport to an existing TCP service | Separate SSH server and chisel endpoint/authentication; inspect security flags |
| [frp](https://github.com/fatedier/frp) | Managed long-lived reverse proxy estate | More operator configuration; transport/TLS requirements depend on mode |
| [Bitvise SSH Server](https://bitvise.com/ssh-server) | Legacy Windows with a compatible vendor version | Confirm version/OS support, licensing and separate instance; administrator setup may be required |
| [PuTTY](https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html) | Existing SSH access from Windows | Client/Plink only; does not itself make the target reachable or HTTPS-compatible |

These are independent products, not bundled fallbacks. Obtain binaries from their official distribution and check the exact version's system requirements. An HTTP CONNECT proxy, TLS interception or corporate VPN may need approved explicit configuration; HTTPS port numbers alone do not guarantee policy compliance.
