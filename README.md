# xgoal

`xgoal` 是一个用 Go 实现的本地 Coding Agent 编排器。用户给出自然语言目标后，Planner 生成冻结的 Goal Contract 与 Work Graph；Kernel 以 SQLite 为唯一运行状态，串行调度 Codex CLI / Claude Code CLI，在当前 Git 主工作目录中实现、捕获 Patch、执行受信 Validator、独立 Review，并把验收结果记录到私有审计 Commit/Ref，最后在当前 Tree 上复验并生成可追溯报告。

核心原则是：Agent 的完成声明只是 Claim，只有 Git/文件事实、受信命令 Receipt、当前 Evidence 与必要的人类决策能够推进完成状态。产品合同见 [产品 SPEC](xgoal-product-spec-v0.1.md)，实现设计见 [技术 SPEC](xgoal-technical-spec-v0.1.md)。

## v0.1 能力

- 单一 Go CLI/Daemon，Unix Socket HTTP/JSON API，每项目私有 SQLite/WAL 状态；
- Goal 与规划请求事务性接受，后台规划、结果原子发布、进程归属登记与重启恢复；
- 自然语言、文件或 stdin Goal，严格 Planner Schema、冻结 Revision、DAG 与 Scope/Validator 校验；
- Codex 与 Claude 的 Probe、Plan、Start、Wait、Cancel、Resume、可选 Accept 和结构化输出适配；
- 同项目串行使用当前工作目录，以私有 index 捕获完整 tracked/untracked/binary/rename/mode/symlink/delete Patch；
- `scope`、`command`、`file_assertion`、`runtime_probe`、`git_assertion` 五类受信 Validator、Command Receipt 与 Evidence；
- Standard 独立 Reviewer Session、Finding、有限 Human Gate 与无进展 Reconcile；
- 配置驱动的 bootstrap、服务依赖/readiness/逆序回收，场景与业务断言关联及不可变制品；
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
make test
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
xgoal status <goal-id> [--watch] [--format json|human]
xgoal logs <attempt-id>
xgoal ids [prefix] [--kind goal|work|gate|invocation]
xgoal work get <work-id>
xgoal gate get <gate-id>
xgoal gate resume <gate-id> --version <approved-gate-version> --owner-version <n>
xgoal invocations <goal-id> [--role planner|implementer|reviewer|acceptance]
xgoal context <invocation-id>
xgoal logs --invocation <invocation-id> [--stream stdout|stderr] [--after <sequence>] [--follow]
xgoal logs --goal <goal-id> [--role <role>] [--follow]
xgoal gates <goal-id>
xgoal approve <gate-id> --version <n> --reason <text>
xgoal approve <gate-id> --version <n> --reason <answer> --resume --owner-version <n>
xgoal pause|resume|cancel <goal-id> --version <n>
xgoal work retry|cancel <work-id> --version <n> [--reason <text>]
xgoal goal plan <goal-id> --expected-version <n> --reason <text> [--proposal-file <proposal.json>]
xgoal goal replan <goal-id> --file <request.json>
xgoal report <goal-id>
xgoal export <goal-id> --output <new-directory-outside-project>
xgoal clean [project-id] --dry-run
xgoal daemon status
xgoal daemon stop [--timeout 30s]
```

`invocations` 列出四个角色的调用；`context` 展示注册输入、有效配置和可验证的公开结果，未观察到的实际模型或负载明确为 unknown。日志的续读游标由 Invocation ID、stream 和 sequence 共同确定；`--goal` 只选择当前最新的一次调用，跟随过程中不会自动切换。Ctrl-C 停止读日志，Goal 继续运行；文件丢失、序号缺口或未收到完整终止帧会明确报错。公开输出不包含私有推理。

所有 command 和 subcommand 都提供 Cobra 标准帮助与参数说明：

```bash
xgoal --help
xgoal goal replan --help
```

Goal、Work、Gate 和 Invocation 接受唯一前缀，精确 ID 优先；歧义返回候选，不记录新的写请求。`ids`、`work get`、`gate get` 提供当前版本，修改仍要求显式版本 CAS。动态补全查询当前项目的 ID 和版本；daemon 不可用时安静返回空候选，不自动启动或初始化项目。

`approve --resume` 先保存 ALLOW 答案，再按 Gate 的归属继续：Planner 新建规划 generation，Work 重试当前失败，最终 Acceptance 新建验收会话。`--owner-version` 对 Planner/最终验收使用 `status` 中的 Goal version，对 Work 使用 `work get` 中的 version。若版本冲突、文件变化或进程未退出，第二步失败但答案保留；检查现场及当前版本后使用 `gate resume`，无需再次批准。权限、信任更新或未知类型的 Gate 仍使用其专用操作。前缀在持续观察的第一帧解析后固定为完整 ID，不会因新增相似 ID 切换目标。

可直接生成 Bash、Zsh、fish 或 PowerShell completion 脚本。例如当前 Zsh 会话可执行：

```bash
source <(xgoal completion zsh)
```

`status --format human` 展示 Goal、Work、当前 Invocation、Gate 版本和下一条读取命令；`--watch` 持续观察，Ctrl-C 只停止观察。`run --wait --format human` 将等待反馈写入 stderr，stdout 仍为接收与终态 JSON。心跳时间仅代表租约信号，最近输出与受信进展单独显示；没有已知事实时为 unknown。重复同一验证结果或只改变采集 ID/时间的环境快照不刷新进展时间。

`run --wait` 持续读取 SQLite 权威状态，并在 Goal `Completed`、`Waiting`、`Cancelled` 时分别退出 0、3、4；不带 `--wait` 只表示 Goal 已被持久接收。

`daemon start` 在后台启动，等待身份握手与真实 readiness；重复启动复用当前实例。`daemon serve` 仍可前台运行。`daemon stop` 请求当前实例退出，等请求、执行和数据库关闭后才释放项目所有权。CLI 退出不会等同于 daemon 停止。

未冻结 Goal 可暂停、恢复或取消规划；`goal plan` 为规划失败创建新的 generation，可提交修正 Proposal。旧请求、失败和结果保留。`status` 给出的 `version` 用于控制命令的并发校验。修改 `xgoal.yaml` 后需重启 daemon 加载配置，再显式重试规划；运行中的 daemon 不自动热加载配置。主动 Probe 与所有 Agent 角色共享项目执行槽，忙时返回 `PROJECT_BUSY`。

项目入口统一使用 `--project`、`--state-dir`、`--socket`，优先级为显式参数、对应 `XGOAL_PROJECT`/`XGOAL_STATE_DIR`/`XGOAL_SOCKET` 环境变量、已绑定项目位置、默认值。例如 `xgoal --project /path/to/A daemon status`。项目绑定后不能通过另一个 state-dir 启动第二个实例；linked worktree 入口明确拒绝。`doctor` 默认可离线运行，不打开、创建或迁移 SQLite；主动 Probe 需要运行中的 daemon。

`init` 和离线/在线 `doctor` 的 `validation_preparation` 会检查 Go、Node package test、Cargo、pytest 和 Make test 入口，展示命令是否可用、是否已配置为 Validator，以及准备步骤。检测不会执行项目代码或安装依赖；`coverage: not_verified` 明确表示尚未证明行为覆盖。没有发现入口时返回 `status: unknown`；默认 `git-diff-check` 只检查空白格式，不能替代业务断言。Node、Cargo、pytest 和 Make 的发现结果仅作建议，需审阅测试、声明可信入口并提交配置后才能用于 Goal。

运行中的 daemon 可导出本地审计快照：

```bash
xgoal export <goal-id-or-unique-prefix> --output /absolute/path/outside-project/new-audit
```

输出包含整个项目的 SQLite 一致快照、所选 Goal 的 JSON 视图、所有 Goal 的已封存文件引用，以及最后发布的 `manifest.json`。清单记录数据库事件边界、每个 Invocation 的日志游标、文件校验和及排除范围。导出使用独立只读连接，不占用控制数据库连接；期间仍可查看或取消 Goal。`clean` 等待导出完成，等待可取消。输出目录必须不存在且位于项目和状态目录外；已有目录不会被覆盖。

导出包含工作 Packet、Patch 对象、Receipt/日志、Review、场景证据、报告和登记的迁移备份；Provider 日志只保留冻结游标内的公开脱敏内容。正在改名的报告从快照内已校验的 blob 生成，并保留 `PENDING_RENAME` 标记；已发布文件损坏不能用 blob 掩盖。预检尚未创建的 Planner Packet、用户提供 Proposal 的无调用路径、已清理 marker 和旧未索引日志各自明确标注。缺失必需文件、校验失败或取消会保留私有 `.incomplete-*` 目录供检查，并返回失败；只有成功发布的目标目录可视为完整导出。总时限 5 分钟、总文件数据 2 GiB、最多 100,000 个文件/引用；超限明确失败。

这是审计数据包：不包含源码仓库、Git 对象、可写环境数据或宿主原生会话库，也不支持导入后继续执行。导出副本的编辑不会改变正在运行的 SQLite 权威状态。

## Agent Profile 与项目规则

`agents[]` 的可选 `model`、`reasoningEffort` 用于同一个 Provider 的可复用执行配置；`orchestration.roleProfiles` 将 `planner`、`implementer`、`reviewer` 及可选 `acceptance` 绑定到 Profile ID。未绑定时保留默认选择，`doctor` 的 `role_selections` 显示选择来源，`effective_roles` 显示实际传递的配置与 CLI 版本。例如：

```yaml
orchestration:
  roleProfiles: {planner: planning, implementer: coding, reviewer: checking}
