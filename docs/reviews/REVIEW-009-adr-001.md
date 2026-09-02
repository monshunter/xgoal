# REVIEW-009：ADR-001 SQLite Driver 与持久事务基线

## 审查对象

- ADR：`ADR-001`
- Revision：review，SHA-256 `dc397da671e56ae3f924b75132dda8bcd76e78e675d568a0771fd2fcc4cb3748`
- 关联 Plan：`PLAN-002` 1.1
- 技术 SPEC：SHA-256 `bcdaa60ff8738edc5339c9aa5b82bd5696bb0cedac8e8d32d6e71654a5699430`

## Verdict

`PASS_WITH_NOTES`

## 发现

没有阻塞发现。

## 审查依据

- Driver 选择基于当前模块版本、最低 Go、CGO/交叉构建、SQLite 恢复缺陷与平台支持事实；没有仅以流行度或历史偏好决策。
- CGo-free modernc 与精确 libc pin 支撑 macOS/Linux 单一二进制；Go 1.25 最低版本和 Toolchain 自动选择代价已明确披露。
- 单连接、Immediate Transaction、WAL/FULL/Foreign Key/Busy Timeout 的合同与 v0.1 单写者、串行调度和持久完成边界一致。
- Migration Hash/版本间隙/版本过新 Fail Closed，全部待应用变更单事务提交；不存在部分 Schema 被当作成功继续运行的路径。
- `VACUUM INTO` 一致备份、只读校验、文件与目录 fsync、原子改名和 incomplete 保留覆盖升级前备份与崩溃边界；不使用运行中裸拷贝。
- Aggregate+Event、CAS、Idempotency 与 Effect Journal 的事务 owner 清楚，ADR 没有发明第二套业务状态机。
- 替代 Driver、回退边界和复审触发条件具体，方案可撤换但没有提前引入抽象层。

## Notes

- `PLAN-002` 1.2 实施 Store Open 时，必须验证默认状态目录为本机文件系统，并对能够识别的网络文件系统 Fail Closed；若某平台无法可靠识别，应报告 unsupported/未验证，不能把“只支持本机文件系统”降格为无检查的文档假设。
- Dependency 落地后必须用实际 `go.mod`、Driver 查询和 `CGO_ENABLED=0` Darwin/Linux 构建确认 Go 1.25.13、modernc v1.57.0、libc v1.74.4 与 SQLite 运行版本，没有用 ADR 文本替代兼容性 Evidence。

## 下一路由

ADR 可进入 `active`，并勾选 `PLAN-002` 1.1。随后按 `autogo-tdd` 领取 1.2；上述 Notes 属于 Store Open 和验收条件。
