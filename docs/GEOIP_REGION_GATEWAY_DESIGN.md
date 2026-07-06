# ProxyLiteChecker GeoIP 与地区网关设计方案

状态：设计草案
适用项目：`/root/ProxyLiteChecker`
目标版本建议：`v0.4.x`
最后更新：2026-07-05

## 1. 目标

ProxyLiteChecker 是单机轻量版，一个 Go 进程同时负责 Web UI、API、SQLite、代理检测、导出和本机 HTTP/SOCKS5 网关。本方案的目标是在不引入 Panel/Agent 架构的前提下，增加完整的代理出口地区识别、地区化库存管理和地区化网关上游选择能力。

核心目标：

1. 使用 GeoIP 识别代理出口 IP 所属国家/地区、洲、ASN 和网络类型。
2. 将地区信息稳定写入代理仓库和目标维度检测结果，支持查询、筛选和统计。
3. 本机网关可按国家代码选择特定地区代理，例如只使用 `US,JP,SG`。
4. GeoIP 数据库支持每天自动更新，失败时保留旧库，确保识别结果尽量新。
5. Web UI 能清晰展示地区库存、地区筛选、网关地区策略和更新状态。

非目标：

- 不引入多节点调度。
- 不把 ProxyPoolChecker 的 Panel/Agent 概念搬入 Lite。
- 不内置或提交商业/免费 GeoIP 数据库文件。
- 第一阶段不做按客户端请求动态切换国家，例如通过 HTTP header 或用户名后缀选择地区。

## 2. 当前基线

当前项目已有基础能力：

- `internal/checkmeta` 会在检测拿到出口 IP 后补充 `country`、`ip_type`、`asn_org`。
- SQLite 表 `proxies` 和 `proxy_checks` 已有 `country`、`ip_type`、`asn_org` 字段。
- 检测结果按 `target_profile` 保存，网关也按目标 profile 提供固定入口。
- 网关 selector 已有内存上游池、轮询/低延迟/稳定优先、失败隔离和热更新配置。

当前不足：

- `country` 目前主要依赖外部 IP 元数据接口，准确性、可用性和速率不可控。
- 没有本地 GeoIP 数据库和自动更新机制。
- `country` 只有单字段，缺少国家名称、洲、来源、更新时间等可解释元数据。
- 网关查询可用上游时不能按国家过滤。
- UI 没有国家分布、国家筛选和 GeoIP 更新状态。

## 3. GeoIP 数据来源设计

### 3.1 推荐数据源

第一优先级：使用 `github.com/oschwald/geoip2-golang` 读取 Country MMDB。

默认数据库文件：

```text
GeoLite2-Country.mmdb
```

默认下载地址：

```text
https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb
```

第一阶段只要求 Country MMDB，用于识别国家代码和国家名称。不随项目分发数据库文件；服务启动时如果本地文件不存在，可按默认 URL 下载，也可由部署者挂载现有 MMDB 文件。

可选补充：

- 当前已有 `PLC_IP_METADATA_ENDPOINT`，继续作为外部回退。
- ASN、IP 类型、洲代码可以继续由外部 endpoint 补充；后续如确有需要，再增加独立 ASN MMDB 或其它查库工具。

### 3.2 查询优先级

`checkmeta.EnrichIP` 的推荐优先级：

1. 本地 MMDB 查询。
2. 外部 endpoint 查询。
3. 基础 IP 类型判断。

合并规则：

- 国家代码以本地 MMDB 为准。
- ASN 和 IP 类型优先使用外部 endpoint 的 `org/isp/as/proxy/hosting/mobile` 信号；Country MMDB 只负责国家识别。
- `ip_type` 先用本地/外部 proxy、hosting、mobile 信号；没有时使用现有启发式。
- 任一来源失败不能影响代理检测主体结果，只降级为少量字段为空。

### 3.3 标准输出字段

内部统一结构建议：

```go
type Metadata struct {
    CountryCode   string
    CountryName   string
    ContinentCode string
    IPType        string
    ASNOrg        string
    Source        string
    UpdatedAt     time.Time
}
```

兼容约定：

- 现有 JSON 字段 `country` 继续保留，值使用 ISO 3166-1 alpha-2 大写国家代码，例如 `US`、`JP`。
- 新增字段使用更明确名称：`country_name`、`continent_code`、`geo_source`、`geo_updated_at`。

## 4. GeoIP 每日自动更新设计

### 4.1 配置项

建议新增环境变量：

