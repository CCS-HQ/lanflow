// Settings on a phone (docs/phone-ui.md): native list rows — the title on
// the left, the value and a chevron on the right, switches kept — and a
// tap that pushes an editor (a full-width input) or a list of choices
// with a check mark, in place of the desktop's bordered inputs, buttons
// and pop-up menus.

package settings

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// phoneSub is a page pushed inside a section on a phone: an editor of one
// text, a list of choices, or a page of its own (body).
type phoneSub struct {
	title string
	// The editor: text is edited in place and save gets it on 完成 and on
	// back; an error it returns keeps the page open and shows under the
	// input. multiline edits lines, mono shows monospace, numeric asks
	// for the number keyboard, readOnly only shows it.
	text                               string
	note, placeholder                  string
	multiline, mono, numeric, readOnly bool
	save                               func(text string) error
	err                                string
	// The choices: picking one calls choose and pops the page.
	options []string
	value   string
	choose  func(v string)
	// body draws a page of its own (a list of rules, its editor).
	body func(c *ui.Context)
	// back runs as the page pops, before it closes.
	back func()
}

// PhoneSub is env.PhoneStack: the pushed page's title and the section's
// name under it.
func (p *Page) PhoneSub() (string, string, bool) {
	if p.sub == nil {
		return "", "", false
	}
	return p.sub.title, p.TabTitle(), true
}

// PhonePop closes the pushed page; an editor saves what it holds first,
// and stays open when that fails.
func (p *Page) PhonePop() {
	sub := p.sub
	if sub == nil {
		return
	}
	if sub.save != nil && !sub.readOnly {
		if err := sub.save(sub.text); err != nil {
			sub.err = err.Error()
			return
		}
	}
	if sub.back != nil {
		sub.back()
	}
	p.sub = nil
}

// pushEdit pushes an editor of text; save gets the new text.
func (p *Page) pushEdit(title, note, text string, save func(string) error, opts ...func(*phoneSub)) {
	sub := &phoneSub{title: title, note: note, text: text, save: save}
	for _, o := range opts {
		o(sub)
	}
	p.sub = sub
}

func multiline(s *phoneSub) { s.multiline, s.mono = true, true }
func mono(s *phoneSub)      { s.mono = true }
func numeric(s *phoneSub)   { s.numeric, s.mono = true, false }
func readOnly(s *phoneSub)  { s.readOnly = true }
func placeholderOpt(ph string) func(*phoneSub) {
	return func(s *phoneSub) { s.placeholder = ph }
}

// pushChoice pushes the list of options with value checked.
func (p *Page) pushChoice(title, value string, options []string, choose func(string)) {
	p.sub = &phoneSub{title: title, value: value, options: options, choose: choose}
}

// pushPage pushes a page drawn by body.
func (p *Page) pushPage(title string, body func(c *ui.Context)) {
	p.sub = &phoneSub{title: title, body: body}
}

// phoneSubView draws the pushed page.
func (p *Page) phoneSubView(c *ui.Context) {
	t := c.Theme()
	sub := p.sub
	ui.Scroll(c.Key("sub:" + sub.title)).Fill().Children(func() {
		ui.Column(c).Padding(16, env.PhoneGap, 24, env.PhoneGap).Gap(8).Children(func() {
			switch {
			case sub.body != nil:
				sub.body(c)
			case sub.options != nil:
				phoneGroup(c, func() {
					for _, o := range sub.options {
						row := ui.ButtonBase(c.Key("opt:"+o)).Row().MinHeight(env.TouchSize).Padding(8, 14).AlignItems(ui.Center).FillWidth().
							Label(o).Role(ui.RoleRadio).Checked(o == sub.value).Cursor(ui.CursorPointer)
						if row.Pressed() {
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Text(c, o).FontSize(15).Grow(1).Shrink(1)
							if o == sub.value {
								ui.Box(c).TextColor(t.Accent).Children(func() { p.e.Kit.Icon(c, "check", 16) })
							}
						})
						if row.Clicked() {
							choose := sub.choose
							p.sub = nil
							if o != sub.value && choose != nil {
								choose(o)
							}
						}
					}
				})
			default:
				p.phoneEditor(c, sub)
			}
		})
	})
}

