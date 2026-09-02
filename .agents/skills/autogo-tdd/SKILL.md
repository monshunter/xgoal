---
name: autogo-tdd
description: "用失败测试、最小实现和安全重构交付可测试行为；在新增行为、修复可复现 Bug 或保护重构时使用。"
---
# autogo-tdd
## 目标
通过失败测试、最小实现和安全重构建立可执行行为契约，降低 Agent 自我确认偏差。
## 输入与发现
- Fast 的轻量行为目标，或 Standard 中当前 Objective、已 Review 的正式 Plan、Phase Item、Spec 与 AC ID
- 行为验收、现有测试框架和最小复现

## 输出与持久制品
- 先失败后通过的测试 Evidence
- 最小实现和必要重构
- 未覆盖风险说明
- Standard 中 Red/Green 成立后更新的 Spec AC 和 Phase Item；Fast 不创建计划状态

## 副作用与 Human Gate
修改测试和实现文件；不越过部署 Human Gate。

## 执行步骤
1. 确认当前路由；Fast 锁定轻量行为目标，Standard 从 Progress、正式 Plan/Spec 与 Plan Review 确认当前 Phase Item 和 AC ID
2. 编写或定位最小失败测试并确认失败原因正确，记录 Red Evidence
3. 实现最小代码使测试通过，记录 Green Evidence
4. 在测试保护下重构重复和坏味道
5. 运行相关测试集，必要时扩大回归范围
6. Red/Green 与回归验证成立后，Standard 才勾选对应 Phase Item 并按 Spec 规则更新 AC；Fast 只保留测试 Evidence，不创建 Progress/Plan/Review
7. 不为难以测试的副作用伪造 TDD；保持未勾选并改用契约、集成或 E2E

## 验证与完成
- 测试在修复前确实能暴露目标问题
- 通过不是由删除断言、跳过测试或错误 Mock 造成
- 测试关注行为而非无价值实现细节
- Standard 的 Phase Item 与相关 Spec AC 状态和当前 Red/Green 结果一致；Fast 未产生计划状态

## 失败、重试与幂等
无法构造可信单测时明确原因并升级为集成/E2E 验证。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
