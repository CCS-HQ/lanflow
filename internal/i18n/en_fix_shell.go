package i18n

// English of the texts added with the fixes of the window shell and the
// settings page (internal/view, internal/view/settings).
func init() {
	for k, v := range map[string]string{
		// internal/view/settings/page.go
		"在文件管理器中显示": "Show in File Manager",
	} {
		en[k] = v
	}
}
