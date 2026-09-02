# PLAN-003：实现 M2 Git、环境与验证闭环

## 目标

建立不信任 Agent 工作区与 Git 历史的 M2 确定性链路：隔离 Workspace、内容寻址 Patch Bundle、干净重放、受信 Validator/Evidence，以及可崩溃恢复的串行 Promotion。

## 范围

包括可信本地 Git 仓库、Integration Branch/Worktree、Patch/Scope、Local Environment Provider、Supervisor、Validator Registry/Receipt、Evidence Staleness 与 Promotion；不包括真实 Codex/Claude Adapter、Daemon/API、完整 Reconcile/Gate/Budget、Final Report 和 Benchmark。

## Phase 1：冻结 M2 契约与设计

关联：[技术 SPEC M2](../../xgoal-technical-spec-v0.1.md#29-实施阶段)

- [x] 1.1 冻结 Patch Bundle、Command Receipt 与 Environment Snapshot 的可校验 v1alpha1 契约
- [x] 1.2 完成 Workspace、Scope、Validator、Evidence 与 Promotion 最小系统设计并通过 Design Review

## Phase 2：隔离并捕获 Agent 结果

- [x] 2.1 实现可信 Git 仓库、私有 Integration Branch 与 Attempt/Validation Worktree 生命周期
- [x] 2.2 实现 tracked/untracked/binary/rename/mode/symlink/delete 的内容寻址 Patch Bundle 捕获与完整性校验
- [x] 2.3 实现 Unicode/大小写/Symlink 安全 Scope 判定和最新 Integration Tree 上的严格干净重放

## Phase 3：建立独立验证 Evidence

- [x] 3.1 实现 Local Environment Provider、环境快照和受监督的受信命令生命周期
- [x] 3.2 实现只接受冻结配置的 Validator Registry、Command Receipt、日志与超时边界
- [x] 3.3 实现 Evidence Repository、权威等级、最终 Tree/Goal/Config 绑定与 Staleness 判定
- [x] 3.4 实现 Workspace、Patch、Environment 与 Validator 制品的 SQLite 持久 Repository 和跨重启严格读回

## Phase 4：串行晋升与恢复验收

- [x] 4.1 实现串行 Promotion、xgoal 元数据 Commit 和 Effect Read Back 幂等恢复
- [x] 4.2 验证 Agent 自建 Commit、Scope 逃逸、Patch 冲突、Validator 过期和 Promotion 崩溃矩阵
- [x] 4.3 完成 M2 可复现验收入口、能力边界说明与 Change Review
