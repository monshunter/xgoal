# Journal 工作区

## 目的

在确有跨会话恢复价值时保存关键决定、未完成项和恢复上下文。

## 制品与命名

- Journal 默认按日期或稳定工作单元命名，只引用真实存在的 Objective、Plan、Spec 或 Bug；不回填 Commit hash。
- 常见记录是跨会话 `handoff`、需要长期保留的 `cancel`，或用户明确要求的工作摘要；普通 Commit 不要求 Journal。
- 只记录可复核事实，不复制大段日志或伪造未运行的验证。

## 生命周期与归档

Journal 为追加式历史记录；更正时保留原结论及修订原因。

## 负责的 Skills

`autogo-work-journal` 是唯一写 owner；`autogo-work-continue` 在 Journal 存在时读取恢复上下文。

## INDEX 维护

Agent 在新增或更正 Journal 后同步维护 `INDEX.md`。
