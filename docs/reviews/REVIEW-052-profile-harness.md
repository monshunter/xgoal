# REVIEW-052：PLAN-013 Agent Profile 与项目 Harness 变更审查

## 审查范围与 revision

本次为独立 `autogo-change-review`，覆盖 [PLAN-013](../plans/PLAN-013.md) 的完整实现与兼容、测试、文档 Diff。对照产品 SPEC 第 20 节、技术 SPEC 第 35 节和 [DESIGN-007](../architecture/DESIGN-007-runtime-harness.md)，不把 PLAN-014/015 的服务、可选 Acceptance 执行、实时日志、交互和导出功能列为本 Plan 缺口。

基线 Commit：`3e8016d6fc7b64a694a02218ac44512bb449fa69`。被审输入包括相对基线的全部已跟踪修改及未跟踪实现/测试文件，同时对账技术 SPEC；排除用户预先存在的 `.gitignore` 中 tmp 规则和本 Review 自身。共 53 个文件，完整内容清单见末尾。按路径排序，将每行编码为 `SHA256 + 两个空格 + path + LF` 后计算的清单 SHA-256：`f202147f45f9419522b8482166a173b714050adcdb2007cc6850c5859149ca9c`。

Reviewer 仅创建本 Review，没有修改实现、Plan、Progress、索引或用户文件。此处 Plan 的 Phase 3 尚未勾选，等待本门禁后由主 Agent 对账关闭。

## Verdict

`PASS`。分段审查及收口发现均已修正并独立复核，最终 Diff 没有剩余 PLAN-013 阻断。可进入本 Plan 的 Close 与 Commit；该结论不表示 OBJ-004 完成，也不代替 PLAN-014/015 及其真实验收。

## 发现与修复复核

### F-001 / P2：Acceptance Bash 规则不能把通配和 shell 展开当作字面入口（已解决）

首版只按 `Bash(...)` 外形识别，接纳 `Bash(*)` 等无界规则。第一次收紧后仍允许 `Bash(./script{1..3})`，Bash 会将其展开为不同文件，与未来 trustedFiles 的字面路径绑定不同。

最终 `config.AcceptanceCommand` 只接纳字面仓库脚本及一个可选的尾部参数通配，拒绝路径逃逸、纯通配、控制字符、复合 shell、花括号展开等；返回仓库相对入口供后续 Acceptance 复用。负例及配置包 race 测试通过。本 Plan 提供角色配置合同，未声称动态 Acceptance 已执行。

### F-002 / P2：执行身份不能借用调用方的可变配置指针（已解决）

首版 metadata 保存 Invocation 的 ExecutionConfig 指针，Start 后调用方修改模型、effort 或工具 slice 时，异步写入的 Session binding 可能不同于实际 argv 与先写入的 Invocation。

两个 Provider 的 Start/Resume/Plan/Review 入口现在深拷贝配置及 Tools/AllowedTools，后续校验、参数和身份使用该副本。冻结回归让子进程等待，Start 返回后修改调用方值，再释放进程并检查持久 Session。Reviewer 独立运行该回归与最终全包 race 均通过。

### F-003 / P2：所有必需 Harness 检查失败应保留准备分类（已解决）

首版只有正常 finish 包装 ErrRequired；symlink、非法路径、读取/大小错误直接返回普通错误。Reviewer 路径会将其归为 ReviewBlocked，并可能按 FixWorkItem 对 Implementer 自动重试。

最终 Discover 的返回出口在 required 场景保留 ErrRequired，设置 compatible=false，并携带原错误诊断。Engine 现有 ErrRequired 分支禁止 safeRetry 并创建 project_harness_required Gate，明确准备项目局部基线后创建新 Goal。必需 symlink 分类回归、缺失 Harness 的 Engine 场景与最终 Harness race 均通过。

### F-004 / P2：两条 doctor 入口应一致汇总 Harness 缺失（已解决）

API 的 passiveProfile 最初只输出 harness_error，Service.Doctor 没有汇入 unmet_capabilities；离线 Inspect 会汇入。最终 API 补齐汇总，同一测试核对两入口均列出两个 Provider 的必需 Harness 准备错误，且不创建 Adapter、不跟随外部 Git 环境或调用模型。Reviewer 最终运行 Control/Doctor race 全包通过。

