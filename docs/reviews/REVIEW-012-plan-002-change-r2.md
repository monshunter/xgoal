# REVIEW-012：PLAN-002 M1 Change Review 修订复审

## 审查对象

- Plan：`PLAN-002`
- Revision：全部 Item 完成后的 `feat/xgoal-v0.1` 工作树
- 前序 Review：`REVIEW-011`
- 产品 SPEC：SHA-256 `f46fedb46d31487a2498efb506664b42aa5e13ccb8d2257c5ba9a4e0cb1c187f`
- 技术 SPEC：SHA-256 `81c53492d72a72842e76c3eb8d0ff85625da6bd70eeddf4ece6ad5ee04a073c8`

## Verdict

`PASS`

## 前序发现处置

- `ClaimWork` 现在于同一 Immediate Transaction 内重查 Work Version/State、active Goal Revision、active Plan、依赖、Required Gate 和项目级单活 Lease；Ready 后新建 Gate 的直连 Claim 负向测试通过。
- `UpdateGoalState` 硬拒绝 `COMPLETED`；只有 `CompleteGoal` 能在重读全部持久事实后写入最终状态。
- `goals` 表新增最终元组 CHECK：`COMPLETED` 必须同时具有 Final Tree、Evidence Set ID 与 Report Hash，其他状态三个字段必须全部为空；伪造初始状态/最终元组测试通过。

## 当前 Evidence

- `make verify-m1`：PASS；覆盖 `gofmt`、全仓测试、20 次乱序、Race Detector、`go vet`、CLI smoke，以及 `CGO_ENABLED=0` 的 Darwin arm64 与 Linux amd64 SQLite 测试包交叉构建。
- 真实子进程退出后的 SQLite 重开、非终态 Effect 扫描、Read Back、恢复状态推进及同 Key 幂等重放：PASS。
- 32 路跨 Work Claim 竞争只有一个项目级 Active Lease/Attempt/Claimed Work；32 路同 Key Idempotency Begin 只有一个创建者：PASS。
- Goal/Plan/Work/Attempt/Lease/Gate/Effect 状态转换矩阵和全部终态不可回退穷举：PASS。
- Completion 未满足拒绝、事件插入故障回滚、满足后最终元组跨重开持久化：PASS。
- `git diff --check`：PASS。

## 范围与剩余边界

- M1 只关闭持久 Store、控制状态、Lease、调度、Effect 恢复与持久完成事务；不证明真实 Agent、Git Workspace/Patch/Promotion、Daemon/API 或用户旅程。
- M5 仍需 OS 级单写者锁、Gate 过期/撤销/消费与完整 Reconcile；M4/M6 仍需 Evidence Store、Final Validator 和报告临时文件原子 rename/read-back。当前 Completion Facts 是这些后续能力写入持久完成事务的最小边界。
- Scope 的 Unicode NFC、大小写碰撞、Symlink 与跨平台路径集合验证仍归 M2；没有产品 `AC-FR-*` / `AC-NF-*` 因 M1 局部 Evidence 被提前勾选。

## 下一路由

允许使用 `autogo-change-close` 对账并关闭 `PLAN-002`，创建一个 M1 原子 Commit；随后为同一 `OBJ-001` 建立并审查 M2 Plan。
