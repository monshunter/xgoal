---
name: autogo-work-continue
description: "从 Git、代码、测试、环境和当前路由允许的制品恢复同一次 run；在用户要求继续、显式调用 autogo-work-continue、会话或 Worktree 切换，或记录状态可能与实际代码漂移时使用；不因存在历史 Plan 把新的独立 Fast 请求绑定到 Standard。"
---
# autogo-work-continue
## 目标
从 Git、代码、测试、环境和当前路由允许的制品恢复同一次 run 的真实状态，选择未完成工作并持续推进；历史摘要不能覆盖当前事实，历史 Standard Plan 不能决定新请求的路由。
## 输入与发现
- Git 分支、Worktree、Diff、未跟踪文件和最近提交
- 根与受影响组件项目指令
- 当前路由；Fast 使用用户授权和轻量目标，Standard 使用 `PROGRESS.md`、正式 Plan 和事实 owner
- Spec、Design、Review、Operation、Deployment record、Decision、Journal 等事实 owner
- 当前测试、环境和运行结果

## 输出与持久制品
- 恢复后的真实状态与差异说明
- Fast 的轻量恢复上下文，或 Standard 对账后的 Progress、正式 Plan 与对应事实 owner
- 被选择并实际执行的下一 Skill
- Completed、Waiting、Cancelled 或继续 Running 的精确结论

## 副作用与 Human Gate
只恢复原授权范围；生产、破坏性、权限、Secret、计费或不可逆副作用仍受原 Human Gate，不因“继续”扩大授权。

## 执行步骤
1. 确认项目根、分支、Worktree、当前 Diff、未跟踪文件、最近 Commit 和 dirty owner
2. 根据真实受影响路径加载项目指令树，确认目标和授权范围仍与原 run 一致；独立新结果返回根合同创建新 run
3. 重新检查代码、测试、环境与外部依赖；Fast 只读取用户轻量目标和必要事实，Standard 才读取 Progress、正式 Plan 和全部相关事实 owner
4. 以当前事实纠正过期记录；Standard 缺失或损坏时重建最小真实 Objective / Plan 状态，Fast 不创建、恢复或要求 Objective、Plan、Review 和 Standard trace
5. Standard 若处于 Waiting，先检查唯一事实 owner 中的精确恢复条件；条件未满足时保持 Objective `正在处理`，不勾选 Plan
6. Standard 为当前未完成 Plan 确认最近一次 Plan Review 及 Spec/Design 决策矩阵；恢复前重新检查 Target 身份、Git Diff 和 active 唯一性，新建或实质调整后尚未通过时先路由 autogo-plan-review
7. Fast 选择轻量目标内的最小安全动作，验证后返回根合同收口；Standard 选择下一条未勾选 Phase Item 或 Reconcile 修复项继续，任务实际完成且 Evidence 成立后才勾选
8. Standard Plan 通过 Change Review 并满足关闭条件后调用 autogo-change-close；存在下一 Plan 时立即继续
9. 直到 Objective Completed、用户 Cancelled、Waiting、用户明确停止或继续会超出授权范围才退出

## 验证与完成
- 重复调用不重复创建任务、提交或副作用
- 当前结论以实际代码与当前测试为准
- 没有被旧 Progress 强行绑定到错误下一步
- 新的独立 Fast 请求没有因历史 Objective、Plan 或部署记录升级 Standard
- Objective、正式 Plan、Review、测试、环境和 Git 状态一致
- 退出原因清晰且状态已对账

## 失败、重试与幂等
记录无效时以实际仓库和当前路由重建最小状态；Fast 发现准入条件失效时保留 Evidence 并返回根合同升级 Standard，同一错误重复时只有新操作能产生新 Evidence 才重试，否则转 autogo-investigate 或 Rally。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
