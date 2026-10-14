# ShellPier 开发导航

- 用户入口：`README.md`；开发流程：[docs/development.md](docs/development.md)。
- 协作与安全：[CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)。
- `cmd/shellpier`：进程入口、取消与退出码。
- `internal/cli`：命令编排、帮助、输出与会话生命周期。
- `internal/access`：票据、SSH 客户端与隔离配置。
- `internal/sshd`：当前用户 SSH exec/SFTP。
- `internal/relay`：WSS 传输、角色配对与撤销。
- `internal/platform`：权限、进程树与平台差异。
- `internal/netutil`：接口、地址与网络诊断。
- `legacy`：独立旧 Windows 路线。
- `scripts`：安装、打包与公开资源校验；`docs`：产品文档。

按模块读取对应 `AGENTS.md`。工具链以 `go.mod` 为准，以下命令从仓库根运行：

```sh
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/shellpier
python3 scripts/validate_docs.py
python3 scripts/validate_skills.py
```

日常修改运行受影响包的测试；并发和撤销改动加 `-race`。平台支持需在对应系统验证。
