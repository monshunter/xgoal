# REVIEW-045：PLAN-011 当前主目录执行 Change Review

## 审查对象

- Objective：`OBJ-003`；本阶段为 [PLAN-011](../plans/PLAN-011.md)，基线 `d8f21a3b369afd20231d2cda0801513d8c5124dd`，分支 `fix/project-daemon-isolation`，审查日期 2026-09-05。
- 上游：[REVIEW-041 Plan Review](REVIEW-041-plans-current-directory.md)、[REVIEW-042 Spec Review](REVIEW-042-spec-current-directory.md)、[REVIEW-043 Design Review](REVIEW-043-design-current-directory.md)。本次核对当前目录执行、用户 Git 状态保护、快照/Patch、现地验证/审查、私有晋升、失败现场、旧状态兼容和真实 CLI/daemon 验收；PLAN-009 的已提交项目所有权作为依赖。
- 被审范围包括 `internal/gitrepo`、`patch`、`workspace`、`environment`、`validator`、`review`、`promotion`、`orchestrator`、SQLite 的 checkout/artifact/promotion/legacy/finalize 状态及 migration 0008，以及 config/init/doctor/control/app 的新模型接入、CLI 验收和相关文档。
- 本记录汇总跨 owner 的独立审查，不能描述为全部实现均由同一个未参与作者的 Reviewer 审查。Git/Patch 作者独立审查 workspace/SQLite；执行链作者独立审查 Git/Patch/Promotion/config/app/control；Promotion 作者独立审查执行链与公开 finalize，并补充真实 Git 故障测试。各 owner 修复自己的实现，发现方复核。汇总者另外编写独立 SQLite 与真实 CLI 场景，未以自己对 Git/Patch 的自审替代 peer review。

持久 Planner、所有角色进程登记与崩溃回收、客户端断连后规划继续、双项目完整 Goal、真实 Provider smoke 和 Linux 原生运行属于后续 PLAN-010。本 Review 不把这些已明确分期的工作当作 PLAN-011 已完成，也不把当前本地 fixture 结果推广为真实模型服务验收。

最终被审代码为相对上述基线修改/新增的 68 个 `internal/**/*.go` 与 `internal/**/*.sql` 文件，包含实现和测试。按相对路径字典序拼接 `SHA256(file) + 两个空格 + path + 换行`，清单 SHA-256 为 `a840f9475fd2dd08dd9aacce1dfa52a86686229494685ae47e827dcd8505ddc1`。该摘要在所有已知修复与最终全仓回归通过后生成；后续代码变化需要按影响重新核对。

## Verdict

`PASS`

当前已知 correctness、用户数据保护与恢复发现均已修复并通过独立复核；最终 `go test ./... -count=1` 全仓通过。当前目录交付、元数据保护、私有晋升和证据闭环符合 PLAN-011 合同，没有阻止本阶段收口的未解决发现。本结论替代本 Review 修复过程中的 `FAIL`；不替代 PLAN-010 的后续实现和整体 Objective 验收。

## 发现与修复复核

### F1 · P1：默认全局 ignore 未进入 Checkout Identity（已修复）

位置：`internal/gitrepo/checkout.go` 的 `readCheckoutMetadata`。

原实现只对显式 `core.excludesFile` 读取内容。未设置该键时，Git 仍读取默认全局 ignore；执行期间改变规则可隐藏新增文件，而 `CheckCheckoutIdentity` 返回成功。独立临时 overlay 的 `TestReviewDefaultGlobalIgnoreIdentity` 实际失败（0.590s），证明不是仅凭代码推断。

