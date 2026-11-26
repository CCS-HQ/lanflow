package profile

//go:generate go run ./internal/ruledbgen ../../bin/geo/geosite.dat aggregate_ruledb_gen.go

import (
	"sort"
	"strings"

	"lanflow/internal/model"
)

// The catalog of an aggregate subscription's editor (docs/aggregate.md):
// the proxy-group modules, the templates and the rule library. Group
// names are part of the generated profile; the editor shows the Label
// and Desc through i18n.T.

// Module categories, in the editor's order.
const (
	AggCatCore    = "core"
	AggCatRegion  = "region"
	AggCatService = "service"
	AggCatSocial  = "social"
	AggCatMedia   = "media"
	AggCatGame    = "game"
	AggCatTech    = "tech"
	AggCatFinance = "finance"
	AggCatOther   = "other"
)

// AggregateCategory is a heading of the module list.
type AggregateCategory struct {
	ID    string
	Label string
}

// AggregateCategories are the module categories, in order.
var AggregateCategories = []AggregateCategory{
	{AggCatCore, "核心组"},
	{AggCatRegion, "地区节点"},
	{AggCatService, "常用服务"},
	{AggCatSocial, "社交通讯"},
	{AggCatMedia, "流媒体"},
	{AggCatGame, "游戏平台"},
	{AggCatTech, "技术服务"},
	{AggCatFinance, "金融服务"},
	{AggCatOther, "其他"},
}

// IDs of the modules the generator treats specially.
const (
	AggModSelect = "select" // 节点选择: every node; the other groups offer it first
	AggModAuto   = "auto"   // 自动选择: url-test over every node
	AggModFinal  = "final"  // 漏网之鱼: MATCH
)

// AggregateModule is a proxy group the editor offers. Its rules (rule
// sets, see AggregateRuleSet) send traffic to it.
type AggregateModule struct {
	ID       string
	Category string
	Emoji    string
	Label    string // the group's name after the emoji; the editor's label
	Desc     string
	Type     string   // model.AggType…
	Rules    []string // rule sets: "geosite:youtube", "geoip:telegram"
	// Extra are the extended rule sets (扩展规则集): a group with
	// Extended set (the 完整版 template's) has them besides Rules.
	Extra []string
	// Filter limits the group to the nodes whose name matches (region
	// groups, set from aggRegionFilter); a group with no node left is not
	// generated.
	Filter string
	// Required modules cannot be turned off (the others refer to them).
	Required bool
}

// Name is the group's name in the profile.
func (m AggregateModule) Name() string { return m.Emoji + " " + m.Label }

