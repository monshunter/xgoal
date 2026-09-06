# REVIEW-056：PLAN-015 运行观测、用户操作与 OBJ-004 收口 Change Review

## 审查范围与输入身份

本次为独立 `autogo-change-review`，覆盖 [PLAN-015](../plans/PLAN-015.md) 的完整 tracked/untracked Diff，产品 SPEC 第 20 节 AC-HR-001–018、技术 SPEC 第 35 节、[DESIGN-007](../architecture/DESIGN-007-runtime-harness.md)、README、操作手册、项目指令与验收入口，并对 OBJ-004 的四份 Plan 进行最终覆盖对账。PLAN-012/013/014 的既有实现结论分别由 [REVIEW-051](REVIEW-051-runtime-trust-recovery.md)、[REVIEW-052](REVIEW-052-profile-harness.md)、[REVIEW-054](REVIEW-054-runtime-acceptance.md) 保存；本次检查它们与最终增量的接缝和当前完整回归，不把历史 PASS 单独当作当前验收。

基线 Commit：`fbcb073f152167e58f5bfdc09a91b73076923beb`；分支：`feat/runtime-harness-closure`。作者提交的收口前清单为 `/private/tmp/xgoal-final-gate.jb9dxv/review-inputs-final.json`，SHA-256：`8dddea513d394ad9e6ae504e67f02cab0097877ba45a5b92bd9e6b48c0c2ba37`。Reviewer 核对其中 110 项文件的实际字节、路径集合与 Git Diff/未跟踪文件一致；排除用户预先存在的 `.gitignore` tmp 规则，没有遗漏实现或测试输入。

Reviewer 仅写本 Review 和 `docs/reviews/INDEX.md`。末尾保存 110 项内容清单，其中索引更新为加入本 Review 后的版本；与上述作者清单的唯一差异是这一索引投影。本 Review 自身不参与自引用哈希。审查时 AC-HR-018、PLAN-015 的 3.3/3.4 尚未勾选，PROGRESS 中 OBJ-004 和 PLAN-015 保持未完成，尚未提交本增量。

## Verdict

`PASS`。已发现的问题完成修复和复核；当前范围没有剩余正确性、安全、兼容、验收或恢复阻断。当前完整门禁由可核对输入身份的分段执行覆盖，不能描述为原 `make verify-m6` 一次无失败通过。该结论允许主 Agent 进入 PLAN-015 与 OBJ-004 的最终 Evidence、勾选、Progress 对账和原子提交，不表示这些收口动作已经发生。

## 已解决发现

以下包含分段独立审查、实际 CLI 验收和作者对账发现；全部是已解决项，不再设置额外审批或平行流程。

