# REVIEW-059：新仓库初始化首次提交 Change Review

## 审查对象与结论

`PASS`

独立 Reviewer 按 `autogo-change-review` 审查 `OBJ-005` / [PLAN-016](../plans/PLAN-016.md) 的完整当前 Diff，包括未跟踪的 `internal/projectinit/commit.go`、`internal/cli/init_commit_test.go`、Plan 和此前 Review。基线为 `aa171a8672f49596d7b2042704b61db4fa458654`，分支 `fix/init-first-commit`。已加载根与 docs 指令；仅写本 Review，不修改被审对象、Progress 或索引。

当前未发现需要修复的 correctness、兼容、范围或验收阻断。实现使只有 `git init` 的可信主工作目录在初始化时形成三文件首次提交，后续沿用现有 HEAD 基线进入规划。该例外没有改变 Goal 运行期的 HEAD/index 保护、私有审计提交、调度或完成判断。

| 被审实现/测试 | 全文 SHA-256 |
| --- | --- |
| `internal/projectinit/commit.go` | `a1f2b1a28f933a68f666dcd129833408457639563b99424c672d6296c1fc05ae` |
| `internal/projectinit/init.go` | `4bdb4cd4645d5e90461a604303d5d7b504ec17e0691a7acf517600ae2e46c6cf` |
| `internal/projectinit/init_test.go` | `5d30a2436db8cc3d110529e415fa48f300f06974c5bb9fe326e241ac6721738e` |
| `internal/cli/init_commit_test.go` | `def622e4fdfd7a351530af2d6ce506ec8a06b6f1818046fb4bc0ac66b177bbc9` |

审查时 Plan hash 为 `dc37bb969f446464340dc401231936124a113fd0df6a70d33d9e296748e77d4f`；1.1、2.1、2.2、3.1 已勾选，3.2 仍未勾选。产品 SPEC 当前 hash 为 `28fd987227c02fde365dd377f1511c95ffd2ca2c5ac5eba8c3588286c79aa76d`，其 AC-INIT-001–003 的 Evidence 与本次测试一致；相对 [REVIEW-058](REVIEW-058-init-contract.md) 的产品版本仅有完成勾选、当前 Evidence 和章节编号纠正，未改变合同。技术 SPEC、DESIGN-001 与 README 与该 Review 所绑定版本相同。索引和 Progress 改动只关联本 Objective，没有混入其他工程意图。

## 正确性与兼容审查

1. **首次提交限定在初始化入口。** `Initialize` 在持有既有项目锁、完成配置和忽略规则准备后调用 `commitUnborn`。可解析 HEAD 直接返回，不执行 add/commit；HEAD 失败后还检查本地符号分支、`show-ref` 的缺失退出码和 loose ref 的存在性，损坏或不可读引用不能被静默替换成新根提交。
2. **提交路径明确，其他内容受保护。** `git add --intent-to-add` 与 `git commit --only` 都显式限定 `.gitignore`、`.xgoalignore`、`xgoal.yaml`。现有无关 dirty 拒绝仍在写入前和获取项目锁后执行；已暂存的运行目录内容由 `--only` 排除且保留在用户 index。已有初始化文件按完整内容纳入，与批准合同一致。
3. **失败可以重跑，不自动覆盖现场。** Git 身份或 index 锁失败返回包含恢复方向的错误，保留生成文件和可能的初始化路径登记。重跑完成首次提交后，后续 init 由 HEAD 检查避免重复；不引入自动 reset、清理或新的恢复状态。原有项目锁、linked worktree 拒绝及显式 state-dir 行为继续复用。
4. **Git 行为边界可见。** 提交命令局部关闭 hooks 和自动签名，使用现有身份和 Git 内容转换，不改全局配置；Git 环境继续清除继承的仓库/index 定位变量。Goal 的原始字节 Tree 准入保持不变，没有把内容转换差异视为已验收结果。
5. **输出保持兼容。** `initial_commit` 使用 `omitempty`，仅本次创建提交时输出；已有 HEAD 的初始化和重复初始化不增加该字段。CLI 仍使用原有失败退出码和 JSON 编码入口，没有增加 API、迁移或 Provider 权限。

