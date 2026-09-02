---
name: autogo-deploy
description: "按当前路由完成预检、部署、健康验证和必要恢复；在发布或重部署到本地、测试、预发、生产环境，以及部署失败后的恢复时使用；调用本 Skill 不自动进入 Standard。"
---
# autogo-deploy
## 目标
在明确目标、审批、验证和回滚的前提下部署组件，并以服务健康和场景验证而非命令退出码判断成功。
## 输入与发现
- 当前路由；Fast 从用户授权和轻量目标读取部署任务，Standard 从 Progress、当前 Objective、Plan 和 Phase Checklist 读取
- 批准的版本/Diff、目标环境、部署入口
- 预检、备份、回滚、健康检查和 Smoke/E2E 方案

## 输出与持久制品
- 部署记录、版本和环境
- 健康检查、Smoke/E2E 与观察 Evidence
- 失败回滚结果和剩余风险
- Standard 已回写的 Spec AC、Phase Item 和部署记录；Fast 的当前 Evidence 和根合同收口入口

## 副作用与 Human Gate
部署会改变目标运行状态，但该事实本身不决定 Standard。仅应用已批准镜像或配置、不改变产品定义行为，且明确、局部、可逆、可验的部署一律使用 Fast；未获授权的生产动作，以及数据、权限、Secret、破坏性、不可逆和公开发布边界按自身风险路由并在需要时满足 Human Gate。

## 执行步骤
1. 确认当前路由；Fast 从用户授权和轻量目标确认部署任务，Standard 从 Progress、当前上层 Plan 和 Phase Checklist 确认，并从正式制品确认 Human Gate
2. 确认目标环境、版本、配置和变更范围；行为不变部署不因环境名称或运行状态变化升级 Standard，独立风险导致 Fast 准入失败时返回根合同重路由
3. 执行预检、Diff/Dry-run、容量和依赖检查
4. 为不可逆部分建立备份和回滚点
5. 在 Human Gate 之前完成所有可安全准备并回写等待状态
6. 获得必要批准后执行部署
7. 验证健康、指标、日志和核心场景
8. 失败立即停止扩散并进入 Reconcile；真实状态受损或继续会扩大影响时执行已准备的回滚、补偿或隔离，否则保留现场调查并修复后重验
9. 只有部署后验证通过才完成部署；Fast 返回根合同对账，Standard 才勾选对应 Phase Items 并按 Spec 规则更新 AC；不把部署细节写入 Plan 或 Progress

## 验证与完成
- 实际运行版本与目标一致
- 健康检查和核心场景通过
- 无新增严重告警或数据异常
- 恢复路径可用；需要持久审计的边界记录完整
- Fast 未创建或要求 Objective、Plan、Review 或 Standard 部署制品；Standard 的部署状态与 Spec AC 和 Phase Checklist 一致，Progress 只在整个上层 Plan 完成后勾选对应项

## 失败、重试与幂等
部署命令成功但健康失败仍视为失败；普通失败不自动回滚，只有止损或恢复条件成立时执行恢复。Fast 发现产品行为、数据、跨边界拓扑、依赖、安全或恢复条件变化时保留 Evidence 并返回根合同升级 Standard；回滚失败时立即升级并保留现场。
- 重复执行前读取当前文件、Git 和运行状态；不重复创建已存在制品或重复执行已生效副作用。
- 相同失败再次出现时停止机械重试，回到 `autogo-investigate` 或上层设计。
- 状态和文档由 Agent 自动维护，不要求用户执行 Harness CRUD 命令。
