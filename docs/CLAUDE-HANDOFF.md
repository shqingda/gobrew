# gobrew 项目交接文档

更新日期：2026-10-27（Asia/Taipei）  
接手工具：Claude Code  
工作目录：`/Users/shqingda/Projects/gobrew`

## 0. 2026-10-27 进展更新（Claude Code）

本节记录交接后的进展，优先于下文中标为“尚未实现”的旧描述：

- **第 3 节首次使用必须手动 init：已解决。** curl 安装器和第一次 `install`/`migrate` 自动初始化；默认 `/opt/gobrew` 需要权限时，CLI 通过 `/usr/bin/sudo` 运行自身 `init` 一次，其余步骤以普通用户运行。用户已在本机实测 `install uv`（创建 `/opt/gobrew`、安装 uv、PATH 生效）。
- **第 4 节 Homebrew 共存：已实现。** 新增 `internal/shellconfig`：在 shell 启动文件末尾写入带标记的 PATH 配置段，gobrew 优先（用户选择）；`install` 提示与 Homebrew 同名的命令，`doctor` 以 warning 报告同名命令与 PATH 顺序。
- **新增：** Homebrew 风格安装输出与下载进度条；`update` 与 `brew update` 对齐（自身更新 + 目录刷新 + 过期列表）。
- 相关提交：`2ff1761`、`60f5ffd`，发布说明 `docs/releases/v0.3.0.md`，目标版本 v0.3.0。
- **仍未开始：第 5 节 nanobrew 同机对照与基于测量的性能优化。** 下一位接手者从这里继续。
- 已知后续项：`install`/`upgrade` 不会像 Homebrew 那样在目录过旧时自动刷新；Cask 仍未支持。

## 0.1 Homebrew 对齐进度（P0，未发版）

计划全文：`~/.claude/plans/p0-homebrew-glimmering-hartmanis.md`。环境变量统一用 `GOBREW_` 加 Homebrew 同名后缀。

已完成：删除 `install x@ver` 简写（只留 `version-install`）；install 升级过时包、pin 时报错（照 Homebrew install/check.rb）；自动更新（install/upgrade/outdated，24h，用 catalog `CURRENT` mtime）；安装后清理与 30 天全量清理、`cleanup [formula] --prune -n -s`；命令别名（ls/rm/remove/ln/dr/abv/up/instal/uninstal/post_install、`-S`）；search 名字/`--desc`/正则与分列输出；`list --installed-on-request`；删除 `version` 子命令（用 `--version`）；`migrate` 改名 `import-homebrew`；PATH 遮蔽检查进 Caveats；错误前缀改为 `Error:`。

未完成：P0 第 8 项“安装后升级过时依赖方”（`GOBREW_NO_INSTALLED_DEPENDENTS_CHECK`）；no-op 基准重测（快路径现会读本地目录索引）；README 未全面补写新环境变量；未做真实端到端验证（version-install 旧版 → install 升级 → 自动清理）。P1/P2 见计划文件。

## 1. 先看这里（交接时的原始状态）

项目已经实现并发布到 **v0.2.0**，不是空项目。当前用户要求暂停开发、交接，原因是额度不足。

**接下来最优先修复首次使用必须手动 init 的问题，然后进行与 nanobrew 的公平实测和性能优化，并完善 Homebrew 共存体验。上述三项尚未实现。** 不要把先前讨论的方案误认为已经落地。（init 与共存已在第 0 节所述提交中完成。）

交接前已核对：

- 当前分支：`codex/gobrew-core`。
- 当前 HEAD：`317cd85d138f0c3527ac8cfb8d9112c697aa5ef6`。
- `main`、开发分支、`v0.2.0` 已推送到 GitHub，发布完成。
- 写本文档之前工作区干净，没有初始化修复或 nanobrew 优化的未提交代码。
- 本文档是本次交接新增文件，尚未提交或推送。
- 临时启动过 nanobrew 调研子任务，但已停止；没有收到可依赖的调研结果，也没有已完成的对照测试。

仓库：<https://github.com/shqingda/gobrew>  
v0.2.0：<https://github.com/shqingda/gobrew/releases/tag/v0.2.0>  
v0.2.0 发布流水线：<https://github.com/shqingda/gobrew/actions/runs/40437440286>  
对应 main CI：<https://github.com/shqingda/gobrew/actions/runs/40437437170>

