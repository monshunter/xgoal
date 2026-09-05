# 项目单实例、当前目录与持久后台执行验收

2026-09-05，OBJ-003；基线 `24960390d71237d6266e2f995b4e3df0b6719a78`。本记录按 PLAN-009、PLAN-011、PLAN-010 分别保存当前 Evidence，替代历史 M2–M6 对 worktree 和同步 Planner 的运行假设。历史验收记录仅证明当时版本。

## 架构判断与范围

保留每项目 CLI + daemon + SQLite + Unix Socket 是当前最小充分方案。CLI 负责输入和观察，daemon 拥有持久意图、执行、恢复与完成判断。两者分离的价值是客户端连接可以结束，而长期目标继续由单一 owner 驱动。单进程前台 CLI 不满足这个生命周期；全局 daemon 则增加跨项目路由、资源公平性与共享故障域，当前没有足够收益。

仓库锁保护跨状态目录的唯一 owner，状态锁兼容旧实例，DB 事务/CAS 保护状态转换，实例身份约束 IPC，Git ref CAS 与 Evidence 保护已验收结果。这些机制对应不同竞态边界，不能互相替代。SQLite WAL 仍只允许一个同时写者；Go HTTP Shutdown 本身也不等于业务 goroutine 和子进程已结束，因此关闭必须由 app 统一取消、等待、关库后释放锁。参见 [SQLite WAL](https://www.sqlite.org/wal.html) 与 [Go Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)。

当前目录执行省去内部 worktree 的创建、清理与多份代码定位；代价是同项目必须串行，且失败不会自动恢复文件。原始字节快照、私有 index、受验 Tree 核对与现场保留保护用户数据；私有审计 Commit 不移动用户 HEAD 或 index。Git 支持直接按对象填充 index、从 Tree 创建 Commit，见 [git-update-index](https://git-scm.com/docs/git-update-index)、[git-commit-tree](https://git-scm.com/docs/git-commit-tree)。这只提供可信本地协作约束，不能证明同账号敌对 Agent 的文件系统隔离。

## PLAN-009：项目身份与生命周期

已执行的定向证据：

- `internal/project` / `projectinit`：项目身份、flags/env、短 socket、绑定、锁文件与初始化；linked/submodule 入口、旧 linked locator 与单 linked 历史均在写入前拒绝，主默认历史状态保留。
- `internal/store/sqlite` 的 `TestProjectBinding*`：预检只读，默认旧状态备份迁移并保留 Goal；错误项目、非空无绑定外部库、冲突历史、非法 JSON、symlink/hardlink 数据库在可写打开前拒绝；错误项目预检前后数据库摘要相同。
- `internal/cli.TestRealCLIConcurrentStartIdentityIsolationAndStop`：真实编译 CLI 启动 A/B 两仓库，PID/状态独立；同项目并发 start 只有一个实例；错误 socket 被拒；停止 A 后 B ready；重启后旧实例客户端被拒。
- `internal/cli.TestOfflineDoctorDoesNotCreateStateOrOpenCorruptDatabase`：无 daemon、无数据库、损坏数据库均可离线诊断；不创建或打开状态库。
- `internal/daemon`：存活 socket 不替换，清理只删除本人 socket inode；请求取消与收尾先于返回；listener 故障主动取消 runtime 后 join；超时仍保留 owner 直到收尾结束。
- `internal/control.TestPassiveDoctorDoesNotCreateAdaptersOrFollowGitEnvironment` 和两个 Adapter 的 `TestPassiveProbeDoesNotCreateRuntime`：被动诊断不构造执行目录，Git 定位环境不导向另一个项目。

预检边界：`CheckProjectBinding` 的 SQLite `mode=ro` 不写业务数据或迁移 schema，但 WAL 模式可能创建空 WAL/SHM 文件；取得兼容状态锁也可能创建 lock。这不等于错误 state-dir 全目录零文件写入。错误绑定测试证明的是拒绝业务与迁移、主库内容保持不变；离线 CLI doctor 则完全不打开 SQLite。

控制面与两个 Provider Adapter 的相关回归通过：`go test ./internal/control ./internal/adapter/codex ./internal/adapter/claude -count=1`。这是本地 fixture 与被动能力检查，不是实际 Provider Goal 验收。

最终组合回归 `go test -race ./internal/project ./internal/projectinit ./internal/store/sqlite ./internal/api ./internal/app ./internal/daemon ./internal/cli ./internal/doctor ./internal/control ./internal/adapter/codex ./internal/adapter/claude -count=1` 全部通过；`go test ./...` 全仓通过。格式、`go vet ./...` 和 Diff 检查通过。[REVIEW-044](../reviews/REVIEW-044-change-project-daemon-isolation.md) 为 `PASS_WITH_NOTES`，唯一 note 为上述 WAL 边界，无阻塞。PLAN-009 已完成对账。未将完整 Goal、所有角色进程恢复或当前目录执行标为通过。

## PLAN-011：当前主目录执行

已取得的定向 Evidence：

- `internal/gitrepo` / `patch` 的 race 回归通过；私有 index 原始字节快照覆盖 binary/rename/mode/symlink/delete、非忽略 untracked、Unicode 名称与冲突。FIFO 被 Git 文件清单遗漏、私有 ref 为 symref 时可能写用户分支、replace refs 改变对象读回这三类问题均有失败复现和修复后通过的测试。Git filters/hooks/fsmonitor 不参与快照与审计提交。
- `internal/workspace`：Attempt/Validation 记录共用实际主目录；清理只删各自 marker 与空元数据容器，源码保留；v1 marker 哈希保持可读，旧目录不能被新 Cleanup 删除。`internal/store/sqlite` 的迁移测试验证 schema 7→8 备份、历史 Report 哈希不变与旧 Goal 迁移等待。
- Checkout 状态与 ClaimWork 共用事务；拒绝未知 dirty 基线和跨 Goal 接管。失败 retry 只认同一 Work 的精确 observed Tree、原身份和活跃 Plan，专用 Gate 与 Work/Goal 转换原子发生。独立审查补充了旧 pending Promotion 阻塞新 Goal、未处理现场 replan、过期 Lease 失败收尾的回归。
- `internal/environment` / `validator` / `review` / `orchestrator` race 回归通过。Validator 退出 0 但修改源码仍失败；bootstrap/Reviewer 非零退出并修改源码也不能把现场记为可接受结果。失败服务日志保留，停止服务后才释放 handle。
- `internal/promotion` race 通过：私有 Commit/Ref，不移动用户 HEAD/index；覆盖 Marker/Commit/ref CAS/Observe 崩溃窗口、ref 已更新后源码漂移与恢复、两个 Work 顺序晋升。SQLite Observe 原子更新 accepted tuple，并关闭已确认恢复的专用 Gate；待恢复 Promotion 的 Lease 不被 Worker 回收提前撤销。
- `internal/control.TestPublicFinalizeRejectsLiveCheckoutDriftAndPreservesScene`的源码、HEAD Commit、符号分支、index 与私有 ref 漂移场景均拒绝公开 finalize，拒绝时保留状态和文件。临时 overlay 去掉公共 dispatch 的 guard 后源码漂移回归按预期失败，实际源码没有被该实验修改。
- `internal/app.TestRealUnixAPIRunsGoalToVerifiedFinalReport` 完整 Unix API Goal 到 `COMPLETED`；旧配置重启后原 Report 逐字节可读。配置不兼容时不构造 Engine/Adapter 执行目录，其他字段非法不会被迁移提示掩盖。config/projectinit/doctor/control/app 组合 race 全通过。

`internal/cli.TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence` 已通过（47.716s）：真实构建 CLI、init/config validate、后台 daemon start、第一 Goal 自然语言规划、第二 Goal Proposal、两次 `run --wait`、status/report、stop。所有角色 CWD 均为当前主目录，第二私有 Commit parent 等于第一 accepted Commit；HEAD、index 字节、用户 refs 与 worktree 列表保持不变。停止后只读 SQLite 确认 Final Tree = 私有 ref Tree = Report Tree = final Evidence/Receipt Tree。Provider 是本地协议 fixture，不能将此记录表述为真实模型服务验收。

独立审查复现并修复了终验期间私有 ref 漂移仍可能完成、Worker 登记失败后取消失败被忽略两项问题。最终四项失败/恢复定向 race 通过（50.112s）；SQLite/workspace/Codex Adapter race 通过（24.998s/4.003s/3.586s）。全仓回归首轮只有 opt-in smoke 旧接口编译失败，适配后 Codex Adapter 单包通过；最后的全仓 `go test ./... -count=1` 全部通过：orchestrator 136.442s、真实 CLI 63.228s、app 35.880s、SQLite 11.060s。默认全局 Git ignore（含缺失→创建、XDG 空值、显式空值与尾空格路径）身份遗漏已修复，Git/Patch 最终 race 通过 15.758s/9.755s。`work.cancel` 在当前未决 Promotion 存在时明确拒绝，避免撤销其读回所需 Lease；旧冻结 Promotion 仍可取消。以上两项有发现方独立复现与修复后通过证据。`go vet ./...`、格式与 Diff 检查通过。[REVIEW-045](../reviews/REVIEW-045-change-current-directory.md) 汇总跨 owner 独立审查及已修复发现，PLAN-011 已完成对账。

## PLAN-010：持久规划与后台恢复

实现、独立 Review、真实 Goal 与组合发布门禁均已通过，PLAN-010 可关闭。以下为 2026-09-05 当前代码的 Evidence；原始失败与修复后结果一并保留。

- API 原子接受：`internal/api/acceptance_test.go` 与 SQLite `TestPlanningAcceptanceIsAtomicAndReplayPrecedesCurrentValidation`、`TestPlanningConcurrentAcceptanceHasOneGoalAndOneEffect` 验证接受事务失败无部分 Goal/Effect/响应、12 个并发同 key 仅一次接受、重复请求在当前配置校验前回放。HTTP 取消后普通写响应使用有界清理 context 落盘。
- 原子发布与恢复：`TestPlanningPublishRollsBackEntireGraphAndCanRetry` 以 SQLite trigger 注入发布失败，Revision/Plan/Work 全部回滚后可重试；保存的 Proposal 在暂停/恢复后只发布一次、不再次调用 Provider。取消、generation/CAS、源 Tree 和配置漂移禁止迟到发布。
- 旧状态恢复：原始请求不能证明归属时保存确定的 REQUEST_INTERRUPTED；旧 READY 仅在初始 Compiler 事件、Contract/配置/Graph 哈希与 Required Criteria 映射可证明时激活。独立 Review 复现的暂停崩溃预算误计、配置 Gate 未关闭、失败人工 replan Draft 被错误恢复均已修复，`planning_legacy_test.go` 正式回归通过。
- 进程生命周期：统一启动 barrier 在目标执行前完成 INTENT→强 PID/PGID/start identity 登记。真实 owning process 直接退出后，新 ProcessManager 可回收已登记且忽略 TERM 的子孙；身份不匹配不会误杀并阻止新执行。服务健康失败不丢失未确认 handle；Validator 遇到未确认回收不发布 Receipt。Attempt journal 版本区分新启动前崩溃与缺少证明的旧执行，未决 Promotion 保留 Lease；未知进程随后确认终止可将原 Gate 更新为可重试等待。
- 实际 CLI 双项目：`TestTwoProjectGoalsContinueIndependentlyAfterClientExitAndPeerDaemonStop` 通过，51.996s。A 的接受响应在慢 Planner 持续等待时返回；B 的 `run --wait` 客户端收到 SIGINT 退出后 Planner 继续。停止已完成 A 的 daemon 后 B 同一实例继续并完成。双方 PID/状态目录、Goal、Report 与最终 Tree 独立，HEAD/index/worktree 列表保持不变。此处 Provider 为本地 Codex/Claude 协议 fixture。
- 实际 daemon 崩溃：`TestDaemonCrashDuringPlanningReclaimsOldProcessAndCompletesNewGeneration` 通过，27.553s。SIGKILL 本测试创建的 daemon 后，重启先回收原 Planner 组，再创建 generation 2，最终 Report/Evidence 绑定 Tree `57b832b6492c66ba7c5df391fce42657e7b9306e`。
- 实际控制链：`TestPlanningPauseResumeAndCancelStopRealProviderGroups` 通过，5.519s。持久 pause 后所属进程终止、Goal 仍为 DRAFT/PAUSED；resume 创建 generation 2；cancel 终止该进程且不生成 Revision。CLI PAUSED/CANCELLED 的 3/4 退出码保持。
- 当前目录连续 Goal：持久规划和统一进程登记接入后，`TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence` 再次通过，53.238s。所有角色的实际 CWD、用户 Git 状态与 final Evidence/Report 绑定继续成立。
- Linux 原生进程验收：以 `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c` 构建 supervisor/recovery 测试，在已有 `alpine:3.20` 的 `linux/arm64` 临时容器内禁网运行，两包均 PASS。覆盖 barrier 登记前拒绝执行、相对 executable/退出码、父进程提前退出、忽略 TERM 的子孙持有输出管道、进程身份不匹配和 daemon 退出后的 SQLite 回收。最终日志 `/tmp/xgoal-plan010-linux-process-final.log`。这是真实 Linux 执行，不只是交叉构建。

全仓 `go test ./... -count=1` 在双项目测试加入后通过（日志 `/tmp/xgoal-plan010-integration-2.log`）；后续收尾发现和测试已分别定向验证。完整 `make verify-m6` 首轮日志为 `/tmp/xgoal-plan010-verify-m6-full.log`，因下述测试期限失败返回 2；其余 targets 通过 `make -o test -o shuffle -o race verify-m6` 完成并返回 0（`/tmp/xgoal-plan010-remaining-gates.log`）。全仓普通测试、完整 race 与失败项随机复验单独对账，不能把原始 make 命令说成返回 0。20 次随机顺序回归保留原次数，测试入口显式设置 90 分钟总期限，避免增长后的实际多进程套件被 Go 默认 10 分钟上限截断。版本探测复用已有 `runVersion` 的 5 秒期限，移除外层 500ms 重复期限；独立 race 复现证明默认 race wrapper 退出观察会使原 500ms 探测误报，父 context 仍可提前取消。

### 真实 Provider 与完整门禁

系统 CLI 的真实 Codex smoke 首次 **FAIL**：本机 Codex CLI 请求当前配置模型 `gpt-6-astra`，服务端返回 `400 invalid_request_error`，明确要求更新 Codex 版本。日志 `/tmp/xgoal-plan010-real-codex.log`；这是外部 CLI/模型兼容性问题，不能被协议 fixture PASS 覆盖。本次没有更新用户全局 CLI 或模型配置。

随后根据[官方变更日志](https://learn.chatgpt.com/docs/changelog)，将固定版本 `@openai/codex@0.153.4` 安装到 `/tmp/xgoal-codex-01534.R8Jq4D`（关闭安装脚本、audit/fund），仅为该测试覆盖进程 PATH。真实 M3 smoke **PASS**，124.094s：active contract、Fast Implementer 与 session resume、Standard Implementer 及持久验证制品均通过，Validator Trees 为 `fed0ceb8d6415bd1201ae5295258a7f45bd6e58e` / `c560ec4542f85bfc6837345c8c1541493c3ca53e`。日志 `/tmp/xgoal-plan010-real-codex-compatible.log`。原系统 `codex-cli 0.145.0` 保持不变，实际使用当前模型仍需选择兼容 CLI。

完整真实 Codex Goal 首次因测试复用的 fixture Profile 缺少 PATH/HOME 导致 npm 启动器退出 127；按示例补齐环境白名单后，Planner 又正确发现 Validator 写入额外 roles 日志与“只修改 output.txt”的目标冲突，进入明确 DRAFT/WAITING（105.458s，`/tmp/xgoal-plan010-real-background-goal-ambiguity.log`）。现使用独立的固定测试配置，Validator 只读、逐字节核验 `accepted\n`，不修改生产权限或目标完成规则。最终测试还直接检查持久 Planner session、批准的 Codex Review 与 Implementer 会话不同、当前 Final Tree、HEAD/index、私有 ref 和全部进程结束。随后一轮在 66.974s 被既有 Contract 非空语义校验拒绝，具体缺失字段因旧测试清理已不可追溯，不据此猜测字段。补齐提示后，下一轮持久 Proposal 明确包含没有 Validator 且非 human_acceptance 的 AC-SCOPE，以及未以 `/` 开头的 Scope；再次正确拒绝（84.860s，`/tmp/xgoal-plan010-real-background-goal-invalid-criterion.log`）。Planner 提示现统一说明编译器已有的 Contract 非空、条件审批、真实 Validator 覆盖、Work Graph 与项目根锚定 Scope 要求；缺少可验证覆盖时返回 ambiguity。没有放宽 Compiler、完成判断或协议。最终真实重跑 **PASS**（266.394s，Goal 4m24.199s），日志 `/tmp/xgoal-plan010-real-background-goal.log`。Planner/Implementer/Reviewer 的实际会话 ID 均非空且不同；Goal 为 COMPLETED，私有 ref 与最终 Report Tree 同为 `d2ac5682cca5244ce28940f69ffde6bbc2bde6c2`，Final Evidence Set 为 `evidence_set_final_8726a7955e239f952c7b4aed`。逐字节输出、HEAD/index/worktree 列表、approved Review、final Evidence/Receipt 与全部进程终态断言通过。

真实 Codex↔Claude 双向 smoke 本轮未执行：自动审批拒绝调用，理由为外部服务、已有登录凭据及账户额度未获足够具体授权。临时固定文本 fixture、传输范围和账户额度使用已提交用户确认，目前未获答复。未绕过拒绝，未使用账户额度重置。该检查是额外的双 Provider 在线兼容性复验；PLAN-010 与 AC-BG-006 要求真实 Provider 完整链路和完整 verify-m6，不要求再次运行不在 verify-m6 内的 m4-real-smoke。历史 AC-FR-050 的在线结果继续仅代表当时版本，本轮不把它当作当前双向在线证据。

完整门禁首轮普通全仓测试通过；随机回归中的 app E2E 在 45 秒总期限处发生 8 次失败，最新状态均有实际推进至执行/终验。测试失败路径未取消 daemon，可能干扰后续轮次；已加入有界 cleanup，并将整个端到端等待设置为 120 秒（不修改 Provider/Validator 业务超时），对 app 包重跑完整 20 轮及 app/orchestrator race。app 修正后完整 20 轮 PASS（546.618s），app race PASS（91.713s）；最后一次 cleanup 改为独立 finished channel 后，m5-safety 与 m6-release 又分别 PASS（30.217s / 31.087s）。orchestrator 的四条完整链路在原 60 秒测试总期限处失败，结合已复现的 race wrapper 退出开销，将该测试总预算设为 3 分钟后，完整 race PASS（460.851s）；所有 Provider 10 秒和 Validator 5 秒业务限制及漂移拒绝断言保持。其余全包 race PASS，包括 CLI 180.895s、SQLite 53.743s。原始失败日志保留，不将修复前的随机回归报告为 PASS。

随机回归全仓完成后，CLI 20 轮 PASS（3124.036s），除 app 与 orchestrator 外其余包全部 PASS。orchestrator 仅两条测试在旧 60 秒总期限附近失败共 5 次；已保留原失败，并以修正后的 3 分钟测试预算重跑两条失败链路及规划测试各 20 轮。真实 Goal 已通过；最后修复后的定向随机复验也全部通过，结果见文末组合门禁结论。


### 逐项验收定位

以下是当前已完成增量要求与证据的映射。

| 要求 | 直接证据 |
| --- | --- |
| AC-FR-001、AC-ISO-002/003/006 | project/projectinit 身份与绑定测试；离线 doctor；CLI flags/env/help/退出码回归；PLAN-009 组合 race |
| AC-ISO-001 | 双项目真实 CLI Goal、客户端 SIGINT、停止 A 后 B 同一实例完成、独立 Report/SQLite |
| AC-ISO-004/005 | `TestOwnershipExcludesAlternateStateAndRejectsBoundOverride`、`TestPathsAreShortAndOverridesUseOnePrecedence`、并发 start 实例身份测试、daemon shutdown/失败收尾/socket inode 测试；208 字节实际仓库根的 init/start/status/stop |
| AC-BG-001 | 接受事务回滚与 12 并发同 key；慢 Planner 时实际 CLI 接受及时返回；客户端退出后完成 |
| AC-BG-002 | 原子发布 trigger 回滚；已保存 Observation 恢复不调用 Provider；真实 daemon SIGKILL 后新 generation 完成 |
| AC-BG-003 | 持久 pause/cancel 与迟到结果隔离、配置/CAS 重试、`goal plan` CLI、真实进程控制测试 |
| AC-BG-004 | ProjectExecutionSlot、active Probe 共享生命周期；Supervisor/Recovery 两平台真实启动屏障、身份核对和组回收 |
| AC-BG-005 | planning_legacy/recovery：IN_PROGRESS、半冻结图、失败 replan 来源、暂停预算；process_recovery：孤立 Work、未知进程、未决 Promotion |
| AC-FR-030、AC-CWD-001/002/003/004/006 | PLAN-011 完整验收与 PLAN-010 接入后的两个连续 Goal；私有 Tree、HEAD/index、工作目录、Report/Receipt、迁移和清理断言 |
| AC-CWD-005 | Scope/现场保留、精确 retry、未知漂移拒绝、pause/cancel/进程恢复，以及 pending Promotion 读回回归 |
| AC-BG-006 | 本节真实 Provider 记录、文末已通过的组合门禁、Linux 原生进程测试及 macOS/Linux 构建 |


### 版本探测故障与最终复验

受限环境下的定向复验在 3.8s 左右一致失败；增加测试失败归因后定位为 `ENVIRONMENT_PREP_FAILED: environment snapshot tool_versions contains an invalid entry`。直接命令对照确认，已安装 Codex `--version` 返回 0 且 stdout 为正确版本，但 stderr 另有无法创建 PATH aliases 的权限警告；原 `runVersion` 将两流拼接，与 Snapshot 的单行版本契约冲突。这不是 Scope 现场丢失，也不是 3 分钟业务回归超时。

最小修复分别收集 stdout/stderr，优先 stdout、为空时使用 stderr；两流合计仍受 64 KiB 上限，非零退出/进程错误仍失败，非法多行版本在 probe 处拒绝。可选 probe 复用既有 unavailable，Required probe 保留工具名和错误。正式 `TestVersionProbesSeparateDiagnosticsAndRejectMalformedRequiredOutput` RED（1.392s）→GREEN（2.752s），独立 Reviewer 在当前 sandbox 复验 PASS（2.862s）并 Review PASS。此变化没有扩大环境权限或隐藏失败。随后 Go cache 写入被 sandbox 拒绝的两次 setup failure 保留记录，未当作测试已启动；最终回归使用已获准的本地构建缓存权限。


补充 AC-ISO-004 的实际长路径检查：用最终代码构建临时 CLI，在 208 字节的 Git 仓库根执行 init、start、身份/readiness status、stop、停止后 status；实际 socket 为 74 字节，实例身份一致，READY 持锁，STOPPED 已释放所有权，全部通过（PID 94443）。临时脚本最初将状态误写为小写、随后遗漏 STOPPED status 的既有退出码 6，修正脚本断言后完成，不修改生产契约。测试未创建 Goal 或调用 Provider，所有所属 daemon 已停止，临时仓库已清理。


## 最终组合门禁与关闭结论

2026-09-05 最终门禁 **PASS**。原始 `make verify-m6` 因测试总期限问题返回 2；没有把该次命令改写为成功。完成全量结果收集后，仅对失败项及随后修改影响的检查复验，组合覆盖所有 verify-m6 targets：

- 全仓普通测试通过；全仓随机 20 轮中的其余包通过，app 修正后完整 20 轮通过。最后的 orchestrator 定向随机命令对两条失败完整链路及规划测试各运行 20 次，40 次完整链路全部通过，包结果 PASS（1874.369s，`/tmp/xgoal-plan010-orchestrator-shuffle-final.log`）。
- 全仓 race 以 app、orchestrator 和其余包组合完成；最终版本探测修复后的 environment 完整 race PASS（23.087s），最后 Planner 提示的定向 race PASS（7.996s）。
- 格式、全仓 vet、CLI smoke、协议/失败矩阵、M5/M6 release、SQLite 与平台交叉构建通过；最终生产修改后再次执行 fmt-check/vet/macOS ARM64 + Linux AMD64 build，返回 0（`/tmp/xgoal-plan010-final-static-build.log`）。
- Linux ARM64 Supervisor/Recovery 原生运行通过；真实 Codex Adapter smoke 与真实 Codex Standard CLI 后台 Goal 通过；跨 Provider 在线补测未执行的边界保持如上。固定 Benchmark Suite validate 通过，真实三组 Benchmark 仍为 NOT_RUN、upload=false。
- 代码/测试/配置范围经 [REVIEW-047](../reviews/REVIEW-047-change-durable-planning.md) 独立审查及最终修复复核，无未决 correctness finding；格式与 tracked/untracked Diff 检查通过。

AC-FR-001/030、全部 AC-ISO/AC-BG/AC-CWD 的本轮增量要求均有当前证据。PLAN-009、PLAN-011、PLAN-010 已全部完成，可汇总 OBJ-003 为已完成。没有引入全局 daemon、消息队列、自动系统服务安装或额外恢复状态机；项目状态、后台执行与当前目录交付的边界一致。
