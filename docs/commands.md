# Commands

Run `shellpier <command> --help` for exact flags. Tokens and access files are secrets; prefer `@file` over a literal token, which may enter shell history or process listings.

| Command | Purpose |
|---|---|
| `share --relay URL --ttl 1h --output access.json` | Create temporary host-side access and run the SSH endpoint |
| `share --listen 0.0.0.0:0 --ttl 1h` | Listen directly where inbound access is allowed |
| `invite --relay URL --ttl 1h --out DIR` | Generate host ticket and controller-only access/key files |
| `host @host.ticket` | Join the relay using the target-only ticket |
| `connect @access.json -- command arg...` | Execute a command through built-in SSH |
| `ssh-config @access.json --out DIR` | Write isolated key, pinned known-hosts and SSH configuration |
| `proxy @access.json` | Transport for generated OpenSSH ProxyCommand; stdin/stdout are protocol bytes |
| `doctor --relay URL --json` | Inspect platform and relay reachability |
| `status --state PATH` | Inspect an explicit local session state |
| `stop --state PATH` | Stop/revoke this tool's session |
| `relay --listen 127.0.0.1:8080 --token-file PATH` | Run an authenticated relay behind TLS termination |
| `revoke @host.ticket` | Revoke a relay session remotely using its target ticket |
| `cleanup --dir DIR` | Remove this generated SSH directory’s owned credential files |
| `version` | Print build version |

`--keep-awake` is opt-in. Keep the host foreground process open throughout the session. TTL is a maximum lifetime, not a persistence or reconnection guarantee. After a transport failure, inspect whether the remote command ran before retrying; execution may have occurred before the response was lost.

The target's current OS account determines file permissions and command access. A temporary SSH key grants that account's execution rights, not a sandbox. SFTP is supported by the SSH endpoint; use an SFTP client with the generated SSH config. Interactive terminals, port-forwarding and GUI automation are not promised by this interface.

Relay creation uses `SHELLPIER_RELAY_KEY` or `--token-file PATH`. `SHELLPIER_RELAY_URL` sets the default relay. `share` and `host` print their private temporary `state.json` path; use that exact file with status/stop. Control uses a loopback endpoint rather than killing a PID. Each SSH config uses alias `shellpier` and must be selected with `-F`; it does not edit your global SSH configuration.

`--ttl 0` explicitly requests a persistent session. Direct mode permits this; relay mode also requires the operator to enable `--allow-persistent`. Ordinary relay sessions default to a maximum of 24 hours unless the operator changes `--max-ttl`. Persistent sessions still run in the foreground unless the user separately configures an approved supervisor.

`connect` defaults to `whoami` when no command is provided. Commands are joined into a remote shell command; quote arguments for the target shell when they contain metacharacters. Choose `--shell powershell`, `--shell cmd` or `--shell sh` on `share`/`host` if automatic selection does not fit. `--work-dir PATH` sets the remote starting directory.

For direct multi-interface access, `share --interface NAME` selects advertised addresses; `--listen` controls the actual bind address. `connect --interface NAME @access.json -- whoami` constrains direct routing on macOS/Linux. Interface selection is not supported for relay sessions; use approved `HTTPS_PROXY` configuration there.

`share --output access.json --json` writes credentials into a new private file and emits session metadata. Without `--output`, stdout/JSON includes the access ticket and must be treated as secret. `host @host.ticket --json` emits metadata without the controller's private key. Existing output files/directories are refused rather than overwritten.

`ssh-config` creates `access.json`, `identity`, `known_hosts`, `ssh_config` and `shellpier-manifest.json`. `invite` also adds `host.ticket`. Stop or revoke first, verify refusal, then use `cleanup --dir DIR`; cleanup removes owned generated files and preserves unrelated files, but does not revoke a running session. An arbitrary access file outside this directory is not removed by cleanup.

`relay --tls-cert CERT.pem --tls-key KEY.pem` serves native HTTPS. Both flags are required together. Without them, place its HTTP listener behind a TLS reverse proxy. Operators can set `--max-sessions` (default 64), `--max-ttl` (default 24h), and explicitly opt into `--allow-persistent`.
