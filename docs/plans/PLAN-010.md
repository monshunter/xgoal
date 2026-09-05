# PLAN-010：持久后台规划与跨项目恢复验收

## 目标

让规划、执行、审查和恢复统一由项目 daemon 拥有；已接受的目标不依赖客户端连接，崩溃后的未完成操作具有确定的恢复去向，并验收整个项目隔离优化目标。

## 范围

持久规划意图、请求接受幂等性、规划结果原子发布、旧 IN_PROGRESS 与半完成 Goal 恢复、Provider 进程归属和有界串行执行；完整 CLI→daemon→当前工作目录→Git→Validator→Report 验收。依赖 PLAN-009 的项目身份与生命周期和 PLAN-011 的无 Git worktree 执行链路。不新增全局调度中心、计费统计、容器 Provider、系统服务安装或远端发布。

## Phase 1：持久规划与幂等接受

关联：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)、[控制面设计](../architecture/DESIGN-004-m5-control-daemon.md)

- [x] 1.1 实现 Goal、规划意图和接受响应的事务性登记及重复请求回放
- [x] 1.2 将 Planner 接入 daemon 串行执行并原子发布冻结 Goal Contract 与 Work Graph
- [x] 1.3 恢复中断规划、旧 IN_PROGRESS 请求和半完成 Goal，保留可追溯失败事实

## Phase 2：统一执行与资源回收

- [x] 2.1 统一 Planner、Implementer、Reviewer 的进程归属、取消、崩溃恢复和防重叠执行
- [x] 2.2 保持 wait/watch 只观察后台目标，并验证断连、暂停、取消、停止和重启语义

## Phase 3：闭环验收与交付

- [x] 3.1 验收双项目完整 Goal、主工作目录单实例、linked worktree 入口拒绝、错误身份拒绝与状态迁移
- [x] 3.2 验收客户端退出、daemon 中断、规划结果提交窗口、进程回收与最终 Evidence/Report
- [x] 3.3 通过全量门禁、平台构建和独立 Change Review，并完成逐项目标审计
- [x] 3.4 对账设计、规范、操作 Evidence、Progress 与 Git 并创建原子提交
