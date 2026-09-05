# DESIGN-005：M6 最终闭环、Benchmark 与发布

## 状态与修订

M6 历史设计与验收保留其原版本含义。OBJ-003 将本设计修订为当前主 checkout 原地执行，替代 clean final validation worktree 的假设；修订版需要当前 Spec/Design 审查及运行验收。工作目录/快照协议由 [DESIGN-001](DESIGN-001-m2-git-environment-validation.md) 拥有，项目单实例与后台生命周期由 [DESIGN-004](DESIGN-004-m5-control-daemon.md) 拥有。

## 1. 核心不变量

1. Final Report 是 SQLite/Evidence 的确定性投影；只报告 xgoal 直接观察到的运行时长与次数，不采集或推断模型 Token/费用。
2. Goal `COMPLETED`、Final Tree、Final Evidence Set 与 Report Hash 在一个 SQLite 事务中提交；Agent claim、退出码或 Reviewer approved 不能参与替代。
3. 报告文件采用 prepare→DB commit→atomic rename→read-back；任一点崩溃都能按已提交 Hash 完成 rename 或重建同内容，不能出现错误内容的“已完成”报告。
4. Benchmark 三组共享同一 fixture hash、目标、隐藏验收和逐任务超时；失败与人工介入不能静默排除。
5. Release checklist 只引用当前执行 Evidence；未运行真实基准时输出 `NOT_RUN`，README 不宣称未经验证的性能提升。
6. 最终文件留在当前项目目录。xgoal 不为实现、验证、审查或最终验收创建、删除 Git worktree，也不另复制执行代码目录。项目执行串行，用户 HEAD、symbolic ref 与真实 index 保持不变。
7. Tree 由当前文件系统与临时私有 index 构造，Commit 写入 `refs/xgoal/goals/<goal-id>/integration` 作为审计记录。更新该私有 ref 不代替核对当前目录内容；Final Evidence 的 Tree 和 Git 身份必须匹配最终现场。

## 2. Final Report 模型

`internal/report` 定义 `xgoal.final-report/v1`：Goal 原文/Revision/Config、Work/Attempt/角色、Final Commit/Tree/Scope/Design Decisions、逐 AC Evidence、Validator command/receipt/reproduction、Gate/Finding、运行时长/次数、限制/取消范围与时间。当前目录执行的报告还绑定项目身份、执行根及 Git 身份，说明 Final Commit 是私有审计引用、用户 HEAD/index 未被提交或切换，交付文件位于当前目录。Fact/Claim/Inference/Decision 均显式标 Authority。

JSON 使用 canonical 编码并计算 `json_hash`；Markdown 由固定顺序和 escaping 渲染，再计算 `markdown_hash`。`report_hash` 对两个内容 Hash、Goal Revision、Config、Final Tree 与 Evidence Set 做 canonical hash。文件只写入项目私有 state 的 `reports/<goal>/`，目录 `0700`、文件 `0600`。

## 3. Finalize 事务与文件状态

SQLite v5 新增 `final_reports`：绑定 Goal/Revision/Config/Tree/Evidence Set、JSON/Markdown 临时与最终相对路径、内容 Hash、组合 Report Hash、`PENDING_RENAME|COMMITTED`、版本与时间。

流程：

1. 在项目串行槽内核对当前目录的 Candidate Tree 与 Git 身份，在同一根目录运行全部 required final validators。每个验证前后重新读回 Tree、HEAD、symbolic ref 与真实 index；发生漂移时，该结果不能进入 current Evidence Set，不进行 finalize。既有 Reviewer Evidence 也必须满足相同 Candidate 绑定与当前性条件。
2. Report Manager 由已读回事实生成字节，在同目录写随机临时文件、`fsync`、Hash 读回。
3. 提交前再次核对私有 Integration Ref、当前目录 Tree 与 Git 身份。`FinalizeGoal` 单事务重读 Required Work、Criteria、Finding、Required Gate、Policy、Final Evidence/Human Acceptance，写 completion facts、`final_reports(PENDING_RENAME)`、Goal `COMPLETED` tuple 和 Event。外部用户并不受 daemon 锁约束；发现外部修改时停止提交并保留现场，不把 SQLite 原子性描述为对外部文件写入的锁定保证。
4. 原子 rename 两个文件并重新 Hash；再把报告状态置 `COMMITTED`。API 只返回 Hash 验证通过的 committed 文件。
5. 重启时先处理 `PENDING_RENAME`：临时文件匹配则 rename；最终文件已匹配则幂等确认；均缺失时用数据库绑定的 canonical payload 重建同 Hash；任何不匹配进入 Invariant Failure/Gate，不能返回报告。

