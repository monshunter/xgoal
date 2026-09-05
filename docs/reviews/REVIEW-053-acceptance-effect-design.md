# REVIEW-053：Acceptance Packet 与 Effect 生命周期设计审查

## 审查对象

- Objective / Phase Item：`OBJ-004`， [PLAN-014](../plans/PLAN-014.md) 2.2。
- 被审设计：[DESIGN-007](../architecture/DESIGN-007-runtime-harness.md) 的 Acceptance Packet / Effect 新增段落；首轮全文 SHA-256：`b3acaeddc76fdc4a42d03075fd16157feae081952b3ed8a187b50d007268f8b0`。
- 最终复审设计全文 SHA-256：`709e9110970f7a13fa00750f4af179c27f163be126f61246335d1cb3d1fd1730`。
- 产品合同：根产品 SPEC 第 20 节；SHA-256：`aa142e0fc9cd0acc7f853745f7976710bd954d115b6fe7c45f84c13579fb0ef1`。技术 SPEC SHA-256：`9bf957663b6c6fc0fc24feed775a2b84c91588e7bd93ad3ee88935f414e4e10c`。
- Git 基线：`22d90cb0a8af60930cf04a56abe0f5704d8fcefa`，分支 `feat/runtime-harness-closure`。本审查不替代 PLAN-014 的最终 Change Review。
- 独立 Reviewer 使用 `autogo-design-review`；仅写本 Review 和 reviews 索引，不修改被审设计、实现、Plan、Progress 或预存 `.gitignore`。

## Verdict

`PASS`

首轮为 `FAIL`，作者已将两组事务约束及恢复规则补入设计，独立 Reviewer 读取最终段落并逐项确认，现结论为 `PASS`。复用现有 `effects` 和进程 Journal 的方向合理，不需要新增状态表、工作图节点或场景工作流引擎。Packet、物理 Owner、Claim 与确定性业务 Evidence 的区分符合原产品合同。下文保留发现、修订要求和实现验收锚点。

## 发现

### F-001 / P1：明确 Acceptance 专用的启动、观察与执行槽约束（已修订）

首轮设计仅概括为“以 Goal/Revision/归属 CAS 进入 EXECUTING”。现有 `internal/store/sqlite/effect.go` 的通用 `UpdateEffect` 只检查 Effect version 与状态转换，不检查 Goal 生命周期、Revision、Tree 或物理 owner；`processSlotAvailable` 则有相同 Attempt owner 的豁免。因此不能直接组合这两个既有 API 就认定 Acceptance 已具备所需门禁。

**修订要求**：以小型 Acceptance Store 操作复用既有事务与状态机，明确绑定 effect ID/version、invocation ID/request hash、Goal 当前版本及可执行状态、active Revision/config/最终 Tree、最后成功 Attempt 与 generation。在一个项目内最多存在一个非终态 Acceptance Effect；当前 owner 豁免只用于该链路中的场景命令，不能用于创建第二个 Acceptance。启动仍需核对未解决 Gate、其他执行/Promotion 与 checkout 归属。终态 SUCCEEDED 只证明调用结果已观测，不能跳过后续最终断言和 Completion Predicate。

**迟到观察边界**：pause/cancel/Revision 或现场漂移后，允许保存已停止进程的历史 Claim/退出事实，但不得据此恢复 Goal、消费 Gate、启动验证或覆盖较新 Invocation；既有终态 Effect 不复活。

**验收锚点**：两个相同 owner 的并发启动只有一个成功；重复请求幂等；旧 Effect version、旧 Invocation、旧 generation 和取消后的回调不能推进执行；未结束 Effect 阻止其他执行，当前环境的合法命令仍可完成清理。

**最终修订核对**：DESIGN-007 已要求专用 Store 事务入口，开始与有效观察核对完整调用/Goal/物理归属身份，限定同项目一个非终态 Acceptance Effect，并明确场景命令的 owner 豁免不授权第二次 Acceptance。迟到结果只作为历史记录，不恢复 Goal 或进入验证。既有配置、Gate、checkout 和进程屏障继续有效，不由此专用入口绕过。本项关闭。

### F-002 / P1：人工重放决定与新调用必须原子绑定（已修订）

首轮设计说明“人工决定后的新 Invocation 消费对应 final owner 决定”，但未定义消费时点与调用落盘的原子关系。先消费后创建会在崩溃时丢失一次性授权；先创建再消费则可能让同一批准发起多次具有外部副作用的调用。使用 Work retry 的旧批准或仅比较 Goal ID 都不足以证明本次重放获得授权。

**修订要求**：在创建/启动下一 Effect 的同一事务内核对并消费确切 final Gate，绑定前次 Invocation、Goal/Revision/config/Tree、Gate owner/version、批准状态、有效期和未消费状态；新请求保留该决定引用并将其纳入不可变 Packet。事务失败不消费，精确请求重试返回同一 Effect，不再次消费或执行。已拒绝、撤销、过期、已消费或属于另一条调用链的决定不得授权重放。

**恢复要求**：沿用既有 Effect 状态转换，进程身份未确认时保留屏障；已持久化的 blocked/failed Claim 原样保留，不能统一改写为 interrupted 后由 `replaySafe` 放行。只有缺少完整结果且已确认进程停止时才记录中断。`replaySafe=true` 仅授权符合原设计边界的恢复重放，不覆盖结构化阻塞/失败或未解决 Gate；暂停/取消时只回收与记录，Completed 不重新验收。后续调用使用新 Effect/Invocation，不复活旧终态。

**验收锚点**：在决定消费/新 Effect 提交前后分别注入崩溃；旧批准不能再次启动。覆盖结果已持久化但 Gate 尚未创建、Provider 退出但结果未落盘、未知进程、paused/cancelled 与已完成 Goal；重启后的 blocked/failed 不能因 replaySafe 自动消失。

**最终修订核对**：DESIGN-007 已明确新 Effect 与准确 final Gate 的消费在同一事务内，核对旧 Invocation、Revision/Tree、owner/version、有效期及 used；恢复遵循既有 Effect 转换，保留已落盘 blocked/failed，不把其降格为可自动重放的中断。终态不复活，暂停/取消只回收记录，Completed 只恢复报告文件。本项关闭。

## 复杂度与下一路由

保留现有 Effect 状态枚举和物理进程 owner，只增加 Acceptance 所需的事务校验与请求/观察 DTO；Gate 和业务 Evidence 继续由现有 owner 管理。无需新平台、通用调度抽象或额外人工审批。

F-001/F-002 均已关闭，进入 PLAN-014 2.2 的专用 Acceptance Effect 事务与 Provider 接入实施，再按上述负例及真实动态交互完成验证。无需调整 Objective/Plan 范围或增加人工审批。以上 Evidence 为当前设计、Spec、Store/进程/恢复接入点的只读检查；本次没有运行模型调用或功能验收，不预先宣告 Acceptance 已实现或整个 PLAN-014 已完成。
