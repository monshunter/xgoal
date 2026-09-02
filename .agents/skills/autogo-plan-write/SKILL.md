---
name: autogo-plan-write
description: "为 Standard Objective 创建或更新必需的单文件 Phase Checklist；在 Intake、新 Plan 或实质调整时使用。"
---
# autogo-plan-write
## 目标
为 Progress 中的当前上层 Plan 创建或更新一份简洁的正式 Plan，把目标拆成按顺序执行的 Phase 和原子 Item，不把 Plan 写成实现说明或第二套控制系统。
## 输入与发现
- Progress 中当前 Objective 和 Plan
- 已批准目标、相关 Spec/Design 和当前代码事实
- 范围、非目标和必要顺序

## 输出与持久制品
- 一份同时承担 Plan 与 Checklist 的 `PLAN-<编号>.md`
- 简短的目标与范围
- 按 `Phase N` 组织的任务清单
- 使用 `N.M` 编号和 `[ ]` / `[x]` 状态的原子 Item
- 必要的 Spec / AC 链接和已更新的索引
- 进入 autogo-plan-review 的明确下一路由

## 副作用与 Human Gate
修改正式 Plan 和索引；仅在开始实际执行时更新 Progress 的 Objective 状态，不直接实现代码。

## 执行步骤
1. 从 Progress 读取当前 Objective；Standard 尚无 Plan 时立即创建并登记，不能把正式 Plan 视为可选
2. 先确认当前目标、范围和代码事实，避免按过期架构拆解任务
3. 按可独立推进的小目标划分有序 Phase；每个 Phase 只表达一个清晰任务单元
4. 在 Phase 内拆出可一次领取和完成的原子 Item；Item 描述要完成的任务，不罗列文件操作、代码写法或执行日志
5. 使用 `## Phase N：标题` 和 `- [ ] N.M 任务`；Phase 的 Item 全部完成时，该 Phase 自然完成，不增加独立状态字段
6. 已有正式 Spec 时，在对应 Phase 下保留简短链接；不建立子 Spec 状态表或重复验收正文
7. 新建 Item 保持未勾选；只有对应任务实际完成后才改为 `[x]`
8. 更新正式 Plan 和索引，将所属 Objective 汇总为 `正在处理`，再进入 autogo-plan-review；Review 通过前不执行第一条 Item

## 验证与完成
- Plan 只有目标、范围和 Phase Checklist，没有独立 Checklist 文件
- 每个 Phase 都是边界清楚的小目标，不是笼统容器
- 每个 Item 都是可以一次领取、执行和勾选的原子任务
- Item 描述结果或任务，不展开实现细节
- Plan 不包含执行过程、验证记录或额外状态字段
- 计划没有超出批准范围
- 新建或实质调整后的 Plan 已明确等待 Plan Review，不会直接进入实现

## 失败、重试与幂等
目标或范围不清时返回 autogo-change-intake；需要先澄清行为时返回 autogo-spec-write；需要设计决策时返回 autogo-solution-design。
- 重复执行前读取当前 Plan，不重复创建相同 Phase 或 Item。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
