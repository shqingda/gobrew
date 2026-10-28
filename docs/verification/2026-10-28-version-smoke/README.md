# 指定版本升级链路：2026-10-28

`scripts/smoke-version.sh` 在本地 Apple Silicon macOS 27.0.1 上退出 0；完整 `commands.txt`、`exits.tsv`、逐步 stdout/stderr 和脚本副本原样保存在 `evidence.tar.gz` 中。目录中的 `manifest.txt`、`result.txt` 和 `noop.stdout` 是便于查阅的副本。

验证 uv 0.13.5 → 0.13.6：自动初始化、历史版本 pin、pin/禁用升级时非零退出并保持原版本、解除后升级、旧 keg 自动清理、配置和外来文件保留、重复 install 不进入 Prepare/Apply、doctor 及卸载清理。

二进制 SHA 见 `manifest.txt`，目录摘要见压缩包内 `catalog-generation.txt`。构建时基线为 `0e608cd`，工作区尚未提交；压缩包内 `source-status.txt` 如实保留当时状态，产品代码差异在 `source.patch`，随后以 `89d480c` 提交。新测试文件不进入产品二进制。不得将这份记录说成干净提交构建或性能基准。

证据原目录 `/tmp/gobrew-version-evidence.0Tuzil`；安装 prefix 和工作目录已由脚本清理，开发者 Homebrew 与真实 shell 配置未参与。脚本副本供复现当时步骤，不作为第二个维护入口。

同一产品二进制随后通过原 `scripts/smoke.sh`，完整输出在 `full-smoke.log.gz`。运行时另建临时 HOME/XDG/TMPDIR，并将 PATH 限制到系统工具，prefix `/tmp/gb.Eg6h` 在退出时清理。覆盖 hello/jq、证书初始化、Node/npm、Python/venv、重装、链接、卸载和清理。
