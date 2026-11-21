# gobrew v0.7 计划：清理遗留项，对齐 brew，优化性能与输出

## Context

v0.6.0 已发布。v0.6 计划留下三类遗留：未做项、延后功能、已知限制。用户要求三件事：

1. 判断每一项是必须做、不得做，还是可选。
2. 把性能、易用性、用户体验和细节做到最好。
3. 优先与 brew 对齐。brew 没有的 gobrew 自带功能（例如 `self`），必须先经用户批准才能加入。

本计划的依据：
- 三路只读分析，分别核对正确性、性能热点和输出层。
- 本机 `/opt/homebrew` 的上游 Ruby 源码。
- 用户的三个决定：
  - 补全：附带静态补全脚本，并修订 0002。
  - 持久化边界：先测量，再单独请用户决定。
  - 过时依赖方升级：本轮实现，与 brew 一致。

约束不变：
- CONSTRAINTS.md 和 docs/architecture.md。
- Plan/Prepare/Apply 契约、不可变的 prepared 产物、durable journal。
- 不得削弱签名、摘要、持久化和文件系统隔离。
- 测试只用临时 prefix。不加通用框架。
- 日常只跑本地 `make check`。不触发 Actions，不打 tag。

执行方式沿用 v0.6：
- 每个阶段单独提交。
- 修复类先写失败的测试，再改代码。
- 性能类先测量，再改，再复测。没有可证明的收益时，回退并记录原因。

## 分类结论

### 必须做

| 项 | 原因 | 证据 |
|---|---|---|
| `--prune=0` 和 `GOBREW_CLEANUP_MAX_AGE_DAYS=0` 不清理任何文件 | 与 brew 相反。brew 把 0 当作 `all` | `internal/cli/cleanup.go:110-114,155-160`、`internal/artifact/cache.go:21,42-44`；上游 `cmd/cleanup.rb:35-43` |
| 成功的操作因释放锁出错而返回非 0；缓存淘汰错误被静默丢弃 | 计划第 6 项只完成一部分 | `internal/artifact/manager.go:158-164`、`internal/engine/operation.go:116` |
| symlink 步骤的 `uninstall` 字段被接受，但从不读取 | 静默忽略。brew 对 Formula 也不执行 uninstall 阶段，homebrew-core 的 166 个使用步骤的 Formula 都没有用这个字段 | `internal/postinstall/steps.go:61,83` |
| 安装时，过时的已装依赖方自动升级 | 用户决定本轮做。与 brew 的 `Upgrade.dependants` 一致 | 上游 `upgrade.rb:259-420` |
| 标题类进度事件会被丢弃 | channel 只有 64 个缓冲，丢掉 `installed` 后，计时行一直显示 | `internal/model/model.go:336-341` |
| 首次安装先下载 catalog，再创建 prefix | 没有 TTY 时，先白下载，再失败 | `internal/cli/install.go:97-131` |
| 警告写到 stdout；JSON 模式丢失警告 | 输出流用错 | `internal/cli/cleanup.go:223,240`、`internal/cli/update.go:160`、`internal/cli/render.go:93-114` |
| `scripts/test-bootstrap.sh` 缺少 bash、fish、`ZDOTDIR`、`GOBREW_NO_MODIFY_PATH` 分支 | 这些分支会写用户的启动文件，现在没有测试 | `internal/shellconfig/shellconfig.go:131-142`、`uninstall.sh:87-94` |
| 冷安装 Prepare 慢于 nanobrew | 已知差距最大的一项 | 见阶段 3 |
| 在 `ff0738d` 上运行 `make check` | 这个依赖更新提交没有验证记录 | — |

### 核实后不改

- `info --analytics` 出错时退出 0。brew 在这里也用 `onoe`，不用 `ofail`（上游 `utils/analytics.rb:276`）。只补一个测试锁定这个行为。

### 不得做

| 项 | 原因 |
|---|---|
| 第三方 tap | tap 没有签名元数据，违反 CONSTRAINTS.md 的签名约束 |
| Cask、`services` | 与 Formula 是不同的领域，维护成本高。0001 只允许按具体需求再排期 |
| 漏洞查询 | brew 没有这个命令。按用户规则，必须先批准 |
| `info --HEAD`、`deps --os/--arch` | gobrew 不支持 HEAD；catalog 只按本机平台建立索引 |
| 跳过 OCI manifest、跳过第二次 SHA、去掉 `msync`、边下载边解压 | 会削弱摘要校验或持久化 |
| 跳过或缓存证书初始化脚本（约 6.5 秒） | 违反约束 |
| 引入 crapper、dryer、mutator | 本环境中没有这些工具。status.md 如实记录 |

### 可选（本轮不做，留在 status.md 的延后清单）

