# gobrew：现有功能与性能收尾计划

更新日期：2026-10-28（Asia/Taipei）。

## 目标与依据

用户已选择：完成真实安装链路验证、必要修复、基准重测和 nanobrew 对比，暂不新增命令。按 ub-mode 的 First Make it Work → Then Make it Right → Follow Practice 执行，每个小单元验证通过后再继续。

功能取舍以 [决策 001](../docs/decisions/0001-homebrew-feature-scope.md) 为唯一依据；质量要求仍由 `CONSTRAINTS.md` 定义。本计划只安排执行顺序，状态集中记录在 [待办清单](todo.md)，行动及原因记录在 [工作日志](work-log.md)。不读取或修改仓库外的 Claude 计划。

完成结果：现有安装、升级、清理行为有可复现证据；当前二进制有独立性能报告；与 nanobrew 的可比场景有实测，不可比场景有明确原因。性能优化只接受测得的改进，不承诺一定超过 nanobrew。

本轮不新增查询命令、不扩展 tap/Cask/services/Bundle，不升级所有仅版本过时的依赖方，不建立通用框架、不做无关重构。已有功能也不因取舍调整而删除。

## 已核实的起点

- 代码基线为 `66f0e9f`：自动初始化、指定版本、install 升级、自动更新和清理已经实现，不能重复开发。
- 本轮之前已有 README、交接文档及决策 001 的未提交修改，保留并在首次文档提交中明确纳入。
- resolver 已有反向依赖、pin 和最低运行版本保护；自动更新、清理底层也有部分单测，先复用再补缺口。
- 实瓶 smoke 缺少“历史版本 → unpin → install 升级 → 自动清理”的完整链路；当前性能文档仍对应旧提交。
- `install --dry-run` 当前在判断 dry-run 前进入自动更新，可能触发 CLI 自身更新；需用入口测试固定下面的边界。

## 第一阶段：First Make it Work

### 1. 固定现有命令行为并补最小回归

依赖：本计划与决策记录。主要涉及 CLI 编排及必要的 resolver 回归测试。

先跑相关现有测试；对已实现但缺覆盖的行为补回归测试，对实际缺陷先写失败测试并确认失败原因，再作最小修复。复用现有临时目录、HTTP fixture 和进程替身，不搭新测试框架。

| 行为 | 验收条件 |
| --- | --- |
| 已安装软件的 install | 过时且未 pin 时升级；最新版本返回 no-op；pin 或 `GOBREW_NO_INSTALL_UPGRADE` 保持版本，沿用当前非零退出及升级指引。 |
| 自动更新 | install/upgrade/outdated 在目录过期时触发，目录新鲜或禁用时不触发；reinstall/version-install 不触发；刷新失败可在已有目录上继续，取消则终止，JSON 始终为单个文档。 |
| dry-run | install/upgrade 的 dry-run 跳过自动更新及 CLI 自身替换，不初始化、提权、安装、清理或修改 shell 配置；规划所需的缺失目录获取仍走现有 catalog 逻辑，文档明确目录缓存可能写入。 |
| 安装后清理 | 清理成功安装项的旧版本；尊重禁用与排除设置；缺失周期标记时先记录周期，到期才全量清理；清理失败显示警告，不把已成功安装描述为未安装。 |
| 依赖方保护 | ABI 变化引发必要处理，被 pin 的不兼容依赖方在切换前阻止操作；仍兼容且仅版本过时的依赖方不被额外升级；清理保留仍被引用或恢复所需的材料。 |

不做全量损坏链接扫描，不重复已有恢复测试。若发现具体保护缺口，先复现再修复；若拟为极低概率竞态增加复杂度，先说明失败方式、发生可能与成本，取得用户同意。

检查点：上述命令入口行为有断言，相关包测试通过，原有依赖与恢复测试无回归。

### 2. 跑通真实旧版升级链路

依赖：任务 1。新增独立的版本升级 smoke 脚本，复用现有二进制参数约定；原完整 smoke 继续保留。