## 接手后的必读清单与阅读顺序

以下路径均相对于仓库根目录。**先读文档再改代码；本交接文档不替代仓库里的约束和架构。**

### 第一轮：开始工作前必须阅读

按以下顺序阅读：

1. `AGENTS.md`：仓库操作规则，尤其是测试只能使用临时 prefix、不得修改开发者的 Homebrew。
2. `CONSTRAINTS.md`：不可降低的正确性、安全、性能测量和验证标准。
3. `docs/CLAUDE-HANDOFF.md`：本次交接的完整上下文、用户最新要求、已完成和未完成的边界。
4. `docs/architecture.md`：模块依赖方向、Plan/Prepare/Apply、状态所有权、锁顺序、持久化恢复和 CLI 自身生命周期。任何安装行为改动前必须读完。
5. `README.md`：当前公开给用户的安装、初始化、命令、恢复和支持范围说明。注意其中手动初始化步骤是当前待修复体验，不是用户要求保留的设计。
6. `docs/releases/v0.2.0.md`：当前已发布版本的能力与升级方式，避免把新需求误当作已有功能。

### 第二轮：按正在处理的任务阅读

| 任务 | 必须阅读的文档 | 随后定位的代码与测试 |
| --- | --- | --- |
| 自动初始化、一次性 sudo 授权 | `docs/architecture.md` 的 Ownership and state、Validation and distribution；`README.md` 的初始化软件目录 | `internal/engine/engine.go`、`internal/cli/cli.go`、`internal/cli/self.go`、`internal/state/store.go`、`install.sh`；对应 engine/cli/state 测试及 `scripts/test-bootstrap.sh` |
| 与 Homebrew 共存、同名命令与 PATH | `README.md` 的初始化、日常操作、迁移、支持边界；`docs/compatibility.md`；架构的 Ownership and state | `shellenvCommand()`、doctor 查询逻辑、`internal/installer` 的链接归属逻辑；`internal/cli/cli_test.go`、`internal/installer/installer_test.go` |
| nanobrew 公平比较、性能优化 | **完整阅读 `docs/performance.md`**，再读 `docs/compatibility.md` 和架构的 Performance and cache identity、Dependency compatibility、Trust boundary | `scripts/benchmark.sh`、`scripts/benchmark/main.go`、`gate.go`、`telemetry.go` 及对应测试；按剖析结果定位实际热点 |
| 修改缓存、重定位或安装提交 | `CONSTRAINTS.md`、完整架构、`docs/compatibility.md`；涉及性能时加读 `docs/performance.md` | `internal/artifact`、`internal/installer`、`internal/postinstall`、`internal/platform`，重点读现有安全与中断恢复测试，而非仅看正常路径 |
| 发布新版本 | `README.md` 的验证与发布；`docs/releases/v0.2.0.md`；架构的 Validation and distribution | `.github/workflows/ci.yml`、`smoke.yml`、`benchmark.yml`、`release.yml`，以及 `scripts/smoke.sh`、`scripts/test-bootstrap.sh` |

### 第三轮：证据与历史参考，按需查阅

- `docs/benchmarks/2026-10-27-m3-pro.json`：本地五轮正式基准原始数据，判断优化前基线与测量口径时阅读。
- `docs/benchmarks/2026-10-27-m3-pro-140eb76.json`：此前有效对照，保留用于追溯，不与其他报告混合挑选样本。
- v0.2.0 Release 附件 `benchmark.json`：当前发布二进制的 CI 实测证据；链接见本文第 6 节。
- `docs/releases/v0.1.0.md`：首版发布历史，仅在核对兼容或迁移差异时阅读，不要改写旧版说明。
- `tasks/plan.md`、`tasks/todo.md`：首轮实现记录。其完成状态**不代表本次自动初始化、共存和 nanobrew 优化已经完成**；新任务需要另行记录。
- `docs/go-tutorial.md`：给用户的独立 Go 教程，当前已完成。除非改动涉及教程内容或 Go 语言说明，否则无需为此次修复通读或改写。
- `THIRD_PARTY_NOTICES.md`、`LICENSE`、`internal/catalog/HOMEBREW-LICENSE.txt`：涉及引入、复制、分发第三方代码或修改发布包时阅读。

