# ProxyLiteChecker v0.4.6 真实流量熔断与 Cloudflare 严格判定路线图

- 状态：进行中
- 当前工作包：`V046-VALIDATION-AND-RELEASE`
- 基线版本：`v0.4.5`
- 目标版本：`v0.4.6`
- 主题：让真实网关流量更快隔离失效上游，并阻止 Cloudflare challenge/blocked 代理进入对应目标池

## 1. 硬性约束

- [ ] 保持单机、单 Go 二进制、SQLite 和原生 Web UI，不增加外部常驻服务。
- [ ] 主动检测继续决定持久化目标资格；真实流量只维护运行期短期熔断，不因单次请求直接覆盖数据库权威状态。
- [ ] 只把可归因于上游代理的错误计入熔断；客户端取消、客户端写失败和服务端 Hijack 能力错误不得误伤代理。
- [ ] Cloudflare `behind_cf` 只表示目标位于 Cloudflare 后方；仅 `challenge` 和 `blocked` 判为对应目标不可用。
- [ ] 保持重试不重复使用同一上游、half-open 单探针、全池降级和旧池刷新失败回退语义。
- [ ] 使用显式 schema `406001` 事务迁移修正历史 Cloudflare 非法可用状态，迁移前备份真实运行数据。
- [ ] 只更新现有 `127.0.0.1:8899` 服务，不启动第二套临时部署。

## 2. 工作包

### V046-BASELINE-AND-DESIGN

- [x] 读取接手文档、既有路线图、网关选择器、HTTP/SOCKS5 转发和 Cloudflare 检测实现。
- [x] 通过版本一致性、全量单测和现有 8899 health 基线。
- [x] 确认缺口：任意 HTTP 响应均记成功、隧道建立后立即记成功、客户端侧错误可能误伤上游、Cloudflare 状态未参与最终目标资格。
- [x] 固化失败分类、隧道结果和 Cloudflare 严格语义的回归测试。

### V046-CLOUDFLARE-STRICT

- [x] Web 与 API 探测都识别 Cloudflare 状态，并按 `blocked > challenge > behind_cf > not_cf` 合并最严重结果。
- [x] HTTP 200 challenge 页面不得计为 Web 可达。
- [x] 带 Cloudflare 特征的 403/429/503 不得借 API 允许状态被计为 API 可达。
- [x] `blocked/challenge` 使该目标检查失败并从对应导出和网关池排除；`behind_cf` 保持正常。
- [x] 补齐 checkmeta、checker、store/网关池语义测试。

### V046-PASSIVE-BREAKER

- [x] HTTP 正向代理只对 Cloudflare HTML 响应读取有界前缀，识别真实流量中的 challenge/blocked，并在安全可重试请求中立即切换下一个上游，避免预读 SSE 等流式响应。
- [x] Cloudflare 被动拦截立即打开该目标端点的运行期熔断；普通非 Cloudflare 401/403/429 不误判为代理故障。
- [x] 连接、代理认证、上游 CONNECT 和请求传输错误继续按连续失败计数；成功保留 EWMA 并关闭熔断。
- [x] 网关事件明确区分上游连接失败、Cloudflare/407 响应拒绝和早期隧道失败。

### V046-TUNNEL-FEEDBACK

- [x] CONNECT/SOCKS5 双向转发返回持续时间和双向字节统计。
- [x] 短时间内客户端已发送数据、上游零返回的隧道计为早期上游失败；已有上游返回数据的隧道计为成功。
- [x] 客户端未发送数据即关闭、Hijack 失败、向客户端写握手响应失败不计入上游熔断；中性 half-open 会释放单探针占位。
- [x] 异步隧道结束反馈保持 handler 非阻塞，并有双向真实字节回调测试。

### V046-VALIDATION-AND-RELEASE

- [x] 定向测试、全量 test/vet/race、preflight、版本一致性、Node、交叉编译和差异检查通过。
- [x] 更新并重启现有 8899，验证 health、登录/bootstrap、gateway status、HTTP/SOCKS5 切换与 Cloudflare 状态语义。
- [ ] 更新 README、CHANGELOG、版本、接手文档并发布 `v0.4.6`。
- [ ] 推送 main 和 annotated tag，确认 CI、Release、8 个资产、Docker 工作流和 GHCR amd64/arm64 成功。

## 3. 实时断点

更新时间：2026-07-11 18:27 Asia/Shanghai

- 当前工作包：`V046-BASELINE-AND-DESIGN`。
- 最近完成：只读审查现有真实流量反馈、轮询/重试/half-open/降级和 Cloudflare 检测；确认 v0.4.5 工作区干净。
- 已通过验证：`./scripts/check_version_consistency.sh`、`go test -count=1 ./...`、`GET http://127.0.0.1:8899/health`。
- 正在执行或准备执行的命令：先补 Cloudflare 严格判定、HTTP 被动熔断和隧道反馈失败路径测试，再实现对应逻辑。
- 当前阻塞：无。
- 唯一下一步：新增 v0.4.6 失败路径回归测试。

