# DESIGN-004：确定性控制面、项目隔离与持久后台执行

> OBJ-003 修订（2026-09-05）。本文件仍是控制面的唯一设计 owner；第 6–12 节替代 M5 中按状态目录识别项目、同步 HTTP Planner 和不完整启动/关闭顺序的旧实现假设；当前目录执行与 worktree 移除以 DESIGN-001 的 OBJ-003 修订为准。产品行为与验收以产品 SPEC FR-100–110、AC-ISO/AC-BG/AC-CWD 为准，底层协议与现有 Evidence/Promotion 不变量继续适用。

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
- `internal/project`：Git 项目身份、只读定位、共享定位信息及仓库/状态所有权。
- `internal/app`：锁、DB、恢复、API 和执行循环的唯一生命周期 owner。
- `internal/daemon`：Unix HTTP listener、身份握手、readiness、后台 start/stop/status 与有界请求结束。
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

## 6. 项目身份与状态所有权

### 6.1 身份模型

Project 的边界是 canonical Git Common Directory，不是 cwd、worktree 路径、仓库名、远端 URL 或任意 state-dir。Project ID 写入 Common Directory 的本地配置；仅支持当前主工作目录入口，linked worktree 明确拒绝；独立 clone 新建。未初始化时 Resolver 只读派生稳定的预初始化身份，init/serve 在仓库锁内将其持久化；已经存在的 Project ID 永不被新值覆盖。同名仓库、同一远端的两个 clone 不合并。

`internal/project` 提供只读 Resolver，返回调用目录、执行根、Common Directory、Project ID、StateDir、RunDir、SocketPath 与 RepositoryIdentity。RepositoryIdentity 为 canonical Common Directory 的摘要，结合持久 Project ID 检测错误定位；它不声称是敌对同账号进程不可伪造的安全凭证。

执行根是当前 Git 主工作目录，状态默认是其 `.xgoal`；已绑定定位以 Git common config 的单个原子 state-binding 值（Project ID、执行根、状态路径）为准，兼容同步 `xgoal.projectID`；不依次写多个独立路径键制造半绑定。旧 linked worktree 不能作为备用执行根；发现其历史状态或多个历史状态库时明确诊断并保留，不自动采用、移动或合并。显式覆盖若与已绑定定位不同则拒绝，不创建平行真理源。路径别名先 canonicalize，包括已存在父目录中的 symlink，拒绝最终敏感文件是 symlink/非普通文件的情况。

状态库 `store_metadata` 中保存 ProjectBinding（Project ID、Common Directory、执行根）。Git 定位只用于找到状态，DB 绑定用于验证所打开的状态属于目标项目，两者不得各自拥有 Goal/Work。绑定写入使用不可覆盖的事务/CAS；若不一致，在执行与迁移前失败。

### 6.2 所有权与旧版本互斥

每次 daemon 启动固定按“仓库锁 → 状态锁”顺序取得非阻塞 advisory lock；失败不继续。仓库锁在 Common Directory 的 xgoal 私有目录；状态锁继续使用 `<state>/run/daemon.lock` 与旧二进制互斥。锁文件不删除，避免 inode 替换制造第二 owner。初始化修改共享定位也使用仓库锁。

绑定预检与启动锁在任何 sqlite.Open、Migration、Worker Recovery、工作区创建之前。状态库打开后所有运行写入经该实例；不同 Goal 的事务/CAS/Lease 继续保护业务不变量，锁不能替代它们。

默认 `.xgoal` 旧库采用规则：不搬迁文件；检查已保存 workspaces 的 Common Directory 和路径、Planner Packet 的 ProjectRoot 等可读取归属；有冲突立即停止。缺乏历史归属的默认状态只在用户明确选定的可信仓库默认位置采用，报告 legacy adoption；任意外部非空无绑定旧库拒绝。启动先完成只读身份预检，再使用现有 SQLite 备份迁移能力。复制同账号可写的整个仓库/状态不属于强安全隔离；不把路径约定描述为密码学身份。

### 6.3 Runtime 路径

默认 socket 在当前用户私有的短 runtime 父目录中按 RepositoryIdentity 分区，避免长仓库名导致 sockaddr_un 溢出；支持 `XGOAL_RUNTIME_DIR`。目录验证真实路径、UID 和 0700，文件 0600。显式 socket 只改变传输位置，不能改写项目归属；检查平台路径长度。

同一 socket 上存在可连接 listener 时一律拒绝抢占，包括不兼容/无法识别的服务。仅确认无 listener 的旧 socket 可移除；listener 关闭禁用不受控自动 unlink，并以记录 inode 核对清理，只删除本实例创建的 socket。独立仓库即使错误指定同一 socket，也不能破坏原服务。

## 7. 统一生命周期与后台操作

