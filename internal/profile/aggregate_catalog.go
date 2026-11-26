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
	// Tail are rule sets placed last, after every other module's (the
	// complete China list after 非中国); they are not counted as the
	// module's rule sets.
	Tail []string
	// Filter limits the group to the nodes whose name matches; a group
	// with no node left is not generated.
	Filter string
	// Required modules cannot be turned off (the others refer to them).
	Required bool
}

// Name is the group's name in the profile.
func (m AggregateModule) Name() string { return m.Emoji + " " + m.Label }

// AggregateModules are the modules, in the order of the generated groups
// (and, within a category, of the editor's list). The groups, their
// rule sets and the templates follow SubBoost's (subboost.org, v2.11).
var AggregateModules = []AggregateModule{
	// 核心组 (私有网络, 国内服务, 非中国 and 漏网之鱼 come after the services)
	{ID: AggModSelect, Category: AggCatCore, Emoji: "🚀", Label: "节点选择", Desc: "手动选择代理节点", Type: model.AggTypeSelect, Required: true},
	{ID: AggModAuto, Category: AggCatCore, Emoji: "⚡", Label: "自动选择", Desc: "自动选择最快节点", Type: model.AggTypeURLTest},
	{ID: "ads", Category: AggCatCore, Emoji: "🛑", Label: "广告拦截", Desc: "拦截广告和追踪器", Type: model.AggTypeRejectFirst, Rules: []string{"geosite:category-ads-all"}},

	// 常用服务
	{ID: "ai", Category: AggCatService, Emoji: "🤖", Label: "AI 服务", Desc: "ChatGPT、Claude、Copilot 等", Type: model.AggTypeSelect, Rules: []string{"geosite:openai", "geosite:anthropic", "geosite:category-ai-chat-!cn"}},
	{ID: "gemini", Category: AggCatService, Emoji: "✨", Label: "Gemini", Desc: "Google Gemini (Bard) 等", Type: model.AggTypeSelect, Rules: []string{"geosite:google-gemini"}},
	{ID: "youtube", Category: AggCatService, Emoji: "📹", Label: "油管视频", Desc: "YouTube 视频、YouTube Music", Type: model.AggTypeSelect, Rules: []string{"geosite:youtube"}},
	{ID: "google", Category: AggCatService, Emoji: "🔍", Label: "谷歌服务", Desc: "Google 搜索、Gmail、Drive", Type: model.AggTypeSelect, Rules: []string{"geosite:google", "geoip:google"}},
	{ID: "microsoft", Category: AggCatService, Emoji: "Ⓜ️", Label: "微软服务", Desc: "Microsoft 365、OneDrive、Bing", Type: model.AggTypeSelect, Rules: []string{"geosite:microsoft", "geosite:onedrive"}},
	{ID: "apple", Category: AggCatService, Emoji: "🍏", Label: "苹果服务", Desc: "iCloud、App Store", Type: model.AggTypeSelect, Rules: []string{"geosite:apple", "geosite:icloud"}},

	// 社交通讯
	{ID: "telegram", Category: AggCatSocial, Emoji: "📲", Label: "电报消息", Desc: "Telegram 消息和通话", Type: model.AggTypeSelect, Rules: []string{"geosite:telegram", "geoip:telegram"}},
	{ID: "twitter", Category: AggCatSocial, Emoji: "🐦", Label: "推特/X", Desc: "Twitter/X 社交平台", Type: model.AggTypeSelect, Rules: []string{"geosite:twitter", "geoip:twitter"}},
	{ID: "meta", Category: AggCatSocial, Emoji: "📘", Label: "Meta 系", Desc: "Facebook、Instagram", Type: model.AggTypeSelect, Rules: []string{"geosite:facebook", "geosite:instagram", "geosite:whatsapp", "geoip:facebook"}},
	{ID: "discord", Category: AggCatSocial, Emoji: "🎙️", Label: "Discord", Desc: "Discord 语音聊天", Type: model.AggTypeSelect, Rules: []string{"geosite:discord"}},
	{ID: "social", Category: AggCatSocial, Emoji: "💬", Label: "其他社交", Desc: "TikTok、Line、Reddit", Type: model.AggTypeSelect, Rules: []string{"geosite:tiktok", "geosite:line", "geosite:reddit", "geosite:linkedin", "geosite:snap", "geosite:pinterest", "geosite:tumblr"}},

	// 流媒体
	{ID: "netflix", Category: AggCatMedia, Emoji: "🎬", Label: "奈飞", Desc: "Netflix 视频", Type: model.AggTypeSelect, Rules: []string{"geosite:netflix", "geoip:netflix"}},
	{ID: "disney", Category: AggCatMedia, Emoji: "🏰", Label: "迪士尼+", Desc: "Disney+ 视频", Type: model.AggTypeSelect, Rules: []string{"geosite:disney"}},
	{ID: "western", Category: AggCatMedia, Emoji: "📺", Label: "欧美流媒体", Desc: "HBO、Hulu、Prime Video", Type: model.AggTypeSelect, Rules: []string{"geosite:hbo", "geosite:hulu", "geosite:primevideo", "geosite:apple-tvplus", "geosite:spotify", "geosite:twitch", "geosite:dazn"}},
	{ID: "asian", Category: AggCatMedia, Emoji: "🎌", Label: "亚洲流媒体", Desc: "巴哈姆特、Abema、Viu、KKTV 等", Type: model.AggTypeSelect, Rules: []string{"geosite:bahamut", "geosite:biliintl", "geosite:niconico", "geosite:abema", "geosite:viu", "geosite:kktv"}},

	// 游戏平台
	{ID: "steam", Category: AggCatGame, Emoji: "🎮", Label: "Steam", Desc: "Steam 游戏平台", Type: model.AggTypeSelect, Rules: []string{"geosite:steam"}},
	{ID: "pcgames", Category: AggCatGame, Emoji: "🖥️", Label: "PC 游戏", Desc: "Epic、EA、Ubisoft、GOG", Type: model.AggTypeSelect, Rules: []string{"geosite:epicgames", "geosite:ea", "geosite:ubisoft", "geosite:blizzard", "geosite:gog", "geosite:riot"}},
	{ID: "console", Category: AggCatGame, Emoji: "🎯", Label: "主机游戏", Desc: "PlayStation、Xbox", Type: model.AggTypeSelect, Rules: []string{"geosite:playstation", "geosite:xbox", "geosite:nintendo"}},

	// 技术服务
	{ID: "github", Category: AggCatTech, Emoji: "🐱", Label: "代码托管", Desc: "GitHub、GitLab、Bitbucket", Type: model.AggTypeSelect, Rules: []string{"geosite:github", "geosite:gitlab", "geosite:atlassian"}},
	{ID: "cloud", Category: AggCatTech, Emoji: "☁️", Label: "云服务", Desc: "AWS、Azure、Cloudflare 等", Type: model.AggTypeSelect, Rules: []string{"geosite:aws", "geosite:azure", "geosite:cloudflare", "geosite:digitalocean", "geosite:vercel", "geosite:netlify", "geoip:cloudflare"}},
	{ID: "dev", Category: AggCatTech, Emoji: "🛠️", Label: "开发工具", Desc: "Docker、npm、JetBrains", Type: model.AggTypeSelect, Rules: []string{"geosite:docker", "geosite:npmjs", "geosite:jetbrains", "geosite:stackexchange"}},
	{ID: "storage", Category: AggCatTech, Emoji: "💾", Label: "网盘存储", Desc: "Dropbox、Notion", Type: model.AggTypeSelect, Rules: []string{"geosite:dropbox", "geosite:notion"}},

	// 金融服务
	{ID: "paypal", Category: AggCatFinance, Emoji: "💳", Label: "支付平台", Desc: "PayPal、Stripe、Wise 等", Type: model.AggTypeSelect, Rules: []string{"geosite:paypal", "geosite:stripe", "geosite:wise"}},
	{ID: "crypto", Category: AggCatFinance, Emoji: "₿", Label: "加密货币", Desc: "Binance 等", Type: model.AggTypeSelect, Rules: []string{"geosite:binance"}},

	// 其他
	{ID: "gscholar", Category: AggCatOther, Emoji: "🎓", Label: "谷歌学术", Desc: "Google Scholar 学术搜索", Type: model.AggTypeSelect, Rules: []string{"geosite:google-scholar"}},
	{ID: "edu", Category: AggCatOther, Emoji: "📚", Label: "教育学术", Desc: "Coursera、Udemy、学术资源等", Type: model.AggTypeSelect, Rules: []string{"geosite:category-scholar-!cn", "geosite:coursera", "geosite:udemy", "geosite:edx", "geosite:khanacademy", "geosite:wikimedia"}},
	{ID: "news", Category: AggCatOther, Emoji: "📰", Label: "新闻资讯", Desc: "BBC、CNN、NYT、Bloomberg 等", Type: model.AggTypeSelect, Rules: []string{"geosite:bbc", "geosite:cnn", "geosite:nytimes", "geosite:wsj", "geosite:bloomberg"}},
	{ID: "shopping", Category: AggCatOther, Emoji: "🛒", Label: "海淘购物", Desc: "Amazon、eBay", Type: model.AggTypeSelect, Rules: []string{"geosite:amazon", "geosite:ebay"}},
	{ID: "porn", Category: AggCatOther, Emoji: "🔞", Label: "成人内容", Desc: "成人网站（默认关闭）", Type: model.AggTypeSelect, Rules: []string{"geosite:category-porn"}},

	// 核心组, after the services
	{ID: "private", Category: AggCatCore, Emoji: "🏠", Label: "私有网络", Desc: "局域网和私有IP直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:private", "geoip:private"}},
	{ID: "cn", Category: AggCatCore, Emoji: "🔒", Label: "国内服务", Desc: "国内网站和服务直连", Type: model.AggTypeDirectFirst, Rules: []string{"geosite:geolocation-cn", "geoip:cn"}, Tail: []string{"geosite:cn"}},
	{ID: "global", Category: AggCatCore, Emoji: "🌍", Label: "非中国", Desc: "非中国域名走代理", Type: model.AggTypeSelect, Rules: []string{"geosite:geolocation-!cn"}},
	{ID: AggModFinal, Category: AggCatCore, Emoji: "🐟", Label: "漏网之鱼", Desc: "未匹配到任何规则的流量", Type: model.AggTypeSelect, Required: true},
}

