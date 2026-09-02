---
name: autogo-change-intake
description: "为已判定为 Standard 的授权变更建立或调整 Objective、正式 Plan 和执行边界；在首次写入、新独立交付、目标或范围实质变化，或恢复后需重新路由时使用；纯回答、Review-only、Diagnose-only 和未授权调查不触发，Fast 也不调用本 Skill。"
---
# autogo-change-intake
## 目标
为根合同已经判定为 Standard 的授权变更建立可恢复入口：对账一个 Objective，立即创建至少一份正式 Plan，并在第一条 Phase Item 执行前路由 Plan Review。
## 输入与发现
- 用户目标、约束、非目标和成功标准
- 根及所有受影响组件的项目指令规则链
- `PROGRESS.md` 中的 Objective 区块、Objective 三态和内嵌 Plans Checklist
- 现有代码、测试、运行状态及相关正式制品

## 输出与持久制品
- 已创建或对账的 Objective、授权范围、非目标、成功标准和系统不变量
- `PROGRESS.md` 中包含名称和路径的 Plans Checklist
- 至少一份使用 Phase Checklist 的正式 `PLAN-<编号>.md`
- 必要的风险控制方向、局部 Skill 路径、验证范围和 Human Gate
- 当前任务需要的事实 owner 与局部能力入口；Review 通过前不执行第一条 Phase Item

## 副作用与 Human Gate
可创建或更新必要文档与进度；不得在 Intake 阶段执行生产、删除、权限或不可逆副作用。

## 执行步骤
1. 确认请求具有写入授权且已进入 Standard；否则返回根合同，不隐式升级只读请求或 Fast
2. 读取项目指令、Git、工作树、`PROGRESS.md`、代码、测试、环境和相关正式制品；确认或建立独立语义分支
3. 用当前事实明确 Objective、授权范围、非目标、成功标准和必要系统不变量；无法推断且会改变产品语义或重大副作用时准备 Human Gate brief
4. 同一结果存在未完成 Objective 时对账复用；只有独立交付结果才创建新的 Objective
5. 从当前代码、测试和正式制品确认任务需要更新的事实 owner；不为形式完整机械创建 Spec、Design 或其他文档
6. 立即创建至少一份正式 Plan，并在 Progress 中写入名称和路径链接；不预建未来占位 Plan
7. 调用 autogo-plan-write，把当前 Plan 写成目标、范围和有序 Phase Checklist；未知实现不得预填为事实
8. 从实际风险信号选择 Investigation、Spec、Design、TDD、E2E、Deploy 等必要局部能力，不复制其触发细节
9. 将 Objective 汇总为 `正在处理`，并在第一条 Phase Item 前调用 autogo-plan-review
10. Plan Review `FAIL` 时修订对应 owner 并重审；`PASS` 或可继续的 `PASS_WITH_NOTES` 后才返回执行 Skill
11. 目标、范围或成功标准变化时重新 Intake；只影响 Phase、顺序或验收覆盖时更新 Plan 并重新 Plan Review

## 验证与完成
- 请求确有写入授权且 Standard 路由成立
- 成功标准可以由当前测试或运行结果验证
- 每个 Plan 只内嵌于一个 Objective，Objective 的状态严格按其 Plans Checklist 汇总
- 当前 Objective 至少有一份正式 Plan，且 Plan Review 已在执行前通过
- 任务实际需要的事实 owner 已确认，没有为流程完整创建无价值制品
- 所有受影响组件规则已加载
- 没有为只读请求或 Fast 创建 Objective、Plan 或 Review

## 失败、重试与幂等
事实不足时先路由 autogo-investigate；产品语义有多种合理解释时先完成安全准备，再提交 decision-ready Human Gate brief。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
