package i18n

// English of 设置 › 常规 and 内核 on a phone (internal/view/settings/
// phone_general.go and the phone rows of caches.go, configsync.go,
// syncpass.go, backup.go, feedback.go, kernel.go and mirror.go;
// docs/phone-ui.md).
func init() {
	for k, v := range map[string]string{
		"%d 个手动节点的凭据不在本机的安全存储中（%s），请在「代理」页编辑并重新填写凭据。": "Credentials of %d manual nodes are not in this device's secure storage (%s). Edit them on the Proxies page and enter the credentials again.",
		"%d 个订阅的链接不在本机的安全存储中（%s），请在「订阅」页编辑并重新输入链接。":   "Links of %d subscriptions are not in this device's secure storage (%s). Edit them on the Subscriptions page and enter the links again.",
		"保存在本机的安全存储中": "Kept in this device's secure storage",
		"反馈会以 POST {上报地址}/v1/reports 发送到你自己部署的服务；留空时反馈只能保存到文件。":                             "Feedback is sent as POST {endpoint}/v1/reports to a service you deploy yourself; when empty, feedback can only be saved to a file.",
		"可选；以 Authorization: Bearer 发送，保存在本机的安全存储中":                                         "Optional; sent as Authorization: Bearer and kept in this device's secure storage",
		"同步文件中设置了 WebDAV 密码，但本机的安全存储里没有；需要备份到 WebDAV 时请重新输入密码。":                             "The sync file sets a WebDAV password, but it is not in this device's secure storage; enter the password again before backing up to WebDAV.",
		"同步文件改回只写凭据引用，加密的链接和 WebDAV 密码从文件中移除，本机安全存储里的派生密钥也会删除。其他设备之后需要自己的凭据存储里有链接，否则要重新输入。": "The sync file goes back to credential references only: the encrypted links and WebDAV password are removed from it, and the derived key in this device's secure storage is deleted. Other devices then need the links in their own credential store, or must enter them again.",
		"地址须以 http:// 或 https:// 开头": "The address must start with http:// or https://",
		"基于 Mihomo（GPL-3.0）":         "Based on Mihomo (GPL-3.0)",
		"导入设置…":                      "Import Settings…",
		"导出设置…":                      "Export Settings…",
		"已设置":                        "Set",
		"把设置、覆写、自定义规则和订阅保存为 lanflow.yaml，放进会同步的文件夹，在多台设备间保持一致；订阅链接留在本机的安全存储中": "Saves settings, overrides, custom rules and subscriptions as lanflow.yaml in a synced folder to keep devices in step; subscription links stay in this device's secure storage",
		"更改文件夹…": "Change Folder…",
		"本机安全存储中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。": "The key derived from the passphrase is deleted from this device's secure storage. Subscription links already here are kept; links added or changed in the sync file later need the passphrase again.",
		"本机没有可用的安全存储：密码经混淆后保存在应用的数据目录，并非加密":                             "No secure storage on this device: the password is obfuscated, not encrypted, in the app's data folder",
		"清除令牌":         "Clear Token",
		"清除密码":         "Clear Password",
		"用于下载内核和地理数据库": "For downloading the core and the geo databases",
		"输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机的安全存储，之后启动无需再次输入。": "Enter the sync passphrase set on another device to unlock the subscription links in the sync file. Once it is right, the links go into this device's secure storage and later launches won't ask again.",
		"连接后才能更新地理数据库": "Connect to update the geo databases",
	} {
		en[k] = v
	}
}
