# ProxyLiteChecker 专属优化升级方案

状态：草案  
基线版本：`v0.2.4`  
目标：保持单机轻量定位，在不引入 panel/agent 架构的前提下，提升网关稳定性、统计可解释性、自动维护能力和本机运维体验。

## 1. 产品定位

ProxyLiteChecker 的核心定位应继续保持清晰：

- 单机部署，一个 Go 进程完成 Web UI、API、SQLite、代理源、检测任务、导出和代理网关。
- 服务对象是“当前机器或同一内网项目需要稳定可用代理”的场景。
- 不发展成多节点控制面，不复制 ProxyPoolChecker 的 agent 调度体系。
- 重点优化代理质量维护和本机 HTTP/SOCKS5 网关消费体验。

和完整协议网关类工具相比，ProxyLiteChecker 不应该追求完整 sing-box 节点协议矩阵。Lite 的优势是能自己拉取、检测、去重、按目标维护库存；网关只是消费这些库存的本机入口。

## 2. 当前基线

当前已具备：

- SQLite 代理仓库，支持代理源拉取、手动导入、去重。
- 目标化检测：`generic`、`openai`、`grok`、`gemini`、`claude`。
- 每个目标独立保存检测结果。
- 固定目标网关入口：默认 `18080-18089`。
- HTTP/SOCKS5 网关按目标从可用代理池选择上游。
- 网关已改为内存上游池，默认 30 秒刷新，避免每个请求重新查库打乱轮询。
- 单目标默认上游限制 `PLC_GATEWAY_UPSTREAM_LIMIT=200`。
- Web UI 已区分唯一可用、目标可用合计、已装载目标槽位。

仍需要优化：

- 网关缺少失败隔离、失败重试、策略配置和有效请求/扫描噪声拆分。
- 网关配置主要依赖环境变量，面板只能展示，不能管理运行时策略。
- 代理源没有质量评分和自动降噪机制。
- 自动任务状态和维护结果还可以更清晰。
- 统计指标语义虽已修正，但需要在界面和 API 中进一步解释。
- 本机运维缺少正式 systemd 示例、备份/恢复、升级脚本和安全启动检查。

## 3. 总体升级原则

1. 每轮只做一个主题，不把网关、检测、UI、部署混在一个版本里。
2. 优先保证统计语义正确，再做展示美化。
3. 网关优化优先服务稳定消费，不牺牲代理仓库检测逻辑。
4. 配置要明确区分“修改后立即生效”和“修改后重启生效”。
5. 所有影响代理选择、删除、失败隔离的逻辑必须有 Go 测试。
6. Web UI 改动必须验证移动端和桌面端文本不溢出。

## 4. 推荐升级顺序

### 阶段 A：网关运行时稳定性

优先级：最高  
建议版本：`v0.3.0`

目标：把网关从“简单轮询入口”升级为“可观测、可恢复、可控策略的本机代理池”。

#### A1. 网关 selector 抽象

当前 `gatewayEndpoint` 已有内存池和轮询索引，建议进一步抽成内部 selector：

```text
gatewayEndpoint
  -> gatewaySelector
       upstreams
       index
       failures
       activeConnections
       lastRefresh
       lastRefreshError
       strategy
       retryAttempts
       failureThreshold
       failureCooldown
```

具体改动：

- 新增 `gatewaySelector` 类型，专门负责上游池刷新、候选过滤、选择策略、失败记录。
- `gatewayEndpoint` 保留监听端口、请求计数、最近上游和状态快照。
- `selectUpstream` 改为 `selector.Next()`，返回上游和选择原因。
- `recordSuccess` / `recordFailure` 同步更新 selector 中的失败窗口。
- 当所有上游都被隔离时，自动释放失败窗口一次，并记录 `last_error`，避免全池死锁。

验收标准：

- 轮询顺序稳定。
- 库内新增/删除代理只在刷新窗口后影响运行池。
- 某个上游连续失败后会被临时跳过。
- 所有上游失败时不会永久无上游可用。
- 测试覆盖轮询、刷新、失败隔离、全隔离释放。

#### A2. 网关失败重试

参考成熟代理网关的 retry 思路，但保持 Lite 简化实现。

HTTP 普通请求、HTTP CONNECT、SOCKS5 CONNECT 都应支持：

