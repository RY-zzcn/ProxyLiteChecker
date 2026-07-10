# ProxyLiteChecker 项目接手与进度总览

- 状态：`v0.4.4` 前端 UI 全面优化已完成并发布
- 当前代码版本：`v0.4.4`
- 当前已发布版本：`v0.4.4`
- 当前唯一开发阶段：无；等待用户制定下一路线
- 下一开发版本：未规划
- 路线图终点：`v0.4.4`
- 最后校准日期：2026-07-10
- 已发布版本提交：`54464d754e8c900dc20b430e37c697a408856014`（v0.4.4）
- 当前工作区基线：`main` 发布提交 `54464d7` 加发布后完成记录；不得自行开始未规划版本

本文是后续 Codex 会话的第一份项目状态入口。已完成的数据模型与性能路线见 [ROADMAP_V0.4.0_TO_V0.4.2.md](ROADMAP_V0.4.0_TO_V0.4.2.md)，部署路线见 [ROADMAP_V0.4.3.md](ROADMAP_V0.4.3.md)，当前 UI 路线见 [ROADMAP_V0.4.4.md](ROADMAP_V0.4.4.md)。

## 1. 30 秒接手摘要

ProxyLiteChecker 是一个单机轻量代理维护服务。一个 Go 进程负责 Web UI、HTTP API、SQLite、代理源拉取、本机检测、TXT/JSON 导出以及 HTTP/SOCKS5 目标网关。

当前工作区已在 `v0.3.4` 可靠性热修基础上完成 `v0.4.0` 状态模型和 `v0.4.1` 持久化任务调度：

- `proxies` 只作为代理身份和来源记录；旧质量列继续作为回退影子。
- `proxy_probe_state` 成为基础协议、出口、GeoIP、基础状态和连续失败的唯一真相。
- `proxy_target_state` 成为各目标状态、capability、等级和失败的唯一真相。
- `schema_migrations` 记录显式迁移版本；当前 schema 版本为 `402001`。
- v0.3.4 数据会自动回填；命名目标 `base-only` 保留诊断能力但重分类为不可用。
- 导出、网关、目标库存、删除和 TTL 已停止读取主表质量快照。
- 代理 API 新增 `probe`、`target_state`、`target_summary`；统计区分基础可用和目标唯一可用。
- probe 与 target 状态分别维护转换时间，周期清理要求连续基础失败。
- `job_runs` 持久化任务参数、进度、终态、父子关系和实例 ID，任务 ID 跨重启不重复。
- `scheduler_state` 持久化计划时间、终态、失败次数、退避和 pending reason。
- 重启会把遗留活动任务标记为 `interrupted`，并只安排一次恢复 catch-up。
- `workCoordinator` 统一管理重任务槽位、取消收尾、自动触发合并和拉取/检测公平轮转。
- 目标低库存先检测已有候选，候选不足才补源；拉取确有新增时才链接父子检测任务。

v0.4.3 的一键部署已完成并发布。当前 v0.4.4 已完成原生 Web 控制台的浅/深色设计系统、区域导航、渐进披露设置、移动端数据卡片和无障碍升级，最终自动化与现有 8899 验收均已通过，只剩 GitHub 发布闭环。

除非出现必须单独发布的回归热修，`v0.3.5` 至 `v0.3.9` 不安排功能版本，避免为了版本号切碎同一结构迁移。

## 2. 文档真相优先级

后续会话发现内容不一致时，按以下顺序判断真实状态：

1. 当前代码、数据库迁移逻辑和自动化测试。
2. `CHANGELOG.md`、Git 标签和 GitHub Release。
3. 本文的当前进度记录。
4. `docs/ROADMAP_V0.4.0_TO_V0.4.2.md` 的待办状态。
5. 历史设计文档。

旧优化草案、GeoIP 设计进度和 v0.3.4 单版本计划已经完整归并到本文、当前路线图和 `CHANGELOG.md` 后删除。后续不得根据 Git 历史中的旧版本建议覆盖当前路线图；如需追溯，只读取对应提交，不恢复成并行的活动计划。

## 3. 产品边界

必须保持：

- 单机部署、单个 Go 服务、SQLite、本机检测、本机网关。
- 所有代理质量结论只代表运行 ProxyLiteChecker 的这台机器所处网络。
- 内置目标当前为 `generic`、`openai`、`grok`、`gemini`、`claude`。
- 保持单二进制友好，不引入必须单独部署的队列、缓存或数据库服务。
- 保持与 `/root/ProxyPoolChecker` 完全分离。

当前路线图明确不做：

- panel/agent、多节点注册、心跳、分布式检测和远程任务派发。
- 把 sing-box 或其它大型代理内核作为必需运行依赖。
- 在完成状态模型和调度器之前开放任意自定义检测脚本。
- 长期保存每一次网关请求明细。

## 4. 当前代码结构

| 路径 | 当前职责 | 后续主要变化 |
| --- | --- | --- |
| `cmd/proxylite/main.go` | 启动、配置、认证、API 路由 | 增加任务历史、调度状态和升级后的状态 API |
| `cmd/proxylite/store.go` | SQLite migration、代理身份、probe/target 权威状态和兼容影子写入 | v0.4.2 聚合 SQL 和批量写入 |
| `cmd/proxylite/checker.go` | 检测计划、并发 worker、目标探测 | v0.4.2 改为代理优先的基础探测与目标探测流水线 |
| `cmd/proxylite/jobs.go` | SQLite 持久化任务状态机、进度节流、历史和重启中断恢复 | v0.4.2 保持完成事件接口 |
| `cmd/proxylite/settings.go` | 持久化调度、退避、低库存、完成事件和生命周期维护 | v0.4.2 接入代理优先检测完成事件 |
| `cmd/proxylite/coordinator.go` | 单一重任务槽位、自动意图合并和公平仲裁 | v0.4.2 保持协调边界 |
| `cmd/proxylite/sources.go` | 内置代理源和拉取任务 | v0.4.1 接入任务完成事件；后续再做源质量闭环 |
| `cmd/proxylite/gateway.go` | HTTP/SOCKS5 网关、重试和统计 | v0.4.2 状态缓存和运行指标降载 |
| `cmd/proxylite/gateway_selector.go` | 内存池、选择策略、失败隔离 | v0.4.2 锁外刷新、EWMA 和半开探测 |
| `internal/checkmeta` | GeoIP MMDB、外部 IP 元数据 | v0.4.2 缓存、限速、singleflight 和热更新修复 |
| `app/web` | 无构建步骤的嵌入式单页控制台 | 各版本仅增加必要展示，v0.4.2 统一轮询和诊断信息 |

当前运行数据使用 SQLite WAL。任务历史、调度计划、失败退避和协调状态已持久化；网络请求中的 goroutine 不做断点续跑，重启后遗留活动任务会转为 `interrupted`。

## 5. 当前版本与验证状态

`v0.3.4` 发布状态：已完成。

- `main`：已推送到 `73c6d4f`。
- 标签：`v0.3.4`，annotated tag 已推送。
- Release：<https://github.com/RY-zzcn/ProxyLiteChecker/releases/tag/v0.3.4>
- Release 资产：六个平台二进制、源码包和 `SHA256SUMS`。
- GHCR：`v0.3.4`、`0.3.4`、`0.3`，支持 `linux/amd64` 和 `linux/arm64`。
- 镜像摘要：`sha256:a782ea55677cb5e387b0a29d325d97c52088e703ae14d1d7b517e661a5a5ea0d`。

已通过：

- `./scripts/preflight_check.sh`
- `go test ./...`
- `go vet ./...`
- `TMPDIR=/root/.cache go test -race -count=1 ./...`
- Windows amd64 和 Linux arm64 交叉编译
- 19,457 条代理的真实 SQLite 备份迁移和完整性验证
- 任务完成、部分成功、失败、取消以及 API 终态冒烟测试

`v0.4.0-v0.4.1` 实现与发布状态：已完成；v0.4.0 作为结构迁移纳入 v0.4.1 累积发布。

