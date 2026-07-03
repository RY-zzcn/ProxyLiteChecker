# ProxyLiteChecker

ProxyLiteChecker 是 ProxyPoolChecker 的单机轻量版本。它没有面板和节点区分，也不需要单独部署 agent。哪个 VPS 或 Windows 机器需要给本机项目使用代理，就把这个服务部署在哪台机器上，由这台机器本机拉取、检测、维护和提供代理。

作者：额丶Y

仓库地址：https://github.com/RY-zzcn/ProxyLiteChecker

## 核心能力

- 单体 Go 服务：Web UI、API、SQLite、代理源、检测任务、导出和本机 HTTP 网关在一个进程内运行。
- 本机视角检测：支持 HTTP、HTTPS、SOCKS4、SOCKS5、SOCKS5H 代理，检测出口 IP、延迟、目标服务可达性、API 域名可达性和 Cloudflare 状态。
- 内置代理源：可一键拉取公开代理源，也可以手动导入代理文本。
- 自动任务：可在 Web UI 中保存自动拉取、低待检补源、自动检测、失败清理、过期重检、检测范围、目标、并发、超时和分页设置。
- 有效代理导出：提供按检测目标分类的 TXT / JSON 导出接口，方便脚本或其它本机服务消费。
- 目标化代理网关：每个检测目标都有固定 HTTP/SOCKS5 入口，可同时供多个不同目标需求的本机项目使用。

## 快速开始

### 方式一：Docker

```bash
docker run -d --name proxylitechecker \
  -p 8899:8899 \
  -p 18080-18089:18080-18089 \
  -e ADMIN_PASSWORD='请改成强密码' \
  -e SECRET_KEY='请改成强随机字符串' \
  -v proxylite-data:/app/data \
  ghcr.io/ry-zzcn/proxylitechecker:latest
```

打开：

```text
http://服务器IP:8899
```

### 方式二：源码运行

```bash
cd /root/ProxyLiteChecker
go mod tidy
go build -o bin/proxylite ./cmd/proxylite
./scripts/start.sh
```

打开：

```text
http://服务器IP:8899
```

默认登录：

```text
admin / admin123
```

公网暴露前必须修改：

```env
ADMIN_PASSWORD=请改成强密码
SECRET_KEY=请改成强随机字符串
```

## Web 控制台

`v0.1.6` 起，代理仓库使用固定高度分页表格，拉取大量代理后页面不会继续被表格撑长。分页大小可在“自动任务与设置”中调整。

自动任务和检测参数保存在 SQLite 中，重启后仍会保留：

| 设置 | 说明 |
| --- | --- |
| 自动拉取 | 按间隔拉取当前选中的代理源，空选择表示全部内置源 |
| 待检不足自动拉取 | 待检代理低于阈值时自动补源，并按冷却时间避免重复触发 |
| 自动检测 | 按间隔检测设置范围内的代理 |
| 失败即删 | 检测失败的代理立即删除；开启后已有失败记录也会被自动维护清理 |
| 可用过期转待检 | 可用代理超过自定义有效期后转回待检，下一次检测时优先于刚拉取的新待检代理 |
| 待检过期删除 | 待检代理超过自定义有效期后自动删除，避免长期未检测的旧源堆积 |
| 检测范围 | 待检、失败、可用、已检或全部 |
| 检测目标 | 可同时勾选常规、OpenAI、Grok、Gemini、Claude；同一代理会按目标保存独立检测结果 |
| 批量 / 并发 / 轮次 / 超时 | 控制单次检测任务规模和速度 |

拉取代理源和手动导入都会按规范化后的 `proxy_key` 去重，重复代理只更新来源和认证信息，不会新增重复记录。

快速操作区的“本机检测”旁边也提供检测范围、多个检测目标、检测数量和导出目标。这些控件会和完整设置面板同步，适合临时改目标或改单次检测规模后马上执行。

网关状态区位于代理仓库上方，按目标展示固定 HTTP/SOCKS5 入口和最近轮询到的上游代理。TXT / JSON 导出链接会跟随“导出目标”生成 `target_profile` 参数；导出目标只影响导出链接，不影响自动检测调度。

拉取和检测属于重任务，同一时间只会运行一个。自动任务遇到手动任务时会延后，不会并发抢占数据库和网络资源。

## Docker 镜像

GitHub Packages 镜像地址：

```text
ghcr.io/ry-zzcn/proxylitechecker
```

常用标签：

| 标签 | 说明 |
| --- | --- |
| `latest` | `main` 分支最新镜像 |
| `v0.1.13` / 其它 `v*` | 对应版本镜像 |
| `0.1` | `0.1.x` 小版本线最新镜像，会随 `v0.1.13`、后续 `v0.1.14` 等自动前移 |

查看仓库 Packages 页面：

```text
https://github.com/RY-zzcn/ProxyLiteChecker/pkgs/container/proxylitechecker
```

如果仓库或 Package 是私有的，拉取镜像需要先登录 GHCR：

```bash
echo '你的 GitHub PAT' | docker login ghcr.io -u 你的GitHub用户名 --password-stdin
```

## Release 二进制文件说明

Release 中的二进制从 `v0.1.5` 开始内置 Web UI，下载后可以直接运行，不需要额外放置 `app/web` 目录。

