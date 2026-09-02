# REVIEW-024：DESIGN-003 M4 Claude Adapter 与独立 Review

## 审查对象

- Design：`DESIGN-003`
- Plan：`PLAN-005`
- 产品/技术边界：FR-020、FR-021、FR-042、FR-050 与技术 SPEC 12、17、M4

## Verdict

`PASS_WITH_NOTES`

## 审查结论

- 供应商 CLI、确定性 M2 Evidence、概率性 Review Finding 和持久状态的 owner 分离清楚；没有让 Reviewer approved 充当正确性证明。
- 正交 `review.Adapter` 保留技术 SPEC 的 Implementer AgentResult 接口，同时允许独立严格 Review Result，避免无版本扩展 Agent Envelope。
- Reviewer 工具集合不含写工具或 Bash，且 validation worktree、Packet、Profile、Session 与实际 Patch/Receipt 绑定，满足最小权限和独立性。
- SQLite 迁移、CAS 状态、Completion Projection 与不可变制品读回覆盖 Finding 的持久语义，并为 M5 主循环留下明确接点。
- 双向真实门禁验证 Provider 组合与 Session，而 M2 继续拥有文件与测试事实，分层合理。

## Notes

- 实现时必须以真实 Claude Stream JSON 事件确认 `structured_output` 与 Session，并确认供应商模型核算字段不会进入 xgoal 状态；fixture 不能替代 Active Probe。
- 如果当前 Codex/Claude 对公开 Review Schema 有不同 Strict 子集，转换只能缩窄 Provider 输出并在返回后重新用公开 Schema 校验，不能维护两个语义合同。
- Completion Projection 的 Finding count 更新必须在 Review 写入/状态转换同一事务内，并有重启/重复请求测试。

## 结论

Design 可进入 Phase 2/3 TDD；Notes 作为实现约束，不阻塞执行。
