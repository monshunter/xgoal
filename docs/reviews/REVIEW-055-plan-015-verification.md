# REVIEW-055：PLAN-015 验收资源与时长调整 Plan Review

## 审查对象

- Objective：`OBJ-004`；被审制品：[PLAN-015](../plans/PLAN-015.md)，重点为调整后的 Phase 3。
- Plan 全文 SHA-256：`bbf7ae45745462130d5e2fe8ff364bb86d69168e6322e58774d7ce012f8f0453`。
- Git 基线：`fbcb073f152167e58f5bfdc09a91b73076923beb`；分支 `feat/runtime-harness-closure`。
- 新约束：用户要求每个门禁用例在分钟级完成，不设计小时级测试任务；保留完整真实场景，减少无必要的重复和资源占用。
- 独立 Reviewer 使用 `autogo-plan-review`，读取根与 docs 指令、Progress、Plan、当前 Makefile、测试入口及现有运行日志。仅写本 Review，不修改 Plan、实现、Progress、索引或用户预存 `.gitignore`。

## Verdict

`PASS`

调整后的 Plan 可以进入 3.2 实施。资源和测试组织问题属于本 Objective 的验收收口范围；先优化资源与用例预算，再执行调整后的完整门禁，最后完成 Change Review 与提交，顺序合理。原功能与真实场景范围未缩减，当前未完成门禁仍保持未勾选。

此结论只批准 Plan 的执行顺序和范围，不代表测试分层已实现、完整发布门禁通过或 OBJ-004 已完成。

作者随后收敛的最小方向也符合本 Plan：默认综合门禁以完整 race 单轮承载全部普通测试与真实 fixture，保留独立 test 入口；20 次乱序仅选择短合同与准确 Store 用例，取消顶层重复聚合测试，保留独立校验和平台构建。不新增分类、runner 或构建缓存。完整单轮的 15 分钟包级保护与短重复集合的 2 分钟保护均需由实际计时确认，不等同于允许单一场景无限等待。

## 覆盖与粒度

| Item | 判断 |
| --- | --- |
| 3.1 已完成的真实验收 | 保留当前 CLI/daemon、服务、双 Provider、阻塞续作和观测 Evidence；不因门禁组织变化重复创建目标或抹去已验证结果。 |
| 3.2 资源、时长与重复分层 | 明确包含分钟级用例预算和完整真实场景覆盖，能够同时解决主机资源压力及全仓 E2E 重复二十遍的问题；未提前绑定新的 runner 平台或具体实现。 |
| 3.3 调整后的全仓门禁与 AC 对账 | 与 3.2 分开，避免仅凭脚本或 Makefile 改好就宣布验收完成；运行结果、未运行项及覆盖差异仍须据实核对。 |
| 3.4 最终 Review、提交与完成审计 | 保留原有质量门禁和 Git 收口，继续由全部 Plan 的实际完成汇总 Objective。 |

Plan 仍是一份目标、范围与 Phase Checklist，没有加入执行日志、第二份状态表或无关平台建设。3.2 是一个可验收的测试入口改进边界，3.3 是随后运行与对账的独立结果，粒度足以领取实施。

## 当前事实与实施建议

以下建议解释现有 3.2/3.3，不要求增加流程制品或另建测试框架。

1. **先去掉重复入口，再选择必要重复。** 当前 `verify-m6` 在全仓 test/shuffle/race 之外，另调用 m2 failure matrix、m3/m4 contract、m5 safety 和 m6 release 的重叠 `go test`。可保留这些历史目标供定向使用，默认综合门禁去重；其中 benchmark 配置校验、CLI smoke 和不同平台编译仍有独立价值，不能随重复测试一同删除。单轮应显式区分新执行与缓存结果。
2. **完整场景继续真实执行，短合同承担高次数扰动。** 20 次乱序可优先限定为 domain/kernel/protocol/evidence/reconcile/policy/scope/canonical 等短合同，以及 SQLite 的 CAS、单 Lease、Effect 原子性、Gate 一次消费、迟到写入和 Invocation 观测身份等具体测试。完整 CLI、HTTP 服务、崩溃、取消和模型 opt-in 场景保留原断言，不用 mock 替代。重复集合应依据测试目的和实际计时确定，不能仅凭名称随意排除；race 与普通运行的重复需有相应并发风险依据。
3. **复用现有接点，避免扩大测试治理。** 当前没有 `testing.Short()`；唯一 CLI `TestMain` 承担真实子进程进入生产 `Run` 的职责，不能整体跳过。CLI 的 `compileCurrentDirectoryCLI`、Orchestrator 的 `runCurrentDirectoryFixture` 和 App 的真实 Unix API 场景已有集中接点及秒/分钟级等待。可据需要复用测试二进制或拆分执行入口；不因优化引入另一套运行状态机。
4. **分钟级预算约束实际用例。** 单纯把包级 timeout 改成 6h 不满足新约束。若长包按顶层用例执行，必须准确运行其全部子用例、保留失败退出码、上下文取消与归属进程清理，并核对分片集合等于原有覆盖。用例超时或未结束不能通过忽略结果、减少断言或跳过场景变成 PASS。短重复集合本身也应保持分钟级预算。
5. **资源限制与覆盖分别验收。** `.NOTPARALLEL`、默认 `GOMAXPROCS=2` 和 Go 包并行上限可约束已确认的测试热点，仍需保留显式覆盖及其他 GOFLAGS。它们不是整个子进程树的 CPU 配额。验证时既要观察实际资源，也要证明真实场景、并发合同和平台编译仍进入门禁。

## Evidence 与下一路由

只读日志 `/private/tmp/xgoal-final-gate.jb9dxv/verify-m6.log` 显示上一轮普通包测试完成：CLI 759.048s、Orchestrator 265.331s、Control 18.637s；随后进入全仓 `-shuffle=on -count=20 -timeout=6h`。这说明包级累计与重复组合需要调整，不能推断任一具体场景本身运行了数小时，也不能把被停止的 shuffle 计为完整门禁通过。代码检查确认目前没有 short 分层，历史聚合目标存在重复调用；本次未运行测试或构建。

下一路由：按 `autogo-change-implement` / 必要定向验证完成 PLAN-015 3.2，再执行 3.3 的调整后门禁和 AC 对账；最终 Change Review 必须覆盖本轮验收入口改动及实际覆盖证据。失败返回对应测试或实现 owner 修复，不恢复小时级超时方案，不要求额外人工批准。Review 索引由主 Agent 在已授权文档对账时同步。
