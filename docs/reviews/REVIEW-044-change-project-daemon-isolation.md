# REVIEW-044：PLAN-009 项目隔离与 Daemon 生命周期 Change Review

## 审查对象

- Objective：`OBJ-003`；当前交付为 [PLAN-009](../plans/PLAN-009.md)，后续顺序为 `PLAN-011` → `PLAN-010`。
- 基线：`24960390d71237d6266e2f995b4e3df0b6719a78`，分支 `fix/project-daemon-isolation`。
- 上游：[REVIEW-041](REVIEW-041-plans-current-directory.md)、[REVIEW-042](REVIEW-042-spec-current-directory.md)、[REVIEW-043](REVIEW-043-design-current-directory.md)。本次只核对 PLAN-009 已实现范围；内部 worktree 移除、持久 Planner 和全部角色进程登记由后续 Plan 实施，不把已明确分期的内容当作本阶段遗漏。
- Reviewer 未编写本阶段实现；被审代码、Plan、Spec、Design 和操作记录只读。本次只写本 Review 与 reviews 索引。

被审代码是相对上述基线的 29 个修改/新增 `internal/**/*.go` 文件，包含实现与测试；文件清单与内容绑定见文末。按路径字典序拼接 `SHA256(file) + 两个空格 + path + 换行` 后的清单 SHA-256 为 `9f158e40c7bdb941cb297536a3b27efd2de1645c6d3ec33cb353d0ee099d36d3`。`PLAN-009.md` 当前 SHA-256 为 `1b7184177d2a86b2fac3868b14951eb0fd1842bc273ece42443618717543a27f`。

## Verdict

`PASS_WITH_NOTES`

项目解析、跨状态目录单实例、DB 绑定预检、身份握手、后台生命周期和被动诊断的主链路符合当前阶段合同，没有阻止 PLAN-009 收口的 correctness 或数据完整性问题。下面一项 note 限定“只读预检”的准确含义，不影响业务数据隔离、迁移保护或后续实施。

## 发现

### N1：WAL 模式的只读身份预检可能创建 SQLite sidecar

位置：`internal/store/sqlite/project_binding.go` 的 `CheckProjectBinding` 调用既有 `openLiveReadOnlyDatabase`；后者以 `mode=ro` 打开 WAL 数据库。

Reviewer 独立构造临时仓库与带外国项目绑定的 WAL-mode `state.db`，正常关闭建库连接后，仅保留主库文件，再执行实际 `go run ./cmd/xgoal daemon serve --project <repo> --state-dir <state>`。进程在项目绑定不匹配处退出，CLI 退出码为 5；主库 SHA-256 前后同为 `c05d4d2723d86980208ee084c58fc9337d5583678c3c0b56cf92b4f86406e91c`，但原状态目录新增空 `state.db-wal` 与 `state.db-shm`。

因此可以证明“错误项目未进入业务执行/迁移且主库内容不变”，不能推广为整个 state-dir 零写入。兼容所有权锁也有独立文件写入边界。该现象不意味着跨项目 Goal 写入或主库篡改；当前不为消除 SQLite 元数据副作用引入数据库复制系统。操作记录和对外结论应精确披露该边界，离线 `doctor` 则继续保持完全不打开 SQLite 的更强保证。

## 审查结论与 Evidence

- **项目边界。** Resolver 统一 flags/env、子目录与真实路径，检查主 checkout、Common Directory 和持久 locator；linked worktree、submodule、冲突历史、错误绑定与最终敏感 symlink 在运行前拒绝。独立 clone 使用不同 Common Directory 身份和 runtime 分区，不按远端 URL 合并。
- **所有权。** `project.Acquire` 固定取得仓库锁再取得兼容状态锁，锁 inode 保留。状态覆盖不能绕过仓库锁，也不能绕过已持久 locator。持锁后的路径复核关闭并发初始化期间的陈旧解析窗口。
- **状态与兼容。** `app.Serve` 在任何可写 Store 打开和迁移前做项目绑定预检；默认旧状态检查可读取的 Workspace/Planner 归属，外部非空无绑定状态拒绝。`OpenProject` 复用备份迁移并以不可覆盖事务写绑定；旧 Goal 与迁移备份的测试直接断言保留。新握手拒绝旧无身份客户端，操作手册明确升级前停止旧实例。
- **生命周期。** app 保留 ownership 直到请求、Engine 和 Store 按反序收尾；启动先完成恢复和 listener 准备。listener 失败主动取消 runtime，HTTP Shutdown 超时不提前释放业务所有权。`daemon start` 使用同一 Run 路径和独立 session，基于真实握手判断 ready；失败只清理本次创建的 child。`stop` 经双次实例核对与服务器 nonce fence，等待原实例退出，不按 PID 文件盲杀。
- **IPC 与状态。** 握手同时核对协议、项目、RepositoryIdentity、root 与 state-dir；后续请求绑定 instance，旧客户端重连不会进入新实例业务。活 socket 不抢占，清理核对创建时 inode。持锁且不可连接报告 UNREACHABLE，503/授权失败与身份不匹配有独立分类。
- **离线诊断。** 被动 doctor 直接检查配置、Git、命令和已有 daemon，不打开/迁移损坏或缺失 DB；Provider 被动能力检查不构造会创建 runtime 的 Adapter。Git 定位变量不能把显式项目导向另一仓库；输出及等待有界。README 与操作手册披露 L0 的主机资源、端口、凭证和配额共享。

