package settings

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
	"lanflow/internal/view/env"
)

// 用口令加密链接 (docs/config-sync.md), in 设置 › 常规 › 配置同步: the
// switch that seals the sync file's links and WebDAV password with a
// passphrase, changing it, forgetting it on this machine, and the dialog
// that sets, changes or enters it. The passphrase is typed here only:
// the API methods are window-only, an agent can neither set nor enter it.

// passMode is what the passphrase dialog does.
type passMode int

const (
	passSet    passMode = iota // turn sealing on
	passChange                 // a new passphrase
	passUnlock                 // enter it on this device
)

// passDialog is the passphrase dialog's state, read and written on the
// UI thread only (a job's outcome arrives through Env.Then).
type passDialog struct {
	mode          passMode
	open          bool
	pass, confirm string
	err           string // shown in the dialog: a wrong passphrase keeps it open
	busy          bool
	session       int // bumps the inputs' keys so they start empty
}

func (p *Page) openPassDialog(mode passMode) {
	n := 0
	if p.sync.pass != nil {
		n = p.sync.pass.session + 1
	}
	d := &passDialog{mode: mode, open: true, session: n}
	p.sync.pass = d
	if p.e.Phone { // a page in place of the dialog; back closes it
		title, _, _ := p.passTexts(mode)
		p.pushPage(title, p.phonePassPage)
		p.sub.back = func() { d.open = false }
	}
}

// passTexts are the passphrase dialog's title, its note and its button
// for mode.
func (p *Page) passTexts(mode passMode) (title, intro, okLabel string) {
	title, intro, okLabel = i18n.T("设置同步口令"), i18n.T("订阅链接和 WebDAV 密码会用这个口令加密后写进同步文件。其他设备开启同步后输入同一口令即可取回。"), i18n.T("加密")
	switch mode {
	case passChange:
		title, intro, okLabel = i18n.T("修改同步口令"), i18n.T("所有链接会用新口令重新加密并写回同步文件；其他设备读到新文件后，需要输入新口令。"), i18n.T("修改口令")
	case passUnlock:
		title = i18n.T("输入同步口令")
		switch {
		case p.e.Phone:
			intro = i18n.T("输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机的安全存储，之后启动无需再次输入。")
		case p.e.Mac:
			intro = i18n.T("输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机钥匙串，之后启动无需再次输入。")
		default:
			intro = i18n.T("输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机凭据管理器，之后启动无需再次输入。")
		}
		okLabel = i18n.T("解锁")
	}
	return title, intro, okLabel
}

// phonePassPage is the passphrase dialog as a phone's page: the note,
// the inputs in a group, the button as a row. It closes when the job
// succeeds.
func (p *Page) phonePassPage(c *ui.Context) {
	d := p.sync.pass
	if d == nil || (!d.open && !d.busy) {
		p.sub = nil
		return
	}
	_, intro, okLabel := p.passTexts(d.mode)
	phoneFoot(c, intro)
	if d.mode != passUnlock {
		phoneDangerNote(c, i18n.T("口令不会保存在任何地方，澜流也无法找回。口令丢失后，同步文件中的链接无法恢复，只能在各设备上重新输入。同步文件放在第三方云盘上，请使用足够长、不与其他服务共用的口令（至少 8 个字符）。"))
	}
	phoneSpacer(c)
	submitted := false
	phoneGroup(c, func() {
		s := strconv.Itoa(d.session)
		submitted = phoneField(c, "sync-pass-1-"+s, i18n.T("同步口令"), i18n.T("同步口令"), &d.pass, true, d.busy).Submitted()
		if d.mode != passUnlock {
			submitted = phoneField(c, "sync-pass-2-"+s, i18n.T("再次输入口令"), i18n.T("再次输入口令"), &d.confirm, true, d.busy).Submitted() || submitted
		}
	})
	if d.err != "" {
		phoneDangerNote(c, d.err)
	}
	phoneSpacer(c)
	phoneGroup(c, func() {
		label := okLabel
		if d.busy {
			label = i18n.T("正在处理")
		}
		if env.ActionRow(c, label, false, d.busy || d.pass == "").Clicked() || submitted {
			p.submitPass()
		}
	})
}