// aggRuleOrder is the order of the catalog's rule sets in the generated
// rules, which is not that of the groups: the narrow sets come before
// the wide ones they overlap (Gemini before Google, Apple TV+ before
// Apple), the complete China list last. Rule sets not listed (a custom
// group's) come before geosite:geolocation-!cn.
var aggRuleOrder = []string{
	"geosite:category-ads-all", "geosite:private", "geoip:private", "geosite:google-gemini",
	"geosite:openai", "geosite:anthropic", "geosite:category-ai-chat-!cn", "geosite:geolocation-cn", "geoip:cn",
	"geosite:youtube", "geosite:google-scholar", "geosite:category-scholar-!cn", "geosite:coursera", "geosite:udemy",
	"geosite:edx", "geosite:khanacademy", "geosite:wikimedia", "geosite:aws", "geosite:azure", "geosite:cloudflare",
	"geosite:digitalocean", "geosite:vercel", "geosite:netlify", "geoip:cloudflare", "geosite:google", "geoip:google",
	"geosite:telegram", "geoip:telegram", "geosite:github", "geosite:gitlab", "geosite:atlassian", "geosite:microsoft",
	"geosite:onedrive", "geosite:apple-tvplus", "geosite:apple", "geosite:icloud", "geosite:twitter", "geoip:twitter",
	"geosite:facebook", "geosite:instagram", "geosite:whatsapp", "geoip:facebook", "geosite:discord", "geosite:tiktok",
	"geosite:line", "geosite:reddit", "geosite:linkedin", "geosite:snap", "geosite:pinterest", "geosite:tumblr",
	"geosite:netflix", "geoip:netflix", "geosite:disney", "geosite:hbo", "geosite:hulu", "geosite:primevideo",
	"geosite:spotify", "geosite:twitch", "geosite:dazn", "geosite:bahamut", "geosite:biliintl", "geosite:niconico",
	"geosite:abema", "geosite:viu", "geosite:kktv", "geosite:steam", "geosite:epicgames", "geosite:ea", "geosite:ubisoft",
	"geosite:blizzard", "geosite:gog", "geosite:riot", "geosite:playstation", "geosite:xbox", "geosite:nintendo",
	"geosite:docker", "geosite:npmjs", "geosite:jetbrains", "geosite:stackexchange", "geosite:dropbox", "geosite:notion",
	"geosite:paypal", "geosite:stripe", "geosite:wise", "geosite:binance", "geosite:bbc", "geosite:cnn", "geosite:nytimes",
	"geosite:wsj", "geosite:bloomberg", "geosite:amazon", "geosite:ebay", "geosite:category-porn",
	"geosite:geolocation-!cn", "geosite:cn",
}