使用独立短 prefix、cache、HOME、XDG 配置和临时目录，禁用自动 PATH 修改；自动更新测试与版本升级测试分别运行。不得写入 `/opt/gobrew`、开发者 Homebrew 或真实 shell 配置。

以 `uv 0.13.5` 为初始历史版本 fixture，开始前确认其受支持 bottle 可下载且旧于本次固定的目录版本；条件不满足时明确记录 fixture 不可用，不跳过校验或把测试记为通过。

按顺序验证：

1. 从源码构建的未托管 CLI 首次 `version-install uv 0.13.5` 自动初始化，程序可执行，状态为 ready 且 pinned。
2. 已 pin 时普通 install 不替换版本；unpin 后开启 `GOBREW_NO_INSTALL_UPGRADE` 也保持原版本和现有错误语义。
3. 清除禁用升级设置，执行 `install uv`，版本、链接和状态切换到固定目录版本，旧 keg 自动清理，配置与非归属文件保留。
4. 目录新鲜且禁止自动刷新时再次 install，版本不变，无 Prepare/Apply；最后运行 doctor、卸载、autoremove 和 cleanup。

记录提交、二进制 SHA、目录摘要、包版本、命令、退出码及关键输出。GitHub API 限流或网络失败保留失败证据，不能作为成功样本。单元测试覆盖历史 bottle 双源不一致和未知安装步骤的拒绝，不依赖外部服务制造这些失败。

检查点：版本链路和原实瓶 smoke 均通过；临时目录清理完成，开发者环境未被修改。

## 第二阶段：Then Make it Right

### 3. 建立当前版本的性能基线

依赖：任务 2；测量期间不并行运行构建、测试或其他基准。

- 复用现有基准脚本，先重测 gobrew/Homebrew 的 `hello,jq,ca-certificates` 集合及实际依赖闭包，至少五轮，冷安装、热缓存重新安装、已安装 no-op 分开记录。
- 记录双方 commit、二进制 SHA、相同包版本/修订、依赖集合、机器与路径。目录刷新单独计时；测量期间双方显式禁用自动更新和清理，默认开启时的行为由任务 1、2 验证。
- 保持现有 Homebrew 冷/热发布门禁。no-op 与旧数据的差异如实说明；没有相同环境和版本证据时，不直接把差异归因于某次代码改动。
- 新报告追加到 `docs/benchmarks/`，保留历史数据。无失败样本丢弃、最快样本拼接或 no-op 冒充重装。

### 4. 完成 nanobrew 同机对照

依赖：任务 3。先证明一个场景可比较，再将同样步骤脚本化，不改造成多包管理器通用框架。

