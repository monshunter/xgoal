<!-- AGENT-HARNESS:BEGIN DOCS-README -->
# Agent-managed project knowledge

本目录由项目与 Agent 共同维护，用于保存长期有效的 Spec、设计、决策、计划、复盘和运行知识。

- 安装器一次性初始化全部标准子工作区，每个工作区自带 `README.md` 和 `INDEX.md`；
- 初始化文件只在缺失时创建，随后归项目所有，重复安装不覆盖已有内容；
- 用户通过自然语言提出目标，无需运行文档 CRUD 命令；
- Agent 根据 Fast / Standard 路由、实际风险信号和任务上下文自动创建、更新、索引和归档制品；Fast 不创建 Objective、Plan 或 Review；
- 根 `PROGRESS.md` 只追踪 Objective 三态和内嵌 Plans Checklist；正式 Plan 使用按 Phase 组织的单文件 Checklist，不拆分第二份状态文件；
- Spec、Design、Scenario、Journal 与 Evolution 只在当前任务确有长期价值时创建，并沿用项目既有文档约定；
- 完整参考样例由 AutoGo 安装到 `.autogo/templates/`，Agent 结合真实项目事实仿写，不复制样例事实。
<!-- AGENT-HARNESS:END DOCS-README -->
