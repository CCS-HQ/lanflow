package settings

import (
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/hotkey"
)

// The settings part of the global search (⌘K, view/palette.go): every
// section and row of the eight tabs, by tab. Choosing one opens its tab
// and brings it into view (handleArg's "tab#label", env.Reveal).
//
// Labels and sections are the i18n keys the tabs draw; keywords are
// extra words to match, mostly the config keys the rows set, in neither
// language. TestSearchIndexMatchesTabs keeps the table in step with what
// the tabs draw.

// SearchEntry is one searchable section or row.
type SearchEntry struct {
	Tab      string // tab key (tabKeys)
	TabName  string // shown
	Section  string // shown; "" for a section itself
	Label    string // shown, as the tab draws it
	Match    string // what a query is matched against: both languages and keywords
	Sections bool   // the entry is a section heading
	// Reveal is what the jump brings into view: the label, or the
	// section of a row the tab draws only in some states.
	Reveal string
}

// Arg is the settings argument that opens the entry (handleArg).
func (e SearchEntry) Arg() string { return e.Tab + "#" + e.Reveal }

type searchRow struct {
	label    string // i18n key
	keywords string
	cond     bool // drawn only in some states: the jump reveals the section
}

type searchSection struct {
	tab   int
	title string // i18n key; "" = rows only, under the tab
	rows  []searchRow
}

func r(label string, keywords ...string) searchRow {
	return searchRow{label: label, keywords: strings.Join(keywords, " ")}
}

// rc is r for a row the tab draws only in some states (WebDAV set up,
// sync on, TUN on…).
func rc(label string, keywords ...string) searchRow {
	row := r(label, keywords...)
	row.cond = true
	return row
}

var searchSections = []searchSection{
	// 常规
	{tabGeneral, "常规", []searchRow{r("开机时启动", "login autostart"), rc("默认使用管理员启动", "admin administrator uac elevate windows"), r("启动后自动开启系统代理", "system proxy"), r("启动时不显示窗口", "silent tray"), r("关闭窗口时", "close tray"), r("外观", "dark light theme"), r("语言", "language english")}},
	{tabGeneral, "功能", []searchRow{r("抓包（HTTPS 解密）", "capture mitm https")}},
	{tabGeneral, "菜单栏显示", []searchRow{r("实时速度", "menubar speed"), r("流量图", "menubar graph"), r("当前节点", "menubar node"), r("代理模式", "menubar mode")}},
	{tabGeneral, "全局快捷键", []searchRow{r("添加快捷键", "hotkey shortcut")}},
	{tabGeneral, "数据", []searchRow{r("配置文件夹", "config folder directory"), r("导出 / 导入设置", "export import lanflow.yaml"), r("诊断信息", "diagnostics zip"), r("缓存", "cache")}},
	{tabGeneral, "配置同步", []searchRow{r("同步位置", "icloud sync"), rc("同步文件", "lanflow.yaml sync"), rc("用口令加密链接", "passphrase encrypt"), rc("同步口令", "passphrase")}},
	{tabGeneral, "备份与同步", []searchRow{r("WebDAV 网盘", "webdav"), rc("密码", "webdav password"), r("备份", "backup"), rc("保留份数", "backup keep"), rc("自动备份", "backup auto")}},
	{tabGeneral, "关于", []searchRow{r("应用更新", "update version"), rc("自动检查更新", "update"), r("在 GitHub 上点 Star", "github star repo"), r("反馈问题", "feedback bug report"), r("反馈上报", "feedback"), r("上报地址", "feedback url"), rc("访问令牌", "feedback token"), rc("出错时自动上报", "feedback crash")}},
	// 代理
	{tabProxy, "代理", []searchRow{r("代理端口", "mixed-port port http socks"), r("允许局域网连接", "allow-lan lan"), r("不走代理的地址", "bypass skip-proxy"), r("实测节点出口位置", "exit probe")}},
	{tabProxy, "延迟测试", []searchRow{r("测速地址", "delay url latency"), r("测速超时", "delay timeout latency"), r("切换节点时断开该组的连接", "close connections switch")}},
	{tabProxy, "按 Wi-Fi 切换订阅", []searchRow{r("其他网络", "wifi ssid subscription")}},
	// TUN
	{tabTUN, "TUN 模式", []searchRow{r("TUN 模式（增强模式）", "tun enhanced"), rc("TUN 协议栈", "tun stack gvisor system mixed"), rc("自动设置系统 DNS", "tun dns"), r("aTrust 兼容", "atrust vpn"), r("Tailscale 兼容", "tailscale tailnet magicdns")}},
	{tabTUN, "进阶 TUN 参数", []searchRow{r("MTU、严格路由与排除网段", "mtu strict-route")}},
	// DNS
	{tabDNS, "DNS", []searchRow{r("DNS 设置来源", "dns")}},
	{tabDNS, "解析", []searchRow{r("模式", "enhanced-mode fake-ip redir-host"), r("上游服务器", "nameserver doh dot"), r("节点域名解析", "proxy-server-nameserver"), r("直连域名解析", "direct-nameserver"), r("遵循分流规则", "respect-rules")}},
	{tabDNS, "按域名指定服务器", []searchRow{r("", "nameserver-policy")}},
	{tabDNS, "Fake-IP 与 Hosts", []searchRow{r("Fake-IP 排除", "fake-ip-filter"), r("使用系统 hosts", "use-system-hosts"), r("自定义 hosts", "hosts")}},
	// 嗅探
	{tabSniffer, "嗅探", []searchRow{r("嗅探设置来源", "sniffer"), r("启用嗅探", "sniffer sniff")}},
	{tabSniffer, "协议与端口", nil},
	{tabSniffer, "行为", []searchRow{r("用嗅探结果覆盖目标地址", "override-destination"), r("嗅探纯 IP 连接", "parse-pure-ip"), r("强制嗅探的域名", "force-domain"), r("跳过嗅探的域名", "skip-domain"), r("跳过嗅探的目标地址", "skip-dst-address")}},
	// 覆写
	{tabOverride, "覆写列表", nil},
	{tabOverride, "订阅使用的覆写", nil},
	{tabOverride, "合成结果", []searchRow{r("运行时配置", "runtime config yaml")}},
	// 内核
	{tabKernel, "内核版本", []searchRow{r("Mihomo 版本", "mihomo core version")}},
	{tabKernel, "内核更新", []searchRow{r("更新通道", "alpha channel"), r("自动安装更新", "core update")}},
	{tabKernel, "内核", []searchRow{r("内核文件", "mihomo binary"), r("日志级别", "log-level")}},
	{tabKernel, "外部资源", []searchRow{r("GEOIP 规则使用", "geodata-mode mmdb dat"), r("自动更新", "geo-auto-update"), r("上次更新", "geoip geosite")}},
	{tabKernel, "下载镜像", []searchRow{r("GitHub 镜像", "mirror github"), r("镜像测速", "mirror")}},
	// 高级
	{tabAdvanced, "端口与局域网", []searchRow{r("HTTP 端口", "port http"), r("SOCKS 端口", "socks-port socks"), r("允许的局域网地址", "lan-allowed-ips"), r("禁止的局域网地址", "lan-disallowed-ips"), r("代理认证", "authentication password"), r("免认证的地址", "skip-auth-prefixes")}},
	{tabAdvanced, "系统代理", []searchRow{r("PAC 模式", "pac"), r("国内域名直连", "pac china"), r("PAC 端口", "pac port")}},
	{tabAdvanced, "TUN", []searchRow{r("严格路由", "strict-route"), r("DNS 模式", "tun dns-mode"), r("MTU", "mtu"), r("网卡名称", "tun device"), r("端点无关 NAT", "endpoint-independent-nat"), r("只接管这些网段", "route-address"), r("不接管的网段", "route-exclude-address"), r("DNS 劫持", "dns-hijack"), r("只在这些网卡上接管", "include-interface"), r("不在这些网卡上接管", "exclude-interface")}},
	{tabAdvanced, "内核行为", []searchRow{r("统一延迟", "unified-delay"), r("TCP 并发", "tcp-concurrent"), r("进程匹配", "find-process-mode"), r("禁用 TCP 保活", "disable-keep-alive"), r("TCP 保活间隔", "keep-alive-interval"), r("TCP 保活空闲", "keep-alive-idle"), r("出站网卡", "interface-name"), r("下载 User-Agent", "global-ua"), r("地理数据加载", "geodata-loader"), r("禁用 ETag", "etag"), r("保存 Fake-IP 映射", "store-fake-ip")}},
	{tabAdvanced, "DNS 高级", []searchRow{r("本机 DNS 监听", "dns listen"), r("优先 HTTP/3", "prefer-h3"), r("缓存算法", "cache-algorithm arc lru"), r("引导服务器", "default-nameserver"), r("备用服务器", "fallback"), r("备用服务器 · 按 GeoIP 过滤", "fallback-filter geoip"), r("备用服务器 · 直连网段", "fallback-filter ipcidr"), r("备用服务器 · 域名", "fallback-filter domain")}},
	{tabAdvanced, "嗅探高级", []searchRow{r("不强制 DNS 映射", "force-dns-mapping")}},
	{tabAgent, "Agent 接入", []searchRow{r("MCP 配置", "mcp claude agent ctl"), r("已授权的 agent", "agent audit consent")}},
}

