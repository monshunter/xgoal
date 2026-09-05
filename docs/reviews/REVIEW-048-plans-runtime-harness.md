# REVIEW-048：PLAN-012 至 PLAN-015 运行时 Harness Plan Review

## 审查对象

- Objective：`OBJ-004`，落实运行时 Harness 综合改进设计，完成配置、验收、恢复与观测的实现和闭环验收。
- 执行顺序：`PLAN-012` → `PLAN-013` → `PLAN-014` → `PLAN-015`。
- 代码基线：`a9b6192192c5bd99e4378bb8234e64f0a1fddf73`；分支：`feat/runtime-harness-closure`。
- 审查方式：独立 Reviewer 未编写被审 Plan；读取根 `AGENTS.md`、`docs/AGENTS.md`、`autogo-plan-review`、Objective、四份 Plan 及当前实现接入点，仅写此 Review。
- 授权：用户已授权将上一轮五项 issue 及优化建议落实到设计、实施并真实闭环验收；不设置额外人为审批。预存 `.gitignore` 修改不属于本次改动。

被审文件 SHA-256：

| 文件 | SHA-256 |
| --- | --- |
| `PROGRESS.md` | `76bab49e3d663f2523eb0a2f57b63db3a0e87157b222b20cbdafbca64df7c2f0` |
| `PLAN-012.md` | `87928175dc1b7c44997c4061d53e818ce1cc44259c4130819c985f86098ac16b` |
| `PLAN-013.md` | `070ef01d077eed5393bfaddb318a1d240ac05baae9337e1e0f217a587e3c38a1` |
| `PLAN-014.md` | `c0906391b74096ad880ab6c3245b86dbf22b6ca800b7a605f5d3129368e68cf4` |
| `PLAN-015.md` | `e48ac79ad44b7f3cb231d88ad1becd5e2d2a384a99fa29745edc519dd47169af` |

## Verdict

`PASS_WITH_NOTES`

四份 Plan 在当前粒度下覆盖授权目标，依赖顺序和收口边界合理，可以开始 PLAN-012 Phase 1。未发现需要在第一条 Item 前修订 Plan 的缺项或错误依赖。以下 notes 是已有 Item 的实施与验收解释，不引入新目标，也不要求增加流程制品；其具体合同由已安排的 Spec/Design Review 核对。

Plan Review 不证明实现、Provider 支持、服务运行或交付验收已经完成。

## 目标与风险覆盖

| 原议题及建议 | 当前任务覆盖 | 判断 |
| --- | --- | --- |
| 区分角色与验收权威；优先确定性环境及 Validator，按需引入验收会话 | PLAN-012 1.1–1.2；PLAN-014 Phase 1、2.1–2.3 | 先明确合同和受信环境，再加入可选会话；不把 Agent Claim 改为完成事实，也不强制每个 Work 新增角色。 |
| 完整环境准备、readiness、场景、业务断言、制品与清理 | PLAN-014 1.1–1.3、2.1、2.3 | 包含服务依赖、实际客户端场景及失败/取消/崩溃后的资源归属恢复；能够验证产品用户入口，而非仅测试底层接口。 |
| Profile 承载模型、思考、权限、工具与 Provider 差异，角色限定职责和权限上限 | PLAN-013 1.1–1.3、3.1 | 同时覆盖有效配置、三条原有调用路径、选择规则、兼容拒绝、恢复身份和真实 Provider 配置验收。 |
| 目标项目 Harness 的发现、兼容、加载证据和委派职责 | PLAN-013 2.1–2.2 | 避免复制 Skills 或另建运行状态机；规则输入与可观测加载证据都有任务 owner。 |
| 区分合法 blocked、执行失败、协议错误；用户决定后可续作；有限自主恢复 | PLAN-012 3.1–3.3；PLAN-015 2.2、3.1 | 包含持久化、可操作 Gate、授权重试、取消、外部编辑、退出不明和真实续作；不会以只生成 Gate 代替恢复。 |
| 受信验收入口及依赖不能随实现被悄悄替换 | PLAN-012 2.1–2.2 | 明确直接脚本、解释器入口、显式依赖和批准更新路径，覆盖此前发现的实际缺口。 |
| 用户实时查看各角色消息、工具输出和上下文，观测不拖慢控制循环 | PLAN-015 1.1–1.2、2.1、3.1 | 包含 Invocation 关联、有界日志、可恢复索引、断线续读、脱敏及存活/输出/进展区别。 |
| 保留 SQLite 事务状态、Git 代码身份、文件制品的分工，并改善可读性与一致性导出 | PLAN-012 范围、1.1–1.2；PLAN-015 2.3 | 保留现有状态引擎，导出承担可读与备份用途，没有增加第二份可写运行状态。 |
| 易读 CLI、wait 反馈、目标定位、补全、版本便利、Gate 连续操作、初始化诊断 | PLAN-015 2.1–2.3 | 保留既有 JSON、退出码和 CAS 合同，同时减少操作者需要手工串联的内部身份。 |
| 设计、实施、真实验收和完整交付对账 | PLAN-012 Phase 1；各 Plan 收口 Phase；PLAN-015 3.1–3.3 | 包含正式合同、定向验证、各 Plan Change Review/提交、真实全链路、全仓门禁及最终逐项完成审计。 |

