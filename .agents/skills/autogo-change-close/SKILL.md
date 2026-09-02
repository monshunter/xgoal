---
name: autogo-change-close
description: "对账 Standard Plan 的实际结果、Review、Progress 与 Git，并在门禁成立后关闭和提交；准备完成当前 Plan 时使用；Fast 不调用本 Skill。"
---
# autogo-change-close
## 目标
以当前 Evidence 核对正式 Plan，关闭已完成工作并在可安全隔离时创建原子 Commit；不检查 Harness 安装、Skill 文案或固定的元流程制品。

## 输入与发现
- Progress 中当前 Objective、状态与 Plans Checklist
- 当前 Plan、Diff、测试、运行结果和 Change Review
- 任务实际需要的 Spec、Design、Scenario、Operation、Deployment record 或 Journal
- 剩余问题、范围外发现和 Git 工作树

## 输出与持久制品
- 当前 Plan 是否满足关闭条件的准确结论
- 已对账的 Plan、Progress 与相关事实 owner
- 可安全隔离时的单一工程意图 Commit
- 下一未完成 Plan，或 Completed、Waiting、Cancelled 的准确状态

## 副作用与 Human Gate
修改 Plan、Progress 和必要的事实 owner，可创建 Commit；不得未经确认重写共享历史、push、发布或执行范围外动作。

## 执行步骤
1. 从 Progress 锁定当前 Plan，逐项核对 Phase Item、正式制品、实现、测试、环境和 Git
2. 确认任务实际需要的 Change Review、E2E 或部署后验证已经通过；失败返回对应 owner Reconcile
3. 只勾选已经完成且有当前 Evidence 的 Phase Item；不把执行日志写入 Plan
4. 全部 Phase Items 完成后，才在 Progress 将当前 Plan 勾选为 `[x]` 并重新汇总 Objective
5. Journal、Session Review 或 Harness Evolution 只在当前任务确有恢复、复盘或演进价值时由对应 Skill 处理，不是关闭前置条件
6. 核对 Diff 与授权范围；能安全隔离时创建一个原子 Commit，不混入无关改动
7. 当前 Objective 还有未完成 Plan 时立即选择下一项并执行 Plan Review；不得把单个 Plan 完成误报为 Objective 完成
8. 全部 Plan 完成后把 Objective 汇总为 `已完成`，交付 Completed brief
9. Human Gate 或真实 Blocker 时不勾选当前 Plan，Objective 保持 `正在处理`；把等待原因和恢复条件写入产生等待的事实 owner，跨会话恢复有价值时再写 handoff Journal
10. 用户明确取消时记录已有资产、未完成事实和清理状态，再从活动 Progress 移除 Objective；需要长期保留取消上下文时再写 cancel Journal

## 验证与完成
- Plan、Progress、Git 与当前 Evidence 一致，没有把未完成工作标记为完成
- 当前任务要求的 Review、测试和运行验证成立
- Harness 自身完整性、Skill 固定文案和非必要流程制品不构成关闭条件
- Commit 保持单一工程意图，或明确说明为什么不能安全提交

## 失败、重试与幂等
验收不足返回对应 Implement、Review、E2E 或部署 owner 进入 Reconcile；无法恢复一致状态时进入 `Waiting`，不强行关闭。
- 重复执行前读取当前文件、Git 和运行状态，不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
