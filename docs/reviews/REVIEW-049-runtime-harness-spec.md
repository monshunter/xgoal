# REVIEW-049：OBJ-004 运行时 Harness 产品 Spec Review

## 审查对象

- Objective / Plan：`OBJ-004`，`PLAN-012` Item 1.1。
- 被审文件：根 `xgoal-product-spec-v0.1.md`，第 20 节及本次角色、Invocation、版本状态的相关调整。
- 首轮被审 SHA-256：`c6e6112f8de2b7106781496f6dd59ff4fda1455f0fb38ccd398f4efc2b6da4f8`。
- 复审被审 SHA-256：`ec20125519b139e15f682462b5b5283d57e2f26f4da13c0888b56f2dbd38c5ac`。
- 最终措辞复核 SHA-256：`2ae731a5fe44962b1f9195ac4da13f08c0fdb4d2e21ef741f2d250d9e39c30c6`。
- Git 基线：`a9b6192192c5bd99e4378bb8234e64f0a1fddf73`；分支：`feat/runtime-harness-closure`。
- 依据：当前用户五项议题及全面实施授权、[REVIEW-048](REVIEW-048-plans-runtime-harness.md)、根及 docs 指令、`autogo-spec-review`、现有产品合同和相应实现接入点。
- 独立 Reviewer 未编写被审 Spec；只写本 Review，不改 Spec、Plan、Progress、索引、实现或预存 `.gitignore`。

## Verdict

`PASS`

首轮结论为 `FAIL`，原因是下述生命周期 owner 与续作、受信基线更新、旧配置兼容边界冲突。作者修订后已按当前内容逐项复审，三项实质问题均已解决，可以进入 PLAN-012 1.2。保留首轮发现及修复结果供追溯。

五项需求及原分析中的优化建议已覆盖，新增 `AC-HR-001` 至 `AC-HR-018` 共 18 项、无重复身份，均保持未勾选。最终复核确认残留总括措辞及 AC-HR-003 的负向路径已同步，不留未解决 notes，不要求扩大 Plan 或新增流程制品。

## 发现

### F-001 / P1：初始规划和最终验收仍被绑定到尚不存在或不适用的身份与续作路径

**证据**：FR-011（约第 428 行）规定初始 Planner 提议 Contract，Kernel 校验后才冻结 Goal Revision；FR-021（第 449 行）却要求“所有角色都关联 Goal Revision”。第 20.2 节（第 976 行）将 Goal Revision、Tree、Packet、Schema 作为全部角色的恢复身份。第 20.5 节（第 998 行）将统一 Gate 决定与续作约束为 Gate/Work CAS 和启动新 Attempt，而 FR-021 已明确存在规划请求与最终验收 owner。`AC-HR-011` 也只验收“新 Attempt 消费实际决定”。

当前 `internal/orchestrator/planning.go` / `internal/planner/planner.go` 的初始规划输入是 Goal ID、原始请求、配置和 Planner Packet，并不存在一个可在调用前绑定的冻结 Goal Revision；这是初始规划的自然生命周期，不能靠伪造 Revision 修补。

**影响**：实现可能为初始规划伪造 Work/Revision，或只给普通 Work 实现可回答 Gate，导致 Planner 和最终 Acceptance 仍只有等待记录而无适用的续作操作。它也会使恢复身份验收无法确定哪一组字段是必须相等的。

**修订方向**：明确所有调用关联 Goal 和唯一持久 owner；初始 Planner 绑定规划请求及其 generation、冻结输入快照，Revision 产生后再建立可追溯关联。Work 与最终验收分别绑定其已有 owner、Revision 和相应快照。Gate 续作按 owner 恢复规划、创建 Work Attempt 或重新进入 final-validation，不能一律要求 Work。补充 `AC-HR-008/011` 对初始规划、普通 Work 与最终 Acceptance 的覆盖。`AC-FR-021` 的旧“每个 Invocation 均有 Work/Attempt”勾选应明确为旧基线范围，并指向新的替代合同，避免当前 AC 与 FR 相互矛盾。

### F-002 / P1：普通 replan 是否能更换受信基线存在冲突

**证据**：第 20.4 节（第 990 行）允许审阅提交新的配置/入口基线后“建立新 Goal 或显式重规划”；FR-011 明确现有目标的 replan 基于当前冻结 Revision 生成新图。P-011 与 FR-062 又要求 Evidence 绑定当前 Goal Revision、Config Hash、Final Tree。当前 `internal/control/service.go` 的 `replanGoal` 读取现有 GoalRevision，仅创建和激活 PlanRevision，也印证现有 replan 的产品语义。

**影响**：若用户按提示修改入口后执行普通 replan，新的入口身份没有可定义的可信更新 owner；要么仍然不能执行，要么实现为使恢复成功而悄悄覆盖冻结配置/Registry，使旧 Evidence 的身份依据失真。当前 `AC-HR-003` 要求新基线可运行，却没有区分两条不同恢复行为。

**修订方向**：最小方案是明确普通 replan 不更新受信基线，此类变更经审阅提交后创建新 Goal，旧 Goal 保留或取消。若确实需要保留 Goal，则必须明确一个创建新 Goal Revision 与配置/验收身份的显式操作及旧证据失效语义，不能复用只改 Work Graph 的普通 replan 名义。将对应用户路径写入 `AC-HR-003`，验证旧 Goal/旧定义无法借批准或普通 replan 使用新入口。

### F-003 / P2：旧配置“继续可用”需要排除原本被静默忽略或缺少新信任声明的配置