- `bundle dump/install` 的子集（清单导出与恢复）。brew 有这个命令。只能对齐它的子集。
- `log`。brew 有这个命令，gobrew 可以用 GitHub API 实现（`internal/catalog/history.go` 已有请求代码）。用户提出需求后再做。
- brew 的 `--json=v1/v2` 结构。只在有具体脚本需求时做。
- codesign 改为进程内签名。工作量大，风险高（必须保留 entitlements）。

### 必须先经用户批准（gobrew 自带，brew 没有）

本轮不加入任何新的 gobrew 专有命令、参数或环境变量。以下几项属于提示文案，不改变命令接口，所以纳入本轮：
- 等锁时的提示。
- 按下 Ctrl-C 时的即时回应。

新的环境变量都与 brew 同名，只把前缀换成 `GOBREW_`，例如 `GOBREW_NO_INSTALLED_DEPENDENTS_CHECK`。这与现有惯例一致，例如 `GOBREW_NO_INSTALL_CLEANUP`。

---

## 阶段 0：验证基线

- 在 `ff0738d` 上运行 `make check`，记录结果。

## 阶段 1：正确性修复

1. `--prune=0` 等于 `all`：在 `internal/cli/cleanup.go` 把 0 映射为“全部满足清理条件”。不得把它映射为 `cache.go` 里表示“关闭”的 0。`GOBREW_CLEANUP_MAX_AGE_DAYS=0` 同样处理。按 brew 的 `\A\d+\Z` 拒绝 `+5`。
2. `manager.go:158-164`：
   - 缓存淘汰错误改为警告事件。
   - 释放锁出错时，已成功的操作仍以成功退出，只输出警告。
3. symlink 的 `uninstall` 字段：从 `steps.go:83` 的白名单中删除，并从结构体中删除。解码时按 `ErrUnsupported` 拒绝。补一个测试。
4. `info --analytics`：补一个测试，锁定退出 0。
5. `scripts/test-bootstrap.sh`：
   - 补 bash 分支：已有 `.profile` 时，不得新建 `.bash_profile`。用 `bash -lc 'command -v gob'` 端到端检查。
   - 补 fish 分支：没有 fish 时，只断言 `conf.d/gobrew.fish` 的内容和删除结果。
   - 补 `ZDOTDIR` 和 `GOBREW_NO_MODIFY_PATH` 分支。
   - pwsh 的 shellenv 输出补断言（`shellconfig_test.go`）。

## 阶段 2：过时依赖方升级（与 brew 对齐）

目标行为与上游 `upgrade.rb` 的 `dependants` 和 `upgrade_dependents` 一致：
- 范围：`install`、`upgrade`、`reinstall` 实际安装或升级的 Formula。
- 找出它们已安装的运行时依赖方。只保留过时的依赖方。
- 排除 versioned 的 core Formula（`name@x`），与上游 `formulae_to_install` 的过滤一致。
- 被 pin 的依赖方不升级，输出 `Warning: Not upgrading N pinned dependent(s):` 和名单。
- 其余依赖方并入同一个计划，在同一个 writer 锁和 journal 中执行。
- 输出：
  - `==> Upgrading N dependents of upgraded formulae:`
  - `Disable this behaviour by setting `GOBREW_NO_INSTALLED_DEPENDENTS_CHECK=1`.`，遵守 `GOBREW_NO_ENV_HINTS`。
  - 每行一个 `name old -> new`。
- `--dry-run` 时用 `Would upgrade`。
- `GOBREW_NO_INSTALLED_DEPENDENTS_CHECK` 设置时，跳过检查，并输出上游的警告文案。
- 上游只对非 core Formula 做 broken linkage 检查。gobrew 只有 core，所以不做这一项。现有的 ABI 检查和“会破坏 pin 时拒绝”保持不变。

实现位置：
- `internal/resolver/resolver.go:215-260` 已有反向依赖扩展，但只针对兼容性变化。在同一个循环中增加“过时”条件，由 `Request` 的一个开关控制。不新增扩展机制。
- `internal/engine/plan.go:50` 已把全部已装记录交给 `LoadForPlan`，不需要多读 catalog。
- 计划结果需要标记哪些包是“依赖方升级”，供 CLI 输出标题。修改位置在 `model.Plan`。
- 环境变量在 `internal/cli` 读取，通过 `engine` 的请求字段传入。`help environment` 补上说明。
- 修订 `docs/decisions/0001`：这一项从“延后”改为“已实现”。

测试：
- 依赖方过时时被升级。
- 被 pin 时只输出警告。
- 开关设置时跳过。
- dry-run 输出正确。
- 已经是最新时不改变计划。

## 阶段 3：性能

### 3a. 先补测量能力（前置）

