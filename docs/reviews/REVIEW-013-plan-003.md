# REVIEW-013：PLAN-003 初始 Plan Review

## 审查对象

- Plan：`PLAN-003`
- Objective：`OBJ-001`
- Revision：初始版本
- 规范：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)

## Verdict

`PASS`

## 覆盖与顺序

- Phase 1 先冻结外部制品协议并审查跨 Git/文件/进程/DB 边界的最小设计，避免捕获、重放与 Evidence 各自形成真理源。
- Phase 2 按 Workspace → Patch Bundle → Scope/Replay 的数据依赖顺序建立不信任 Agent 输出的边界。
- Phase 3 在可读回的干净 Tree 上建立 Local Environment、受信 Validator/Receipt 和 Evidence Staleness，顺序足以支撑 Promotion。
- Phase 4 最后串行 Promotion，并以 SPEC 明示的 Agent Commit、逃逸、冲突、过期与崩溃故障覆盖 M2 门禁。

## 粒度与边界

- 每个 Item 都对应一个可单独实现、测试和勾选的能力结果；Plan 未复制实现步骤、状态日志或 SPEC 正文。
- M3/M4 的真实 Agent/Reviewer、M5 的 Daemon/Reconcile/Policy、M6 的 Final Report/Benchmark 均明确排除，没有扩大本 Plan。
- Git 分支、worktree、命令进程和 Commit 都是可恢复外部 Effect；设计与实现必须延续 M1 的 Request/Read Back 纪律。

## 风险控制

- Git Common Dir、`.git`、submodule/gitlink、非普通文件、绝对/逃逸 Symlink 和大小写/NFC 碰撞必须 Fail Closed。
- Agent Commit/Index 不能成为 Promotion 输入；Patch Bundle 只从冻结 Base Tree 与文件系统事实计算。
- Validator Definition 和 Evidence 必须绑定 Goal Revision、Config、Definition 与实际 Tree；日志不得泄漏 Secret。
- Promotion 只写私有 Integration Branch，不写用户 checkout、不 push、不触及生产。

## 下一路由

允许从 Phase 1 Item 1.1 开始；先调用协议/Spec 与系统设计能力，再进入 TDD 和实现。
