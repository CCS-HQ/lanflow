# Tailscale

澜流有两种方式与 tailnet 打交道，二选一：

| | Tailscale 兼容 | 内置 Tailscale |
|---|---|---|
| 需要 Tailscale App | 是（在澜流旁边运行） | 否 |
| 谁连 tailnet | Tailscale App（系统网卡 utunN） | 澜流的内核（Mihomo `type: tailscale` 出站，tsnet） |
| 哪些流量能到 tailnet | 本机所有流量 | 只有经过澜流的流量：TUN、系统代理、设置了代理的应用 |
| 别的设备访问本机服务（SSH、文件共享） | 可以 | 不可以：本机不是 tailnet 节点 |
| Taildrop、Tailscale SSH | 可用 | 不可用 |
| 退出澜流 | tailnet 不受影响 | tailnet 断开 |
| 设置项 | `TailscaleCompat`（默认开） | `TailscaleBuiltin`（默认关，只在本机，不参与配置同步） |

两者共用隐藏出站名 `LANFLOW-TAILSCALE`（`profile.TailscaleOutbound`）和用户的补充列表
`Tailscale.Domains / Routes / DNS`（「设置 › TUN › Tailscale 兼容 › 配置…」），所以为它写的自定义规则在两种模式下都有效。

**互斥**：开启内置 Tailscale 后，检测（每 10 秒，窗口隐藏时 30 秒）发现 Tailscale App 在运行，内置节点就不写入配置，状态显示「已暂停」并发通知；`TailscaleCompat` 开着时改由兼容模式接管。Tailscale App 退出（连续 3 次检测不到）后内置节点自动恢复。

## 内置 Tailscale

### 合成（`internal/profile/tailscale.go`，`internal/app/tailscale_builtin.go`）

```yaml
proxies:
  - name: LANFLOW-TAILSCALE
    type: tailscale
    udp: true
    hostname: <设置的设备名，或「本机名-lanflow」>
    auth-key: <钥匙串里的 auth key，有才写>
    control-url: <Headscale 地址，有才写>
    state-dir: tailscale            # 退出登录后为 tailscale-N
    accept-routes: false            # 总是写出，节点会记住上次的偏好
    exit-node: <有才写>
rules:                              # 置顶
  - DOMAIN-SUFFIX,ts.net,LANFLOW-TAILSCALE
  - DOMAIN-SUFFIX,<Tailscale.Domains>,LANFLOW-TAILSCALE
  - IP-CIDR,100.64.0.0/10,LANFLOW-TAILSCALE,no-resolve
  - IP-CIDR6,fd7a:115c:a1e0::/48,LANFLOW-TAILSCALE,no-resolve
  - IP-CIDR,<Tailscale.Routes>,LANFLOW-TAILSCALE,no-resolve
dns:
  fake-ip-filter: [+.ts.net, ...]
  nameserver-policy:                # 置顶
    "+.ts.net": [tailscale://LANFLOW-TAILSCALE]
    "+.<Tailscale.Domains>": [<Tailscale.DNS>#LANFLOW-TAILSCALE]   # 未填 DNS 时用 tailscale://
sniffer:
  skip-domain: [+.ts.net, ...]
```

- 出站从 include-all / filter 组中排除（`exclude-filter`），也不出现在节点列表里。
- 网段**不**写入 `route-exclude-address`：系统里没有 Tailscale 网卡，只有内核能到 tailnet，必须留在 TUN 里；系统代理例外列表也不加 tailnet。
- `ts.net` 有规则：经系统代理来的 MagicDNS 域名否则会落到 MATCH 去远端节点。代价是别的 tailnet 的 Funnel 地址（`*.ts.net` 公网 IP）经系统代理访问会失败；TUN 下 ts.net 不分配 fake-ip，按真实 IP 匹配，不受影响。
- Headscale 的 MagicDNS 后缀不是 ts.net 时，请把它加入 `Tailscale.Domains`。
- 子网路由：开启「接受子网路由」后，要经 tailnet 访问的网段仍需写进 `Tailscale.Routes`，规则才会交给内置节点。
- 出口节点只对自定义规则里指向 `LANFLOW-TAILSCALE` 的流量生效（例如 `MATCH,LANFLOW-TAILSCALE`）。

