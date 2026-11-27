package i18n

// English of the settings on a phone (internal/view/settings/phone.go;
// docs/phone-ui.md).
func init() {
	for k, v := range map[string]string{
		"%s 毫秒": "%s ms",
		"1–65535；HTTP 和 SOCKS5 共用此端口": "1–65535; HTTP and SOCKS5 share this port",
		"代理页和代理组的测速都用这里的地址和超时":        "Latency tests on the Proxies page and in groups use this URL and timeout",
		"使用当前 Wi-Fi「%s」":              "Use current Wi-Fi “%s”",
		"切换到":                         "Switch to",
		"读不到当前 Wi-Fi 的名称时，请手动填写。":     "When the current Wi-Fi's name can't be read, type it in.",
		"连接 Wi-Fi「%s」时切换到":            "When connected to Wi-Fi “%s”, switch to",
	} {
		en[k] = v
	}
}