测试实现已逐项核对断言，覆盖真实子进程 CLI 的 A/B 并发启动、同项目选主、Goal 读隔离、错 socket 拒绝、停止 A 后 B 存活、重启后旧实例请求拒绝；数据库摘要、历史 Goal/备份、linked/submodule 拒绝前后快照、锁 inode、listener 故障与请求超时收尾也有直接断言。它们没有用进程退出 0 代替业务隔离结果。

主任务在当前代码上执行并报告完整原始工具结果通过：

```text
go test -race ./internal/project ./internal/projectinit ./internal/store/sqlite ./internal/api ./internal/app ./internal/daemon ./internal/cli ./internal/doctor ./internal/control ./internal/adapter/codex ./internal/adapter/claude -count=1
go test ./...
```

本 Reviewer 另外执行上面的错误绑定 WAL 独立场景，直接观察拒绝退出、主库摘要和 sidecar 结果。没有调用真实 Provider；未把这些证据写成 Linux 原生进程验收或双项目完整 Goal 验收。阶段运行证据由 [项目验收记录](../operations/PROJECT-DAEMON-CURRENT-DIRECTORY-ACCEPTANCE.md) 保存。

## 下一路由

允许 PLAN-009 完成正式制品/Git 对账并创建原子提交；本 Review 不替代 Plan 勾选或整个 OBJ-003 验收。随后按已审顺序实施 PLAN-011，完整当前目录、持久规划、恢复与 Provider 验收仍须由后续 Plan 收口。

## 被审代码内容清单

```text
daa17d619f4368f9f91f9f1caf8b9772828a9210b42b5241dd62573956874e86  internal/adapter/claude/passive.go
6fe29fb8e1428b7b194d7947d77f21c80283e8548c675fc875283cefe9b20dba  internal/adapter/claude/passive_test.go
cbe80912cd68a216a7492c0f485c05fc761f09ec2363b411f37d7dd22ab49d93  internal/adapter/codex/passive.go
bd6cf80a593c5ab724667594367c07ba45de57c0e31a3d048204eb51eb834f32  internal/adapter/codex/passive_test.go
b7b62394329436fefd95bb7da3a437e47e01cd9d87744385756f032c81e1ce24  internal/api/client.go
b7991c374b8e316f0ef379a28beeaec1e574fee3bb687e7361e951b88af837bb  internal/api/client_test.go
d5d193c2e2581e378e3ba3d659156e4c5b6b44fd3d66bcdb9b621e5340884f3a  internal/app/app.go
9a128b05479cc6b5a8ff8ccdcc23c46d8b897bf5ffd2bf5b7f4573238caeee57  internal/app/app_test.go
b13f9fc03666e05346a91d4aa0c1c4bd9fc97c418f5fd1e555fb46ad3134033d  internal/app/lifecycle.go
3ec379f5113b756c23a5e462f2f4688fe556f9c3533ef1b63dc17ce975e990f0  internal/app/lifecycle_test.go
6884d3d16f88d957d639639df3b8285804136e6562dbdd9b738e17ae835f4d36  internal/cli/cli.go
da61cad638ec1054de91836eadda779101100aa90ded61c40efb002f2fb6d461  internal/cli/commands_api.go
00884c72a786b68058f278dad34f845b164ffa65556bf71645fbc33e125262a7  internal/cli/commands_local.go
eef42bfd8ac11c34c84c27a225884bcd54df404c98e1f7e35de2ff2c2df31831  internal/cli/lifecycle_test.go
69b129624b4907ba0c7fdf42857d7bcf44255ba3a069b146c5108af11b433ff7  internal/cli/request_test.go
f7d81b8e8c117fae79a43e9a1c0ec963ac51e6ad5aae3df1ac2d132a74631f5b  internal/control/doctor_test.go
40c9ea469302996adcfdb895ac911707fb7d7a5828c2d9640c3ba2b68cc22a5b  internal/control/service.go
a1dbfadada3885618a23490d5ee645953ff8fad605e553634e9ed6c93b737c64  internal/daemon/daemon.go
80ea89afa7968697bf8a4e96b99eb2458a41c96441b55409f38ebb33a17b01b8  internal/daemon/daemon_test.go
a7bb18ac3e42483e89f21894d9480ad4a70a90137d959a49dad921e7e88ad52f  internal/daemon/lifecycle_test.go
d7ea567f20ff20f0ad5436f9464b3b70133a67156a2b8c2935d3aecd6f64bb18  internal/doctor/doctor.go
1be44f4c62b8ae22aabe806d7a9a03b59f34309296d1ea1f5703d25bcd096ee4  internal/doctor/doctor_test.go
19272aec909800c5141742b856a344516a435aaed76329409d8f11c5d9e9868f  internal/project/ownership.go
8306b3ce773078de26d86fc119b84b22f54592f3c5cc787ac8df92525e822061  internal/project/project.go
18514da37dbe507c3e8bb402d0788c812773cffad6b1b0e236dc75329c4da213  internal/project/project_test.go
c84d477bbf0aa751e8b5f1b7414f88340fc704e8aa81007796ae98d957efc6c9  internal/projectinit/init.go
104503ab94eeda39b871be7551c3f6093ffcab223192542f0cd809f571a7b734  internal/projectinit/init_test.go
8579c29e0e07bf98bd6d813754db7d12a6bbc8d5ae7033d5c8fe10fdcff2741d  internal/store/sqlite/project_binding.go
db08f7d898ef69ff6185071595943cf83eaf98f3b5a2b54598fd4a6183aef62c  internal/store/sqlite/project_binding_test.go
```
