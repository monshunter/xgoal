# REVIEW-022：PLAN-004 M3 Change Review

## 审查对象

- Plan：`PLAN-004`
- Revision：`a9d9e3c` 之后 `feat/xgoal-v0.1` 的 M3 未提交工作树
- 产品 SPEC：SHA-256 `f46fedb46d31487a2498efb506664b42aa5e13ccb8d2257c5ba9a4e0cb1c187f`
- 技术 SPEC：SHA-256 `81c53492d72a72842e76c3eb8d0ff85625da6bd70eeddf4ece6ad5ee04a073c8`
- Design：`DESIGN-002`
- 前序 Review：`REVIEW-020`、`REVIEW-021`

## Verdict

`PASS`

## 正确性与边界结论

- Adapter 通过参数数组、stdin Prompt、固定 `--ask-for-approval never` 和角色 Sandbox 启动 Codex；不经过 shell 拼接，不接受 `danger-full-access` 或无法落实的 Tool Allowlist。
- Work Packet 必须 canonical、只读且与 Work/Role/Workspace/Hash 完全匹配；公开 Agent Result Schema 先做精确身份校验，再转换为当前 Codex Strict Structured Output 子集，最终消息仍由 xgoal 严格 Decoder 校验。
- JSONL 逐行与总量有界；未知事件保留为脱敏 immutable raw ref，缺失 Session/Result、截断、Schema 错误、日志/Sink 失败及非零退出均 Fail Closed。Agent Event 与 Agent Result 保持 `CLAIM` 权限。
- stdout/stderr 在持久化前递归或模式脱敏，Invocation 不接收 Token/Secret/Key 型环境变量；制品目录、文件权限、不可覆盖发布和 Session Binding Hash 均由受信侧检查。
- Resume 只允许成功回合产生的本地持久 Session Binding，且 Profile、Work、Attempt、Goal/Plan Revision、Base Tree、Packet、Workspace、Sandbox、Tool Policy、环境变量名和 Provider Schema 任一漂移都会拒绝。
- Active Probe 必须显式开启 Provider Transport 并给出正超时；Passive Probe 不产生模型调用，也不宣称 Provider Transport 可用。
- 真实 Fast 与 Standard 修改均由 M2 从 worktree 读回、按冻结 Scope 捕获并在干净 validation worktree 重放；Validator Receipt 和 Standard SQLite 制品重启读回通过，Agent 自报与退出码没有替代确定性验收。

## 当前 Evidence

- `make m3-real-smoke`：PASS；当前 `codex-cli 0.145.0` 完成 Active Contract、Fast Implementer、同 Session Resume 和持久 Standard Implementer Attempt，耗时 `265.26s`。
- 真实 Fast/Standard Candidate Tree 分别为 `fed0ceb8d6415bd1201ae5295258a7f45bd6e58e`、`c560ec4542f85bfc6837345c8c1541493c3ca53e`，对应冻结 Validator 均为 `PASSED`。
- `make verify-m2 m3-contract`：PASS，包含 M0–M2 全量回归、20 次全仓乱序、Race Detector、`go vet`、CLI smoke、失败矩阵及交叉编译。
- 修订后的 `go test ./... -count=1`、`go vet ./...`：PASS。
- M3 定向 `go test -shuffle=on -count=20`、Race Detector、Linux amd64 no-CGO 编译：PASS。
- `git diff --check` 与 `scripts/xgoal/gofmt-check.sh`：PASS。

## Notes

- Codex CLI/Provider Schema 是外部演进合同；`m3-contract` 适合日常本地回归，发布兼容性仍须显式运行 `m3-real-smoke`。
- 当前 L0 和 Codex Sandbox 不构成主机级网络、文件或 Credential 硬隔离；M3 只对用户明确信任的本地仓库成立。
- M4 仍需实现 Claude Adapter、结构化 Finding、独立 Reviewer Session，以及 Codex 实现/Claude Review 与 Claude 实现/Codex Review 两条真实路径。

## 结论

PLAN-004 的实现、真实 Provider 合同、恢复绑定、独立文件系统验收和跨重启制品门禁成立，可进入 Close 与原子提交。
