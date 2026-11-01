# 当前状态

更新日期：2026-11-01（Asia/Taipei）。

## 版本

最新正式版是 [v0.4.0](releases/v0.4.0.md)，于 2026-10-29 发布，发布流水线的全部门禁均已通过。`main` 之后的整理分支 `tidy/v0.5` 没有新增命令，只做了以下几类改动：

- 清理冗余代码。
- 拆分大文件。
- 修补架构边界。
- 减少 GHCR 请求的网络往返。
- 打磨交互输出。
- 整理测试与文档。

本轮计划和进度见 [tasks/plan.md](../tasks/plan.md) 与 [tasks/todo.md](../tasks/todo.md)。

## 支持范围

功能取舍以 [0001 决定](decisions/0001-homebrew-feature-scope.md) 为准。

- **支持**：
  - 官方 homebrew/core 的 ARM64 bottle，macOS 15 或更高版本。
  - 依赖解析与 ABI 检查。
  - pin、指定版本安装（`version-install`）。
  - 卸载时自动清理无用依赖、cleanup、doctor 与 PATH 诊断。
  - 从 Homebrew 迁移已请求的软件。
- **不做**：
  - Formula 开发工具。
  - 源码构建，以及 `--HEAD`。
  - Homebrew 自身的 Git 管理。
  - `--ignore-dependencies`。
  - 参数与提示语逐字兼容。
- **延后（按需求再排期）**：
  - 软件清单的导出与恢复。
  - `home`、`desc`、`log` 等辅助入口。
  - 依赖方仅版本过时时的自动升级。
  - Homebrew JSON 格式兼容。
  - 第三方 tap、`services`、Cask、漏洞查询。

## 验证方式

- **日常**：在本地运行 `make check`，并交付 dev 构建，由用户安装后测试。
- **安装行为变更**：另外运行 `scripts/smoke.sh` 和 `scripts/smoke-version.sh`，在隔离目录中验证真实 bottle。
- **GitHub Actions**：只在用户确认发版后使用，详见 [工程约束](../CONSTRAINTS.md)。
- **性能数据**：每组数据只绑定当时的提交，见 [性能验证记录](performance.md)。

## 已知限制

- 冷安装的 Prepare 阶段在小包上仍慢于 nanobrew，见 [对比报告](nanobrew-comparison.md)。2026-11-01 已经减少了一次 GHCR 往返，与 nanobrew 的对比尚未重测。
- 证书包初始化会完整运行上游脚本，在 M3 Pro 上约 6.5 秒。为了保留信任检查，这一步不跳过、也不缓存。
- 专用质量工具 crapper、dryer、mutator 在本环境中不可用，从未运行，也没有评分。
