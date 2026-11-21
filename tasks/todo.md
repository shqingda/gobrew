# 本轮待办：v0.7 对齐 brew、性能与输出

计划见 [plan.md](plan.md)。每一项都要验收通过才能勾选；有偏差、受阻或评估后不做的，都要写明原因。用户决定：静态补全并修订 0002；持久化边界先测量、再单独提请；过时依赖方升级本轮实现。brew 没有的新命令、参数或环境变量，必须先经用户批准；`GOBREW_` 前缀的 brew 同名变量按惯例可以加。

- [x] 阶段 0：基线 `make check`（`ff0738d`）通过。
- [x] 阶段 1：正确性修复（`b2b23c3`）。
  - 偏差：symlink 步骤的 `uninstall` 字段保留，不拒绝。brew 只在 Cask 卸载时用它，拒绝会让 brew 能装的 Formula 装不上。
  - 偏差：`info --analytics` 只补了注释。brew 在这里用 `onoe`，退出 0；没有 CLI 级的假 catalog 测试夹具，所以没加测试。
  - 偏差：pwsh 断言移到阶段 5，因为上游 shellenv 已改。
- [x] 阶段 2：过时依赖方升级（`f65a8f5`）。
  - 未验证：真实 bottle 的依赖方升级。`version-install oniguruma 6.9.9` 报 `OCI index has no creation time`，`jq 1.8.1` 报 homebrew-core 提交不匹配；已用单元测试覆盖。
- [x] 阶段 3：测量与性能（`bd8be92`）。
  - 保留：OCI 元数据预取、解压缓存父目录 Root、重定位跳过多余 chmod、清理无候选时跳过 Mach-O 扫描。hello+jq 冷安装中位数 2041 → 1362 毫秒。
  - 回退：CDN 连接预热、gzip 外层加大缓冲，均无可测收益。
  - 未做：每个 bottle 的设备屏障从 6 次减为 4 次。需要先补中断恢复测试。
  - 未做：100 包清理场景。
  - 只测量：Apply 的持久化边界。数据和选项见 [0004 提案](../docs/decisions/0004-apply-persistence-boundary.md)，待用户决定。
- [x] 阶段 4：输出与体验（`834da62`）。
  - 跳过：relocate、签名、链接阶段的单独标题。brew 只显示 Pouring。
  - 跳过：进度条 `[n/N]` 和开始时就计入全部下载总量。catalog 中没有 bottle 大小。
  - 跳过：等锁超过 1 秒时的即时提示。需要给 state 增加回调，收益小。
  - 跳过：`--quiet` 抑制警告。brew 的 `--quiet` 不抑制 `opoo`。
  - 偏差：无变化时的警告保留 `is already installed.`，不加 `and up-to-date`。这条路径不读 catalog，无法确认是否最新。
- [x] 阶段 5：补全与 shellenv（`1c448d4`）。
  - 偏差：补全脚本不打进发布压缩包，改为安装时由已校验的程序生成。安装和自更新都只读取压缩包中的可执行文件，不按路径解包。原因写在 0002 修订中。
- [x] 阶段 6：文档与最终验证。
  - 更新了 `status.md`、`performance.md`、v0.7.0 发布说明草稿、`plan.md` 和本文件。
  - 未运行：GitHub Actions 发布门禁，以及与 nanobrew 的重测。两者都需要用户先确认发版。没有打 tag。
