# 如何添加新功能

较大的 CLI、认证、平台或架构变化先开 Issue；协作约定见 [CONTRIBUTING](../CONTRIBUTING.md)。修改前读取所在模块的 `AGENTS.md`。

## 仓库地图

| 位置 | 职责 |
|---|---|
| `cmd/shellpier/main.go` | 信号取消、标准流、错误输出与退出码 |
| `internal/cli` | 命令分发、帮助、输出与会话生命周期 |
| `internal/access` | 票据、SSH 连接、主机公钥校验与配置生成 |
| `internal/sshd` | 当前用户 SSH exec、SFTP 与会话关闭 |
| `internal/relay` | HTTPS/WSS、角色配对、认证、容量、TTL 与撤销 |
| `internal/platform` | 文件权限、命令进程树与防睡眠 |
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

使用传入的流输出，由入口处理退出码。沿用 `flags` helper 和相应命令的参数解析规则；帮助必须先于读取票据、创建文件与网络请求。`Run` 将 `flag.ErrHelp` 转为成功。

同步更新 `cli.go` 的分发与顶层帮助、`completion.go` 的 `completionCommands`，以及新增参数的 shell 补全。正文走 stdout，诊断走 stderr；JSON 摘要参考 `ticketSummary`/`summarizeTicket`，不直接序列化含私钥或 token 的票据。

票据与配置复用 `access.Read`、`access.WriteConfig`；需要 SSH 时复用 `access.DialRetry`，保留主机公钥校验、TTL 与取消。超时从上游 context 派生，结束时关闭连接、文件与辅助 goroutine。重试只作用于建连，不自动重放远程命令。

## 文件与平台行为

写入功能明确覆盖政策，失败只清理本次创建且仍能确认归属的文件。传输实现参考 `transfer.go`；本地路径用 `filepath`，远端路径遵循 SFTP 约定。测试覆盖空格、中文、空文件、已有目标、目录误用与权限失败。

OS 专有能力放在 `internal/platform` 或对应包的平台文件，通过 build tags 隔离。涉及服务、防火墙或防睡眠时明确权限、资源归属和恢复方式。Windows 命令测试包括首次启动、退出码与编码；Legacy 保持独立入口。

## 验证

按改动范围覆盖成功、参数边界、错误与取消路径，检查实际副作用、凭据泄漏和既有文件保护。并发或生命周期变化运行 race；传输参考 `transfer_test.go`，身份与断线行为参考 CLI/access/relay 现有测试。

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
