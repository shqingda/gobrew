# 实机兼容性记录

## 1. 验证环境

2026-10-27 在 Apple M3 Pro、18 GiB 内存、macOS 27.0.1、Go 1.27.3 上验证。使用普通用户和独立短路径 `/tmp/gb.WDeQ`，未修改现有 Homebrew，也未创建默认 `/opt/gobrew`。

官方签名目录的本地快照为 `b03174d39e5d84778dfbd8269cf55e2700c31106a2a9cea0f53df0cd3db5b894`。安装共得到 38 个 `ready` 包。表中版本为 Formula 的 package version，包含 revision；命令本身可能只显示上游版本。

## 2. 已执行的功能验证

| Formula | 版本 | 实际验证 |
| --- | --- | --- |
| `hello` | `2.12.3` | 执行程序，输出 `Hello, world!` |
| `jq` | `1.8.2` | 读取 JSON 并得到整数 42，加载 oniguruma 动态库 |
| `ca-certificates` | `2026-10-20` | 运行声明式初始化，生成非空证书包 |
| `git` | `2.56.0_1` | 执行 `git --version` |
| `node` | `26.12.1_1` | 执行 JavaScript、随机数生成、ICU 国际化日期格式化 |
| `node` 附带 npm | `11.20.0` | 执行初始化后公开的 `npm --version` |
| `python@3.15` | `3.15.1` | 导入 SSL、SQLite；内存数据库查询；创建默认 TLS 上下文 |
| Python 虚拟环境 | Python `3.15.1`、pip `26.2.2` | 创建 venv，运行其中的 pip，读取证书信任库 |
| `go` | `1.27.3` | 用安装出的工具链编译并运行标准库程序，输出 42 |
| `ripgrep` | `15.2.0` | 执行程序并读取版本和正则引擎信息 |
| `sqlite` | `3.54.1` | 通过 keg-only `opt` 路径运行 SQL，得到 42 |
| `openssl@3` | `3.5.9` | 通过 keg-only `opt` 路径执行版本查询 |

Python 实际加载 OpenSSL 4.1.0，默认 TLS 上下文读取到 189 个 CA 证书。Node 可执行文件通过系统 `codesign --verify`。`doctor` 对该安装集合未报告问题；对 `node python homebrew/core/go` 的重复安装返回三个已安装包，没有准备或提交阶段。

验证结束后，已通过正常卸载、`autoremove` 和 `cleanup` 移除全部 38 个包，`list --json` 返回空数组；证书配置文件仍被保留。

## 3. 已处理的真实 bottle 差异

- 现代 bottle 不一定包含 `INSTALL_RECEIPT.json`。缺失时从官方 OCI manifest 获取 runtime facts，并核对 manifest 摘要及其 layer 与签名目录中的 bottle SHA。
- Java class 与 fat Mach-O 共享 `CAFEBABE` magic。gettext 内的 class 文件按完整 Java 结构识别，不能仅凭四字节头误当 Mach-O。
- Python 的证书链接通过相对路径指向 prefix 下的 `etc`。只接受落在明确允许目录内的映射，提取时最后创建链接并禁止跟随。
- `@@HOMEBREW_LIBRARY@@` 等已知占位符有明确映射；未知占位符拒绝激活。
- 旧收据可能含有已脱离当前依赖图的间接 ICU 依赖。只有经过真实 Mach-O 导入检查才允许过滤；当前图、直接声明和实际导入的最低版本要求继续保留。

## 4. 验证范围

这是一组具体版本的兼容性证据，不代表整个 Homebrew core 已逐包验证。官方签名或上游步骤发生变化时，安装仍须重新通过预检。支持集之外的步骤、不可解析的加载路径和无法证明兼容的依赖在激活前拒绝。

macOS 15 与 26 的 ARM64 实机流程已配置在 GitHub Actions；本地记录的操作系统仅为 27.0.1。当前任务没有推送代码或触发远程 CI，因此不能把配置好的矩阵当作已通过的远程结果。

故障测试通过临时文件系统和可控制的中断边界验证恢复、归属、配置保留及初始化失败语义。未实施物理断电实验，也不承诺撤销任意第三方外部命令已经产生的数据副作用。

## 5. 可复现检查

```sh
CGO_ENABLED=0 go build -trimpath -o bin/gobrew ./cmd/gobrew
scripts/smoke.sh "$PWD/bin/gobrew"
```

脚本使用自己创建的临时 prefix 和缓存，覆盖 CLI、动态库、Node/npm、Python/venv、证书、重复安装、重装、pin、链接、卸载及清理。退出时删除这些临时目录。

本次已完整运行该脚本并通过；测试 prefix 为 `/tmp/gb.1PzR`，结束后已由脚本清理。
