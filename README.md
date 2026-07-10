# ProxyLiteChecker

ProxyLiteChecker 是一个单机代理检测、维护和网关服务。一个 Go 进程同时提供 Web UI、HTTP API、SQLite、代理源拉取、本机检测、TXT/JSON 导出以及按目标划分的 HTTP/SOCKS5 网关。

项目只反映部署机器自身的网络质量，不包含面板、节点、agent 或分布式调度。

- 作者：额丶Y
- 仓库：<https://github.com/RY-zzcn/ProxyLiteChecker>
- 镜像：`ghcr.io/ry-zzcn/proxylitechecker`

## 一键部署（推荐）

Linux 服务器使用 root 或 sudo 执行：

```bash
curl -fsSL https://raw.githubusercontent.com/RY-zzcn/ProxyLiteChecker/main/scripts/install.sh | sudo bash
```

脚本只需要选择一种部署方式：

```text
1. 二进制部署（GitHub Release + systemd）
2. Docker 部署（GHCR 镜像）
```

统一安装到：

```text
/opt/ProxyLiteChecker
```

部署完成后打开：

```text
http://服务器IP:8899
```

首次安装会自动生成管理员密码并显示一次，同时写入权限为 `600` 的：

```text
/opt/ProxyLiteChecker/.env
```

数据保存在：

```text
/opt/ProxyLiteChecker/data
```

### 脚本会做什么

二进制部署：

- 自动识别 Linux `amd64`、`arm64` 或 `armv7`。
- 下载 GitHub 最新正式 Release 的成品二进制和 `SHA256SUMS`。
- 校验 SHA256 后安装，不在服务器编译源码。
- 创建并启动系统级 `proxylitechecker.service`。

Docker 部署：

- 检查 Docker Engine、Docker Compose 和 daemon 是否可用。
- Docker 环境缺失时先询问是否使用 Docker 官方脚本安装。
- 如果拒绝安装、安装失败或安装后仍不可用，立即停止 ProxyLiteChecker 部署并说明原因。
- 使用 GitHub 最新正式 Release 对应的 GHCR 镜像标签，不在服务器构建镜像。

通用行为：

- 重复执行即可升级，已有 `.env` 和 `data/` 不会被覆盖。
- 切换部署方式时会停止当前项目的另一种运行方式，避免端口冲突。
- 下载目录、校验文件和 Docker 安装脚本无论成功或失败都会自动清理。
- 启动后必须通过本机 `8899/health` 检查才会报告部署成功。

### 非交互命令

直接选择二进制：

```bash
curl -fsSL https://raw.githubusercontent.com/RY-zzcn/ProxyLiteChecker/main/scripts/install.sh | sudo bash -s -- --mode binary
```

直接选择 Docker：

```bash
curl -fsSL https://raw.githubusercontent.com/RY-zzcn/ProxyLiteChecker/main/scripts/install.sh | sudo bash -s -- --mode docker
```

Docker 缺失时自动同意安装：

```bash
curl -fsSL https://raw.githubusercontent.com/RY-zzcn/ProxyLiteChecker/main/scripts/install.sh | sudo bash -s -- --mode docker --yes
```

部署指定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/RY-zzcn/ProxyLiteChecker/main/scripts/install.sh | sudo bash -s -- --mode binary --version v0.4.3
```

## 升级

重新执行一键部署命令即可。脚本会重新获取最新正式 Release，保留配置和数据库，并更新当前部署方式。

升级前建议备份：

```bash
sudo tar -czf "proxylite-backup-$(date +%Y%m%d-%H%M%S).tar.gz" -C /opt/ProxyLiteChecker data .env
```

## 日常运维

### 二进制部署

```bash
sudo systemctl status proxylitechecker
sudo systemctl restart proxylitechecker
sudo journalctl -u proxylitechecker -f
```

### Docker 部署

```bash
cd /opt/ProxyLiteChecker
sudo docker compose ps
sudo docker compose restart
sudo docker compose logs -f
```

## 核心能力

- 支持 HTTP、HTTPS、SOCKS4、SOCKS5、SOCKS5H 代理。
- 检测基础链路、出口 IP、延迟、国家地区、目标 Web/API 和 Cloudflare 状态。
- 内置公开代理源，也支持手动批量导入。
- 自动拉取、低库存补源、自动检测、失败清理和过期重检。
- 检测目标支持常规、OpenAI、Grok、Gemini、Claude。
- 一个代理的多目标检测共享协议、出口 IP 和 GeoIP 探测，并原子保存全部结果。
- 外部 IP 元数据通过有界后台队列和 SQLite 缓存异步补充。
- 任务历史、调度时间、失败退避和重启中断状态持久化。
- 提供按目标和国家筛选的 TXT/JSON 导出。
- 每个目标提供固定 HTTP/SOCKS5 本机网关，支持 EWMA、隔离、half-open 和降级选路。

## 默认端口

| 目标 | HTTP | SOCKS5 |
| --- | ---: | ---: |
| Web UI / API | `8899` | - |
| 常规 | `18080` | `18081` |
| OpenAI | `18082` | `18083` |
| Grok | `18084` | `18085` |
| Gemini | `18086` | `18087` |
| Claude | `18088` | `18089` |

默认网关监听 `0.0.0.0`。公网部署时请使用防火墙或安全组限制 `8899` 和 `18080-18089` 的访问来源，避免形成开放代理。

## 配置

一键部署配置文件：

```text
/opt/ProxyLiteChecker/.env
```

修改后重启对应服务。

常用变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8899` | Web UI/API 端口 |
| `DATABASE_URL` | `sqlite:///./data/proxylite.db` | SQLite 数据库位置 |
| `ADMIN_USERNAME` | `admin` | 管理员用户名 |
| `ADMIN_PASSWORD` | 自动生成 | 管理员密码 |
| `SECRET_KEY` | 自动生成 | 登录令牌签名密钥 |
| `PLC_REQUIRE_SECURE` | `1` | 禁止使用默认密码和密钥启动 |
| `PLC_EXPORT_TOKEN` | 空 | 可选的免登录导出令牌 |
| `PLC_GEOIP_ENABLED` | `1` | 启用 GeoIP 国家数据库 |
| `PLC_GATEWAY_ENABLED` | `1` | 启用 HTTP 网关 |
| `PLC_SOCKS5_GATEWAY_ENABLED` | `1` | 启用 SOCKS5 网关 |
| `PLC_GATEWAY_TARGET_PROFILES` | `all` | 开放的目标入口 |
| `PLC_GATEWAY_UPSTREAM_LIMIT` | `200` | 每个目标最多装载的上游数 |
| `PLC_GATEWAY_UPSTREAM_STRATEGY` | `round_robin` | `round_robin`、`lowest_latency` 或 `stability_first` |

