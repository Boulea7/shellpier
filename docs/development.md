# 如何添加新功能

较大的 CLI、认证、平台或架构变化先开 Issue；协作约定见 [CONTRIBUTING](../CONTRIBUTING.md)。修改前读取所在模块的 `AGENTS.md`。Claude Code 通过同目录的 `CLAUDE.md` 导入这些规则，共享约定只维护一份。

## 仓库地图

| 位置 | 职责 |
|---|---|
| `cmd/shellpier/main.go` | 信号取消、标准流、错误输出与退出码 |
| `internal/cli` | 命令分发、机器接口、持久终端与会话生命周期 |
| `internal/access` | 票据、SSH 连接、主机公钥校验与配置生成 |
| `internal/sshd` | 当前用户 SSH exec、PTY/SFTP、目录文件共享与转发 |
| `internal/jobs` | host 生命周期内的有界任务、去重与输出缓冲 |
| `internal/relay` | HTTPS/WSS、角色配对、认证、容量、TTL 与撤销 |
| `internal/platform` | 文件权限、Unix PTY/Windows ConPTY、命令进程树与防睡眠 |
| `internal/netutil` | 地址、接口与网络诊断 |
| `legacy` | 独立 PowerShell/Bitvise/Plink 路线及契约测试 |
| `scripts` | 下载、启动、打包与文档/skill 校验 |
| `docs`、`assets` | 产品文档与图形 |
| `.github` | CI、发行与社区模板 |
| `skills/shellpier` | Agent 操作流程 |

## 增加命令或参数

新命令放在 `internal/cli` 的独立主题文件，对应测试放在 `_test.go`。处理函数接收 context、参数和所需流，返回 error；入口的签名为：

```go
Run(ctx context.Context, args []string, stdin io.Reader,
    stdout, stderr io.Writer, version string) error
```

使用传入的流输出，由入口处理退出码。使用 `flags` 创建参数集、`parseFlags` 支持选项与位置参数混排；远端命令通过 `splitCommandArgs` 保留 `--` 之后的原始参数；帮助必须先于读取票据、创建文件与网络请求。`Run` 将 `flag.ErrHelp` 转为成功。

新命令在 `schema.go` / `schema_features.go` 的 `commandSpecs` 注册 usage、flags、effects 和 stdio/output 模式；`schema` 与补全从同一列表读取。同步 `cli.go` 分发、顶层帮助和公开命令说明。MCP 工具从相同 schema 生成，但还须在 `mcp_tools.go` 显式纳入允许列表；有界执行、取消、输出上限和敏感数据处理通过审查后才开放。正文走 stdout，诊断走 stderr；全局 `--json` 的错误由 `Run` 统一输出，命令返回 error，不重复打印错误 JSON。JSON 摘要不直接序列化含私钥或 token 的票据。

票据与配置复用 `access.Read`、`access.WriteConfig`；需要 SSH 时复用 `access.DialRetry`，保留主机公钥校验、TTL 与取消。超时从上游 context 派生，结束时关闭连接、文件与辅助 goroutine。重试只作用于建连，不自动重放远程命令。

exec 保留 stdout/stderr、退出码和输出截断信息；shell 使用 PTY 合并字节流。持久 terminal 通过本机私有控制接口操作同一会话，read 使用绝对 cursor，报告缓冲丢失字节；wait 等待整个终端结束。输入 operation ID 的去重记录覆盖 worker 生命周期，满表拒绝新 ID。jobs 绑定 host context，保留已完成任务以防 ID 被再次执行；SSH 断开不取消 jobs，host 结束则取消。输出和 ID 均有明确容量上限。机器能力从目标端 `capabilities` 查询，不能用本机 schema 的平台信息代替。

## 文件与平台行为

