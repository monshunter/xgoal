# SCN-001：Agent 参考完整样例创建 Spec

> 这是真实 E2E 场景文档的结构样例。代码测试、fixture 或 mock backend 不能替代运行中系统的 E2E 证据。

状态：`ready`

关联验收：[SPEC-001 AC-003](../specs/SPEC-001.md#验收标准)

## 用户与系统目标

用户只描述“为上传失败补充可验收规范”。Agent 应从已安装参考资料理解合格 Spec 的写法，并基于项目事实创建文档，不要求用户填写模板变量。

## 前置状态

- 角色：已获得项目写入授权的维护者。
- 用户结果：无需填写模板变量即可获得可验收 Spec。
- AutoGo 已安装到测试项目。
- `.autogo/templates/documents/spec.md` 存在且可读。
- 项目已有 `PROGRESS.md` 和当前 Plan。
- `docs/specs/` 工作区已初始化。

## 交互能力

- 目标界面：不适用；本场景只验证 Agent 创建项目文档，不要求浏览器或桌面 UI。
- 显式工具约束：无。
- 必需能力：项目文件与命令入口。若场景包含真实 UI，本节必须改为实际 URL 或应用、登录上下文、浏览器控制或桌面 UI 控制需求，并保留用户显式指定的工具。

## 操作步骤

1. 用户向 Agent 描述上传失败的目标和约束。
2. Agent 从 Progress 确认当前 Objective/Plan，再读取 Plan、代码事实和 Spec 参考样例。
3. 若行为合同确需长期维护，Agent 创建或更新 Spec，写明正常行为、失败边界、非目标和验收项；否则直接更新代码与测试事实 owner。
4. Agent 更新当前 Plan 和文档索引；仅在 Objective 状态或内嵌 Plans Checklist 变化时更新 Progress。

## 预期结果

- UI：不适用；本场景不声称浏览器结果。
- API/CLI：Agent 创建的新 Spec 不包含未解析变量或示例项目事实。
- 文档状态：存在 Spec 时，每个验收项可通过 API 或浏览器行为验证，没有证据的验收项保持未完成。
- 后台状态：Progress 只在 Objective 或 Plans Checklist 变化时更新，Index 与正文一致。

## 自动化入口

执行 Harness 安装 acceptance；不得只凭代码测试宣称真实用户场景通过。

## 证据

最新 Evidence 只引用对应 Operation 或 Review：保存 Agent 创建或更新的 Spec 路径、当前 Plan diff 和索引 diff；若 Objective 状态或内嵌 Plans Checklist 变化，同时保存 Progress diff。若执行了真实 UI 验收，再记录实际交互能力、选择原因、revision、URL/应用、viewport、截图、console/page/network 异常、API/后台状态与清理结果，不复制原始日志。

## 清理

测试项目位于隔离的 `temp/`。场景结束后保留用户要求的验收制品；不删除源码仓库或无关项目数据。

失败时恢复隔离测试项目到场景开始前快照；无法证明清理完成时标记 `BLOCKED`，不继续复用污染环境。
