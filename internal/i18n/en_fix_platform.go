package i18n

// English of the texts added with the platform fixes (main.go).
func init() {
	for k, v := range map[string]string{
		// main.go
		"澜流无法启动：%s\n\n请检查数据目录是否可以读写；问题仍然存在时，请带上这段信息反馈。": "Lanflow cannot start: %s\n\nCheck that the data directory can be read and written. If the problem remains, include this message when you report it.",
	} {
		en[k] = v
	}
}
