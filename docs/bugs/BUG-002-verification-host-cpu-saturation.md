# BUG-002：发布验收并发耗尽主机 CPU

## 状态

`verified`：默认并发控制和分钟级用例组织已修复并复验；完整分段门禁通过，独立 [REVIEW-056](../reviews/REVIEW-056-runtime-observation-and-closure.md) 为 PASS。

## 用户可见现象与影响

2026-09-06 08:20（Asia/Shanghai），用户观察到12个逻辑 CPU 接近100%、load average 29.49，桌面与项目服务争用主机资源。当时执行的是 `make verify-m6` 的 `go test -shuffle=on -count=20 -timeout=6h ./...`，并未启动真实模型 smoke。

## 根因与证据

- Makefile 未限制 Go 包级并行，默认按 GOMAXPROCS 同时运行多个测试二进制；SQLite 又有多处 `t.Parallel`。CLI、Orchestrator、SQLite 等测试同时创建临时仓库、数据库和服务，CPU 与文件 I/O 叠加。
- 进程快照确认 SQLite PID29614、CLI、Orchestrator 等测试共同属于 Go PID70766、验收进程组38340。SQLite 参数明确为 `-test.count=20`；这是有限重复，不能据此断言无限循环。
- 停止前连续采样 CPU idle 为0.49%、2.49%、2.22%；向该验收进程组发送 SIGINT 后，测试退出。一个可精确归属的测试 daemon 以原 binary/project/socket 正常停止，fixture 保留；没有停止用户的虚拟机、索引服务或其他应用。
- 停止后 idle 先回升到49–62%，随后74–78%。Spotlight、WindowServer、虚拟机等也有负载，不能把主机全部消耗归为 xgoal。
- SQLite 进程的2秒栈采样包含等待和文件 I/O，没有获得稳定热点函数的完整 Go 符号证据。代码与独立审查确认 Engine 等待 queue/context/1s ticker；无 READY Work 返回，失败转 WAITING，自动 retry 由 Store 事务校验并递增有限计数。本次没有发现调度器无等待无限循环，不作为排除所有潜在循环的证明。

原始快照、采样与已中断门禁日志保存在本机私有目录 `/private/tmp/xgoal-final-gate.jb9dxv/`。停止前普通全包已 PASS，但中断的 shuffle 和整个门禁不记为通过。

## 修复与验证

- Makefile 使用 `.NOTPARALLEL`，同一次 `make -j` 也不会重叠运行重型门禁。
- 默认导出 `GOMAXPROCS=2`、`GOFLAGS=-p=1`；包内 `t.Parallel` 默认随 GOMAXPROCS，测试启动的 Go 构建继承设置。显式 `GO_PACKAGE_PARALLEL` 与 GOMAXPROCS 可调整资源，既有 GOFLAGS 的其他选项保留。
- 当前 macOS GNU Make3.81 实际环境检查：默认2/-p=1，显式覆盖3/-trimpath -p=2，均通过；`make -j12` 临时顺序探针得到 first-start → first-end → second-start。独立 Reviewer 另行核验覆盖与其他 flags 保留。
- 使用当前 Makefile 环境、`nice -n10` 执行 SQLite 全包 `-shuffle=on -count=2 -timeout=15m`，PASS28.182s。运行中三次增量 CPU idle 为79.46%、80.20%、82.60%；这是当时采样，不能当成所有场景的性能保证。
- 同一资源配置下，Engine 的合法 blocked/failed、有限自动修复/耗尽场景 PASS62.214s；Store 自动/手动重试的21类安全边界 PASS0.881s。既有超时、失败和无进展屏障没有通过放宽运行时规则绕过。

## 用例时长补充

用户进一步要求单用例为分钟级，不设计小时级门禁任务。原30m/45m/6h包级预算不是充分修复，已被替代；独立 [REVIEW-055](../reviews/REVIEW-055-plan-015-verification.md) 批准在当前 Plan 中调整验收组织。

- 默认综合门禁改为完整 race 单轮，包含全部真实 CLI/服务/故障用例；普通 test 单轮仍可独立运行。移除顶层历史测试聚合的重复调用，保留其定向入口及 Benchmark/平台编译校验。
- 20次 shuffle 限定为8个短合同包及11个明确选中的 SQLite 状态/事务用例；先检查名单逐个存在，空集/缺失均失败。2分钟单包预算下实际 PASS：8包各约0.25s，SQLite8.218s。没有用缩减断言或 mock 替代真实单轮。
- 普通全包累计保护为15分钟、race为20分钟，各实际场景仍用秒到分钟的等待期限；完整门禁需在该预算内实际通过，失败不恢复小时级预算。完整 race 实测 Orchestrator891.008s，在原15分钟保护下通过但仅剩约1%余量；独立审查后仅将 race 整包保护设为20分钟，保留单场景3分钟期限。该设置承认多用例累计及机器波动，不把宽预算用作通过 Evidence。
- 两处测试 helper 曾用一小时 sleep 模拟等待被终止的服务；正常路径本来在秒级停止，现补为30秒失败兜底，防止父测试失控后遗留长寿命进程。虚拟时钟推进几小时的状态测试不会真实等待，保持原覆盖。
- 当前 Supervisor/Environment 完整定向 race PASS13.385s/23.523s，包含取消、readiness、逆序停止和诊断；独立增量审查无阻断。调整后的完整门禁正在按单轮与低并发执行，开始阶段三次 CPU idle 采样为78.18%、83.44%、83.20%，尚不代表整轮通过。
- CLI 构建实测0.66s，没有证据支持引入新缓存或测试 runner。

## 预防与边界

验收入口优先采用保守并发，专用机器可显式提高；完整场景的单轮覆盖与短合同的高次数扰动分别验证。GOMAXPROCS 是每个 Go 进程的并行执行限制，GOFLAGS 限制 Go 包并行，均不是整个主机或进程树的 CPU 硬配额；外部工具和多个独立 make 仍可叠加负载。中断真实进程测试后，先按精确身份检查归属进程，再正常回收，不能按通用进程名批量杀死服务。本次未改变用户全局 Harness 或主机服务配置。

## 完整门禁中发现的测试前置条件

完整 race 单轮中45个包通过，仅 Project 的两项链接锁测试失败。它们用 `os.WriteFile(...,0644)` 创建文件后假定初始权限必为0644；验收进程使用 umask077，诊断复现证明 Acquire 前后均为0600，未发生生产锁权限修改。测试显式 Chmod 为0644后再建立链接，继续断言拒绝链接锁且目标权限不变，避免在0600前置条件下漏检错误 chmod。生产锁代码未修改。

Project 全包在 umask077与022下分别 race PASS5.421s/5.426s。初始整条门禁仍记录 FAIL；保留其余45包的新鲜通过结果，修复后重新执行完整 Project race，再以 `make -o race verify-m6` 继续剩余门禁。未重复已通过且源码未变的长场景，最终结论由分段结果及最终输入身份共同对账。

剩余门禁执行 exit0，短合同 shuffle20、vet、CLI/config smoke、Benchmark 配置及平台编译均通过；唯一 Go/SQL 输入变化为已重跑的 Project 测试文件。完整分段结果经 REVIEW-056 独立核对后通过，原失败记录保留。最慢单个叶场景155.24s；CLI/Orchestrator 的741.599s/891.008s属于多个用例的累计时间。测试和归属 daemon 已退出，收口阶段不重复运行长场景。