- 第一次选择上游失败时，自动尝试下一个候选。
- `retry_attempts` 默认为 `2` 或 `3`。
- 同一个请求内尽量不重复尝试同一个上游。
- 最终失败时把最后一个错误写入状态。

建议配置：

| 配置 | 默认值 | 生效方式 | 说明 |
| --- | --- | --- | --- |
| `gateway_retry_attempts` | `2` | 立即生效 | 单个请求最多尝试几个上游 |
| `gateway_failure_threshold` | `3` | 立即生效 | 连续失败几次临时隔离 |
| `gateway_failure_cooldown_seconds` | `300` | 立即生效 | 失败隔离时长 |

验收标准：

- 构造第一个上游拨号失败、第二个成功时，请求成功。
- 请求失败计数不被重试次数错误放大。
- 最近上游能显示最终成功的上游。
- 失败上游在冷却时间内不会被继续选择。

#### A3. 调度策略

初期建议提供 3 种策略：

| 策略 | 说明 | 适用场景 |
| --- | --- | --- |
| `round_robin` | 稳定轮询 | 默认，最可解释 |
| `lowest_latency` | 优先低延迟 | 需要响应速度 |
| `stability_first` | 失败少、成功率高优先 | 需要稳定性 |

暂不建议加入 `random`，因为随机不利于定位问题。后续如需要再补。

实现要点：

- `lowest_latency` 使用最近检测延迟，不在请求路径上实时测速。
- `stability_first` 排序维度：隔离状态、失败次数、成功率、延迟、URL。
- 轮询仍作为无足够数据时的兜底。

验收标准：

- 每种策略有单元测试。
- 策略切换后下一次选择立即按新策略执行。
- UI 明确展示当前策略。

### 阶段 B：网关配置面板化

优先级：高  
建议版本：`v0.3.2`

目标：把用户经常需要调整的网关运行策略放到 Web UI，减少改环境变量和重启的成本。

#### B1. 配置边界

建议分两类：

修改后重启生效：

- 监听地址。
- HTTP/SOCKS5 端口。
- 目标入口列表。
- 是否启用 SOCKS5。

修改后立即生效：

- 上游装载上限。
- 刷新间隔。
- 调度策略。
- 重试次数。
- 失败隔离阈值和冷却时间。
- 请求超时。

如果短期不想改动启动流程，可以先只做展示和持久化，提示“重启后生效”；但建议优先让运行时策略热生效。

#### B2. 数据模型

可选方案：

方案一：继续放在 `app_settings` JSON 中。

- 优点：改动小。
- 缺点：设置字段越来越多，迁移和校验不清晰。

方案二：新增 `gateway_settings` 表。

- 优点：语义清晰，后续可拓展目标级配置。
- 缺点：需要 schema 迁移。

建议采用方案二：

```text
gateway_settings
  id = 1
  upstream_limit
  refresh_seconds
  request_timeout_seconds
  strategy
  retry_attempts
  failure_threshold
  failure_cooldown_seconds
  updated_at
```

验收标准：

- API `GET /api/gateway/config` 返回当前有效配置。
- API `POST /api/gateway/config` 校验并保存配置。
- 保存后 gateway selector 读取新配置。
- UI 标注哪些字段需要重启。

#### B3. 安全配置

当前本机网关如果暴露到公网，缺少访问保护。建议增加：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `gateway_access_token` | 空 | HTTP 用 `X-Proxy-Auth` 或 Basic，SOCKS5 用密码 |
| `gateway_allowed_ips` | 空 | CIDR/IP 白名单 |
| `gateway_rate_limit_per_minute` | `0` | 每客户端 IP 限速 |

默认仍允许无认证，避免破坏现有本机使用；但 UI 必须在 `0.0.0.0` 监听且无保护时显示明确风险提示。

### 阶段 C：网关统计和诊断

优先级：高  
建议版本：`v0.3.2`

目标：让“为什么没用也有请求数”“为什么装载少于可用”“当前网关是否健康”在界面上直接解释清楚。

#### C1. 请求计数拆分

当前 `total_requests` 会包含端口扫描、错误握手、无效请求。建议拆分：

| 字段 | 说明 |
| --- | --- |
| `total_connections` | 入口连接总数 |
| `valid_requests` | 通过 HTTP/SOCKS 协议解析的有效请求 |
| `rejected_requests` | 鉴权失败、IP 不允许、限速、无效握手 |
| `upstream_attempts` | 实际尝试上游次数 |
| `success_requests` | 最终转发成功 |
| `failed_requests` | 最终转发失败 |

