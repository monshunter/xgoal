# REVIEW-062：目标驱动验收实现 Change Review

## 审查对象与结论

- Objective：`OBJ-006`；被审 Plan：[PLAN-017](../plans/PLAN-017.md)。
- 合同：[REVIEW-061](REVIEW-061-goal-acceptance-contract.md) 已通过的产品第 22 节、技术增量与 DESIGN-007。
- Git 基线：`8059cfc0310539ec14ea6386c819838eba7bf234`；分支 `codex/goal-driven-acceptance` 的当前未提交实现。
- 最终复核包含 `internal/`、`cmd/` 和改动 `go.mod`/`go.sum` 下 69 份源码、测试与 fixture；按路径排序，将每项 `path + NUL + 文件 SHA-256 + LF` 拼接后的清单 SHA-256 为 `13a1a730b22e46bd2d2a9d257143e6a571036c001e3353284fc3905f94efb3a0`。独立 Reviewer 再次计算一致。
- 独立 Reviewer 按 `autogo-change-review` 阅读完整实现 Diff 与新增代码，沿输入、规划、Gate、冻结、执行、Review、Evidence 和恢复核对；未修改实现、测试或 Plan。最后复核了完整工程回归和两次真实游戏的交付证据。

**Verdict：`PASS`。** 四项代码发现均已修复，完整 `make verify-m6` 退出 0，后补材料与旧身份测试单独 race 通过；新项目真实贪吃蛇和用户原 demo5 恢复交付均经独立复核。当前没有未关闭阻断，可关闭 PLAN-017 并原子提交。以下早期段落保留审查过程，当前结论以本节与最后复核为准。

## 发现

### F-1：公开 Work/Review JSON Schema 拒绝新 Packet 字段（P2，已关闭）

初次 Diff 已在 Work `goal` 中写入 `raw_goal`、`contract`，并在 Review 顶层写入 `raw_goal`、`goal_contract`，但两份公开 Schema 的相应对象为 `additionalProperties: false` 且未声明新增字段。所有实际调用（包括无生成验收的旧 Goal）都开始写这些字段，严格 Schema 消费者会拒绝新 Packet。

修复已同步 `internal/protocol/schema/work-packet-v1alpha1.json` 与 `review-packet-v1alpha1.json`，保持字段可选并声明字符串/冻结对象类型。`TestWorkPacketBindsFrozenAcceptanceAndSchemaFields` 核对实际序列化字段及冻结对象篡改，`TestReviewPacketDeclaresFrozenAcceptanceInputs` 核对 Review 字段类型；独立执行 PASS。

### F-2：修正配置验收路径后，同 Goal 重试仍保留失效路径（P2，已关闭）

初次实现把 `previous.Request.AcceptanceFiles` 当作 CLI 输入重新传给 `planningRequest`；该列表已合并旧配置与 CLI。用户把 `planning.acceptanceFiles` 中写错的 `missing.md` 修正为 `acceptance.md` 并重启，`goal plan` 仍然合并旧路径，再次进入相同缺失失败。

修复新增可选 `explicit_acceptance_files` 来源字段：新请求保存 CLI 的明确集合（包括空集合），重试只保留该集合并刷新当前配置；旧记录无来源字段时保守保留原输入，避免静默删除历史要求。`TestPlanningRetryRefreshesConfiguredAcceptanceButPreservesCLIInputs` 验证配置路径替换且保留 `explicit.md`；独立执行 PASS。

### F-3：仍要求至少一个预配置 Validator，阻止纯生成验收启动（P2，已关闭）

位置：`internal/planner/planner.go` 的 `validatePacket` 中 `len(packet.TrustedValidators) == 0` 条件。

`config.validateValidators` 接受空列表；新的 allow/human-gate 合同允许 Planner 根据目标准备全部断言。但 `validators: []` 项目仍在 Provider 调用前以 `invalid Planner packet` 失败。默认 init 的 `git-diff-check` 恰好掩盖了该条件，系统仍隐含要求用户预装一个占位检查。

