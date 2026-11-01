# gobrew v0.5 整理计划：清理、重构、性能、体验、测试与文档

## Context

v0.4.0 已发布，上一轮计划全部完成。这一轮不加新命令，目标有六个：清理冗余代码和文件，拆分过大的文件，修补架构上的小越界，只做没有争议的性能优化，打磨交互细节，清理或调整测试，整理文档。

约束：CONSTRAINTS.md 和 docs/architecture.md 照常适用。必须保持 Plan/Prepare/Apply 契约、不可变的 prepared 产物，以及 durable journal。不能削弱签名、摘要、持久化或文件系统隔离。测试一律使用临时 prefix，不碰开发机上的 Homebrew。不引入通用框架。日常只在本地跑 `make check`，不触发 Actions。**`docs/go-tutorial.md` 不改**，`scripts/check-tutorial.go` 也照常执行。

已确定的取舍：
- 新功能只写进最后的“后续阶段”，本轮不实施。
- 历史交接文档和 tasks/ 下的旧文件直接删除，靠 git 历史追溯。
- 性能只做无争议项，有争议的只写评估、不实施。
- 现有证据归档保留不动；以后新产生的大证据放 Release 或本地，仓库里只提交摘要 JSON。

执行方式：每个阶段单独提交，阶段内按单元再细分。重构类改动不改变行为，靠现有测试兜底。修复类改动先写出会失败的测试，再修。

---

## 阶段 0：清理无用文件与代码（低风险，先做）

文件（均未纳入 Git 跟踪，可直接删除）：
- 空目录 `internal/artifact/.proof`、`internal/postinstall/.proof`
- 根目录过时的 `./benchmark` 二进制，以及 `scripts/__pycache__/`

代码：
- 删除只有测试在用的包装函数：`artifact/relocate.go:33` 的 `relocateTree`，改测试调用 `relocateTreeChecked`；`installer/installer.go:363` 的 `changeLink`，改测试调用 `changeLinks`。
- 删除 `state.SyncDir`（`state/store.go:374`）这个单纯转发的包装，调用方直接用 `platform.SyncDir`。
- 去掉未使用的参数：`replacements` 的 `p`（`relocate.go:23`）、`readReceipt` 的 `p`（`manager.go:314`）。
- `postinstall` 的 `step.ResolveSource`（`steps.go:68,92`）runner 从未读取。先确认语义：如果 Homebrew 的步骤定义需要它，就实现读取并补测试；否则在解码时拒绝或删除，不能静默忽略。
- 把 `resolver.SelectBottle` 改为不导出。
- 替换已弃用的写法：`tar.TypeRegA`（`extract.go:103`），以及 `bolt.ErrTimeout` 改从 `bbolt/errors` 引入。
- 给 `java.go:80` 加一行注释，说明两次 `u2()` 会前移读取位置，防止被当成重复表达式误删。
- 用 gopls 的 modernize 建议做机械更新（range-over-int、`maps.Copy`、`slices.*` 等），限定在本阶段动到的文件。
- 保留：`Record.InstalledAt`、`Plan.CatalogGeneration`（属于持久化/JSON 契约），以及 `scripts/benchmark*`、`check-tutorial.go`、各个 workflow。

## 阶段 1：合并重复逻辑并修复持久化不一致

- **原子写**：在 `platform` 新增一个 `WriteFileAtomic`（临时文件 → `SyncFile` → rename → `SyncDir`），替换四处重复实现：`catalog/client.go:346` `atomicWrite`、`selfupdate/install.go:180` `atomicFile`、`shellconfig/shellconfig.go:230` `write`，以及 `installer/installer.go:844` 中适用的部分。
- **修复 catalog 的弱 fsync**：删掉 `catalog/client.go:369` 的私有 `syncDir`，改用 full-flush 的 `platform.SyncDir`。
- **路径组件校验**：在 `model` 新增 `ValidPathComponent`，替换 `installer.go:231`、`artifact/manager.go:300`、`artifact/receipt.go:142`、`catalog/normalize.go:104` 四处；`postinstall/steps.go:218` 只在语义一致时替换。
- **GHCR 认证**：`artifact` 内部的“401 → 取 token → 重试”逻辑有两份（`download.go:65-90`、`receipt.go:162-190`），合并成一个 helper。`catalog/history.go:323` 与 `artifact/download.go:133` 的 token 获取，限额和错误文案各自保留，只抽出公共请求部分；如果抽出来不比现在更简单，就不动。
- **owner marker 名称**：`.gobrew-owner` 改成在 `model` 定义一个常量，`installer.go:25` 和 `artifact/extract.go:76` 都引用它。
- **User-Agent**：统一用一个 `model` 常量，不做通用 HTTP 封装。

## 阶段 2：拆分大文件（只移动代码，不改行为）

