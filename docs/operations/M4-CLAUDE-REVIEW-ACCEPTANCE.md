# M4 Claude Adapter 与跨 Provider Review 验收

## 验收范围

M4 接入 Claude Code CLI，并让 Codex 与 Claude Code 都可作为 Implementer 或独立 Reviewer。Reviewer 只接收绑定实际 Attempt、Session、Patch、Validator Receipt 与候选 Tree 的不可变 Review Packet；其结果和 Finding 权威为 `INFERENCE`，不能替代确定性验证。

## 可复现入口

Hermetic 合同与故障测试：

```sh
make m4-contract
```

显式使用本机 Codex/Claude 登录态和 Provider Transport 的双向真实验收：

```sh
make m4-real-smoke
```

完整 M0–M4 发布门禁：

```sh
make verify-m4
```

真实 smoke 是 opt-in；普通 `go test ./...` 不调用 Provider。

## 2026-09-02 当前 Evidence

- `claude --version`：`2.1.235 (Claude Code)`；实际 CLI 要求传入的 JSON Schema 去掉其本地校验器不识别的 Draft 2020 `$schema` / `$id` 元字段，公开 xgoal Schema 与最终严格解码合同保持不变。
- `claude_to_codex`：Claude Implementer Session `b622c0d3-0a75-4e99-abf1-d065b760c6df`；Codex Reviewer Session `01a0617d-1a5a-70e2-8748-8f52a3c91a4c`；候选 Tree `d20a1b28c86220c5d7b2b65f379564b39ecf12a0`；PASS。
- `codex_to_claude`：Codex Implementer Session `01a06182-b1cc-7102-b147-9686958ad1f4`；Claude Reviewer Session `6e05b7ca-13a2-4571-85ff-b8b08b91e94e`；候选 Tree `7e86cfbdc8cb1e3006979e4cb464fa857834cd3c`；PASS。
- 两条路径均先由测试进程校验文件精确字节并计算 Git Tree，再接受 Reviewer 的 `approved` 推断；Reviewer Session 与 Implementer Session 不同。

## 能力与安全边界

- Claude 使用 Print Mode、`stream-json`、严格 JSON Schema、stdin Prompt、`dontAsk`、角色工具白名单、进程组超时/取消和持久 Session provenance。
- Implementer 默认工具为 `Read,Glob,Grep,Edit,Write`；Reviewer/Planner 只允许只读工具，真实最小 Review 可进一步收窄为 `Read`。
- Provider Credential 仅按显式环境白名单传给 CLI 顶层进程，变量值不写入 Packet、元数据或日志；L0 本地进程隔离无法证明 CLI 子进程不可见，因此报告继续披露 `credential_isolation=L0`。
- Claude 未知事件保存为脱敏原始制品并归一为 `unknown`；完整的 EOF 末事件可被解析，截断 JSON、错误结果、非零退出、超限输出均 fail closed。
- 当前真实 smoke 使用有界的精确文件验证作为确定性证据；完整 Patch/Receipt 真实性约束由 Review Coordinator 和 hermetic 合同测试覆盖。
