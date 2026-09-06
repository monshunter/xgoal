# REVIEW-058：无首次提交仓库的初始化合同审查

## 审查对象

- Objective / Plan：`OBJ-005` / [PLAN-016](../plans/PLAN-016.md) Phase 1.1。
- Git 基线：`aa171a8672f49596d7b2042704b61db4fa458654`；分支 `fix/init-first-commit`。
- 独立 Reviewer 使用 `autogo-spec-review` 与 `autogo-design-review`，按根与 docs 规则仅审查下列制品当前 Diff 中的 init 合同；读取既有初始化实现、Git 边界及 Plan 作为上下文，仅写本 Review。

| 被审制品 | 全文 SHA-256 |
| --- | --- |
| [产品 SPEC](../../xgoal-product-spec-v0.1.md)，FR-001 / AC-FR-001 / AC-INIT-001–003 | `37762aeae1dbc3f08dce71fde0eca377180ba08263a1a4d99abfcf9d56f2bca7` |
| [技术 SPEC](../../xgoal-technical-spec-v0.1.md)，14.1 初始化例外 | `0c46fa336f76795c2d920681470f1b7b7e4d44db8f042f1dcf384aa7fc8b13e4` |
| [DESIGN-001](../architecture/DESIGN-001-m2-git-environment-validation.md)，不变量与第 3 节 | `1851ae95b06ad5e34944a96d53af7ecc9555b5b06aec943dfdceb286bcad2f85` |
| [README](../../README.md)，快速开始 | `6b683a1fc437e9ed3e70b6f2f555a55ba9aa9e7a236cb7483d02d7a35a2146ee` |

## Spec Verdict

`PASS`

用户批准的行为已经落到既有 FR-001，没有创建平行 Spec。适用条件限定为命名主工作目录尚无首次提交；提交内容精确列为三个完整初始化文件，已有配置及忽略规则内容的归属明确。已有 HEAD 和重复 init 不自动提交，原有业务 dirty 拒绝保持，运行目录和其他暂存内容明确排除。`initial_commit` 仅在本次创建提交时出现，与现有 JSON 的其他字段兼容。

身份、锁和 Git 失败的诊断、文件与可能 intent-to-add 状态保留、禁止自动清理/回滚和修改全局配置均有明确合同。新增 AC-INIT-001–003 使用稳定 ID，分别覆盖新仓库与幂等、失败与范围保护、真实 CLI 进入规划；当前均未勾选，未用设计内容冒充运行结果。README 与产品合同一致，补充了运行 daemon 持锁时先正常停止的恢复入口。

审查中的措辞建议已由作者落实：历史 AC-CWD-002 已收窄为“Goal 执行期间系统操作保留用户 HEAD、符号分支和 index”，与 FR-001、AC-FR-001 和无首次提交初始化节的例外一致。当前无遗留发现。

## Design Verdict

`PASS`

`projectinit` 拥有初始化副作用，继续复用现有项目排他锁、文件生成和无关 dirty 检查；Git 管理指定路径提交及对应 index 更新。设计没有引入私有初始基线、数据库迁移或新的恢复状态机，符合本次最小修复目标。

技术 SPEC 与 DESIGN-001 将“不改用户 HEAD/index”限定于 Goal 执行期间，首次 init 的显式例外与运行期快照、私有 ref 晋升和 Agent 元数据禁止修改合同一致。真正 unborn、已有 HEAD 和损坏引用分开处理；设计要求先识别分支事实，不能把所有 HEAD 错误视为新仓库。具体 Git helper 和 ref 检查的正确性仍由实现测试与 Change Review 证明。

显式三路径 intent-to-add 与 `git commit --only` 的选择复用 Git 原生能力；既有配置按完整文件纳入、hooks/自动签名禁用、身份及内容转换按用户 Git 配置执行，均已公开记录。后续 Goal 仍执行原始字节 Tree 准入，不因初始化成功而隐式接受 filter/CRLF 差异。提交失败保留当前文件和登记状态供重跑，避免自动恢复覆盖用户内容；已有 HEAD 的重跑不重复提交，也不会转入运行期晋升路径。

## 验证边界与下一路由

本次仅审查文档合同、其相互关系和当前实现接点，未运行测试或 Provider，也未证明拟议 Git 命令已经在实现中正确使用。两项 Verdict 均可继续。Plan 在 Phase 1 补充指向上述既有合同的链接，目标、范围、顺序与 Checklist 未变，不触发 Plan 重审。

下一路由：完成 Phase 1.1 对账后执行 2.1 的失败测试，再实施并验证 2.2。测试应检查实际 Commit Tree、根提交父数、HEAD/index、业务文件字节与重复执行结果，并覆盖坏引用、Git 身份/锁/提交失败和已有提交仓库。Phase 3 单独保存真实 CLI 进入 Planner 的 Evidence；不得将固定 Provider fixture 的成功描述为真实模型或游戏交付。Review 索引由主 Agent 统一同步。