完整变量和注释见 [`.env.example`](.env.example)。

## Web 控制台

控制台可以完成：

- 选择代理源并拉取。
- 导入自定义代理文本。
- 选择检测范围、目标、数量、并发、轮次和超时。
- 查看基础链路状态与每个目标的独立能力。
- 配置自动拉取、自动检测、低库存维护、TTL 和清理策略。
- 查看持久化任务历史、调度退避、网关池龄、电路状态和缓存新鲜度。
- 下载当前目标的 TXT/JSON 代理列表。

命名目标只有对应 Web 或 API 可达时才会进入导出和网关池。仅基础出口可用的代理保留 `base` 诊断能力，但不会被误计为 OpenAI、Grok、Gemini 或 Claude 可用。

## API

登录后常用接口：

| 接口 | 说明 |
| --- | --- |
| `GET /health` | 服务健康与版本 |
| `POST /api/auth/login` | 登录 |
| `GET /api/bootstrap` | 控制台初始化数据 |
| `GET /api/proxies` | 代理、基础状态和目标状态 |
| `GET /api/stats` | 聚合统计与缓存新鲜度 |
| `GET /api/jobs` | 持久化任务历史 |
| `GET /api/scheduler/status` | 自动调度状态 |
| `GET /api/gateway/status` | 网关、池和电路诊断 |
| `GET /api/export/proxies.txt` | TXT 导出 |
| `GET /api/export/proxies.json` | JSON 导出 |

导出示例：

```text
/api/export/proxies.txt?target_profile=grok
/api/export/proxies.json?target_profile=openai&countries=US,JP
```

## 手动 Docker 部署

如果不使用一键脚本：

```bash
docker run -d --name proxylitechecker \
  --restart unless-stopped \
  -p 8899:8899 \
  -p 18080-18089:18080-18089 \
  -e TZ=Asia/Shanghai \
  -e ADMIN_PASSWORD='请改成强密码' \
  -e SECRET_KEY='请改成强随机字符串' \
  -e PLC_REQUIRE_SECURE=1 \
  -v proxylite-data:/app/data \
  ghcr.io/ry-zzcn/proxylitechecker:v0.4.3
```

## 手动下载二进制

GitHub Release 提供：

| 文件 | 平台 |
| --- | --- |
| `proxylite-linux-amd64` | Linux x86_64 |
| `proxylite-linux-arm64` | Linux arm64 |
| `proxylite-linux-armv7` | Linux armv7 |
| `proxylite-windows-amd64.exe` | Windows 64 位 |
| `proxylite-darwin-amd64` | Intel macOS |
| `proxylite-darwin-arm64` | Apple Silicon macOS |

Linux/macOS 二进制已内置 Web UI，不需要额外下载静态目录。Windows 可以直接运行：

```powershell
$env:ADMIN_PASSWORD="请改成强密码"
$env:SECRET_KEY="请改成强随机字符串"
$env:PLC_REQUIRE_SECURE="1"
.\proxylite-windows-amd64.exe
```

## 本地源码开发

要求 Go 1.22 或更新版本：

```bash
git clone https://github.com/RY-zzcn/ProxyLiteChecker.git
cd ProxyLiteChecker
go test ./...
go build -o bin/proxylite ./cmd/proxylite
./scripts/start.sh
```

发布前检查：

```bash
./scripts/preflight_check.sh
```

项目运行数据位于 `data/`，不会提交到 Git。不要与 `/root/ProxyPoolChecker` 混用目录或数据。

## 更多文档

- [部署、备份和回滚](docs/deployment.md)
- [项目接手与实时进度](docs/PROJECT_HANDOFF.md)
- [v0.4.3 一键部署路线图](docs/ROADMAP_V0.4.3.md)
- [更新记录](CHANGELOG.md)
