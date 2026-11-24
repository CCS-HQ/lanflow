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
  - macOS 升级替换（helper 0.1.5 起）：`launchctl bootout` 可能在旧 helper 还在停止内核时就返回，安装程序先从 `launchctl print` 取得旧进程 pid，等它真正退出（25 秒，超时且仍是已安装的 helper 才 SIGKILL）后再加载新的 LaunchDaemon；否则新 helper 会去回收旧内核，旧 helper 收尾时又把新 helper 刚建的套接字删掉，新组件在运行却连不上，安装超时回滚。helper 退出时只删除仍是自己创建的那个套接字（关闭监听时不再由 Go 自动删除路径）。安装等待新 helper 的套接字可连接最多 30 秒（launchd 对启动后很快退出的守护进程有 10 秒的重启节流）；失败时错误信息带上 `helper.log` 中本次新写入的最后几行和 launchd 记录的状态与退出码。
  - 安装必须带 `--core-sha256`。内核文件在用户可写的位置，应用在请求管理员授权前先算好它的 SHA-256，提权后的安装程序只接受摘要一致的副本（按写入的字节计算，授权期间文件被换掉即拒绝）；缺少或格式不对的摘要一律拒绝安装，不退回到「不校验」。`--core` 本身是符号链接也拒绝。
  - 启动内核时，数据目录内的工作目录与配置文件路径不允许经过符号链接（Windows 上为任何重解析点），即使它此刻指向数据目录内部；数据目录之上的链接（如链接的主目录）是用户自己的布局，仍允许。这一步只是尽早给出看得懂的拒绝，真正的保护是下一条。
  - 特权内核不在用户的文件上运行（helper 0.1.4 起，`service/stage.go`、`service/sanitize.go`）。helper 自己有一个只有 root/SYSTEM 可写的工作目录：macOS 是 `/Library/Application Support/Lanflow Helper/work`（0700），Windows 是 `%ProgramData%\Lanflow Helper\work`（DACL 只给 SYSTEM 和 Administrators）。每次启动：
    - 用户的 `runtime.yaml` 以不跟随链接的方式读取（macOS 逐级 `O_NOFOLLOW`，且必须是属于安装用户、只有一个硬链接的普通文件；Windows 不跟随重解析点并核对句柄的最终路径），检查后写进该目录，内核以 `-d <该目录> -f <该目录>/runtime.yaml` 启动。
    - 检查内容：去掉 `external-controller*`、`external-ui*`、`external-doh-server`、`secret`（控制接口只由命令行给出；澜流自己合成的配置本来就不带这些键，原样通过，一个字节不改）；`ntp.write-to-system` 也去掉（它让内核按配置里的 NTP 服务器改系统时钟）。这些键若是经顶层合并键 `<<` 带入的，无法单独去除，整份配置拒绝。`proxy-providers` / `rule-providers` 的 `path` 必须是工作目录内的相对路径，绝对路径或带 `..` 的配置直接拒绝启动；`path` 也不能是或位于 helper 自己的条目之下（`runtime.yaml`、地理数据库、`cache.db`，大小写不敏感）。配置上限 16 MB、检查限时 10 秒；检查按 `yaml.Node` 线性进行（35 万个键约 1 秒），别名与合并的展开量有上限。
    - 地理数据库（`geoip.metadb`、`geosite.dat`、`geoip.dat`、`country.mmdb`、`ASN.mmdb`、`GeoLite2-ASN.mmdb`）与 provider 缓存文件（配置里 `path` 指到的文件，以及 `proxies/`、`rules/` 下的文件）从用户的工作目录复制进去，同样不跟随链接；只在该目录里没有、或用户那份更新时才复制，副本保留源文件的时间，所以几十 MB 的数据库不会每次启动都复制。不复制 `cache.db`、内核程序和日志。一次复制有上限（1024 个文件、768 MB、8 秒），超出的不复制、由内核自己下载；清洗后的配置在所有复制完成之后才写入，复制进来的文件无论以什么名字落地都盖不掉它。每次启动前清掉 30 天没动过、当前配置不引用、用户目录里也没有的旧文件，以及占了内核自有文件名的目录。读取、检查、复制都不占用 helper 的状态锁，期间 `status`/`stop`/`ping` 照常应答，此时到达的 `stop` 会取消这次启动。
    - 不往回复制：特权内核运行期间写下的东西（`cache.db`、TUN 下更新的地理数据库、provider 文件）留在该目录，只供下一次特权运行使用。所以 TUN 模式下「更新地理数据库」更新的是 helper 目录里的那份，设置页显示的日期和「清理内核缓存」针对的仍是用户目录里的那份；节点选择由澜流自己记录并在每次启动后重新应用，不受影响。
    - 热重载：特权内核只肯加载自己工作目录内的文件，所以应用先向 helper 发 `stage`（`protocol.OpStage`，`core.ConfigStager`），helper 重新复制并检查配置，返回副本路径，应用再让内核重载这个路径。helper 拒绝时内核不会被要求重载，继续用原来的配置；内核拒绝重载时，应用回滚 `runtime.yaml` 后再 `stage` 一次，helper 目录里的副本随之回到正在运行的那份。
    - 日志仍写在用户数据目录的 `core/mihomo-tun.log`（helper 以不跟随链接的方式打开后把句柄交给内核）；控制接口的套接字/管道位置不变。
    - 这道检查管的是「启动时的配置」。与安装用户同身份的进程本来就能连上控制接口换掉运行中的配置，helper 看不到；此时起作用的是工作目录归 root/SYSTEM 所有——内核自己「路径必须在工作目录内」的检查不会再被目录里的链接带到别处。
    - 尚未处理：macOS 上控制接口的套接字仍由特权内核创建在用户自己的目录里（内核对该路径先删除、再监听、再改权限），用户在校验之后替换该目录仍是一个竞态窗口。
  - 协议版本（`protocol.Version`）没有变：`stage` 是新增的操作，旧 helper 对它回答「未知操作」，应用据此改为让内核按用户目录里的 `runtime.yaml` 重载（升级后被接管的旧内核本来就运行在用户目录上），所以重装 helper 之前切换订阅、改规则照常可用。应用靠 helper 的版本号（`cmd/lanflow-helper/VERSION`）发现旧 helper 并提示重装；`status` 和 `stop` 在新旧版本之间照常可用，应用才能停掉旧 helper 还在运行的内核。
  - macOS：helper 把内核的 PID 记在只有 root 可写的目录里的 `core.pid`，内核退出时删除；helper 启动时若发现上一个 helper（崩溃或被强杀）留下的内核仍在运行、且确实是受管的内核程序，就结束它（先中断、超时再强杀），PID 已被其他进程占用则不动。
  - Windows：内核进程放进 `KILL_ON_JOB_CLOSE` 的 Job Object，helper 持有唯一句柄，helper 崩溃、被结束或没走完停止流程时系统随之结束内核；建不了 Job 时内核照常运行，只在日志里记一笔。