- 新增表：`schema_migrations`、`proxy_probe_state`、`proxy_target_state`。
- 迁移版本：`400001`，迁移事务可重复启动且第二次不重复应用。
- 真实 v0.3.4 备份：19,457 个代理、1,581 个旧目标结果；迁移后代理数不变、probe 行 19,457、target 行 1,581、缺失目标 0、命名目标非法可用 0、孤立行 0、完整性检查 `ok`。
- 已通过：`go test ./...`、vet、race、preflight、真实迁移、8899 API/Web/网关冒烟和发布资产检查。
- 发布提交 `e1a6a5c`、annotated `v0.4.1` tag、GitHub Release 和 GHCR 多架构镜像均已完成。
- v0.4.1 新增表：`job_runs`、`scheduler_state`、`coordinator_state`；schema 版本提升至 `401001`。

下一次开始开发时仍必须重新运行基线检查，不能把本节当作当前工作区永远有效的测试结果。

## 6. 深度审查问题闭环表

| 编号 | 原问题 | v0.3.4 后状态 | 后续归属 |
| --- | --- | --- | --- |
| P0-1 | 仅基础出口可达也被计为命名目标可用 | 已完成。命名目标必须 Web/API 可达；base 只保留诊断能力 | v0.4.0 将能力和状态正式拆表 |
| P0-2 | `proxies` 与目标结果形成两套真相，单目标失败可能误删 | 已完成。probe/target 各有唯一权威表，主表和 `proxy_checks` 仅保留回退影子 | v0.4.0 |
| P0-3 | 取消立即释放重任务锁 | 已完成。`cancel_requested` 仍属于活动状态，worker 确认后才进入 `cancelled` | v0.4.1 持久化并补 `cancelling/interrupted` |
| P0-4 | 全源失败、落库失败、前端终态显示假成功 | 已完成。终态和前端 job ID 轮询已修正 | v0.4.1 持久化任务历史和配置快照 |
| P1-5 | 调度状态仅在内存，保存设置会推迟任务 | 部分完成。无关设置不再重置计划；重启仍丢失状态，调度器仍不知道最终结果 | v0.4.1 |
| P1-6 | 自动拉取长期压制自动检测 | 部分完成。tick 已改为检测先于拉取；尚无公平仲裁、触发合并和完成事件 | v0.4.1 |
| P1-7 | 维护过频、重复计数、待检 TTL 时间错误 | 已完成状态层部分。probe/target 独立转换时间和重排队，待检 TTL 使用转换时间 | v0.4.0；调度持久化仍属 v0.4.1 |
| E-8 | 多目标重复协议、出口 IP、GeoIP 和基础探测 | 已完成。检测按代理优先，每轮基础探测和出口信息只执行一次 | v0.4.2 |
| E-9 | 外部 GeoIP 元数据处于关键路径且无缓存；热更新有竞态 | 已完成。持久缓存、限速后台队列、失败退避和 reader 生命周期均已落地 | v0.4.2 |
| G-10 | 网关持锁查库、统计 SQL 多、前端轮询重、稳定优先过于短视 | 已完成。锁外刷新、聚合短缓存、轮询合并、EWMA/half-open/degraded 均已落地 | v0.4.2 |

## 7. 当前必须保持的行为不变量

任何后续重构都必须有测试保护以下行为：

1. `generic` 可使用基础出口或通用站点可达判定；命名目标必须 Web 或 API 至少一个可达。
2. base-only 结果不得进入 OpenAI、Grok、Gemini、Claude 的导出和网关池。
3. 一个目标失败不得覆盖或删除其它目标的可用状态。
4. 只有基础链路确认失败、计划目标都失败、结果成功持久化且不存在其它可用目标时，才允许立即硬删除。
5. `running`、`cancel_requested`，以及 v0.4.1 后的 `cancelling` 都必须占用重任务槽位。
6. 全部源失败为 `failed`，部分源失败为 `partial`，无候选为成功但结果包含 `noop=true`。
7. 状态 TTL 从该状态的 `status_changed_at` 开始，不得退回使用旧检测时间。
8. 自动检测不能被连续低库存拉取永久饿死。
9. 网关失败时可以继续使用最后一次成功装载的内存池。
10. 所有用户展示时间和调度时间继续使用 `Asia/Shanghai`。

## 8. 当前已知结构性债务

### 8.1 状态模型

- v0.4.0 已建立 probe/target 唯一状态边界。
- `proxies` 旧质量列与 `proxy_checks` 仍同步写入，作为一个版本线内的回退影子；不得恢复为业务权威查询。
- 当前检查器仍按目标重复产生基础探测，v0.4.2 才会改成代理优先并一次保存多目标 bundle。

### 8.2 任务与调度

- job ID 从进程内计数器开始，重启后重新计数。
- 任务历史最多只在内存保留 200 条。
- 重启无法区分“上次正常空闲”与“任务中途进程退出”。
- 调度器只知道任务是否创建成功，不知道最终 `completed/partial/failed/cancelled`。
- 冲突通过一分钟后重试处理，尚未区分正常 deferred 与真实错误。
- 周期触发和低库存触发没有统一合并与公平轮转。

### 8.3 检测、GeoIP 和数据库

- 每个目标会重复协议探测、出口 IP 查询和 GeoIP 查询。
- 单条目标结果使用独立事务保存，并对单个代理重复聚合状态。
- 外部 IP 元数据查询默认发生在检测关键路径，没有出口 IP 缓存、限速或请求合并。
- GeoIP 首次加载失败受 `sync.Once` 限制，reader 更新和手动/自动更新需要更严格互斥。
- `Stats()` 和目标统计会执行多次查询；高频前端轮询会放大开销。

### 8.4 网关

- selector 在互斥锁内查询 SQLite，慢查询会阻塞所有选路。
- `stability_first` 只参考运行期连续失败，一次成功会清空全部历史。
- 全池隔离时当前会一次清空失败记录，缺少半开探测和降级候选语义。
- 内存池仍以 URL 字符串为主，缺少检测时间、EWMA 延迟和稳定性快照。

## 9. 版本路线摘要

| 版本 | 状态 | 单一主题 | 完成后得到什么 |
| --- | --- | --- | --- |
| `v0.3.4` | 已发布 | 可靠性热修 | 正确的目标可用、删除、取消、终态、TTL 和基本调度优先级 |
| `v0.4.0` | 已完成，纳入 v0.4.1 发布 | 状态模型 | `proxies` 只负责身份；基础探测和目标状态各有唯一真相 |
| `v0.4.1` | 已发布 | 任务与调度 | job 和 scheduler 可持久化、可恢复、可公平仲裁并感知真实终态 |
| `v0.4.2` | 已发布 | 性能与可观测性 | 每个代理基础探测一次，GeoIP 不阻塞，统计和网关低开销 |
| `v0.4.3` | 已发布 | 一键部署 | 二进制/Docker 交互安装、Docker 环境处理、`/opt` 规范目录和简化文档 |
| `v0.4.4` | 已发布 | 前端 UI | 设计系统、深色主题、信息架构、响应式数据视图和无障碍 |

详细工作包、表结构、API、测试和验收标准见 [ROADMAP_V0.4.0_TO_V0.4.2.md](ROADMAP_V0.4.0_TO_V0.4.2.md)。

## 10. 后续 Codex 会话标准流程

当用户要求继续开发时，先执行：

```bash
cd /root/ProxyLiteChecker
git status --short --branch
git log -1 --oneline --decorate
sed -n '1,240p' AGENTS.md
sed -n '1,280p' docs/PROJECT_HANDOFF.md
sed -n '1,360p' docs/ROADMAP_V0.4.0_TO_V0.4.2.md
./scripts/check_version_consistency.sh
go test ./...
```

然后：

