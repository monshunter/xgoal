# REVIEW-039：项目隔离与持久后台执行 Spec Review

## 审查对象

- Objective / Plan：`OBJ-003` / `PLAN-009` Item 1.1。
- 产品 SPEC：FR-011、FR-100–108、AC-ISO-001–006、AC-BG-001–006，及其既有目标录入、生命周期、诊断和 CLI 合同。
- 技术 SPEC：第 5、9.1、10.1、22.4、23 节，及其既有状态、Effect 和退出码约束。
- 代码事实基线：`24960390d71237d6266e2f995b4e3df0b6719a78`，当前文档修订尚未实现。
- 产品 SPEC SHA-256：`a737df6269cd154f18aceb958653438a86af25ec87f20a433e6672975e759177`。
- 技术 SPEC SHA-256：`70b73e10df66743904fcd98bfd0f18d51775486e808ee967cdccb5db5942d048`。

## Verdict

`PASS`

当前修订覆盖用户批准的设计、实现与验收范围，验收 ID 稳定，行为可独立验证。此结论批准规范内容，不表示实现或任何新增 AC 已通过。

## 发现与修订对账

审查过程中发现并已由规范 owner 修正两处一致性问题：

1. FR-011 原先要求 Planner 先取得已冻结 Revision 再生成 Work Graph，与初始 Proposal 的 Revision/Graph 原子发布语义不一致。当前明确初始 Planner 在同一 Proposal 中提议 Contract 与 Graph，经 Kernel 校验后原子发布；已冻结目标的 replan 单独保留原合同。
2. 技术 SPEC 9.1 原先把所有不可自动恢复问题都归为 Goal `WAITING`，但 DRAFT 状态没有相应恢复路径。当前明确未冻结 Goal 保持 `DRAFT`，以持久规划控制和 Effect 投影 QUEUED/RUNNING/PAUSED/WAITING；resume 只恢复规划，规划等待时 wait 返回 3。已冻结目标才使用既有 WAITING→RUNNING 路径，避免无 Revision 的 RUNNING。

当前没有未解决的阻塞发现。

## 审查依据

- 同一 Git Common Directory 是唯一项目边界，Project ID、执行根、状态库、socket 与 daemon 关联明确；linked worktree、路径别名和独立 clone 均有可验收语义。显式覆盖不能制造另一有效 owner。
- 所有权覆盖打开数据库到关闭全过程；错接项目、协议不匹配、多个历史库、启动失败和旧 instance 停止均有拒绝或收尾要求，覆盖原实现的锁时序及身份缺口。
- 接受事务保存 Goal、Planner 意图和可回放响应；客户端退出只停止观察。规划结果原子发布，可靠结果读回复用，暂停取消、无效输出、配置漂移与重试均有持久去向。该模型与现有 Effect、Lease fencing、Evidence 完成判定互补。
- 旧状态采用要求原位验证与备份，冲突不自动合并、移动或删除。未知执行者不被猜测性回收，未决请求、Attempt/Lease/Work 必须得到确定结果或可操作等待。
- CLI 入口统一定位优先级，start/stop/status 与 serve 的职责清楚；被动 doctor、help/version/completion 保持无 daemon、无状态写入，主动 Probe 显式进入项目串行槽。
- AC-ISO 覆盖独立项目、共同仓库身份、兼容迁移、锁/socket、后台操作和只读入口；AC-BG 覆盖接受及发布窗口、规划控制、各角色进程、历史恢复和最终链路。真实 Provider smoke、确定性 fixture、平台运行 Evidence 被区分，没有把交叉编译或测试替身等同全部运行验收。
- 规范保留 L0 的共享宿主机资源事实，排除全局调度、强多租户隔离和第二个全局状态服务，符合当前项目规模下的最小充分方案。

## 下一路由

允许进入 `PLAN-009` Item 1.2 的 `DESIGN-004` Design Review。通过设计审查后实施定向测试和实现；新增 AC 必须由当前运行 Evidence 支撑后才能勾选。若行为合同实质变化，应更新此规范 owner 并重新审查。

本次审查只读取文档及相关代码事实，未执行功能测试，未更新 Plan 或 Progress。
