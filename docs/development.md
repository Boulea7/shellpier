# 如何添加新功能

先从用户能观察到的结果定义功能：输入什么、成功返回什么、失败时留下什么、何时结束。确认现有命令和替代方案不能满足，再选择改动入口。较大的 CLI、认证、平台或架构变化先开 issue；完整协作约定见 [CONTRIBUTING](../CONTRIBUTING.md)。

## 仓库地图

| 位置 | 职责 | 常见扩展 |
|---|---|---|
| `cmd/shellpier/main.go` | 信号 context、标准流、错误输出和进程退出码 | 通常无需为业务命令修改入口 |
| `internal/cli` | `cli.go` 分发/help；`host.go` 生命周期；`connect.go` / `invite.go` 连接与邀请；`inspect.go` / `doctor.go` 诊断；`transfer.go`、`keygen.go`、`completion.go` 等独立命令 | 新命令或参数、用户可见输出 |
| `internal/access` | 票据校验、受限读取、SSH host-key pin、连接和配置文件 | 复用连接能力，保持角色与凭据边界 |
| `internal/sshd` | 当前用户 SSH exec、SFTP 与会话关闭 | 服务端协议行为；不把它改成系统登录服务 |
| `internal/relay` | HTTPS/WSS、host/client 配对、认证、容量、TTL/撤销 | 连接协议或中继策略 |
| `internal/platform` | OS 文件权限、命令进程树、keep-awake | 使用平台文件/build tags 隔离差异 |
| `internal/netutil` | 地址、接口、实际路由与代理诊断 | 不把广告地址等同于实际绑定/路由 |
| `legacy` | 独立 PowerShell/Bitvise/Plink 路线及契约测试 | 旧 Windows 的独立变更与验收 |
| `docs`、`assets` | 公开说明、图形与品牌材料 | 跟随真实能力更新，不存内部计划 |
| `scripts` | 安装、启动、打包、文档/skill 验证 | 不偷偷修改全局环境或部署服务 |
| `.github` | CI、发行、社区模板 | 权限最小化，执行结果与配置分开说明 |
| `skills/shellpier` | Agent 操作流程与授权边界 | 遵循同一 CLI；不能额外授予远程操作权限 |

包之间复用已有明确能力，如 `access.Read`、`access.Dial`、`access.WriteConfig`。同包内的诊断摘要可参考 `ticketSummary`/`summarizeTicket`；需要跨包复用时先确认边界，避免为一次使用增加公共注册框架或扩大 API。以当前源码为准。

## 示例：增加一个 CLI 能力

假设需要一个读取远端状态的新命令。先确定它是否真的需要执行远端命令；若只需票据元数据，可以复用本地检查流程，避免连接。新能力放在 `internal/cli` 独立主题文件，测试放对应 `_test.go`，不持续堆入 `cli.go`。

处理函数沿用现有约定：接收 `context.Context`、参数和实际需要的输入/输出流，返回 `error`。`Run` 的签名为：

```go
Run(ctx context.Context, args []string, stdin io.Reader,
    stdout, stderr io.Writer, version string) error
```

命令通过传入流读写，便于嵌入和测试；不要直接调用 `os.Exit`、打印到全局 stdout 或使用 `context.Background()` 丢弃上游取消。入口统一处理返回错误，并保留已有远端退出码语义。

按现有 `flags` helper/参数解析模式实现，新参数须校验边界。注意仓库已有支持票据与 flag 顺序的命令：不要未经说明引入另一套无法兼容的参数规则。解析 help 应先于票据读取、文件创建或网络请求；`new-command --help` 应返回成功且无外部副作用。`Run` 已统一将 `flag.ErrHelp` 转为成功。

随后更新 `cli.go` 的命令分发和顶层 help，及 `completion.go` 的 `completionCommands`；若增加子命令或有明确补全价值的参数，同步四种 shell 的实际补全行为。帮助和补全生成不应读取凭据或自动修改 shell profile。

