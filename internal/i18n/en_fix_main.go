package i18n

// English of the texts added while integrating the review fixes.
func init() {
	for k, v := range map[string]string{
		// internal/view/agents.go
		"这是一项影响较大的操作（导入或切换订阅、覆写、内核启停、系统代理、TUN）。允许只对这次连接有效，不受「始终允许」影响。":                                                                                   "This is a high-impact action (importing or switching a subscription, overrides, starting or stopping the core, the system proxy, TUN). Allowing it holds for this connection only; “Always allow” does not cover it.",
		"一个脚本或其他程序通过 lanflow ctl 请求执行影响较大的操作（导入或切换订阅、覆写、内核启停、系统代理、TUN）。「仅本次」只对这一次运行有效；选择「始终允许」后，之后通过 lanflow ctl 运行的这类操作都不再询问，可在 设置 › 高级 › Agent 接入 撤销。": "A script or another program asks, through lanflow ctl, for a high-impact action (importing or switching a subscription, overrides, starting or stopping the core, the system proxy, TUN). “Just this time” holds for this one run; with “Always allow”, later actions of this kind run through lanflow ctl are no longer asked about. Revoke it in Settings › Advanced › Agent access.",
	} {
		en[k] = v
	}
}
