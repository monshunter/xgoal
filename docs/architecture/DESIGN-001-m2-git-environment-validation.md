# DESIGN-001：M2 Git、环境与验证闭环

## 状态与范围

`accepted`

本设计实现 PLAN-003，只覆盖受信本地 Git 仓库上的 Workspace、Patch、Scope、Local Environment、Validator、Evidence 与 Promotion。真实 Agent Adapter、Daemon、完整 Policy/Reconcile、Final Report 和 Benchmark 分别留给 M3–M6。

## 目标与不变量

- 用户 checkout 和用户 base branch 只读；xgoal 只创建私有 `xgoal/<goal-id>/integration` 引用和运行目录下的临时 worktree。
- Agent worktree、Agent Commit、Agent index 和文字声明都不是晋升真相；唯一晋升输入是从冻结 Base Tree 与当前文件系统计算的内容寻址 Patch Bundle。
- Patch 在最新 Integration HEAD 的 detached validation worktree 严格重放；before mode/hash 任一不匹配即冲突，不做模糊三方合并。
- Validator 只能来自冻结 base revision 的严格配置，使用 argv 启动而非 shell 拼接；Receipt/Evidence 必须绑定 Goal Revision、Config、Definition、Environment 与实际 Tree。
- Git/文件/进程副作用遵循 `Request → Execute → Read Back → Observe`；DB 与外部事实不一致时进入恢复或失败，不猜测成功。
- v0.1 项目执行和 Promotion 均串行；M2 使用进程内锁与 Git ref CAS，M5 再增加跨进程单写者锁。

## 运行布局与事实 owner

```text
trusted repository                project runtime root (0700)
├── user checkout (read only)     ├── state.db / backups
└── common Git object database    ├── workspaces/
    └── refs/heads/xgoal/...      │   ├── attempts/<attempt>/worktree
                                  │   └── validation/<attempt>/worktree
                                  ├── packets/
                                  ├── patches/<attempt>/
                                  ├── logs/
                                  └── effects/<effect>/marker.json
```

- Git object/ref、Commit/Tree 与 worktree registration 的事实 owner 是 Git CLI 读回。
- Patch object、manifest、receipt、log 和 marker 的事实 owner 是运行目录中的原子文件及其 SHA-256。
- Aggregate、Effect、Workspace、Validator Run、Evidence 和 Promotion 状态的事实 owner 是 SQLite。
- Event 是同事务审计记录，不反向重建或覆盖当前状态。

## 组件与依赖方向

| 组件 | 单一职责 | 依赖 |
|---|---|---|
| `gitrepo` | 使用 argv 形式调用系统 Git，校验 repository/common-dir/commit/tree/worktree/ref | OS process/filesystem |
| `scope` | NFC 路径规范化、大小写碰撞、Glob、Git 元数据和 Symlink target 判定 | 标准库、Unicode normalization |
| `patch` | 从 Base Tree 和文件系统捕获 Bundle、原子保存对象、完整性校验与严格重放 | `gitrepo`、`scope`、`protocol` |
| `workspace` | 创建/读回/清理 Attempt 与 detached validation worktree，保存外部 marker | `gitrepo`、SQLite Effect |
| `environment/local` | 采集 L0 Snapshot，按环境白名单运行 bootstrap/service 生命周期 | Supervisor、`protocol` |
| `validator` | 从冻结配置建立 Registry，运行受信定义并生成不可变 Receipt | Environment/Supervisor、`protocol` |
| `evidence` | 持久 Evidence/Receipt 绑定与 Staleness 查询 | SQLite、`protocol` |
| `promotion` | 串行重放、验证、创建 xgoal Commit、CAS 更新 Integration Ref 并读回恢复 | 上述组件、SQLite Effect |

高层组件依赖低层确定性能力；`gitrepo`、`scope` 和 `protocol` 不依赖 SQLite 或 Kernel，避免循环。

