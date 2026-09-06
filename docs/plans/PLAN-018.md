# PLAN-018：项目 Goal 列表与状态总览

目标：用户无需预先知道 Goal ID，即可发现项目内的全部 Goal 并查看当前状态。

范围：只读 Goal 集合 API、稳定分页、状态筛选、CLI JSON/human 输出及文档和验收；保持已有命令合同与 demo5 数据，不触发 Goal 执行或 Provider 调用。

## Phase 1：明确列表合同

合同：[产品 Spec](../../xgoal-product-spec-v0.1.md)、[技术 Spec](../../xgoal-technical-spec-v0.1.md)。

- [x] 1.1 明确字段、分页、筛选、异常及兼容边界并完成 Spec Review。

## Phase 2：实现完整查询入口

- [x] 2.1 实现有界、无遗漏的 Goal 集合查询与 API，验证状态、描述和分页边界。
- [x] 2.2 实现 goal list 的 JSON/human 输出、参数校验和补全，更新用户说明并验证既有入口兼容。

## Phase 3：真实验收与收口

- [x] 3.1 运行定向回归和真实 CLI/daemon 场景，并在 tmp/demo5 对账已有 Goal 与数据保留。
- [x] 3.2 完成独立 Change Review、制品对账和原子提交。
