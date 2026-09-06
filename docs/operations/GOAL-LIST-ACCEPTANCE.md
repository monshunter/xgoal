# 项目 Goal 列表真实验收

对应 [PLAN-018](../plans/PLAN-018.md)、产品 FR-090A / AC-GL-001–004。[REVIEW-065](../reviews/REVIEW-065-goal-list-change.md) 保存独立代码审查与工程回归结论。本记录拥有 demo5 运行与数据保留 Evidence。

## 环境与预检

2026-09-06，macOS arm64，代码基线 `169148e0a3d49dc698d7eb3a6dfb1eb676a2d5b6` 加 `feat/goal-list` 当前实现。目标目录 `/Users/tanzhangyu/Documents/my-opensources/xgoal/tmp/demo5`，项目 ID `project_bb8d18e971b374d243dec8f3bb481e92`。本次只验收列表查询，没有运行游戏或重做其历史业务验收。

旧 daemon 为 PID 25483 / instance `70ddacc33752373b7c0a80f58d5450af`，二进制 `/private/tmp/xgoal-ga-real-9b1egk55/xgoal-final`，可作为恢复入口。原 Goal 已为 COMPLETED version 6，用户生成源码仍为未跟踪文件；未创建/重跑/修改 Goal，未提交 demo5 用户源码。

先用 Python SQLite 只读连接制作一致备份 `/private/tmp/xgoal-goal-list-pp8v2qbn/demo5-backup.db`，并保存八张业务表的有序行 SHA-256、用户文件和 `.git/HEAD` / `.git/index` 原始字节 SHA-256；基线为 `before.json`。生产变更预验收 Review PASS 后，构建 `bin/xgoal`、安装 `/Users/tanzhangyu/go/bin/xgoal`，正常停止/启动该项目 daemon。若启动或查询失败，可由旧二进制重新启动同一状态目录；无 Schema 迁移，不需要覆盖数据库。

实际命令：

```sh
xgoal --project tmp/demo5 daemon stop --timeout 20s
xgoal --project tmp/demo5 daemon start --timeout 20s
xgoal --project tmp/demo5 goal list
xgoal --project tmp/demo5 goal list --format human
xgoal --project tmp/demo5 goal list --state COMPLETED --limit 1
xgoal --project tmp/demo5 goal list --state RUNNING
xgoal --project tmp/demo5 status goal_63dd18ee20824620553a2503
xgoal --project tmp/demo5 ids
```

## 观察结果

运行二进制 SHA-256 `24fe859fec833ad5defd9ed32d497015bc857d59f4ce90f7d583c309812fdbcc`；新 daemon PID 48184 / instance `f72c1f0c5c1a4be26cf1f30f81094155`，真实 Socket API readiness 为 READY。运行二进制与原始命令结果保留于 `/private/tmp/xgoal-goal-list-pp8v2qbn`。

| 字段 | 实际值 |
| --- | --- |
| goal_id | `goal_63dd18ee20824620553a2503` |
| state / version | COMPLETED / 6 |
| planning_state | SUCCEEDED |
| summary | 编写一个贪吃蛇游戏 |
| created_at | `2026-09-06T03:04:17.960347Z` |
| updated_at | `2026-09-06T05:16:54.706591Z` |

默认 JSON `items` 一条，`next_cursor` 为空；human 表格显示相同内容。COMPLETED 筛选返回同一页，RUNNING 筛选返回 `items: []`；所有 list 查询退出 0、stderr 为空。单 Goal status 的 ID/状态/版本与列表相同，既有 ids 候选仍可读取。升级前新 CLI 访问旧 daemon 明确返回 NOT_FOUND，未伪装为空列表。

## 数据保留与清理

分别在更新前、更新后、查询后读取快照：`before.json`、`after-restart.json`、`after-query.json` 全部结构和值一致。比较的是每张表完整有序行的摘要，不仅是行数。

| 业务表 | 行数 | 三次内容摘要 |
| --- | --- | --- |
| `goals` | 1 | 相同 |
| `goal_revisions` | 1 | 相同 |
| `plan_revisions` | 1 | 相同 |
| `work_items` | 1 | 相同 |
| `events` | 94 | 相同 |
| `effects` | 3 | 相同 |
| `invocations` | 4 | 相同 |
| `idempotency_records` | 3 | 相同 |

10 份文件（包括用户源码、配置和 HEAD/index）的摘要均一致；HEAD 仍为 `482749c3b542955c000c095836de728af7e3be2c`。未新增 Provider Invocation、事件或幂等写记录。备份和脚本位于私有临时目录，不进入 Git；demo5 daemon 保持运行，用户数据完整保留。

## 可复现自动场景

`TestRealCLIGoalListPaginationIsolationAndReadOnly` 使用真实编译 CLI、两个独立 daemon/Unix Socket 与 SQLite：105 个既有取消 Goal（包含超过 32 KiB 且含 tab 的 ID）按一条/页完整遍历，无遗漏/重复；默认第一页 100 条且给出有界游标；验证 human 下一页命令、项目与状态筛选隔离、ids 原有 100 条候选行为和 status CANCELLED 退出码 4；查询前后业务计数、HEAD/index/配置一致，Invocation 始终为 0。该场景自行停止两个测试 daemon 并清理临时项目，未使用 Provider fixture 或真实 Provider。

主 Agent 重跑测试 PASS（11.32s）；独立 Reviewer 重跑 PASS（11.09s）。真实用户命令路径与 demo5 原数据验收均为 **PASS**。跨页是当前状态读取，不是冻结快照；游标因数据库维护失效时按合同从首页刷新。


## 最终工程回归对账

非 CLI Go 包在 `GOMAXPROCS=2 go test -p=2 ./...` 首轮全部通过；首轮 CLI 使用了修正前测试快照且在默认 10 分钟包预算耗尽，因此该单次命令不计 PASS。使用当前测试源码编译二进制，按 `cli-test-groups.json` 对全部 67 个 CLI 顶层测试无遗漏/无重复分组，沿用项目 15 分钟预算：两组均退出 0，合计 63 PASS、4 个显式真实 Provider smoke SKIP。日志为 `cli-group-1.log`、`cli-group-2.log`，结果为 `cli-group-results.json`，均位于本记录的私有 Evidence 目录。首轮超时遗留的测试 daemon 经过项目身份核对后停止，临时 fixture 清理记录为 `timeout-cleanup.json`；demo5 未参与该清理。

四包定向 race（Goal list、Identifier、Planning 与 API routing）和全仓 `go vet` 均 PASS，gofmt 与 Diff 空白检查 PASS。独立 Change Review 绑定的 14 份生产/测试文件摘要 `ad0c655f8153833324775e18a4b99527081d599f42cb13620ad7db7168844626` 与回归后源码一致；完整覆盖由非 CLI 包结果与全部 CLI 分组结果共同成立，不以首次失败运行冒充通过。
