# REVIEW-054：PLAN-014 受信环境与独立 Acceptance 变更审查

## 审查范围与 revision

本次为独立 `autogo-change-review`，覆盖 [PLAN-014](../plans/PLAN-014.md) 的全部已跟踪 Diff、未跟踪实现与测试，以及 README、产品 SPEC 第 20 节、技术 SPEC 第 35 节、[DESIGN-007](../architecture/DESIGN-007-runtime-harness.md) 和 [REVIEW-053](REVIEW-053-acceptance-effect-design.md) 的对账。

基线 Commit：`22d90cb0a8af60930cf04a56abe0f5704d8fcefa`；分支：`feat/runtime-harness-closure`。排除用户预先存在的 `.gitignore` 中 tmp 规则和本 Review 自身。末尾清单保存本次全部变更输入的内容 SHA-256，包含最终 Review 索引；未修改的技术 SPEC 另列参考身份。Reviewer 仅写本 Review 和 `docs/reviews/INDEX.md`，没有修改实现、Plan、Progress 或用户文件。

此时 PLAN-014 Phase 1/2 已完成，Phase 3.1 尚未勾选，等待本门禁后对账提交。PLAN-015 的实时上下文、操作便利性、一致导出及综合发布验收不属于本 Plan 的已实现范围。

## Verdict

`PASS`。分段审查和最终回归发现均已修复并复核，当前 Diff 没有剩余 PLAN-014 阻断。可以进入本 Plan 的 Close 与 Commit；本结论不表示 OBJ-004 完成，也不代替 PLAN-015。

## 发现与修复复核

| 发现 | 影响与最终处置 | 验证依据 |
| --- | --- | --- |
| F-001 / P1：readiness 成功时本次服务已退出 | 旧端点的探针成功可能冒充本次实例就绪。`supervisor.Group.Start` 在成功后核对 PID/start identity、存活、done 和 deadline，异常回收并失败。 | 进程已退出而探针返回成功的负例；真实服务链及取消/崩溃回归。 |
| F-002 / P2：恢复按随机进程 ID 清理 | 可能先停依赖再停客户端。正常 Group 保存启动顺序，Store 恢复按单 daemon 串行 Journal 的持久 rowid 倒序处理。 | 同时间戳、随机 ID 与 Store 重开的顺序负例；真实 api→db 停止断言。 |
| F-003 / P1：Finalize 可整体遗漏场景 | 只检查 Report 已提供的 manifest 会允许省略所有场景。事务现在从冻结合同核对 Criterion 的 scenarioIDs/validatorIDs、manifest 集合及同一 final Evidence Set 的通过收据。 | `TestFinalizeCannotOmitFrozenScenarioMappingAndArtifacts` 先复现错误完成，修复后拒绝且保持 VERIFYING；场景 race 与真实缺失制品负例。 |
| F-004 / P2：Work 可使用 final-only Validator | 编译与实际执行阶段不一致。编译在有冻结能力时要求 Work Validator 支持 change；执行入口再次核对 phase。 | `TestCompileRequiresEveryScenarioBusinessAssertion` 的 final-only 负例及编译包 race。 |
| F-005 / P1：资源清理与完成事务缺少完整屏障 | 最终断言后必须先封存制品、逆序停服、确认退出和 Tree/HEAD/index，再提交完成。清理错误不丢弃；Store 完成事务同时拒绝未结束 process/worker/effect ownership。 | Finalize process/worker 负例先红后绿；SQLite 定向 race 和真实停止/取消/崩溃链路。 |
| F-006 / P1：RECOVERING 收到的新 Claim 未写盘 | 进程 UNKNOWN 时晚到的 blocked 可能被后续恢复覆盖为中断。相同 RECOVERING 状态也通过 version CAS 持久化新 observation，后续保留已有完整 Claim。 | 三步测试：首次中断→晚到 blocked 且 UNKNOWN→确认退出后恢复；不得通过 replaySafe 忽略问题。 |
| F-007 / P2：Historical observation 可能重新成为 current | 独立 Gate 后来批准、不改变 Goal version 时，历史回调可能重新放行。有效观察条件加入 `!old.Historical`，历史标记单向保留。 | 第三步明确模拟 `recovering=false` 的普通晚到回调；实现与负例独立复核通过。 |
| F-008 / P1：完整 Provider 事件与 result.json 之间的崩溃窗口 | 原恢复只读 result.json，可能把已记录 blocked/failed 改为 interrupted。现在先匹配 Invocation 元数据，再从有界连续持久事件恢复完整 Claim；Claim 不证明进程 exit 0。 | 双 Provider event-only 测试；实际 daemon SIGKILL 在 blocked 事件已落盘、result.json 不存在且进程仍挂起时发生，重启保留问题且不重放。 |
| F-009 / P2：终态错误误分类为无结果中断 | Claude is_error 通常没有 structured_output，不能先跳过候选空值。恢复先识别终态错误；Provider 已结束但无有效结果也返回明确失败，阻止安全重放。 | Codex turn.failed/无结果 turn.completed、Claude is_error/缺 structured_output 负例及 Acceptance race。 |
| F-010 / P1：手工 Finalize 可跳过配置的 Acceptance | 最终事务从冻结 planning request 确认必需会话，核对最新 Effect 的完整身份、完成 Claim、退出和非历史观察，并将场景 manifest 绑定同一 EnvironmentID；独立收据仍是业务权威。 | `TestFinalizationCannotOmitConfiguredAcceptanceSession` 与 SQLite 定向 race；正常 CLI 完成链路。 |
| F-011 / P2：原生 Adapter 测试的 1 秒预算不足以运行 race | Reviewer 首轮 race 的两个 Provider 子例均在约 1.06s 超时。只将该测试的实际调用预算设为 10s，未修改生产超时或跳过测试。 | Reviewer 重新运行 Acceptance 全包 race PASS 4.703s。 |

