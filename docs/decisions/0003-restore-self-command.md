# 0003：恢复 `self` 命令

## 状态与日期

已接受，2026-11-07（Asia/Taipei）。用户要求把 `gobrew self` 加回来。本记录修改 [0002](0002-homebrew-command-compatibility.md) 中“命令是 Homebrew 命令的子集”的规定：`self` 是唯一的例外，0002 的其余取舍不变，`init`、`import-homebrew` 和 `completion` 仍然不提供。

## 决定

- 恢复 `self install [--bin-dir DIR]`、`self update` 和 `self uninstall`，行为与 v0.5.0 之前一致。这些命令也可以写成 `gob self ...`。
- `self` 只管理命令行程序，不触碰 Formula 状态，也不走 Plan/Prepare/Apply。`self uninstall` 保留已安装软件、配置、数据和缓存，并留下少量归属记录和锁文件供中断恢复。
- 与现有入口并存，而不是替换它们：
  - `install.sh` 继续用 `GOBREW_INSTALL_BIN_DIR=<dir> gobrew update` 交接，这样无论选中哪个已发布版本都能安装。
  - `uninstall.sh` 继续作为完整卸载入口，还会移除 `.gobrew` 中的记录。在 `self uninstall` 之后运行它，同样可以完成清理。
  - `update` 仍然包含自身更新，`self update` 只更新程序，不刷新 Formula 目录。

## 影响

v0.5.0 的发布说明写的是删除 `self`，下一个版本的发布说明需要写明恢复。
