# 工作日志

## 2026-10-28：制定收尾计划

| 行动 | 原因 | 结果 |
| --- | --- | --- |
| 读取 ub-mode、planning-and-task-breakdown 技能及仓库约束 | 按用户指定原则拆分可验证单元 | 使用小步验证、行为测试、测量后优化；不改变现有架构。 |
| 核对 Git 状态、首轮计划、待办与决策 001 | 避免覆盖未完任务或重新引入已排除功能 | 首轮待办全部完成；此前范围文档尚未提交，保留原修改及首轮历史。 |
| 核对 CLI、resolver 测试、smoke、benchmark 和发布工作流，并委派只读复核 | 将计划建立在实际覆盖缺口上 | 已有局部测试可复用；补真实旧版升级链路、入口编排、清理验证，明确 dry-run 边界。未运行测试，未修改产品代码。 |
| 核对 nanobrew 官方仓库首页 | 确认比较需绑定版本与等价安装条件 | 已记录执行前的固定版本、隔离及可比性检查；未安装 nanobrew，也未声称已有对比结果。 |
| 检查会话工具及 PATH 中的 crapper/dryer/mutator | 如实落实 ub-mode 的专用检查要求 | 未找到可用工具；计划中记录工具缺口，未虚构检查结果。 |
| 重新发送中断的范围选项 | 首次问题未得到答复 | 用户明确选择“现有功能与性能收尾”，暂不新增命令。 |
| 更新仓库计划与待办、增加本日志和交接链接 | 让下一位执行者获得同一任务顺序和验收标准 | 仅文档变更；未读取或修改仓库外 Claude 计划，未开始实现、提交、推送或发版。 |
| 执行 `git diff --check` 并检查新计划相关 Markdown 本地链接 | 验证文档格式与导航 | 检查通过；本次只有计划文档变更，没有运行产品测试。 |

执行阶段继续追加每个单元的行动、原因、命令结果和证据路径；受阻或未运行事项不得记为通过。

## 2026-10-28：执行第一阶段

| 行动 | 原因 | 结果 |
| --- | --- | --- |
| 核对当前工作区、架构及 Git/TDD/增量实现技能，提交范围文档与计划 | 保留已确认决策，建立可追溯起点 | 文档提交 `0e608cd`；继续使用现有开发分支，无新增 worktree。 |
| 运行 CLI/resolver/engine/installer/artifact 现有测试 | 在改动前确认基线 | 全部通过，Go 1.27.3 darwin/arm64。 |
| 新增 dry-run 命令入口测试并先运行 | 复现自动更新副作用 | install/upgrade 两例均失败：dry-run 仍各发出一次目录刷新请求。 |
| 在 CLI 自动更新条件中排除 dry-run，补命令触发、失败回退及取消验证 | 修复已复现行为，保留缺失目录的正常规划获取 | `go test ./internal/cli ./internal/resolver -count=1` 通过；JSON 仍是单文档。 |
| 补安装后清理与兼容依赖方回归 | 覆盖编排缺口，不重写已有保护 | 验证禁用/排除、周期清理、清理失败、部分安装成功的修复材料保留；兼容但过时的依赖方不额外升级。 |
| 运行相关包 race 检查及构建 | 确认本单元可交付 | `go test -race ./internal/cli ./internal/resolver ./internal/engine ./internal/installer ./internal/artifact` 和 `CGO_ENABLED=0` 构建通过。 |
| 增加并运行独立版本 smoke | 用真实 bottle 验证整个版本切换链路 | uv 0.13.5 → 0.13.6 全链路通过；证据保存在 `docs/verification/2026-10-28-version-smoke/`，包含构建时未提交状态与对应产品补丁。 |
| 核对 nanobrew v0.1.212 的源码和发布物 | 确认隔离与比较条件 | 原版硬编码 `/opt/nanobrew`，不在开发机运行；转用一次性 GitHub hosted macOS runner 同机对比，固定 commit 和发布物 SHA。 |
| 运行 vet、依赖校验、教程和漏洞检查 | 验证静态质量及依赖状况 | 全部通过；16 个教程示例通过，govulncheck@v1.9.0 未发现漏洞。 |
| 在独立 HOME/XDG/TMPDIR 下运行原完整 smoke，并执行全量 race 与 bootstrap | 验证动态依赖、初始化和 CLI 生命周期 | 全部通过；原 smoke 输出压缩保存在版本验证目录，检查点 A 完成。 |

