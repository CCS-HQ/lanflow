# 链式代理

让用户在澜流里按顺序选几个节点（入口 → … → 落地），合成一条「链路」，像普通节点一样被选用、测速和探测出口；不需要手写 YAML。

## 1. 现状

实现之前的情况（实现见第 6 节）：

| 部分 | 状态 |
| --- | --- |
| 运行 | 订阅或覆写里写的 `dialer-proxy` 原样交给内核；指向抓包解密出站的会被移除（`profile/capture.go`） |
| 识别与显示 | 按连接的 `chains` 计跳数，标「链式代理 · N 跳」（`app/exits.go`、`locate/analyze.go`、网络页、出口地图）。**对 `dialer-proxy` 链路无效**，见下 |
| 配置界面 | 没有；只能在覆写里手写 |

**内核约束（v1.19.33 / Alpha 源码核对）**：

- `relay` 类型的代理组**已被 Mihomo 移除**，加载时报错 `The group [...] with relay type was removed, please using dialer-proxy instead`（`adapter/outboundgroup/parser.go`）。`model.GroupRelay` 与「链式代理」组名只对旧内核有意义。新功能只能用 `dialer-proxy`。
- `dialer-proxy` 写在**节点**上（`adapter/outbound/base.go`），值是节点名或代理组名；写在代理组上无效。
- 加载时 `validateDialerProxies`（`config/utils.go`）只在配置级的 `proxies` 与代理组里查找目标，运行时 `proxydialer/byname.go` 查的是同一张表：**订阅提供方里的节点不能被 `dialer-proxy` 直接引用**，否则整份配置加载失败（`dialer-proxy [X] not found`）。
- 环路检测只沿「节点 → `dialer-proxy`」的边走，不展开代理组；运行时也没有防护。
- `proxy-providers` 可以整体设 `dialer-proxy`，或经 `override.dialer-proxy` 设置，作用于该提供方的全部节点（再经 `filter` 收窄）。
- 前置跳**不出现在外层连接的 `chains` 里**：`proxydialer` 把它作为一条独立的 `INNER` 连接登记（`component/proxydialer/proxydialer.go`）。所以现有的 `chainHops` 对 `dialer-proxy` 链路只得到 1，前置跳在连接页里显示为额外的连接，流量按跳各算一次。

## 2. Clash Verge Rev 的做法（参考）

`src/components/proxy/proxy-chain.tsx` + `src-tauri/src/config/runtime.rs`：

1. 代理页切到「链式代理」模式，按顺序选节点 A、B、C。
2. 后端在**运行时配置**里先删掉所有节点的 `dialer-proxy`，再写 B 的前置为 A、C 的前置为 B，然后重载。
3. 把当前代理组（全局模式下是 GLOBAL）切到 C；所选组和落地节点存在前端 localStorage。
4. 断开：删掉全部 `dialer-proxy`，代理组切到 DIRECT 再切回原节点，再关掉所有连接。

不足：直接改原节点，C 在别的组里也会走链路；一键清空会冲掉订阅自带的 `dialer-proxy`；只能改 `proxies` 里的节点，订阅提供方的节点做不了；链路只活在运行时配置和 localStorage，不进同步。

## 3. 设计

### 3.1 原则

- **不改原节点**。链路由澜流在合成时生成「克隆节点」承载，原节点、订阅自带的 `dialer-proxy` 一律不动。
- 链路是**用户配置**，和手动节点一样存储、进 `lanflow.yaml` 同步，换订阅、重启后仍在。
- 某条链路失效（订阅更新后节点没了）只让这条链路停用并提示，**不让整份配置加载失败**——包括引用它的规则（见 3.3「占位」）。

### 3.2 数据