| 发现与原影响 | 当前处置与复核依据 |
| --- | --- |
| F-001 / P1：失败后读取 SessionID 可能等待未结束输出；观测写入失败可能覆盖 Provider 的业务结果。 | 四角色仅在正常 Wait 完成后直接读取会话；错误路径从持久事件保留已观察会话。Finish 最多 8 次 CAS 重读，tracker 不把观测错误合并进原业务 cause。关闭观测 Store 的成功/失败负例和真实四角色索引验证通过。 |
| F-002 / P1：stderr/status 写入的中间目录 symlink 可逃出 runtimeRoot。 | 固定 root、`os.OpenRoot`、逐层拒绝链接、私有临时文件与排他 Link 发布；两个中间 symlink 负例确认外部目录零新增。公开内容使用完整行脱敏，超长行和总量受限，截断/I/O 错误不伪称完整。 |
| F-003 / P2：事件刚发布时 ENOENT 被误报成永久 gap。 | 目录快照只有发现后续编号且 next 确实不存在才认定 gap；next 已出现则留待下一轮读取。确定性的 ENOENT→publish→checkGap 负例通过。 |
| F-004 / P1：HTTP 200 后断流或错误帧可能被 CLI 当作成功完成。 | Invocation NDJSON 独立校验 identity、stream、seq、error envelope 和 EOF；Complete 要求 Next 等于 DurableCursor，且状态为 returned/failed/interrupted。终态 gap 明确 unavailable，不能 Complete。真实首帧后删除下一事件的 CLI 负例及游标重连通过。 |
| F-005 / P1：公开上下文可能接受与登记输入矛盾的元数据，或把变换后的 schema hash 当作输入字节 hash。 | 按实际角色核对冻结 ExecutionConfig、owner、generation/request、Revision/Tree、权限和工具，只公开已核对字段；`UseNumber` 保留大于 2^53 的整数 generation；所有 artifact_errors 脱敏。篡改与精度负例通过。 |
| F-006 / P2：只有 stderr 追加时，独立 status 的输出时间停滞；重复事实可能伪造进展。 | goal.get 在读状态前独立进行 2 秒、每流至多 256 条补扫，不持控制连接扫描文件。Activity 分开存活/输出/实质进展，环境去 id/captured_at，重复 Validator result 和其他 Goal 不刷新，COMMITTED FinalReport 才计入对应发布进展。真实 human wait/watch 回归通过。 |
| F-007 / P2：前缀选择可能在 watch 后续请求漂移，字面 ID/cursor 的 ?/#/% 可能改变 HTTP 路由含义。 | 首次解析固定完整 ID，后续身份不符失败；路径统一 PathEscape、cursor QueryEscape。精确优先、歧义无业务/幂等写入、既有请求在后来歧义时仍精确重放的负例及真实补全/context/follow 验证通过。 |
| F-008 / P1：决定并续作可能绕过当前 owner、并行权限 Gate 或消费后的现场变化。 | Planner、Work、Acceptance 复用各自现有事务入口；Planner 的续作与 Begin 双重核对授权和 Tree/checkout，Work 复用 retry 事务，Acceptance 新 Effect 原子消费准确 Gate。插入失败回滚、过期、未知进程、外部编辑、暂停迟到结果、单次答案崩溃重放负例通过。Historical 仍是旧事实，只有显式新授权可进入新调用。 |
| F-009 / P1：导出把合法无 Planner Packet 当作损坏，或对旧 Packet 只核对部分冻结请求。 | supplied Proposal、尚未创建输入的 preflight/crash 与已登记输入分开；登记输入强制存在且核对原字节 hash/Effect/Goal/generation。旧输入校验所有已知冻结字段、复用 Packet.Validate，并明确旧无字节 hash 的限制。模式/Validator 集合篡改、旧已返回输入丢失均拒绝；双 Goal 含 supplied Proposal 的实际导出通过。 |
| F-010 / P1：导出错误的原始路径/JSON 可能把哨兵秘密写入 API 与幂等响应。 | 导出入口统一脱敏；实际 CLI 损坏 Packet 的失败路径同时检查 API 和持久响应，无秘密残留。完整文件闭包、Validator log 损坏拒绝和源/副本独立性均有实际运行证据。 |
| F-011 / P2：Gate/Work 直接读取成功被 `mapStoreError(nil)` 包装为 500。 | nil 原样返回；真实 CLI 回归读取准确 Gate/Work version，并按既有合同接受 WAITING 的 exit3。修补前 RED，修补后 PASS 42.072s。 |
| F-012 / P2：首次 Work 的提示要求不存在的 prior；Acceptance 使用 env 前缀导致 dontAsk 拒绝。 | prior 仅存在时要求读取；Acceptance 明确 Kernel 已注入环境，应执行允许的原始脚本命令。权限没有扩大。当前双 Provider 四角色通过；先前 blocked/损坏输出仍保留为失败事实。 |
| F-013 / P2：250ms WaitDelay 把正常持久输出排空误判为执行失败。 | 确定性 750ms Writer 先复现；WaitDelay 改为 5s，进程 termination grace、归属和调用 timeout 不变，日志 I/O 错误仍传播。Supervisor/ActiveProbe 重复 3 次通过。该值不是对任意阻塞 Writer 的硬实时保证，未知退出仍受原有屏障限制。 |
| F-014 / P2：全仓真实 E2E 乱序重复 20 次与并行包构建造成主机饱和、小时级门禁。 | 经 REVIEW-055 重新 Plan Review：完整 race 单轮保留全部默认测试，20 次仅短合同，去掉 M2–M6 聚合重复。默认 GOMAXPROCS=2、包并行 1、Make 禁止重型目标重叠；两处失败 helper 改为 30 秒。保留完整服务/取消/崩溃等真实 fixture，不新增 runner/cache，不以 mock 替换覆盖。 |
| F-015 / P2：链接锁测试假设 WriteFile(0644) 可忽略 umask。 | RED 显示 Acquire 前后均为 0600，生产代码没有改坏权限。仅在两个测试中显式 Chmod(0644) 建立前置条件并检查 Stat 错误；拒绝 symlink/hardlink 且保持目标权限的断言保留。Project 全包在 umask077/022 下 race 分别通过。 |

## 系统关系、兼容与最小性

