# ProxyLiteChecker v0.4.3 一键部署路线图

- 状态：实现与本机 8899 验收完成，GitHub 发布中
- 当前工作包：`GITHUB-RELEASE`
- 基线版本：`v0.4.2`
- 目标版本：`v0.4.3`
- 主题：最小化的一键部署与完整 README 更新

## 1. 范围

本阶段只改善 Linux 部署体验，不改变代理检测、SQLite schema、任务调度、网关或 Web 功能。

## 2. 一键部署脚本

- [x] 新增单文件 `scripts/install.sh`，默认交互选择二进制或 Docker 部署。
- [x] 只支持 Linux root/sudo 执行，并对缺少交互终端、架构不支持和关键命令失败给出明确错误。
- [x] 默认安装到 `/opt/ProxyLiteChecker`，重复执行时保留 `.env` 与 `data/`。
- [x] 二进制模式从 GitHub 最新 Release 下载对应 Linux 成品和 `SHA256SUMS`，校验后原子安装。
- [x] 二进制模式创建 systemd 系统服务并完成 enable/restart/health 验证。
- [x] Docker 模式使用 GitHub 最新 Release 对应的 GHCR 镜像，不在目标机编译源码。
- [x] Docker/Compose 缺失时询问是否安装；拒绝安装或安装失败时立即停止项目部署并说明原因。
- [x] Docker 模式只写必要的 Compose 配置，验证 daemon、镜像拉取、启动和 health。
- [x] 首次部署生成安全的管理员密码和签名密钥；已有 `.env` 不覆盖。
- [x] 临时目录、下载二进制、校验文件和 Docker 安装包在成功或失败退出时都自动清理。
- [x] 两种模式切换时停止当前项目的另一种运行方式，避免 8899/网关端口冲突。

## 3. 文档和发布

- [x] 重写 `README.md` 全文，以一键部署为首选入口，并保留功能、端口、环境变量、升级、备份和开发说明。
- [x] 更新 `docs/deployment.md`、`.env.example`、`CHANGELOG.md`、版本常量、Web 版本和 Docker Compose 标签。
- [x] 更新 preflight，使其检查一键部署脚本语法与帮助入口。
- [x] 完成 `bash -n`、帮助/参数错误测试、全量 Go test/vet/race、preflight、交叉编译和差异检查。
- [x] 仅更新现有 `127.0.0.1:8899` 部署并完成 v0.4.3 health、登录/bootstrap 和 Web 冒烟。
- [ ] 创建发布提交、推送 main、annotated `v0.4.3` tag、GitHub Release、8 个资产和 GHCR amd64/arm64。

## 4. 实时断点

更新时间：2026-07-10 Asia/Shanghai

- 当前工作包：`V043-INSTALLER-DESIGN`
- 最近完成：读取 v0.4.2 发布状态、现有部署文档、Release 资产命名、Docker workflow、systemd 与本机更新脚本。
- 正在执行或准备执行的命令：运行基线版本一致性与全量测试；随后实现 `scripts/install.sh` 和静态测试入口。
- 当前阻塞：无。
- 唯一下一步：完成基线验证后实现一键部署脚本。

- 断点更新：基线版本一致性、全量 Go test 和差异检查通过。一键安装器、README 全文重写、v0.4.3 版本与发布文档已实现。
- 安装器验证：`bash -n`、帮助与错误参数；mock GitHub/systemd/Docker 的二进制与 Docker 成功路径；配置/数据保留与模式切换；Docker 安装失败和用户拒绝安装的停止路径；真实 latest Release 和 SHA256SUMS 格式均通过。
- 当前工作包：`V043-VALIDATION`。
- 正在执行或准备执行的命令：`./scripts/preflight_check.sh`、`go vet ./...`、`TMPDIR=/root/.cache go test -race -count=1 ./...`、Windows amd64/Linux arm64 交叉编译和 `git diff --check`。
- 当前阻塞：无。
- 唯一下一步：完成 v0.4.3 最终自动化门禁。

- 断点更新：`./scripts/preflight_check.sh`、`go vet ./...`、全包 race、Node 语法、Windows amd64/Linux arm64 交叉编译和 `git diff --check` 全部通过。安装器真实 Release 格式及 mock 成功/失败/拒绝路径均通过。
- 当前工作包：`LOCAL-8899-RELEASE`。
- 正在执行或准备执行的命令：构建正式 `bin/proxylite`，只重启现有 8899 进程并验证 v0.4.3 health、登录/bootstrap、Web 版本和安装文档静态内容。
- 当前阻塞：无。
- 唯一下一步：完成现有 `127.0.0.1:8899` 的 v0.4.3 验收。

- 断点更新：正式二进制已构建；停止旧 PID `984050` 后宿主现有机制只在原 8899 端口拉起 PID `1014746`。`/health`、默认管理员登录、bootstrap、stats、Web `v0.4.3`、5 个网关卡片和 18080–18089 监听均通过。headless Chromium 页面可见、无横向溢出、GET 合并和轮询生命周期冒烟通过，未启动第二套服务。
- 当前工作包：`GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计 README、安装器权限/语法、提交范围和版本一致性；创建发布提交并推送 main、annotated `v0.4.3` tag，监控 CI/Release/Docker。
- 当前阻塞：无。
- 唯一下一步：创建并推送 v0.4.3 发布提交。

- 断点更新：最终 preflight 与差异检查通过；`scripts/install.sh` 权限为 `755`，README、部署文档和 Release 页命令一致。提交范围只包含 v0.4.3 代码、脚本、测试入口和文档，不包含数据库、二进制、安装包、mock 目录或截图。
- 正在执行或准备执行的命令：`git add`、创建 v0.4.3 发布提交并推送 main。
- 当前阻塞：无。
- 唯一下一步：发布提交推送后创建并推送 annotated `v0.4.3` tag。