修复已允许空既有 Validator 集合，最终 Proposal 编译继续要求每条标准和 Work 引用真实已有或合法生成断言；deny 加明确能力缺口诊断，Prepare/PrepareInvocation 保留该具体错误。`TestPlannerDoesNotRequirePlaceholderValidators` 与更新后的 `TestGeneratedAcceptancePolicyAndExactApprovalResume` 覆盖 allow/human-gate 空集合、deny 拒绝、审批前后的原始 Proposal 投影与同一脚本续作，独立执行 PASS。

## 已核对的实现边界

- **输入来源。** CLI/配置路径先规范化，规划从实际 InputTree 读取 UTF-8 普通文件，拒绝缺失、symlink、deny 路径；Kernel 在持久观察前覆盖 Proposal 的 `acceptance_inputs`，模型不能替换这些事实。
- **冻结与隔离。** 编译把生成 ID 规范为当前 Goal 命名空间并拒绝静态覆盖、碰撞和未被标准/Work 使用的定义；Contract 冻结内联脚本。Registry overlay 克隆静态定义，额外保护材料，生成 Definition 绑定脚本和材料，未重新读取可变候选测试作为基线。
- **审批。** `preparedValidationHash` 覆盖规范化 Contract 与 Plan hash；`PublishPlanning` 再次核对。准确 Gate 续作复制已观察 Proposal、保存批准 hash、消费旧决定并创建下一 generation；实际执行重新核对配置、现场和旧输入。现有用例证明批准后 Provider 不被再次调用，审批不能被重复使用。
- **执行与恢复。** Work、final 和失败现场读取相同冻结 Registry；CommandRunner 在执行前后验证材料和当前 Tree。finalScenarios 已补入生成能力，避免 final coverage 仍只认识静态配置。
- **独立 Review。** 生成脚本使 Work Review 不受 fast/standard 开关绕过；Packet 包含原目标、冻结标准与材料。持久完成事务另外要求每个 required Work 具备独立 approved Review，不仅依赖 Engine 的条件分支。
- **完成证据。** 最终 Validator 沿原 Receipt/Evidence 绑定 Definition、Revision、配置与最终 Tree，报告区分 `agent_generated` 与 `project_configuration`。模型声明与脚本退出码不能跳过现有完成条件。生成脚本本身错误提示保留基线并指向审阅新 Proposal、新 Goal 的恢复方式。
- **兼容边界。** 新字段为可选，已存在的无新字段 Packet 仍可解码；旧执行器的严格 Contract 解码会拒绝新字段。单纯代码可读不等于旧 canonical hash 已逐样本验证，后续最终证据需要记录兼容回归结果。

## 独立执行的 Evidence

1. `go test ./internal/goalcompile ./internal/validator ./internal/protocol ./internal/config ./internal/orchestrator -run 'Generated|AcceptanceOverlay|PlanningAcceptanceInputs' -count=1`：goalcompile PASS `0.326s`，validator PASS `1.141s`，orchestrator PASS `10.783s`。protocol/config 在该过滤器下没有匹配测试，不计为这两包的覆盖。
2. `go test ./internal/protocol ./internal/control ./internal/store/sqlite ./internal/goalcompile -run 'TestWorkPacketBindsFrozenAcceptanceAndSchemaFields|TestReviewPacketDeclaresFrozenAcceptanceInputs|TestPlanningRetryRefreshesConfiguredAcceptanceButPreservesCLIInputs|TestGeneratedApprovalBindsScriptsInputsAndWholePlan|TestGeneratedAcceptanceCompletionCannotBypassIndependentReview|TestCompileGenerated' -count=1`：protocol PASS `0.168s`，control PASS `0.288s`，SQLite PASS `0.276s`，goalcompile PASS `0.360s`。
3. 最后增量独立复跑 Planner 空集合、exact approval/旧 Gate 提案读取、审批内容变化、配置输入刷新、强制 Review 完成门禁及 Packet Schema：planner PASS `0.187s`，orchestrator PASS `6.600s`，SQLite PASS `0.305s`，control PASS `0.744s`，protocol PASS `0.864s`。`git diff --check` 无输出通过。

