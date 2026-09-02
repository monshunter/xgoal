---
name: autogo-spec-write
description: "创建或更新可验收的行为规范、边界和非目标；在新行为、产品语义、公共契约、接口、数据或兼容边界需要明确，或已有 Spec 与批准需求不一致时使用。"
---
# autogo-spec-write
## 目标
为当前正式 Plan 的相关 Phase 创建或更新可独立验收的 Spec，描述外部行为、边界、约束和验收标准，不把实现方案伪装成需求。
## 输入与发现
- Progress 中当前 Objective 和 Plan
- 当前正式 Plan 的 Phase Checklist 和已有 Spec 链接
- 用户目标、业务约束、现有行为和相关事实源
- autogo-change-intake 结果和调查证据
- 既有 Spec、契约、术语和非目标
- 当前任务已确认需要长期保存的行为合同

## 输出与持久制品
- 新建或更新的最小长期 Spec；任务不需要时不创建
- 明确的范围、非目标、术语、行为、异常和验收标准
- 带稳定 AC ID 的验收 Checklist 和 Evidence 占位
- 假设、开放问题和需用户决定项
- 对应 Plan Phase 下的简短 Spec 链接和已更新的 docs 工作区索引

## 副作用与 Human Gate
修改 docs；不得改变代码或在未批准情况下改变产品语义。

## 执行步骤
1. 从 Progress 确认当前上层 Plan，再读取对应正式 Plan；无上层 Plan 时返回 autogo-change-intake，不创建孤立 Spec
2. 先确认行为合同是否需要跨实现长期存在；已有准确 owner 时更新或引用，当前代码和测试已足够表达局部变更时停止且不创建 Spec
3. 只有新的独立行为边界没有现有 owner 时才新建；不为新 Plan、编号或形式完整复制 Spec
4. 使用已初始化的 `docs/specs/` 或项目既有权威路径，保留项目自有 README/INDEX 内容并更新索引
5. 使用项目既有身份、状态和替代约定；项目没有这些约定时不额外引入
6. 定义正常流程、异常流程、数据/权限/兼容性约束
7. 为每条验收标准分配稳定 AC ID，并初始化未勾选的验收 Checklist；未获得 Evidence 前不得打勾
8. 标记事实、假设和开放决策；不在 Spec 中展开低层实现
9. 在对应 Plan Phase 下补充简短 Spec 链接并更新索引，再路由 autogo-spec-review；不把 Spec 状态表写入 Plan 或 Progress

## 验证与完成
- 每个需求有可验证验收标准
- 行为边界只有一个清晰事实 owner，且 Progress 不复制 Spec 内容
- 没有为流程完整创建低价值 Spec
- 不与其他权威契约冲突；冲突已显式对账
- 范围和非目标足以阻止隐式扩张
- 未为 Fast 或已有契约内的简单局部变更创建无价值 Spec

## 失败、重试与幂等
产品语义不明确时提供推荐解释和影响，在 Spec、Review 或 Agent Todo 中记录阻塞并停在最小 Human Gate；Progress 的 Objective 保持 `正在处理`。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
