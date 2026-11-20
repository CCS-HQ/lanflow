package i18n

// English of the configuration, sync and backup fixes (internal/app:
// configsync.go, configapply.go, backup.go, webdav.go, subs.go,
// overrides.go).
func init() {
	for k, v := range map[string]string{
		// internal/app/configapply.go
		"保存配置失败，已恢复原有配置：%v":           "Could not save the configuration; the previous one was restored: %v",
		"%w（恢复原有配置时又出错，可能只加载了一部分：%v）": "%w (restoring the previous configuration failed as well, so it may be partly loaded: %v)",
		"配置已加载，但有一步未完成":               "Configuration Loaded, One Step Incomplete",

		// internal/app/configsync.go
		"同步文件夹里有一个本机未能加载的版本，本机不会覆盖它。请先解决加载失败的原因，或关闭配置同步后重新开启并选择「用本机配置覆盖文件」。":               "The sync folder holds a version this machine could not load, and this machine will not write over it. Fix what kept it from loading first, or turn configuration sync off and on again and choose “Overwrite with this machine's”.",
		"同步文件由更新版本的澜流写入，或使用了本机不支持的加密方式，本机不能覆盖它。请先更新本机的澜流；如确实要弃用这份文件，请先在同步文件夹中手动移走它。":       "The sync file was written by a newer version of Lanflow, or is encrypted in a way this machine does not support, so this machine cannot write over it. Update Lanflow on this machine first; to give the file up anyway, move it out of the sync folder by hand.",
		"本机不会改写这份文件；在它被加载之前，本机的配置改动也不会写入同步文件夹。请先更新本机的澜流。":                                  "This machine will not rewrite the file, and until it loads, changes made on this machine are not written to the sync folder either. Update Lanflow on this machine first.",
		"稍后会自动重试加载。在此之前本机不会覆盖这份文件，本机的配置改动也不会写入同步文件夹；如要以本机配置为准，请关闭配置同步后重新开启并选择「用本机配置覆盖文件」。": "Loading will be tried again later. Until then this machine does not write over the file, and changes made on this machine are not written to the sync folder; to make this machine's configuration win, turn configuration sync off and on again and choose “Overwrite with this machine's”.",
		"本机不会覆盖这份文件，本机的配置改动也暂不写入同步文件夹；请修正文件，或关闭配置同步后重新开启并选择「用本机配置覆盖文件」。":                   "This machine does not write over the file, and changes made on this machine are not written to the sync folder for now. Correct the file, or turn configuration sync off and on again and choose “Overwrite with this machine's”.",

		// internal/app/backup.go, webdav.go
		"自动备份已暂停": "Automatic Backups Paused",
		"WebDAV 服务器地址是通过配置同步或导入更改的，自动备份已暂停。请在「设置 › 备份」中确认地址无误后，点一次「测试连接」或「立即备份」，自动备份即恢复。": "The WebDAV server address was changed by configuration sync or an import, so automatic backups are paused. Check the address in Settings › Backup, then press Test Connection or Back Up Now once and automatic backups resume.",
		"WebDAV 地址使用 http:// 时密码会以明文发送，只允许本机或局域网内的服务器；公网服务器请使用 https://。":                 "With an http:// WebDAV address the password is sent in the clear, so only servers on this machine or the local network are allowed; use https:// for a server on the internet.",

		// internal/app/subs.go, overrides.go
		"订阅在更新期间被修改了，本次下载的内容已放弃，请重新更新。": "The subscription was changed while it was updating; the download was discarded. Update it again.",
		"这条覆写在下载期间被修改了，请重新更新。":          "This override was changed while it was downloading. Update it again.",
	} {
		en[k] = v
	}
}
