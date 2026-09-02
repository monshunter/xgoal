# PLAN-002：实现 M1 持久状态与控制循环

## 目标

把 M0 内存模拟骨架升级为可跨进程重开、并发安全且可确定恢复的 SQLite 状态与控制循环，满足 M1 重复 Lease、状态属性和进程重启恢复门禁。

## 范围

包括 SQLite 驱动决策、Migration、Repository/Event/CAS/Idempotency/Effect、完整 M1 领域状态、Lease Generation 与持久 Completion；不包括 Git worktree/Patch/Promotion、真实 Agent Adapter、Daemon/API、完整 Reconcile/Gate UI、Final Report 与 Benchmark。

## Phase 1：冻结持久化决策与 Schema

关联：[技术 SPEC M1](../../xgoal-technical-spec-v0.1.md#29-实施阶段)

- [x] 1.1 形成 SQLite Driver、CGO、事务与迁移策略 ADR 并通过 Design Review
- [x] 1.2 建立可升级的 SQLite Migration 与项目 Store 打开/关闭边界
- [x] 1.3 实现 Aggregate Repository 的状态、Event 与 CAS 原子事务
- [x] 1.4 实现 API Idempotency Record 与外部 Effect Journal 的持久不变量

## Phase 2：实现持久控制状态

- [x] 2.1 补齐 Goal/Revision/Plan/Work 的持久状态合同
- [x] 2.2 补齐 Attempt/Gate/Lease/Effect 的持久状态合同
- [x] 2.3 实现 Lease 获取、Heartbeat、Generation、过期读回与迟到写回隔离
- [x] 2.4 将调度和 Completion Predicate 接入持久 Store 与原子完成事务

## Phase 3：验证重启恢复与并发不变量

- [x] 3.1 实现 Store 重开、进程边界和 Effect Read Back 的确定性恢复场景
- [x] 3.2 建立状态模型、CAS、Idempotency、重复 Lease 与竞态测试
- [x] 3.3 完成 M1 可复现验收入口与能力边界说明
