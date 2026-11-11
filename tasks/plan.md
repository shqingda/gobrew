# gobrew v0.6 计划：修复、体验、性能与功能补足

## Context

v0.5.0 已发布，main 上有 7 个未发布提交（`self` 恢复、多批 brew flag 对齐、`--prefix/--cellar/--cache`、网络重试）。用户要求盘点还要做的功能、性能、UI/UX 和需补足/修复之处并排期。

本计划基于三路只读审查（CLI 体验、性能热点、缺陷/测试/文档）加对关键项的源码核对。约束照旧：CONSTRAINTS.md、docs/architecture.md、Plan/Prepare/Apply 契约、不可变 prepared 产物、durable journal；不削弱签名、摘要、持久化与文件系统隔离；测试只用临时 prefix；不加通用框架；日常只跑本地 `make check`，不触发 Actions、不打 tag。

执行方式沿用 v0.5：每阶段单独提交；修复类先写会失败的测试再改；性能类先归因测量、再改、再复测，无可证明收益即回退并记录。

标注：【已核对】= 我亲自看过源码；其余来自审查报告，动手前先复核。

---

## 阶段 0：修复缺陷（最高优先，全部不改契约）

1. **catalog 快照永不回收**【已核对：全仓只有写入 `client.go:207` 和读取 `:309`】。每次上游变化生成一个约 59 MB 的 `snapshots/<gen>.db`，每日自动更新下无上限增长。
   - 在 `catalog.Client` 发布新 `CURRENT` 成功后删除非当前快照（保留当前一代；正被只读打开的文件在 macOS 上 unlink 安全）；`cleanup` 也调用同一函数。
2. **fish 的 MANPATH 丢系统路径**【已核对：`shellconfig.go:96`】。未设 MANPATH 时结果无空元素，`man ls` 失效。改为与 POSIX 版同义（保留空元素）；INFOPATH 同查。补 fish/csh/pwsh 输出断言（`cli_test.go:112` 目前只执行 sh）。
3. **手工 PATH 提示跑不通**【已核对：`path.go:43`】。`eval "$(gobrew shellenv)"` 在 gobrew 不在 PATH 时无效，且 shellenv 不含 `~/.local/bin`。提示改用可执行文件绝对路径，并一并给出命令目录。
4. **下载整体超时**【已核对：`manager.go:57` `Timeout: 15m` 覆盖响应体】。慢网大 bottle 超时后从头重试 4 次。改为连接/响应头超时 + 读空闲超时（包一层带 deadline 重置的 Reader，仍每次全量校验）。catalog 的 2 分钟整体超时（`client.go:183`）同样处理。
5. **`--prune` 天数溢出**【已核对：`cleanup.go:94,136,209`】。`time.Duration(days)*24h` 无上界，大值溢出反而清空缓存。加上界校验并按用法错误报出；`GOBREW_AUTO_UPDATE_SECS`（`update.go:129`）同查。
6. **成功的安装因缓存淘汰失败而报错**：`manager.go:156-159` 的 `Release` 把 Cleanup 错误 Join 进返回值，而 `Prepare`（`:140`）忽略。统一为警告，不改变已成功操作的退出码。
7. **post-install 输出末行丢失**：`installer/postinstall.go:261-276` 的 `outputEvents` 未 flush 无换行的尾段。
8. **空目录累积**：`.gobrew/staging|backups|trash/<id>` 空父目录从不删除（`apply.go:97`、`records.go:48`、`cleanup.go:207-240`）。操作成功后与 cleanup 时移除空目录。
9. **symlink 步骤的 `uninstall` 字段被接受但从不读取**（`postinstall/steps.go:83`）。确认上游语义后实现或在解码时拒绝，不静默忽略。
10. **未知 macOS 主版本时 variations 不合并**（`normalize.go:91-104`；版本表在 `resolver/bottle.go:15` 与 `normalize.go:17` 各一份）。合并为一份表，未知新版本时与 `HostTag` 一致地回退到最近的已知版本。
11. **`update` 自更新失败同一错误输出两次**（`update.go:98-99,114`）；`outdated <name>` 文本模式退出 1 而 `--json` 退出 0（`update.go:268`）；`info --analytics` 打印 `Error:` 却退出 0（`query.go:578`）；`open` 失败报成 `write output`（`query.go:186,449,1174`）。逐项统一。
12. **install.sh**：Rosetta 终端被误判为 Intel（`:19`，改查 `sysctl.proc_translated`/`hw.optional.arm64`）；`:24,40,68-69` 的静默 `exit 1` 补原因；`uninstall.sh:55-65` 加 trap 清理 `.gobrew-write-*`。`scripts/test-bootstrap.sh` 补 bash/fish 分支。

