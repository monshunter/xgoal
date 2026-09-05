# xgoal

`xgoal` 是一个用 Go 实现的本地 Coding Agent 编排器。用户给出自然语言目标后，Planner 生成冻结的 Goal Contract 与 Work Graph；Kernel 以 SQLite 为唯一运行状态，串行调度 Codex CLI / Claude Code CLI，在当前 Git 主工作目录中实现、捕获 Patch、执行受信 Validator、独立 Review，并把验收结果记录到私有审计 Commit/Ref，最后在当前 Tree 上复验并生成可追溯报告。

核心原则是：Agent 的完成声明只是 Claim，只有 Git/文件事实、受信命令 Receipt、当前 Evidence 与必要的人类决策能够推进完成状态。产品合同见 [产品 SPEC](xgoal-product-spec-v0.1.md)，实现设计见 [技术 SPEC](xgoal-technical-spec-v0.1.md)。

## v0.1 能力

- 单一 Go CLI/Daemon，Unix Socket HTTP/JSON API，每项目私有 SQLite/WAL 状态；
- Goal 与规划请求事务性接受，后台规划、结果原子发布、进程归属登记与重启恢复；
- 自然语言、文件或 stdin Goal，严格 Planner Schema、冻结 Revision、DAG 与 Scope/Validator 校验；
- Codex 与 Claude 的 Probe、Plan、Start、Wait、Cancel、Resume 和结构化输出适配；
- 同项目串行使用当前工作目录，以私有 index 捕获完整 tracked/untracked/binary/rename/mode/symlink/delete Patch；
- `scope`、`command`、`file_assertion`、`runtime_probe`、`git_assertion` 五类受信 Validator、Command Receipt 与 Evidence；
- Standard 独立 Reviewer Session、Finding、有限 Human Gate 与无进展 Reconcile；
- Git ref CAS、xgoal Commit Trailer、Promotion Effect Journal 和崩溃后幂等读回；
- `status`、事件流、日志、Gate、清理与 Markdown/JSON Final Report；
- 固定六类 fixture、Native/AutoGo single/xgoal Standard 三组同口径 Benchmark Harness。

## 要求与构建

- macOS 或 Linux；
- Git；
- Go 1.25（`go.mod` 固定 `go1.25.13`，可使用标准 `GOTOOLCHAIN=auto`）；
- 至少一个已安装、登录且与所选模型兼容的 `codex` 或 `claude` CLI。

```bash
go build -o ./bin/xgoal ./cmd/xgoal
./bin/xgoal version
go test ./...
```

## 快速开始

以下示例在可信 Git 主工作目录执行；首次运行 Goal 前提交配置与已有修改，保持工作目录干净：

```bash
xgoal init
xgoal config validate --file xgoal.yaml
xgoal daemon start
xgoal daemon status
```

同一终端即可继续：

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

未指定 `--mode` 时使用 `xgoal.yaml` 的 `orchestration.defaultMode`。`standard` 要求独立 Reviewer；`fast` 仍必须经过 Scope、Patch、Validator、Promotion 与 Final Validation，不信任 Agent 自述。创建请求返回 `DRAFT` 和 `planning_state=QUEUED` 后，daemon 在后台规划。Planner 发现不能安全推断的关键语义时，未冻结 Goal 保持 `DRAFT`，以 `planning_state=WAITING` 和 Gate 说明原因。

常用控制命令：

```text
xgoal status <goal-id> [--watch]
xgoal logs <attempt-id>
xgoal gates <goal-id>
xgoal approve <gate-id> --version <n> --reason <text>
xgoal pause|resume|cancel <goal-id> --version <n>
xgoal work retry|cancel <work-id> --version <n> [--reason <text>]
xgoal goal plan <goal-id> --expected-version <n> --reason <text> [--proposal-file <proposal.json>]
xgoal goal replan <goal-id> --file <request.json>
xgoal report <goal-id>
xgoal clean [project-id] --dry-run
xgoal daemon status
xgoal daemon stop [--timeout 30s]
```

所有 command 和 subcommand 都提供 Cobra 标准帮助与参数说明：

```bash
xgoal --help
xgoal goal replan --help
```

可直接生成 Bash、Zsh、fish 或 PowerShell completion 脚本。例如当前 Zsh 会话可执行：

```bash
source <(xgoal completion zsh)
```

`run --wait` 持续读取 SQLite 权威状态，并在 Goal `Completed`、`Waiting`、`Cancelled` 时分别退出 0、3、4；不带 `--wait` 只表示 Goal 已被持久接收。

`daemon start` 在后台启动，等待身份握手与真实 readiness；重复启动复用当前实例。`daemon serve` 仍可前台运行。`daemon stop` 请求当前实例退出，等请求、执行和数据库关闭后才释放项目所有权。CLI 退出不会等同于 daemon 停止。

未冻结 Goal 可暂停、恢复或取消规划；`goal plan` 为规划失败创建新的 generation，可提交修正 Proposal。旧请求、失败和结果保留。`status` 给出的 `version` 用于控制命令的并发校验。修改 `xgoal.yaml` 后需重启 daemon 加载配置，再显式重试规划；运行中的 daemon 不自动热加载配置。主动 Probe 与所有 Agent 角色共享项目执行槽，忙时返回 `PROJECT_BUSY`。

