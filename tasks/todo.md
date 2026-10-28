# 当前交付清单：现有功能与性能收尾

更新日期：2026-10-28（Asia/Taipei）。任务定义和验收条件见 [执行计划](plan.md)，过程证据见 [工作日志](work-log.md)。每项只有验收通过后才能勾选；受阻、不可比和未运行必须写明。

- [x] 用户选择本轮范围：现有功能与性能收尾，暂不新增命令；记录新计划。
- [x] 1. 固定 install/pin/no-op、自动更新、dry-run、清理及依赖保护的行为；补必要回归，修复实际失败。
- [x] 2. 在完全隔离环境完成历史 uv → unpin → install 升级 → 自动清理链路，并通过原实瓶 smoke。
- [x] 检查点 A：行为测试与真实链路均通过，开发者环境未修改。
- [x] 3. 重测当前 gobrew/Homebrew 五轮冷安装、热缓存重装、no-op，保存版本与原始证据。
- [ ] 4. 固定 nanobrew 版本并验证隔离；完成等价场景的五轮同机对比，单列不可比或受阻场景。
- [ ] 检查点 B：当前基线及比较结果可复现，旧性能数据未覆盖，失败样本未丢弃。
- [ ] 5. 根据测量处理一个主要瓶颈；有确定收益则保留，否则记录无确定收益，不强行优化。
- [ ] 6. 同步 README、交接及性能文档；完成全量检查、最终审查，记录专用质量工具的真实执行状态。
- [ ] 交付：按单元提交，通过检查后合入并推送 main；正式 tag/发版不在本轮默认操作内。

## 历史：首轮交付清单

以下勾选仅表示首轮工作完成，不适用于当前版本的新增验证。

- [x] Architecture contracts, constraints and shared domain values.
- [x] Verified official JWS catalog and local immutable index.
- [x] Dependency planning, versions, pins and reverse dependencies.
- [x] Artifact preparation, safe extraction, immutable cache and macOS adaptation.
- [x] CLI command surface, JSON contracts, completion and cancellation.
- [x] Formal independent Go tutorial and executable example checker.
- [x] Complete independent boundary review and recovery regression fixes.
- [x] Real software smoke tests including dynamic dependencies and initialization.
- [x] Five-round isolated Homebrew performance comparison and report.
- [x] Final race/vet/vulnerability/build and workflow checks.
- [x] Record compatibility evidence, release gates and remaining limitations.
