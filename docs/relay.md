# Relay deployment

ShellPier ships no public relay. A trusted operator supplies an HTTPS URL and creation key. Session creation requires that key; established host/client roles have separate secrets. SSH payloads remain encrypted between endpoints. The relay still sees connection metadata and timing.

## One relay, many temporary sessions

Build `shellpier`, generate a random key with `shellpier relay --init-key /private/path/relay.key` (or `openssl rand -hex 32` saved privately), and start:

```sh
shellpier relay --listen 127.0.0.1:8080 --token-file /private/path/relay.key
```

Use a DNS name you control and a normal TLS certificate. Example [Caddy](https://caddyserver.com/docs/quick-starts/reverse-proxy) configuration:

```caddyfile
relay.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

The HTTPS frontend must pass WebSocket upgrades and allow long-lived connections. Configure its idle/read/write timeouts to accommodate the intended session TTL. Expose only the TLS frontend; bind the unauthenticated transport port to loopback or an isolated container network. The API itself requires authentication, but that is not a replacement for TLS.

[deploy/Caddyfile](../deploy/Caddyfile) and [deploy/Dockerfile](../deploy/Dockerfile) are examples. The Dockerfile deliberately builds from the project's Go toolchain instead of naming an unverified image version. Build with `docker build --build-arg GO_IMAGE=<approved-pinned-go-image> -f deploy/Dockerfile -t shellpier .`; supply the key as a read-only runtime mount. Do not bake it into an image.

Before giving a URL to users, run `doctor` from both networks, then an actual short-lived `share`/`connect` session. Confirm TTL expiry and explicit stop reject new connections. A health response proves HTTP reachability; it does not prove a complete SSH session.

## Operator responsibilities

Protect and rotate the creation key; distribute it only to people allowed to create sessions. A reverse proxy can add organization authentication, request limits and monitoring, but browser-only login may be incompatible with CLI WebSockets. Use an organization-approved machine authentication path. Do not bypass an authentication portal.

Bound session capacity and proxy connection limits, monitor process memory, and keep clock synchronization healthy. Service management is an operator choice; client commands do not install background startup hooks. Restarts end in-memory sessions. Avoid access-token query logging and do not log authorization headers. Revoke compromised sessions and rotate any exposed creation key.
