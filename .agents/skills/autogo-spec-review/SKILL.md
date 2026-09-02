---
name: autogo-spec-review
description: "只读审查 Spec 的一致性、完备性和可测试性；在 Spec 新建或实质变化后、进入设计或实现前，或发现规范—实现漂移时使用。"
---
# autogo-spec-review
## 目标
以只读方式审查 Spec 的一致性、完备性、可测试性、简洁性和事实依据。
## 输入与发现
- Progress 中的 Objective 和 Plan；待审 Spec、上游目标、现有契约和项目事实从正式制品读取
- 当前任务与待审 Spec 的真实关系
- 相关组件项目指令和实际风险信号

## 输出与持久制品
- PASS / PASS_WITH_NOTES / FAIL / BLOCKED
- 按严重度排序的发现、证据、影响和修复建议
- 绑定当前 Spec 与真实行为边界的 Review 结论、未解决决策点和建议下一路由
- 已写入 Review 制品的阻塞或下一步；Review PASS 不等于该 Spec 已交付完成

## 副作用与 Human Gate
默认只读被审 Spec；只可写 Review 记录和索引。

## 执行步骤
1. 从 Progress 确认当前上层 Plan，再从对应 Phase 确认待审 Spec 和版本
2. 确认审查范围和权威事实源，检查是否错误新建了本可由现有合同拥有的重复 Spec
3. 按项目既有文档约定检查身份、状态和替代关系，再逐项检查目标、边界、术语、行为、异常、兼容性和验收
4. 检查是否混入不必要实现细节或过度设计
5. 检查每条验收标准有稳定 AC ID 且能被独立验证
6. 只记录发现，不在审查中隐式修改 Spec
7. 将 Verdict 和下一阶段写入 Review 制品；PASS 只批准规范内容，不勾选交付验收项，也不更新 Progress

## 验证与完成
- 每条 FAIL 有具体证据和可执行修复方向
- 结论与发现严重度一致
- Review、Spec 和正式 Plan 指向同一审查对象
- 没有因作者身份而降低审查标准

## 失败、重试与幂等
事实不足时 BLOCKED 并路由 autogo-investigate；需要修复时返回 autogo-spec-write。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
