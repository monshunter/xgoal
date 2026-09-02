# xgoal

`xgoal` 是一个用 Go 实现的本地 Coding Agent 编排器。用户给出自然语言目标后，Planner 生成冻结的 Goal Contract 与 Work Graph；Kernel 以 SQLite 为唯一运行状态，串行调度 Codex CLI / Claude Code CLI，在独立 Git worktree 中实现、捕获 Patch、执行受信 Validator、独立 Review、晋升到私有 Integration Branch，最后在当前 Tree 上复验并生成可追溯报告。

核心原则是：Agent 的完成声明只是 Claim，只有 Git/文件事实、受信命令 Receipt、当前 Evidence 与必要的人类决策能够推进完成状态。产品合同见 [产品 SPEC](xgoal-product-spec-v0.1.md)，实现设计见 [技术 SPEC](xgoal-technical-spec-v0.1.md)。

## v0.1 能力

- 单一 Go CLI/Daemon，Unix Socket HTTP/JSON API，每项目私有 SQLite/WAL 状态；
- 自然语言、文件或 stdin Goal，严格 Planner Schema、冻结 Revision、DAG 与 Scope/Validator 校验；
- Codex 与 Claude 的 Probe、Plan、Start、Wait、Cancel、Resume 和结构化输出适配；
- 独立 Attempt/Validation worktree，完整 tracked/untracked/binary/rename/mode/symlink/delete Patch 捕获；
- `scope`、`command`、`file_assertion`、`runtime_probe`、`git_assertion` 五类受信 Validator、Command Receipt 与 Evidence；
- Standard 独立 Reviewer Session、Finding、有限 Human Gate 与无进展 Reconcile；
- Git ref CAS、xgoal Commit Trailer、Promotion Effect Journal 和崩溃后幂等读回；
- `status`、事件流、日志、Gate、清理与 Markdown/JSON Final Report；
- 固定六类 fixture、Native/AutoGo single/xgoal Standard 三组同口径 Benchmark Harness。

## 要求与构建

- macOS 或 Linux；
- Git；
- Go 1.25（`go.mod` 固定 `go1.25.13`，可使用标准 `GOTOOLCHAIN=auto`）；
- 至少一个已安装并登录的 `codex` 或 `claude` CLI。

```bash
go build -o ./bin/xgoal ./cmd/xgoal
./bin/xgoal version
go test ./...
```

## 快速开始

在干净、可信且位于命名分支的 Git 仓库根目录执行：

```bash
xgoal init
xgoal config validate --file xgoal.yaml
xgoal daemon serve
```

另一个终端中：

```bash
xgoal doctor
xgoal run --goal "为 HTTP 客户端增加有界重试并补齐测试" --id goal-retry --wait
xgoal status goal-retry --watch
xgoal gates goal-retry
xgoal report goal-retry
```

也可使用文件或 stdin：

```bash
xgoal run --goal-file ./GOAL.md
printf '%s\n' '修复并验收当前回归' | xgoal run --goal-file -
```

未指定 `--mode` 时使用 `xgoal.yaml` 的 `orchestration.defaultMode`。`standard` 要求独立 Reviewer；`fast` 仍必须经过 Scope、Patch、Validator、Promotion 与 Final Validation，不信任 Agent 自述。Planner 发现不能安全推断的关键语义时，Goal 进入 `WAITING` 并创建 Gate。

常用控制命令：

```text
xgoal status <goal-id> [--watch]
xgoal logs <attempt-id>
xgoal gates <goal-id>
xgoal approve <gate-id> --version <n> --reason <text>
xgoal pause|resume|cancel <goal-id> --version <n>
xgoal work retry|cancel <work-id> --version <n> [--reason <text>]
xgoal goal replan <goal-id> --file <request.json>
xgoal report <goal-id>
xgoal clean [project-id] --dry-run
```

`run --wait` 持续读取 SQLite 权威状态，并在 Goal `Completed`、`Waiting`、`Cancelled` 时分别退出 0、3、4；不带 `--wait` 只表示 Goal 已被持久接收。

## 配置与安全

[xgoal.example.yaml](xgoal.example.yaml) 展示完整 v0.1 配置。Validator 的 `argv` 来自版本管理配置，Agent 不能注入命令或削弱 Required Validator。远端 push、发布、生产操作和项目 Secret 默认拒绝；Provider Transport 与 CLI 自有登录态是单独的受信执行通道。

v0.1 是 Local Process Provider，实际隔离等级为 L0。它不能像容器/VM 一样证明 Agent CLI 无法读取用户主目录或访问主机网络，只适合用户明确授权的可信本地仓库。详见 [威胁模型](docs/architecture/THREAT-MODEL-v0.1.md) 和 [操作与恢复手册](docs/operations/V0.1-OPERATIONS-RECOVERY.md)。

## 验收与 Benchmark

不会调用 Provider 的完整发布门禁：

```bash
make verify-m6
```

真实 Provider smoke 必须显式运行，不包含在 `verify-m6` 中：

```bash
make m3-real-smoke
make m4-real-smoke
```

固定 Benchmark Suite 校验：

```bash
xgoal benchmark validate --file benchmarks/suite.json
xgoal benchmark run --file benchmarks/suite.json --task <task-id> --group <group-id> --run 1 -- <runner argv...>
```

当前仓库只确认 suite/fixture/隐藏验收/三组逐任务超时口径可复现；没有执行或发布三组真实性能比较，因此不声称 xgoal 比对照组更快或成功率更高。逐条结果见 [v0.1 发布验收](docs/operations/V0.1-RELEASE-ACCEPTANCE.md)。

## 设计与许可

- [架构与 M0–M6 设计](docs/architecture/INDEX.md)
- [ADR](docs/decisions/INDEX.md)
- [操作记录](docs/operations/INDEX.md)
- [Acknowledgements](ACKNOWLEDGEMENTS.md)

本项目使用 [Apache License 2.0](LICENSE)。
