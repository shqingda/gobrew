# 0002：命令与 Homebrew 兼容

## 状态与日期

已接受，2026-11-01（Asia/Taipei）。用户要求 gobrew 与 Homebrew 完全兼容，除非某个命令 gobrew 没有。本记录取代 [0001](0001-homebrew-feature-scope.md) 中“参数与提示语逐字兼容”不做的那一行；0001 的其余取舍不变。

## 决定

- gobrew 的命令应是 Homebrew 命令的子集：gobrew 没有的命令可以缺席，但已有的同名命令、参数和默认行为要与 brew 一致。
- 默认行为以 [Homebrew 手册](https://docs.brew.sh/Manpage) 为准。例如 `install` 和 `version-install` 都不自动 pin；`upgrade` 跳过被 pin 的软件并提示 `Not upgrading N pinned package(s)`，只有显式点名被 pin 的软件时才以退出码 1 结束。
- 供脚本读取的文本输出与 brew 一致，例如 `list`、`outdated`、`deps`、`uses` 和 `search`。`--json` 仍是 gobrew 自己的稳定结构。
- 因为 gobrew 只有 Formula、从不交互确认，`--formula`、`-y/--no-ask` 这类参数照常接受，不改变行为。
- 保障正确性的拒绝不因兼容而放宽：仍被依赖的软件不能卸载；升级依赖会破坏被 pin 的依赖方时，在切换前拒绝。

## 尚存差异

在 [当前状态](../status.md) 中维护，每一项都注明是待改，还是因正确性或安全约束有意保留。
