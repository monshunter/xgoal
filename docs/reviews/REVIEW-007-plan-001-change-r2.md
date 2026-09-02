# REVIEW-007：PLAN-001 M0 Change Review 修订复审

## 审查对象

- Plan：`PLAN-001`
- Revision：全部 Item 完成，SHA-256 `d965954da69c2e693e8b3e148cb8361f6c6cdc2ff9d005c9ffbaf79a602c9cfd`
- 前序 Review：`REVIEW-006`
- 产品 SPEC：SHA-256 `f46fedb46d31487a2498efb506664b42aa5e13ccb8d2257c5ba9a4e0cb1c187f`
- 技术 SPEC：SHA-256 `ae92fbdda66baae8937b2e81b75e80d3d4631bbbde6ccc67b7be8545410ce290`

## Verdict

`PASS`

## 前序发现处置

- Work Packet 现在收敛 Project Network/Secret 枚举并硬拒绝 Git Push/Production；Scope 基线拒绝 `.git`、逃逸与非法 Globstar；Prior Attempt 必填字段被校验。
- Agent Event 现在拒绝空 Command/File Change；Evidence Authority/State 只接受冻结枚举。模型核算字段不属于 xgoal Agent Event。
- `config validate` 现在校验 Workspace、Scope Policy、Bootstrap、Validator Phase/Timeout、Review、Policy、Report 以及 Agent 专属模式与白名单。
- 新增协议/配置负向矩阵先稳定触发失败，再由最小实现转绿；Schema 与 Go Validator 不再接受 Review 中列出的 Fail-Open 输入。

## 当前 Evidence

- `make verify-m0`：PASS；覆盖 gofmt、`go test ./...`、`go test -race ./...`、`go vet ./...`、version 与示例配置 CLI smoke。
- `go test -shuffle=on -count=10 ./internal/...`：PASS。
- 状态转换穷举一致性、32 路 Lease 竞争、Evidence Subject/Goal/Config/Tree/Authority/State 失配矩阵：PASS。
- Darwin arm64 与 Linux amd64 `cmd/xgoal` 双目标构建：PASS，分别生成 Mach-O 与静态 ELF 后删除临时构建目录。
- `git diff --check` 与受信 `gofmt-check.sh`：PASS。

## 范围与剩余边界

- M0 只证明协议、内存状态、测试替身和模拟完成判定，不证明真实 Agent、Git Workspace、持久恢复、Daemon 或产品用户闭环；README 已明确披露。
- Scope Pattern 的 NFC/大小写碰撞、symlink 与跨平台路径集合验证属于 M2；当前 Packet/配置入口只冻结安全语法基线。
- SQLite Store/Recovery 属于 M1；Codex/Claude Active Contract Probe 与真实双向角色链路属于 M3/M4。
- 没有产品 `AC-FR-*` 或 `AC-NF-*` 因 M0 模拟测试被勾选。

## 下一路由

允许使用 `autogo-change-close` 对账并关闭 `PLAN-001`，创建一个 M0 原子 Commit；随后为同一 `OBJ-001` 建立并审查 M1 Plan。