以上是本 Reviewer 实际运行的定向检查，不包含真实模型调用或浏览器交互，不替代主 Agent 正在执行的完整 CLI、负向恢复和真实游戏验收。

## 最终门禁仍需对账

- 提供当前 Diff 的 CLI 默认/fast 双 Goal 实际流程、human-gate 输入或内容变化/过期/重启/重复消费负向，以及用户脚本和 Markdown 材料的运行结果。
- 提供真实 Provider 在新仓库仅输入贪吃蛇目标后的源码、运行交互（移动、进食增长、碰撞、计分、重开）和同一最终 Tree 的 Evidence/Report；区分实际观察与模型自述。
- 最终回归通过后冻结被审 Diff 身份，完成 Plan、Spec AC、Review、Progress 与 Git 对账；不得以阶段测试或 Commit 成功代替真实交付。

下一路由：主 Agent 继续当前验证工作，随后提交最终 Diff 与运行证据进行增量复核。Review 索引由主 Agent 在文档对账时同步。

## 增量复核：空洞断言负例与默认等待反馈

本次增量仍属于 PLAN-017 3.1/3.2 的验收防误报和状态反馈范围，没有改变目标、Phase 顺序或授权边界。对产品 AC-GA-006、DESIGN-007 与 README 的交互终端说明做独立合同复核，结论为 **PASS**；这不改变上文最终真实交付 Evidence 尚待核对的门禁状态。

- 交互终端仅在 `run --wait` 且未显式指定 format 时，根据 stderr 的实际终端属性启用原有 `humanFeedback`。`request.wait` 保持创建与终态 stdout JSON 分支，显式 json 禁止自动人类反馈，非终端 stderr 默认静默，显式 human 保持原行为。限频、退出码、CAS 和等待取消机制复用现有代码。`go-isatty v0.0.24` 由间接依赖变为直接依赖，没有引入新版本或新库。
- `TestInteractiveWaitDefaultsToProgressWithoutChangingJSONContract` 覆盖终端默认、重定向默认、显式 json、显式 human 四种组合，并从 stdout 连续解码创建及完成 JSON；相关格式校验与等待回归独立执行，CLI PASS `0.494s`。终端选择通过注入判定测试，真实 Fd 检测代码只调用既有 isatty。
- `TestGeneratedVacuousCheckIsBlockedByReviewEvenInFast` 使用冻结的 `true` 命令、fast 和 `requiredInStandard=false`，保留实际本地 Agent CLI 子进程、Validator、Review、Store 和 Gate 链路；独立 Reviewer fixture 返回 high/test_gap 后 Goal 保持 WAITING 且无最终 Evidence。独立执行 PASS `15.061s`。此证据证明拒绝路径及强制审查，未声称实际模型能识别任意空洞断言。
- 当前增量代码清单包括 `internal/`、`cmd/` 和改动的 `go.mod`/`go.sum`，共 58 份文件；沿上述清单算法 SHA-256 为 `ef08f334cee648abf840949256401c9210da060a20bb6ff7825f0202e862ef29`。产品 Spec SHA-256 `ccdbb48642eb0ae2fd84b98a72a25b2f8e7b20a332816a6dd27f670ed2db7cce`；DESIGN-007 `bf61ddc00e23da931347e26269bd839e9e2ca4cfc15ef7d69a1d5bac399fc038`；README `9d56e32d67c36b271f160e57a473e338f1afe12ff2262613fff50fdc74845c23`。`git diff --check` 通过。

没有新增未关闭代码发现。最终记录应区分全量门禁已构建的代码与后续定向复核的增量，不能把较早测试进程的结果当作后续修改的完整测试 Evidence；当前真实 Provider 仍在规划，尚不构成游戏源码或运行验收结果。