nanobrew 的外部资料也应按顺序检查：**固定版本的 README → BENCHMARKS.md → 对应基准 workflow/脚本 → prefix、缓存、fallback、初始化的源码实现**。仓库首页或最新 README 只能用于发现资料，最终报告必须绑定实际测试的 commit。尚未确认安全隔离前，不运行其默认安装器。

## 2. 用户明确要求与工作方式

- 使用 Go，当前工具链 **Go 1.27.3**；Apple Silicon、macOS 15+。
- 主命令保留 `gobrew`，`gob` 是指向它的符号链接，不能重命名主程序。
- 官方 Homebrew core 元数据与 bottles；尽可能兼容 Homebrew Formula 使用习惯。**Cask 后续再做。**
- 开箱即用：不应让用户手工发现并补执行 `init`。首次系统授权可以由产品流程直接引导。
- 尽可能高性能，但绝不通过省略依赖、校验、重定位、初始化、恢复或持久化来制造提速。
- 少依赖、现代而流行的库，优先标准库；避免通用框架和过度设计。
- 必要测试即可，不追求机械覆盖率，不堆 DTO/简单转发测试。
- 未来 MyGo GUI 直接使用 Go 核心；当前不引入 GUI、daemon、HTTP 服务或 IPC。
- 用户是前端/全栈工程师，教程帮助理解 Go，但项目代码标准不能因此降低。
- 正式中文 Markdown 教程，必要代码注释、无 emoji、无学习计划。这部分已完成。
- GitHub Actions 发版。用户此前明确授权过推送、发版，v0.1.0/v0.2.0 均已执行。
- 用户不喜欢反复请求确认。已明确的任务直接做；报告真实结果，不把未完成工作包装成已完成。

开始改安装行为前必须读取：`AGENTS.md`、`CONSTRAINTS.md`、`docs/architecture.md`。

## 3. 当前用户遇到的实际问题

用户执行：

```text
❯ gob install uv
gobrew: not found: run gobrew init first

❯ gobrew init
gobrew: create prefix: mkdir /opt/gobrew: permission denied; for the default location run sudo gobrew init, then gobrew init
```

用户追问：为什么还要 init，应该像 Homebrew 一样开箱即用，并要求“无门槛兼容 homebrew（cask 回来再做）”。

### 已定位的原因

1. `install.sh` 仅下载并校验 CLI，再执行 `self install`，没有初始化 Formula prefix。
2. `internal/engine/engine.go` 的 `checkMutation()` 检查 `<prefix>/.gobrew/state.db`，不存在就拒绝安装。
3. `Engine.Init()` 的普通用户分支直接 `os.Mkdir("/opt/gobrew")`，普通用户一般无权写 `/opt`。
4. 已有 root 分支只允许创建**缺失的** `/opt/gobrew` 并按 `SUDO_UID`/`SUDO_GID` 移交所有权；它不建 DB，不联网，不安装软件。随后还需要普通用户调用 `Init()`。
5. 错误提示中的 `sudo gobrew init` 还可能因为 sudo 的 PATH 不包含 `~/.local/bin` 而失败；应使用正确引用的绝对程序路径。

相关位置：

- `internal/engine/engine.go`：`Init()`、`checkMutation()`、安装执行入口。
- `internal/cli/cli.go`：`application.init()`、`installCommand()`、`shellenvCommand()`。
- `internal/state/store.go`：`Initialize()`、状态读取和目录安全检查。
- `install.sh`、`internal/cli/self.go`：当前 CLI 安装流程与提示。
- `scripts/test-bootstrap.sh`：现有隔离引导安装测试。

### 已向用户承诺、用户回答“好”的改进方向

- curl 安装过程中完成必要初始化；已有版本第一次 `install` 也能自动补齐。
- `/opt/gobrew` 首次创建需要权限时，程序直接发起一次 sudo 密码提示，之后恢复普通用户完成 DB 初始化和实际安装。
- 后续安装/升级/卸载不使用 root；不让用户手工补 `init`。
- 保留 `init` 作为高级/修复入口可以，但正常路径不要求用户知道它。
- 常用 Formula 命令保持 Homebrew 的使用习惯，不能用跳过必要步骤来宣称兼容。

### 实施注意

这是方向而非已经写好的技术方案。推荐将交互提权编排放在 CLI/引导安装边界，不让 engine 调终端、sudo 或 os.Exit。