文件按职责拆分，包不变：
- `internal/cli/cli.go`（1564 行）拆成 `app.go`、`root.go`、`init.go`、`query.go`、`update.go`、`install.go`、`cleanup.go`、`render.go`。划分以探查给出的行号分组为准。
  - `root()`（103 行）的子命令注册按组拆开。
  - `infoCommand` 和 `searchCommand` 嵌套较深的渲染部分抽成 `render*` 函数。
  - 新增 `envSettings`，在 `Run` 里一次读完所有 `GOBREW_*` 变量（目前散落在 524、802-817、909、1136、1166-1202 各行）。
- `internal/installer/installer.go`（864 行）拆成 `apply.go`、`links.go`、`journal.go`、`records.go`、`postinstall.go`。
  - `stage`（107 行）拆出 `stagePackage` 和 `backupExistingKeg`。
  - `newID` 改为返回 error，不再 panic（`installer.go:36`）。
- `internal/engine/engine.go`（746 行）拆成 `config.go`、`init.go`、`plan.go`、`operation.go`、`mutate.go`、`maintenance.go`。

## 阶段 3：修补架构上的小越界

- `Init` 不再自己读进程状态（`engine.go:125-133` 中的 `os.Geteuid`、`SUDO_UID/GID`），改由 CLI 通过 `Config` 传入 uid/gid。
- `engine.go:106` 硬编码的 `.gobrew/state.db` 改用 `state` 包提供的 `Exists`，路径只在 `state/store.go:55` 定义一处。
- `createOwnedPrefix`、`checkOwnedPrefix`（`engine.go:157-213`）里的 mkdir、chown、stat 原语移到 `platform`，engine 只保留编排。
- `sudo` 改用注入的 `a.stderr`（`cli.go:478`）。
- 在 `docs/architecture.md` 的依赖方向里补上 `postinstall`。

## 阶段 4：性能（只做无争议项）

每项先归因测量，再改，再复测；拿不出可证明的收益就回退，并在文档里说明。
1. **GHCR token 预取**：对 ghcr.io 直接先取 token，一个计划尽量只取一次（可以合并多个 scope），省掉每个 bottle 一次 401 往返（`download.go:77-90`）。这是冷安装 Prepare 阶段落后 nanobrew 的主要嫌疑。
2. **去掉重复哈希**：刚下载并已校验过的 blob，在 `extractBottle`（`manager.go:236`）不再重算 SHA。从缓存读取的 blob 仍然要校验。
3. **包内并行**：`relocate.go:42` 的重定位和 `relocate.go:208-214` 的 codesign 批次，改用有上限的 worker pool。
4. **减少全树遍历**：`logicalSize`（`manager.go:265`）合并到已有的遍历中；每次安装里 cache `Cleanup` 跑了两次（`manager.go:142,163`），合并为一次。
5. **并行 fsync**：`sync.go:112` 的逐文件 `syncInode` 改为并行，最后的根目录 barrier 保持不变。
6. **postinstall 与其他包重叠执行**：ca-certificates 的脚本可以和互不依赖的包的 Apply 重叠进行。前提是状态保存仍然串行，journal 的恢复边界不变。这一项风险最高，放在最后做，必须单独审查。
- **只评估、不实施**：边下载边流式解压、`msync` 去重、合并 `finishPackage` 的多次提交、hardlink 改用 clonefile。评估结论写进 `docs/performance.md`。
- **禁止**：跳过或缓存证书脚本、在 SHA 校验通过前信任数据、去掉 F_FULLFSYNC 或 journal、跳过 codesign 或链接检查。

## 阶段 5：交互体验打磨

- **进度**：postinstall 和 relocate 期间显示 spinner 或已用时间；安装结束时给出总耗时。不在终端时，保留关键行并带上耗时。
- **postinstall 输出**：formula 的 `warn` 和脚本的 stderr 在文本模式下以 Warning 转发给用户（目前是 `installer.go:715` 的 `io.Discard`）；JSON 模式下仍不混入 stdout。
- **颜色**：`search`（`cli.go:527`）要遵守 `NO_COLOR` 和 `TERM=dumb`；终端检测统一成一个函数（目前 `cli.go:464` 与 `progress.go:33` 各有一套）。
- **文案统一**：
  - 提示里的命令名统一成一种。随 README 一起决定：建议用实际执行的程序名，不硬编码。
  - Formula/formulae 的大小写统一。
  - 错误前缀统一成 `Error:`。
  - “Updated gobrew”的样式统一。
  - Warning 一律输出到 stderr（`cli.go:1495`、`1509`）。
  - `pin`/`unpin` 改为输出完整的句子。
- **错误提示**：
  - 下载遇到 404 时提示运行 `update`；遇到 401/403/429 时提示设置 token（`download.go:95`）。
  - 缓存参数错误时写明是哪个 flag（`cli.go:231`）。
  - 用法错误提示具体子命令的 `--help`。
