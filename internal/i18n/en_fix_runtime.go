package i18n

// English of the texts added by the runtime fixes in internal/app
// (lifecycle.go, exits.go).
func init() {
	for k, v := range map[string]string{
		// internal/app/lifecycle.go
		"内核未能应用 TUN 状态，请查看内核日志。":              "The core did not apply the TUN state. Check the core log.",
		"内核重新加载配置失败：%v；恢复运行配置也失败：%v。请重新启动内核。": "The core failed to reload its configuration: %v; restoring the running configuration also failed: %v. Restart the core.",
		"正在等待系统释放代理端口":                        "Waiting for the system to release the proxy port",
		"TUN 模式下的连接刚关闭，系统要再占用端口 %d 最多约 %d 秒。": "Connections from TUN mode just closed; the system holds port %d for up to about %d more seconds.",
		"等待代理端口 %d 释放失败：%v。请稍后重试。":            "Waiting for proxy port %d to become available failed: %v. Try again shortly.",
		"%w。关闭系统代理失败（%v），请在系统设置中手动关闭代理。":      "%w. Turning the system proxy off failed (%v); turn the proxy off by hand in System Settings.",
		"%w。系统代理已关闭，内核恢复后请重新打开。":              "%w. The system proxy was turned off; turn it on again once the core runs.",
		"关闭系统代理失败（%v），请在系统设置中手动关闭代理。":         "Turning the system proxy off failed (%v); turn the proxy off by hand in System Settings.",
		"%s。%s可在「日志」中查看原因，或重新启动内核。":           "%s. %s See Logs for the cause, or restart the core.",
		// internal/app/exits.go
		"其他检测正在使用探测通道，请稍后再试。": "Another check is using the probe channel. Try again in a moment.",
	} {
		en[k] = v
	}
}
