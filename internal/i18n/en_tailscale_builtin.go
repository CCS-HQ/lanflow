package i18n

// English for 内置 Tailscale: the service (internal/app/tailscale_builtin.go,
// tailscale.go) and the settings row and sheet
// (internal/view/settings/tailscale_builtin.go).
func init() {
	for k, v := range map[string]string{
		// ---- internal/app ----
		"内置 Tailscale": "Built-in Tailscale",
		"auth key 格式不正确（不能含空白，通常以 tskey- 开头）。": "The auth key is malformed (no spaces; it usually starts with tskey-).",
		"内置 Tailscale 设置未能生效":                  "Built-in Tailscale settings could not be applied",
		"内置 Tailscale 需要登录":                    "Built-in Tailscale needs a login",
		"请在「设置 › TUN › 内置 Tailscale」中点「登录…」，在浏览器里把这台设备加入 tailnet；也可以填写 auth key。": "Click Log In… under Settings > TUN > Built-in Tailscale to add this device to the tailnet in the browser, or enter an auth key.",
		"内置 Tailscale 等待批准": "Built-in Tailscale awaits approval",
		"这台设备已登录，需要 tailnet 管理员在管理后台批准后才能连上。":               "This device is logged in; a tailnet admin must approve it in the admin console before it connects.",
		"内置 Tailscale 已连上 tailnet":                          "Built-in Tailscale connected to the tailnet",
		"经过澜流的流量（TUN、系统代理）现在可以访问 tailnet 的地址和 MagicDNS 域名。": "Traffic through Lanflow (TUN, system proxy) can now reach tailnet addresses and MagicDNS names.",
		"检测到 Tailscale App 正在运行，内置 Tailscale 已暂停（二者只能用一个）。退出 Tailscale App 后，内置节点会自动启用；或关闭内置 Tailscale，改用「Tailscale 兼容」。": "The Tailscale app is running, so Built-in Tailscale is paused (only one can be used). Quit the Tailscale app and the built-in node starts by itself; or turn Built-in Tailscale off and use Tailscale Compatibility.",
		"内置 Tailscale 需要登录：在浏览器打开 %s 把这台设备加入 tailnet，或设置 auth key（只能由用户在设置页填写）。":                                          "Built-in Tailscale needs a login: open %s in a browser to add this device to the tailnet, or set an auth key (only the user can enter it, in Settings).",
		"内置 Tailscale 需要登录，登录链接稍后出现在设置页。":                                                                                 "Built-in Tailscale needs a login; the login link appears in Settings shortly.",
		"内置 Tailscale 已登录，等待 tailnet 管理员批准这台设备。":                                                                          "Built-in Tailscale is logged in and waits for a tailnet admin to approve this device.",
		"内核未运行：内置 Tailscale 随内核启动。":                                                                                       "The core is not running: Built-in Tailscale starts with the core.",
		"内置 Tailscale 报告了错误：%s": "Built-in Tailscale reported an error: %s",
		"出口节点只用于自定义规则中指向 LANFLOW-TAILSCALE 的流量；其他流量仍按订阅规则走。":                                                                                                      "The exit node only carries traffic that custom rules send to LANFLOW-TAILSCALE; everything else follows the subscription's rules.",
		"已接受子网路由，但没有填写网段：请在「Tailscale 兼容 › 配置…」的子网路由中填写要经 tailnet 访问的网段（设置项 Tailscale.Routes），规则才会把它们交给内置节点。":                                                     "Subnet routes are accepted but no ranges are entered: add the ranges to reach through the tailnet under Subnet Routes in Tailscale Compatibility > Configure… (setting Tailscale.Routes), so rules hand them to the built-in node.",
		"内置模式的局限：只有经过澜流的流量（TUN、系统代理、设置了代理的应用）能访问 tailnet；本机不会以自己的身份成为 tailnet 节点，别的设备无法经 tailnet 访问本机的服务（SSH、文件共享），Taildrop 和 Tailscale SSH 不可用；退出澜流即断开 tailnet。": "Limits of the built-in mode: only traffic through Lanflow (TUN, system proxy, apps set to use the proxy) reaches the tailnet; this machine is not a tailnet node of its own, so other devices cannot reach its services (SSH, file sharing) through the tailnet, and Taildrop and Tailscale SSH are not available; quitting Lanflow leaves the tailnet.",
		"检测到 Tailscale App，内置 Tailscale 已暂停": "Tailscale app detected, Built-in Tailscale paused",
		"Tailscale App 和内置 Tailscale 只能用一个。退出 Tailscale App 后内置节点会自动启用；也可以关闭内置 Tailscale。": "Only one of the Tailscale app and Built-in Tailscale can be used. Quit the Tailscale app and the built-in node starts by itself; or turn Built-in Tailscale off.",
		"Tailscale App 已退出，内置 Tailscale 已启用": "Tailscale app quit, Built-in Tailscale on",
		"经过澜流的流量改由内置节点访问 tailnet。":           "Traffic through Lanflow now reaches the tailnet through the built-in node.",

		// ---- internal/view/settings/tailscale_builtin.go ----
		"设备名只能含字母、数字和连字符":                         "The device name may only contain letters, digits and hyphens",
		"请填写 http(s):// 开头的完整地址":                  "Enter a full address starting with http(s)://",
		"请填写出口节点的 tailnet IP、设备名，或 auto:any":      "Enter the exit node's tailnet IP or device name, or auto:any",
		"检测到 Tailscale App 正在运行，内置节点已暂停（二者只能用一个）": "The Tailscale app is running; the built-in node is paused (only one can be used)",
		"未登录：点「登录…」在浏览器中把这台设备加入 tailnet":          "Not logged in: click Log In… to add this device to the tailnet in the browser",
		"已登录，等待 tailnet 管理员批准这台设备":                "Logged in, waiting for a tailnet admin to approve this device",
		"已连上 tailnet":   "Connected to the tailnet",
		"已停止":           "Stopped",
		"正在连接 tailnet…": "Connecting to the tailnet…",
		"由澜流的内核加入 tailnet，无需运行 Tailscale App；只有经过澜流的流量能访问 tailnet": "Lanflow's core joins the tailnet itself, without the Tailscale app; only traffic through Lanflow reaches the tailnet",
		"登录…": "Log In…",
		"澜流的内核自己作为设备加入 tailnet，不用再运行 Tailscale App。局限：只有经过澜流的流量（TUN、系统代理、设置了代理的应用）能访问 tailnet；本机不会以自己的身份成为 tailnet 节点，别的设备无法经 tailnet 访问本机的服务（SSH、文件共享），Taildrop 和 Tailscale SSH 不可用；退出澜流即断开 tailnet。检测到 Tailscale App 运行时内置节点会暂停。": "Lanflow's core joins the tailnet as a device itself, so the Tailscale app need not run. Limits: only traffic through Lanflow (TUN, system proxy, apps set to use the proxy) reaches the tailnet; this machine is not a tailnet node of its own, so other devices cannot reach its services (SSH, file sharing) through the tailnet, and Taildrop and Tailscale SSH are not available; quitting Lanflow leaves the tailnet. While the Tailscale app runs, the built-in node is paused.",
		"在浏览器中登录": "Log In in Browser",
		"设备名":     "Device Name",
		"在 tailnet 中显示的名字；留空为「本机名-lanflow」":                             "The name shown in the tailnet; empty = \"<computer name>-lanflow\"",
		"可选：tailnet IP、设备名或 auto:any；只用于自定义规则中指向 LANFLOW-TAILSCALE 的流量": "Optional: tailnet IP, device name or auto:any; only for traffic custom rules send to LANFLOW-TAILSCALE",
		"控制服务器": "Control Server",
		"可选：Headscale 等自建控制服务器的地址；留空使用 Tailscale 官方": "Optional: the address of a self-hosted control server such as Headscale; empty uses Tailscale's",
		"接受子网路由": "Accept Subnet Routes",
		"要经 tailnet 访问的网段和拆分 DNS 域名请填在「Tailscale 兼容 › 配置…」中，两种模式共用": "Enter the ranges and split DNS domains to reach through the tailnet under Tailscale Compatibility > Configure…; both modes use them",
		"出口节点": "Exit Node",
		"可选：在 Tailscale 管理后台生成，填写后无需在浏览器登录；保存在系统钥匙串中。已登录的设备会忽略它": "Optional: generated in the Tailscale admin console; with it no browser login is needed. Kept in the system keychain. A device already logged in ignores it",
		"已设置；保存在系统钥匙串中。已登录的设备会忽略它":                               "Set; kept in the system keychain. A device already logged in ignores it",
		"Auth key 已保存":        "Auth key saved",
		"Auth key 已清除":        "Auth key cleared",
		"退出登录…":               "Log Out…",
		"退出内置 Tailscale 的登录？": "Log Built-in Tailscale out?",
		"内置节点会换用新的状态目录、重新登录（或使用 auth key）；原来的设备仍留在 tailnet 管理后台中，可以在那里删除。": "The built-in node moves to a new state directory and logs in again (or uses the auth key); the old device stays in the tailnet's admin console, where it can be removed.",
		"退出登录":                "Log Out",
		"已退出内置 Tailscale 的登录": "Built-in Tailscale logged out",
		"内置 Tailscale 配置已保存":  "Built-in Tailscale configuration saved",
	} {
		en[k] = v
	}
}
