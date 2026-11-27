package i18n

// English of the DNS, 嗅探, 覆写 and 高级 tabs on a phone
// (internal/view/settings/{dns,sniffer,override,advanced,phone_forms}.go;
// docs/phone-ui.md).
func init() {
	for k, v := range map[string]string{
		"下载链接指向的 YAML 作为一条覆写；之后可在它的页面中「更新」。":    "Downloads the YAML the link points to as an override; later, tap Update on its page.",
		"两个字母的国家代码，例如 CN":                       "A two-letter country code, e.g. CN",
		"内核的出站流量绑定到这个网卡；留空自动选择（interface-name）": "The core's outbound traffic is bound to this interface; leave empty to choose automatically (interface-name)",
		"创建后打开它的 YAML 编辑器。":                     "Its YAML editor opens once it's created.",
		"删除此项":   "Delete",
		"删除覆写":   "Delete Override",
		"启用":     "Enable",
		"已启用":    "Enabled",
		"添加服务器…": "Add Server…",
		"端口用逗号分隔，可写范围，例如 80, 8080-8880": "Ports separated by commas, ranges allowed, e.g. 80, 8080-8880",
		"编辑 YAML…": "Edit YAML…",
	} {
		en[k] = v
	}
}
