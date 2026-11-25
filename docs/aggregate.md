# 聚合订阅

把几个来源的节点合并成一个订阅，由澜流在本机生成完整的 Mihomo 配置：节点筛选与重命名、代理组和分流规则都在界面里勾选，不需要手写 YAML，也不依赖在线的订阅转换服务。思路参考了 [SubBoost](https://github.com/SubBoost/subboost) 和 Clash Party 集成的 [Sub-Store](https://github.com/sub-store-org/Sub-Store)，代码是澜流自己用 Go 写的（两者都是 AGPL-3.0，与澜流的 MIT 不兼容，没有复制任何代码）。

## 1. 用户看到的

- 订阅页「导入 ▾ › 新建聚合订阅…」（没有订阅时空状态里也有）打开编辑页，代替订阅表格显示；行菜单「编辑聚合…」重新打开。
- 聚合订阅在订阅表格里来源列显示「聚合」，和链接订阅一样可以启用、更新、自动更新、编辑覆写、查看配置；侧栏的当前订阅卡片可一键更新。
- 编辑页四个分栏：

| 分栏 | 内容 |
| --- | --- |
| 来源 | 名称、自动更新、User-Agent、通过代理更新、下载超时；新建时「保存后立即使用」。来源列表：订阅链接、节点链接（每行一个分享链接）、已有订阅；每个来源可停用、设名称前缀 |
| 节点处理 | 「保留」「排除」两个正则（按原始名称；排除默认去掉剩余流量、到期等信息条目）；有序的正则重命名规则（`$1` 引用分组） |
| 分组与规则 | 「自动选择」开关；地区分组（香港、台湾、日本、新加坡、美国、韩国、英国、德国）；分流模块（广告拦截、AI 服务、Telegram、YouTube、Netflix、Disney+、Spotify、TikTok、社交媒体、GitHub、谷歌、微软、苹果、游戏平台、国外网站、国内直连） |
| 预览 | 下载来源并生成配置但不保存：每个来源读到 / 保留 / 无法识别的数量或错误，警告，代理组，节点列表，生成的 YAML。设置改过后提示预览已过时 |

- 「保存」先下载来源、生成配置并用内核校验，通过后才写入；失败时编辑页保持打开并显示原因，原来的配置不变。

## 2. 生成规则

处理顺序：读取来源 → 按原始名称「保留」「排除」→ 重命名规则依次替换 → 加来源前缀 → 去重编号。

- **来源**
  - 订阅链接：用 `FetchRaw` 下载（不要求是 Clash 格式），内容可以是 Clash/Mihomo 配置（取 `proxies`；`proxy-providers` 里的节点并不进来，给出警告），也可以是分享链接列表，原文或 Base64。并行下载，使用订阅自己的 User-Agent、代理与超时设置。
  - 节点链接：用户粘贴的分享链接。解析复用手动节点的 `parseNodeURI` 与 `BuildManualProxyNode`，所以支持的协议与「添加节点 › 导入剪贴板」一致（ss、vmess、vless、trojan、trojan-go、hysteria、hysteria2、tuic、anytls、socks、http）；不认识的行计入「无法识别」。
  - 已有订阅：读取那个订阅上次下载的内容；不能引用另一个聚合订阅。
  - YAML 节点里的锚点、别名和 `<<` 合并键会展开成独立的映射，再放进新文档。
- **命名**：名称为空时用协议名；与生成的组名、`DIRECT` / `REJECT` 等内置策略或彼此重名时追加 ` 2`、` 3`……；`LANFLOW-` 前缀是澜流保留的（探测、抓包、链式代理），会被去掉。最多 5000 个节点。
- **代理组**（顺序即配置顺序）：
  1. `节点选择`（select）：自动选择、各地区组、全部节点、DIRECT。它是第一个选择组，所以手动节点和链式代理会接在它下面，代理页把它当作主组。
  2. `自动选择`（url-test，全部节点），开启时生成。
  3. 每个勾选的分流模块一个 select 组：代理类以 `节点选择` 开头，微软 / 苹果 / 游戏以 `DIRECT` 开头，广告拦截以 `REJECT` 开头。「国外网站」「国内直连」不生成组，规则直接指向 `节点选择` / `DIRECT`。
  4. `漏网之鱼`（select：节点选择、DIRECT、自动选择、地区组）。
  5. 地区组（url-test），按名称正则匹配（`aggRegionMatch`）；没有节点的地区不生成。
  - url-test 统一用 `https://www.gstatic.com/generate_204`、300 秒、容差 50、`lazy: true`。
- **规则**：私有地址与局域网（`GEOSITE,private` 和私有网段）始终直连 → 勾选的模块按目录顺序 → `MATCH,漏网之鱼`。规则只用 `GEOSITE` / `GEOIP,CN` 与固定网段（Telegram），读的是澜流自带的 `geosite.dat` / `geoip.metadb`，启动内核不需要下载规则集；`geoip.metadb` 只有国家代码，所以 Telegram 用 IP 段而不是 `GEOIP,telegram`。
- **用量**：所有订阅链接来源的 `subscription-userinfo` 相加，到期取最早的一个。
- 生成的配置只含 `proxies`、`proxy-groups`、`rules`；DNS、嗅探、端口等仍由澜流的设置与覆写决定（合成顺序不变，见 [advanced.md](advanced.md)）。

## 3. 更新与失败

- 「更新」「全部更新」和自动更新都会重新下载来源并重新生成；「全部更新」先更新链接订阅，再更新聚合订阅，所以引用已有订阅的来源能拿到刚更新的内容。
- 部分来源失败：照常更新，失败的来源这次不并入，订阅行显示 ⚠，悬停可见「已更新，但有 N 个来源失败：…」。
- 全部失败或没有剩下节点：更新失败，继续使用上次的配置。
- 生成的配置没有通过内核校验时与普通订阅相同：弹出「配置无法使用」对话框，旧配置保留。

## 4. 存储、同步与 agent

- 聚合设置存在 `profiles/<id>.aggregate.json`（`model.AggregateSpec`），生成的配置照常是 `profiles/<id>.yaml`；删除订阅时一起删除。
- 聚合订阅和从文件导入的订阅一样**只在本机**，不写进 `lanflow.yaml`；同步时保留在本机。
- 接口：`AggregateSpec`、`PreviewAggregate`（敏感读取）、`SaveAggregate`（写入）。返回给 agent 的聚合设置会去掉来源链接（`redactAggregate`）；agent 提交时没带链接的来源按 ID 用已保存的链接补上（`keepAggregateLinks`），与 `EditSubscription` 的订阅链接相同。

## 5. 代码

| 位置 | 内容 |
| --- | --- |
| `internal/model/aggregate.go` | `AggregateSpec`、来源、重命名规则、预览结果 |
| `internal/profile/aggregate.go` | 校验、来源解析、筛选重命名、组与规则生成（纯函数 `BuildAggregate`） |
| `internal/profile/aggregate_catalog.go` | 地区、分流模块与提示文字的目录（界面经 i18n 翻译） |
| `internal/app/aggregate.go` | 下载来源、保存、预览；`subs.go` 的更新与自动更新 |
| `internal/view/subs/aggregate.go` | 编辑页 |

## 6. 暂不做

- 通过局域网向其他设备提供聚合后的订阅地址。
- 并入来源里 `proxy-providers` 的节点（需要下载提供方，或生成派生提供方）。
- 远程规则集（rule-providers）与自定义分流模块；目前可用自定义规则和覆写补充。
- 配置同步。
