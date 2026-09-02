# REVIEW-010：技术 SPEC CLAIMED 恢复转换定向复审

## 审查对象

- 技术 SPEC：SHA-256 `81c53492d72a72842e76c3eb8d0ff85625da6bd70eeddf4ece6ad5ee04a073c8`
- 变更：Work Item 新增 `CLAIMED → RECONCILING` 恢复转换及其前置条件
- 关联 Plan：`PLAN-002` 2.3

## Verdict

`PASS`

## 发现

没有阻塞发现。

## 审查依据

- Lease 可能在 Attempt 启动前到期；若没有该转换，Work 会永久停留在 `CLAIMED`，与启动恢复矩阵和确定性恢复目标矛盾。
- 新转换只覆盖“启动前失败”或“已通过外部读回确认旧 Worker 不再写入”，没有把 TTL 到期错误等同于安全重试。
- 恢复仍先进入 `RECONCILING`，再由既有 `RECONCILING → READY/WAITING` 决策，不绕过 Gate、Budget、No-progress 或新 Attempt 边界。
- `COMPLETED`、`CANCELLED` 终态不变；Goal、Attempt、Effect、Lease 的既有状态合同不受影响。
- 产品 Feature、验收映射、Plan 范围与顺序未改变。

## 下一路由

按 `autogo-tdd` 继续 `PLAN-002` 2.3，测试必须证明单凭过期不能释放 Lease，只有确认旧 Worker 停止后才能进入 Reconcile 和递增 Generation。
