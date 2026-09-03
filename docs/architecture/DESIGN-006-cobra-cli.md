# DESIGN-006：Cobra CLI 迁移设计

## 目标与约束

以 Cobra v1.10.2 替换 `internal/cli` 的手写顶层 `switch`、嵌套命令判断和线性 flag 扫描，同时满足 [SPEC-001](../specs/SPEC-001-cobra-cli.md)。迁移必须保持单一 `xgoal` 二进制、Unix Socket API 边界、命令输出与退出语义，不引入第二套业务执行路径。

## 现状

`cmd/xgoal/main.go` 调用 `cli.Run(args, stdout, stderr)`。当前 `internal/cli/cli.go` 同时承担命令发现、usage 文本、flag 扫描、参数校验、API request 构造和运行执行；新增命令需要修改多个 `switch`，嵌套层级没有独立 help，未知 flag 在部分路径可能被忽略。

## 组件与依赖方向

```text
cmd/xgoal/main.go
  -> cli.Run(args, stdout, stderr) int
       -> Cobra root / typed command tree
            -> local handlers (init/config/benchmark/daemon)
            -> API request builders
                 -> shared API executor
                      -> internal/api.Client
```

- `Run` 保持现有签名，负责注入进程 args/stdio、执行 root、打印一次错误并返回稳定退出码。
- root 负责产品描述、版本模板、命令分组、标准 help/completion 和全局错误策略，不包含业务 `switch`。
- 每个可执行命令拥有自己的 typed option struct、位置参数约束、flag 定义和 `RunE`；父命令只组织子命令。
- 本地 handlers 继续调用现有 `projectinit`、`config`、`benchmark` 和 `app.Serve`。
- API request builders 从 typed options 生成 method/path/body/watch/wait；共享 executor 唯一拥有 client 调用、幂等键、timeout、stream、pretty JSON 和状态退出码。

依赖只从 CLI 指向现有包；domain/API/daemon 不反向依赖 Cobra。

## 命令树

```text
xgoal
├── init
├── doctor
├── run
├── status
├── logs
├── gates
├── approve
├── pause | resume | cancel
├── report
├── clean
├── config validate
├── benchmark validate | run
├── daemon serve
├── goal get | replan | finalize
├── work list | retry | cancel
├── version
├── completion bash | zsh | fish | powershell
└── help [command]
```

Cobra 默认生成 help 和 completion；已知枚举使用 flag completion，ID/path 等自由值不伪造动态查询。

## 执行与错误模型

1. Cobra 在 `RunE` 前处理命令、flag 和位置参数。root 使用 `SilenceErrors`、`SilenceUsage`，避免库和 `Run` 重复打印。
2. CLI 内部错误携带稳定退出码。解析/校验错误映射为 2；本地内部失败为 5；API transport 为 6；HTTP/Goal 状态继续由既有映射产生 0/2/3/4/5/6/7。
3. help、version 和 completion 直接写 root stdout 并返回 nil，不创建 API client。
4. API executor 只在命令完成本地校验和输入读取后创建 client；GET 不生成 key，mutation 在发送前生成 key。
5. watch、wait 和 daemon handler 使用 signal context；其他 API 请求保持既有有界 timeout。
6. `benchmark run` 开启 interspersed flag parsing，但通过 Cobra 的 dash index 只把 `--` 后内容交给 runner，禁止将 runner flags 解析为 xgoal flags。

## 可测试性

- 命令构造接收 stdin/stdout/stderr 和 API client factory；生产 `Run` 注入 `os.Stdin` 与当前项目 client。
- request builder 保持纯函数，以表驱动测试覆盖全部 method/path/body/watch/wait。
- fake client 驱动命令级测试，证明校验发生在 client 创建前，并覆盖 stdout/stderr 与退出码。
- 真实 CLI smoke 继续从 `cmd/xgoal` 运行 version/config/benchmark；增加 root/subcommand help 与四种 completion smoke。

## 兼容与迁移

- 一次性替换内部 CLI 解析，没有数据或配置迁移；`cmd/xgoal/main.go` 和 `cli.Run` 调用合同不变。
- 现有命令名、位置参数、flag 名、API 请求、JSON 输出和退出码不变。
- 允许的用户可见差异仅限 Cobra 标准 help、usage、未知命令建议和更严格的未知/多余参数拒绝。
- README 以实际 Cobra help 为准更新入口，不改历史 v0.1 验收记录。

## 失败、恢复与回滚

- 所有解析和本地输入校验在外部副作用前完成；completion/help/version 无副作用。
- API 与 daemon 失败沿用现有安全边界，不因 Cobra 自动重试。
- 若迁移回归，单个原子提交可恢复旧 `internal/cli` 和移除 Cobra 依赖；没有持久状态需要回滚。

## 关键权衡

- 选择 Cobra 默认 help/completion，而非自定义模板或第三方样式层，以减少长期维护并保留生态一致性。
- 保留 `cli.Run` 的 int 返回值，而非让 `main` 直接 `Execute`，以维持测试入口和公开退出码合同。
- 不引入 Viper；本 Objective 只解决命令模型，不建立新的配置事实源。

## 上游依据

- Cobra 官方仓库与 User Guide：标准嵌套命令、help、version、参数校验和 shell completion。
- Cobra v1.10.2 是实施时上游最新 release；依赖通过 Go module checksum 固定。