必须考虑：

- curl 管道的 stdin 不是终端，但用户可能有控制终端 `/dev/tty`；不要错误判定不能授权，也不要通过 stdin 读取/记录密码。
- 非交互、JSON、CI、取消、拒绝授权场景不能挂死；给出准确错误和可执行的绝对路径指引。
- 不递归 chown 既有目录，不接管别人目录，不触碰 `/opt/homebrew`。
- `install --dry-run`、只读命令、已安装 no-op 不应意外触发提权或初始化副作用。
- 自定义可写临时 prefix 不需要 sudo；root helper 仍只处理固定的默认缺失目录。
- 并发初始化及“目录创建成功但 DB 尚未创建”的恢复要可处理。
- 测试使用临时目录/可替换进程边界，**不要在开发机真实执行 sudo 或创建 `/opt/gobrew` 来跑测试**。
- 用户本机的 `/opt/gobrew` 当前状态尚未重新检查，不能假设已修好或已安装 uv。

旧版临时绕过方法只是 `sudo "$HOME/.local/bin/gobrew" init` 再普通用户 `gobrew init`，但用户已经明确表示不接受把这当最终产品体验。

## 4. Homebrew 共存：需要解释并产品化

用户不清楚两边安装同一个软件会发生什么。

已向用户说明：

- Homebrew 默认 `/opt/homebrew`，gobrew 默认 `/opt/gobrew`，各自管理自己的 keg、依赖和链接，不互相覆盖。
- 终端调用哪个同名命令取决于 PATH 顺序。
- 会完善安装提示、诊断和明确的切换方法；这部分尚未实现。

接手时建议先检查当前 `shellenv` 和 `doctor`，再选择最小实现。当前 `gobrew shellenv` 会把自身 `bin`、`sbin` 放到 PATH 前面，因此会改变同名命令的优先级。

可用于文档/诊断的基本操作：

```sh
type -a uv
command -v uv
/opt/gobrew/bin/uv --version
/opt/homebrew/bin/uv --version
```

不必为了共存自动卸载 Homebrew 版本；也不能静默采用 Homebrew 的依赖来破坏独立安装语义。两个版本的 CLI 仍可能共用用户目录中的配置/缓存，这是应用自己的行为，不能承诺完全隔离。动态库和构建工具还受显式路径、pkg-config 等配置影响，不只取决于 PATH。

现有 `migrate --from-homebrew ...` 只读 Homebrew 安装清单，通过正常 gobrew 管线重新安装，不接管原目录。

## 5. nanobrew 对照与优化任务

用户明确要求：**同机、同软件包、同安装流程** 对比 nanobrew，继续优化，尽量达到或超过它，但不得牺牲安装正确性。

参考：<https://github.com/justrach/nanobrew>

尚无本项目与 nanobrew 的直接实测，不能声称同样快、已超过或慢多少。

最近读取的 upstream README 提到：

- 一些毫秒级 warm 结果实际是已经安装的 no-op。
- 有原生支持集合及 Homebrew fallback。
- 不执行 Ruby post_install hooks，某些安装可能不完整。
- 部分性能比较使用 `--skip-post-install`。

这些内容可能变化，**应固定实际比较的 release/commit 并复核源码**，不能只看宣传表格。先前网页读取没有固定一个可执行对照版本。

### 下一步建议

1. 固定 nanobrew 版本、二进制 SHA、源码 commit、构建方式和测试机信息。
2. 查明其 prefix/cache/home 配置、是否自动调用 brew，确保可在独立短路径安全测试。必要时用 macOS sandbox 限制访问/写入真实 Homebrew，不能为了基准去安装它的默认全局环境。
3. 优先 `uv`（用户当前任务）、纯 CLI 小包及带动态库包。选择两边都能完整正确安装的集合；需要初始化但某方不支持的场景，应记录“不具备同等完成条件”，不伪造比较倍数。
4. 两边安装完全相同版本/修订/依赖集合；确认无隐式依赖用户 Homebrew 的行为。
5. 区分冷安装、保留下载/准备缓存但清空安装状态的热安装、已安装 no-op；目录刷新单独计时。
6. 至少五轮，交替执行顺序更好；记录原始样本、中位数、阶段时间、磁盘占用和运行结果。不要把 no-op 宣称为重装。
7. 实际运行软件、动态依赖/签名/初始化结果验证通过，才能接受性能样本。
8. 建立当前 gobrew baseline，再一次只改一个测得的瓶颈；优化后重跑同样方法，只有超出噪声且正确性不退步才保留。
9. 原始数据、失败样本及限制落入 `docs/benchmarks/`、`docs/performance.md`，提供可复现脚本。