- 新 `invocations` 表及 migration0012 只保存观测投影，未替代 Goal/Work/Attempt、Effect 或 process_invocations 的状态权威。注册绑定四角色真实 owner、不可变输入和有效配置，初始 Planner 使用 request/generation/input Tree，不伪造尚不存在的 Revision；恢复和迟到观测不能复活执行权。
- stdout/stderr 独立游标、有界持久事件、单槽合并通知和批量补扫形成拉取链路。通知不是事实源；慢消费者、缺失日志、截断、错误观测均可诊断，不能影响已得到的 Provider 业务结果或被当作 Goal 成功。API 只访问登记路径，公开白名单不包含私有推理；慢 HTTP 客户端受写入期限约束。
- human status/watch/wait 保持原 JSON stdout、Waiting/Cancelled 退出码和 Ctrl-C 语义。反馈写 stderr 并限频；Next 命令保留显式 project/socket/state-dir。心跳、模型输出和受信进展分列，不用“还在输出”代替实际推进，也不把观测当成完成谓词。
- ID 解析按资源种类、字面匹配和精确优先；缺失/歧义在写入前解决，CAS 仍由业务 owner 校验。决定与继续是两个可恢复动作：决定成功但续作失败时保留决定并返回可执行诊断，不能隐藏已生效副作用；下一 generation/Attempt/Acceptance Packet 携带真实问题与一次答案。
- 初始化发现只读已知 Go/Node/Cargo/pytest/Make 项目信号，不执行发现的脚本、不安装依赖、不自动授予信任。未知项目给出准备步骤，发现文件不冒充运行覆盖。
- 导出使用独立只读 SQLite 连接和一致快照，不占用控制连接扫描/复制。快照后覆盖全库所有 Goal 的文件引用，包括 Packet、Patch、Receipt 与日志、Review、Environment、Scenario、Report、marker、登记备份及 Invocation/Acceptance 输入和冻结游标日志；不把当前选择的 Goal 当作全库闭包。
- 导出从同一读取字节复制并哈希；原始 hash 与公开脱敏投影 hash 分开。PENDING_RENAME 报告可从已核验快照 blob 生成并标明状态；COMMITTED 必需文件损坏不能用 blob 掩盖。旧未索引日志、合法无输入和已清理 marker 有明确 artifact_status，不谎称历史完整。
- 导出与 clean 共享可取消排他锁；整体时间、DB/文件/引用数量有界，私有 staging、manifest 最后发布和平台原子不覆盖 rename 防止半成品被当作完成。正在运行的工作区、私有 index、socket、凭据和活进程不是可导入的不可变制品；当前功能不承诺导入、复制 Git 对象库或接管运行中的进程。
- SQLite 继续作为事务/CAS/迁移的唯一可写状态；结构化目录提供可读审计副本，没有引入文件状态机。Profile、Harness、环境、Scenario 与可选 Acceptance 继续沿既有设计 owner 运行。默认三角色、旧合法配置/历史数据和原 CLI 机器契约保留；确需受信声明或有效会话身份的旧项提供显式迁移/拒绝诊断。
- README、AGENTS 能力清单、操作手册、设计、产品验收和 Makefile 已对账。L0 隔离、受限 Acceptance 能力矩阵、模型别名可观测性及未执行 Benchmark 均明确。没有扩大生产部署、推送、独立 Secret 平台或全自动权限。

## 当前验收 Evidence 与分段恢复

Reviewer 在分段审查中运行过 Invocation/API/Control、Exporter/SQLite 和续作相关定向测试并复核负例；最终轮遵守主机资源限制，没有重新启动全仓测试、构建或实际模型。返回“无匹配用例”的定向包不计为验证通过。以下最终门禁结论来自读取原始日志、测试实现、输入哈希及保留运行制品，不仅依赖作者摘要。

### 完整默认测试与剩余发布门禁

1. `verify-m6-minute.log` 中完整 `go test -race -count=1 -timeout=15m ./...`：45 个包 PASS，Project 两个链接锁 fixture 因 F-015 FAIL，没有 DATA RACE。CLI 累计 741.599s、Orchestrator 累计 891.008s。原整条 make 命令为 FAIL，后续成功不改写这一事实。
2. `project-umask-red.log` 证明操作前后权限均为 0600。修补只改 `internal/project/project_test.go` 的前置条件和错误处理；Project 全包在 umask077/022 下 race PASS 5.421s/5.426s。Reviewer 将初始门禁清单与最终清单逐项比较：唯一 Go/SQL 字节变化为这份测试文件；其余 45 个已通过包输入未变，因此可复用该轮当前证据，无需重复长场景。
3. `make -o race verify-m6 GOFLAGS=-v` 的剩余执行 exit0，由 `verify-m6-resumed.log` 保存：fmt、8 个短合同包与 11 个精确 SQLite 用例 shuffle20、vet、CLI/help/config、SQLite 平台测试编译、全仓 Linux 测试编译、Benchmark validate、Linux/Darwin 二进制构建通过。SQLite 原生 shuffle20 为 7.868s；随后 `-exec=true` 的短耗时项目只证明跨平台编译，不冒充原生运行结果。
4. 发布聚合入口用全仓 race 单轮承载全部默认断言，没有删除普通测试或实际服务 fixture；`make test` 保留独立全仓单轮入口。显式 opt-in 的真实 Provider 测试仍由下一节的实际运行补齐。短 shuffle 有精确存在/空集失败校验，不把所有 CLI/故障场景重复 20 遍。
5. `minute-gate-metrics.json` 中最慢叶场景为 155.24s，Engine 单场景 3m context 保持；自动修复顶层 190.22s 是两个子例之和，不伪称单个叶场景 190s。最新 race 20m 是多用例整包累计保护；当前 891.008s 在更严格的原 15m 内已经通过。普通包 15m、短 shuffle 2m，无小时级预算。默认并发限制不是主机硬 CPU 配额，实际耗时仍与环境有关。

