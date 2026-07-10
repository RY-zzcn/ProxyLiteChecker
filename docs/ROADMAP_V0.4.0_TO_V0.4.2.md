# ProxyLiteChecker v0.4.0 至 v0.4.2 详细开发路线图

- 状态：v0.4.1 已完成本机 8899 验收和 GitHub 发布闭环；下一阶段为 v0.4.2
- 基线版本：`v0.3.4`
- 基线提交：`73c6d4fb34f2e7898ee4b21bf9b3b9090b9f4d80`
- 制定日期：2026-07-10
- 路线主题：状态模型、可靠任务调度、检测与网关性能

本文是 v0.4.0 至 v0.4.2 的唯一当前路线图。项目当前状态和 Codex 恢复流程见 [PROJECT_HANDOFF.md](PROJECT_HANDOFF.md)。

## 1. 路线目标

`v0.3.4` 已经修复最危险的结果误判、跨目标误删、取消锁提前释放、任务假成功和 TTL 时间错误，但这些修复仍建立在旧数据结构和内存调度器之上。

接下来三个版本必须按依赖顺序完成：

```text
v0.4.0 状态模型
  └─ 为每个代理建立唯一的基础探测状态和目标状态
      └─ v0.4.1 任务与调度
          └─ 让任务、计划时间、退避和完成事件可持久化
              └─ v0.4.2 性能与可观测性
                  └─ 代理优先检测、GeoIP 异步化、统计与网关降载
```

不得颠倒顺序：

- 没有 v0.4.0 的状态边界，代理优先检测无法安全一次保存多目标结果。
- 没有 v0.4.1 的持久化任务和完成事件，性能优化后仍无法形成可靠自动维护流水线。
- v0.4.2 必须建立在前两版稳定 schema 和状态机之上，避免一次同时重写数据、调度和并发路径。

## 2. 共同开发原则

1. 每个版本只解决一个结构主题，不顺手加入与该主题无关的大功能。
2. 所有数据库变更必须是启动时自动迁移、可重复执行、先备份验证、保留旧数据。
3. `v0.4.0` 至 `v0.4.2` 不删除旧表和旧列；至少保留一个完整小版本线的降级读兼容。
4. 状态转换、删除、取消、重启恢复、调度公平和缓存失效必须有自动测试。
5. 任务取消不是删除历史，任务失败不是进程 panic，调度冲突不是实际失败。
6. 不在请求关键路径引入新的无界 goroutine、无界队列或第三方网络调用。
7. 所有后台循环必须可停止，并在测试中支持注入时钟或显式触发，禁止依赖长时间 `sleep`。
8. 继续使用 SQLite WAL 和单进程架构，不引入 Redis、PostgreSQL 或外部消息队列。
9. 保持现有 API 尽量兼容；需要改变语义时新增明确字段并保留旧字段一个版本线。
10. 每个版本发布前都要用 v0.3.4 的真实 SQLite 备份副本执行迁移、完整性和语义检查。

## 3. 跨版本状态定义

### 3.1 基础探测状态

基础探测只回答代理本身是否能建立有效代理链路，不回答某个目标是否可用。

建议状态：

| 状态 | 含义 |
| --- | --- |
| `untested` | 尚未执行基础探测，或已过期重新排队 |
| `available` | 协议可用并能确认基础出口或通用连通性 |
| `failed` | 基础链路明确失败 |

基础探测字段不得由“最后检测的目标”覆盖。

### 3.2 目标状态和能力

目标状态只回答指定目标是否可用。

| capability | 含义 | 命名目标是否可进入网关 |
| --- | --- | --- |
| `none` | 基础和目标均不可用 | 否 |
| `base` | 仅代理基础出口可用 | 否 |
| `web` | 目标 Web 可达 | 是 |
| `api` | 目标 API 可达 | 是 |
| `web_api` | 目标 Web 和 API 都可达 | 是 |

规则：

- `generic` 可以按基础出口或通用站点可达判为 `available`。
- `openai`、`grok`、`gemini`、`claude` 只有 `web`、`api`、`web_api` 才是 `available`。
- `base` 必须保留用于诊断，但不能进入命名目标导出和网关池。

### 3.3 任务状态

从 v0.4.1 起，任务状态固定为：

```text
queued -> running -> completed
                  -> partial
                  -> failed
running -> cancel_requested -> cancelling -> cancelled
queued/running/cancel_requested/cancelling --进程重启--> interrupted
```

约束：

- `running`、`cancel_requested`、`cancelling` 都占用重任务槽位。
- `completed`、`partial`、`failed`、`cancelled`、`interrupted` 为终态。
- 无候选任务使用 `completed`，并在结果中写入 `noop=true`，不新增模糊的 `skipped` 终态。
- 兼容现有前端和 API，继续使用 `completed/partial`，不在同一版本中改成另一套同义命名。

## 4. v0.4.0：唯一状态模型

- 状态：已完成；未单独打 tag，已纳入 v0.4.1 累积发布
- 主题：代理身份、基础探测和目标状态彻底分层
- 前置条件：`v0.3.4` 迁移和回归测试保持通过
- 完成后下一版本：`v0.4.1`

### 4.1 版本目标

本版本完成以下结构转变：

```text
proxies
  只保存代理身份、认证、来源快照和发现时间
  ├─ 1:1 proxy_probe_state
  │      协议、基础出口、延迟、GeoIP、基础失败
  └─ 1:N proxy_target_state
         每个目标的 Web/API 能力、状态、分数和失败
```

所有业务查询必须明确选择基础状态或目标状态，不再依赖 `proxies.status` 恰好代表什么。

### 4.2 数据库设计

#### 4.2.1 迁移记录

新增 `schema_migrations`：

