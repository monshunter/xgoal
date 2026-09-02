# DESIGN-002：M3 Codex CLI Adapter

## 状态与范围

`accepted`

本设计实现 PLAN-004，只覆盖 Codex CLI 的 Passive/Active Probe、非交互执行、JSONL、结构化 Agent Result、角色 Sandbox、取消和安全 Resume，并把真实输出交给 M2 Workspace/Patch/Validator 独立验收。Claude Adapter、Reviewer、完整 Kernel Reconcile/Gate/Budget、Daemon/API 与 Final Report 属于 M4–M6。

## 当前 CLI 合同

当前验收环境为 `codex-cli 0.145.0`，本机 `codex exec --help` 与 `codex exec resume --help` 已确认：

- 非交互入口为 `codex exec`，Prompt 使用 stdin `-`；
- `--json` 输出 JSONL，`--output-schema <FILE>`约束最终消息；
- 顶层 `--sandbox read-only|workspace-write`、`--ask-for-approval never` 与 `--cd <DIR>`可作用于 exec/resume；
- Resume 使用 `codex exec resume <session-id> -`；
- `codex login status` 是不产生模型调用的本地认证状态检查，当前报告 ChatGPT 登录可用。

Adapter 不依赖未承诺的内部 Rust API；版本或帮助面缺失任一必要能力时 Passive Probe 失败关闭。

## 命令与权限

Start 的规范命令为：

```text
codex --ask-for-approval never --sandbox <role-policy> --cd <workspace>
  exec --json --output-schema <private-schema-file> --color never -
```

Resume 在完全匹配的 Session Binding 上使用同一顶层权限参数，再执行：

```text
codex --ask-for-approval never --sandbox <bound-policy> --cd <same-workspace>
  exec resume --json --output-schema <private-schema-file> <session-id> -
```

- Planner/Reviewer 只接受 `read-only`；Implementer 可使用 `workspace-write`，不得接受 `danger-full-access`。
- v0.1 Codex CLI 没有受信的细粒度 Tool Allowlist 合同，因此声明 `ToolAllowlist=false`；Invocation 请求非空 ToolPolicy 时拒绝，不伪装已限制。
- Adapter 只传入调用方已白名单化的环境，默认补充固定 locale；M3 `cli-session` 不接收 Token/Key/Secret 型环境变量。
- Packet 必须是绝对路径的只读 regular file；Prompt 可引用 Packet，但 Packet 内容不拼成命令参数。

## 执行生命周期与制品

```text
Start/Resume
  → 校验 Invocation + 创建 private invocation directory
  → 写 canonical invocation metadata / schema
  → 启动独立进程组并流式消费 stdout/stderr
  → 每行 JSON 先解析和脱敏，再发布 immutable raw event
  → 规范化 AgentEvent 并推送 EventSink
  → 校验最终 agent_message 为 AgentResult
  → Wait 返回 Claim；M2 再读真实文件系统和 Validator
```

运行布局：

```text
<runtime>/adapters/codex/
├── invocations/<invocation-id>/
│   ├── invocation.json
│   ├── output-schema.json
│   ├── events/000001.json ...
│   ├── stderr.log
│   └── result.json
├── sessions/<sha256(session-id)>.json
└── probes/<probe-id>.json
```

stdout 原始内容不直接落盘；Adapter 在内存中按行限长，解析为 JSON 后递归脱敏字符串，再以 `0600` immutable 文件发布。stderr 同样在限长内先脱敏后落盘。`--output-last-message` 不使用，避免 CLI 绕过脱敏直接写原始文本。

Cancel 取消 execution context；Supervisor 向独立进程组发送 TERM、等待 grace period 后 KILL 并 Wait。`Start` 仅建立一次 Invocation；重复 ID、目录或 result 发布均按幂等冲突拒绝。

## JSONL 与结果合同

- `thread.started` 提取受信 Session ID 并生成 `session` Event；`turn.*`、`item.*`、`error`、Usage 映射到规范类型。
- Command/File Change 只是 Claim；无法无损表达 argv 的供应商 command 字符串保留为 Summary，不伪造已执行事实。
- 未知 Event Type 仍保存脱敏 raw ref，并产生可审计 `unknown` Event，不使 parser 崩溃。
- 非 JSON、单行/总量超限、EOF 前未换行、没有 `thread.started`、缺失最终 `item.completed/agent_message`、Agent Result Schema 不匹配，统一返回 `INVALID_OUTPUT`；退出码 0 不能覆盖这些错误。
- 最终 Result 重新用 xgoal 严格 Decoder 校验并 Canonical 化，`AgentResult.Authority()` 仍是 `CLAIM`。
- Adapter 只接受与公开 Agent Result Schema 完全相同的调用方 Schema；发送给当前 Codex Provider 前移除声明性 `$schema`/`$id`、为 `const`/`enum` 补齐 JSON 类型，并把全部属性列入 `required`，以满足当前 Strict Structured Output 子集。Provider 结果返回后仍由公开 xgoal Schema 对应的严格 Decoder 再校验，转换不会扩大可接受结果。

## Probe 分层

- Passive：只执行 binary lookup、`--version`、`exec --help`、`exec resume --help` 和 `login status`；Provider Transport 为 `unknown`，不会创建 Agent Invocation。
- Active Contract：必须显式 `ProviderTransport=true` 且有正 MaxWallTime；运行最小 read-only 结构化回合，保存 Session、Usage（供应商提供多少记录多少）及 `cost=unknown`。超时、认证、JSONL 或 Schema 失败不能被 Passive 结果覆盖。

## Session 安全绑定

Session Binding 使用 Canonical Hash 绑定 Adapter/Profile、Work、Attempt、Goal Revision、Plan Revision、Base Tree、Packet Hash、Workspace、Sandbox、Tool Policy、环境变量名称和 Output Schema Hash。只有：

- Invocation 明确使用 `resume-compatible`；
- Session ID 来自已解析 `thread.started`；
- 本地 immutable Binding 存在且所有字段完全相等；
- 上次失败未由调用方分类为协议损坏或 no-progress；

才允许 Resume。任何漂移或缺失都拒绝；Kernel 后续可选择 Fresh Session，但 Adapter 不自动降级以掩盖恢复失败。

## M3 验收

- fixture executable 覆盖命令参数、stdin、已知/未知 JSONL、Usage、结构化结果、截断、退出码 0 但结果无效、取消进程组及新 Adapter 实例 Resume。
- 真实 CLI smoke 首先运行 Active Contract 与 Resume，再在临时真实 Git 仓库执行一个有界 Fast 文件修改和一个 Standard Implementer 修改。
- 两个写路径都由 M2 从冻结 Base Tree 捕获 Patch、检查 Scope、在干净 validation worktree 重放并运行受信 Validator；Agent Claim 或 CLI 退出码不作为完成判据。

## 失败与兼容

| 失败 | 决策 |
|---|---|
| Binary/help/认证缺失 | Probe unavailable，禁止调度 |
| JSONL/Result 损坏 | `INVALID_OUTPUT`，Session 不可 Resume |
| Sink/日志写入失败 | Cancel 进程组，保留已脱敏制品 |
| Session/权限/Revision 漂移 | 拒绝 Resume，交由 Kernel 创建 Fresh Attempt |
| 输出超限或超时 | 回收进程组，返回明确错误与有限日志 |
| 未知事件 | 保存并标为 unknown，继续读取已知合同 |

M3 不声明 Codex 提供成本金额，也不声明 L0 可阻止模型生成工具访问主机；实际限制由 Codex Sandbox 与项目可信边界共同提供，并在 Capability/Report 中披露。