## 阶段 1：并发与只读可用性（需要你的一个决定，见文末 D1）

- 【已核对：`store.go:164-204`】`WithLock` 在整个 Apply 期间持有读写 bbolt 句柄，含外部 post-install（证书约 6.5 秒）；并发的 `list`/`info` 250 ms 后报 `ErrBusy` 退出 4。这与 architecture.md:136 “短暂的 bbolt 打开/事务” 不符。
- 推荐做法：`Session` 改为每次读写短开短关（写锁 `writer.lock` 仍全程持有，锁顺序不变），使只读命令在安装期间可用；补“安装进行中并发只读”测试。
- 同时：等待写锁超过约 1 秒时在 stderr 提示正在等待另一个 gobrew 操作，超时错误写明锁路径（`store.go:180-188`）。

## 阶段 2：错误消息与文案统一（UX）

- **“找不到 Formula” 两套文案**：统一为 brew 的 `No available formula with the name "x".`，并基于本地 catalog 给 `Did you mean` 建议（`brewpath.go:73` 已有 brew 文案；`catalog/client.go:70`、`resolver.go:48`、`query.go:308` 待改）。
- **“未安装” 三套文案**：统一为 `No such keg: …`（`mutate.go:19,55`、`query.go:298`）。
- **内部前缀与术语外泄**：`unsupported:`、`conflict:`、`installation needs repair:`、`create a new plan`、`dependency closure`、`versioned receipt fact` 等。在 `cli/app.go` 的错误映射层按 sentinel 改写成面向用户的句子；`model` 的 sentinel 不动，JSON `code` 不变。
- **补可操作提示**：uninstall 被依赖时提示 `gobrew uses --installed X`；doctor 退出 5、`Needs repair:`、`Not executed:` 给出下一条命令（`postinstall`/`link`/`doctor --repair`）；`doctor` 的原始状态串（`maintenance.go:47-57`）改成人话。
- **Ctrl-C**：把 `Error: context canceled` 改为说明已中断、状态安全或下次自动恢复（`app.go:177-202`）。
- **颜色**：`==>` 标题统一走 `p.title()`（`install.go:218,391`、`cleanup.go:112,202,210`、`update.go:59,101`）；`Warning:`/`Error:` 在 TTY 上着黄/红；认 `HOMEBREW_NO_COLOR`；`GOBREW_NO_EMOJI` 对 info 的 ✔/✘ 也生效（`query.go:866-882`）。
- **小项**：用法错误句号/大小写统一；`Formulae` 大小写；环境变量写法统一带不带 `$`；提示中的程序名跟随实际调用名（`gob`/`gobrew`）；dry-run 空计划给出“无需变更”与总下载量；`autoremove` 加标题与 uninstall 一致。

## 阶段 3：进度与静默阶段（UX）

- **首次 catalog 下载、`update`、`self update`、auto-update 没有进度**（`engine.go:69-86`、`update.go:41-53,137`）。接入现有 `withEvents` 与 `progress.go` 的下载条；catalog 的验签/建索引阶段显示带计时的状态行。
- **relocate/codesign/link 阶段无事件**（`progress.go:93-94` 丢弃 relocate）。大包（node、python）上显示阶段名与计时；复用 post-install 已有的秒表（`progress.go:130-142`）。
- 下载重试时输出一行 `Retrying…`（`download.go:70`）；进度条加速度；处理 SIGWINCH（低优先）。
- 首次 install 的输出顺序：先 sudo 说明与 prefix 创建确认，再 auto-update；PATH 自动写入时说明同名命令将先于 Homebrew（`path.go:55-59`、`install.go:96-131`）。
- `--json` 模式：把 PATH/cleanup 的 `notes` 与 auto-update 失败写进结果信封（`install.go:138,401-411`、`update.go:147`）；warning 遵守 `--quiet`。

## 阶段 4：帮助与 flag 对齐（功能，小）

