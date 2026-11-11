# 本轮待办：v0.6 修复、体验、性能与功能补足

计划见 [plan.md](plan.md)。每一项都要验收通过才能勾选；有偏差、受阻或评估后不做的，都要写明原因。决定事项按推荐执行：D1 短开短关 bbolt，D2 只做 `config`/`leaves`/`desc`/`home`/doctor 增强，D3 不动持久化边界。

- [x] 阶段 0：修复缺陷（`7984eec`）。
  - 未做：symlink 步骤的 `uninstall` 字段。要实现或拒绝它，需要先确认 Homebrew 上游的语义。
  - 未做：`info --analytics` 失败时的退出码。取决于 brew 在这里用的是 `onoe` 还是 `ofail`，有待核实。
  - 未做：`scripts/test-bootstrap.sh` 的 bash/fish 分支。
- [x] 阶段 1：安装期间只读命令可用（`bab0e2f`）。先写的测试在旧代码上失败。
- [x] 阶段 2：错误文案与颜色（`9f6c6f2`）。JSON 的 `message` 与 `code` 不变。
- [x] 阶段 3：进度输出（`5c1ea49`）。
  - 未做：relocate/codesign/link 阶段的事件、下载速度、SIGWINCH，以及首次安装的输出顺序。
- [x] 阶段 4：帮助与 flag 对齐（`bc8aaa9`）。
  - 偏差：`gobrew -v` 不带命令时仍打印版本，带命令时才是 `--verbose`。
  - 偏差：`search --formula`、`deps --full-name` 保留为显式忽略，因为 gobrew 只有 Formula，官方 Formula 的全名就是名字本身。
- [x] 阶段 5：`config`、`leaves`、`desc`、`home` 与 doctor 增强（`096e94d`）。
  - `doctor --repair` 补链接走现有的所有权 journal，没有新增机制。
- [x] 阶段 6：性能（`a0d67bf`）。
  - 做了：按名查询改为按 key 读取；目录未变化时 `update` 复用已有索引；自更新与目录下载并发。数据见 [performance.md](../docs/performance.md)。
  - 未做：`fs_usage` 测量需要 root；“预装约 100 个包”的场景需要下载数 GB。没有这两组测量，清理读记录、状态分 key、批量刷盘都不动手。TLS 预热与 OCI 预取同样待冷缓存测量。
- [x] 阶段 7：中断恢复与 history 拒绝测试（`1e534ab`）。
- [x] 阶段 8：文档与发布工程。
  - 写了 v0.6.0 发布说明草稿，其中写明恢复 `self`。`release.yml` 缺少说明文件时直接失败；新增对被测量的同一个发布二进制运行 `smoke.sh` 与 `smoke-version.sh`。只改了文件，没有触发 Actions。
  - 更新了 README、架构文件清单和 status。`query.go` 按 search/info/list/deps/shellenv 拆分，只移动、不改行为；拆分前后代码行逐行比对一致。
  - 大小上限：压缩包两边本来都是 32 MiB；install.sh 解压时补上了与自更新一致的 64 MiB 可执行文件上限。
- [x] 最终验证：`make check`、`scripts/smoke.sh`、`scripts/smoke-version.sh`。
  - `make check` 在 `1b3f3fc` 上全部通过。测试构建为 `bin/gobrew`，SHA-256 为 `484b5258…18c6`。
  - `smoke.sh` 与 `smoke-version.sh` 都在隔离的临时目录中对同一个构建运行，均通过。
  - 未运行：GitHub Actions 发布门禁，以及与 nanobrew 的重测。两者都需要用户先确认发版。没有打 tag。
