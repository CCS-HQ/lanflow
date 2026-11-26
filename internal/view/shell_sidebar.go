// The sidebar: always-visible control center combining Lanflow's native
// styling with Clash Party's high-frequency card ergonomics — brand header
// with core status dot + settings gear, full-width mode pill, clickable
// System Proxy & TUN quick cards, current subscription card, bento nav cards
// (including Overrides), full-width Connections card with live traffic wave,
// full-width Core card (version, memory & one-click restart), and a compact
// secondary tools strip, and the 聚合订阅 card at the bottom.

package view

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/capture"
	"lanflow/internal/view/env"
	"lanflow/internal/view/settings"
)

var sidebarModes = []model.Mode{model.ModeRule, model.ModeGlobal, model.ModeDirect}

var sidebarSecondaryPages = []env.Page{env.Net, env.Stats, env.Probe}

// settingsTab returns the active settings tab key when on env.Settings, "" otherwise.
func (s *Shell) settingsTab() string {
	if s.page != env.Settings {
		return ""
	}
	if sp, ok := s.pages[env.Settings].(*settings.Page); ok {
		return sp.ActiveTab()
	}
	return "general"
}

// sidebarHeader draws the macOS traffic-light drag strip and the brand row
// (Lanflow title + live core dot on the left, settings gear on the right).
func (s *Shell) sidebarHeader(c *ui.Context, st model.Status, barH float32) {
	t := c.Theme()
	k := s.env.Kit
	stab := s.settingsTab()
	// Highlight the gear when on general / dns / sniffer / advanced (tabs
	// that don't have their own dedicated sidebar card).
	gearActive := s.page == env.Settings && stab != "proxy" && stab != "tun" && stab != "override" && stab != "kernel"

	ui.Column(c).Shrink(0).Children(func() {
		if barH > 0 {
			ui.Box(c).Height(max(barH-24, 26)).FillWidth().DragWindow().Label(i18n.T("标题栏"))
		}
		ui.Row(c).Height(40).Padding(0, 10, 4, 14).Gap(6).AlignItems(ui.Center).Children(func() {
			brand := ui.Row(c).Grow(1).FillHeight().AlignItems(ui.Center).Gap(7)
			if barH > 0 {
				brand.DragWindow()
			}
			brand.Children(func() {
				ui.Text(c, "Lanflow").FontSize(18).FontWeight(700).SingleLine()
				switch st.Core {
				case model.CoreRunning:
					k.Dot(c, "green")
				case model.CoreStarting:
					ui.Spinner(c).Size(10, 10).Label(i18n.T("启动中"))
				case model.CoreError, model.CoreMissing:
					k.Dot(c, "red")
				default:
					k.Dot(c, "gray")
				}
			})

			setBtn := ui.ButtonBase(c.Key("sb:settings")).Size(32, 32).Radius(8).
				AlignItems(ui.Center).Justify(ui.Center).
				Label(i18n.T("设置")).Tooltip(i18n.T("设置 (⌘,)")).Cursor(ui.CursorPointer)
			if gearActive {
				setBtn.Background(t.Accent).TextColor(t.AccentText)
			} else {
				setBtn.TextColor(t.Text)
				if setBtn.Hovered() {
					setBtn.Background(t.SurfaceHover)
				}
			}
			setBtn.Children(func() { k.Icon(c, "settings", 19) })
			if setBtn.Clicked() {
				s.navigate(env.Settings, "general")
			}
		})
	})
}

