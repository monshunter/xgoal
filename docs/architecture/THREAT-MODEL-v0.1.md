# xgoal v0.1 威胁模型

## 修订关系

OBJ-003 将执行模型改为项目当前主 checkout，替代历史设计中的 detached Attempt/validation worktree；本文件同步该目标模型的控制与剩余风险，不表示新控制已经通过运行验收。历史 Evidence 按其原版本保留。具体协议由 [DESIGN-001](DESIGN-001-m2-git-environment-validation.md) 与 [DESIGN-004](DESIGN-004-m5-control-daemon.md) 拥有。

## 保护目标与信任边界

xgoal 保护用户 checkout、Git 历史、运行数据库、受信 Validator、Evidence、Agent/Review Packet、Provider Credential 和明确授权边界。SQLite 是唯一运行状态真相；Agent 输出、日志、Markdown 与 Review 都不能反向覆盖它。

v0.1 假设用户主动选择可信的本地 Git 仓库和本机 CLI 登录态。Kernel、当前二进制、`xgoal.yaml`、Git、SQLite Driver 与本机 OS 属于受信计算基；仓库源文件、Agent 输出、Agent 修改、Review 结论和外部进程退出码均按不可信输入处理。

一个 Git 仓库只允许一个活动 xgoal daemon，运行入口为当前主 checkout；linked worktree 入口被明确拒绝，子目录和路径别名解析到同一个主目录。所有角色直接在该目录串行执行，不创建或删除 Git worktree，也不另复制执行代码。用户编辑器、手工 Git 命令与其他同账号进程不服从 xgoal 的锁，因此仍属于需要检测的外部写入来源。

## 主要威胁与控制

| 威胁 | 控制 | 剩余风险 |
| --- | --- | --- |
| Agent 伪报完成或注入命令 | 严格结构化输出；命令只从冻结配置注册；Patch/Validator/Final Tree 独立读回 | Validator 本身可能设计不充分 |
| 修改 Scope 外文件、`.git` 或符号链接逃逸 | 当前根锚定 Scope、deny 优先、NFC/大小写碰撞拒绝、Base/当前文件系统快照和发布前检查 | 直接修改已发生于当前目录；L0 不能事前阻止所有越界写入，也不能保证自动撤销 |
| Agent 自建 Commit、切分支或操纵 index | 冻结 HEAD、symbolic ref、真实 index 指纹；通过临时私有 index 构造候选 Tree；各执行/验证/审查边界读回，漂移使结果失效并停止发布 | 同账号进程可在检查之间修改 Git 状态；发现后保留现场等待处理，不强制改回旧值 |
| B 项目误接 A 的 daemon、数据库或覆盖状态目录 | canonical 项目身份、Git 共享定位与 SQLite 项目绑定；迁移前归属预检；CLI 握手及每次业务请求校验项目/仓库/实例 | 同账号可写进程可伪造身份；这些控制防止误路由，不是多租户认证 |
| 两个 xgoal Writer、迟到 Worker 或 PID 复用 | 仓库锁与旧版状态锁覆盖完整生命周期；SQLite CAS、单 Active Lease、Generation、启动前身份登记与进程组读回 | 锁不约束外部用户进程；强制杀死后的 OS 资源回收仍依赖内核 |
| Review/Validator 运行期间现场变化 | 项目串行槽、Reviewer 独立 Profile/Session 与只读权限；前后 Tree/HEAD/ref/index 必须匹配候选快照，漂移拒绝 Evidence | Validator 或同账号进程仍可能产生外部副作用；观察无法提供文件系统事务隔离 |
| Promotion 重复、冲突或中途崩溃 | `refs/xgoal/goals/<id>/integration` 私有审计引用、Git ref CAS、Effect Journal、Commit Trailer/Tree 与当前目录读回、幂等 Marker | 不自动恢复被外部改写的引用或文件；私有 Commit 存在不代表当前目录仍匹配 |
| Evidence/Packet/Report 篡改 | canonical hash、只读/私有权限、DB 绑定、读回校验、stale 状态 | 拥有同一 OS 账号的恶意进程仍可改权限和文件 |
| Secret 泄漏 | 环境白名单、Project Secret 默认拒绝、递归日志脱敏、Packet 不含 Credential 值 | Provider CLI 在 L0 下使用自身登录态，无法证明与用户主目录强隔离 |
| 未授权网络、push、发布或生产变更 | Project Network 与 Provider Transport 分离；push/publish/production 默认 deny；Gate 按动作、资源、次数、期限授权 | L0 不能在 OS 层强制阻断所有子进程网络 |
| 输出膨胀、卡死和重试风暴 | 输出字节上限、超时、进程组取消、Failure Fingerprint 与 no-progress Gate | 本地 L0 无法消除 Provider 或项目自身的全部外部副作用 |
| 恶意仓库 bootstrap/Validator | 仅可信仓库；命令显式配置；网络/Secret 策略；实际 argv/Receipt 入 Evidence | 用户批准的脚本拥有本机进程权限 |

## 隔离声明

Local Process Provider 的实际等级为 `L0`。当前目录执行没有文件系统隔离；环境白名单、工具参数、私有状态权限和进程组用于归因及减少误操作，不是敌对多租户沙箱。Reviewer 的独立性来自不同 Profile/Session、只读权限和前后快照核对，不来自另一份工作区。

独立仓库各有 daemon、数据库、串行执行槽和项目身份；CPU、内存、磁盘、端口、外部数据库/Docker 服务及 Provider 登录态/配额仍共享宿主机或外部服务。项目单实例不等于跨项目全局资源限制。`doctor`、`status` 和 Final Report 必须披露 `L0`、Provider Transport、Project Network 与 Project Secret 策略，不能声称“完全无网络”“凭据完全隔离”或全局并发保证。强隔离属于后续 Container/VM Provider，本次不引入。

## 恢复与止损

未知外部状态不猜测成功。启动先取得项目所有权并核对状态归属，再处理 Worker、未完成 Effect 和报告临时文件，准备完成后才开放就绪 API。停止时取消并等待所属调用、进程组与执行循环，持久化中断结果并关闭数据库，最后释放所有权。不能仅以直接子进程退出判断整个任务已经停止。

冲突、篡改、权限漂移、当前 Tree/Git 身份变化或无法证明的副作用进入可操作的 Reconcile/Waiting/Gate。失败、停止和 cancel 保留当前修改与诊断，不自动 reset、clean、stash、切分支、删除旧 worktree 或复制旧内容覆盖当前文件。旧工作区历史只读保留；绑定旧执行模型的未完成目标进入明确迁移等待，不自动续跑。恢复动作必须先核对现场，报告恢复只重建报告文件，不能借恢复回滚用户工作。

xgoal 不自动 push、发布、操作生产、删除被引用 Evidence/Report，也不自动改写用户共享历史。新设计的完整性需由当前目录执行、外部漂移、跨项目误路由、进程中断与旧状态恢复测试证明；历史成功不能覆盖这些新增风险。
