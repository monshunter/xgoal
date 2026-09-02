# PLAN-006：实现 M5 Reconcile、Gate、Budget 与 Daemon

## 目标

在 M1–M4 的持久状态、验证、Agent 与 Review 基础上，建立确定性的失败归因与防机械重试、有限 Human Gate 授权、可解释预算控制，以及由单写 Daemon 承载的 Unix Socket API、状态流和进程恢复。

## 范围

包括 Failure Class/Fingerprint、Material Progress、Reconcile/Replan 决策；Policy 与有限授权消费；Budget 预检和实际 Usage 记账；SQLite 迁移与全局事件游标；Unix Socket HTTP/JSON API、NDJSON 状态流、Daemon 生命周期和 Worker 恢复；对应 CLI 与故障注入门禁。最终报告、Benchmark、Threat Model 与发布清单属于 M6。

## Phase 1：冻结控制面合同

关联：[技术 SPEC 18–20、23、27 与 M5](../../xgoal-technical-spec-v0.1.md#18-reconcile-与防机械重试)

- [x] 1.1 冻结 Failure Fingerprint、Material Progress、Reconcile/Replan、Policy/Gate、Budget 的确定性合同
- [x] 1.2 冻结 SQLite、Unix Socket API、NDJSON 游标、Daemon 单写和进程恢复设计并通过 Design Review

## Phase 2：实现 Reconcile、Policy/Gate 与 Budget

- [x] 2.1 以测试驱动实现稳定 Failure Class、错误归一化、Fingerprint、Material Progress 和禁止同策略机械重试的决策表
- [x] 2.2 实现角色 Policy、Gate 列表/决定/撤销及按 Goal/Work/Attempt/动作/Scope/过期/次数原子消费
- [x] 2.3 实现 Goal/Work/Agent/时间/并发/token/费用/Validator/磁盘预算预检与记账，未知 Usage 保持 unknown
- [x] 2.4 增加 SQLite 持久化、事件、重启读回、CAS/并发与篡改拒绝测试

## Phase 3：实现 Local API、Daemon 与 CLI

- [x] 3.1 实现 Unix Socket HTTP/JSON API、写请求幂等、当前 UID/`0600`、结构化错误与全部 v0.1 Endpoint
- [x] 3.2 实现按 Event ID 续传的 NDJSON 状态流、事实型状态摘要和日志/报告安全读回
- [x] 3.3 实现 Daemon 单写锁、优雅停止、启动恢复、Worker 进程组重绑定或终止与迟到结果隔离
- [x] 3.4 实现 init/doctor/run/status/logs/gates/approve/pause/resume/cancel/report/clean 及高级资源 CLI 和退出码映射

## Phase 4：故障注入验收与收口

- [x] 4.1 通过同指纹无进展、Gate 越权/过期/超次、Budget soft/hard/unknown 安全矩阵
- [x] 4.2 通过 Socket 权限、单写竞争、幂等写、NDJSON 断线续传与 Daemon/Worker 崩溃恢复矩阵
- [x] 4.3 通过 M5 全量回归、跨平台构建、运行验收与 Change Review
