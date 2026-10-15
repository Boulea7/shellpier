# Windows 运行时调查与构建补丁

[文档首页](README.md) · [兼容性](compatibility.md) · [开发指南](development.md)

更新日期：2026-10-16。Windows 构建回补了一项已经合入 Go 上游的运行时修复。它消除已知缺陷；原 v0.5 CI 崩溃的历史根因仍无法确认，不能把两者视为已经建立因果关系。

## 已知问题与证据边界

v0.5 的 [CI 运行 38721298937](https://github.com/Boulea7/shellpier/actions/runs/38721298937/job/116376231925) 在 `net/http.Transport.prepareTransportCancel` 报 `fatal error: concurrent map writes`。应用代码复查没有发现复制已使用的 Transport、全局修改 Transport 或 Windows unsafe 写坏内存的证据；这也不足以证明应用绝无缺陷。旧日志没有保留当时的 CPU、寄存器与锁状态，无法追认崩溃机制。

Go 已确认 Windows 线程在处理异常（包括缺页）或系统调用时，异步抢占修改线程上下文可能导致崩溃。上游修复请求线程异常状态报告，仅在状态明确安全时注入异步抢占，其他情况正常恢复线程、稍后重试。[Go 官方修复](https://github.com/golang/go/commit/0042b271db9c51f90d1887435026562a0778d7d9) · [问题 #79249](https://github.com/golang/go/issues/79249) · [CL 828906](https://go-review.googlesource.com/c/go/+/828906)

该缺陷与历史现象具有调查相关性，但相似错误和重复测试通过不能证明原事故由它引起。

## 本项目的处理

Windows 构建使用 Go 1.27.2 的隔离副本，回补上游提交 `0042b271db9c51f90d1887435026562a0778d7d9` 的线程状态判断，并保留发行分支原有的抢占条件。只修改运行时的 Windows 文件，不全局关闭异步抢占；Linux/macOS 不使用此 Windows 补丁。

构建包装与诊断脚本不修改开发者已经安装的 Go。Windows ZIP 随包保留工具链来源记录；`version --json` 的 `runtime_patch` 标识上游提交。这里的摘要与清单由构建自动生成，用户正常运行无需手工校验或配置补丁。Go 原有 BSD 许可证见 [第三方声明](../THIRD_PARTY_NOTICES.md)。

## 对照测试与复现方法

诊断同时运行原版 Go、原版加 `GODEBUG=asyncpreemptoff=1`、回补官方判断的 Go，交错启动冷进程，保存 CPU/OS、环境、命令、退出码和完整失败日志。负载包含运行时 canary、重复 `TestHostRetry` 与完整 CLI 测试；关闭抢占只用于诊断对照，不是发行版默认配置。

- [初轮诊断 38778611721](https://github.com/Boulea7/shellpier/actions/runs/38778611721)：EPYC 7763 上三组各 24 个进程、每进程 50 次 HostRetry，均未复现。
- [增强诊断 38779987839](https://github.com/Boulea7/shellpier/actions/runs/38779987839)：两台 7763、两台 9V45 上，已完成的 278 个 canary 进程（原版 93、关闭抢占 92、补丁 93）和 206 个 HostRetry 进程（69/68/69，每进程 50 次）均未复现。该工作流整体失败来自诊断脚本的 Windows 文件占用清理与工作目录问题，不能计作原运行时错误复现，也不能计作完整测试通过。
- [修正后的诊断 38781098758](https://github.com/Boulea7/shellpier/actions/runs/38781098758)：修正工作目录、派生构建的工具链与 Windows 映像清理后重新采集。完整结果与每台机器的环境保存在工作流 artifacts 中。

维护者可在原生 Windows、已安装 Go 1.27.2 与 Python 3 的源码目录运行：

```powershell
python scripts/windows_diagnostics.py --rounds 24 --count 50 --full-rounds 3 --blob-mib 96 --out "$env:TEMP/shellpier-runtime-diagnostics"
```

也可手动触发仓库的 Windows diagnostics 工作流，读取各 runner 的 artifacts。不要只重跑失败项后省略首轮日志；结果用于描述覆盖范围，零次复现不是原事故归因证明。

## 移除条件

官方稳定版 Go 包含同一修复后，升级工具链，验证 Windows 冷启动、CLI 与跨平台发行包，再移除本地回补和相应构建标记。保留这份历史记录及发行来源，不长期维护独立运行时分支。