现有基准入口：`scripts/benchmark.sh`、`scripts/benchmark/{main,gate,telemetry}.go`。目前是 gobrew/Homebrew 双方，尚未支持 nanobrew，不应硬塞不同语义让现有门槛失真。

## 6. 已有性能证据

### 本地 M3 Pro 对 Homebrew（正式记录已在仓库）

机器：Apple M3 Pro、18 GiB、macOS 27.0.1、Go 1.27.3。
集合：hello、jq、oniguruma、ca-certificates，五轮、完整证书初始化。

| 场景 | gobrew | Homebrew | gobrew 用时减少 |
| --- | ---: | ---: | ---: |
| 冷安装 | 9.580 s | 12.814 s | 25.2% |
| 热缓存重新安装 | 6.994 s | 10.863 s | 35.6% |
| no-op | 14.586 ms | 359.059 ms | 95.9% |

文件：`docs/performance.md`、`docs/benchmarks/2026-10-27-m3-pro.json`。之前一次有效样本也保留，未挑选混合最快结果。

已定位该集合的热安装主要时间是上游证书初始化：一次实测约 6.593 s，串行启动很多 security/openssl 进程。内部目录选择读取和链接批量持久化已优化过。不要跳过证书初始化来追求纳秒数字。

### v0.2.0 GitHub Actions 实测（不同机器，不能混为本地数值）

| 场景 | gobrew | Homebrew |
| --- | ---: | ---: |
| 冷安装 | 11.972 s | 18.494 s |
| 热缓存重新安装 | 11.037 s | 18.401 s |
| no-op | 8.653 ms | 481.429 ms |

原始发布数据：<https://github.com/shqingda/gobrew/releases/download/v0.2.0/benchmark.json>

现有 Homebrew 基线固定 commit `c6069d2a943b71b0a90d91fc1ac93cb9c1634dcc`（7.0.11），使用独立 prefix/cache/temp、正式 --force-bottle，没有修改其代码。更长的独立 prefix、网络及页缓存都会影响结果；已在文档披露限制。

## 7. 当前架构与实现重点

不是要重写。保持 `Plan → Prepare → Apply`、不可变准备材料、明确归属及持久化恢复。

| 模块 | 内容 |
| --- | --- |
| `internal/cli` | Cobra、JSON、退出码、信号上层适配、shellenv/completion/self |
| `internal/engine` | 类型化用例、固定计划、准备租约、提交编排、阶段计时 |
| `internal/resolver` | 平台/版本/依赖/反向依赖/pin；纯规则 |
| `internal/catalog` | Homebrew PS512 JWS 校验、不可变 bbolt 目录、选择性闭包读取 |
| `internal/artifact` | SHA 下载、安全解压、OCI runtime facts、文本/Mach-O 重定位、签名、缓存 |
| `internal/installer` | keg、链接、配置合并、持久化操作日志及恢复 |
| `internal/postinstall` | 有明确支持集的声明式安装步骤；未知/Ruby hook 拒绝 |
| `internal/state` | 短期开关 bbolt、安装状态、日志与锁 |
| `internal/platform` | APFS clone、文件复制、flock、fsync/F_FULLFSYNC 等 |
| `internal/selfupdate` | CLI 自身安装/更新/卸载，独立于 Formula DB 和 prefix |

直接依赖：Cobra 1.10.2、bbolt 1.5.0、x/sys 0.49.0、x/sync 0.20.0。无新增 GUI/发布框架依赖。

重要现状：

