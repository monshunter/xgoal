---
name: autogo-bug-report
description: "记录具有复用价值的 Bug 现象、根因、修复、验证和预防；在重大、重复或跨会话问题修复后，或修复暴露 Harness/流程缺陷时使用。"
---
# autogo-bug-report
## 目标
记录具有复用价值的 Bug 现象、根因、修复、验证和预防措施，而不是把每个微小错误都文档化。
## 输入与发现
- 现象、时间线、最小复现、根因证据
- 修复 Diff、测试和运行结果

## 输出与持久制品
- Bug Report、影响范围、根因和促成因素
- 修复与验证 Evidence
- 预防、监控和 Evolution Candidate

## 副作用与 Human Gate
修改 docs 和索引；不直接改变治理规则。

## 执行步骤
1. 使用已初始化的 `docs/bugs/`，保留项目自有 README/INDEX 内容
2. 记录用户可见现象与实际影响
3. 区分根因、触发条件和促成因素
4. 关联修复、测试、Commit 和运行证据
5. 提出最小预防措施，避免泛化规则
6. 更新索引；系统性 Harness 问题送 autogo-session-review 或 autogo-harness-evolve

## 验证与完成
- 根因有证据，不用模糊“偶发”替代
- 预防措施对应根因且可验证
- 不重复创建同一问题报告

## 失败、重试与幂等
根因未确认时标记 investigation，不伪造结论。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
