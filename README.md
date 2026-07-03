# ProxyLiteChecker

ProxyLiteChecker 是 ProxyPoolChecker 的单机轻量版本。它没有面板和节点区分，也不需要单独部署 agent。哪个 VPS 或 Windows 机器需要给本机项目使用代理，就把这个服务部署在哪台机器上，由这台机器本机拉取、检测、维护和提供代理。

作者：额丶Y

仓库地址：https://github.com/RY-zzcn/ProxyLiteChecker

## 核心能力

- 单体 Go 服务：Web UI、API、SQLite、代理源、检测任务、导出和本机 HTTP 网关在一个进程内运行。
- 本机视角检测：支持 HTTP、HTTPS、SOCKS4、SOCKS5、SOCKS5H 代理，检测出口 IP、延迟、目标服务可达性、API 域名可达性和 Cloudflare 状态。
- 内置代理源：可一键拉取公开代理源，也可以手动导入代理文本。
- 有效代理导出：提供 TXT / JSON 导出接口，方便脚本或其它本机服务消费。
- HTTP 代理网关：默认绑定 `0.0.0.0:18080`，本机项目、Docker 容器或同网络机器可以直接把它当作 HTTP 代理入口使用。

## 快速开始

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

## 默认端口

| 端口 | 用途 |
| --- | --- |
| `8899` | Web UI 和 API |
| `18080` | HTTP 代理网关 |

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
| `PLC_GATEWAY_PORT` | `18080` | 网关端口 |

默认网关监听 `0.0.0.0` 是为了方便本机 Docker 容器和同网络服务访问。公网部署时建议用防火墙或安全组限制 `18080` 的访问来源，避免把代理网关暴露成开放代理。

## 与 ProxyPoolChecker 的区别

ProxyPoolChecker 适合集中管理多台检测节点；ProxyLiteChecker 适合单机自用。这里没有节点列表、节点排序、agent 安装命令、心跳和分布式检测调度。所有任务都在当前进程内完成，结果只代表当前机器到代理和目标服务的链路质量。
