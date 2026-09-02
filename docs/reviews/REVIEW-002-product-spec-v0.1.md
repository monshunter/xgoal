# REVIEW-002：xgoal 产品 SPEC v0.1

## 审查对象

- Spec：`xgoal-product-spec-v0.1.md`
- Revision：v0.1 Draft，SHA-256 `1a1ef571c9172b158e042c3045b53b8055d31b64f040a6b2ee77b8cfbcbf0eb2`
- Plan：`PLAN-001` Phase 1

## Verdict

`FAIL`

产品目标、责任边界、FR 和 v0.1 范围总体一致，但当前验收合同不足以证明“全部 feature 已完成”，不能直接进入实现基线。

## 发现

### High：发布验收项缺少稳定 AC ID

第 17 节的验收 Checklist 没有稳定标识；第 8.1 节的 `AC-001` 只是 Goal Contract 示例，不能作为 xgoal v0.1 自身的规范 AC。这样无法稳定建立 `FR → Work Item → Validator → Evidence → Final Report` 的追溯链，也无法在条目增删后辨认旧证据是否仍有效。

修复方向：为 v0.1 发布验收项分配稳定、唯一的 AC ID，并明确这些 ID 是实现与最终报告的规范入口。

### High：FR 与发布验收清单之间没有完整覆盖映射

第 10 节定义了 `FR-001` 至 `FR-091` 共 24 个 Feature Requirement，但第 17 节只有 20 条未编号结果，且没有说明 `init`、`doctor`、Planner/Plan Revision、环境准备、Validator Registry、失败归因、Gate 内容、生命周期、状态/日志/报告 CLI 等要求由哪条验收与 Validator 覆盖。仅通过发布清单无法区分“实现了核心闭环”和“实现了全部 v0.1 feature”。

修复方向：增加完整的 FR→AC 追溯表；缺失的用户可观察验收应补充 AC，不以一个宽泛 E2E 代替所有行为。

### Medium：“公开 Benchmark Suite”超出当前本地交付边界

第 17 节要求“公开 Benchmark Suite”，而 v0.1 非目标明确不自动 push、发布制品或执行外部发布。当前用户授权的是本地仓库实现与验收，没有外部发布授权。

修复方向：把 v0.1 可验收结果定义为“仓库内固定 Commit/fixture、可复现的 Benchmark Suite”；真正对外公开保留为单独发布动作和 Human Gate。

### Medium：可选 AutoGo 集成与强制 Benchmark 对照需要分开表述

`G-010` 和 P0 将 AutoGo 集成定义为可选，但发布验收要求必须比较 AutoGo 单 Agent。运行时集成是否可选、Benchmark 中的 AutoGo 对照是否必需没有明确分层。

修复方向：明确“运行时调用/安装 AutoGo”为可选 feature；Benchmark 的 AutoGo 对照是独立的发布评估输入，不要求 xgoal 运行时依赖 AutoGo。

## 下一路由

继续执行 `PLAN-001` 的 `1.2`，先完成技术设计审查；随后在 `1.3` 使用 `autogo-spec-write` 修复上述规范 owner，并重新执行 Spec Review。