为支持重建，SQLite 同时保存 canonical JSON 和 Markdown bytes；路径只保存 state root 内相对路径。报告重建只恢复报告字节，不还原或覆盖用户文件。`clean` 查询引用，只处理策略允许的私有制品，不能删除当前 checkout、真实 index、旧 Git worktree、未过保留期或 Evidence/Report 引用对象。

## 4. Benchmark 合同

`benchmarks/suite.json` 固定任务 ID、类别、initial fixture directory/hash、goal、acceptance argv 和 timeout。Harness 为每组建立独立的临时可信 fixture 仓库；这属于比较实验的输入准备。Runner 得到该仓库与 Goal 后，xgoal 的实现、验证与 Review 均在其当前主 checkout 执行，不再复制执行代码或创建 worktree。隐藏 acceptance 在 runner 完成后直接针对该最终目录执行。

- A `native`：直接调用一个受信 Agent CLI，不注入 AutoGo/xgoal runtime。
- B `autogo-single`：同一 Agent CLI，加项目内 AutoGo 治理入口，仍只允许一个实现 Agent。
- C `xgoal-standard`：经 xgoal Standard 控制面、独立验证与 Review。

Runner 是命令模板，不在 suite 中保存 Secret。Harness 创建临时 Git fixture、固定 initial commit/tree，记录 tool versions、argv hash、start/end、exit、acceptance receipt 与人工介入。三组的 goal/fixture/acceptance/timeout hash 必须完全相同，否则拒绝比较。

结果字段严格包括规范指标；`false_completed = runner_claimed_completed && !final_acceptance_pass`。聚合保留每次失败与方差所需原始 run，不自动上传。

## 5. 发布制品与授权

- `LICENSE` 采用 Apache-2.0；用户的“自行决策”授权覆盖仓库许可证选择，但本实现不构成法律意见。
- `ACKNOWLEDGEMENTS.md` 说明 AutoGo 与 LoopX 的思想启发及 clean-room 边界，不暗示代码复制或背书。
- Threat Model 明确可信仓库、L0、Provider Transport/Project Network/Secret、恶意脚本、PID 复用、日志和授权风险。
- ADR 固化确定性 Kernel、SQLite 单一真相、串行 Promotion、报告提交协议与 Benchmark 公平性。
- 不 push、不创建远端 release、不发布 Benchmark；这些仍需独立 Human Gate。

## 6. 验证与发布判定

发布 AC 矩阵每项只有 `PASS|FAIL|NOT_RUN`；PASS 必须指向 test/operation/artifact/commit。`False Completed=0` 只对实际执行的 benchmark runs 计算，不能从空集合宣称成功；真实三组 benchmark 未运行时，AC-NF-004 的“Suite 可复现”可 PASS，但所有比较指标保持 NOT_RUN。

最终门禁包括：format/vet/test/race/shuffle、Darwin/Linux 无 CGO 编译、migration/restart/tamper、每个 Effect 边界故障测试、真实跨 Provider 路径、真实 Unix CLI、Final Report 恢复和 release AC 审计。OBJ-003 必须补充当前目录协议的运行证据：所有角色 CWD、worktree 清单不变、HEAD/ref/index 保留、验证/Review 前后快照、私有 ref/最终目录/Report 一致性，以及停止/取消/崩溃后的现场保留与可操作恢复。历史 worktree 验收不能替代这些检查；平台原生进程/恢复测试与交叉编译分别报告。

## 7. 非目标与恢复

M6 不增强 L0 为强隔离，不允许 remote push/publish/production，不替用户公开 Benchmark。报告恢复失败时保留 DB、临时文件与诊断，Goal 的 completion tuple 不被静默改写；读 API fail closed，等待人工处理或从同一数据库 payload 重建。

失败、停止与 cancel 均保留当前文件及 Git 现场，不自动 reset、clean、stash、切分支或从旧工作区恢复覆盖。旧版历史 worktree、Review 和报告只读保留；绑定旧工作区的未完成 Goal 进入明确迁移等待，不能自动在当前目录继续执行。历史已完成报告仍按原 Hash、Tree 与版本读回，不追溯改写为当前目录验收。
