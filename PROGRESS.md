# Project Progress

> 项目根目录全局唯一的上层进度视图。`autogo` 只在文件缺失时创建；之后由主 Agent 维护，重装不覆盖。

## 使用规则

- 每个 Objective 使用一个由 `---` 分隔的区块，包含 `ID`、`Objective`、`Status` 和内嵌的 `Plans` Checklist。
- Objective 状态只允许使用：`还没开始`、`正在处理`、`已完成`。
- 接受新目标时，追加一个 Objective 区块，并同时列出完成它所需的 Plans；不创建未来占位项。
- 每个 Plan 包含名称和路径；`[ ]` 表示未完成，`[x]` 表示已完成。
- 开始处理任一未完成 Plan 前，将所属 Objective 改为 `正在处理`；Plan 达到自身完成条件并通过当前验证后，才勾选为 `[x]`。
- Plans 全部未开始时，Objective 使用 `还没开始`；开始处理后且仍有未勾选 Plan 时，使用 `正在处理`；全部 Plans 勾选后，使用 `已完成`。
- 新增、拆分或调整 Plans 后立即重新汇总所属 Objective。除此之外，本文件不记录任何 Plan 内部内容。

## 格式样例

> 以下内容只演示格式，不表示项目真实进度。

---
ID: OBJ-001
Objective: 完成示例功能
Status: 正在处理
Plans:
- [x] [PLAN-001：完成基础实现](docs/plans/PLAN-001.md)
- [ ] [PLAN-002：完成验证与收口](docs/plans/PLAN-002.md)
---

## Objectives

---
ID: OBJ-001
Objective: 基于产品与技术 SPEC 完成并验收 xgoal v0.1 全部功能
Status: 已完成
Plans:
- [x] [PLAN-001：建立 v0.1 契约基线与 M0 可执行骨架](docs/plans/PLAN-001.md)
- [x] [PLAN-002：实现 M1 持久状态与控制循环](docs/plans/PLAN-002.md)
- [x] [PLAN-003：实现 M2 Git、环境与验证闭环](docs/plans/PLAN-003.md)
- [x] [PLAN-004：实现 M3 Codex Adapter 与真实执行门禁](docs/plans/PLAN-004.md)
- [x] [PLAN-005：实现 M4 Claude Adapter 与独立 Review](docs/plans/PLAN-005.md)
- [x] [PLAN-006：实现 M5 Reconcile、Gate 与 Daemon](docs/plans/PLAN-006.md)
- [x] [PLAN-007：实现 M6 Final Report、Benchmark 与 v0.1 发布验收](docs/plans/PLAN-007.md)
---

---
ID: OBJ-002
Objective: 使用 Cobra 全面替换当前 CLI 实现模式并提供现代化 Command 体验
Status: 已完成
Plans:
- [x] [PLAN-008：迁移并验收 Cobra CLI](docs/plans/PLAN-008.md)
---

---
ID: OBJ-003
Objective: 落实项目单实例与当前工作目录后台执行设计，移除 Git worktree 并闭环验收
Status: 正在处理
Plans:
- [x] [PLAN-009：统一项目身份与 Daemon 生命周期](docs/plans/PLAN-009.md)
- [x] [PLAN-011：改为当前工作目录执行并移除 Git worktree](docs/plans/PLAN-011.md)
- [ ] [PLAN-010：持久后台规划与跨项目恢复验收](docs/plans/PLAN-010.md)
---
