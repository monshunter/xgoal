# PLAN-007：实现 M6 Final Report、Benchmark 与 v0.1 发布验收

## 目标

在 M0–M5 的执行、证据和恢复能力上完成最终验证/报告原子协议、可复现三组 Benchmark、Threat Model/ADR/许可证/归属与产品 AC 对账，使 v0.1 只在当前 Evidence 闭环时发布。

## 范围

包括 Markdown/JSON Final Report、报告文件崩溃恢复与 Completion 事务；固定 Benchmark fixture、A/B/C runner、公平性校验和实际执行指标；README、Threat Model、ADR、Apache-2.0、Acknowledgements、操作/恢复文档；产品 SPEC AC-FR/AC-NF 逐项验收；从 v0.1 契约、配置、状态、报告、Benchmark 与实现中移除模型 Token/费用核算。远端发布、push、托管 Benchmark 和 v0.2 容器 Provider 不在范围。

## Phase 1：冻结最终闭环与发布合同

关联：[技术 SPEC 21–22、25、28–30 与 M6](../../xgoal-technical-spec-v0.1.md#21-completion-predicate)

- [x] 1.1 冻结 Final Validation→Report→Completion 的原子状态、文件落盘、哈希读回与恢复合同
- [x] 1.2 冻结 Benchmark A/B/C 公平性、fixture、执行指标、发布 AC 与文档/许可证边界并通过 Design Review

## Phase 2：实现 Final Report 与恢复

- [x] 2.1 实现版本化 Final Report 模型、AC→Evidence/Validator/Gate/Finding/执行统计/限制映射及 Markdown/JSON 确定性渲染
- [x] 2.2 实现私有临时文件、内容哈希、SQLite 报告状态与 Goal Completion 单事务提交
- [x] 2.3 实现 rename 前后崩溃恢复、文件篡改拒绝、API/CLI 报告读回和安全 clean 引用保护
- [x] 2.4 通过 Final Tree/Evidence/Gate/Finding/Human Acceptance/Report 组合与故障注入测试

## Phase 3：实现 Benchmark Suite

- [x] 3.1 实现固定 task fixture、suite schema 与初始内容哈希，覆盖 Bug/Feature/Refactor/Environment/Recovery/Multi-module
- [x] 3.2 实现 Native、AutoGo single-agent、xgoal 三组 runner 和相同输入/验收/逐任务超时的确定性公平性检查
- [x] 3.3 实现实际结果采集、失败/人工介入保留、False Completed 判定与 JSON/Markdown 输出
- [x] 3.4 通过无模型 fixture dry-run/合同测试并记录未执行真实对照时不得发布比较结论

## Phase 4：发布文档与全量验收

- [x] 4.1 补齐 daemon 的单写者执行引擎，将 READY Work 串联到 Agent、Patch、Validator、独立 Review、Promotion、下一 Work 与 Final Validation/Report
- [x] 4.2 以受控本地 Provider CLI fixture 运行 Goal→多 Work→独立 Review→Promotion→Final Report 的真实 Unix Socket E2E，并覆盖失败/Gate/恢复边界
- [x] 4.3 完成 README、Threat Model、ADR、操作/恢复、Apache-2.0 License 与 Acknowledgements
- [x] 4.4 移除 Budget、Token/费用采集与核算的配置、协议、状态、API、持久化、报告、Benchmark、测试和文档表面，并保留超时、输出字节上限、取消与无进展安全边界
- [x] 4.5 逐条执行并记录产品 SPEC AC-FR/AC-NF 的当前 Evidence、PASS/FAIL/NOT_RUN 与边界
- [x] 4.6 执行最终 required validators、故障矩阵、race/shuffle/cross-build、已记录真实 CLI/Agent 链路与无虚构指标审计
- [x] 4.7 完成 M6 Change Review、关闭 OBJ-001 并创建原子提交
