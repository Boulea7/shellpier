# 票据与 SSH 客户端

- access/host 角色与各自密钥不得互换；校验期限、格式和输入大小。
- 主机公钥必须严格匹配，不提供跳过校验路径。
- SSH 配置使用独立 identity/known_hosts/manifest，不编辑全局配置。
- 回滚保留无关文件及已被替换的文件。

相关测试：`go test ./internal/access -run 'TestDial|TestInvalidTickets|TestConfigRollback'`。
