# DESIGN-004：M5 确定性控制面与单写 Daemon

## 1. 目标与不变量

M5 把 M1–M4 的持久状态和执行边界接入一个可恢复的本地控制面。设计必须保持以下不变量：

1. 同一 Failure Fingerprint 且没有 Material Progress 时，相同 Strategy 不能再返回 `RETRY`。
2. Policy 默认拒绝高风险动作；Human Gate 只生成绑定 Goal/Work/Attempt、动作、Scope、有效期和次数的有限授权。
3. 超时、输出字节上限、进程取消与 no-progress 判定只用于防止本地执行失控，不形成模型用量账本。
4. Daemon 是 SQLite 与运行状态的唯一写者；CLI 只通过当前用户可访问的 `0600` Unix Socket 调用它。
5. 写请求幂等；事件流可由稳定全局 Event ID 断点续传；崩溃恢复先读持久事实，再处理存活进程和未完成 Effect。

## 2. 组件与 owner

```text
CLI ── HTTP/JSON over 0600 Unix Socket ── API
                                             │
                    ┌────────────────────────┼────────────────────┐
                    ▼                        ▼                    ▼
             Control Service          Event Stream          Doctor/Reads
                    │
          ┌─────────┼──────────────────────────┐
          ▼         ▼                          ▼
      Reconcile   Policy                    Recovery
          └─────────┴──────────────────────────┘
                            │
                         SQLite
```

- `internal/reconcile`：纯函数 Failure 分类、归一化、Fingerprint、Progress 比较与决策；不写数据库。
- `internal/policy`：角色默认策略和一次请求所需的授权判定；不持有 Gate 状态。
- `internal/store/sqlite`：Gate 授权消费、Failure/Decision、Worker 和全局 Event 游标的唯一持久 owner。
- `internal/api`：版本化 Endpoint、幂等写协议、结构化错误和 NDJSON；业务状态转换委托给 Service/Store。
- `internal/daemon`：Socket/lock 生命周期、单写保证和启动恢复顺序。
- `internal/cli`：参数、显示和退出码；不得直接打开状态数据库。

## 3. Reconcile 合同

`Failure` 使用技术 SPEC 的稳定枚举，输入同时携带 primary error、validator definition hash、base/result tree、goal revision hash 与 relevant config hash。错误归一化只去除 RFC3339/Unix 时间、随机端口、已知临时根路径和空白/重复日志噪声；不删除退出码、文件名、错误类型等语义字段。Fingerprint 对规范化结构做 canonical JSON + SHA-256。

`Snapshot` 与技术 SPEC 的六个字段一一对应。`MaterialProgress(prev,curr)` 是纯函数；新增解释、重复 Patch 或相同失败输出不会改变 Snapshot。

决策输入包含 Failure、当前/前一 Snapshot、相同 Fingerprint + Strategy 的历史次数和 Policy 状态。输出只允许 `RETRY_NEW_ATTEMPT`、`DIAGNOSE`、`FIX_WORK_ITEM`、`SWITCH_STRATEGY`、`REPLAN`、`WAIT_GATE`、`QUARANTINE`、`STOP_INVARIANT`。重复指纹且无实质进展时优先选择非重试动作；Replan 保存旧 Revision 与影响分析，改变 Goal Contract 时转 Gate。

## 4. Policy、Gate 与有限授权

角色默认矩阵由代码常量实现，Provider Transport 与 Project/Tool Network 分开。受信 Profile 可获 `CONNECT_PROVIDER`，但不能派生 `ACCESS_PROJECT_NETWORK`；显式 Provider Key 仍视为 `USE_PROVIDER_CREDENTIAL`，必须由有限 Gate 授权并只注入 Agent 顶层进程。

已有 `gates` 表继续作为问题与决定的 owner。新增事务操作 `ConsumeAuthorization`：

