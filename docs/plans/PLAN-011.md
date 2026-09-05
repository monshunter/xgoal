# PLAN-011：改为当前工作目录执行并移除 Git worktree

## 目标

xgoal 在用户当前 Git 主工作目录中完成实现、验证、审查和交付，每个项目只有一个实例与一个执行者，不创建或删除任何 Git worktree；结果直接保留在当前工作目录，并继续满足当前 Tree、Evidence 和 Report 的完成判断。

## 范围

更新现有产品/技术 SPEC、Git/环境/验证设计及相关架构 owner；替换内部工作区实现、Patch/Tree 捕获、验证与 Promotion 链路、配置和历史状态兼容；保护用户 HEAD、分支、index 和已有修改。依赖 PLAN-009 项目单实例；完成后进入 PLAN-010 的持久规划与完整恢复验收。不自动 stash、reset、切换用户分支、清理历史 worktree 或修改远端。

## Phase 1：统一当前工作目录执行合同

关联：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)、[Git 环境验证设计](../architecture/DESIGN-001-m2-git-environment-validation.md)

- [x] 1.1 定义并审查原地执行、用户修改保护、最终结果、配置和历史状态兼容的行为合同
- [x] 1.2 设计并审查无 worktree 的快照、验证、审查、晋升及失败恢复链路

## Phase 2：实现原地执行与快照

- [ ] 2.1 实现主工作目录独占会话、干净基线准入和用户 HEAD/index 完整性核对
- [ ] 2.2 用独立临时 index 捕获当前目录 Tree 和完整 Patch，排除 Git 元数据及 xgoal 状态
- [ ] 2.3 替换工作区状态与制品布局，保留旧历史可读并拒绝不安全的旧运行恢复

## Phase 3：贯通验证、审查与结果交付

- [ ] 3.1 在当前目录执行 Validator 与独立 Reviewer，并核对执行前后的受验收 Tree
- [ ] 3.2 从已验证 Tree 创建私有集成 Commit/Ref，保留用户 HEAD/index 和当前目录结果
- [ ] 3.3 实现失败、中断和外部编辑的现场保留、等待及明确恢复去向
- [ ] 3.4 对齐配置、初始化、CLI 状态、README 和操作文档，移除实际 worktree 创建入口

## Phase 4：验证与收口

- [ ] 4.1 覆盖多 Work 顺序执行、完整文件差异、元数据保护、验证变更和历史兼容的定向测试
- [ ] 4.2 以真实 CLI/daemon 场景证明工作目录结果、Git worktree 列表不变及最终 Evidence/Report 绑定
- [ ] 4.3 完成相关回归、独立 Change Review、制品对账与原子提交