`app.Serve` 持有全部资源句柄；Recovery 只恢复，不启动 Engine goroutine。先取得所有权、绑定预检、Open/Bind Store，再完成 Worker/Effect/Report/任务恢复，准备 listener；所有准备成功后才启动 Engine 与 API，握手返回 ready。每个启动失败路径按资源反序回收，禁止先调度后发现 listen 失败。

停止使用独立的有界收尾 context：停止接收 mutation，取消 HTTP request context/SSE 和 Engine context，等待 handler、Engine、Adapter observer、Validator/服务进程结束；归档 Interrupted 状态及其可操作恢复去向；关闭 DB，再释放状态和仓库锁。HTTP Shutdown 超时后强制关闭连接并等待 handler 收尾。不能仅等父进程退出就宣称整个进程组停止。

`daemon serve` 保持前台。`daemon start` 用同一二进制执行 serve，由 launcher 建立独立 session、空 stdin、私有日志，等待真实 API 握手与身份一致；失败只回收本次启动的 child。并发 start 由所有权锁选出一个实例，其他调用可在匹配实例 ready 后返回 already_running。不存在自动重启控制器，也不自动安装 launchd/systemd 配置；这两者需要时直接托管 serve。

`GET /v1/daemon` 返回 protocol_version、software_version、project_id、repository_identity、project_root、state_dir、instance_id、pid、started_at、state。readiness 表示迁移/恢复完成且执行循环已被监督；不能用 socket 文件存在或 PID 存活替代。

普通 API 请求携带预期 Project ID/RepositoryIdentity/instance，服务器核对后才进入业务 handler，防止握手后重连到了另一个实例。协议不兼容 fail closed；兼容协议的软件版本差异可诊断而不伪装升级已生效。

`POST /v1/daemon/stop` 携带 instance nonce，先响应接受停止，再触发统一关闭；客户端等待原 instance 释放所有权/服务退出，不读取 PID 文件盲杀，不把新实例退出当成旧实例停止。status 不打开DB、不写文件，准确区分 ready/stopped/unreachable/identity mismatch；持锁但未ready不能报告stopped。

## 8. 持久规划与幂等

### 8.1 原子接受

复用 `effects` 和 `idempotency_records`，不引入消息队列或第二个 Job 系统。`POST /v1/goals` 在一个事务中登记 DRAFT Goal、原始请求事件、`effect_type=planner` 的请求和已完成的接受响应；响应仍为 201，增加 planning_state=QUEUED，提交成功不等于目标完成。无有效配置的请求保留可解释状态/等待，不制造不可恢复的空任务。

HTTP 层为该操作委托原子接受 Store 方法，不先调用通用 Begin 再提交业务。相同 scope/key/hash 返回原始响应，异 hash 冲突；不同 key 使用同 Goal ID 也不能重复创建或覆盖。事务提交后调用 Wake 只是提示，漏掉提示时 Store 扫描仍能调度。

Planner Effect 的 immutable request 包含 Goal ID、raw_goal、mode、created_by、config hash、选中 profile 和可选用户 Proposal；不持久化 Secret。新建调用目录包含 invocation/generation，旧 packet 存在时只允许校验后复用，不能覆盖不同内容。

### 8.2 执行与发布

项目 Engine 在同一串行槽选择待规划与可运行 Goal。Provider Planner、Implementer、Reviewer 与 active probe 共用该槽；active probe 在忙时返回可诊断 PROJECT_BUSY，不另起无限并行。独立项目不共享这个槽。Provider 超时与输出限制继续执行。

Planner Effect 复用 REQUESTED/EXECUTING/OBSERVING/RECOVERING/SUCCEEDED/FAILED。先登记 invocation，再调用 Provider；完成后先保存可校验 Proposal/result observation。Compile 成功后一个事务冻结 GoalRevision、创建Plan/Work/Dependencies、激活、完成Effect与Event，任何失败不留下半激活图。

恢复 OBSERVING 的结果只确定性编译/发布，不重复Provider。中断且没有结果时，先证明旧执行已结束，再创建新generation；重复失败无进展遵守原Reconcile策略。已取消/暂停Goal或过期generation的结果不发布。配置hash漂移进入等待，不能默默换Profile/Validator重跑。

### 8.3 可操作等待与控制

DRAFT 的 pause/resume 用持久规划控制意图表示，状态投影包含 planning_state=PAUSED，不能伪造无Revision的RUNNING。cancel终态保留历史，取消当前Provider并防止迟到提交；wait遇到规划暂停/等待以3结束。

`goal plan <goal-id> --expected-version N --reason ... [--proposal-file ...]` / `POST /v1/goals/{id}/plan` 为未冻结目标提供受CAS保护的修正Proposal或新规划尝试。记录新规划generation和原失败，必要时明确解决对应planner Gate，不把批准普通Scope Gate等同于通过Proposal验证。显式 `goal plan` 重试可以绑定当前配置hash/profile以解决配置漂移等待，须记录旧/新配置身份和操作者理由；旧Effect不可变，创建新generation。已冻结目标继续使用既有replan合同。

