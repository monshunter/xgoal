---
name: autogo-design-review
description: "只读审查系统设计的边界、契约、状态、风险和复杂度；在 Solution Design 新建或实质更新后，或架构、数据、契约和基础设施决策进入实现前使用。"
---
# autogo-design-review
## 目标
只读审查 Solution Design 是否满足 Spec、边界清晰、可验证、可运维且不过度设计。
## 输入与发现
- Progress 中当前 Objective 和 Plan；待审 Design 与内部 Spec 从正式制品读取
- Spec、Design、ADR、现有系统事实和风险约束
- 当前任务与待审 Design 的真实关系

## 输出与持久制品
- PASS / PASS_WITH_NOTES / FAIL / BLOCKED
- 边界、契约、状态、失败、安全、成本和复杂度发现
- 必须修正项与建议替代方案
- 已写入 Review 制品的 Verdict 和下一步

## 副作用与 Human Gate
默认只读被审 Design；只可写 Review 制品和索引。

## 执行步骤
1. 从 Progress 确认当前上层 Plan，再绑定实际待审 Design
2. 从 Spec 验收和系统不变量反向审查，并检查是否错误新建了重复架构 owner
3. 按项目既有约定检查文档身份、状态和替代关系，再检查组件职责、依赖方向和事实源
4. 检查 API/事件/数据兼容性和迁移
5. 检查失败恢复、观测、安全、成本和回滚
6. 寻找更简单的生态方案或可删除抽象
7. 只记录发现，不边审边改
8. 将 Verdict、阻塞和下一步写入 Review 制品，不写入 Progress

## 验证与完成
- 关键风险均有覆盖
- 推荐方案相对主要替代方案的理由清楚
- FAIL 可追溯到 Spec、事实或工程原则
- Review 与当前正式 Plan/Spec 和设计版本一致

## 失败、重试与幂等
事实不足时 BLOCKED；设计缺陷返回 autogo-solution-design；规范缺陷返回 autogo-spec-write。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