// syncPassphraseRows draws the 用口令加密链接 row and its actions.
func (p *Page) syncPassphraseRows(c *ui.Context, st model.ConfigSyncStatus, busy bool) {
	e := p.e
	desc := i18n.T("把订阅链接和 WebDAV 密码用口令加密后写进同步文件，其他设备（包括 Windows）输入同一口令即可取回。口令本身不保存；丢失口令后，文件中的链接无法恢复，只能在各设备上重新输入。")
	if !st.Encrypted {
		if e.Mac {
			desc = i18n.T("关闭时同步文件只写钥匙串引用，另一台设备需要重新输入订阅链接。开启后，链接和 WebDAV 密码用口令加密写进文件；口令丢失后无法恢复这些链接。")
		} else {
			desc = i18n.T("关闭时同步文件只写凭据引用，另一台设备需要重新输入订阅链接。开启后，链接和 WebDAV 密码用口令加密写进文件；口令丢失后无法恢复这些链接。")
		}
	}
	e.Kit.Row(c, i18n.T("用口令加密链接"), desc, func() {
		on := st.Encrypted
		if ui.Switch(c.Key("sync-seal"), &on).Label(i18n.T("用口令加密链接")).Disabled(busy).Changed() {
			if on {
				p.openPassDialog(passSet)
			} else {
				confirmBody := i18n.T("同步文件改回只写钥匙串引用，加密的链接和 WebDAV 密码从文件中移除，本机钥匙串里的派生密钥也会删除。其他设备之后需要自己的钥匙串里有链接，否则要重新输入。")
				doneMsg := i18n.T("已关闭口令加密；同步文件改回钥匙串引用")
				if !e.Mac {
					confirmBody = i18n.T("同步文件改回只写凭据引用，加密的链接和 WebDAV 密码从文件中移除，本机凭据管理器里的派生密钥也会删除。其他设备之后需要自己的凭据存储里有链接，否则要重新输入。")
					doneMsg = i18n.T("已关闭口令加密；同步文件改回凭据引用")
				}
				if e.Phone {
					confirmBody = i18n.T("同步文件改回只写凭据引用，加密的链接和 WebDAV 密码从文件中移除，本机安全存储里的派生密钥也会删除。其他设备之后需要自己的凭据存储里有链接，否则要重新输入。")
				}
				e.Confirm(i18n.T("关闭口令加密？"),
					confirmBody,
					i18n.T("关闭加密"), true, func() {
						p.syncJob(func() error { return e.API.SetSyncPassphrase("") }, doneMsg)
					})
			}
		}
	})
	if !st.Encrypted || st.Locked {
		return
	}
	forget := func() {
		forgetBody := i18n.T("本机钥匙串中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。")
		if !e.Mac {
			forgetBody = i18n.T("本机凭据管理器中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。")
		}
		if e.Phone {
			forgetBody = i18n.T("本机安全存储中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。")
		}
		e.Confirm(i18n.T("在本机忘记同步口令？"),
			forgetBody,
			i18n.T("忘记口令"), false, func() {
				p.syncJob(func() error { return e.API.ForgetSyncPassphrase() }, i18n.T("已在本机忘记同步口令"))
			})
	}
	passDesc := i18n.T("已在本机保存由口令派生的密钥，启动时无需再次输入。修改口令后，其他设备需要输入新口令。")
	if e.Phone { // the row says what is kept, each action is a row
		infoRow(c, i18n.T("同步口令"), passDesc, "")
		if env.ActionRow(c, i18n.T("修改口令…"), false, busy).Clicked() {
			p.openPassDialog(passChange)
		}
		if env.ActionRow(c, i18n.T("在本机忘记口令"), false, busy).Clicked() {
			forget()
		}
		return
	}
	e.Kit.Row(c, i18n.T("同步口令"), passDesc, func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, i18n.T("修改口令…")).Disabled(busy).OnClick(func() { p.openPassDialog(passChange) })
			ui.Button(c, i18n.T("在本机忘记口令")).Disabled(busy).OnClick(forget)
		})
	})
}

