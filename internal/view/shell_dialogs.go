// The shell's dialogs: confirmations, the error detail behind a toast,
// the invalid-config alert and the system-proxy recovery.

package view

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// confirmReq is a pending confirmation dialog.
type confirmReq struct {
	title, body, ok string
	danger          bool
	onOK            func()
	// once: a question that is one and the same however often it is asked
	// (退出); "" = every request is its own, even with the same words.
	once string
}

// detailReq is the full text behind an error toast's 「查看详情」.
type detailReq struct {
	title, text string
}

// ask is Env.Confirm; safe from any goroutine (the quit question of the
// split comes on the RPC event goroutine). Confirmations queue, as the
// agents' consent requests do: one asked while another is open shows
// after it, and each gets its answer; before, the newer one replaced
// the open one, whose onOK then never ran. The same question asked again
// while it waits (退出 chosen twice) is asked once. onOK runs on the UI
// thread.
func (s *Shell) ask(title, body, okLabel string, danger bool, onOK func()) {
	s.askReq(&confirmReq{title: title, body: body, ok: okLabel, danger: danger, onOK: onOK})
}

func (s *Shell) askReq(req *confirmReq) {
	s.mu.Lock()
	dup := false
	for _, q := range s.asked {
		dup = dup || q.same(req)
	}
	if !dup {
		s.asked = append(s.asked, req)
	}
	s.mu.Unlock()
	s.Redraw()
}

func (r *confirmReq) same(o *confirmReq) bool { return r.once != "" && r.once == o.once }

// takeAsked moves what ask queued to the confirmations the UI thread
// shows, dropping a question that already waits there.
func (s *Shell) takeAsked() {
	s.mu.Lock()
	asked := s.asked
	s.asked = nil
	s.mu.Unlock()
next:
	for _, req := range asked {
		for _, q := range s.confirms {
			if q.same(req) {
				continue next
			}
		}
		s.confirms = append(s.confirms, req)
	}
}

// confirmDialog draws the oldest confirmation waiting.
func (s *Shell) confirmDialog(c *ui.Context) {
	s.takeAsked()
	if len(s.confirms) == 0 {
		return
	}
	cf := s.confirms[0]
	if s.confirm != cf {
		s.confirm, s.confirmOpen = cf, true // the next of the queue comes up
	}
	answer := ui.AlertDialog(c, &s.confirmOpen, cf.title, cf.body, i18n.T("取消"), cf.ok)
	if answer != 1 && s.confirmOpen {
		return
	}
	s.confirms = s.confirms[1:]
	s.confirm = nil
	if len(s.confirms) > 0 {
		s.Redraw()
	}
	if answer == 1 && cf.onOK != nil {
		cf.onOK()
	}
}

// AskQuit shows the quit confirmation; quit runs on the UI thread if the
// user agrees.
func (s *Shell) AskQuit(quit func()) {
	s.askReq(&confirmReq{title: i18n.T("退出后将关闭系统代理。"), body: i18n.T("由澜流开启的系统代理会恢复为关闭，内核也会停止。"), ok: i18n.T("退出"), onOK: quit, once: "quit"})
}

// errorToast shows an error toast with a short first line and 「查看详情」
// opening the full text (docs/design.md §6.5).
func (s *Shell) errorToast(msg, detail string) {
	short, full := splitError(msg)
	if detail != "" {
		full = strings.TrimSpace(msg + "\n\n" + detail)
	}
	s.toast(toastReq{msg: short, isError: true, action: i18n.T("查看详情"), onAction: func() { s.showDetail(full) }})
}

// splitError returns the toast line of an error message and the full text:
// the first line, cut at the first sentence end once it gets long.
func splitError(msg string) (short, full string) {
	full = strings.TrimSpace(msg)
	short = full
	if i := strings.IndexAny(short, "\n\r"); i >= 0 {
		short = strings.TrimSpace(short[:i])
	}
	const limit = 64
	if r := []rune(short); len(r) > limit {
		cut := -1
		for i, c := range r[:limit] {
			if i >= 16 && (c == '。' || c == '：' || c == '，' || c == '；' || c == '.' || c == ':') {
				cut = i
			}
		}
		if cut > 0 {
			short = string(r[:cut]) + "…"
		} else {
			short = string(r[:limit]) + "…"
		}
	}
	return short, full
}

// showDetail opens the detail dialog; safe from any goroutine.
func (s *Shell) showDetail(text string) {
	s.post(func() {
		s.detail = &detailReq{title: i18n.T("错误详情"), text: text}
		s.detailOpen = true
	})
}

