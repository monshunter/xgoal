# REVIEW-006：PLAN-001 M0 Change Review

## 审查对象

- Plan：`PLAN-001`
- Objective：`OBJ-001`
- Revision：`feat/xgoal-v0.1` 未提交工作树，完成全部 Phase Item 后的首次 Change Review
- 规范：[产品 SPEC](../../xgoal-product-spec-v0.1.md)、[技术 SPEC](../../xgoal-technical-spec-v0.1.md)

## Verdict

`FAIL`

## 发现

### [High] Go 协议校验比冻结 Schema 和安全合同更宽

- 位置：`internal/protocol/protocol.go` 的 `WorkPacket.Validate` 与 `AgentEvent.Validate`
- Evidence：`WorkPacket.Validate` 只要求 Network/Secret 非空，不校验允许值，也不拒绝 `git_push=true` 或 `production=true`；`AgentEvent.Validate` 不拒绝缺失 Command argv。嵌入 Schema 对这些字段有 Enum、Const 与 Required 约束。
- 影响：非法或越权 Packet/Event 可以通过 Go 校验并进入 Canonical Hash 或 Kernel，形成 Schema 与运行时双重真理源，破坏 Fail Closed。
- 路由：回到 `autogo-tdd`/协议 owner，建立 Schema 对齐负向测试并收紧 Go 校验。

### [High] `config validate` 会接受不完整或无效的受信执行配置

- 位置：`internal/config/config.go` 的 `Config.Validate`、`validateAgents` 与 `validateValidators`
- Evidence：Workspace Provider、Bootstrap Command、Scope Policy、Review/Policy/Report 未校验；Validator 可缺少 Phase、正 Timeout、Expected Exit Code；非 Fake Agent 的 Adapter 专属 Sandbox/PermissionMode 也未收敛。当前 README 把该入口描述为严格配置校验。
- 影响：拼写正确但语义无效或 Fail-Open 的受信配置会被 CLI 报告为 valid，后续执行组件无法安全依赖配置不变量。
- 路由：回到 `autogo-tdd`/配置 owner，为当前公开字段建立最小完整校验和负向矩阵。

## 已成立 Evidence

- `make verify-m0` 在首次 Review 前通过格式、全量测试、Race Detector、`go vet` 与 CLI smoke。
- 状态转换穷举一致性、32 路 Lease 竞争、Evidence 绑定失配矩阵与协议 Hash Golden Test 已通过。
- 上述 Evidence 不覆盖本 Review 新发现的非法协议/配置输入，因此不能将 Plan 关闭。

## 下一路由

修复两个 High 发现并运行定向负向测试、`make verify-m0` 与新的 Change Review。`PLAN-001` 的 Item 完成勾选保持不变，但 `PROGRESS.md` 中的 Plan 不能勾选。