// phoneEditor is the editor of one text: a full-width input in a group,
// the note under it, 完成 under that.
func (p *Page) phoneEditor(c *ui.Context, sub *phoneSub) {
	t := c.Theme()
	phoneGroup(c, func() {
		ui.Box(c).Padding(6, 10).Children(func() {
			if sub.multiline {
				ui.TextArea(c.Key("sub-edit"), &sub.text).Font("monospace").FontSize(13).Lines(8, 24).ReadOnly(sub.readOnly).
					Label(sub.title).FillWidth().Border(0, ui.Transparent).Background(ui.Transparent)
				return
			}
			in := ui.TextInput(c.Key("sub-edit"), &sub.text).Label(sub.title).FillWidth().Height(36).FontSize(16).ReadOnly(sub.readOnly).
				Border(0, ui.Transparent).Background(ui.Transparent).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			if sub.mono {
				in.Font("monospace").FontSize(14)
			}
			if sub.numeric {
				in.Keyboard(ui.KeyboardNumber).FontFeatures("tnum")
			}
			if sub.placeholder != "" {
				in.Placeholder(sub.placeholder)
			}
			if in.Changed() {
				sub.err = ""
			}
			if in.Submitted() {
				p.PhonePop()
			}
		})
	})
	if sub.err != "" {
		ui.Text(c, sub.err).FontSize(13).TextColor(t.Danger).PaddingX(14)
	}
	if sub.note != "" {
		ui.Text(c, sub.note).FontSize(13).TextColor(t.TextMuted).PaddingX(14)
	}
	if !sub.readOnly {
		ui.Box(c).Margin(12, 0, 0, 0).Children(func() {
			phoneGroup(c, func() {
				if env.ActionRow(c, i18n.T("完成"), false, false).Clicked() {
					p.PhonePop()
				}
			})
		})
	}
}

// phoneGroup is a group of rows on a phone: the surface on the background,
// rounded, hairlines between the rows.
func phoneGroup(c *ui.Context, rows func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Radius(env.PhoneRadius).Background(t.Surface).Clip().Dividers(0.5, t.Border).Children(rows)
}

// navRow is env.NavRow with the page's kit.
func (p *Page) navRow(c *ui.Context, label, desc, value string) bool {
	return env.NavRow(c, p.e.Kit, label, desc, value).Clicked()
}

// valueOr is a value as a row shows it: placeholder when empty.
func valueOr(v, empty string) string {
	if strings.TrimSpace(v) == "" {
		return empty
	}
	return v
}

// linesValue is a list's value as a row shows it: how many lines it has,
// or 未设置.
func linesValue(text string) string {
	n := len(lines(text))
	if n == 0 {
		return i18n.T("未设置")
	}
	return i18n.Tf("%d 条", n)
}

// phoneErr is a field's error message as an editor's error, nil when none.
func phoneErr(msg string) error {
	if msg == "" {
		return nil
	}
	return errors.New(msg)
}

// phonePortRow is 代理端口 on a phone: the port and ›, its editor behind.
func (p *Page) phonePortRow(c *ui.Context, s model.Settings) {
	if p.navRow(c, i18n.T("代理端口"), i18n.T("HTTP 和 SOCKS5 共用此端口"), strconv.Itoa(s.MixedPort)) {
		p.pushEdit(i18n.T("代理端口"), i18n.T("1–65535；HTTP 和 SOCKS5 共用此端口"), strconv.Itoa(s.MixedPort), func(v string) error {
			p.port.text = v
			p.portErr = ""
			p.commitPort(p.current())
			return phoneErr(p.portErr)
		}, numeric)
	}
}