- 已安装 install 是零网络本地 no-op；显式 upgrade 才更新。
- 准备阶段下载/解压各自限流，跨进程缓存租约，缓存原始 bottle 和初始化前文件树；克隆失败普通复制，禁止硬链接污染缓存。
- 完整依赖准备后提交；计划过期必须报错，不静默换计划。
- 后初始化失败保留 `needs-postinstall`；不自动重放结果未知的非幂等命令。
- 未知安装语义、无 bottle 等明确拒绝。用户现在要求更低门槛兼容，应逐项补足，不是删除这些安全拒绝。
- prepared cache schema 当前 8；改变准备/重定位语义要评估升级，防止复用旧材料。
- Mach-O 导入、现代 OCI runtime metadata、Java CAFEBABE 识别、相对链接、证书/Node/Python 初始化等均已有专项处理，避免无意回退。

## 8. v0.2.0 自身生命周期已完成

```sh
curl -fsSL https://github.com/shqingda/gobrew/releases/latest/download/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
gob --help
gob self update
gob self uninstall
```

当前这些命令只管理 CLI，**不解决第 3 节的 Formula 初始化问题**。

- 默认命令目录 `~/.local/bin`，可用 `GOBREW_BIN_DIR` 指定；`GOBREW_VERSION` 可固定稳定版本。
- `gobrew → .gobrew/current`，`gob → gobrew`；真正二进制在 `.gobrew/versions/<SHA256>`。
- receipt 先登记归属再发布文件，Active 在文件同步后更新；current 原子切换。
- 自更新绑定 GitHub 最新稳定 tag，校验 tar SHA，只读取指定普通文件成员，拒绝自动降级。
- 未托管的手工二进制需先 `self install`；不会接管同名外部文件。
- 自身卸载保留 Formula、配置、数据、缓存及少量自身归属/锁记录，供安全重试。
- Bash/Zsh/Fish/PowerShell 均注册两个命令的共享补全；Bash/Zsh 已实际加载验证。
- 压缩包和校验清单同属 GitHub 信任域，不是独立签名/公证方案。

## 9. 已完成验证与发布流程

v0.2.0 全部通过：

- `go vet ./...`、`go test -race ./...`、gofmt、go mod verify。
- `govulncheck@v1.9.0` 无已知漏洞。
- Go 1.27.3 教程 16 个完整示例的校验（此前已完成；发布 CI 继续执行）。
- CGO_ENABLED=0 构建。
- actionlint 1.7.12，shell 语法检查，隔离的 `scripts/test-bootstrap.sh`。
- macOS 15/26 ARM64 真 bottle 烟测；五轮 Homebrew 比较门槛。
- 真实公开 curl URL 安装到临时目录，两个命令可用。
- 使用带旧版本号的测试构建实际下载/切换正式 v0.2.0，再次更新 changed=false；这是测试构建，不要称为原版 v0.1.0 原生支持 self update。
- 两个临时托管安装的自身卸载，确认自有二进制/链接移除而 Formula 配置保留。
- 正式发布二进制 SHA 与 benchmark.json 一致。

此前还验证了 38 个真实 Formula，包括 Node、npm、Python/venv/pip/SSL、Git、Go、rg、sqlite、OpenSSL；详见 `docs/compatibility.md`。这些不能替代新改动后的回归测试。

工作流：

- `.github/workflows/ci.yml`：main/PR/reusable 检查。
- `smoke.yml`：macOS 15/26 真瓶。
- `benchmark.yml`：五轮性能与被测二进制上传。
- `release.yml`：v* tag 触发，等所有门槛通过后发布**同一个被测二进制**，不另行重建。
- 发布资源：tar.gz、install.sh、checksums.txt、benchmark.json；发布说明读取 `docs/releases/<tag>.md`。

旧标签和 Release 不要重写。新修复完成后按变更范围选新版本，并重新通过发布门槛。

## 10. 给接手者的执行顺序

1. 阅读本文件、约束和架构，核对 git 状态；不要重做已经完成的项目搭建。
2. 先实现自动初始化与一次性授权流程，补必要隔离测试，实测普通用户首次 `install uv` 的流程；不修改真实 Homebrew。
3. 完善共存说明/提示/诊断与 PATH 切换，明确同名软件选择。
4. 固定 nanobrew 实验对象，建立安全同机对照，保存原始数据。
5. 根据测量优化可证明的瓶颈；保留正确性，不承诺尚未测出的速度。
6. 更新文档、验证、提交、推送/发版，并报告真实完成与剩余限制。

当前不要沿用旧临时目录假定它们存在。之前的主要基准/安装临时目录都已清理；`/tmp` 中可能留有日志，仓库和 Release 中的证据才是正式来源。
