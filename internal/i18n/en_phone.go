package i18n

// English for the phone builds: the system VPN (internal/platform/tunnel)
// and the service's phone dependencies (internal/app/deps_phone.go).
func init() {
	for k, v := range map[string]string{
		"此版本没有可用的系统 VPN":       "This build has no system VPN",
		"手机上没有系统代理，请使用 VPN 模式": "Phones have no system proxy; use VPN mode",
	} {
		en[k] = v
	}
}
