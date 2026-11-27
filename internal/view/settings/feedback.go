package settings

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// 设置 › 关于 › 反馈问题 and 反馈上报 (docs/feedback-api.md): the entry to
// the 反馈问题 dialog, and where reports go — the endpoint, its token in
// the keychain and the optional automatic report. Empty by default: reports
// can then only be saved to a file.

// feedbackDocURL explains the report contract for a self-hosted endpoint.
const feedbackDocURL = "https://github.com/CCS-HQ/lanflow/blob/main/docs/feedback-api.md"

// repoURL is the project's GitHub page, where 关于 › 在 GitHub 上点 Star
// sends the user.
const repoURL = "https://github.com/CCS-HQ/lanflow"

type feedbackUI struct {
	open         bool
	endpoint     field
	endpointErr  string
	token        string
	tokenSession int
}

func validEndpoint(v string) bool {
	return strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://localhost") || strings.HasPrefix(v, "http://127.0.0.1")
}

// starRow is the row of 关于 under 应用更新 that opens the project's
// GitHub page, to star it or follow its releases.
func (p *Page) starRow(c *ui.Context) {
	p.e.Kit.Row(c, i18n.T("在 GitHub 上点 Star"), i18n.T("觉得好用欢迎点个 Star，也可在那里关注新版本"), func() {
		ui.Button(c, i18n.T("打开 GitHub")).Tooltip(repoURL).OnClick(func() { _ = mygo.Shell.OpenExternal(repoURL) })
	})
}

// feedbackRows are the rows of 关于.
func (p *Page) feedbackRows(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	info := e.API.FeedbackInfo()
	e.Kit.Row(c, i18n.T("反馈问题"), i18n.T("描述遇到的问题，附上脱敏后的诊断信息；发送前可以查看全部内容"), func() {
		ui.Button(c, i18n.T("反馈问题…")).Disabled(e.OpenFeedback == nil).OnClick(func() {
			if e.OpenFeedback != nil {
				e.OpenFeedback()
			}
		})
	})
	desc := i18n.T("未设置上报地址：反馈只能保存到文件")
	if info.Endpoint != "" {
		desc = i18n.Tf("发送到 %s", info.Endpoint)
		if info.AutoReport {
			desc += i18n.T(" · 出错时自动上报错误摘要")
		}
	}
	e.Kit.Row(c, i18n.T("反馈上报"), desc, func() {
		if ui.Button(c, i18n.T("配置…")).Clicked() {
			p.fbk.open, p.fbk.endpointErr = true, ""
		}
	})
	p.feedbackConfigDialog(c, t, s, info)
}

// phoneFeedback is 反馈问题 and 反馈上报 on a phone: 反馈问题 as a row,
// the endpoint and the token each with an editor behind, the switch.
func (p *Page) phoneFeedback(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	info := e.API.FeedbackInfo()
	phoneBlock(c, "", "", func() {
		if e.OpenFeedback != nil && p.navRow(c, i18n.T("反馈问题"), i18n.T("描述遇到的问题，附上脱敏后的诊断信息；发送前可以查看全部内容"), "") {
			e.OpenFeedback()
		}
	})
	foot := i18n.T("未设置上报地址：反馈只能保存到文件")
	if info.Endpoint != "" {
		foot = i18n.Tf("发送到 %s", info.Endpoint)
	}
	phoneBlock(c, i18n.T("反馈上报"), foot, func() {
		host := strings.TrimPrefix(strings.TrimPrefix(s.Feedback.Endpoint, "https://"), "http://")
		if p.navRow(c, i18n.T("上报地址"), "", valueOr(host, i18n.T("未设置"))) {
			p.pushEdit(i18n.T("上报地址"), i18n.T("反馈会以 POST {上报地址}/v1/reports 发送到你自己部署的服务；留空时反馈只能保存到文件。"), s.Feedback.Endpoint, func(v string) error {
				v = strings.TrimRight(strings.TrimSpace(v), "/")
				s := p.current()
				switch {
				case v != "" && !validEndpoint(v):
					return errors.New(i18n.T("上报地址须是以 https:// 开头的完整地址"))
				case v != s.Feedback.Endpoint:
					s.Feedback.Endpoint = v
					if v == "" {
						s.Feedback.AutoReport = false
					}
					p.save(s, "", nil)
				}
				return nil
			}, mono, placeholderOpt("https://lanflow-feedback.example.workers.dev"))
		}
		tok := i18n.T("未设置")
		if info.TokenSet {
			tok = i18n.T("已设置")
		}
		if p.navRow(c, i18n.T("访问令牌"), "", tok) {
			p.fbk.token = ""
			p.pushPage(i18n.T("访问令牌"), p.phoneFeedbackToken)
		}
		if s.Feedback.Endpoint != "" {
			p.switchRow(c, i18n.T("出错时自动上报"), i18n.T("只发送错误摘要（时间、类别、脱敏后的消息与版本），不含诊断信息和日志；默认关闭"), s.Feedback.AutoReport, func(on bool) {
				s.Feedback.AutoReport = on
				p.save(s, "", nil)
			})
		}
	})
}

