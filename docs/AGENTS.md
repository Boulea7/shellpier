# 公开产品文档

- README 保持简短，详细操作放在本目录；中英文入口同步关键行为。
- PowerShell 的 `@file` 参数加引号，Windows/Unix 示例各自可复制。
- 示例使用占位域名；图形放在 assets 并使用相对链接。
- 发行包不含完整源码，本地链接须指向包内文件。

包内链接检查：`python3 scripts/validate_docs.py --root /path/to/unpacked-release`。
