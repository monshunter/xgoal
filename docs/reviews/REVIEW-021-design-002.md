# REVIEW-021：DESIGN-002 M3 Codex CLI Adapter

## 审查对象

- Design：`DESIGN-002-m3-codex-adapter`
- Plan：`PLAN-004`
- 当前 CLI：`codex-cli 0.145.0`

## Verdict

`PASS_WITH_NOTES`

## 审查结论

- 设计使用公开 CLI 参数和 stdin，不经 shell 拼接；角色 Sandbox、非交互批准、Packet/Schema 路径和环境边界可直接测试。
- stdout/stderr 在落盘前限长和脱敏，未知事件可追溯但不影响已知事件解析；结果缺失或不合法时即使退出码 0 也 Fail Closed。
- Session Binding 覆盖 SPEC 要求的 Work/Profile/Goal/Plan/Base/权限，并额外绑定 Attempt、Packet、Workspace 与 Schema；新进程实例可读回，错误时不自动放宽为 Fresh。
- Probe 明确区分不调用 Provider 的 Passive 与显式 Active；Active 使用正超时并保存独立 Evidence。
- 真实写入验收以 M2 Patch/Scope/Replay/Validator 为最终 owner，符合 Claim 与 Evidence 分层。

## Notes

- 当前 Codex JSONL 的具体 Item 子类型允许向前兼容；只有 Session 与最终 Agent Result 等进入控制决策的字段使用严格已知合同。
- M5 接入 Kernel 时必须把协议损坏/no-progress 分类传入 SessionPolicy，并让 Active Probe 保持显式触发和独立 Evidence。

## 下一路由

允许进入 Phase 2，按 fixture-first 的 TDD 顺序实现 parser、process、probe 与 resume。