### F-005 / P2：可选 Harness 缺失也需要准备说明（已解决）

最初可选缺失只有 manifest absent。最终 Diagnostics 增加所选 Provider 的根入口、Skills、manifest 和 doctor 准备指引；optional 继续执行，未安装不被提升为必需。文件系统回归及最终 Harness race 通过。

### F-006 / P1：Provider 认证环境不得传给项目 bootstrap（已解决）

主 Agent 在收口时发现 prepareAttemptEnvironment 同时把 Profile 环境白名单交给 Local Prepare 与 bootstrap RunCommand。显式转发 Claude 认证环境后，项目 bootstrap 也可能继承 Provider 专用值；无 bootstrap 的真实夹具不能证明此边界。

作者用哨兵环境先复现错误，再从准备函数及其调用中移除 Profile 参数，使 bootstrap 只使用 Local Provider 的最小环境。Provider 的 profileEnvironment 路径独立保留。Reviewer 核对这两条环境流及技术合同，独立运行哨兵与原有“bootstrap 修改源码，即使非零退出也判定漂移”回归，race PASS 17.338s。项目环境的后续声明能力仍由 PLAN-014 拥有。

### F-007 / P2：未实现的 CredentialSource 不应静默使用既有 CLI 路径（已解决）

凭据边界对账发现旧技术合同声称显式环境必须经过 Secret Provider/Gate，但当前没有该平台；配置又接纳 secret-provider，却实际继续使用原有 CLI 环境。最终技术 §12.9、§19.2、§24.2 明确受信 Profile 可按名称授权既有宿主 CLI 环境，仅在进程输入转发，不持久化值、不授予项目 Secret 权限。配置只支持 cli-session，对 secret-provider 给出未实现及可用路径诊断，避免暗中更换来源。

作者负例先红后绿；Reviewer 独立运行最终 Config race 全包 PASS 1.226s，并确认项目 bootstrap/Validator 不继承 Profile 列表。L0 对同 UID 及原生 CLI 子工具的隔离局限仍保留，没有将白名单描述为容器级安全。

## 最终系统关系检查

- 配置使用同一 Effective 解析；角色显式绑定优先，缺省保持确定性选择。Planner、Implementer、Reviewer 均贯通 Profile、model、effort、权限、工具和 CLI 版本。Codex 的 never/角色 sandbox、Claude 的 dontAsk/明确工具不随模型选择扩大；冲突字段明确报错，初始化及示例同步迁移混合角色的旧 sandbox 配置。
- Invocation 记录请求配置与输入引用；模型未填写保持 native-inheritance，不冒充原生实际模型。主动 Probe 使用相同 model/effort 但明确记录为只读窄权限角色；它不是对 Implementer 全部工具行为的证明。
- Resume 重新 passive probe 当前 CLI 版本，要求显式且匹配的 model/effort、权限、工具、委派哈希及原 owner/Tree/Packet/Schema 身份。缺身份或继承值不明的新请求不能恢复；新增可选字段保持历史 canonical 对象可读，新身份不会自动认可旧会话。
- Planner 在 Revision 尚不存在时使用持久 request hash、generation 和 input Tree；Work/Review 使用各自 Packet 与原有 owner。合法 Claude 原生 is_error 归为 Provider 不可用并保留有界脱敏原因，不伪造结构化角色结果；错误 criterion 引用仍由 Kernel 拒绝，提示示例只改善输入表达。
- Harness 只盘点项目局部入口及 AutoGo schema 3/core 清单声明的知识，限制文件数量与体积、拒绝 symlink/逃逸/非 regular 文件，不安装或覆盖规则。清单兼容不等于 Skill 语义质量。
- 三个角色在各自 Provider 调用前执行 Harness preflight；Packet 纳入路径和 SHA-256。Codex developer_instructions 与 Claude append-system-prompt 注入同一委派职责，并写 delegation_hash。发现、路径输入和原生读取证据不混淆；load_observation 保持 unknown，原始公开事件可以供后续观察使用。
- 委派职责保留项目业务知识，避免 worker 新建竞争 Objective/Plan/Progress、操纵 Git/环境或自行宣告系统完成。SQLite 仍拥有运行状态，Git 仍拥有内容身份，文件仍保存不可变输入与运行制品；没有新增可写状态源或迁移。
- README、产品 Evidence、设计、示例与已实现范围一致；用户预先存在的 .gitignore 修改排除。仍未实现的服务/Acceptance、实时上下文与一致导出由后续 Plans 拥有。