修复读取有效 ignore 路径、存在性及内容：未设键时使用 `XDG_CONFIG_HOME/git/ignore`；XDG 未设或为空时使用 `HOME/.config/git/ignore`。显式路径优先，显式空值禁用默认；NUL 输出保留合法路径首尾空格。默认路径依据 [Git 官方 core.excludesFile 文档](https://github.com/git/git/blob/master/Documentation/config/core.adoc)；显式空值与路径选择同时由实际 Git 快照测试确认。

正式 `TestCheckoutIdentityTracksEffectiveGlobalIgnore` 覆盖默认文件已有/原先不存在、空 XDG 回退、显式尾空格路径与显式空值。修复前全部五个子场景失败（2.608s），修复后全部通过（4.398s）；隔离临时 HOME/XDG，不修改真实用户配置。发现方重跑原 overlay 通过（1.168s）；修复后 Git/Patch 完整 race 通过（15.758s/9.755s），定向 vet 通过。

### F2 · P1：RefUpdated 后取消 Work 会破坏 Promotion 恢复（已修复）

位置：`internal/store/sqlite/work_control.go` 的 `CancelWork`、`promotion.go` 的 `Observe`。

独立审查复现私有 ref 已更新、DB observation 未完成时取消 Work 会提前撤销 Lease，使 journal 后续无法通过 Observe 的生命周期前置条件。结果是目录已有晋升结果，数据库却无法完成原子观察。

作者已在取消事务中拒绝当前执行模型的未完成 Promotion，提示先恢复再取消；旧执行模型的冻结 journal 不阻止取消旧 Goal。`TestPromotionJournalPreflightAndEffectObservation` 增加 RefUpdated → 拒绝 Cancel 并保留 Work/Lease → Worker recovery 保留 Lease → Observe 收口的链路。发现方重跑原 overlay 通过（0.783s），最终全仓回归也覆盖正式回归。

### F3 · P1：终验期间仅修改私有 Ref 仍可能错误完成（已修复）

位置：`internal/orchestrator/finalize.go`，Finalizer 调用前的最终读回。

原代码只复核当前源码 Tree/HEAD/index，SQLite accepted tuple 不能观察 Git ref。独立 overlay 让只在 final 阶段运行的 Validator 把 `refs/xgoal/goals/goal_e2e/integration` 改回原 HEAD；源码仍正确，Goal 却进入 `COMPLETED`。实际 RED 为 44.883s。修复在完成事务前重新读取私有 ref，要求 Commit 与 Tree 都等于本轮 integration；正式 `TestEngineRejectsFinalValidationPrivateRefMutation` 保留这一覆盖。

发现方独立执行 final-ref、取消后退出确认、过期 Lease 失败处理、未知退出保留所有权四项 race 测试全部通过（50.112s，其中 final-ref 47.83s）。这证明私有 ref 漂移不能被源码 Tree 相同掩盖。

### F4 · P1：公开 finalize 缺少当前目录和私有 Ref 核对（已修复）

位置：`internal/control/service.go` 的 finalize dispatch、`internal/control/checkout.go`、`internal/store/sqlite/final_report.go`。

原公开入口可以绕过 Engine 的现地检查，用旧受验 Tree 发起完成。修复在 dispatch 检查当前 Checkout Identity/raw Tree、私有 Ref 和 accepted tuple，并在 Store 完成事务约束执行模型与 accepted tuple。

独立 `TestPublicFinalizeRejectsLiveCheckoutDriftAndPreservesScene` 实际改变源码、HEAD Commit、符号分支、index、私有 Ref，五种场景均返回 `409 CHECKOUT_WAITING`，文件与 Goal state/version 保持；显式恢复后 guard 通过。定向测试通过（8.410s）；临时屏蔽 dispatch guard 的 overlay 使源码场景按预期失败（1.248s），实际生产文件没有被该反证实验修改。control/app/config/init/doctor 组合 race 全通过。

### F5 · P1：未知退出或恢复中 Promotion 被提前释放执行所有权（已修复）

位置：`internal/orchestrator/execute.go` 的 Worker 登记失败处理与 `waitAgent`；`internal/store/sqlite/worker.go` 的 `ResolveWorkerRecovery`。

登记失败后原路径忽略取消失败，可能把仍存活执行者当作已停止并释放 Lease。修复用独立关闭 context 复用取消/Wait 确认；不能确认退出则保留 `errExecutionStillRunning` 和执行槽。已退出 Worker 仍有待观察 Promotion 时，记录退出但保留 Attempt/Work/Lease，由 Promotion Observe 完成生命周期。未知/LOST 的旧进程不能凭推测释放。

独立四项 race 回归覆盖取消后实际退出确认、未知退出保留所有权和过期 Lease 下的失败收尾，全部通过。SQLite Observe 同一事务更新 accepted commit/tree、清空已处理 Work、完成 effect/生命周期并只关闭匹配的 recovery Gate，不关闭无关人工 Gate。

### F6 · P1：旧冻结 Promotion 永久阻塞新干净 Goal（已修复）

位置：`internal/store/sqlite/checkout.go` 的 `checkoutIdle`、`legacy_execution.go` 的 `ReconcileLegacyExecution`。

旧未决 Promotion 不会按新模型恢复，却被计入当前执行槽；旧已退出 Worker 的 ACTIVE Lease 也持续占有槽。修复只统计 current-directory 的未完成 Promotion，且只对有 `EXITED`/`TERMINATED` 记录的旧 Worker 撤销 Lease并记录 Event，保留未知退出的保守阻塞。

独立 `TestLegacyPendingPromotionDoesNotOwnNewCheckoutAfterRecordedExit` 覆盖已退出和未知退出两个分支：Reconcile 重复执行保持旧 journal/effect 不变；有退出证据时允许新干净 Goal 准入，未知退出时仍返回 `ErrCheckoutBusy`。

### F7 · P2：未处理现场可被 Replan 遗弃，旧 Work 可错误 Retry（已修复）

位置：`internal/store/sqlite/control_m5.go` 的 `ActivateReplan`、`checkout.go` 的 `RetryCheckoutWork`。

修复在 Replan 事务变更前拒绝仍归属某 Work 的未处理现场；Retry 同时要求活跃 Plan 和当前 Goal Revision。独立 `TestCheckoutReplanAndRetryRespectUnresolvedSceneAndActivePlan` 直接断言拒绝时 Plan/Work/Checkout 原子保持、成功 Replan 后旧 Work 不能复活。该测试与 F6 组合通过（0.428s）。

### F8 · P1/P2：原始文件与 Git 对象边界缺口（已修复）

位置：`internal/gitrepo/checkout.go`、`repository.go`、`commit.go`、`internal/patch`。

已有真实失败测试分别暴露 Git untracked 清单遗漏 FIFO、私有 Ref 是指向用户分支的 symref 时 CAS 会解引用、replace refs 可以改变已固定 Blob ID 的读回。修复使用不跟随链接的原始文件遍历并由 Git ignore 筛选，拒绝不支持类型；私有 Ref 拒绝 symref 且更新使用 `--no-deref`；所有对象操作禁用 replace refs。Promotion 的独立 symref 故障测试确认不能移动用户 HEAD/index。

二进制、重命名、删除、模式、链接、非忽略 untracked、Unicode 原始名称及冲突的原有覆盖得到保留。Patch Replay 在对象级重建 Tree 并核对当前目录，不执行源码写入；私有 index、raw blob 和禁用 hooks/fsmonitor 的操作不触发 Agent 配置的过滤器或 hooks。

## 系统结论与验收 Evidence

**一致性。** 所有业务角色使用同一个当前主目录，workspace marker 的制品路径与执行路径分开。Checkout 状态与 ClaimWork 同事务，覆盖跨 Goal 接管与失败现场；Git Identity/raw Tree、私有 Ref CAS、SQLite accepted tuple 分别约束文件、Git 和状态边界。最终报告与 Evidence 仍沿用确定性完成判断，没有把 Agent `done` 或进程退出 0 当作成功。

**兼容与最小化。** migration 0008 为现有表增加执行模型/路径/身份并引入单一 checkout 控制，不重建历史 Evidence 表。旧 marker/Report 按旧版本读取，Hash 保留；旧未完成执行进入明确迁移等待，新 clean 只删 metadata。配置不兼容的 daemon 仍提供历史查询和诊断，不构造 Engine/Adapter 执行路径。默认 `current-directory`、README、操作手册和最终验收合同与新行为对齐。

**实际用户链路。** `TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence` 实际构建 CLI，执行 init/config validate、后台 start、第一 Goal 自然语言 Planner、第二 Goal Proposal、两次 `run --wait`、status/report、stop，测试通过（47.716s）。断言角色 CWD 为主目录、结果文件直接存在、两个 Goal 顺序采用 accepted commit；HEAD、index 原始字节、用户分支 refs 和 Git worktree 列表前后不变。停止后只读 SQLite 验证 Final Tree = 私有 Ref Tree = Report Tree = Final Evidence/Receipt Tree；第一历史 Report Hash 保持。Provider 为本地协议 fixture，daemon 是独立实际进程。

已核对测试代码及工具结果的主要命令如下；来源区分为本汇总者实际执行和跨 owner 独立复核，不把未执行命令列为通过。

| Evidence | 执行与结果 |
| --- | --- |
| `go test ./internal/cli -run TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence -count=1 -v` | 汇总者实际执行，PASS 47.716s。 |
| `go test ./internal/store/sqlite -run 'TestLegacyPendingPromotionDoesNotOwnNewCheckoutAfterRecordedExit\|TestCheckoutReplanAndRetryRespectUnresolvedSceneAndActivePlan' -count=1` | 汇总者实际执行，PASS 0.428s。 |
| `go test -race ./internal/gitrepo ./internal/patch -count=1` | 汇总者在 ignore 修复后实际执行，PASS 15.758s/9.755s；原 ignore overlay 由发现方独立复核 PASS 1.168s。 |
| `go test ./internal/gitrepo ./internal/patch ./internal/promotion ./internal/config ./internal/app ./internal/control -count=1` | 跨 owner 独立六包回归，PASS 9.498s/8.208s/32.078s/0.928s/25.181s/12.713s；随后新增 F1/F2 已分别原 overlay 独立复核，并由最终全仓回归覆盖最后代码。 |
| `go test ./internal/control -run TestPublicFinalizeRejectsLiveCheckoutDriftAndPreservesScene -count=1 -v` | 独立 peer 执行，五漂移 PASS 8.410s。 |
| `go test -race ./internal/orchestrator -run 'TestEngineRejectsFinalValidationPrivateRefMutation\|TestWaitAgentConfirmsProcessExitAfterCancellation\|TestFailureAfterLeaseExpiryReconcilesBeforeSceneDecision\|TestFailureWithUnconfirmedShutdownPreservesExecutionOwnership' -count=1 -v` | 发现方独立复核，PASS 50.112s。 |
| `go test -race ./internal/environment ./internal/validator ./internal/review -count=1` | 独立 peer 全包回归，PASS 5.151s/22.053s/9.686s。 |
| `go test -race ./internal/config ./internal/projectinit ./internal/doctor ./internal/control ./internal/app -count=1` | 独立 peer 全包回归，PASS 1.255s/3.353s/1.773s/18.306s/28.723s。 |
| SQLite/workspace/Codex Adapter 相关 race | 主任务执行，PASS 24.998s/4.003s/3.586s；含 schema 7→8 历史保护、当前目录 cleanup 与过期 Lease 故障。 |
| `go vet ./...`、格式与 Diff 检查 | 主任务已执行通过；最后 ignore 修复后 `go vet ./internal/gitrepo ./internal/patch` 与 Diff 检查通过。 |
| `go test ./... -count=1` | 主任务最终全仓回归 exit 0；orchestrator 136.442s、真实 CLI 63.228s、app 35.880s、SQLite 11.060s、gitrepo 22.750s、promotion 40.896s。首轮 opt-in Codex smoke 的旧接口编译问题已适配，最终结果不再包含该失败。 |

完整阶段运行记录见 [项目验收记录](../operations/PROJECT-DAEMON-CURRENT-DIRECTORY-ACCEPTANCE.md)。L0 模型不阻止同账号外部进程在检查间隙写文件；当前方案通过阶段前后读回和失败保留处理协作性变更，没有宣称文件系统事务隔离。

## 下一路由

允许主任务完成 PLAN-011 的 Spec/Operation/Plan/Progress 与 Git 对账，并创建原子提交；本 Review 本身没有勾选 Plan/Progress 或提交代码。随后按已审顺序继续 PLAN-010 的持久 Planner、全部角色生命周期和最终整体验收。若冻结摘要之后还有代码变化，应按实际影响重新验证和复核；本阶段 `PASS` 不代表整个 OBJ-003 完成。