```go
// model.ProxyChain：一条链路。Hops 按顺序为入口 → … → 落地，按名称引用。
type ProxyChain struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`    // 显示名，也是生成的链路组的名字
	Hops    []string `json:"hops"`    // 2–5 个
	Enabled bool     `json:"enabled"`
}
```

存放在 `profile.Store`，和 `ManualNodes` 并列（`ProxyChains()` / `SetProxyChains()`）；`syncfile` 增加 `proxyChains`，只有名称，不含任何秘密。`Input` 增加 `ProxyChains []model.ProxyChain`。

### 3.3 合成

新增 `profile/chains.go`，在手动节点层之后、自定义规则之前执行（自定义规则可以把链路当作目标），层名「链式代理」，生成内容在运行时配置视图里按层标注来源。

对链路 `[H1, H2, …, Hk]`：

| 跳 | 允许的类型 | 生成 |
| --- | --- | --- |
| H1（入口） | `proxies` 里的节点，或代理组（组里可以是订阅提供方的节点） | 不生成，直接被引用 |
| H2 … Hk（中间与落地） | `proxies` 里的节点（订阅内联或手动节点），不能是代理组 | 克隆为节点 `LANFLOW-CHAIN-<id>-<i>`，`dialer-proxy` 指向前一跳（H2 指向 H1，其余指向前一个克隆） |

**命名与对外形态**

- 克隆一律用保留前缀 `LANFLOW-CHAIN-`。对外只暴露一个以链路 `Name` 命名的 `select` 组，成员只有最后一个克隆；规则、上层组、`store-selected` 都引用这个组。这样克隆名不随改名变化，规则写的是组名。
- 所有链路组放进保留名「链式代理」的 `select` 组；订阅里已有同名但不是 `select` 的组时，改用「链式代理（澜流）」。这个组像手动节点组一样追加到第一个可见的选择组。没有启用的链路时不生成空组（空组会让内核报 `errMissProxy`）。
- Mihomo 的节点没有 `hidden`。克隆是 `proxies` 里的普通节点，会进入 GLOBAL、`LANFLOW-PROBE` 和所有 `include-all` / `include-all-proxies` 组，`url-test` / `fallback` 可能自动选中它们；`exclude-filter` 对内联节点不生效。合成时处理：
  - 对 `include-all*` 组把成员展开为显式列表并去掉保留前缀（或改写它们的 `filter` 加上 `^(?!LANFLOW-CHAIN-)`，二选一，以实现简单为准，需有测试）；
  - GLOBAL 与 `LANFLOW-PROBE` 同样排除克隆，探测改为探测链路组；
  - 界面（代理页、连接页、出口地图）隐藏保留前缀的名字，在链路组下展示跳的列表。

**校验**（不合格的链路停用并警告，不中断合成）：

- 跳数 2–5；同一名称不得出现两次；中间与落地不得是代理组；任何一跳都不能带 `LANFLOW-` 前缀（包括抓包解密出站 `LANFLOW-MITM-*`），因为 `scrubCaptureRefs` 会去掉指向解密出站的 `dialer-proxy`，链路会悄悄变成直连。
- 每一跳必须在本次合成结果里存在；中间与落地必须在 `proxies` 里。
- **环路**：入口是代理组时，合成时传递展开它的成员（含嵌套组、`include-all*`、提供方无法展开的部分按「可能包含」处理），只要可能包含链路组或「链式代理」组本身就拒绝。例如入口不能是那个追加了「链式代理」的第一个选择组。
- `Name` 不得与现有节点、代理组重名，不得用 `LANFLOW-` 前缀。
- 中间与落地是 WireGuard 节点时警告：克隆与原节点共用私钥，同时在用会互相顶掉会话。
- 原节点自带 `dialer-proxy` 时，克隆上的值被覆盖并警告。

**占位**：停用的链路如果被自定义规则或上层组引用，仍生成同名 `select` 组，成员只有 `REJECT`，并在警告里说明「链路 X 已停用，命中它的流量被拒绝」。这样规则不会因为目标不存在让整份配置加载失败；拒绝而不是放行，避免静默改走直连。

**UDP**：是否能走 UDP 由每一跳自己的传输决定。某一跳的传输本身基于 UDP（hysteria2、tuic、wireguard，或要转发 UDP 的 shadowsocks）时，**它的前一跳**必须支持 UDP，而不只是入口。中间与落地在合成时已知，可以静态检查并在编辑界面提示；入口是代理组时只能提示「取决于组当前选择的节点」。

### 3.4 界面

**代理页**

- 顶部「⋯」菜单加「链式代理…」；有链路时，「链式代理」组照常显示在组列表里，可选择、测速、定位。
- 节点右键菜单加「以此为落地新建链路」「以此为入口新建链路」（订阅提供方的节点只出现前者的入口版本，并提示需要先放进一个代理组）。

**链路编辑弹层**

```text
名称  [ 家宽中转 · 东京 ]
入口  [ 香港自动 ▾ ]           ← 节点或代理组
中间  [ + 添加一跳 ]
落地  [ 东京 VPS（手动）▾ ]    ← 只列 proxies 里的节点
      你 → 香港自动 → 东京 VPS → 目标网站