// sidebar draws the scrollable control center.
func (s *Shell) sidebar(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	busy := st.Core == model.CoreStarting || st.Core == model.CoreMissing
	stab := s.settingsTab()

	if s.sbSubsDirty.Swap(false) {
		var cur *model.Subscription
		s.sbAggCount = 0
		for _, sub := range s.api.Subscriptions() {
			if sub.Source == model.SourceAggregate {
				s.sbAggCount++
			}
			if sub.Current {
				subCopy := sub
				cur = &subCopy
				break
			}
		}
		s.sbCurSub = cur
	}
	if s.sbSetDirty.Swap(false) {
		s.sbCapOn = s.api.Settings().CaptureEnabled
	}
	if s.sbCapDirty.Swap(false) {
		s.sbCap = s.api.CaptureStatus()
	}

	curSub := s.sbCurSub
	groupCount := st.GroupCount
	ruleCount := st.RuleCount
	overrideCount := st.OverrideCount
	cs := s.sbCap

	ui.Scroll(c).Grow(1).Shrink(1).Children(func() {
		ui.Column(c).Padding(4, 10, 12, 10).Gap(10).Children(func() {
			// 1. Full-width 3-column pill mode switcher
			s.modeSwitcher(c, st.Mode)

			// 2. Clickable quick toggle cards: 系统代理 + TUN (click card → 代理设置)
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				proxyOn := st.Core == model.CoreRunning && st.SystemProxy
				s.quickToggleCard(c, "sb:sysproxy", "proxies", i18n.T("系统代理"), i18n.T("快捷系统代理开关"), proxyOn, busy, stab == "proxy", func(on bool) {
					msg := i18n.T("系统代理已关闭")
					if on {
						msg = i18n.T("系统代理已开启")
					}
					s.env.Run(func() error { return s.api.SetSystemProxy(on) }, msg)
				}, func() {
					s.navigate(env.Settings, "proxy")
				})
				s.quickToggleCard(c, "sb:tun", "tun", i18n.T("TUN"), i18n.T("快捷 TUN 开关"), st.TUN, busy, stab == "tun", func(on bool) {
					env.SetTUN(s.env, on)
				}, func() {
					s.navigate(env.Settings, "tun")
				})
			})

			// 3. Current subscription card
			if curSub != nil {
				s.subCard(c, *curSub)
			}

			// 4. Primary 2-column bento cards: 代理 + 规则
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				gb := ""
				if groupCount > 0 {
					gb = fmt.Sprint(groupCount)
				}
				rb := ""
				if ruleCount > 0 {
					rb = fmt.Sprint(ruleCount)
				}
				s.bentoNavCard(c, env.Proxies, gb, t, k)
				s.bentoNavCard(c, env.Rules, rb, t, k)
			})

			// 5. 2-column bento cards: 覆写 (Settings › override) + 概览 (3D Exit Globe)
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				ob := ""
				if overrideCount > 0 {
					ob = fmt.Sprint(overrideCount)
				}
				s.bentoActionCard(c, "nav:override", "doc", i18n.T("覆写"), ob, stab == "override", t, k, func() {
					s.navigate(env.Settings, "override")
				})
				s.bentoNavCard(c, env.Overview, "", t, k)
			})

			// 6. Full-width live-speed Connections card with wave chart
			s.connsSpeedCard(c, st, t, k)

			// 7. Full-width Core card (version, live memory, one-click restart → Settings › kernel)
			s.coreCard(c, st, stab == "kernel", busy, t, k)

			// 8. 2-column bento cards: 日志 + 抓包
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				s.bentoNavCard(c, env.Logs, "", t, k)
				if cs.Enabled || s.sbCapOn {
					cb := ""
					if cs.Enabled {
						cb = i18n.T("解密中")
					}
					s.bentoNavCard(c, env.Capture, cb, t, k)
				}
			})

			// 9. Capture active warning banner (if decrypting)
			if cs.Enabled {
				ui.Row(c).Gap(6).Padding(6, 8).Radius(8).Background(t.Warning.Alpha(0.12)).AlignItems(ui.Center).Children(func() {
					k.Dot(c, "orange")
					txt := capture.StatusText(cs)
					ui.Text(c, txt).FontSize(11.5).TextColor(t.Warning).MaxLines(2).Shrink(1).Tooltip(txt)
				})
			}

			// 10. Compact 3-pill strip for lower-frequency diagnostic tools: 网络 / 统计 / 检测
			ui.Row(c).Gap(6).AlignItems(ui.Stretch).Children(func() {
				for _, p := range sidebarSecondaryPages {
					s.compactToolPill(c, p, t, k)
				}
			})

			// 11. 聚合订阅: the subscriptions built and maintained locally
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				ab := ""
				if s.sbAggCount > 0 {
					ab = fmt.Sprint(s.sbAggCount)
				}
				s.bentoNavCard(c, env.Aggregate, ab, t, k)
			})
		})
	})
}