UI 展示：

- 总连接：包含扫描噪声。
- 有效请求：真实代理请求。
- 上游尝试：重试后可能大于有效请求。

验收标准：

- SOCKS5 非 0x05 握手计入 rejected，不计入 valid。
- HTTP 乱扫路径计入 rejected 或 invalid，不误导为真实代理使用。
- UI tooltip 解释端口扫描会让连接数增加。

#### C2. 网关事件日志

不建议一开始保存所有请求，容易写爆 SQLite。建议保存滚动事件：

- 内存保留最近 100 条事件。
- 可选 SQLite 事件表，只保存失败和隔离事件。

事件字段：

```text
time
target_profile
gateway_type
client_ip
upstream_masked
event_type
message
duration_ms
```

UI 增加“网关诊断”折叠区：

- 最近失败。
- 当前隔离上游。
- 最近刷新错误。
- 最近刷新时间。
- 当前池年龄。

#### C3. 统计口径固定说明

在 API 和 UI 中保留明确口径：

| 名称 | 口径 |
| --- | --- |
| 全局可用 | 全库唯一可用代理 URL 数，跨目标去重 |
| 目标可用合计 | 每个目标可用数相加，同一个代理覆盖多个目标会重复 |
| 已装载目标槽位 | 网关当前内存池数量之和，受单目标上限影响 |
| 单目标上限 | 每个目标最多装载多少唯一上游 |

验收标准：

- 页面上不再需要靠人工解释 “438、869、400” 这类数值关系。
- API 字段命名保持兼容，但新增更明确字段。

### 阶段 D：代理源质量和自动维护

优先级：中高  
建议版本：`v0.4.0`

目标：降低低质量公开源带来的噪声，让自动任务更像维护系统，而不是简单定时执行。

#### D1. 代理源健康评分

新增每个内置源/手动源的最近表现：

| 字段 | 说明 |
| --- | --- |
| `last_fetch_at` | 最近拉取时间 |
| `last_fetch_status` | 成功/失败 |
| `last_imported` | 最近导入数量 |
| `last_new` | 最近新增数量 |
| `recent_available` | 最近检测后可用数量 |
| `failure_streak` | 连续拉取失败 |
| `disabled_until` | 自动暂停到什么时候 |

策略：

- 连续拉取失败 N 次后自动冷却。
- 导入数量长期为 0 的源降低优先级。
- 最近可用率极低的源减少自动拉取频率。
- 手动拉取仍可绕过自动冷却。

验收标准：

- 自动拉取不会一直卡在长期失败源。
- UI 显示源质量和最近结果。
- 测试覆盖连续失败冷却和手动绕过。

#### D2. 目标维度低库存补源

当前低库存更偏全局待检数量。建议加入目标维度：

- OpenAI 可用少于阈值时，优先检测待检/过期代理的 OpenAI 目标。
- Grok 可用少于阈值时，优先检测 Grok。
- 目标维度低库存不要盲目拉更多源，先复检已有库存。

可配置：

```text
target_low_stock_enabled
target_low_stock_minimum
target_low_stock_profiles
```

验收标准：

- 某个目标可用数低时，自动检测优先补该目标。
- 不影响导出目标选择。
- UI 显示触发原因。

#### D3. 生命周期维护日志

维护动作需要可追溯：

- 过期可用转待检。
- 待检过期删除。
- 失败即删。
- 失败记录保留但降权。

建议新增 `maintenance_events` 表或复用 run log：

```text
time
action
count
target_profile
reason
settings_snapshot
```

### 阶段 E：检测引擎优化

优先级：中  
建议版本：`v0.4.1`

目标：让检测更准确、更省资源、更适合目标服务。

#### E1. 自适应并发

建议先做简单规则：

- 连续网络错误多时自动降低并发。
- 成功率高且延迟稳定时允许提高到配置上限。
- SQLite 写入慢时降低批量写频率。

不要一开始做复杂算法，先保守实现。

验收标准：

- 并发不会超过用户设置上限。
- 日志中能看到自动降并发原因。
- 测试覆盖边界值。

#### E2. 检测目标配置化

当前目标是代码内置。后续可支持自定义目标：