- 真实后端创建失败时（`main.go` `backend`）不再退回模拟后端：记日志、弹原生错误对话框说明原因，然后以状态 1 退出。模拟后端只在 `LANFLOW_FAKE=1`（开发）时使用——否则界面会显示样例节点和流量、开关看似可用而实际没有代理，ctl 和 MCP 也会拿到同样的假数据。
- 先接 GET /version、GET /proxies 和 PUT /proxies/{name}；再接 /configs、/traffic、/logs、/connections。按官方 API 文档确认方法和请求字段。
- UI 线程不运行阻塞请求；后台请求通过明确的状态更新刷新 UI。退出时取消请求并回收自身子进程。页面自己的状态只在 UI 线程读写：后台任务经 `Env.RunThen`（或方法 `Env.Then`，`internal/view/env`）执行，任务本身只计算，结果由 `done` 回调在 UI 线程、下一次构建之前写回，视图读取时不需要锁；`Env.OnUI` 供任意 goroutine 把一段代码排到 UI 线程。设置按补丁保存：页面调用 `API.UpdateSettings(base, next)`（`base` 是它读到的设置，`next` 是改过的；`app.EditSettings(api, func(*Settings))` 是读、改、提交的简写），服务在持有操作锁时只把两者不同的设置项合并进当前存储的设置（`model.MergeSettings`：由导出字段组成的结构体逐字段比较，列表、时间各算一项），再走与 `SaveSettings` 相同的校验、落盘和应用路径。因此两个页面、界面与 `lanflow ctl`／MCP、界面与配置同步同时各改各的设置项不会互相覆盖；同一个列表（如连通性网站）被两方同时修改时仍是后写的生效。`SaveSettings` 是整份替换，只留给确实要替换全部设置的场景（导入设置、加载配置文件，服务内部经 `saveSettingsFrom`）。设置页的保存仍在视图层排队逐个执行（`internal/view/settings/page.go` `saveThen`），保证同一设置项的先后两次修改按顺序落地；排队期间控件显示并基于草稿继续修改，某次保存失败时草稿按存储的设置加上仍在排队的修改重算。
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

现状：本项目以 MIT 发布（[LICENSE](../LICENSE)）。`scripts/prepare-resources.sh` 调用 `scripts/collect-licenses.sh`，把 Mihomo 的 GPL-3.0 全文、指向固定版本源代码的说明、地理数据库的来源与署名、编入程序的 Go 模块的许可证放进每个安装包的 `licenses/`；清单见 [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)，发布流程把它附到 Release 并核对包内文件。应用标识是 `com.lanflow.app`（`mygo.json` 的 `identifier`，与权限组件的 `com.lanflow.helper` 同一前缀）。0.1.9 及以前用的是占位标识 `com.example.lanflow`：系统凭据库里按旧标识存放的条目在第一次读取时迁到新标识（`main_secrets.go`），Windows 上旧安装程序留下的卸载项在新安装程序写入同一目录的卸载项后删除（`main_identifier_windows.go`）。数据目录按应用名而不是标识命名，不受影响。Developer ID 签名、公证尚未确定。

## 官方资料

- [MyGo 入门](https://github.com/egoist/mygo/blob/main/docs/getting-started.md)
- [MyGo 分发](https://github.com/egoist/mygo/blob/main/docs/distribution.md)
- [Mihomo Releases](https://github.com/MetaCubeX/mihomo/releases)
- [Mihomo API](https://wiki.metacubex.one/api/)
- [Mihomo LICENSE](https://github.com/MetaCubeX/mihomo/blob/Meta/LICENSE)
