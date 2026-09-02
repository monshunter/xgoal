---
name: autogo-change-review
description: "以新鲜上下文审查 Standard 变更的正确性、兼容性、测试和范围；在实现或修复通过定向验证后、提交或部署前使用；standalone Review-only 也可使用，但项目状态完全只读。"
---
# autogo-change-review
## 目标
以新鲜上下文只读审查整个变更与 Spec/Design/Plan 的一致性、完备性、正确性和风险。
## 输入与发现
- Standard 从 Progress 读取当前 Objective 和 Plan；当前 Phase Item 和相关 Spec 从正式制品读取
- standalone Review-only 只读取用户指定的对象、范围和当前 Evidence
- 当前范围的完整 Diff、相关规范、设计、计划、测试和运行 Evidence
- 所有受影响组件项目指令

## 输出与持久制品
- PASS / PASS_WITH_NOTES / FAIL / BLOCKED
- 按严重度排序的 correctness、安全、兼容、测试和范围发现
- 缺失 Evidence 和下一修复路由
- Standard 中已写入 Review 制品的结论和下一路由；standalone Review-only 只向用户报告

## 副作用与 Human Gate
被审对象始终只读。Standard 可写 Review 文档，但不写 Progress 内部细节；用户要求“只审查”“不要修改”等 standalone Review-only 时，项目状态也完全只读，不创建或更新任何制品。

## 执行步骤
1. Standard 从 Progress 确认当前上层 Plan，再从已完成的 Phase Items 和相关 Spec 锁定范围；standalone Review-only 直接按用户范围审查
2. 先阅读目标和验收，再审 Diff
3. 检查所有 producer/consumer、接口、数据和状态链路
4. 检查异常、并发、安全、资源和兼容性
5. 检查任务实际需要的 Spec、Design、迁移、Scenario 或部署记录与当前 Diff 是否一致，不要求固定制品矩阵
6. 检查测试是否真正覆盖当前目标、已完成 Phase Items 与回归风险；需要真实 UI 或跨组件链路时检查实际交互能力、环境和结果 Evidence
7. 检查无关改动、过度设计、Scenario/Operation 混淆和文档漂移
8. 只记录与当前变更正确性、风险和验收直接相关的发现；不检查 Harness 安装、Skill 固定措辞或无关流程制品。失败返回对应 owner 进入 Reconcile，修复和验证后重新 Review，不因普通失败自动回滚
9. Standard 将 Verdict、缺失 Evidence 和下一路由写入 Review；standalone Review-only 只报告。Plan 只更新任务是否完成，PASS 不自动勾选 Progress 中的上层 Plan

## 验证与完成
- 每条发现有位置、证据、影响和建议
- 结论不依赖作者自述
- Review 覆盖实际受影响的正式制品、已完成 Phase Items 和验收项
- 真实 UI E2E 没有用低层测试或静态截图替代，Evidence 同时覆盖可见结果、console/page/network 和 API/后台边界
- 跨组件、公共契约、数据、安全或生产风险优先使用新上下文或独立 Reviewer

## 失败、重试与幂等
无法验证时 `BLOCKED` 并进入 Investigation；缺少授权、凭证、权限或外部条件时进入 `Waiting`。真实状态受损或继续会扩大影响时才止损或恢复。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