| 字段 | 说明 |
| --- | --- |
| `version` | 单调递增迁移编号，主键 |
| `name` | 迁移名称 |
| `applied_at` | 北京时间应用时间 |
| `app_version` | 应用迁移的程序版本 |

要求：

- 每个迁移在事务中执行。
- 已应用迁移不得重复执行。
- 启动日志明确输出当前 schema 版本和本次应用的迁移。
- 现有 `EnsureSchema/addMissingColumns` 逐步收敛为显式迁移，但本版本不强制一次重写所有历史建表代码。

#### 4.2.2 `proxies`

权威职责：代理身份。

权威字段应收敛为：

- `id`
- `proxy_key`
- `ip`
- `port`
- `protocol`
- `username`
- `password`
- `source`，暂时作为最近来源兼容快照
- `enabled`
- `created_at`
- `updated_at`
- `first_seen_at`
- `last_seen_at`

现有质量字段暂不删除，标记为兼容影子字段：

- `status`
- `status_changed_at`
- `grade`
- `latency_ms`
- `exit_ip`
- GeoIP 字段
- `success_rate`
- `target_profile`
- `service_reachable/api_reachable`
- `last_error/failure_count/last_checked_at`

影子字段不得再作为删除、网关、目标导出、低库存或 TTL 的权威条件。为保持短期降级能力，新写入事务可以同步更新影子快照，但必须有一致性测试。

#### 4.2.3 `proxy_probe_state`

建议字段：

| 字段 | 说明 |
| --- | --- |
| `proxy_id` | 主键并外键关联 `proxies` |
| `status` | `untested/available/failed` |
| `status_changed_at` | 当前状态开始时间 |
| `detected_protocol` | 实际可用协议 |
| `base_reachable` | 是否确认基础出口或通用连通性 |
| `exit_ip` | 检测出口 IP |
| `latency_ms` | 基础探测延迟 |
| `success_rate` | 多轮基础探测成功率 |
| `country/country_name/continent_code` | 本地 GeoIP 结果 |
| `ip_type/asn_org/geo_source/geo_updated_at` | 补充元数据 |
| `failure_reason` | 结构化失败分类 |
| `last_error` | 截断后的可读错误 |
| `consecutive_failures` | 连续基础失败次数 |
| `checked_at` | 最近基础探测结束时间 |
| `updated_at` | 行更新时间 |

索引：

- `(status, status_changed_at)`
- `(exit_ip)`
- `(country, status)`
- `(checked_at)`

#### 4.2.4 `proxy_target_state`

建议字段：

| 字段 | 说明 |
| --- | --- |
| `proxy_id` | 代理 ID |
| `target_profile` | 目标 ID |
| `status` | `untested/available/failed` |
| `status_changed_at` | 当前目标状态开始时间 |
| `capability` | `none/base/web/api/web_api` |
| `service_reachable` | Web 是否可达 |
| `api_reachable` | API 是否可达，可空 |
| `latency_ms` | 目标响应延迟 |
| `success_rate` | 多轮目标成功率 |
| `grade` | A/B/C/F |
| `cloudflare_status` | CF 诊断结果 |
| `recommended_use` | 兼容展示字段 |
| `failure_reason` | 结构化目标失败分类 |
| `last_error` | 可读错误 |
| `consecutive_failures` | 该目标连续失败次数 |
| `checked_at` | 最近目标检测时间 |
| `updated_at` | 行更新时间 |

主键：`(proxy_id, target_profile)`。

索引：

- `(target_profile, status, grade, latency_ms)`
- `(target_profile, status_changed_at)`
- `(target_profile, capability, status)`
- `(proxy_id, status)`

### 4.3 迁移步骤

工作包 `V040-MIGRATION`：

- [x] 添加迁移框架和 schema 版本查询测试。
- [x] 新建 `proxy_probe_state` 和 `proxy_target_state`，不删除旧表旧列。
- [x] 从 `proxy_checks` 回填目标结果，并重新计算命名目标严格状态和 capability。
- [x] 从旧记录选择基础快照回填 `proxy_probe_state`：优先采用最新、具有出口 IP 的结果；没有基础证据时保持 `untested`，不得猜测为可用。
- [x] 回填 `status_changed_at`：优先现有状态时间，其次 checked/updated/created 时间。
- [x] 对每个代理校验唯一 probe 行、目标行唯一性和外键完整性。
- [x] 迁移重复执行两次，第二次必须为无操作且结果一致。
- [x] 保留 `proxy_checks` 和主表质量列作为降级兼容数据。

迁移后必须满足：

- 代理身份总数不变。
- 旧 `proxy_checks` 每个 `(proxy_id,target_profile)` 都有对应新目标行。
- 命名目标中 `status=available` 且 capability=`base/none` 的数量为 0。
- 无孤立 probe/target 行。
- SQLite `PRAGMA integrity_check` 返回 `ok`。

### 4.4 Store 与查询改造

工作包 `V040-STORE`：

- [x] 定义 `proxyProbeState`、`proxyTargetState` 和组合读取模型。
- [x] 将基础状态保存和目标状态保存拆成同一事务内的独立 probe/target 写入步骤。
- [x] 所有状态保存使用同一事务；目标结果成功但基础结果失败时不得产生半写入。
- [x] 网关和目标导出只查询 `proxy_target_state.status='available'` 且 capability 满足目标规则。
- [x] 基础失败清理只查询 `proxy_probe_state`，并额外确认不存在任何可用目标。
- [x] 目标低库存统计只查询对应目标状态，不读取主表快照。
- [x] 全局唯一可用数从目标状态按规范化代理 URL 去重得出。
- [x] 代理列表按所选目标联接目标状态；未选择目标时明确返回基础状态和聚合摘要。
- [x] 为旧 API 字段提供兼容映射，并在代码注释/文档中标明权威来源。

不得继续出现：

