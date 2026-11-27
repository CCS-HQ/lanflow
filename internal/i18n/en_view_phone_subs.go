package i18n

// English of the phone layout of the 订阅, 聚合订阅 and 规则 pages
// (internal/view/subs/phone.go, internal/view/rules/phone.go).
func init() {
	for k, v := range map[string]string{
		// internal/view/subs/phone.go
		"从订阅链接导入，或选择一个 .yaml / .yml 配置文件。": "Import from a subscription link, or choose a .yaml / .yml config file.",
	} {
		en[k] = v
	}
}
