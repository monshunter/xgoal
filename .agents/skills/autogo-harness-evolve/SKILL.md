---
name: autogo-harness-evolve
description: "基于重复证据提出受控 Harness Evolution Proposal，并在授权后验证和应用；在用户显式调用、流程缺陷反复出现、一次高严重度事故，或规则冲突、Skill 重叠和确定性自动化机会出现时使用。"
---
# autogo-harness-evolve
## 目标
根据重复失败和复盘证据，以 Proposal、复杂度预算、回归和回滚受控演进 Harness；默认不直接修改治理。
## 输入与发现
- 用户反馈、Bug Report、历史 Evidence 和明确的复盘结论
- 现有项目指令、Skills、模板和 Skill 内包资源
- 自我保护内核与用户目标

## 输出与持久制品
- Observation / Candidate / Evolution Proposal
- 根因、最小变更、影响面、风险、验证、回滚和复杂度 Delta
- 批准后应用结果与历史场景回放

## 副作用与 Human Gate
未获得项目写入授权时，只在对话中报告 Observation 或 Proposal，不写文件。获得对应 docs 写入授权后才持久化 Proposal；Apply 可修改 Harness 文件，但治理核心必须用户批准。

## 执行步骤
1. 聚合重复证据并区分偶发问题和系统缺陷
2. 优先考虑删除规则、合并 Skill、减少状态或转为确定性检查
3. 形成最小 Proposal，列出新增/删除规则和用户介入变化
4. 保护真实性、风险、Human Gate、Evidence、不可逆规则和演进审批本身
5. 根治理、风险模型、个人全局 Harness 变化请求用户批准
6. 批准后小步应用，并运行与本次真实故障模式直接对应的测试或场景回放；不向目标项目安装 Harness 自检能力
7. 失败回滚并记录，成功后 Promote

## 验证与完成
- Proposal 有重复证据或高严重度理由
- 复杂度净变化可见且符合奥卡姆剃刀
- 未建立第二控制面或用户 CRUD
- 回归通过且可回滚

## 失败、重试与幂等
证据不足降为 Observation；无法验证不 Apply；新增规则引发更大冲突时回滚。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