// phoneFeedbackToken is the token's page on a phone: a new one to type
// and 保存, 清除 when one is set.
func (p *Page) phoneFeedbackToken(c *ui.Context) {
	e := p.e
	f := &p.fbk
	info := e.API.FeedbackInfo()
	ph := i18n.T("输入令牌")
	if info.TokenSet {
		ph = "••••••••"
	}
	save := func() {
		tok := f.token
		if strings.TrimSpace(tok) == "" {
			return
		}
		f.token = ""
		f.tokenSession++
		p.sub = nil
		e.Run(func() error { return e.API.SetFeedbackToken(tok) }, i18n.T("令牌已保存"))
	}
	phoneGroup(c, func() {
		if phoneField(c, "fb-token-"+strconv.Itoa(f.tokenSession), i18n.T("访问令牌"), ph, &f.token, true, false).Submitted() {
			save()
		}
	})
	phoneFoot(c, i18n.T("可选；以 Authorization: Bearer 发送，保存在本机的安全存储中"))
	phoneSpacer(c)
	phoneGroup(c, func() {
		if env.ActionRow(c, i18n.T("保存"), false, strings.TrimSpace(f.token) == "").Clicked() {
			save()
		}
	})
	if info.TokenSet {
		phoneSpacer(c)
		phoneGroup(c, func() {
			if env.ActionRow(c, i18n.T("清除令牌"), true, false).Clicked() {
				p.sub = nil
				e.Run(func() error { return e.API.SetFeedbackToken("") }, i18n.T("令牌已清除"))
			}
		})
	}
}

func (p *Page) feedbackConfigDialog(c *ui.Context, t *ui.Theme, s model.Settings, info model.FeedbackInfo) {
	e := p.e
	f := &p.fbk
	if !f.open {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c.Key("feedback-config")).Width(env.DialogWidth(c, 520)).Gap(12).Children(func() {
			ui.Text(c, i18n.T("反馈上报")).FontSize(15).Bold()
			ui.RichText(c).FontSize(12).TextColor(t.TextMuted).Children(func() {
				ui.Text(c, i18n.T("反馈会以 POST {上报地址}/v1/reports 发送到你自己部署的服务（例如 Cloudflare Worker）；接口约定见 "))
				ui.Link(c, "docs/feedback-api.md", feedbackDocURL)
				ui.Text(c, i18n.T("。留空时反馈只能保存到文件，再自行发送。"))
			})
			ui.Column(c).Radius(8).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
				e.Kit.Row(c, i18n.T("上报地址"), i18n.T("例如 https://lanflow-feedback.example.workers.dev"), func() {
					ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
						if f.endpointErr == "" {
							f.endpoint.show(s.Feedback.Endpoint)
						}
						in := ui.TextInput(c.Key("fb-endpoint"), &f.endpoint.text).Width(280).Label(i18n.T("上报地址")).Invalid(f.endpointErr != "")
						if in.Changed() {
							f.endpointErr = ""
						}
						if f.endpoint.commit(in) {
							v := strings.TrimRight(strings.TrimSpace(f.endpoint.text), "/")
							switch {
							case v != "" && !validEndpoint(v):
								f.endpointErr = i18n.T("上报地址须是以 https:// 开头的完整地址")
							case v != s.Feedback.Endpoint:
								s.Feedback.Endpoint = v
								if v == "" {
									s.Feedback.AutoReport = false
								}
								p.save(s, "", nil)
							}
						}
						if f.endpointErr != "" {
							ui.Text(c, f.endpointErr).FontSize(11).TextColor(t.Danger)
						}
					})
				})
				tokDesc := i18n.T("可选；以 Authorization: Bearer 发送，保存在系统钥匙串中")
				if info.TokenSet {
					tokDesc = i18n.T("已设置；保存在系统钥匙串中")
				}
				e.Kit.Row(c, i18n.T("访问令牌"), tokDesc, func() {
					ui.Row(c.Key("fb-token-" + strconv.Itoa(f.tokenSession))).Gap(8).AlignItems(ui.Center).Children(func() {
						ph := i18n.T("输入令牌")
						if info.TokenSet {
							ph = "••••••••"
						}
						in := ui.TextInput(c.Key("fb-token"), &f.token).Width(160).Password().Placeholder(ph).Label(i18n.T("访问令牌"))
						save := func() {
							tok := f.token
							if strings.TrimSpace(tok) == "" {
								return
							}
							f.token = ""
							f.tokenSession++
							e.Run(func() error { return e.API.SetFeedbackToken(tok) }, i18n.T("令牌已保存"))
						}
						if in.Submitted() {
							save()
						}
						ui.Button(c, i18n.T("保存")).Disabled(strings.TrimSpace(f.token) == "").OnClick(save)
						if info.TokenSet {
							ui.Button(c, i18n.T("清除")).OnClick(func() {
								e.Run(func() error { return e.API.SetFeedbackToken("") }, i18n.T("令牌已清除"))
							})
						}
					})
				})
				e.Kit.Row(c, i18n.T("出错时自动上报"), i18n.T("只发送错误摘要（时间、类别、脱敏后的消息与版本），不含诊断信息和日志；默认关闭"), func() {
					v := s.Feedback.AutoReport
					if ui.Switch(c.Key("fb-auto"), &v).Label(i18n.T("出错时自动上报")).Disabled(p.saving || s.Feedback.Endpoint == "").Changed() {
						s.Feedback.AutoReport = v
						p.save(s, "", nil)
					}
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("完成")).Clicked() {
					f.open = false
				}
			})
		})
	})
}
