# iOS 版：系统 VPN 与包隧道扩展

iOS 上 App 接管流量的唯一途径是系统 VPN（NetworkExtension 的包隧道），而且 iOS 不允许启动子进程。所以澜流的 iOS 版这样组成：

```
Lanflow.app                       主 App（MyGo 构建）：界面 + 服务层
├── Lanflow                       Go（含 internal/platform/tunnel/iosvpn）+ MyGo 宿主 Swift
│                                 + 插件 lanflowtunnel（NETunnelProviderManager）
└── PlugIns/PacketTunnel.appex    包隧道扩展（scripts/build-ios.sh 构建）
    └── PacketTunnel              Swift（NEPacketTunnelProvider）+ Mihomo（Go c-archive）
```

## 运行方式

| 环节 | 位置 | 做什么 |
| --- | --- | --- |
| `tunnel.Tunnel` | `internal/platform/tunnel/tunnel_ios.go` | iOS 构建时 `init` 里注册；`Binary` 为 `tunnel.BuiltinBinary`，`Version` 为内置版本 `iosvpn.CoreVersion` |
| Runner | `internal/platform/tunnel/iosvpn/vpn_ios.go` | `Start`：把配置交给扩展，经插件 `startVPNTunnel`；`Stop`：`stopVPNTunnel` 并等系统报告断开；系统报告断开而非 `Stop` 时经 `Exited` 报告（附 `fetchLastDisconnectError` 的原因和内核日志末尾）。同时实现 `core.ConfigStager`（热重载）和 `core.Adopter`（App 重开时 VPN 仍在运行） |
| 插件 | `internal/platform/tunnel/iosvpn/ios/LanflowTunnelPlugin.swift` | 方法 `start` / `stop` / `status` / `message`，事件 `status`；首次 `start` 时保存 VPN 配置，系统随之请求用户允许 |
| 扩展 | `mobile/ios/PacketTunnel/PacketTunnelProvider.swift` | 设置隧道网络（地址、全部路由、TUN 内的 DNS、IPv6 视配置而定），拿到 utun 描述符后启动内核；处理 App 消息 `stage` / `memory` / `version` |
| 内核 | `mobile/ios/tunnelcore`（独立 Go 模块） | Mihomo v1.19.34 以库方式运行；`-buildmode=c-archive` 链进扩展 |

### App Group 里的文件

扩展读不到主 App 的沙盒，两者经 App Group 容器（`group.com.lanflow.app`，两边 Info.plist 的 `LanflowAppGroup`）交接：

```
<group>/core/          Mihomo 的 home（-d）
  source.yaml          主 App 交来的运行时配置（其中指向主 App 内核目录的绝对路径已改到此目录）
  config.yaml          扩展据 source.yaml 生成、内核实际运行的配置
  geoip.metadb 等      从主 App 内核目录复制的地理数据（大小或时间变了才复制）
  mihomo.log           内核日志（超过 2 MB 轮转一份）
<group>/c.sock         控制接口的 unix socket，主 App 的 controller 客户端照常连接
```

真机上容器路径形如 `/private/var/mobile/Containers/Shared/AppGroup/<UUID>`，socket 路径约 90 字节，在 unix socket 的 104 字节上限内（超长时 `Start` 直接报错）。模拟器的容器路径更长，但模拟器本来就不运行包隧道。

### 为什么由扩展生成 config.yaml

TUN 的文件描述符只有扩展知道，而控制接口的 `PUT /configs` 会按所载文件重新应用 `tun` 段——一份没有描述符的配置会让 TUN 掉线。所以每次启动和热重载前，配置都经扩展（`stage` 包）覆盖这些项再交给内核：

- `tun`：`enable`、`stack: mixed`（TCP 走 system 栈、UDP 走 gVisor，比纯 gVisor 省内存）、`file-descriptor`、`mtu: 1500`、`dns-hijack: [any:53, tcp://any:53]`；`auto-route`、`auto-redirect`、`auto-detect-interface`、`strict-route` 关闭（路由和 DNS 由 NetworkExtension 设置，扩展自身的连接本来就不经过隧道）；去掉 `device`。
- 控制接口只留 `external-controller-unix`；去掉 TCP/TLS 控制端口和 `external-ui`。
- `find-process-mode: off`、去掉 `interface-name` / `routing-mark`。
- `geodata-loader: memconservative`、`geo-auto-update: false`。

TUN 的 IPv4 地址由 Mihomo 按 `dns.fake-ip-range` 的首地址取 /30（默认 `198.18.0.1`），扩展据此设置隧道网络；DNS 服务器设为下一个地址，由 dns-hijack 应答。

热重载流程：服务层调用 `ConfigStager.StageConfig` → 主 App 重写 `source.yaml` → `sendProviderMessage({"op":"stage"})` → 扩展生成 `config.yaml` 并回复其路径 → 服务层 `PUT /configs` 该路径（在 Mihomo home 内，通过 `IsSafePath`）。

### 配置校验

