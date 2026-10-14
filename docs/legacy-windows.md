# Windows 7 legacy CLI

`legacy/shellpier.cmd` is the Windows PowerShell 2.0 / .NET Framework 3.5 entry point. The current Go executable is not compatible with Windows 7. Copy the entire `legacy` directory to a local folder and extract archives before running. The scripts use ASCII with CRLF line endings; local structured state uses PowerShell CLIXML. No private key, account, host address, installer or license is bundled.

## Implemented behavior

| Command | Behavior | Privilege / external effects |
|---|---|---|
| `doctor` | OS, service pack, architecture, CLR, installed .NET, PowerShell, desktop account/operator and SID, IPv4 interfaces, tool locations, WinHTTP proxy, firewall profiles, existing Bitvise services | Read only, normal user |
| `prepare` | Concrete offline direct setup steps | Prints only |
| `prepare -Mode reverse` | Checks a supplied Plink binary hash and Windows Authenticode publisher; prints a host-key-pinned reverse SSH command | Prints only; guide, no connection or TTL |
| `start` | Creates a fresh GUID-named Bitvise instance, configures public-key-required access for the detected desktop account, exact client IPv4 policy, selected IPv4 port, restricted firewall rule and durable SYSTEM expiry task | Administrator; requires `-AllowInstall -AcceptLicense` |
| `status -Session ID` | Reads owned session state, service, expiry and listener ownership; reports expired-but-running/degraded cases | Read only; admin may be required to read protected state |
| `stop -Session ID` | Disables/stops only the owned named service, clears its accounts/keys, saves a deny-all source policy and verifies revocation, removes the owned firewall rule and cleanup task | Administrator |

`start` never selects or modifies the default Bitvise instance or existing named instances. It chooses `ShellPier-<random GUID>` and refuses a collision. The installer is a vendor service installer: the **CLI files are portable; the SSH server is not an installation-free portable server**. Installing an additional instance may still register shared vendor components; review vendor messages when other Bitvise versions are present. Scripts do not silently elevate, reboot, change passwords, disable the Windows firewall, alter sleep settings or clear system audit logs.

## Direct setup

From ordinary CMD:

```bat
shellpier.cmd doctor
shellpier.cmd prepare
```

Obtain the official **Bitvise SSH Server 9.66** installer offline from the vendor and retain its original bytes. This implementation pins the locally verified official installer SHA-256:

```text
d1407f989d18f4505bf623eb1c33a68cd5c79a483214ebc1ff86bee9821293a6
```

The hash is compared, not merely printed. Windows must also trust its Authenticode signature with publisher `Bitvise Limited`. A corrupt file, wrong publisher or untrusted signature stops before execution; no bypass for missing Windows certificate updates is offered. Other installer releases are deliberately rejected until reviewed and repinned. No runtime download is performed.

