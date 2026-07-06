# GeoIP 与地区网关开发进度

状态：第一轮已完成
开始时间：2026-07-05
范围：ProxyLiteChecker 单机版 GeoIP 识别、地区化存储、网关国家过滤和 UI 设置。

## 当前目标

第一轮实现可运行闭环：

- 扩展 GeoIP 元数据结构，支持可选本地 MMDB，并保留现有外部 endpoint 回退。
- 扩展 SQLite schema 和检测结果保存，记录国家名称、洲、GeoIP 来源和更新时间。
- 支持代理列表国家筛选。
- 支持网关按国家代码过滤上游，并提供 strict/fallback_any 策略。
- Web UI 增加网关国家限制配置和基础状态展示。
- 补充测试，确保旧配置不受影响。

## 进度记录

| 时间 | 状态 | 记录 |
| --- | --- | --- |
| 2026-07-05 | 开始 | 建立开发进度文档，准备审查 Lite 现有 schema、settings、checker、gateway 和 Web UI。 |
| 2026-07-05 | 已完成 | 审查 `main.go`、`settings.go`、`store.go`、`checker.go`、`gateway.go`、Web UI 和现有测试；确认第一轮可基于现有 `proxy_checks` 目标维度结果扩展。 |
| 2026-07-05 | 已完成 | 接入 `geoip2-golang` Country MMDB 读取、默认下载源、启动初始化、24 小时自动更新和手动更新 API。 |
| 2026-07-05 | 已完成 | 扩展检测结果、SQLite schema、迁移列、列表扫描和保存逻辑，记录国家名称、洲、GeoIP 来源、更新时间。 |
| 2026-07-05 | 已完成 | 增加国家代码规范化、代理列表过滤、导出过滤、网关 `strict/fallback_any` 国家策略和 selector 过滤。 |
| 2026-07-05 | 已完成 | Web UI 增加网关国家限制、国家策略、GeoIP 状态/更新、代理列表国家筛选和国家显示。 |
| 2026-07-05 | 已验证 | `node --check app/web/static/app.js`、`go test ./...`、`go build -o /tmp/proxylite-check ./cmd/proxylite` 均通过。 |

## 待办清单

- [x] 审查关键代码路径和测试覆盖。
- [x] 引入可选 MMDB 依赖与 GeoIP reader。
- [x] 扩展 `checkmeta.Metadata` 和回退逻辑。
- [x] 扩展 `proxies` / `proxy_checks` schema。
- [x] 扩展 `CheckResult`、`SaveCheckResult`、列表扫描。
- [x] 增加国家筛选和国家规范化辅助函数。
- [x] 扩展网关配置、selector 刷新和状态输出。
- [x] 更新 Web UI 设置保存/回填和网关展示。
- [x] 更新 `.env.example` / README 或部署说明。
- [x] 运行 `go test ./...` 和构建。

## 恢复提示

如果开发中断，优先检查：

```bash
git -C /root/ProxyLiteChecker status --short
go test ./...
```

然后从本文件的待办清单继续。
