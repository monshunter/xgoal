# REVIEW-065：项目 Goal 列表 Change Review

审查对象：OBJ-007 / PLAN-018，产品 FR-090A / AC-GL-001–004、技术 Spec 第 23.4 节，以及 Goal 集合查询的存储、控制面、API、CLI、测试与 README。独立 Reviewer 依据根 AGENTS.md 第 7.2 节和 autogo-change-review 审查；被审实现只读。

基线：`169148e0a3d49dc698d7eb3a6dfb1eb676a2d5b6`，分支 `feat/goal-list`。最终生产与测试共 14 个 `internal/` 文件的 revision SHA-256：`ad0c655f8153833324775e18a4b99527081d599f42cb13620ad7db7168844626`（按路径排序，对 `路径 + NUL + 文件内容 + NUL` 累计 SHA-256）。同时复核 README、AGENTS.md 的命令事实更新、产品/技术 Spec 与 [真实验收记录](../operations/GOAL-LIST-ACCEPTANCE.md)。

## 当前 Verdict

**PASS**。原分页游标发现已修复；生产 Diff、定向回归、真实 CLI 多页/双项目场景与 demo5 当前状态和数据保留均通过独立复核。FR-090A / AC-GL-001–004 均有当前、强度匹配的 Evidence，无未关闭的 correctness、兼容或范围阻塞项。可进入 PLAN-018 的最终对账和原子提交；Review 不自行勾选 Progress 或替代 Git 收口。

## 发现与 Reconcile

### P2：游标不能限制完整 Goal ID 的可分页范围

原 `internal/store/sqlite/goal_list.go` 的 `GoalListQuery.Validate` 限制 `after` 为最多 256 字节且禁止控制字符，但 `internal/store/sqlite/planning.go` 的既有 Goal 创建校验允许更长 ID 和内部 tab。合法 ID 成为页尾时，下一页请求会被拒绝，无法满足完整遍历。直接 base64url 编码完整 ID 还会将长 ID 放大：`internal/daemon/daemon.go:141` 的请求头上限为 32 KiB，而 Goal 创建请求体允许 1 MiB。

修复方向：保留 ID keyset 排序，使用有界无状态定位游标；校验定位事实，失效时显式要求从首页刷新，避免删除或 VACUUM 后误跳。无需修改创建合同、提高 daemon 请求头上限或新增持久游标表。复核需覆盖长 ID、内部 tab、损坏/失效游标及真实 CLI 的长 ID 跨页。

**已修复 / 独立复核 PASS**：现实现使用规范 base64url 编码的 `rowid + 完整 ID SHA-256`，限制为 128 字节。定位读取先核对 ID 摘要，缺失或不符返回 INVALID_REQUEST；集合查询仍按原始完整 ID 排序和绑定下界。最多两次只读查询，没有持久游标状态。`TestGoalListCursorRoundTripsLegacyIDs` 覆盖约 36 KB、超过 32 KiB 且含 tab 的 ID，`TestGoalListRejectsStaleCursor` 覆盖不存在行和摘要变化。规范第 23.4 节同步说明失效刷新语义，未更改 Goal 创建合同或 daemon 请求头上限。

## 已审事实与验证

- 列表沿既有只读 API Query 路由进入存储，未调用单 Goal 的 Invocation refresh，也未进入 Effect、Event、幂等请求或 Provider 写入链路。
- 目标状态直接来自 goals；摘要优先活动 Revision contract，否则取当前 planning effect raw_goal。`planningState` 从现有详情实现提取，保持 QUEUED、WAITING、暂停、恢复、取消和历史执行模型的优先级，没有新增生命周期真理源。
- 摘要先调用现有脱敏再压缩空白并截取 160 个 Unicode code point，避免凭证跨截断边界漏检；human 继续清理终端控制字符。
- JSON 保持默认，human 通过既有 API 执行器的可选 renderer 输出；非法参数、帮助与补全在建立 client 前处理。`goal list` 无位置参数，未继承单 Goal ID 动态补全。已有 ids/status 路径未被替换。
- Reviewer 在紧凑游标修复后再次独立执行 `go test ./internal/cli ./internal/store/sqlite ./internal/control ./internal/api -run 'TestGoalList|TestIdentifier|TestCobraRead|Test.*Help|TestPlanning|TestAllSpecifiedEndpointsAreRouted' -count=1`，四包全部 PASS。覆盖 105 个 Goal 完整遍历、全部状态、摘要/规划投影、长 ID、失效游标、API 参数错误，以及既有 CLI/Planning 合同。
- 已审阅并独立运行 `GOMAXPROCS=2 go test -p=2 ./internal/cli -run '^TestRealCLIGoalList' -count=1 -v`，PASS（测试本体 11.09s）。`TestRealCLIGoalListPaginationIsolationAndReadOnly` 使用真实二进制、daemon、Unix socket 和 SQLite，按每页 1 条遍历 105 个 Goal（含 36,003 字节及 tab 的 ID），检查空项目隔离、JSON/human、默认 100 条、status 的 CANCELLED 退出码 4 与 ids 上限兼容，并对账持久记录数量和 Git/index/config；运行期间 Invocation 数量始终为 0。

## demo5 终验复核

