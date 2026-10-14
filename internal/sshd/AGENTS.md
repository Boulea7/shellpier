# 内置 SSH 服务

提供进程所属 OS 用户的 exec/SFTP；不支持 PTY、PAM、账户切换或文件系统沙箱。

- 保留授权公钥校验与握手超时。
- context、TTL 与会话关闭须结束连接及受管理命令进程。
- Windows Job Object 的行为需在 Windows 验证。

相关测试：`go test ./internal/sshd -run 'TestExecAndSFTP|TestTTLStopsCommandTree|TestSessionCloseStopsCommand'`。
