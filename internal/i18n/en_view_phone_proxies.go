package i18n

// English of the 代理 page on a phone (internal/view/proxies/phone.go).
func init() {
	for k, v := range map[string]string{
		"测速当前组":              "Test This Group",
		"当前 %s · %d 个节点":     "Using %s · %d nodes",
		"当前 %s · %d 个节点|one": "Using %s · %d node",
		"%d 个节点|one":         "%d node",
		"没有代理组":              "No Proxy Groups",
	} {
		en[k] = v
	}
}