## 验证 Evidence

Reviewer 独立运行：

- Phase 1 的 config、adapter/...、review、planner、doctor 全包测试 PASS；配置冻结的两个 Provider 定向 race PASS（Codex 2.795s、Claude 2.643s）。
- Phase 2 的 harness、protocol、adapter/...、planner、review、doctor 全包测试 PASS；`go test ./internal/orchestrator -run TestEngineStopsBeforeProviderWhenRequiredHarnessIsMissing -count=1` PASS，0.775s。该场景验证 Waiting、零 Attempt、零 Provider 执行与 Git 身份保持。
- 最终 `go test -race ./internal/config ./internal/harness ./internal/protocol ./internal/adapter/... ./internal/planner ./internal/review ./internal/control ./internal/doctor ./internal/projectinit -count=1` 全部 PASS：Config 1.304s、Harness 1.760s、Protocol 2.248s、Claude 24.002s、Codex 32.884s、Fake 1.582s、Planner 2.086s、Review 19.369s、Control 31.789s、Doctor 3.142s、ProjectInit 4.993s。普通测试未请求真实 Provider。
- 上述全包 race 之后，F-006/F-007 的最终定向复核为 `go test -race ./internal/orchestrator -run TestBootstrap -count=1` PASS 17.338s，以及 `go test -race ./internal/config -count=1` PASS 1.226s；前面的全包结果不冒充在这两项最终修补上重新运行。
- 最终 `git diff --check 3e8016d` 无输出。Review 新文件另做 whitespace 检查。

作者执行并保存于产品 SPEC 20.9 的 Evidence：

- Phase 1 Engine/App/CLI 全包 PASS（265.045s/27.565s/142.891s）；Phase 2 双 Work→Review→Promotion→Final 链路及 Harness 回归 PASS（45.496s）。后续局部修补另有定向验证，不把早期全包结果说成在所有最终文件上重新运行。
- 最新 Config/Harness/Protocol/Adapter/Planner/Review/Control/Doctor/ProjectInit 与 Planner/Harness Engine 定向测试 PASS；`go vet ./...` PASS。F-006 之前另一次 Engine/App/CLI 全包 PASS（253.024s/27.577s/136.782s）；F-006 后 bootstrap 定向 race PASS 17.611s，并运行 Orchestrator vet。
- Codex CLI 0.153.4、请求模型 gpt-6-astra、effort low：真实三角色及最终断言测试 PASS，148.69s；最终 Tree `7a65186e75d8b303dc4173955650ca943aa896b7`，最终 Evidence Set `evidence_set_final_d6d638dbc162facda4c1298b`。
- Claude Code 2.1.235、请求模型 sonnet、effort low：真实三角色及最终断言测试 PASS，88.04s；最终 Tree `fa89c749423e66dbfbdf322a7daefb9a3766a9db`，最终 Evidence Set `evidence_set_final_31a848ae90e161835663962f`。

Reviewer 阅读了真实 CLI 测试及其断言：它检查实际文件字节、用户 HEAD/index/worktree、独立会话、Planner provenance、三个角色配置、私有 Integration Tree、最终 Evidence/Receipt 的相同 Tree 与 PASSED 状态，以及退出后不存在未确认进程。成功夹具按测试合同清理；Reviewer 没有重复发起模型调用，真实运行结果归作者执行 Evidence。

Claude 两次先前实验分别因认证环境未转发、criterion 正文误作 ID 而进入可见 Waiting；最终测试明确转发环境变量名称并修订 Planner 引用提示，未记录凭据值或改动全局配置。这些失败保留在产品 Evidence，不被后来的 PASS 覆盖。上述实验只证明所测 CLI/模型请求组合；不证明别名最终解析、模型理解规则、全部 effort 组合或完整公平性 Benchmark。