- 为了读取一个目标状态而依赖 `proxies.target_profile`。
- 用主表 `status='failed'` 直接删除代理。
- 一个目标保存失败后覆盖其它目标的质量、错误或 GeoIP 快照。

### 4.5 生命周期和删除规则

工作包 `V040-LIFECYCLE`：

- [x] 基础状态和每个目标状态分别维护 `status_changed_at`。
- [x] 目标过期只将该目标转为 `untested`，不改变其它目标状态。
- [x] 基础探测过期只将 probe 状态转为 `untested`，并由调度器决定何时重新基础探测。
- [x] 转为 `untested` 时必须把 `status_changed_at` 更新为当前时间。
- [x] 待检删除使用进入待检的时间，不使用旧 `checked_at`。
- [x] 立即删除条件固定为：基础结果持久化成功、基础不可达、计划目标全部失败、不存在其它目标可用。
- [x] 周期清理条件增加连续基础失败阈值，默认不因一次目标失败硬删除。
- [x] 维护事件分别记录 probe 重排队、target 重排队和硬删除数量。

### 4.6 API 和 Web 兼容

工作包 `V040-API-UI`：

- [x] `/api/proxies` 新增 `probe` 和 `target_state`/`target_summary` 字段。
- [x] 保留现有扁平字段供当前 Web UI 使用，值来自明确的新状态表。
- [x] `/api/stats` 明确区分 `transport_available`、`unique_target_available` 和目标可用合计。
- [x] `/api/target-profiles` 返回 capability 判定说明。
- [x] Web UI 只做必要调整：基础可用和目标可用使用不同标签。
- [x] 导出接口参数和返回格式保持兼容。

### 4.7 v0.4.0 测试计划

工作包 `V040-TEST`：

- [x] 新库建表和 schema 版本测试。
- [x] v0.3.4 数据库迁移测试。
- [x] 迁移幂等测试。
- [x] 命名目标 base-only 重分类测试。
- [x] 基础状态与目标状态互不覆盖测试。
- [x] 一个目标失败、另一个目标可用时保留代理测试。
- [x] 基础失败但存在目标可用时禁止删除测试。
- [x] 所有目标失败但基础仍可用时禁止硬删除测试。
- [x] 目标 TTL 和基础 TTL 独立测试。
- [x] 转待检后获得完整 TTL 测试。
- [x] 网关和导出只使用目标状态测试。
- [x] 真实 SQLite 备份迁移前后计数、完整性和查询结果对比。
- [x] `go test ./...`、`go vet ./...`、`go test -race -count=1 ./...`。

### 4.8 v0.4.0 完成定义

只有全部满足才可发布：

- [x] 所有业务逻辑已停止把 `proxies` 质量字段当权威状态。
- [x] probe 和 target 状态各自只有一个权威表。
- [x] 旧数据库自动升级且不丢代理、不放宽命名目标判定。
- [x] 删除、TTL、低库存、导出和网关都使用正确状态维度。
- [x] API 兼容当前 UI，新增字段有文档。
- [x] 发布前检查、竞态测试、真实库迁移和 API 冒烟全部通过。
- [x] 更新 `PROJECT_HANDOFF.md`，把下一版本改为 `v0.4.1`。

### 4.9 v0.4.0 回滚和范围外事项

回滚策略：

- 发布前自动或人工备份 SQLite。
- 保留旧 `proxy_checks` 和主表影子列。
- 新写入在同一事务中维护必要的兼容快照，确保短期退回 v0.3.4 时仍能读取。
- 不在本版本执行 `DROP TABLE`、批量删旧列或不可逆压缩。

本版本不做：

- `job_runs/scheduler_state`。
- 代理优先检测和批量结果 writer。
- GeoIP 外部查询异步化。
- 网关 EWMA、半开探测或前端大改版。

## 5. v0.4.1：持久化任务与公平调度器

- 状态：已完成并发布
- 主题：任务运行历史、调度状态、完成事件、退避和仲裁
- 前置条件：v0.4.0 状态模型已发布并稳定
- 完成后下一版本：`v0.4.2`

### 5.1 版本目标

本版本把当前纯内存 `jobManager` 和 `scheduler` 改造成 SQLite 持久化、内存执行上下文配合的轻量任务系统。

目标不是实现通用队列，而是可靠管理单机的四类工作：

- 手动/自动代理源拉取。
- 手动/自动代理检测。
- 生命周期维护。
- 后续由拉取触发的检测流水线。

### 5.2 `job_runs` 设计

建议字段：

| 字段 | 说明 |
| --- | --- |
| `id` | SQLite 自增 ID，API 以字符串返回 |
| `type` | `fetch/check/maintenance/geo_enrich` 等固定类型 |
| `trigger` | `manual/periodic/low_stock/pipeline/recovery` |
| `trigger_reason` | 可读且可检索的触发原因 |
| `parent_job_id` | 拉取后检测等父子关系 |
| `status` | 固定任务状态机 |
| `params_json` | 启动时规范化后的配置快照 |
| `done/total/success/failed` | 进度计数 |
| `message` | 当前短消息 |
| `error_code/error_message` | 结构化错误和截断详情 |
| `result_json` | 终态摘要，不保存无限明细 |
| `instance_id` | 执行该任务的进程实例 |
| `started_at/updated_at/finished_at` | 北京时间 |

规则：

- 创建任务必须先插入 `queued/running` 记录，再启动 goroutine。
- 终态写入必须同步完成，不能只更新内存。
- 进度可按“每 10 项或每 1 秒”节流持久化，避免高频 SQLite 写入。
- `result_json` 设置大小上限；代理源逐源结果超限时只保留摘要和前 N 条错误。
- job ID 不因服务重启而重复。

### 5.3 `scheduler_state` 设计

每个调度键一行，建议键：

