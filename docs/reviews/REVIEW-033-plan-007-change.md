# REVIEW-033：PLAN-007 M6 与 v0.1 Change Review

## 审查对象

- Objective：`OBJ-001`
- Plan：`PLAN-007`
- Design：`DESIGN-005`
- 产品/技术合同：`xgoal-product-spec-v0.1.md`、`xgoal-technical-spec-v0.1.md`
- Revision：`feat/xgoal-v0.1` 当前工作树
- 范围：M6 Final Report、完整 daemon 编排、Benchmark、发布文档/许可证、产品 AC 验收，以及用户明确要求的模型核算边界移除

## Verdict

`PASS`

## 当前 Evidence

- `make verify-m6`：PASS。覆盖 gofmt、全包测试、shuffle×20、全包 race、vet、CLI/config smoke、SQLite Darwin/Linux 编译、M2 故障矩阵、M3/M4 hermetic Adapter/Review 合同、M5 safety、M6 release 测试和 Linux amd64 / Darwin arm64 二进制构建。
- Benchmark Suite Hash：`24321abd52ae4c8ce3e6a232e8907312d6c982c58deeb9d9adafff9eecc36e7e`；六个任务、三个组共 18 个 Comparison Identity，三组使用同一 fixture/goal/acceptance/timeout；真实执行保持 `NOT_RUN`，`upload=false`。
- M3/M4 已保存真实 Provider Evidence：Codex Fast/Standard，以及 Claude Implementer→Codex Reviewer、Codex Implementer→Claude Reviewer 两条独立会话路径；本次未重复发起模型回合。
- Unix Socket E2E 运行真实 daemon、真实子进程 fixture、两 Work、独立 Review、Patch、Validator、Promotion、Final Validation 与 Final Report；短 Lease TTL + 慢 Review 连续 10 次通过。
- `git diff --check` 与模型核算残留审计：PASS。

## 审查发现与处置

- 初审发现 CLI 版本仍输出开发标记；已改为 `xgoal v0.1.0` 并补测试。
- 初审发现 Benchmark 声明的回归、恢复与无进展指标未从 runner sidecar 投影到 JSON/Markdown；已补齐严格字段、校验、渲染和测试，且不加入 Token/费用指标。
- 初审发现 FR-080 的 Work Item cancel 缺失；已补齐 Store 原子取消、精确运行中断、API/CLI 和历史保留测试。Required Work 被取消后 Goal 确定性进入 `WAITING`，不会保持不可推进的伪 `RUNNING`。
- 初审发现 Final Report 泛化字段名 `resources` 容易重新引入模型核算语义；已收敛为 `execution_metrics`，仅记录 wall time、Attempt 与 Human Gate。
- 初审发现技术 SPEC 的 Agent Event 示例仍残留 `Usage`；已从设计合同删除，Provider 原始用量字段在 Adapter 持久化前丢弃。
- 初审发现 Flaky Validator 虽已冻结在 Definition，但 Final Report 未披露；已将 flaky 标记纳入 Validator Trace/Markdown，并验证重跑使用不可覆盖的独立 Receipt。
- 初审发现 gitlink 已在实现中 fail closed，但缺少技术验收要求的显式 submodule 回归；已增加 `TestListTreeFailsClosedOnSubmoduleGitlink`。
- 最终 shuffle 暴露 Unix Socket E2E 只观察文件节点而未证明 listener 可连接的 readiness 竞态；测试已改为有界真实 Unix dial，并以 100 次随机运行通过后重入完整门禁。

## 审查结论

- Goal→Planner→Work Graph→Lease/Attempt→独立 worktree→Patch→Validator→独立 Review→Promotion→Final Validation→Final Report→Completion 已由单写 daemon 串联，Agent 自述、退出码或 Reviewer Claim 均不能单独推进完成。
- Final Report JSON/Markdown、哈希、SQLite Completion 和文件发布使用可恢复协议；rename 前后崩溃、篡改和未满足 Completion Predicate 均 fail closed。
- pause/resume/cancel/retry/replan 采用 CAS 并保留历史；活动 Lease 阻止无损 replan，Work cancel 不误停其他工作。
- 模型 Budget、Token/费用、余额与账单已从配置、公开协议、领域状态、Reconcile、API/CLI、SQLite 当前 Schema、报告和 Benchmark 移除；迁移 0007 删除旧草案表，历史 migration 保持不可变以继续检测 drift。
- 超时、输出字节上限、取消、单写并发、Lease Heartbeat 与 no-progress 仍作为执行安全边界，不具备模型计量语义，也不参与 Completion Predicate。
- README、架构、Threat Model、ADR、操作/恢复、Apache-2.0 和 Acknowledgements 与当前实现一致，没有把 L0 描述成强隔离，也没有发布未运行的 Benchmark 比较结论。

## Notes

- v0.1 只支持用户信任的本地 Git 仓库和 L0 Local Process Provider；容器/VM 级隔离、远端 Worker、生产自治和公开发布不在本 Objective。
- 真实三组 Benchmark 尚未执行，因此只能发布 Suite/Runner 合同，不能发布性能或成功率优势。
- 本次没有 push、merge、rebase、PR/Release、Benchmark 上传或生产动作。

## 结论

PLAN-007 的 Change Review 门禁成立，可以对账产品/技术 AC、关闭 `OBJ-001` 并创建单一工程意图的原子提交。
