# 目标驱动验收：新项目贪吃蛇与恢复合同

本记录对应 [PLAN-017](../plans/PLAN-017.md) 与产品 AC-GA-001–007。代码审查及回归由 [REVIEW-062](../reviews/REVIEW-062-goal-acceptance-change.md) 保存；本文件拥有真实 Provider 与游戏交互 Evidence。

## 环境与输入

2026-09-06，macOS arm64，本地可信 Git 仓库、L0；Codex CLI `0.153.4`、Claude Code `2.1.235`。Codex 已登录，Claude 已安装但未登录；未改用户全局模型或认证配置。本机 Codex 缺省为 `gpt-6-astra`、`xhigh`。Invocation 对 observed_model 报 `unknown`，不将配置值冒充模型实测身份。

独立测试目录 `/private/tmp/xgoal-ga-real-9b1egk55/project`。初始没有源码、业务测试或验收文档，只执行：

```sh
git init -b main
/private/tmp/xgoal-ga-real-9b1egk55/xgoal init
/private/tmp/xgoal-ga-real-9b1egk55/xgoal daemon start --timeout 20s
/private/tmp/xgoal-ga-real-9b1egk55/xgoal run --goal '编写一个贪吃蛇游戏' --wait --format human
```

`init` 创建提交 `a0928fc35185bbbe20773908b39a1aa58b6a9849`，仅包含 `.gitignore`、`.xgoalignore` 和 `xgoal.yaml`。配置只有 `git-diff-check`，没有业务验证器；生成策略使用缺省 allow。新 Goal 为 `goal_df66a76ea52a642ec2222e86`。首次运行二进制 SHA-256 为 `ba96254b6e4855118d3e205da511a798b0b45cff8e222e95de5246f3b9c4f7af`，基于 `8059cfc` 加本次实现；后续 Schema、材料来源恢复、审批读取和终端默认反馈增量由当前定向测试单独验证。

## 运行观察

- 04:00:04 UTC 开始 Planner，Invocation `planner_f44ed4258d0e194691471353`；真实 session `01a074df-9939-7203-9e41-46ba9bd06c6e`。
- Planner 选择无依赖、可离线打开的中文 HTML/CSS/JavaScript 游戏，并在只读阶段用 `node --check` 验证生成脚本语法。
- 约 04:14:22 UTC 规划发布，冻结 3 条验收标准、2 个业务验证器，进入一个 required Work `work_ac484ba9e35feb429bb8e2ac`。期间没有人为补充标准、测试或批准。
- `snake-core-behavior` 为 5,895 字节，验证真实核心状态转换、移动、输入约束、增长计分、食物、碰撞、尾格例外、满盘胜利和重开。
- `snake-offline-ui` 为 8,437 字节，在确定性 DOM/Canvas/计时器边界中执行实际界面代码，验证事件与绘制联动。此检查不代表真实浏览器视觉验收。
- Implementer Invocation `invoke_e444ae5e3cb16531eb494ebb` 于 04:18:21 UTC 返回，5 份游戏文件由真实 Codex 产生，两份业务验证器通过。
- 首次独立 Review 默认选择 Claude，返回 `Not logged in · Please run /login`。Goal 进入 WAITING，Gate `gate_d3cb16856a6e7337e0bc63a7` 为 `checkout_retry_required`；不是自动完成。本次修复默认 Reviewer 运行前选择后，停止并重启该独立测试项目的 daemon，未改变项目配置、冻结标准或游戏源码。
- 恢复二进制 `/private/tmp/xgoal-ga-real-9b1egk55/xgoal-next` SHA-256 为 `6920cf5d94456e4b95403a708b2461512c86caba1107776a9b755048d6d1ca9c`。执行一次 `work retry work_ac484ba9e35feb429bb8e2ac --version 7 --reason ...`，保留原 Goal Revision。此记录明确包含一次修复后的人工操作，不把它表述为从头到尾无干预运行。
- 第二次 Implementer 会话为 `01a074fb-1487-7d13-a379-da59c3c760a3`，独立 Codex Review 会话为 `01a074fc-75c6-7523-a6ee-9d40d3561b59`，Invocation `review_invoke_074b5061252113affe24297a`。Review 已批准；两次使用相同 Provider，但会话独立。
- 04:35:04 UTC Goal 自动进入 **COMPLETED version 7**，Work 为 **COMPLETED version 12**，3 条标准全部 PASS；没有未解决 Finding。生成验证器的最终 Receipt 均为 PASSED，既有 `git-diff-check` 也通过。