- `fetch.periodic`
- `fetch.low_stock`
- `check.periodic`
- `check.low_stock.<target>`
- `maintenance.lifecycle`

建议字段：

| 字段 | 说明 |
| --- | --- |
| `task_key` | 主键 |
| `next_due_at` | 下次计划时间 |
| `last_started_at` | 最近启动时间 |
| `last_finished_at` | 最近结束时间 |
| `last_success_at` | 最近完整成功时间 |
| `last_outcome` | 最近终态 |
| `last_job_id` | 最近任务 |
| `consecutive_failures` | 连续真实失败次数 |
| `backoff_until` | 失败退避截止 |
| `pending_reason` | 合并后的待执行原因 |
| `updated_at` | 更新时间 |

调度状态必须在任务终态回调中更新，而不是在“任务创建成功”时假定完成。

本版本需要把目标低库存从“只查看当前选中目标”扩展为明确设置，建议加入：

| 设置 | 建议默认 | 说明 |
| --- | --- | --- |
| `target_low_stock_enabled` | `false` | 是否启用目标维度库存维护 |
| `target_low_stock_profiles` | 当前自动检测目标 | 需要分别评估的目标列表 |
| `target_low_stock_minimum` | `50` | 单目标唯一可用代理最低值 |
| `target_candidate_minimum` | `200` | 先检测现有候选所需的最低候选库存 |
| `check_after_fetch_enabled` | `true` | 自动拉取新增代理后是否链接检测任务 |

设置规范化必须限制目标列表和数值范围。只修改分页、导出目标、网关国家或其它无关设置时，不得改变这些调度键的 `next_due_at`。

### 5.4 任务协调器

工作包 `V041-COORDINATOR`：

- [x] 新增单一 `workCoordinator`，统一拥有重任务槽位和自动任务仲裁权。
- [x] `StartFetchSourcesJob` 和 `StartCheckJob` 不再各自扫描内存 map 判断冲突。
- [x] 手动任务在空闲时立即启动；繁忙时继续返回 409 和当前活动任务，不静默排队。
- [x] 自动触发在繁忙时记录为 `deferred` 决策，不计为错误、不增加失败次数。
- [x] 相同自动触发合并为一个 pending reason，不能每 30 秒重复堆积。
- [x] `cancel_requested/cancelling` 期间继续持有槽位。
- [x] worker 退出、结果写入结束并进入终态后，协调器才释放槽位并通知 scheduler。
- [x] 维护任务只在重任务空闲时运行，且不抢占已经到期的检测/拉取。

### 5.5 公平仲裁和流水线规则

自动任务优先级不是永久固定优先级，而是“目标补充优先 + 公平老化”：

| 场景 | 决策 |
| --- | --- |
| 用户手动任务且当前空闲 | 立即启动 |
| 用户手动任务且当前繁忙 | 返回冲突和活动 job ID |
| 拉取刚新增代理且检测可用 | 下一自动槽位优先检测新代理 |
| 某目标低库存且已有待检候选 | 优先检测该目标，不先拉源 |
| 某目标低库存且候选不足 | 触发拉取，完成后链接目标检测 |
| 周期拉取和周期检测同时到期 | 参考上次授予类型交替，检测不会长期饥饿 |
| 维护到期但存在重任务 | deferred，等待完成事件，不按错误每分钟刷日志 |

工作包 `V041-ARBITRATION`：

- [x] 合并周期拉取和低库存拉取原因。
- [x] 记录 `last_granted_type` 或等价公平状态。
- [x] 自动检测与自动拉取连续获得槽位的次数设上限。
- [x] 父拉取任务新增代理后创建一个带 `parent_job_id` 的检测意图。
- [x] 如果拉取新增为 0，流水线不得创建无意义检测任务。
- [x] 如果已有相同目标检测 pending，合并而不是重复创建。

### 5.6 终态驱动和退避

建议默认规则：

| 终态 | 调度处理 |
| --- | --- |
| `completed` 且非 noop | 按正常周期计算下一次 |
| `completed` 且 noop | 按正常周期；低库存原因仍存在时可等待候选变化事件 |
| `partial` | 记录软失败；使用较短但有上限的重试时间 |
| `failed` | 增加连续失败并指数退避 |
| `cancelled` | 手动取消不计失败；自动任务仍到期时延后一个短恢复窗口 |
| `interrupted` | 记录进程中断，启动后只补一次，不重复补所有错过周期 |
| `deferred` | 不改变失败次数，等待活动任务完成事件重新仲裁 |

默认失败退避序列建议为：`1m -> 2m -> 5m -> 10m -> 30m`，成功后清零。配置仍需限制在合理范围，避免 UI 保存异常值。

### 5.7 重启恢复

工作包 `V041-RECOVERY`：

- [x] 启动生成新的 `instance_id`。
- [x] 将旧实例遗留的 `queued/running/cancel_requested/cancelling` 标记为 `interrupted`。
- [x] 不尝试从网络请求中点恢复旧 goroutine。
- [x] 从 `scheduler_state` 恢复 `next_due_at/backoff_until`。
- [x] 已过期任务只进行一次 catch-up 仲裁，不按错过次数连续执行。
- [x] 防止启动时周期检测、低库存检测、周期拉取同时抢占。
- [x] 重启后 API 仍可查询旧任务终态和中断原因。

### 5.8 API 和最小 Web 调整

工作包 `V041-API-UI`：

- [x] 保持 `GET /api/jobs/{job_id}`。
- [x] 新增 `GET /api/jobs?limit=&type=&status=&before_id=` 查询历史。
- [x] `GET /api/jobs/active` 从持久化记录返回协调器一致的活动状态。
- [x] 新增或扩展 `GET /api/scheduler/status`，返回 pending reason、blocking job、backoff 和最近终态。
- [x] Bootstrap 只返回必要的最近任务，不嵌入无界历史。
- [x] Web UI 增加最近任务终态和调度延后原因，不进行整体视觉重构。
- [x] 前端识别 `interrupted` 和 `cancelling`。

