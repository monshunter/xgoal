# PLAN-013：贯通 Agent Profile 与项目 Harness

目标：使用户配置、角色权限、Provider 调用和恢复记录一致，并明确目标项目工程 Harness 的可用状态。

范围：模型、思考深度、权限和工具配置，确定性 Profile 选择，三个原有角色的执行配置快照，Harness 检查及委派职责。

## Phase 1：执行配置

- [ ] 1.1 为 Profile 建立模型、思考深度与角色约束的有效配置解析及兼容校验。
- [ ] 1.2 将有效配置贯通 Planner、Implementer、Reviewer 的 Provider 参数及会话恢复身份。
- [ ] 1.3 明确多 Profile 的角色绑定与诊断输出，验证不支持和冲突配置不会静默生效。

## Phase 2：项目 Harness

- [ ] 2.1 实现所需 Harness 的发现、兼容检查与执行前拒绝，并提供 Provider 可发现的最小委派合同。
- [ ] 2.2 验证工程知识与 xgoal 运行状态的 owner 边界，记录规则输入及可观测加载证据。

## Phase 3：收口

- [ ] 3.1 完成双 Provider 参数合同与实际 Provider 配置验收、Change Review、文档对账和提交。
