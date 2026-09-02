# REVIEW-011：PLAN-002 M1 Change Review

## 审查对象

- Plan：`PLAN-002`
- Objective：`OBJ-001`
- Revision：`feat/xgoal-v0.1` 未提交工作树，全部 Phase Item 完成后的首次 Change Review
- 规范：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)

## Verdict

`FAIL`

## 发现

### [High] READY Work 可绕过后置 Required Gate 被直接 Claim

- 位置：`internal/store/sqlite/control_repository.go` 的 `ClaimWork`
- Evidence：`ClaimWork` 只验证 Work Version/State 和项目单活 Lease；Required Gate 只在 Ready 派生与 `NextReadyWork` 中检查。Work 进入 `READY` 后新建 Required Gate 时，调用方可直接 Claim。
- 影响：Store 的权威写入口没有 Fail Closed，调度器过滤不能构成授权边界。
- 路由：在 Claim 的同一事务内重查 active Goal/Plan、依赖与 Required Gate，并加入 Gate 后置创建的负向测试。

### [High] 通用 Goal 状态入口可绕过 Completion Predicate

- 位置：`internal/store/sqlite/goal_repository.go` 的 `UpdateGoalState`
- Evidence：领域状态图允许 `VERIFYING → COMPLETED`，通用 Repository 方法会直接执行该转换，不要求 Completion Facts、Required Work、Gate、Evidence Set 或 Report Hash。
- 影响：违反 AC-FR-062 和技术 SPEC 21.2，Agent/调用方可绕过唯一完成事务。
- 路由：通用入口硬拒绝 `COMPLETED`，只允许 `CompleteGoal` 写入；同时以下沉的 SQLite CHECK 约束最终状态元组完整性。

## 已成立 Evidence

- `make verify-m1`：PASS；覆盖格式、全仓测试、20 次乱序、Race Detector、vet、CLI smoke 和无 CGO 双平台 SQLite 交叉构建。
- 真实测试子进程退出后的 Store 重开、Effect Read Back 与幂等重放：PASS。
- 32 路跨 Work 项目单槽 Lease 竞争、32 路 Idempotency 同 Key 竞争与终态穷举：PASS。
- 上述 Evidence 未覆盖两个绕过入口，因此不能关闭 Plan。

## 下一路由

回到 `autogo-tdd` / `autogo-change-implement` 修复两个 High 发现，运行定向负向测试与完整 `make verify-m1` 后重新 Change Review。
