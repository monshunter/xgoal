# REVIEW-051：PLAN-012 受信验收与恢复边界 Change Review

## 审查对象与 revision

- Objective / Plan：OBJ-004 / PLAN-012，统一 Spec/Design、受信入口及依赖、合法 Agent outcomes、有限自动恢复和 Work 决定后的续作。
- 独立 Reviewer 使用 `autogo-change-review`；仅写本 Review，不改代码、Plan、Progress、索引或预存 `.gitignore`。
- Git 基线：`a9b6192192c5bd99e4378bb8234e64f0a1fddf73`；分支 `feat/runtime-harness-closure`。
- 初轮被审输入共 37 个文件，内容清单 SHA-256：`80978436d31fb57fd6dc15c27de4b219d2223ca2cb7a3e4ae00fcba9f7c4bce7`。清单算法：取相对 HEAD 的 tracked Diff 与 Git 未忽略 untracked 文件，保留 `internal/**`、README、example、根产品/技术 SPEC、DESIGN-007 和 PLAN-012，按路径排序；每项为 `path + NUL + SHA256(file bytes) + LF`，再对 UTF-8 清单求 SHA-256。排除 Progress、索引、其他 Plan/Review 与 `.gitignore`。

初轮关键文件 SHA-256：

| 文件 | SHA-256 |
| --- | --- |
| internal/validator/trust.go | `dcb06c9aed51ebdd51e8e6ff4a6246763a2398cc0bae95a03a0eb0fd38b16132` |
| internal/store/sqlite/gate_boundary.go | `a1b17cbe9f04464e0f2da6d4226bdaac5176b02e15a8f566228998b3b41bb56f` |
| internal/store/sqlite/migrations/0011_agent_outcomes.sql | `516a16e33c7b153965100c78f7f8cf002928208e56fa6d74594792e5142460a2` |
| internal/store/sqlite/planning_control.go | `9713518aade34901db92c0bd2dc253b6e818babadd47fcdfcd38039ad7c0ee75` |
| internal/store/sqlite/artifact.go | `aa4aaa53bd668d6501169ded44540ad2d7a163c70ebb06069185f9d239d83751` |

最终复审输入共 42 个文件，按同一算法计算的内容清单 SHA-256：`9ff449c01b21d63d48300951f37567239b948ed345b7129634b406a09577336b`。关键最终文件：

| 文件 | 最终 SHA-256 |
| --- | --- |
| internal/validator/trust.go | `dcb06c9aed51ebdd51e8e6ff4a6246763a2398cc0bae95a03a0eb0fd38b16132` |
| internal/store/sqlite/gate_boundary.go | `a1b17cbe9f04464e0f2da6d4226bdaac5176b02e15a8f566228998b3b41bb56f` |
| internal/store/sqlite/migrations/0011_agent_outcomes.sql | `94cd96ae3e15f0d4bd6580321a511d0cff4ee18f2e6cadf05f0ed190cc38d3a3` |
| internal/store/sqlite/planning_control.go | `f1cd615e6e1488a062b26d436137e9e4ba50c8f7fffc851abbba882c6532f1c9` |
| internal/store/sqlite/artifact.go | `edf6b4be51485ebbcc43deda56b64e1a4f5c6e3607241e2b45acdb97e9a60264` |
| docs/architecture/DESIGN-007-runtime-harness.md | `9aa9b088151ccefbac985cb69f1269295a28919963ca1fd080d78852f3fdfe6d` |
| xgoal-product-spec-v0.1.md | `9c5773b664819fad288eb6e366749f4572ceac854e7b2b68bb6b26b9a45b2cdd` |
| xgoal-technical-spec-v0.1.md | `e41221b6e0d8bebd70a97d5f15c149079708587072f46e8cccb91d000c05a5d2` |

## Verdict

`PASS`。初轮为 FAIL，四组发现均已完成修补、独立代码复核及定向回归，当前没有 PLAN-012 的剩余阻断项。下文保留原始问题与修复结论。此次 PASS 只允许 PLAN-012 进入收口，不把 PLAN-013–015 尚未实现的 Profile/Harness、动态 Acceptance、日志和导出能力判为本 Plan 缺口，也不表示 OBJ-004 完成。

## 发现与修复复核

### F-001 / P1：命令例外必须保持受信入口闭包（已解决）

