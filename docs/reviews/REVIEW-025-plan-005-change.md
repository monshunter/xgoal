# REVIEW-025：PLAN-005 M4 Change Review

## Verdict

**PASS_WITH_NOTES**

M4 变更与产品/技术 SPEC、DESIGN-003 和 PLAN-005 一致：Claude Code Adapter、双 Provider Reviewer、版本化 Review Packet/Result/Finding、SQLite 迁移与状态转换、不可变制品和真实双向路径均已形成闭环。未发现阻止 M4 关闭的 correctness、安全、兼容、测试或范围问题。

## 审查范围

- `internal/adapter/claude` 与 `internal/adapter/codex/review.go`
- `internal/protocol` Review 契约与 Schema
- `internal/review` Coordinator、制品与双向真实 smoke
- `internal/store/sqlite/migrations/0003_review.sql` 与 Review/Finding Repository
- `Makefile`、PLAN/DESIGN/Operation 与索引投影

## 当前 Evidence

- `make fmt-check m4-contract vet`：PASS。
- `go test -race ./...`：PASS。
- `go test -shuffle=on -count=20 ./internal/adapter/... ./internal/protocol ./internal/review ./internal/store/sqlite`：PASS。
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...`：PASS。
- `XGOAL_RUN_CROSS_REVIEW_SMOKE=1 go test ./internal/review -run '^TestM4RealCrossProviderReview/claude_to_codex$' -count=1 -v`：PASS；候选 Tree `d20a1b28c86220c5d7b2b65f379564b39ecf12a0`。
- `XGOAL_RUN_CROSS_REVIEW_SMOKE=1 go test ./internal/review -run '^TestM4RealCrossProviderReview/codex_to_claude$' -count=1 -v`：PASS；候选 Tree `7e86cfbdc8cb1e3006979e4cb464fa857834cd3c`。
- Coordinator 集成测试实际创建 Patch Bundle、Validation Workspace、环境快照与 Validator Receipt，并拒绝候选 Tree 漂移。
- SQLite 测试覆盖 migration、幂等写入、重启读回、Finding 终态与文件篡改拒绝。

## 审查结论

- Provider 差异只存在 Adapter 内；Kernel 继续使用规范化 Agent Result，Reviewer 使用正交的严格 Review Result。
- Claude CLI 的 Draft 2020 元字段兼容转换不改变公开 Schema 或最终严格解码，未知/截断/错误输出仍 fail closed。
- Implementer 与 Reviewer Profile/Session 强制分离；Reviewer 工具集只能由 `Read`、`Glob`、`Grep` 构成。
- Finding 权威固定为 `INFERENCE`；Blocker/High 的 `OPEN` 投影进入 Completion Facts，不能把 Reviewer 自述升级为确定性证据。
- Review DB 记录在写入和读回时重新校验不可变 Packet/Result Hash 与字段绑定，Finding 转换使用单事务状态更新、事件追加和 Completion 投影刷新。
- 变更未引入远端写、发布、生产操作或项目网络能力。

## Notes

- 当前本机 Claude CLI 的 Provider 通道实际依赖显式 `ANTHROPIC_*` 环境白名单。生产编排必须由 M5 Policy/Gate 决定是否注入；本次真实 smoke 只在临时可信仓库中使用，值未进入 Packet、元数据、Validator 或日志。
- L0 本地进程隔离不能证明 CLI 及其内部工具对主机凭据的硬隔离，状态和最终报告必须继续披露该边界。
- 两条真实子路径在修复后分别执行并均 PASS；没有再次机械重复相同路径。

## 下一路由

PLAN-005 可关闭并提交；随后进入 M5 Reconcile、Gate 与 Daemon。
