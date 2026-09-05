# REVIEW-050：OBJ-004 运行时 Harness Design Review

## 审查对象

- Objective / Plan：`OBJ-004`，`PLAN-012` Item 1.2。
- 产品合同：根产品 SPEC 第 20 节、AC-HR-001–018；产品 SHA-256 `2ae731a5fe44962b1f9195ac4da13f08c0fdb4d2e21ef741f2d250d9e39c30c6`，已由 [REVIEW-049](REVIEW-049-runtime-harness-spec.md) 最终复核为 PASS。
- 被审技术合同：`xgoal-technical-spec-v0.1.md` 第 35 节；SHA-256 `af18a51b03c1d106fae5581f43694e54faf7f0c5054c19709253279b00e8ad13`。
- 被审设计：`docs/architecture/DESIGN-007-runtime-harness.md`；首轮 SHA-256 `4705332d8d951c6dcde495813462ecd0cd707be2b227b9606ed0136519f75fbc`；已读回 F-001/F-002 修订版 SHA-256 `38e33f733bf5e986fc9ef7e2d8631658ef85241199cfeaab6ad15fd90f9b13b1`。
- 最终复审技术 SPEC SHA-256：`9141f3232683f779ca68b4cd0e5d5221e39af3b51fd783ac88f8325759a0119a`；DESIGN-007 SHA-256：`7b420af9693a9567e2834a90b0223d3be77fb7d4cda96136fadd9614c5aac1a7`。
- Git 基线：`a9b6192192c5bd99e4378bb8234e64f0a1fddf73`，分支 `feat/runtime-harness-closure`。
- 独立审查使用 `autogo-design-review`，只写本 Review 并更新 REVIEW-049，不改设计、代码、Plan、Progress、索引或预存 `.gitignore`。

## Verdict

`PASS`

首轮为 `FAIL`；作者已修订全部五项发现，独立 Reviewer 读取最终全文、技术 SPEC 当前 Diff 并逐项确认，现结论为 `PASS`。设计选择复用 SQLite、项目槽、现有进程归属和 Validator 是合理的，五项产品需求均有接入 owner，可以进入 PLAN-012 Phase 2。下文保留发现、修订方向和后续实现验收锚点，不表示这些功能已经实现。

## 发现与处置

### F-001 / P1：服务退出确认必须早于完成提交（已修订）

首轮设计的最终验证顺序为“Evidence/Report → 停止服务”。当前 `internal/orchestrator/finalize.go` 调用 `engine.finalizer.Finalize` 后会提交 GoalCompleted，`internal/store/sqlite/final_report.go` 事务只检查 active Lease，未检查 `process_invocations`。若完成后 Cleanup 返回退出不明，系统已经无法把完成事实改成正确的等待状态。

**修订及核对**：DESIGN-007 已改为断言与必要证据封存后逆序停止服务、确认退出、重新核对 Tree/HEAD/index 和进程屏障，最后 Final Report/Completion；明确 Store.FinalizeGoal 事务拒绝未确认 process/worker owner，停止失败不得发布 Completed。当前内容解决了此缺陷。

**实现验收锚点**：AC-HR-005/017，注入断言通过但服务退出不明的场景，确认没有 GoalCompleted/最终完成报告，恢复确认退出后才能重新验收；不能仅断言 Cleanup 返回了 error。

### F-002 / P1：不能把被测应用入口自动加入验收信任保护集（已修订）

首轮环境段将服务命令入口和依赖也交给受信声明规则。若服务使用 `python app.py` 或 `node server.js`，解释器自动入口识别会冻结被测业务程序，随后 Work 修改该程序就会被 ProtectedPaths 拒绝，违背业务源码允许演进的产品合同。

**修订及核对**：DESIGN-007 已明确冻结启动配置及显式控制/断言脚本；被测程序只绑定当前候选 Tree，不因为是服务入口自动加入 ProtectedPaths，readiness/Validator 仍使用受信规则。当前内容解决了此缺陷。

