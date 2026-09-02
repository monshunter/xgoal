---
name: autogo-change-implement
description: "在批准范围内实施代码、配置、迁移、测试和必要文档；当 Fast 准入成立、Standard Plan Review 已通过且有可领取 Phase Item，或 Change Review FAIL 需要 Reconcile 时使用。"
---
# autogo-change-implement
## 目标
在已批准范围内实施代码、配置、Migration 与必要文档变更。Fast 使用明确的轻量目标；Standard 只执行已通过 Plan Review 的当前 Phase Item。
## 输入与发现
- 当前路由：Fast 轻量目标，或 Standard Objective、正式 Plan 和已通过的 Plan Review
- Standard 的未完成 Phase Item、相关 Spec/Design 与 Change Review 发现
- 当前代码、相关项目指令、测试入口和环境状态

## 输出与持久制品
- 最小充分实现与相关测试
- 实际变更说明、偏离 Plan 的原因和新发现
- 可复核 Diff 与局部验证 Evidence
- Standard 中仅在任务实际完成后更新的 Spec 验收项与 Phase Item；Fast 不创建或更新 Progress/Plan
- Standard 必要时写入 Spec 或其他事实 owner；Journal 只在跨会话恢复或用户明确要求时调用

## 副作用与 Human Gate
可修改项目文件和执行当前风险允许的命令；生产、删除、权限和不可逆动作受 Human Gate。

## 执行步骤
1. 确认当前路由；Fast 读取轻量目标和准入 Evidence，Standard 从 Progress、正式 Plan 与 Plan Review 锁定一个未完成 Phase Item
2. 确认工作区和 Diff，避免覆盖无关修改
3. Standard 一次只领取一个未完成 Phase Item；Fast 只实施已确认的最小范围；优先按 TDD 或先建立可复现验证
4. 分小步修改，每步保持可运行或可恢复
5. 仅修改当前批准范围；Fast 条件失效时立即升级 Standard，Standard 的目标或授权范围变化时重新 Intake，Plan 实质变化时更新并重新 Plan Review
6. 同步更新契约、Migration、consumer、测试和文档
7. 运行与真实故障模式相称的验证；Standard 任务实际完成后才勾选对应 Phase Item，并按 Spec 规则更新相关 AC
8. 实现过程不写入 Plan；必要记录放入对应事实 owner，Progress 只在上层 Plan 开始或完成时更新
9. Fast 完成后回根合同对账；Standard 当前交付增量完成后进入 autogo-change-review，Review `FAIL` 时修复并重审

## 验证与完成
- Diff 与目标一一对应且无无关改动
- 受影响测试通过或失败被准确记录
- Standard 的已勾选 Phase Items 都与当前实际结果一致，未完成项保持未勾选
- Fast 未创建 Objective、Plan 或 Review；Standard 的 Progress 与正式制品事实一致
- 没有把未完成工作写成完成
- 未越过生产、破坏性、权限、Secret、计费或不可逆副作用的 Human Gate

## 失败、重试与幂等
普通失败进入 Reconcile，不自动回滚；真实状态受损或继续会扩大影响时才止损或恢复。同一失败只有新操作能产生新 Evidence 时才重试，否则转 autogo-investigate。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
