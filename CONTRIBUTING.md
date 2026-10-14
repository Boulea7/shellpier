# 参与贡献

欢迎修正文档、改善错误提示、补充可复现的故障案例和实现小范围改进。先阅读 [README](README.md)、[兼容性边界](docs/compatibility.md)和相关代码；添加能力见[开发指南](docs/development.md)。使用过程中遇到问题见 [SUPPORT](SUPPORT.md)，安全漏洞走[私密报告](SECURITY.md)。

## 第一次贡献

1. 搜索现有 issue/PR，避免重复工作。可以从文档错误、命令示例和明确的缺陷开始，不必等待某个标签。
2. Fork 仓库并创建主题分支。开发需要 Go 1.26+，具体以 `go.mod` 为准；Python 3 用于 skill 校验与打包。
3. 用最小改动解决明确问题，补充适当验证，提交 PR。较大的功能、公开 CLI、认证、平台支持或架构改动，先通过 issue 说明场景、预期行为与替代方案。

不要为简单修正增加依赖或泛化框架。复用现有入口与依赖，保留当前用户权限、固定主机公钥、角色分离、到期和撤销边界。新增依赖应解释用途、许可和维护影响。

## 按改动选择验证

| 改动 | 最小验证 |
|---|---|
| 文档、社区模板 | `python3 scripts/validate_docs.py`，检查格式、命令与实际 `--help` 是否一致；不要求仅改文字就跑全部 Go 测试 |
| Agent skill | `python3 scripts/validate_skills.py`，检查授权、秘密处理与真实发现能力；格式通过不代表 Agent 已加载 |
| Go 逻辑 | `gofmt` 修改文件，`go vet ./...`、受影响包的 `go test`，构建 `go build ./cmd/shellpier`；逻辑/安全修复补有意义的回归测试 |
| 并发、连接、取消、TTL | 受影响包运行 `go test -race`，按需 `go test -count=10`；覆盖活动和等待连接 |
| 平台代码 | 对应 OS 的实际 smoke test；交叉编译只能证明编译。无法实测时明确写出缺口 |
| Legacy PowerShell | parser 与 `legacy/tests/core-contract.ps1`、`legacy/tests/bitvise-contract.ps1`；涉及服务、任务或防火墙时还须对应 Windows 验收 |
| 安装/打包/CI | 校验相关脚本、产物、checksum 与实际工作流结果；不要把配置存在写成执行通过 |

CI 会执行更广的检查。PR 列出你实际运行的命令和结果，说明失败、未执行检查和平台缺口。不要为证明正确而加入仅复述实现或文档的测试。

## 提交与评审

提交按独立主题拆分，标题直接描述变化；可以使用 `fix:`、`feat:`、`docs:`，不强制。提交前检查 `git diff --cached`，不要包含凭据、登录态、访问票据、真实用户标识、内部协作文档或无关改动。

PR 应说明具体问题、修改后行为、验证和兼容性影响。默认使用中文交流；代码注释和 docstring 使用英文。文档示例使用占位地址，公开日志先脱敏。使用 AI 辅助的贡献也需要贡献者理解代码、核验输出并承担相同的质量责任。

当前维护者为 [Boulea7](https://github.com/Boulea7)，负责范围取舍、评审、合并与发布。提交 PR 不保证接收，也没有固定评审或修复时限；维护者可要求缩小范围或补充证据。尚未合并的改动不构成产品承诺。

贡献按仓库 [MIT 许可](LICENSE)分发；参与讨论与评审须遵守[社区行为准则](CODE_OF_CONDUCT.md)。