// modeSwitcher is a full-width 3-equal-column segmented pill.
func (s *Shell) modeSwitcher(c *ui.Context, cur model.Mode) {
	t := c.Theme()
	labels := [3]string{i18n.T("规则"), i18n.T("全局"), i18n.T("直连")}
	ui.Row(c.Key("sb:mode")).Padding(3).Gap(3).Radius(12).
		Background(t.Background).Border(1, t.Border.Alpha(0.55)).
		AlignItems(ui.Center).Children(func() {
		for i, m := range sidebarModes {
			active := cur == m
			btn := ui.ButtonBase(c.Key("sb:mode:" + string(m))).Grow(1).Basis(0).Height(32).Radius(9).
				AlignItems(ui.Center).Justify(ui.Center).
				Label(labels[i]).Cursor(ui.CursorPointer)
			if active {
				btn.Background(t.Accent)
			} else if btn.Hovered() {
				btn.Background(t.SurfaceHover)
			}
			btn.Children(func() {
				txt := ui.Text(c, labels[i]).FontSize(13.5).FontWeight(500).SingleLine()
				if active {
					txt.TextColor(t.AccentText).FontWeight(600)
				} else {
					txt.TextColor(t.TextMuted)
				}
			})
			if btn.Clicked() && !active {
				mode := m
				s.env.Run(func() error { return s.api.SetMode(mode) }, i18n.Tf("已切换到%s模式", env.ModeName(mode)))
			}
		}
	})
}

func (s *Shell) quickToggleCard(c *ui.Context, key, icon, title, switchLabel string, on, busy, active bool, onToggle func(bool), onOpen func()) {
	t := c.Theme()
	k := s.env.Kit
	card := ui.ButtonBase(c.Key(key)).Column().Grow(1).Basis(0).
		Padding(10, 12).Gap(10).Radius(14).
		AlignItems(ui.Start).Label(title).Cursor(ui.CursorPointer)
	if active {
		card.Background(t.Accent).Border(1, t.Accent)
	} else if on {
		card.Background(t.Accent.Alpha(0.08)).Border(1.5, t.Accent.Alpha(0.80))
		if card.Hovered() {
			card.Background(t.Accent.Alpha(0.13))
		}
	} else {
		card.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
	}
	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).FillWidth().Children(func() {
			ic := ui.Box(c).TextColor(t.Text)
			if active {
				ic.TextColor(t.AccentText)
			} else if on {
				ic.TextColor(t.Accent)
			}
			ic.Children(func() { k.Icon(c, icon, 22) })
			ui.Spacer(c)
			swWrap := ui.Box(c).Radius(999)
			if active {
				if on {
					swWrap.Padding(1).Border(1.5, t.AccentText.Alpha(0.92))
				} else {
					swWrap.Padding(1).Border(1.5, t.AccentText.Alpha(0.45))
				}
			}
			swWrap.Children(func() {
				v := on
				ui.Switch(c, &v).Label(switchLabel).Disabled(busy).OnChange(func() { onToggle(v) })
			})
		})
		lbl := ui.Text(c, title).FontSize(14.5).FontWeight(600).SingleLine()
		if active {
			lbl.TextColor(t.AccentText)
		}
	})
	if card.Clicked() && onOpen != nil {
		onOpen()
	}
}

func (s *Shell) subCard(c *ui.Context, sub model.Subscription) {
	t := c.Theme()
	k := s.env.Kit
	active := s.page == env.Subs
	card := env.ClickableColumn(c.Key("sb:sub:"+sub.ID), i18n.Tf("当前订阅 %s，点击管理订阅", sub.Name)).
		Padding(11, 12).Gap(8).Radius(14)
	if active {
		card.Background(t.Accent).Border(1, t.Accent)
	} else {
		card.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
	}
	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).FillWidth().Children(func() {
			name := ui.Text(c, sub.Name).FontSize(15).FontWeight(700).SingleLine().Grow(1).Shrink(1).Tooltip(sub.Name)
			if active {
				name.TextColor(t.AccentText)
			}
			if sub.Source == "url" || sub.Source == model.SourceAggregate {
				ref := ui.ButtonBase(c.Key("sb:sub:refresh")).Size(26, 26).Radius(6).
					AlignItems(ui.Center).Justify(ui.Center).
					Label(i18n.T("更新当前订阅")).Tooltip(i18n.T("更新当前订阅")).
					Cursor(ui.CursorPointer)
				if active {
					ref.TextColor(t.AccentText)
					if ref.Hovered() {
						ref.Background(t.AccentText.Alpha(0.20))
					}
				} else {
					ref.TextColor(t.Text)
					if ref.Hovered() {
						ref.Background(t.Accent.Alpha(0.15))
					}
				}
				ref.Children(func() {
					if sub.Updating {
						ui.Spinner(c).Size(13, 13)
					} else {
						k.Icon(c, "refresh", 16)
					}
				})
				if ref.Clicked() && !sub.Updating {
					id := sub.ID
					s.env.Run(func() error { return s.api.UpdateSubscription(id) }, i18n.T("订阅已更新"))
				}
			}
		})
		if sub.Total > 0 {
			used := sub.Upload + sub.Download
			frac := min(1.0, float64(used)/float64(sub.Total))
			ui.Row(c).AlignItems(ui.Center).Gap(4).FillWidth().Children(func() {
				u := ui.Text(c, fmt.Sprintf("%s / %s GB", env.FmtGB(used), env.FmtGB(sub.Total))).
					FontSize(12).FontFeatures("tnum").SingleLine().Grow(1).Shrink(1)
				if active {
					u.TextColor(t.AccentText.Alpha(0.85))
				} else {
					u.TextColor(t.TextMuted)
				}
				if !sub.Expire.IsZero() {
					ex := ui.Text(c, sub.Expire.Format("01-02")).FontSize(11).FontFeatures("tnum").SingleLine().Shrink(0)
					if active {
						ex.TextColor(t.AccentText.Alpha(0.85))
					} else {
						ex.TextColor(t.TextMuted)
					}
				}
			})
			ui.Progress(c, frac).FillWidth().Height(5).Label(i18n.T("订阅流量"))
		} else if !sub.Expire.IsZero() {
			ex := ui.Text(c, i18n.Tf("到期 %s", sub.Expire.Format("2006-01-02"))).FontSize(12).SingleLine()
			if active {
				ex.TextColor(t.AccentText.Alpha(0.85))
			} else {
				ex.TextColor(t.TextMuted)
			}
		}
	})
	if card.Clicked() {
		s.navigate(env.Subs, "")
	}
}