- 断点更新：主动检测的 Web/API 两条路径均已识别 Cloudflare，200 challenge 与带 CF 特征的 403/429/503 不再计为可达；保存层再次强制 `blocked/challenge -> failed`，防止其它调用路径绕过并进入导出/网关池。
- 被动熔断更新：新增 Cloudflare/407 立即打开运行期熔断、响应前缀有界检查、隧道双向字节与持续时间反馈；客户端 Hijack/握手响应写失败不再处罚上游。
- 已通过验证：`go test -count=1 ./internal/checkmeta ./cmd/proxylite`（保存层约束测试新增后待复跑）。
- 当前工作包：`V046-CLOUDFLARE-STRICT`。
- 正在执行或准备执行的命令：`gofmt` 后复跑 checkmeta/checker/store/gateway 定向测试，随后补 HTTP 重试与异步隧道回调集成覆盖。
- 当前阻塞：无。
- 唯一下一步：复跑包含保存层 Cloudflare 不变量的定向测试。

- 断点更新：Cloudflare 主动检测与保存层不变量、HTTP 被动拦截后同请求切换、立即熔断、流式响应不预读、隧道早期失败/成功/中性、half-open 中性释放和双向字节回调均已实现。
- 已通过验证：`go test -count=1 ./cmd/proxylite ./internal/checkmeta`、`git diff --check`。
- 当前工作包：`V046-VALIDATION-AND-RELEASE`。
- 正在执行或准备执行的命令：先完成版本/README/CHANGELOG 更新，然后执行 `./scripts/preflight_check.sh`、全量 test/vet/race、版本一致性、Node、交叉编译和差异检查。
- 当前阻塞：无。
- 唯一下一步：更新 v0.4.6 版本和发布文档。

- 断点更新：版本常量、嵌入式页面、`.env.example`、Docker Compose、README、CHANGELOG 和路线入口已更新为 v0.4.6；preflight 已完成构建和全量单测。
- 已通过验证：版本一致性、Node 语法、`./scripts/preflight_check.sh`、全量 `go test ./...`、差异检查。
- 当前工作包：`V046-VALIDATION-AND-RELEASE`。
- 正在执行或准备执行的命令：`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`、Windows amd64/Linux arm64 交叉编译和最终源码审计。
- 当前阻塞：无。
- 唯一下一步：执行 vet、race 和交叉编译门禁。

- 最终代码门禁：preflight、全量 test、vet、分包 race、版本一致性、Node、Windows amd64、Linux arm64 和差异检查全部通过。
- 当前工作包：`V046-LOCAL-8899-ACCEPTANCE`。
- 正在执行或准备执行的命令：重建 `bin/proxylite`，重启现有 `proxylitechecker-test.service`，只在 `127.0.0.1:8899` 验证 health、登录/bootstrap、gateway status 和目标端口 HTTP/SOCKS5 冒烟。
- 当前阻塞：无。
- 唯一下一步：备份真实运行数据并执行迁移。

- 真实库迁移与现有 8899 验收完成：schema `406001`，迁移记录 1 条，历史 Cloudflare 非法可用从 102 条归零，SQLite 完整性 `ok`；health、登录/bootstrap、gateway status 通过。
- Grok HTTP `18084` 真实 HTTPS 请求返回 200；SOCKS5 `18085` 首次遇到单上游连接超时，未误计为运行期失败，下一请求切换上游并返回 200；最终电路 551 active / 0 open / 0 half-open，未降级。
- 当前工作包：`V046-GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计提交范围，创建并推送 v0.4.6 发布提交和 annotated tag，随后监控 CI、Release、8 个资产、Docker 与 GHCR 双架构。
- 当前阻塞：无。
- 唯一下一步：创建并推送 v0.4.6 发布提交。

- 真实运行数据备份：`backups/proxylite-data-20260711-195220.tar.gz` 已生成。
- 当前工作包：`V046-MIGRATION-REVALIDATION`。
- 正在执行或准备执行的命令：迁移变更后的 preflight、全量 test/vet/race、版本一致性、交叉编译和差异检查；全部通过后再重建并重启现有 8899。
- 当前阻塞：无。
- 唯一下一步：复跑迁移后的完整自动化门禁。

- 迁移更新：新增 schema `406001`，事务内重分类历史 target/compatibility Cloudflare `blocked/challenge` 可用记录；重复启动幂等测试通过。
- 真实库迁移前审计：schema `402001`，16,057 proxies，638 条 blocked/challenge 目标记录，其中 102 条错误保持 available；完整性 `ok`。
- 当前工作包：`V046-LOCAL-8899-ACCEPTANCE`。
- 正在执行或准备执行的命令：`./scripts/backup_data.sh`，重建并重启现有 `proxylitechecker-test.service`，验证 schema 406001、非法可用归零、完整性、API 和 HTTP/SOCKS5。
- 当前阻塞：无。
- 唯一下一步：备份真实运行数据并执行迁移。