F-005/F-010 包含作者在收口对账时补齐的边界；其余来自分段独立审查或本轮回归。以上均为已解决发现，不保留额外流程门禁。

## 系统关系与兼容检查

- 配置声明服务 DAG、readiness、停止预算及 Validator/Scenario 所需依赖。空服务请求不意味着启动全部服务；准备和验证复用 Local Provider。启动配置、readiness、bootstrap 与 Acceptance 客户端的控制文件按受信基线冻结，业务服务入口仍属于可修改的候选源码，不能因服务执行而冻结全部业务文件。
- 项目环境先收集明确声明的可用名称，再按每条命令选择；没有名单不继承其他命令的值，也不继承 Provider Profile 名单。Kernel 提供私有临时/场景目录与环境 ID。Local 仍为 L0，Profile 凭据可能由原生 CLI 子工具继承；受信客户端包装脚本应限制转发范围，README 和真实 Claude 夹具明确这一边界。
- bootstrap/readiness/服务输出保留在归属环境目录。诊断按完整行脱敏，处理跨 chunk 和超长行，单流总量 4 MiB、单行 64 KiB，超限有标记。服务日志保持打开直至停止；Cleanup 释放进程与内存能力，保留诊断目录，删除仍由显式 clean 拥有。
- `scenarios[].validators` 是关联 owner，Validator 的反向 scenarioIDs 不能建立冲突关系。Planner 获得冻结能力和步骤；缺少 description 时显示 unspecified。编译校验引用与声明关系，不从命令名称推断业务语义，也不声称证明任意断言的充分性。
- 制品限定私有场景目录中的精确普通文件，拒绝 glob、逃逸、symlink、超限和捕获期间变化；文件与目录逐步 fsync，manifest 最后发布。单文件 32 MiB、单场景 128 MiB/64 文件；读取重新校验封存内容、模式和哈希。Report/SCENARIO Evidence/业务 Receipt 共同绑定 Revision/config/最终 Tree/环境，不使用 Agent 的 checks_claimed 生成通过证据。
- Acceptance 的 Packet/Invocation 独立于 Work，物理 owner 复用最后成功 Attempt 和 generation。Provider 原生参数使用统一 Effective 配置和深拷贝；Codex 固定 read-only/never 且拒绝服务场景，Claude 使用 dontAsk、基础工具和独立的字面受信脚本允许规则。接受 Claim 后仍检查受信入口与源码身份，再运行最终业务断言。
- 专用 Begin/Observe 事务核对 request hash、effect/invocation、Goal version、Revision/config/Tree 和 owner generation；同项目最多一个非终态 Acceptance。场景命令的同 owner 豁免不能启动第二个 Acceptance，通用 RequestEffect/UpdateEffect 拒绝绕过专用入口。人工 Gate 的准确 owner、旧 invocation、scope、version、期限和单次 used，与新请求原子消费；触发器失败回滚不会耗用授权，重开后的精确请求不会再次执行。
- 完整 blocked/failed 和历史结果不可通过恢复重新取得执行权威。未知进程保持屏障；缺少完整结果且明确 replaySafe 的已停止中断才允许有界新 Invocation。默认重放需 final Gate；取消/暂停只能保存历史与回收资源。最终断言或清理失败后再次运行也不会直接复用旧会话的 completed Claim。
- 新可选 JSON 字段使用省略规则，未填写的 bootstrap env 不改变旧 canonical 身份；旧未启用服务/Acceptance 的三角色流程保留。需要显式依赖声明的旧入口仍按既有迁移合同处理，不原地重新信任旧 Goal。此次没有新的状态表或 Schema 迁移；SQLite 仍为唯一可写状态，Git 仍为内容身份，文件仅保存输入、日志和不可变制品。
- README 的配置片段、示例覆盖描述、支持矩阵、受信脚本执行位、网络前置条件、Gate 回答/续作和重放限制与当前实现相符。原有 JSON、取消 exit4 及已公开命令未改为新语义；实时角色日志/统一上下文入口等仍由 PLAN-015 收口。

