package settings

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Tailscale 兼容 › 配置…: the user's lists (model.TailscaleConfig) edited
// one item per line, on top of what detection finds.

// tailscaleDialog is the sheet's state.
type tailscaleDialog struct {
	open    bool
	session int
	form    tailscaleForm
}

// tailscaleForm is the sheet's fields, one entry per line.
type tailscaleForm struct {
	domains, routes, dns string
}

func tailscaleFormFrom(cfg model.TailscaleConfig) tailscaleForm {
	return tailscaleForm{domains: joinLines(cfg.Domains), routes: joinLines(cfg.Routes), dns: joinLines(cfg.DNS)}
}

// Field keys of the validation messages.
const (
	fieldTSDomains = "tsDomains"
	fieldTSRoutes  = "tsRoutes"
	fieldTSDNS     = "tsDNS"
)

// parse validates every line and returns the configuration and one
// message per field in error (the first problem of that field), with the
// rules the service applies (app.CheckTailscaleConfig).
func (f tailscaleForm) parse() (model.TailscaleConfig, map[string]string) {
	errs := map[string]string{}
	lines := func(field, text, bad string, check func([]string) []string) []string {
		var out []string
		for i, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if len(check([]string{l})) > 0 {
				if errs[field] == "" {
					errs[field] = fmt.Sprintf(bad, i+1, l)
				}
				continue
			}
			out = append(out, l)
		}
		return out
	}
	only := func(set func(*model.TailscaleConfig, []string)) func([]string) []string {
		return func(v []string) []string {
			var c model.TailscaleConfig
			set(&c, v)
			_, bad := app.CheckTailscaleConfig(c)
			return bad
		}
	}
	raw := model.TailscaleConfig{
		Domains: lines(fieldTSDomains, f.domains, i18n.T("第 %d 行不是有效的域名：%s"), only(func(c *model.TailscaleConfig, v []string) { c.Domains = v })),
		Routes:  lines(fieldTSRoutes, f.routes, i18n.T("第 %d 行不是有效的网段：%s"), only(func(c *model.TailscaleConfig, v []string) { c.Routes = v })),
		DNS:     lines(fieldTSDNS, f.dns, i18n.T("第 %d 行不是有效的 IPv4 地址：%s"), only(func(c *model.TailscaleConfig, v []string) { c.DNS = v })),
	}
	cfg, _ := app.CheckTailscaleConfig(raw)
	return cfg, errs
}

// tailscaleRowDesc words the setting's row: what detection found and what
// is configured.
func tailscaleRowDesc(on bool, st model.TailscaleState, cfg model.TailscaleConfig) string {
	var parts []string
	switch {
	case !on:
		parts = append(parts, i18n.T("关闭后 tailnet 地址和 MagicDNS 域名按订阅规则处理，开启 TUN 时通常无法访问"))
	case st.Running && st.ExitNode:
		parts = append(parts, i18n.Tf("已检测到 Tailscale（%s），正在使用出口节点：不能与澜流的 TUN 同时开启", st.Interface))
	case st.Running:
		s := i18n.Tf("已检测到 Tailscale（%s）：%d 个网段、%d 个域名直接经 Tailscale 访问", st.Interface, len(st.CIDRs), len(st.Domains))
		if st.Suffix != "" {
			s += " · " + st.Suffix
		}
		parts = append(parts, s)
	default:
		parts = append(parts, i18n.T("检测到 Tailscale 时，tailnet 地址和 MagicDNS 域名直接经 Tailscale 访问、不进入 TUN，系统代理也绕过它们"))
	}
	if !cfg.Empty() {
		parts = append(parts, i18n.Tf("已配置 %d 个域名、%d 个网段", len(cfg.Domains), len(cfg.Routes)))
	}
	return strings.Join(parts, " · ")
}

// tailscaleRow is the 「Tailscale 兼容」 setting: the switch and 配置….
func (p *Page) tailscaleRow(c *ui.Context, s model.Settings, st model.Status) {
	label := i18n.T("Tailscale 兼容")
	p.e.Kit.Row(c, label, tailscaleRowDesc(s.TailscaleCompat, st.Tailscale, s.Tailscale), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("ts-config"), i18n.T("配置…")).Clicked() {
				p.tailscale.form = tailscaleFormFrom(s.Tailscale)
				p.tailscale.session++
				p.tailscale.open = true
			}
			v := s.TailscaleCompat
			if ui.Switch(c.Key(label), &v).Label(label).Disabled(p.saving).Changed() {
				s.TailscaleCompat = v
				p.save(s, "", nil)
			}
		})
	})
}

func (p *Page) tailscaleDialog(c *ui.Context, t *ui.Theme, s model.Settings) {
	d := &p.tailscale
	if !d.open {
		return
	}
	cfg, errs := d.form.parse()
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key(d.session)).Width(env.DialogWidth(c, 640)).Gap(10).Children(func() {
			ui.Text(c, i18n.T("Tailscale 兼容配置")).FontSize(15).Bold()
			ui.Text(c, i18n.T("每行一项，叠加在自动检测的结果之上。tailnet 网段（100.64.0.0/10、fd7a:115c:a1e0::/48）和 MagicDNS 无需填写；这里只补充检测不到的拆分 DNS 域名和子网路由。")).FontSize(12).TextColor(t.TextMuted)
			ui.Scroll(c.Key("ts-lists")).MaxHeight(env.DialogMaxHeight(c, 200)).Shrink(1).Children(func() {
				ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
					ui.Column(c).Gap(10).Grow(1).Basis(0).Children(func() {
						p.atrustField(c, t, "ts-domains", &d.form.domains, i18n.T("域名"), i18n.T("经 Tailscale 访问的域名后缀（如 tailnet 后缀、拆分 DNS 域名）：不分配 fake-ip、不嗅探，由下方 DNS 或 MagicDNS 解析"), errs[fieldTSDomains])
						p.atrustField(c, t, "ts-dns", &d.form.dns, i18n.T("DNS"), i18n.T("解析上述域名的 IPv4 DNS；留空则使用 MagicDNS（100.100.100.100）"), errs[fieldTSDNS])
					})
					ui.Column(c).Gap(10).Grow(1).Basis(0).Children(func() {
						p.atrustField(c, t, "ts-routes", &d.form.routes, i18n.T("子网路由"), i18n.T("经 Tailscale 访问的网段：不进入 TUN，直接交给 Tailscale；含系统 DNS 的网段会留在 TUN 内"), errs[fieldTSRoutes])
					})
				})
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					d.open = false
				}
				ui.PrimaryButton(c, i18n.T("保存")).Disabled(len(errs) > 0 || p.saving).OnClick(func() {
					s.Tailscale = cfg
					d.open = false
					p.save(s, i18n.T("Tailscale 兼容配置已保存"), nil)
				})
			})
		})
	})
}