- 全局接受 brew 的 `-v/--verbose`、`-d/--debug`（目前 `install -v` 是用法错误）；verbose 至少打印阶段计时与重试，debug 打印请求 URL 与缓存命中。
- `--cask` 给出明确的“gobrew 只支持 Formula”而不是 unknown flag（`caskflags.go`）。
- brew 有而 gobrew 没有的命令（`tap`、`services`、`bundle`、`fetch` 等）给出“不支持，见 status.md”的明确拒绝，其余未知命令给拼写建议（`root.go:18`）。
- 帮助：新增 `gobrew help environment`（或根 Long 段）列出全部 `GOBREW_*`，让四处 “see `gobrew help`”（`update.go:140`、`cleanup.go:221`、`path.go:266`、`query.go:1153`）成立；给 info/list/deps/uses/cleanup/doctor/link/pin/uninstall/outdated/update/self 补 Example；`uninstall` 说明提到自动移除孤儿依赖；`--prefix/--cellar/--cache` 取消 Hidden 或在根帮助注明。
- 半实现 flag：`search --formula`、`deps --full-name` 的未读变量（`query.go:29,79,1131`）要么实现要么删变量保留 flag 并注释。
- `--minimum-version`/`--min-version` 的重复处理合并并拒绝同时给出（`install.go:31`、`update.go:207`）。

## 阶段 5：新增小功能（按 0001 的延后清单，推荐做前三项）

1. `config`：打印版本、prefix、cache、catalog 代数与刷新时间、CLI 托管状态、生效的 `GOBREW_*`、macOS/CPU。问题报告的必需品；brew 有同名命令，不违背 0002。
2. `leaves`、`desc`、`home`：都是本地 catalog/state 的薄查询，复用 `engine/query.go` 与 `engine/deps.go` 的现有函数。
3. `doctor` 补检查：catalog 过期/缺失、缓存目录可写、CLI 托管与版本、启动文件标记块损坏、平台；`--repair` 能修 gobrew 自己声明的缺失链接。
4. （见 D2）shell 补全、清单导出/恢复、`fetch`、brew `--json=v2`：等你决定。

## 阶段 6：性能（先测量，再动手）

**6a. 先补测量缺口**（基准此前禁用了自动更新和安装后清理，且只在空 prefix 上测）：
- 用 `fs_usage` 统计一次热安装的 `F_FULLFSYNC` 与 bbolt commit 次数（审查推测 hello+jq 约 60 次）。
- 在预装约 100 个包的临时 prefix 上测默认开启清理时的 install 与 Apply。
- 对 `update` 做 CPU profile，拆分网络/验签/normalize/publish。
- 结果以摘要 JSON/Markdown 记入 `docs/benchmarks/` 与 `docs/performance.md`。

**6b. 不触红线、预期有收益的项**（按预期收益排序）：
1. **安装后清理扫描全部 keg 的每个文件**（`installer/cleanup.go:66-121`，每次 install/upgrade 后触发）。把 Prepare 阶段 `artifact/linkage.go:19` 已解析的 Cellar 引用存进 record，清理时读记录；有 post-install 的 keg 保留实时扫描兜底。需证明保护结果仍然保守。
2. **只读查询全量解码 catalog**（每次约 55 ms / 28 MB）：`info`、`outdated`、`namedFormulae` 改走已有的按 key 读取（`LoadForPlan`）；加 alias bucket 消除别名回退全量（`client.go:55-66,349-354`）；`search` 加精简 bucket；`update` 后的 `Outdated` 复用内存快照（`update.go:46,51`）。提升 catalog schema 版本。
3. **`update` 未变化时不重建**：generation 在 normalize 前已算出（`client.go:150-154`），快照已存在则只重写 `CURRENT`；加 ETag/If-None-Match。normalize 的三次 JSON 往返改为直接合并并并行（`normalize.go:85-108`）；publish 排序插入并设 FillPercent。
4. **状态整份 JSON 重写**（`state/store.go:251-301`，journal 带 Before/After 两份全量）：按 key 存 record、generation 独立 key、journal 记增量。需 schema 迁移与恢复测试；已装规模大时收益大。
5. **刷盘批量化（不改提交点）**：`moveKeg` 改用已有 `platform.SyncDirs`（`journal.go:51`、`sync.go:25`）；stage 阶段先全部 clone 再统一 barrier（`apply.go:144,153`）；Prepare 的 manifest 写在 `SyncTree` 之前由同一 barrier 覆盖（`manager.go:262-291`）。每项都要过中断恢复测试。
6. **冷安装 Prepare**（落后 nanobrew 的部分）：命令开始时并发预热 ghcr.io 与 CDN 的 TLS；缺 receipt 的 bottle 的 OCI index/manifest 与 blob 并发预取而不占 CPU 槽（`receipt.go:67,99`、`manager.go:219-247`）；relocate 去掉两次无效 chmod、先只读锚点预检再进入完整处理、包内文件并行（`relocate.go:37-178`，此前只测过 fsync/codesign 并行）；解压缓存已建目录（`extract.go:96`）。
7. **小项**：auto-update 的自更新与 catalog 刷新并发（`update.go:42,46`）；`uses`/`deps` 全量的跨候选备忘（`engine/query.go:115-141`、`deps.go:231-391`）；`info` analytics 并发上限（`query.go:657-671`）；Manager 与 engine 两套并发默认值统一（`manager.go:44-48`、`engine.go:39`）；下载断点续传（Range，续传后仍全量校验）。

