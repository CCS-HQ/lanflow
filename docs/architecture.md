# 澜流搭建方案

## 当前状态

下文是最初的搭建方案，分层和目录均已按此实现：系统代理、TUN（权限组件）、订阅与覆写、内核下载与多版本管理均可用，功能见 [README](../README.md) 与 [design.md](design.md)。Mihomo 内核随安装包分发，也可在应用内下载其他版本；分阶段验收一节保留为当时的计划，以代码和 README 为准。

## 分层

```text
MyGo 原生 UI / 托盘
        ↓
应用服务：配置、节点、订阅、运行状态
        ↓
Mihomo 进程管理器 + 本机 API 客户端
        ↓
Mihomo 子进程：代理协议、路由规则、DNS

系统代理适配器 / 后续 TUN 权限助手独立于上述层次
```

目录：

| 目录 | 职责 |
| --- | --- |
| internal/app | 应用服务与状态，UI 不直接执行内核命令 |
| internal/core | Mihomo 生命周期、版本校验与退出恢复 |
| internal/controller | REST、WebSocket、鉴权、超时与取消 |
| internal/profile | 原始配置、订阅、配置合成与校验 |
| internal/platform | 分平台系统代理、开机启动、权限助手 |
| internal/autostart | 开机启动服务进程的登录项：macOS LaunchAgent、Windows 当前用户 Run 键、Linux 及其他 Unix 桌面的 XDG autostart（`xdg.go`：`$XDG_CONFIG_HOME/autostart/lanflow.service.desktop`，未设置时在 `~/.config` 下） |
| internal/view | MyGo 页面与组件 |
| resources | 图标与打包资源 |
| bin（忽略） | `scripts/fetch-core.sh` 下载并校验过的内核（`bin/<goos>-<goarch>/`，宿主平台另有 `bin/mihomo` 供测试用）、地理数据（`bin/geo/`）和压缩包缓存 |
| runtime（忽略） | 仅供开发的运行数据 |

按功能创建目录，避免提前堆积空接口。初期设置使用 JSON、代理配置使用 YAML；暂不引入数据库。

## 内核接入

- 固定版本 v1.19.33（`scripts/fetch-core.sh` 的 `PINNED_VERSION`、`internal/app/coredl.go` 的 `pinnedCoreVersion`），独立于 MyGo 版本升级。各平台官方压缩包的 SHA-256 取自 github.com/MetaCubeX/mihomo 发布页的 asset digest，同时写在 `fetch-core.sh` 和 `internal/app/coresums.go` 中，测试保证两处一致。
- 随包分发：`fetch-core.sh` 先确定期望的 SHA-256（固定版本用内置值，其他版本向 api.github.com 查询 asset digest，不经镜像），下载（可经 `GITHUB_MIRROR`）后核对，不符即退出；通过后写入 `bin/<goos>-<goarch>/`，`prepare-resources.sh` 再拷入 `resources/<goos>-<goarch>/`。地理数据（MetaCubeX/meta-rules-dat）只有滚动更新的 `latest`，没有可固定的 tag，按同一 release 的 asset digest 校验，也可用 `GEOIP_SHA256` 等环境变量指定。发布流程在校验作业里下载一次，各平台的构建作业用 `GEOIP_SHA256` 等变量核对同一份文件，SHA-256 记入 Release 说明。
- 应用内下载（设置 › 内核）：同样先取得期望的 SHA-256 再下载，压缩包核对通过后才解压、运行 `mihomo -v` 与 `-t` 并安装；GitHub 没有给出 digest 的版本一律拒绝。也可选择用户指定的本机内核文件。
- 启动前检查版本和配置，使用独立工作目录；管理自己启动的 PID，不结束其他代理客户端的进程。
- controller 仅监听 127.0.0.1，生成随机 secret。API 客户端绕开系统代理，设置请求超时，不把 secret 或订阅令牌写进日志。
- 应用覆盖项与订阅原始文件分开保存。订阅不得覆盖本地 controller、secret、监听地址和 TUN 开关。
- 「允许局域网」关闭时，订阅自己带的监听也收敛到回环（`internal/profile/lan.go`，合成时在各覆写层之前对订阅层执行）：`listeners` 每一项的 `listen` 改为 `127.0.0.1`，`tunnels` 的本地地址、`dns.listen`（澜流接管 DNS 时由澜流自己写，不在此列）改成回环地址，本地地址无法识别的隧道和 `dns.listen` 直接移除，每处改动记一条告警。覆写是用户自己写的内容，保持原样；澜流后加的监听（探测、HTTPS 解密、高级选项）也不受影响。
- 权限组件（`internal/platform/helper`、`cmd/lanflow-helper`）：
  - 安装必须带 `--core-sha256`。内核文件在用户可写的位置，应用在请求管理员授权前先算好它的 SHA-256，提权后的安装程序只接受摘要一致的副本（按写入的字节计算，授权期间文件被换掉即拒绝）；缺少或格式不对的摘要一律拒绝安装，不退回到「不校验」。`--core` 本身是符号链接也拒绝。
  - 启动内核时，数据目录内的工作目录与配置文件路径不允许经过符号链接（Windows 上为任何重解析点），即使它此刻指向数据目录内部；数据目录之上的链接（如链接的主目录）是用户自己的布局，仍允许。这只是收窄而非堵死：检查之后用户仍可把目录换成链接，内核自己也会打开配置里的路径。
  - macOS：helper 把内核的 PID 记在只有 root 可写的目录里的 `core.pid`，内核退出时删除；helper 启动时若发现上一个 helper（崩溃或被强杀）留下的内核仍在运行、且确实是受管的内核程序，就结束它（先中断、超时再强杀），PID 已被其他进程占用则不动。
  - Windows：内核进程放进 `KILL_ON_JOB_CLOSE` 的 Job Object，helper 持有唯一句柄，helper 崩溃、被结束或没走完停止流程时系统随之结束内核；建不了 Job 时内核照常运行，只在日志里记一笔。