```text
PLC_GEOIP_ENABLED=true
PLC_GEOIP_DB_DIR=data/geoip
PLC_GEOIP_COUNTRY_DB=data/geoip/current/GeoLite2-Country.mmdb
PLC_GEOIP_COUNTRY_URL=https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb

PLC_GEOIP_UPDATE_ENABLED=true
PLC_GEOIP_UPDATE_AT=04:20
PLC_GEOIP_UPDATE_JITTER_SECONDS=600
PLC_GEOIP_UPDATE_MIN_INTERVAL_HOURS=20
PLC_GEOIP_UPDATE_RETAIN=3

PLC_GEOIP_UPDATE_PROXY=
```

说明：

- `PLC_GEOIP_UPDATE_AT` 使用北京时间，符合项目已有时间展示习惯。
- `PLC_GEOIP_UPDATE_MIN_INTERVAL_HOURS=20` 避免频繁重启导致重复下载。
- `PLC_GEOIP_COUNTRY_URL` 为空时不自动下载，只读取本地文件。
- `PLC_GEOIP_UPDATE_PROXY` 可用于下载 GeoIP 库时走指定 HTTP 代理。

### 4.2 更新流程

每日更新任务在服务启动后由后台 goroutine 管理：

1. 启动时检查本地数据库是否存在。
2. 如不存在且配置了下载 URL，立即尝试一次下载。
3. 每天 `PLC_GEOIP_UPDATE_AT` 加随机抖动后执行更新。
4. 下载到临时文件：`data/geoip/downloads/GeoLite2-Country-{timestamp}.mmdb.tmp`。
5. 校验文件大小、MMDB metadata 和可选 sha256。
6. 使用 `geoip2-golang` 打开文件并查询固定测试 IP，确认文件可读。
7. 写入 manifest：构建时间、sha256、文件大小、下载时间。
8. 原子替换 `data/geoip/current/GeoLite2-Country.mmdb`。
9. 热加载 reader，新的检测立即使用新库。
10. 保留最近 N 个历史版本，删除更旧版本。

失败策略：

- 下载失败：记录错误，继续使用旧库。
- 校验或打开失败：删除临时文件，继续使用旧库。
- 校验失败：拒绝切换，继续使用旧库。
- 热加载失败：回滚到旧 reader。

### 4.3 状态持久化

建议新增表：

```sql
CREATE TABLE IF NOT EXISTS geoip_update_state (
  id INTEGER PRIMARY KEY DEFAULT 1,
  enabled INTEGER NOT NULL DEFAULT 0,
  country_db_path TEXT NOT NULL DEFAULT '',
  country_build_epoch INTEGER,
  country_sha256 TEXT NOT NULL DEFAULT '',
  last_check_at TEXT,
  last_success_at TEXT,
  last_error TEXT,
  last_source TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
```

可选事件表：

```sql
CREATE TABLE IF NOT EXISTS geoip_update_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_type TEXT NOT NULL,
  edition TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
```

用途：

- UI 显示当前 GeoIP 数据是否可用、最后更新时间、失败原因。
- 排障时可以看最近更新记录。

## 5. 数据存储设计

### 5.1 表字段扩展

`proxies` 新增：

```sql
ALTER TABLE proxies ADD COLUMN country_name TEXT;
ALTER TABLE proxies ADD COLUMN continent_code TEXT;
ALTER TABLE proxies ADD COLUMN geo_source TEXT;
ALTER TABLE proxies ADD COLUMN geo_updated_at TEXT;
```

`proxy_checks` 新增：

```sql
ALTER TABLE proxy_checks ADD COLUMN country_name TEXT;
ALTER TABLE proxy_checks ADD COLUMN continent_code TEXT;
ALTER TABLE proxy_checks ADD COLUMN geo_source TEXT;
ALTER TABLE proxy_checks ADD COLUMN geo_updated_at TEXT;
```

索引：

```sql
CREATE INDEX IF NOT EXISTS idx_proxies_country ON proxies(country);
CREATE INDEX IF NOT EXISTS idx_proxy_checks_target_country_status
ON proxy_checks(target_profile, country, status);
```

### 5.2 出口 IP Geo 缓存

建议新增缓存表，避免同一个出口 IP 反复查询：

```sql
CREATE TABLE IF NOT EXISTS ip_geo_cache (
  ip TEXT PRIMARY KEY,
  country TEXT,
  country_name TEXT,
  continent_code TEXT,
  ip_type TEXT,
  asn_org TEXT,
  geo_source TEXT,
  geo_db_build_epoch INTEGER,
  created_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
```

缓存失效规则：

- GeoIP 数据库 build epoch 变更后，旧缓存可继续保留，但新检测优先重新查询并覆盖。
- 外部 endpoint 结果缓存建议最多保留 7 天。
- 本地 MMDB 查询很快，缓存主要用于减少外部 endpoint 请求和统一展示。

