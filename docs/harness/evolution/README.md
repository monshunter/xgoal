# Harness Evolution 工作区

## 目的

保存会话观察、重复摩擦证据和经过审批的 Harness 演进提案。

## 制品与命名

- Evolution 使用 `EVO-<编号>.md`，记录 `OBSERVATION`、`CANDIDATE` 或 `PROPOSAL`；应用结果保存在同一演进链。
- 单次普通问题不直接升级为规则；提案必须写明复杂度变化、验证和回滚。
- 没有可复用改进时不创建 Evolution 文档，也不要求记录空结论。

## 生命周期与归档

使用 `observation → candidate → proposed → approved/applied → superseded/archive`；治理核心变更遵守 Human Gate。

## 负责的 Skills

`autogo-session-review`、`autogo-harness-evolve`。

## INDEX 维护

Agent 在候选升级、审批、应用或回滚后同步维护 `INDEX.md`。
