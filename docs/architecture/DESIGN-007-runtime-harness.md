# DESIGN-007：运行时 Harness 接入、恢复与观测

本设计拥有 OBJ-004 的跨组件接缝。产品行为由根产品 SPEC 第 20 节拥有；技术不变量由技术 SPEC 第 35 节拥有。DESIGN-001–005 继续拥有 Git/环境/验证、Adapter、Review、Control 和最终报告内部机制；本设计对其新增接入点作增量细化，不复制整套架构。基线为 `a9b6192`，实施前需独立 Design Review。

## 目标与取舍

目标是让用户配置的能力真正贯通任务、执行、环境、证据、失败处理与观测。默认保留三角色；可选 Acceptance 只对动态测试增加独立会话。保留 SQLite 的事务状态、Git 的代码身份和文件的不可变制品，不引入文件 Store、Manager LLM、新的服务平台或通用工作流引擎。

系统边界是一个可信本地 Git 项目与其 daemon：单执行槽、同一主目录、可归属子进程、不可变候选 Tree。Provider/登录态、端口和外部服务处于开放边界，有超时、Probe、退出确认和 Gate；Prompt 不是同 UID 安全隔离。服务成功不代表业务正确，Agent 成功不代表交付完成，日志输出不代表实质进展。

## 接入关系

```text
Config + trusted Git baseline + project rules
    → Profile/Harness/Validator preflight
    → immutable planning request / Work Packet / acceptance input
    → Provider Adapter + process intent + bounded event files
    → stopped scene + Patch/Tree verification
    → Environment services/readiness + deterministic assertions
    → independent Review / optional Acceptance + final assertions
    → Evidence Set + Report + existing Completion Predicate

Failure → typed record + scene observation → bounded repair OR durable Gate
Gate decision + owner CAS → next planning generation / Work Attempt / final invocation
SQLite + immutable file references → status/context/log cursors / consistent export
```

## Profile、角色和原生 CLI

在 `config.Agent` 增加可选 `model` 与 `reasoningEffort`；`orchestration.roleProfiles` 为 role→Profile ID 映射。Profile 可由多个角色复用，角色默认权限由统一解析器生成。绑定需存在且 Profile 声明支持该角色；未绑定保留当前 Implementer 首个匹配、Reviewer 独立会话/优先不同 Provider 的选择，诊断显示实际选择来源。新角色只有显式配置验收场景才执行。

统一有效执行结构由 config/adapter 边界共享，不为 Planner、Reviewer 另造参数语义。至少包含 profile ID、provider、request model/effort、权限、工具、source、CLI version；缺省模型标识 native inheritance，Provider 未报告实际模型时 observation 为 unknown。所有 Invocation 的可恢复身份包含该结构及原有输入身份，CLI 升级或显式配置变化也不能默默复用旧会话。模型字符串不伪装成一份静态最新型号列表；拒绝空白/控制字符，原生 CLI 对模型/effort 组合的拒绝归为可诊断执行失败，主动 Probe 可提前验证。

Codex 始终使用 `exec --json --output-schema`，`--ask-for-approval never`，角色 sandbox 上限；显式模型经 `--model`，effort 经安全 argv `-c model_reasoning_effort="…"`，不用 shell 拼接。Claude 始终使用 `-p --output-format stream-json --json-schema`，`dontAsk` 和明确工具，模型/effort 分别经 `--model`/`--effort`。原生 plan/auto/default/acceptEdits/bypassPermissions 不作为 xgoal fast/standard；无人值守或角色不兼容的配置给出迁移错误。多角色 Profile 可以未指定 sandbox，由角色推导；指定时必须兼容所有已声明角色，不能静默忽略。

只读角色的默认工具为读取。本增量的 Acceptance 支持矩阵明确区分：Codex 支持 read-only 的无网络观察场景；需要本地服务交互的场景使用 Claude `dontAsk`，`--tools` 只包含 Bash/读取等基础工具名，`--allowedTools` 独立使用配置中受信客户端命令的原生允许规则（例如限定脚本入口的 `Bash(… *)`），不能将规则字符串误传到基础工具集合。无命令允许规则的无限制 Bash 配置不符合 Acceptance 上限；客户端控制脚本适用 trustedFiles 绑定。仍进行源 Tree 和 Git 身份前后核对。Codex 加网络场景在 preflight 明确拒绝并提示选兼容 Profile，不改成 workspace-write/full access 或静默开放网络。Claude 规则不构成 OS 级只读或端点隔离，仍属于 L0 可信 Agent 执行；配置中的项目网络授权与测试目标独立核对，越界修改使验收失败。需要动态交互的原生权限请求在非交互边界失败或转换为结构化 blocked/Gate，不维护无限 stdin 问答通道。