### 5.3 字段语义

| 字段 | 含义 |
| --- | --- |
| `country` | 出口 IP 国家代码，ISO alpha-2，大写 |
| `country_name` | 英文国家名称，第一阶段不做多语言映射 |
| `continent_code` | 洲代码，例如 `NA`、`AS`、`EU` |
| `ip_type` | `public`、`datacenter`、`residential`、`mobile`、`proxy` 等 |
| `asn_org` | ASN 组织名 |
| `geo_source` | `mmdb`、`endpoint`、`cache`、`local` |
| `geo_updated_at` | 当前记录 Geo 字段更新时间 |

## 6. 检测链路设计

检测时机不变：

1. 代理探测目标服务。
2. 获取出口 IP。
3. 对出口 IP 做 GeoIP 查询。
4. 保存检测结果。

保存规则：

- `proxy_checks` 保存目标维度的 GeoIP 信息。
- `proxies` 保存最近一次成功/默认结果，保持现有 UI 和统计兼容。
- 检测失败但拿不到出口 IP 时，不清空已有国家信息；只更新失败字段。
- 复测成功后覆盖旧国家信息。

需要注意：

- 代理服务器 IP 所在地不等于出口 IP 所在地。地区分类必须以 `exit_ip` 为准。
- 如果未来要展示代理入口地址所在地，可新增 `proxy_host_country`，但第一阶段不做。

## 7. 网关地区选择设计

### 7.1 全局配置

新增应用设置字段：

```go
GatewayCountries     []string `json:"gateway_countries"`
GatewayCountryPolicy string   `json:"gateway_country_policy"`
```

策略值：

| 值 | 说明 |
| --- | --- |
| `strict` | 只使用指定国家；没有匹配上游时返回无可用上游 |
| `fallback_any` | 优先指定国家；没有匹配上游时回退任意国家 |

默认值：

```text
gateway_countries=[]
gateway_country_policy=strict
```

空国家列表表示不限制地区。

### 7.2 查询改造

将当前：

```go
AvailableProxyURLs(limit, targetProfile)
CountAvailableProxyURLs(targetProfile)
```

升级为：

```go
type availableProxyFilter struct {
    TargetProfile string
    Limit         int
    Countries     []string
    CountryPolicy string
}
```

查询原则：

- `proxy_checks.status = 'available'`
- `proxy_checks.target_profile = endpoint.TargetProfile`
- `country IN (...)` 仅在 countries 非空时追加。
- 排序仍以 grade、latency、success_rate、checked_at 为主。
- `fallback_any` 只在地区查询结果为空时执行第二次无地区查询。

### 7.3 按目标配置

第一阶段建议只做全局国家过滤。第二阶段可加入按目标覆盖：

```json
{
  "gateway_country_rules": {
    "generic": [],
    "openai": ["US", "JP"],
    "grok": ["US"],
    "gemini": ["JP", "SG"],
    "claude": ["US", "GB"]
  }
}
```

优先级：

1. 目标专属规则。
2. 全局 `gateway_countries`。
3. 不限制国家。

### 7.4 状态输出

`/api/gateway/status` 建议新增：

```json
{
  "countries": ["US", "JP"],
  "country_policy": "strict",
  "country_distribution": {
    "US": 128,
    "JP": 42
  },
  "country_limited": true,
  "country_fallback_used": false
}
```

每个 profile 也输出同样字段，便于 UI 在单个目标网关卡片上展示。

## 8. Web UI 设计

### 8.1 设置页

在“网关运行时”区域增加：

- 国家限制输入：`US,JP,SG`
- 策略选择：`严格匹配` / `无匹配时回退`
- 当前 GeoIP 状态：数据库构建时间、最后更新、失败原因

交互规则：

- 输入统一转大写并去重。
- 无效国家代码显示表单提示，不保存。
- 空值显示为“不限制地区”。

### 8.2 代理列表

新增筛选：

- 国家：支持输入或下拉选择。
- 洲：第二阶段可加。
- IP 类型：复用已有 `ip_type` 字段。

表格展示：

- 国家列显示 `US`，鼠标悬浮显示 `United States`。
- 空国家显示 `-`。
- 可用代理按国家分布可在表格上方用紧凑标签展示：`US 120`、`JP 42`。

### 8.3 网关卡片

每个网关卡片显示：

- 当前国家策略：`US, JP · 严格匹配`
- 已加载上游：总数、活跃、隔离、地区匹配数
- 国家分布：最多展示前 5 个国家，其余合并为 `其他`
- 无匹配上游时提示 `指定国家无可用上游`