- 真实后端创建失败时（`main.go` `backend`）不再退回模拟后端：记日志、弹原生错误对话框说明原因，然后以状态 1 退出。模拟后端只在 `LANFLOW_FAKE=1`（开发）时使用——否则界面会显示样例节点和流量、开关看似可用而实际没有代理，ctl 和 MCP 也会拿到同样的假数据。
- 先接 GET /version、GET /proxies 和 PUT /proxies/{name}；再接 /configs、/traffic、/logs、/connections。按官方 API 文档确认方法和请求字段。
- UI 线程不运行阻塞请求；后台请求通过明确的状态更新刷新 UI。退出时取消请求并回收自身子进程。页面自己的状态只在 UI 线程读写：后台任务经 `Env.RunThen`（或方法 `Env.Then`，`internal/view/env`）执行，任务本身只计算，结果由 `done` 回调在 UI 线程、下一次构建之前写回，视图读取时不需要锁；`Env.OnUI` 供任意 goroutine 把一段代码排到 UI 线程。设置页的保存在视图层串行（`internal/view/settings/page.go` `saveThen`）：每次保存都是整份设置的快照，并发保存可能以任意顺序落地、旧的覆盖新的，所以排队逐个执行，排队期间控件显示并基于最新的草稿继续修改。
- 本地配置默认权限 0600，运行目录 0700；正式应用使用系统用户数据目录，避免把订阅和凭据存进代码仓库。

## 分阶段验收

### 1. 框架与内核连通

完成窗口、内核路径设置、版本显示、启动/停止、状态与错误提示。验证启动失败、端口占用、重复启动、内核意外退出和正常退出后的进程清理。

### 2. 最小代理闭环

实现本地配置导入、校验、代理组列表和节点切换。验收：错误 YAML 不替换可用配置；切换后从内核读回结果；不配置系统代理也能显式指定代理端口测试流量。

### 3. 日常使用

实现订阅更新、延迟测试、流量、日志、托盘及系统代理开关。系统代理启用前记录原设置，退出时仅恢复仍由本应用控制的设置；异常退出后提供恢复入口，避免覆盖用户或其他软件的修改。

### 4. TUN 与发行

单独设计最小权限助手、权限申请和撤销、DNS/路由恢复。验证睡眠唤醒、网络切换及崩溃恢复后，再做多平台安装包、签名、公证和更新回滚。

## 资源占用

- 窗口不在前台时不做装饰性动画：地球的空闲摆动与「你」的脉冲只在窗口激活且有流量经过时播放；请求动画同样只在窗口激活时播放（60 fps），没有流量的路由不画；窗口非激活时只把出口标记的移动和视角调整以 30 fps 走完，随后停止重绘（`internal/view/overview/exitmap.go` `drawMap`、`drawFlows`）。经纬网与陆地点阵画进两张 `ui.Canvas`，只在视角变化时重绘（`internal/view/overview/globe_ground.go`）。
- 数据变化触发的重绘有节流：前台每秒至多 4 帧、非激活窗口每秒 1 帧（`view/shell.go` `redrawData`）；窗口隐藏或最小化时服务层进入后台模式（`Service.SetBackground`）：连接流 3 s 一次、代理与规则 60 s 刷新一次，统计按增量累计不受影响。
- 进程内存：`main_pprof.go` 设 96 MiB 软上限与 GOGC 50；`LANFLOW_PPROF=127.0.0.1:6060` 开启 net/http/pprof（默认关闭）。
- 流量统计落盘为 gzip JSON：当月按天、往月合并为 `YYYY-MM.json.gz`，旧版 `.json` 自动迁移（`internal/app/stats_store.go`）。缓存大小与「清理缓存」在设置 › 数据。（当时的度量与前后对比报告是本地生成的 `build/perf-report.md`，该目录不入库。）

## 发布准备

MyGo 为 MIT，Mihomo 仓库声明 GPL-3.0。随包分发内核时需准备相应许可证、版权与源码信息；使用独立进程不应被视为自动免除分发义务。

现状：本项目以 MIT 发布（[LICENSE](../LICENSE)）。`scripts/prepare-resources.sh` 调用 `scripts/collect-licenses.sh`，把 Mihomo 的 GPL-3.0 全文、指向固定版本源代码的说明、地理数据库的来源与署名、编入程序的 Go 模块的许可证放进每个安装包的 `licenses/`；清单见 [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)，发布流程把它附到 Release 并核对包内文件。正式应用标识（`mygo.json` 的 `identifier` 仍是 `com.example.lanflow`）和 Developer ID 签名、公证尚未确定。

## 官方资料

- [MyGo 入门](https://github.com/egoist/mygo/blob/main/docs/getting-started.md)
- [MyGo 分发](https://github.com/egoist/mygo/blob/main/docs/distribution.md)
- [Mihomo Releases](https://github.com/MetaCubeX/mihomo/releases)
- [Mihomo API](https://wiki.metacubex.one/api/)
- [Mihomo LICENSE](https://github.com/MetaCubeX/mihomo/blob/Meta/LICENSE)
