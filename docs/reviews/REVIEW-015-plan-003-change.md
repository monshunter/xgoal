# REVIEW-015：PLAN-003 M2 Change Review

## 审查对象

- Plan：`PLAN-003`
- Revision：`feat/xgoal-v0.1` 未提交工作树，Phase 4 验收后的首次 Change Review
- 产品 SPEC：SHA-256 `f46fedb46d31487a2498efb506664b42aa5e13ccb8d2257c5ba9a4e0cb1c187f`
- 技术 SPEC：SHA-256 `81c53492d72a72842e76c3eb8d0ff85625da6bd70eeddf4ece6ad5ee04a073c8`
- Design：`DESIGN-001`

## Verdict

`FAIL`

## 发现

### [High] M2 运行制品只有 Schema，没有持久 Repository 与读回入口

- 位置：`internal/store/sqlite/migrations/0002_git_validation.sql`、`internal/workspace`、`internal/environment`、`internal/validator`、`internal/patch`
- Evidence：Migration 已创建 `workspaces`、`patch_bundles`、`environment_snapshots`、`validator_definitions`、`validator_runs`；生产 Go 代码对这些表没有任何 INSERT/SELECT，Promotion 测试只能直接执行 SQL 伪造 `patch_bundles` 行。
- 影响：进程重启后无法把 Workspace marker、Patch manifest、Environment Snapshot、Validator Definition/Receipt 与 Attempt/Effect 重新绑定；Promotion Preflight 所依赖的 Patch 行也没有合法生产写入口。当前通过的是孤立组件测试，不是 Design 声明的 SQLite 事实 owner 与可恢复链路。
- 路由：在 SQLite Store 增加五类不可变/状态化制品的严格写入与读回 API，逐字段核对 Canonical Hash、外键身份与磁盘引用；用公开 API 替换 Promotion fixture 的直接 SQL，并加入重开、重复写冲突、篡改拒绝和事务原子性测试。

## 已成立 Evidence

- `make verify-m2`：PASS；覆盖格式、全仓测试、五项 M2 失败矩阵、20 次乱序、Race Detector、`go vet`、CLI smoke、SQLite 双平台及全仓 Linux 无 CGO 编译。
- 真实 Git detached worktree、Agent 自建 Commit 后文件系统捕获、Scope/Symlink 逃逸、Patch 冲突、文件/目录拓扑转换、Validator 超时与定义变更、Promotion Commit 后崩溃恢复：PASS。
- Promotion 成功/失败后的 Attempt、Work、Lease 与 Effect 原子状态对账：PASS。
- 上述 Evidence 没有覆盖制品跨 Store 重开持久读回，因此不能关闭 Plan。

## 下一路由

回到 `autogo-tdd` / `autogo-change-implement` 补齐 M2 Artifact Repository，运行定向持久化测试与完整 `make verify-m2` 后重新 Change Review。
