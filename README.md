# gobrew

使用 Go 实现的 macOS 包管理器，读取 Homebrew 官方 core 元数据与预编译 bottles。CLI 和未来 macOS 客户端共享同一安装核心。

当前支持 Apple Silicon、macOS 15+，构建使用 Go 1.27.3。Formula 安装在独立 prefix，默认 `/opt/gobrew`。不调用 Homebrew 或 Ruby 作为安装兜底。

## 安装命令行

从 v0.2.0 起，可以通过 curl 安装最新稳定版，无需预先安装 Go：

```sh
curl -fsSL https://github.com/shqingda/gobrew/releases/latest/download/install.sh | sh
```

安装器以普通用户运行，将命令放在 `~/.local/bin`，创建 `gob → gobrew` 符号链接，准备 Formula 目录，并把 gobrew 加入新终端的 `PATH`。`gobrew` 仍是主命令，两者使用相同的参数和数据。装完后新开一个终端（或执行 `exec zsh`）即可直接使用 `gob` 和之后安装的命令。也可以先下载并查看 `install.sh`，再执行。

指定版本或命令目录时，将环境变量传给执行脚本的 shell：

```sh
curl -fsSL https://github.com/shqingda/gobrew/releases/latest/download/install.sh \
  | GOBREW_VERSION=v0.2.0 GOBREW_BIN_DIR="$HOME/bin" sh
```

自定义命令目录须为当前用户拥有的绝对路径，同样会自动加入 `PATH`。安装器遇到已有的外部 `gobrew` 或 `gob` 文件时拒绝覆盖；升级已有托管安装可直接再次运行安装器。

脚本先固定一个发布版本，再下载对应压缩包并核对 SHA-256。压缩包和校验文件来自同一个官方 GitHub Release；校验和用于检测文件损坏，不是独立发布签名。

## 初始化软件目录

不需要单独执行 `init`。curl 安装器在安装命令后会立即准备 Formula 目录；如果当时没有完成，第一次 `gob install` 会自动补齐：

```sh
gob install uv
```

默认目录 `/opt/gobrew` 位于 root 拥有的 `/opt` 下，首次创建时程序会说明原因，并通过 `sudo` 在终端里请求一次管理员密码。提权进程只创建 `/opt`（若不存在）和这个新目录，并把新目录交给当前用户；它不联网、不运行软件包代码、不修改已有目录的所有权，也不会触碰 `/opt/homebrew`。随后的数据库初始化、下载和安装都以普通用户身份进行，之后的安装、升级、卸载不再需要 `sudo`。

没有可交互终端（CI、`--json`、无控制终端的脚本）时不会请求密码，而是失败并打印一条使用绝对路径的命令，例如 `sudo '/Users/you/.local/bin/gobrew' init`；在终端执行一次后重试原命令即可。拒绝授权或按 Ctrl-C 取消时不会留下半成品目录。`install --dry-run`、`list`、`doctor` 等只读命令不会创建目录或请求权限。

`gobrew init` 仍然保留，用于预先初始化或修复：它是幂等的，可补全中断留下的空目录结构，但拒绝接管含有其他内容或属于其他用户的目录。

## PATH 自动配置

安装器和 `gob install` 会让新终端直接找到 gobrew 的命令，不需要手工编辑配置。它们在 shell 启动文件末尾写入一段带标记的配置，把命令目录（默认 `~/.local/bin`）和 `/opt/gobrew/bin`、`/opt/gobrew/sbin` 放到 `PATH` 最前面：

| shell | 写入位置 |
| --- | --- |
| zsh | `${ZDOTDIR:-$HOME}/.zshrc` |
| bash | 已存在的 `~/.bash_profile`、`~/.bash_login` 或 `~/.profile`，都没有时新建 `~/.bash_profile` |
| fish | `~/.config/fish/conf.d/gobrew.fish` |

这段配置以 `# >>> gobrew >>>` 开始、`# <<< gobrew <<<` 结束；gobrew 只修改这两行之间的内容，重复执行不会重复添加，`self uninstall` 时移除。写在文件末尾是为了即使前面有 `export PATH=...` 这类重写 PATH 的语句也能生效。软链接的配置文件会更新其指向的真实文件；标记损坏、不归当前用户所有或有多个硬链接的文件不会被改动，而是打印手工配置方法。

已经打开的终端无法被其他程序修改环境变量，因此第一次安装后需要新开终端或执行 `exec zsh`，Homebrew 也是如此。当前终端已包含这些目录时，`install` 不会读写任何配置文件。只有默认目录 `/opt/gobrew` 会写入启动文件；`--prefix` 指定的开发或测试目录不会，可用 `gobrew --prefix DIR shellenv` 打印配置。设置 `GOBREW_NO_MODIFY_PATH=1` 可完全关闭自动配置。shellenv 导出的 `GOBREW_PREFIX` 也会被 gobrew 读取，作为 `--prefix` 的默认值。

