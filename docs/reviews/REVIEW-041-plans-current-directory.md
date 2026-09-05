# REVIEW-041：PLAN-009 / PLAN-011 / PLAN-010 当前目录执行 Plan Review

## 审查对象

- Objective：`OBJ-003`，落实项目单实例与当前工作目录后台执行设计，移除 Git worktree 并闭环验收。
- 执行顺序：`PLAN-009` → `PLAN-011` → `PLAN-010`。
- 代码基线：`24960390d71237d6266e2f995b4e3df0b6719a78`；分支：`fix/project-daemon-isolation`。
- 审查方式：独立 Reviewer 未编写三份 Plan，只读核对当前文件、用户授权、相关设计及工作区实现；仅写入此 Review 和 Review 索引。
- 当前授权包含设计落地、实现与闭环验收；新增澄清要求 xgoal 在当前 Git 工作目录原地执行，不为 xgoal 创建 Git worktree。当前 Plans 将其明确为主工作目录入口、项目单实例和全部实现/验证/审查链路不创建或删除 worktree。

被审文件 SHA-256：

| 文件 | SHA-256 |
| --- | --- |
| `PROGRESS.md` | `eb5849611c1156833b48aacd1be576b3279c79ad59222f12274a64d250ce760a` |
| `PLAN-009.md` | `9f7d813d6c197cc88be6048256a2043bb797417a6b8b9ea207b3c3218f9ee7cb` |
| `PLAN-011.md` | `2f9cebf2af5437b634a6ae28d4b990aa835188d77f1188577193a0cb9c1cc32d` |
| `PLAN-010.md` | `791e82c86886fa690a9b446d2d5eba5d5d83ade6c90e167fb65fe71e01563841` |

## Verdict

`PASS`

三份 Plan 覆盖完整目标，边界和依赖顺序可以支持继续执行。本结论替代 [REVIEW-038](REVIEW-038-plans-009-010.md) 对这些 Plan 当前版本的结论；旧 Review 的 hash、linked worktree 共享入口及两份 Plan 执行顺序不能用于当前范围。Plan PASS 不代表当前 Spec/Design、未完成实现或运行验收已通过。

## 目标覆盖与 Evidence

| 当前目标或真实风险 | 当前 Plan 覆盖 | 判断 |
| --- | --- | --- |
| 同一仓库单实例，独立项目可同时运行 | PLAN-009 2.1–2.3、4.1–4.2；PLAN-010 3.1 | 覆盖项目解析、身份绑定、跨状态路径排他、双项目真实场景；没有退化为每状态目录一把锁。 |
| 当前工作目录执行，不创建任何 Git worktree | PLAN-011 目标、范围、2.1–2.3、3.1–3.4、4.2 | 明确覆盖内部 Attempt、Validator、Reviewer 和 Promotion，且要求实际 worktree 清单不变；不是只拒绝 linked 入口或替换命名。 |
| 保护用户 HEAD、分支、index 和预存 dirty | PLAN-011 范围、1.1、2.1–2.2、3.2–3.3、4.1 | 干净基线准入、独立临时 index、完整性核对及现场保留进入实现范围；没有自动 stash、reset、切分支或覆盖历史的授权。 |
| 最终结果位于当前目录且绑定当前 Tree | PLAN-011 3.1–3.2、4.2；PLAN-010 3.1–3.3 | 现地验证/审查前后核对、私有 Commit/Ref、最终 Evidence/Report 均有对应任务，不以私有 ref 更新替代用户目录交付。 |
| daemon 与 CLI 生命周期解耦 | PLAN-009 2.3、3.1；PLAN-010 1.1–1.3、2.1–2.2 | 同时覆盖可靠启动停止、规划事务性接受、串行执行、原子发布和 wait/watch 仅观察。 |
| 失败、中断、旧状态与恢复 | PLAN-009 2.2、4.1；PLAN-011 2.3、3.3、4.1；PLAN-010 1.3、2.1–2.2、3.2 | 旧历史保留可读、不安全旧运行拒绝、现场保留、幂等请求与规划提交窗口均未遗漏。 |
| 长期设计一致性与完整闭环 | 各 Plan 的合同/设计、文档对齐、验证及收口 Phase；PLAN-010 3.3–3.4 | 先形成经过审查的行为和设计，再实施并独立 Change Review；最终包括全量门禁、平台构建和逐项目标审计。 |