### 8.4 GeoIP 更新状态

可放在设置页或系统信息区域：

- `GeoIP：可用`
- `Country：2026-07-05`
- `上次更新：04:21`
- `失败：download url missing` 仅在失败时展示

## 9. API 设计

新增或扩展：

```text
GET /api/geoip/status
POST /api/geoip/update
```

`POST /api/geoip/update`：

- 需要登录。
- 手动触发后台更新。
- 如果已有更新正在运行，返回当前任务状态。

代理列表 API 扩展查询参数：

```text
country=US
countries=US,JP
continent=AS
has_country=true
```

导出 API 可选扩展：

```text
/api/export/proxies.txt?target_profile=openai&countries=US,JP
```

## 10. 配置和部署

`.env.example` 需要补充：

```text
# GeoIP
PLC_GEOIP_ENABLED=true
PLC_GEOIP_DB_DIR=data/geoip
PLC_GEOIP_COUNTRY_DB=data/geoip/current/GeoLite2-Country.mmdb
PLC_GEOIP_COUNTRY_URL=https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb
PLC_GEOIP_UPDATE_ENABLED=false
PLC_GEOIP_UPDATE_AT=04:20
```

Docker 部署建议：

```yaml
volumes:
  - ./data:/app/data
environment:
  PLC_GEOIP_ENABLED: "true"
  PLC_GEOIP_UPDATE_ENABLED: "true"
```

离线部署：

```text
data/geoip/current/GeoLite2-Country.mmdb
```

只要文件存在，即可不配置下载 URL。

## 11. 安全与合规

- 不把 GeoIP 数据库提交到 git。
- 不把带鉴权信息的下载 URL 输出到日志、状态 API 或 UI。
- 下载 URL、错误日志需要脱敏。
- MMDB 文件来自配置的下载 URL 或用户挂载，项目只提供读取和更新机制。
- 如果使用面向公网的下载代理，需明确该代理只用于 GeoIP 下载，不影响代理检测。

## 12. 测试计划

Go 单元测试：

- 国家代码规范化：`us, jp, SG` -> `US,JP,SG`。
- 无效国家代码过滤或报错。
- `strict` 无匹配返回空。
- `fallback_any` 无匹配时回退。
- GeoIP reader 文件不存在时不影响检测。
- GeoIP update 校验失败不切换当前库。

存储测试：

- schema 迁移为旧库补列。
- `SaveCheckResult` 写入新 Geo 字段。
- `AvailableProxyURLs` 按国家过滤。
- `CountAvailableProxyURLs` 按国家统计。

前端验证：

- 设置页国家输入保存/回填。
- 代理表国家筛选。
- 网关卡片国家策略展示。
- 移动端文本不溢出。

## 13. 分阶段开发计划

### 阶段 A：GeoIP 基础能力

- 引入 MMDB reader。
- 扩展 `checkmeta.Metadata`。
- 添加环境变量读取。
- 添加 GeoIP 状态 API。
- 添加 schema 迁移和结果保存字段。
- 保持现有外部 endpoint 回退。

验收：

- 没有 MMDB 时行为与当前一致。
- 有 Country MMDB 时检测结果写入国家代码和国家名称；ASN 和 IP 类型继续由外部 endpoint 或启发式补充。

### 阶段 B：每日自动更新

- 后台更新调度。
- Country MMDB 下载、校验、原子切换。
- 更新状态表和 UI 状态展示。
- 手动更新 API。

验收：

- 更新成功后 reader 热加载。
- 更新失败不影响旧库和代理检测。
- UI 能看到最后更新时间和失败原因。

### 阶段 C：网关地区过滤

- 添加设置字段和标准化。
- 改造上游查询和统计。
- 网关状态输出地区策略和分布。
- UI 设置项和网关卡片展示。

验收：

- `gateway_countries=US` 时网关只加载美国出口代理。
- `fallback_any` 在美国库存为空时能回退任意国家。

### 阶段 D：列表和统计体验

- 代理列表国家筛选。
- 国家分布统计。
- 导出 API 支持国家过滤。
- 文档和 `.env.example` 补齐。

## 14. 后续可扩展方向

- 按目标 profile 设置不同国家规则。
- 按用户名后缀或本地端口选择地区，例如 `user-us` 或 `18090=US`。
- 根据延迟和成功率自动推荐国家。
- 增加洲级过滤：`AS`、`EU`、`NA`。
- 支持更多 GeoIP 数据源和下载器。
- 对历史记录做批量 GeoIP 回填任务。
