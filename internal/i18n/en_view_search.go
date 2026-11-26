package i18n

// English of the global search (internal/view/palette.go).
func init() {
	for k, v := range map[string]string{
		"全局搜索":          "Search",
		"全局搜索 (⌘K)":     "Search (⌘K)",
		"全局搜索 (Ctrl+K)": "Search (Ctrl+K)",
		"搜索页面、操作、设置、节点、订阅…":     "Search pages, actions, settings, nodes, subscriptions…",
		"没有找到匹配的结果":             "No matches",
		"↑↓ 选择 · ↩ 打开 · Esc 关闭": "↑↓ to move · ↩ to open · Esc to close",
		"页面":           "Pages",
		"操作|搜索":        "Actions",
		"节点|搜索":        "Nodes",
		"在页面中搜索":       "Search in Page",
		"在「%s」中搜索「%s」": "Search %[1]s for “%[2]s”",
		"停止内核":         "Stop Core",
		"测试全部节点延迟":     "Test All Node Delays",
		"更新全部订阅":       "Update All Subscriptions",
		"导入订阅链接":       "Import Subscription Link",
		"关闭全部连接":       "Close All Connections",
		"关闭全部连接？":      "Close all connections?",
		"正在进行的下载、通话等会中断，应用会重新建立连接。": "Downloads, calls and other connections in progress are cut; apps connect again.",
		"关闭全部":          "Close All",
		"已关闭全部连接":       "All connections closed",
		"清空 DNS 缓存":     "Flush DNS Cache",
		"DNS 缓存已清空":     "DNS cache flushed",
		"清空 Fake-IP 缓存": "Flush Fake-IP Cache",
		"Fake-IP 缓存已清空": "Fake-IP cache flushed",
		"运行泄漏检查":        "Run Leak Check",
		"检测网站连通性":       "Test Site Connectivity",
		"清空日志":          "Clear Logs",
		"日志已清空":         "Logs cleared",
		"更新地理数据库":       "Update Geo Databases",
		"查看运行时配置":       "View Runtime Config",
		"切换「%s」到此节点":    "Use in %s",
		"切换到此订阅":        "Switch to this subscription",
		"已切换到订阅「%s」":    "Switched to subscription %s",
	} {
		en[k] = v
	}
}