1. 检查工作区是否存在用户未提交改动，不得覆盖。
2. 对照 `CHANGELOG.md`、版本常量和路线图，确认当前真实版本。
3. 找到路线图中第一个“进行中”或“未开始”的版本；当前 v0.4.3 只剩 GitHub 发布闭环，不得开始后续版本。
4. 实施前先为关键状态转换、迁移和失败路径补测试。
5. 涉及 SQLite 时使用真实旧版本备份副本验证升级，不直接修改运行库。
6. 开发过程中实时更新本文和路线图，不能等到会话结束才补记录。每完成一个工作包、迁移、重要重构或验证节点，立即记录：当前工作包、已完成内容、已执行测试、当前阻塞和唯一下一步；执行长时间测试或发布命令前先写下待执行命令。
7. 阶段实现完成后，备份必要数据，更新并重启本机现有 `127.0.0.1:8899` 部署，只在该端口完成 health、登录/bootstrap、变更 API、Web 和必要网关冒烟；不得额外部署临时 ProxyLiteChecker 实例或改用其它应用端口。
8. 同步更新路线图勾选项、`CHANGELOG.md`、README、版本常量和发布文件。
9. 每个阶段必须完成 Git commit、推送 GitHub、annotated tag、GitHub Release、发布资产以及发布工作流包含的 GHCR 标签。上述步骤未全部成功时不得把阶段标记为“已完成”。
10. 若网络、凭据、CI 或发布资产阻塞，记录精确错误和恢复命令，将阶段保持为“进行中/阻塞”，不得跳过发布直接开始下一版本。

上述三条是项目长期硬性要求，用户本次指令已经提供后续执行所需的常驻授权；未来会话不再把正常的阶段提交、推送、打标签和发布视为需要再次确认的可选步骤。

## 11. 一句话恢复提示

下次用户可以直接说：

> 继续 ProxyLiteChecker，读取项目接手文档并按路线图从第一个未完成版本继续，完成实现、测试和进度记录。

根目录 `AGENTS.md` 会要求 Codex 自动读取本文和详细路线图，因此不需要用户重新粘贴历史审查内容。

## 12. 每次开发结束必须更新

- 当前正式版本和下一版本。
- 当前提交、标签和发布状态。
- 本轮实际完成的路线图工作包。
- 新增或改变的数据表、状态机和兼容策略。
- 执行过的测试，以及未执行测试的原因。
- 开发过程中的实时断点：当前工作包、最近完成动作、当前命令/阻塞和唯一下一步。
- 本机 `127.0.0.1:8899` 部署更新、重启与冒烟测试结果。
- Git commit、push、tag、GitHub Release、发布资产和 GHCR 状态。
- 仍存在的阻塞、风险和下一条明确可执行任务。
- 工作区是否包含尚未提交或尚未推送的变更。

没有完成上述记录、本机 8899 验证和 GitHub 发布闭环时，不应把一个版本标记为已完成。

## 12.1 实时断点记录格式

开发过程中在本文最新实施记录或路线图进度区持续维护以下内容：

```text
更新时间：YYYY-MM-DD HH:MM Asia/Shanghai
当前版本 / 工作包：
最近完成：
正在执行或准备执行的命令：
已通过验证：
当前阻塞：
唯一下一步：
```

任何可能超过数分钟的测试、迁移、构建或发布操作开始前，先更新“正在执行或准备执行的命令”；操作结束后立即写入结果。这样即使会话或进程突然中断，也能从唯一下一步恢复。

## 13. 2026-07-10 v0.4.0 实现记录

- 状态：已完成，作为 v0.4.1 累积发布的一部分交付。
- 完成工作包：`V040-MIGRATION`、`V040-STORE`、`V040-LIFECYCLE`、`V040-API-UI`、`V040-TEST`。
- 数据迁移：新增 `schema_migrations`、`proxy_probe_state`、`proxy_target_state`；schema 版本 `400001`；保留 `proxy_checks` 和主表质量列作为兼容影子。
- 兼容策略：所有状态写入在一个事务中同步 probe、target 和旧影子；业务读取只使用新状态表；旧版本回退前仍建议恢复升级前备份。
- 真实库验证：使用 `/root/.cache/proxylite-v034-migration.db` 的副本，迁移前后均为 19,457 个代理；1,581 个旧目标结果全部回填；probe 行 19,457；target 行 1,581；命名目标 `available + base/none` 为 0；缺失和孤立行均为 0；`foreign_key_check` 无输出；`integrity_check=ok`；迁移记录 app version 为 `0.4.0`。
- 自动化验证：`go test ./...`、`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`、`./scripts/preflight_check.sh`、Windows amd64 和 Linux arm64 交叉编译全部通过。
- API 冒烟：通过无网络监听的 handler 级测试验证 `/api/proxies`、`/api/stats` 和 `/api/target-profiles` 的 v0.4.0 字段与兼容语义。受当前沙箱限制，真实 TCP 监听返回 `socket: operation not permitted`，不属于应用失败。
- 发布：已包含在提交 `e1a6a5c`、tag `v0.4.1`、GitHub Release 和 GHCR 镜像中；没有修改 `/root/ProxyPoolChecker`。
- 剩余风险：旧影子字段仍会同步写入，必须持续防止后续代码把它们重新用作权威查询；v0.4.2 前多目标检测仍会重复基础探测。
- 下一条明确任务：开始 `v0.4.2 / V042-CHECK-PLAN`，先固化代理优先检测计划与兼容测试。

## 14. 2026-07-10 v0.4.1 实现记录

- 状态：已完成并发布。
- 完成工作包：`V041-MIGRATION`、`V041-COORDINATOR`、`V041-ARBITRATION`、`V041-RECOVERY`、`V041-API-UI`、`V041-TEST`。
- 数据迁移：schema 版本 `401001`；新增 `job_runs`、`scheduler_state`、`coordinator_state`，不修改或删除 v0.4.0 状态表。
- 任务语义：SQLite 自增 job ID；持久化参数、进度、终态、错误、结果、实例 ID 和父任务；进度按 10 项或 1 秒节流；活动任务重启后转为 `interrupted`。
- 协调语义：`workCoordinator` 是唯一重任务槽位；手动冲突返回活动任务；自动冲突合并 pending；`cancel_requested/cancelling` 保持占槽；终态回调后才释放。
- 调度语义：计划时间、pending、真实终态、失败次数和退避持久化；退避为 `1m/2m/5m/10m/30m`；成功清零；拉取和检测连续授权上限为 2。
- 流水线：目标低库存先检查现有待检候选，候选不足再拉取；只有拉取 `added>0` 才创建带 `parent_job_id` 的检测任务，相同目标 pending 会合并。
- API/UI：新增任务历史列表和 scheduler status；bootstrap 仅返回最近 10 条任务；Web 识别 `cancelling/interrupted` 并展示阻塞、pending 和 backoff。
- 真实库验证：从 19,457 代理的 v0.4.0 数据库副本升级，代理/probe/target 数量保持 19,457/19,457/1,581；迁移记录 app version `0.4.1`；外键检查无输出；完整性检查 `ok`。
- 自动化验证：`go test ./...`、`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`、`./scripts/preflight_check.sh`、Windows amd64 和 Linux arm64 交叉编译全部通过；前端 `node --check` 通过。
- 发布：提交 `e1a6a5c` 已推送；annotated `v0.4.1` tag、GitHub Release、8 个发布资产和 GHCR 多架构镜像均已成功。
- 剩余风险：自动任务只在单进程内协调，不提供多进程 leader election；网络任务重启后不会断点续跑；v0.4.2 前检测仍按目标重复基础请求。
- 本机验收：现有 `127.0.0.1:8899` 已更新至 v0.4.1；health、登录、bootstrap、jobs、scheduler、target/proxy API、headless Web、SQLite 完整性和 Grok HTTP/SOCKS5 网关全部通过。
- 发布验收：CI、Release、main Docker 和 tag Docker 四条 Actions 均成功；Release 8 个资产已上传；GHCR `v0.4.1`/`latest` 均包含 amd64/arm64。
- 下一条明确任务：开始 `v0.4.2 / V042-CHECK-PLAN`。

### v0.4.1 发布闭环实时断点

