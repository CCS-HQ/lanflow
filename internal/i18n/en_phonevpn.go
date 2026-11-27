package i18n

// English for Android's system VPN (internal/platform/tunnel/tunnel_android.go).
func init() {
	for k, v := range map[string]string{
		"未获得建立 VPN 的许可":            "Lanflow was not allowed to set up a VPN",
		"VPN 已被关闭（系统设置或其他 VPN 接管）": "The VPN was turned off (in the system's settings, or another VPN took over)",
		"系统结束了 VPN 服务":             "The system ended the VPN service",
		"Mihomo 内核已在运行，请先停止":       "The Mihomo core is already running; stop it first",
		"无法启动 Mihomo 内核 %s：%v":     "Could not start the Mihomo core %s: %v",
		"Mihomo 内核意外退出（%s）":        "The Mihomo core exited unexpectedly (%s)",
		"，最近日志：\n":                 "; recent log:\n",
	} {
		en[k] = v
	}
}
