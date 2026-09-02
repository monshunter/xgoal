## AutoGo 项目上下文参考样例

> 这是一份“有具体事实的项目上下文”示例，不是让 Agent 复制到其他项目的标准答案。真实初始化必须从目标项目 README、代码、测试、配置和运行结果重新取证。

### 1. 项目使命与当前阶段

- 项目使命：把一套 Agent-neutral Harness 安全安装到 Codex 或 Claude Code 项目中，让原生 Agent 直接发现 Skill，并通过项目本地治理和文档完成工作闭环。
- 当前阶段：外置 Harness 资源包与 Go 安装器已经形成，正在收敛安装布局、参考资料和长期升级合同。
- 当前最高优先级：保证安装、更新、卸载、冲突保护和真实黑盒验收使用同一套当前语义。

### 2. 核心用户、场景与非目标

- 核心用户：希望在现有代码库中使用完整 Harness、但不想学习额外任务 CRUD 的项目维护者。
- 核心场景：从仓库根安装 `autogo`，再对一个现有或新项目执行 Codex 默认安装或显式 Claude Code 安装。
- 当前非目标：不充当长期运行的任务控制面，不替代原生 Agent，不提供 Windows 支持，不把项目文档上传到外部服务。

### 3. 目录与语义组件地图

| 路径 | 组件职责 | 本地事实源 | 构建/测试入口 | 局部项目指令 |
|---|---|---|---|---|
| `cmd/autogo/` | CLI 进程入口 | `main.go` | `go test ./...` | 继承根规则 |
| `internal/cli/` | 参数解析与命令路由 | `cli.go`、`cli_test.go` | `go test ./internal/cli` | 继承根规则 |
| `internal/install/` | 安装事务、Manifest、升级与冲突 | Go 源码和安装测试 | `go test ./internal/install` | 可按风险增加局部契约 |
| `internal/resources/` | 外置资源包加载和完整性校验 | `harness/pack.json` | `go test ./internal/resources` | 继承根规则 |
| `harness/` | Skill、工作流、参考样例和资源清单 | 当前文件内容与 `pack.json` 登记 | `make resource-check` | 继承根规则 |
| `temp/` | 黑盒安装验收制品 | acceptance 运行结果 | `make acceptance` | 不保存源码副本 |

### 4. 构建、测试、运行与部署入口

- 安装依赖：Go toolchain；安装和更新本身不依赖 Python。
- 本地运行：`go run ./cmd/autogo --help`。
- 单元测试：`go test ./...`。
- 竞态与静态检查：`go test -race ./...`、`go vet ./...`。
- 黑盒验收：`make acceptance`，从仓库根构建安装后进入 `temp/` 通过 `PATH` 调用二进制。
- 发布前检查：分别运行 `make format-check`、`make test`、`make race`、`make vet`、`make cross-build`、`make acceptance` 和 `git diff --check`。

### 5. 不得破坏的项目不变量

1. 默认 Agent 是 Codex，Claude Code 通过显式参数选择。
2. Agent 原生目录只保存 `skills/`，其他 Harness 资源进入 `.autogo/`。
3. 本地修改、项目 docs seed 和无关服务必须保留。
4. 资源包路径、结构或 locale 无效时 fail closed。
5. 真实 E2E 必须从 `temp/` 使用已安装 binary 和资源包，不能偷读仓库资源。

### 6. 外部依赖、环境与敏感边界

- `AUTOGO_HOME` 和 `BINDIR` 可以重定向安装位置；测试必须使用隔离目录。
- 不在资源、日志、Manifest 或测试 fixture 中写入凭证。
- push、merge、release 和破坏性历史改写仍是人工门禁。

### 7. 项目特有完成标准

- 改动必须有与风险相称的当前测试结果。
- Harness 正文变化必须通过资源结构校验和安装验收。
- 变更安装生命周期时必须覆盖成功、失败、重复执行、冲突和恢复。
- 未运行的跨平台或运行时验证必须明确标记为未验证。