## 当前验证 Evidence

Reviewer 独立执行以下短测，退出 0，`internal/projectinit` **4.112s**：

```bash
GOMAXPROCS=2 go test -p=1 ./internal/projectinit \
  -run 'TestInitialize(CommitsUnbornRepositoryOnce|CommitFailureCanRetry|DoesNotCommitRuntimeIndexEntries|RejectsDirtyRepositoryBeforeWriting|RejectsBrokenHead|CreatesStrictProjectAndSharedIDWithoutRemoteEffects)$' \
  -count=1 -v
```

实际覆盖六个顶层用例及 identity/index-lock 两个失败子例：三文件 Commit Tree、恰好一次提交、现有忽略内容保留、hooks/签名禁用、失败修正后重跑、运行数据不入 Commit 且暂存保留、业务 staged/unstaged/untracked 拒绝、损坏 ref 保留、已有 HEAD 与 index 字节不变。独立检查当前 tracked Diff 和两个新增 Go 文件的空白错误均通过。

另读取主 Agent 从本次工具实际返回保存的证据，未为生成日志重复运行完整 CLI 场景：

| 验证 | 结果与来源 |
| --- | --- |
| 新增完整 CLI 场景及初始化定向集合 | `/tmp/xgoal-init-review.tdMH2F/targeted-transcript.log` 明确标为从工具 chunk `f12cf3` 复制的摘录；projectinit **5.562s**，CLI **28.930s**，退出 0。新无手工 commit 场景 **25.77s**；已知/未知项目初始化回归 **2.85s**。 |
| projectinit / project race | `/tmp/xgoal-init-review.tdMH2F/race.log` 保存本次 exec 返回；`go test -race -p=1 ./internal/projectinit ./internal/project -count=1` 分别 **6.887s / 5.678s**，退出 0。 |
| 既有连续两 Goal CLI 回归 | `/tmp/xgoal-init-review.tdMH2F/existing-cli.log` 保存本次 exec 返回；`TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence` **51.59s**，CLI 包 **51.874s**，退出 0；两 Goal 均 Completed 并绑定各自最终 Tree。 |
| vet | 主 Agent 的本次工具 chunk `d115a3` 报告 `GOMAXPROCS=2 go vet -p=1 ./internal/projectinit ./internal/cli` 退出 0、无输出；Reviewer 未单独重跑 vet。 |

新增 CLI 测试源码复核确认：`cliRepository` 仅执行 git init，没有手工 commit；现有受审配置由 init 纳入基线；随后调用真实 CLI、daemon、Git 和 SQLite。工具摘录中的首次 Commit 为 `9414a06cb6b14a70f1ce0d04bc3709688d07351b`；测试检查只含三个初始化文件、Planner/Implementer/独立 Reviewer/受信 Validator 实际执行、Goal Completed、`output.txt` 精确为 `accepted\n`，以及 Goal 前后的初始化 HEAD/index 不变。完整 CLI Provider 使用确定性可执行夹具；这些证据没有证明真实模型质量或贪吃蛇游戏产出。

## 收口与剩余边界

当前实现、合同、Plan 已完成项和相关 Evidence 一致。可以进入 `autogo-change-close` 对账并创建本 Objective 的原子 Commit；本 Review 不直接勾选 Plan 或 Progress。

本次未重跑全仓 `make verify-m6`、未执行真实模型/贪吃蛇验收，未修改用户 `tmp/demo3` 的状态或代其重试原 Goal。此次结论仅覆盖新仓库初始化提交及其后续 CLI 规划/执行链路。后续实现或合同发生实质变化时，需针对新 Diff 重新验证并审查；索引由主 Agent 在收口时同步。
