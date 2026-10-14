# 独立 Legacy Windows 路线

保持 Windows PowerShell 2.0/.NET 3.5 兼容语法；现代 Go 程序与下载脚本不是此模块入口。

- Bitvise 使用独立实例，保留已有服务、账户和无关防火墙规则。
- 验证安装包与 Plink 的厂商签名；许可和管理员操作须显式确认。
- TTL/stop 只撤销本实例资源；输出 reverse 命令不代表已连接。

从仓库根用 PowerShell 运行：

```powershell
./legacy/tests/core-contract.ps1
./legacy/tests/bitvise-contract.ps1
```

安装、首次登录、计划任务到期和清理需在对应旧系统验证。