// SearchIndex returns the searchable sections and rows of the settings,
// in the language shown, tab by tab.
func SearchIndex() []SearchEntry {
	tabs := i18n.Options(tabNames...)
	var out []SearchEntry
	add := func(tab int, section, label, reveal, match string, isSection bool) {
		out = append(out, SearchEntry{Tab: tabKeys[tab], TabName: tabs[tab], Section: section, Label: label, Reveal: reveal,
			Match: strings.ToLower(match + " " + i18n.Both(tabNames[tab])), Sections: isSection})
	}
	for _, s := range searchSections {
		title := ""
		if s.title != "" {
			// A row without a label only adds words to its section.
			extra := ""
			for _, row := range s.rows {
				if row.label == "" {
					extra += " " + row.keywords
				}
			}
			title = i18n.T(s.title)
			add(s.tab, "", title, title, i18n.Both(s.title)+extra, true)
		}
		for _, row := range s.rows {
			if row.label == "" {
				continue
			}
			label := i18n.T(row.label)
			reveal := label
			if row.cond && title != "" {
				reveal = title
			}
			add(s.tab, title, label, reveal, i18n.Both(row.label)+" "+row.keywords, false)
		}
	}
	// One row per global shortcut action (hotkey.go).
	shortcuts := i18n.T("全局快捷键")
	for _, a := range model.ShortcutActions {
		// Only the actions with a key have a row: the jump shows the section.
		add(tabGeneral, shortcuts, hotkey.ActionName(a), shortcuts, hotkey.ActionName(a)+" "+i18n.Both("全局快捷键")+" hotkey shortcut", false)
	}
	return out
}

// openEntry handles "tab#label": the tab, and the label brought into
// view once the tab draws it.
func (p *Page) openEntry(a string) bool {
	tab, label, ok := strings.Cut(a, "#")
	if !ok {
		return false
	}
	for i, k := range tabKeys {
		if k == tab {
			p.requestTab(i)
			if label != "" {
				env.RequestReveal(label)
			}
			return true
		}
	}
	return false
}
