# gobrew

使用 Go 实现的 macOS 包管理器，读取 Homebrew 官方 core 元数据与预编译 bottles。CLI 和未来 macOS 客户端共享同一安装核心。

当前支持 Apple Silicon、macOS 15+，构建使用 Go 1.27.3。安装在独立 prefix，默认 `/opt/gobrew`。不调用 Homebrew 或 Ruby 作为安装兜底。

## 构建与初始化

```sh
go build -trimpath -o bin/gobrew ./cmd/gobrew
./bin/gobrew version
```

默认目录的首次创建需要管理员权限。提权过程只创建一个新目录并移交给当前用户；不会下载或执行软件，也不会修改已有目录的所有权。

```sh
sudo ./bin/gobrew init
./bin/gobrew init
eval "$(./bin/gobrew shellenv)"
```

若 `/opt/gobrew` 已经由当前用户拥有，直接执行非 root 的 `init`。其他软件操作必须以普通用户运行。`shellenv` 只打印命令，不自动改写 shell 配置；确认后可将输出加入自己的 shell 启动文件。

开发或隔离验证可使用短的独立目录：

```sh
prefix=$(mktemp -d /tmp/gb.XXXX)
./bin/gobrew --prefix "$prefix" init
```

自定义 prefix 会影响 bottle 可重定位性；不能任意使用很长的路径。

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

`update` 刷新目录，`upgrade` 更新软件。首次需要目录时会下载官方签名快照；后续操作使用本地快照。`install` 保留已安装版本，显式请求原先作为依赖安装的软件时会将它保留为用户请求。所有根包均已安装时不访问网络。

`deps` 查询传递依赖，`uses` 查询直接反向依赖；`uses --installed` 使用本机安装记录。keg-only 默认只提供 `opt` 路径；`link --force` 可显式公开其命令，但不会覆盖别人的文件。`pin` 约束升级，不是绕过依赖检查。

安装类命令支持 `--dry-run`。配置入口是全局参数；`gobrew --help` 列出 prefix、cache、并发与缓存容量选项。

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
scripts/smoke.sh "$PWD/bin/gobrew"
```

测试集中于签名、归档安全、依赖、缓存隔离、状态恢复、链接归属和真实运行。没有覆盖率配额。实瓶 smoke 使用自己的临时目录，不操作现有 Homebrew。

`benchmark` 工作流分别记录冷安装、热重装和已安装 no-op；Homebrew 对照必须使用空的独立安装，禁止传入日常使用的 `/opt/homebrew` 或 `/usr/local`。环境、轮数、版本及逐轮数据随结果保留，不将 no-op 当作重装。

M3 Pro 上的五轮实测中，`hello`、`jq`、证书集合的冷安装中位数为 9.58 秒，热缓存重新安装为 6.99 秒；对照 Homebrew 为 12.81 秒和 10.86 秒。具体方法、限制与原始数据见[性能验证记录](docs/performance.md)。

GitHub Actions 在 PR 运行检查。版本 tag 必须通过检查、真实安装及五轮性能门禁，才发布已测量的 ARM64 二进制、校验文件和基准原始数据。不会自动创建版本 tag。

## 文档

- [Go 1.27.3 入门教程](docs/go-tutorial.md)：独立中文单页文档，面向熟悉 JS/TS 的开发者。
- [系统与代码架构](docs/architecture.md)
- [实机兼容性记录](docs/compatibility.md)
- [性能验证与原始数据](docs/performance.md)
- [工程约束](CONSTRAINTS.md)
- [第三方声明](THIRD_PARTY_NOTICES.md)

## 许可证

项目采用 MIT 许可证。Homebrew 派生部分及其公钥保留原 BSD-2-Clause 声明，详见第三方声明。