### 8.4 旧记录恢复

不盲目重放所有IN_PROGRESS。创建请求可证明与GoalCreated事实及完整请求对应时，重建接受响应并补规划意图；半冻结状态核对已持久Contract/Plan后完成确定性激活或建立明确Gate。无法证明归属、请求冲突或不安全副作用，保存REQUEST_INTERRUPTED等确定响应和诊断，保留旧请求/事实，不永久停在REQUEST_IN_PROGRESS。恢复必须幂等。

## 9. 所有执行者的归属与进程回收

扩展现有进程监督能力，使Planner、Implementer、Reviewer、active probe和由daemon启动的验证/服务命令具备可恢复的invocation身份。新增持久记录可引用不同owner kind/id，包含启动意图、PID/PGID、启动身份、generation、退出观察；不滥用只有Attempt外键的旧worker_processes表。旧表数据继续读回兼容。

Provider真正执行前需要启动握手：先持久化启动意图，启动阻塞等待的受控wrapper，记录该PID/PGID/启动身份，随后通过私有pipe释放wrapper exec目标命令。父daemon在登记前后崩溃时，未释放wrapper因pipe EOF退出；已释放的执行者有可读回归属。仅仅在exec之后加OnStart回调不能关闭未登记窗口。

进程回收对匹配身份的组发TERM，宽限后KILL，并确认组内执行者结束；父进程提前退出和子孙持有stdout pipe必须有测试。os/exec WaitDelay防止pipe无限阻塞，但不替代进程清理。未知PID或身份变化不发信号，持久记录lost/无法证明的事实，必要时阻止新执行。

关闭与重启同时对账Attempt/Lease/Work：中断执行归档后回到可调度的新Attempt或可操作Gate；不要只撤Lease留下RECONCILING Work。Provider observer和日志文件收尾使用有效清理context并等待完成，之后才能关DB。

## 10. CLI、隔离与观测

Cobra执行入口保持，所有项目命令共用Resolver。全局--project/--state-dir/--socket与相应环境变量按同一优先级解释；显式错误覆盖必须显示准确目标，不隐式切回默认。help/version/completion在任何项目初始化或API前结束。

被动doctor直接检查Git、配置、命令版本、定位和已有daemon握手；不构造会创建目录的Adapter，不打开SQLite、不触发Provider。无daemon/损坏数据库时仍能报告store未打开与实际诊断。active doctor继续经daemon保存Evidence并使用串行槽。

status和报告披露L0：主机CPU/内存/磁盘、端口、外部数据库/Docker和Provider登录态/配额共享。运行配置中的服务端口、数据库名、容器名由项目明确配置作用域；目前没有全局资源仲裁，不报告容器隔离或全局并发保证。不增加模型计费账本。

## 11. 迁移、回滚与验证

SQLite仅新增顺序Migration，保留旧checksum，先备份再迁移。项目绑定采用原位兼容，既有目录/文件/Goal/Report不删除。新的定位和身份握手意味着旧CLI不能对新版daemon绕过检查；升级时先停旧daemon，再启动新版本，使用匹配CLI。

回滚必须停止所有新实例并保留新版本DB/制品；旧二进制不读取新版schema。只有明确放弃升级后新增工作时才由操作员使用已验证备份恢复旧状态，不能自动降schema或覆盖新Evidence。定位/路径移动出现不一致时保留状态并诊断，不自动重写历史绝对路径。

验证从当前失败复现出发：项目Resolver与绑定单测、SQLite原子性/迁移测试、真实多进程锁/socket/start/stop测试、故障注入的Provider fixture系统链路，以及opt-in真实Provider smoke。fixture可验证Kernel不变量，不能证明真实Provider兼容性或模型效果。证据分别保存于Operation/Change Review，并映射产品AC-ISO/AC-BG。macOS本机执行、Linux原生环境执行进程/恢复合同；交叉编译只证明可构建。

## 12. 最小充分方案与非目标

保留每项目daemon + SQLite + Unix HTTP API；全部执行在当前主工作目录串行运行，不创建 Git worktree。前台serve与后台start复用同一Kernel；一个全局daemon会新增跨项目路由、公平性和故障域，本次无必要。项目内单写者、DB CAS、Lease fencing、Git ref CAS与Evidence分别保护不同边界，不能因存在其中一个就删除其他保障。

不实现全局registry.db、消息队列、第二个Job状态系统、系统服务自动安装、自动重启、容器/VM、跨项目配额或强多租户隔离。远端push/publish/生产默认Deny不变。任何工作区执行成功仍须最终Tree上的Validator/Review/Gate/Evidence/Report闭环；进程退出0和锁释放都不是Goal完成证据。
