package i18n

// English for Tailscale 兼容: the service (internal/app/tailscale.go) and
// the settings row and sheet (internal/view/settings/tailscale.go).
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/tailscale.go ----
		"检测到 Tailscale，已启用兼容模式":                                   "Tailscale detected, compatibility mode on",
		"tailnet 地址和 MagicDNS 域名将直接经 Tailscale 访问，不经过澜流的节点和 TUN。": "Tailnet addresses and MagicDNS names now go straight through Tailscale, bypassing Lanflow's nodes and TUN.",
		"Tailscale 正在使用出口节点":                                      "Tailscale is using an exit node",
		"出口节点和澜流的 TUN 都会接管全部流量，同时开启会互相冲突。请在 Tailscale 中关闭出口节点，或关闭澜流的 TUN 只用系统代理。": "An exit node and Lanflow's TUN both take over all traffic and conflict when both are on. Turn the exit node off in Tailscale, or turn Lanflow's TUN off and use the system proxy only.",
		"Tailscale 兼容设置未能生效": "Tailscale compatibility settings could not be applied",
		"Tailscale 正在使用出口节点，接管了全部流量。请在 Tailscale 中关闭出口节点后再开启澜流的 TUN；不关闭时可只用系统代理，tailnet 仍可直接访问":                                                                 "Tailscale is using an exit node and has taken over all traffic. Turn the exit node off in Tailscale before turning on Lanflow's TUN; otherwise use the system proxy only, and the tailnet stays reachable directly",
		"Tailscale 兼容已关闭：tailnet 地址和 MagicDNS 域名按订阅规则处理，开启 TUN 时通常无法访问。":                                                                                        "Tailscale compatibility is off: tailnet addresses and MagicDNS names follow the subscription's rules and are usually unreachable while TUN is on.",
		"未检测到 Tailscale。Tailscale 连上后，澜流会在 10 秒内自动应用兼容配置。":                                                                                                      "Tailscale not detected. Once Tailscale connects, Lanflow applies the compatibility configuration within 10 seconds.",
		"Tailscale 正在使用出口节点：它和澜流的 TUN 不能同时开启。可关闭出口节点，或只用澜流的系统代理。":                                                                                               "Tailscale is using an exit node: it cannot run together with Lanflow's TUN. Turn the exit node off, or use Lanflow's system proxy only.",
		"未能确定 tailnet 的 MagicDNS 后缀（未找到 tailscale 命令行，也没有读到它的解析器）。经系统代理访问 tailnet 域名时，请把后缀（如 tail1234.ts.net）加入「Tailscale 兼容 › 配置…」的域名（设置项 Tailscale.Domains）。": "The tailnet's MagicDNS suffix is unknown (no tailscale command line found and none of its resolvers read). To reach tailnet names through the system proxy, add the suffix (such as tail1234.ts.net) to Domains under Tailscale Compatibility > Configure… (setting Tailscale.Domains).",
		"Windows 上不读取 Tailscale 的子网路由和拆分 DNS：需要时请在「Tailscale 兼容 › 配置…」中填写（设置项 Tailscale.Routes、Tailscale.Domains）。":                                             "On Windows, Tailscale's subnet routes and split DNS are not read: enter them under Tailscale Compatibility > Configure… when needed (settings Tailscale.Routes, Tailscale.Domains).",

		// ---- internal/view/settings/tailscale.go ----
		"Tailscale 兼容":      "Tailscale Compatibility",
		"Tailscale 兼容配置":    "Tailscale Compatibility Configuration",
		"Tailscale 兼容配置已保存": "Tailscale compatibility configuration saved",
		"关闭后 tailnet 地址和 MagicDNS 域名按订阅规则处理，开启 TUN 时通常无法访问":                                                        "When off, tailnet addresses and MagicDNS names follow the subscription's rules and are usually unreachable while TUN is on",
		"已检测到 Tailscale（%s），正在使用出口节点：不能与澜流的 TUN 同时开启":                                                              "Tailscale detected (%s) using an exit node: it cannot run together with Lanflow's TUN",
		"已检测到 Tailscale（%s）：%d 个网段、%d 个域名直接经 Tailscale 访问":                                                         "Tailscale detected (%s): %d ranges and %d domains go straight through Tailscale",
		"检测到 Tailscale 时，tailnet 地址和 MagicDNS 域名直接经 Tailscale 访问、不进入 TUN，系统代理也绕过它们":                                "While Tailscale runs, tailnet addresses and MagicDNS names go straight through Tailscale, stay out of the TUN and bypass the system proxy",
		"每行一项，叠加在自动检测的结果之上。tailnet 网段（100.64.0.0/10、fd7a:115c:a1e0::/48）和 MagicDNS 无需填写；这里只补充检测不到的拆分 DNS 域名和子网路由。": "One entry per line, added to what is detected. The tailnet ranges (100.64.0.0/10, fd7a:115c:a1e0::/48) and MagicDNS need no entry; add only split DNS domains and subnet routes detection cannot see.",
		"经 Tailscale 访问的域名后缀（如 tailnet 后缀、拆分 DNS 域名）：不分配 fake-ip、不嗅探，由下方 DNS 或 MagicDNS 解析":                        "Domain suffixes reached through Tailscale (the tailnet suffix, split DNS domains): no fake IP, not sniffed, resolved by the DNS below or MagicDNS",
		"解析上述域名的 IPv4 DNS；留空则使用 MagicDNS（100.100.100.100）":                                                         "IPv4 DNS servers for these domains; empty uses MagicDNS (100.100.100.100)",
		"子网路由": "Subnet Routes",
		"经 Tailscale 访问的网段：不进入 TUN，直接交给 Tailscale；含系统 DNS 的网段会留在 TUN 内": "Ranges reached through Tailscale: kept out of the TUN and handed to Tailscale; a range holding the system DNS stays in the TUN",
		"第 %d 行不是有效的 IPv4 地址：%s": "Line %d is not a valid IPv4 address: %s",
	} {
		en[k] = v
	}
}