## 2026-10-28：性能基线与文档核对

| 行动 | 原因 | 结果 |
| --- | --- | --- |
| 独立复核 dry-run 修复与新增行为测试 | 验证测试确实检查命令行为 | 未发现阻塞问题；补充返回 Plan 的 action/unchanged 断言，针对性 CLI 测试通过。原失败测试在修复前已检测到自动更新副作用。 |
| 核对 README、架构和仓库交接记录 | 消除旧的“install 保持版本／永不联网”描述 | 同步自动更新入口、dry-run 缺失目录缓存、pin/held、14 个环境变量和清理语义；历史交接段落明确标记日期与适用范围。 |
| 重建被测二进制，在隔离 Homebrew 上连续测量五轮 | 为当前产品代码建立独立基线 | 绑定 `046d271` 和二进制 SHA；冷安装 9.853s 对 13.665s、热重装 7.374s 对 11.191s，冷/热门禁均通过；no-op 为 14.981ms 对 326.253ms。测量期间未并行构建或测试。 |
| 保存完整 JSON、命令与日志，检查逐轮阶段和版本 | 保留可复现证据，不混合历史样本 | `docs/benchmarks/2026-10-28-m3-pro-046d271.json` 及同名 `-evidence.tar.gz`；双方完整版本集合一致。固定先 gobrew 后 Homebrew，未据此声称统计置信区间或与旧版差异的因果关系。 |
| 新增 nanobrew 独立比较工作流与脚本 | 原版固定 prefix，需一次性 hosted runner 上隔离双方并屏蔽预装 Homebrew | 固定 v0.1.212、源码提交和发布物 SHA；预检后交替测量 uv/hello/jq 五轮，逐次检查完整包集合、版本/修订、bottle SHA、实际执行、签名与链接来源。尚未有远端实测结果。 |
| 对比较器补真实树遍历的失败 fixture，再修复校验 | 防止 symlink keg 导致零文件检查却报告成功 | 旧实现暴露零签名检查和空树未拒绝，修复后 6 项 Python 测试通过；磁盘报告区分分区和逐 keg 统计，不重复累加或宣称物理空间节省。 |
| 创建草稿 PR #1 并运行 CI | 在真正的一次性 macOS runner 验证比较可执行性 | 首次普通 CI 发现两处已有 import 格式问题，已 gofmt；nanobrew 预检实测 uv 的额外 bzip2/xz 闭包，不能与 gobrew 的系统依赖语义直接比较。失败原始证据保存在 `docs/benchmarks/2026-10-28-nanobrew-attempt1-evidence.tar.gz`。 |
| 分离不可比结果与执行失败，继续预检其余候选 | 一个候选的真实依赖差异不应挡住 hello/jq 的验证 | 先以失败测试复现提前停止，再改为记录 uv 不可比、寻找下一例等价安装；不放宽版本/依赖/SHA 比较。7 项 Python 测试通过；同时校正 gobrew 缓存统计到 `artifacts/blobs` 与 `artifacts/trees`。 |
| 取消跟踪误提交的构建产物并忽略 Python bytecode | 保持源码与验证证据清晰 | 根目录 `benchmark` 二进制仍保留在本地，后续不再纳入 Git；未删除用户数据。 |
| 复现并修复显式降级绕过已安装消费者最低版本的问题 | 相同 ABI 标记不代表旧依赖满足已记录的 runtime minimum | 版本不足／修订不足，在消费者 pin/unpin 两种状态下共四例先失败；在反向依赖扩展完成后检查保留消费者，低于最低值时拒绝，满足最低值或同步替换消费者仍允许。resolver race 测试通过。 |
| 修正 no-op 文本，不再无依据声称 up-to-date | 无本地目录的后备安装及历史指定版本只能证明已安装 | 新命令入口测试先因旧文案失败，去掉 `and up-to-date` 后通过；CLI 全包 race 通过，保留版本及 reinstall 指引，无新增状态。 |
| 对 Apply 的主要开销做同版独立归因 | 只在测量证明可控收益时保留优化 | 完整证书包 Apply 7433.961ms；原样初始化脚本五次中位数 6482.646ms，189 张证书、286958 字节、输出 SHA 全部一致。正式组暂停其他构建/测试，有干扰探索组另存。未发现值得保留的最小产品优化；不省略信任检查，不产生虚假优化提交。证据见 `docs/verification/2026-10-28-apply-profile/`。 |
| 复现并修复历史配方独有依赖遗漏 | 当前配方的索引闭包可能不包含旧版依赖 | Engine.Plan 的历史 demo→oldlib→leaf fixture 先因 oldlib 找不到失败；显式版本改为读取一代完整目录再替换私有配方，普通请求仍用原快路径。新测试确认依赖顺序、历史 pin 及持久目录未修改；engine/catalog/resolver race 通过。 |
| 完成 nanobrew 逐候选预检与五轮同机对照 | 用实际等价安装替代宣传数字 | hello、jq 的完整闭包/SHA、执行、签名、链接及五轮校验通过；gobrew 冷安装更慢、热重装和 no-op 更快；uv 依赖集合不同，明确不可比，无速度结论。最后一组 `7e11f28` 记录 Apple M1 Virtual、3 CPU、7 GiB；前三次运行及其失败或完整样本均保留，没有挑选最快组。 |
| 独立复核新增降级、历史依赖与比较器结果分类 | 防止修复扩大行为或失败被记成通过 | 复核未发现阻塞；命令/签名失败仍非零，只有成功安装后的闭包差异记为不可比且无速度摘要。相关测试通过；之后测量期间各代理只作静态文档工作。 |
| 对最终产品源码 `384b44d` 重跑完整质量检查与构建 | 审查发现的两处安装修复后重新验证 | 格式、模块校验、vet、全量 race、16 个教程示例、govulncheck@v1.9.0 和 CGO=0 构建全部通过；远端对应 CI 亦通过。重建二进制的 SHA 为 `851e4c324529eb9703371ac5a0b63364a0adbfd6010b0e08308c2b2797a89dbd`，正在补最终实瓶和性能证据。 |
| 对同一最终二进制顺序重跑 bootstrap、两个实瓶 smoke 和五轮基准 | 确认产品修复后门禁仍成立 | 全部通过；冷安装 10.541s 对 14.820s、热重装 7.801s 对 11.825s、no-op 13.228ms 对 344.093ms。最终质量/smoke 证据在 `docs/verification/2026-10-28-final/`，性能原始数据单独保存为 `2026-10-28-m3-pro-384b44d`，未替换先前基线。 |
| 再次检查 ub-mode 专用工具与工作树 | 如实记录未运行检查及清理边界 | crapper/dryer/mutator 在会话与 PATH 仍不可用，未运行、未评分；没有新建 managed worktree，唯一工作树是当前仓库。 |
| 按用户要求暂停，重启后收到“继续”再恢复 | 保持暂停期间不推进工作 | 目标先标记 paused；恢复后确认代码、未提交文档和仓库内归档均完整，最终二进制 SHA 与报告一致。已知本轮 `/tmp` 实验目录重启后均不存在，原始证据已归档。 |
| 对最终报告与归档再次独立只读复核，提交 `e11b470` | 确认数字、构建身份及结论可追溯 | 文档链接、完整样本、归档内外 JSON、命令输出与预期退出码均一致，未发现阻塞问题；产品代码与最终已测 `384b44d` 无差异。 |
| 等待最终提交的远端检查，重新排队一次未分配 runner 的 CI | 保持原检查要求并处理执行基础设施停滞 | nanobrew [最终 PR 检查](https://github.com/shqingda/gobrew/actions/runs/40704359757)通过；常规 CI 首次排队约 10 分钟未执行，取消后重跑同一提交，[第二次执行](https://github.com/shqingda/gobrew/actions/runs/40704359761)全部通过，没有删减检查。 |
| 合并 PR #1 并快进同步本地 main，记录交付完成 | 完成已批准计划与 ub-mode 的主线交付 | 远端合并提交 `00d43c6a5bf4c44555ccd065fba74c789e4ab78d`，保留有序提交；本地 main 同步，追加本次文档完成记录。无额外工作树待清理，未打 tag 或正式发版。 |

## 2026-10-28：后续验证方式

按用户要求，日常验证改为本地 release 模式构建，交付产物／路径及安装、测试说明，由用户安装后测试；只有明确确认发版才使用 GitHub Actions。规则写入 CONSTRAINTS.md，README 与 AGENTS.md 提供入口；移除普通 push/PR 的自动 CI 及 nanobrew 比较触发，保留正式发布检查。此次只调整文档和触发条件，本地核对，不执行 Actions 验证。

随后新增 `make check`／`make build` 统一入口，由 `scripts/ci-check.sh` 在本地执行原 CI 全部检查及已有比较器测试，再构建带提交信息的 dev 测试程序与 SHA-256、验证 bootstrap；发布 CI 复用该脚本。实际本地运行全部通过，工作流 YAML 与触发条件核对通过，独立审查无阻塞。已请求取消此前普通提交触发的 CI（run 40706690325），此次没有启动 Actions；正式发布的实瓶及性能门禁保持不变。

## 2026-10-28：卸载后清理无用依赖

用户实测卸载 jq 后 oniguruma 遗留。回归测试先复现只返回 jq 的失败，再在 Engine.Uninstall 原有单次 MutateRecords 内复用 autoremove 的可达性规则，同时卸载无用依赖；保留主动安装、pin 和其他主动安装包仍需要的依赖，仍拒绝直接卸载被依赖的包。补充 requested 含义和卸载语义文档。

四组回归检查记录与真实 keg 的删除／保留，包含传递依赖；本地 `make check` 全部通过。隔离临时 prefix/HOME/cache 的真实 jq 1.8.2 + oniguruma 6.9.10 验证通过：jq 可执行、直接删除依赖被拒绝、卸载 jq 自动移除依赖、显式安装的 oniguruma 保留。未修改用户安装，也未运行 GitHub Actions。审查确认复用原有写锁、归属校验和 durable journal，无新事务框架。命令与日志见 `docs/verification/2026-10-28-uninstall/`。

## 2026-10-28：明确卸载结果

按用户反馈参考 Homebrew 的卸载／自动清理提示，普通文本改为 `Uninstalled NAME`，自动移除的依赖另列 `Autoremoved N unused dependency/dependencies` 标题；仅成功后显示，JSON 数组协议不变，不额外扫描磁盘或改变卸载行为。按低影响文案变更处理，未添加只断言字符串的单元测试。

本地 `make check` 全部通过；临时 prefix/HOME/cache 的 jq 实瓶复测确认显式卸载、自动依赖清理和 requested 保留的文本正确，JSON uninstall 仍返回名称数组。自审确认错误输出与终端文本清洗保留。未运行 Actions、未正式发版，继续由用户安装测试。参考：https://docs.brew.sh/rubydoc/Homebrew/Cleanup.html 。本地日志：`/tmp/gobrew-uninstall-output-check.log`、`/tmp/gobrew-uninstall-output-smoke.log`。

## 2026-10-29：发布 v0.4.0

用户明确要求发版，授权本次使用 GitHub Actions。相对 v0.3.0 包含指定版本安装及安装／更新／清理行为调整，采用次版本 v0.4.0；新增面向用户的发布说明，特别注明自动卸载依赖和 dev 测试版升级方法。发布前本地 `make check` 全部通过，日志 `/tmp/gobrew-v0.4.0-check.log`。代码沿用已审查和验证的 main；保留原有代码检查、macOS 15/26 实瓶及五轮性能发布门槛，发布状态待远端运行确认。
