# BUG-001：Attempt Lease 在 Review 阶段过期

## 状态

`verified`

## 用户可见现象与影响

2026-09-02 执行全仓 `go test ./... -count=1` 时，真实 Unix Socket E2E 的 Goal 在 Implementer、Patch 和 Validator 已成功后停在 `WAITING`。状态显示 Attempt 为 `REVIEWING`、Lease 仍为 `ACTIVE` 但已过期，并生成 `internal_invariant_violation` Gate。该缺陷会让耗时超过 Lease TTL 的合法 Review、Validator 或 Promotion 无法完成。

## 根因与触发条件

- 根因：Orchestrator 只在 `waitAgent` 等待 Implementer 子进程期间调用 `HeartbeatLease`。
- 缺失阶段：环境准备、Patch 捕获、Validator、独立 Review 和 Promotion 都属于同一 Attempt/Lease 生命周期，却没有续租。
- 触发条件：任一缺失阶段加上调度抖动的耗时超过剩余 TTL；全仓并行测试负载使 5 秒测试 TTL 稳定暴露该边界。
- 正确的 Store 行为是到期后 fail closed；错误位于 Orchestrator 的 Lease 生命周期 owner，而不是 Store 的过期检查。

## 修复

- Claim 成功后启动单一 `keepLeaseAlive`，由它独占该 Lease 的版本并覆盖整个 Attempt 生命周期。
- 心跳失败会用 cause 取消当前 Attempt；若持久 Lease 已由正常 Promotion/失败处理释放，则视为正常终止而不制造错误 Gate。
- `waitAgent` 只负责子进程等待与取消，不再并行竞争 Lease CAS。
- 修复与本报告进入同一个 M6 原子提交，Git history 以 `BUG-001` 关联。

## 验证 Evidence

- `go test ./internal/orchestrator ./internal/app -count=1`：PASS。
- 将 Unix E2E 配置为 `leaseTTL=400ms`、`heartbeatInterval=50ms`，Reviewer 固定等待 `800ms`；`go test ./internal/app -run '^TestRealUnixAPIRunsGoalToVerifiedFinalReport$' -count=10`：10/10 PASS，总时长 40.673s。
- 修复后的 `go test ./... -count=1`：PASS。

## 预防

- Lease 心跳必须绑定 Attempt 生命周期而不是单一外部子进程阶段。
- 保留“Review 明确长于 TTL”的 E2E 夹具，防止未来重构把续租范围缩回 Agent Wait。
- Store 继续在 Heartbeat、Promotion、Release 和迟到 Generation 上 fail closed，不通过放宽过期检查规避问题。