// submitPass runs the dialog's action off the UI thread (the key
// derivation takes a moment); errors stay in the dialog.
func (p *Page) submitPass() {
	e := p.e
	d := p.sync.pass
	if d == nil || d.busy {
		return
	}
	mode, pass, confirm := d.mode, d.pass, d.confirm
	switch {
	case pass == "":
		d.err = i18n.T("请输入同步口令。")
	case mode != passUnlock && utf8.RuneCountInString(pass) < syncfile.MinPassphraseLen:
		d.err = syncfile.ErrShortPassphrase.Error()
	case mode != passUnlock && pass != confirm:
		d.err = i18n.T("两次输入的口令不一致。")
	default:
		d.err, d.busy = "", true
	}
	if !d.busy {
		return
	}
	ok := i18n.T("同步口令正确，已解锁订阅链接")
	switch mode {
	case passSet:
		ok = i18n.T("已用口令加密同步文件中的链接")
	case passChange:
		ok = i18n.T("同步口令已修改；其他设备需要输入新口令")
	}
	e.Then(func() error {
		if mode == passUnlock {
			return e.API.UnlockSync(pass)
		}
		return e.API.SetSyncPassphrase(pass)
	}, func(err error) {
		// d is the dialog the job started from, which Escape may have
		// closed meanwhile: its error is then toasted instead.
		closed := !d.open
		defer func() {
			if closed && err != nil {
				e.Toast(d.err, true)
			}
		}()
		d.busy = false
		switch {
		case errors.Is(err, syncfile.ErrWrongPassphrase):
			d.err = i18n.T("同步口令不正确，请重新输入。")
			d.pass = ""
			d.session++
		case err != nil:
			d.err = err.Error()
		default:
			d.open, d.pass, d.confirm = false, "", ""
			e.Toast(ok, false)
		}
	})
}

// passphraseDialog draws the dialog that sets, changes or enters the
// sync passphrase.
func (p *Page) passphraseDialog(c *ui.Context, t *ui.Theme) {
	d := p.sync.pass
	if d != nil && !d.open && !d.busy {
		p.sync.pass = nil
		d = nil
	}
	if d == nil {
		return
	}
	if p.e.Phone { // a page there (phonePassPage)
		return
	}
	busy, errText, session := d.busy, d.err, d.session
	title, intro, okLabel := p.passTexts(d.mode)
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key("sync-pass")).Width(env.DialogWidth(c, 440)).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Text(c, intro).FontSize(12).TextColor(t.TextMuted)
			if d.mode != passUnlock {
				ui.Text(c, i18n.T("口令不会保存在任何地方，澜流也无法找回。口令丢失后，同步文件中的链接无法恢复，只能在各设备上重新输入。同步文件放在第三方云盘上，请使用足够长、不与其他服务共用的口令（至少 8 个字符）。")).FontSize(12).TextColor(t.Danger)
			}
			key := func(k string) *ui.Context { return c.Key(k + "-" + strconv.Itoa(session)) }
			in := ui.TextInput(key("sync-pass-1"), &d.pass).Password().Disabled(busy).Placeholder(i18n.T("同步口令")).Label(i18n.T("同步口令"))
			submitted := in.Submitted()
			if d.mode != passUnlock {
				in2 := ui.TextInput(key("sync-pass-2"), &d.confirm).Password().Disabled(busy).Placeholder(i18n.T("再次输入口令")).Label(i18n.T("再次输入口令"))
				submitted = submitted || in2.Submitted()
			}
			if errText != "" {
				ui.Text(c, errText).FontSize(12).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Disabled(busy).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, okLabel).Disabled(busy || d.pass == "").Clicked() || submitted {
					p.submitPass() // the work runs in e.Run, off the UI thread
				}
			})
		})
	})
}
