package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// 内置 Tailscale (model.TailscaleBuiltin): the row in 设置 › TUN, with the
// switch, 登录… while the node waits for a login, and 配置…: the device
// name, the coordination server, subnet routes, the exit node, the auth
// key (keychain) and 退出登录.

// tsBuiltinDialog is the sheet's state.
type tsBuiltinDialog struct {
	open    bool
	session int
	form    tsBuiltinForm
	key     string // the auth key being typed; never filled from the keychain
	keyN    int
}

type tsBuiltinForm struct {
	hostname, controlURL, exitNode string
	acceptRoutes                   bool
}

func tsBuiltinFormFrom(b model.TailscaleBuiltin) tsBuiltinForm {
	return tsBuiltinForm{hostname: b.Hostname, controlURL: b.ControlURL, exitNode: b.ExitNode, acceptRoutes: b.AcceptRoutes}
}

// apply puts the form into b and reports the problems, one per field.
func (f tsBuiltinForm) apply(b model.TailscaleBuiltin) (model.TailscaleBuiltin, map[string]string) {
	b.Hostname, b.ControlURL, b.ExitNode, b.AcceptRoutes = f.hostname, f.controlURL, f.exitNode, f.acceptRoutes
	out, bad := app.CheckTailscaleBuiltin(b)
	errs := map[string]string{}
	for _, m := range bad {
		switch {
		case strings.HasPrefix(m, "TailscaleBuiltin.Hostname"):
			errs["host"] = i18n.T("设备名只能含字母、数字和连字符")
		case strings.HasPrefix(m, "TailscaleBuiltin.ControlURL"):
			errs["control"] = i18n.T("请填写 http(s):// 开头的完整地址")
		case strings.HasPrefix(m, "TailscaleBuiltin.ExitNode"):
			errs["exit"] = i18n.T("请填写出口节点的 tailnet IP、设备名，或 auto:any")
		}
	}
	return out, errs
}

// tsBuiltinStateText words the node's state.
func tsBuiltinStateText(st model.TailscaleBuiltinState) string {
	switch st.State {
	case model.TSStatePausedForApp:
		return i18n.T("检测到 Tailscale App 正在运行，内置节点已暂停（二者只能用一个）")
	case model.TSStateNeedsLogin:
		return i18n.T("未登录：点「登录…」在浏览器中把这台设备加入 tailnet")
	case model.TSStateMachineAuth:
		return i18n.T("已登录，等待 tailnet 管理员批准这台设备")
	case model.TSStateRunning:
		s := i18n.T("已连上 tailnet")
		if len(st.Addresses) > 0 {
			s += " · " + strings.Join(st.Addresses, ", ")
		}
		if st.LoginName != "" {
			s += " · " + st.LoginName
		}
		return s
	case model.TSStateStopped:
		return i18n.T("已停止")
	case model.TSStateStarting:
		return i18n.T("正在连接 tailnet…")
	}
	return ""
}

// tsBuiltinRowDesc words the row.
func tsBuiltinRowDesc(b model.TailscaleBuiltin, st model.TailscaleState, coreRunning bool) string {
	if !b.Enabled {
		return i18n.T("由澜流的内核加入 tailnet，无需运行 Tailscale App；只有经过澜流的流量能访问 tailnet")
	}
	parts := []string{}
	if h := st.Builtin.Hostname; h != "" {
		parts = append(parts, h)
	}
	switch {
	case st.Builtin.State == model.TSStatePausedForApp:
		parts = append(parts, tsBuiltinStateText(st.Builtin))
	case !coreRunning:
		parts = append(parts, i18n.T("内核未运行"))
	default:
		if s := tsBuiltinStateText(st.Builtin); s != "" {
			parts = append(parts, s)
		}
	}
	if st.Builtin.Error != "" {
		parts = append(parts, st.Builtin.Error)
	}
	return strings.Join(parts, " · ")
}

// tailscaleBuiltinRow is the 「内置 Tailscale」 setting.
func (p *Page) tailscaleBuiltinRow(c *ui.Context, s model.Settings, st model.Status) {
	label := i18n.T("内置 Tailscale")
	running := st.Core == model.CoreRunning
	b := st.Tailscale.Builtin
	p.e.Kit.Row(c, label, tsBuiltinRowDesc(s.TailscaleBuiltin, st.Tailscale, running), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if s.TailscaleBuiltin.Enabled && b.State == model.TSStateNeedsLogin && b.LoginURL != "" {
				u := b.LoginURL
				ui.PrimaryButton(c.Key("tsb-login"), i18n.T("登录…")).Tooltip(u).OnClick(func() { _ = mygo.Shell.OpenExternal(u) })
			}
			if ui.Button(c.Key("tsb-config"), i18n.T("配置…")).Clicked() {
				p.tsBuiltin.form = tsBuiltinFormFrom(s.TailscaleBuiltin)
				p.tsBuiltin.key = ""
				p.tsBuiltin.session++
				p.tsBuiltin.open = true
			}
			v := s.TailscaleBuiltin.Enabled
			if ui.Switch(c.Key(label), &v).Label(label).Disabled(p.saving).Changed() {
				s.TailscaleBuiltin.Enabled = v
				p.save(s, "", nil)
			}
		})
	})
}

