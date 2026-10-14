# 内置 SSH 服务

提供进程所属 OS 用户的 exec、PTY/SFTP；不支持 PAM、账户切换或文件系统沙箱。

- 保留授权公钥校验与握手超时。
- PTY request、env、工作目录与启动顺序须校验；window-change 与 signal 操作正在运行的同一会话。
- 能力查询报告目标机实际可用功能，不从客户端 OS 推断。
- context、TTL 与会话关闭须结束连接及受管理命令进程。
- Windows Job Object 的行为需在 Windows 验证。

相关测试：`go test ./internal/sshd -run 'TestExecAndSFTP|TestTTLStopsCommandTree|TestSessionCloseStopsCommand|TestPTY|TestAuthenticatedCapabilities'`。
