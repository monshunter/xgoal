# REVIEW-017：PLAN-003 M2 Change Review 修订复审

## 审查对象

- Plan：`PLAN-003`
- Revision：`REVIEW-015` High 修复后的 `feat/xgoal-v0.1` 未提交工作树
- 前序 Review：`REVIEW-015`

## Verdict

`FAIL`

## 前序发现处置

- Workspace、Patch、Environment、Validator Definition/Run 已有 SQLite 写入、幂等与跨重启读回 API；Promotion fixture 不再直接伪造 Patch 行。
- Command Receipt 在返回前原子写入私有文件，读回同时校验 Canonical JSON、regular-file/权限/inode/size 与 stdout/stderr Hash。
- M1→M2 Migration 会先生成并校验 Schema 1 备份，M1 Goal 在升级后与备份中均保留。

## 新发现

### [High] 制品读回没有完整重验 canonical runtime root 绑定

- 位置：`internal/validator/command.go` 的 `ReadReceipt`；`internal/store/sqlite/artifact.go` 的 Workspace/Patch 读回
- Evidence：`ReadReceipt` 直接 `EvalSymlinks(runtimeRoot/validator)`，未要求结果仍等于 canonical runtime root 下的固定子目录；`readWorkspaceArtifact` 没有 runtime root 参数；`readPatchBundleArtifact` 读回时不比较持久 `bundle_path` 与固定布局。
- 影响：若运行目录或数据库路径字段漂移到外部同内容制品，Hash 可保持一致，读回会错误接受不属于当前项目 runtime 的事实 owner。
- 路由：先 canonicalize runtime root，再要求固定子目录精确相等；Workspace/Patch 每次公开与事务内读回都重验路径位于当前 runtime，并加入目录 Symlink 与数据库路径篡改负向测试。

## 当前 Evidence

- 修订后 `make verify-m2`：PASS。
- Artifact 跨重启、marker/object/log 内容篡改、同内容日志 Symlink、重复写与 Event 失败事务回滚：PASS。
- 上述测试尚未覆盖整个制品目录或数据库路径 owner 漂移，因此不能关闭 Plan。

## 下一路由

回到 `autogo-tdd` / `autogo-change-implement` 修复路径绑定并重跑完整验收，再进行下一轮 Change Review。