项目入口统一使用 `--project`、`--state-dir`、`--socket`，优先级为显式参数、对应 `XGOAL_PROJECT`/`XGOAL_STATE_DIR`/`XGOAL_SOCKET` 环境变量、已绑定项目位置、默认值。例如 `xgoal --project /path/to/A daemon status`。项目绑定后不能通过另一个 state-dir 启动第二个实例；linked worktree 入口明确拒绝。`doctor` 默认可离线运行，不打开、创建或迁移 SQLite；主动 Probe 需要运行中的 daemon。

## 当前目录执行与恢复

xgoal 不创建或删除 Git worktree。Implementer、Reviewer 和 Validator 使用同一个主工作目录；工作区记录只是独立的会话元数据。每个项目只允许一个执行者，项目 A 与 B 的执行互不占用对方的执行槽。

首次运行要求原始文件 Tree、用户 index Tree 与 HEAD Tree 一致。xgoal 使用私有临时 index 和 `refs/xgoal/goals/<goal-id>/integration` 记录结果，保留用户 HEAD、分支和 index。完成后结果直接留在当前目录；后续 Goal 可以继承上一已完成 Goal 的精确验收 Tree，也可以在用户正常提交、工作目录干净后建立新基线。

失败或外部编辑时保留文件与诊断日志，不自动 stash、reset 或 clean。仅经过 Scope 检查并记录的同一 Work 失败现场允许 `work retry`；文件、HEAD、index 或配置再变化会拒绝重试。未处理现场不能直接 replan。晋升已更新私有 ref 而读回未完成时，先恢复提示要求的精确文件与 Git 元数据，再重启或 resume 以完成读回。

合法 `blocked` 会保留问题、建议与 Invocation 引用。先通过 `approve <gate-id> --version <version> --decision ALLOW --reason <answer>` 回答，再执行 `work retry <work-id> --version <version> --reason <reason>`；续作把已消费回答交给新 Attempt，仍检查原现场和权限。拒绝、撤销、未消费的过期 Gate、取消和未确认进程都阻止继续。`orchestration.autoRetryLimit` 缺省为 `0`；配置正数可授权安全现场的有限自动修复，总数按 Work 持久化，重复无进展会停止，不能自动回答 blocked 或批准 Gate。

旧 `workspace.provider: git-worktree` 配置提示 `CONFIG_MIGRATION_REQUIRED`；确认当前目录执行语义后改为 `current-directory` 并重启。旧 Goal、报告、Evidence 和 worktree 文件保留可读；未完成旧 Goal 不会自动转为原地执行，可取消旧 Goal 后从已审查的干净主目录创建新 Goal。`clean` 只处理允许删除的会话元数据，不删除当前源码或旧 worktree；服务诊断日志保留用于检查。

## 配置与安全

[xgoal.example.yaml](xgoal.example.yaml) 展示完整 v0.1 配置。Validator 的 `argv` 来自版本管理配置，Agent 不能注入命令或削弱 Required Validator。远端 push、发布、生产操作和项目 Secret 默认拒绝；Provider Transport 与 CLI 自有登录态是单独的受信执行通道。

Validator 的直接脚本和常见解释器脚本入口会从受信 Git 基线冻结内容和文件模式；入口相对 `cwd`，额外 `trustedFiles` 相对仓库根。Make/npm、包装命令（如 `env sh …`）、版本解释器（如 `python3.11`）、自定义 runner、解释器选项和内联 shell 需显式声明控制文件与依赖；自包含内联断言可写 `trustedFiles: [xgoal.yaml]`。这不自动发现所有传递依赖，也不冻结待开发的全部业务测试。候选修改受信文件会被阻止，包括未被当前 Work 选择的 Validator 依赖。需要更新时先保留现场、取消旧 Goal、审阅并提交新基线，再创建新 Goal；批准 Gate 或普通 replan 都不能重绑定旧验收权威。

升级前已经登记过旧脚本定义的基线，若与新增文件绑定产生身份冲突，会进入 `TRUST_BINDING_MIGRATION_REQUIRED`。保留旧 Goal/Evidence，在 `xgoal.yaml` 显式添加对应脚本的 `trustedFiles`，审阅并提交配置，再创建新 Goal。旧定义和注册仍可读取，不覆盖旧证据或自动把它升级为当前证据。

A、B 两个独立 Git 仓库分别拥有 daemon、状态库、锁和 socket，可同时运行；子目录和路径别名定位同一个实例。IPC 请求同时核对项目身份、协议和 daemon 实例，错误 socket 不会执行到另一个项目。CPU、内存、磁盘、端口、外部数据库/Docker、Provider 登录态与配额仍可能共享。

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
XGOAL_RUN_REAL_GOAL_SMOKE=1 go test ./internal/cli -run '^TestRealCodexCLIBackgroundGoalToFinalReport$' -count=1 -timeout=15m -v
```

最后一项使用临时固定仓库验证真实 Codex 的后台完整 Goal，Standard Review 使用同 Provider 的独立会话；它与 Codex/Claude 双向审查 smoke 分开记录。

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
