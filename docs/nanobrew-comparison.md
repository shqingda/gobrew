# nanobrew 实机对照记录

2026-10-28 的同机五轮对照中，gobrew 安装 `hello` 和 `jq` 的**冷安装中位数都比 nanobrew 慢，热缓存重新安装和已安装 no-op 都更快**。`uv` 的安装依赖集合不同，不能计算等价速度比。结论仅适用于下面记录的二进制、Formula、runner 和测量条件，不能概括为 gobrew 整体快于 nanobrew。

本页主结果完整取自分支头 `7e11f28b1e735d87890b334655b27ec3193b3fd7` 的一次成功运行。该提交仅增加 runner 硬件记录，产品源码与 `384b44d26e52485924ad4415c4099a914a9c911a` 相同。

## 被测版本与证据

- 测量开始：2026-10-28 20:53:04（Asia/Taipei），即 `2026-10-28T12:53:04Z`。
- 机器：一次性 GitHub hosted `macos-26` ARM64 runner，实测 `Apple M1 (Virtual)`、7 GiB 内存（7516192768 字节）、3 个逻辑 CPU；实际系统 `macOS-26.6.2-arm64-arm-64bit-Mach-O`，镜像 `20261013.0424.1`。不是 [Homebrew 对照](performance.md)使用的本地 M3 Pro，不能跨机器直接比较绝对耗时。
- gobrew 分支头：`7e11f28b1e735d87890b334655b27ec3193b3fd7`。PR 工作流实际检出并构建的合并提交（JSON 的 `source_commit`）为 `851ee8f8c9ff3fef23c7e47106c3bf096002ffb2`，两者不是同一个 SHA。
- gobrew 使用 Go 1.27.3、`CGO_ENABLED=0 go build -trimpath` 构建，二进制 SHA-256：`bb1d19047dedd43da13ebc95a1f62c437700d7e8d34746a197fea4a152505cb5`。
- nanobrew 使用未修改的 [v0.1.212 ARM64 发布物](https://github.com/justrach/nanobrew/releases/tag/v0.1.212)，对应源码提交 `9be557eb99bcac7f4ea9a489ec7d090c29221f08`；压缩包 SHA-256 为 `17d11f5f0b727db70ef6a330903085999cf50f6ac0733efaffc6615c72727b3a`，二进制 SHA-256 为 `be6d44a25aea87560b9d05cd4bcaef16a540d50372db4d32212c19de2fd713ff`。运行前分别校验这两个摘要。

证据：[工作流运行](https://github.com/shqingda/gobrew/actions/runs/40691414156)、[完整 JSON](benchmarks/2026-10-28-nanobrew-7e11f28.json)、[逐命令参数与原始输出归档](benchmarks/2026-10-28-nanobrew-7e11f28-evidence.tar.gz)。JSON 包含 60 个正式样本和 4 个预检样本；预检不计入五轮中位数。`passed` 表示该 Formula 完成等价校验和五轮测量，不表示 gobrew 获胜，也不替代发布检查。

此前两次完整测量保留为历史：分支头 `560a05a` 的[结果](benchmarks/2026-10-28-nanobrew-560a05a.json)与[原始证据](benchmarks/2026-10-28-nanobrew-560a05a-evidence.tar.gz)，以及 `384b44d` 的[结果](benchmarks/2026-10-28-nanobrew-384b44d.json)与[原始证据](benchmarks/2026-10-28-nanobrew-384b44d-evidence.tar.gz)。主表没有混入其中任何样本。不同 runner 上的测量不是受控 A/B 实验，不将耗时差异归因于这期间的提交。

## 五轮完整命令耗时

每个场景、每个工具均有五个样本。下表单位为毫秒；比例为 gobrew 中位数除以 nanobrew 中位数。

| Formula | 场景 | gobrew 中位数 | nanobrew 中位数 | 耗时比 | gobrew 相对耗时 |
| --- | --- | ---: | ---: | ---: | --- |
| hello | 冷安装 | 697.241 | 471.982 | 1.477 | 多 47.7% |
| hello | 热缓存重新安装 | 56.362 | 137.577 | 0.410 | 少 59.0% |
| hello | 已安装 no-op | 18.132 | 86.184 | 0.210 | 少 79.0% |
| jq | 冷安装 | 771.240 | 587.091 | 1.314 | 多 31.4% |
| jq | 热缓存重新安装 | 70.199 | 149.025 | 0.471 | 少 52.9% |
| jq | 已安装 no-op | 20.338 | 84.320 | 0.241 | 少 75.9% |

全部样本保留，没有删去波动较大的轮次。例如 gobrew 的 jq 第四轮热安装为 96.614 毫秒，nanobrew 的 jq 第五轮热安装为 207.556 毫秒。JSON 的 `p95_ms` 在五样本下取最大值，不能据此推导稳定的尾延迟或统计置信区间。

gobrew 自报阶段的独立中位数如下，单位同为毫秒。各列中位数不能相加作为完整命令的中位数，阶段范围也不能直接与 nanobrew 的 `NB_BENCH` 内部阶段逐项相减。

| Formula | 场景 | Plan | Prepare | Apply |
| --- | --- | ---: | ---: | ---: |
| hello | 冷安装 | 0.764 | 640.372 | 31.615 |
| hello | 热缓存重新安装 | 0.507 | 0.372 | 31.114 |
| hello | 已安装 no-op | 0.514 | 0 | 0 |
| jq | 冷安装 | 0.461 | 706.517 | 48.385 |
| jq | 热缓存重新安装 | 0.464 | 0.524 | 51.632 |
| jq | 已安装 no-op | 0.702 | 0 | 0 |

这组冷安装的 gobrew 时间主要落在 Prepare。当前实验没有进一步隔离网络、解压、重定位和签名开销，不把阶段分布直接解释成某个实现造成的因果差异，也没有通过跳过校验取得优化结果。

## 等价条件与 uv 不可比

正式样本逐次要求完整安装包集合、版本（含 revision）和 bottle SHA-256 相同，并与本轮冻结的 gobrew 签名目录计划一致。两例使用相同的 `arm64_tahoe` bottle：

| 安装请求 | 完整集合 | bottle SHA-256 |
| --- | --- | --- |
| hello | hello 2.12.3 | `ae6237e3001bd354783f469d754cee875ee9828910461b85a5803f5990213dde` |
| jq | jq 1.8.2 | `ca67c64d0aaf1e5472790ec2cc081ff7972316f27095d8a8aab81b3321247036` |
| jq | oniguruma 6.9.10 | `eb6bda3b333f497b5d294388f39fd0902a5c79a52ae16858eff711d2d104cc4d` |

nanobrew 的状态不记录依赖边列表，因此这里比较的是实际安装闭包，未声称逐字段证明两个解析器的全部依赖语义相同。相同 SHA 将 nanobrew 下载的字节绑定到 gobrew 选中的 bottle；版本号相同本身不足以通过。

uv 两端都成功安装并验证 `0.13.6`，uv bottle SHA 也相同；但 gobrew 仅安装 uv，nanobrew 还安装 `bzip2 1.0.8` 和 `xz 5.8.4`。固定 nanobrew 源码在 macOS 上[无条件把 `uses_from_macos` 字符串项加入依赖](https://github.com/justrach/nanobrew/blob/9be557eb99bcac7f4ea9a489ec7d090c29221f08/src/api/client.zig#L1024-L1041)，没有按 `uses_from_macos_bounds` 判断系统是否已提供；[gobrew 的归一化](https://github.com/shqingda/gobrew/blob/560a05a5f60afae95cc5f8f9f7ebfce699b623d5/internal/catalog/normalize.go#L126-L162)则根据上下文、`since` 与宿主系统判断。此次 uv 记录为 `not-comparable`，不提供性能比，不修改任一工具的依赖规则来凑齐集合。

首次 [run 40689222160](https://github.com/shqingda/gobrew/actions/runs/40689222160) 在 uv 预检发现这一差异后停止；[完整失败证据](benchmarks/2026-10-28-nanobrew-attempt1-evidence.tar.gz)保留，构建的 PR 合并提交为 `c78faeb95c5031f653b6a13c46b37ea285ea4f85`。当时脚本只有 uv 预检，尚未产生正式五轮样本。随后只调整比较器：记录不可比候选并继续下一个，hello 预检等价后才开始正式测量；安装失败或验证失败仍是错误，不会被当作不可比静默略过。

## 测量与隔离

- [比较脚本](../scripts/benchmark-nanobrew.py)拒绝开发机和已有 `/opt/nanobrew`、`/opt/nb` 的环境。nanobrew 使用编译时固定的 `/opt/nanobrew/prefix` 及 `/opt/nb` 短链接；gobrew 使用 `/tmp/d8fa3wlo`。短路径满足重定位需求，长度和布局差异仍可能影响工作量，不能当作默认 `/opt/gobrew` 的结果。
- 两端拥有各自的 HOME、TMPDIR、XDG 配置与缓存，PATH 仅包含系统目录。所有被测进程及其子进程经 `sandbox-exec` 拒绝读写预装的 `/opt/homebrew` 和 `/usr/local`；安装后还检查 Mach-O 链接，拒绝这些位置的依赖。没有借用或修改 runner 预装的 Homebrew，也没有在开发机运行原版 nanobrew。
- 计时覆盖完整命令启动至退出，包括两端共同的 `sandbox-exec` 启动开销；没有测量空沙盒基线或扣除固定开销，因此毫秒级 no-op 尤其不能直接外推为不带沙盒的日常耗时。数据是顺序执行的同机对照，每轮顺序交替为 gobrew→nanobrew、nanobrew→gobrew，共五轮；没有清空操作系统页缓存，也没有排除共享 runner 的系统与网络波动。
- 冷安装清空双方的安装状态及下载／准备缓存，保留预取的目录元数据。热安装再次清空安装状态和可变配置，保留该轮下载、解压和准备缓存；no-op 保留热安装后的完整状态。每个空 prefix 在计时前初始化，因此表中不包含一次性的目录初始化／权限设置。冷热安装都执行各自正常的安装工作；没有手工跳过链接、签名或 post-install。热安装不是两端 `reinstall` 命令的同义词。
- 目录刷新和元数据预取在安装计时外。gobrew 保留签名元数据验证；nanobrew 设置 `NANOBREW_DISABLE_UPSTREAM=1`，使用其原生 Homebrew HTTPS JSON 与 bottle SHA 路径，以便取得相同 bottle。此路径的元数据信任保证不同；本表不是默认上游注册表模式的全功能对照，也没有降低 gobrew 的签名保证来匹配它。
- gobrew 设置 `GOBREW_NO_AUTO_UPDATE=1`、`GOBREW_NO_INSTALL_CLEANUP=1`、`GOBREW_NO_MODIFY_PATH=1`；nanobrew 设置 `NANOBREW_NO_TELEMETRY=1` 和 `NB_BENCH=1`，后者在命令内记录阶段时间。这些设置与脚本一起保留，结果不包含 gobrew 自动更新或周期清理的触发成本。
- 计时后验证 hello 输出、jq 实际查询和 uv 版本命令；检查 gobrew ready 状态、每个已装 keg 的 Mach-O 签名与动态库来源。每个 keg 必须至少验证一个 Mach-O 可执行文件或库；文件遍历、磁盘统计、验证命令均不计入安装时间。gobrew no-op 还要求 Prepare、Apply 为零且没有新安装成功项。

## 磁盘统计的范围

以下取各例第五轮热安装后的逻辑字节数，同例五轮这些字段相同。它们不是 APFS 实际分配量，也不是全安装总占用：nanobrew 的数据库等文件位于 prefix 之外，两个工具的分区布局与目录覆盖范围也不同。

| 请求／工具 | 安装 keg | prefix | 下载缓存 | 准备缓存 | 独立解压缓存 | 元数据缓存 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| hello／gobrew | 189943 | 452087 | 54641 | 190078 | 0 | 59064385 |
| hello／nanobrew | 189905 | 189905 | 54641 | 189907 | 189905 | 744976 |
| jq／gobrew | 2768979 | 3031123 | 898280 | 2769366 | 0 | 59064385 |
| jq／nanobrew | 2768965 | 2768965 | 898280 | 2768969 | 2735325 | 753532 |

分区统计不跟随符号链接；“安装 keg”会解析 keg 根后统计，因此与 prefix 或 store 分区重叠，**不能把各列全部累加**。gobrew 的元数据是整份签名目录，nanobrew 是运行累积的 API 缓存，内容范围不同；重定位、签名和所有权记录也会改变安装后的字节数。APFS clonefile 的共享块不反映在逻辑大小中，不能据此宣称节省了多少物理空间。

## 复现

使用仓库的 [Nanobrew comparison 工作流](../.github/workflows/nanobrew.yml)，在一次性 GitHub hosted macOS 26 ARM64 runner 上执行。手动运行时，`preflight_only=true` 只做到首个等价候选；完整运行保留默认 `false`。不要把脚本的 runner 检查移除后在开发机试跑。

每次重测单独保留 JSON、命令输出、实际构建提交和二进制 SHA。后续结果替换本页主表时，此前完整证据仍保留为历史，不拼接不同提交或不同 runner 的最快样本。
