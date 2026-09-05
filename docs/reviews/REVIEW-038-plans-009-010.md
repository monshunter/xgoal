# REVIEW-038：PLAN-009 / PLAN-010 项目隔离与后台执行 Plan Review

## 审查对象

- Objective：`OBJ-003`，落实项目隔离与后台执行设计，优化 xgoal 并闭环验收。
- Plan：`PLAN-009`、`PLAN-010`，初始版本，全部 Item 尚未执行。
- 代码基线：`24960390d71237d6266e2f995b4e3df0b6719a78`。
- 分支：`fix/project-daemon-isolation`。
- `PLAN-009` 内容 SHA-256：`b081b653db9122a29dfa275bcd21bc2abad5601a0911c183740d921c53a6bdcc`。
- `PLAN-010` 内容 SHA-256：`83a82a0c0d3c14e5099886832bb0dcf54b106f67aef6d9e14d39f5995317ead4`。
- 授权：将前一轮隔离与 daemon 审查的发现落实为设计、实现优化及闭环验收；不包含对外发布、生产操作或全局服务安装。

## Verdict

`PASS`

两份 Plan 覆盖当前目标，边界和依赖顺序足以支持执行；本结论只通过计划，不代表设计、实现或验收通过。

## 审查结论与证据

- `PLAN-009` 先审查可验收合同和统一设计，再实现身份、所有权、生命周期与操作入口；`PLAN-010` 明确依赖前者，处理持久规划与恢复，最后验收完整目标。该分解把项目所有权和后台任务持久性两个可分别验证的边界分开，没有以一次包罗全部实现的 Item 替代工程工作。
- 每个 Phase 聚焦一个结果，每个 Item 对应可领取和验证的行为边界。项目解析的多种入口属于同一个 Resolver 合同；状态绑定和跨目录排他属于同一个所有权不变量；多种 Provider 的进程回收属于统一的执行生命周期。列举这些场景没有退化成文件级操作步骤。
- 计划分别覆盖了项目身份误配、同仓库多 daemon、锁晚于数据库迁移、未等待执行退出、客户端取消规划、幂等请求悬挂、DRAFT 恢复遗漏、配置覆盖差异、长 socket 路径以及文档模型漂移。现有代码仍可直接看到 `app.Serve` 先调用 `sqlite.Open` 再进入持锁的 `daemon.Server.Serve`、`engineRecovery.Recover` 直接启动 goroutine、`api.serveWrite` 用请求 context 分阶段登记和完成幂等请求；因此计划针对的是当前代码的真实路径。
- 旧状态兼容、迁移、失败恢复和后台接受语义已进入合同与实现范围；没有只修复新建项目而省略既有状态。`PLAN-010` 同时列入原子发布和提交窗口验收，能够覆盖数据库与外部进程无法由单一事务共同提交的风险。
- 验证范围包含双项目完整 Goal、linked worktree、错误身份、状态迁移、重复所有权、断连、暂停取消、daemon 中断、进程回收及最终 Evidence/Report。它明确要求真实 CLI/daemon 场景、相关回归、全量门禁与独立 Change Review，强度与用户“闭环验收”的目标一致。
- 两份 Plan 均保持目标、范围、Phase Checklist 的职责，没有执行日志、Evidence 台账或第二套状态表；根 Progress 只汇总 Objective 与 Plan。规范和设计链接处于相应 Phase，长期合同由既有产品/技术 SPEC 与 `DESIGN-004` 持有。
- 每项目 daemon、SQLite 和 L0 架构继续作为最小实现边界。未把强隔离、全局配额调度或系统服务安装作为本次完成前提；资源共享限制由文档对齐 Item 说明，符合前一轮建议的条件性改进范围。

## 下一路由

允许领取 `PLAN-009` Item 1.1，进入 Spec 更新与独立 Spec Review；随后完成 Item 1.2 的设计与 Design Review。合同审查应给出项目身份、旧状态处置、错误连接拒绝、readiness 与停止顺序、持久接受及恢复的明确验收行为，再进入实现。

`PLAN-010` 的初始 Plan Review 已通过，其实现仍须遵守 `PLAN-009` 所声明的依赖，并以通过审查后的合同与设计为输入。若合同或设计结果实质改变任一 Plan 的目标、范围、Phase、顺序或验收覆盖，应先更新该 Plan 并重新 Plan Review。

本次仅只读审查被审文件和相关代码、写入此 Review；未执行功能测试，未修改 Plan 勾选或 Progress。Review 索引由主 Agent 在文档对账时同步。
