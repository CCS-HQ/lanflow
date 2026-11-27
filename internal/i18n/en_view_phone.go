package i18n

// English of the phone's shell and 首页 (internal/view/shell_phone.go,
// internal/view/overview/phone.go; docs/phone-ui.md).
func init() {
	for k, v := range map[string]string{
		// internal/view/shell_phone.go
		"工具":     "Tools",
		"首页":     "Home",
		"活动|标签页": "Activity",
		"地图":     "Map",
		// internal/view/overview/phone.go, page.go
		"已连接":    "Connected",
		"未连接":    "Not connected",
		"正在连接…":  "Connecting…",
		"连接出错":   "Connection error",
		"已运行 %s": "Up for %s",
		"筛选连接":   "Filter connections",
		"全部断开…":  "Disconnect All…",
		"直连模式：流量不经过节点，无出口可显示": "Direct mode: traffic takes no node, so there is no exit to show",
		"轻点按钮连接":     "Tap the button to connect",
		"连接 VPN":     "Connect VPN",
		"断开 VPN":     "Disconnect VPN",
		"VPN 已连接":    "VPN connected",
		"VPN 已断开":    "VPN disconnected",
		"当前节点，打开代理页": "Current node, open Proxies",
		"Mihomo 内核意外退出，自动重启也失败了，VPN 已断开。": "The Mihomo core quit unexpectedly and could not be restarted. The VPN is off.",
		"Mihomo 内核意外退出：%s。VPN 已断开。":       "The Mihomo core quit unexpectedly: %s. The VPN is off.",
	} {
		en[k] = v
	}
}