## 当前实现与拆分依据

- `internal/config/config.go` 的 `Agent` 尚无模型与思考深度；`internal/orchestrator/execute.go` 在实际调用处按角色生成 sandbox/tools，并为 Claude 固定 `dontAsk`。因此 PLAN-013 需要贯通到 Invocation 和 Provider，不能仅添加配置字段。
- `internal/orchestrator/execute.go` 将所有非 `completed` 的合法结果归为 `AGENT_PROTOCOL_INVALID`，而 `internal/protocol/protocol.go` 已声明 `blocked` 和 `failed`。PLAN-012 3.1 对应真实协议消费端缺口。
- `internal/orchestrator/failure.go` 的自动重试分支要求 `!checkoutOwned`；受控当前目录执行常保留 Checkout owner。PLAN-012 3.2 必须以现场和执行者身份可证明安全为准，不能只增大次数限制。
- `internal/validator/registry.go` 的脚本身份绑定只处理 `argv[0]` 为 `./` 路径的情况；解释器后的脚本不进入相同保护。PLAN-012 Phase 2 是必要的受信边界修复。
- `internal/environment/environment.go` 已有 `StartServices/StopServices` 接口，用户配置与编排尚未完整接入；Planner、Implementer、Reviewer 的调用也尚未接通统一 EventSink。PLAN-014 和 PLAN-015 分别完成独立的环境与观测边界，拆分合理。

## Notes 与验收解释

1. **可选验收 Agent 以真实场景证明收益。** PLAN-014 2.2–2.3 应选择需要根据服务或页面反馈继续操作的场景，并验证会话阻塞和错误 Claim；静态命令执行成功不能证明 Agent 辅助验收有效。环境归属、受信命令、事实采集与完成判断仍由确定性路径负责。复用 PLAN-013 的 Profile 解析、身份、权限和 Harness 合同，不为新角色另起配置体系；PLAN-015 的“各角色”包括此可选会话。原有三角色场景应继续可用，无须强制增加会话。
2. **运行模式维度在设计中明确分开。** PLAN-012 1.1–1.2、PLAN-013 1.1–1.3 已覆盖的配置合同，应区分 xgoal fast/standard、角色、Provider 非交互方式、模型/思考以及权限。原生 plan/自动模式不应被静默映射成 xgoal 编排模式；不兼容组合要可诊断。实际请求配置、CLI 版本、可观测实际模型和来源需进入同一有效 Invocation 身份；无法观测的字段不得写成已验证事实。
3. **文件导出需要证明一致时间点。** PLAN-015 2.3 的“一致结构化导出”包括数据库状态和被引用文件的关联完整性及校验，才能兑现此前关于一致性备份的建议。实现不必增加另一套恢复引擎；需证明运行中导出不会把不同时间点的状态拼接成一份看似完整的结果，不把直接复制 WAL 中的数据库文件当作一致备份。
4. **真实验收从用户入口启动，负向路径必须生效。** PLAN-013 3.1 与 PLAN-015 3.1 的实际 Provider 验收覆盖 Codex、Claude 对支持配置的真实调用；fake Adapter 只补充可确定注入的故障，不能替代这些运行 Evidence。PLAN-014 2.3 与 PLAN-015 3.1 应从用户配置和 CLI/daemon 贯通真实服务、客户端断言、错误阻断、等待续作和日志，而非仅分别调用底层包。凭证或外部服务不可用时记录具体未完成项，不能用普通测试 PASS 关闭这些 Item。

这些 notes 均落在现有合同、配置、场景或综合验收 Item 内，未要求扩大范围、重排 Phase 或新增 Plan。若后续设计决定改变用户行为、角色职责或验收覆盖，再按实际变化更新相应 Plan 并复审。

## 顺序、粒度与下一路由

PLAN-012 先统一整个 Objective 的行为与设计，并处理信任和恢复基础；PLAN-013 使执行配置与委派规则成为可复用合同；PLAN-014 在该合同上接入环境和可选验收会话；PLAN-015 汇合所有角色的观测与操作，并验证最终用户结果。该顺序避免新会话绕过配置、失败和信任边界。

每份 Plan 是目标、范围和按 Phase 组织的单文件 Checklist，没有嵌入执行日志、独立状态表或第二套 Evidence 台账。Item 表达一个可验收结果；对应边界的失败、超时和恢复场景是同一不变量的验证，不必机械拆成微观实现步骤。

下一路由为 `autogo-spec-write` / `autogo-spec-review`，完成 PLAN-012 1.1，随后更新技术合同及最小必要设计并进行独立 Design Review。完成合同后按现有 Plan 顺序持续执行；只在对应 Evidence 和 Change Review 成立后关闭各 Plan。Review 索引由主 Agent 在文档对账时同步。

本次未运行功能测试，未改 Plan、Progress、实现、索引或预存 `.gitignore`。审查 Evidence 为当前文件、SHA-256、代码接入点和用户授权范围的逐项对照；历史测试结果未用作当前通过依据。