首轮 `bindTrustedFiles` 仅识别若干精确解释器名称，其余命令缺省放行。`env sh scripts/check.sh`、版本解释器或自定义 runner 可以无 `trustedFiles` 加载仓库控制脚本，使该文件不进入 Definition 身份和 ProtectedPaths。作者已改为未知命令必须显式声明，并补包装器的旧 Registry / 篡改候选 Tree / 新基线用例。

完整 Review 又发现 `builtinAssertion` 的 Go 例外允许 `go vet -vettool=./scripts/custom-vet`，只排除 `-exec/-toolexec`。本机 `go help vet` 确认 `-vettool` 用于选择自定义分析器，属于必须声明的控制入口。最终代码加入 `-vettool` 声明要求，等号与分离参数两种负例均由 Reviewer 实际运行通过。普通 Go 测试源码仍属于候选内容，无需全部冻结。

### F-002 / P1：统一 Gate 屏障需要迁移已解决的历史规划诊断（已解决）

`gateBlocksExecution` 将 `REVOKED + required=1` 作为阻断。旧 `clearPlanningGatesTx` / `recoverReadyPlanningTx` 曾这样保存已由 Kernel 解决的规划诊断，升级后会把历史事实重新变成运行障碍。仅修改未来写入的 `required=0` 不足以兼容旧库。

第一次迁移修补只按 `owner=planning` 退休旧 REVOKED Gate，范围过宽：用户对 planning Gate 执行 ALLOW 后再人工 Revoke 也有相同 owner，不能因此解除人工撤销。最终 SQL 仅退休有 Kernel 解决事件或专属恢复原因、且未被人工决定的旧 REVOKED 诊断；APPROVED 诊断需有有效期内发布或下一 generation 的 Kernel 事件证明。`TestPlanningGateMigrationRequiresKernelResolutionProof` 用 v10 真实写入形态验证完整历史字段/事件不变，已解决 Gate 不阻断升级后调度，尚未解决批准和人工 planning/permission 撤销继续阻断，Reviewer 复跑通过。

### F-003 / P1：当前规划诊断退休要处理批准、到期与人工拒绝（已解决）

`clearPlanningGatesTx` 原先只处理 OPEN。用户先 ALLOW 再进入合法新 generation / 发布时，旧 Gate 留下 `APPROVED + used=0 + required=1`，到期后会被新统一谓词重新阻断。

最终修补在同一发布/重试事务中退休已解决的有效 APPROVED 诊断并保留决定；DENIED/REVOKED/EXPIRED 及按当前 clock 判断的自然到期、未消费 APPROVED 均拒绝。`TestPlanningPublicationRetiresOnlyValidDecisions` 实际走自动发布，覆盖允许、拒绝、撤销和仅推进时间的自然到期；允许后未来到期不追溯阻断，拒绝时事务回滚且原 Gate 仍 required。Reviewer 复跑通过，并核对发布和新 generation 使用同一事务 helper。

### F-004 / P1：新 Definition 身份会与旧注册唯一键冲突（已解决）

新增 `TrustedFiles` 改变直接和解释器 Definition hash，但同一配置的 Config hash 与 Git base commit 可以不变。`RecordValidatorRegistry` 在 `internal/store/sqlite/artifact.go` 以 `(config_hash, base_commit, validator_id)` 读取注册；旧 registration 的 DefinitionHash 不同即返回普通 `ErrIdempotencyConflict`。旧库升级后重新验收、甚至从同一干净基线创建新 Goal 都可能遇到该冲突。

最终选择最小迁移诊断方案：Store 识别旧定义缺少新增绑定的同键冲突，返回明确 `ErrTrustBindingMigrationRequired`，事务不改写旧注册或证据；Work 与最终验收调用点均转为可操作的 Waiting Gate。README 和设计说明先保留旧 Goal/现场，在配置明确受信文件、审阅提交新基线后创建新 Goal。

`TestLegacyValidatorBindingRequiresReviewedConfiguration` 的直接与解释器两组由 Reviewer 实际运行通过：v10 注册升级后历史 Definition/注册保持可读且原值不变，旧键获得明确迁移诊断；按提示提交显式声明后，新基线成功注册并执行 PASSED Receipt，Definition 身份不同。`TestTrustBindingMigrationWaitsWithActionableGoalGate` 同时验证 Work/Final 的 Gate owner、action、Waiting 与可执行提示，复跑通过。

## 已核对的系统关系