**证据**：第 20.2 节（第 974 行）要求无人值守交互权限、冲突 sandbox/tools 和不支持字段给出诊断且不能静默忽略；第 20.4 节（第 988 行）为 Make/npm/内联 shell 等入口要求显式受信文件声明。第 20.7 节（第 1014 行）却要求新增配置保持旧配置默认行为，`AC-HR-006` 又无条件要求“旧配置继续可用”。当前 `internal/config/config.go` 接受 `default`、`acceptEdits`、`bypassPermissions`、`plan`，而 `internal/orchestrator/execute.go` 实际固定 `dontAsk`；旧配置因此可能校验通过但从未按声明执行。`internal/projectinit/init.go` 生成的 Codex Profile 同时承担三角色，声明 `workspace-write`，也是需要定义请求上限与角色收窄关系的实际兼容样例。

**影响**：验收者无法区分“缺省字段兼容”“请求经有记录的角色上限收窄”和“原有矛盾配置必须迁移”。实现为满足无条件兼容可能继续忽略字段或放宽权限，另一实现则可能不必要地拒绝所有既有配置。

**修订方向**：限定兼容承诺为既有安全缺省配置和在新有效配置规则下有明确含义的组合；原本静默忽略、需要交互、不能强制执行的权限，以及缺必要受信入口声明的旧配置，允许并要求执行前诊断迁移。明确请求是权限上限还是精确值；若允许按角色收窄，实际值和理由必须可观察，不能伪装原值已生效。`AC-HR-006/017` 分别验证可继续配置及必须迁移配置，保留旧状态、日志和报告可读取。

## 需求覆盖与可测试性

| 目标 | 产品合同 | 验收 |
| --- | --- | --- |
| 最小角色、可选独立 Acceptance、确定性业务断言 | P-012、9.2、20.1 | AC-HR-001/002 |
| 环境准备、服务依赖/readiness、诊断、资源归属和清理 | 20.4 | AC-HR-004/005 |
| 验收入口及显式依赖保护、批准新基线路径 | 20.4 | AC-HR-003；需按 F-002 修订恢复路径 |
| 五个配置维度、统一 Profile、显式绑定、有效身份 | 20.2 | AC-HR-006/007/008；需按 F-001/F-003 澄清 |
| 目标项目 Harness 发现、兼容、加载证据与委派 owner | 20.3 | AC-HR-009 |
| 合法阻塞、用户决定、有限恢复、无进展止损 | 20.5 | AC-HR-010/011/012；需按 F-001 覆盖所有 owner |
| 实时上下文和日志、游标、截断、脱敏及控制循环隔离 | 20.6 | AC-HR-013/014 |
| 人类输出、wait 反馈、ID/版本便利、初始化说明 | 20.6 | AC-HR-014/015 |
| SQLite 单一状态、Git/文件分工、一致可读导出与历史兼容 | 20.7 | AC-HR-016/017；需按 F-003 澄清兼容边界 |
| 当前双 Provider、真实 CLI/daemon/服务、全仓门禁与文档闭环 | 20.8 | AC-HR-018 |

合同没有以新增 Agent 替代确定性验收，没有额外 Store、全局管理 Agent 或新的调度状态机。日志上限和稳定游标、重试总上限与进程退出证明、数据库一致快照与文件引用校验都对应实际失败模式。产品级 AC 可以独立验证正负行为；具体故障注入、Provider 参数组合和场景制品应在已安排的技术设计与 Scenario 中确定，无须把实现步骤写进产品 Spec。

尚未产生的技术 SPEC 第 35 节及 DESIGN-007 由 PLAN-012 1.2 负责，本次不以它们尚未完成为失败理由。所有新增 AC 未勾选，未把历史回归或本次 Spec Review 当成实现 Evidence。

## 下一路由与验证边界

复审修复确认：

| 发现 | 当前修订及验证 | 结论 |
| --- | --- | --- |
| F-001 | FR-021 明确初始 Planner 的 Goal ID、请求/配置、输入 Tree、generation 身份及后续冻结 Revision；20.5 改为 owner CAS，分别定义规划 generation、Work Attempt、最终 Tree 上新验收 Invocation；AC-HR-008/011 覆盖三类 owner；AC-FR-021 明确旧 Work 基线及替代增量。 | 实质冲突已解决。 |
| F-002 | 20.4 明确只有从审阅提交后的新基线创建新 Goal 才接纳新入口；普通 replan 只改工作图，不能重新信任入口，旧 Evidence 不因 Gate 批准复活。 | 已解决。 |
| F-003 | 20.7 限定旧缺省/有效安全配置兼容，旧冲突权限和缺必要信任声明入口需诊断迁移；AC-HR-006 明确运行前拒绝冲突与不支持字段，历史对象仍可读。 | 已解决。 |

最终措辞复核：FR-021 已使用“对应 owner 的不可变 Packet，Revision 尚未产生时使用规划请求身份”；20.2 恢复身份明确引用 FR-021；AC-HR-003 明确“旧 Goal 的 Gate/普通 replan 不接受新入口，仅新 Goal 接纳审阅后的新基线”。此前三条非阻塞 notes 均已消除。

下一路由为 PLAN-012 1.2 的技术合同与最小必要设计及独立 Design Review。Spec Review 只允许继续设计，不勾选任何交付验收，不改变 Progress。

本次未执行功能测试；审查 Evidence 是当前 Spec、实现接入点及规范之间的静态对照。上述代码事实仅用于定位已有 owner 和配置行为，不构成增量功能通过证明。
