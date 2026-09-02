# ADR-002：确定性 Kernel、串行 Promotion 与最终报告协议

## 状态

`active`

## 上下文

xgoal 需要让多个概率性 Agent 参与长期工程目标，同时避免聊天状态、Agent 自述、并发 Git 写入和旧测试结果成为完成依据。文件系统副作用又不能与 SQLite 做真正的跨介质原子事务。

## 决策

1. Go Kernel 独占 Goal、Work、Attempt、Lease、Gate、Evidence、Effect、Promotion 和 Completion 状态；Planner/Implementer/Reviewer 只返回版本化结构化结果。
2. SQLite 当前表与同事务追加 Event 是唯一运行真相；不把 Markdown、会话或完整 Event replay 设为第二真相。
3. v0.1 `maxParallel=1`，每次 Attempt 使用独立 worktree。Agent 修改被捕获为不可变 Patch Bundle，在最新 Integration Tree 上重放、复验后，由 xgoal 用 Git ref CAS 串行创建带 Trailer 的 Commit。
4. Agent Claim、Reviewer Inference、文件/Git Fact、确定性 Evidence 与 Human Decision 显式标 Authority；低等级信息不能覆盖高等级事实。
5. Final Report 先 canonical render 和私有临时落盘，再与 Completion tuple 一起提交为 `PENDING_RENAME`，随后原子 rename、Hash 读回并标记 `COMMITTED`。重启或读取 pending 报告时按数据库 payload 幂等恢复。
6. Benchmark 三组必须共享 fixture、Goal、隐藏验收和资源上限 Hash；失败与 unknown 指标保留，未执行的比较保持 `NOT_RUN`。

## 后果

正确性与恢复优先于吞吐；独立 Review 和最终复验会增加时延/资源，但资源缺失不得伪造为零。文件/DB 的可见状态存在短暂 `PENDING_RENAME`，但报告字节和 Hash 已在 DB 中持久化，读取与启动恢复会收敛。L0 只提供可归因本地执行，不提供敌对隔离。

## 验证

- 状态机、CAS/Lease/Generation、Effect、Evidence Staleness 与 Completion 的模型/并发测试；
- Patch 全类型捕获、Scope/逃逸拒绝、Promotion Commit 后恢复不重复 Commit；
- Final Report rename 中断、缺失重建、篡改拒绝与读取时幂等恢复；
- 自动 Planner→多 Work→失败重试→独立 Review→Promotion→Final Report 的 Unix Socket E2E；
- 固定 Benchmark Suite 的三组 Comparison Identity 校验。