写入功能明确覆盖政策，失败只清理本次创建且仍能确认归属的文件。传输实现参考 `transfer*.go`；本地路径用 `filepath`，远端路径遵循 SFTP 约定。续传 journal 绑定方向、主机公钥、规范路径和源内容，提交保持不覆盖；硬链接不可用时用独占复制并报告非原子可见。目录传输先检查容量、路径冲突和特殊文件。普通传输直接复制，不增加全文件 hash 门槛；`--skip-existing` 跳过已有普通文件，不比较内容，结构冲突仍报错。仅续传在内部绑定原始来源与 partial，用户无需手动计算摘要。服务管理仅操作可验证归属的用户级资源，plan/dry-run 不修改状态。

`--root DIR` 自动选择目录限定的 SFTP 模式，虚拟路径从 `/` 开始，同时关闭 exec、PTY、jobs 和转发。它不创建 OS shell 沙箱，不隔离目录内的挂载或既有硬链接别名。普通 full 模式默认提供有界 jobs 与 TCP 转发；可选参数用于有明确需要的限制，不在普通流程额外增加许可步骤。正向、反向、SOCKS5 CONNECT 与 `proxy --to` 复用 SSH 生命周期及服务端目的地址限制，连接中断不重放字节。SOCKS5 只协商无认证 CONNECT，不增加 UDP 或路由控制。流传输以 `-` 指代 stdin/stdout；下载 stdout 不得混入 JSON、进度或普通日志，上传继续独占新建目标。

OS 专有能力放在 `internal/platform` 或对应包的平台文件，通过 build tags 隔离。涉及服务、防火墙或防睡眠时明确权限、资源归属和恢复方式。Windows 命令测试包括首次启动、退出码与编码；Legacy 保持独立入口。

## 验证

按改动范围覆盖成功、参数边界、错误与取消路径，检查实际副作用、凭据泄漏和既有文件保护。并发或生命周期变化运行 race。机器接口参考 `schema_test.go`，参数混排与 exec/PTY 参考 `execution_test.go`；持久终端需覆盖 worker 生命周期、输入串行化和环形缓冲 cursor，会话发现参考 `sessions_test.go`。传输参考 `transfer_test.go`，身份与断线行为参考 CLI/access/relay 现有测试。

从仓库根选择受影响的包运行：

```sh
go vet ./...
go test ./internal/cli
go test -race ./internal/cli
go build ./cmd/shellpier
python3 scripts/validate_docs.py
python3 scripts/validate_skills.py
```

修改后的 Go 文件运行 `gofmt`。跨包变更扩大测试范围；仅改文档运行相应校验即可。

## 文档与发行

同步中文与英文 README、[命令说明](commands.md)和相关工作流；Agent 流程变化同步 skill。平台与权限变化更新[兼容性说明](compatibility.md)或[安全策略](../SECURITY.md)。

`scripts/package.py` 收集发行文件；新增目录或资源时更新打包范围，并校验解压后的包：

```sh
python3 scripts/validate_docs.py --root /path/to/unpacked-release
```

发行包不包含凭据、缓存或内部协作文档。依赖、许可或支持平台变化在 PR 中说明。

Windows 构建使用隔离的 Go 1.27.2 副本回补官方运行时线程状态判断，不修改本机已安装 Go。原因、对照测试、复现方法与移除条件见 [Windows 运行时调查](windows-runtime.md)；不要把修复已知缺陷写成已确认原事故根因。

多监听 forward 必须先全部绑定再报告 ready，任一绑定失败关闭已绑定监听；目的服务拒绝或单 stream 失败不取消整个转发，SSH 失联/TTL 才结束全组。文件清单的格式、容量、相对路径、大小写和源目标检查保持跨平台一致，预检后执行复检不宣称整批事务。terminal tail 的原始 stdout 与 MCP 协议流分离，取消读取不取消终端。host 托管可重连 PTY 需要独立 manager、状态恢复、输入去重、TTL 与 Windows ConPTY 生命周期设计，不能通过 ControlPersist 或重启客户端替代。
