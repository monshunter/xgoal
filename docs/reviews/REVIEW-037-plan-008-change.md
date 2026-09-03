# REVIEW-037：PLAN-008 Cobra CLI Change Review

## 审查对象

- Objective：`OBJ-002`
- Plan：`PLAN-008`
- Spec：`SPEC-001-cobra-cli`
- Design：`DESIGN-006-cobra-cli`
- Revision：`feat/cobra-cli` 当前工作树
- 范围：Cobra 命令树、typed flags/args、API request 执行边界、help/version/completion、README 与 CLI smoke

## Verdict

`PASS`

## 当前 Evidence

- `go test ./internal/cli -count=1`：PASS。覆盖完整命令清单、root/嵌套 help、未知 command/flag、typed 参数校验、stdin、全部 API method/path/body、幂等键、watch/wait、0/2/3/4/5/6/7、输出流、取消上下文、四种 completion 和 benchmark dash 透传。
- `make verify-m6`：PASS。覆盖 gofmt、全包测试、shuffle×20、全包 race、vet、CLI/config/benchmark smoke、SQLite/daemon 跨平台编译、M2 故障矩阵、M3/M4 contract、M5 safety、M6 release 与 Linux amd64 / Darwin arm64 二进制构建。
- `go mod verify`：PASS；直接依赖固定为 `github.com/spf13/cobra v1.10.2`，传递依赖由 `go.sum` 校验。
- 手写解析残留审计：`internal/cli` 与 `cmd/xgoal` 中不存在 `commandRequest`、`option`、`hasFlag`、`switch args[0]` 或 `printUsage`。
- `git diff --check`：PASS；文档索引由当前文件和一级标题重建。

## 审查结论

- `cli.Run` 仍是稳定进程边界，内部只有一棵 Cobra tree；本地命令、API 命令和 nested resource command 均没有回落到旧参数扫描器。
- Cobra 在 handler 前完成 command/flag/position/required/type 校验，业务约束在 client 创建前完成；help、version 和 completion 无 daemon 连接或项目副作用。
- 共享 API executor 继续唯一负责 Unix Socket client、mutation request ID、timeout、signal、stream、pretty JSON、wait polling 和稳定退出码，没有改变 daemon/API/domain/SQLite 契约。
- `benchmark run` 使用 dash index 强制 `-- <argv...>`，runner argv 顺序和值由测试证明不被 pflag 解析。
- 新增帮助与 completion 写入长期 CLI smoke，README 与实际输出一致；未引入 Viper、TUI、自定义样式层或范围外命令。
- Diff 只包含 CLI 实现/测试、Cobra module 依赖、README/Makefile 和本 Objective 的正式制品，没有无关工作树内容。

## Notes

- 本次未重跑真实外部 Codex/Claude Provider smoke；变更不触及 Adapter 或模型执行链路，当前 hermetic contract、daemon/API 集成和跨平台构建已覆盖受影响边界。
- 未执行真实三组 Benchmark 比较；`benchmark validate` 保持 `execution=NOT_RUN`，本 Objective 不发布比较结论。

## 下一路由

允许完成 `PLAN-008` Phase 3.2，并进入 `autogo-change-close` 对账、关闭 Objective 和创建原子提交。
