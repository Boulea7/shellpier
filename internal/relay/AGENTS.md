# Relay 传输与会话

中继配对 host/client，不解密 SSH 负载。

- 角色凭据分离；认证信息不转发到重定向目标。
- 外部 URL 使用 HTTPS，仅允许受限的 loopback HTTP 例外。
- 撤销与到期关闭活动和等待连接；容量限制也覆盖等待资源。
- 持久会话须由中继管理员显式允许。

相关测试：`go test ./internal/relay -run 'TestRoleAuthentication|TestTTL|TestRevoke|TestRedirect|TestWSS'`。
