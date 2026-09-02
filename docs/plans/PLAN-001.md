# PLAN-001：建立 v0.1 契约基线与 M0 可执行骨架

## 目标

把两份 v0.1 Draft 转换为可追溯、可测试的实施基线，并完成无需真实 Agent 账号即可运行的 Go 协议骨架和模拟证据闭环。

## 范围

包括产品/技术 SPEC 的阻塞性审查与验收映射、Go 工程入口、配置与领域协议、测试替身、内存状态和最小确定性闭环。SQLite、Git worktree、真实 Codex/Claude Adapter、Daemon、完整恢复、最终报告与 Benchmark 由后续独立 Plan 承担。

## Phase 1：建立可实施契约基线

关联：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)

- [x] 1.1 审查产品需求的一致性、完整性、稳定标识和可测试性
- [x] 1.2 审查技术设计对产品边界、状态不变量和验收要求的覆盖
- [x] 1.3 修订审查确认的阻塞性契约缺口并建立 Feature/AC 追溯基线

## Phase 2：实现协议与工程骨架

- [x] 2.1 建立可构建的 Go 模块、CLI 入口和严格配置基线
- [x] 2.2 实现领域状态、Work Packet、Agent Result、Event 与 Evidence 协议
- [x] 2.3 实现 Fake Clock、Fake Adapter、Fake Process 与内存状态能力

## Phase 3：形成模拟证据闭环

- [x] 3.1 实现最小确定性 Kernel 闭环和 Completion Predicate
- [x] 3.2 建立协议 Golden Test、状态不变量测试和模拟闭环测试
- [x] 3.3 完成 M0 用户入口、限制说明和可复现验收