- 更新时间：2026-07-10 Asia/Shanghai
- 当前版本 / 工作包：`v0.4.1 / LOCAL-8899-RELEASE`
- 最近完成：v0.4.1 代码、迁移、自动化测试和发布指导文档已完成。
- 正在执行或准备执行的命令：检查 8899/systemd/GitHub 状态；备份 `data/`；运行 preflight；更新并重启现有 8899 服务；执行 health/login/bootstrap/jobs/scheduler/Web/网关冒烟；随后 commit、push、annotated tag、GitHub Release，并检查资产、CI 和 GHCR。
- 已通过验证：上一轮 `go test ./...`、`go vet ./...`、race、preflight、前端语法和交叉编译。
- 当前阻塞：尚未审计本机服务状态、GitHub 凭据与发布工作流。
- 唯一下一步：读取当前 8899 进程、systemd、Git remote、GitHub CLI 和发布工作流状态。

- 断点更新：现有 8899 服务为 PID `407867` 直接运行 `/root/ProxyLiteChecker/bin/proxylite`，无 `proxylite.service`；Git remote 为 `git@github-proxylitechecker:RY-zzcn/ProxyLiteChecker.git`；SSH 远端读取可用；本机无 `gh`；tag push 会触发 `.github/workflows/release.yml` 自动创建六平台二进制、源码包和 `SHA256SUMS`，Docker workflow 负责 GHCR。
- 正在执行或准备执行的命令：`./scripts/backup_data.sh`；`./scripts/preflight_check.sh`；构建 `bin/proxylite`；停止 PID 407867；以现有 `.env` 在 8899 原地启动新二进制；执行 curl/API/Web/网关冒烟。
- 当前阻塞：无；发布确认将使用 GitHub Actions/API，因为本机未安装 `gh`。
- 唯一下一步：完成数据备份和 preflight。

- 断点更新：备份已生成 `backups/proxylite-data-20260710-143108.tar.gz`；`./scripts/preflight_check.sh` 通过。重启前 `/health` 显示运行版本为 `0.3.3`，旧进程可执行文件已显示 `(deleted)`；仓库无 `.env`，将继续使用现有默认环境语义。
- 正在执行或准备执行的命令：停止 PID 407867；使用 `systemd-run --user --unit=proxylite --collect --working-directory=/root/ProxyLiteChecker ./scripts/start.sh` 在原 8899 端口启动 v0.4.1。
- 当前阻塞：无。
- 唯一下一步：停止旧进程并确认 8899 端口释放。

- 断点更新：停止 PID 407867 后，宿主现有机制自动在同一 8899 端口拉起 PID `868716`；新进程直接使用当前 `bin/proxylite`，`GET /health` 返回版本 `0.4.1`。未创建第二个部署或备用端口。
- 正在执行或准备执行的命令：在 `127.0.0.1:8899` 执行登录、bootstrap、jobs、scheduler、Web 静态资源和 gateway status 冒烟；检查 SQLite schema `401001`、完整性和网关监听端口。
- 当前阻塞：无。
- 唯一下一步：完成 8899 登录与认证 API 冒烟。

- 断点更新：8899 登录成功；bootstrap 返回 app `0.4.1`、18,694 个代理、schema 401001 调度状态和最近 maintenance job；jobs/scheduler/target-profiles/proxies API 通过；8899 与 18080-18089 全部监听；Grok HTTP 18084 和 SOCKS5 18085 实际请求均返回 HTTP 200；SQLite 外键检查无输出且完整性 `ok`。
- 浏览器冒烟：首次 headless Chromium 因 `/tmp` tmpfs 97% 占用报 `No space left on device`，属于环境阻塞。
- 正在执行或准备执行的命令：仅删除 `/tmp` 中旧 Chromium profile/component cache 和残留 `go-build*` 临时目录，然后重新运行 8899 headless Web 冒烟。
- 当前阻塞：临时目录空间不足，预计清理后解除。
- 唯一下一步：清理明确的 `/tmp` 临时缓存并重试 Chromium。

- 断点更新：已清理明确的旧 Chromium profile 和 Go build 临时缓存，`/tmp` 使用率从 97% 降至 55%；在宿主环境对现有 `127.0.0.1:8899` 执行 headless Chromium 冒烟成功，完整 DOM 返回并确认页面版本 `v0.4.1`、任务/调度及新增设置控件均已加载。
- 正在执行或准备执行的命令：`git diff --check`、`./scripts/preflight_check.sh` 和最终 Git 状态审计；全部通过后提交 `main`、推送并创建 annotated `v0.4.1` tag。
- 当前阻塞：无。
- 唯一下一步：完成最终本地检查并提交 v0.4.1。

- 断点更新：`git diff --check` 与 `./scripts/preflight_check.sh` 最终门禁通过；现有 8899 部署的 health、认证 API、Web、SQLite 和 HTTP/SOCKS5 网关验收全部完成。提交范围已审计，未包含运行数据、备份或临时浏览器目录。
- 正在执行或准备执行的命令：提交当前 v0.4.0/v0.4.1 累积实现和文档，推送 `main`，创建并推送 annotated `v0.4.1` tag。
- 当前阻塞：无。
- 唯一下一步：创建 v0.4.1 发布提交并推送 GitHub。

- 断点更新：发布提交 `e1a6a5c`、`main` 推送和 annotated `v0.4.1` tag 推送成功。GitHub Actions：CI `29074496398`、Release `29074504004`、main Docker `29074496302`、tag Docker `29074504010` 均为 `success`。
- 发布资产：GitHub Release `v0.4.1` 已公开，六平台二进制、`proxylitechecker-v0.4.1.tar.gz` 和 `SHA256SUMS` 共 8 个资产全部 uploaded。
- GHCR：`ghcr.io/ry-zzcn/proxylitechecker:v0.4.1` 和 `latest` 均可读取，OCI index 摘要为 `sha256:ae3d1e5445d814a2818901066a61666dd36c78d843056f5f65ac4bf53aac6933`，包含 `linux/amd64` 和 `linux/arm64`。
- 当前版本 / 工作包：`v0.4.2 / V042-CHECK-PLAN`。
- 当前阻塞：无。
- 唯一下一步：读取 v0.4.2 检测计划工作包，先补代理优先检测的计划与兼容测试，再开始实现。

## 15. 2026-07-10 v0.4.2 实施记录

- 更新时间：2026-07-10 15:02 Asia/Shanghai
- 当前版本 / 工作包：`v0.4.2 / V042-CHECK-PLAN`
- 最近完成：重新读取项目约束、接手文档和 v0.4.2 全部工作包；确认首个未完成项为代理优先检测计划。
- 正在执行或准备执行的命令：读取 `CHANGELOG.md` 和检查器/状态写入相关代码与测试；运行 `go test ./...` 基线；随后先补单代理多目标与多轮请求计数测试。
- 已通过验证：工作区干净，`main` 与 `origin/main` 一致，版本一致性检查为 `0.4.1`。
- 当前阻塞：无。
- 唯一下一步：完成基线测试和当前目标优先检查器结构审计。

- 断点更新：基线 `go test ./...` 通过；已把候选计划改为“一个代理 + 一组目标”，按代理 ID 合并各目标候选，并让每个代理每轮只创建一次协议 client、执行一次出口 IP fallback 和一次唯一出口 GeoIP 查询。目标探测复用同一 client/transport，代理内部目标并发上限为 3；取消后停止派发新代理并继续排空已开始 bundle。
- 新增验证：候选合并测试；五目标两轮请求计数测试（协议/出口各 2 次而非 10 次）；出口 endpoint 命中即停止；同出口 GeoIP 只查询一次；目标并发上限测试。`go test -count=1 ./cmd/proxylite` 已通过。
- 正在执行或准备执行的命令：`go test ./...`、`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`。
- 当前阻塞：无。
- 唯一下一步：完成 V042-CHECK-PLAN 全量、vet 和 race 验证。

