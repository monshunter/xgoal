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

## PLAN-011 与 PLAN-010：待实施与验收

当前目录执行、私有 Tree/Evidence 链路、旧配置/历史兼容、持久 Planner、所有角色进程归属与双项目完整 Goal 尚未验收。AC-ISO-001、AC-BG、AC-CWD 保持未完成。真实 Provider smoke 与 Linux 原生进程测试将在后续阶段单列；不以交叉编译替代运行证据。
