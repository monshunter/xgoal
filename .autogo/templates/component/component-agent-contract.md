# `internal/install` 组件 Agent 契约

> 这是局部项目指令的完整参考样例。它只展示如何记录相对根项目指令的领域增量；真实组件必须根据自己的边界、命令和风险重写，不能复制本样例事实。

## 1. 组件职责

`internal/install` 负责把经过校验的 Harness 资源安装到目标项目，维护 Manifest、托管文件和托管区块的生命周期，并在失败时回滚本轮写入。

它不决定 Agent 应执行什么任务，也不拥有业务项目的文档内容。

## 2. 边界与非目标

- 接收已加载的资源 catalog 和 Host Adapter 结果，不自行解释 Skill 内容。
- 只修改 Manifest 声明的精确路径和托管区块。
- 不运行项目测试、部署或数据迁移。
- 不把 `.autogo/` 当作业务项目的通用备份目录。

## 3. 本地事实源

| 事实 | 主要位置 |
|---|---|
| 安装事务和写入顺序 | `internal/install/install.go` |
| Manifest schema 与退休路径 | `internal/install/manifest.go` |
| Agent 路径映射 | `internal/agent/` |
| 资源 catalog | `harness/pack.json`、`internal/resources/` |
| 行为验证 | `internal/install/*_test.go`、`make acceptance` |

## 4. 不得破坏的不变量

1. AutoGo 管理文件每次安装都按当前资源包覆盖；托管区块外和项目 seed 内容不得被覆盖。
2. 路径必须保持在目标项目内，符号链接逃逸必须拒绝。
3. Manifest 损坏或 owner 不匹配时 fail closed。
4. 安装失败不得留下部分新状态。
5. docs seed 一旦由项目接管，重复安装不得覆盖。

## 5. 依赖方向

`internal/install` 可以依赖 `internal/resources`、`internal/agent` 和安全文件系统辅助层；资源加载和 Agent Adapter 不得反向依赖安装事务。

Harness 正文必须留在外置资源包中，禁止重新嵌回安装器 Go 源码。

## 6. 构建、运行与测试入口

```bash
go test ./internal/install
go test ./...
go test -race ./...
make acceptance
```

只修改资源正文时仍需运行安装验收，因为路径、覆盖和升级行为属于本组件合同。

## 7. 本地风险升级规则

- 新增删除或覆盖范围时，先审计历史 Manifest 和回滚路径。
- 修改 Manifest schema、公共 CLI 行为或不可逆清理策略时，需要用户明确授权。
- 遇到来源不明的 dirty 文件时保留现场，不使用 stash、reset 或覆盖绕过。

## 8. 完成标准

- 目标改动有单元测试覆盖相应故障模式。
- 当前资源包校验、Go 测试和真实安装验收实际通过。
- dry-run、重复安装、覆盖和回滚语义没有退化。
- 最终报告区分已验证结果与未运行范围。

## 9. 与其他组件的契约

- `internal/resources` 提供路径干净且已登记的 catalog；安装层仍执行目标路径安全检查和事务内结果验证。
- `internal/agent` 只提供 Agent 原生目录和根指令文件名映射。
- `internal/cli` 负责参数解析，不越过安装层直接写项目文件。
- `harness/pack.json` 是发行资源清单的唯一 owner。