## 下一路由

主 Agent 可按 `autogo-change-close` 对账 REVIEW-052、实际验收、索引、Plan/Progress 和 Git，关闭并提交 PLAN-013，然后继续 OBJ-004 的 PLAN-014。若收口引入实质代码变化或新失败，应回到对应 owner 修复、验证并更新 Review。仅完成勾选和索引投影不改变本次代码结论。

## 被审内容清单

```text
691731ba9f0b6f0b4b261759a437d85806d895a53ab659fe09220bc51803d993  README.md
8b7f6f5989be97305151e42fbf2da5dbd7585e092e6cc39379b215eea63869e1  docs/architecture/DESIGN-007-runtime-harness.md
4f02b3d8e21b5745a611bc29065317e05d0ec1de44a1249a0363ed53bb5c917d  docs/plans/PLAN-013.md
82706bee4ff006f5a8ea97a72905d013105c60a6d58980f46d6eab46d115b29d  internal/adapter/adapter.go
bd3d1c3cbb6d1205bc2b05d458c0c1b0d4ffb495adc4da384b9fe8c1a9a21fdf  internal/adapter/claude/artifact.go
ccc07bb3de16d338045126f47022738b16d5e4fb5d6f48248b780593f8bdaefe  internal/adapter/claude/claude.go
2bb5247cf636de8e6fc9e1ba97bba63303698aa2fc63858ad0b58f4e43733de8  internal/adapter/claude/claude_test.go
9068e50e58fdc5a0c07b0418e241150272eda6dc18c6ae28581cb0894b4e7a58  internal/adapter/claude/execution.go
5ff0278d06c45fb762df307d5f981210b69ce2c130fd92289aa6b6facfbc56e3  internal/adapter/claude/parser.go
7a8b422b7ca22376f1f6ce2bdc036d68f4e270506dde8525549c2c3f3608f267  internal/adapter/claude/parser_test.go
0337d5505dbf660412188c35a50e9dc96dcde1c54def5f5022e1074050504411  internal/adapter/claude/planner.go
cccda78e4b62919150ab09f6e8f11548ad4d193b2aa9e7993a896202a8c472a7  internal/adapter/claude/review.go
bbcda00ba7e116848e91d48c4b010ebd7981558c31c647939e7302840135ffb6  internal/adapter/codex/artifact.go
0898419509012d08a75c2558dac0662c3b84210010adc04e4ccc30e87eb89520  internal/adapter/codex/codex.go
96a36df8f5d9b8526cee61837488220c36ed0e4eafb4378a2a2f8f7d2857784c  internal/adapter/codex/codex_test.go
eaeafeadad43c132fe4fbccca60aecb350bbf246b9cc019debaf33260870f9e0  internal/adapter/codex/execution.go
81df055459cd5f9d1f798c826b90acdab1e3cc0d68228a6d8ccacfa81cbe2419  internal/adapter/codex/planner.go
2926cae257033a05b9796663059e20b5113d7f9b1e9a3774e732367d383bb9e2  internal/adapter/codex/planner_config_test.go
56e11bc9d9e0d47e64d069454992b2e9f45f101dfb9762842a3739114ded1c09  internal/adapter/codex/real_smoke_test.go
69aa328012bd671413778af9604c4d5f6ee69883f034938db8a814517a2158e5  internal/adapter/codex/review.go
7191ff74d41a407edb1abf6dc9bbba05903006b713c5834dd57557274de31cad  internal/adapter/execution.go
1b19cff3d54fd27eddc378177e6c52fc02e6d4faea9b92a4f8687af1e0e689fc  internal/app/app_test.go
f1987ba606d2ef8d695b9c320b969d20715a69eecbdbf2a6c1bac9be3b906420  internal/cli/current_directory_test.go
51fff1644758b707b9411f9a8b94b0b339c4f48fde37fb93366ade83f46690c7  internal/cli/real_goal_test.go
4fa20648039a2c1dc3a846a0f92cd2de33306929ecb582cbb6524216faf034be  internal/config/config.go
82285bcb21abbb41cb6b28c6bd2a87bc09578782fc21ce7356d9380660471774  internal/config/profile.go
bfb769adf5d949731165ae0e7f1c8c7bedbb2e1b0b1fdde168a9781c5c9a5d34  internal/config/profile_test.go
b1fd64f08d57731f5bec313f0e0c89c803456db8670eb5096f5e4c83d1e12959  internal/control/doctor_test.go
49f95bb3d2b5a0bff1db9ac5ac67cac4617dfb2048147f04e99b21f2c15e1d84  internal/control/planning.go
47b1ab9f1a0c3e7ae487e0fc8fcbfa94105602356acf6f4f230c76fe3fcce5da  internal/control/service.go
29fdd2f34a769382969fee594b7500398b1fcdbe95870bf919beb17691cf6307  internal/doctor/doctor.go
8370f118cb1ac8422a64f2243b8c1a75afd9ed6d35a1e079671b93b9bf25f8f3  internal/harness/discovery.go
738c34b4622a7a8881268e9b8d6bfc25cdb0b11d0216cdec0d471da611fe627b  internal/harness/discovery_test.go
f001f79e4b5ad595d2e4bea647be17d4bb5a4f4a1c5cfa5eca74a349029e0326  internal/orchestrator/bootstrap_test.go
efe59d935b5cd652a686cfb2d07382f0744432cd2ae51feb491e924b954910d5  internal/orchestrator/execute.go
65f3f88aa03a50a0160b92071c4b534d0814fccc468329a4a7d39a5f70f3f3c1  internal/orchestrator/failure.go
a5fab490d2f5bddba5b88965c74fe0507a90d8da7b2b50a5665d7d6891156acf  internal/orchestrator/helpers.go
81052c96edcd0b8e67cbf9d55125018c8e7f0a1c4e513e89772013be3fbdec2c  internal/orchestrator/orchestrator_test.go
e62c1cb4187f94ea9305bb1cd17424ea3ffbe7d2e41ae4445c709ee53cccc877  internal/orchestrator/planning.go
0f6b2161793cb74be7eca7252c513787fea9aa2ae11359932c84f777c777b961  internal/planner/invocation.go
704f41d8d7ec0c544bf91fac01cc2df88a5002fe6c7b88a1d2cebdc39a871868  internal/planner/planner.go
445e751c249bc0107a1107ba8c88934e0e1d697847993bd95fd5ba4c43835875  internal/projectinit/init.go
1ea98e2385b0f675adcac12d26844898349f9d939c0374b3b968de232ed51a8b  internal/protocol/harness.go
b80ad70e5b5e51cc18deff6adc74d1b1750b00fd31c75fa46d16b07cee095515  internal/protocol/protocol.go
d20f9faed9325e0dea54b43a0232de2d80ff816b220e6f6787a3b4c39a3a9993  internal/protocol/protocol_test.go
1f1d465bbebb0d81b79e345f17bcca445c9c5a6c637473fb2882024d0927fe45  internal/protocol/review.go
f5465ce1851b3e77a3c4077833077c6c5a78860bf9891b2aad2a8875725c3341  internal/protocol/schema/review-packet-v1alpha1.json
f7befde5b09d74a8952761e4ea3101b8e76f6c119e2bc1defa85bd01a7bd4f4e  internal/protocol/schema/work-packet-v1alpha1.json
2a88b8bbdd7d7961af12b2310c6ff8638d77cb5a1da6f733c57b7fd189295922  internal/review/adapter_test.go
f50d2e547f1f6506711950ce621b9002599503294b2be895f97fd52146a369d9  internal/review/review.go
26e822183b86b287f4a2abb43b5151824bb86d8a68c0df08d59de9c484d2de9b  xgoal-product-spec-v0.1.md
9bf957663b6c6fc0fc24feed775a2b84c91588e7bd93ad3ee88935f414e4e10c  xgoal-technical-spec-v0.1.md
d05f78597edbffdf8e8a54c96d8e398a542a1ced32cbf64abf1f1a238708ab31  xgoal.example.yaml
```