// AggregateModules are the modules, in the default order of groups and rules.
var AggregateModules = []AggregateModule{
	// 核心组
	{ID: AggModSelect, Category: AggCatCore, Emoji: "🚀", Label: "节点选择", Desc: "手动选择节点，其他代理组默认跟随它", Type: model.AggTypeSelect, Required: true},
	{ID: AggModAuto, Category: AggCatCore, Emoji: "⚡", Label: "自动选择", Desc: "按延迟自动选用最快的节点", Type: model.AggTypeURLTest},
	{ID: "ads", Category: AggCatCore, Emoji: "🛑", Label: "广告拦截", Desc: "拦截常见广告与跟踪域名", Type: model.AggTypeRejectFirst, Rules: []string{"geosite:category-ads-all"}, Extra: []string{"geosite:win-spy", "geosite:win-extra"}},
	{ID: "private", Category: AggCatCore, Emoji: "🏠", Label: "私有网络", Desc: "局域网与私有地址直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:private", "geoip:private"}},
	{ID: "cn", Category: AggCatCore, Emoji: "🔒", Label: "国内服务", Desc: "国内域名与 IP 直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:cn", "geoip:cn"}},
	{ID: "global", Category: AggCatCore, Emoji: "🌍", Label: "非中国", Desc: "境外域名走代理", Type: model.AggTypeSelect, Rules: []string{"geosite:geolocation-!cn"}},
	{ID: AggModFinal, Category: AggCatCore, Emoji: "🐟", Label: "漏网之鱼", Desc: "没有命中任何规则的流量", Type: model.AggTypeSelect, Required: true},

	// 地区节点
	{ID: "hk", Category: AggCatRegion, Emoji: "🇭🇰", Label: "香港节点", Desc: "名称含香港 / HK 的节点", Type: model.AggTypeURLTest},
	{ID: "tw", Category: AggCatRegion, Emoji: "🌏", Label: "台湾节点", Desc: "名称含台湾 / TW 的节点", Type: model.AggTypeURLTest},
	{ID: "jp", Category: AggCatRegion, Emoji: "🇯🇵", Label: "日本节点", Desc: "名称含日本 / JP 的节点", Type: model.AggTypeURLTest},
	{ID: "sg", Category: AggCatRegion, Emoji: "🇸🇬", Label: "新加坡节点", Desc: "名称含新加坡 / SG 的节点", Type: model.AggTypeURLTest},
	{ID: "us", Category: AggCatRegion, Emoji: "🇺🇸", Label: "美国节点", Desc: "名称含美国 / US 的节点", Type: model.AggTypeURLTest},
	{ID: "kr", Category: AggCatRegion, Emoji: "🇰🇷", Label: "韩国节点", Desc: "名称含韩国 / KR 的节点", Type: model.AggTypeURLTest},
	{ID: "gb", Category: AggCatRegion, Emoji: "🇬🇧", Label: "英国节点", Desc: "名称含英国 / UK 的节点", Type: model.AggTypeURLTest},
	{ID: "de", Category: AggCatRegion, Emoji: "🇩🇪", Label: "德国节点", Desc: "名称含德国 / DE 的节点", Type: model.AggTypeURLTest},

	// 常用服务
	{ID: "ai", Category: AggCatService, Emoji: "🤖", Label: "AI 服务", Desc: "ChatGPT、Claude、Gemini 等", Type: model.AggTypeSelect, Rules: []string{"geosite:openai", "geosite:anthropic", "geosite:google-gemini", "geosite:category-ai-!cn"}, Extra: []string{"geosite:perplexity", "geosite:xai", "geosite:category-ai-chat-!cn", "geosite:huggingface", "geosite:cursor", "geosite:github-copilot"}},
	{ID: "youtube", Category: AggCatService, Emoji: "📹", Label: "YouTube", Desc: "YouTube 与 YouTube Music", Type: model.AggTypeSelect, Rules: []string{"geosite:youtube"}},
	{ID: "google", Category: AggCatService, Emoji: "🔍", Label: "谷歌服务", Desc: "搜索、Gmail、云端硬盘等", Type: model.AggTypeSelect, Rules: []string{"geosite:google", "geoip:google"}, Extra: []string{"geosite:google-play", "geosite:googlefcm", "geosite:android", "geosite:google-scholar"}},
	{ID: "microsoft", Category: AggCatService, Emoji: "Ⓜ️", Label: "微软服务", Desc: "Microsoft 365、OneDrive、Bing", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:microsoft", "geosite:onedrive"}, Extra: []string{"geosite:bing", "geosite:msn", "geosite:azure"}},
	{ID: "apple", Category: AggCatService, Emoji: "🍏", Label: "苹果服务", Desc: "iCloud、App Store", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:apple", "geosite:icloud"}, Extra: []string{"geosite:apple-tvplus", "geosite:apple-music", "geosite:apple-dev"}},
	{ID: "speedtest", Category: AggCatService, Emoji: "📶", Label: "网络测速", Desc: "Speedtest 等测速站点", Type: model.AggTypeSelect, Rules: []string{"geosite:ookla-speedtest"}, Extra: []string{"geosite:speedtest", "geosite:category-speedtest"}},

	// 社交通讯
	{ID: "telegram", Category: AggCatSocial, Emoji: "📲", Label: "Telegram", Desc: "Telegram 消息与通话", Type: model.AggTypeSelect, Rules: []string{"geosite:telegram", "geoip:telegram"}},
	{ID: "twitter", Category: AggCatSocial, Emoji: "🐦", Label: "X / Twitter", Desc: "X（Twitter）", Type: model.AggTypeSelect, Rules: []string{"geosite:twitter"}},
	{ID: "meta", Category: AggCatSocial, Emoji: "📘", Label: "Meta", Desc: "Facebook、Instagram、WhatsApp", Type: model.AggTypeSelect, Rules: []string{"geosite:facebook", "geosite:instagram", "geosite:whatsapp"}, Extra: []string{"geosite:threads"}},
	{ID: "discord", Category: AggCatSocial, Emoji: "💬", Label: "Discord", Desc: "Discord 社区与语音", Type: model.AggTypeSelect, Rules: []string{"geosite:discord"}},
	{ID: "messaging", Category: AggCatSocial, Emoji: "📞", Label: "海外通讯", Desc: "LINE、Signal", Type: model.AggTypeSelect, Rules: []string{"geosite:line", "geosite:signal"}},

	// 流媒体
	{ID: "netflix", Category: AggCatMedia, Emoji: "🎬", Label: "Netflix", Desc: "Netflix 网页与客户端", Type: model.AggTypeSelect, Rules: []string{"geosite:netflix", "geoip:netflix"}},
	{ID: "disney", Category: AggCatMedia, Emoji: "🏰", Label: "Disney+", Desc: "Disney+ 流媒体", Type: model.AggTypeSelect, Rules: []string{"geosite:disney"}},
	{ID: "spotify", Category: AggCatMedia, Emoji: "🎵", Label: "Spotify", Desc: "Spotify 音乐", Type: model.AggTypeSelect, Rules: []string{"geosite:spotify"}},
	{ID: "tiktok", Category: AggCatMedia, Emoji: "🎶", Label: "TikTok", Desc: "TikTok 国际版", Type: model.AggTypeSelect, Rules: []string{"geosite:tiktok"}},
	{ID: "streaming", Category: AggCatMedia, Emoji: "📺", Label: "海外流媒体", Desc: "HBO、Prime Video、Twitch、巴哈姆特", Type: model.AggTypeSelect, Rules: []string{"geosite:hbo", "geosite:primevideo", "geosite:twitch", "geosite:bahamut"}, Extra: []string{"geosite:hulu", "geosite:dazn", "geosite:abema", "geosite:niconico", "geosite:kick", "geosite:soundcloud", "geosite:deezer", "geosite:tidal"}},
	{ID: "bilibili", Category: AggCatMedia, Emoji: "📺", Label: "哔哩哔哩", Desc: "B 站，港澳台番剧可切代理", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:bilibili"}},

	// 游戏平台
	{ID: "steam", Category: AggCatGame, Emoji: "🎮", Label: "Steam", Desc: "Steam 商店与社区", Type: model.AggTypeSelect, Rules: []string{"geosite:steam"}},
	{ID: "epic", Category: AggCatGame, Emoji: "🎯", Label: "Epic Games", Desc: "Epic 商店", Type: model.AggTypeSelect, Rules: []string{"geosite:epicgames"}},
	{ID: "console", Category: AggCatGame, Emoji: "🕹️", Label: "主机平台", Desc: "PlayStation、Nintendo", Type: model.AggTypeSelect, Rules: []string{"geosite:playstation", "geosite:nintendo"}},
	{ID: "games", Category: AggCatGame, Emoji: "👾", Label: "游戏服务", Desc: "暴雪、EA、育碧", Type: model.AggTypeSelect, Rules: []string{"geosite:blizzard", "geosite:ea", "geosite:ubisoft"}, Extra: []string{"geosite:category-games", "geosite:rockstar", "geosite:xbox", "geosite:activision", "geosite:2kgames", "geosite:gog", "geosite:origin"}},

	// 技术服务
	{ID: "github", Category: AggCatTech, Emoji: "🐱", Label: "GitHub", Desc: "GitHub 与其资源下载", Type: model.AggTypeSelect, Rules: []string{"geosite:github"}},
	{ID: "dev", Category: AggCatTech, Emoji: "🧰", Label: "开发者服务", Desc: "GitLab、Docker、JetBrains", Type: model.AggTypeSelect, Rules: []string{"geosite:gitlab", "geosite:docker", "geosite:jetbrains"}, Extra: []string{"geosite:npmjs", "geosite:python", "geosite:gitbook", "geosite:stackexchange", "geosite:atlassian", "geosite:vercel", "geosite:netlify", "geosite:category-dev"}},
	{ID: "cloudflare", Category: AggCatTech, Emoji: "☁️", Label: "Cloudflare", Desc: "Cloudflare 服务", Type: model.AggTypeSelect, Rules: []string{"geosite:cloudflare"}},
	{ID: "scholar", Category: AggCatTech, Emoji: "🎓", Label: "学术网站", Desc: "海外论文与学术资源", Type: model.AggTypeSelect, Rules: []string{"geosite:category-scholar-!cn"}},

	// 金融服务
	{ID: "paypal", Category: AggCatFinance, Emoji: "💳", Label: "海外支付", Desc: "PayPal、Stripe", Type: model.AggTypeSelect, Rules: []string{"geosite:paypal", "geosite:stripe"}, Extra: []string{"geosite:wise", "geosite:visa", "geosite:mastercard"}},
	{ID: "crypto", Category: AggCatFinance, Emoji: "🪙", Label: "加密货币", Desc: "交易所与钱包站点", Type: model.AggTypeSelect, Rules: []string{"geosite:category-cryptocurrency", "geosite:binance"}},

	// 其他
	{ID: "reddit", Category: AggCatOther, Emoji: "👽", Label: "Reddit", Desc: "Reddit 社区", Type: model.AggTypeSelect, Rules: []string{"geosite:reddit"}},
	{ID: "pt", Category: AggCatOther, Emoji: "📥", Label: "PT 下载", Desc: "PT 站点直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:category-pt"}},
	{ID: "games-cn", Category: AggCatOther, Emoji: "🀄", Label: "国内游戏", Desc: "国内游戏平台直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:category-games-cn"}},
}