## 验证 Evidence

Reviewer 独立执行：

- 分段验收期间复核服务存活/逆序恢复、场景遗漏/阶段约束、Acceptance CAS/Gate/Claim 恢复的定向负例。三步历史测试与禁止省略 Acceptance 测试合并 PASS 0.404s；双 Provider event-only 与终态错误分类测试 PASS 0.657s。早期 Report 定向命令曾返回无匹配用例，未将该命令计为验证；后续 Report 全包通过。
- 最终 `go test -race ./internal/config ./internal/environment ./internal/scenario ./internal/acceptance ./internal/goalcompile ./internal/report ./internal/supervisor -count=1`：Config 1.302s、Environment 23.402s、Scenario 1.536s、GoalCompile 1.858s、Report 2.181s、Supervisor 12.342s PASS；Acceptance 首轮因 F-011 超时失败，不能把整条首轮命令写为通过。
- F-011 修补及 Packet 新目录父目录 fsync 补齐后，独立 `go test -race ./internal/acceptance -count=1` PASS 4.703s。生产调用仍使用 Profile.Timeout。
- `go test -race ./internal/store/sqlite -run 'Acceptance|Finalize|Scenario|RecoverableProcess|RecoveryOrder' -count=1` PASS 23.523s。
- 最终已跟踪 Diff whitespace 检查无输出；29 个未跟踪输入分别进行 no-index whitespace 检查，无诊断。Review 和新增索引另行检查。

作者执行结果由产品 SPEC 第 20.9 节保存，Reviewer 已阅读对应测试实现及结果断言，没有重复调用付费模型：