func (p *Page) tailscaleBuiltinDialog(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	d := &p.tsBuiltin
	if !d.open {
		return
	}
	e := p.e
	next, errs := d.form.apply(s.TailscaleBuiltin)
	b := st.Tailscale.Builtin
	keySet := e.API.TailscaleAuthKeySet()
	field := func(key string, text *string, label, hint, ph, errMsg string) {
		ui.Column(c).Gap(3).Children(func() {
			ui.Text(c, label).FontSize(13)
			ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted)
			ui.TextInput(c.Key(key), text).Placeholder(ph).Label(label)
			if errMsg != "" {
				ui.Text(c, errMsg).FontSize(11).TextColor(t.Danger)
			}
		})
	}
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key(d.session)).Width(env.DialogWidth(c, 640)).Gap(10).Children(func() {
			ui.Text(c, i18n.T("内置 Tailscale")).FontSize(15).Bold()
			ui.Text(c, i18n.T("澜流的内核自己作为设备加入 tailnet，不用再运行 Tailscale App。局限：只有经过澜流的流量（TUN、系统代理、设置了代理的应用）能访问 tailnet；本机不会以自己的身份成为 tailnet 节点，别的设备无法经 tailnet 访问本机的服务（SSH、文件共享），Taildrop 和 Tailscale SSH 不可用；退出澜流即断开 tailnet。检测到 Tailscale App 运行时内置节点会暂停。")).FontSize(12).TextColor(t.TextMuted)
			if s.TailscaleBuiltin.Enabled {
				if txt := tsBuiltinStateText(b); txt != "" {
					ui.Text(c, txt).FontSize(12)
				}
				if b.State == model.TSStateNeedsLogin && b.LoginURL != "" {
					u := b.LoginURL
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, u).FontSize(11).Font("monospace").TextColor(t.TextMuted).Shrink(1)
						ui.Button(c, i18n.T("在浏览器中登录")).OnClick(func() { _ = mygo.Shell.OpenExternal(u) })
					})
				}
				if b.Error != "" {
					ui.Text(c, b.Error).FontSize(11).TextColor(t.Danger)
				}
			}
			ui.Scroll(c.Key("tsb-fields")).MaxHeight(env.DialogMaxHeight(c, 260)).Shrink(1).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
						ui.Column(c).Gap(10).Grow(1).Basis(0).Children(func() {
							field("tsb-host", &d.form.hostname, i18n.T("设备名"), i18n.T("在 tailnet 中显示的名字；留空为「本机名-lanflow」"), b.Hostname, errs["host"])
							field("tsb-exit", &d.form.exitNode, i18n.T("出口节点"), i18n.T("可选：tailnet IP、设备名或 auto:any；只用于自定义规则中指向 LANFLOW-TAILSCALE 的流量"), "", errs["exit"])
						})
						ui.Column(c).Gap(10).Grow(1).Basis(0).Children(func() {
							field("tsb-control", &d.form.controlURL, i18n.T("控制服务器"), i18n.T("可选：Headscale 等自建控制服务器的地址；留空使用 Tailscale 官方"), "https://controlplane.tailscale.com", errs["control"])
							ui.Column(c).Gap(3).Children(func() {
								ui.Switch(c.Key("tsb-routes"), &d.form.acceptRoutes).Label(i18n.T("接受子网路由"))
								ui.Text(c, i18n.T("要经 tailnet 访问的网段和拆分 DNS 域名请填在「Tailscale 兼容 › 配置…」中，两种模式共用")).FontSize(11).TextColor(t.TextMuted)
							})
						})
					})
					ui.Column(c).Gap(3).Children(func() {
						ui.Text(c, i18n.T("Auth key")).FontSize(13)
						hint := i18n.T("可选：在 Tailscale 管理后台生成，填写后无需在浏览器登录；保存在系统钥匙串中。已登录的设备会忽略它")
						if keySet {
							hint = i18n.T("已设置；保存在系统钥匙串中。已登录的设备会忽略它")
						}
						ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted)
						ui.Row(c.Key("tsb-key-" + strconv.Itoa(d.keyN))).Gap(8).AlignItems(ui.Center).Children(func() {
							ph := "tskey-auth-…"
							if keySet {
								ph = "••••••••"
							}
							in := ui.TextInput(c.Key("tsb-key"), &d.key).Width(240).Password().Placeholder(ph).Label(i18n.T("Auth key"))
							save := func() {
								k := strings.TrimSpace(d.key)
								if k == "" {
									return
								}
								d.key = ""
								d.keyN++
								e.Run(func() error { return e.API.SetTailscaleAuthKey(k) }, i18n.T("Auth key 已保存"))
							}
							if in.Submitted() {
								save()
							}
							ui.Button(c, i18n.T("保存")).Disabled(strings.TrimSpace(d.key) == "").OnClick(save)
							if keySet {
								ui.Button(c, i18n.T("清除")).OnClick(func() {
									e.Run(func() error { return e.API.SetTailscaleAuthKey("") }, i18n.T("Auth key 已清除"))
								})
							}
						})
					})
				})
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if s.TailscaleBuiltin.Enabled || b.LoginName != "" {
					ui.Button(c, i18n.T("退出登录…")).OnClick(func() {
						e.Confirm(i18n.T("退出内置 Tailscale 的登录？"), i18n.T("内置节点会换用新的状态目录、重新登录（或使用 auth key）；原来的设备仍留在 tailnet 管理后台中，可以在那里删除。"), i18n.T("退出登录"), true, func() {
							e.Run(e.API.LogoutTailscale, i18n.T("已退出内置 Tailscale 的登录"))
						})
					})
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					d.open = false
				}
				ui.PrimaryButton(c, i18n.T("保存")).Disabled(len(errs) > 0 || p.saving).OnClick(func() {
					s.TailscaleBuiltin = next
					d.open = false
					p.save(s, i18n.T("内置 Tailscale 配置已保存"), nil)
				})
			})
		})
	})
}
