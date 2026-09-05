# REVIEW-042：当前工作目录与持久后台执行 Spec Review

## 审查对象

- Objective：`OBJ-003`；Plans：`PLAN-009` → `PLAN-011` → `PLAN-010`；Plan 门禁见 [REVIEW-041](REVIEW-041-plans-current-directory.md)。
- 上游要求：设计落地、实现与闭环验收；xgoal 在当前 Git 工作目录执行，不创建任何 Git worktree，一个项目只有一个实例。
- 代码基线：`24960390d71237d6266e2f995b4e3df0b6719a78`，分支 `fix/project-daemon-isolation`。本次审查目标合同，不以当前未完成代码证明实现符合合同。

| 被审制品 | SHA-256 |
| --- | --- |
| [产品 SPEC](../../xgoal-product-spec-v0.1.md) | `5fddc7f50f861560e37d25ce24b015240879e7a71fb8b94d02d56ce7577ef5c0` |
| [技术 SPEC](../../xgoal-technical-spec-v0.1.md) | `d37defbf8aa324a8d4a9ae7ef7f5bc8865c9b70dcb79a4a4c56fa3c96ee1a1c6` |

## Verdict

`PASS`

当前合同覆盖用户完整目标，一致性、失败边界和可验性足以进入设计审查。本结论替代 [REVIEW-039](REVIEW-039-spec-project-background.md) 对当前修订版的结论，不代表旧验收记录已证明原地实现通过。

## 审查结论与 Evidence

- 产品 FR-030、FR-061、FR-100–110 与技术第 7、9、14、22 节共同定义当前主目录入口、linked 入口拒绝、仓库单实例、项目身份绑定和每项目 daemon；不同 clone 可以独立运行，未引入用户级全局服务或全局资源仲裁。
- 初始准入要求用户 index Tree、HEAD Tree、原始字节工作目录 Tree 一致；连续 Goal 可以接管完全匹配已验收记录的目录。其他预存 dirty 和未知元数据变化保留并拒绝接管。私有 index/原始 blob 捕获与用户 HEAD、分支、真实 index 分离，不借 Git filters、hooks、reset 或 checkout 达成一致。
- FR-061 与技术 14.5–14.6 将物理工作区重放改为对象级重建，要求当前目录与 candidate Tree 相同，再现地验证和 Review，最后通过私有 `refs/xgoal/goals/<goal-id>/integration` 发布审计 Commit。结果留在当前目录由用户审阅提交，不以隐藏的另一份代码替代交付。
- FR-105–107 保留原子接受、原子规划发布、进程归属、项目串行槽、客户端仅观察及旧幂等请求恢复。新增原地范围没有挤掉原有后台恢复缺口；DRAFT 的 planning_state 与冻结后的 Goal 状态有清楚分界。
- FR-110 与技术 14.7 明确失败现场保留；只有原 Goal/Work、已登记观察、当前目录匹配且 HEAD/index 未变时，显式 retry 可继续。未知漂移和 Scope 越界不得自动归属或覆盖；不新增 restore Effect。FR-109 保留旧完成记录的版本/Hash，旧未完成 worktree 目标迁移等待，clean 不删除当前或历史代码目录。
- AC-ISO、AC-BG、AC-CWD 使用稳定 ID，覆盖双项目、入口/身份、并发启动停止、客户端断连、各规划持久窗口、迟到结果、完整文件差异、元数据保护、失败保留、历史兼容以及最终目录/Tree/Evidence/Report 绑定。fixture 与真实 Provider、平台原生运行与交叉编译分别标注，验证强度与主张对应。

## 本轮修订核对

审查期间反馈给作者的歧义已在当前合同消除：

1. AC-CWD-001 现在区分 Agent 的 root CWD 与 Validator/bootstrap 在 root 内受信相对 CWD，保留现有可配置命令入口且不允许额外执行代码目录。
2. Promotion 串行范围明确为项目内；技术 11.4 将未来并行标为当前不启用的候选，不再同时承诺当前共享目录并行执行。
3. 产品 P-007 明确同一验证阶段受管服务可为探针存活，禁止并发修改源码并在阶段结束前回收；不把 Provider 串行误解释为禁止服务探针。
4. 技术 14.1 明确 raw Tree 的干净准入，不能用可能依赖过滤器或 index 刷新的 git status 结论代替。

这些是已有授权范围内的合同对齐，不引入新任务边界或用户审批。

## 下一路由

允许以当前 hash 进入 Design Review。实现验收必须特别覆盖：相对 CWD 仍受 root containment 约束、filters/CRLF 导致 raw Tree 不一致时明确拒绝、旧配置迁移诊断不阻止已有报告读取、失败 retry 不能接管第三方编辑。相关能力已经包含在 AC-CWD/AC-BG，不另建平行验收台账。

本次独立只读审查 Spec、相关设计与既有代码约束；仅写本 Review 与索引，未修改 Spec、Plan、Progress、实现或验收勾选，未运行功能测试。