- `model.Event` 加时间戳。
- 新增事件：`metadata`（OCI 请求）、`sign`、`sync`、`prepared`。
- `-v` 和 `--display-times` 显示每个包各阶段的耗时：下载、解压、元数据、重定位、签名、同步、Apply。
- 刷盘次数：在 `internal/platform/sync.go` 加一个进程内计数器，只在 `--debug` 时输出。这一项代替需要 root 的 `fs_usage`。
- 新的基准场景：在临时 prefix 中预装约 100 个最小的 bottle，大约几百 MB，代替“下载数 GB”的场景。然后在默认开启清理的条件下测量 install。在 `scripts/benchmark` 中增加这个场景。

### 3b. 冷 Prepare（按预期收益排序）

1. **OCI 元数据预取，并从关键路径中移出**（估算：hello 减少 150–350ms）。
   - `prepare()` 开始时，用 goroutine 取 index 和 manifest。
   - 下载、解压、重定位、codesign、SyncTree 照常进行。
   - 只在写 manifest 之前（`manager.go:274`）等待结果。
   - 包内有 `INSTALL_RECEIPT.json` 时，丢弃预取结果。
   - 校验逻辑不变：manifest 按 digest 校验，layer 必须等于签名 catalog 中的 SHA（`receipt.go:111-118,188-195`）。
   - 等待网络时，不得占用准备槽。
2. **CDN 连接预热**：Prepare 开始时，并发向 `pkg-containers.githubusercontent.com` 建立 TLS 连接，复用 `http.DefaultTransport` 的连接池。这只是一项启发式优化：上游改变跳转主机时，只是没有收益，安全性不变。
3. **重定位**（`relocate.go`）：
   - 去掉两次空操作的 chmod（`:72-75`）。解压时文件已经以 `|0600` 创建。
   - 用两个锚点预扫描，跳过与替换无关的非 Mach-O 文件。
   - 同一个包内的文件并行处理。
   - 每项单独测量，用 python 或 node 这类大包。
4. **解压**：按目录缓存子 Root，代替每个条目一次 `MkdirAll`（`extract.go:96`）。gzip 外层加大缓冲区。不引入新依赖。
5. **Prepare 刷盘合并**：每个 bottle 的 6 次 F_FULLFSYNC 减为 4 次。manifest 并入同一轮 fsync，再做一次屏障。必须通过中断恢复测试。

### 3c. 安装后清理

- `internal/installer/cleanup.go:66-121`：先计算候选 keg。候选为空时，跳过对全部 keg 的文件扫描。保护规则不变。
- 用 3a 的 100 包场景测量前后对比。

### 3d. 持久化边界（只测量，不改）

- 用 3a 的计时和刷盘计数，测量状态整份重写和每个包约 9 次提交的开销。
- 收益明显时，单独写一份提案，请用户决定。本轮不改。

## 阶段 4：输出与体验（对齐 brew）

1. **进度事件不丢**：标题类事件（`extract`、`postinstall`、`installed`、`step`、新增的阶段事件）改为阻塞发送，或单独使用一个 channel。只有下载进度可以丢。
2. **补上静默阶段的反馈**：relocate、签名、链接显示阶段名，TTY 上显示计时（`progress.go:105`、`installer/postinstall.go:118`）。
3. **进度条**：
   - 显示 bottle 的序号和总数，例如 `[2/5]`。
   - 显示下载速度。
   - 处理 SIGWINCH，窗口变化时重读宽度。
   - 开始时就计入全部下载总量，百分比不得往回跳。
4. **首次安装顺序**：先创建 prefix（包括 sudo 说明），再自动更新。没有 TTY 时提前报错（`install.go:97,128`）。
5. **摘要与 brew 一致**：
   - `🍺  /opt/gobrew/Cellar/jq/1.8.2: 19 files, 1.1MB`。`GOBREW_NO_EMOJI` 可以关闭。
   - upgrade 输出 `==> Upgrading N outdated packages:` 和 `a 1.0 -> 1.1`。
   - uninstall 输出 `Uninstalling <keg>... (N files, SIZE)`。
   - 自动移除依赖的标题与 autoremove 统一。
   - `already installed` 的文案与 brew 逐字核对。
   - 每一条在实现前，必须先在上游源码中核对原文。
6. **错误文案**：
   - 首字母统一大写。
   - `ErrNeedsRepair` 去掉内部前缀，改为给出下一条命令，例如 `gobrew postinstall jq`。
   - 删除 `validation.go:111` 叠加的第二条提示。
   - 网络错误只显示主机名，并提示检查网络。
   - 重试行加 `Warning:`，不显示完整 URL。
   - 内部术语换成用户能懂的说法：`Using prepared`、`dependency closure`、`minimum-version verification`。
