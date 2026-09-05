# DESIGN-001：当前工作目录、Git 快照与验证闭环

## 状态与范围

OBJ-003 / PLAN-011 修订，2026-09-05，待本次 Design Review。本文替代原 M2 的独立 Attempt/Validation Git worktree 执行模型；旧版本验收与 Review 保留历史意义，不证明本次原地模型通过。项目所有权、daemon 与持久规划由 DESIGN-004 拥有。

用户目标是 xgoal 运行于当前 Git 工作目录，一个项目只有一个实例，不创建 Git worktree。本方案不复制第二份执行代码目录，不用改名的临时 checkout 保留旧模型。

## 1. 不变量与用户结果

- 一个 Git Common Directory 仅一个 daemon；只支持当前主工作目录，linked worktree 入口明确拒绝。所有角色与命令在该目录串行运行。
- 系统不改用户 HEAD、符号分支和 index；不自动 stash/reset/clean/checkout，不自动提交到用户分支。Agent 改动这些元数据会阻断发布，系统不通过悄悄恢复掩盖越界。
- 当前文件内容是实际输入，Git Tree 是不可变身份。原始基线来自当前 HEAD，后续基线来自已验收的私有集成 Commit/Tree，不要求用户 HEAD 跟着移动。
- 系统快照使用私有临时 index，原始内容直接写 Git blob，不执行 clean/smudge filters 或 hooks。Git/状态目录不进入业务 Patch。
- Patch、Scope、Validator、独立 Reviewer、Gate 与最终 Tree-bound Evidence 继续决定晋升与完成；退出 0 或目录存在都不足以通过。
- 成功后的代码保留在当前目录；审计 Commit 保存在 refs/xgoal/goals/<goal-id>/integration。用户可以直接查看差异并自行提交到当前分支。
- 失败、停止、暂停、取消均保留代码现场；未知变化不得自动覆盖或删除。L0 只能协作性独占，不能阻止同一 OS 用户手动修改文件。

## 2. 组件与事实 owner

```text
当前 Git 主工作目录（唯一执行代码目录）
  Planner → Implementer → Capture → Validator → Reviewer → Final Validation
                        │                     │
                        └── Git Tree / Patch ─┘
                                  │
                 私有审计 Commit/ref + SQLite Evidence/Report

.xgoal（元数据与内容制品，不是第二份执行目录）
  state.db / workspace manifests / patches / receipts / logs / tmp indexes
```

| 组件 | 职责 |
|---|---|
| project/app | 主目录定位、仓库/状态双锁、生命周期；不拥有 Goal 内容 |
| gitrepo | 当前 checkout 身份、Git 对象、私有 index 和私有 ref CAS |
| workspace | 当前目录会话与不可变快照 marker；只清理元数据 |
| patch | 基于 Base Tree/实际文件捕获 Bundle、对象完整性与对象级候选 Tree 重建 |
| environment/validator | 当前目录的受信命令、环境快照、源 Tree 前后核对与 Receipt |
| review | 独立 Profile/Session/只读策略和 Packet，不以独立代码目录定义审查独立性 |
| promotion | 已验证 candidate 的 commit-tree/ref CAS、marker 与崩溃读回 |
| sqlite/orchestrator | 会话归属、Attempt/Lease/Effect、验收与执行顺序的唯一运行状态 |

Git 对象/ref、文件内容和进程是外部事实；SQLite 保存状态和绑定，不复制执行代码。事件同事务审计，不另建事件重放数据库。

同一验证阶段的受管后台服务可与 runtime probe 共存；服务不得并发修改源码，阶段结束前必须回收。整个阶段的 Tree 与 Git 身份核对覆盖这些服务。

## 3. 当前目录准入与连续目标

项目会话保存 root/CommonDir、用户 HEAD commit、符号 HEAD、index fingerprint、最后已验收 Commit/Tree、当前 owner 和最后观察 Tree。Git 指令使用显式目录并清除继承的 GIT_DIR/GIT_WORK_TREE/GIT_COMMON_DIR/GIT_INDEX_FILE 等定位变量；命令属性、hooks、filters 不作为可信捕获依赖。

