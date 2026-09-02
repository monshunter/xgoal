# REVIEW-028：PLAN-006 M5 Change Review

## 审查对象

- Plan：`PLAN-006`
- Design：`DESIGN-004`
- Revision：M5 当前工作树
- 范围：Reconcile、Policy/Gate、Budget、SQLite v4、Local API、Daemon、Recovery、CLI 与 M5 验收入口

## Verdict

`PASS_WITH_NOTES`

## 当前 Evidence

- `make verify-m5`：PASS；包含全仓测试、shuffle×20、全仓 race、vet、CLI smoke、SQLite Darwin/Linux 构建、M2 Linux 跨运行及 M3/M4 合同回归。
- 修订后 `make fmt-check m5-safety m5-cross-build && go vet ./...`：PASS。
- 修订后 `go test -race ./internal/api ./internal/control ./internal/app`：PASS。
- 真实编译二进制经私有 Unix Socket 完成 init/doctor/run/status/gates；第二 Daemon 被单写锁拒绝，重启后 Goal 从 SQLite v4 读回。
- `git diff --check`：PASS。

## 审查结论

- Failure Class 与技术 SPEC 枚举一致；Fingerprint 使用 canonical hash，并只归一化时间、随机 loopback 端口、临时路径、空白和重复行。Material Progress 只使用规范六字段。
- Reconcile 决策表在相同 Fingerprint + Strategy 且无实质进展时先返回 Diagnose，不能落入相同策略 Retry；Budget、Scope、冲突、Review 与内部不变量分别 fail closed。
- Provider Transport、Project Network 和 Credential 被不同 Policy Action 管理；Gate 授权消费精确绑定 Goal/Work/Attempt/Action/Scope/expiry/max uses，最后一次并发消费只有一个成功。
- Budget 使用整数最小单位和显式 `Known`，unknown 不会伪装为 0；soft/hard 预检、消费与实际 Observation 由 SQLite 事务和 Event 绑定。
- API 已注册技术 SPEC 主要 Endpoint，严格 JSON，写请求经过持久 Idempotency；同 key 不同请求返回 409，相同请求重放原响应。Goal status 投影 Work/Attempt/Lease/Workspace/Gate/Budget/Failure/Tree，并标明 Authority。
- Daemon 在恢复后才监听，持有进程级单写锁；run dir `0700`、Socket `0600`，Darwin/Linux peer UID 必须与当前 UID 一致。Worker 只有 PID/PGID/启动身份全匹配才被终止，所有权与状态随后事务收敛。
- `doctor` 只做本地 Passive Probe，展示 OS/Arch、Git、Agent CLI、Store、Provider Transport、Credential Status、Project Network 与 L0 边界，没有触发模型调用。

## Notes

- `run` 的成功仅表示 Goal 已以 `DRAFT` 持久接收；Planner/Plan 未冻结前不启动执行，这一状态在 CLI 与 Operation 中明确披露。
- Final Report Endpoint 在 M6 报告生成前返回 `REPORT_NOT_READY`；不以空报告或 HTTP 200 冒充最终验收。
- L0、peer UID、工具白名单与进程组管理不是容器级隔离；最终 Threat Model 必须保留这一残余风险。

## 结论

M5 门禁成立，`PLAN-006` 可关闭并提交；进入 M6 Final Report、Benchmark 与发布验收。