func (s *Shell) bentoNavCard(c *ui.Context, p env.Page, badge string, t *ui.Theme, k env.Kit) {
	s.bentoActionCard(c, "nav:"+string(p), pageIcons[string(p)], env.Title(p), badge, s.page == p, t, k, func() {
		s.navigate(p, "")
	})
}

func (s *Shell) bentoActionCard(c *ui.Context, key, icon, title, badge string, active bool, t *ui.Theme, k env.Kit, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).Column().Grow(1).Basis(0).
		Padding(10, 12).Gap(10).Radius(14).AlignItems(ui.Start).
		Label(title).Cursor(ui.CursorPointer)
	if active {
		btn.Background(t.Accent).Border(1, t.Accent)
	} else {
		btn.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
	}
	btn.Children(func() {
		ui.Row(c).AlignItems(ui.Center).FillWidth().Children(func() {
			ic := ui.Box(c).TextColor(t.Text)
			if active {
				ic.TextColor(t.AccentText)
			}
			ic.Children(func() { k.Icon(c, icon, 22) })
			ui.Spacer(c)
			if badge != "" {
				b := ui.Box(c).Padding(1, 8).Radius(10)
				if active {
					b.Background(t.AccentText.Alpha(0.20)).Border(1, t.AccentText.Alpha(0.45))
					b.Children(func() {
						ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(t.AccentText).FontFeatures("tnum").SingleLine()
					})
				} else {
					b.Background(t.Background).Border(1, t.Accent.Alpha(0.60))
					b.Children(func() {
						ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(t.Accent).FontFeatures("tnum").SingleLine()
					})
				}
			}
		})
		lbl := ui.Text(c, title).FontSize(14.5).FontWeight(600).SingleLine()
		if active {
			lbl.TextColor(t.AccentText)
		}
	})
	if btn.Clicked() && onClick != nil {
		onClick()
	}
}

