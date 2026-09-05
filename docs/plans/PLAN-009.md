# PLAN-009：统一项目身份与 Daemon 生命周期

## 目标

将已复现的隔离与生命周期缺口落实为一致的长期设计，使同一 Git 项目只有一个有效状态 owner，独立项目可同时运行，CLI 能可靠定位、启动、观察与停止对应 daemon。

## 范围

更新现有产品/技术 SPEC 与 DESIGN-004；统一当前主工作目录的 Git 项目身份、状态绑定与 socket 发现，明确拒绝 linked worktree 入口；单写锁覆盖数据库迁移、恢复、执行与关闭；增加后台 start/stop/status、离线 doctor 和必要兼容迁移。保留每项目 daemon、SQLite、L0 本地进程架构；内部 Git worktree 的移除由 PLAN-011 承接，不引入全局状态服务、容器隔离、系统服务安装、远端发布或生产操作。

## Phase 1：冻结隔离与后台执行合同

关联：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)、[控制面设计](../architecture/DESIGN-004-m5-control-daemon.md)

- [x] 1.1 定义并审查项目单实例、当前主工作目录、生命周期、持久规划、迁移和失败恢复的可验收合同
- [x] 1.2 完成并审查统一控制面设计及与既有规范的替代关系

## Phase 2：实现项目身份与唯一所有权

- [x] 2.1 统一子目录、路径别名、独立 clone、状态覆盖与短 socket 路径的解析和身份握手，并拒绝 linked worktree 入口
- [x] 2.2 实现状态库身份绑定、旧状态兼容检查和跨状态目录的仓库排他所有权
- [x] 2.3 使单写所有权覆盖迁移、启动失败、执行等待和数据库关闭的完整生命周期

## Phase 3：完善后台操作入口

- [x] 3.1 实现具备身份验证、真实 readiness 和安全停止语义的 daemon start/stop/status
- [x] 3.2 统一 CLI 参数与环境覆盖，并支持无 daemon 的被动 doctor
- [x] 3.3 对齐 README、操作手册和资源隔离限制

## Phase 4：验证与收口

- [x] 4.1 通过项目解析、状态迁移、双进程互斥和启动关闭故障的定向验证
- [x] 4.2 通过跨项目真实 CLI/daemon 场景、相关回归和独立 Change Review
- [x] 4.3 对账正式制品、Progress 与 Git 并创建原子提交