该分段证据完整覆盖当前 release gate；没有以“跳过 race”的第二条命令单独证明完整门禁，也没有用跨平台 test 编译替代本机 race。

| 原始 Evidence（目录 `/private/tmp/xgoal-final-gate.jb9dxv/`） | SHA-256 |
| --- | --- |
| `verify-m6-minute.log` | `3528285b00f428a018e8d9115f7dfafb80b2a60dbd54a76eb5c561bba2c03f2a` |
| `minute-gate-metrics.json` | `a87c4b6f368908a3a6e8cc8c47f43f3de5829410bb5108c888a0ec4bf89f8d54` |
| `project-umask-red.log` | `7ea24112a523504f7ee59151df985200b713ca031792e788f11aca105fb4f878` |
| `project-umask077-green.log` | `c1f8a1b6dce22b13da401ba1db07a8d48775eff4b0fd3bae944e724e5e108c7d` |
| `project-umask022-green.log` | `a156d87c1e8178cbdf2e8552ad34a08f262acb141e5bf1d28c1b9fa36f7ddfbf` |
| `verify-m6-resumed.log` | `70c847fafa0db731984f25aa9286ec8f0120058a28feec723a66bb4b752b534a` |

### 实际 CLI、服务与模型

产品 SPEC 20.9 保存当前真实 CLI/daemon 的 live context/logs、断线游标、人类状态、三种 Gate 续作、完整双 Goal 导出、损坏拒绝和正在运行 Work 的快照证据。Reviewer 已检查对应断言；故障 Provider 使用可执行 fixture 的场景不冒充真实模型，但 daemon、进程、HTTP 服务、客户端、Git 和 SQLite 是实际运行。

- `TestRealCLIExportsLiveWorkWhileLogsAndLeaseContinue` PASS 10.091s：源 Work 保持 RUNNING/ACTIVE lease 时快照，源游标和心跳继续增长；源取消、进程结束后副本仍为快照时 RUNNING，未吸入后续日志。
- `TestRealCLIGateContinuationPlannerAndWork` 最终 PASS 42.072s：用户入口读取 Gate/Work version、回答并续作，实际新进程读取原问题和答案；WAITING 的 exit3 没有被改成失败或成功伪装。
- Codex CLI 0.153.4，requested `gpt-6-astra/low`：当前四角色、运行中 context/logs 和最终 export PASS 153.042s。最终 Tree `2a54b5b88d98072acf457c8a4d12e9f95d02571d`，Evidence Set `evidence_set_final_93e536cd101663d972bb62ab`。原生事件没有报告实际模型，observed 保留 unknown。
- Claude Code 2.1.235，requested `sonnet/low`：相同范围 PASS 153.459s。最终 Tree `1daf47caaa0fbc6ea302ee1cf015f1e9b7cb4526`，Evidence Set `evidence_set_final_2c5bc5e82f12f7711ab18b6b`。用户现有通道公开报告 observed `deepseek-v4-flash[1m]`，本结论证明 Claude Code CLI 调用链，不能宣称实际 Anthropic Sonnet 模型通过。
- 先前保留的真实 Claude blocked 现场 `/private/tmp/xgoal-real-goal-2535447384` 经重启后的 CLI approve/resume 完成。Reviewer 独立只读查询其 SQLite：旧 Claim 仍 blocked，新 Packet Prior 指向原 Invocation、携带相同已持久决定；Gate APPROVED/ALLOW 且 used=1，新旧 config/Tree 相同。Goal COMPLETED，未确认退出的归属进程 0，Evidence Set `evidence_set_final_96d7f9c15f8fd6e08e617293`。完成导出含 7,282 文件，manifest SHA-256 为 `da2fdb1f05a9d5c82c96f9c07666588d8970af9058cb6e55f29ce60c3cd34b03`。

成功 smoke 的临时夹具按测试合同清理；保留现场和产品 Evidence 记录失败与恢复，未忽略 blocked、dontAsk 拒绝或损坏原生输出。上述实际模型和 CLI 结果对应当前实现输入，后续仅 Project fixture 与门禁预算变更不改变该运行链。三组真实性能 Benchmark 仍为 `NOT_RUN`、`upload=false`，未推送或发布。

