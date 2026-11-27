# Android 版：系统 VPN 与内核

澜流在 Android 上用系统 VPN（`VpnService`）接管流量，Mihomo 内核作为子进程跑在 VPN 的 TUN 文件描述符上。本文说明结构、构建、验证情况和已知限制。

## 结构

| 位置 | 作用 |
|---|---|
| `internal/platform/tunnel/android/` | MyGo 原生模块：Go 接口 `VPN`（`android.go`），生成的绑定（`android_native.go`、`*.mygo.kt`，勿手改，`mygo native check` 重新生成） |
| `kotlin/.../LanflowVpnService.kt` | `VpnService`：前台服务通知、建立接口、`onRevoke`/`onDestroy` 事件 |
| `kotlin/.../Android.kt` | 给 Go 的入口：`prepare`（用户授权，顺带不等待地申请通知权限）、`start` 返回 fd、`stop`、`events`、`nativeLibraryDir`、`apkPath` |
| `kotlin/src/main/AndroidManifest.xml` | service 声明（`BIND_VPN_SERVICE`、`foregroundServiceType="specialUse"`）、前台服务权限；合并进 App 的 manifest |
| `kotlin/src/main/jniLibs/arm64-v8a/libmihomo.so` | 内核程序（不入库，`scripts/build-core-android.sh` 生成） |
| `internal/platform/tunnel/tunnel_android.go` | 只在 Android 编译：`init` 里 `tunnel.Register`；实现 `Tunnel`、`core.Runner`、`core.ConfigStager` |
| `internal/platform/tunnel/fdconfig.go` | 把运行配置改写成"在 fd 上跑 TUN"的版本（各平台都编译，桌面可跑单元测试） |
| `mygo.json` 的 `android.minSdk: 29` | 从 APK 内运行程序需要 Android 10+ 的动态链接器 |

### 启动流程

1. 服务层（`internal/app`）照常生成 `runtime.yaml`，调 `Runner.Start(spec)`。
2. Runner 用 `fdConfig` 改写 `spec.Config`，写到 `<workDir>/android-runtime.yaml`：
   - `tun.enable: true`、`tun.file-descriptor: 3`、`tun.stack: gvisor`（见下文）
   - `auto-route`、`auto-detect-interface`、`strict-route` 关掉；删掉 `device`、`include/exclude-interface`、`route-address`、uid/package 过滤、`auto-redirect` 和根上的 `interface-name`——路由和出口网卡归系统 VPN
   - `inet4-address`（默认 `198.18.0.1/30`）、`inet6-address`（默认 `fdfe:dcba:9876::1/126`）、`mtu`（默认 9000）写成显式值，VPN 接口用同样的地址
   - 配置没有启用的 `dns` 时补上与 `profile.compose` 开 TUN 时相同的默认 DNS（fake-ip、doh.pub/alidns）：VPN 给应用的 DNS 服务器只有 TUN 的 `dns-hijack` 会应答，没有内核 DNS 就解析不了域名
3. `VPN.Prepare`：首次弹系统的 VPN 授权框（`VpnService.prepare`）。Android 13+ 顺带申请通知权限但不等结果——服务层的启动有时限，曾因等这个弹框而超时。
4. `VPN.Start`：启动前台服务，`Builder` 设地址、`0.0.0.0/0` 与 `::/0`、DNS（TUN 地址的下一个，如 `198.18.0.2`）、`route-exclude-address`（Android 13+ 的 `excludeRoute`）、`addDisallowedApplication(自己)`，`establish()` 后 `detachFd()` 把 fd 交给 Go。
5. Go 用 `exec.Cmd.ExtraFiles` 把 fd 作为 3 号描述符传给内核：`/system/bin/linker64 <apk>!/lib/arm64-v8a/libmihomo.so -d <workDir> -f android-runtime.yaml -ext-ctl-unix <sock>`，随后关掉自己那份 fd——内核退出时接口随之失效，不会把流量吞进黑洞。
6. 服务层照常经控制接口（unix socket，`core.ControllerAddress()`，即 MyGo 设置的 `TMPDIR` = 应用 cache 目录，实测 `/data/user/0/<包名>/cache/lanflow-<uid>.sock`，远短于 `sun_path` 限制，仅本应用可写）等 `GET /version`。

停止：SIGINT，5 秒后 SIGKILL，再停前台服务。用户在系统设置里断开 VPN 或别的 VPN 接管（`onRevoke`）、系统结束服务（`onDestroy`）时，Runner 结束内核并经 `Exited()` 报告原因。"停止"的标记按服务实例记，避免上一个实例迟到的 `onDestroy` 误杀新启动的内核（实测遇到过）。

### 为什么从 APK 内运行内核

Android 10 起应用不能执行数据目录里的文件（W^X），能执行的只有 APK 和原生库目录里的。MyGo 的 Gradle 模板固定 `useLegacyPackaging = false`（合并后 `extractNativeLibs=false`），原生库不解压，`nativeLibraryDir` 是空的。所以内核以 `libmihomo.so` 未压缩地放在 APK 里，用系统动态链接器 `/system/bin/linker64` 按 `base.apk!/lib/arm64-v8a/libmihomo.so` 路径加载运行（与系统加载 APK 内 `.so` 的方式相同；要求内核是 PIE，Android 版 Mihomo 是）。若某天原生库被解压了，`Binary()` 优先用 `nativeLibraryDir` 里的文件直接执行。

