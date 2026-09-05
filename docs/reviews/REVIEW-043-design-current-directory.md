# REVIEW-043：当前工作目录、持久后台与历史兼容 Design Review

## 审查对象

- Objective：`OBJ-003`；Plans：`PLAN-009` → `PLAN-011` → `PLAN-010`。
- 上游合同：[REVIEW-042](REVIEW-042-spec-current-directory.md) 已对其绑定的产品/技术 SPEC 给出 `PASS`。
- 代码基线：`24960390d71237d6266e2f995b4e3df0b6719a78`，分支 `fix/project-daemon-isolation`。
- 独立 Reviewer 未编写下列 Design；审查只写 Review 和索引，不修改设计、实现、Plan 或 Progress。

| 被审制品 | SHA-256 |
| --- | --- |
| [DESIGN-001](../architecture/DESIGN-001-m2-git-environment-validation.md) | `b2575e94703423ec585a0004bd33ec9689c2642686e3bec95002c6e299b90092` |
| [DESIGN-002](../architecture/DESIGN-002-m3-codex-adapter.md) | `a81fbde6cfdf7ade3cb84a185f67620bf05d2db39cd581da21a74426d295aac0` |
| [DESIGN-003](../architecture/DESIGN-003-m4-claude-review.md) | `6d45e4db4518d4fd3c7cd4ca1e495519f9463ace1a5a75ec00eea8e6829d693c` |
| [DESIGN-004](../architecture/DESIGN-004-m5-control-daemon.md) | `99468c31ddd0f35e5aada237817a972604d775e286e6f7055c5382d6e03ac698` |
| [DESIGN-005](../architecture/DESIGN-005-m6-finalization-release.md) | `662ff396183f5333b7257858fc7aa937914966ebb1addce6bfa80a7632da7fb1` |
| [THREAT-MODEL](../architecture/THREAT-MODEL-v0.1.md) | `befc5753562eb0fd9737ef3e56a89594249a5fdc1a1ff602ba97932930410377` |

## Verdict

`PASS`

当前设计满足所审 Spec，职责、数据兼容、失败恢复和验收路径足以进入实现，无未解决阻塞。原有 M2–M6 验收仍只证明对应历史执行模型；[REVIEW-040](REVIEW-040-design-004-project-background.md) 不能替代当前修订版的本次结论。

## 审查结论与 Evidence

- **单一执行目录。** DESIGN-001 明确所有业务代码执行发生在绑定的当前主 checkout，`.xgoal` 只保存 metadata、内容对象、日志和临时 index。对象级 Patch 重建不创建物理代码副本。DESIGN-002/003 的 Adapter、Review 和 DESIGN-005 的 Final Validation/Benchmark 同步该约束，没有隐藏的旧 worktree 后门。
- **用户 Git 状态与快照分离。** 初始 HEAD/index/raw Tree 准入、连续已验收结果采用、私有 index 和原始 blob 构造、HEAD/symbolic ref/index 前后核对，与当前 `gitrepo.IndexAndWriteTree` 会修改 index 的真实实现差异对应。`refs/xgoal/...` 与用户分支分离；快照、审计 Commit 和最终目录交付各有清楚用途。
- **现地验收。** DESIGN-001 第 6 节、DESIGN-003 Review 合同与 DESIGN-005 Finalize 流程都要求 Tree/Git 身份前后读回。期间漂移时不能沿用旧 Tree 的 PASS。独立性保留为不同 Profile/Session、只读权限和绑定 Packet，不再虚构物理工作区隔离。同阶段受管服务可为 runtime probe 存活，必须禁止并发写源码并在阶段结束回收。
- **状态及迁移最小化。** 新 workspace Snapshot 的执行路径是当前 root，数据库原有 `workspaces.path UNIQUE` 对新模型保存唯一制品目录，另用 `execution_path/execution_model` 区分执行根和旧数据语义。该方案保留原外键/历史，不为重复执行路径重建所有 Evidence 表。持久 checkout 控制只拥有活动归属及已验收/已观察 Tree，marker 仍是不可变历史，未建立第二个调度状态系统。
- **失败现场与连续执行。** DESIGN-001 第 3、7 节拒绝其他 Goal 接管未验收修改；原 Goal/Work 的显式 retry 必须匹配保存的观察和 HEAD/index。未知变化、Scope 越界或无法证明归属时保留等待，不自动 reset/clean/stash、不引入 restore Effect。正常用户提交后可从新的干净 HEAD 建立基线。该方案比自动逐文件回滚更简单，也符合保留用户文件的授权边界。
- **持久后台与旧执行者。** DESIGN-004 原子接受 Goal/Planner Effect/响应，原子发布 Revision/Plan/Work；OBSERVING 只读回结果，generation 与最新控制意图阻止迟到发布。项目锁覆盖 DB 打开到关闭；启动 pipe 握手关闭“已执行但未登记”的窗口，所有角色/验证服务均具备可恢复进程归属。进程组确认结束与输出消费者结束均列为关闭条件，不以父 PID 退出代替。
- **Promotion 与最终闭环。** DESIGN-001 保留 Effect/Commit marker/ref CAS/DB observation，并将 checkout 最后验收结果纳入事务收口。DESIGN-005 在 finalize 前再次核对私有 ref、当前目录与 Git 身份，随后保留既有报告 prepare/commit/rename/read-back 协议。报告恢复只重建报告字节，不恢复覆盖用户代码。
- **旧历史与能力限制。** 新 marker/执行模型按版本分派；旧完成 Report/Evidence 保留原 Hash，旧未完成 worktree Goal、Session、Promotion 只进入明确迁移等待。旧配置给出 CONFIG_MIGRATION_REQUIRED，但已有报告和诊断仍可读；clean 永不删除当前或旧 worktree。THREAT-MODEL 明确 L0 无文件系统事务隔离、同账号编辑不受锁约束、跨项目资源仍共享，不用 SQLite 原子性夸大外部文件保证。

## 必须落实的现有验收边界

以下是当前设计完成条件的实施核对点，不是新增机制或延后项：

1. `workspaces.path` 制品位置与 `execution_path` 不得在 Environment、Review、Validator、Promotion 或 Cleanup 中混用；旧 marker/报告必须按旧协议读取，不能补新字段后重算历史 Hash。
2. 准入与捕获不得为了获得“clean”而调用会修改用户 index、执行 filters 或重新定位仓库的 Git 路径；覆盖继承 Git 定位变量、CRLF/filter、tracked ignore、未跟踪文件和 `.git/.xgoal` 排除测试。
3. legacy 配置/执行模型等待不得阻止 daemon 提供只读历史查询；旧未决 Effect 不自动按新模型继续，也不让其静默占据新执行槽。
4. 故障测试必须覆盖当前目录保留、explicit retry 的 CAS/归属、前后验证漂移、进程启动屏障及子孙回收、Commit/ref/DB/report 窗口；macOS/Linux 原生行为与交叉编译分别报告。

## 下一路由

允许按已审 Plan 顺序实现当前版本。PLAN-011 可以领取原地目录会话、私有 index/对象捕获及布局兼容的原子 Item；PLAN-010 在其依赖完成后贯通持久规划和统一资源回收。任何后续改动若改变唯一目录、用户 Git 状态、执行模型迁移或失败恢复语义，应返回对应 Spec/Design owner 并重新审查。

本次未运行功能测试，也未将设计审查结论用于勾选实现或验收。Evidence 来自被审文件及 SHA-256、现有 `workspace`/`gitrepo`/`patch`/`validator`/`promotion`/`sqlite` 接入约束和已通过 Spec 的逐项对照。