**6c. 触及持久化边界、默认不做**（见 D3）：合并 `finishPackage` 的 bbolt 事务；缩小 `syncPostInstall` 范围。证书脚本约 6.5 秒按约束不跳过、不缓存。

## 阶段 7：测试补足

- journal 中断注入扩展到升级（备份旧 keg + 发布新 keg 的倒序撤销，`journal.go:108`）、卸载与 autoremove 的 trash 移动（`records.go:37-70`）、未知 phase（`journal.go:102`）。
- `catalog/history`（唯一未签名元数据路径）：`pour_bottle?`、revision/rebuild/version 不匹配、异常 `formulaPath` 的拒绝用例。
- 随各阶段新增：快照回收、fish 输出、空闲超时、溢出校验、并发只读、错误文案映射。
- 复用 `internal/testutil` 的现有夹具，不新建框架。

## 阶段 8：文档与发布工程

- 写 `docs/releases/v0.6.0.md` 草稿（0003 要求写明 `self` 恢复）；`release.yml:60-65` 缺说明文件时失败而非 `--generate-notes`【已核对】；发布门禁加跑 `smoke-version.sh`，且 smoke 测最终发布的同一二进制（`release.yml:13-14`）。这两处 workflow 修改只改文件，不触发 Actions。
- README：补未记录的 flag（`4a2b124`、`14d7f53` 引入的）与 `GOBREW_DISPLAY_INSTALL_TIMES`、`GOBREW_INSTALL_BIN_DIR`。
- `docs/architecture.md:19-25` 文件清单与 `:136` 锁描述同步；`docs/status.md` 的差异清单补 `-v/-d`，并随本计划各项更新。
- `tasks/plan.md`、`tasks/todo.md` 换成本轮内容。
- `query.go`（1396 行）按 search/info/list/deps/shellenv 拆文件，只移动不改行为；install.sh/selfupdate 的三个大小上限对齐。

---

## 需要你决定的事项（默认按推荐执行）

- **D1 并发只读**：推荐改为短开短关 bbolt（阶段 1）。备选是只改文档、承认安装期间只读命令会 busy。
- **D2 延后功能**：推荐本轮只做 `config`/`leaves`/`desc`/`home`/doctor 增强。shell 补全在 0002 中被明确删除（brew 以独立脚本而非命令提供补全）——若要恢复，建议以随发布包附带的静态补全脚本形式，不新增命令；清单导出/恢复、Cask、tap、services、brew `--json=v2` 继续延后。
- **D3 持久化边界**：推荐不合并 `finishPackage` 事务、不缩小 post-install 同步范围，除非 6a 的测量显示它们是主要开销，届时再单独提请。

## 建议顺序

阶段 0 → 1 → 2 → 3 → 4 → 8（文档部分）可直接进行；阶段 6 先做 6a 再排 6b；阶段 5、7 穿插。阶段 0–4 完成后即可交付一个 dev 构建供试用。

## 验证

- 每阶段：`make check`（gofmt、go vet、`go test -race`、教程示例、govulncheck、CGO=0 release 构建、bootstrap）。
- 安装行为变更（阶段 0 的 4/6/8/10、阶段 1、阶段 6）：隔离 prefix/HOME/缓存下运行 `scripts/smoke.sh` 与 `scripts/smoke-version.sh`。
- UX（阶段 2–4）：在 TTY、管道、`NO_COLOR`、`--json`、`--quiet` 下手工跑 install、search、info、uninstall、doctor 及错误场景（未知 formula、未安装、被依赖卸载、Ctrl-C、锁占用），确认 stdout 仍是单一 JSON 文档、stderr 无转义字符泄漏。
- 性能：`scripts/benchmark` 五轮冷/热/no-op 对照，另加“预装 100 包 + 默认清理开启”场景；结果另存新 JSON，不覆盖旧基线。nanobrew 重测与发布门禁需 Actions，等你确认发版。
- 交付：本地 release 构建的路径、源码提交与 SHA-256；不打 tag、不触发 Actions。