### 为什么自己编译内核

官方 `mihomo-android-arm64` 发布版的 TUN 启动时无条件读 `/data/system/packages.xml`（用于包名↔uid 映射），只有 root 能读，普通 App 里报 `build android rules: read packages list: permission denied`，TUN 起不来。`-tags cmfa` 可以绕开，但会打开 embed 模式，禁用 `PUT /configs` 热重载和 system DNS。

所以 `scripts/build-core-android.sh` 从源码编译：
- 源码是 Go 模块 `github.com/metacubex/mihomo@v1.19.34`，`go mod download` 按 sum.golang.org 校验，脚本再比对固定的 `h1:` 值；依赖按 Mihomo 自己的 go.sum 校验
- 只改一处（在源码副本上）：`listener/sing_tun/server_android.go` 的 `buildAndroidRules` 在配置没写 `include-package`/`exclude-package`/`include-android-user` 时直接返回，不读包列表。澜流不用这些字段，分应用由 VpnService 决定。上游这个函数变了脚本会停下
- 构建参数同官方发布版：`-tags with_gvisor -trimpath`，`constant.Version` 写版本号；用 NDK 的 `aarch64-linux-android29-clang`

升级内核时改脚本里的 `PINNED_VERSION`/`PINNED_H1`，并确认补丁仍然适用。

### 为什么排除自己、为什么是 gVisor

内核是应用的子进程，UID 相同。子进程不能调 `VpnService.protect()`（只有持有 `VpnService` 的进程能），所以用 `addDisallowedApplication(packageName)` 让整个应用（含内核）走物理网络，避免回环。代价：

- 应用自身的 Go 网络请求（订阅更新、出口 IP 探测等）不经 VPN；需要经代理的请求要显式走内核的 mixed 端口（现有的探测已经这么做）。
- Mihomo 的 `system` 栈（以及 `mixed` 的 TCP 部分）经内核协议栈回包，被排除 UID 的回包会走物理网卡而不是 TUN，所以强制 `gvisor`。

### 热重载

`vpnRunner` 实现了 `core.ConfigStager`：重载前要把新的 `runtime.yaml` 改写成 `android-runtime.yaml`，否则新配置里没有 `file-descriptor`，内核会关掉 TUN。服务层的 `reloadPath` 只在 `state.tunRunner` 为真时调用 `StageConfig`；手机上设置里 TUN 关着时目前不会走这条路，需要服务层在手机上总是调用（见 PR 说明，未在本分支修改 `internal/app`）。

## 构建

前提：Android SDK（API 36）、**JDK 17**（MyGo 的 Gradle 工程固定 `jvmToolchain(17)`，JDK 21/25 都不行，用 `JAVA_HOME` 指定）、NDK（MyGo 用它的 clang 编 Go，取 `$ANDROID_HOME/ndk/` 下最高版本或 `ANDROID_NDK_HOME`；本机验证用 29.0.14206865）。

```bash
scripts/build-core-android.sh
```

```bash
JAVA_HOME=<JDK 17> go tool mygo build -platform android/arm64
```

调试时 `go tool mygo dev -platform android -device emulator-5554` 装可调试版（包名 `com.lanflow.app.dev`），可以 `adb shell run-as com.lanflow.app.dev cat files/Lanflow/logs/mihomo.log` 看内核日志。

## 验证情况（模拟器 armadra-api36，arm64，Android 16 / API 36）

已验证：
- 导入订阅后弹出系统 VPN 授权框，授权后 `tun0` 建立（`198.18.0.1/30`、`fdfe:dcba:9876::1/126`、DNS `198.18.0.2`、MTU 9000、本应用 UID 被排除），状态栏有钥匙图标
- 内核经 `linker64` 从 APK 内启动，日志 `[TUN] Tun adapter listening at: tun0(fd=3) … ip stack: gVisor`；控制接口 unix socket 有响应，界面显示 "Running · Mihomo v1.19.34"
- VPN 内应用（`adb shell` 的 UID 2000）`nc example.com 80`：DNS 经 fake-ip 解析，TCP 经内核转到本机测试 HTTP 代理节点，节点记到 `CONNECT example.com:80`，客户端收到 HTTP 响应；直连 IP（`1.1.1.1:80`）同样经节点

未验证：
- 用户在系统设置里断开 VPN / 另一个 VPN 接管时的 `onRevoke` 路径
- 设置里 TUN 关着时的热重载（已知会丢 TUN，见上）
- 浏览器流量（模拟器 Chrome 首次启动要同意服务条款，未代为同意；改用 shell 的 `nc` 验证）
- 真机、x86_64、Android 10–15

## 已知限制

- 只打 arm64-v8a。
- 应用自身流量不经 VPN（见上）。
- geodata 不在 APK 里时，用到 GEOIP/GEOSITE 的规则会让内核首次启动时自行下载。
- "始终开启的 VPN"由系统拉起服务时，服务直接退出：内核只由应用启动。
- 首次授权弹框若停留过久，服务层的启动时限会先到，需再点一次启动。
