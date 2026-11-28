package i18n

// English of the texts added for Linux desktops.
func init() {
	for k, v := range map[string]string{
		// internal/view/settings/wifi.go
		"当前 Wi-Fi 名称不可用：没有连接 Wi-Fi，或无法从 NetworkManager 读取": "The current Wi-Fi name is not available: not connected to Wi-Fi, or NetworkManager could not be read",
		// internal/view/settings/advanced.go
		"内核的出站流量绑定到这个网卡，例如 eth0 或 wlan0；留空自动选择（interface-name）": "Binds the core's outbound traffic to this interface, e.g. eth0 or wlan0; empty = automatic (interface-name)",
		// internal/view/logs/page.go
		"在文件管理器中显示日志文件": "Show Log File in File Manager",
		// internal/view/env/tun.go, overview/page.go, settings/page.go
		"Linux 版暂不支持 TUN 模式，请先使用系统代理": "TUN mode is not available on Linux yet; use the system proxy for now",
		"Linux 版暂不支持": "Not available on Linux yet",
		"Linux 版暂不支持 TUN 模式，将在后续版本提供；请先使用系统代理": "TUN mode is not available on Linux yet and will come in a later version; use the system proxy for now",
	} {
		en[k] = v
	}
}