- 真实 CLI/daemon 双 Goal、实际 HTTP 依赖服务、真实客户端、业务断言和场景封存贯通；源码/index/HEAD 保持、环境隔离、日志脱敏、api→db 停止、端口关闭和进程记录结束均有断言。七类服务故障最终矩阵 PASS 127.636s：bootstrap、startup、readiness、HTTP 200 但业务错误、缺少最终制品、cancel、daemon SIGKILL。
- 完整 Acceptance CLI 回归 PASS 243.343s，包含正向、blocked 回答后新 Invocation 的准确 Packet 决定消费、源码写入拒绝、虚假 completed 被真实业务断言拒绝、持久 blocked 崩溃、安全中断重放和取消。关键窗口单独结果：persisted-blocked 29.73s、interrupted-safe 41.46s、cancel 32.155s。Provider 在这些故障注入场景中是 CLI fixture，服务、客户端、进程与 daemon 为实际运行，不能替代下一条模型 Evidence。
- 实际 Claude Code 2.1.235 / sonnet / low 的四角色 Standard Goal PASS 110.432s（运行 1m48.007s）；Acceptance 通过受信客户端执行实际服务的 readiness→业务交互，独立最终断言随后运行。最终 Tree `9c1b8815d58315425745970577ade425ba385cd7`，Evidence Set `evidence_set_final_8360541179784451360a8d6f`，Acceptance session `fe23093d-f5aa-415f-a697-3af8bbe4123d`。
- 实际 Codex CLI 0.153.4 / gpt-6-astra / low 的四角色只读观察 Goal PASS 220.028s（运行 3m37.575s）；最终 Tree `2a54b5b88d98072acf457c8a4d12e9f95d02571d`，Evidence Set `evidence_set_final_802eaedc5121e4d9cdb18af9`，Acceptance session `01a071cd-34c6-7ec3-8d16-3bc4d1ee2a34`。该场景合法声明零个文件制品；初次共享测试辅助函数误要求至少一个文件，按 `len(Files)==len(ArtifactPaths)` 修订后重跑通过，未以测试假设修改产品范围。
- 最后 Orchestrator 全包 PASS 247.727s、Control 12.143s、Codex Adapter 5.983s、Claude Adapter 4.063s、Recovery 1.160s；Config/GoalCompile/Scenario/Report/SQLite/Acceptance/Supervisor/Validator 全包通过；`go vet ./...`、示例配置校验和 Diff 检查通过。

成功的真实模型夹具按测试合同清理，临时失败事实仍由产品 Evidence 保存。实际模型别名解析、任意 effort 组合、同 UID 恶意隔离、共享外部数据自动回滚和完整公平性 Benchmark 不属于上述结论。原有三个角色的完整链路与新第四角色的受限路径分别有证据，未把模型声明或历史 PASS 当作当前业务断言。

## 下一路由

主 Agent 可按 `autogo-change-close` 对账本 Review、实际验收、Plan/Progress 和 Git，关闭并原子提交 PLAN-014；随后继续 OBJ-004 的 PLAN-015。保留并排除用户 `.gitignore` 修改，不自动推送或发布。仅勾选、索引投影与提交不改变此代码结论；如果收口再引入实质实现变化或新失败，应修复、验证并重新绑定 Review。

## 内容身份清单

以下 65 个变更输入按路径排序，每行为 `SHA256 + 两个空格 + path + LF`。清单 SHA-256：`a876dc8f83ed3e17498ec814d8fdba415705e3349007d7a92731783620c4ba20`。

未修改的参考合同 `xgoal-technical-spec-v0.1.md` SHA-256：`9bf957663b6c6fc0fc24feed775a2b84c91588e7bd93ad3ee88935f414e4e10c`。

