# 0002：命令与 Homebrew 兼容

## 状态与日期

已接受，2026-11-01（Asia/Taipei）。`self` 已由 [0003](0003-restore-self-command.md) 恢复，是下面“命令是子集”的唯一例外。用户要求 gobrew 与 Homebrew 完全兼容，除非某个命令 gobrew 没有。本记录取代 [0001](0001-homebrew-feature-scope.md) 中“参数与提示语逐字兼容”不做的那一行；0001 的其余取舍不变。

## 决定

- gobrew 的命令应是 Homebrew 命令的子集：gobrew 没有的命令可以缺席，但已有的同名命令、参数和默认行为要与 brew 一致。因此 2026-11-01 删除了 brew 没有的 `self`、`init`、`import-homebrew` 和 `completion`：
  - 安装命令行改由 `install.sh` 以 `GOBREW_INSTALL_BIN_DIR=<dir> gobrew update` 交给已校验的程序完成；自身更新仍由 `update` 负责。
  - 卸载命令行与 Homebrew 一样使用单独的 `uninstall.sh`。
  - 初始化由第一次 `install` 自动完成，需要提权时 sudo 只运行固定的 `mkdir` 和 `chown`。
  - 迁移 Homebrew 软件可用 `brew list --installed-on-request | xargs gob install`。
  - `completion` 命令不恢复。shell 补全改为静态脚本，见下方 2026-11-22 修订。
- 默认行为以 [Homebrew 手册](https://docs.brew.sh/Manpage) 为准。例如 `install` 和 `version-install` 都不自动 pin；`upgrade` 跳过被 pin 的软件并提示 `Not upgrading N pinned package(s)`，只有显式点名被 pin 的软件时才以退出码 1 结束。
- 供脚本读取的文本输出与 brew 一致，例如 `list`、`outdated`、`deps`、`uses` 和 `search`。`--json` 仍是 gobrew 自己的稳定结构。
- 因为 gobrew 只有 Formula、从不交互确认，`--formula`、`-y/--no-ask` 这类参数照常接受，不改变行为。
- 保障正确性的拒绝不因兼容而放宽：仍被依赖的软件不能卸载；升级依赖会破坏被 pin 的依赖方时，在切换前拒绝。

## 尚存差异

在 [当前状态](../status.md) 中维护，每一项都注明是待改，还是因正确性或安全约束有意保留。

## 修订：shell 补全（2026-11-22）

用户批准附带静态补全脚本。brew 也在 `completions/` 中附带补全脚本，没有 `completion` 命令，所以这一项不算新增命令。

- 不恢复 `completion` 命令。
- 补全脚本由已校验的 gobrew 程序用 cobra 生成，覆盖 zsh、bash、fish，同时适用于 `gobrew` 和 `gob`。按 Tab 时，脚本调用程序内隐藏的 `__complete`，所以补全内容始终与已安装的版本一致。
- 生成时机：`install.sh`（经 `GOBREW_INSTALL_BIN_DIR`）、`self install` 和自身更新。脚本写入 CLI 存储目录（`<bin>/.gobrew/completions`），不写 prefix。
- 与原计划不同：脚本不打进发布压缩包。原因如下：
  - `install.sh` 和自身更新都只从压缩包中读取一个可执行文件，从不按路径解包（见 `selfupdate.executable`）。
  - 从同一个已校验的程序生成，内容相同，也不增加新的信任面。
  - 发布流程（`release.yml`）因此不用改。
- 加载方式：由 gobrew 已有的启动块负责，块外的文本不改。
  - zsh：把补全目录放在 `fpath` 最前面。compinit 已运行时，立即用 `compdef` 注册。
  - bash：只在 bash-completion 已加载时才 source。brew 的 bash 补全同样需要 bash-completion。
  - fish：交互式 shell 中 source。
- 自身更新会刷新已有的启动块。gobrew 不会给原本没有启动块的文件新增启动块。
- 删除：
  - `uninstall.sh` 删除整个存储目录，补全脚本随之删除。
  - `self uninstall` 删除补全目录。
- `shellenv` 按上游 `brew shellenv` 对齐：
  - zsh 的 `fpath` 前置 `$prefix/share/zsh/site-functions`。
  - MANPATH 不再前置 `share/man`，只把已有的值规范为以冒号开头。
  - INFOPATH 前置 `share/info`。
  - PATH 用 `${PATH+:$PATH}`，PATH 未设置时不留空项。

