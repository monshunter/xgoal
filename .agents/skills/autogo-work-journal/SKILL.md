---
name: autogo-work-journal
description: "保存跨会话恢复、Waiting、Cancelled 或用户明确要求的短 Journal；当当前事实需要长期交接时使用；普通 Fast/Standard Commit 默认不调用。"
---
# autogo-work-journal
## 目标
只在恢复价值超过维护成本时保存最小上下文，让下一次工作能够继续；Journal 不是每次交付的强制凭证，也不是第二套 Evidence 台账。

## 输入与发现
- 当前目标，以及存在时的 Objective、Plan 或相关事实 owner
- 当前 Diff、已完成验证、关键决定和恢复入口
- Waiting、Cancelled、跨会话 handoff 或用户明确要求记录的原因

## 输出与持久制品
- `docs/journal/` 中一条必要的 `handoff`、`cancel` 或用户要求的记录
- 已完成事实、未完成事实、风险和精确恢复方式
- 已同步且幂等的 Journal `INDEX.md`

## 副作用与 Human Gate
只修改 Journal 与对应 Index；不创建 Commit，不改变 Objective、Plan、Review 或其他事实 owner 的结论。

## 执行步骤
1. 先判断后续恢复是否会因缺少当前上下文而明显变难；若 Git、Plan、Review 或 Operation 已足够，停止且不创建 Journal
2. 锁定记录类型和关联工作单元，只读取当前真实 Diff、验证与状态
3. 记录已经发生的事实、未完成项、风险和下一恢复点，不复制原始日志，不发明未验证结论
4. 通过 autogo-doc-index 同步 `docs/journal/INDEX.md`
5. 重复执行时更新同一工作单元记录，不重复追加相同内容，不回填 Commit hash

## 验证与完成
- Journal 的存在由真实交接需要或用户要求证明，不由 Fast/Standard 路由机械触发
- 内容足以恢复且没有复制其他 owner 的完整 Evidence
- Index 与正文一致，重复更新不产生 Diff

## 失败、重试与幂等
缺少可复核事实时不创建猜测记录；Journal 无法安全更新时只阻塞明确依赖该交接记录的状态变化，不阻塞无关 Commit。
- 重复执行前读取当前文件、Git 和运行状态，不重复创建已存在制品。
- 相同失败再次出现时停止机械重试，回到真实 owner。
