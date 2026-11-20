package i18n

// English of the capture fixes (internal/app/capture.go).
func init() {
	for k, v := range map[string]string{
		"HAR 已导出，部分记录缺失": "HAR Exported, Some Records Missing",
		"有 %d 条记录在导出时已不在抓包缓冲区中（被更新的记录挤出或已清空），未写入文件。": "%d records were no longer in the capture buffer during the export (pushed out by newer records, or cleared) and are not in the file.",
	} {
		en[k] = v
	}
}
