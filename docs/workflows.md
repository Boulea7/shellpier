# Practical workflows

## Same LAN or USB tether: try direct first

On the target, inspect interfaces and start a short session. This exposes a temporary TCP listener, so choose a trusted network and permitted bind address.

```sh
shellpier doctor
shellpier share --ttl 15m --listen 0.0.0.0:0 --output access.json --json
```

The JSON event reports the selected addresses, expiry and state path. Transfer `access.json` privately. On the controller, `shellpier connect @access.json -- hostname` and `shellpier connect @access.json -- whoami` prove target identity and actual peer reachability. A local IP in doctor output alone does not prove peers can reach it; guest Wi-Fi/campus client isolation and phone hotspot NAT can block direct access.

On macOS/Linux with several interfaces, use `share --interface NAME` to advertise the intended addresses and `connect --interface NAME @access.json -- whoami` to constrain direct routing. The share flag does not replace `--listen`; it selects advertisement. Do not infer interface names from examples—read your own doctor output.

## Outbound-only target: controller creates the invitation

This keeps the controller's private SSH key on the controller from the beginning. Obtain an approved relay URL and creation-key file first.

```sh
shellpier doctor --relay https://relay.example.com
shellpier invite --relay https://relay.example.com --token-file ./relay.key --ttl 30m --out ./invitation
```

Send only `invitation/host.ticket` to the intended target through an authorized private channel. On the target:

```sh
shellpier doctor --relay https://relay.example.com
shellpier host @host.ticket
```

On the controller:

```sh
shellpier connect @invitation/access.json -- hostname
shellpier connect @invitation/access.json -- whoami
ssh -F ./invitation/ssh_config shellpier hostname
sftp -F ./invitation/ssh_config shellpier
```

The target runs in the foreground. A network interruption may be retried within the session lifetime; interrupted commands are never replayed. Check remote effects before resubmitting a command whose response was lost.

When finished, revoke and prove refusal before deleting credentials:

```sh
shellpier revoke @invitation/host.ticket
# This fresh attempt must fail after revocation:
shellpier connect @invitation/access.json -- whoami
shellpier cleanup --dir ./invitation
```

The refusal is evidence of revocation; a successful cleanup is only evidence that owned local files were removed. Preserve the host ticket if revocation could not be confirmed, so you can retry. Do not delete unrelated user files to make cleanup remove a directory.

## Windows target and paths containing spaces

Use the native executable on supported modern Windows. PowerShell quotes paths; invoking a quoted executable path requires `&`:

```powershell
& 'C:\Users\you\Tools\ShellPier\shellpier.exe' host '@C:\Users\you\Downloads\Session Files\host.ticket' --shell powershell --work-dir 'C:\Users\you\工作目录'
```

Use `--shell cmd` only when the intended commands are CMD syntax. Built-in connect joins arguments into a remote shell command, so escaping follows the **target** shell. Prefer small, explicit commands before complex quoting or transferring a reviewed script with SFTP. Do not assume Unix `hostname`/`whoami` command syntax extends to every command on Windows.

`--keep-awake` is opt-in and fails visibly when the OS mechanism is unavailable; it does not guarantee connectivity through lock, logout, reboot, lid policy or power loss. Default operation does not modify autostart, firewall rules, global routes or system SSH services. For Windows 7/8, use the [independent legacy guide](legacy-windows.md).

## TTL, stop and persistence

A normal session lasts one hour; set a shorter TTL for a probe. Keep the printed state path privately. Inspect status while running, then stop:

```sh
shellpier status --state /printed/private/runtime/state.json
shellpier stop --state /printed/private/runtime/state.json
```

Shutdown removes its runtime state, so a subsequent status failure can mean it is already stopped. Prove actual rejection using the access ticket. A TTL acceptance test similarly attempts a fresh connection after the deadline.

`--ttl 0` is an explicit persistent choice. Relays reject it unless operators enable `--allow-persistent`. It still does not install a service. If you need a supervisor, configure one separately with a dedicated approved account and private files; document who owns it, how to stop it and how to rotate/revoke credentials. No session persists across relay restarts in the current in-memory design.

In PowerShell, quote every `@file` ticket argument, e.g. `.\shellpier.exe connect '@access.json' -- whoami`. In CMD, `shellpier.exe connect @access.json -- whoami` is valid. The shell examples elsewhere use Unix/PATH syntax; bootstrap does not add the executable to PATH.
