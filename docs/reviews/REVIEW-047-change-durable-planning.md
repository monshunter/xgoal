# REVIEW-047：持久规划、执行归属与恢复变更审查

2026-09-05；OBJ-003 / PLAN-010；基线 `3fd7066`，分支 `fix/project-daemon-isolation`。最终审查范围为 67 个代码/构建文件，按路径排序后以 `path + NUL + bytes + NUL` 合并的 SHA-256 为 `aaebf3ef21814d8d159288c3bc2b9f7aea907f78404d82b5bfa6a2bf05dec9a4`。设计 owner 为 DESIGN-004 §8–9，验收 owner 为 PROJECT-DAEMON-CURRENT-DIRECTORY-ACCEPTANCE。

## 结论

代码 Review **PASS**。独立 Reviewer `planning_final_review` 在新上下文中只读审查，使用 `/tmp` Go overlay 复现问题，并在修复后复验正式测试。没有剩余 correctness finding。真实 Provider 完整链路与完整组合门禁已通过，PLAN-010 的最终对账成立。

## 已修复发现

1. 持久 pause 已提交、进程清理 observation 尚未保存时 daemon 崩溃，原恢复误计为无进展失败。恢复与正常失败收尾共用 generation 的持久 pause 事件判断；用户暂停不消耗中断预算。
2. 完整旧 READY 因配置缺失打开规划 Gate 后，配置恢复虽激活 Goal 却留下 Gate，使 Work 永久 PENDING。证明初始图后在同一事务撤销对应规划图 Gate，保留普通授权 Gate。
3. 旧创建在冻结 Revision 后中断，随后失败的人工 replan 可以留下结构/哈希有效但不匹配 Contract 的 Draft。恢复现在要求旧初始 Compiler 来源、冻结 Contract 语义/哈希、配置身份、完整 DAG 与 Required Criteria 覆盖；无法证明不激活。负例使用真实旧冻结事件与失败 ReplanProposed 来源，专门保护该边界。
4. 统一启动 wrapper 后，工具快照外层 500ms 超时与内部 runVersion 5s 限制冲突。race 对照中同一立即退出命令在 500ms 下失败、5s 下约 1.025s 正常完成。移除重复外层计时，复用已有有界调用；父 context 取消继续有效。

主 Agent 收尾还修复并测试：服务启动失败不丢失无法确认的 handle/error；Adapter 不在未知清理后无限等待；Validator 不把未确认执行发布成不可变 Receipt；恢复对账已撤 Lease 的孤立 Work，并在未知进程后续确认终止时更新原 Gate。未决 Promotion 保留读回所需 Lease。

## 独立验证

- SQLite、Supervisor、Recovery 普通包测试全部 PASS。
- Supervisor、Recovery 完整 race PASS；规划、legacy 与 process 定向 race PASS（42.516s）。
- 修正后的 failed-replan 正式用例与 `TestM2ArtifactsPersistAndFailClosedOnDiskTamperingAcrossRestart` 均在 race 下 PASS；后者独立复验 30.35s，主 Agent 复验 29.764s。
- 当前 3 个生产修复均有原失败复现与正式回归；`/tmp/xgoal-planning-review.SueMeJ`、`/tmp/xgoal-race-probe-review.ayb21j` 仅作临时实验，不进入提交。

本 Reviewer 只新增 macOS 代码和进程验证；双项目完整 Goal、真实 CLI 断连/崩溃、Linux 原生进程及外部 Provider 的结果见操作记录。代码采用既有 SQLite Effect/事务/CAS 和串行槽，没有新增全局 daemon、独立任务数据库或另一个恢复调度器。wrapper 是一个 invocation 的启动屏障与回收凭证，目的和失败边界明确。

## 下一步

原始 make、失败修复及补跑结果已在操作记录汇总，允许关闭 Plan 并创建本地提交。额外跨 Provider 在线补测仍准确标为未执行。

## 组合门禁后的测试修正

全量随机回归暴露 app E2E 的 45 秒总期限不足及失败路径未清理 daemon。总期限调整为 120 秒，业务超时不变；独立 Reviewer 随后发现 cleanup 可能二次消费启动结果，现改为结果写入后关闭独立 finished channel。cleanup 可重复、有界 join，正常关闭同样有界等待；修复再次获独立只读 PASS。app 完整 race 已通过 91.713s。orchestrator 的两 Work/Review/终验测试原 60 秒期限在 race 下不足，测试总预算调整为 3 分钟，完整 race 已通过 460.851s；随机回归中同类旧期限失败已完成受影响测试各 20 次复验。生产行为未因这些测试期限修正改变。


## 真实规划契约与最终测试审查

新增 opt-in `TestRealCodexCLIBackgroundGoalToFinalReport` 使用固定临时 Git 仓库、真实 Codex Planner/Implementer 和独立 Codex Reviewer 会话，Validator 只读逐字节检查。独立 Reviewer 发现并复核了启动响应解码前 cleanup 注册、Planner effect/session 与独立 approved Review 的直接 DB 断言两项补齐，最终测试结构 PASS。失败保留临时目录供归因，成功清理。

真实调用先后证明配置环境不完整、fixture Validator 额外日志与目标矛盾、Contract 必填语义缺失，以及 AC-SCOPE 无 Validator / Scope 未锚定。前两项修复测试配置；后两项将编译器既有条件统一显式传给 Planner，不放宽 Compiler/协议/完成标准，也不在验收原始目标中代写 Proposal。失败及逐轮结果由操作记录拥有。

PLAN-010 和 AC-BG-006 要求真实 Provider 完整链路，没有将本轮额外 Codex↔Claude 双向在线 smoke 作为前置门禁；该额外检查因自动审批拒绝而未执行。历史 AC-FR-050 仅保留当时证据，不表示本轮重验。该范围判断已经独立 Reviewer 核对。


最新多行 Planner 提示经独立 Reviewer 再次只读 PASS：明确实际 Validator 覆盖、条件审批、Required Work、根锚定 Scope、依赖与冲突排序，均来自现有编译和 Scope 规则。无法证明时仍返回 ambiguity。真实执行已成功发布冻结 Contract/Work Graph；完整链路与剩余随机门禁已通过，结果见操作记录。


版本探测的 producer/consumer 单行契约缺陷已由独立 Reviewer 复核 PASS，并独立执行正式 warning/stdout/stderr/malformed optional/required 测试通过 2.862s。非零退出、回收错误、64 KiB 总上限与 5s 超时保持；修复不改环境权限。真实完整 Goal 已 PASS266.394s，其后这项输出解析修复由针对原失败模式的本地回归覆盖，不重复调用收费服务。最后随机与环境 race 均已通过。


## 最终 Evidence 对账

完整链路与私有 ref 漂移拒绝各 20 次，共 40 次全部 PASS；相关规划测试亦各 20 次 PASS，包结果1874.369s。全仓普通、随机和 race 的组合覆盖以及 fmt/vet/CLI/合同/失败矩阵/平台构建均通过，原始 make 失败不被抹去。最终 environment race 23.087s、Planner 定向 race 7.996s 通过；真实 Standard Goal 和 Linux 原生进程证据分别记录。源文件摘要与本 Review 首段一致，未决 correctness finding 为零，允许 PLAN-010 关闭并提交。