```text
adbe53720d7b8f6e021377c9837aef2a3b123b75fc054cf7064ed908d81d9917  README.md
709e9110970f7a13fa00750f4af179c27f163be126f61246335d1cb3d1fd1730  docs/architecture/DESIGN-007-runtime-harness.md
77e73c60293b42e0ac8dea61cd10e947d25b9bfc8c57ab8ecbf8f8ad957fcb54  docs/plans/PLAN-014.md
01b5b6c4021be6d3244b068173a1624f10b8337f718e6d2bfc1d99d7f7d22b22  docs/reviews/INDEX.md
803cc457f1ea112b2d3a868ea432d48a4b34cbecb21b47c3c2d1006f0a9a01bf  docs/reviews/REVIEW-053-acceptance-effect-design.md
cafff07f7b230c22f421d4f7a2494b66133cbfcbc74b2de355f8f0fd0cdebe1f  internal/acceptance/acceptance.go
8bdf6b07297f3920319c28ebedcc02252054f56deed0dce4aa0de06043bb16f9  internal/acceptance/acceptance_test.go
338836d8dc155727a723e6320eb3afa29f367f722b75a6a1d3cee237da51d1b0  internal/acceptance/artifact.go
abe4fdd148ec264136f79b4336853a7e9750cf0abbea2fd24883159a32cfb983  internal/acceptance/artifact_test.go
4da8684a0d264791824242fd7fbf283acd801cd46e36279350e3027a9100beab  internal/acceptance/effect.go
147cf04765cae43aae8c941037a4dd1ada5591a64157087997a686025a6fc3c4  internal/acceptance/provider_test.go
3275712e371888ac264526b9ec429a6f7cca70727311ed6147b33e52bbce61c9  internal/adapter/claude/acceptance.go
e88f95954f5c2b22a286628216ba7e2ca13d03b2ddbd6390f7e7a83a07efc348  internal/adapter/claude/claude.go
77e6ba03d0aa8bdb65665b44fcf146d306b1d17ea9aef9fbf588c805695fdede  internal/adapter/codex/acceptance.go
842fcf62e93091c448d5aed7d67c93a878bfaf2288617099e5bde1bbf03b6eb6  internal/adapter/codex/codex.go
cce6b9f4cfc34f63195ac00318d1d8178fed05653c3b7792dccdf4873eee876c  internal/cli/acceptance_test.go
6c5d136da6d77f4619191387df8335e11d5ce53c265930235f084004815c3bf1  internal/cli/background_test.go
d89e94db55cdf470d43a2da1e10df58b821637ce007a4a54d2a6c346c705e7c4  internal/cli/current_directory_test.go
265c4927a57797227e7d8f5bb9367cafd334c5d50d68fef924b2e7223be89fbf  internal/cli/real_goal_test.go
39822fa75d39d7c7af8032537d587cabbebe74ec8f7503112ffa8f927cc76b3b  internal/cli/services_test.go
9f0bf6148a17e6d5017a34477c119cfe16423683e6a212f41610d96116efd23e  internal/config/config.go
00f8cb59fa0cc8cb6b53e6e6e01b17ff9c08963ff16d15e715b2272495a6f95e  internal/config/scenarios.go
9ad6870c15c2fcfff4428346eea006ae405220be2bec804631b0c2b243d143df  internal/config/scenarios_test.go
0dfcb2cfe6520e886fe85a43a4db2247b402b03c0a46a1aed50f0ebbf82f1ece  internal/config/services.go
7d30a3c1dd5dc421c9177cf05a5f390dcf809fb15e5861861a6e4bcf764e4feb  internal/config/services_test.go
a8f1edfa1fe06e745f29080e233c98432b7a04b67e220553ec26f22d7d7cc1a3  internal/control/planning.go
ed5c42906dd71e5e0fdf803275a3cfd417e0486475b1b99fe67b874c3cc296cb  internal/environment/diagnostics.go
c3c83ad9584655fb6c7a50e8c6b73742de9ff1b26ab4fb9ec60c64df6fbe1abf  internal/environment/diagnostics_test.go
921c2ecf48e777dab0fb9c04990efd36da6c5ce9ab16e22fea2b85398ef9328f  internal/environment/environment.go
c3a7ebf1bc0826c66f6f8714919a8e0ba6e864cde3cad394971003ec91da6671  internal/environment/local.go
afd60ffd42ec352172e363fa1068b933f18b1f57795a4fa31a6fc15a9e6159bd  internal/goalcompile/compile.go
b0a1ba9673f6f42e637903690bb2be8f39481554d23b4e74d8597d3ba38f0f26  internal/goalcompile/coverage.go
61ee4d1e0a5edce627b44c745d9a6bb69015dc1ef157afb0d56f21379e1a4ac3  internal/goalcompile/coverage_test.go
b789e594980d8dafbef14aad779640bb94fa955a2b1ac4ad1c1ac11d15e9f94d  internal/orchestrator/acceptance.go
6b0ed754a225ea73f85c3654f7ea266be8f95337ec5ee197310633b075ee0a62  internal/orchestrator/bootstrap_test.go
3230af3f53aeb546e1443453181529ab1564592dc038dba243d777e88a836b26  internal/orchestrator/engine.go
0260e675393ef5e7f6a6f1fc4e03d4329de9fc09a908160418a99f3cbdb3a153  internal/orchestrator/environment.go
a7352a8c35c0148033643957f57bb424210a720d1e2f61274f6d7cf3be4f169c  internal/orchestrator/execute.go
bc1c53a6661657a9fc8216b547e6bb9c896540524f603523402335ad102a71ba  internal/orchestrator/finalize.go
457f228dabd3dde1b18b1d62f003925f1ddccc45444b512577939402c9ca6547  internal/orchestrator/planning.go
61146b155af3a6998e6d404eade14c5d17a431f644b4dd1f4d531066ebf544b0  internal/orchestrator/scenarios.go
f803789164342ae0906d2ab7364927873a7bbdd59a6c1ab07c696b473f04bdc4  internal/planner/planner.go
8d0b771303c94f817e302cc2e56595d506469a7692ef99c3fa12392efb2bef1d  internal/planner/request.go
a1ee5617a94ed8e330a1828fcc66d9e50f7e764402d2ea36fccbcd4c5de0a709  internal/planner/schema.json
ef93bc9576374ec0f499608e0f27d35287b8c3a9487bbf04a56699b1d0ff2301  internal/report/report.go
5d9e9428b23f60603dfe946efdeabc487b748737ce5c525be182ec61cf275dff  internal/scenario/artifact.go
b546a8d6edcb98b1ab9742b604455688d83d60820b132346d33bfc6f2fbf9093  internal/scenario/artifact_test.go
59aa20919f3095fbf6d0cedc27b5a8a5da6635b6667aca1a8e37a98e8e95b629  internal/store/sqlite/acceptance.go
f43fb7759167e2ca73c35abf7a081608b700f4708843a8c47fe71d35becb9ae6  internal/store/sqlite/acceptance_test.go
9cefbe0b41e0fc8778dad3aa291e2a44485aa84ca2b553b8d0cb51d3aa6da6a8  internal/store/sqlite/effect.go
51c25fb4f91f8ff2c0246380d1d90c8e05d65c1930ac841ff4552ee13325273a  internal/store/sqlite/final_report.go
2c9e0904355695b79d94ae919c71717a1582d1c4d6c2ffecd9f5c5d1698b1f66  internal/store/sqlite/final_report_test.go
7db7d145ddb1563759f98e98861b2fc5827e7cf0da62536c0d69bf7076248ad0  internal/store/sqlite/planning.go
3d9b60e158cba729d809f5361fba3fbabb663c834611942d535e6c8ef1ecb8e4  internal/store/sqlite/process.go
723e1c2c3aef39d0f3c361f8258eec3dfa658be2cf409f53c6331c5a510d5bd5  internal/store/sqlite/process_recovery_test.go
c6c22e3dda0515d506bc6a5aaa5fcb66d18e4256a69a6c2083e9c9ea44f03e01  internal/store/sqlite/scenario.go
8296bab2dace9fc578bed35707d71eabf1a70c532ce8f6a1161d6bf84981215b  internal/store/sqlite/scenario_test.go
a4981324fa6b6fb584edbf793ad4fc1f63109b8e2735a707eba396a67421c0d1  internal/supervisor/runner.go
72399fcfd3f173d7dadc088def060b2882a611d9ebead124d2abeccdbc543570  internal/supervisor/runner_test.go
0e02f6ee7bfd00c25e642a78e5bda75b264fc75fc4ca7853505c53875d62aa25  internal/validator/control.go
27a1fa20e7ee76aa0e371e681e16c253341c3fba591642e02907bf4cc364c460  internal/validator/registry.go
491aea44b305a52525a3937308876cea95add72858703e868d7a42ce267f4aec  internal/validator/trust.go
a5f0003eed42d4cc127e8529e196d1a20bc721d2f6246fca3fc72b3d3eab332b  internal/validator/trusted_files_test.go
6e6b47ede98a4c1481f33f63fb8e5def47ef83c351a90999e86c4c11c0f46385  xgoal-product-spec-v0.1.md
92c0ec39897ffb230160385efb54a0ae6cc1e524004af27eafea262221243780  xgoal.example.yaml
```
