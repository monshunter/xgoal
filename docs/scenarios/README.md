# Scenarios 工作区

## 目的

保存真实 E2E 场景合同、前置条件、步骤、预期结果和当前证据引用。

## 制品与命名

- 场景文档使用 `SCN-<编号>.md` 或项目已有的稳定场景 ID，并链接对应 Spec AC。
- 每份 Scenario 说明角色与用户结果、环境/fixture/前置状态、可重复步骤、UI/API/后台预期、清理恢复、自动化入口和最新 Evidence 引用。
- 真实 UI Scenario 还要声明目标 URL 或应用、登录上下文、显式工具约束以及需要浏览器控制、外部浏览器上下文、桌面 UI 控制或组合能力；实际使用的能力和选择原因属于当前 Evidence。
- 日志、截图和录像存入项目制品目录；本文档只保存摘要与引用。

## 生命周期与归档

使用 `draft → ready → verified → retired/archive`，不得把 mock 或代码测试包装成真实 E2E。

## 负责的 Skills

`autogo-e2e-run`、`autogo-env-manage`。重复运行或跨会话交接有价值时维护稳定 Scenario；一次性局部验证可以直接从当前验收目标运行。显式工具约束不可静默替换，必需能力、权限、认证或环境不足时保持未完成并标记 `BLOCKED`。

## INDEX 维护

Agent 在场景合同或验证状态变化后同步维护 `INDEX.md`。
