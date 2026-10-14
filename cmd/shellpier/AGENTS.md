# 进程入口

此模块负责信号 context、标准流、版本注入和退出码；业务命令放在 `internal/cli`。

- Ctrl+C/SIGTERM 取消传递到 `cli.Run`。
- 保留远端 SSH 退出码语义，业务包不调用 `os.Exit`。
- `main.version` 是发行打包的 ldflags 注入点。

相关测试：`go test ./cmd/shellpier`。
