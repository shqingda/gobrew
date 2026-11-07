# 当前状态

更新日期：2026-11-07（Asia/Taipei）。

## 版本

最新正式版是 [v0.5.0](releases/v0.5.0.md)，于 2026-11-01 发布，发布流水线的全部门禁均已通过（代码检查、macOS 15/26 实瓶测试、Homebrew 性能门槛）。本版的命令是 Homebrew 命令的子集，详见 [0002 决定](decisions/0002-homebrew-command-compatibility.md)。此后 `self` 已按 [0003 决定](decisions/0003-restore-self-command.md) 恢复，尚未发布。

## 支持范围

功能取舍以 [0001 决定](decisions/0001-homebrew-feature-scope.md) 为准；已有命令的参数和默认行为与 brew 一致，见 [0002 决定](decisions/0002-homebrew-command-compatibility.md)。

- **支持**：
  - 官方 homebrew/core 的 ARM64 bottle，macOS 15 或更高版本。
  - 依赖解析与 ABI 检查。
  - pin、指定版本安装（`version-install`）。
  - 卸载时自动清理无用依赖、cleanup、doctor 与 PATH 诊断。
- **不做**：
  - Formula 开发工具。
  - 源码构建，以及 `--HEAD`。
  - Homebrew 自身的 Git 管理。
  - `--ignore-dependencies`。
- **延后（按需求再排期）**：
  - 软件清单的导出与恢复。
  - `home`、`desc`、`log` 等辅助入口。
  - 依赖方仅版本过时时的自动升级。
  - Homebrew JSON 格式兼容。
  - 第三方 tap、`services`、Cask、漏洞查询。

## 与 brew 的尚存差异

2026-11-01 对照 [Homebrew 手册](https://docs.brew.sh/Manpage) 逐条检查后，以下差异仍然存在：

- **待决定（涉及产品形态）**：
  - 全局参数 `--prefix DIR`、`--cache-dir` 等是 gobrew 的配置项；brew 的 `--prefix`、`--cache`、`--cellar` 是打印路径的命令。
  - `doctor --repair` 和 `reinstall --dry-run` 是 brew 没有的参数。
- **因正确性或安全约束有意保留**：
  - 不支持 `uninstall --ignore-dependencies` 与 `--force`：仍被依赖的软件不能卸载。
  - 升级依赖会破坏被 pin 的依赖方时，gobrew 在切换前拒绝；brew 会直接升级。
  - 不支持 `link --overwrite`：从不覆盖别人的文件。
  - `cleanup --prune-prefix` 和完整 `cleanup` 只清理指向本 prefix `Cellar`、且没有 record 声明的失效链接，以及链接目录下的空目录；brew 会删除所有失效链接。`etc`、`var` 从不清理。
  - `self` 是 brew 没有的命令，由用户决定保留，见 [0003 决定](decisions/0003-restore-self-command.md)。
  - 一个 prefix 里每个 Formula 只有一个 keg，旧版本进 `.gobrew/backups` 等待清理，所以 `list --versions` 只显示当前版本，`list --multiple` 总是空的。
  - 同时标记了 deprecated 和 disabled 的 Formula，gobrew 仍按 disabled 拒绝安装；brew 只给警告。
  - `install --skip-link` 仍然检查 `conflicts_with`；brew 这时跳过检查。
  - `--display-times` 的每个包时间从它的第一个进度事件算到安装完成；gobrew 并行准备，所以各包时间会重叠，brew 是逐个计时。
  - 没有 `doctor --list-checks`：brew 列出的是它自己的 `Diagnostic::Checks` 方法名，gobrew 的检查不是按这些方法组织的。
  - `version-install` 在原名下安装指定版本；brew 会把它提取成个人 tap 中的独立 Formula（如 `uv@0.13.5`）。
- **尚未实现的参数和输出**：
  - `info` 没有 `--HEAD` 相关内容、Options，以及 `--analytics` 的完整表格；摘要形式的 Analytics 已提供，可用 `GOBREW_NO_ANALYTICS` 或 `GOBREW_NO_GITHUB_API` 关闭。
  - `deps --annotate -1` 里同名依赖同时有带标签和不带标签两项时，并列顺序可能与 brew 不同，因为 brew 用的是不稳定排序。
  - brew 的 `--json=v1/v2` 结构（gobrew 的 `--json` 是自己的格式）。

## 验证方式

- **日常**：在本地运行 `make check`，并交付 dev 构建，由用户安装后测试。
- **安装行为变更**：另外运行 `scripts/smoke.sh` 和 `scripts/smoke-version.sh`，在隔离目录中验证真实 bottle。
- **GitHub Actions**：只在用户确认发版后使用，详见 [工程约束](../CONSTRAINTS.md)。
- **`deps` 对照**：`--include-build`、`--include-optional`、`--skip-recommended` 全量逐行对比 8706 个 Formula，结果一致；`--tree`、`--graph --dot`、`--annotate`、`--include-test`、`--missing` 用单个 Formula 抽样对比。全量模式下 brew 的 `--include-test` 会因内部缓存受遍历顺序影响，所以不用全量结果比较。
- **性能数据**：每组数据只绑定当时的提交，见 [性能验证记录](performance.md)。

## 已知限制

- 冷安装的 Prepare 阶段在小包上仍慢于 nanobrew，见 [对比报告](nanobrew-comparison.md)。2026-11-01 已经减少了一次 GHCR 往返，与 nanobrew 的对比尚未重测。
- 证书包初始化会完整运行上游脚本，在 M3 Pro 上约 6.5 秒。为了保留信任检查，这一步不跳过、也不缓存。
- 专用质量工具 crapper、dryer、mutator 在本环境中不可用，从未运行，也没有评分。
