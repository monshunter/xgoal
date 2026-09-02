# PLAN-004：实现 M3 Codex Adapter 与真实执行门禁

## 目标

把已验证的 M0–M2 Kernel 边界接到真实 Codex CLI：以非交互、可取消、可恢复且可审计的方式解析 JSONL 事件和结构化结果，并用本机真实 CLI 完成一个 Fast Goal 与一个 Standard Implementer Attempt。

## 范围

包括 Codex Passive/Active Probe、`codex exec --json --output-schema`、角色 Sandbox、原始事件脱敏与不可变引用、结构化结果校验、进程组取消、Session Resume 安全绑定、fixture contract 和真实 CLI smoke；不包括 Claude Adapter/Reviewer、完整 Reconcile/Gate/Budget、Daemon/API、Final Report 与 Benchmark。

## Phase 1：冻结 Codex 运行合同与设计

关联：[技术 SPEC M3](../../xgoal-technical-spec-v0.1.md#29-实施阶段)

- [x] 1.1 以当前安装的 Codex CLI 与 SPEC 冻结命令、Capability、JSONL、结果与错误分类合同
- [x] 1.2 完成 Adapter 生命周期、日志/凭据边界、Session 绑定和真实 smoke 设计并通过 Design Review

## Phase 2：实现 Codex Adapter

- [x] 2.1 实现未知事件兼容、截断拒绝、Usage/Claim 规范化、脱敏原始事件与严格 Agent Result 解析
- [x] 2.2 实现受控 `Start/Wait/Cancel`、角色 Sandbox、stdin Prompt、只读 Schema/Packet 和进程组回收
- [x] 2.3 实现无模型调用的 Passive Probe 与显式受预算约束的 Active Contract Probe
- [x] 2.4 实现 Session provenance 持久绑定与安全 `exec resume`，策略漂移或协议损坏时 Fail Closed

## Phase 3：真实执行与验收

- [x] 3.1 通过 fake Codex executable 的 fixture contract、失败注入与跨进程恢复测试
- [x] 3.2 使用真实 Codex CLI 完成一个 Fast Goal 和一个 Standard Implementer Attempt，并由 M2 Workspace/Patch/Scope/Validator 独立读回
- [x] 3.3 完成 M3 可复现验收入口、能力边界说明与 Change Review
