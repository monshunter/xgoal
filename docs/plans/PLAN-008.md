# PLAN-008：迁移并验收 Cobra CLI

## 目标

使用 Cobra 建立完整、可发现、可扩展的 xgoal 命令树，替换当前手写命令分派和参数扫描模式，同时保持既有命令语义、API 请求、输出数据、信号处理与退出码兼容。

## 范围

包括 root、顶层命令和 `config`、`benchmark`、`daemon`、`goal`、`work` 子命令的 Cobra 化；标准帮助、参数错误、版本与 shell completion 体验；可测试的依赖注入和命令级校验；README 与当前 CLI 合同同步。Daemon/API/domain 行为、持久化语义、配置格式、远端发布和生产操作不在范围。

## Phase 1：冻结 CLI 行为与迁移设计

关联：[Cobra CLI 行为规范](../specs/SPEC-001-cobra-cli.md)、[Cobra CLI 迁移设计](../architecture/DESIGN-006-cobra-cli.md)

- [x] 1.1 定义并审查命令树、参数错误、帮助、completion 与兼容性验收合同
- [x] 1.2 设计并审查 Cobra 组件边界、执行模型、退出码映射与迁移回滚路径

## Phase 2：迁移完整命令树

- [x] 2.1 迁移 root、本地命令与服务类子命令，并提供一致的帮助和版本体验
- [x] 2.2 迁移 API 查询、控制与嵌套资源命令，并保持请求和退出语义兼容
- [x] 2.3 补齐 shell completion、README 使用入口与兼容性说明

## Phase 3：回归验证与收口

- [x] 3.1 以命令级测试覆盖帮助、参数校验、请求构造、输出与退出码
- [x] 3.2 通过项目全量门禁、CLI smoke、跨平台构建与 Change Review
- [x] 3.3 对账 Objective、Plan、正式制品与 Git，并创建原子提交
