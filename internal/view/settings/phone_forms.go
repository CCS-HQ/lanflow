// The forms of DNS, 嗅探, 覆写 and 高级 on a phone (docs/phone-ui.md):
// what they share beyond phone.go — a row that only shows a value, a
// choice that is read-only at times, a list of pairs with its editor, a
// group with a footnote under it and an input of a pushed page.

package settings

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// phoneSection is a section on a phone with its footnote under the group
// (the explanation a desktop puts beside the title).
func (p *Page) phoneSection(c *ui.Context, title, foot string, body func()) {
	t := c.Theme()
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		p.e.Kit.Section(c, title, "", body)
		if foot != "" {
			ui.Text(c, foot).FontSize(12).TextColor(t.TextMuted).PaddingX(14)
		}
	})
}

// phoneValueRow is a row that shows a value it cannot change here: the
// label, the value muted, no chevron.
func (p *Page) phoneValueRow(c *ui.Context, label, desc, value string) {
	t := c.Theme()
	p.e.Kit.Row(c, label, desc, func() {
		ui.Text(c, value).FontSize(15).TextColor(t.TextMuted).SingleLine().MaxWidth(170)
	})
}

// phoneChoiceRow is a choice of options: the value and ›, the list of
// choices behind it; only the value when ro.
func (p *Page) phoneChoiceRow(c *ui.Context, label, desc, value string, options []string, ro bool, set func(string)) {
	if ro {
		p.phoneValueRow(c, label, desc, value)
		return
	}
	if p.navRow(c, label, desc, value) {
		p.pushChoice(label, value, options, set)
	}
}

// phoneSwitchRow is a row with a switch bound to on.
func (p *Page) phoneSwitchRow(c *ui.Context, key, label, desc string, on *bool, disabled bool) bool {
	changed := false
	p.e.Kit.Row(c, label, desc, func() {
		changed = ui.Switch(c.Key(key), on).Label(label).Disabled(disabled).Changed()
	})
	return changed
}

// phoneInput is a pushed page's full-width input in a group of its own,
// under its label.
func phoneInput(c *ui.Context, key, label string, text *string, placeholder string, mono, ro bool) ui.Element {
	t := c.Theme()
	ui.Text(c, label).FontSize(13).TextColor(t.TextMuted).Padding(4, 14, 0, 14)
	var in ui.Element
	phoneGroup(c, func() {
		ui.Box(c).Padding(6, 10).Children(func() {
			in = ui.TextInput(c.Key(key), text).Label(label).FillWidth().Height(36).FontSize(16).ReadOnly(ro).
				Border(0, ui.Transparent).Background(ui.Transparent).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			if mono {
				in.Font("monospace").FontSize(14)
			}
			if placeholder != "" {
				in.Placeholder(placeholder)
			}
		})
	})
	return in
}

// phoneActions is a group of action rows set apart from what is above.
func phoneActions(c *ui.Context, rows func()) {
	ui.Box(c).Margin(12, 0, 0, 0).Children(func() { phoneGroup(c, rows) })
}

// kvSpec names a list of pairs on a phone: the editor's title, the two
// fields with their hints, and the rows that add and delete.
type kvSpec struct {
	title                string
	keyLabel, valueLabel string
	keyHint, valueHint   string
	add, del             string
}

// phoneKVList is a list of key → value pairs on a phone: a row a pair,
// 添加 at the end, each pushing the pair's editor; only the rows when ro.
func (p *Page) phoneKVList(c *ui.Context, kvs *[]model.KV, sp kvSpec, ro bool) {
	t := c.Theme()
	if ro && len(*kvs) == 0 {
		ui.Text(c, i18n.T("未设置")).FontSize(15).TextColor(t.TextMuted).Padding(12, 14)
	}
	for i, kv := range *kvs {
		ui.Column(c.Key(i)).FillWidth().Children(func() {
			// The pair on two lines, the key over → the value: both are
			// long (a rule set, a URL), too long to share one.
			label := valueOr(kv.Key, i18n.T("未设置"))
			desc := "→ " + valueOr(kv.Value, i18n.T("未设置"))
			if ro {
				p.e.Kit.Row(c, label, desc, nil)
				return
			}
			if p.navRow(c, label, desc, "") {
				p.pushKV(kvs, i, sp)
			}
		})
	}
	if !ro && env.ActionRow(c, sp.add, false, false).Clicked() {
		*kvs = append(*kvs, model.KV{})
		p.pushKV(kvs, len(*kvs)-1, sp)
	}
}

// pushKV pushes the editor of pair i; a pair left empty goes as it pops.
func (p *Page) pushKV(kvs *[]model.KV, i int, sp kvSpec) {
	p.pushPage(sp.title, func(c *ui.Context) { p.phoneKVEdit(c, kvs, i, sp) })
	p.sub.back = func() {
		if i < len(*kvs) && strings.TrimSpace((*kvs)[i].Key) == "" && strings.TrimSpace((*kvs)[i].Value) == "" {
			*kvs = append((*kvs)[:i], (*kvs)[i+1:]...)
		}
	}
}

// phoneKVEdit edits pair i: its two fields, 完成, and 删除 in red.
func (p *Page) phoneKVEdit(c *ui.Context, kvs *[]model.KV, i int, sp kvSpec) {
	if i >= len(*kvs) {
		p.sub = nil
		return
	}
	kv := &(*kvs)[i]
	phoneInput(c, "kv-k", sp.keyLabel, &kv.Key, sp.keyHint, true, false)
	phoneInput(c, "kv-v", sp.valueLabel, &kv.Value, sp.valueHint, true, false)
	phoneActions(c, func() {
		if env.ActionRow(c, i18n.T("完成"), false, false).Clicked() {
			p.PhonePop()
		}
	})
	phoneActions(c, func() {
		if env.ActionRow(c, sp.del, true, false).Clicked() {
			*kvs = append((*kvs)[:i], (*kvs)[i+1:]...)
			p.sub = nil
		}
	})
}

// hostsText is hosts as the lines its editor shows: a domain, a space,
// its address.
func hostsText(kvs []model.KV) string {
	var b strings.Builder
	for _, kv := range kvs {
		b.WriteString(kv.Key + " " + kv.Value + "\n")
	}
	return b.String()
}

// parseHosts reads hostsText's lines back.
func parseHosts(text string) []model.KV {
	var kvs []model.KV
	for _, l := range lines(text) {
		fs := strings.Fields(l)
		kv := model.KV{Key: fs[0]}
		if len(fs) > 1 {
			kv.Value = fs[1]
		}
		kvs = append(kvs, kv)
	}
	return kvs
}