## 最后增量：可用 Reviewer、混合材料、旧身份与真实交付

### F-4：取消覆盖进程归属不确定错误（已关闭）

默认 Reviewer 预检初稿在检查 `ErrProcessUnconfirmed` 之前返回 `ctx.Err()`；若父 Context 已取消且无法证明子进程退出，后续失败收口可能丢失保持执行 generation 隔离的必要错误。修复改为优先传播进程不确定错误，并补充同次 Probe 取消且返回该错误的组合负例。两 Adapter 的被动 Probe 保留 `%w` 错误链，认证超时或进程未确认时不再误报缺少凭证。独立 Reviewer 复核正确，取消组合 race PASS `1.311s`。

默认候选只有在真正 Review 调用前因 Probe 失败或明确缺凭证时被跳过；显式 role binding 不替换，unknown 不等于 missing，审查失败或拒绝不会更换 Reviewer。只从临时副本移除候选的 reviewer role，保留 Implementer Provider 排序与配置身份。daemon 构造阶段的无效 CLI 路径仍需先修正，不声称本次覆盖该启动失败。

两份真实 Adapter 认证超时测试以标记文件确认进入认证命令，要求 `errors.Is(context.DeadlineExceeded)` 且不得返回 `missing`。初始 2 秒预算在 race executable barrier 的 version/help 阶段耗尽，因此调整为可覆盖前置 Probe 的 10 秒预算；不是放宽产品 Probe。最终定向 race：Codex PASS `11.454s`，Claude PASS `11.295s`。

### 完整材料链与审批重启

`TestRealCLIAcceptanceMaterialsAndApprovalSurviveRestart` 使用真实 CLI、daemon、Git、SQLite、Packet、Validator、Review 与最终 Receipt；Provider 为确定性可执行 fixture，不声称真实模型语义判断。

- 配置 `planning.acceptanceFiles: [acceptance.md]` 与 CLI `--acceptance-file acceptance.sh` 合并，human-gate 展示两份确切材料及方案 hash；停止并重启 daemon，再对原 Gate 批准续作。
- Planner 只调用一次，Work 和独立 Review Packet 都携带原材料的内容/hash；最终生成检查实际运行 `sh acceptance.sh`，Goal COMPLETED，最终 Evidence 绑定最终 Tree，材料、配置、HEAD 和 index 不变。
- 负例中真实 Implementer 将 `acceptance.sh` 改为 `true`，明确出现 OPEN `trusted_validator_change`，Goal WAITING 且无最终 Evidence；同时核对现场确实已发生篡改，避免“未执行到篡改”造成误报。
- 最后与旧 hash 用例一同运行 `go test -race ./internal/cli -run '^TestRealCLIAcceptanceMaterialsAndApprovalSurviveRestart$|^TestLegacyAcceptanceFieldsPreserveCanonicalIdentity$' -count=1`，PASS `42.294s`。独立 Reviewer 已只读审查这些断言，没有新增阻断。
- 生成审批的拒绝、过期、双版本 CAS 与现场变化沿共享 `ResumePlanningGate` 的既有负向用例；生成专有绑定由 `TestGeneratedApprovalBindsScriptsInputsAndWholePlan` 核对脚本、Plan 和输入变化，exact approval 测试核对重复消费拒绝。

### 旧数据身份

独立 Reviewer 使用 `git archive 8059cfc` 在 `/private/tmp/xgoal-legacy-compat-st8cbqdm/old` 构建旧代码，与当前独立快照比较 7 份固定样本的 canonical bytes/hash，全部相同：Config、Planning Request、Goal Contract、Goal Envelope、Planner/Work/Review Packet。固定 golden 已纳入 `internal/cli/testdata/legacy-pre-acceptance.json`（SHA-256 `f251c23d49ba296900505da54213ade08d829dcb56999b7927af3b72592ddc93`），当前 typed 结构重读再哈希的回归 PASS `1.371s`。不存在把“当前结构重建两次”当作旧版本兼容性的情况。