- 断点更新：`go test -count=1 ./...`、`go vet ./...` 和 `TMPDIR=/root/.cache go test -race -count=1 ./...` 全部通过，`V042-CHECK-PLAN` 验证完成。
- 当前版本 / 工作包：`v0.4.2 / V042-WRITER`。
- 正在执行或准备执行的命令：审计 `SaveCheckResult` 事务边界，新增 `SaveProxyCheckBundle` 和原子回滚测试，然后让代理优先检查器按 bundle 保存。
- 当前阻塞：无。
- 唯一下一步：实现单代理 probe + 多 target 原子保存接口。

- 断点更新：新增 `SaveProxyCheckBundleContext`，单事务写入一次 probe、全部 target、兼容 `proxy_checks` 和一次聚合快照；bundle 校验混合代理 ID/重复目标，SQLite busy/locked 最多重试 3 次并服从 context。检查器按 bundle 保存，取消后对已产生结果使用最长 5 秒的收尾写入，并在任务结果中区分 `network_failed` 与 `persistence_failed`。
- 新增验证：多目标第二项写入失败时 probe/target/影子整体回滚；两目标基础失败只递增一次 probe failure；已取消 context 不进入写入。`go test -count=1 ./cmd/proxylite` 已通过。
- 正在执行或准备执行的命令：全量 test、vet、race 和差异检查；通过后进入 `V042-GEOIP`。
- 当前阻塞：无。
- 唯一下一步：完成 V042-WRITER 全量、vet 和 race 验证。

- 断点更新：`V042-WRITER` 的全量 test、vet、race 与差异检查全部通过。
- 当前版本 / 工作包：`v0.4.2 / V042-GEOIP`。
- 正在执行或准备执行的命令：审计 `internal/checkmeta` 初始化/更新锁、服务启动结构和 migration；设计 `ip_geo_cache`、有界 enrichment 队列、TTL/退避和 singleflight 测试。
- 当前阻塞：无。
- 唯一下一步：完成现有 GeoIP 生命周期与调用路径审计。

- 断点更新：新增 schema `402001` 与 `ip_geo_cache`；检测路径只同步读取持久缓存、本地 IP 类型和 MMDB，外部元数据通过单 worker、有界队列（默认 128）、同 IP pending 合并、默认 2 秒限速、7 天 TTL 和 30 分钟失败退避异步补充。补充完成仅按出口 IP 更新 metadata，不改变 probe/target 可用状态。
- GeoIP 生命周期：首次加载失败现在允许重试；查询持有 reader 读锁直到完成，更新不会提前关闭旧 reader；手动与自动更新共用 update mutex；下载继续使用临时文件、校验打开成功后原子替换。
- 新增验证：migration 幂等、异步 metadata 更新不改变状态、并发同 IP 只请求一次、cache TTL、retry_after、队列满、限速、检测非阻塞以及首次初始化失败重试。定向测试已通过。
- 正在执行或准备执行的命令：全量 test、vet、race 和差异检查；通过后进入 `V042-STATS`。
- 当前阻塞：无。
- 唯一下一步：完成 V042-GEOIP 全量、vet 和 race 验证。

- 断点更新：`V042-GEOIP` 全量 test、vet、race 和差异检查通过。
- 当前版本 / 工作包：`v0.4.2 / V042-STATS`。
- 正在执行或准备执行的命令：审计 `Stats()`、目标聚合、gateway status 查询与写入入口；新增聚合 SQL、2–5 秒短缓存、generation 失效及一致性测试。
- 当前阻塞：无。
- 唯一下一步：完成统计与 gateway status 当前查询路径审计。

- 断点更新：`Stats()` 改为 3 组聚合 SQL：全局 probe 状态、五目标状态/grade/唯一 URL、全局唯一目标 URL；新增 3 秒短缓存和 store generation。导入、bundle 写入、删除、TTL 重排队和 GeoIP metadata 更新都会失效缓存。Gateway Status 新增同 TTL 缓存，并由配置 generation 和 store generation 失效。
- API 新字段：Stats 与 Gateway Status 返回 `generated_at`、`cache_age_ms`；缓存仅包含聚合与运行状态，不包含代理认证明细。
- 新增验证：Stats cache 命中及 import/check 写入失效；Gateway cache 命中、显式 generation 和 store generation 失效。定向测试通过。
- 正在执行或准备执行的命令：全量 test、vet、race；通过后进入 `V042-GATEWAY`，完成池替换 generation 后回填剩余 STATS 勾选项。
- 当前阻塞：无。
- 唯一下一步：完成 V042-STATS 全量、vet 和 race 验证。

- 断点更新：`V042-STATS` 全量 test、vet 通过；race 按 `cmd/proxylite` 与 `internal/checkmeta` 分包运行均通过（聚合命令在工具会话中提前结束但无测试失败）。
- 当前版本 / 工作包：`v0.4.2 / V042-GATEWAY`。
- 正在执行或准备执行的命令：重构 selector 锁外加载/原子池替换、配置 generation、EWMA、half-open、degraded 和状态字段；补慢数据库不阻塞旧池选路并发测试。
- 当前阻塞：无。
- 唯一下一步：实现不可变上游池快照与锁外刷新。

- 断点更新：selector 候选查询已移到锁外，旧不可变池在异步刷新期间持续选路；返回结果仅在配置 generation 未变化时短锁原子替换。刷新失败保留旧池并记录错误。池元素包含检测延迟、成功率、检查时间、capability 和目标。
- 运行策略：`lowest_latency` 合并检测延迟与网关 EWMA；`stability_first` 合并检测成功率与运行 EWMA，成功不再清空历史。隔离到期进入单请求 half-open；全池 open 时选择明确 degraded 候选，不再清空失败记录。
- 状态 API：新增 closed/open/half_open、degraded、pool_generation、pool_age_ms；池替换会失效 Gateway Status 缓存并补齐 STATS generation 要求。
- 新增验证：慢 loader 不阻塞旧池、刷新原子替换、刷新失败保留旧池、half-open 单探测、EWMA 保留和 degraded 语义。定向测试通过。
- 正在执行或准备执行的命令：全量 test、vet、race 和差异检查；通过后进入 `V042-WEB`。
- 当前阻塞：无。
- 唯一下一步：完成 V042-GATEWAY 全量、vet 和 race 验证。

- 断点更新：`V042-GATEWAY` 全量 test、vet 和差异检查通过；race 按 Gateway 与非 Gateway 测试组分别运行均通过。
- 当前版本 / 工作包：`v0.4.2 / V042-WEB`。
- 正在执行或准备执行的命令：审计前端 bootstrap/job/stats/gateway/settings 定时器与 fetch 重叠；实现资源级 in-flight 合并、可见性暂停、任务期间降频和新鲜度/池诊断展示。
- 当前阻塞：无。
- 唯一下一步：完成前端轮询与请求入口审计。

- 断点更新：前端 GET 请求按 method+URL 合并 in-flight；任务运行时仅轮询具体 job ID并暂停普通 Gateway 轮询，终态再并行刷新 Stats/Gateway/Settings/Proxies。普通 Gateway 轮询改为递归 4 秒 timeout，避免请求重叠。
- 可见性：页面隐藏时清除高频 job/gateway timer但保留 watched job ID；恢复可见时立即并行刷新 jobs/stats/gateway/proxies，再按活动任务恢复对应轮询。
- 展示：Stats tooltip 显示目标能力摘要和新鲜度；Gateway 总览/卡片显示 generated/cache age、closed/open/half-open、degraded、pool age 和 refresh error；现有 scheduler pending/backoff/blocking/终态展示保持。
- 已通过验证：`node --check app/web/static/app.js`。
- 正在执行或准备执行的命令：Go 全量 test/vet/race、前端语法和差异检查；通过后进入 `V042-PERF-TEST`。
- 当前阻塞：无。
- 唯一下一步：完成 V042-WEB 自动化验证。

- 断点更新：`V042-WEB` 全量 test、vet、node 语法、差异检查及 Gateway/非 Gateway/checkmeta 分组 race 全部通过。
- 当前版本 / 工作包：`v0.4.2 / V042-PERF-TEST`。
- 正在执行或准备执行的命令：备份现有运行数据；在副本验证 v0.3.4→v0.4.2 migration；记录约 20k 与构造 100k 数据库的 Stats/分页/候选/网关查询计划和时延；执行浏览器无重叠轮询冒烟与交叉编译。
- 当前阻塞：无。
- 唯一下一步：完成性能验收基线与数据库副本准备。