独立检查 `/private/tmp/xgoal-goal-list-pp8v2qbn` 的 `snapshot.py`、`accept_demo5.py`、`result.json`、备份数据库、三份快照和原始 CLI stdout/stderr。三份快照结构和值完全相同；备份的 Goal 为 `goal_63dd18ee20824620553a2503` / COMPLETED / version 6，查询的 planning_state 为 SUCCEEDED、summary 为“编写一个贪吃蛇游戏”。list、COMPLETED 筛选、RUNNING 空筛选、human、status 和 ids 结果互相一致，相关 stderr 全为空。

Reviewer 随后独立执行当前安装二进制的 `xgoal --project tmp/demo5 goal list --format human`，再次观察相同 Goal/状态/版本/摘要；直接通过 SQLite `mode=ro` 重算八张业务表完整有序行 SHA-256，并重算十份用户文件/HEAD/index 摘要，实时结果仍与更新前的 `before.json` 完全相同。events 仍为 94，invocations 仍为 4，idempotency_records 仍为 3。安装二进制 SHA-256 为 `24fe859fec833ad5defd9ed32d497015bc857d59f4ce90f7d583c309812fdbcc`，与验收副本和运行记录一致。AC-GL-004 独立复核 **PASS**。

## 范围边界与下一路由

本次验证的是 Goal 发现/状态查询，不重跑 demo5 游戏的历史业务验收，也未运行 Provider Benchmark 或声明 Provider 成功率。分页明确不是跨请求冻结快照；维护导致定位失效时返回刷新错误。全仓回归按下节保留首次失败与后续分组重验事实，不把最初单次 `go test ./...` 记为成功。完成制品与 Git 对账后关闭 PLAN-018。


## 全仓回归首次执行与 Reconcile

主 Agent 的 `GOMAXPROCS=2 go test -p=2 ./...` 首轮未通过：所有非 CLI 包通过（其中 orchestrator 268.217s、SQLite 14.234s）；CLI 编译时仍捕获修正前的新增测试（将 CANCELLED status 退出 4 误当失败），并在包级默认 10 分钟预算耗尽时中止于刚开始 4 秒的既有 Unborn init 场景。当前源文件已修正退出码断言，并已由双方独立运行新增场景通过。项目 Makefile test 入口的包级预算是 15 分钟。

该次执行本身没有通过。后续使用当前测试源码、项目规定的 15 分钟包预算，对 CLI 全部测试作分组完整重验，保留逐测试日志；不更改生产逻辑或放宽业务断言。

Reviewer 已独立核对 `/private/tmp/xgoal-goal-list-pp8v2qbn` 中的 `run_cli_groups.py`、`cli-test-groups.json`、两份逐测试日志和 `cli-group-results.json`。当前源文件与当前测试二进制列出的顶层测试均为 67 项，两组为 34/33 项，并集完全相同，无遗漏或重复；每组使用锚定测试名的精确表达式、`GOMAXPROCS=2` 和 `-test.timeout=15m`。两个进程最终退出码均为 0，日志末行为 PASS。

| 当前 CLI 分组 | 顶层测试 | PASS | 显式 SKIP | FAIL |
| --- | --- | --- | --- | --- |
| 第 1 组 | 34 | 32 | 2 | 0 |
| 第 2 组 | 33 | 31 | 2 | 0 |
| 合计 | 67 | 63 | 4 | 0 |

四项 SKIP 均为 Codex/Claude 真实 Provider smoke，日志明确要求 `XGOAL_RUN_REAL_GOAL_SMOKE=1`；未运行项没有记为 PASS。两组日志 SHA-256 分别为 `c4c3cdfbad42e3305edd61bf037a3eb1f9f6003393941a0c23626f58304193b7`、`8d45b19f427aba1d8ef041b5106bf731728ee30ade5d6b9351b553c7914c765a`；测试二进制 SHA-256 为 `9fa08eef5225c33c4a3083ec0deb35d2b05f0859e776d8f7a6a04f1c1509e545`。受审生产/测试源码摘要仍为本 Review 开头的 `ad0c655f…8844626`。

非 CLI 结果保存在同目录的 `non-cli-results.txt`（SHA-256 `eb6664a87adb50ce3ee7485b9b81b69ed30be85b07b9caa9ca646cd816493163`）。该文件准确标为主 Agent 从当前会话 exec_command session 69049、chunks 963c25/5d744f 等工具输出逐行摘录的包级结果，并保留原命令退出 1；它不是完整原始日志或新执行。Reviewer 用当前 `go list ./...` 独立对账，50 个非 CLI 包集合完全一致，其中 46 个包为 `ok`、4 个为 `[no test files]`，无遗漏或重复。

**回归对账 PASS**：首轮非 CLI 包结果，加上当前 CLI 全部 67 项的分组重验，覆盖当前仓库 51 个包的回归范围，无遗漏；四项显式 opt-in Provider smoke 保持 SKIP。最初单次全仓命令仍记录为失败，不用本次分组结果改写历史。无需为了修复已失效的 CLI 断言再次重跑已经通过的非 CLI 包。

超时遗留资源的 `timeout-cleanup.json` 已复核：`/private/tmp/xgoal-unborn-cli-2653401518/project` 的状态为 STOPPED、ownership_held 为 false、属于该测试的剩余进程为 0；随后只移除了该测试创建的临时 fixture。Reviewer 的进程检查也没有发现该项目的 daemon，未把这些资源与 demo5 混淆。