Create an SSH key on the connecting machine. Put its single-line **public** key in `access.pub`; retain the private key there. Review the [vendor EULA](https://bitvise.com/ssh-server-license) and select a license valid for your actual use. ShellPier's own license does not license Bitvise, and neither non-commercial eligibility nor an evaluation entitlement is assumed. Accepting terms does not activate or purchase a license.

Open CMD as administrator and run, substituting the connecting machine's actual IPv4:

```bat
shellpier.cmd start -Installer .\BvSshServer-Inst.exe -PublicKey .\access.pub -AllowedClient 192.0.2.10 -Port 2222 -TtlMinutes 60 -AcceptLicense -AllowInstall
```

It uses the installer's documented `-instance=<name> -acceptEULA` switches and omits `-startService`. Any nonzero installer exit is treated as requiring attention, including possible restart warnings; it does not proceed with an unverified partial installation. The service is disabled and stopped before configuration, then set to manual startup immediately before starting it. The configuration explicitly selects the owned instance, writes only domain fields for domain accounts, removes its IPv6 SSH bindings, requires the imported public key and saves/reloads settings to check the result. The global and account source policies permit exactly the selected client IPv4 followed by deny-all. Automatic vendor firewall management is disabled for this instance.

First Bitvise installation can require a Windows restart before passwordless Windows logon works. The CLI never reboots automatically. If a restart is needed, review the vendor's instructions, clean up the failed session, reboot yourself and start a new session; do not regard a listener as successful remote authentication. Startup does not request or change the Windows password. The full detected `COMPUTER\account` or `DOMAIN\account` identity is printed with its SID. Under elevation through another account, the desktop account and operator may differ and doctor reports both.

Copy the printed session ID for status and stop. On the connecting machine, independently verify the new server's SSH host key through the vendor control panel or another trusted channel before accepting it, then run `hostname` and `whoami` over SSH. ShellPier reports **LISTENING / remote login unverified**, not a successful connection. Each new instance has its own host keys.

## Expiry and cleanup

Before installation, start creates a protected `%ProgramData%\ShellPierLegacy\<session>` directory and registers a SYSTEM Task Scheduler task with the exact UTC expiry. The task uses protected script copies, runs on battery, requests wake, and runs when available after a missed trigger. It survives closing the launching terminal. ACLs use well-known Administrator/SYSTEM SIDs, avoiding localized group names. The state and setup backup are protected; they are not public diagnostics.

At expiry or manual stop the service is disabled first, then stopped, accounts including their imported keys are removed and its source policy becomes deny-all. Only the session's GUID firewall rule and task are removed. The installed but inactive named Bitvise instance, protected local state/backups, vendor logs and Windows audit history are retained. Uninstallation is deliberately not hidden inside TTL: use the vendor control panel to remove the exact printed named instance after reviewing that it is inactive. Do not remove the default instance.

Expiry relies on Windows Task Scheduler and the machine being able to execute the cleanup. When the machine is suspended, powered off, its clock is changed or scheduling is disabled, exact wall-clock execution cannot be guaranteed. `status` flags expiry while still running. Use `stop` after a scheduling failure. A manual or malicious restart of a disabled service is outside the TTL guarantee. This CLI does not provide reverse-mode TTL; its Plink route is an explicitly supervised guide.

## Reverse SSH guide for NAT

Use an SSH relay you control, with a narrowly scoped relay account authorized for a loopback reverse forward. Windows still needs a working local SSH server. Plink is the outbound transport and does not itself supply that server.

For a new ShellPier-managed local server behind the reverse tunnel, run the direct `start` command with `-ListenAddress 127.0.0.1 -AllowedClient 127.0.0.1`. Windows receives these forwarded connections from local Plink, so a policy allowing only the external client's IPv4 would reject them. Supply the connecting client's public key for Windows login, separate from the relay `.ppk` credential. Keep both private keys outside published packages. The local server's TTL still applies; the outbound Plink process needs separate supervision.

As reviewed on 2026-10-14, the [official PuTTY download page](https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html) lists **0.85**, released 2026-08-16. The official [32-bit Plink 0.85 binary](https://the.earth.li/~sgtatham/putty/0.85/w32/plink.exe) was downloaded without executing it; its SHA-256 is the default pin:

```text
c7be84cc42565ad4de7e77e99f0a61f3a8dec17c77d5ff1afad79d005af54c93
```

Its embedded publisher certificate names Simon Tatham. The command also requires a **Valid Windows Authenticode** result for `Simon Tatham`; embedded certificate extraction on a Mac is not that validation. For another architecture/release, independently verify the official package/signatures and explicitly provide its reviewed `-PlinkSha256`; never replace the pin with the hash of an untrusted download merely to pass the check. Win7 trust stores may require legitimate Windows updates to validate current signatures.

Obtain the relay's SHA-256 host key fingerprint through a trusted separate channel and use a PuTTY `.ppk` key authorized on that relay. No password is inserted into generated commands. In CMD:

```bat
shellpier.cmd prepare -Mode reverse -Plink .\plink.exe -RelayHost relay.example.com -RelayUser relay-user -RelayHostKey SHA256:TRUSTED_FINGERPRINT -RelayPrivateKey .\relay.ppk -RelayPort 443 -RemotePort 2222 -Port 2222
```

The generated command uses `-batch -noagent -noshare -N -hostkey` and `-R 127.0.0.1:2222:127.0.0.1:2222`. It is a guide: **no connection is started and relay policy is not provisioned**. Verify relay `AllowTcpForwarding` / permitted listener settings and retain loopback binding rather than exposing the forwarded port publicly. See the [official Plink manual](https://the.earth.li/~sgtatham/putty/0.85/htmldoc/Chapter7.html). Arrange separate lifetime supervision and stop the Plink process when finished. Access the Windows SSH server through an authenticated relay session and verify the Windows server host key too.

SSH on TCP port 443 is **SSH, not HTTPS**. It can cross ordinary NAT through an allowed outbound TCP connection, but does not automatically cross HTTP-only proxies, TLS inspection, captive portals or networks that block SSH. The CLI does not weaken proxy/TLS checks or provision NAT traversal infrastructure. Complete captive portal login yourself first. Doctor's WinHTTP proxy output is only one proxy surface; environment variables, application settings and VPN routes require separate checks.

## Diagnostic considerations

- Default and Wi-Fi routes can reach different hosts with the same private IP. Verify server host key and identity, not only port 22. `--noproxy` affects an HTTP application proxy and does not bypass a VPN route. Select the current real interface on the connecting machine; do not assume its device name or source IP is permanent.
- `LocalSubnet` can block another routed campus subnet. Start's source policy uses one exact current client IP. If the client changes IP, stop and create a new session rather than widening access to everyone.
- Laboratory, campus and virtual-adapter IPv4 addresses have different reachability. Doctor lists interfaces and gateways rather than automatically selecting the first address.
- `winDomain` is disabled for local Bitvise accounts even for reads or writing an empty string. Domain fields are touched only after selecting a domain-account type. There is no `publicKeyMethod` property in the reviewed schema; imported account keys and `auth.publicKeyAuth` control this access.
- PowerShell `-EncodedCommand` expects UTF-16LE Base64. PowerShell output can be forced to UTF-8, but native CMD tools on Chinese Win7 may still output GB18030. Windows PowerShell transcripts may be UTF-16 with a BOM. Decode the actual producer's encoding rather than assuming all output is UTF-8.
- A service shell normally runs outside the interactive desktop session. Starting GUI applications over SSH is not proof that they appeared on the user's desktop. Win7 CMD paste commonly uses the right-click menu instead of Ctrl+V.

## Validation boundaries

This CLI uses COM account-field behavior, address-list `Clear`, hash API `Clear` for .NET 3.5, save/reload checks and identity detection. Independent named instances and durable TTL require separate runtime acceptance.

The new independent installation, Task Scheduler expiry, firewall cleanup and end-to-end Plink reverse path **have not been accepted on a Windows machine**. Strict mock COM tests cover local/domain account fields, imported-key failure before save/unlock and refusal of the default instance; core tests cover hashing, malformed IP/ports and signature/publisher refusal. All scripts pass the local PowerShell parser. Modern parser/mock success is not PowerShell 2.0 or Win7 runtime acceptance.