7. **输出流**：清理失败用 `a.warn` 写 stderr。JSON 模式把警告和自动更新失败写进 envelope。`--quiet` 抑制警告。
8. **锁等待与 Ctrl-C**：
   - 等锁超过约 1 秒时，在 stderr 提示正在等待另一个 gobrew 进程。
   - 裸的 `ErrBusy` 补上路径和下一步建议（`store.go:186-194,248`、`manager.go:93`）。
   - 按下 Ctrl-C 时，立即输出一行，说明正在等待安全点。
9. **ANSI 残渣**：`cleanText` 整段删除 CSI 序列（`render.go:175`），并修正 `progress_test.go:25` 的预期。
10. **帮助文本**：
    - 给 `unpin`、`unlink`、`autoremove`、`postinstall`、`self *` 补 Example。
    - 删除被 `helptext.go:65-68` 覆盖的死代码（`install.go:56-60`、`search.go:86`）。
    - `help environment` 补上 `COLUMNS` 和新的环境变量。
    - 统一 `update` 和 `self update` 的“已是最新”文案。
11. **颜色开关**：认 `HOMEBREW_NO_EMOJI`；认 `HOMEBREW_COLOR` 的等价变量 `GOBREW_COLOR`，用于强制开启颜色。

## 阶段 5：Shell 补全（用户已批准，修订 0002）

- 修订 `docs/decisions/0002`：静态补全脚本随发布包附带，不算新增命令。brew 也在 `completions/` 目录中附带补全脚本。
- 构建时用 cobra 生成 zsh、bash、fish 三个脚本，放进发布压缩包。不得恢复 `completion` 命令。
- `install.sh` 和 `self install` 把脚本写到用户目录；`uninstall.sh` 和 `self uninstall` 删除它们。
- zsh 的 shellenv 加上 `fpath` 中的 `$prefix/share/zsh/site-functions`（`shellconfig.go:66-80`）。必须先核对上游 `brew shellenv` 的写法。这样，用 gobrew 安装的软件自带的 zsh 补全也能生效。
- `test-bootstrap.sh` 补测补全脚本的安装和删除。

## 阶段 6：文档与收尾

- 更新 `docs/status.md`：
  - 本轮新的分类。
  - 延后清单。
  - 与 brew 的差异变化。
  - 性能数据。
- 写 `docs/releases/v0.7.0.md` 草稿。
- 把每组性能数据写入 `docs/performance.md` 和 `docs/benchmarks/`。新数据另存，不覆盖旧基线。
- `tasks/plan.md` 和 `tasks/todo.md` 换成本轮内容。

## 关键文件

- 正确性：`internal/cli/cleanup.go`、`internal/artifact/manager.go`、`internal/postinstall/steps.go`、`scripts/test-bootstrap.sh`
- 依赖方升级：`internal/resolver/resolver.go`、`internal/engine/plan.go`、`internal/model/model.go`、`internal/cli/install.go`、`internal/cli/render.go`
- 性能：`internal/artifact/{manager,receipt,download,extract,relocate}.go`、`internal/platform/sync.go`、`internal/installer/cleanup.go`、`scripts/benchmark/main.go`
- 输出：`internal/cli/{progress,render,install,update,app,helptext,path}.go`、`internal/state/store.go`、`cmd/gobrew/main.go`
- 补全：`internal/shellconfig/shellconfig.go`、`install.sh`、`uninstall.sh`、`internal/selfupdate`、`.github/workflows/release.yml`（只改文件，不触发）

## 验证

- 每个阶段：运行 `make check`（gofmt、go vet、`go test -race`、教程示例、govulncheck、CGO=0 构建、bootstrap）。
- 安装行为变更（阶段 1–3、阶段 5）：在隔离的 prefix、HOME 和缓存下，运行 `scripts/smoke.sh` 和 `scripts/smoke-version.sh`。
- 依赖方升级：在临时 prefix 中先用 `version-install` 装旧版依赖方，再安装或升级它的依赖，检查升级结果、pin 警告和开关行为。
- 性能：
  - `scripts/benchmark` 跑五轮冷、热、no-op，新旧二进制交替。
  - 再加 100 包场景。
  - 每项优化单独对比。没有收益时，回退并记录。
- 输出：在 TTY、管道、`NO_COLOR`、`--json`、`--quiet` 下手工运行 install、upgrade、uninstall、doctor 和错误场景。检查：
  - stdout 只有一个 JSON 文档。
  - stderr 没有 ANSI 残渣。
  - 文案与上游源码一致。
- 交付：本地 release 构建的路径、源码提交和 SHA-256。
- 未验证项：与 nanobrew 的重新对比需要 Actions。必须先由用户确认发版，才能运行。
