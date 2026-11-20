package i18n

// English of the texts added by the runtime fixes in internal/app
// (lifecycle.go, exits.go).
func init() {
	for k, v := range map[string]string{
		// internal/app/lifecycle.go
		"%w。关闭系统代理失败（%v），请在系统设置中手动关闭代理。": "%w. Turning the system proxy off failed (%v); turn the proxy off by hand in System Settings.",
		"%w。系统代理已关闭，内核恢复后请重新打开。":         "%w. The system proxy was turned off; turn it on again once the core runs.",
		"关闭系统代理失败（%v），请在系统设置中手动关闭代理。":    "Turning the system proxy off failed (%v); turn the proxy off by hand in System Settings.",
		"%s。%s可在「日志」中查看原因，或重新启动内核。":      "%s. %s See Logs for the cause, or restart the core.",
		// internal/app/exits.go
		"其他检测正在使用探测通道，请稍后再试。": "Another check is using the probe channel. Try again in a moment.",
	} {
		en[k] = v
	}
}