## 与 Homebrew 共存

Homebrew 安装在 `/opt/homebrew`，gobrew 安装在 `/opt/gobrew`，各自管理自己的 keg、依赖和链接，互不覆盖。同一个命令两边都安装时，终端运行 `PATH` 中靠前的那个；gobrew 的配置写在启动文件末尾并放在最前，因此新终端默认运行 gobrew 的版本，Homebrew 的版本仍可通过完整路径使用，例如 `/opt/homebrew/bin/uv`。

- `gob install` 发现 Homebrew 也提供同名命令时会提示，并给出 `brew uninstall` 命令供你决定是否只保留一份；gobrew 不会卸载或修改 Homebrew 的任何内容。
- `gob doctor` 列出与 Homebrew 同名的命令和当前终端实际运行的路径，并在 gobrew 不在 `PATH` 或排在 Homebrew 之后时提醒。这些是警告，不影响 doctor 的退出码。
- 两个版本可能共用软件自己在用户目录中的配置和缓存（例如 `~/.cache/uv`），这是软件本身的行为；动态库和编译工具的选择还受 pkg-config 等设置影响，不只取决于 `PATH`。
- 想把 Homebrew 中的软件整体迁移过来，见下文“恢复与迁移”中的 `migrate`。

排查当前终端会运行哪个版本：

```sh
type -a uv
```

## 构建源码

```sh
go build -trimpath -o bin/gobrew ./cmd/gobrew
./bin/gobrew version
```

手工下载的 v0.2.0 及之后的发布程序可通过下面的命令加入托管安装，获得 `gob` 链接及自身更新、卸载能力：

```sh
./gobrew self install
```

源码构建也可执行 `./bin/gobrew self install`；没有稳定版本号的开发构建不支持 `self update`，可重新构建后再次安装。

开发或隔离验证可使用短的独立目录：

```sh
prefix=$(mktemp -d /tmp/gb.XXXX)
./bin/gobrew --prefix "$prefix" install hello
```

自定义 prefix 必须是当前用户可写的位置，不会触发 `sudo`。

自定义 prefix 会影响 bottle 可重定位性；不能任意使用很长的路径。

## 更新与卸载命令行

```sh
gobrew self update
gobrew self uninstall
```

`self update` 检查最新稳定发布，校验下载内容后原子切换可执行文件，不自动降级。它只更新托管的 CLI，不更新 Formula；日常使用 `gob update` 即可，它包含这一步。`self uninstall` 移除托管的程序、`gobrew`、`gob` 链接以及 shell 启动文件中的 gobrew 配置段，保留所有已安装软件、配置、数据和缓存；少量归属记录和锁文件保留在命令目录的 `.gobrew` 中，供中断恢复及重新安装使用。用户改动过的文件不会被静默删除。

这些命令也可以写为 `gob self update` 和 `gob self uninstall`。手工放置且未执行 `self install` 的程序会被明确拒绝管理。操作中断后，可重试原命令；如果入口已不存在，重新运行 curl 安装器。

## 日常操作

```sh
gobrew update
gobrew search ripgrep
gobrew info ripgrep
gobrew install ripgrep jq
gobrew list
gobrew outdated
gobrew upgrade
gobrew pin node
gobrew unpin node
gobrew reinstall jq
gobrew uninstall jq
gobrew autoremove --dry-run
gobrew cleanup
gobrew doctor
```

命令语义与 Homebrew 一致：`update` 先更新 gobrew 自身，再刷新并校验 Formula 目录，最后列出可升级的软件；`upgrade` 升级已安装软件及必要依赖，不带名称时升级全部未 pin 的软件。源码构建或未托管的程序会跳过自身更新并说明原因，目录仍会刷新；自身更新失败时目录照常刷新，命令以非零状态退出。`uninstall NAME` 卸载指定 Formula，`self uninstall` 卸载命令行。首次需要目录时会下载官方签名快照；后续操作使用本地快照。`install` 保留已安装版本，显式请求原先作为依赖安装的软件时会将它保留为用户请求。所有根包均已安装时不访问网络。

`deps` 查询传递依赖，`uses` 查询直接反向依赖；`uses --installed` 使用本机安装记录。keg-only 默认只提供 `opt` 路径；`link --force` 可显式公开其命令，但不会覆盖别人的文件。`pin` 约束升级，不是绕过依赖检查。