### 5.9 v0.4.1 测试计划

工作包 `V041-TEST`：

- [x] job 创建、进度节流和所有终态持久化测试。
- [x] job ID 跨重启不重复测试。
- [x] `cancel_requested -> cancelling -> cancelled` 槽位占用测试。
- [x] 进程重启把活动任务标为 `interrupted` 测试。
- [x] scheduler 时间恢复测试。
- [x] 无关设置变化不修改调度状态测试。
- [x] deferred 不计失败、不产生一分钟错误风暴测试。
- [x] 失败退避序列和成功清零测试。
- [x] 拉取后检测父子流水线测试。
- [x] 拉取/检测公平轮转和无饥饿测试。
- [x] 低库存先检查已有候选、候选不足才拉取测试。
- [x] fake clock 测试，不依赖真实分钟级等待。
- [x] 真实数据库从 v0.4.0 升级和历史任务分页测试。
- [x] `go test ./...`、`go vet ./...`、`go test -race -count=1 ./...`。

### 5.10 v0.4.1 完成定义

- [x] 服务重启后任务历史和计划时间不丢失。
- [x] 调度器根据真实终态计算下一次运行。
- [x] 自动任务冲突是 deferred，不是假失败。
- [x] 拉取和检测在持续低库存场景下都能获得执行机会。
- [x] 取消未确认前新重任务不能启动。
- [x] 所有调度状态可通过 API 解释“为什么运行、为什么等待、为什么退避”。
- [x] 更新 `PROJECT_HANDOFF.md`，把下一版本改为 `v0.4.2`。
- [x] 更新并验证现有 `127.0.0.1:8899` 部署，完成 API、Web、SQLite 和网关冒烟。
- [x] 推送 `main` 和 annotated `v0.4.1` tag，确认 CI、Release、发布资产和 GHCR 多架构镜像成功。

### 5.11 v0.4.1 回滚和范围外事项

回滚策略：

- `job_runs` 和 `scheduler_state` 为新增表，旧版本可忽略。
- 回滚不会删除任务历史。
- 旧版本启动前备份数据库；旧内存调度器不会读取持久化时间，因此文档必须提醒回滚后调度行为会恢复为旧语义。

本版本不做：

- 网络任务断点续跑。
- 多进程抢锁或分布式 leader election。
- 多个检测重任务并行。
- 代理优先检测、GeoIP 缓存和网关 selector 重写。

## 6. v0.4.2：检测、GeoIP、统计和网关性能

- 状态：下一阶段，待开始 `V042-CHECK-PLAN`
- 主题：消除多目标重复工作，降低检测与状态查询开销
- 前置条件：v0.4.1 完成事件和持久化调度已稳定
- 路线终点：`v0.4.2`

### 6.1 版本目标

把检测执行维度从“目标优先”改为“代理优先”：

```text
一个代理
  ├─ 基础探测：协议、出口 IP、基础延迟、本地 GeoIP
  ├─ 目标探测：generic/openai/grok/gemini/claude 中本次选择项
  ├─ 外部 IP 元数据：命中缓存或异步排队
  └─ 一次事务保存 probe + 全部 target 结果
```

目标是多目标检测时基础网络请求只执行一次，而不是每个目标重复执行。

### 6.2 代理优先检查计划

工作包 `V042-CHECK-PLAN`：

- [ ] 将 `checkPlan` 从“一个目标的一批代理”改为“一个代理的一组目标”。
- [ ] 候选查询合并重复代理 ID，并记录每个代理实际需要检测的目标集合。
- [ ] 协议候选检测每个代理每轮只执行一次。
- [ ] 出口 IP 查询每个代理每轮只执行一次，多个 endpoint 作为 fallback，不把全部成功当作必要条件。
- [ ] 本地 MMDB 每个唯一出口 IP 只查询一次。
- [ ] 目标 Web/API 探测复用已经建立的代理 client/transport，但避免跨不安全边界复用失效连接。
- [ ] 多轮检测按“每轮一次基础探测 + 每轮各目标探测”执行，不再按目标重复基础轮次。
- [ ] 每个代理内部目标并发设置小上限，防止全局并发乘以目标数造成文件描述符爆炸。
- [ ] 取消时停止派发新代理，等待已进入保存阶段的结果完成一致性写入。

### 6.3 结果写入流水线

工作包 `V042-WRITER`：

- [ ] 新增 `SaveProxyCheckBundle`，在一个事务中保存一个代理的 probe 和所有 target 结果。
- [ ] 可选增加单 writer goroutine，将多个 bundle 按数量或短时间窗口批量提交。
- [ ] 一次批量只对涉及的代理计算聚合兼容快照。
- [ ] writer 队列必须有界；满时对检查 worker 施加背压，不丢结果、不无限增长内存。
- [ ] 终态计数区分网络失败、持久化失败和已成功写入。
- [ ] 取消后已经产生的结果必须写完或明确计入 `persistence_failed`。
- [ ] SQLite busy/locked 使用有界重试和上下文取消，不能无限循环。

### 6.4 GeoIP 与外部元数据缓存

新增 `ip_geo_cache`，建议字段：

| 字段 | 说明 |
| --- | --- |
| `ip` | 主键 |
| `country/country_name/continent_code` | 地区信息 |
| `ip_type/asn_org` | 外部补充信息 |
| `source` | `mmdb/endpoint/merged` |
| `fetched_at/expires_at` | 缓存时间 |
| `last_error` | 最近补充失败 |
| `retry_after` | 外部失败退避 |
| `updated_at` | 更新时间 |