`Validate`（代替 `mihomo -t`）在主 App 里只检查配置是一个 YAML 映射：主 App 不链接 Mihomo（否则主 App 多出几十 MB），在扩展里另做一次完整解析又会让内存峰值翻倍。完整校验由 Mihomo 加载时完成：启动失败时 VPN 断开并带原因，热重载失败时 `PUT /configs` 返回 400，服务层保留原配置。

## 内存

包隧道扩展的内存上限约 50 MB（超出即被系统结束）。已采取的措施：

- `debug.SetMemoryLimit(32 MB)` + `GOGC=50`；
- Mihomo 构建标签 `with_low_memory`（小缓冲区），去掉 Tailscale / ZeroTier / EasyTier 出站（`no_*`，它们自带的用户态网络栈单独就会超限）；
- `geodata-loader: memconservative`，不在扩展里更新地理数据；MMDB 以 mmap 读取，干净的文件页不计入占用；
- MTU 1500（gVisor / system 栈的每个包缓冲按 MTU 分配）。

估计：Go 堆空闲约 10–15 MB，规则较多（几万条域名规则）时 20–30 MB；加上可执行文件的脏页、Swift 与 NetworkExtension 约 5–8 MB。中小规模订阅应在上限内；超大规则集、GeoSite 规则（`.dat` 加载开销大）和大量并发连接是主要风险。可执行文件约 40 MB（其中 `__noptrbss` 的 32 MB 是 Go 标准库 FIPS 140 熵源的暂存区，只在 FIPS 模式下使用，零填充页不占内存）。

真机上可用扩展的 `memory` 消息或 Mihomo 控制接口的 `GET /memory` 查看实际占用，Xcode 的 Debug Navigator / Instruments 可看扩展进程的 footprint。

## 构建

```bash
scripts/build-ios.sh simulator
```

先 `go tool mygo build -platform ios/simulator`，再编译 tunnelcore（`GOOS=ios`、c-archive）、编译并链接扩展、放进 `Lanflow.app/PlugIns/`、先签扩展再签外层 App（模拟器为 ad hoc 签名，权限像 MyGo 对主 App 那样以 `__TEXT,__entitlements` 段写进可执行文件）。**模拟器不支持包隧道**：模拟器构建只用来检查编译和包结构，VPN 启动会失败。

```bash
LANFLOW_IOS_EXT_PROFILE=/path/to/PacketTunnel.mobileprovision scripts/build-ios.sh device
```

真机构建：MyGo 用主 App 的描述文件签出 `.ipa`，脚本解开后放入扩展，用扩展的描述文件签扩展，再用同一身份重签外层 App（Network Extension 权限收窄为 `packet-tunnel-provider`），重新打包 `.ipa`。

检查签名：

```bash
codesign -dv --entitlements - build/ios-simulator/Lanflow.app/PlugIns/PacketTunnel.appex
```

```bash
codesign -dv --entitlements - build/ios-simulator/Lanflow.app
```

### 真机需要的准备（开发者账号）

包隧道需要付费的 Apple Developer Program 账号。在 developer.apple.com 上：

1. 建 App Group `group.com.lanflow.app`。
2. App ID `com.lanflow.app`：打开 Network Extensions 与 App Groups（选上面的组）。
3. App ID `com.lanflow.app.PacketTunnel`：同样打开 Network Extensions 与 App Groups。
4. 为两个 App ID 各做一个描述文件（开发用选 Development 并勾选测试设备；上架用 App Store），下载安装。
5. 在 `mygo.json` 的 `ios` 一节填 `team`（以及需要时的 `provisioningProfile`、`distribution`），然后用 `LANFLOW_IOS_EXT_PROFILE` 指向扩展的描述文件运行 `scripts/build-ios.sh device`。
6. 用 Xcode 的 Devices 窗口或 `xcrun devicectl device install app` 安装 `.ipa`，首次连接时系统会请求允许添加 VPN 配置。

## 已知限制

- **许可证**：扩展静态链接了 Mihomo 等 GPL-3.0 代码，扩展整体按 GPL-3.0 分发（见 [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)）。GPL-3.0 与 App Store 的使用条款之间有公认的冲突（额外限制条款），上架 App Store 前需要评估；TestFlight / Ad Hoc / 企业分发同样涉及。
- 扩展内不支持 Tailscale、ZeroTier、EasyTier 类型的出站。
- 改变 `dns.fake-ip-range` 后热重载，TUN 地址与隧道网络会不一致，需要重启 VPN。
- `tun.route-exclude-address` 等路由类设置不生效（路由由 NetworkExtension 设置，目前是全局路由）。
- 内核日志在 App Group 的 `core/mihomo.log`，服务层的 `spec.LogFile` 不会被写入。
- 服务层在手机上启动和热重载都经 `ConfigStager.StageConfig`（`reloadPath` 的手机分支由 Android 分支提交），与设置里的 TUN 开关无关。VPN 未连接时（启动前）`StageConfig` 只把配置交到 App Group，返回 `source.yaml`，由扩展启动时生成 `config.yaml`；已连接时经扩展生成并返回 `config.yaml`。
- 每次 App 启动若服务层直接 `Start`，正在运行的 VPN 会先停后起；接管运行中的 VPN 已实现为 `core.Adopter`，服务层手机分支目前没有调用。