## Git 与 Workspace 流程

1. 打开仓库时解析 root、`--git-common-dir`、object format、base commit/tree；路径全部 `EvalSymlinks` 后比较，拒绝 bare、submodule worktree 和不受信来源。
2. Integration Branch 首次通过 `git update-ref <ref> <base> <zero>` 原子创建；已存在时必须属于同 Goal 记录且可读回，不覆盖。
3. Attempt Workspace Effect 先记录请求，再以冻结 base commit 执行 `git worktree add --detach`；读回 HEAD、Tree、common-dir 和 `git worktree list --porcelain` 后才标记成功。
4. `.git` 管理文件只由 xgoal/Git 管理。捕获时校验根管理文件与登记一致，跳过该文件；其他任意层级或大小写的 `.git`、gitlink、设备/FIFO/socket 均 Fail Closed。
5. 清理只针对 DB/marker 精确绑定且读回匹配的 worktree，先 `git worktree remove`，再验证 registration 消失；不以宽泛目录删除代替 Git 生命周期。

## Patch Bundle 与 Scope

- Base 侧由 `git ls-tree -r -z` 和 `git cat-file` 读取 mode/blob；文件系统侧用 `Lstat` 遍历，不跟随 Symlink。
- Regular/Executable/Symlink 分别映射 `100644/100755/120000`；Symlink object 是 target 原始字节。绝对 target 或相对解析逃出 worktree 时直接 Invalid/Quarantine。
- added/modified/deleted 由 Base 与文件系统的 path/mode/content hash 比较得到；唯一且 mode/hash 相同的一对 delete/add 可确定标记为 rename，其余保持 delete+add。
- Path 转换为 NFC、`/` 分隔的仓库相对路径；规范化碰撞、Unicode 大小写折叠碰撞、`.git`、NUL、反斜杠和 `..` 均拒绝。
- Scope Pattern 根锚定；`**` 只作为完整 segment。每个 changed path 必须命中允许 Write Scope 且不命中 deny；Validator/配置变化按后续 Policy 处理，M2 默认拒绝弱化。
- 对象先写同目录唯一 temp，设置 `0600`、fsync 后以 no-clobber 语义发布，再 fsync 目录；Manifest 最后发布。读回拒绝缺失、额外、长度或 Hash 不符对象。
- 重放先逐项验证 before，再按 path 稳定顺序执行 rename 的删除侧、普通删除、目录准备与对象写入；失败不部分晋升，validation worktree 可整体丢弃。

## Environment、Validator 与 Evidence

- Local Provider 明示 `L0`，不宣称容器级文件或网络隔离。Snapshot 记录 OS/Kernel/Arch、Git、Base Commit/Tree、工具版本、锁文件 Hash、Config/Goal Hash 和仅名称的环境白名单。
- 命令仅接收已验证 argv、仓库相对 cwd、正 timeout、expected exit codes 和环境变量名称；子进程新建进程组，超时/取消时先终止整组，Wait 后再生成 Receipt。
- 默认环境由固定安全基线加 definition allowlist 构成；不把 Agent 环境、Provider Credential 或未列入变量传给项目命令。stdout/stderr 直接写私有日志文件，Receipt 只保存引用与内容 Hash。
- Registry 由冻结 base commit 中的 `xgoal.yaml` 构造并保存 Definition Hash；Agent workspace 对配置/受信脚本的修改不改变本次 Registry。
- Evidence 是 append-only 元数据，引用不可变 Receipt/Patch/Snapshot；`CURRENT` 必须同时匹配当前 Goal Revision Hash、Config Hash、Definition Hash、Environment Hash 和 Tree。任一 owner 变化只把旧记录标为 `STALE`/`SUPERSEDED`，不删除。

## Promotion 与崩溃恢复

