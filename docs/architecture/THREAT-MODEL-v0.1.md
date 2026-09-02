# xgoal v0.1 威胁模型

## 保护目标与信任边界

xgoal 保护用户 checkout、Git 历史、运行数据库、受信 Validator、Evidence、Agent/Review Packet、Provider Credential 和明确授权边界。SQLite 是唯一运行状态真相；Agent 输出、日志、Markdown 与 Review 都不能反向覆盖它。

v0.1 假设用户主动选择可信的本地 Git 仓库和本机 CLI 登录态。Kernel、当前二进制、`xgoal.yaml`、Git、SQLite Driver 与本机 OS 属于受信计算基；仓库源文件、Agent 输出、Agent 修改、Review 结论和外部进程退出码均按不可信输入处理。

## 主要威胁与控制

| 威胁 | 控制 | 剩余风险 |
| --- | --- | --- |
| Agent 伪报完成或注入命令 | 严格结构化输出；命令只从冻结配置注册；Patch/Validator/Final Tree 独立读回 | Validator 本身可能设计不充分 |
| 修改 Scope 外文件、`.git` 或符号链接逃逸 | detached worktree、根锚定 Scope、deny 优先、NFC/大小写碰撞拒绝、Patch 重放前检查 | L0 不能阻止 Agent 尝试访问其他主机路径 |
| Agent 自建 Commit 或操纵 index | Patch 由 Base Tree 与文件系统计算；Agent Commit/index 只作诊断 | 恶意本地 Git 可执行文件属于受信基破坏 |
| 两个 Writer、迟到 Worker 或 PID 复用 | Daemon 文件锁、SQLite CAS、单 Active Lease、Generation、进程启动身份核对 | 强制杀死后 OS 资源回收仍依赖内核 |
| Promotion 重复、冲突或中途崩溃 | 私有 Integration Ref、Git ref CAS、Effect Journal、Commit Trailer/Tree 读回、幂等 Marker | 不自动恢复被用户外部改写的 Integration Ref |
| Evidence/Packet/Report 篡改 | canonical hash、只读/私有权限、DB 绑定、读回校验、stale 状态 | 拥有同一 OS 账号的恶意进程仍可改权限和文件 |
| Secret 泄漏 | 环境白名单、Project Secret 默认拒绝、递归日志脱敏、Packet 不含 Credential 值 | Provider CLI 在 L0 下使用自身登录态，无法证明与用户主目录强隔离 |
| 未授权网络、push、发布或生产变更 | Project Network 与 Provider Transport 分离；push/publish/production 默认 deny；Gate 按动作、资源、次数、期限授权 | L0 不能在 OS 层强制阻断所有子进程网络 |
| 输出膨胀、卡死和重试风暴 | 输出字节上限、超时、进程组取消、Failure Fingerprint 与 no-progress Gate | 本地 L0 无法消除 Provider 或项目自身的全部外部副作用 |
| 恶意仓库 bootstrap/Validator | 仅可信仓库；命令显式配置；网络/Secret 策略；实际 argv/Receipt 入 Evidence | 用户批准的脚本拥有本机进程权限 |

## 隔离声明

Local Process Provider 的实际等级为 `L0`：worktree、环境白名单、工具参数和进程组是归因与减少误操作的控制，不是敌对多租户沙箱。`doctor`、`status` 和 Final Report 必须披露 `L0`、Provider Transport、Project Network 与 Project Secret 策略，不能声称“完全无网络”或“凭据完全隔离”。强隔离属于后续 Container/VM Provider。

## 恢复与止损

未知外部状态不猜测成功。重启先处理 Worker、未完成 Effect 和报告临时文件，再开放 Socket；冲突、篡改、权限漂移或无法证明的副作用进入 Reconcile/Waiting/Gate。xgoal 不自动 push、发布、操作生产、删除被引用 Evidence/Report，也不自动改写用户共享历史。
