# REVIEW-040：DESIGN-004 项目隔离与持久后台执行 Design Review

## 审查对象

- Objective / Plan：`OBJ-003` / `PLAN-009` Item 1.2；实现和最终验收同时由 `PLAN-010` 承接。
- Design：`DESIGN-004-m5-control-daemon`，OBJ-003 修订，第 6–12 节及其继承不变量。
- Design SHA-256：`502ace9b68eb6aac1563d55d83b8df8186091269dc0ce3004e792b7d77f93d52`。
- 上游规范：产品 FR-100–108、AC-ISO/AC-BG，技术 SPEC 第 5、9.1、10.1、22.4、23 节；以 [REVIEW-039](REVIEW-039-spec-project-background.md) 绑定的当前内容为准，Spec Review 已 `PASS`。
- 代码事实基线：`24960390d71237d6266e2f995b4e3df0b6719a78`。本次设计尚未实现，运行正确性留待 Change Review 和闭环验收证明。

## Verdict

`PASS`

设计满足通过审查的规范，关键状态和副作用均有 owner，迁移、失败及恢复路径完整到可以进入实现；没有未解决的阻塞发现。

## 审查结论

- `internal/project` 统一只读定位与项目身份，`internal/app` 持有资源句柄并监督生命周期，`internal/daemon` 负责传输和后台入口，SQLite 继续拥有运行事实。Git Common config 的单个原子 state-binding 值只保存定位，DB ProjectBinding 只验证状态归属，没有新增 Goal 真理源。未初始化身份的只读派生、锁内持久化及已有 ID 不覆盖规则，使无写入入口与初始化可以共存。
- 仓库锁先于状态锁，二者覆盖 preflight、迁移、恢复、执行和 DB 关闭；沿用旧状态锁路径以保护升级。DB/配置绑定拒绝冲突，旧库只在可证明归属或明确可信默认位置采用，并保留多历史库冲突诊断、迁移备份及人工回滚边界。原位采用不伪装成无损自动路径迁移。
- 短 runtime socket、UID/权限检查、拒绝抢占存活 listener、inode 核对清理与逐请求项目/instance 核对，覆盖了错误 socket、过长路径和握手后实例替换。readiness 绑定恢复完成及受监督执行循环；启动失败在调度之前发现 listener 错误，避免失败启动留下后台工作。
- 统一停止先拒绝 mutation，再取消请求流和执行，等待 handler、Provider、Validator 及制品收尾，关闭 DB 后解锁。stop 使用当前 instance nonce，等待旧 instance 结束，不依赖可陈旧的 PID 文件；start 复用同一 serve 二进制和所有权锁。该方案不需要另建 supervisor 或全局 daemon。
- Planner 复用 Effect 和幂等记录：接受事务与发布事务分别拥有清晰原子边界；持久 observation 支持结果读回，generation 和 Goal 控制意图防止迟到发布。Wake 只是提示，数据库扫描仍能驱动任务，客户端生命周期不再是持久执行前提。
- DRAFT 规划控制与已有 Goal 状态机分离且投影明确。受 CAS 保护的 `goal plan` 提供 Proposal 修正和显式新规划入口；配置漂移只能通过有理由的新 generation 绑定当前配置/profile，保留旧 Effect 和配置身份，避免等待没有出口或静默改换 Validator/Profile。
- 进程归属扩展到 Planner、Implementer、Reviewer、Probe 与 daemon 启动的验证/服务命令。启动意图、等待 pipe 的 wrapper、身份落盘后释放执行的协议关闭了 exec 与登记之间的崩溃窗口；进程组回收、未知身份保守阻断和未决 Attempt/Lease/Work 对账相互配合。只追加 OnStart 回调或只等待父进程退出均不能满足该设计。
- 最小方案保留每项目 daemon、SQLite、Unix API、既有 CAS/Lease/Git/Evidence；每层保障保护不同边界，不相互替代。强隔离、全局资源仲裁、系统服务安装和自动重启没有混入本次实现。

## 实现与验证关注点

以下是当前设计要求的验证落点，不是新增范围或未决设计决策：

- 用真实双进程验证锁失败前无建库/迁移，以及监听失败和停止后没有所属执行者；并发 start 与旧 instance stop 必须基于实际 API 身份检查。
- 在启动 wrapper 登记前后、释放前后、结果持久化和原子发布前后注入中断，读回持久事实证明不重叠执行、不重复模型回合或发布。
- 对父进程先退出、子孙忽略 TERM/持有 stdout、旧进程身份无法确认分别验证清理或阻断路径；macOS 与 Linux 使用对应原生运行 Evidence，交叉编译只证明可构建。
- 迁移验证保留旧 Goal/Report/Evidence 和 migration checksum；恢复多次运行不能重复创建图、重复副作用或把旧 IN_PROGRESS 原样永远保留。

## 下一路由

允许执行 `PLAN-009` Phase 2 的首个可领取 Item，按身份解析、状态绑定、完整生命周期、后台入口的顺序推进；之后执行 `PLAN-010` 的持久规划和跨项目恢复验收。实现或实验若否定设计假设，应先 Reconcile 到 Design/Spec owner，必要时重新审查 Plan。

本次仅审查并写入此 Review，未修改设计、Plan 或 Progress，也未把设计审查当作运行验收。