输出须适合人和自动化使用：正文走 stdout，诊断走 stderr；JSON 使用明确字段，不能直接序列化含私钥/token 的完整票据。`inspect` 的元数据摘要可以作为参考，但地址、ID 也可能需要用户脱敏。已有 `share` 的显式凭据输出契约不能成为新诊断命令泄漏秘密的理由。

需要 SSH 时复用 `access.Dial`，保留 host-key pin、TTL 校验和连接取消。超时从上游 context 派生，所有 client、文件与辅助 goroutine 在结束时关闭。断线后不自动重放命令，错误不能解释为“远端没有执行”。

## 文件与平台边界

`upload`/`download` 已有单文件传输能力，新功能应复用它们的连接与校验经验。写入目标必须明确覆盖政策：默认不覆盖、不跟随不受信任链接，失败清理只处理本次创建且仍可确认归属的文件。不要用无条件 `RemoveAll` 回滚用户目录。

本地路径用 `filepath`，远端 SFTP 路径遵循实际服务端约定；不要直接把 Windows 盘符路径当作 Unix 路径。覆盖空格、中文、空文件、现有目标、目录误用和权限失败。PowerShell 中票据写为 `'@access.json'`，当前目录程序使用 `./shellpier.exe`；CMD 和 Unix 示例分别检查引用规则。

Windows shell/命令行为应在实际 Windows 上测试，包括新进程首次启动时的行为、退出码与编码，而非仅依赖一个已初始化的终端。PowerShell 补全的版本要求与现代 bootstrap、Legacy PS2 是不同契约。不要因为编译成功就扩大旧 OS 的支持声明。

新增 OS 特有能力优先放进 `internal/platform` 或现有对应包的平台文件；没有明确需求就不加依赖。修改 keep-awake、服务或防火墙等系统状态时，先明确权限、归属和撤销路径，避免影响既有用户配置。

## 功能如何验证

为新逻辑选择能证明结果的测试：

- 成功路径验证输出与实际副作用，必要时走本地 endpoint；不仅检查返回 nil。
- 输入边界覆盖无效票据、过期、错误角色、路径与参数，以及帮助不读取票据/不连网。
- 错误路径覆盖认证/权限/连接失败，并检查不会泄漏秘密或损坏既有文件。
- 取消、超时和 TTL 覆盖等待及活动资源；并发能力跑 race，避免任意 sleep 形成不稳定测试。
- 文件传输参考 `transfer_test.go`，身份与断线行为参考现有 CLI/access/relay 测试；不重复实现同一契约。

常用验证命令如下，按受影响范围选择包；涉及多个包、认证或生命周期时扩大范围：

```sh
gofmt -w internal/cli/new_command.go internal/cli/new_command_test.go
go vet ./...
go test ./internal/cli
go test -race ./internal/cli
go build ./cmd/shellpier
python3 scripts/validate_docs.py
python3 scripts/validate_skills.py
```

以上 `new_command` 文件名是示例，不是仓库已有文件。并发重试验证可按需加 `-count=10`；仅改文字不要求跑全套 race。记录实际执行结果和未测试平台，CI 由实际 job 结果证明。

## 让功能可发现、可发行

同步默认中文 README、英文 README、[命令说明](commands.md)和相关工作流；示例须能在所标明 shell 上复制使用，不包含真实凭据。影响兼容性或安全边界时更新相应文档，涉及 Agent 流程时更新 skill。

打包由 `scripts/package.py` 收集公开文件。新增目录或资源后检查它是否进入发行包，并对解压后的包运行文档链接检查；checkout 链接通过不代表包内链接存在。不要带入内部协作文档、缓存或凭据。依赖、许可或支持平台发生变化时在 PR 中说明。

最后检查 diff，按独立主题提交，PR 说明问题、行为与验证。命令注册、测试、补全和文档应在可评审的改动中保持一致；尚未完成的网络部署或实机验收不能写为已交付。
