# REVIEW-005：xgoal 技术 SPEC v0.1 修订复审

## 审查对象

- Design：`xgoal-technical-spec-v0.1.md`
- Revision：v0.1 Draft 修订 2，SHA-256 `ae92fbdda66baae8937b2e81b75e80d3d4631bbbde6ccc67b7be8545410ce290`
- 前序 Review：`REVIEW-003`

## Verdict

`PASS_WITH_NOTES`

## 发现处置

- Provider Control Plane、Project/Tool Network、CLI Credential 与 Project Secret 已成为独立 Policy Action 和配置事实，核心 Agent 调用不再与默认网络 Deny 自相矛盾。
- Passive Probe 与显式 Active Contract Probe 已分层；真实模型调用的认证、网络、预算和 Evidence 边界明确。
- RFC 8785 Canonical Encoding、域分隔 Hash 与 Scope Pattern v1 已冻结，并要求跨平台 Golden Test。
- 内容寻址 Patch Bundle 已覆盖 tracked/untracked/binary/rename/mode/symlink/delete，文本 diff 仅作为 Review 投影。
- 技术验收已增加 Provider/Probe、Canonical/Scope 和 Patch Bundle 门禁。

## Notes

- SQLite Driver/CGO 策略必须在 M1 实施前形成 ADR。
- macOS/Linux 文件锁与 Unix Socket Peer UID 方案必须在 M5 Daemon/API 实施前形成 ADR。
- 当前 Codex/Claude Evidence 只证明本机 binary/version/help 命令面；真实认证、事件 Schema、取消与 Resume 必须在 M3/M4 使用 Active Contract Probe 和双向角色 E2E 验收。

这些 Notes 不阻塞 M0 内存协议与模拟闭环，但在对应后续 Plan 中属于必需任务。

## 下一路由

允许进入 `PLAN-001` Phase 2；按 `autogo-tdd` 与 `autogo-change-implement` 实现 M0。