// AggModule returns the module id of the catalog, nil if there is none.
func AggModule(id string) *AggregateModule {
	for i := range AggregateModules {
		if AggregateModules[i].ID == id {
			return &AggregateModules[i]
		}
	}
	return nil
}

// AggregateTemplate is a built-in template of the quick mode (精简 / 标准 / 完整).
type AggregateTemplate struct {
	ID      string
	Label   string
	Desc    string
	Modules []string // IDs, in catalog order
	// Extended: the groups also have their modules' extended rule sets.
	Extended bool
}

// The built-in templates.
var AggregateTemplates = []AggregateTemplate{
	{ID: "lite", Label: "精简版", Desc: "基础代理组 + 国内外分流", Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final"}},
	{ID: "standard", Label: "标准版", Desc: "完整代理组 + 常用规则", Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final",
		"ai", "youtube", "google", "microsoft", "apple", "telegram", "netflix"}},
	{ID: "full", Label: "完整版", Desc: "全部功能 + 扩展规则集", Extended: true, Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final",
		"hk", "tw", "jp", "sg", "us", "kr",
		"ai", "youtube", "google", "microsoft", "apple", "speedtest", "telegram", "twitter", "meta", "discord",
		"netflix", "disney", "spotify", "tiktok", "streaming", "steam", "games", "github", "dev", "paypal"}},
}

