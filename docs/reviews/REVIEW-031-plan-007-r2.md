# REVIEW-031：PLAN-007 系统闭环补充 Plan Review

## 审查对象

- Plan：`PLAN-007`
- Objective：`OBJ-001`
- Revision：R2
- 变更原因：发布前 AC 对账发现 daemon 尚未编排已实现的 M1–M5 组件

## Verdict

`PASS_WITH_NOTES`

## 审查结论

- 新增 Item 4.1–4.2 是原 Objective“实现并验收所有 v0.1 feature”的必要缺口修复，不扩大到远端发布、生产操作或 v0.2 强隔离。
- 执行引擎必须复用现有 SQLite CAS/Lease、不可变 Packet、Workspace/Patch、Validator/Evidence、Review 和 Promotion owner，不另建平行状态或绕过门禁。
- E2E 必须通过真实 Unix Socket 和真实子进程边界运行；Provider CLI 可用本地确定性 fixture 代替付费模型，但不能替代已记录的两条真实跨 Provider smoke。
- 引擎错误必须进入稳定 Failure/Reconcile/Gate 或可恢复 Effect 状态，不能只写日志后丢失。

## Notes

- Planner 的自然语言生成与 Implementer 执行应分开验收；显式 `--proposal-file` 是可复现入口，但不能被描述为自动 Planner 已完成。
- 若自动 Final Report 仍需要调用方拼装可信 facts，AC-FR-062/091 只能保持 FAIL；引擎必须从 Store 当前事实投影而不是信任请求体。

## 下一路由

允许继续 PLAN-007 Phase 4.1；实现后重新执行 Change Review。
