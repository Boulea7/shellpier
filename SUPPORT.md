# 获取帮助

ShellPier 是 alpha 阶段的开源项目，由 [Boulea7](https://github.com/Boulea7)维护。仓库 issue 用于可复现问题和产品建议，不是紧急运维服务；不承诺回复或修复时限。

## 先定位失败在哪一层

1. 阅读[安装指南](docs/install.md)，确认版本、OS、架构、入口与文件路径。Windows 7/8 使用独立 [Legacy 路线](docs/legacy-windows.md)。
2. 运行 `doctor`，需要中继时添加你的可信 relay URL。按系统/路由 → HTTPS → WSS upgrade/代理 → 角色认证 → SSH 身份 → 命令/SFTP 分层判断。doctor 成功不等于已经登录。
3. 对照[排障指南](docs/troubleshooting.md)和[兼容性边界](docs/compatibility.md)。校园认证门户须正常登录；没有预置免费公共 relay，现有服务的部署须取得授权。
4. 命令响应丢失时先核查实际副作用，不直接重复操作。结束后用 stop/revoke 并尝试新连接，凭据 cleanup 本身不撤销访问。

## 提交问题

先搜索[现有 issue](https://github.com/Boulea7/shellpier/issues)。使用 Bug 表单，附 CLI 版本、系统版本、架构、连接方式、最小复现、预期/实际行为及脱敏诊断；明确哪些平台或步骤没有验证。功能建议说明具体场景和现有替代方案。

不要原样粘贴命令输出：`share` 未指定输出文件时可能直接打印秘密；访问票据、`access.json`、私钥、创建密钥、授权头、代理凭据与个人标识都须移除。使用占位地址与测试账户描述复现，不上传真实连接材料。公共 issue 会被公开检索。

疑似漏洞通过 [GitHub 私密报告](https://github.com/Boulea7/shellpier/security/advisories/new)，不要放到普通故障表单；详见 [SECURITY](SECURITY.md)。愿意修复问题请阅读 [CONTRIBUTING](CONTRIBUTING.md)，讨论时遵守[行为准则](CODE_OF_CONDUCT.md)。