- Registry 统一拥有全部 Validator 的 ProtectedPaths；Patch 新旧路径、失败现场观察和独立 Runner 使用一致边界。直接/解释器 CWD、regular mode、symlink 拒绝及执行前后 hash 检查已有实现和回归；Gate 批准后实际 `RetryCheckoutWork` 仍拒绝篡改现场。
- 合法 `blocked`、`failed` 分别落入专门 FailureClass，不再成为协议错误；类型化结果在 Gate 中保留摘要、blockers、建议和 Invocation 引用。两种 Provider 的结果在传入此路径前已经脱敏。
- 自动恢复缺省关闭；Kernel 路径先持久化当前失败，再在 Store 事务中核对 Goal、active Plan、Work version、配置、最新失败、次数、现场及执行闲置。`executionIdle` 包含 INTENT/REGISTERED/UNKNOWN、worker、Lease 和未完成 Promotion；失败事务不消耗次数。
- 自动次数按 Work 持久化；换错误文本/Fingerprint 不重置。重复相同失败/策略无进展转 Diagnose；blocked、信任/范围/环境依赖/外部编辑及未知进程不自动放行。文件系统与 SQLite 不能原子锁定，执行前的再次 Snapshot 仍是必要屏障。
- Work 用户回答只消费最新失败 Attempt 对应的续作 Gate，Packet 包含消费后的版本与回答；新一次 blocked 有新 Gate，旧回答不直接投影为新问题的答案。权限 Gate 的 action/scope 消费者不由该回答路径替换。
- migration0011 复制 failure/reconcile 父子关系后按 child/parent 顺序替换，并保留历史 BUDGET_EXHAUSTED / WAIT_BUDGET 枚举，不恢复旧运行功能。新可选 Packet/Config 字段使用 `omitempty`；新信任身份的兼容问题由 F-004 单独处理。
- 没有增加第四类必需 Work、第二个可写状态源或新的重试状态机；README、example、产品/技术 SPEC 与设计已对账最终配置和迁移行为，未把当前 fixture 验证标为真实 Provider 或整个 Objective 验收完成。

## 当前验证 Evidence

Reviewer 实际运行：

- `go test ./internal/config ./internal/protocol ./internal/validator -count=1`：PASS，分别 0.283s / 0.440s / 46.700s；运行点早于最后的 `-vettool` 修补。
- `go test ./internal/store/sqlite -run 'TestAgentOutcomeMigrationPreservesFailureDecisionHistory|TestAutomaticRetryRejectsUnsafeOrUnapprovedScenes|TestRequiredGateDecisionAndExpiryFenceSchedulingAndCompletion' -count=1`：PASS，1.408s；新增规划迁移修补的专门回归尚待最终读回。
- 分段审查实际运行过合法 outcomes、Validator/Reviewer/受信源修改的 Orchestrator 链路，PASS 55.861s；该历史局部结果不替代完整最终 Diff 的验证。
- `git diff --check`：当前读回无输出。

最终 Reviewer 实际运行：

- `go test ./internal/store/sqlite ./internal/validator ./internal/orchestrator -run 'TestPlanningGateMigrationRequiresKernelResolutionProof|TestPlanningPublicationRetiresOnlyValidDecisions|TestLegacyValidatorBindingRequiresReviewedConfiguration|TestRegistryRejectsUndeclaredControlAndUnsafeFiles|Test.*Trust.*Migration' -count=1`：PASS，SQLite 5.052s、Validator 2.665s、Orchestrator 0.760s。
- 最终 `git diff --check`：PASS；本 Review 新文件 whitespace 检查无输出。

作者运行并在产品 SPEC 20.9 保存的结果：跨重启 blocked 回答、新 Attempt 实际读取 Packet、两个 Work、Review、Final Report 链路 PASS 44.807s；全仓 `go test ./... -count=1` PASS，包含 CLI 159.364s、Orchestrator 276.276s、SQLite 16.597s。全仓运行后的小范围修补由上述最终定向回归覆盖；作者还报告 `make fmt-check`、三个变更包 `go vet` 通过。这里明确区分作者执行和 Reviewer 复跑，不把 fixture Provider 称为真实模型调用。

## 下一路由

PLAN-012 可进入 `autogo-change-close`，由主 Agent 汇总当前 Evidence、完成文档/索引和 Git 对账后关闭并提交。Plan/Progress 的完成勾选不改变本次实现结论；若后续检查发现新失败，返回对应 owner 修复并重审。随后继续 OBJ-004 的 PLAN-013，真实双 Provider、动态服务 Acceptance、观测/导出与整体 AC 审计按后续 Plan 实施，本次不提前关闭 Objective。
