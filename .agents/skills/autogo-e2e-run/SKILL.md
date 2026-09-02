---
name: autogo-e2e-run
description: "运行真实用户或系统端到端场景并收集 Evidence；在前端功能验收、页面或组件交互、视觉与响应式变化、真实用户旅程、浏览器或桌面 UI 交互、核心或跨组件链路、单元与集成测试不足，或部署前后需要 Smoke/E2E 时使用。"
---
# autogo-e2e-run
## 目标
按真实用户或系统场景运行端到端验证，收集 Evidence，并对失败完成调查—修复—重验闭环。
## 输入与发现
- 验收场景、目标环境、测试数据和环境入口
- Progress 中当前 Objective 和 Plan；待验证 AC 和 Phase Items 从正式制品读取
- 相关 Spec、Plan、风险与恢复要求
- 存在时复用与待运行链路对应的 Scenario；一次性局部验证可直接从验收目标运行
- 用户显式指定的浏览器、Chrome Skill、Browser、Computer Use 或其他交互能力约束
- 目标 URL 或应用、登录/会话上下文，以及当前 Agent 实际可用的浏览器控制与桌面 UI 控制能力

## 输出与持久制品
- 场景级 PASS/FAIL/BLOCKED 结果
- 实际选择的交互能力、选择原因和显式工具约束满足情况
- revision、环境、URL 或应用、viewport、命令、日志、截图/录像引用和关键观察
- console/page/network 异常、API 或后台状态与清理结果
- 失败根因候选和下一路由
- 仅在场景通过后更新的 Spec AC、Phase Item 和对应 Evidence
- 有长期复用价值时更新 Scenario 与 Index；一次性验证只保存当前 Evidence

## 副作用与 Human Gate
可操作已授权测试环境、浏览器和桌面应用；使用已登录个人上下文、扩大网站/应用/系统权限、生产环境、破坏性清理、Secret 或不可逆操作必须满足对应 Human Gate。不得自行安装插件或修改用户个人全局 Harness。

## 执行步骤
1. 从 Progress 确认当前上层 Plan，再从正式 Plan/Spec 读取待验证 AC 和 Phase Items
2. 先判断场景是否会重复运行或需要跨会话交接；有复用价值时创建或更新 Scenario，一次性局部验证直接使用当前验收目标和环境事实
3. 判断真实 UI 是否为用户结果的必要 Evidence；前端功能、页面/组件交互、视觉、响应式、浏览器兼容、真实旅程或截图验收需要真实 UI E2E，纯代码/API/文档结果不得机械调用 UI 能力
4. 从当前 Agent 的 Skills、plugins、tools 和环境事实解析实际可用能力，预检目标 URL 或应用、revision、环境、登录态、网站或应用授权、系统权限与连接状态；不假设某个 Agent 专属插件存在，不读取或记录 Secret
5. 先应用显式工具约束：用户指定的 Chrome、Browser、Computer Use 或等价能力是硬约束；对应能力不可用、未授权或无法连接时返回 `BLOCKED` 和精确恢复条件，不得静默替换
6. 未指定工具时选择最小充分能力：纯 Web 交互使用专用浏览器控制；依赖已有标签页、登录态、browser profile 或扩展时使用外部浏览器控制；本地 Web 使用当前 Agent 推荐的隔离浏览器能力；桌面应用、系统对话框、文件选择器或跨应用步骤才使用桌面 UI 控制。只有场景同时跨越 Web 与非浏览器 UI 时才组合两类能力，并在 Scenario 中划分各自步骤
7. 通过 autogo-env-manage 准备隔离环境和测试数据，确认起始状态后运行场景，记录时间、revision、输入、实际交互能力、选择原因、URL/应用和 viewport
8. 同时验证用户可见结果、console/page/network 异常、API 或后台状态；截图只证明画面，不单独证明完整链路
9. PASS 后才勾选对应 Phase Items 并按 Spec 规则更新 AC；FAIL/BLOCKED 保持未完成并记录 Evidence
10. 应用行为失败时保留 Evidence，转 autogo-investigate/implement；修复后必须重新运行同一验收场景，不以低层测试代替重验
11. 测试结束清理临时资源，更新 Scenario、正式制品和 Index；最新 Evidence 只在 Scenario 中引用，不复制 Operation 日志，不把场景细节写入 Progress

## 验证与完成
- 场景可复现且与验收标准对应
- 有长期复用价值的 E2E 与 Scenario 可追溯；一次性局部验证不被强制创建文档
- 实际交互能力符合场景需求和显式工具约束；未指定工具时只使用最小充分能力
- Evidence 足以区分应用失败和环境失败
- Evidence 记录 revision、环境、URL/应用、viewport、截图、console/page/network、API/后台与清理边界
- 未使用伪造截图、mock、单元/集成测试或仅凭 UI 表象宣称真实 E2E 通过
- Spec AC、Phase Items 与场景结果一致；Progress 只在整个上层 Plan 完成后勾选对应项

## 失败、重试与幂等
必需交互能力、连接、权限、认证或环境不可用时 `BLOCKED`，显式工具约束不得 fallback；应用失败转 autogo-investigate/implement 后必须重新运行同一验收场景。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
