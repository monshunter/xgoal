# REVIEW-032：v0.1 计量边界移除复审

## 审查对象

- 产品 SPEC：`xgoal-product-spec-v0.1.md`
- 技术 SPEC：`xgoal-technical-spec-v0.1.md`
- Design：`DESIGN-005`
- Plan：`PLAN-007`
- Revision：用户于 2026-09-02 明确收窄的产品边界

## Verdict

`PASS`

## 审查结论

- xgoal 被定义为原生 Agent 的外在执行骨架，不再采集、估算、限制或归因模型 Token、费用、余额与账单。
- Active Probe 仍须显式触发、使用受信 Provider Transport，并在调用方给定的正超时内结束；这是进程安全合同，不是计量合同。
- 输出字节上限、进程取消、并发策略和无实质进展判定继续防止本地执行失控，且不参与 Completion Predicate。
- Final Report 只投影 xgoal 可直接观察的运行时长、Attempt/Human Gate 次数；Benchmark 公平性只冻结输入、验收、工具事实与逐任务超时。
- PLAN-007 新增的移除项覆盖配置、协议、Adapter、状态、API、Store、报告、Benchmark、测试和文档，足以防止只删 UI 而保留隐式账本。

## 实施约束

- 旧 SQLite 数据库升级必须安全移除不再使用的计量表；迁移失败仍按既有备份与 fail-closed 合同处理。
- Provider 原始输出若包含用量字段，Adapter 不得把它们提升为规范化事件、状态或报告，并应避免在脱敏后的保存载荷中保留。
- 不能把移除计量误写成移除超时、输出限制、取消、Lease 或 no-progress 控制。

## 下一路由

允许继续 PLAN-007 Phase 4.4，并在完整验证后进入 M6 Change Review。