### 登录与状态

Mihomo 懒启动 tsnet：第一条连接或 DNS 查询到达出站时才启动。因此每次内核启动 / 重载后澜流用延迟测试探一次
（`ProxyDelay(LANFLOW-TAILSCALE, http://100.100.100.100/)`）让它启动。

状态只能从内核日志读到，澜流为此单独开一条日志流（`runTailscaleLogs`，与「日志」页的级别无关）：

| 日志（前缀 `[Tailscale](LANFLOW-TAILSCALE) `） | 级别 | 状态 |
|---|---|---|
| `To start this tsnet server, … go to: <URL>` | info | 未登录，显示登录链接并通知一次 |
| `AuthLoop: state is <State>; done` | info | Starting / Running / NeedsMachineAuth |
| `Switching ipn state A -> B` | debug | 状态变化 |
| `peerapi: serving on http://<IP>:<port>` | debug | tailnet 地址 |
| `active login: <account>` | debug | 登录账号 |
| 该前缀的 warning / error | | 错误 |

每次（重新）启动后的 3 分钟内（或直到读到地址）这条流用 debug 级别，之后改回 info。
状态在 `Status.Tailscale.Builtin`，agent 经 `TailscaleReport`（MCP `tailscale_report`）读到状态、登录链接、地址和需要处理的事项。

没有 auth key 时用户点「登录…」在浏览器完成登录；auth key 由用户在「内置 Tailscale › 配置…」填写，存系统钥匙串
（`tailscale-auth-key`，没有钥匙串时为数据目录下混淆的 `tailscale-auth-key.secret`），合成时写入运行配置。
`SetTailscaleAuthKey` 只供窗口调用（`LevelUI`），agent 不能设置凭据。已登录的节点会忽略 auth key。

「退出登录…」把 `StateGen` 加一：节点换用新的状态目录 `tailscale-N` 重新登录，用户数据目录里的旧目录被删除；
原设备留在 tailnet 管理后台，需要时在那里删除。

### 状态目录与权限组件

`state-dir` 是相对内核工作目录的路径（Mihomo 拒绝工作目录以外的路径）：

- 以 TUN 关闭状态启动：内核以用户身份运行，状态在 `<数据目录>/core/tailscale/`。
- 开 TUN：内核由权限组件以 root 运行，工作目录是权限组件自己的目录
  （macOS `/Library/Application Support/Lanflow Helper/work`），状态在那里的 `tailscale/`，用户进程无法读取。
- 热切换关闭 TUN 后：现有内核、工作目录和状态目录保持不变，直到停止或重启内核。

为了让两种运行方式是同一台 tailnet 设备，权限组件（0.1.9 起）在每次启动内核前：

- 把用户工作目录里的 `tailscale*/tailscaled.state` 复制过去（缺失或用户的更新时；只在启动时，重载时运行中的内核拥有它）。不开 TUN 时登录过，开 TUN 后不用再登录。
- 清理旧文件时保留用户目录里也存在的 `tailscale*` 目录（节点密钥很少变化，30 天未改也不能删）；用户目录里已没有的（退出登录留下的）照常清理。

不会复制回去：只在 TUN 下首次登录时，关掉 TUN 并重启为用户身份的内核后需要再登录一次，此后两边一致（用户目录较新的状态会覆盖权限组件的那份）。单独关闭 TUN 的热切换仍使用原状态目录。

## Tailscale 兼容

见 `internal/app/tailscale.go` 的包注释：检测 Tailscale 网卡，tailnet 网段写入 `route-exclude-address`
（含系统 DNS 的留在 TUN），隐藏的 direct 出站 `LANFLOW-TAILSCALE` 绑定 Tailscale 网卡，MagicDNS 经它解析，系统代理绕过 tailnet。