1. 获取项目进程内 Promotion Lock，重读 Integration Ref 与 active Attempt/Lease/Bundle。
2. 创建 detached validation worktree，严格重放 Bundle，计算候选 Tree 并运行 Work/影响范围 Required Validators。
3. 检查 Receipt/Evidence 当前、Scope 通过且无 M1 Required Gate；记录 Promotion Effect Request，内容绑定 old ref、Goal/Work/Attempt、Bundle 与 Evidence Set。
4. xgoal 在 detached HEAD 创建带规范 Trailer 的 Commit；原子写 marker（Commit/Tree/Trailer Hash），再以 `git update-ref <integration-ref> <new> <old>` 做 CAS。
5. 读回 ref、Commit Tree 与 Trailers，写 Promotion Observation，并在同一 DB 事务内关联 Work/Attempt/Evidence、推进 Work `COMPLETED` 与 Event。
6. 崩溃恢复优先读 marker 与 Integration Ref：ref 已指向且元数据匹配则补写 DB；Commit 存在但 ref 未更新则在 old ref 仍匹配时继续 CAS；ref 漂移或任何 Hash 不同则 Effect Failed/Work Reconcile，绝不重复 Commit。

## Schema 与迁移

M1 的 `0001_storage.sql` 已提交后不可修改。M2 使用 `0002_git_validation.sql` 新增 Workspace、Patch Bundle、Environment Snapshot、Validator Definition/Registration/Run、Evidence、Evidence Set 与 Promotion 表，并由外键、唯一键、Version/State CHECK 固化关键关系。Definition 以内容 Hash 去重，Registration 单独绑定冻结的 Config/Base/Validator ID，避免无关 Base 或配置变化伪造 Definition 变化或产生幂等冲突。大型内容只存私有文件和 Hash，SQLite 不复制日志或 Patch object。

## 失败、观测与恢复

| 失败点 | 读回 | 决策 |
|---|---|---|
| worktree 命令未知 | registration、HEAD、common-dir、marker | 匹配则补写；不匹配则清理候选或 Reconcile |
| 捕获中断 | temp/object/manifest Hash | 完整则发布；不完整隔离保留，不当成功 |
| Validator 超时 | 进程组、日志、Receipt | 回收后写 `TIMED_OUT`，不覆盖旧 Run |
| Evidence 绑定变化 | 当前 Hash 元组 | 旧 Evidence 标 Stale，重新运行 |
| Commit 后 DB 前崩溃 | marker、ref、Tree、Trailer | 幂等关联或完成 ref CAS，不重复 Commit |
| Integration Ref 漂移 | `update-ref` old-value CAS | Promotion 失败并进入 Reconcile |

## 安全、兼容与回滚

- 所有 Git/项目命令使用 argv，不经 shell；Git 禁用交互提示，输出有上限并脱敏后进入日志。
- 只支持 macOS/Linux、系统 Git 和可信本地非 bare 仓库；能力不满足时 Fail Closed。
- 不 push、不改用户当前分支、不执行生产/发布。Integration Branch 和 runtime artifacts 可按精确引用清理，但默认保留以供恢复审计。
- M2 回滚代码不会自动 Down Migration；旧二进制若不认识 Schema 2 会按 M1 版本过新策略拒绝打开。恢复可使用 M1 迁移前一致备份。

## 验证矩阵

- 固定 fixture 覆盖 tracked/untracked/binary/rename/mode/symlink/delete 和 Agent 自建 Commit/index 扰动。
- Scope 覆盖 NFC、大小写、`.git`、绝对/逃逸 Symlink、范围外路径和 deny 优先。
- Validator 覆盖 argv 防注入、cwd/env 白名单、成功/失败/超时、日志/Receipt Hash、定义/Tree/环境过期。
- Promotion 在 worktree、Patch、Validator、Commit、ref CAS、DB Observation 边界注入失败，验证重启收敛且不重复 Commit。
- 运行 Race Detector、真实系统 Git 集成测试和 Darwin/Linux 无 CGO 构建。