**实现验收锚点**：AC-HR-003/004，同时验证业务 `app.py` 在授权 Scope 内可修改并被新服务运行，受信 readiness/业务断言脚本的变化仍被拒绝。

### F-003 / P1：Acceptance 的只读源码与本地服务交互需要明确可执行的权限组合（已修订）

DESIGN-007 Profile 段承诺 Codex 始终使用旧 `--sandbox` 角色策略，又称 Acceptance 可以使用受控命令操作本地服务，但尚未定义网络、可写场景目录、工具允许范围和 Provider 不支持时的路径。当前 Codex Adapter 不接受非空 ToolPolicy，Claude 的 roleTools 则只接受固定读/编辑工具列表；这不是仅添加 role 值即可完成的接缝。

本机 `codex-cli 0.145.0` 的 `exec --help` 已确认 `--sandbox`、配置覆盖和非交互入口。官方 Permissions 文档明确：新 permission profiles 可分别设置文件系统与网络，但显式 `--sandbox` 会选择旧机制；网络域名限制还需开启原生 network proxy。因此不能假定“只读 sandbox + never”天然提供指定本地服务交互，也不能混用两套设置后声称两者均生效。[Codex Permissions](https://learn.chatgpt.com/docs/permissions)

**修订方向**：给出至少一条原生支持、可真实验收的 Acceptance 配置路径，分别明确源码只读、场景目录写入、允许端点和工具、有效配置身份与 Probe；不支持的组合在执行前说明。若 Codex 使用新 permission profile，就明确该角色不同时传旧 `--sandbox`；若选择其他受信原生工具路径，明确其边界与降级条件。不能用开 workspace-write/全网权限或跳过实际交互替代此功能。

**验收锚点**：AC-HR-001/006/007，真实 Provider 动态读取服务响应后执行下一步；源码变更、未声明网络或命令仍不能被当作合规成功。产品不承诺 L0 敌对隔离，测试应核对真实能力及明确限制，不以 Prompt 自述证明权限已强制生效。

**最终修订核对**：设计已明确 Codex Acceptance 支持无网络只读观察，网络场景 preflight 拒绝并提示兼容 Profile；动态本地服务交互由 Claude 的显式受信客户端命令承担，基础 `--tools` 与 `--allowedTools` 规则分开，无规则的无限制 Bash 被拒绝，客户端脚本进入信任绑定。明确 Claude 规则不是 OS 只读或端点硬隔离，仍必须核对项目网络授权、测试目标及源码/Git 身份。该矩阵保留了实际动态 Acceptance 功能，不要求所有 Provider 支持每个能力组合，也避免引入额外 beta 权限栈；双 Provider 的原有角色与参数功能仍须真实验收。此发现已解决。

### F-004 / P2：未知原生继承参数不能构成已证明相同的恢复身份（已修订）

设计记录 native inheritance/unknown，并将请求 model/effort、Profile 和 CLI 版本组成恢复身份，但缺省参数的原生配置可能在 xgoal Profile 未变化时变化；两个 unknown 相同不能证明有效配置相同。现有 CLI 明确允许从用户配置继承，且当前 session binding 不记录原生配置来源的可验证身份。

**修订方向**：能够确定并绑定有效参数时记录其身份；如果不读取原生配置或实际模型/effort 仍未知，则继续允许新会话，但保守拒绝复用原 session，并显示原因。无需读取凭据、扫描整个用户目录或增加全局配置系统。

**验收锚点**：AC-HR-008，显式且可绑定的配置在其他条件成立时可恢复；继承未知值、原生输入漂移或有效参数变化不能因 request 字段仍为空而复用旧会话。

**最终修订核对**：设计已增加独立段落，继承原生模型/effort 且无法冻结完整身份时只允许 fresh；显式模型/effort、角色权限、工具、CLI 版本和 Packet 均可绑定时才允许安全 resume，旧缺身份会话只读保留，不自动用于新执行。此发现已解决。

### F-005 / P2：在线导出不能复用占满唯一控制连接的迁移备份调用（已修订）

设计提出复用现有 VACUUM INTO 模式，但没有区分迁移时独占执行与运行中导出。`internal/store/sqlite/store.go` 当前把数据库连接池设为一个连接，`internal/store/sqlite/backup.go` 的 VACUUM 使用所传数据库句柄。运行中直接复用这一路径，复制持续期间心跳、CAS 和取消状态写入都会等待同一个连接，可能把只读导出变成正在执行任务的 Lease 超时原因。

**修订方向**：明确在线快照使用独立、受控的连接/一致读快照，不借用 Kernel 唯一事务连接执行长复制；设有界超时与取消，快照之后的 hash/文件复制完全离开控制连接。保持 WAL 下合法并发及 clean 引用保护，不增加第二个可写 Store。实现时验证选择的 SQLite 快照方法确实可在该连接模式使用。

**验收锚点**：AC-HR-013/016/017，在延长导出或大快照复制期间验证心跳、状态 CAS 和取消仍可推进；验证中断导出不出现 complete 清单且不影响 Goal 状态，不能只验证空闲数据库导出的文件内容。

**最终修订核对**：设计已明确独立只读连接、有界 context、WAL 一致读快照与心跳/CAS 并发，hash/复制阶段不持控制连接或写事务，能力不支持时不回退到锁住控制连接；负向验收加入长导出期间心跳/CAS/取消。额外明确完整项目 DB 快照必须包含所有 Goal 的文件引用闭包，不能只复制选定 Goal 制品却宣称完整备份。此发现已解决。

## 完整性、复杂度与实现边界

- Profile 解析统一四角色，显式 role→Profile 绑定与 legacy 选择来源可共存；不需要第二套每 Work 模型路由。新 Provider 能力只在原生版本支持且实际调用证据成立时声明。
- 受信文件集由 Registry 拥有，Patch 与 Runner 复用同一身份，失败修复也核验同一 ProtectedPaths；新信任只能由新 Goal 建立，避免 Gate 隐式换基线。
- 自动重试单独事务复用人工 retry 的现场核验是正确方向。当前 RetryCheckoutWork 写死 human actor 并会解决特定 Gate，因此不能直接由 Kernel 调用冒充人工。设计已要求总计数、owner/版本/进程检查、无 Gate 自动批准与执行前再次 Snapshot，覆盖 SQLite 与文件系统无法原子提交的边界。
- 初始规划、Work、最终验收各有输入身份和续作 owner，复用现有进程屏障不需要虚构 Work。最终物理进程可以沿用已有 last-successful Attempt generation，但新的验收 Invocation 和 Gate 必须保持 final owner 的可追溯语义。
- Invocation 索引与 process_invocations 职责不同，前者为用户观测、后者为恢复屏障，分开有必要。复用已落盘的有序事件文件并只丢可重建通知，符合最小充分方案；索引刷新失败不能假装进程停止。
- 数据库新 migration 保留 failure/reconcile 历史关系、只增可选字段，不改历史 migration；导出 manifest 最后发布且核对冻结引用，符合单一状态权威。
- 当前设计的场景、配置、负向验收可覆盖 AC-HR-001–018；具体命令与 fixture 不需要机械写入设计，但上述五个决定会直接改变行为或恢复方式，必须先收敛。

## 下一路由

五项发现均已在最终版本解决，下一路由为 PLAN-012 Phase 2 的受信入口保护实施和定向验证。后续按既有四份 Plan 持续推进 Profile/Harness、真实环境与 Acceptance、观测与导出；每个实现边界完成当前 Evidence 和 Change Review 后再关闭。无需新 Objective、新 Plan、人为审批或产品范围外平台。

本次 Evidence 为设计、产品合同、代码接入点、本机无模型请求的 CLI help/version 及已读取官方文档。未启动真实 Provider、服务或运行功能测试；Design PASS 也不构成任何增量功能已完成的证明。
