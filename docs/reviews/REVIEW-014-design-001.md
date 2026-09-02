# REVIEW-014：DESIGN-001 M2 Git、环境与验证闭环

## 审查对象

- Design：`DESIGN-001-m2-git-environment-validation`
- Plan：`PLAN-003` Item 1.2
- Revision：初始版本
- 规范：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)

## Verdict

`PASS_WITH_NOTES`

## 结论

- 设计把 Git、运行文件与 SQLite 的事实 owner 分开，并用 Effect/Read Back 连接外部副作用，没有引入第二套运行状态。
- Patch 真相来自冻结 Base Tree 与文件系统，Agent Commit/index 仅是被观测扰动；符合 FR-030/060。
- detached validation worktree、严格 before 校验、受信 Registry、Receipt/Evidence Hash 绑定和 ref old-value CAS 共同覆盖 FR-040/041/061 的关键失败模型。
- 使用系统 Git CLI、标准文件能力和现有 SQLite/Protocol，未引入 go-git、容器或消息队列，复杂度与 v0.1 L0 边界相称。

## Notes

- M2 的进程内 Promotion Lock 不能防止两个 xgoal 进程同时写；实现仍必须依赖 Git ref CAS 和 SQLite 事务 Fail Closed，跨进程单写者锁在 M5 前不得宣称成立。
- 系统 Git 可能执行受仓库配置影响的 filter/hook。实现必须对 xgoal 自建 Commit 禁用 hooks，并在捕获/重放路径避免把 Agent 配置或 shell 字符串作为命令输入；无法禁用的受信仓库行为要在 Environment/Receipt 中披露。
- 大小写折叠采用确定性 Unicode 规则用于碰撞拒绝，不应被描述为精确模拟所有文件系统；保守拒绝可接受。

## 下一路由

Notes 不改变目标、安全、验收或恢复路径；允许进入 Workspace/Patch/Scope TDD 实现。
