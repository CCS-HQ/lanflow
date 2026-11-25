package profile

// The catalogs of an aggregate subscription's editor (docs/aggregate.md).
// Their group names are part of the generated profile and also the
// editor's labels, which it shows through i18n.T.

// AggregateRegion is a region group the editor offers.
type AggregateRegion struct {
	ID    string
	Group string // group name in the profile, also the editor's label
}

// AggregateRegions are the region groups, in the order they are generated;
// the node names each matches are in aggRegionMatch (aggregate.go).
var AggregateRegions = []AggregateRegion{
	{"hk", "香港节点"},
	{"tw", "台湾节点"},
	{"jp", "日本节点"},
	{"sg", "新加坡节点"},
	{"us", "美国节点"},
	{"kr", "韩国节点"},
	{"gb", "英国节点"},
	{"de", "德国节点"},
}

// Module policies: what a module's own group selects first.
const (
	aggProxyFirst  = "proxy"
	aggDirectFirst = "direct"
	aggRejectFirst = "reject"
	aggNoGroup     = "" // the rules target a fixed policy, no group
)

// AggregateModule is a routing module the editor offers.
type AggregateModule struct {
	ID     string
	Group  string // group name, also the editor's label
	Policy string // aggProxyFirst … ; aggNoGroup: Target is used
	Target string // with aggNoGroup: DIRECT, or AggGroupSelect
	// Rules are rule lines without their target ("GEOSITE,youtube"); a
	// trailing ",no-resolve" is kept after the target.
	Rules []string
}

// AggregateModules are the routing modules, in rule order. The rules use
// the GEOSITE / GEOIP databases shipped with Lanflow, so starting the
// core never waits for a rule download.
var AggregateModules = []AggregateModule{
	{ID: "ads", Group: "广告拦截", Policy: aggRejectFirst, Rules: []string{"GEOSITE,category-ads-all"}},
	{ID: "ai", Group: "AI 服务", Policy: aggProxyFirst, Rules: []string{"GEOSITE,category-ai-!cn"}},
	{ID: "telegram", Group: "Telegram", Policy: aggProxyFirst, Rules: []string{
		"GEOSITE,telegram",
		"IP-CIDR,91.108.4.0/22,no-resolve", "IP-CIDR,91.108.8.0/22,no-resolve", "IP-CIDR,91.108.12.0/22,no-resolve",
		"IP-CIDR,91.108.16.0/22,no-resolve", "IP-CIDR,91.108.20.0/22,no-resolve", "IP-CIDR,91.108.56.0/22,no-resolve",
		"IP-CIDR,95.161.64.0/20,no-resolve", "IP-CIDR,149.154.160.0/20,no-resolve",
		"IP-CIDR6,2001:67c:4e8::/48,no-resolve", "IP-CIDR6,2001:b28:f23d::/48,no-resolve", "IP-CIDR6,2001:b28:f23f::/48,no-resolve",
	}},
	{ID: "youtube", Group: "YouTube", Policy: aggProxyFirst, Rules: []string{"GEOSITE,youtube"}},
	{ID: "netflix", Group: "Netflix", Policy: aggProxyFirst, Rules: []string{"GEOSITE,netflix"}},
	{ID: "disney", Group: "Disney+", Policy: aggProxyFirst, Rules: []string{"GEOSITE,disney"}},
	{ID: "spotify", Group: "Spotify", Policy: aggProxyFirst, Rules: []string{"GEOSITE,spotify"}},
	{ID: "tiktok", Group: "TikTok", Policy: aggProxyFirst, Rules: []string{"GEOSITE,tiktok"}},
	{ID: "social", Group: "社交媒体", Policy: aggProxyFirst, Rules: []string{"GEOSITE,twitter", "GEOSITE,facebook", "GEOSITE,instagram"}},
	{ID: "github", Group: "GitHub", Policy: aggProxyFirst, Rules: []string{"GEOSITE,github"}},
	{ID: "google", Group: "谷歌服务", Policy: aggProxyFirst, Rules: []string{"GEOSITE,google"}},
	{ID: "microsoft", Group: "微软服务", Policy: aggDirectFirst, Rules: []string{"GEOSITE,microsoft"}},
	{ID: "apple", Group: "苹果服务", Policy: aggDirectFirst, Rules: []string{"GEOSITE,apple"}},
	{ID: "games", Group: "游戏平台", Policy: aggDirectFirst, Rules: []string{"GEOSITE,steam", "GEOSITE,category-games"}},
	{ID: "global", Group: "国外网站", Policy: aggNoGroup, Target: AggGroupSelect, Rules: []string{"GEOSITE,geolocation-!cn"}},
	{ID: "cn", Group: "国内直连", Policy: aggNoGroup, Target: "DIRECT", Rules: []string{"GEOSITE,cn", "GEOIP,CN,no-resolve"}},
}

// aggFormats are the texts of validating and building an aggregate
// subscription, by code (Translator.text); the interface translates them.
var aggFormats = map[string]string{
	"tooManySources": "来源不能超过 %d 个",
	"tooManyRenames": "重命名规则不能超过 %d 条",
	"dupSource":      "第 %d 个来源的 ID 重复",
	"badURL":         "第 %d 个来源的订阅链接无效：请填写以 http:// 或 https:// 开头的完整地址",
	"noLinks":        "第 %d 个来源没有填写节点链接",
	"linksTooLong":   "第 %d 个来源的节点链接超过 1 MB",
	"noSub":          "第 %d 个来源没有选择订阅",
	"badKind":        "第 %d 个来源的类型无效：%q",
	"noSources":      "至少需要一个启用的来源",
	"badInclude":     "「保留」的正则表达式无效：%v",
	"badExclude":     "「排除」的正则表达式无效：%v",
	"emptyRename":    "第 %d 条重命名规则缺少要匹配的内容",
	"badRename":      "第 %d 条重命名规则的正则表达式无效：%v",
	"badRegion":      "未知的地区分组：%q",
	"badModule":      "未知的分流模块：%q",
	"patternTooLong": "超过 %d 个字符",
	"tooManyNodes":   "节点超过 %d 个，多出的部分没有并入",
	"noNodesBecause": "没有可用的节点：%s",
	"noNodes":        "没有可用的节点：来源里没有能识别的节点，或全部被筛选掉了",
	"labelURL":       "订阅链接",
	"labelLinks":     "节点链接",
	"labelSub":       "订阅",
	"sourceWarning":  "%s：%s",
	"empty":          "内容为空",
	"providers":      "这个配置的部分节点在 proxy-providers 里，聚合订阅只能并入 proxies 里的节点",
	"unknownContent": "无法识别内容：既不是 Clash/Mihomo 配置，也不是节点分享链接",
	"noLinkUsable":   "%d 条链接都无法识别或缺少必填项",
}
