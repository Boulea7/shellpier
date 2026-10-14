# CLI 编排

按命令主题拆文件；`cli.go` 保留分发和公共帮助。

- 帮助先于文件读取、凭据生成与连网；输出使用传入流。
- 新命令同步 dispatch、help、completion 和公开命令文档。
- 诊断不输出票据秘密；显式凭据输出沿用既有契约。
- 重连不重放旧命令；stop/revoke 撤销访问，cleanup 只清理文件。
- 失败清理只处理本次创建且仍归本会话所有的文件。

相关测试：`go test ./internal/cli -run 'TestHelp|TestCompletion|TestRelayInviteHostConnectRevoke'`。
