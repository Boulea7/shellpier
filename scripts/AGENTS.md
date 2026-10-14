# 安装与发行辅助

- 下载不执行程序或编辑全局环境；Bootstrap 每跳使用 HTTPS，并限制重定向次数。
- 打包保留第三方许可、公开文档与离线链接，不收集凭据或内部笔记。
- 已发布文件默认不覆盖；发行版本须对应记录的源码提交。

打包检查：

```sh
sh -n scripts/bootstrap.sh
python3 scripts/package.py --version validation --out /tmp/shellpier-validation
python3 scripts/validate_docs.py --root /path/to/unpacked-release
```

PowerShell 脚本另做 parser 和行为检查。
