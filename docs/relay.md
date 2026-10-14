# 自托管中继

ShellPier 没有公共中继。团队运营者需要提供可信 HTTPS URL 和创建密钥，使用者才能建立跨 NAT 会话。先在本机验证中继，再部署 HTTPS。

## 1. 本机演练

在程序目录生成新密钥文件，然后运行中继：

```sh
./shellpier relay --init-key ./relay.key
./shellpier relay --listen 127.0.0.1:8080 --token-file ./relay.key
```

这两个命令分步执行：第一步生成文件后退出，第二步保持前台。另开终端：

```sh
./shellpier doctor --relay http://127.0.0.1:8080
./shellpier invite --relay http://127.0.0.1:8080 --token-file ./relay.key --ttl 5m --out ./relay-demo
```

再开两个终端分别执行：

```sh
./shellpier host @relay-demo/host.ticket
```

```sh
./shellpier connect @relay-demo/access.json -- whoami
./shellpier revoke @relay-demo/host.ticket
./shellpier connect @relay-demo/access.json -- whoami
./shellpier cleanup --dir ./relay-demo
```

撤销后新连接应失败。明文 HTTP 仅允许回环地址；两台机器或跨网络部署使用 HTTPS/WSS。

PowerShell 将 `./shellpier` 换成 `.\shellpier.exe`，给 `@relay-demo/host.ticket` 和 `@relay-demo/access.json` 加单引号。

## 2. 对外提供 HTTPS

常见部署是 ShellPier 监听回环地址，Caddy 在 443 提供 HTTPS。准备自己的域名、DNS 和正常 TLS 证书。ShellPier 保持上面的 `127.0.0.1:8080` 配置，Caddy 配置如下：

```caddyfile
relay.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

`relay.example.com` 需要替换为自己的域名。参见 [Caddy 反向代理文档](https://caddyserver.com/docs/quick-starts/reverse-proxy)和仓库[配置示例](../deploy/Caddyfile)。只暴露 HTTPS 前端，后端留在回环或隔离容器网络。反向代理须支持 WebSocket Upgrade，并给长连接保留合适的读写与空闲期限。

也可由 ShellPier 原生提供 HTTPS：

```sh
./shellpier relay --listen 0.0.0.0:8443 --token-file ./relay.key --tls-cert ./fullchain.pem --tls-key ./privkey.pem
```

证书与私钥参数必须同时提供。证书必须受客户端信任，不能通过关闭 TLS 验证解决证书错误。

从目标端和控制端分别运行：

```sh
./shellpier doctor --relay https://relay.example.com
```

随后按[邀请工作流](workflows.md#3-跨网络控制端发邀请)执行真正的 `host → connect`，验证 TTL 和撤销后连接拒绝。健康检查成功只说明 HTTP 路径通，不说明 WSS、SSH 或双方网络都通。

## 3. Docker 从源码构建

[Dockerfile](../deploy/Dockerfile) 会编译项目源码，必须先取得完整源码 checkout；发行 ZIP 的二进制和文档不能直接充当构建上下文。

```sh
git clone https://github.com/Boulea7/shellpier.git
cd shellpier
docker build --build-arg GO_IMAGE='golang:1.27.2' -f deploy/Dockerfile -t shellpier .
```

源码工具链下限以 [go.mod](../go.mod) 为准；现有发行二进制使用 Go 1.27.2。

镜像从 scratch 运行，使用 UID/GID 65532。将创建密钥作为只读文件挂载到 `/run/secrets/relay.key`，保证该 UID 可读，并只把后端端口映射到本机回环：

```sh
docker run --rm --name shellpier-relay -p 127.0.0.1:8080:8080 --mount type=bind,src=/实际私有目录/relay.key,dst=/run/secrets/relay.key,readonly shellpier
```

宿主机 Caddy 再代理到 `127.0.0.1:8080`。示例以前台运行便于检查，服务管理与重启策略由运营者单独配置。密钥放在镜像外，更新镜像无需把它重新打包。

## 运行参数与边界

| 参数 | 默认值 / 用途 |
|---|---|
| `--token-file FILE` | 创建会话所需的私有密钥文件，也可使用 `SHELLPIER_RELAY_KEY` |
| `--max-sessions 64` | 内存会话数量上限 |
| `--max-ttl 24h` | 普通会话最大期限 |
| `--allow-persistent` | 明确允许客户端请求 `--ttl 0` |
| `--tls-cert` / `--tls-key` | 原生 HTTPS 证书与私钥 |

创建密钥只能交给允许创建会话的人；每个会话的 host/client 角色票据不同。SSH 内容在两端加密，中继仍可观察连接来源、时长等元数据。中继重启会丢失所有内存会话，包括长期会话。

客户端网络重连不恢复已丢失的中继会话，也不重放命令或传输。会话默认有效期为 3 小时；重试参数见[命令手册](commands.md)，机制见[工作流](workflows.md#长期运行与断线)。

组织认证必须兼容 CLI 的 HTTPS/WSS，浏览器门户登录不一定适用。HTTP CONNECT 代理需要网络和认证方式适配；不自动支持 PAC、NTLM/Kerberos 或继承浏览器登录态。确保应用和反向代理不记录 Authorization 或票据。发生凭据泄露时撤销对应会话、更新受影响密钥。
