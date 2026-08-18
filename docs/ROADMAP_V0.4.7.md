# ProxyLiteChecker v0.4.7 路线图

主题：代理源可维护性与运行环境 TLS 修复。

## 工作包

- [x] 新增 `proxy_sources` 持久化目录，首次迁移种子现有内置源。
- [x] 支持代理源新增、编辑、启用/禁用和删除；保留现有拉取、自动调度和健康记录兼容性。
- [x] Web 控制台提供源目录维护表单，并显示最近错误和源状态。
- [x] Docker 运行镜像安装 CA 根证书，修复代理源和 GeoIP HTTPS 下载。
- [x] 补充迁移、源 CRUD、动态拉取和部署配置测试。
- [x] 完成版本、CHANGELOG、部署文档和本机回环地址迁移/API 冒烟。
- [ ] 完成 GitHub Release、二进制资产与 GHCR 多架构镜像核验。

当前工作包：`v0.4.7_release`。
当前阻塞：无；Deploy key 已验证，发布提交和 tag 已推送。
已通过：preflight、全量 Go test/vet/race、Node 语法、版本一致性、差异检查、schema `407001` 迁移、健康检查、登录、33 个内置源读取、自定义源 CRUD 和 GeoIP 状态 API 冒烟。
正在执行或准备执行：等待 CI `32168526273`、Release `32168607662`、main Docker `32168526272` 和 tag Docker `32168607889` 终态，随后核验 Release 资产与 GHCR manifest。
唯一下一步：取得四条 v0.4.7 工作流的最终终态。