## 浏览器交互验收

通过 Codex In-app Browser 对实际生成的 HTML/CSS/JavaScript 做交互测试。浏览器不允许直接导航本地 file URL，因此使用仅监听 `127.0.0.1`、只提供 4 份游戏资源的临时 HTTP 预览；不开放 `.git`、`.xgoal`、项目配置或其他文件。该服务器只用于验收，没有修改游戏或增加游戏运行依赖。

| 操作与观察 | 结果 |
| --- | --- |
| 初始中文页面、20×20 棋盘、三节蛇、零分，暂停按钮禁用 | PASS |
| 点击开始，方向键下、左、上转向；每段按游戏时钟推进后暂停观察 | PASS，蛇按方向实际移动 |
| 沿已观察到的食物位置转向吃食 | PASS，三节变四节，得分显示 10，食物重新生成 |
| 空格暂停/继续，暂停期间反复观察棋盘 | PASS，暂停位置稳定，按钮显示“继续游戏” |
| 继续直行直到撞墙 | PASS，显示“碰撞了，游戏结束”，保留 10 分，暂停禁用 |
| 点击重新开始，再按 W 转向和空格暂停 | PASS，得分清零、蛇恢复三节并向上移动 |
| 浏览器 warning/error 日志 | 空列表 |

截图保存在测试根目录：`browser-score-10.png`、`browser-collision.png`、`browser-restart-wasd.png`。交互完全通过按钮与键盘，没有直接改写页面状态、替换实现或注入测试数据。满盘胜利等额外边界由冻结核心脚本验证，本次未宣称在浏览器中逐格玩到满盘。

## 最终 Tree、审计与保留边界

- 最终 Tree：`9bb79ef02e6cc248b62b7ae1d9774b6d8caf279a`；私有审计 Commit：`39a4f323baf3eb03f3d262cd3fb5374f485f1c30`。
- Report hash：`46a4e31035c8ac3f6f759f2f9143aa983fd797d36a79cb1a677ff6112e8e769f`；Final Evidence Set：`evidence_set_final_996308d443801fddfa81b643`。
- 核心验证 Receipt hash：`f2c5eabd4262d592649274c294a03a5b04e39ef2849469c3acd5267672143928`；界面验证：`7cbfee76daff48edccc99fe42dced3d921430fcea7e02ba1802446f23c0486b5`。
- 使用 `git show <final-tree>:<path>` 逐字节对比全部 5 份工作目录源码，全部相同；HEAD 仍为 init 提交，index Tree 仍为 HEAD Tree。按 Kernel 的 `SHA256(0x01 + index bytes)` 算法，index fingerprint 仍为规划前的 `aeef1499ad8e1c973b908777318e818d3a046d7dad214fad66f8a9a2496e6d1b`，配置文件也与 init 提交逐字节相同。
- `export` 成功导出 171 个文件；目录为测试根目录的 `audit`，manifest SHA-256 `6bebd38d5f3b3e98ad8f5971bdf2c89603f839fe4b63bc5c6063488ef6dea151`。`final-report.json`、`acceptance-summary.json` 与 `invocation-summary.json` 保存便于复核的副本。
- 为便于本地打开，5 份源码逐字节复制到仓库忽略的 `tmp/goal-driven-snake/`，直接打开 `index.html` 即可；该目录是交付副本，原 Goal/daemon/审计状态仍在上述独立测试项目中。
- 截图和审计导出完成后，已正常停止该独立测试项目的 daemon，并终止本次临时预览服务器；源码、SQLite、报告与审计文件保留。游戏本身无需该服务器。

真实用户结果验收为 **PASS**。最终恢复二进制之后的“取消同时进程无法确认”错误优先级修正由当前定向 race 测试覆盖；本次真实完成没有触发取消分支。完整工程回归及最终 Diff 结论由 REVIEW-062 保存。

## 原 demo5 阻塞目标恢复