### 真实用户结果

[操作记录](../operations/GOAL-DRIVEN-ACCEPTANCE.md) 已完成新项目真实贪吃蛇交付。独立 Reviewer 从审计 SQLite、Receipt、Report、实际源码和三张浏览器截图核对：Goal COMPLETED、唯一 Revision 1、独立 approved Review、三条标准 PASS、所有 9 份阶段/最终 Receipt 绑定同一 Tree 且 PASSED；5 份源码逐字节等于最终 Tree；HEAD、index fingerprint 和配置保持初始值。171 个导出文件的 size/SHA-256 均与 manifest 相符。

本次实际过程包含修复未登录 Claude 的默认选择后的一次 Work retry，记录没有把它冒充无干预运行。真实浏览器通过按键/按钮验证移动、吃食增长和 10 分、暂停、撞墙结束及重开清零。AC-GA-001/007 有当前真实证据；既有材料链、审批重启和旧身份缺口也已关闭。

最后代码与 fixture 清单（`internal/`、`cmd/`、改动 `go.mod`/`go.sum`）为 69 份，清单 SHA-256 `13a1a730b22e46bd2d2a9d257143e6a571036c001e3353284fc3905f94efb3a0`。本轮 `make verify-m6` 在生产源码停止修改后运行，CLI 全量 race 已 PASS `805.405s`；随后新增的两项测试及 golden 使用上述定向 race 单独覆盖。其余工程门禁完成后再更新最终 Verdict。

## 最终复核与关闭证据

`make verify-m6` 最终退出 **0**，日志 `/private/tmp/xgoal-ga-real-9b1egk55/verify-m6-complete.log` SHA-256 为 `c622dee2e2a59f656b3751a390d8e358dd2d9425b8f17dd431e4e31fd4b1af71`。主 Agent 观察进程退出；独立 Reviewer 对照 Makefile 核对完整日志和冻结源码清单，没有失败或漂移。

- 格式、全量原生 race、关键状态合同 shuffle 20 次、`go vet ./...`、CLI/配置/completion smoke 全部通过。主要全量包：CLI `805.405s`、orchestrator `950.911s`、SQLite `126.083s`、validator `76.245s`。
- Darwin arm64 和 Linux amd64 的纯 Go SQLite 编译、Linux 全测试包编译及两平台最终 CLI 构建通过。`-exec=true` 是跨平台编译验证，不作为 Linux 原生测试执行。
- 固定 Benchmark 配置校验 valid；真实 Benchmark 仍为 `NOT_RUN`、`upload=false`，本次真实 Provider Evidence 是两次游戏运行。
- 全量 CLI 构建之后新增的材料审批重启/篡改和旧 hash golden 测试，单独 race PASS `42.294s`；生产源码在完整回归启动后没有改变。全量结果与该增量共同覆盖最终被审代码，未将较早测试冒充覆盖后续新增测试。
- 原用户 demo5 的实时 SQLite 与 144 份审计导出一致：Goal COMPLETED v6 / generation 2、单 Work、单 SUCCEEDED Attempt、独立 approved Review；6 份阶段/最终 Receipt 均 PASSED 并绑定最终 Tree `7687578a3d72701f4d31614dc59ad1623fd4d03d`。没有未解决 required Gate、Finding 或未退出 Invocation。
- Reviewer 独立逐字节比较 demo5 的 5 份源码与最终 Tree，并核对私有 Commit、HEAD、index fingerprint、配置、每份导出文件的 size/SHA-256；均符合恢复前身份和最终报告。四张真实浏览器截图支持 1 分四节蛇、碰撞、零分重开与方向控制。

独立 Reviewer 最终结论 **PASS，可进入关闭与提交**。未新增必需修改或未完成验收；Plan、产品 AC、操作记录、Progress 和当前 Git Diff 在关闭时统一对账。