| 文件名 | 适用系统 | 说明 |
| --- | --- | --- |
| `proxylite-linux-amd64` | Linux x86_64 / amd64 | 常见 VPS、服务器、Intel/AMD Linux |
| `proxylite-linux-arm64` | Linux arm64 / aarch64 | ARM VPS、树莓派 4/5 64 位、部分 NAS |
| `proxylite-linux-armv7` | Linux armv7 | 32 位 ARM 设备 |
| `proxylite-windows-amd64.exe` | Windows 64 位 | Windows 10/11、Windows Server 64 位 |
| `proxylite-darwin-amd64` | macOS Intel | Intel Mac |
| `proxylite-darwin-arm64` | macOS Apple Silicon | M1/M2/M3/M4 Mac |
| `proxylitechecker-vX.Y.Z.tar.gz` | 源码/静态资源包 | 包含 README、脚本、Docker Compose 和静态资源 |
| `SHA256SUMS` | 校验文件 | 用于核对下载文件完整性 |

### Linux 运行

```bash
chmod +x proxylite-linux-amd64
ADMIN_PASSWORD='请改成强密码' SECRET_KEY='请改成强随机字符串' ./proxylite-linux-amd64
```

打开：

```text
http://服务器IP:8899
```

### Windows 运行

Windows 64 位下载 `proxylite-windows-amd64.exe`。建议在 PowerShell 中运行，这样可以设置密码和查看日志：

```powershell
$env:ADMIN_PASSWORD="请改成强密码"
$env:SECRET_KEY="请改成强随机字符串"
.\proxylite-windows-amd64.exe
```

打开：

```text
http://127.0.0.1:8899
```

如果浏览器或系统提示下载的 exe 被阻止，可以在 PowerShell 执行：

```powershell
Unblock-File .\proxylite-windows-amd64.exe
```

如果需要其它设备或 Docker 容器访问 Windows 上的服务，请在 Windows 防火墙中放行：

| 端口 | 用途 |
| --- | --- |
| `8899` | Web UI 和 API |
| `18080` / `18081` | 常规目标 HTTP / SOCKS5 代理网关 |
| `18082` / `18083` | OpenAI 目标 HTTP / SOCKS5 代理网关 |
| `18084` / `18085` | Grok 目标 HTTP / SOCKS5 代理网关 |
| `18086` / `18087` | Gemini 目标 HTTP / SOCKS5 代理网关 |
| `18088` / `18089` | Claude 目标 HTTP / SOCKS5 代理网关 |

## 默认端口

| 端口 | 用途 |
| --- | --- |
| `8899` | Web UI 和 API |
| `18080` / `18081` | 常规目标 HTTP / SOCKS5 代理网关 |
| `18082` / `18083` | OpenAI 目标 HTTP / SOCKS5 代理网关 |
| `18084` / `18085` | Grok 目标 HTTP / SOCKS5 代理网关 |
| `18086` / `18087` | Gemini 目标 HTTP / SOCKS5 代理网关 |
| `18088` / `18089` | Claude 目标 HTTP / SOCKS5 代理网关 |

## 环境变量

复制 `.env.example` 为 `.env` 后按需修改。源码启动脚本会读取 `.env`。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8899` | Web UI/API 端口 |
| `DATABASE_URL` | `sqlite:///./data/proxylite.db` | SQLite 数据库 |
| `ADMIN_USERNAME` | `admin` | 管理员用户名 |
| `ADMIN_PASSWORD` | `admin123` | 管理员密码 |
| `SECRET_KEY` | `change-this-secret` | 登录令牌签名密钥 |
| `PLC_EXPORT_TOKEN` | 空 | 导出接口令牌，空时仅登录用户可访问 |
| `PLC_GATEWAY_ENABLED` | `1` | 是否启动本机 HTTP 网关 |
| `PLC_GATEWAY_HOST` | `0.0.0.0` | 网关绑定地址 |
| `PLC_GATEWAY_PORT` | `18080` | 首个 HTTP 目标入口端口 |
| `PLC_GATEWAY_TARGET_PROFILES` | `all` | 开放哪些目标入口，逗号分隔；`all` 表示内置全部目标 |
| `PLC_GATEWAY_PROFILE_PORT_STRIDE` | `2` | 未显式覆盖端口时，每个目标端口的步进 |
| `PLC_GATEWAY_HTTP_PROFILE_PORTS` | 空 | 覆盖 HTTP 目标端口，如 `openai:18082,claude:18088` |
| `PLC_SOCKS5_GATEWAY_ENABLED` | `1` | 是否启动 SOCKS5 网关 |
| `PLC_SOCKS5_GATEWAY_HOST` | `0.0.0.0` | SOCKS5 网关绑定地址 |
| `PLC_SOCKS5_GATEWAY_PORT` | `18081` | 首个 SOCKS5 目标入口端口 |
| `PLC_GATEWAY_SOCKS5_PROFILE_PORTS` | 空 | 覆盖 SOCKS5 目标端口，如 `openai:18083,claude:18089` |

默认网关监听 `0.0.0.0` 是为了方便本机 Docker 容器和同网络服务访问。公网部署时建议用防火墙或安全组限制 `18080-18089` 的访问来源，避免把代理网关暴露成开放代理。

## 与 ProxyPoolChecker 的区别

ProxyPoolChecker 适合集中管理多台检测节点；ProxyLiteChecker 适合单机自用。这里没有节点列表、节点排序、agent 安装命令、心跳和分布式检测调度。所有任务都在当前进程内完成，结果只代表当前机器到代理和目标服务的链路质量。