在以上新项目完成后，再核对用户原项目 `tmp/demo5`：工作目录干净，Goal `goal_63dd18ee20824620553a2503` 仍处于最初的 DRAFT version 1 / planning WAITING，阻塞原因是缺少业务验证器。04:53:24 UTC 正常停止该项目旧 daemon，以包含全部生产修正的 `xgoal-final` 重启；二进制 SHA-256 `07ae4cc9e1dc055f9e674c8e47443be316ebe06d05701fb45e9e3b756df678fb`。

对原 Goal 执行一次 `goal plan --expected-version 1 --reason ...`，保留历史并进入 generation 2；没有改配置或增加用户验收材料。Planner Invocation 为 `planner_d75de30abcd1b35dd6c33258`，会话 `01a07510-6ef8-7d30-8c59-d476374fb36e`。恢复前 HEAD、index fingerprint 和配置 hash 已保存于测试根目录 `demo5-recovery.json`。

- Planner 自主冻结 2 条标准和 2 个业务检查：`snake-core-behavior`（7,724 字符）、`snake-page-integration`（11,379 字符）。
- 唯一 Work `work_5b321961050dd980c48bd063`、唯一 Attempt `attempt_889ecd3967fbc58555e9382f`。Implementer 会话 `01a0751e-d422-7e22-9aff-0f28c5015538`；独立 Codex Reviewer 会话 `01a07523-cbb3-7961-95b0-fbf1f1368464`，结果 approved。新的默认选择在 Review 调用前跳过未登录 Claude。
- **05:16:54 UTC（本地 13:16:54）原 Goal 自动进入 COMPLETED version 6**，Work COMPLETED version 6。重新规划之后没有人工重试、审批、编写测试或编辑游戏源码；既有失败事件保留，旧规划 Gate 已撤销。
- 原目录产出 `index.html`、`styles.css`、`src/snake-core.js`、`src/app.js`、`README.md`，全部由真实 Codex 生成，直接打开 `index.html` 可运行。逐文件与最终 Tree `7687578a3d72701f4d31614dc59ad1623fd4d03d` 比较，5 份文件完全相同。
- 私有审计 Commit `abfb632c5181a126baad066535731bd5781c46c6`；Report hash `94b2cb9d50ed054145ed4af049947fef5ad3c1461c69364517406c1eac8fef04`；Final Evidence Set `evidence_set_final_1f4f80c61de73977807923ca`。两条标准 PASS，两个生成检查及既有 `git-diff-check` 的最终 Receipt 均 PASSED 并绑定该 Tree。
- HEAD 保持 `482749c3b542955c000c095836de728af7e3be2c`；index fingerprint 保持 `0fb6812f65a56dc75cea4e17588724d61fbdf6f8a62c394b7c7da448c01938c5`；配置 SHA-256 保持 `26c455876aa2146c68c61fab75dbe20a4a57573d5db0675f7b3bc821b7fcad2f`。源码留在用户工作目录，未擅自提交用户项目分支。
- `demo5-audit` 导出 144 个文件，manifest SHA-256 `44cf7c3ee8a7f6e619f635aadb84eff167f12e75dc829adbfcf889816640a9e5`；`demo5-final-report.json`、`demo5-acceptance-summary.json` 保存完成、Receipt、源码和会话摘要。

对 demo5 自身页面另做真实浏览器验收，临时服务器仅监听 `127.0.0.1:53990` 并提供 4 份游戏资源；仍只使用按钮和键盘操作。观察到开始和方向键移动、吃食后从三节变四节且得分为 **1**、暂停和继续、撞墙显示结束、重新开始回到零分准备状态、屏幕方向按钮和 WASD 转向，warning/error 日志为空。其计分和重开行为不同于先前新项目，本记录分别验收。截图为 `demo5-food-score.png`、`demo5-collision.png`、`demo5-restart.png`、`demo5-controls.png`，浏览器使用的源码摘要与最终 Tree 一致。

原 demo5 恢复及真实用户结果为 **PASS**。验收后停止临时预览服务器；保留用户项目 daemon、SQLite、源码及审计，用户可继续查询原 Goal。

两次运行的原始证据分别保留于各项目的 `.xgoal/state.db`、Packet、Provider 公开日志、Receipt 和 Review；独立测试根目录下的 `run.stdout`、`run.stderr`、`planner-log.json` 与 `frozen-contract.json` 为辅助观察副本。Provider 的过程声明只作为 Claim，完成结论以最终报告、当前 Tree、真实命令和交互结果为准。