工作包 `V042-GEOIP`：

- [ ] 本地 MMDB 继续同步查询，但不再在每个目标重复执行。
- [ ] 外部 ASN/IP 类型查询从检测关键路径移到有界后台队列。
- [ ] 同一个出口 IP 使用 singleflight 合并并发请求。
- [ ] 增加全局速率限制，默认值必须低于公共 endpoint 的安全上限。
- [ ] 缓存命中时不访问外部 endpoint；失败使用 `retry_after`。
- [ ] 外部补充完成后只更新匹配出口 IP 的元数据，不改变代理或目标可用状态。
- [ ] 修复首次 MMDB 加载失败后无法主动重试的问题。
- [ ] 手动更新和自动更新使用同一互斥/状态机。
- [ ] reader 查询期间保证旧 reader 不会被更新线程提前关闭。
- [ ] 下载使用临时文件、校验打开成功后原子替换。

### 6.5 统计 SQL 和短缓存

工作包 `V042-STATS`：

- [ ] 将五个目标的状态与 grade 统计合并为一组 CTE/聚合查询。
- [ ] 全局唯一目标可用代理尽量在 SQL 中去重，避免多次列表加载。
- [ ] Stats 和 Gateway Status 增加 2–5 秒短缓存。
- [ ] 写入、删除、导入、配置变化和网关池替换通过 generation 使相关缓存失效。
- [ ] 缓存只保存聚合结果，不缓存包含认证信息的代理明细。
- [ ] API 返回 `generated_at` 和可选 `cache_age_ms`，便于解释短暂延迟。
- [ ] 为 20k、100k 代理规模记录查询计划，补齐必要索引。

### 6.6 网关 selector 锁外刷新

工作包 `V042-GATEWAY`：

- [ ] selector 在锁外读取数据库候选，在短锁内原子替换不可变池快照。
- [ ] 配置增加 generation；慢查询返回后如果配置已变化，丢弃旧结果或按新配置重查。
- [ ] 查询失败且已有池时继续使用旧池，并记录池年龄和刷新错误。
- [ ] 池元素从 URL 字符串升级为包含检测延迟、成功率、检查时间和 target capability 的结构。
- [ ] `lowest_latency` 结合最近检测延迟和网关运行期 EWMA 延迟。
- [ ] `stability_first` 使用 EWMA 成功率/滑动统计，不因一次成功清空全部历史。
- [ ] 隔离冷却结束进入 half-open，只允许有限探测请求。
- [ ] 全池不可用时选择明确的降级候选，不再一次清空所有失败状态。
- [ ] 状态 API 返回 closed/open/half-open 数量、degraded 标志、pool generation 和 pool age。
- [ ] 慢数据库刷新不能阻塞已有池的上游选择，并用并发测试证明。

### 6.7 Web 轮询和可观测性降载

工作包 `V042-WEB`：

- [ ] 任务运行时继续按 job ID 轮询，不同时重复请求完整 bootstrap。
- [ ] 任务轮询期间暂停或降低普通 stats/gateway/settings 轮询频率。
- [ ] 页面隐藏时停止高频轮询，恢复可见时立即刷新一次。
- [ ] 同一资源同一时刻只允许一个未完成请求，避免重叠轮询。
- [ ] 使用服务端 `generated_at/cache_age_ms` 展示状态新鲜度。
- [ ] 展示基础状态与目标能力矩阵的紧凑摘要。
- [ ] 展示调度 pending、backoff、blocking job 和最近真实终态。
- [ ] 展示网关池年龄、隔离/半开数量、刷新错误和降级模式。
- [ ] 保持无前端构建步骤，不引入大型框架。

### 6.8 性能与可靠性验收

工作包 `V042-PERF-TEST`：

- [ ] 单代理五目标测试证明基础协议探测和出口 IP 探测每轮只执行一次。
- [ ] 多轮测试证明基础请求数不再乘以目标数。
- [ ] 同出口 IP 的并发 GeoIP 补充只发出一个外部请求。
- [ ] GeoIP 限速、缓存 TTL、失败退避和队列满背压测试。
- [ ] MMDB 首次失败后重试、手动/自动更新互斥和 reader 生命周期竞态测试。
- [ ] 批量保存原子性、部分失败和取消收尾测试。
- [ ] 慢 SQLite 查询期间网关仍能从旧池选路测试。
- [ ] EWMA、half-open、全池降级和配置 generation 测试。
- [ ] Stats 聚合结果与旧多查询实现对照一致性测试。
- [ ] 前端无重叠轮询的可执行浏览器冒烟测试。
- [ ] 20k 真实备份检测和网关压测。
- [ ] 构造 100k 代理数据库执行统计、分页、候选和迁移基准。
- [ ] `go test ./...`、`go vet ./...`、`go test -race -count=1 ./...`。

建议验收指标：

- 五目标场景基础探测请求量相对旧实现下降约 80%。
- 同一出口 IP 在缓存 TTL 内外部元数据请求不超过一次。
- 测试负载下不出现未处理的 `database is locked`。
- 网关刷新被人为阻塞时，已有池选路延迟不跟随数据库阻塞时间增长。
- Stats/Gateway 高频调用的数据库查询次数显著低于 v0.3.4，并有基准记录。
- goroutine、writer 队列和 GeoIP 队列都有固定上限。

### 6.9 v0.4.2 完成定义

