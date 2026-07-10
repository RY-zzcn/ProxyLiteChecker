# ProxyLiteChecker v0.4.4 前端 UI 全面优化路线图

- 状态：已完成并发布
- 当前工作包：`COMPLETE`
- 基线版本：`v0.4.3`
- 目标版本：`v0.4.4`
- 主题：无构建步骤的专业运维控制台视觉、响应式与可访问性升级

## 1. 设计原则

- [x] 保持原生 HTML/CSS/JavaScript 和单二进制嵌入，不增加前端构建链或外部运行依赖。
- [x] 使用语义化颜色、间距、圆角、阴影、动效和层级 tokens，统一全部组件视觉语言。
- [x] 保持高信息密度，但通过分区导航、层级标题和渐进披露降低认知负担。
- [x] 正文与控件达到 WCAG AA 对比度，键盘 focus 清晰，动效尊重 `prefers-reduced-motion`。

## 2. 工作包

### V044-UI-FOUNDATION

- [x] 重构全局页面背景、字体层级、容器宽度、表面、边框、阴影和状态色。
- [x] 增加浅色/深色主题切换并持久化，首次遵循系统主题。
- [x] 优化登录页、粘性顶栏、品牌状态、按钮、输入框、标签和 Toast。

### V044-INFORMATION-ARCHITECTURE

- [x] 增加页内区域导航，明确“概览、操作、自动化、网关、仓库”结构。
- [x] 优化统计卡、快速操作、实时任务、代理源、导入和设置的视觉优先级。
- [x] 自动化设置改为可折叠的渐进披露组件，并保持现有字段 ID 与行为兼容。

### V044-DATA-RESPONSIVE

- [x] 优化网关卡片、诊断事件、代理表格、分页和空状态。
- [x] 手机端代理表格改为可读卡片，不要求横向滚动主数据。
- [x] 验证 375/390、768、1024、1440 宽度，无页面横向溢出。

### V044-ACCESSIBILITY

- [x] 增加跳转主内容链接、ARIA 标签、进度语义、错误通知和主题按钮状态。
- [x] 可点击区域、按钮与输入在触屏场景达到约 44px，禁用/加载/按下状态明确。
- [x] 键盘导航、focus 顺序、响应式字体/控件缩放和 reduced-motion 通过浏览器冒烟。

## 3. 验证与发布

- [x] Node 语法、HTML ID/绑定兼容、全量 Go test/vet/race、preflight、交叉编译和差异检查通过。
- [x] 只更新现有 `127.0.0.1:8899`，完成登录、bootstrap、全页面桌面/手机截图和关键交互冒烟。
- [x] 创建发布提交、推送 main、annotated `v0.4.4` tag、GitHub Release、8 个资产和 GHCR amd64/arm64。

## 4. 实时断点

更新时间：2026-07-10 Asia/Shanghai

- 当前工作包：`V044-UI-FOUNDATION`。
- 最近完成：读取 `ui-ux-pro-max` 与 `ui-styling` 指引；生成“专业、密集、低动效、蓝青状态色”的设计系统；完成 1440×1200 和 390×844 当前 UI 基线截图。
- 基线发现：桌面信息层级偏平，自动设置过密；手机页面高约 8121px，全部设置同时展开；原生 checkbox 尺寸 15px，页内无区域导航；主题仅浅色。
- 正在执行或准备执行的命令：重构 index 语义结构、主题/导航交互和 CSS 设计系统，保持所有现有业务 ID。
- 当前阻塞：无。
- 唯一下一步：完成 UI foundation 与信息架构实现。

- 断点更新：UI foundation 与信息架构已实现。新增浅/深色主题持久化、语义 tokens、区域导航、统计图标与说明、操作图标、设置 `<details>` 渐进披露、移动端代理卡片、跳转主内容、进度 ARIA、工具栏标签和数字格式化；保持全部业务 ID/API 不变。
- 已通过验证：`node --check`、`go test -count=1 ./app/web ./cmd/proxylite`、86 个 JS 绑定 ID 存在性和 `git diff --check`。
- 当前工作包：`V044-VISUAL-TEST`。
- 正在执行或准备执行的命令：构建并只更新现有 8899；执行 1440/1024/768/390 截图、浅/深色、设置折叠、区域导航和移动端表格冒烟。
- 当前阻塞：无。
- 唯一下一步：完成首轮浏览器视觉验收并修正布局问题。