```text
target_profiles
  id
  label
  probe_url
  api_url
  success_statuses
  required_keywords
  headers_json
  timeout_seconds
  enabled
```

短期建议只开放“读取和展示”，不要马上支持用户任意编辑，避免错误配置影响检测准确性。

#### E3. 失败原因分类

标准化失败原因：

- DNS 失败。
- TCP 连接失败。
- TLS 失败。
- 代理认证失败。
- HTTP 状态异常。
- 目标关键字不匹配。
- Cloudflare 拦截。
- 超时。

收益：

- 代理仓库可以按失败原因筛选。
- 自动删除策略可以更稳。
- 网关失败隔离可复用同一分类。

### 阶段 F：Web UI 体验优化

优先级：中  
建议版本：`v0.4.2`

目标：让单页控制台继续保持紧凑，但信息更易解释。

建议改动：

- 顶部统计卡增加 tooltip，解释统计口径。
- 网关卡片展示：
  - 当前策略。
  - 装载池年龄。
  - 隔离上游数。
  - 有效请求/扫描连接。
  - 最近错误。
- 代理源区域增加源质量列。
- 任务区增加“为什么跳过/为什么失败”的短原因。
- 代理仓库增加失败原因、目标可用矩阵的紧凑展示。
- 设置面板按“拉取、检测、维护、网关、安全”分组。

验收标准：

- 移动端不出现按钮文字溢出。
- 网关地址复制仍带协议前缀。
- 没有卡片套卡片。
- 所有长错误文本截断并可 hover 查看。

### 阶段 G：运维和发布质量

优先级：中  
建议版本：`v0.5.0`

目标：让单机部署更像长期服务。

#### G1. systemd 和更新脚本

新增：

- `docs/deployment.md`
- `scripts/install_systemd.sh`
- `scripts/update_local.sh`
- `scripts/backup_data.sh`
- `scripts/restore_data.sh`

systemd 示例需要说明：

- 工作目录。
- 环境变量文件。
- 数据目录。
- 自动重启。
- 日志查看。

#### G2. 安全启动检查

建议引入：

```text
PLC_REQUIRE_SECURE=1
```

开启后拒绝：

- 默认管理员密码。
- 默认 secret。
- 网关公网监听但无 token/IP 白名单。

#### G3. 版本一致性检查

当前 Lite 没有版本一致性脚本。建议补：

- `cmd/proxylite/main.go`
- `app/web/index.html`
- `.env.example`
- `docker-compose.yml`
- `README.md`
- `CHANGELOG.md`

验收标准：

- preflight 自动检查版本一致。
- tag 版本不一致时 CI 失败。

## 5. 不建议近期做的事

- 不建议把 Lite 改成多节点架构；那会和 ProxyPoolChecker 重叠。
- 不建议直接引入 sing-box 作为核心转发层；依赖体积和协议复杂度会破坏 Lite 的轻量定位。
- 不建议一次性开放自定义检测目标的全部编辑能力；先把数据结构和展示做稳。
- 不建议把所有请求事件长期落库；公网端口扫描会产生大量噪声。
- 不建议默认开启网关鉴权破坏现有使用，但必须给公网场景强提醒。

## 6. 优先级总表

| 优先级 | 阶段 | 主题 | 主要收益 |
| --- | --- | --- | --- |
| P0 | A | 网关 selector、失败隔离、重试 | 代理入口稳定性 |
| P0 | B | 网关策略配置面板化 | 用户可调、少重启 |
| P1 | C | 请求统计拆分和诊断 | 解释扫描请求和真实使用 |
| P1 | D | 代理源质量和目标低库存 | 降低噪声，提高可用库存 |
| P2 | E | 检测引擎优化 | 准确率和资源控制 |
| P2 | F | UI 解释能力 | 减少误读和操作成本 |
| P2 | G | 运维脚本和安全启动 | 长期部署可靠性 |

## 7. 下一次升级建议

下一次建议先做阶段 A，不要同时改 UI 大布局。

最小可交付范围：

1. 新增 `gatewaySelector`。
2. 增加失败隔离和重试。
3. 状态接口增加 `active_upstreams`、`skipped_upstreams`、`strategy`、`last_refresh_at`、`last_refresh_error`。
4. UI 只做最小展示。
5. 补齐单元测试。
6. 发布一个小版本。

这样风险最低，也能直接改善你现在最关心的网关轮询和可用性问题。