// AggTemplate returns the built-in template id, nil if there is none.
func AggTemplate(id string) *AggregateTemplate {
	for i := range AggregateTemplates {
		if AggregateTemplates[i].ID == id {
			return &AggregateTemplates[i]
		}
	}
	return nil
}

// TemplateGroups are the groups of template t, in catalog order.
func (t AggregateTemplate) Groups() []model.AggregateGroup {
	var out []model.AggregateGroup
	for _, m := range AggregateModules {
		if contains(t.Modules, m.ID) {
			out = append(out, model.AggregateGroup{ID: m.ID, Extended: t.Extended && len(m.Extra) > 0})
		}
	}
	return out
}

// RuleSets counts the rule sets of the modules of template t.
func (t AggregateTemplate) RuleSets() int {
	n := 0
	for _, g := range t.Groups() {
		n += len(AggGroupRules(g))
	}
	return n
}

// DefaultRuleBaseURL is where remote rule sets come from: MetaCubeX's
// meta-rules-dat, whose geosite/ and geoip/ folders hold one .mrs file per
// category.
const DefaultRuleBaseURL = "https://github.com/MetaCubeX/meta-rules-dat/raw/refs/heads/meta/geo"

// geoIPExtra are the geoip sets of meta-rules-dat beyond country codes.
// With the bundled database only private (GEOIP,LAN) and telegram (its
// address ranges) work; the others are left out.
var geoIPExtra = []string{"private", "telegram", "google", "netflix", "facebook", "twitter", "cloudflare", "cloudfront", "fastly"}

// geoIPCountries are the country codes offered by the rule library.
var geoIPCountries = strings.Fields("cn hk mo tw jp kr sg us gb de fr nl ru in au ca my th vn ph id tr ae br")

// AggregateRuleSet is a rule set of the rule library.
type AggregateRuleSet struct {
	Ref  string // "geosite:netflix", "geoip:cn"
	Name string // the category
	IP   bool   // a geoip set
}

