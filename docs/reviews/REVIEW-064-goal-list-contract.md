# REVIEW-064：Goal 列表合同 Review

审查对象：PLAN-018 1.1，产品 FR-090A / AC-GL-001–004 与技术 Spec 第 23.4 节。以下为最终行为合同及已补 Evidence 链接的内容身份。

- `xgoal-product-spec-v0.1.md` SHA-256 `b57eaca9faa2bce8a36957d720de48c86afbcd363ce42e7a805974cdaa1294df`。
- `xgoal-technical-spec-v0.1.md` SHA-256 `abc48e81c5d1536322cf80d4be00aeae11cf1c269e48c29147767417f8c40afc`。

## Verdict

**PASS**。集合查询提供完整 ID 与当前状态，分页约定足以发现静态项目全部 Goal，明确非快照边界；状态筛选不混淆 Goal 与 Planning 状态。有界摘要、空结果、非法输入、旧 daemon 错误、公开脱敏与无写入均可由 API/CLI 和 demo5 对账验证。

现有状态、活动 goal_revisions 和 planning effect 已拥有数据，无需 Schema、domain 状态机、Provider 或架构变更。planning_state 复用当前单 Goal 详情的优先级，包含冻结后 SUCCEEDED、历史缺失请求 WAITING、暂停及恢复，不产生第二套生命周期语义。

游标边界经独立 Reviewer 复核：Goal 创建体允许 1 MiB，现有 ID 校验允许长 ID 和内部 tab，而 daemon 请求头限制 32 KiB。仅把完整 ID 放进游标会阻断合法目标的遍历。最终采用不超过 128 字节的无状态 rowid + 完整 ID SHA-256 定位游标；有游标时增加一次只读定位，仍按 ID keyset 顺序查询。定位缺失或变化明确报错并从首页刷新，不引入游标表、签名框架或更大的请求头，不承诺维护后 rowid 永恒不变。

验收须覆盖长 ID/控制字符、损坏/失效游标和真实 CLI 分页。当前 Spec AC 的完成状态由 Operation 与 Change Review Evidence 拥有，本文的 PASS 只批准合同及其增量完善，不代替功能交付。
