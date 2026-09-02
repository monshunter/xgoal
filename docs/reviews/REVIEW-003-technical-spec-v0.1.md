# REVIEW-003：xgoal 技术 SPEC v0.1

## 审查对象

- Design：`xgoal-technical-spec-v0.1.md`
- Revision：v0.1 Draft，SHA-256 `09dedbf04beee6b252b9f89c6a2f2a41d00c01bd4353bc8aff62f1c6d170e185`
- Plan：`PLAN-001` Phase 1

## Verdict

`FAIL`

总体架构、状态 owner、Effect 恢复和最终证据谓词具备实施基础，但以下边界直接影响可运行性、安全与跨进程一致性，必须在进入协议实现前归位到技术 SPEC。

## 当前环境 Evidence

- `go version`：`go1.24.5 darwin/arm64`
- `git --version`：`git version 2.39.2 (Apple Git-143)`
- `codex --version`：`codex-cli 0.145.0`
- `claude --version`：`2.1.235 (Claude Code)`
- 当前 Codex CLI 和[官方命令参考](https://developers.openai.com/codex/cli/reference)均确认 `codex exec`、JSONL、`--output-schema`、`read-only|workspace-write` sandbox 与 `exec resume` 能力。
- 当前 Claude Code CLI 确认 Print Mode、`json|stream-json`、`--json-schema`、`--allowedTools`、`--permission-mode` 与 `--resume` 能力。

这些命令只证明本机版本与静态命令面存在，不证明认证、供应商网络、真实结构化回合或 Resume 已通过。

## 发现

### Blocker：供应商控制面连接与项目网络策略被合并为同一种权限

设计一方面要求 Codex/Claude CLI 调用远端模型，另一方面把 Planner、Implementer、Reviewer 的 `ACCESS_NETWORK` 全部默认设为 `DENY`，并以 `runtime.network: deny` 表达默认策略。若它包含 CLI 的供应商连接，核心 Goal 编译和 Attempt 无法运行；若它只指 Agent 在项目内发起的网络工具调用，当前 Policy Action、配置和 Evidence 又无法表达两者差异。

修复方向：明确区分受信的 Provider Control Plane Transport 与 Agent/Project Tool Network。前者按 Agent Profile 和用户已有认证调用且不得向 Work Packet 暴露凭据；后者继续默认拒绝并只能有限 Gate。状态、doctor 和报告必须分别展示两种能力。

### High：Provider 认证与 `USE_SECRET=DENY` 的边界未定义

CLI 需要读取自身登录态、Keychain 或受控 API Key 才能调用模型，但配置又声明 `secrets: deny`。当前设计没有说明 Kernel 可以让 CLI 使用何种 Provider Credential、如何避免将其传给 Agent 工具/项目命令、如何脱敏和如何在缺失认证时 Fail Closed。

修复方向：增加 Provider Credential 边界；v0.1 优先复用 CLI 自有登录态，不把 Secret 写入 Packet、日志或 Validator 环境，显式 API Key 注入必须走受限 Secret/Gate。

### High：Probe 的被动诊断与付费主动协议测试没有分层

Codex Adapter 要求 `Probe` 执行“最小无副作用协议测试”，但真实模型回合会使用供应商网络与认证。普通 `doctor` 不应在没有明确提示时自动产生该副作用。

修复方向：定义 Passive Probe（binary/version/help/config/auth presence）与 Active Contract Probe（真实最小回合）两级；Active 仅由显式参数、适用 Policy、正超时与 Evidence 触发。

### High：安全哈希与写 Scope 缺少规范算法

Goal、Plan、Config、Packet 和 Evidence 多处依赖 Canonical Hash，但没有指定 Canonical JSON/YAML 算法；Scope 使用 `/**`、`/internal/**` 等表达式，却没有定义 glob 方言、路径根、大小写、分隔符、目录自身匹配、symlink 和 Unicode 规范化语义。不同实现结果会破坏证据过期、幂等键和范围隔离。

修复方向：冻结 v1alpha1 的 canonical encoding 与 scope pattern contract，并用 Golden Test 覆盖 macOS/Linux 行为。

### High：Patch Artifact 无法完整承载技术验收要求

Patch Manifest 只给出 entry 元数据和一个 `patch_file`，未规定 untracked、binary、rename、mode、symlink target 与删除内容的不可变存储和干净重放格式，但技术验收明确要求这些变化都能归因和重放。

修复方向：定义 Patch Bundle 的清单、内容对象、哈希、模式/链接语义和应用顺序；禁止依赖 Agent Commit 或仅靠文本 diff。

### Medium：平台关键实现决策应在对应里程碑前补充 ADR

SQLite Driver/CGO 策略、macOS/Linux 文件锁与 Unix Socket Peer UID 读取都影响发布可移植性。它们不阻塞 M0 内存骨架，但必须在 M1/M5 实施前形成明确 ADR 和跨平台测试入口。

## 下一路由

在 `PLAN-001` 的 `1.3` 使用 `autogo-spec-write` 与必要的设计修订修复 Product/Technical SPEC 阻塞项，随后重新执行 Spec Review 与 Design Review。平台 ADR 在对应后续 Plan 中创建，不提前虚构实现事实。
