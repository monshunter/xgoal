# REVIEW-027：DESIGN-004 M5 确定性控制面与单写 Daemon

## 审查对象

- Design：`DESIGN-004`
- Plan：`PLAN-006`
- Revision：初始版本
- 产品/技术边界：FR-051、FR-052、FR-070、FR-071、FR-080、技术 SPEC 18–20、23、27 与 M5

## Verdict

`PASS_WITH_NOTES`

## 审查结论

- Failure、Progress 与决策均为确定性输入输出，重复指纹 + 无实质进展明确禁止相同 Strategy 重试，没有把调度裁决交给 Agent 文本。
- Policy 区分 Provider Transport、Project Network 与 Secret；Gate 消费绑定资源、Scope、时限和次数，并由 SQLite 事务保证最后一次授权不会并发超用。
- 超时、输出上限、取消与 no-progress 只作为运行安全边界，不参与 Completion 成功判定。
- API/Daemon 保持单写边界，复用既有 Idempotency/Event owner；NDJSON 以唯一 Event ID 对应的 `(created_at,id)` 游标续传。
- 恢复基于持久状态、Lease Generation 与进程启动身份，不能确认归属时不误杀；迟到结果不推进状态。

## Notes

- Unix peer UID 在不同平台实现差异较大；实现必须以 build-tag 或能力探测隔离，不得因 macOS/Linux API 差异破坏跨平台构建。
- “全部 Endpoint”允许薄 Service 组合已有 Store 能力，但不得用 HTTP 200 空响应假装功能成立；未满足前必须返回结构化不可用错误并使 M5 Gate 失败。
- CLI 的 `daemon start` 是否后台化不影响协议正确性；至少提供可被服务管理器可靠拉起的前台 `daemon serve` 和可验证的单写/恢复行为。

## 结论

Design 可进入 Phase 2/3 TDD；Notes 是实现和验收约束。