- 固定 nanobrew 的 release/commit 与二进制 SHA，按该版本核对 README、基准脚本及 prefix/cache/fallback 源码。上游入口为 [justrach/nanobrew](https://github.com/justrach/nanobrew)；首页宣传数字不作为实测证据。
- 不运行默认全局安装器。必须确认可隔离 prefix、HOME、缓存及配置，且所选场景不隐式调用开发者 Homebrew；无法隔离则停止执行该场景并记录阻碍。
- 固定候选为 `uv`、`hello`、`jq`（含其依赖）；分别验证两个工具安装的版本、修订与完整依赖集合相同。需要上游初始化而某方不支持时，标记不可比，不删去必要步骤。
- 使用独立比较脚本及报告，保留现有 Homebrew 发布门禁和报告格式。先手动完成一个等价冷安装，再实现五轮冷/热/no-op 测量，逐轮交替执行顺序。
- 每次安装后在计时外运行软件，检查动态库来自隔离安装、签名和必要初始化；原始样本、失败、阶段时间及逻辑磁盘占用随报告保留。无法形成等价样本时不得给出速度倍数，也不得声称对比已经完成。

检查点：当前版本基线可复现；nanobrew 各候选场景明确为已测且可比、不可比或受阻，没有未验证的速度结论。

### 5. 只优化已测出的主要瓶颈

依赖：任务 3、4。先按阶段时间定位，必要时补局部 profile，再选择一个最大且可控的瓶颈。

- 优先删除重复计算、无用读取或无效工作；不为未测出的竞态、缓存命中或扩展性加新层。
- 每次只改一个瓶颈，先固定行为，完成最小修改后重跑同口径对照；至少两组各五轮的交替测量都显示同方向收益，且正确性无回归，才保留优化。其余记录为无确定收益，撤回性能改动。
- 同一前提下两种修法都失败时，按 ub-mode 写清该前提与责任归属，重新定位原因，不叠加第三层补丁。
- 不跳过依赖、签名、重定位、初始化、持久化或恢复。没有可证明收益时，以报告结论结束该项，不制造优化提交。

## 第三阶段：Follow Practice

### 6. 文档、检查和交付

依赖：任务 1–5；受阻项必须单列，不能勾为通过。

- README 同步 install 升级/no-op 的准确条件、自动更新触发范围、dry-run 边界及已实现环境变量的默认值；交接记录清楚区分历史描述与当前事实，性能文档绑定被测版本。
- 每个单元运行针对性验证，最终运行格式检查、`go mod verify`、`go vet ./...`、`go test -race ./...`、教程校验、govulncheck@v1.9.0、`CGO_ENABLED=0` 构建、bootstrap 和两个实瓶 smoke。修改后的最终二进制重新通过受影响的性能门禁。
- 使用 ub-mode 的 crapper、dryer、mutator 检查本轮改动涉及的代码：CRAP < 10，处理重复度 ≥ 0.8 的重复代码及有意义的存活变异，不借机全库重构。当前会话和 PATH 未找到这三个工具；执行时仍不可用就记录未运行，不能伪造分数或以普通测试声称代替。安装额外工具若改变项目依赖，应先说明来源与成本。
- 每次行动记录原因、结果和证据路径；小步提交，提交说明写清改动理由及验证。最终审查完成后按 ub-mode 合入并推送 main，遵守远端保护规则、不强推；tag 与正式发版另行决定。
- 本次未创建额外 worktree；执行阶段若创建，仅清理确认不再使用的工作树，未提交工作先保留。

公开接口默认保持现有命令、参数及 JSON schema；明确行为调整仅为 dry-run 不触发自动更新／自身替换。其余修改限于本轮验证复现的缺陷，保留 Plan/Prepare/Apply 与不可变准备材料。

## 历史：首轮实现计划（已完成）

以下保留原记录，不代表本轮验证已经完成。

Approved scope: Go 1.27.3, macOS ARM64 15+, official core bottles, independent
`/opt/gobrew`, no Ruby/Homebrew fallback. CLI now, shared engine for future MyGo.
A separate formal Chinese Go tutorial is part of this delivery.

The system and code contracts are in `docs/architecture.md`; quality requirements
are in `CONSTRAINTS.md`. This document records execution order rather than a
second architecture specification.

1. Define shared values and Plan/Prepare/Apply contracts; verify signed catalog
   ingestion and pure dependency resolution.
2. Verify real bottles in a disposable short prefix: digest, extraction,
   relocation, signing, initialization and actual execution.
3. Complete durable filesystem activation, cache leases, failure recovery,
   shared config retention and installed dependency protection.
4. Integrate CLI lifecycle, JSON, cancellation, migration and diagnostic commands.
5. Perform independent boundary review, focused regression tests and race/vet.
6. Run five-round cold/warm/no-op measurements against isolated Homebrew with
   identical package versions and initialization. Preserve actual evidence.
7. Ship tag-driven GitHub Actions with correctness and performance gates.

Source remains single-module and CGO-free for the CLI. Runtime dependencies are
limited to Cobra, bbolt, x/sys, x/sync and their necessary transitive dependencies.
No threshold may be loosened or required initialization omitted to pass a gate.