- 断点更新：运行数据备份 `backups/proxylite-data-20260710-165543.tar.gz` 已生成。真实无 migration 表的 v0.3.4 副本已完整升级到 schema `402001`：19,457 proxies / 19,457 probe / 1,581 target，`integrity_check=ok`，外键错误 0，重复启动幂等。
- 19.5k 基准：Stats 108ms；目标深分页 668ms；候选 108ms；网关候选 153ms；五目标 proxy-first plan 3.65s；内存 selector 10,000 次选择 124ms。
- 100k 基准：100,000 proxies / 500,000 target states；Stats 4.55s；缓存命中 6.8µs；offset 50k 分页 6.20s；候选 554ms；网关候选 1.31s。新增 `idx_proxy_target_stats` covering index 和 probe status index。
- 查询计划：目标聚合使用 covering `idx_proxy_target_stats`；全局唯一可用使用 `idx_proxy_target_proxy_status`；候选/深分页仍需临时排序，但 100k 时低于 10 秒验收线。
- 正在执行或准备执行的命令：统一升级版本常量、README/CHANGELOG/部署/API 文档和 AGENTS；运行最终自动化后更新现有 8899，执行浏览器无重叠轮询和网关冒烟。
- 当前阻塞：无。
- 唯一下一步：完成 v0.4.2 版本与发布文档更新。

- 断点更新：版本常量、Web、`.env.example`、Docker Compose、CHANGELOG、README、部署说明和 AGENTS 已统一到 `0.4.2`；`./scripts/check_version_consistency.sh` 通过。
- 正在执行或准备执行的命令：`./scripts/preflight_check.sh`、全量/vet/分组 race、`node --check`、Windows amd64 与 Linux arm64 交叉编译、`git diff --check`。
- 当前阻塞：最终 preflight 发现 Stats 唯一可用数按 proxy identity 计数，未合并“不同原始协议、相同最终 detected protocol”的规范化 URL；定向修复进行中。
- 唯一下一步：用单次聚合 CTE 恢复每目标和全局规范化 URL 去重，运行定向测试、全量 preflight 与 100k 性能回归。

- 断点更新：Stats 已用单次 CTE 按最终协议、凭据、规范化 IP 和端口同时计算每目标及全局唯一 URL；`available_records` 继续保留原始可用记录数。定向回归测试通过，并补充 OpenAI 目标与全局唯一数断言。
- 正在执行或准备执行的命令：`go test -count=1 ./...`、`./scripts/preflight_check.sh`，通过后重跑 100k 外部数据库性能测试。
- 当前阻塞：无。
- 唯一下一步：完成全量自动化和 100k Stats 性能回归。

- 断点更新：规范化 URL 去重修复后的 `go test -count=1 ./...` 与 `./scripts/preflight_check.sh` 均通过。
- 正在执行或准备执行的命令：`PLC_TEST_PERF_DB=/root/.cache/proxylite-v042-perf.db go test -count=1 -run TestExternalDatabasePerformance -v ./cmd/proxylite`。
- 当前阻塞：无。
- 唯一下一步：确认 100k Stats 正确性修复后的性能仍满足验收线，然后执行最终 race/node/cross-build/diff 门禁。

- 断点更新：100k proxies / 500k target states 性能回归通过；修复后 Stats 8.48s、缓存命中 6.7µs、深分页 4.90s、候选 641ms、网关候选 1.80s，仍低于 10 秒验收线。`go vet`、前端 node 语法、Windows amd64/Linux arm64 交叉编译和 `git diff --check` 已通过。
- 正在执行或准备执行的命令：`TMPDIR=/root/.cache go test -race -count=1 ./...`；通过后构建正式 `bin/proxylite` 并更新现有 8899 服务。
- 当前阻塞：无。
- 唯一下一步：完成最终 race 门禁。

- 断点更新：`TMPDIR=/root/.cache go test -race -count=1 ./...` 全包通过；发布前自动化门禁完成。
- 当前版本 / 工作包：`v0.4.2 / LOCAL-8899-RELEASE`。
- 正在执行或准备执行的命令：构建 `bin/proxylite`，审计现有 8899 PID/启动方式，停止旧 PID 后仅依赖宿主现有机制在同一端口拉起新版本。
- 当前阻塞：无。
- 唯一下一步：更新并验收现有 `127.0.0.1:8899` 部署。

- 断点更新：正式 `bin/proxylite` 已构建；停止旧 PID `868716` 后宿主现有机制在同一 8899 端口拉起 PID `984050`，`/health` 返回 `0.4.2`，未启动第二套服务。
- 8899 验收：默认管理员登录、bootstrap/stats/jobs/scheduler/gateway/proxies API 通过；Stats 返回 `generated_at/cache_age_ms` 和目标唯一/记录计数，代理返回 `probe/target_summary`；Gateway 返回 pool generation/age、closed/open/half-open/degraded。数据库 schema `402001`、integrity `ok`、FK 错误 0；18080–18089 全部监听；Grok HTTP 18084 和 SOCKS5 18085 实际请求均返回 200。
- 正在执行或准备执行的命令：headless Chromium 验证 Web 新诊断字段、页面隐藏/恢复逻辑和 GET 请求无重叠；通过后勾选自动化与完成定义，进入 GitHub 发布。
- 当前阻塞：无。
- 唯一下一步：完成现有 8899 的浏览器冒烟。

