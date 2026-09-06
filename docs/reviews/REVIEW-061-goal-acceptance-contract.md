# REVIEW-061：目标驱动验收合同与设计 Review

## 审查对象

- Objective：`OBJ-006`；Plan：[PLAN-017](../plans/PLAN-017.md) 1.1。
- 产品合同：[产品 Spec 第 22 节](../../xgoal-product-spec-v0.1.md#22-目标驱动的验收准备obj-006--plan-017)。全文 SHA-256：`88076ce41d6cd0d41bbed7dfaa76a32de5757780bc1cf13e271364dc9fa0dee7`。
- 技术合同：[技术 Spec 的目标驱动验收扩展](../../xgoal-technical-spec-v0.1.md#目标驱动验收obj-006)。全文 SHA-256：`e9416a5606813a3f00c0aa92709cb8194f6d038a9b28359a635c2c31ab113f39`。
- 系统设计：[DESIGN-007 的目标驱动验收扩展](../architecture/DESIGN-007-runtime-harness.md#目标驱动验收扩展obj-006)。全文 SHA-256：`41a01a4ed450dfc132c924562ec411142036039a519408b9e6de38e7f291b5af`。
- Git 基线：`8059cfc0310539ec14ea6386c819838eba7bf234`；分支 `codex/goal-driven-acceptance`。
- 独立 Reviewer 按 `autogo-spec-review` 与 `autogo-design-review` 检查文档、现有 Planner/Contract、Registry/CommandRunner、Work 验证与 Review 顺序、配置与恢复接缝。只写本 Review，没有修改被审制品或代码。

## Verdict

`PASS`

当前文档已关闭审查中发现的两项必修问题，可以进入 PLAN-017 Phase 2。方案将生成脚本作为 Goal Contract 的不可变内容，重建为既有 Validator Definition，沿用当前验证、Review 和 Evidence 链路，能够解除“没有测试所以无法开始写实现与测试”的循环依赖；不需要新增准备 Agent 或第二套调度状态。

本次 PASS 是合同与设计门禁，不代表实现、兼容性、崩溃恢复或真实游戏运行已经通过。AC-GA-001–007 仍未勾选。

## 已发现并关闭的必修项

### 1. 冻结脚本自身错误缺少可执行恢复路径

初稿仅规定保留错误输出、禁止实现者修改脚本和启用有限实现重试。当前 `internal/orchestrator/execute.go` 先运行 Validator，失败即返回，成功后才运行独立 Reviewer。因此脚本自身语法错误或错误断言可能令实现重试全部失败，不能靠后置 Reviewer 自动修复，也不能要求实现者迁就错误标准。

修订后的产品 Spec 与 DESIGN-007 明确：这属于验收基线缺陷；保留旧失败和源码，审查修正 Proposal 后取消旧 Goal，在正常提交保留源码并使工作目录干净后通过 `run --proposal-file` 创建新 Goal；旧 Gate 和 Evidence 不迁移，human-gate 对新脚本重新确认。未冻结提案可经 `goal plan --proposal-file` 修正。当前版本不承诺自动判断和修改已冻结的错误标准。

结论：**已关闭**。这是可执行、保留审计身份的最小恢复边界；实现必须提供相符提示，不能将所有验证失败都描述成业务实现错误。

### 2. fast 和关闭 standard Review 会跳过生成断言的语义审查

初稿把覆盖与断言有效性交给现有独立 Reviewer，但当前调用条件是 `mode == standard && review.requiredInStandard`。如果直接复用原条件，fast 或显式关闭 standard Review 的 Goal 可以跳过唯一声明的语义审查，仅凭结构有效和空洞脚本 exit 0 进入完成。

修订后的产品 Spec 与 DESIGN-007 明确：存在生成脚本时，Work 和最终完成事实均必须具备独立 Review，不受上述两个开关绕过；发布前检查 Reviewer 能力，缺失时明确等待。没有生成脚本的旧行为保持。

结论：**已关闭**。实现测试须覆盖 fast 和 `requiredInStandard=false` 两条负向路径，不能只验证默认 standard。

## 一致性、完整性与最小方案判断

1. **用户材料优先且来源由 Kernel 拥有。** CLI/配置材料从审查的 InputTree 读取，记录普通文件 mode、hash 和内容，经 Packet 进入规划，发布时由 Kernel 注入 Contract。模型输出不能替换这些来源事实。Markdown 作为要求消费，不自动授予执行权限；已有 required Validator/Scenario 不被生成项替换。
2. **静态与 Goal 定义分开注册。** 生成 ID 按 Goal 命名空间处理，不覆盖项目 Validator。脚本文本和材料绑定可纳入生成 Definition hash；静态项目 Definition 不因某个 Goal 的材料改变。Registry 额外保存当前 Revision 的输入保护，因此只有用户材料、没有生成脚本时也须执行保护。该方式复用原注册键并避免不同 Goal 污染同一 ConfigHash/BaseCommit/项目 ID。
3. **一次审批指向同一可执行对象。** human-gate 在可靠 Proposal Observation 之后、发布之前打开；批准与新 generation 的创建在一个事务消费，并复制同一 Proposal，不重新调用模型生成另一份脚本。发布入口再次核对身份，直接 supplied Proposal 也不能绕过。实现应使审批 hash 覆盖规范化 ID 和 Kernel 材料绑定后的实际可执行内容，而非只有可变说明文字。
4. **默认自主与权限边界相容。** allow 表示目标范围内的本地生成验收授权；deny 仍允许完全由已有验证器支持的 Goal，human-gate 是明确配置的策略。生成脚本不因此获得改写既有配置、网络、Secret 或外部部署授权。L0 是既有可信本地运行边界，结构校验不是通用脚本安全证明；相关限制应继续如实展示。
5. **语义审查复用既有独立 Reviewer。** 不另增预冻结角色是可接受的最小选择；后置 Review 必须拿到原目标、完整冻结标准/脚本、用户材料、候选实现和真实结果。语法、运行时可用性、执行输出、业务覆盖是不同事实，不能用其中一项替代其余事项。
6. **Hash 兼容有明确范围。** 新可选字段缺省不参与 canonical 身份；已有无生成合同、Request、Packet 和配置应保留 hash。含新执行合同的 Goal 不承诺旧二进制反向执行兼容；现有 Work/finalize 调用的 `decodeFrozenContract` 使用严格解码，未知字段会拒绝执行，文档进一步禁止降级运行包含新 Goal 的状态库。实现不得把 `omitempty` 当作降级兼容机制，需要以当前旧样本验证正向读取及 hash 稳定，并保留可复核的升级/回退边界。
7. **断言与源码保持可归因。** 生成脚本冻在 Contract 中，用内联解释器命令执行，不从可变候选测试文件重新读取基线；Work 正常生成业务代码、项目测试和启动说明。CommandReceipt 绑定定义及当前 Revision/Tree，执行前后核对现场，最终报告区分来源。当前不保证任意目标或 Agent 断言语义完备，真实游戏交互仍是必要验收。

## 实现验证重点与下一路由

按当前 Plan 的已有范围执行，无需新增流程制品：

- 保存一组旧配置、Request、Contract 和 Packet canonical hash，分别验证新字段缺省、显式新配置和两个 Goal 不同材料的行为。
- 用实际 Git 输入验证 CLI/配置材料、缺失/越界/symlink/deny 拒绝，以及无生成脚本时材料仍不可被 Work 或验证阶段改写。
- 覆盖生成脚本边界、ID 规范化/映射、静态 Definition 不变、发布前 Reviewer 能力检查、Fast 不绕过 Review。
- 在 human-gate 的观察、决定、续作和发布之间注入崩溃或输入变化；核对批准对象不变、仅消费一次、无第二次 Provider 规划调用、旧批准和 supplied Proposal 无法绕过。
- 以无效断言、错误实现和冻结脚本错误验证拒绝完成与恢复提示；真实 Provider 的游戏源码、实际交互和最终 Evidence 单独验收。

本次仅作代码与文档的只读审查，未运行测试、构建或 Provider。下一路由为 PLAN-017 2.1 的输入实现及定向验证；实现与测试证据应由后续 Change Review 重新审查。

## 增量合同与设计 Review：默认 Reviewer 的被动可用性选择

审查对象：产品第 22 节新增默认 Reviewer 说明（全文 SHA-256 `fe9693df3f64a43c84543b33ce6607a47053eb6c5a1d8aab40abd48ad5d5e3e9`），DESIGN-007 末尾默认选择扩展（全文 SHA-256 `2e6ef0fbcde8b55aedda2538f70f3fb81e1eb471f61228854b7b0ace76699bc9`）。关联 Plan 增量见 REVIEW-060。

**Verdict：PASS。** 保留 `Config.SelectProfile` 的偏好顺序，在实际 Review 开始前排除被动 Probe 明确不可用或凭证 missing 的默认候选，是可实现且局部的运行时选择。项目已经信任的同 Provider 新会话可以保持审查独立；没有必要为了 Provider 多样性选择已知不可调用的 CLI。显式 roleProfiles 绑定不自动替换，unknown 不当作 missing，不修改用户配置、已有标准、网络或 Secret 权限。

实现与验证需落实下列既有合同解释：

1. 候选过滤后保持原 Implementer Provider 作为偏好比较基准，不因移除一个候选而改变排序来源；按稳定顺序选择一次，不新增全局角色重排。
2. 被动预检有明确时限；父 context 取消或进程归属退出不明立即返回，不能作为继续探测另一候选的理由。全部不可用时保留逐候选的可诊断原因，并给出登录或配置恢复方向。
3. “审查失败或拒绝后不改换 Reviewer”约束同一 Review/Attempt 的自动行为，防止挑选有利审查结论。用户明确执行原有 `work retry` 后的新 Attempt 可以重新按当前可用性预检；该重试仍必须保留旧失败/Review 记录，并通过原现场、Gate 和版本检查。
4. 显式绑定的缺凭证失败不得回退；unknown 仍按既有配置优先级保留；仅作执行前选择，不能先调用一个 Reviewer 后因 changes_requested 或调用错误自动更换。无需新增角色、数据库状态或 Provider。

本次仅审查增量合同及现有选择/Probe 接缝，没有运行或修改实现。上述四点进入 helpers/reviewerProfile 的实施与定向验证，代码及真实恢复结果由 REVIEW-062 后续增量复核。