```

绑定的 Profile 必须存在并声明支持对应角色。模型未填写时继承原生配置，不把配置推断当作实际模型观测。Codex effort 支持本 Adapter 合同的 `minimal/low/medium/high/xhigh`，Claude 支持 `low/medium/high/xhigh/max`；具体模型组合由原生 CLI 判断，可用 `doctor --active --profile <id> --timeout 3m` 提前验证，普通 doctor 不调用模型。

Codex 使用无人值守 `never` 与角色 sandbox；同一 Profile 同时用于只读和实现角色时省略 `sandbox`，或拆分 Profile。Claude 使用 `dontAsk`；Planner/Reviewer 仅允许 Read/Glob/Grep，Implementer 默认再允许 Edit/Write。以前被忽略的冲突权限或工具配置现在会报错，请按提示迁移。原生 plan/auto/交互模式不会替代 xgoal 的 Fast/Standard 流程。

每次调用保存有效配置与输入引用。兼容 resume 要求显式 model/effort、相同输入和当前 CLI 版本；继承值不明、旧会话缺少身份或配置发生变化时使用新会话。用户回答后的 Work retry 仍是带历史问题与回答的新 Attempt。

`project.harness: {type: autogo, required: true}` 要求所选 Provider 有项目局部入口、兼容安装清单及其声明的知识文件。Codex 使用 `AGENTS.md`、`.agents/skills/` 与 `.autogo/manifests/codex.json`；Claude 使用 `CLAUDE.md`、`.claude/skills/` 与对应 `claude.json`。请在创建 Goal 前准备并提交所需文件；缺少必需能力时启动前停止。未要求 Harness 的项目可以继续使用。

xgoal 将文件路径与哈希放入 Packet，并通过原生指令参数传递委派职责：Kernel 管理状态、Gate、Git、环境与完成判定，Agent 执行当前角色并遵守业务规则。检查不安装或覆盖项目规则。`found/compatible` 表示本地发现与清单兼容；`load_observation: unknown` 表示没有把路径提供当作模型已加载的证据，实际可观测行为保留在 Provider 事件中。

## 环境、场景与可选验收会话

Kernel 根据 Validator/Scenario 的 `services` 引用准备环境，按 `dependsOn` 启动服务并执行 readiness；最后按相反顺序停止，确认退出后才允许完成。`bootstrap.commands` 用于受信的环境准备。项目命令使用自己的环境白名单，不继承 Agent Profile 的登录环境；Kernel 提供私有 `XGOAL_SCENARIO_DIR`、`XGOAL_ENVIRONMENT_ID` 和临时目录。服务端点与测试数据可写入该场景目录，源码与受信客户端必须保持不变。

以下片段适用于已有启动、探测和业务断言脚本的项目；脚本和配置需先审阅提交，启动命令不可假定 readiness。访问本地服务需要明确配置 `runtime.projectNetwork: allow` 与对应服务的 `network: allow`；`require-gate` 会在执行前停止，需先解决网络授权和配置。

```yaml
services:
  - id: api
    argv: [python3, app.py]
    network: allow
    readiness:
      argv: [python3, scripts/api-check.py, ready]
      timeout: 20s
      interval: 200ms
    stopGracePeriod: 3s