- 断点更新：现有 8899 的 headless Chromium 冒烟通过。页面显示 v0.4.2、目标能力摘要、缓存新鲜度、5 个网关卡片、电路 182/0/0 与池龄，横向溢出 0；三次并发 Stats/Gateway 调用各只产生 1 个 GET，所有 API 最大并发均为 1；轮询启动、隐藏暂停、恢复可见刷新与重新调度均通过。截图：`/tmp/proxylite-v042.png`。
- 当前版本 / 工作包：`v0.4.2 / GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计差异与工作区，创建发布提交并推送 `main`；创建 annotated `v0.4.2` tag 并推送；监控 CI、Release 与 Docker 工作流。
- 当前阻塞：无。
- 唯一下一步：提交并推送 v0.4.2 发布变更。

- 断点更新：浏览器冒烟后的最终 `./scripts/preflight_check.sh` 与 `git diff --check` 再次通过；提交审计确认只包含 v0.4.2 代码、测试和文档，不包含运行数据库、备份、二进制或临时截图。
- 正在执行或准备执行的命令：`git add`、创建 v0.4.2 发布提交并推送 `main`。
- 当前阻塞：无。
- 唯一下一步：推送发布提交后创建并推送 annotated `v0.4.2` tag。

- 断点更新：v0.4.2 发布提交 `6a74ec8` 已创建并推送到 GitHub `main`。
- 正在执行或准备执行的命令：创建 annotated `v0.4.2` tag 并推送，随后监控 CI、Release 和 Docker 工作流。
- 当前阻塞：无。
- 唯一下一步：推送 `v0.4.2` tag 触发发布工作流。

- 断点更新：annotated `v0.4.2` tag 已指向 `6a74ec872117f698984f9e490f06448cedca7999` 并推送成功。
- 正在执行或准备执行的命令：轮询 GitHub Actions，确认 main/tag 触发的 CI、Release 与 Docker 工作流全部成功；随后核验 Release 8 个资产和 GHCR amd64/arm64 manifest。
- 当前阻塞：无，等待远端工作流完成。
- 唯一下一步：取得全部发布工作流终态。

- 断点更新：CI `29086857798` 与 Release `29086900834` 已成功；GitHub Release `v0.4.2` 已公开，六平台二进制、源码包和 `SHA256SUMS` 共 8 个资产全部为 uploaded。
- 正在执行或准备执行的命令：继续轮询 main Docker `29086857789` 与 tag Docker `29086900843`，成功后核验 GHCR `v0.4.2` amd64/arm64 manifest。
- 当前阻塞：无，等待两条 Docker 工作流完成。
- 唯一下一步：取得 Docker 工作流成功终态并核验镜像。

- 断点更新：main Docker `29086857789` 与 tag Docker `29086900843` 均成功；四条 Actions 全部完成。GHCR `ghcr.io/ry-zzcn/proxylitechecker:v0.4.2` 为 OCI multi-arch index，摘要 `sha256:19284d77a4293cc0eaaa64aa52ddb7cc31ae8bbb8da5f2ecbe911019fdf158fd`，包含 `linux/amd64` 和 `linux/arm64`。
- 发布结果：<https://github.com/RY-zzcn/ProxyLiteChecker/releases/tag/v0.4.2>；发布提交 `6a74ec8`；annotated tag `v0.4.2`；8 个资产全部 uploaded。
- 当前版本 / 工作包：`v0.4.2 / COMPLETE`。
- 正在执行或准备执行的命令：提交并推送本次发布后进度记录，确认 post-release CI/Docker 和工作区干净。
- 当前阻塞：无。
- 唯一下一步：完成发布后文档提交与最终工作区审计；之后等待用户制定下一路线。

- 最终断点：发布后进度提交 `92a8f4c` 已推送；对应 CI `29087301422` 和 main Docker `29087301442` 均成功。最终完成记录使用 `[skip ci]` 文档提交，避免仅记录工作流结果再次触发无意义的 Docker 构建。
- 当前版本 / 工作包：`v0.4.2 / COMPLETE`。
- 已通过验证：实现、全量 test/vet/race、preflight、交叉编译、真实迁移、20k/100k 性能、现有 8899 API/Web/SQLite/HTTP/SOCKS5、GitHub CI/Release/Docker、8 个资产和 GHCR 双架构全部成功。
- 当前阻塞：无。
- 唯一下一步：等待用户制定 v0.4.2 之后的新路线；不得自行开始未规划版本。

## 16. 2026-07-10 v0.4.3 一键部署实施记录

- 当前版本 / 工作包：`v0.4.3 / V043-INSTALLER-DESIGN`。
- 最近完成：用户明确制定新阶段；已审计 Release 资产、GHCR 标签、现有 Docker Compose、systemd 和更新脚本，确认不需要 schema 迁移。
- 正在执行或准备执行的命令：`./scripts/check_version_consistency.sh`、`go test -count=1 ./...`；通过后新增 `scripts/install.sh` 和静态验证。
- 当前阻塞：无。
- 唯一下一步：完成基线验证后实现一键部署脚本。

- 断点更新：基线版本一致性、全量 Go test 和差异检查通过。新增 `scripts/install.sh`：交互/参数选择二进制或 Docker，固定 `/opt/ProxyLiteChecker`，安全生成 `.env`，保留配置和数据，Release 二进制 SHA256 校验、systemd 服务、GHCR 正式标签、Docker 环境询问/官方安装、模式切换、8899 health 和退出清理均已实现。
- 安装器验证：`bash -n`、帮助与错误参数通过；使用 mount namespace 和 mock Release/systemd/Docker 完成二进制安装、Docker 安装、配置/数据保留、Docker→二进制切换、Docker 安装失败停止且不落文件、拒绝安装 Docker 立即停止测试。真实 GitHub latest 跳转与 `SHA256SUMS` 格式已核验。
- 文档与版本：README 已全文重写为一键部署优先，版本常量、Web、`.env.example`、Compose、CHANGELOG、部署说明、Release 页面和 preflight 已同步到 `0.4.3`；版本一致性检查通过。
- 当前版本 / 工作包：`v0.4.3 / V043-VALIDATION`。
- 正在执行或准备执行的命令：审查安装器失败路径和文档一致性；运行 preflight、Go vet/race、交叉编译与差异检查。
- 当前阻塞：无。
- 唯一下一步：完成 v0.4.3 最终自动化门禁。

- 断点更新：`./scripts/preflight_check.sh`、`go vet ./...`、全包 race、Node 语法、Windows amd64/Linux arm64 交叉编译和 `git diff --check` 全部通过。安装器真实 Release 格式及 mock 成功/失败/拒绝路径均通过。
- 当前版本 / 工作包：`v0.4.3 / LOCAL-8899-RELEASE`。
- 正在执行或准备执行的命令：构建正式 `bin/proxylite`，只重启现有 8899 进程并验证 v0.4.3 health、登录/bootstrap、Web 版本和安装文档静态内容。
- 当前阻塞：无。
- 唯一下一步：完成现有 `127.0.0.1:8899` 的 v0.4.3 验收。

- 断点更新：正式二进制已构建；停止旧 PID `984050` 后宿主现有机制只在原 8899 端口拉起 PID `1014746`。`/health`、默认管理员登录、bootstrap、stats、Web `v0.4.3`、5 个网关卡片和 18080–18089 监听均通过。headless Chromium 页面可见、无横向溢出、GET 合并和轮询生命周期冒烟通过，未启动第二套服务。
- 当前版本 / 工作包：`v0.4.3 / GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计 README、安装器权限/语法、提交范围和版本一致性；创建发布提交并推送 main、annotated `v0.4.3` tag，监控 CI/Release/Docker。
- 当前阻塞：无。
- 唯一下一步：创建并推送 v0.4.3 发布提交。

- 断点更新：最终 preflight 与差异检查通过；`scripts/install.sh` 权限为 `755`，README、部署文档和 Release 页命令一致。提交范围只包含 v0.4.3 代码、脚本、测试入口和文档，不包含数据库、二进制、安装包、mock 目录或截图。
- 正在执行或准备执行的命令：`git add`、创建 v0.4.3 发布提交并推送 main。
- 当前阻塞：无。
- 唯一下一步：发布提交推送后创建并推送 annotated `v0.4.3` tag。

- 断点更新：v0.4.3 发布提交 `1a29ce6` 已创建并推送到 GitHub main。
- 正在执行或准备执行的命令：创建并推送 annotated `v0.4.3` tag，随后监控 CI、Release 和 Docker 工作流。
- 当前阻塞：无。
- 唯一下一步：推送 `v0.4.3` tag 触发正式发布。

- 断点更新：annotated `v0.4.3` tag 已指向 `1a29ce6ce14f8d144095a5f047062414c06aa6ad` 并推送成功。
- 正在执行或准备执行的命令：轮询 GitHub Actions；核验 Release 8 个资产、源码包中的可执行安装脚本、raw README 一键命令和 GHCR amd64/arm64 manifest。
- 当前阻塞：无，等待远端工作流完成。
- 唯一下一步：取得 CI、Release 与 Docker 全部成功终态。

- 断点更新：CI `29089408339` 与 Release `29089465901` 已成功；v0.4.3 Release 已公开，8 个资产全部 uploaded。实际下载源码包确认 `scripts/install.sh` 权限为可执行，GitHub raw 脚本 `--help` 可运行；核验后下载包已删除。
- 正在执行或准备执行的命令：继续轮询 main Docker `29089408369` 与 tag Docker `29089465871`，成功后核验 GHCR `v0.4.3` amd64/arm64 manifest。
- 当前阻塞：无，等待 Docker 工作流完成。
- 唯一下一步：取得 Docker 成功终态并核验镜像清单。

- 断点更新：main Docker `29089408369` 与 tag Docker `29089465871` 均成功，四条发布 Actions 全部完成。GHCR `v0.4.3` 为 OCI multi-arch index，摘要 `sha256:074b5da4c2c5aeb84412d9130d6789817ba38786c3b2517ad41d8a857c7ab19f`，包含 `linux/amd64` 和 `linux/arm64`。
- 发布结果：<https://github.com/RY-zzcn/ProxyLiteChecker/releases/tag/v0.4.3>；发布提交 `1a29ce6`；annotated tag `v0.4.3`；8 个资产全部 uploaded；GitHub README 已完整更新。
- 当前版本 / 工作包：`v0.4.3 / COMPLETE`。
- 正在执行或准备执行的命令：提交并推送发布后完成记录，确认 post-release CI/Docker 和工作区干净。
- 当前阻塞：无。
- 唯一下一步：完成发布后文档提交；之后等待用户制定下一路线。