该矩阵避免为一个可选场景引入另一套 beta 权限/代理配置。官方 [Codex Permissions](https://developers.openai.com/codex/permissions) 支持 read-only 与网络分开，但 profiles 不能与旧 `--sandbox`/原生配置混用，域名约束还依赖启用原生 proxy；仅添加配置字段不能证明限制已生效。若以后扩展该组合，必须独立证明实际 sandbox、网络与原生配置栈兼容，不能把它视为本次已支持。Claude 工具的可用与允许边界见 [Permissions](https://code.claude.com/docs/en/permissions)。

有效配置在 Adapter 入口深拷贝，包含工具列表，防止调用方后续修改改变已执行会话的身份；兼容 Resume 另外重新 passive probe 当前 CLI 版本。配置合同支持 Codex minimal/low/medium/high/xhigh 和 Claude low/medium/high/xhigh/max，原生模型组合通过主动 Probe 或实际执行验证。Acceptance 的 Bash 规则只接纳 `Bash(./trusted-script)` 或 `Bash(./trusted-script *)`；返回精确仓库入口供 trustedFiles 绑定，不接受纯通配、路径逃逸、shell 操作符或花括号展开。

Provider Profile 的 environmentAllowlist 只用于原生 CLI。bootstrap 与 Validator 不继承此列表，项目环境由 Kernel 的独立配置生成；未声明项目环境变量时只提供最小运行环境。

继承原生模型/effort 而无法冻结完整有效身份时，只允许 fresh Invocation，resume 给出身份不完整诊断。显式模型/effort、角色权限、工具、CLI 版本及 Packet 都可绑定时才允许兼容 resume；不因两次“未填写”就推断原生配置未变，不读取用户凭据来推测身份。历史会话可读但缺少新增身份的旧会话不能自动恢复为新执行。

## 项目 Harness 与委派规则

复用 `project.harness`。发现 AGENTS.md、对应 Provider 的规则入口、项目 Skills/manifest 和引用的知识路径，记录路径及内容哈希；不扫描凭据或整个用户目录。必需 autogo 缺少入口/manifest 或不支持所选 Provider 时给出准备步骤并在启动前停止。可选无 Harness 不阻断。

为每次 Packet 加入委派职责和被发现规则摘要，原生 Provider 通过本机支持的指令入口/系统提示附加机制获得明确边界：xgoal 管理上层状态、分支/提交、环境归属和完成判定；worker 只执行当前角色。项目业务规则继续加载。检测路径、提供给 Agent、可观测读取/回执是三种事实，不能互相替代；日志中没有证明则 loaded 为 unknown。doctor 显示 found/compatible，Invocation 显示 inputs/observations。初始化可以生成项目局部、可审查的 xgoal 委派说明，但不得覆盖 AGENTS.md、克隆全局 Skills 或设置另一份运行状态。

发现器按当前原生 Provider 读取 AutoGo schema_version=3/core manifest、根指令入口及清单声明的 Skills/知识文件，确认文件存在并计算 SHA-256。它检查安装格式与声明引用，不能单独证明 Skill 语义质量或模型遵循了规则。读取限定在项目目录，通过 regular 文件、逐段 symlink 拒绝、单文件/总量上限约束。发现和 required preflight 不写项目文件。

Codex 通过 `-c developer_instructions=...`、Claude 通过 `--append-system-prompt` 获得同一最小委派文本。其哈希写入 Invocation/Session 身份；Packet 的 Harness 引用也参与输入 hash。Planner 在 Revision 尚未存在时记录 request hash、generation 和 input Tree。输入仅声明 `packet-path-references` 和未证明的 `load_observation=unknown`；原生加载没有事件证明时保持 Unknown，已持久化 Provider 事件提供可检查的读取行为。

## 受信入口

在 Validator 配置增加 `trustedFiles: []string`（相对仓库根的精确 regular 文件，不支持 glob）。Definition 增加排序去重的 `{path, mode, sha256}`；旧 `trusted_executable_*` 字段保留以读取历史定义。直接 `./entry` 相对 CWD 解析，常见 `sh/bash/python/python3/node/ruby/perl` 的脚本位置按明确的 argv 规则自动加入；不支持或含解释器求值参数的形式必须由操作者显式声明，不猜测任意 shell 语法和传递依赖。Make/npm/内联 shell 的规则入口和必要依赖由 `trustedFiles` 声明，缺少时迁移诊断。CWD 与路径必须 canonical，拒绝逃逸和 symlink；读取固定 Git baseline 的 blob/mode，执行前后检查当前文件同一身份。

`Registry.ProtectedPaths` 提供 xgoal.yaml 与全量受信文件集；Patch 的新旧路径触及这些内容均拒绝，涵盖 rename/delete/mode。失败现场观察调用同一能力，避免“失败后自动修复”绕过信任门禁。Runner 即使被独立调用也核对冻结文件，候选 Tree 中篡改脚本不能执行。直接入口的 legacy hash 可继续验证；新绑定影响 Definition hash，旧 Evidence 不能标为 current。信任更新唯一产品路径是审阅提交新基线并创建新 Goal；approve 或普通 replan 不重绑定。

自包含内联断言可显式声明 `trustedFiles: [xgoal.yaml]`，表示可执行断言全部在冻结配置内；加载其他程序文件时必须列出真实依赖，不能以此声明替代。这样不需另增 inline 信任开关，也不通过猜测 shell 文本来推断传递依赖。

命令识别采用封闭缺省：包装命令、版本解释器和自定义 runner 无显式声明即拒绝。已知直接断言仅包括 `test/true/false/sleep`、`git diff --check` 与 Go `test/vet/version`；Go 自定义 `-exec/-toolexec/-vettool` 仍需声明。宿主可执行程序、系统配置和工具链属于可信本地环境；该文件绑定不是供应链沙箱。

旧 `(config_hash, base_commit, validator_id)` 注册保持唯一且不改写。新增 TrustedFiles 使相同旧键对应不同定义时，返回 `TRUST_BINDING_MIGRATION_REQUIRED` 并让 Work/最终验收进入可操作 Waiting Gate；在配置显式列出脚本依赖、审阅提交新配置并创建新 Goal 后登记新键。保留旧 Definition/Receipt 身份，不为注册迁移扩充第二套版本状态，也不将旧 Evidence 标为当前。

该机制不冻结全部业务源码/测试，不声称发现所有解释器动态导入。验证权威只在所声明的信任闭包内成立；Local Process 不防御同 UID 恶意并发写回，需要保持现有可信仓库前提及运行前后检查。

## 合法结果、Gate 与重试

增加 FailureClass `AGENT_BLOCKED`/`AGENT_FAILED`。AgentResult.Validate 继续接受三种合法状态，消费端把 blocked 转 WAIT_GATE，failed 进入有界修复候选，只有 malformed/缺失结果使用 AGENT_PROTOCOL_INVALID。用类型化错误保存完整 summary/blockers/recommended action 和 Invocation 引用，Failure 保存规范化 fingerprint，Gate 保存脱敏结构化事实。safe scene 只证明能继续，不证明用户问题已回答，不能把 `agent_blocked` 覆盖为 `checkout_retry_required`。

`orchestration.autoRetryLimit` 是非负整数，缺省 0。自动决策需要当前 Work 的总自动重试数小于上限、已完成失败记录属于同一 Work/config、允许的 class、无重复 fingerprint+strategy 且无进展、无必要 Gate、无未完成 Promotion、所有进程/Lease 已结束以及 exact observed Tree/HEAD/index。允许首次失败反馈驱动同一 Work 修复；不以新 session ID/错误措辞作为进展。不自动处理 blocked、policy、scope、环境依赖不明、未知进程或外部编辑。

Store 提供自动续作事务，与人工 Retry 共用现场核验但不自动批准任何 Gate。事务内检查 Goal/active plan/Work version、checkout owner/observed identity、失败 ID/配置、进程闲置、次数和必要 Gate，原子递增计数并记录 kernel actor、Work Ready、checkout retry authorization。人工调用保留 human actor 和原有 CAS。计数存在 Work 上，重启、换 fingerprint 不重置；新 Work/新 Goal 才有新计数。外部写入无法与 SQLite 原子锁定，执行前仍重新 Snapshot，漂移拒绝。

Packet 的可选 prior_attempt 加入实际错误、建议和证据；`decisions[]` 保存最新失败 Attempt 对应已消费 ALLOW 的 `{gate_id, gate_version, answer}`，其他失败的旧回答不注入。决定是用户输入而非不受限系统指令；权限仍从配置/Gate action/scope 解析。工作重试事务只消费 EXEC_COMMAND 的 `agent_blocked/checkout_retry_required` 续作决定，独立权限 Gate 保留自己的动作消费者。拒绝/撤销/未消费过期 Gate 在调度、领取、重试、晋升和完成判断中共用阻断谓词；已消费决定的事实不因时间流逝追溯失效，也不能再次消费。Kernel 已解决的历史规划 Gate 取消 required 标志，与用户撤销权限分开；迁移保持原决定和事件。CLI 的一次“决定并继续”先记录决定，再进入 owner-specific CAS；若第二步遇到外部编辑，保留已生效决定并返回待恢复原因，重试不重复决定。

初始规划有 request/config hash、input Tree 和 generation，不要求尚不存在的 Goal Revision。问题/失败保留在规划 effect/阶段 Gate；续作时新 generation 消费反馈，成功 Proposal 才原子冻结 Revision。Work 续作建新 Attempt。最终验收问题保留 final owner 与当前 Revision/Tree，续作重新准备环境并新建验收 Invocation；不借用一个虚构 Work 修改状态。三条路径保留现有 daemon 项目槽和进程意图记录。

## 环境、场景与 Acceptance

增加 `services[]`：ID、argv、CWD、dependsOn、env.allow、network、readiness 的 argv/timeout/interval 与停止 grace period。配置校验重复/缺失依赖/环及正超时；依赖拓扑排序稳定，停止按逆序。优先复用 Supervisor 的 command Probe，HTTP/业务探针可用已提交受信脚本，不建插件化探针框架。冻结的是启动配置以及明确声明的控制/断言脚本；被测 `python app.py` / `node server.js` 等业务程序只绑定当前候选 Tree，不能因作为服务入口而自动加入 ProtectedPaths。readiness/Validator 的控制与断言入口适用前述受信规则。network 授权仍独立，只启动当前 Validator/Scenario 需要的依赖闭包。

准备环境/服务过程复用 Local Provider/Supervisor 与持久 Owner；start intent 必须在执行前提交，PID/start identity 在进程放行前记录。Kernel 分配项目运行目录下的独立场景目录，服务端点和临时文件通过明确 `XGOAL_*` 值注入，禁止展开任意宿主变量/Secret；配置可声明固定端点，也可由服务向该私有目录发布动态端点供探针/客户端读取。bootstrap 使用同一诊断路径并限制输出，不能只丢弃 stdout/stderr。

配置增加 `scenarios[]`（ID、描述/步骤、services、validators、artifactPaths）以及可选 `acceptance`（场景 IDs 和是否允许安全重放）；Profile 通过 roleProfiles.acceptance 选择。Validator 增加 description/scenario IDs/services。Planner Packet 提供实际能力和场景说明；冻结 Goal 编译器校验引用存在/必要验证器包含在 Criterion 中，不能从名称推断覆盖。旧未描述 Validator 标为未说明覆盖；人工给定合同仍保留，但不能虚构业务能力。

`scenarios[].validators` 拥有关联；Validator 的可选 `scenarioIDs` 只能注解既有关联，能力投影从场景定义推导完整反向引用。Criterion 的可选 `scenario_ids` 将场景绑定到验收目标，同一 Criterion 必须包含场景的全部业务 Validator；启用 Acceptance 的场景必须被映射。Work 只使用支持 `change` 的 Validator，Criterion 只使用支持 `final` 的 Validator，执行入口再次检查阶段。能力说明随初始 planning request 和 Packet 冻结；无 description 时使用 `coverage=unspecified`，不从名称推断覆盖。编译器检查声明关系，不声称证明断言本身的语义完备。

`artifactPaths` 是私有 `XGOAL_SCENARIO_DIR` 下的精确普通文件路径，拒绝 glob、路径逃逸和 symlink。断言结束后复制到 `scenarios/<evidence-id>/files/`，单文件上限 32 MiB、单场景总量 128 MiB、最多 64 个文件；逐文件保存大小与 SHA-256，最后写入不可变 manifest 并同步目录。manifest 绑定场景定义、Goal Revision/config/最终 Tree、环境和每个业务收据。新的 `SCENARIO` Evidence 引用 manifest hash，Report 包含同一 manifest，读取时重算封存文件 hash；Agent 的检查声明不进入这些收据。Finalize 在事务前核对文件，在事务内从冻结合同核对必需场景映射和 manifest 集合，并检查同一 final Evidence Set 的业务收据，避免报告整体遗漏场景而绕过验收。

最终验证在同一个准备好的 Environment 生命周期内：核对 Tree → bootstrap/服务 readiness → 配置的 Acceptance 独立 Invocation → 核对 Tree/HEAD/index → 受信最终断言 → hash/封存场景制品及必要业务 Evidence → 逆序停止服务并确认全部进程退出 → 再核对 Tree/HEAD/index 和进程屏障 → Final Report/Completion 提交。Store.FinalizeGoal 的事务除 active Lease 外必须拒绝任何未确认 process_invocations/worker ownership；停止失败不发布 Completed，只保存当前失败和恢复 Gate。Acceptance 只读源码、可操作测试数据，不产生 Patch/Promotion；其 AgentResult 永远是 Claim，后续断言失败必须失败。blocked 打开 Gate并保留日志/现场，服务安全停止后等待；再次运行创建新环境，只有声明可重放的测试场景能自动重演，否则先询问具体外部条件。配置默认不启用 Acceptance，也不新增第4类必需 Work。

Acceptance 使用专用不可变 Packet 和 Provider `Accept` 调用，Packet 绑定 Goal/Revision/config/最终 Tree、Profile、场景与私有环境、最后成功 Attempt 的归属及 lease generation。它不伪造 Work ID 或写入 Patch。客户端的直接脚本入口与 `acceptance.trustedFiles` 声明的依赖复用受信控制定义，执行前后核对；Provider 仅获得 Profile 明确的环境及 Kernel 生成的场景目录/环境 ID。

会话外部调用复用现有 `effects`，`effect_type=acceptance`：调用前持久化请求并以 Goal/Revision/归属 CAS 进入 EXECUTING，完成观测后保存脱敏结构化 Claim 和退出事实，终态 SUCCEEDED 只表示会话观测完成。最终业务收据仍独立决定完成。未结束的 acceptance Effect 与进程归属共同阻止别的执行；当前归属的场景命令仍可运行。启动恢复先确认进程退出，再结束不完整 Effect 并保存中断事实；未知进程继续保留屏障。同一 Goal/Revision/Tree 曾有验收调用而 Goal 尚未完成时，缺省进入 final owner 的重放决定 Gate；只有 `acceptance.replaySafe=true` 可由恢复自动新建 Invocation，结构化 blocked/failed 不因该开关自动忽略。人工决定后的新 Invocation 消费对应 final owner 决定，不能挪用 Work 重试。该机制不增加第二份 Goal 状态或文件状态机，PLAN-015 的 Invocation 索引只投影这些事实。

Acceptance 使用专用 Store 事务入口，不直接依赖只检查 Effect version 的通用 UpdateEffect。开始与有效观察同时核对 effect/invocation/request hash、Goal 当前 version/Revision/最终 Tree、最后成功 Attempt 与 generation；同项目最多一个非终态 acceptance Effect。进程 owner 豁免仅允许该链路的场景命令，不允许启动第二个 Acceptance。人工续作在创建/启动新 Effect 的同一事务消费准确 final Gate，检查 owner、旧 invocation、Revision/Tree、version、有效期与 used。pause/cancel 或身份变化后的迟到结果只记录历史退出/Claim，不能恢复 Goal、消费决定或开始验证。

恢复沿用现有 Effect 转换：REQUESTED→EXECUTING→OBSERVING→SUCCEEDED/FAILED，RECOVERING→OBSERVING。已持久化的 blocked/failed Claim 原样保留，不能统一改成 interrupted 后通过 replaySafe 放行；只有缺少完整结果且进程确认停止时才记录中断。终态历史不复活，后续调用始终使用新 Effect/Invocation。暂停或取消仍可回收进程与记录退出，不能自动重放；Goal 完成后只恢复报告文件。以上 CAS、单槽、一次消费和结果保留分别以并发控制/崩溃注入负例验收。

服务退出未知使 Cleanup 返回 ErrProcessUnconfirmed，保留项目槽和持久进程记录；恢复先回收拥有的进程再允许新验证。准备失败、探针失败、断言失败、取消都记录阶段/命令/输出引用并执行逆序停止；停止失败优先返回，不能隐藏在 defer。不得删除用户数据或未知资源。

Probe 成功后仍核对本次启动进程的 PID/start identity、存活、退出及 deadline；受信 Probe 应使用本次私有环境发布的端点，不能靠固定端点的 HTTP 200 推断实例归属。正常停止按 Group 启动顺序倒序；崩溃恢复按单 daemon 串行进程 Journal 的持久插入顺序倒序回收，先客户端、后服务依赖者、再依赖，不使用随机进程 ID 或墙钟排序。

## 实时上下文与操作

新增 Invocation 索引（与已有 process_invocations 分工：前者是 Agent 会话观测，后者是全部子进程的恢复屏障）。字段包含 ID、Goal、owner kind/id/generation、role/Profile、输入与有效配置引用、Provider目录、状态、已持久事件游标/截断状态。规划、Work、Review、Acceptance 在调用前登记；完成后关联会话/结果。进程恢复通过原有 owner 判断安全，不依赖日志索引是否追上。

Adapter 把脱敏的原生事件写为有序不可变文件，查询时按公开字段投影并排除 thinking/reasoning 等私有内容；复用这些文件作为日志事实，不再逐 token 复制到全局业务 events。EventSink 使用有界异步通知触发索引刷新，不允许慢订阅者阻塞 Adapter/心跳；队列满丢通知不丢已落盘事件，通过目录序号恢复索引。日志 I/O 失败/限额明确标记并取消该会话，回收仍独立执行。stdout/stderr 均有上限与脱敏，截断消息占用预留空间。

Invocation 表使用 migration 0012，输入身份与观测分开；索引的终态是调用返回观测，不能解释为 Goal 完成或进程已确认退出。索引维护冲突按当前版本有界重读，刷新失败不改变原生调用结果；恢复把缺少返回观测的调用标为 interrupted，进程安全仍由原归属机制决定。每次文件补扫最多 256 条，通知容量为 1 并合并刷新；读者可继续补扫。stdout 与 stderr 使用各自的连续序号，游标必须同时携带 Invocation ID 与 stream。stderr 按完整行脱敏后发布不可变事件，单行 64 KiB、总量 4 MiB，与 stdout 共享调用总额度；输出超限终止调用并在额度外保留最多 4 KiB 的状态标记。单 Invocation 每个流最多 16384 条，单持久事件读取最多 16 MiB。文件操作用固定 runtime root 和逐级目录检查，拒绝链接、路径逃逸、非普通文件或公开权限。

Control 提供 Goal 的 invocations、Invocation context 和 logs 页/流（稳定 invocation ID + sequence 游标）。只读解析已登记的私有路径和已校验文件，拒绝任意路径读取；短 ID 定位有唯一性约束。上下文展示 Packet、指令输入、有效配置、公开消息/工具事件和已观察模型，私有推理不展示。Packet 按注册字节哈希核验；Provider 元数据按角色核对 owner、generation、Tree、配置及委派身份，结果按对应角色协议严格解码。结构性日志缺失阻止 complete，采集限额错误保留已落盘日志。NDJSON 客户端核对身份、连续游标及明确终态，到达持久边界后才成功；HTTP 200 后的错误帧或提前 EOF 仍返回失败。

CLI 保留旧 logs <attempt-id> 和默认 JSON，增加按 Goal/role/invocation 查询及 --follow/游标；显式 --format human 展示概览。wait 的人类反馈写 stderr，最终 JSON/退出码保持；heartbeat、last_output、last_material_progress 三个时间独立。新增 ID 补全/唯一前缀解析、版本展示和 Gate 决定后续作便利参数，变更仍传版本 CAS；歧义返回候选且不写入。init 从 go.mod/package.json/已知 Python 测试配置发现入口，不执行依赖安装，未知项目输出覆盖缺口及准备命令。

Activity 是既有表的只读派生投影，不新增进度状态机：租约 Heartbeat、持久日志 LastOutput、受信 MaterialProgress 分列。进展来源为冻结 Goal Revision、生效 Plan、Gate 决定、已观察且 Tree 改变的 Promotion、已关闭 Finding、同 Revision/config/Tree/Definition 的 Validator 结果变化、移除采集 ID/时间后的环境事实变化，以及已发布 Final Report。相同验证结果和相同环境事实不因重复执行而刷新时间，所有关联按 Goal 的 Work/Attempt/Workspace 归属隔离。状态查询在 Store 连接外对最新 Invocation 做最多 2s、每流256条的补扫，使 stderr-only 输出可见；索引错误作为观测诊断，不改变 Goal 状态。human wait 输出到 stderr，变化反馈限频1s、无变化15s再提示、终态立即提示；human watch 每秒读取状态，JSON watch 保持原事件游标协议。Next 命令保留显式项目/状态目录/socket 参数。

ID 查询限定 Goal/Work/Gate/Invocation 四类，按字面前缀匹配，精确 ID 优先；每页最多100项并标记 more。读取完整 Work/Gate 可获取版本，Shell 动态补全有500ms请求期限。新歧义写请求在记录幂等意图前拒绝；此前已完成的请求即使后来前缀变歧义仍重放原响应。流式读取只解析一次身份，Invocation 响应以明确完整 ID 绑定客户端帧校验，后续出现相似 ID 不能切换订阅。

PLAN-015 的续作入口为 `POST /v1/gates/{id}/resume`，请求同时携带 `expected_gate_version` 和 `expected_owner_version`。CLI `approve --resume --owner-version` 顺序执行决定与续作，第二步固定第一次返回的完整 Gate ID 和版本；`gate resume` 只重试续作。两步各有幂等请求，不自动刷新 CAS；部分成功保留决定并给出读取当前状态后的精确续作入口。

Planner 的新请求及 Packet 保存一个 `prior`：上代 Effect ID/request hash/generation、退出后的 Observation（问题、失败或 Proposal）及已批准答案。事务核对同代 Gate、配置、旧现场、必要 Gate 和进程归属后消费一次并创建下一代；实际 BeginPlanning 再核对 Prior Tree/CheckoutIdentity 和必要 Gate。现场或权限变化进入可诊断 Waiting。已消费答案的 generation 中断后需要新决定，不能由恢复重复使用有限授权。Work 复用原现场重试事务，同时核对所选 Gate 版本与最新失败 Attempt；不根据任意 Work ID 代替权限或信任 Gate 的专属消费者。

Final owner 续作核对当前 Goal version/Revision/config/Tree、最新 Acceptance Effect、场景归属、必要 Gate、进程退出和完整 checkout。Goal 恢复与答案消费保持原 owner 边界：新 Acceptance Effect 创建时才原子消费，重新准备环境失败不丢失答案。新 Packet 的可选 Prior 保留上一 Invocation、Observation hash 与完整公开 Observation；历史迟到结果可以作为明确标记的反馈，准确人工决定才可启动新会话，旧 Claim 不能恢复成当前 Evidence，`replaySafe` 不能自动重放历史 Claim。

## 一致导出与迁移

使用已有 `VACUUM INTO` 模式取得数据库一致快照，完整性验证后从快照读取指定 Goal 的结构化状态及引用。在线导出使用独立只读 SQLite 连接和有界 context，不占用控制 Store 的唯一连接；WAL 读快照与心跳/CAS 并发，复制/hash 阶段完全不持有控制连接或写事务。若平台的只读连接不支持该快照入口，返回明确不支持而不回退到锁住控制连接。为兑现本地备份用途可包含该项目的完整数据库快照，清楚标注范围；Goal JSON 只是其投影。只从快照冻结的引用复制不可变制品及已落盘日志序号范围，逐文件路径/regular/mode/size/hash 验证，未索引的较新运行日志不纳入该时间边界。索引滞后但文件存在不破坏已导出边界，清楚展示 durable cursor。

包含完整项目数据库快照时，文件闭包也必须覆盖快照内所有 Goal 的被引用制品；不能只复制选定 Goal 文件而将整库标成完整备份。选定 Goal 的结构化视图是操作焦点，导出范围和其他 Goal 包含事实由 manifest 明示。

使用私有临时导出目录，snapshot/data/files/manifest 的清单最后写入并 fsync/rename；任一缺失/损坏/清理竞态导致 incomplete，不发布 complete。clean 与导出串行或有引用保护，避免导出期间主动删除引用。导出仅本地只读，不提供导入执行或跨机器进程恢复；不要复制宿主凭据、未脱敏原生会话库或可写工作区冒充完成制品。

具体入口为 `export <goal> --output <new-directory>` / `POST /v1/goals/{id}/exports`。输出固定为 `snapshot/state.db`、`data/goal.json`、`files/<runtime-relative-path>` 和 `manifest.json`；完整项目范围在清单中显式说明。独立 WAL-aware `mode=ro` 连接执行 VACUUM INTO，完成副本通过 quick_check/foreign_key_check 后以 immutable 方式读取；不能对在线源使用 immutable，因为它会忽略 WAL。边界保存复制起止区间和各 aggregate 的最大 Event ID/sequence；不用可能被 VACUUM 重排的 rowid 冒充全局时间。此机制沿用 [SQLite VACUUM INTO 的一致快照语义](https://www.sqlite.org/lang_vacuum.html)。

快照 owner 枚举覆盖所有 Attempt Packet、Patch manifest/对象、Validator Receipt/输出、Review Packet/Result、SCENARIO Evidence manifest/文件、Environment JSON、Final Report、Invocation 和 Planner/Acceptance Effect、未清理 Workspace marker 及已登记迁移备份。单一受限读取器检查运行目录下的相对路径、目录/文件权限、符号链接、常规文件和大小；复制读取到的同一份字节，再用协议 owner/hash 与副本校验闭包。源码路径、Harness 文件、Git Tree/commit 及可写工作目录是事实引用，不变成任意宿主文件复制入口。

Invocation 日志边界是快照 cursor/字节数，公开投影同时记录源字节 SHA-256 和导出字节 SHA-256；更晚的事件不追入快照。未登记 hash 的 Provider metadata/result 文件及旧未索引流明确排除，冻结输入和观察结果仍在 DB。初始 Planner 的 BeginPlanning 先于预检/Packet；用户提供 Proposal 时根本没有 Provider Packet。未索引且无返回 Proposal/session 的缺失输入标为 preflight/legacy unknown，不能当作丢失已封存制品；已登记 Invocation 或已返回的旧 Provider 输入仍要求存在。已 CLEANED marker 标为 retired。PENDING_RENAME Report 从快照 blob 生成并保留原状态；COMMITTED 报告必须验证在线文件，不能回填遮盖损坏。

Control 使用同一可取消互斥串行化 clean 与整个导出，从创建快照前持有到文件复制/发布完成；普通状态、Heartbeat/CAS/取消不获取此锁。文件最后 fsync，Darwin `RENAME_EXCL` / Linux `RENAME_NOREPLACE` 原子发布且不覆盖任何已存在目录；不支持时明确失败。输出位于项目及状态目录外。总时限 5 分钟，其中快照 2 分钟；文件总量 2 GiB，文件/引用各 100,000，引用 JSON 256 MiB，数据库/迁移备份单文件 512 MiB，其余沿各 owner 限额并以 128 MiB 为上限。失败保留私有未完成目录；公开错误入口再次脱敏，再进入 API/幂等响应。

Provider 进程退出与公开输出落盘是两个观察阶段。Supervisor 仍核对进程组退出并保留调用超时/终止 grace；Go `exec.WaitDelay` 设为 5 秒，超期关闭未完成管道并保留错误，避免把短暂 fsync 延迟错误归类为 Provider 失败。这不是任意 `io.Writer` 的硬截止，已开始的本地写入仍遵循操作系统 I/O 完成语义；Terminate 的既有期限到达后保留 Unknown 屏障。未完成或失败的输出仍不当作完整成功，不以进程 exit 0 跳过日志错误。

新增 migration 0011 扩展 failure CHECK 并保留旧 BUDGET_EXHAUSTED 等历史行（不恢复运行功能），重建引用它的 reconcile 表时先复制关系、删除旧 child/parent、重命名并 FK 检查；追加 Work 自动计数。后续 Invocation 表按独立 migration 追加。旧 migrations 不修改。可选 JSON 字段 omitempty，历史 Definition/Packet/Config 可读取；新解释器信任绑定故意改变 Definition hash，当前运行须重新验收。旧冲突配置给出具体字段和迁移步骤，不静默容忍。

运行时升级先停止旧 daemon/确认旧进程归属，沿用迁移备份；回退仅使用升级前备份并重新核对现场，不允许旧二进制写新库或 reset 用户文件。没有生产部署和外部发布动作。

## 验证策略与剩余限制

PLAN-012 验证信任变更和失败恢复；PLAN-013 验证 Profile、规则与实际双 Provider；PLAN-014 从真实服务/客户端证明可选验收及断言错误阻断；PLAN-015 从 CLI/daemon 验证实时日志、决定续作、一致导出和完整发布门禁。每份 Plan 独立 Change Review 后提交，最后按 AC-HR-001–018 对账。真实 Provider 费用与可用性通过原有显式 smoke 入口控制，本 Objective 已授权实际功能验收。

关键负向场景：候选脚本/依赖篡改、入口 mode/symlink/CWD 逃逸、同 fingerprint 机械重试、变化 fingerprint 耗尽总数、旧 generation、人工外部编辑、取消与未知进程、准备/探针/业务断言失败、错误 Acceptance Claim、输入变更或继承身份未知时的 session resume、慢日志读者与损坏制品、长导出同时仍能心跳/CAS/取消和迁移外键历史。fixture Adapter 用于可重复注入故障；实际 Codex/Claude 调用与真实服务从用户入口验收，不能互相替代。

仍不承诺任意测试的语义完备、模型一定遵守规则、同 UID 敌对隔离、共享外部数据自动回滚、未知 Provider 私有上下文读取或本次未运行的完整 Benchmark 成绩。
