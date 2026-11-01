# 本轮待办：v0.5 整理

计划见 [plan.md](plan.md)。每一项都要验收通过才能勾选；有偏差、受阻或评估后不做的，都要写明原因。

- [x] 阶段 0：清理无用文件与代码（`c39363f`）。
  - `resolve_source` 的处理：确认 source 必须解析，因为 `install_name_tool` 会通过 rename 替换掉 symlink。保留这一行为，并加了注释说明。
- [x] 阶段 1：合并重复逻辑（`8e4abbb`）。
  - catalog 的 `CURRENT` 文件和 self-update 的 receipt 改为使用 F_FULLFSYNC。
  - `catalog/history` 的 token 获取保持独立：限额和错误文案都不同，合并后并不会更简单。
- [x] 阶段 2：拆分大文件（`ded25f2`）。
  - 偏差：`newID` 只删掉了不可达的 panic 分支，没有改成返回 error。从 Go 1.24 起 `crypto/rand.Read` 已不会返回错误。
  - 偏差：`envSettings` 没有实现。各处在使用点读取环境变量更直观，测试用 `t.Setenv` 也已足够。
  - 偏差：`root()` 的命令注册没有再拆分。
- [x] 阶段 3：修补架构越界（`4d424f9`）。
  - 偏差：euid 检查保留在核心层。它检查的是真实进程的安全属性，不应信任调用方传入的值。只把 `SUDO_UID/GID` 的读取移到了 CLI。
- [x] 阶段 4：性能（`83871ed`）。
  - GHCR 匿名凭据：hello+jq 冷安装中位数从 2529 降到 2068 毫秒。
  - 包内并行 fsync/codesign 实测没有收益，已回退。其余候选的评估结论见 [performance.md](../docs/performance.md)。
- [x] 阶段 5：交互体验打磨（`d75486a`）。
  - 偏差：held 情况保持退出码 1，与 brew 一致，只在 JSON 中新增 `held` 错误码，没有新增退出码。
  - 偏差：`hasTerminal`（判断 `/dev/tty`，供 sudo 使用）与进度输出的 stderr 判断不是一回事，两者分开保留。
- [x] 阶段 6：测试清理与调整（`281803a`）。
  - 没有删除测试：逐个审查后，现有用例都在保护边界或可见契约。
  - `cli/cleanup_test` 仍然通过 installer 构造已安装状态。这是最直接的真实路径；如果改走 engine，就得伪造整个 Prepare 链路。
- [x] 阶段 7：文档整理，`docs/go-tutorial.md` 未改动。
- [x] 最终验证：`make check`、`scripts/smoke.sh`、`scripts/smoke-version.sh`，以及 UX 手工检查。
  - `make check` 在 `d144722` 上全部通过。测试构建为 `bin/gobrew`，SHA-256 为 `2fdb6970…7f6`。
  - `smoke.sh` 通过。postinstall 的计时行正常，转发的输出中没有多余内容。
  - `smoke-version.sh` 首次运行失败：脚本仍在断言旧文案 `gob unpin uv`。已将断言改为新文案，并增加对 `held` 错误码的检查，重跑通过。
  - UX 手工检查（隔离的短 prefix）：管道下没有转义字符；`NO_COLOR` 生效；重复安装的警告写入 stderr；`Pinned`/`Unpinned` 正常；用法错误提示指向子命令；`--json` 输出单个文档。
  - 未运行：GitHub Actions 发布门禁，以及与 nanobrew 的重测。两者都需要用户先确认发版。
- [x] prefix 过长时的报错：只有打开 bottle 中的文件，才知道 `:any` 类 bottle 里是否嵌着构建路径，元数据里看不出来，所以无法提前到规划阶段报错。现在 Prepare 阶段的报错会写明 keg 内的相对路径、允许的最大 prefix 字节数和当前长度，不再暴露暂存目录路径。
