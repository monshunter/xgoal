# REVIEW-035：SPEC-001 Cobra CLI 行为规范

## 审查对象

- Spec：`SPEC-001-cobra-cli`
- Objective：`OBJ-002`
- Plan：`PLAN-008` Phase 1.1
- Revision：初始版本
- 事实基线：当前 `internal/cli/cli.go`、CLI/request tests、README 与 Cobra 官方命令/帮助/completion 能力

## Verdict

`PASS`

## 审查结论

- Spec 枚举了当前实现的全部顶层和嵌套命令，明确保留 HTTP 请求、输出流、信号与退出码，并允许帮助布局采用 Cobra 标准表达。
- root/subcommand help、未知命令与 flag、位置参数、必填/枚举/类型校验、stdin、runner argv 和移除 flags 都有可观察的失败边界。
- completion 明确覆盖 Cobra 支持的 Bash、Zsh、fish、PowerShell，且生成过程不得连接 daemon 或修改项目。
- 六条 AC 可由命令测试、API fake、CLI smoke 和项目既有全量门禁分别验证，没有把实现文件结构写成产品要求。
- Spec 只拥有本 Objective 新增的 CLI 交互合同；既有产品/技术 Spec 继续拥有 daemon、API、domain 和持久化语义，不形成双重事实源。

## 下一路由

允许进入 `PLAN-008` Phase 1.2，设计最小 Cobra 命令组件、执行/错误边界与可回滚迁移路径。