[测试延迟]           [取消] [保存]
```

- 跳的列表可拖动排序；选项按 3.3 的规则过滤，不可选的灰显并说明原因。
- 「测试延迟」先保存为临时链路并重载，再对链路组测速；取消时恢复。
- 保存后走正常的「重新合成 → 热重载」流程。

**改名与删除**

- 改名：链路组名随之改变。重载前把自定义规则里引用旧名的目标改成新名；重载后，原先选中旧名的组按链路 ID 重新选中新名（`store-selected` 记的是名字，不改会丢）。
- 删除或停用：被引用的部分按 3.3「占位」处理；原先选中它的组在重载后会退到组的第一个成员，澜流在删除前记下这些组，重载后改选到用户原来的上一个选择（没有记录时保持内核的结果），并提示。

**出口、网络与连接页**

- 跳数：澜流生成的链路直接取链路定义；订阅自带的 `dialer-proxy` 链路从运行时配置沿 `dialer-proxy` 计算，替换现在按 `chains` 计数的做法（后者对 `dialer-proxy` 永远是 1）。
- 连接页：`INNER` 类型的连接是前置跳，默认折叠到它所服务的外层连接下或单独标注「链路前置」；流量统计和托盘网速不重复计算。

### 3.5 ctl / MCP

- 新增 `proxy_chains` / `set_proxy_chains`（接口方法 `ProxyChains` / `SetProxyChains`，工具名按方法名生成，与 `manual_nodes`、`custom_rules` 一致；`lanflow ctl chains list|set <文件|->`），写入记入 `audit.log`，`undo` 可撤销（连同改名时改写的自定义规则）。
- 风险等级与手动节点相同：会改变流量路径，每个连接确认一次。

### 3.6 暂不做

- **订阅提供方的节点做中间跳或落地**：提供方节点由内核运行时下载，合成时拿不到映射，无法克隆。可行做法是再生成一个派生提供方（同一 `url`、独立 `path`，`filter` 只留目标节点，`override.dialer-proxy` 指向前一跳，`override.additional-prefix` 改名），代价是同一订阅多下载一次、缓存文件翻倍。先观察需求再做。
- Clash Verge Rev 式的「临时改原节点」模式。

## 4. 实现步骤

1. `model.ProxyChain`、`profile.Store` 存取、`syncfile` 与 schema（`UPDATE_SCHEMA=1`）。
2. `profile/chains.go` + 合成测试：克隆与命名、`dialer-proxy` 指向、链路组与「链式代理」组（含重名的非 `select` 组、无链路时不生成）、`include-all*` / GLOBAL / `LANFLOW-PROBE` 排除克隆、缺失跳 / 重复跳 / 中间跳为组 / 提供方节点做落地、入口组传递包含链路组的环路、停用链路的 `REJECT` 占位、`LANFLOW-` 前缀拒绝、WireGuard 警告、覆写层自带 `dialer-proxy` 不受影响；最后用内核 `-t` 校验生成的配置。
3. 服务层接口（改名改写规则、按 ID 重新选中、删除前记录选择）、ctl / MCP、审计与撤销。
4. 跳数改为读配置（`app/exits.go`），连接页处理 `INNER` 连接。
5. 代理页入口、编辑弹层、右键菜单、英文文案。
6. 清理：`model.GroupRelay` 保留以兼容旧内核，但新建入口不再提及 `relay` 组；`docs/exit-map.md` 里「`relay` 组」与「`chains` 能列出每一跳」改为按 `dialer-proxy` 配置计算。

## 5. 验证（虚拟机）

- 两个本机 SOCKS5 服务做 A、B，链路 A → B，确认 B 的服务端看到的来源是 A；落地出口探测显示「链式代理 · 2 跳」。
- 入口为代理组（组内是订阅提供方的节点），切换组的选择后链路随之变化。
- 删除订阅里的入口节点后重新更新订阅：链路停用并提示，引用它的规则命中 `REJECT`，其余配置正常运行。
- 改名、删除正在使用的链路后，所在组的选择符合 3.4。
- 带 `url-test` + `include-all` 的订阅：克隆不出现在组里，不会被自动选中。
- TUN 开启时 UDP（DNS over QUIC、hysteria2 落地）经链路可用；入口不支持 UDP 时得到明确提示。

### 验证记录（2026-11-24，Parallels macOS 虚拟机，内核 v1.19.33，TUN 开启）

测试代理在宿主机上，只绑定在虚拟机网关地址：A 是测试代理组里的 SOCKS5，B 是另一个独立的 mihomo（SOCKS5 + hysteria2），两者不能在同一个进程里，否则内核的回环检测会拒绝 A 转给 B 的连接。

| 项 | 结果 |
| --- | --- |
| A → B | 通过。A 看到的是虚拟机连向 B，B 看到的来源是 A 所在的宿主机地址；出口探测为「链式代理 · 2 跳」（`ChainHops: 2`） |
| 入口为代理组（组内是订阅提供方的节点） | 通过。切换组的选择后，前置跳的 `INNER` 连接从 vmess 换到 vless |
| 删掉订阅里的入口后更新 | 通过。引用它的自定义规则命中 `A到B[REJECT]`，未被引用的链路不生成，其余配置正常运行，并弹出提示 |
| 改名、删除正在使用的链路 | 通过。改名后自定义规则目标与组选择随之改名；被规则引用时删除被拒绝；删除后内核退到第一个成员，澜流改回之前的选择并提示 |
| `url-test` + `include-all` | 通过。克隆节点不在 `url-test` 组、GLOBAL 与 `LANFLOW-PROBE` 里 |
| UDP | 通过。hysteria2 落地、SOCKS5（UDP）入口的链路可测速；TUN 下的 UDP 流量经链路从 B 发出。UDP 的回包未能验证：这个网络环境里直连的 NTP 也不通 |

验证中发现并已修正：链路列表的英文标题显示成了旧的「Relay」；合成产生的链路警告在英文界面里仍是中文。

## 6. 实现说明

| 部分 | 位置 |
| --- | --- |
| 数据与存储 | `model/proxy_chain.go`；`profile.Store.ProxyChains / SetProxyChains`（`proxy-chains.json`）；`syncfile` 的 `proxyChains` |
| 合成 | `profile/chains.go`：`addProxyChains`（手动节点之后、自定义规则之前）、`excludeChainClones`（所有组就位之后） |
| 服务 | `app/chains.go`：改名、删除、停用时的规则与选择；合成警告变化时提示 |
| 跳数与流量 | `app/exits.go` 的 `chainHops` / `dialersOf`；`app/streams.go` 扣除 `INNER` 连接 |
| 界面 | `view/proxies/chains.go`；连接页与出口卡片用 `profile.DisplayName` 显示克隆对应的原节点 |

与第 3 节的取舍：

- **排除克隆**：`include-all` / `include-all-proxies` 组改写 `filter`（没有时设为 `^(?!LANFLOW-CHAIN-)`，已有的每个正则 F 改为 `^(?!LANFLOW-CHAIN-)(?=.*?(?:F))`，匹配结果不变，只去掉克隆）。配置没有自己的 `GLOBAL` 时，写出一个成员与内核自动生成的相同（DIRECT、REJECT、节点、组，按配置顺序）但不含克隆的 `GLOBAL`；配置自带的 `GLOBAL` 不改。`LANFLOW-PROBE` 同样改写 `filter`，并加入各链路组：探测链路落地时选的是链路组。
- **删除被自定义规则引用的链路**：拒绝并提示先改规则或改为停用，而不是删除后占位——删除后合成时已不知道这个名字。停用的链路仍按 3.3 占位。订阅或覆写里引用被删链路的规则不在此列，交给内核校验。
- **删除前的选择**：服务在本次运行内记下每个组的上一个选择；删除或停用正在使用的链路后，内核退到组的第一个成员时改选回去并提示。重启后没有记录，保持内核的结果。
- **编辑弹层**：跳的顺序用「上移 / 下移」调整，没有拖动；不可选的入口与节点不出现在下拉列表里（规则写在弹层的说明里），而不是灰显。「测试延迟」只对已保存并生效的链路可用，不做临时链路。
- **连接页**：`INNER` 连接单独标为「链路前置」，不折叠到外层连接下（内核不记录二者的对应关系）；它们不计入流量统计和出口路由，内核的总量与 `/traffic` 网速扣除它们，托盘网速随之只算一次。
- **同步**：`lanflow.yaml` 总是写出 `proxyChains`；读到没有这个键的文件（旧版本写的）时保留本机链路，`proxyChains: []` 才清空。
