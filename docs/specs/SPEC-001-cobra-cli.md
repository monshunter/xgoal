# SPEC-001：Cobra CLI 行为规范

## 目标

将 xgoal 的公共命令入口统一为一棵可发现、可组合的 Cobra 命令树。用户应能从任意层级获得准确帮助，使用标准 flag 语法和 shell completion，并继续依赖既有命令的请求、输出和退出语义。

## 范围

- root 命令，以及 `init`、`doctor`、`run`、`status`、`logs`、`gates`、`approve`、`pause`、`resume`、`cancel`、`report`、`clean`、`version`。
- `config validate`、`benchmark validate|run`、`daemon serve`、`goal get|replan|finalize`、`work list|retry|cancel` 子命令。
- 命令和 flag 帮助、参数校验、命令建议、版本输出、Bash/Zsh/fish/PowerShell completion。
- 现有 Unix Socket API、JSON/text 输出、信号处理和进程退出码的兼容。

## 非目标

- 不改变 daemon、HTTP API、domain、SQLite、配置文件或 Benchmark 数据语义。
- 不新增现有清单之外的业务命令、远端操作、生产行为或隐式副作用。
- 不引入交互式 TUI、颜色主题、全局配置框架或额外插件系统。
- 不承诺旧手写 usage 文本逐字节不变；帮助布局和参数错误允许采用 Cobra 的标准表达。

## 行为规范

### 命令发现与帮助

1. 无参数、`help`、`--help` 和 `-h` 必须在 stdout 输出 root 帮助并退出 0。
2. 每个含子命令的节点必须列出可用子命令；每个可执行命令必须展示用途、位置参数和具名 flags。
3. `xgoal help <path>` 与 `xgoal <path> --help` 必须展示同一命令层级的帮助并退出 0，且不得连接 daemon 或执行命令副作用。
4. 未知命令或未知 flag 必须在 stderr 给出明确错误；相近命令可给出建议，并以退出码 2 结束。
5. `xgoal version`、`xgoal --version` 和 `xgoal -v` 必须继续输出 `xgoal v0.1.0`。

### 参数与输入

1. 位置参数数量由对应命令显式约束，缺少或多余参数均视为 usage error。
2. 必填 flag、枚举、正整数和正 duration 在发起任何 API/文件副作用前完成校验。
3. `run` 必须且只能接收 `--goal` 或 `--goal-file` 之一；`--goal-file -` 继续从 stdin 读取至多 1 MiB。
4. `benchmark run` 的 runner argv 必须位于 `--` 之后并保持原始顺序和值。
5. 已移除的模型计量 flags 继续 fail closed，不得被 Cobra 的未知 flag 或透传机制重新接受。

### 执行与兼容

1. 现有命令必须继续生成相同 HTTP method、path 和 JSON request body；GET 不生成幂等键，mutation 继续生成请求 ID。
2. 成功响应继续写 stdout，HTTP 错误响应和诊断写 stderr；JSON 继续使用两空格缩进。
3. `status --watch`、`run --wait` 和 `daemon serve` 继续响应 interrupt/SIGTERM；watch 与等待状态不得被命令解析层吞掉。
4. 退出码保持：成功 0、usage/invalid request 2、Waiting 或限流 3、Cancelled 4、内部/验收失败 5、API/daemon transport failure 6、forbidden 7。
5. `init` 继续直接初始化当前项目；其余 API 命令继续通过当前项目解析出的 Unix Socket 通信。

### Completion

1. `xgoal completion bash|zsh|fish|powershell` 必须将脚本写到 stdout，并且不连接 daemon、不修改项目。
2. completion 必须覆盖命令、子命令、flags 和已知枚举值；生成失败属于内部错误，不得输出半成功提示。

## 验收标准

- [x] **AC-CLI-001**：root 帮助展示完整顶层命令树，所有嵌套命令均可由 `help` 和 `--help` 到达。
- [x] **AC-CLI-002**：未知命令、未知 flag、缺失/多余位置参数、缺失必填 flag 与非法类型在副作用前失败并退出 2。
- [x] **AC-CLI-003**：全部现有 API 命令的 method、path、body、watch/wait 行为和输出流保持兼容。
- [x] **AC-CLI-004**：`version` 命令和 root version flags 保持 `xgoal v0.1.0`，四种 shell completion 可生成非空脚本。
- [x] **AC-CLI-005**：0/2/3/4/5/6/7 退出码映射、JSON pretty print、stdin Goal 与信号取消均有当前测试或运行 Evidence。
- [x] **AC-CLI-006**：CLI smoke、全包测试、race/shuffle/vet 和 Linux/Darwin 构建通过，README 与实际帮助一致。

## 假设与开放问题

- 当前命令名和参数名是兼容基线；本 Objective 不重命名或删除命令。
- Cobra 默认帮助与 completion 是可接受的现代化基线，不需要自定义终端样式。
- 当前没有需要用户选择的开放产品语义。