// connsSpeedCard is a full-width card showing active connections, live
// upload/download speed, and a subtle Clash Party–style real-time traffic
// wave drawn behind the card content.
func (s *Shell) connsSpeedCard(c *ui.Context, st model.Status, t *ui.Theme, k env.Kit) {
	active := s.page == env.Conns
	title := env.Title(env.Conns)
	_, rawDown := s.api.TrafficHistory()
	btn := ui.ButtonBase(c.Key("nav:conns")).FillWidth().
		Radius(14).Clip().Label(title).Cursor(ui.CursorPointer)
	if active {
		btn.Background(t.Accent).Border(1, t.Accent)
	} else {
		btn.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
	}
	btn.Children(func() {
		// Background live traffic wave: smoothed last 20 samples in lower half
		// so it looks fluid like Clash Party without jagged spikes over text.
		if st.Core == model.CoreRunning && len(rawDown) >= 2 {
			src := rawDown
			if len(src) > 20 {
				src = src[len(src)-20:]
			}
			smooth := make([]float64, len(src))
			for i := range src {
				sum := float64(src[i])
				cnt := 1.0
				if i > 0 {
					sum += float64(src[i-1])
					cnt++
				}
				if i+1 < len(src) {
					sum += float64(src[i+1])
					cnt++
				}
				smooth[i] = sum / cnt
			}
			waveFill := t.Accent.Alpha(0.09)
			waveStroke := t.Accent.Alpha(0.28)
			if active {
				waveFill = t.AccentText.Alpha(0.14)
				waveStroke = t.AccentText.Alpha(0.35)
			}
			ui.Box(c).Absolute().Fill().Draw(func(p *ui.Painter, r ui.Rect) {
				if r.W <= 0 || r.H <= 0 || len(smooth) < 2 {
					return
				}
				peak := 1024.0
				for _, v := range smooth {
					if v > peak {
						peak = v
					}
				}
				var line, area ui.Path
				n := len(smooth)
				for i, v := range smooth {
					x := r.X + r.W*float32(i)/float32(n-1)
					y := r.Y + r.H - r.H*0.48*float32(v/peak)
					if i == 0 {
						line.MoveTo(x, y)
						area.MoveTo(x, r.Y+r.H)
						area.LineTo(x, y)
					} else {
						line.LineTo(x, y)
						area.LineTo(x, y)
					}
				}
				area.LineTo(r.X+r.W, r.Y+r.H)
				area.Close()
				p.FillPath(&area, waveFill)
				p.StrokePath(&line, 1.4, waveStroke)
			})
		}

		// Foreground card content: Clash Party 2-row rhythm (icon + speeds on top, title + count on bottom)
		ui.Column(c).FillWidth().Padding(10, 12).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Start).Children(func() {
				ic := ui.Box(c).TextColor(t.Text).Padding(2, 0, 0, 0)
				if active {
					ic.TextColor(t.AccentText)
				}
				ic.Children(func() { k.Icon(c, "conns", 22) })
				ui.Spacer(c)
				ui.Column(c).Gap(3).AlignItems(ui.End).Children(func() {
					if st.Core == model.CoreRunning {
						up := ui.Text(c, env.FmtSpeed(st.Up)+" ↑").FontSize(13).FontWeight(500).FontFeatures("tnum").SingleLine()
						dn := ui.Text(c, env.FmtSpeed(st.Down)+" ↓").FontSize(13).FontWeight(500).FontFeatures("tnum").SingleLine()
						if active {
							up.TextColor(t.AccentText)
							dn.TextColor(t.AccentText)
						} else {
							up.TextColor(t.TextMuted)
							dn.TextColor(t.Text)
						}
					} else {
						statusTxt := i18n.T("未启动")
						if st.Core == model.CoreStarting {
							statusTxt = i18n.T("启动中…")
						} else if st.Core == model.CoreError {
							statusTxt = i18n.T("内核异常")
						} else if st.Core == model.CoreMissing {
							statusTxt = i18n.T("未找到内核")
						}
						txt := ui.Text(c, statusTxt).FontSize(13).SingleLine()
						if active {
							txt.TextColor(t.AccentText)
						} else {
							txt.TextColor(t.TextMuted)
						}
					}
				})
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				lbl := ui.Text(c, title).FontSize(15).FontWeight(600).SingleLine()
				if active {
					lbl.TextColor(t.AccentText)
				}
				ui.Spacer(c)
				if st.Connections > 0 && st.Core == model.CoreRunning {
					b := ui.Box(c).Padding(1, 8).Radius(10)
					if active {
						b.Background(t.AccentText.Alpha(0.20)).Border(1, t.AccentText.Alpha(0.45))
						b.Children(func() {
							ui.Text(c, fmt.Sprint(st.Connections)).FontSize(11.5).FontWeight(600).TextColor(t.AccentText).FontFeatures("tnum").SingleLine()
						})
					} else {
						b.Background(t.Background).Border(1, t.Accent.Alpha(0.60))
						b.Children(func() {
							ui.Text(c, fmt.Sprint(st.Connections)).FontSize(11.5).FontWeight(600).TextColor(t.Accent).FontFeatures("tnum").SingleLine()
						})
					}
				}
			})
		})
	})
	if btn.Clicked() {
		s.navigate(env.Conns, "")
	}
}

