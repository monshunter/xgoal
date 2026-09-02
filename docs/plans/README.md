# Plans 工作区

## 目的

保存 Standard Objective 下按 Phase 组织的单文件 Plan Checklist，让 Agent 在 Plan Review 通过后选择下一条未完成任务并继续工作。

## 制品与命名

- 正式制品使用 `PLAN-<编号>.md`，Plan 与 Checklist 不拆成两个文件。
- 正文先写简短目标与范围，再使用 `## Phase N：标题` 组织任务。
- Phase 表示一个边界清楚的小目标；Phase 内使用 `- [ ] N.M 任务` 表示可一次领取和完成的原子 Item。
- Item 说明要完成的任务，不展开实现过程、验证记录或额外状态。
- 需要关联 Spec 时，在对应 Phase 下保留简短链接，不建立子 Spec 状态表。

## 生命周期与归档

Standard Intake 后立即创建至少一份正式 Plan。Plan 新建或目标、范围、Phase、顺序、验收覆盖发生实质调整后，必须在第一条 Item 前通过 Plan Review。新建 Item 使用 `[ ]`，任务实际完成且当前 Evidence 成立后改为 `[x]`。所有 Phase 的 Item 完成并通过 Change Review 与必要验证后，在 Progress 勾选对应上层 Plan；历史 Plan 按项目需要归档，不另设一套状态字段。

## 负责的 Skills

`autogo-plan-write`、`autogo-plan-review`、`autogo-change-close`。

## INDEX 维护

Agent 在 Plan 新建、改名或归档后同步维护 `INDEX.md`。
