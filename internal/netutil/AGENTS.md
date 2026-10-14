# 网络地址与接口

不自动修改路由、防火墙或代理配置。

- 广告地址、监听地址和对端可达性是不同信息。
- 平台接口约束放在对应 build-tag 文件，未知接口明确失败。
- 代理诊断不打印用户名、密码或完整 URL。
- HTTPS health 不代表 WSS/SSH 连接成功。

相关测试：`go test ./internal/netutil -run 'TestInvalidInterface|TestInspectProxyPrivacy|TestInterfaceAddressFiltering'`。