- **退出码**：被 pin 或 held 挡下的升级单独使用一个退出码和 JSON code（`cli.go:1449-1458`），并同步更新 README 的退出码表。这一项改变了对外契约，在发布说明中注明。
- **补全与帮助**：`install`、`reinstall`、`version-install`、`deps` 的补全改为从本地目录取候选名（离线、轻量）；常用命令补上 `Example`。

## 阶段 6：测试清理与调整

原则沿用 CONSTRAINTS.md：只测高损失的边界和可观察行为，不测平凡的转发或数据容器。
- **归位**：`cli/path_test.go` 里与 path 无关的用例移到对应的测试文件，包括进度渲染、命令别名、列布局、自动更新（`:98`、`:122`、`:133`、`:169`、`:187`）。
- **审视只断言字符串的用例**：`TestProgressRendersHomebrewStyle`、`TestColumnsMatchHomebrewLayout`、`TestShadowCaveatMatchesHomebrew` 等，只保留真正保护可见契约的断言；纯样式断言删掉，或改成检查结构。
- **拆分** `installer_test.go`（805 行），与阶段 2 的源码拆分对应：apply、links、postinstall、cleanup。
- **共享夹具**：新建 `internal/testutil`，只允许 `_test.go` 引用，只依赖 `model`、`state` 和标准库。提供 `InstalledPrefix`、`OwnedKeg`、`Tree`、`TarGz`、`NoNetwork`，以及局部的 `preparedPackage` helper。用它替换 installer 中 13 处重复的 Prepared 字面量、17 处 bottle 树构造，以及 engine 和 cli 中重复的已安装记录和 store 初始化。
- **去掉对内部实现的耦合**：`cli/cleanup_test.go:49,103` 目前直接调用 `installer.Apply`，改为通过 engine 或 testutil 构造状态。
- **删除冗余**：随阶段 0 删除测试专用的包装后，同步调整相关测试；合并互相覆盖的用例。每删一个都要说明对应的保护由哪个测试承担。
- 新代码的测试：阶段 1 的原子写和路径校验、阶段 3 的 uid 注入、阶段 4 的并行化（race），以及阶段 5 的退出码和 Warning 去向。

## 阶段 7：文档整理（不动 go-tutorial.md）

- 删除 `docs/CLAUDE-HANDOFF.md`、`tasks/plan.md`、`tasks/todo.md`、`tasks/work-log.md`。同步修改引用它们的 `docs/verification/2026-10-28-final/README.md`，必要时把原链接改成提交哈希。
- 新建一份简短的 `docs/status.md`：写当前版本、已支持和不支持的范围、延后的功能、验证方式，并指向 0001 决定。本轮的计划和进度放在新的 `tasks/plan.md` 和 `tasks/todo.md`。
- 整理 README：
  - 日常操作示例补上 `link`/`unlink`、`deps`、`uses`、`shellenv`。
  - 退出码、环境变量和阶段 5 的文案与实际代码保持一致。
  - 历史性能段落精简，改为链接到 `docs/performance.md`。
- 精简 `docs/performance.md` 和 `docs/nanobrew-comparison.md` 中重复的历史叙述，原始证据和链接保留。补写阶段 4 的测量结果和评估结论。
- `docs/architecture.md` 同步阶段 2、3 后的文件布局和依赖方向。
- 在 `CONSTRAINTS.md` 补一条：今后大的证据归档放 Release 或本地，仓库只提交摘要 JSON。
- 在 `docs/verification/2026-10-28-uninstall/gobrew-uninstall-smoke.sh` 顶部注明它是一次性证据，不是可复用脚本。

## 后续阶段（本轮不实施，仅列候选）

按 0001 决定，优先级从高到低：软件清单导出与恢复；`home`、`desc`、`log` 等辅助入口；依赖方仅过时时的自动升级评估；Homebrew JSON 兼容（按需）；tap、services、Cask、漏洞查询。

## 验证

- 每个阶段结束都在本地跑 `make check`，内容包括 gofmt、go vet、全量 `go test -race`、教程示例、比较器测试、govulncheck、CGO=0 release 构建和 bootstrap。
- 阶段 2、6 结束后，对比重构前后的测试名单和覆盖范围，确认没有误删保护。
- 阶段 4、5 结束后，在隔离的 HOME、prefix 和缓存中跑 `scripts/smoke.sh` 和 `scripts/smoke-version.sh` 的实瓶测试。用 `scripts/benchmark` 做五轮 Homebrew 对照（冷安装、热重装、no-op），结果另存一份新的 JSON，不覆盖旧基线。nanobrew 对照需要 Actions，留到用户确认发版时再跑。
- 手动检查 UX：分别在 TTY、`NO_COLOR`、管道和 `--json` 下运行 install、search、info、uninstall 和错误场景，确认 stdout 仍是单一 JSON 文档。
- 交付时提供本地 release 构建的路径、提交和 SHA-256，由用户安装测试；不打 tag、不触发 Actions。
