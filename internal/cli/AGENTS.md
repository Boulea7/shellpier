# CLI 编排

按命令主题拆文件；`cli.go` 保留分发和公共帮助。

- 帮助先于文件读取、凭据生成与连网；输出使用传入流。
- 新命令更新 `schema.go` 的 commandSpecs 与 dispatch；schema 和补全共用该列表。参数使用 `parseFlags`，`--` 后内容保留为远端命令。
- 诊断不输出票据秘密；显式凭据输出沿用既有契约。
- exec JSON 保留独立 stdout/stderr 和退出码；PTY 输出是合并的原始字节流。
- terminal 状态、配置和快照使用本账户私有文件；read 保留绝对 cursor 与丢失字节信息。
- 重连不重放旧命令；stop/revoke 撤销访问，cleanup 只清理文件。
- 失败清理只处理本次创建且仍归本会话所有的文件。

相关测试：`go test ./internal/cli -run 'TestHelp|TestCompletion|TestSchema|TestExec|TestShell|TestTerminal|TestSessions'`。