## OBJ-004 / AC 覆盖对账

| 验收范围 | 当前覆盖结论 |
| --- | --- |
| AC-HR-001–005：角色、业务断言、受信入口、环境与进程回收 | PLAN-012/014 已关闭；当前完整 race 保留三角色、可选四角色、服务正向/故障/取消/崩溃、篡改和最终 Evidence 负例，真实四角色与服务 smoke 补充当前用户链路。 |
| AC-HR-006–009：Profile、原生参数、会话身份、Harness | PLAN-013 及本次四角色 Invocation/公开上下文对账；实际请求配置与可观测模型值分列，未知值不造假，Harness 路径存在不冒充模型已理解。 |
| AC-HR-010–012：失败、人工续作、自动修复 | 当前 Store/Engine race、三类真实 CLI 续作、实际 Claude 跨重启回答与一次消费；有界 retry 默认关闭，不安全/无进展/未知退出仍阻断。 |
| AC-HR-013–015：实时观测、人类状态、定位/补全/初始化 | PLAN-015 Phase1/2 当前实现和真实 live/reconnect、stderr-only、版本读取、明确缺失/篡改、ID/取消合同。 |
| AC-HR-016–017：全库导出、兼容与持久权威 | 运行中快照、全类型及双 Goal 闭包、损坏/限额/不覆盖/clean 负例；migration0011/0012、历史数据和原有完成屏障回归。 |
| AC-HR-018：综合门禁、当前 Provider 与文档 | 本 Review 的分段全仓门禁、当前双 Provider 和保留现场证据成立；可在追加精确最终 Evidence 后勾选，不把原失败命令或未运行 Benchmark 写为通过。 |

用户五项原始问题均有对应 owner：保留默认三角色并支持可选 Acceptance；共享 Profile 表达 Agent 能力及角色绑定；Harness/确定性测试/反馈恢复闭环；SQLite 保持事务权威而导出提供结构化可读性；用户操作和观测有实际 CLI 证据。没有为“完备”而新增平行流程状态机或泛化平台。

## 下一路由与收口边界

主 Agent 可按 `autogo-change-close` 追加本次分段结果到产品 Evidence，勾选 AC-HR-018 和 PLAN-015 3.3/3.4，对账 PROGRESS 中 PLAN-015/OBJ-004，再创建隔离本工程意图的原子 Commit。仅这些结果记录、完成勾选、索引投影与提交允许沿用本审查；它们的收口后 hash 自然不同于本文的收口前身份，不得伪称输入字节没有变化。

实现、测试逻辑、权限、验收范围或恢复语义再有实质修改，或出现新的失败，应回到对应 owner 修复、定向验证并重新绑定 Review。保留并排除用户 `.gitignore`，不自动 push、merge、发布或部署。当前没有缺失的本范围必要 Evidence，也不要求无变化重跑长场景；本次 PASS 不代替最终 Git/环境收口的实际确认。

## 内容身份清单

以下 110 个输入按路径排序，每行为 `SHA256 + 两个空格 + path + LF`。清单 SHA-256：`c175fc07f69704d4208d5f8f602d9c310b39acb0814c4452f674f0c46c937f16`。

