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

## PLAN-010：待实施与验收

持久 Planner、所有角色进程归属、客户端断连与双项目完整 Goal 尚未验收。AC-ISO-001、AC-BG 与包含全部中断恢复语义的 AC-CWD-005 保持未完成。真实 Provider smoke 与 Linux 原生进程测试将在后续阶段单列；不以交叉编译替代运行证据。