审查读取的当前实现进一步说明拆分必要性：

- `internal/workspace/manager.go` 的 `Create` 仍调用 `AddDetachedWorktree`；`internal/orchestrator/execute.go` 与 `finalize.go` 分别创建 Attempt、Validation 和最终验证工作区。因此 PLAN-011 的完整链路替换是独立必要边界，不能由 PLAN-009 的入口限制替代。
- `internal/gitrepo/tree.go` 的 `IndexAndWriteTree` 使用工作区 index 执行 `git add --all`；`internal/store/sqlite/migrations/0002_git_validation.sql` 的工作区 `path` 有唯一约束，`artifact.go` 还验证固定运行目录布局。PLAN-011 2.2–2.3 对临时 index、制品布局和历史兼容的安排与这些真实约束对应。
- `internal/control/service.go` 的自然语言 Planner 当前仍在创建请求内执行，`internal/api/api.go` 分别登记和完成幂等请求，`internal/store/sqlite/orchestration.go` 不扫描未冻结 DRAFT。PLAN-010 保留这些后台语义修复，不因新增原地执行范围而缩减原目标。

## 顺序、粒度与最小充分性

- PLAN-009 建立项目身份和 daemon 所有权，PLAN-011 随后改变被该 owner 管理的文件系统与 Git 验收链路，PLAN-010 最后统一持久规划、全部执行者生命周期并验收完整目标。该顺序避免先把 Planner/恢复接到随后会被替换的工作区模型上。Plan 编号不连续排序不影响依赖，Progress 已明确真实顺序。
- 三个边界可分别验证和提交，放在同一 Objective 保留了当前用户目标的连续性。PLAN-009 没有宣称完成内部 worktree 移除，PLAN-011 没有宣称完成全部后台恢复，PLAN-010 承担最终完整审计，收口关系明确。
- 每份 Plan 仍为单文件目标、范围和 Phase Checklist；每个 Phase 有边界清楚的目标，Item 表达可验收结果，未写入文件清单、命令日志、第二套状态或独立 Evidence 台账。并列的解析场景、文件变化类型和进程角色属于各自同一不变量，不需要机械拆为更小的表格任务。
- 不引入全局调度中心、强隔离容器、自动系统服务安装、计费统计、远端发布或生产操作。当前工作目录的失败策略允许保留现场并建立可操作等待，不要求另建自动 reset/restore 引擎；详细恢复合同由 PLAN-011 1.1–1.2 决定。
- PLAN-010 的“Provider 进程归属和有界串行执行”应按关联设计覆盖实际 Provider 调用，包括 active probe；原地 Validator/服务的启动、退出与清理由 PLAN-011 的执行链路和 PLAN-010 的统一资源回收共同落实。该说明是现有范围的解析，不建立额外 Goal 或 Plan。

## 下一路由与边界

允许依次继续 PLAN-009、PLAN-011、PLAN-010。由于用户范围已经改变，先完成 PLAN-009 1.1–1.2 对当前主目录/linked 入口合同的更新与复审；旧 Spec/Design Review 不能替代其修订版审查。PLAN-011 第一 Phase 需明确现地快照、dirty 准入、当前目录结果、失败等待、旧工作区历史保留和 Evidence 失效条件，并通过 Spec/Design Review 后再实现该链路。

恢复入口必须能实际从等待继续或明确取消，不得只生成无对应操作的 Gate；这属于已有 PLAN-011 3.3、PLAN-010 1.3/2.2 的验收要求。过时规范、历史工作区和旧 Evidence 按版本保留替代关系，不把旧记录重写为原地执行证据。

本次未运行功能测试、未修改被审 Plan/Progress 的内容或勾选。审查 Evidence 为上述当前文件读取、SHA-256、既有实现接入点与授权范围对照。后续实质修改目标、范围、Phase、顺序或验收覆盖时须再次 Plan Review。
