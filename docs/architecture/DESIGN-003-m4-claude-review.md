# DESIGN-003：M4 Claude Adapter 与独立 Review

## 状态与范围

`accepted`

本设计实现 PLAN-005。Claude Code 负责原生推理/工具回合；xgoal Adapter 负责供应商进程和不可信输出；Review Coordinator 负责把实际 Patch/Validator 事实编译为只读 Review Packet；SQLite Store 负责 Review/Finding 的持久状态。M5 才把 Finding 接入完整 Reconcile、Gate、Promotion 和 Completion 主循环。

## 当前 Claude CLI 合同

当前验收版本为 `Claude Code 2.1.235`：

```text
claude -p --input-format text --output-format stream-json --verbose
  --json-schema <canonical-json>
  --permission-mode dontAsk
  --tools <role-tools> --allowedTools <role-tools>
  [--resume <bound-session-id>]
```

- Prompt 走 stdin；不得使用 shell 拼接。
- Implementer 工具基线为 `Read,Glob,Grep,Edit,Write`；M4 smoke 不授予 Bash、网络、MCP 或外部写入。
- Planner/Reviewer 仅为 `Read,Glob,Grep`，不授予 Edit、Write 或 Bash。
- `--permission-mode dontAsk` 使未预授权工具直接拒绝，不进入 CLI 交互确认。
- Passive Probe 只执行 binary/version/help 与 `auth status`，不产生模型请求；Active Probe 才显式使用 Provider Transport 与预算。
- Stream JSON 的 init/result Session ID、最终 `structured_output`、Usage/Cost 是供应商输入，必须校验、脱敏并持久化后才能归一化。

## Adapter 生命周期与恢复

Claude Adapter 与 M3 共享 `adapter.Adapter` 合同：Start/Resume 创建私有 Invocation 目录，写只读 Schema/Packet，Supervisor 管理独立进程组，stdout 逐行解析且 stderr 限长，Wait 只返回严格 `AgentResult` Claim。未知事件保存为 `unknown`；截断、无 init/result、`is_error=true`、Schema 错误或退出码不一致均 Fail Closed。

Session Binding 使用本地不可变 Hash 绑定 Adapter/Profile、Work、Attempt、Goal/Plan Revision、Base Tree、Packet、Workspace、Role、Permission Mode、工具集合、环境变量名和 Output Schema。仅成功且协议完整的回合可发布 Binding；Resume 任一字段漂移时拒绝。

## Review 公共合同

M4 新增两个协议：

```text
Review Packet v1alpha1
  implementation attempt/work/goal/plan/base/candidate/config
  validation workspace + Patch Bundle hash/path
  Validator Receipt hash/path
  required review checks + reviewer profile/session policy

Review Result v1alpha1
  review_status: approved | changes_requested | blocked
  findings[]: client id/severity/category/path/line/claim/basis/recommended_fix
  suggested_validators[]
```

Review Packet 由 Coordinator 从 M2 实际制品构建，保存为 `0400` canonical 文件。Reviewer Prompt 只引用 Packet；Reviewer 在 validation worktree 使用只读权限。Review Result 和每个 Finding 的 Authority 固定为 `INFERENCE`；`approved` 只代表未发现问题。

现有 `adapter.Adapter.Wait → AgentResult` 保持技术 SPEC 合同不变。Reviewer 是正交扩展接口 `review.Adapter.Review`，由 Codex/Claude Adapter 实现同步的只读结构化回合，并复用相同进程监督、脱敏、Schema 与 Session Binding 原则；这样不向 Implementer Envelope 注入版本外字段。

独立性条件：Reviewer Profile 与 Implementer Profile 不同、Reviewer Session 与 Implementer Session 不同、Role 为 reviewer、Workspace 为 M2 validation worktree、Packet/Schema/权限完全绑定。任一条件未知即拒绝记录 Review。

## SQLite owner

Migration 0003 新增 `review_runs` 与 `review_findings`：Review Run 唯一绑定 Work、Implementation Attempt、Reviewer Profile/Session、Packet/Result Hash 和不可变路径；Finding 绑定 Review Run 并保存稳定状态：

```text
OPEN → RESOLVED_BY_PATCH | DISPROVED_BY_EVIDENCE | WAIVED_BY_HUMAN | SUPERSEDED
```

记录 Blocker/High Finding 时更新 Completion Projection 的 open count；状态转换使用 Version CAS 和同事务 Event。Result/Packet 文件或 Hash 不匹配时拒绝跨重启读回。Reviewer 建议 Validator 只保存为 Claim，不直接改变冻结 Registry。

## 双向真实验收

临时可信 Git 仓库执行两条路径：

1. Codex Implementer → M2 Capture/Scope/Replay/Validator → Claude Reviewer；
2. Claude Implementer → M2 Capture/Scope/Replay/Validator → Codex Reviewer。

每条路径都验证不同 Session、只读 Reviewer 权限、结构化 Result/Finding、不变的 Candidate Tree、不可变 Review 制品和 SQLite 重启读回。Reviewer 可返回无 Finding 的 approved；验收仅证明结构化审查路径成立，不宣称 Reviewer 证明实现正确。

## 失败与边界

| 失败 | 决策 |
|---|---|
| Claude binary/help/auth 缺失 | Probe unavailable，禁止对应调度 |
| Stream JSON/structured output 损坏 | `INVALID_OUTPUT`，禁止 Resume/Review 记录 |
| 未授权工具请求 | CLI `dontAsk` 拒绝；Result blocked/failed 或 Adapter 错误 |
| Reviewer 与 Implementer Session/Profile 相同 | 独立性失败，拒绝持久化 |
| Packet/Patch/Receipt/Tree 漂移 | Review Artifact invalid，返回 Reconcile owner |
| Blocker/High Finding | 保存 OPEN；M5 接入阻断 Promotion/Completion |
| Medium/Low/Note | 保存并披露，不自动覆盖确定性 Evidence |

L0 与原生 CLI 权限不是主机级隔离。Provider Credential 仍由 CLI 登录态持有，不进入 Packet、Prompt、Validator 环境或未脱敏日志；真实 smoke 仅在用户信任的临时仓库运行。
