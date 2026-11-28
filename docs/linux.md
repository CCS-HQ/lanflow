# Linux 版

澜流的 Linux 版用 MyGo 的 Linux 后端：界面用 GTK 3，运行时才加载，构建时不需要 cgo，所以在 macOS 上也能交叉编译出 Linux 包。目前先支持 x64 和 arm64，优先照顾 Ubuntu / GNOME。

## 安装包

`scripts/prepare-resources.sh linux <arch>` 和 `scripts/build-app.sh linux <arch>` 会在 `build/linux-<arch>/` 下生成以下文件：

| 文件 | 安装位置 | 更新 |
| --- | --- | --- |
| `lanflow_<版本>_<arch>.deb` | `/opt/lanflow`，命令 `/usr/bin/lanflow` | 应用内不更新，新版本重新安装 |
| `lanflow-<版本>-1.<arch>.rpm` | 同上 | 同上 |
| `lanflow-<版本>-linux-<arch>.tar.gz` + `install.sh` | 当前用户的 `~/.local/lanflow.app`，命令 `~/.local/bin/lanflow`，不需要 root | 应用内自动更新（`update-linux-<arch>.json`） |

`install.sh` 按 `uname -m` 选择架构，从最新 Release 下载对应的归档并校验签名。所有架构共用一份脚本，Release 上只放一个：

```sh
curl -fsSL https://github.com/Owlbay/lanflow/releases/latest/download/install.sh | sh
sh install.sh --uninstall
```

`mygo.json` 的 `linux` 段说明如下：
- 包依赖 GTK 3 和 `libayatana-appindicator3-1`（托盘需要）。
- 界面是原生 UI，不用 WebKitGTK（`webview: false`）。
- 桌面入口归到「网络」类。
- 安装后提供 `lanflow` 命令，`lanflow ctl …` 也可以直接用。

## 第一期支持的功能

- **托盘**：用 AppIndicator 显示。GNOME 需要 AppIndicator 扩展（Ubuntu 默认已装）。单击弹出菜单，双击打开窗口。菜单里不显示快捷键。macOS 菜单栏上的流量图和文字在 Linux 上不显示。
- **系统代理**：写入 GNOME 的 `org.gnome.system.proxy`，用 `gsettings`，见 `internal/platform/sysproxy/gnome.go`。GNOME、Cinnamon、Budgie、MATE 以及读这套设置的应用（GLib、Chromium、Firefox 等）都会生效。PAC 模式写入 `autoconfig-url`。GNOME 用同一个 `mode` 在「手动」和「自动」之间切换，所以手动代理和 PAC 不能同时开着：关闭系统代理时只关「手动」，关闭 PAC 时只关「自动」。开启前会记下原来的设置，关闭时还原。找不到 `gsettings` 或 GNOME 的设置项，或者 dconf 不可用（GLib 退回内存后端，写了也不会保存）时，会明确报错，不会假装成功。
- **开机启动**：写 XDG autostart 的 `.desktop` 文件。开发构建不写。
- **控制接口**：Unix 套接字，按 `SO_PEERCRED` 识别对端进程。

## 还不支持的功能（第二期）

- **TUN 模式**：需要权限组件以 systemd 服务的形式运行，安装时用 pkexec 请求管理员权限。Linux 上现在没有权限组件，TUN 开关不可用，界面会提示「Linux 版暂不支持」。
- **系统 DNS**：Ubuntu 上 systemd-resolved 会把查询转给局域网 DNS。等 TUN 能用以后，需要用 `resolvectl` 或 NetworkManager 把 DNS 引到澜流。
- **网络信息**：读取网关、DNS、Wi-Fi 名称（NetworkManager），以及物理网卡绑定（`SO_BINDTODEVICE`，出口 IP 检测会用到）。
- **KDE 的系统代理**：要用 `kwriteconfig`。现在只写 GNOME 的设置，KDE 应用不会读。
- **抓包用的根证书**：要用 `update-ca-certificates` 和 NSS 的 `certutil` 安装，现在不能安装。

## 验证情况

第一期只验证了能编译（本机交叉编译 arm64 包，CI 在 Ubuntu 上跑全部测试，发布流程构建两个架构），还没有在真实的 Linux 桌面上运行过。下一步在 Parallels 里建 Ubuntu 24.04 arm64 虚拟机，测试托盘、窗口、系统代理和安装、更新流程。
