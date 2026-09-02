# xgoal

`xgoal` 是一个面向长期软件工程目标的本地多 Agent 编排器。确定性 Go Kernel 负责任务状态、权限、验证、恢复和完成判断；Codex CLI 与 Claude Code CLI 只通过 Adapter 提供规划、实现和审查能力。Agent 的完成声明属于 Claim，不能替代受信 Validator 生成的 Evidence。

产品边界见 [产品 SPEC](xgoal-product-spec-v0.1.md)，协议、状态机和里程碑见 [技术 SPEC](xgoal-technical-spec-v0.1.md)。

## 当前实现状态

当前代码已完成 M0 契约骨架、M1 持久状态与控制循环、M2 Git/环境/验证闭环，以及 M3 Codex Adapter：

- Go CLI 入口，以及严格的 `xgoal.yaml` 解析和校验；
- Goal、Work、Attempt、Effect 与 Evidence 状态类型；
- Work Packet、Agent Result、Agent Event 与 Evidence `v1alpha1` 协议和 Golden Hash；
- Fake Clock、Fake Adapter、Fake Process、内存 CAS/Lease Store；
- 仅供开发测试的确定性模拟 Kernel，证明 Agent Claim 不能直接完成 Goal。
- 每项目私有 SQLite Store、固定 WAL/同步参数和运行版本校验；
- 内嵌单向 Migration、历史 Hash 校验、升级前一致备份、失败回滚与中断备份保留。
- Goal Revision、Plan DAG、Work、Attempt、Lease、Gate、Idempotency 与 Effect Journal 的持久 Repository；
- 当前状态与 Event 同事务提交、Version CAS、项目级单活 Lease、Generation/TTL/Heartbeat 和迟到写回隔离；
- 依赖与 Required Gate 驱动的持久 Ready 调度，以及重读事实后原子提交的 Completion Predicate；
- 非终态 Effect 扫描与 `Request → Execute → Read Back → Observe` 跨进程恢复路径。
- 可信本地 Git 仓库、私有 Integration Ref，以及 detached Attempt/Validation Worktree 生命周期；
- 基于冻结 Base Tree 与实际文件系统的内容寻址 Patch Bundle，覆盖 tracked/untracked/binary/rename/mode/symlink/delete 和文件/目录拓扑转换；
- NFC/Unicode 大小写碰撞、根锚定 Scope、deny 优先、Symlink 逃逸和 Git 元数据防护；
- 明示 L0 能力的 Local Environment Provider、进程组监督、服务健康探针、白名单环境和可清理生命周期；
- 从冻结 Base Commit 加载的 Validator Registry、不可变日志与 Command Receipt，以及绑定 Goal/Config/Definition/Environment/Tree 的 Evidence；
- Workspace marker、Patch manifest/object、Environment Snapshot、Validator Definition/Run/Receipt 的 SQLite 关联、跨重启严格读回和篡改拒绝；
- 项目内串行 Promotion、xgoal Commit Trailer、Git Ref CAS、Effect Read Back 和 Commit 后崩溃幂等恢复。
- Codex CLI Passive/Active Probe、非交互 `exec`、流式 JSONL、严格结构化结果、角色级 sandbox、进程组取消和持久 Session Resume 绑定；
- 未知 Codex Event 的前向兼容、截断/缺失结果 Fail Closed、stdout/stderr 总量限制，以及递归脱敏后的不可变运行制品；
- 内容寻址只读 Work Packet Store，以及由真实 Codex 完成、再经 M2 Patch/Scope/Validator 独立验收的 Fast Goal 与持久 Standard Implementer Attempt。

当前尚不能通过用户 CLI 运行完整 Goal。Claude Adapter/独立 Reviewer、完整 Reconcile/Policy/Budget、Daemon/API、报告和 Benchmark 将在后续里程碑实现。M3 已完成真实 Codex 模型回合，但其 smoke 是 Adapter 发布门禁，不等同于 M4–M6 的完整用户旅程。

## M0 使用与验收

要求 Go 1.25 或兼容版本；`go.mod` 建议使用 Go 1.25.13，较旧的 Go 命令需允许标准 `GOTOOLCHAIN=auto` 下载匹配 Toolchain。

```bash
go run ./cmd/xgoal version
go run ./cmd/xgoal config validate --file xgoal.example.yaml
make verify-m0
```

`make verify-m0` 依次执行格式检查、单元测试、Race Detector、`go vet` 和两个真实 CLI 命令。命令全部成功才表示 M0 骨架在当前 checkout 通过验收，不表示全部 v0.1 Feature 已完成。

## M1 使用与验收

```bash
make verify-m1
```

`make verify-m1` 在 M0 基础上增加全仓 20 次乱序执行、真实子进程重启/Effect Read Back 恢复场景，以及 `CGO_ENABLED=0` 的 Darwin arm64、Linux amd64 SQLite 测试包交叉构建。通过只证明 M1 持久状态与控制不变量成立，不代表 M2–M6 或完整 v0.1 已交付。

## M2 使用与验收

```bash
make m2-failure-matrix
make verify-m2
```

`m2-failure-matrix` 明确运行 Agent 自建 Commit、Scope/Symlink 逃逸、Patch 冲突、Evidence 过期和 Promotion Commit 后崩溃恢复门禁。`verify-m2` 还执行全仓格式、单元/集成测试、20 次乱序、Race Detector、`go vet`、CLI smoke，以及 SQLite 与全仓 Linux 无 CGO 编译检查。通过只证明受信本地仓库上的 M2 确定性链路成立；L0 不隔离主机文件或网络，也不代表 M3–M6 的后续能力已验收。

## M3 使用与验收

```bash
make m3-contract
make m3-real-smoke
make verify-m3
```

`m3-contract` 使用本地 fake executable 验证参数、stdin、JSONL、脱敏、错误、取消与 Resume，不发起模型请求。`m3-real-smoke` 是显式有费用的真实门禁：使用 Codex CLI 自有登录态和 Provider Transport，在临时 Git 仓库完成 Active Contract、Fast、Resume 与 Standard Implementer 回合，再从 worktree 独立捕获和重放 Patch、执行冻结 Validator，并重启 SQLite 读回 Standard 制品。`verify-m3` 先执行全部 M2 与 M3 无费用检查，最后执行该真实门禁。

真实门禁不把 Agent 的 `AgentResult` 或文件变更事件当作完成证据。当前 Codex Profile 不承诺细粒度 Tool Allowlist 或成本金额报告；L0 也不能隔离主机文件或项目工具网络，因此只应在用户明确信任的仓库和主机登录态上运行。

当前可用命令：

```text
xgoal version
xgoal config validate --file <path>
```

其他命令会以退出码 `2` 明确拒绝，不会返回占位成功。

## 安全边界

v0.1 只面向 macOS/Linux 上由用户明确信任的本地 Git 仓库，默认串行执行和 L0 本地进程隔离。L0 不等同于容器或虚拟机安全边界；Project/Tool Network 与 Project Secret 默认拒绝。Provider CLI 自有控制面连接和登录态是独立边界，不会写入 Work Packet 或 Validator 环境。
