# OS 适配

用平台文件/build tags 隔离文件安全、shell、Unix PTY/Windows ConPTY、进程树和 keep-awake。

- 权限收紧失败返回错误；保留 Windows ACL 与 Unix 文件归属检查。
- 子进程停止依赖 Windows Job Object / Unix 进程组，不是恶意进程沙箱。
- PTY resize、signal 与 close 须处理并发；Windows ConPTY 不可用时返回明确错误，不退回普通管道冒充终端。
- keep-awake 只释放本次资源，不修改用户电源计划或系统服务。

权限、shell 与进程树在对应系统验证；关联包为 `internal/platform`、`internal/sshd` 和 `internal/cli`。