- 断点更新：现有 8899 已更新为 v0.4.4。浏览器完成 1440、1024、768、390、375 宽度及浅色/深色验收：全部页面横向溢出为 0；390/375 使用代理卡片且设置初始折叠；768 及以下可见控件最小 44px；未标注表单控件 0。
- 交互验证：主题切换写入 localStorage 且刷新后保持 dark；设置 details 从 0→1 展开；区域导航 active 状态、themeToggle 键盘 focus、登录页、ARIA 进度、reduced-motion 0.01ms 均通过。浅/深色正文、muted、主色、成功/警告/危险语义色对比度均达到 AA，最低为浅色 muted 4.55:1。
- 视觉修正：手机代理区限制为 760px 内部纵向滚动，整页高度从首轮约 13,257px 降至约 6,914px；深色 checkbox、空上游和禁用复制状态已统一。
- 当前工作包：`V044-FINAL-VALIDATION`。
- 正在执行或准备执行的命令：全量 test/vet/race、preflight、Node、版本一致性、交叉编译、HTML/ID 兼容和差异检查。
- 当前阻塞：无。
- 唯一下一步：完成最终自动化门禁并重建嵌入资源二进制。

- 断点更新：preflight、全量 test/vet/race、Node、94 个唯一 HTML ID、86 个 JS 绑定 ID、Windows amd64/Linux arm64 交叉编译和差异检查全部通过。新增 `app/web/embed_test.go`，验证单二进制内嵌 v0.4.4 主题、导航、details、移动数据标签和 CSS。
- 当前工作包：`LOCAL-8899-RELEASE`。
- 正在执行或准备执行的命令：停止现有 8899 PID，让宿主在同一端口拉起最终 `bin/proxylite`；复验 health、登录/bootstrap、主题与 Web 页面。
- 当前阻塞：无。
- 唯一下一步：完成最终嵌入资源二进制的 8899 验收。

- 断点更新：停止 PID `1056518` 后宿主只在原 8899 端口拉起最终 PID `1106526`。health v0.4.4、登录/bootstrap、Stats、Gateway、Web `themeToggle`、`workspace-nav` 和 CSS `v0.4.4 interface system` 标记均通过；未启动第二套服务。
- 当前工作包：`GITHUB-RELEASE`。
- 正在执行或准备执行的命令：最终审计差异、截图、提交范围与文档；创建发布提交、推送 main、annotated `v0.4.4` tag，监控 CI/Release/Docker。
- 当前阻塞：无。
- 唯一下一步：创建并推送 v0.4.4 发布提交。

- 断点更新：最终 preflight、版本一致性和差异检查再次通过。提交审计确认只包含 v0.4.4 UI、嵌入资源测试、版本、README、路线图和接手记录，不包含运行数据、二进制、浏览器 profile 或截图。
- 正在执行或准备执行的命令：`git add`、创建 v0.4.4 发布提交并推送 main。
- 当前阻塞：无。
- 唯一下一步：推送发布提交后创建并推送 annotated `v0.4.4` tag。

- 断点更新：v0.4.4 发布提交 `54464d7` 已创建并推送到 GitHub main。
- 正在执行或准备执行的命令：创建并推送 annotated `v0.4.4` tag，随后监控 CI、Release 和 Docker 工作流。
- 当前阻塞：无。
- 唯一下一步：推送 `v0.4.4` tag 触发正式发布。

- 断点更新：annotated `v0.4.4` tag 已指向 `54464d754e8c900dc20b430e37c697a408856014` 并推送成功。
- 正在执行或准备执行的命令：轮询 GitHub Actions；核验 Release 8 个资产、CI 内嵌 UI 测试和 GHCR amd64/arm64 manifest。
- 当前阻塞：无，等待远端工作流完成。
- 唯一下一步：取得 CI、Release 与 Docker 全部成功终态。

- 断点更新：CI `29102285426`、Release `29102551657`、main Docker `29102285393`、tag Docker `29102552004` 全部成功。v0.4.4 Release 8 个资产全部 uploaded；GHCR `v0.4.4` 摘要 `sha256:a83d167deb9b0af7c68dc0acdb55026462dfe179c25af78480d0ded3070a4657`，包含 `linux/amd64` 与 `linux/arm64`。
- 发布结果：<https://github.com/RY-zzcn/ProxyLiteChecker/releases/tag/v0.4.4>；发布提交 `54464d7`；annotated tag `v0.4.4`。
- 当前工作包：`COMPLETE`。
- 正在执行或准备执行的命令：提交并推送发布后完成记录，确认 post-release CI/Docker 和工作区干净。
- 当前阻塞：无。
- 唯一下一步：完成发布后文档提交；之后等待用户制定下一路线。

- 最终断点：发布后完成记录提交 `f3d364c` 已推送；对应 CI `29104191202` 和 main Docker `29104191167` 均成功。最终断点使用 `[skip ci]` 文档提交，避免仅记录工作流结果再次触发 Docker 构建。
- 当前工作包：`COMPLETE`。
- 已通过验证：设计系统、浅/深色、375/390/768/1024/1440、设置折叠、移动代理卡片、主题持久化、键盘焦点、ARIA、reduced-motion、AA 对比度、嵌入资源、全量自动化、现有 8899、Release 与 GHCR 全部成功。
- 当前阻塞：无。
- 唯一下一步：等待用户制定 v0.4.4 之后的新路线。