1. 精确匹配 Gate ID、Goal/Work/Attempt、Action 和请求 Scope；请求 Scope 必须是授权 Scope 的子集。
2. 要求状态 `APPROVED`、当前时刻早于 expiry、`used < max_uses`，且未撤销。
3. CAS 增加 `used` 和 `version`，同事务追加 Event；并发只有一个调用可消费最后一次授权。
4. 过期/撤销会持久转换状态；一次授权不产生全局配置变化。

## 5. 持久化与事件游标

迁移新增：

- `failure_records`：规范输入、fingerprint、strategy、progress hash 和重复次数；
- `reconcile_decisions`：输入绑定、动作、理由与 plan revision；
- `worker_processes`：Attempt、PID/PGID、进程启动身份、状态和版本。

Event 表仍是审计真理源。全局流按 `(created_at,id)` 有序，但断点使用唯一 Event ID：查询先解析该 ID 的 tuple，再返回严格大于它的事件，避免只按时间丢失同时间事件。API 响应携带最新 ID。

## 6. Local API 与幂等

API 实现技术 SPEC 23.2 的全部 Endpoint。每个写请求必须携带合法 `Idempotency-Key`；Service 通过已有 `idempotency_records` 绑定 route scope、canonical request hash 和完整 JSON response。相同 key + 相同请求返回原响应；相同 key + 不同请求返回冲突。

JSON decoder 拒绝未知字段和尾随值；响应统一包含稳定错误 code。`GET /v1/goals/{id}/events?after_event_id=...&watch=1` 使用 `application/x-ndjson`，先补历史，再等待通知/心跳；客户端重连从最后完整 Event ID 继续。状态只投影数据库 Fact/Decision/Inference/Claim 权威，不生成 LLM 摘要。

Socket 创建前只移除已确认的 stale socket；父目录 `0700`、socket `0600`。服务记录启动 UID，Unix peer credential 可用的平台校验 peer UID；不支持的平台仅依赖 socket 文件权限并在 doctor 明示降级。

## 7. Daemon 与恢复顺序

Daemon 对状态目录获取非阻塞文件锁，第二实例直接失败。启动顺序固定为：迁移/完整性检查 → 未完成幂等/Effect 对账 → Lease/Worker 恢复 → Socket listen → 接受请求。

Worker 记录 PID、PGID 和进程启动身份，避免 PID 复用误杀。重启时：身份和 Attempt 匹配则进入受限观察；无法安全重绑定的进程组先 TERM、超时后 KILL，再把 Attempt 标为 Interrupted 并进入 Reconcile。旧 Lease Generation 或迟到输出只能进入 Quarantine，不能推进状态。关闭时停止新调度、关闭 listener，并按同一进程组策略回收子进程。

## 8. CLI 与退出码

CLI 覆盖产品 SPEC 11.1 及技术 SPEC Endpoint：`init/doctor/run/status/logs/gates/approve/pause/resume/cancel/report/clean` 和 `goal/work/agent/env/evidence` 查询。命令只组装 API 请求。退出码严格使用技术 SPEC 的 `0/2/3/4/5/6/7`；提交请求成功不等于 Goal Completed。

## 9. 验证与恢复

- 领域/属性测试：归一化稳定、Fingerprint 对语义变化敏感、Material Progress 和决策表。
- Store 并发测试：Gate 最后一次授权、全局 Event 游标、重启读回。
- API 测试：`0600`、UID、未知字段、缺少/冲突幂等键、全部 Endpoint、NDJSON 断线续传。
- 故障注入：DB commit 前后、启动 Worker 前后、Daemon 运行中退出、stale socket、双 Daemon、迟到 Worker。
- 恢复不删除项目数据；只回收由匹配进程身份证明属于当前 xgoal 的 Worker。

## 10. 非目标

M5 不生成最终验收报告或发布 Benchmark，不实现远端 API/多用户认证，不把 v0.1 的 push/生产操作从默认 Deny 改为 Allow，也不声称 L0 进程边界等同容器级安全隔离。