// aggRuleRank is the position of a rule set key in aggRuleOrder; an
// unlisted one ranks just before geosite:geolocation-!cn.
func aggRuleRank(key string) int {
	for i, k := range aggRuleOrder {
		if k == key {
			return 2 * i
		}
	}
	for i, k := range aggRuleOrder {
		if k == "geosite:geolocation-!cn" {
			return 2*i - 1
		}
	}
	return 2 * len(aggRuleOrder)
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
}

// The built-in templates.
var AggregateTemplates = []AggregateTemplate{
	{ID: "lite", Label: "精简版", Desc: "基础代理组 + 国内外分流", Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final"}},
	{ID: "standard", Label: "标准版", Desc: "完整代理组 + 常用规则", Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final",
		"ai", "youtube", "google", "microsoft", "apple", "telegram", "github"}},
	{ID: "full", Label: "完整版", Desc: "全部功能 + 扩展规则集", Modules: []string{"select", "auto", "ads", "private", "cn", "global", "final",
		"ai", "youtube", "google", "microsoft", "apple", "telegram", "twitter", "meta", "discord", "social",
		"netflix", "disney", "western", "asian", "steam", "pcgames", "console", "github", "cloud", "dev", "storage",
		"paypal", "crypto", "edu", "news", "shopping"}},
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
			out = append(out, model.AggregateGroup{ID: m.ID})
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
