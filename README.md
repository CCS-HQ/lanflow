# 澜流 Lanflow

基于 MyGo 原生 UI 的桌面代理客户端，内核为 Mihomo（Clash.Meta），支持**系统代理**与 **TUN 模式**，macOS + Windows。

界面与功能见 [设计文档](docs/design.md)，可交互原型见 [docs/prototype/index.html](docs/prototype/index.html)；开发、CI 与发布见 [docs/development.md](docs/development.md)。

## 安装

从 [最新 Release](https://github.com/Owlbay/lanflow/releases/latest) 下载：

- **macOS**：Apple 芯片选 `lanflow-<版本>-arm64.dmg`，Intel 选 `-amd64.dmg`，把 Lanflow 拖到「应用程序」。安装包未经 Apple 签名公证，首次打开会被拦截：在「系统设置 › 隐私与安全性」里点「仍要打开」，或在终端执行 `xattr -dr com.apple.quarantine /Applications/Lanflow.app`。
- **Windows**：运行 `lanflow-setup-<版本>-amd64.exe`。安装程序未签名，SmartScreen 提示时点「更多信息 › 仍要运行」。
- **Android**（10 及以上，arm64）：下载 `lanflow-<版本>-android-arm64.apk` 安装，需允许浏览器或文件管理器「安装未知应用」。应用内不检查更新，新版本下载后直接覆盖安装。

### 验证下载

每个 Release 附带 `SHA256SUMS.txt`。把它和安装包放在同一目录后：

```sh
shasum -a 256 -c SHA256SUMS.txt --ignore-missing     # macOS
sha256sum -c SHA256SUMS.txt --ignore-missing         # Linux、Git Bash
```

Windows PowerShell 可用 `Get-FileHash .\lanflow-setup-<版本>-amd64.exe -Algorithm SHA256` 与 `SHA256SUMS.txt` 中的值对照。

安装包由 GitHub Actions 构建并带有构建来源证明，可用 [GitHub CLI](https://cli.github.com/) 确认它出自本仓库的发布流程：

```sh
gh attestation verify lanflow-<版本>-arm64.dmg --repo Owlbay/lanflow
```

仓库在 `CCS-HQ/lanflow` 下时发布的旧版本（v0.1.20 及以前的部分版本），证明里记录的是当时的仓库名；按上面的命令校验不通过时，把 `--repo` 换成 `CCS-HQ/lanflow` 再试。

## 功能

- **概览**：内核、系统代理（全局或 PAC）、TUN 开关与规则 / 全局 / 直连模式切换，出口地图，当前订阅与流量用量，实时速度；侧栏底部的状态条常驻显示内核状态、模式与上下行速度。TUN 通过权限组件 `lanflow-helper` 运行，应用本身不提权，IPv4 与 IPv6 一并接管
- **概览**：可拖动旋转、缩放的 3D 地球出口图（请求流向、实时速度、自动聚焦）、终端代理命令；内核异常、找不到内核或配置同步出错时在这里提示
- **代理与订阅**：代理组、节点切换、测速（可设测速地址与超时）、组图标、定位当前节点；订阅链接 / 文件 / 拖入导入、自动更新、按 Wi-Fi 自动切换订阅；聚合订阅：把多个订阅链接、YAML 配置、节点分享链接和已有订阅合并成一个，在左右两栏的配置生成器里套用模板或逐项设置节点处理、中转组、分流代理组、自定义规则与排序，由澜流在本机生成配置并预览（[docs/aggregate.md](docs/aggregate.md)）；多个有序 YAML 覆写（本地或链接）
- **连接**：列表与网络拓扑两种视图（来源 → 进程 → 规则 → 代理组 → 节点 → 目标），应用图标，从连接添加规则
- **日志**：内核日志与澜流运行日志，级别筛选、搜索；一键导出脱敏的诊断信息
- **规则**：自定义规则、命中统计、临时停用、规则测试、规则集
- **网络**：直连与代理出口识别、网站连通性、DNS 查询、泄漏检查（DNS / IPv6 / WebRTC / IP 类型 / DNS 对比）
- **统计**：按设备、域名、进程、规则、代理、地区、入站统计请求数与上下行流量，按天保存
- **检测**：出口 IP 纯净度、常见服务解锁检测、按地区探查节点
- **抓包**（默认关闭，在「设置 › 常规 › 功能」开启；关闭时侧栏、连接页菜单与托盘都不显示抓包入口，并同时停止解密）：按域名（含通配）或程序选择性解密 HTTPS，查看请求 / 响应头、正文、TLS 与耗时，默认打码，可导出 HAR、重新发送或编辑后重新发送；连接页可直接「解密此主机 / 此程序」，「检查目标」逐项说明某个地址能否抓包。根证书在本机生成、私钥不出钥匙串（Windows 为不可导出的 CNG 密钥），解密 30 分钟后自动关闭；未选中的流量不经过解密层
- **内核**：Mihomo 正式版 / 预发布多版本并存，一键切换与回滚，检查更新，下载镜像
- **设置**：常规（外观、语言（简体中文 / English）、快捷键、开机启动、菜单栏显示（macOS：实时速度、流量图、当前节点、代理模式）、配置同步、WebDAV 备份、检查更新、反馈上报）、代理（端口、局域网共享、绕过地址、测速）、TUN（协议栈、自动设置系统 DNS、aTrust 兼容）、DNS、嗅探（跟随订阅 / 由澜流接管）、覆写与运行时配置、内核与外部资源、高级（独立端口、局域网访问控制与认证、TUN 细项、内核行为、DNS 与嗅探的其余字段）
- **共存**：与其他代理软件共存，深信服 aTrust 兼容模式
- **反馈问题**：填写描述、可选附带诊断包，发送前完整展示将要发出的内容；报告发往你在「设置 › 常规 › 关于 › 反馈上报」里填写的自建服务（默认为空，只能保存到文件），接口约定见 [feedback-api.md](docs/feedback-api.md)，参考实现见 [examples/feedback-worker](examples/feedback-worker/)
- **后台服务与按需界面**：代理、订阅更新、统计和托盘由常驻的后台服务进程负责，关掉窗口后界面进程退出、释放内存，代理和连接不受影响；托盘图标按状态区分（接管流量 / 仅内核运行 / 关闭，TUN 开启时变色，解密中带圆点）
- **全局搜索**（⌘K / Ctrl+K）：一个搜索框找到全部页面、常用操作、每个设置项（可用 Mihomo 字段名搜）、代理组与节点、订阅和自定义规则，也能直接在连接、日志、规则等页按输入内容筛选
- 托盘菜单、全局快捷键、单实例、异常退出后的系统代理恢复、内核崩溃自动重启

## 目录

| 目录 | 职责 |
| --- | --- |
| `main*.go` | 入口：后台服务进程（`--service`）与窗口进程、单进程排障模式、`ctl` / `mcp` / `capture-agent` 子命令 |
| `internal/split`、`internal/autostart` | 两个进程的启动、连接与交接；登录时启动后台服务 |
| `internal/model` | 共享数据类型 |
| `internal/app` | 服务层（`app.API` 的实现）；`app/remote` 为窗口进程使用的远程客户端；`app/fake` 为界面开发用的模拟数据 |
| `internal/core` | Mihomo 进程管理（unix socket / 命名管道控制，无 TCP 端口） |
| `internal/controller` | Mihomo REST / WebSocket 客户端 |
| `internal/profile` | 订阅存储与下载、覆写合并、运行时配置合成 |
| `internal/syncfile` | 配置同步文件 `lanflow.yaml` 的格式、校验与 JSON Schema |
| `internal/capture` | HTTPS 解密：解密子进程（`agent`）、根证书（`ca`）、系统信任（`trust`）、打码（`redact`）、HAR 导出（`har`） |
| `internal/platform/sysproxy`、`sysdns`、`netinfo` | 系统代理、系统 DNS、本机网络信息 |
| `internal/platform/atrust` | 深信服 aTrust 等分隧道 VPN 的检测 |
| `internal/platform/procicon` | 应用图标 |
| `internal/platform/helper`、`cmd/lanflow-helper` | TUN 权限组件（macOS LaunchDaemon / Windows 服务） |
| `internal/geo`、`internal/locate` | 节点名识别、Cloudflare trace 实测、中转分析、陆地点阵 |
| `internal/applog` | 澜流运行日志与本地错误收集 |
| `internal/i18n` | 界面文案（简体中文为源语言，另有英文） |
| `internal/netx`、`internal/fsx`、`internal/seqring` | 不走系统代理的 HTTP 客户端、原子写文件、跨进程增量读取用的环形缓冲 |
| `internal/view/...` | 界面：外壳、托盘与各页面 |
| `internal/rpc` | 本机接口（JSON-RPC 服务端、权限分级、审计与撤销）与方法清单 |
| `internal/ctl`、`internal/mcp`、`skills/lanflow` | `lanflow ctl`、`lanflow mcp` 与配套的 Claude skill |
| `scripts/` | 下载内核、准备打包资源与许可证材料、构建应用、虚拟机测试 |
| `tools/landgrid` | 生成陆地点阵数据 |
| `examples/feedback-worker` | 反馈上报服务的 Cloudflare Worker 示例 |

技术方案见 [architecture.md](docs/architecture.md)、[service-split.md](docs/service-split.md)（后台服务与本机接口）、[capture.md](docs/capture.md)（HTTPS 解密）、[config-sync.md](docs/config-sync.md)、[advanced.md](docs/advanced.md)、[exit-map.md](docs/exit-map.md)、[feedback-api.md](docs/feedback-api.md)。

## 本地开发

需要 `go.mod` 中声明的 Go 版本或更新。先下载官方 Mihomo（版本见 `scripts/fetch-core.sh` 的 `PINNED_VERSION`，按 SHA-256 校验）与地理数据库到 `bin/`，然后运行：

```sh
scripts/fetch-core.sh                 # 宿主平台；也可 scripts/fetch-core.sh windows amd64
go tool mygo dev                      # 真实服务
LANFLOW_FAKE=1 go tool mygo dev       # 模拟数据
go vet ./... && go tool mygo vet . && go test -shuffle=on ./...
```

测试的细节、CI、打包、发布流程和应用自动更新见 [docs/development.md](docs/development.md)。

## 配置同步

「设置 › 常规 › 配置同步」把设置、覆写、自定义规则和订阅保存为一个声明式文件 `lanflow.yaml`（`format: 1`，结构见 [`docs/schema/lanflow.schema.json`](docs/schema/lanflow.schema.json)），放进 iCloud 云盘（macOS）或任意同步盘文件夹（OneDrive、坚果云、Dropbox……），多台设备共用：

- 文件变化时澜流会校验（含 `mihomo -t`）后热加载，按改动热更新、重载或重启内核；不合法的修改被拒绝，配置保持原状，设置页和概览页给出原因与行号。本机自己的写入按内容哈希识别，不会被当成外部修改。
- 订阅链接和 WebDAV 密码只以 `keychain:<id>` 引用写进文件，值存系统钥匙串（macOS Keychain / Windows 凭据管理器）。另一台设备的钥匙串里没有时，订阅标为「需要重新输入链接」，不影响其余配置。目前不能借助 iCloud 钥匙串同步这些条目（需要团队签名的应用权限），原因见文档。
- TUN、内核路径、网卡名、界面状态、agent 授权等本机项不同步；缓存、统计、`runtime.yaml` 等运行时数据一直留在本机。
- 开启时若文件夹里已有不同的配置，会让你选择用文件还是用本机配置覆盖；冲突副本（如 `lanflow 2.yaml`）只提示、由你选择保留哪个，不自动删除。
- 导出 / 导入与 WebDAV 备份也改用这个格式，旧版本导出的 JSON 和 `.json` 备份仍可导入。

agent 可以直接编辑 `lanflow.yaml`，或用 `lanflow ctl config sync apply <文件> --dry-run` 预览差异；`lanflow ctl config sync status` 查看同步状态。详见 [docs/config-sync.md](docs/config-sync.md)。

## 用 agent 管理澜流

澜流的后台服务在本机提供一个只有当前用户能连接的接口（macOS `~/Library/Application Support/Lanflow/run/lanflow.sock`，Windows 命名管道 `\\.\pipe\lanflow-<用户 SID>`），不开 TCP 端口。同一个可执行文件提供两个客户端：

- **`lanflow mcp`**：stdio MCP 服务，供 Claude Desktop、Claude Code 等使用。在「设置 › Agent」点「复制 MCP 配置」，粘贴到 `claude_desktop_config.json` 或项目的 `.mcp.json`；或复制 `claude mcp add` 命令在终端运行。macOS 上的配置形如：

  ```json
  {
    "mcpServers": {
      "lanflow": {
        "command": "/Applications/Lanflow.app/Contents/MacOS/Lanflow",
        "args": ["mcp"]
      }
    }
  }
  ```

  工具由接口的方法清单生成，另有组合工具 `route_domain`（让域名走某个节点或代理组：规则测试 → 生成规则 → 预览 → 应用 → 验证）、`diagnose_site`、`diagnose`、`switch_node`，以及与窗口里全局搜索（⌘K）对应的 `search`（一次查找节点、代理组、订阅、规则、覆写、设置字段和工具本身）。仓库里的 [`skills/lanflow/SKILL.md`](skills/lanflow/SKILL.md) 是配套的 Claude skill（标准流程、常见请求、禁区）。
- **`lanflow ctl <命令>`**：命令行，默认输出 JSON，`--format table` 给人看：`status`、`proxies list|select|test`、`mode set`、`sub list|add|use|update`、`rule add|test`、`config get|set|apply`（`--dry-run` 输出差异）、`config sync status|apply`、`diag`、`undo`、`audit`，以及 `methods`（本机接口的方法清单与权限级别）和 `call <方法> [JSON 参数...]`（直接调用其中一个方法）。macOS 上可以 `ln -s /Applications/Lanflow.app/Contents/MacOS/Lanflow /usr/local/bin/lanflow`。澜流没运行时以退出码 3 报错。

权限分级（`docs/service-split.md` §4）：

- 读取状态、节点、规则、设置随时可用；返回的设置和订阅都经过与诊断导出相同的脱敏，订阅链接不会出现。
- 连接明细、日志原文、订阅原文、运行时配置属于敏感读取：MCP 每次会话需在澜流里确认；ctl 需加 `--reveal` 并确认。
- 修改配置时，每个 MCP 客户端第一次写入会在澜流里弹窗（同时发系统通知）请你选择「仅本次 / 始终允许 / 拒绝」，结果可在「设置 › Agent」撤销。ctl 由你本人在终端执行，不再询问。
- 安装权限组件、切换内核、删除订阅、导出/恢复备份等危险操作不向 MCP 开放；ctl 每次都要在澜流里确认。
- 每次写入都记入日志目录下的 `audit.log`（参数已脱敏）；`lanflow ctl undo`（或 MCP 的 `undo`）撤销本次运行中最近一次写入，覆盖设置、自定义规则、覆写、节点选择和当前订阅。

## 日志与诊断

- **澜流运行日志** `lanflow.log`（macOS `~/Library/Logs/Lanflow/`，Windows `%LOCALAPPDATA%\Lanflow\Logs\`，满 5 MB 轮转，保留 1 份）：记录内核启停、重载和崩溃，系统代理、TUN 和模式切换，权限组件安装，订阅导入、更新和自动更新，覆写保存，设置改动（只记字段名），系统 DNS 的设置与恢复，aTrust 检测结果，以及界面上弹出过的每条提示和错误。写入前会把链接改成只保留域名，并给 token、password 等值打码。
- **内核日志** `mihomo.log`（同目录，5 MB 轮转）；TUN 模式下由权限组件运行的内核写 `<数据目录>/core/mihomo-tun.log`；权限组件的日志在 `/Library/Logs/Lanflow Helper/`。
- **日志页**：可在「内核」和「澜流」两种日志之间切换，支持级别筛选、搜索、暂停、复制，也可在访达中显示对应文件。要看 DNS 解析过程，把内核日志级别调到「调试」后搜索 `dns`。
- **导出诊断信息**（日志页 ⋯ 菜单，或「设置 › 常规」）：打包成 ZIP，包含上述日志（每个文件最多取最后 2 MB）、运行时配置和覆写（只保留节点名称和类型，删去服务器地址、UUID、密码、密钥和传输参数）、设置、状态摘要、订阅列表（只保留域名），以及系统路由表、DNS 解析器和系统代理设置。内核日志里仍有访问过的域名，分享前请自行检查。

## 与其他代理软件共存

- 代理端口被占用时不会悄悄运行：首次运行若 7890 已被占用会自动选下一个空闲端口；之后改到被占用的端口或启动时端口被占用，都会明确报错。
- TUN 已通过权限组件启动后，开关 TUN 会热重载同一个内核；关闭时释放虚拟网卡并恢复 DNS，保留系统代理监听端口，避免切换时等待约 30 秒。关闭 TUN 后内核仍由权限组件以 root/SYSTEM 身份托管；停止或重启内核、退出应用时会结束它，下次按保存的模式选择普通或权限内核。macOS 真正从权限内核切回普通内核时仍需等待端口释放（最多 35 秒），该等待记录会跨应用重启保留。
- 开启系统代理前会保存原设置（包括其他软件设置的代理和绕过列表），关闭时若仍指向澜流就原样恢复；若已被其他软件改过则不去动。
- 检测到其他软件的 TUN 正在接管默认路由时，拒绝开启澜流的 TUN 并说明原因。
- macOS 开启 TUN 时默认把系统 DNS 指向 223.5.5.5、119.29.29.29（否则发往路由器等局域网 DNS 的查询不经过 TUN），关闭 TUN、停止内核或退出时恢复原设置；期间被用户改过的网络服务不会被覆盖，异常退出后下次启动时恢复。可在「设置 › TUN」中关闭。
- **aTrust 兼容**（「设置 › TUN」，默认关闭）：检测到深信服 aTrust 运行时（进程名含 aTrust / Sangfor），读取它的隧道路由、内网 DNS 域名和解析器以及它自身连接的网关（macOS 用 `netstat -rn`、`scutil --dns`、`netstat -anv`；Windows 用系统路由表、NRPT 规则和 TCP 连接表），然后：内网网段和网关写入 `tun.route-exclude-address`，不进入澜流的 TUN（内核重启也不会打断 aTrust 隧道）；含有 DNS 服务器（系统 DNS、aTrust 的解析器或配置的公司 DNS）的网段**不会**被排除，只直连并仍经过 TUN，否则 DNS 劫持失效，需要走代理的网站全部无法访问；aTrust 进程、内网网段、内网域名作为最前面的 DIRECT 规则；内网域名加入 `fake-ip-filter` 和嗅探的 `skip-domain`，并通过 `nameserver-policy` 交给公司 DNS 解析；aTrust 在线时加密 DNS 常被掐断，因此追加 223.5.5.5 / 119.29.29.29 纯 UDP 上游兜底；fake-ip 网段或 TUN 地址与内网重叠时自动换用其他网段；澜流的 TUN 固定命名为 `utun1700`，与 aTrust 的 utun 区分；系统代理的例外列表加入内网。「自动设置系统 DNS」保持开启。每 10 秒重新检测，aTrust 连上、断开或退出后自动重新生成配置。这些规则在「运行时配置」里标为「aTrust 兼容」。
  - **配置**（「设置 › TUN › aTrust 兼容 › 配置…」）：检测不到的部分按行填写——内网域名（走公司 DNS）、直连域名（钉钉、企业微信等公网 SaaS，直连但绝不走公司 DNS）、公司 DNS、不进入 TUN 的网段（aTrust 映射段、只能经 aTrust 到达的内网段）、仅直连的网段（含公司 DNS 的 10.0.0.0/8 之类），以及「未运行 aTrust 时也应用」（办公网不开 aTrust 时内网域名仍需公司 DNS）。表单实时校验，含公司 DNS 的网段放进排除列表会被拦下。澜流不内置任何公司的地址：「复制参考配置」/「导出参考配置…」给出一份带中文说明、用示例值（corp.example.com、10.x、203.0.113.x）写成的 YAML 模板；「导入…」读取澜流的 YAML/JSON，或 JavaScript 覆写脚本（如 `atrust-coexist.js`）（`atrustDns`→公司 DNS、`corpDomains`→内网域名、`publicSaasDomains`→直连域名、`atrustCIDRs`→不进入 TUN 的网段、`IP-CIDR,…,DIRECT` 规则→仅直连的网段），校验去重后填入表单，确认后再保存；「导出…」写出 YAML。其他分隧道 VPN（如 Tailscale）的网段在 aTrust 运行时也会一并直连；接管全部流量的其他代理 TUN 不算在内。
- **内置 Tailscale**（「设置 › TUN」，默认关闭，只在本机）：澜流的内核自己作为设备加入 tailnet（Mihomo 的 tailscale 出站），不用再运行 Tailscale App。没有 auth key 时点「登录…」在浏览器完成登录，auth key 存系统钥匙串；支持 Headscale 控制服务器、接受子网路由和出口节点；设置页显示登录状态、tailnet 地址和账号。局限：只有经过澜流的流量（TUN、系统代理、设置了代理的应用）能访问 tailnet；本机不会以自己的身份成为 tailnet 节点，别的设备无法经 tailnet 访问本机的服务（SSH、文件共享），Taildrop 和 Tailscale SSH 不可用；退出澜流即断开 tailnet。检测到 Tailscale App 运行时内置节点自动暂停，二者只用一个。详见 [docs/tailscale.md](docs/tailscale.md)。

## 已知限制

- Windows 版的测试（含内核端到端测试和界面测试）在 CI 的 Windows 机器上运行，尚未在真实 Windows 桌面上人工验证。
- TUN 已在 macOS 26 虚拟机中实测（`scripts/vm-tun-test.sh <虚拟机名>`）：安装权限组件、开启 TUN、无代理设置的请求被 TUN 接管、关闭后路由恢复、卸载无残留。图形界面里的管理员授权弹窗尚未实测。
- 「自动设置系统 DNS」目前只支持 macOS；Windows 上 DNS 若指向局域网地址，查询不经过 TUN，只能按 IP 和嗅探分流。
- aTrust 兼容尚未在装有 aTrust 的机器上实测。
- 本机同时运行其他代理软件的 TUN 时，「直连」探测也会被其接管，地图上「你」的位置可能不准。
- HTTPS 解密在 Windows 上只做了编译检查，CNG 密钥与 `CurrentUser\Root` 的证书信任尚未实测；卸载程序不会移除根证书，需要先在应用内移除。详见 [capture.md §15](docs/capture.md#15-实现状态)。
- macOS 14 起，未授予定位权限时系统隐藏 Wi-Fi 名称。

## 许可

澜流本身以 [MIT 许可证](LICENSE) 发布。

安装包里另有第三方组件，清单与许可证见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)，许可证全文在安装包内的 `licenses/` 文件夹（macOS：`Lanflow.app/Contents/Resources/licenses/`）：

- **Mihomo** 内核为 GPL-3.0，以独立程序随包分发，未作修改；包内附有许可证全文和对应版本源代码的地址；随包的版本见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)，上游最新版见 [Mihomo Releases](https://github.com/MetaCubeX/mihomo/releases/latest)。
- **地理数据库**来自 [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat)。This product includes GeoLite2 Data created by MaxMind, available from <https://www.maxmind.com>.
- 地图陆地数据来自 Natural Earth（公有领域）。

参考：[MyGo](https://github.com/egoist/mygo)、[Mihomo](https://github.com/MetaCubeX/mihomo)、[Mihomo API](https://wiki.metacubex.one/api/)。

## 致谢

澜流的灵感来自 Clash Party。所有代理能力都直接基于 Mihomo 内核的配置与接口实现，代码为本项目独立编写。
