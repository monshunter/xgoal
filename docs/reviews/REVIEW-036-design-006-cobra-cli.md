# REVIEW-036：DESIGN-006 Cobra CLI 迁移设计

## 审查对象

- Design：`DESIGN-006-cobra-cli`
- Spec：`SPEC-001-cobra-cli`
- Objective：`OBJ-002`
- Plan：`PLAN-008` Phase 1.2
- Revision：初始版本

## Verdict

`PASS`

## 审查结论

- 设计以一棵 Cobra 命令树替换手写分派，但保留 `cli.Run`、现有业务包和唯一 API executor，依赖方向单向且没有第二套行为事实源。
- typed options、Cobra Args/flag 校验和带退出码错误把解析失败稳定地约束在外部副作用之前；help/version/completion 不创建 API client。
- method/path/body、幂等键、JSON 流、watch/wait、signal context 和 0/2/3/4/5/6/7 退出码仍由共享执行边界统一负责，满足 Spec 兼容要求。
- Cobra 默认 help/completion 加少量枚举 completion 已足够满足目标；不引入 Viper、自定义渲染层或动态 daemon 查询，复杂度最小充分。
- 迁移没有持久数据或配置变化，失败时可用单个原子提交回滚；测试路径同时覆盖纯 request builder、fake client 命令行为和真实二进制 smoke。

## Notes

- 实现必须验证 `benchmark run -- <argv...>` 的 dash 边界，避免 pflag 吞掉 runner 参数。
- root version template 需要显式保持 `xgoal v0.1.0`，不能接受 Cobra 默认模板的额外单词。

## 下一路由

允许进入 `PLAN-008` Phase 2，使用 TDD 迁移完整命令树。
