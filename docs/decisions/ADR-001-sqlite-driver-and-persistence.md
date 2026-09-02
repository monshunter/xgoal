# ADR-001：SQLite Driver 与持久事务基线

## 状态

`active`

## 上下文

xgoal v0.1 需要在 macOS/Linux 上以单一 Go 二进制保存每项目状态，并保证 Aggregate 状态、Event、Lease、Idempotency 和 Effect Journal 在崩溃前后可确定读回。技术 SPEC 已固定 `WAL`、`foreign_keys=ON`、`synchronous=FULL`、`busy_timeout=5000`，但没有决定 Driver、CGO、连接池、Migration 和备份边界。

当前仓库最低版本是 Go 1.24。2026-09-02 的当前依赖事实是：

- `modernc.org/sqlite v1.57.0` 是 CGo-free `database/sql` Driver，支持 darwin/linux amd64/arm64，内含 SQLite 3.53.3 及项目维护的当前恢复修复；其 `go.mod` 要求 Go 1.25，并精确依赖 `modernc.org/libc v1.74.4`。[Driver 文档](https://pkg.go.dev/modernc.org/sqlite)、[v1.57.0 Changelog](https://gitlab.com/cznic/sqlite/-/blob/v1.57.0/CHANGELOG.md)
- `github.com/mattn/go-sqlite3 v1.14.50` 支持 Go 1.21 并内含更新 SQLite，但要求 `CGO_ENABLED=1` 与 C 编译器；从 macOS 构建 Linux 需要额外交叉编译器或容器工具链。[项目 README](https://github.com/mattn/go-sqlite3/blob/v1.14.50/README.md)
- `github.com/ncruces/go-sqlite3 v0.35.4` 同样 CGo-free，但仍是 pre-v1、要求 Go 1.26，且每连接 Wasm 环境增加内存开销。[项目 README](https://github.com/ncruces/go-sqlite3/blob/v0.35.4/README.md)
- SQLite WAL 只适合同主机文件系统、同一时刻仍只有一个 Writer；`synchronous=FULL` 在每次提交同步 WAL。WAL 文件是数据库持久状态的一部分，运行中不能脱离 WAL 直接复制主文件。[SQLite WAL](https://www.sqlite.org/wal.html)
- Go 1.21+ 的标准 `GOTOOLCHAIN=auto` 能按 `go.mod` 下载并选择更新 Toolchain；`go` 行仍是硬最低版本。[Go Toolchains](https://go.dev/doc/toolchain)

## 决策

### Driver 与 Go 版本

1. 使用 `modernc.org/sqlite v1.57.0` 和标准 `database/sql`。
2. 将项目最低版本升级为 `go 1.25.0`，建议 Toolchain 固定为 `go1.25.13`；Go 1.24 命令可在标准 `GOTOOLCHAIN=auto` 下获取该 Toolchain，禁用自动下载的环境必须预装 Go 1.25.13 或更新受支持版本。
3. 在主模块中保留 Driver 选定的精确 `modernc.org/libc v1.74.4`，依赖升级必须同时核对 Driver Changelog、SQLite 版本/补丁与 libc pin。
4. 不建立 Driver 抽象层；Store 只依赖 `database/sql` 和 xgoal Repository 接口。只有真实兼容或供应链问题出现时才增加可替换 Driver seam。

### 连接与运行合同

1. 每个项目一个本机 `state.db`；拒绝网络文件系统作为支持配置。
2. 使用 Driver 的已校验 DSN shorthand 设置：
   - `_busy_timeout=5000`
   - `_foreign_keys=1`
   - `_journal_mode=WAL`
   - `_synchronous=FULL`
   - `_txlock=immediate`
   - `_dqs=false`
   - `_defensive=true`
3. `database/sql` 设置 `MaxOpenConns(1)` 与 `MaxIdleConns(1)`。这与 v0.1 单写者、`maxParallel=1` 一致，也保证连接级 PRAGMA 不因连接池分叉；读写先串行，后续只有在测量证明必要且 M5 单写者锁成立后才能放宽。
4. Open 必须 `Ping` 并读回 SQLite Version、`journal_mode`、`foreign_keys`、`synchronous` 与 `busy_timeout`；任一不符 Fail Closed。
5. 不使用 `ATTACH` 形成跨数据库事务；不允许普通 SQL 改写 `journal_mode`、`writable_schema` 或 `schema_version`。

### Migration 与事务

1. Migration 是编译进二进制的、单调递增且不可修改的编号 SQL；数据库保存 `version`、稳定名称、SQL SHA-256 和应用时间。
2. Open 在服务其他请求前持有单连接并按编号检查 Migration；历史编号 Hash 不一致、版本间隙或数据库版本高于二进制时 Fail Closed。备份完成后，全部待应用 Migration 与 `schema_migrations`/`user_version` 更新在一个 `BEGIN IMMEDIATE` Transaction 中提交，任一失败则整体回滚。
3. 已有数据库存在待应用 Migration 时，先在项目私有 `backups/` 下用 SQLite `VACUUM INTO` 生成一致快照。输出先写唯一 `.tmp`，通过只读连接 `quick_check`、文件 fsync、`0600` 权限与 SHA-256 后原子改名为版本化 `.db`，再 fsync `backups/` 目录；只有备份目录项持久化后才开始 Migration。崩溃残留 `.tmp` 在恢复时原子改名为 `.incomplete`、fsync 目录并保留，不静默删除。该方式按 [SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html#vacuuminto) 生成一致快照并在 FULL synchronous 下同步输出。
4. 新建空数据库不创建无意义备份；Migration 全部完成后记录来源版本、目标版本和备份 Hash。Migration 失败时 Store 不启动写循环，已完成备份保持可读。M1 不自动清理 Migration 备份；引用与保留策略由后续 `clean` 能力统一处理。
5. 不提供自动 Down Migration。回退二进制前必须使用兼容性声明或受控备份恢复，不能让旧二进制猜测新 Schema。
6. Aggregate 状态变更、Version CAS 和对应 Event 在同一 SQL Transaction 中提交；冲突不追加 Event。
7. Idempotency Key 以 `(scope, key)` 唯一，绑定请求 Hash 与最终响应；同 Key 不同请求 Hash 是冲突。
8. Effect Journal 先持久化 Request，再执行外部动作，随后持久化 Observation；恢复只根据持久请求与外部 Read Back 决定，不因进程退出重复猜测副作用。

### 文件与恢复

1. Store 与 `backups/` 目录创建为 `0700`，主 DB 和完成备份校验/收敛为 `0600`；WAL/SHM 只位于该私有目录。M5 再增加 OS 级单写者锁。
2. 运行中不通过裸文件复制备份 `state.db`。需要备份时使用关闭后的完整文件集合或 Driver/SQLite Online Backup API，并把恢复验证作为独立 Operation。
3. Close 先停止新事务、等待 `database/sql` 关闭；不删除残留 WAL/SHM，重开交给 SQLite 恢复并再执行一致性检查。

## 替代方案

### `mattn/go-sqlite3`

优点是成熟、Go 1.24 可直接使用且当前嵌入 SQLite 更新。拒绝原因是 CGO 和跨平台 C 工具链会扩大安装、CI 与可复现交付面，不符合 xgoal v0.1 单一 Go 二进制的最小运维目标。若 modernc 出现不可接受的正确性或性能缺陷，可在原生 macOS/Linux CI 与发行工具链接受 CGO 后重新评估。

### `ncruces/go-sqlite3`

优点是 CGo-free、依赖较少、Online Backup API 清楚。拒绝原因是当前仍为 pre-v1、最低 Go 1.26，并为每个连接引入 Wasm 内存开销；xgoal 的轻量状态库没有需要其扩展能力的证据。

### 旧版 modernc 维持 Go 1.24

拒绝。可兼容 Go 1.24 的近期版本嵌入处于 SQLite 官方已披露 WAL-reset 缺陷影响范围内；即使单连接设计降低触发条件，也不以运行假设代替已可获得的修复版本。

## 后果

- 正面：无 CGO，macOS/Linux 可直接交叉构建；事务与连接合同单一；可使用标准 `database/sql`、Race Detector 和临时目录集成测试。
- 代价：最低 Go 从 1.24 提升到 1.25；现代 C 转译依赖下载与编译体积更大；Driver/libc 必须成对精确升级。
- 性能：v0.1 主动序列化为单连接，优先证明正确性；任何提高连接数的优化必须重新验证 PRAGMA、WAL checkpoint、CAS、Lease 和崩溃恢复。
- 兼容：Schema 只能前向迁移；回退依赖备份或显式兼容版本，不承诺任意二进制降级。

## 验证要求

- 在 Darwin arm64 与 Linux amd64、`CGO_ENABLED=0` 下构建单一二进制。
- Open 后执行主库 `quick_check` 并断言 SQLite Version/PRAGMA/权限，关闭重开后状态与 WAL 恢复一致。
- Migration 首次、重复、Hash 漂移、版本过新和中途失败测试。
- 已有库升级前备份的完整、Hash、权限、原子改名与中断残留测试。
- Aggregate 状态+Event 原子性、CAS 冲突、Idempotency 重放、Effect Read Back、重复 Lease 与 Race 测试。
- 模拟异常 Close/进程退出后重开，不产生部分状态、重复有效 Lease或重复 Effect。

## 复审触发条件

- Driver 或 SQLite 披露数据完整性/恢复漏洞；
- modernc/libc 无法在受支持 Go Toolchain 或 darwin/linux 目标构建；
- M1 Benchmark 证明 Driver 成为用户可感知瓶颈；
- M5 需要多读连接、在线备份或跨进程只读访问；
- 需要数据库加密、不可信数据库输入或网络文件系统。