第一个目标只接管干净的 tracked/index/非忽略 untracked。第二个目标也可接管与系统最后已验收 Tree 完全相同且 HEAD/index 未变的目录，从上次审计 Commit 继续；不强迫每个 Goal 后用户先提交。用户正常提交后，若目录干净，以新的用户 HEAD 重新建立基线。其他 dirty 只报告路径和冲突，不能自动认领。

每个 Work/角色执行前核对会话身份及预期当前 Tree。新的待执行 Goal 不能在另一个 Goal 尚拥有未验收修改时接管目录。已观察失败现场只允许原 Goal/Work 的显式 retry 继续修复；其他 Goal 保持队列或给出确定的 checkout busy/dirty 等待。

干净准入以用户 index Tree == HEAD Tree 且原始字节工作目录 Tree == HEAD Tree 为准，不能用 git status clean 代替；若 CRLF/filter 导致原始字节不一致则明确等待，不执行 filter 自动规范化。

## 4. Workspace 与版本兼容

新的 Workspace 是一次执行/验证的元数据身份，Snapshot 的实际执行路径始终是当前 root；不同 Attempt/Validation 有独立 marker、BaseCommit/BaseTree、输入 Tree、HEAD/index 身份和 ConfigHash。

在保留历史数据、外键与唯一性的前提下追加 schema：旧 workspaces.path 保留历史含义；新模型用唯一制品目录占据其 path，并通过显式 execution_path 与 execution_model=current-directory 指向当前代码目录。读取和校验必须按模型分派，不能把制品 path 当执行路径。新 marker 有新协议版本和独立 marker 路径，不再假设 marker 位于代码目录父级。旧行和 marker 不被改写成当前 root。

会话控制使用单个持久 checkout 记录；Workspace marker 是历史事实，不成为第二套可调度状态。创建/清理元数据保持幂等和引用保护，Cleanup 永不删除当前 root。旧 Git worktree registration 可用于只读诊断，运行和 clean 均不调用 git worktree add/remove/prune。

## 5. 内容捕获与对象级 Patch 重建

1. 从 Base Tree 读取 tracked path/mode/blob；枚举当前非忽略 untracked。tracked 文件即使匹配 ignore 也必须捕获。ignored 构建输出和预存 ignored 文件不删除、不作为源 Tree 的一部分。
2. 无条件排除根 .git 的文件/目录形态、xgoal state/runtime 及元数据。其他嵌套 Git 元数据、gitlink、不支持的设备/FIFO/socket、路径规范化碰撞及 symlink 逃逸 fail closed。
3. Lstat 读取文件 mode 和原始字节，不跟随 symlink。100644/100755/120000、二进制、空文件、rename、delete 的语义沿用 Patch Bundle。
4. 私有临时 GIT_INDEX_FILE 装载 Base Tree，hash-object/update-index 写受控 blob 与 mode，write-tree 得到当前 Tree；不使用用户 index，不调用 git add --all。
5. Bundle 保存确定性 before/after 内容 Hash、mode 和内容寻址对象。严格检查完整性和 Scope；Object/Manifest 原子发布并 fsync。
6. 将 Bundle 按 before path/mode/hash 应用到最新 Integration Tree 的私有 index，生成 candidate。只操作 Git 对象，不再次写当前代码目录；不做模糊三方合并。
7. candidate 必须等于再次捕获的当前目录 Tree，且 HEAD/index 身份未变，才进入验证。

Git 对象写入并不等同于通过；只有合法 Bundle、当前目录和有效 Evidence 才能发布引用。快照中断留下的临时元数据可以按精确路径清理，不能广泛删除业务文件。

## 6. 环境、验证、审查与最终完成

环境 Snapshot 明确区分用户 HEAD 与实际输入 Tree，不再要求实际工作目录 HEAD 等于私有 Integration Commit。工具版本、锁文件、配置和 Goal Hash 沿用原 Evidence 合同。

Validator 命令来自冻结配置/受信脚本，以 argv、受控 cwd/env、timeout 和独立进程组运行。每个 Validator 前后、整个验证集合结束、Reviewer 前后都重新核对代码 Tree、用户 HEAD/index 和受信配置身份。验证命令修改源文件或产生非忽略文件时，其退出 0 不得作为原 Tree 的 PASS；保存日志和漂移事实并重新捕获/验证或等待，不能偷换 Receipt 的 TreeHash。

