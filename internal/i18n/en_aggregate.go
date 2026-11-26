package i18n

// English of aggregate subscriptions (docs/aggregate.md): the service
// (internal/app/aggregate.go), the editor's catalogs
// (internal/profile/aggregate_catalog.go) and the editor
// (internal/view/subs/aggregate.go).
func init() {
	for k, v := range map[string]string{
		// internal/app/aggregate.go, subs.go
		"这不是聚合订阅。":               "This is not an aggregate subscription.",
		"聚合订阅的设置丢失了，请重新添加来源。":    "The aggregate subscription's settings are missing. Add its sources again.",
		"请填写订阅名称。":               "Enter a subscription name.",
		"生成的配置未通过校验，聚合订阅没有保存：%v": "The generated config failed validation; the aggregate subscription was not saved: %v",
		"已更新，但有 %d 个来源失败：%s":     "Updated, but %d sources failed: %s",
		"引用的订阅已被删除":              "The subscription it reads from was deleted",
		"不能引用另一个聚合订阅":            "Cannot read from another aggregate subscription",
		"读取「%s」失败：%v":            "Could not read “%s”: %v",
		"聚合订阅更新失败：%v。仍在使用上次的配置。": "Aggregate subscription update failed: %v. Still using the previous config.",

		// internal/profile/aggregate_catalog.go
		"广告拦截":  "Ad blocking",
		"AI 服务": "AI services",
		"谷歌服务":  "Google",
		"微软服务":  "Microsoft",
		"苹果服务":  "Apple",
		"游戏平台":  "Games",

		// internal/profile/aggregate_catalog.go: aggFormats
		"来源不能超过 %d 个":     "No more than %d sources",
		"重命名规则不能超过 %d 条":  "No more than %d rename rules",
		"第 %d 个来源的 ID 重复": "Source %d has a duplicate ID",
		"第 %d 个来源的订阅链接无效：请填写以 http:// 或 https:// 开头的完整地址": "The subscription link of source %d is invalid: enter a full address starting with http:// or https://",
		"第 %d 个来源没有填写节点链接":                                "Source %d has no node links",
		"第 %d 个来源没有选择订阅":                                  "Source %d has no subscription chosen",
		"第 %d 个来源的类型无效：%q":                                "Source %d has an invalid type: %q",
		"至少需要一个启用的来源":                                     "At least one enabled source is needed",
		"第 %d 条重命名规则缺少要匹配的内容":                             "Rename rule %d has nothing to match",
		"第 %d 条重命名规则的正则表达式无效：%v":                          "The regular expression of rename rule %d is invalid: %v",
		"超过 %d 个字符":                                       "longer than %d characters",
		"节点超过 %d 个，多出的部分没有并入":                             "More than %d nodes; the rest were left out",
		"没有可用的节点：%s":                                      "No usable nodes: %s",
		"没有可用的节点：来源里没有能识别的节点，或全部被筛选掉了":                    "No usable nodes: the sources have no node Lanflow recognizes, or the filters removed them all",
		"内容为空": "The content is empty",
		"这个配置的部分节点在 proxy-providers 里，聚合订阅只能并入 proxies 里的节点": "Some of this config's nodes are in proxy-providers; an aggregate subscription can only merge the nodes under proxies",
		"无法识别内容：既不是 Clash/Mihomo 配置或节点，也不是节点分享链接":            "Unrecognized content: neither a Clash/Mihomo config or nodes nor node share links",
		"%d 条链接都无法识别或缺少必填项":                                  "None of the %d links was recognized or complete",

		// internal/view/subs/aggregate.go, page.go
		"聚合订阅":    "Aggregate subscription",
		"新建聚合订阅":  "New Aggregate Subscription",
		"新建聚合订阅…": "New Aggregate Subscription…",
		"编辑聚合订阅":  "Edit Aggregate Subscription",
		"编辑聚合…":   "Edit Aggregate…",
		"聚合":      "Aggregate",
		"更新（重新下载来源并生成）": "Update (download sources and rebuild)",
		"预览":         "Preview",
		"节点链接":       "Node links",
		"已有订阅":       "Existing subscription",
		"删除来源":       "Remove source",
		"还没有其他订阅可选。": "There are no other subscriptions to choose.",
		"使用这个订阅上次下载的内容；先更新它，再更新聚合订阅即可拿到新节点。": "Uses what this subscription last downloaded; update it, then the aggregate subscription, to get its new nodes.",
		"可选，例如「机场A · 」":  "Optional, e.g. “Provider A · ”",
		"名称前缀":           "Name prefix",
		"替换为":            "Replace with",
		"自动选择":           "Auto select",
		"正在下载来源并生成配置…":   "Downloading the sources and building the config…",
		"设置改过了，预览可能已过时。": "The settings changed; the preview may be out of date.",
		"还有 %d 个节点未列出":   "%d more nodes not listed",
		"生成的 YAML":       "Generated YAML",
	} {
		en[k] = v
	}
}
