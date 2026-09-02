# REVIEW-018：PLAN-003 M2 Change Review 第二次修订复审

## 审查对象

- Plan：`PLAN-003`
- Revision：`REVIEW-017` High 修复并通过完整 M2 门禁后的 `feat/xgoal-v0.1` 未提交工作树
- 前序 Review：`REVIEW-015`、`REVIEW-017`

## Verdict

`FAIL`

## 前序发现处置

- Workspace、Patch 与 Validator Receipt 的公开读回已重新绑定当前 canonical runtime root，并有外部同内容 marker、数据库路径漂移、目录 Symlink 和日志篡改负向测试。
- `make verify-m2` 已在上述修订后重新通过，前序两项 High 不再复现。

## 新发现

### [High] Workspace 首次记录可提交不符合固定 runtime 布局的内部路径

- 位置：`internal/store/sqlite/artifact.go` 的 `RecordWorkspace`
- Evidence：首次写入仅使用 `artifactWithin` 判断 `snapshot.Path` 与 `MarkerPath` 位于 runtime root 内；固定 `workspaces/{attempts|validation}/<id>/{tree|marker.json}` 布局只在后续 `readWorkspaceArtifact` 执行。
- 影响：由 runtime 内其他嵌套目录产生、且 marker/hash 自洽的 Workspace 可返回写入成功，但同一记录随后无法由 Repository 读回，破坏“事务成功即持久事实可恢复”的不变量。
- 路由：首次写入前复用固定布局校验，并加入“runtime 内、固定布局外”的真实 marker 负向测试。

### [High] Validator Definition 把内容身份与 Base/Config 注册来源合并为单一主键

- 位置：`internal/store/sqlite/migrations/0002_git_validation.sql` 的 `validator_definitions`；`internal/store/sqlite/artifact.go` 的 `RecordValidatorRegistry`
- Evidence：`definition_hash` 只由 Validator Definition 内容计算，却同时作为包含单个 `config_hash`、`base_commit` 的表主键；相同 Validator 在无关配置或 Base Commit 变化后保持相同 Definition Hash，第二次注册会被判为幂等冲突。
- 影响：冻结 Registry 无法跨正常 Goal/Base/Config 演进持久化；为了注册而改变 Definition Hash 又会错误地让无关 Base 漂移使 Evidence 失效。
- 路由：将不可变 Definition 内容与 `(config_hash, base_commit, validator_id)` 注册来源拆为两个事实表；Validator Run 通过 Workspace Base/Config 校验对应注册，并加入跨 Base/Config 重用同一 Definition 的重启测试。

## 当前 Evidence

- 修订前 `make verify-m2`：PASS。
- 外部 runtime 路径与磁盘制品篡改已 Fail Closed；本轮发现针对的是仍未覆盖的 runtime 内错误布局及长期 Registry 演进。

## 下一路由

回到 `autogo-tdd` / `autogo-change-implement` 修复两个持久化模型问题，运行定向迁移、跨重启测试和完整 `make verify-m2` 后重新 Change Review。