- 最终断点：发布后完成记录提交 `2d5d7e0` 已推送；对应 CI `29089956976` 和 main Docker `29089956981` 均成功。最终断点使用 `[skip ci]` 文档提交，避免仅记录工作流结果再次触发 Docker 构建。
- 当前版本 / 工作包：`v0.4.3 / COMPLETE`。
- 已通过验证：安装器成功/失败/拒绝/升级切换路径、GitHub Release/SHA256 真实格式、全量 test/vet/race/preflight/交叉编译、现有 8899 API/Web、GitHub raw 脚本、Release 8 个资产、源码包执行权限和 GHCR 双架构全部成功。
- 当前阻塞：无。
- 唯一下一步：等待用户制定 v0.4.3 之后的新路线。

## 17. 2026-07-10 v0.4.4 前端 UI 全面优化实施记录

- 当前版本 / 工作包：`v0.4.4 / V044-UI-FOUNDATION`。
- 最近完成：读取并应用 `ui-ux-pro-max`、`ui-styling`；生成专业运维控制台设计系统；在现有 8899 上取得桌面/手机基线截图和布局数据。
- 基线问题：桌面层级偏平、自动化设置密度过高；手机页面约 8121px，设置缺少渐进披露；无页内导航和深色主题；部分原生控件触控尺寸偏小。
- 正在执行或准备执行的命令：重构 index 语义结构、主题/导航交互和 CSS 设计系统，保持全部业务 ID 与 API 行为。
- 当前阻塞：无。
- 唯一下一步：完成 UI foundation 与信息架构实现。

- 断点更新：UI foundation 与信息架构已实现。新增浅/深色主题持久化、语义 tokens、区域导航、统计图标与说明、操作图标、设置 `<details>` 渐进披露、移动端代理卡片、跳转主内容、进度 ARIA、工具栏标签和数字格式化；保持全部业务 ID/API 不变。
- 已通过验证：`node --check`、`go test -count=1 ./app/web ./cmd/proxylite`、86 个 JS 绑定 ID 存在性和 `git diff --check`。
- 当前版本 / 工作包：`v0.4.4 / V044-VISUAL-TEST`。
- 正在执行或准备执行的命令：构建并只更新现有 8899；执行 1440/1024/768/390 截图、浅/深色、设置折叠、区域导航和移动端表格冒烟。
- 当前阻塞：无。
- 唯一下一步：完成首轮浏览器视觉验收并修正布局问题。

- 断点更新：现有 8899 已更新为 v0.4.4。浏览器完成 1440、1024、768、390、375 宽度及浅色/深色验收：全部页面横向溢出为 0；390/375 使用代理卡片且设置初始折叠；768 及以下可见控件最小 44px；未标注表单控件 0。
- 交互验证：主题切换写入 localStorage 且刷新后保持 dark；设置 details 从 0→1 展开；区域导航 active 状态、themeToggle 键盘 focus、登录页、ARIA 进度、reduced-motion 0.01ms 均通过。浅/深色正文、muted、主色、成功/警告/危险语义色对比度均达到 AA，最低为浅色 muted 4.55:1。
- 视觉修正：手机代理区限制为 760px 内部纵向滚动，整页高度从首轮约 13,257px 降至约 6,914px；深色 checkbox、空上游和禁用复制状态已统一。
- 当前版本 / 工作包：`v0.4.4 / V044-FINAL-VALIDATION`。
- 正在执行或准备执行的命令：全量 test/vet/race、preflight、Node、版本一致性、交叉编译、HTML/ID 兼容和差异检查。
- 当前阻塞：无。
- 唯一下一步：完成最终自动化门禁并重建嵌入资源二进制。

- 断点更新：preflight、全量 test/vet/race、Node、94 个唯一 HTML ID、86 个 JS 绑定 ID、Windows amd64/Linux arm64 交叉编译和差异检查全部通过。新增 `app/web/embed_test.go`，验证单二进制内嵌 v0.4.4 主题、导航、details、移动数据标签和 CSS。
- 当前版本 / 工作包：`v0.4.4 / LOCAL-8899-RELEASE`。
- 正在执行或准备执行的命令：停止现有 8899 PID，让宿主在同一端口拉起最终 `bin/proxylite`；复验 health、登录/bootstrap、主题与 Web 页面。
- 当前阻塞：无。
- 唯一下一步：完成最终嵌入资源二进制的 8899 验收。

- 断点更新：停止 PID `1056518` 后宿主只在原 8899 端口拉起最终 PID `1106526`。health v0.4.4、登录/bootstrap、Stats、Gateway、Web `themeToggle`、`workspace-nav` 和 CSS `v0.4.4 interface system` 标记均通过；未启动第二套服务。
- 当前版本 / 工作包：`v0.4.4 / GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计差异、截图、提交范围与文档；创建发布提交、推送 main、annotated `v0.4.4` tag，监控 CI/Release/Docker。
- 当前阻塞：无。
- 唯一下一步：创建并推送 v0.4.4 发布提交。

- 断点更新：最终 preflight、版本一致性和差异检查再次通过。提交审计确认只包含 v0.4.4 UI、嵌入资源测试、版本、README、路线图和接手记录，不包含运行数据、二进制、浏览器 profile 或截图。
- 正在执行或准备执行的命令：`git add`、创建 v0.4.4 发布提交并推送 main。
- 当前阻塞：无。
- 唯一下一步：推送发布提交后创建并推送 annotated `v0.4.4` tag。

- 断点更新：v0.4.4 发布提交 `54464d7` 已创建并推送到 GitHub main。
- 正在执行或准备执行的命令：创建并推送 annotated `v0.4.4` tag，随后监控 CI、Release 和 Docker 工作流。
- 当前阻塞：无。
- 唯一下一步：推送 `v0.4.4` tag 触发正式发布。

- 断点更新：annotated `v0.4.4` tag 已指向 `54464d754e8c900dc20b430e37c697a408856014` 并推送成功。
- 正在执行或准备执行的命令：轮询 GitHub Actions；核验 Release 8 个资产、CI 内嵌 UI 测试和 GHCR amd64/arm64 manifest。
- 当前阻塞：无，等待远端工作流完成。
- 唯一下一步：取得 CI、Release 与 Docker 全部成功终态。

- 断点更新：CI `29102285426`、Release `29102551657`、main Docker `29102285393`、tag Docker `29102552004` 全部成功。v0.4.4 Release 8 个资产全部 uploaded；GHCR `v0.4.4` 摘要 `sha256:a83d167deb9b0af7c68dc0acdb55026462dfe179c25af78480d0ded3070a4657`，包含 `linux/amd64` 与 `linux/arm64`。
- 发布结果：<https://github.com/RY-zzcn/ProxyLiteChecker/releases/tag/v0.4.4>；发布提交 `54464d7`；annotated tag `v0.4.4`。
- 当前版本 / 工作包：`v0.4.4 / COMPLETE`。
- 正在执行或准备执行的命令：提交并推送发布后完成记录，确认 post-release CI/Docker 和工作区干净。
- 当前阻塞：无。
- 唯一下一步：完成发布后文档提交；之后等待用户制定下一路线。

- 最终断点：发布后完成记录提交 `f3d364c` 已推送；对应 CI `29104191202` 和 main Docker `29104191167` 均成功。最终断点使用 `[skip ci]` 文档提交，避免仅记录工作流结果再次触发 Docker 构建。
- 当前版本 / 工作包：`v0.4.4 / COMPLETE`。
- 已通过验证：设计系统、浅/深色、375/390/768/1024/1440、设置折叠、移动代理卡片、主题持久化、键盘焦点、ARIA、reduced-motion、AA 对比度、嵌入资源、全量自动化、现有 8899、Release 与 GHCR 全部成功。
- 当前阻塞：无。
- 唯一下一步：等待用户制定 v0.4.4 之后的新路线。