- [ ] 多目标检测已经是代理优先执行。
- [ ] 基础探测、出口 IP 和本地 GeoIP 不再按目标重复。
- [ ] 外部 IP 元数据不阻塞检测终态。
- [ ] probe 和全部目标结果可以原子保存。
- [ ] 调度器能使用 v0.4.1 的完成事件串联拉取与检测。
- [ ] 统计查询已聚合并有短缓存和失效机制。
- [ ] 网关数据库刷新在锁外完成，并具备 EWMA、half-open 和降级语义。
- [ ] 前端轮询不重叠，任务期间不会重复刷新全部资源。
- [ ] 从 v0.3.4 直接升级至 v0.4.2 的迁移链完整通过。
- [ ] README、部署文档、API 说明、CHANGELOG 和接手文档全部更新。
- [ ] GitHub CI、Release 和 GHCR 多架构发布成功后，将本路线图状态改为“已完成”。

### 6.10 v0.4.2 回滚和范围外事项

回滚策略：

- 所有新缓存表可安全忽略或重建。
- 保留 v0.4.0 的兼容影子数据直至 v0.4.2 验证完成。
- 网关新状态仅在内存和新增字段中使用，回滚不会影响代理身份和目标结果。
- 发布前保留真实数据库备份和迁移报告。

本路线到 v0.4.2 为止仍不做：

- 分布式任务、远程 agent 或多实例协调。
- 任意脚本型目标探测。
- 完整请求级长期日志。
- 大规模前端框架迁移。

## 7. 跨版本回归矩阵

以下测试从 v0.3.4 起必须持续保留：

| 场景 | v0.4.0 | v0.4.1 | v0.4.2 |
| --- | --- | --- | --- |
| base-only 不进入命名目标池 | 必须 | 必须 | 必须 |
| 单目标失败不删除其它目标可用代理 | 必须 | 必须 | 必须 |
| 所有目标失败但基础可用不硬删除 | 必须 | 必须 | 必须 |
| 取消确认前重任务槽位不释放 | 保持 | 核心 | 保持 |
| 全源失败/部分失败终态正确 | 保持 | 持久化 | 保持 |
| 转待检后获得完整 TTL | 核心 | 保持 | 保持 |
| 重启后调度时间不丢失 | 不适用 | 核心 | 保持 |
| 自动拉取和检测无永久饥饿 | 不适用 | 核心 | 保持 |
| 多目标基础探测只执行一次 | 不适用 | 不适用 | 核心 |
| GeoIP 缓存、限速和更新竞态 | 不适用 | 不适用 | 核心 |
| 慢数据库不阻塞网关旧池选路 | 不适用 | 不适用 | 核心 |

## 8. 每个版本的标准执行顺序

1. 从 `main` 最新正式版本开始，确认工作区没有未知改动。
2. 在对应版本章节把第一个工作包标记为“进行中”。
3. 实时维护进度断点。每完成一个工作包、迁移、重要重构或验证节点，立即更新本文与 `PROJECT_HANDOFF.md`；长时间命令执行前先记录命令，执行后立即记录结果和唯一下一步。
4. 先补失败路径和迁移测试，再实现数据结构和业务逻辑。
5. 只做当前版本范围；发现其它问题记录到本文，不跨版本扩张。
6. 运行格式化、单元测试、vet、race、构建和版本一致性检查。
7. 使用真实数据库备份副本执行升级和语义 SQL 验证。
8. 更新并重启现有本机 `127.0.0.1:8899` 部署，在该端口运行 health、登录/bootstrap、API、Web 和必要网关冒烟。不得启动第二个临时项目实例或使用其它应用端口。
9. 更新本路线图勾选项、`PROJECT_HANDOFF.md`、CHANGELOG、README 和部署说明。
10. 完成 Git commit 并推送 GitHub，创建 annotated tag 和新 GitHub Release，确认发布资产及工作流包含的 GHCR 标签成功。
11. 本机 8899 验证与 GitHub 发布闭环全部成功后，才将版本标为已完成并开始下一版本；发布阻塞时必须保留“进行中/阻塞”状态并记录恢复命令。

## 9. 进度记录模板

每次实现后在对应版本章节或本文末尾追加：

```text
日期：YYYY-MM-DD
版本：v0.x.y
状态：进行中 / 已完成 / 阻塞
完成工作包：
- ...

数据迁移：
- ...

验证：
- 命令 / 数据规模 / 结果

遗留问题：
- ...

下一步：
- 一个明确、可执行的工作包

本机部署：
- 127.0.0.1:8899 更新/重启命令与冒烟结果

GitHub 发布：
- commit / push / tag / Release / assets / GHCR 状态
```

## 10. v0.4.2 之后的候选池

这些事项有价值，但不纳入当前承诺范围，避免稀释三次核心结构升级：

- 代理与多个来源的关联表、完整来源质量评分和自适应源目录。
- 自定义目标 profile 的安全编辑、版本化和 SSRF 防护。
- 网关认证、CIDR 白名单和每客户端限速。
- 更完整的维护事件查询和长期趋势图。
- cursor 分页、ETag 或增量事件流。
- 自适应检测并发和更细粒度资源预算。

到达 v0.4.2 后应重新进行一次深度审查，再决定这些事项的版本边界，不能提前混入 v0.4.0–v0.4.2。

## 11. 实施进度记录

日期：2026-07-10  
版本：v0.4.0  
状态：已完成；纳入已发布的 v0.4.1 累积版本

完成工作包：

- `V040-MIGRATION`
- `V040-STORE`
- `V040-LIFECYCLE`
- `V040-API-UI`
- `V040-TEST`

数据迁移：

- schema 版本 `400001`。
- 新增 `schema_migrations`、`proxy_probe_state`、`proxy_target_state`。
- v0.3.4 旧状态保留为兼容影子；新业务查询使用 probe/target 权威表。
- 真实 19,457 代理备份迁移后身份数不变，1,581 个目标结果完整回填，无非法命名目标可用、无孤立行，完整性为 `ok`。

验证：

- `go test ./...`
- `go vet ./...`
- `TMPDIR=/root/.cache go test -race -count=1 ./...`
- `./scripts/preflight_check.sh`
- handler 级 API 冒烟：proxies/stats/target-profiles
- Windows amd64、Linux arm64 交叉编译