// SearchRuleSets returns up to limit rule sets whose name contains q
// (case-insensitive): exact matches first, then prefix matches, then the
// rest, each by name.
func SearchRuleSets(q string, limit int) []AggregateRuleSet {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []AggregateRuleSet
	add := func(name string, ip bool) {
		if q == "" || strings.Contains(name, q) {
			kind := "geosite:"
			if ip {
				kind = "geoip:"
			}
			out = append(out, AggregateRuleSet{Ref: kind + name, Name: name, IP: ip})
		}
	}
	for _, n := range geositeCategories {
		add(n, false)
	}
	for _, n := range geoIPCountries {
		add(n, true)
	}
	for _, n := range geoIPExtra {
		add(n, true)
	}
	rank := func(s AggregateRuleSet) int {
		switch {
		case s.Name == q:
			return 0
		case strings.HasPrefix(s.Name, q):
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RuleSetCount is the number of rule sets the rule library offers.
func RuleSetCount() int { return len(geositeCategories) + len(geoIPCountries) + len(geoIPExtra) }

// validRuleSetRef reports whether ref names a rule set of the library.
func validRuleSetRef(ref string) bool {
	kind, name, ok := strings.Cut(ref, ":")
	if !ok || name == "" {
		return false
	}
	switch kind {
	case "geosite":
		i := sort.SearchStrings(geositeCategories, name)
		return i < len(geositeCategories) && geositeCategories[i] == name
	case "geoip":
		return contains(geoIPCountries, name) || contains(geoIPExtra, name) || len(name) == 2
	}
	return false
}

// aggFormats are the texts of validating and building an aggregate
// subscription, by code (Translator.text); the interface translates them.
var aggFormats = map[string]string{
	"tooManySources": "来源不能超过 %d 个",
	"tooManyRenames": "重命名规则不能超过 %d 条",
	"tooManyRules":   "自定义规则不能超过 %d 条",
	"tooManyGroups":  "代理组不能超过 %d 个",
	"dupSource":      "第 %d 个来源的 ID 重复",
	"badURL":         "第 %d 个来源的订阅链接无效：请填写以 http:// 或 https:// 开头的完整地址",
	"noYAML":         "第 %d 个来源没有填写 YAML 配置",
	"noLinks":        "第 %d 个来源没有填写节点链接",
	"linksTooLong":   "第 %d 个来源的内容超过 1 MB",
	"noSub":          "第 %d 个来源没有选择订阅",
	"badKind":        "第 %d 个来源的类型无效：%q",
	"noSources":      "至少需要一个启用的来源",
	"badInclude":     "「保留」第 %d 行的正则表达式无效：%v",
	"badExclude":     "「排除」第 %d 行的正则表达式无效：%v",
	"emptyRename":    "第 %d 条重命名规则缺少要匹配的内容",
	"badRename":      "第 %d 条重命名规则的正则表达式无效：%v",
	"badModule":      "未知的代理组：%q",
	"dupGroup":       "代理组重复：%q",
	"badGroupType":   "代理组「%s」的类型无效：%q",
	"noGroupName":    "第 %d 个自定义分组没有名称",
	"dupGroupName":   "代理组名称重复：「%s」",
	"badGroupFilter": "代理组「%s」的节点筛选无效：%v",
	"badGroupRule":   "代理组「%s」的规则集无效：%q",
	"badRelay":       "第 %d 个中转组：%s",
	"relayNoName":    "没有名称",
	"relayNoRelay":   "没有填写中转节点的匹配",
	"relayNoLanding": "没有填写落地节点的匹配",
	"relayBadRe":     "正则表达式无效：%v",
	"badRule":        "第 %d 条自定义规则：%s",
	"ruleBadType":    "类型无效：%q",
	"ruleNoValue":    "没有填写规则值",
	"ruleBadSet":     "规则集不存在：%q",
	"ruleBadTarget":  "目标代理组不存在：%q",
	"ruleBadCIDR":    "不是有效的 IP 段：%q",
	"badRuleSource":  "规则来源无效：%q",
	"badRuleBase":    "规则集 URL 无效：请填写以 http:// 或 https:// 开头的地址",
	"badListen":      "监听端口须为 1024–%d 之间的数字",
	"badBase":        "基础和 DNS 配置不是有效的 YAML 映射：%v",
	"patternTooLong": "超过 %d 个字符",
	"tooManyNodes":   "节点超过 %d 个，多出的部分没有并入",
	"noNodesBecause": "没有可用的节点：%s",
	"noNodes":        "没有可用的节点：来源里没有能识别的节点，或全部被筛选掉了",
	"allHidden":      "没有可用的节点：节点都被隐藏了",
	"labelURL":       "订阅链接",
	"labelYAML":      "YAML 配置",
	"labelLinks":     "节点链接",
	"labelSub":       "订阅",
	"sourceWarning":  "%s：%s",
	"empty":          "内容为空",
	"providers":      "这个配置的部分节点在 proxy-providers 里，聚合订阅只能并入 proxies 里的节点",
	"unknownContent": "无法识别内容：既不是 Clash/Mihomo 配置或节点，也不是节点分享链接",
	"noLinkUsable":   "%d 条链接都无法识别或缺少必填项",
	"geoSkipped":     "自带数据库没有这些 IP 规则集，已跳过：%s（改用远程规则集即可使用）",
	"relayEmpty":     "中转组「%s」没有匹配到中转节点，已跳过",
}