validators:
  - id: api-business
    description: 创建记录后重新查询，并断言字段与状态正确。
    type: command
    phases: [change, final]
    services: [api]
    argv: [python3, scripts/api-check.py, assert]
    timeout: 30s
    required: true
scenarios:
  - id: record-workflow
    description: 创建并查询一条测试记录
    steps: [创建记录, 查询结果并保存 response.json]
    services: [api]
    validators: [api-business]
    artifactPaths: [response.json]
```

`scenarios[].validators` 定义确定性覆盖；Planner 会获得场景步骤和 Validator 的 `description`，没有说明的覆盖标为 `unspecified`。场景制品路径相对私有场景目录，必须是明确的常规文件路径。最终验证后，Kernel 将文件连同哈希、环境与 Receipt 身份封存；声明的文件缺失、链接、越界、超限或损坏都会阻止完成。

通常由这些受信命令即可完成验收。确需 Agent 阅读反馈再继续操作时，添加支持 `acceptance` 的独立 Profile，并配置：

```yaml
acceptance:
  scenarioIDs: [record-workflow]
  trustedFiles: [scripts/api-check.py]
  replaySafe: false
orchestration:
  roleProfiles: {acceptance: scenario-checker}
```

`scenario-checker` 声明 `roles: [acceptance]`。Claude 使用 `dontAsk` 和明确的工具，例如 `allowedTools: [Read, Glob, Grep, "Bash(./scripts/api-client.sh *)"]`；脚本入口需有执行位，其依赖列入 `acceptance.trustedFiles`。若 Profile 为 CLI 登录继承了凭证环境，客户端包装脚本应只转发测试所需变量。Codex 使用只读 sandbox，不能配置 `allowedTools`，当前只支持无需服务交互的源码/制品观察场景；服务场景配置会提前拒绝。

Acceptance 会话使用独立 Packet/Invocation，只返回 Claim；之后仍执行受信业务断言。blocked/failed 或不可自动重放的中断进入 `owner: final` Gate，停止服务并保留问题、公开事件与诊断。先用 `approve ... --reason <answer>` 回答，再用 `resume <goal-id> --version <n>` 继续；新 Invocation 在同一 Revision/Tree 上原子消费该决定并重新准备环境。`replaySafe: true` 只允许没有完整结果、已确认进程退出的中断作有界恢复，次数受 `noProgressLimit` 约束（缺省或零时为 3，最多 100 次）；不能忽略 blocked/failed、取消或未知进程。

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

完整门禁使用 `go test -race -count=1 ./...` 完整执行一次全部测试，包含实际 CLI/daemon/服务故障场景；不再先跑普通全仓、再重复运行历史 M2–M6 聚合测试。`make test` 保留独立的普通单轮入口。`make shuffle` 只对短状态/协议合同及准确列出的 SQLite CAS、Lease、Effect、Gate、Invocation 用例重复20次；所选用例缺失会直接失败。真实模型 smoke 继续独立显式启用。

用例应在秒到分钟级完成，超时/租约状态优先使用虚拟时钟，真实进程场景使用有界条件等待。普通测试15分钟、race 20分钟的超时是一个包中全部用例的累计保护，短 shuffle 集合的单包总预算为2分钟；没有小时级门禁预算。实际耗时随机器负载变化，慢用例应按运行记录定位，不能持续放宽总超时掩盖问题。

Makefile 默认串行运行门禁，即使传入 `make -j`；Go 默认同时运行一个包，每个 Go 进程使用 `GOMAXPROCS=2`，包内 `t.Parallel` 默认也为2，嵌套 Go 构建继承限制。可用 `GOMAXPROCS=4 make verify-m6 GO_PACKAGE_PARALLEL=2` 显式提高并发；原有 `GOFLAGS` 中的其他选项保留。这是并发限制，不是主机 CPU 硬配额，服务、Git 和其他应用仍可能占用资源。主机繁忙时先确认进程归属，停止当前验收并正常回收其测试 daemon，再降低并发重跑。

真实 Provider smoke 必须显式运行，不包含在 `verify-m6` 中：

```bash
make m3-real-smoke
make m4-real-smoke
XGOAL_RUN_REAL_GOAL_SMOKE=1 go test ./internal/cli -run '^TestRealCodexCLIBackgroundGoalToFinalReport$' -count=1 -timeout=15m -v
XGOAL_RUN_REAL_GOAL_SMOKE=1 go test ./internal/cli -run '^TestRealCodexCLIAcceptanceReadOnlyGoalToFinalReport$' -count=1 -timeout=15m -v
XGOAL_RUN_REAL_GOAL_SMOKE=1 go test ./internal/cli -run '^TestRealClaudeCLIAcceptanceServiceGoalToFinalReport$' -count=1 -timeout=15m -v
```

这些完整 Goal 使用临时固定仓库，Standard Review 使用同 Provider 的独立会话，与 Codex/Claude 双向审查 smoke 分开记录。后两项分别验证 Codex 只读 Acceptance 和 Claude 实际服务交互，同时检查四角色的运行中上下文、公开输出、最终 Evidence 与导出。测试明确请求的模型和 Profile 见对应测试；所需 CLI/模型及登录状态必须可用，不自动安装或修改用户全局配置。

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
