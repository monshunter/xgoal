# PLAN-005：实现 M4 Claude Adapter 与独立 Review

## 目标

在 M3 Codex 与 M2 确定性验收基础上接入 Claude Code CLI，并建立独立 Reviewer Packet、结构化 Finding 与持久审查状态，使 Codex 实现/Claude Review 和 Claude 实现/Codex Review 两条真实路径成立。

## 范围

包括 Claude Passive/Active Probe、Print Mode/Stream JSON/JSON Schema、角色工具权限、取消和安全 Resume；Review Packet/Result/Finding 契约与不可变制品、SQLite 迁移和状态转换；独立 Reviewer Session；两条跨 Provider 真实 smoke。完整 Reconcile/Gate、Daemon/API、最终报告与 Benchmark 属于 M5–M6。

## Phase 1：冻结 Claude 与 Review 合同

关联：[技术 SPEC M4](../../xgoal-technical-spec-v0.1.md#29-实施阶段)

- [x] 1.1 以当前 Claude Code CLI、产品/技术 SPEC 冻结命令、Capability、Stream JSON、权限、结果和错误合同
- [x] 1.2 冻结 Review Packet、Review Result、Finding 权威/状态、独立会话和持久化设计并通过 Design Review

## Phase 2：实现 Claude Adapter

- [x] 2.1 实现 Stream JSON 未知事件兼容、限长脱敏制品、供应商模型核算字段丢弃与严格 Agent Result 解析
- [x] 2.2 实现 `Start/Wait/Cancel`、角色最小工具集、`dontAsk`、stdin Prompt、只读 Packet 和进程组回收
- [x] 2.3 实现 Passive/Active Probe、显式正超时和 Session provenance 安全 Resume
- [x] 2.4 通过 fake Claude executable 的合同、失败注入、权限和跨进程恢复测试

## Phase 3：实现独立 Review 与 Finding

- [x] 3.1 实现版本化 Review Packet/Result/Finding、严格 Schema、不可变制品与独立 Reviewer Session 约束
- [x] 3.2 实现 Codex/Claude Reviewer 执行、实际 Patch/Validator 输入绑定和 Finding Authority
- [x] 3.3 增加 SQLite Review/Finding 迁移、幂等写入/读回、阻断 Finding 状态转换与篡改拒绝

## Phase 4：双向真实验收与收口

- [x] 4.1 使用真实 Codex 实现并由独立 Claude Session Review，M2 确定性结果与 Review Finding 分层一致
- [x] 4.2 使用真实 Claude 实现并由独立 Codex Session Review，权限、Session 与制品绑定可读回
- [x] 4.3 完成 M4 可复现验收入口、回归/迁移/失败矩阵、能力边界说明与 Change Review
