# 收尾验证：2026-10-28

产品源码固定为 `384b44d26e52485924ad4415c4099a914a9c911a`，后续提交仅涉及比较器的硬件记录与文档。最终本地被测二进制 SHA-256 为 `851e4c324529eb9703371ac5a0b63364a0adbfd6010b0e08308c2b2797a89dbd`。构建使用 Go 1.27.3、`CGO_ENABLED=0`、`-buildvcs=false -trimpath`，版本为 dev；完整参数记录于原始证据。

| 验证 | 结果 |
| --- | --- |
| 全库 gofmt、go mod verify、go vet | 通过 |
| go test -race ./... | 通过 |
| 教程示例检查 | 16 个示例通过 |
| govulncheck@v1.9.0 | 通过，未报告漏洞 |
| CGO=0 构建与 bootstrap | 通过 |
| 原完整实瓶 smoke | 通过，包括 Node、Python、动态库与证书初始化 |
| 历史版本 smoke | uv 0.13.5 → pin/held → unpin → 0.13.6 → 自动清理 → no-op → doctor/卸载/清理，全链路通过 |

两条 smoke 均使用独立短 prefix、HOME、XDG 与缓存，PATH 只搜索系统工具；未修改开发者 Homebrew 或 `/opt/gobrew`。历史版本脚本保留自己的 source-status、命令、退出码与 manifest；二进制的实际构建提交由 build 输出与上述摘要标识，脚本执行时的文档提交不应误认为二进制被重新构建。

[原始质量检查与 smoke 证据](evidence.tar.gz)保留完整命令、退出码和输出。最终性能结果见 [Homebrew 报告](../../performance.md)与 [nanobrew 报告](../../nanobrew-comparison.md)，分别绑定各自的二进制与运行环境；没有用不同机器的绝对时间相互比较。

独立审查覆盖命令编排、清理、历史版本、依赖最低版本及比较器结果分类。发现的问题都先有失败回归，再完成最小修复；相关回归和全量 race 通过。错误语义仍在 Plan/Prepare/Apply 与原有所有权、持久化恢复边界内。无新增命令、无通用框架、无保留的猜测性性能改动。

ub-mode 的 `crapper`、`dryer`、`mutator` 在本会话工具与 PATH 中仍不可用，因此**未执行，未给出 CRAP、重复率或变异分数**。手工回归的红绿结果证明具体修复能被测试检测，但不替代这些专用工具。

详细行动、原因与阶段证据见 [工作日志](../../../tasks/work-log.md)；交付状态见 [待办清单](../../../tasks/todo.md)。