// coreCard matches Clash Party's 2-row mihomo-core card:
// top row: version (left) + restart button (right);
// bottom row: 「内核设置」 (left) + live memory (right).
func (s *Shell) coreCard(c *ui.Context, st model.Status, active, busy bool, t *ui.Theme, k env.Kit) {
	card := ui.ButtonBase(c.Key("nav:kernel")).Column().FillWidth().
		Padding(10, 12).Gap(8).Radius(14).AlignItems(ui.Start).
		Label(i18n.T("内核")).Cursor(ui.CursorPointer)
	if active {
		card.Background(t.Accent).Border(1, t.Accent)
	} else {
		card.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
	}
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ver := i18n.T("未启动")
			if st.Core == model.CoreRunning {
				if st.CoreVersion != "" {
					ver = st.CoreVersion
				} else {
					ver = i18n.T("运行中")
				}
			} else if st.Core == model.CoreStarting {
				ver = i18n.T("启动中…")
			} else if st.Core == model.CoreError {
				ver = i18n.T("内核异常")
			} else if st.Core == model.CoreMissing {
				ver = i18n.T("未找到内核")
			}
			verTxt := ui.Text(c, ver).FontSize(14.5).FontWeight(600).FontFeatures("tnum").SingleLine().Shrink(1)
			if active {
				verTxt.TextColor(t.AccentText)
			}
			ui.Spacer(c)
			rst := ui.ButtonBase(c.Key("sb:core:restart")).Size(28, 28).Radius(7).
				AlignItems(ui.Center).Justify(ui.Center).
				Label(i18n.T("重启内核")).Tooltip(i18n.T("重启内核")).
				Disabled(busy).Cursor(ui.CursorPointer)
			if active {
				rst.TextColor(t.AccentText)
				if rst.Hovered() {
					rst.Background(t.AccentText.Alpha(0.20))
				}
			} else {
				rst.TextColor(t.Text)
				if rst.Hovered() {
					rst.Background(t.SurfaceHover)
				}
			}
			rst.Children(func() { k.Icon(c, "refresh", 17) })
			if rst.Clicked() {
				s.env.Run(func() error { return s.api.RestartCore() }, i18n.T("内核已重启"))
			}
		})
		ui.Spacer(c)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			lbl := ui.Text(c, i18n.T("内核设置")).FontSize(15).FontWeight(600).SingleLine()
			if active {
				lbl.TextColor(t.AccentText)
			}
			ui.Spacer(c)
			if st.Core == model.CoreRunning && st.Memory > 0 {
				mem := ui.Text(c, env.FmtBytes(st.Memory)).FontSize(14).FontWeight(600).FontFeatures("tnum").SingleLine()
				if active {
					mem.TextColor(t.AccentText)
				} else {
					mem.TextColor(t.TextMuted)
				}
			}
		})
	})
	if card.Clicked() {
		s.navigate(env.Settings, "kernel")
	}
}

// compactToolPill renders a low-frequency diagnostic page in a 3-column bottom strip.
func (s *Shell) compactToolPill(c *ui.Context, p env.Page, t *ui.Theme, k env.Kit) {
	active := s.page == p
	title := env.Title(p)
	btn := ui.ButtonBase(c.Key("nav:"+string(p))).Row().Grow(1).Basis(0).
		Padding(7, 6).Gap(5).Radius(10).AlignItems(ui.Center).Justify(ui.Center).
		Label(title).Cursor(ui.CursorPointer)
	if active {
		btn.Background(t.Accent).Border(1, t.Accent)
	} else {
		btn.Background(t.Background).Border(1, t.Border.Alpha(0.55))
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
	}
	btn.Children(func() {
		ic := ui.Box(c).TextColor(t.TextMuted)
		if active {
			ic.TextColor(t.AccentText)
		}
		ic.Children(func() { k.Icon(c, pageIcons[string(p)], 14) })
		lbl := ui.Text(c, title).FontSize(12).FontWeight(500).SingleLine()
		if active {
			lbl.TextColor(t.AccentText).FontWeight(600)
		} else {
			lbl.TextColor(t.TextMuted)
		}
	})
	if btn.Clicked() {
		s.navigate(p, "")
	}
}

// shortcuts are ⌘1–⌘9 for the nine pages and ⌘, for settings.
func (s *Shell) shortcuts(c *ui.Context) {
	for i, p := range env.Order[:9] {
		if c.Shortcut(ui.Cmd, ui.Key1+ui.Key(i)) {
			s.navigate(p, "")
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		s.navigate(env.Settings, "")
	}
}