Reviewer 在当前目录只读运行，Profile/Session 与 Implementer 独立，Packet 绑定实际 diff、candidate Tree、Validator Evidence 与已知 Finding；它不拥有写入权限或自动清理权。

Final Validation 在当前目录重新执行。Completed 同时要求当前目录 Tree、私有 Integration Tree、当前 Goal Revision/Config/Validator/Review/Gate、Final Evidence 与 Report 一致。检测到外部编辑时停在明确等待，不用历史 PASS 代替当前结果。

## 7. Promotion 与现场恢复

Promotion 保留既有 Request→Execute→Read Back→Observe。Request 绑定 old ref、candidate、Goal/Work/Attempt、Bundle 和 Evidence Set。commit-tree 创建带规范 XGoal Trailer 的确定性审计 Commit，原子保存 marker，再以 update-ref old-value CAS 更新 refs/xgoal/goals/<id>/integration。读回 Tree/Trailer/ref 后事务性推进 Work 并更新 checkout 最后验收结果。用户 HEAD/index 不动。

Commit 已创建或 ref 已更新而 DB 未观察时，恢复读 marker/ref 完成同一操作，不重复 Commit。当前目录必须仍对应被晋升 Tree；不对应时保留私有 Git 事实和用户文件，进入等待。

失败、取消和进程中断先回收所属执行者、保存可安全捕获的 Tree/Patch 与日志，再释放项目所有权。失败现场不自动回滚。若当前内容仍匹配已保存观察、HEAD/index 未变且修改属于原 Goal/Work，显式 retry 可以在此继续修复，并最终相对最后已验收 Tree 重新 Capture/验证。无观察、Scope 越界、元数据变化或第三方编辑时保留现场并给出可执行诊断；原始 Git Tree/历史 Patch 是恢复材料，不意味着系统有权覆盖文件。

不新增 restore Effect、自动 checkout 或第二个回滚控制器。pause/resume/cancel 与 generation fencing 继续由 DESIGN-004 的持久控制意图管理。

## 8. 配置、历史与迁移

新配置只使用 workspace.provider=current-directory，省略时也采用该模型。旧 git-worktree 值给出明确 CONFIG_MIGRATION_REQUIRED，不静默接受后换行为。初始化示例与 CLI/README 同步。旧 baseBranch/integrationBranchPrefix 不再决定当前目录基线或用户分支；迁移说明明确新基线和私有 ref 位置。

迁移只追加顺序 schema，保留旧 checksum，使用现有一致备份。旧完成 Goal/Report/Evidence/marker 按原版本继续读回；旧未完成 worktree Goal、未决 Promotion 或运行者先安全终止/登记历史，再进入明确迁移等待，不自动恢复旧执行策略、不自动导入未验收 Patch，也不删除原工作树。新模型目标与旧目标通过 execution_model 区分。

旧配置或旧目标导致不能执行时，诊断和已有报告读取仍可用；新 run 明确拒绝或进入可操作等待。回滚不降 schema、不覆盖新 Evidence；只有操作员明确选择一致备份恢复时才可回退。

## 9. 验证矩阵

- 真实 CLI/daemon 完整 Goal：各角色 CWD 为当前 root，Git worktree 清单不增加，最终代码可见，HEAD/符号分支/index 前后相同。
- 两个连续 Goal、多 Work：后续基线使用已验收结果；未验收现场不能被另一 Goal 接管。
- preexisting staged/unstaged/untracked、外部编辑、Agent metadata 修改：拒绝或保留等待，原字节不丢失。
- Patch tracked/untracked/binary/rename/mode/symlink/delete、ignored 输出、大小写/NFC、Git/state 排除、filter/hook 不执行。
- Validator/Reviewer 改源文件：Evidence 不得绑定旧 Tree；最终验收三方 Tree 一致。
- fail/pause/cancel/SIGTERM/SIGKILL：进程被回收或新执行被阻止；现场保留且显式 retry 有确定去向。
- Commit/ref/DB 窗口中断：重复恢复不重复晋升，不修改用户 HEAD/index。
- 旧完成历史可读、旧未完成等待、旧配置诊断和 clean 不删当前/历史代码目录。

真实 Provider smoke 与固定 Provider fixture 的确定性故障验收分别记录；原生 macOS/Linux 运行 Evidence 与交叉编译证据分开。