```text
3dc0bc3d2031f72d6667e70ffa48f3962f49dd692e23faeb09513add51275a1e  AGENTS.md
b3bf3fda14a7af3a63cf58f27d576d03a595d4ef15f096d161c1b5718272243a  Makefile
aa04f014705e254a218ec95b8aa230c719e167b77ee4b9047150dcb40cbbe754  README.md
f5147448a01ca10a93f7bc8a22d889f5e3d27cfee88b43a699b0da927d673fe8  docs/architecture/DESIGN-007-runtime-harness.md
5a46837c559f966e729d4654a890aefdab9fb50017218b27d63a09141a3b3ac3  docs/bugs/BUG-002-verification-host-cpu-saturation.md
510394a4beb83b148c7c86a3eff97183fd719372d5e3cee930935faffbc98445  docs/bugs/INDEX.md
a9a805283beb5a33784995c50ae3f5813f278660c25c2f658530872102773b3f  docs/operations/V0.1-OPERATIONS-RECOVERY.md
5488c628e4233772be5bfecb8824ef0f47465e01f8d45ce1a2d020fe64cfa69f  docs/plans/PLAN-015.md
6cbdbbe06fdb4481ff30b41b8e2099592f84d919fbaaf248cfa91df20631905a  docs/reviews/INDEX.md
b67a19768fa7ead17164e8f59872eb985af5221a7bc9fd1ecc1ce3fbe2213399  docs/reviews/REVIEW-055-plan-015-verification.md
5985c4cf1999574bd88823e4149187532ecdfaf744ea9bcb1b785c592d20a7ff  internal/acceptance/acceptance.go
edaca477b1d5b7a0ec9b2a38650ca548e0525e4096d70f5044f92c11cdd26c5f  internal/adapter/claude/acceptance.go
e1d2905284ed15f8ec53f60e7e4ff3c4fc1f8bf9084bffc2e3d90d0e3af958f2  internal/adapter/claude/claude.go
e640ced5c3c7532b6f2e56c6d2fc3410d05857629a2913362e1373854700ae36  internal/adapter/claude/live_logs_test.go
c9067616fe7b250738139370ae59dd7365269bc51e1d6e9e3f5f302d56afb0c0  internal/adapter/claude/observability_test.go
466748e5cb01f5bbae26b31b5e05bd1151d3877f94267e41005f79a5a7cb871f  internal/adapter/claude/parser.go
7c04925a482ec3fbcbe5050872f1b0faf16978289d5875271bcba8c8d25ca250  internal/adapter/claude/planner.go
1cdcfeeed8012edb7d50da10c7d22ce56be763ce7355ad520c7552a9388a56c3  internal/adapter/claude/review.go
aecd71df972808912bf423ba55b5930ab7cbf369157856d4eb2a1d320a4d06f5  internal/adapter/codex/acceptance.go
031ae417c62ce51a5524d06c6a1b7f5d65b6094db06bb52256c26dad2590657d  internal/adapter/codex/codex.go
0b702685d425dd37c811001b8f1a50d61e873cc30c84c1d18cc57c0610e3cf34  internal/adapter/codex/live_logs_test.go
e97680c4b9c11e400ece862f4806566964c25774259cca9c29ce891253735452  internal/adapter/codex/parser.go
08e71215479f363f336f19b9eb3d5ff706d92a1924d9b9c96871fb1b2d8b7179  internal/adapter/codex/planner.go
37b3b8d71b65e35ff423b60b532a9c05108015b18b4b1309beb002e5d2b94c6d  internal/adapter/codex/review.go
70cc68ccf0bce372370bb0b1da8701ce49598bc3b1922b8332e7ef91dbef8177  internal/api/api.go
6f87b722e7eec26ce1712622636cff220dd4b35c4c27fef5824e6055ecb60485  internal/api/api_test.go
754029a020c044fd026e7877746918daaa30741ef0fde86b662cbe5828c24e5a  internal/api/client.go
6c98a8e8afe0ed283b7f94cd1d6d4ee73c726ced1c692360fc4ec2e48cd6966f  internal/api/invocation.go
d2573625968c4fa31e2286b10050bba205b733d4c463051026d0913acf33eae0  internal/api/invocation_client.go
25dbd8f6b48ca83ab45426707d6752924bee27ebd036271151c55a2a1b1c3875  internal/api/invocation_client_test.go
555843506efa25663ea88d6d8d50d6692f9e2dc7c9f21ae6b656ee33c33b1ce0  internal/cli/acceptance_test.go
98fd83ed0a47723ae7ca98f806a461c466ca3b85e36f0cbfd0190adf78f88f97  internal/cli/cli.go
a1aa9665d0fe0d23859b48a0b6a71e71db1d08941f9d32032fa1680c84de6d27  internal/cli/commands_api.go
d0f3b88b25e13f7ff84cd0a0645125f1c5b0f29ccb16973cd19ad10e5f909f27  internal/cli/current_directory_test.go
dd8f2d82ab92c2134c73292bf578b9c99ab9d466643286f9e60e535df3e107b4  internal/cli/discovery_runtime_test.go
57ebfbcabf87375621451298252287f8fa6dc4c30e4ac1d7611097f49b8e687e  internal/cli/export.go
26363f690b706a506e1b6dd48c00409cdc3933750306797a4d171f3f09a7e4ca  internal/cli/export_runtime_test.go
c9b0ae5d8e85ac3b6eae6023572c5ffe8419ffa6a336b04cd0f38c0cc8bd083b  internal/cli/gate_continuation.go
8f8e44f942a1c835408e18588eda1f218d80f26a46da69d47f767e59da3f40ce  internal/cli/gate_continuation_test.go
095df8f4649aa1008623f5e6025c2029d17c3cb4b8909c39c9f3b755737b5d99  internal/cli/gate_runtime_test.go
60b620a2d3b5d99aa5235d743c86b70d30912fd9dcabc752d7d30fc9d90b658b  internal/cli/human.go
fa4e74f9554098702579d8de5d36dfe31cb9f542bc89dca7ea0d5ddf5e014f88  internal/cli/human_runtime_test.go
74aa8fdfeeb21318461f070a17f3daf4f9f2b06911030864f5f95e12df4bbff9  internal/cli/human_test.go
9df23b60c41a74fcc8646f7a8da87b470bd53bd1995c46d74c8e8df7e7d06ccd  internal/cli/identifiers.go
e9056e562b26edcc6f7c3990c2949698672bcaf7bc7c19bcc631df9e357d26da  internal/cli/identifiers_test.go
92e6a6270207963eeb010867a100355902438f7a2c0619c49f26642610b365f8  internal/cli/invocation.go
d4ba9fb0a69989bfd7e1bc38a2c5182525885cf45a87896c8eb63610f3e87f16  internal/cli/invocation_test.go
0a892ddbdb31ee2503c0c8b91653e49b395af4443e13dfa3208d596933d9a2ba  internal/cli/real_goal_test.go
61061f884a1e0a3b94be6f2c4b0ab79fef647253a36c5778c918dd67feb81a85  internal/cli/request_test.go
b709d50118b4213f09251bafedaee5a4cb63868aeb71af9e60adc8a3529a9e4a  internal/control/checkout.go
eb4be2eea3604bc1233f3e555db94d323694ef360c76c319498c382d1a4c1dd9  internal/control/export.go
7b120155e29c8d0fa338348066f3a58c35ac1c3ad26eea4c15e10102647a8a32  internal/control/export_test.go
774c15a5e316f7fc5981d7028f7d9ed821968511cea9a4b1ecdfaf8b89a9a8c9  internal/control/gate_continuation.go
e3eb082e251d73c4f58ee09acca00c719eeeca77d81b66455d35f44e49dffbc4  internal/control/identifiers.go
0ba4333b7ac1b43d89cfc49de477a81fc73b2083837b2cfafe5d1d9cb3839151  internal/control/identifiers_test.go
1b773f6af97750643c7db2f152e16e47d0256408fe5d847935b33bfb31652512  internal/control/invocation.go
0f4be5b1df602ca210df9a8a719be66a119dc822924f1fdb1f467f2b232c17c3  internal/control/invocation_test.go
2624334c89301bf67682821d6a81f41299105834ee7832355ad87e3abe10330e  internal/control/service.go
32c14b81cef79cd1ade3631259d95e5172b93c74fea7f816131107a60891858c  internal/doctor/doctor.go
fa4ed139a5c96734300a3164d047574042e65ccc88034b05b334464c2e46162c  internal/environment/local_test.go
31aab4dc75c4ab999ecabf5503bdad7480c1c3ccb9463b7ae8aa805ab9681b21  internal/exporter/artifacts.go
b471b26a2d45275d1b4f96e348d0c73dbf36a6844629643613d345e0f424f898  internal/exporter/boundaries_test.go
075a7be1a1c02c7cfb25548df15ffdc8e03cfe12a67252d747c039e32b6c0a07  internal/exporter/export.go
245b2bb51365f22580b4e6ebc48139c7de736a24039734ad766afd23b4fce492  internal/exporter/export_test.go
2b2849d1eb6183362b3d74f4463da43c217c99961bcbe1d5be26244192a82aaa  internal/exporter/rename_darwin.go
2415c3c692e93cc6d508528cf682c38a1a9a9d12e2b9f092bdd3438b3414e2c7  internal/exporter/rename_linux.go
c52b82f8f1e4b76c6d8a88d9c63cf269fb431093ca87dc1f22357bbc43cfb723  internal/invocation/context.go
a543a55e00db603d7bb9926791571ee5114db626ef42c053b247f957efff4d2c  internal/invocation/context_test.go
c0688d52b5c51781e1d297f4f9c2316c3a690515a022feedd6e0e4c3ffc8c859  internal/invocation/files.go
3244f830a065353c32f4d360d71bde2de18e59b63854a8e67ae4d89e04c0d60d  internal/invocation/files_test.go
e553d2358093286e149b9eedeabdf3690ba2961461e14130d3b2a7a7a2a2dbdd  internal/invocation/invocation.go
221f9e8c584d9500a2e39974ff4b4035f90678762eae1e31fb2bd3d7bc95379a  internal/invocation/notify.go
445f6e43d09ba1c23b93a2ac9dddb603a9faed9b87e57d4abc76b66513a532a1  internal/invocation/stderr.go
6dea5a734f2c479c73fc81b6226fd4c0f6637a63f8451db4f017ac3b842bf500  internal/invocation/stderr_test.go
ba40506fdbc8c2cfd5186331e7fa56769f81eb306115f1329cceefaa31b11282  internal/orchestrator/acceptance.go
010c208eb72b5361f52494ee2a5b7b02b70ea3ce4561863c624b469e70aee7b1  internal/orchestrator/engine.go
6f78de62f37ce663f2eaaaac25a7d324e64896ed57e57533a47fe5e4a0048409  internal/orchestrator/execute.go
9afa5695ccb6c6eb2c8d449cf1611525c1d38dcae852493d49a933129cdd2cdf  internal/orchestrator/invocation.go
8b32d1b6c0d64bcd66026308535c575f3c46dda12b9a13d7d19c2652aa3818c5  internal/orchestrator/invocation_test.go
f4cb75bae301526e101d73f92460406b0002fe0cd8c9f6f42d83eb70bcf97a35  internal/orchestrator/planning.go
4c815d53be5d547326708f8fdf7d7b5bd2c38a73cad31a731d23f332e75e82c8  internal/planner/planner.go
9d9380e5bfe0dee1c0f58a9ba5d4521a00bdf1e25ba3ee6d7acad8f1f780c985  internal/planner/request.go
f235440ef9ab3ebdf50e527ebe058cb88edf402a3a1479ccf5707669adf51637  internal/project/project_test.go
9aac31f74ebce7e24cee441c2fdee4e1d55d3637eee18a31107de23ba8de1898  internal/projectinit/init.go
4950c6945aa2c9585e5c7408e21f4a207b6f3e3bbab8a084e8aaee6f6383cf49  internal/projectinit/init_test.go
981ac06ea4523d30befb44613b609e4e747ddb7cf3e89f880982da1c2c00c1bb  internal/store/sqlite/acceptance.go
94ee86375d7c2bbe1c41521a9c950cf923f9725e8a9211a545c078e32a0b05f3  internal/store/sqlite/activity.go
a31bbbabc0c10ca12a2d091c45bee92022b2199e9c7971a639e1aed83f40a9c4  internal/store/sqlite/activity_test.go
143de099eb1f3a684f316b1887644169bbae122e6136b608d82966f339a3cf52  internal/store/sqlite/checkout.go
5dca134531a548ca49be195692b5ce1507f8d6a1d6d352dc0b941663ef24b758  internal/store/sqlite/export_references.go
c30e32ecd3a08a5e1fc10f1e9585c9883eeb1f8db259fc91d26cc4b795e4a588  internal/store/sqlite/gate_continuation.go
a240c0ff62d36781eb28e9628f5acad0bf96e258e4cd38ca923f923e86d54769  internal/store/sqlite/gate_continuation_test.go
b4615f5834a1e69ab152ec1cce7098781f791a14150b899223f9e31bc5827487  internal/store/sqlite/identifiers.go
5a50632cf066dce3c03718dfbe626c88cb223361b953dfebd8df58b375eeafd6  internal/store/sqlite/identifiers_test.go
4441681ce405a6e26ab38256d967e60f67583fd96d4f0973a77da80bc16dd4b1  internal/store/sqlite/invocation.go
468eec6463286fc215cc5d02aff1b8d1f0f3e07c5b34b1a7551dadc54f3e4b40  internal/store/sqlite/invocation_test.go
37b04d97e52bc0d37bbdc0d15f9ad3b19099bdb30e7b46aa12fbab13d14563d3  internal/store/sqlite/migrations/0012_invocations.sql
5d9b6bb75605154a00059843b5f60b8a6eeca1943a9ab722a687e4eabb787d9f  internal/store/sqlite/planning.go
f2ca7918dd9b77ad3954b59e0516eb1c0079281f624f3037393f064d0041d592  internal/store/sqlite/planning_control.go
3d452ac42ac86b729a085ab63231185694fb313c249b493b6a6149ec19bff4d6  internal/store/sqlite/planning_recovery.go
8ec234166b88e0c60817e78e7866641365c7ae64bdc799dd5e7aed00b1735f9f  internal/store/sqlite/snapshot.go
2a5c86c37f830b8ab7b96edd48489b2062754bda109a6e8aefe04540a512820c  internal/store/sqlite/snapshot_test.go
d569a2d6961012503112ca08396d8b8b276936606416e2eb33babd625c5e79f2  internal/store/sqlite/status.go
cb89bb8dadaac1f12bdaacb73e1d9ca85124ac12b386f0cf389eb337de6cf16e  internal/supervisor/barrier.go
10102260ce0c38f7fd06923be4741b22d15a8e1517d814ee5f28c29ab5da8b2e  internal/supervisor/runner_test.go
de5b23bc9563f9caae6b3d8717480b1cee997296a9ce3481d6a71ede570edb2c  internal/testdiscovery/discovery.go
a50a8ac66138864cdee94be1b072e2e470200ccb6c7f7c5571a0c3ea23b1656a  internal/testdiscovery/discovery_test.go
ede46df0d9a04cfb07dc2bab7d280215e38bd049b0465df083c363593468dfa8  internal/workspace/manager.go
10aa5481f252357090394b35afe51e4c2d9e3c9ddd956644b8c389a611a19c3c  xgoal-product-spec-v0.1.md
328195315739d58622dc0e982e9f2086741cc71eaa393281dec1251ac66e9ff9  xgoal-technical-spec-v0.1.md
```