// phoneDelayRows are 测速地址 and 测速超时 on a phone.
func (p *Page) phoneDelayRows(c *ui.Context, s model.Settings) {
	if p.navRow(c, i18n.T("测速地址"), "", s.DelayURL) {
		p.pushEdit(i18n.T("测速地址"), i18n.T("节点经代理访问该地址，默认 www.gstatic.com/generate_204"), s.DelayURL, func(v string) error {
			p.delayURL.text, p.delayURLErr = v, ""
			p.commitDelayURL(p.current())
			return phoneErr(p.delayURLErr)
		}, mono)
	}
	ms := strconv.Itoa(s.DelayTimeoutMs)
	if p.navRow(c, i18n.T("测速超时"), "", i18n.Tf("%s 毫秒", ms)) {
		p.pushEdit(i18n.T("测速超时"), i18n.Tf("毫秒，%d–%d；超过即显示为超时", minDelayMs, maxDelayMs), ms, func(v string) error {
			p.delayMs.text, p.delayMsErr = v, ""
			p.commitDelayMs(p.current())
			return phoneErr(p.delayMsErr)
		}, numeric)
	}
}

// phoneWiFi is 按 Wi-Fi 切换订阅 on a phone: a row per rule (the Wi-Fi →
// the subscription), 添加规则 at the end, each pushing its editor, and
// 其他网络's choice.
func (p *Page) phoneWiFi(c *ui.Context, s model.Settings) {
	e := p.e
	note := i18n.T("连接到某个 Wi-Fi 时自动切换到对应的订阅")
	e.Kit.Section(c, i18n.T("按 Wi-Fi 切换订阅"), note, func() {
		names, byName, byID := p.subNames()
		for _, r := range s.SSIDRules {
			ssid := r.SSID
			sub := byID[r.SubscriptionID]
			desc := ""
			if sub == "" {
				sub, desc = i18n.T(wifiNoSwitch), i18n.T("对应的订阅已不存在")
			}
			if p.navRow(c, ssid, desc, "→ "+sub) {
				p.pushPage(ssid, func(c *ui.Context) { p.phoneWiFiRule(c, ssid) })
			}
		}
		if env.ActionRow(c, i18n.T("添加规则"), false, p.saving || len(names) == 0).Clicked() {
			p.f2.wifiSSID, p.f2.wifiErr = strings.TrimSpace(e.API.NetInfo().SSID), ""
			if len(names) > 0 {
				p.f2.wifiSub = names[0]
			}
			p.pushPage(i18n.T("添加规则"), p.phoneWiFiAdd)
		}
		def := byID[s.SSIDDefaultSub]
		if def == "" {
			def = i18n.T(wifiNoSwitch)
		}
		p.selectRow(c, i18n.T("其他网络"), i18n.T("连接到没有规则的 Wi-Fi 时使用的订阅；有线或无法读取名称的网络不切换"), def,
			append([]string{i18n.T(wifiNoSwitch)}, names...), func(v string) {
				s := p.current()
				s.SSIDDefaultSub = byName[v]
				p.save(s, "", nil)
			})
	})
}

// subNames are the subscriptions' names in order, and the maps between
// names and IDs.
func (p *Page) subNames() ([]string, map[string]string, map[string]string) {
	subs := p.e.API.Subscriptions()
	names := make([]string, 0, len(subs))
	byName, byID := map[string]string{}, map[string]string{}
	for _, sub := range subs {
		names = append(names, sub.Name)
		byName[sub.Name] = sub.ID
		byID[sub.ID] = sub.Name
	}
	return names, byName, byID
}