Formula 安装类命令支持 `--dry-run`。配置入口是全局参数；`gobrew --help` 列出 prefix、cache、并发与缓存容量选项。

可证明 ABI 兼容的旧依赖可以复用，准备阶段仍须验证实际 bottle 的最低版本。缺少证据或 pin 导致版本不足时，操作会在切换前拒绝并要求重新规划。

## 恢复与迁移

如果安装后的初始化失败，软件会显示 `needs-postinstall`，命令退出非零。配置和数据保留，错误可通过 `info` / `doctor` 查看。

```sh
gobrew doctor --repair
gobrew postinstall NAME
```

`doctor --repair` 恢复已记录的文件切换，不自动重跑外部命令。`postinstall` 是显式重试；已确认完成初始化而仅链接失败时，只修复链接。若进程在外部命令返回与完成记录之间终止，执行结果可能未知，应先检查具体错误和数据。

迁移只读取现有 Homebrew receipt，重新下载并安装所选根包，不接管原 Cellar：

```sh
gobrew migrate --from-homebrew /opt/homebrew --dry-run
gobrew migrate --from-homebrew /opt/homebrew
```

## 脚本与客户端接口

`--json` 输出单个带 `schema_version`、`command`、`data`、`error` 的 JSON 文档。进度不混入标准输出。安装结果的 `timings_ms` 给出计划、准备与提交耗时。退出码：0 成功，1 操作失败，2 参数错误，3 不支持，4 冲突/过期计划/忙，5 需要修复，130 取消。

```sh
gobrew --json list
gobrew completion zsh
```

核心使用 `Plan → Prepare → Apply`，通过 `context.Context` 取消，以类型化事件提供进度。没有 daemon、HTTP 服务或 GUI 依赖。参考 [系统架构](docs/architecture.md)。

## 支持边界

- 官方 core bottles、版本化 Formula、平台与依赖解析、keg-only、pin、恢复和配置保留。
- 官方声明式安装步骤使用明确支持集；未知步骤、需要提权的步骤及旧 Ruby hook 会被拒绝，不会静默略过。
- 不支持 Cask、第三方 taps、源码构建、services、任意历史版本或 Intel 安装。
- Go CLI 不需要 CGO；修复部分 Mach-O 路径和签名时使用 macOS 系统工具。
- 目录签名和 bottle 摘要都验证；准备缓存只包含初始化前的不可变树。APFS 克隆不可用时使用独立副本。
- 首版缓存预算默认压缩包 2 GiB、准备树 8 GiB，按逻辑大小计量；活跃操作的缓存暂不淘汰。

## 验证与发布

```sh
go test -race ./...
go vet ./...
go run ./scripts/check-tutorial.go
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
CGO_ENABLED=0 go build -trimpath -o bin/gobrew ./cmd/gobrew
scripts/test-bootstrap.sh "$PWD/bin/gobrew"
scripts/smoke.sh "$PWD/bin/gobrew"
```

测试集中于签名、归档安全、依赖、缓存隔离、状态恢复、链接归属和真实运行。没有覆盖率配额。实瓶 smoke 使用自己的临时目录，不操作现有 Homebrew。

`benchmark` 工作流分别记录冷安装、热重装和已安装 no-op；Homebrew 对照必须使用空的独立安装，禁止传入日常使用的 `/opt/homebrew` 或 `/usr/local`。环境、轮数、版本及逐轮数据随结果保留，不将 no-op 当作重装。

M3 Pro 上的五轮实测中，`hello`、`jq`、证书集合的冷安装中位数为 9.58 秒，热缓存重新安装为 6.99 秒；对照 Homebrew 为 12.81 秒和 10.86 秒。具体方法、限制与原始数据见[性能验证记录](docs/performance.md)。

GitHub Actions 在 PR 运行检查，包括临时目录内的 curl 引导安装、别名和自身卸载验证。版本 tag 必须通过检查、真实安装及五轮性能门禁，才发布已测量的 ARM64 二进制、`gob` 链接、安装脚本、校验文件和基准原始数据。不会自动创建版本 tag。

## 文档

- [Go 1.27.3 入门教程](docs/go-tutorial.md)：独立中文单页文档，面向熟悉 JS/TS 的开发者。
- [系统与代码架构](docs/architecture.md)
- [实机兼容性记录](docs/compatibility.md)
- [性能验证与原始数据](docs/performance.md)
- [工程约束](CONSTRAINTS.md)
- [第三方声明](THIRD_PARTY_NOTICES.md)

## 许可证

项目采用 MIT 许可证。Homebrew 派生部分及其公钥保留原 BSD-2-Clause 声明，详见第三方声明。