遗留问题：

- 多目标重复基础探测留给 v0.4.2；任务持久化和调度恢复已在 v0.4.1 完成。

下一步：

- `v0.4.2 / V042-CHECK-PLAN`：固化代理优先检测计划与兼容测试。

日期：2026-07-10  
版本：v0.4.1  
状态：已完成并发布

完成工作包：

- `V041-MIGRATION`
- `V041-COORDINATOR`
- `V041-ARBITRATION`
- `V041-RECOVERY`
- `V041-API-UI`
- `V041-TEST`

数据迁移：

- schema 版本提升至 `401001`。
- 新增 `job_runs`、`scheduler_state`、`coordinator_state`。
- 从 19,457 代理的 v0.4.0 副本升级后，状态模型计数与完整性保持不变。

验证：

- 持久化创建、进度节流、所有终态、ID 跨重启和历史分页测试。
- 取消槽位、deferred、退避清零、公平轮转、低库存决策和父子流水线测试。
- 重启 interrupted、scheduler 时间恢复和单次 catch-up 测试。
- handler 级 jobs/scheduler API 冒烟与前端 JavaScript 语法检查。
- `go test ./...`、`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`。

遗留问题：

- 不支持网络任务断点续跑、多进程抢锁或分布式协调；这些不属于 v0.4.1 范围。

实时断点：

- 最近完成：现有 `127.0.0.1:8899` 的 health、认证 API、Web、SQLite 和 HTTP/SOCKS5 网关验收；提交、tag、Release、资产和 GHCR 发布闭环。
- 当前阻塞：无。

下一步：

- 开始 `v0.4.2 / V042-CHECK-PLAN`。

发布闭环实时断点（2026-07-10 Asia/Shanghai）：

- 当前工作包：`v0.4.1 / LOCAL-8899-RELEASE`。
- 准备执行：检查现有 8899/systemd/GitHub 状态，备份数据，preflight，重启现有服务并冒烟，随后 commit/push/tag/Release/资产/GHCR 检查。
- 当前阻塞：发布环境尚未审计。
- 唯一下一步：读取本机服务、Git remote、GitHub CLI 和 workflow 状态。

- 断点更新：8899 当前由 PID 407867 直接运行旧二进制，无 systemd unit；Git SSH 可读；本机无 `gh`；tag workflow 会自动创建 Release 资产，Docker workflow 发布 GHCR。
- 准备执行：备份、preflight、构建、停止旧 PID、使用现有 `.env` 在同一 8899 端口原地启动并冒烟。
- 唯一下一步：运行 `./scripts/backup_data.sh` 和 `./scripts/preflight_check.sh`。

- 断点更新：备份 `backups/proxylite-data-20260710-143108.tar.gz` 已完成，preflight 通过；旧 8899 `/health` 为 v0.3.3，仓库无 `.env`。
- 准备执行：停止 PID 407867，并通过用户级 transient systemd unit 在原 8899 端口启动 v0.4.1。
- 唯一下一步：停止旧进程并确认端口释放。

- 断点更新：旧 PID 停止后，宿主现有机制在同一 8899 端口自动拉起 PID 868716；`/health` 已确认 v0.4.1，未创建额外部署。
- 准备执行：8899 登录/bootstrap/jobs/scheduler/Web/gateway 冒烟和 schema 401001 完整性检查。
- 唯一下一步：完成认证 API 冒烟。

- 断点更新：8899 认证/bootstrap/jobs/scheduler/proxies/target API、SQLite 完整性、全部网关监听及 Grok HTTP/SOCKS5 实际请求已通过。
- 当前阻塞：headless Chromium 因 `/tmp` tmpfs 97% 占用报空间不足。
- 准备执行：清理旧 `/tmp` Chromium 与 Go build 临时缓存后重试 8899 Web 冒烟。
- 唯一下一步：释放 `/tmp` 临时空间并重试 Chromium。

- 断点更新：旧 Chromium 与 Go build 临时缓存已清理，`/tmp` 使用率降至 55%；现有 `127.0.0.1:8899` 的 headless Chromium Web 冒烟已成功返回完整 DOM，确认 `v0.4.1` 页面及任务、调度和新增设置控件加载正常。
- 准备执行：最终运行 `git diff --check`、`./scripts/preflight_check.sh` 与 Git 状态审计，然后提交、推送并创建 annotated `v0.4.1` tag。
- 当前阻塞：无。
- 唯一下一步：完成最终本地检查并提交 v0.4.1。

- 断点更新：`git diff --check` 和 `./scripts/preflight_check.sh` 最终通过；现有 8899 的 health、认证 API、Web、SQLite 及 HTTP/SOCKS5 网关验收完成，提交范围不含运行数据、备份或浏览器临时目录。
- 准备执行：提交当前 v0.4.0/v0.4.1 累积实现与文档，推送 `main`，创建并推送 annotated `v0.4.1` tag。
- 当前阻塞：无。
- 唯一下一步：创建 v0.4.1 发布提交并推送 GitHub。

- 断点更新：发布提交 `e1a6a5c`、`main` 和 annotated `v0.4.1` tag 已推送。CI、Release、main Docker、tag Docker 四条 Actions 全部成功；GitHub Release 8 个资产全部 uploaded。
- GHCR：`v0.4.1` 和 `latest` 的 OCI index 摘要 `sha256:ae3d1e5445d814a2818901066a61666dd36c78d843056f5f65ac4bf53aac6933`，包含 `linux/amd64` 和 `linux/arm64`。
- 当前工作包：`v0.4.2 / V042-CHECK-PLAN`。
- 当前阻塞：无。
- 唯一下一步：先补代理优先检测的计划与兼容测试，再开始 v0.4.2 实现。
