# xgoal

`xgoal` 是一个面向长期软件工程目标的本地多 Agent 编排器。确定性 Go Kernel 负责任务状态、权限、验证、恢复和完成判断；Codex CLI 与 Claude Code CLI 只通过 Adapter 提供规划、实现和审查能力。Agent 的完成声明属于 Claim，不能替代受信 Validator 生成的 Evidence。

产品边界见 [产品 SPEC](xgoal-product-spec-v0.1.md)，协议、状态机和里程碑见 [技术 SPEC](xgoal-technical-spec-v0.1.md)。

## 当前实现状态

当前代码已完成 M0 契约骨架和 M1 持久状态与控制循环：

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

当前尚不能运行真实 Goal。Git worktree/Patch/Promotion、真实 Codex/Claude Adapter、Daemon/API、完整 Reconcile/Policy、报告和 Benchmark 将在后续里程碑实现。M1 的恢复测试使用真实子进程和 SQLite 文件，但仍不构成真实 Agent、Git 或用户旅程验收。

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

当前可用命令：

```text
xgoal version
xgoal config validate --file <path>
```

其他命令会以退出码 `2` 明确拒绝，不会返回占位成功。

## 安全边界

v0.1 只面向 macOS/Linux 上由用户明确信任的本地 Git 仓库，默认串行执行和 L0 本地进程隔离。L0 不等同于容器或虚拟机安全边界；Project/Tool Network 与 Project Secret 默认拒绝。Provider CLI 自有控制面连接和登录态是独立边界，不会写入 Work Packet 或 Validator 环境。
