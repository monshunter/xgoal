# DESIGN-003：M4 Claude Adapter 与独立 Review

## 状态与范围

M4 历史版本对应 PLAN-005，已经验收；OBJ-003 修订工作目录与 Review Evidence 合同，替代独立 validation worktree 假设。修订版需要当前 Spec/Design 审查与实现验收，旧 Review/Receipt 仍保留原版本与 Tree 绑定，不重写为新合同证据。

Claude Code 负责原生推理/工具回合；xgoal Adapter 负责供应商进程和不可信输出；Review Coordinator 负责把实际 Patch/Validator 事实编译为只读 Review Packet；SQLite Store 负责 Review/Finding 的持久状态。项目单实例及生命周期见 [DESIGN-004](DESIGN-004-m5-control-daemon.md)，当前目录和快照协议见 [DESIGN-001](DESIGN-001-m2-git-environment-validation.md)，最终闭环见 DESIGN-005。

## 当前 Claude CLI 合同

M4 历史验收版本为 `Claude Code 2.1.235`；当前运行版本及以下调用能力须重新经过 Passive Probe：

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
- Passive Probe 只执行 binary/version/help 与 `auth status`，不产生模型请求；Active Probe 才显式使用 Provider Transport，并受正超时边界约束。
- Stream JSON 的 init/result Session ID 与最终 `structured_output` 是供应商输入，必须校验、脱敏并持久化后才能归一化；供应商 Usage/Cost 字段在持久化前丢弃，不进入 xgoal 协议、状态或报告。
- 所有角色的进程 CWD 均为项目绑定的当前主 checkout 根目录；不创建或删除 Git worktree，也不另复制代码作为执行目录。Implementer 直接修改该目录，Reviewer 使用只读工具权限；调用与验证受项目内串行执行槽监督。

## Adapter 生命周期与恢复

Claude Adapter 与 M3 共享 `adapter.Adapter` 合同：Start/Resume 创建保存元数据的私有 Invocation 目录，写只读 Schema/Packet；Supervisor 在目标命令执行前完成启动意图与进程身份登记，管理独立进程组，stdout 逐行解析且 stderr 限长，Wait 只返回严格 `AgentResult` Claim。Invocation 目录不承载另一份执行代码。未知事件保存为 `unknown`；截断、无 init/result、`is_error=true`、Schema 错误或退出码不一致均 Fail Closed。

Session Binding 使用本地不可变 Hash 绑定 Adapter/Profile、Work、Attempt、Goal/Plan Revision、Base Tree、Packet、当前项目执行根、Role、Permission Mode、工具集合、环境变量名和 Output Schema。仅成功且协议完整的回合可发布 Binding；Resume 任一字段漂移，或 Kernel 无法确认当前 Git 身份与现场时拒绝。旧 worktree Session 不改绑后自动续跑；相应未完成目标进入可操作的迁移等待。

## Review 公共合同

M4 新增两个协议：

```text
Review Packet v1alpha1
  implementation attempt/work/goal/plan/base/candidate/config
  current project root + Git identity + candidate snapshot
  Patch Bundle hash/path
  Validator Receipt hash/path
  required review checks + reviewer profile/session policy

Review Result v1alpha1
  review_status: approved | changes_requested | blocked
  findings[]: client id/severity/category/path/line/claim/basis/recommended_fix
  suggested_validators[]
```

Review Packet 由 Coordinator 从实际制品构建，保存为 `0400` canonical 文件。Reviewer Prompt 只引用 Packet；Reviewer 在当前项目根目录使用只读权限。Review 启动前核对 Candidate Tree、项目身份、HEAD、symbolic ref 与真实 index 指纹；结束后再次核对相同内容。只有前后均匹配时，Result 才能作为绑定该 Candidate 的 Review 记录进入 Evidence。期间出现变更即失效，不能仅凭只读参数或 `approved` 忽略漂移。Review Result 和每个 Finding 的 Authority 固定为 `INFERENCE`；`approved` 只代表未发现问题。

现有 `adapter.Adapter.Wait → AgentResult` 保持技术 SPEC 合同不变。Reviewer 是正交扩展接口 `review.Adapter.Review`，由 Codex/Claude Adapter 实现同步的只读结构化回合，并复用相同进程监督、脱敏、Schema 与 Session Binding 原则；这样不向 Implementer Envelope 注入版本外字段。

独立性条件：Reviewer Profile 与 Implementer Profile 不同、Reviewer Session 与 Implementer Session 不同、Role 为 reviewer、CWD 为绑定的当前项目根、Packet/Schema/权限及所审 Candidate 完全绑定。Reviewer 在 Implementer 与 Validator 结束并完成快照核对后串行运行。独立性来自不同 Profile/Session 与只读权限、前后快照核对，不声称来自工作区或主机隔离。任一条件未知即拒绝记录 Review。

## SQLite owner

Migration 0003 新增 `review_runs` 与 `review_findings`：Review Run 唯一绑定 Work、Implementation Attempt、Reviewer Profile/Session、Packet/Result Hash 和不可变路径；Finding 绑定 Review Run 并保存稳定状态：

```text
OPEN → RESOLVED_BY_PATCH | DISPROVED_BY_EVIDENCE | WAIVED_BY_HUMAN | SUPERSEDED
```

记录 Blocker/High Finding 时更新 Completion Projection 的 open count；状态转换使用 Version CAS 和同事务 Event。Result/Packet 文件或 Hash 不匹配时拒绝跨重启读回。Reviewer 建议 Validator 只保存为 Claim，不直接改变冻结 Registry。

## 双向真实验收

临时可信 Git 仓库执行两条路径：

1. 当前目录 Codex Implementer → Capture/Scope/同目录 Validator → Claude Reviewer；
2. 当前目录 Claude Implementer → Capture/Scope/同目录 Validator → Codex Reviewer。

每条路径都验证不同 Profile/Session、只读 Reviewer 权限、结构化 Result/Finding、Validator 与 Reviewer 前后不变的 Candidate Tree/Git 身份、不可变 Review 制品和 SQLite 重启读回。还须验证所有角色在同一当前根目录执行、没有新增 worktree 或执行代码副本、用户 HEAD/ref/index 保持且修改留在当前目录。Reviewer 可返回无 Finding 的 approved；验收仅证明结构化审查路径成立，不宣称 Reviewer 证明实现正确。

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

Review 失败、取消或发现现场漂移时，保留当前修改、历史 worktree 与全部已登记制品，返回 Reconcile/等待；不自动 reset、clean、stash、切分支或将另一份代码覆盖回当前目录。
