# Reviews 工作区

## 目的

保存对 Spec、Design、Plan、代码或交付结果的可追溯审查发现与结论。

## 制品与命名

- Review 使用 `REVIEW-<编号>.md` 或能稳定指向被审对象的文件名。
- 必须记录被审制品 ID、revision、发现、证据和结论；结论使用 `PASS`、`PASS_WITH_NOTES`、`FAIL`、`BLOCKED`。
- 每个 Standard Plan 在第一条 Item 前完成 Plan Review，并在实现和定向验证后完成 Change Review；Review 只检查当前任务的正确性、风险与 Evidence，不检查 Harness 自身。

## 生命周期与归档

`FAIL` 返回对应 owner 进入 Reconcile，修复和验证后重新 Review，不因普通失败自动回滚。`BLOCKED` 进入 Investigation，或在缺少授权、凭证、权限和外部条件时进入 Waiting。保留历史发现及其处置结果。

## 负责的 Skills

`autogo-spec-review`、`autogo-design-review`、`autogo-plan-review`、`autogo-change-review`。

## INDEX 维护

Agent 在 Review 新建、更新或关闭后同步维护 `INDEX.md`。
