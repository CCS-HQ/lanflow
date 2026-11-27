package i18n

// English of the phone layouts of the 网络, 统计 and 检测 pages
// (internal/view/netpage, stats and probe with env.Env.Phone).
func init() {
	for k, v := range map[string]string{
		// internal/view/netpage/page.go
		"分别经代理和直连访问一次": "Each reached once via the proxy and once directly",
		"「%s」的操作":      "Actions for “%s”",
		"已连接，本机流量经澜流":  "Connected; this device's traffic goes through Lanflow",

		// internal/view/stats/phone.go
		"另有 %d 项未列出，搜索可以找到":     "%d more not listed; search to find them",
		"另有 %d 项未列出，搜索可以找到|one": "%d more not listed; search to find it",
	} {
		en[k] = v
	}
}
