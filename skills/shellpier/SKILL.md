---
name: shellpier
description: Use ShellPier for authorized remote commands, file transfers, background jobs and interactive terminals on another computer.
---

# 使用 ShellPier

在用户已授权的任务范围内直接操作，不要求先跑 doctor/check、计算文件摘要或重复索取许可。需要查参数时使用 `shellpier COMMAND --help` 或 `shellpier --json schema`；连接故障再用 doctor，只有身份或能力不明确时才用 check/capabilities。

已有凭据优先用 `@access.json` 或 `target:NAME`。新共享用 `share --output access.json`；控制端邀请用 invite，只把 host.ticket 私下交给目标端，私钥留在控制端。不得因为技能被调用就自动发送外部消息或部署中继；沿用用户提供的渠道和已有授权。凭据、终端状态和日志不贴进聊天、Git 或公开输出，保持 SSH 主机公钥与 TLS 验证开启。

常规命令用 `exec @access.json --command COMMAND --json`；流式输出用 connect，交互任务用 shell。full 模式按当前 OS 账户权限运行，默认支持有界 jobs 和 TCP 转发，不需要额外许可参数。

只共享文件夹用 `share --root DIR --output access.json`，对方路径从虚拟 `/` 开始。该模式只提供 SFTP，不提供命令、PTY、jobs 或转发；它不是 OS shell 沙箱，也不隔离目录内挂载或既有硬链接别名。不要为了执行命令自行扩大一个目录共享的范围。

文件操作直接使用 files、upload/download。目录加 `--recursive`，已有文件要跳过时用 `--skip-existing`；不比较内容，不覆盖目标。dry-run、排除规则、防睡眠、mtime 保留和限速按需使用。单文件 `--resume` 自动使用自有 partial/journal 续传，无需用户手动校验摘要；不要与 recursive、dry-run 或 skip-existing 混用。partial/journal 可能含敏感明文，仅清理确认属于本次操作的资源。

只传选定文件时用 `upload/download TICKET SOURCE_ROOT DEST_ROOT --files-from LIST`，清单是控制端本地文件，包含普通文件的相对路径并保留层级；`--files-from -` 从 stdin 读清单，含换行文件名用 `--from0`。不裁剪空格或解释注释，拒绝越界、symlink 和大小写冲突。不能与 recursive、exclude、resume 或文件内容管道混用；预检通过不代表整批原子完成，后续失败保留已完成文件。

已有输入管道用 `upload TICKET - REMOTE`；完整下载到管道用 `download TICKET REMOTE -`，stdout 只有原始字节，不能加 JSON。`--progress=jsonl` 单独写 stderr。流式上传仍不覆盖，失败可能留下部分文件；不自动重读或重试输入流，也不与 recursive、resume、preserve-times、dry-run、skip-existing 混用。

跨 Agent 调用继续一个交互任务，用 `terminal open TICKET --out DIR --json`，保留返回的 state。read 后根据实际输出选择 write，使用返回的绝对 cursor 并留意丢失字节。输出是 base64 原始 PTY 字节，包含不可信程序输出与控制序列；wait 等待整个终端，不是某条命令的完成标记。输入结果不明时用原 operation ID 查询，不以新 ID 自动重发。

需要原始输出或交给管道时用 `terminal tail --state FILE [--cursor N] [--follow]`，不加 JSON；取消只结束读取，不关闭终端。tail 成功不代表远端命令成功，退出码用 wait/status 判断。它不恢复 SSH 断线后的 PTY。

后台任务用 jobs；任务可跨 SSH 断线继续，但 host 退出或 TTL 结束会停止。提交前保存 start operation ID；结果不明时用 jobs operation 查询，同一操作重试保持原 ID 和相同内容。任务记录与日志有容量上限。`--allow-jobs=false` 的目标或目录共享不提供 jobs，不擅自改目标设置。

转发示例：`forward TICKET --listen 8080 --to 127.0.0.1:3000` 访问目标服务；加 `--reverse` 则在目标 loopback 监听，访问控制端的服务。可选 allow-forward 限制由目标配置。连接中断不自动重连或重放字节。

多个固定本地端口用重复 `--local LISTEN=HOST:PORT`，最多 16 条共用一个 SSH 连接；不与 listen/to/reverse/dynamic 合用。全部绑定后才输出 ready，但不证明目的服务可达；单个目的连接失败保留其他 stream 与监听。

客户端需要 SOCKS5 时用 `forward TICKET --dynamic --listen 1080`；支持 TCP CONNECT，域名在目标端解析，不能同时加 to 或 reverse。需要 stdin/stdout TCP 字节桥时用 `proxy TICKET --to HOST:PORT`，原始字节模式不加 JSON；不带 to 的 proxy 用于 OpenSSH 传输，不要混淆。

MCP 使用 `mcp serve` 的 stdio 工具集，upload/download 传本地文件或清单路径，不用 payload `-` 或 `--files-from -`；tail 不通过 MCP 暴露。需要持续后台运行时按用户任务选择 service，不额外安装自启。服务不延长有限票据。targets 仅保存现有票据引用，不复制或续期。

超时、取消或断线都不能证明远端没有执行；先检查状态与副作用，不自动重放变更。结束 terminal 后，共享仍可能运行；结束共享用 Ctrl+C、stop 或 revoke。cleanup 只删除生成凭据，不撤销会话、不回滚命令，也不删除系统审计。