// dialogs draws the confirmation, the invalid-config alert and the
// recovery dialog.
func (s *Shell) dialogs(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	s.confirmDialog(c)
	if s.detail != nil {
		d := s.detail
		env.Dialog(c, &s.detailOpen, env.DialogOptions{
			Title: d.title, Width: 480, Expanded: &s.detailBig,
			Icon: func() { ui.Box(c).TextColor(t.Danger).Children(func() { k.Icon(c, "warn", 18) }) },
		}, func() {
			env.LongText(c, d.text, false).Grow(1)
		}, func() {
			if ui.Button(c, i18n.T("复制")).Clicked() {
				c.WriteClipboard(d.text)
				c.Toast(i18n.T("已复制"))
			}
			if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
				s.detailOpen = false
			}
		})
		if !s.detailOpen {
			s.detail = nil
		}
	}
	if s.invalid != nil {
		n := s.invalid
		env.Dialog(c, &s.invalidOpen, env.DialogOptions{
			Title: firstNonEmpty(n.Title, i18n.T("配置无法使用")), Width: 460, Expanded: &s.invalidBig,
			Icon: func() { ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) }) },
		}, func() {
			ui.Text(c, s.invalidBody(n)).FontSize(13).Shrink(0)
			if n.Detail != "" {
				// A validation error can run to many lines (one per bad
				// proxy or rule): it scrolls, the buttons stay in view.
				env.LongText(c, n.Detail, true).Grow(1)
			}
		}, func() {
			if n.Detail != "" && ui.Button(c, i18n.T("复制错误")).Clicked() {
				c.WriteClipboard(n.Detail)
				c.Toast(i18n.T("已复制"))
			}
			if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
				s.invalidOpen = false
			}
		})
		if !s.invalidOpen {
			s.invalid = nil
		}
	}
	s.consentDialog(c)
	s.feedbackDialogs(c)
	// The prompt follows the status: it is up while the question is open
	// (whether or not a notice announced it, and again after an answer
	// that failed: this dialog is the only place that answers it) and
	// goes when it was answered elsewhere (ctl, a new service process
	// that took over the core). It hides while an answer is applied.
	if !s.recoveryBusy {
		s.recovery = st.PendingRecovery
	}
	if s.recovery {
		resolve := func(off bool, ok string) {
			s.recovery, s.recoveryBusy = false, true
			s.runThen(func() error { return s.api.ResolveRecovery(off) }, func(err error) {
				s.recoveryBusy = false
				if err != nil {
					s.errorToast(err.Error(), "")
				} else if ok != "" {
					s.toast(toastReq{msg: ok})
				}
			})
		}
		dlg := ui.Modal(c, &s.recovery, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, i18n.T("系统代理未恢复")).FontSize(15).FontWeight(600)
				})
				port := st.MixedPort
				if port == 0 {
					port = 7890
				}
				ui.Text(c, i18n.Tf("上次澜流未正常退出，系统代理仍指向 127.0.0.1:%d。如果不处理，部分应用可能无法联网。", port)).FontSize(13)
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("保持开启并启动内核")).Clicked() {
						resolve(false, "")
					}
					if ui.PrimaryButton(c, i18n.T("恢复为关闭")).Clicked() {
						resolve(true, i18n.T("系统代理已关闭"))
					}
				})
			})
		})
		if dlg.Dismissed() {
			// Escape or a click outside does not answer it: the system
			// proxy would stay pointed at a port nothing listens on, with
			// no other place in the window to fix it. It stays up until
			// one of the two is chosen (or it is answered elsewhere).
			s.recovery = true
		}
	}
}

// invalidBody words the invalid-config message with the subscription names.
func (s *Shell) invalidBody(n *app.Notice) string {
	var bad, cur string
	for _, sub := range s.api.Subscriptions() {
		if sub.ID == n.Subject {
			bad = sub.Name
		}
		if sub.Current {
			cur = sub.Name
		}
	}
	switch {
	case bad != "" && bad == cur:
		// An update of the current subscription failed validation.
		return i18n.Tf("「%s」更新后的配置未通过校验，已继续使用上次的配置。", bad)
	case bad != "" && cur != "":
		return i18n.Tf("「%s」的配置未通过校验，已继续使用「%s」。", bad, cur)
	case bad != "":
		return i18n.Tf("「%s」的配置未通过校验，当前配置保持不变。", bad)
	default:
		return i18n.T("配置未通过校验，当前配置保持不变。")
	}
}