// phoneWiFiRule edits the rule of ssid: the subscription it switches to,
// and 删除规则.
func (p *Page) phoneWiFiRule(c *ui.Context, ssid string) {
	t := c.Theme()
	s := p.current()
	names, byName, byID := p.subNames()
	i := -1
	for n, r := range s.SSIDRules {
		if r.SSID == ssid {
			i = n
		}
	}
	if i < 0 {
		p.sub = nil // deleted
		return
	}
	cur := byID[s.SSIDRules[i].SubscriptionID]
	ui.Text(c, i18n.Tf("连接 Wi-Fi「%s」时切换到", ssid)).FontSize(13).TextColor(t.TextMuted).PaddingX(14)
	phoneGroup(c, func() {
		for _, name := range append([]string{i18n.T(wifiNoSwitch)}, names...) {
			if phoneCheckRow(c, p.e.Kit, name, name == cur || (cur == "" && name == i18n.T(wifiNoSwitch))) && name != cur {
				rules := append([]model.SSIDRule(nil), s.SSIDRules...)
				rules[i].SubscriptionID = byName[name]
				s.SSIDRules = rules
				p.save(s, "", nil)
			}
		}
	})
	ui.Box(c).Margin(16, 0, 0, 0).Children(func() {
		phoneGroup(c, func() {
			if env.ActionRow(c, i18n.T("删除规则"), true, p.saving).Clicked() {
				rules := append([]model.SSIDRule(nil), s.SSIDRules[:i]...)
				s.SSIDRules = append(rules, s.SSIDRules[i+1:]...)
				p.sub = nil
				p.save(s, i18n.Tf("已删除 Wi-Fi「%s」的规则", ssid), nil)
			}
		})
	})
}

// phoneWiFiAdd adds a rule: the Wi-Fi's name (the current one filled in),
// the subscription, 添加.
func (p *Page) phoneWiFiAdd(c *ui.Context) {
	t := c.Theme()
	e := p.e
	names, byName, _ := p.subNames()
	cur := strings.TrimSpace(e.API.NetInfo().SSID)
	ui.Text(c, i18n.T("Wi-Fi 名称（SSID）")).FontSize(13).TextColor(t.TextMuted).PaddingX(14)
	phoneGroup(c, func() {
		ui.Box(c).Padding(6, 10).Children(func() {
			in := ui.TextInput(c.Key("wifi-ssid"), &p.f2.wifiSSID).Placeholder(i18n.T("Wi-Fi 名称（SSID）")).Label(i18n.T("Wi-Fi 名称（SSID）")).
				FillWidth().Height(36).FontSize(16).Border(0, ui.Transparent).Background(ui.Transparent).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			if in.Changed() {
				p.f2.wifiErr = ""
			}
		})
		if cur != "" && p.f2.wifiSSID != cur && env.ActionRow(c, i18n.Tf("使用当前 Wi-Fi「%s」", cur), false, false).Clicked() {
			p.f2.wifiSSID, p.f2.wifiErr = cur, ""
		}
	})
	if cur == "" {
		ui.Text(c, i18n.T("读不到当前 Wi-Fi 的名称时，请手动填写。")).FontSize(13).TextColor(t.TextMuted).PaddingX(14)
	}
	ui.Text(c, i18n.T("切换到")).FontSize(13).TextColor(t.TextMuted).Padding(12, 14, 0, 14)
	phoneGroup(c, func() {
		for _, name := range names {
			if phoneCheckRow(c, e.Kit, name, name == p.f2.wifiSub) {
				p.f2.wifiSub = name
			}
		}
	})
	if p.f2.wifiErr != "" {
		ui.Text(c, p.f2.wifiErr).FontSize(13).TextColor(t.Danger).PaddingX(14)
	}
	ui.Box(c).Margin(16, 0, 0, 0).Children(func() {
		phoneGroup(c, func() {
			if env.ActionRow(c, i18n.T("添加"), false, p.saving || len(names) == 0).Clicked() {
				before := p.f2.wifiAdded
				p.addWiFiRule(p.current(), byName)
				if p.f2.wifiAdded != before {
					p.sub = nil
				}
			}
		})
	})
}

// phoneCheckRow is a row of a list of choices, checked when on; it
// reports a tap.
func phoneCheckRow(c *ui.Context, k env.Kit, label string, on bool) bool {
	t := c.Theme()
	row := ui.ButtonBase(c.Key("check:"+label)).Row().MinHeight(env.TouchSize).Padding(8, 14).AlignItems(ui.Center).FillWidth().
		Label(label).Role(ui.RoleRadio).Checked(on).Cursor(ui.CursorPointer)
	if row.Pressed() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Text(c, label).FontSize(15).Grow(1).Shrink(1)
		if on {
			ui.Box(c).TextColor(t.Accent).Children(func() { k.Icon(c, "check", 16) })
		}
	})
	return row.Clicked()
}
