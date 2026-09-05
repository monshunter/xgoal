# M5 Reconcile、Gate 与 Daemon 验收记录

> 历史版本验收记录。OBJ-003 项目单实例、当前目录执行与持久后台合同的当前证据见[增量验收](PROJECT-DAEMON-CURRENT-DIRECTORY-ACCEPTANCE.md)。

## 范围

本记录对应 `PLAN-006` 与 `DESIGN-004`，只证明 M5 确定性控制面、有限授权、Local API、Daemon 和恢复能力；M4 已完成的真实 Codex/Claude 双向 Agent/Review 不在本记录重复调用，最终报告与 Benchmark 留给 M6。

## 当前运行事实

- 分支：`feat/xgoal-v0.1`。
- Go：`go.mod` 固定 Go 1.25 / toolchain 1.25.13。
- SQLite：运行时 `3.53.3`，WAL、foreign keys、`synchronous=FULL`、busy timeout 5000 ms；当前迁移会删除早期草案曾创建、但已移出产品边界的计量表。
- 本机：darwin/arm64；Passive Doctor 读取到 Git `2.39.2`、Codex CLI `0.145.0`、Claude Code `2.1.235`。
- Doctor 没有发起模型回合；Provider Transport、Credential Status、Project Network 与 L0 隔离分别展示。

## 可复现门禁

```bash
make m5-safety
make m5-cross-build
go test -race ./internal/reconcile ./internal/policy ./internal/store/sqlite ./internal/api ./internal/control ./internal/daemon ./internal/recovery ./internal/app
```

完整回归入口：

```bash
make verify-m5
```

`verify-m5` 不重跑 M3/M4 真实模型 smoke；它仍会运行两代 Adapter/Review 合同测试。

## 故障与安全矩阵

| 场景 | 当前 Evidence | 结果 |
|---|---|---|
| 时间、临时路径、随机 loopback 端口变化 | Reconcile 单测 | Fingerprint 稳定；退出码等语义变化会改变 Hash |
| 同 Fingerprint + Strategy 且无 Material Progress | 决策表单测与 SQLite 重启读回 | 不返回 `RETRY_NEW_ATTEMPT`，转 Diagnose/Switch/Replan/Gate |
| Gate Scope/Goal/Work/Attempt/Action 不匹配 | Gate Store 单测 | `ErrAuthorizationDenied`，不增加使用次数 |
| Gate 最后一次授权并发消费 | 8 个并发消费者 | 只有一个成功，`used == max_uses` |
| Gate 过期 | Fake Clock 边界测试 | 原子持久化 `EXPIRED` 和 Event，再向调用方返回 expired |
| 旧计量 Schema | Store 迁移测试 | 最新 Schema 不保留早期草案的计量表，重启与迁移历史校验仍成立 |
| API 写请求无 key/相同 key 重放 | Handler 与真实 Unix API 集成测试 | 缺 key 为 400；同请求只写一次并重放原响应 |
| Event Stream 断线续传 | NDJSON 单测 | 从最后完整 Event ID 后返回，不重复前一 Event |
| 双 Daemon 竞争 | 文件锁集成测试与真实第二进程 | 第二实例 fail closed：`xgoal daemon is already running` |
| Socket 权限/peer UID | Daemon 集成测试 | run dir `0700`、Socket `0600`，Darwin/Linux peer credential 必须匹配当前 UID |
| Daemon 重启后状态读回 | 真实 Unix CLI smoke | `goal_m5_smoke` 在重启后仍以同版本/状态读回 |
| Worker 存活且身份匹配 | Recovery 单测 | 只终止匹配的 PGID，Attempt/Lease/Work 原子转 Interrupted/Revoked/Reconcile |
| PID 复用、身份变化或进程消失 | Recovery 单测 | 不发送信号，标记 Lost 并隔离旧 Worker 所有权 |

## 真实 Unix Socket CLI smoke

验收使用临时私有状态目录启动编译后的 `xgoal daemon serve`，再依次执行 `xgoal init`、`doctor`、`run --goal-file ... --id goal_m5_smoke`、`status`、`gates`。第二个 Daemon 被单写锁拒绝；停止后重启 Daemon，`status goal_m5_smoke` 从 SQLite 读回相同 `DRAFT/version=1`。

该 smoke 证明“请求已持久接收与重启可读”，不把 `DRAFT` 误报为 Goal 已运行或完成。Goal 的最终闭环必须继续满足 Work、Validator、Finding、Gate、Policy、Final Evidence 和 Final Report 的 Completion Predicate。

## 已知边界

- v0.1 使用 L0 本地进程隔离；不能把 peer UID、工具白名单或进程组恢复描述为容器级安全边界。
- `run` 接收 Goal 后保持 `DRAFT`，直到 Planner/Plan 被持久冻结并激活；接收成功不是完成 Evidence。
- Final Report endpoint 在 M6 生成最终报告前明确返回 `REPORT_NOT_READY`。
