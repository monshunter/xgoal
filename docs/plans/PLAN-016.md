# PLAN-016：初始化无首次提交的 Git 仓库

目标：让只有 `git init` 的新仓库在 `xgoal init` 后拥有仅包含初始化文件的首次提交，可以进入现有 Goal 规划流程。

范围：无 HEAD 仓库的初始化提交、结果输出、失败与幂等保护、现有仓库兼容和真实 CLI 验证；不修改 Goal 调度、验收语义或用户业务文件。

## Phase 1：初始化合同

合同：[产品 Spec FR-001](../../xgoal-product-spec-v0.1.md)、[技术 Spec](../../xgoal-technical-spec-v0.1.md)、[Git Design](../architecture/DESIGN-001-m2-git-environment-validation.md)。

- [x] 1.1 对齐产品、技术与使用文档中的首次提交例外、文件范围及失败恢复行为，并完成 Spec/Design Review。

## Phase 2：实现与回归

- [x] 2.1 用失败测试覆盖新仓库首次提交、重复初始化、已有仓库和无关文件保护。
- [x] 2.2 实现仅初始化文件的首次提交及可诊断失败，完成相关回归验证。

## Phase 3：运行验收与收口

- [x] 3.1 验证真实 CLI 从无首次提交仓库初始化后进入规划，核对提交范围和 Git 状态。
- [x] 3.2 完成 Change Review、文档与 Git 对账及原子提交。
