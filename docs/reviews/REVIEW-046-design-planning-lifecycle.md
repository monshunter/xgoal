# REVIEW-046：后台规划配置与进程归属细化审查

## 审查对象与结论

`PASS`。审查对象为 [DESIGN-004](../architecture/DESIGN-004-m5-control-daemon.md) §8.3 与 §9，相对 `3fd7066` 的两处细化；该文档 SHA-256 为 `8e8cb44cbd4003eb6c9983fa14407b043621f087f3963210a623528d1bebfd95`。关联 [PLAN-010](../plans/PLAN-010.md)，没有改变已审 Plan 的范围、顺序或验收目标。

主任务提出细化后，独立执行任务 `project_impl` 只读审查这两项设计，并返回 PASS；本记录保存其结论，不把设计审查作为实现验收。

## 审查判断

daemon 维持启动时经验证的配置快照；规划开始与发布同时核对请求绑定 hash、启动 hash 和当前文件 hash。配置漂移后以重启加显式 `goal plan` 创建新 generation，使配置 owner 与执行 owner 一致，也避免新增热加载状态机。

单次 invocation 的 wrapper 保持可核对的进程组 leader，直到目标及同组子孙结束或完成回收。它填补目标父进程提前退出后失去组身份的缺口，复用现有 supervisor；没有引入第二个调度器或全局 daemon。

## 实现验收要求

仍需用当前代码证明：TERM 期间 leader 保留、父进程先退出且子孙持有输出 pipe 时的回收、未释放启动 pipe 的 EOF、重启后的精确身份核对、未知 PID 不被误杀，以及配置漂移后的显式重试。上述证据归 PLAN-010 Change Review 与操作记录，不由本 Review 预先判定通过。
