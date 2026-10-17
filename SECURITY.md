# 安全策略

简体中文 · [English](SECURITY.en.md)

ShellPier 以目标机当前用户运行。默认 full 模式可执行命令、操作文件、运行有界后台任务和转发 TCP；收到访问票据的人获得相应账户能力。票据、私钥和中继创建密钥应私下保管。

## 报告漏洞与支持版本

使用 [GitHub 私密漏洞报告](https://github.com/Boulea7/shellpier/security/advisories/new)，提供版本、系统、脱敏复现和影响，不附有效凭据。安全修复面向当前开发线与[最新 alpha](https://github.com/Boulea7/shellpier/releases)，平台范围见[兼容性说明](docs/compatibility.md)。

## 实际边界

- SSH 对命令和文件流量端到端加密，并固定会话主机公钥；TLS 保护到中继的连接。中继能看到连接元数据，也能中断服务。
- 普通命令继承当前 OS 账户权限，`--work-dir` 不是文件系统隔离。默认 jobs 随 host 或 TTL 结束；`--allow-jobs=false` 可关闭。默认 TCP 转发不需要目标许可列表，`--allow-forward` 仅用于可选限制。
- `--root DIR` 自动切换为 SFTP 目录共享，路径从虚拟 `/` 开始；服务端拒绝 exec、PTY、jobs 和转发。无需管理员，但它不是 OS shell 沙箱，不隔离目录内挂载或既有硬链接别名。可选只读模式用于明确需要禁止写入的场景。
- 正向转发在控制端监听，反向转发在目标端 loopback 监听并访问控制端服务；标准 SSH `-R` 也可用。SSH 断开会关闭转发，不自动重放数据。SOCKS5 仅支持无认证 CONNECT，loopback 上其他本机进程也可能连接；正向 SOCKS5 的域名在目标端解析，反向 SOCKS5 在控制端解析。`proxy --to` 使用同一目的地址访问边界。通过转发可访问的服务仍有自己的账户与认证边界。
- 默认不覆盖已有文件，`--skip-existing` 跳过已有普通文件而不比较内容。续传使用本次拥有的 partial/journal，内部绑定来源；失败时可能保留明文文件和路径，按实际需要清理。普通传输不提供额外 SHA 验证保证。
- PTY 输出合并 stdout/stderr，可能含控制序列；terminal 快照和可选日志保存在控制端私有目录，均可能包含敏感信息。关闭后文件不自动消失，`terminal wait` 只表示整个终端结束。
- jobs start 与 terminal write 的操作 ID 只在对应 manager/worker 生命周期内去重，记录有容量上限。未知结果不代表没有执行；先查状态和副作用，不以新 ID 猜测重试。
- MCP 通过 stdio 提供明确工具集，不提供 Web 前端。工具超时或取消不回滚远端副作用。targets 引用现有私有票据，不复制或续期；用户级 service 的注册、自启需显式选择，不提升权限或延长票据。
- OpenSSH 连接复用默认关闭；启用后先结束 master 再清理配置。`cleanup` 只删除生成文件，不撤销已经运行的连接。结束共享用 Ctrl+C、stop 或 revoke；TTL 不回滚命令，也不删除系统或网络审计。
- 崩溃、SIGKILL 或断电可能留下状态与凭据；Unix 子进程可逃离受管理进程组。Legacy 到期受计划任务、供电和时钟影响。

v0.8 开发线：host 托管 `terminals` 的 PTY/有界输出和 operation 记录保存在目标进程中，SSH 断线可继续，host 退出或 TTL 结束即失效；同票据持有者可控制同一终端，不能归因到不同的人。旧 `terminal` 的本地快照行为保持。反向 SOCKS5 在控制端解析和出站，`--allow-forward` 仅限制目标端正向出站。`files copy` 经控制端 SFTP 创建独立副本，失败可留目标部分文件，不按不稳定路径身份盲删。

普通故障见[获取帮助](SUPPORT.md)。
