package i18n

// English for the phone builds: the system VPN (internal/platform/tunnel)
// and the service's phone dependencies (internal/app/deps_phone.go).
func init() {
	for k, v := range map[string]string{
		"此版本没有可用的系统 VPN":       "This build has no system VPN",
		"手机上没有系统代理，请使用 VPN 模式": "Phones have no system proxy; use VPN mode",

		// iOS: the packet tunnel extension (internal/platform/tunnel/iosvpn).
		"系统 VPN 已断开":               "The system VPN disconnected",
		"系统 VPN 已断开（%s）":           "The system VPN disconnected (%s)",
		"系统 VPN 已断开，最近日志：\n%s":     "The system VPN disconnected. Recent log:\n%s",
		"系统 VPN 已断开（%s），最近日志：\n%s": "The system VPN disconnected (%s). Recent log:\n%s",
		"系统 VPN 未能停止：%w":           "The system VPN did not stop: %w",
		"无法把配置交给系统 VPN：%w":         "Cannot hand the config to the system VPN: %w",
		"无法启动系统 VPN：%w":            "Cannot start the system VPN: %w",
		"无法停止系统 VPN：%w":            "Cannot stop the system VPN: %w",
		"系统 VPN 未能接收配置：%w":         "The system VPN did not take the config: %w",
		"系统 VPN 的回复无法解析：%w":        "Cannot read the system VPN's answer: %w",
		"配置不是有效的 YAML：%w":          "The config is not valid YAML: %w",
		"控制接口的套接字路径有 %d 字节，超过 unix 套接字的 %d 字节上限：%s":                "The controller's socket path is %d bytes, over the %d a unix socket takes: %s",
		"无法创建系统 VPN 的内核目录：%w":                                      "Cannot create the system VPN's core directory: %w",
		"无法把 %s 复制给系统 VPN：%w":                                      "Cannot copy %s to the system VPN: %w",
		"App Group 容器不可用：App 须带有 Info.plist 中 %s 所指 App Group 的签名": "The App Group container is unavailable: the app must be signed with the App Group that %s in Info.plist names",
		"配置为空": "The config is empty",
	} {
		en[k] = v
	}
}
